package resolver

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/net/idna"
	"golang.org/x/time/rate"
)

const (
	defaultResolvers = "1.1.1.1:853,1.0.0.1:853"
	defaultSNI       = "cloudflare-dns.com"
	queryTimeout     = 3 * time.Second        // per-resolver, not per-Lookup
	lookupDeadline   = 5 * time.Second        // wall-clock cap on the whole nine-fan-out + fallback (R7)
	edns0UDPSize     = 4096                   // R15
	rateSustain      = 100                    // q/s sustain (R16)
	rateBurst        = 200                    // q burst (R16)
	preflightTimeout = 200 * time.Millisecond // sticky-resolver picker preflight
)

// Client talks DoT to a Cloudflare-or-equivalent recursive resolver
// and fans nine record-type queries per Lookup call. One Client per
// process; safe for concurrent use (miekg/dns.Client is, and the
// rate.Limiter is).
type Client struct {
	resolvers []string      // ["1.1.1.1:853", "1.0.0.1:853"] — try in order
	dns       *dns.Client   // configured with Net=tcp-tls + TLSConfig
	limiter   *rate.Limiter // process-global egress gate (R16)
	dialer    *net.Dialer   // used by pickResolver preflight (test-overridable indirectly via custom Dialer)
}

// New parses resolverList ("host:port[,host:port...]") and returns a
// Client wired for DoT. Empty / unset list falls back to defaultResolvers.
// SNI is fixed to cloudflare-dns.com unless DNS_RESOLVER_SNI overrides;
// document the assumption in docs/security.md (operators using a
// non-Cloudflare resolver MUST set DNS_RESOLVER_SNI).
//
// Returns an error on syntactically invalid input so cmd/imagelet can
// fail-fast at startup. An empty list is NOT an error — defaults apply.
func New(resolverList string) (*Client, error) {
	addrs, err := parseResolverList(resolverList)
	if err != nil {
		return nil, err
	}
	sni := os.Getenv("DNS_RESOLVER_SNI")
	if sni == "" {
		sni = defaultSNI
	}
	return &Client{
		resolvers: addrs,
		dns: &dns.Client{
			Net:     "tcp-tls",
			Timeout: queryTimeout,
			TLSConfig: &tls.Config{
				ServerName: sni,
				MinVersion: tls.VersionTLS12,
			},
		},
		limiter: rate.NewLimiter(rate.Limit(rateSustain), rateBurst),
		dialer:  &net.Dialer{},
	}, nil
}

// NewWithEndpoint mirrors profile.NewWithEndpoint — point at a single
// host:port and inject a pre-built dns.Client (so tests can wire
// Net="tcp" without TLS). Test-only.
//
// foxcpp/go-mockdns is a stub UDP/TCP DNS server; it does NOT terminate
// TLS. Resolver tests construct the dnsClient with Net: "tcp" (NOT
// "tcp-tls") so go-mockdns can serve the queries.
//
// The DoT wrap (Net="tcp-tls" + TLSConfig) is exercised in production
// only; a one-time integration test TestResolver_LiveDoT queries the
// real 1.1.1.1:853 and is skipped under -short.
func NewWithEndpoint(addr string, dnsClient *dns.Client) *Client {
	return &Client{
		resolvers: []string{addr},
		dns:       dnsClient,
		limiter:   rate.NewLimiter(rate.Limit(rateSustain), rateBurst),
		dialer:    &net.Dialer{},
	}
}

// Resolvers returns the configured resolver addresses in order. Used
// by the startup log line so cmd/imagelet doesn't have to re-parse the
// env var.
func (c *Client) Resolvers() []string {
	out := make([]string, len(c.resolvers))
	copy(out, c.resolvers)
	return out
}

// parseResolverList splits "a:1,b:2" on commas, trims spaces, returns
// the in-order slice. Empty input → defaultResolvers split on commas.
// Returns an error when any entry lacks ":port" (DoT requires explicit
// port — silent default would mask a typo).
func parseResolverList(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		s = defaultResolvers
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		host, port, err := net.SplitHostPort(p)
		if err != nil {
			return nil, fmt.Errorf("dns: invalid resolver %q: %w", p, err)
		}
		if host == "" || port == "" {
			return nil, fmt.Errorf("dns: invalid resolver %q: empty host or port", p)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, errors.New("dns: empty resolver list")
	}
	return out, nil
}

// canonicalize is the shared helper documented in R13. Used by Lookup
// (cache-keying + wire format) and re-used by the handler for cache
// alignment.
//
//	host = strings.ToLower(strings.TrimSpace(host))
//	host = strings.TrimSuffix(host, ".")
//	host = idna.Lookup.ToASCII(host)   // only when non-ASCII present
//
// idna.Lookup is strict and rejects RFC 8552 underscored labels
// (_dmarc, _spf, _acme-challenge, ...) — but underscore-bearing
// hostnames are by construction pure ASCII, so we short-circuit IDN
// translation when the input has no non-ASCII runes. This keeps the
// canonicalization rule from R13 verbatim for IDN inputs (Unicode
// → punycode) AND lets the underscored-attribute headline use cases
// from R4 actually work end-to-end.
func canonicalize(host string) (string, error) {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return "", errors.New("dns: empty hostname")
	}
	if isASCII(host) {
		return host, nil
	}
	return idna.Lookup.ToASCII(host)
}

// isASCII reports whether s consists entirely of bytes < 0x80.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// classifyTransportErr maps a per-query miekg/dns ExchangeContext error
// + Rcode to one of the resolver sentinels. Connection-class transport
// errors become ErrUnavailable; explicit NXDOMAIN becomes ErrNotFound;
// other dishonest Rcodes (SERVFAIL/REFUSED/FORMERR) map to
// ErrUnavailable so the sticky picker handles cross-Lookup fallback.
//
// resp may be nil when err != nil (transport error before any response).
func classifyTransportErr(resp *dns.Msg, err error) error {
	if err != nil {
		return ErrUnavailable
	}
	if resp == nil {
		return ErrUnavailable
	}
	switch resp.Rcode {
	case dns.RcodeSuccess:
		return nil
	case dns.RcodeNameError:
		return ErrNotFound
	default:
		return ErrUnavailable
	}
}

// newQuery builds a DoT-friendly query: EDNS0 with 4096 udp size +
// AuthenticatedData bit set so the upstream surfaces its DNSSEC
// validation result (R14, R15).
func newQuery(host string, qtype uint16) *dns.Msg {
	m := new(dns.Msg)
	m.SetQuestion(dns.Fqdn(host), qtype)
	m.SetEdns0(edns0UDPSize, false)
	m.AuthenticatedData = true
	return m
}
