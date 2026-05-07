package resolver

import (
	"context"
	"errors"
	"math"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"
)

// verificationPrefixes is the prefix table used by classifyTXT. Order
// is not significant; first prefix-match wins per RR. Hale extends as
// new vendors ship; tests pin the current set.
var verificationPrefixes = []string{
	"google-site-verification=",
	"apple-domain-verification=",
	"facebook-domain-verification=",
	"stripe-verification=",
	"MS=",
	"_atproto",
}

// minTTLSentinel is the initial value for Records.MinTTL. Replaced by
// min(rr.Ttl) as RRsets populate; the cached layer's [60s, 600s] clamp
// catches both this sentinel and the all-zero-TTL case.
const minTTLSentinel = time.Duration(math.MaxUint32) * time.Second

// Lookup canonicalizes hostname (R13), enforces a 5s wall-clock
// deadline (lookupDeadline), picks ONE live resolver via pickResolver,
// fans nine record-type queries in parallel via errgroup.WithContext,
// and aggregates results into a Records value.
//
// Error propagation:
//
//   - All resolvers fail preflight → ErrUnavailable (from pickResolver)
//   - NS query NXDOMAIN (RcodeNameError) → ErrNotFound. Empty NS
//     RRset on NOERROR is NOT NXDOMAIN — record-leaf names like
//     `_dmarc.gmail.com` have TXT but no NS RR.
//   - NS query connection-class err → ErrUnavailable
//   - Other 8 query types: per-type errors return nil from the
//     goroutine (debug-log + row drops); they do NOT propagate
//   - ErrSelfThrottled from any per-type call → ErrSelfThrottled
//     (deliberate sibling-cancel; egress gate is process-global)
//   - ctx deadline → ctx.Err() (from any goroutine that's still
//     blocked when the lookupDeadline expires)
//
// MinTTL is the min of every per-RR-set TTL across populated record
// types. DNSSECVerified is true iff at least one per-type response
// carried the AD bit (OR aggregation; R14).
func (c *Client) Lookup(ctx context.Context, hostname string) (Records, error) {
	t0 := time.Now()

	host, err := canonicalize(hostname)
	if err != nil {
		return Records{}, err
	}

	// Lookup-level wall-clock deadline (R7). Caps total time across nine
	// parallel queries × UDP+TCP retry × resolver fallback.
	ctx, cancel := context.WithTimeout(ctx, lookupDeadline)
	defer cancel()

	// Sticky resolver pick (one TLS handshake, not nine on failure).
	addr, err := c.pickResolver(ctx)
	if err != nil {
		return Records{}, err // ErrUnavailable when every resolver preflight fails
	}

	rec := Records{Hostname: host, MinTTL: minTTLSentinel}
	var (
		mu        sync.Mutex
		adAny     bool
		minTTL    = uint32(math.MaxUint32)
		populated bool
	)

	noteAD := func(ad bool) {
		if !ad {
			return
		}
		mu.Lock()
		adAny = true
		mu.Unlock()
	}
	noteTTL := func(ttl uint32) {
		if ttl == 0 {
			return
		}
		mu.Lock()
		populated = true
		if ttl < minTTL {
			minTTL = ttl
		}
		mu.Unlock()
	}

	g, gctx := errgroup.WithContext(ctx)

	// A
	g.Go(func() error {
		addrs, ttl, ad, err := c.lookupA(gctx, addr, host)
		if err != nil {
			return passThroughThrottle(err, "A", host)
		}
		mu.Lock()
		rec.A = addrs
		mu.Unlock()
		noteAD(ad)
		noteTTL(ttl)
		return nil
	})
	// AAAA
	g.Go(func() error {
		addrs, ttl, ad, err := c.lookupAAAA(gctx, addr, host)
		if err != nil {
			return passThroughThrottle(err, "AAAA", host)
		}
		mu.Lock()
		rec.AAAA = addrs
		mu.Unlock()
		noteAD(ad)
		noteTTL(ttl)
		return nil
	})
	// CNAME
	g.Go(func() error {
		cname, ttl, ad, err := c.lookupCNAME(gctx, addr, host)
		if err != nil {
			return passThroughThrottle(err, "CNAME", host)
		}
		mu.Lock()
		rec.CNAME = cname
		mu.Unlock()
		noteAD(ad)
		noteTTL(ttl)
		return nil
	})
	// MX
	g.Go(func() error {
		mxs, ttl, ad, err := c.lookupMX(gctx, addr, host)
		if err != nil {
			return passThroughThrottle(err, "MX", host)
		}
		mu.Lock()
		rec.MX = mxs
		mu.Unlock()
		noteAD(ad)
		noteTTL(ttl)
		return nil
	})
	// NS — existence probe, ONLY goroutine that propagates errors.
	g.Go(func() error {
		nss, ttl, ad, err := c.lookupNS(gctx, addr, host)
		if err != nil {
			// ctx errors propagate as-is; sentinels propagate verbatim.
			if errors.Is(err, ErrSelfThrottled) {
				return err
			}
			if errors.Is(err, ErrNotFound) {
				return ErrNotFound
			}
			if errors.Is(err, ErrUnavailable) {
				return ErrUnavailable
			}
			return err
		}
		// NS NOERROR with empty answer is NOT NXDOMAIN — record-leaf
		// names like `_dmarc.gmail.com` have TXT but no NS RR. True
		// NXDOMAIN comes back as ErrNotFound via classifyTransportErr
		// (RcodeNameError), already handled in the err branch above.
		// Just record whatever NS RRs came back (possibly none) and let
		// other record-type goroutines populate their own rows.
		mu.Lock()
		rec.NS = nss
		mu.Unlock()
		noteAD(ad)
		noteTTL(ttl)
		return nil
	})
	// TXT
	g.Go(func() error {
		txt, ttl, ad, err := c.lookupTXT(gctx, addr, host)
		if err != nil {
			return passThroughThrottle(err, "TXT", host)
		}
		mu.Lock()
		rec.TXT = txt
		mu.Unlock()
		noteAD(ad)
		noteTTL(ttl)
		return nil
	})
	// SOA
	g.Go(func() error {
		soa, ttl, ad, err := c.lookupSOA(gctx, addr, host)
		if err != nil {
			return passThroughThrottle(err, "SOA", host)
		}
		mu.Lock()
		rec.SOA = soa
		mu.Unlock()
		noteAD(ad)
		noteTTL(ttl)
		return nil
	})
	// CAA
	g.Go(func() error {
		caa, ttl, ad, err := c.lookupCAA(gctx, addr, host)
		if err != nil {
			return passThroughThrottle(err, "CAA", host)
		}
		mu.Lock()
		rec.CAA = caa
		mu.Unlock()
		noteAD(ad)
		noteTTL(ttl)
		return nil
	})
	// SRV
	g.Go(func() error {
		srv, ttl, ad, err := c.lookupSRV(gctx, addr, host)
		if err != nil {
			return passThroughThrottle(err, "SRV", host)
		}
		mu.Lock()
		rec.SRV = srv
		mu.Unlock()
		noteAD(ad)
		noteTTL(ttl)
		return nil
	})

	if err := g.Wait(); err != nil {
		return Records{}, err
	}

	rec.DNSSECVerified = adAny
	if populated {
		rec.MinTTL = time.Duration(minTTL) * time.Second
	}
	// Single happy-path observability emission (R11). Per-type errors emit
	// their own debug lines; this row covers the common case where every
	// per-type query succeeded and on-call needs the wall-clock + populated
	// count to spot upstream-slow drift.
	log.Debug().
		Str("host", host).
		Str("resolver", addr).
		Dur("elapsed", time.Since(t0)).
		Bool("populated", populated).
		Bool("dnssec", rec.DNSSECVerified).
		Msg("dns lookup")
	return rec, nil
}

// passThroughThrottle is the per-type goroutine error-handling rule:
// ErrSelfThrottled propagates (deliberate sibling-cancel; egress gate
// is process-global), every other err is debug-logged and the row
// drops via nil.
func passThroughThrottle(err error, qtype, host string) error {
	if errors.Is(err, ErrSelfThrottled) {
		return err
	}
	log.Debug().Err(err).Str("qtype", qtype).Str("host", host).Msg("dns: per-type lookup error (row dropped)")
	return nil
}

// pickResolver returns the first resolver in c.resolvers that answers a
// 200ms TCP-dial preflight. Used once per Lookup; subsequent per-type
// queries reuse the picked addr. Bounds the TLS handshake count from
// 9-on-failure to 1-then-fallback.
//
// Preflight strategy: a TCP dial to addr with a 200ms deadline. We do
// NOT issue a TLS handshake here — a TCP-level dial is sufficient
// liveness (the per-query ExchangeContext will perform the handshake on
// the actual query and pay it once).
//
// All resolvers fail preflight → ErrUnavailable.
func (c *Client) pickResolver(ctx context.Context) (string, error) {
	for _, addr := range c.resolvers {
		pctx, cancel := context.WithTimeout(ctx, preflightTimeout)
		conn, err := c.dialer.DialContext(pctx, "tcp", addr)
		cancel()
		if err == nil {
			_ = conn.Close()
			return addr, nil
		}
	}
	return "", ErrUnavailable
}

// exchange snapshots ctx state, gates on the limiter, and issues one
// dns.ExchangeContext against addr. Returns the parsed response or a
// resolver sentinel. See §3 of DESIGN.md for the per-query hot-path
// rules (pre-cancelled-vs-self-throttled disambiguation).
func (c *Client) exchange(ctx context.Context, addr string, m *dns.Msg) (*dns.Msg, error) {
	var preCancelled bool
	select {
	case <-ctx.Done():
		preCancelled = true
	default:
	}
	if err := c.limiter.Wait(ctx); err != nil {
		if preCancelled {
			return nil, ctx.Err()
		}
		return nil, ErrSelfThrottled
	}
	resp, _, err := c.dns.ExchangeContext(ctx, m, addr)
	if err != nil {
		// Caller-cancellation propagates as ctx.Err() (NOT ErrUnavailable)
		// so handlers can distinguish a 502 from a client-disconnect.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		// TLS handshake failures (cert mismatch, SNI wrong, host unreachable)
		// land here. Surface at warn so operators see the actual cause —
		// otherwise the 502 at the handler is the only signal and there's
		// no clue why.
		log.Warn().Err(err).Str("addr", addr).Msg("dns: upstream exchange failed")
		return nil, ErrUnavailable
	}
	if e := classifyTransportErr(resp, nil); e != nil {
		return nil, e
	}
	return resp, nil
}

// minRRTTL returns min(rr.Header().Ttl) across rrs. Returns 0 when rrs
// is empty. Caller drops zero (no useful TTL signal there).
func minRRTTL(rrs []dns.RR) uint32 {
	var min uint32 = math.MaxUint32
	hit := false
	for _, rr := range rrs {
		ttl := rr.Header().Ttl
		if ttl < min {
			min = ttl
		}
		hit = true
	}
	if !hit {
		return 0
	}
	return min
}

// isPublicAddr reports whether a is renderable on a public banner. R5
// applies to A/AAAA only — strip private / loopback / link-local /
// unspecified / multicast addresses.
func isPublicAddr(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	if a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsUnspecified() || a.IsMulticast() {
		return false
	}
	return true
}

// trimDot strips a trailing root-zone dot, leaving the bare label.
func trimDot(s string) string { return strings.TrimSuffix(s, ".") }

// ----- Per-type query helpers -----

func (c *Client) lookupA(ctx context.Context, addr, host string) ([]netip.Addr, uint32, bool, error) {
	resp, err := c.exchange(ctx, addr, newQuery(host, dns.TypeA))
	if err != nil {
		return nil, 0, false, err
	}
	out := make([]netip.Addr, 0, len(resp.Answer))
	for _, rr := range resp.Answer {
		a, ok := rr.(*dns.A)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(a.A.To4())
		if !ok {
			continue
		}
		ip = ip.Unmap()
		if !isPublicAddr(ip) {
			continue
		}
		out = append(out, ip)
	}
	return out, minRRTTL(resp.Answer), resp.AuthenticatedData, nil
}

func (c *Client) lookupAAAA(ctx context.Context, addr, host string) ([]netip.Addr, uint32, bool, error) {
	resp, err := c.exchange(ctx, addr, newQuery(host, dns.TypeAAAA))
	if err != nil {
		return nil, 0, false, err
	}
	out := make([]netip.Addr, 0, len(resp.Answer))
	for _, rr := range resp.Answer {
		aaaa, ok := rr.(*dns.AAAA)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(aaaa.AAAA)
		if !ok {
			continue
		}
		// IPv4-mapped IPv6 (::ffff:a.b.c.d) reads as IsPrivate==false
		// because the Is4In6 form sits outside the v6-private prefixes;
		// Unmap collapses it back to the v4 form so the v4-private check
		// catches embedded RFC1918 addresses (would otherwise leak as
		// "10.0.0.1" rendered through an AAAA record).
		ip = ip.Unmap()
		if !isPublicAddr(ip) {
			continue
		}
		out = append(out, ip)
	}
	return out, minRRTTL(resp.Answer), resp.AuthenticatedData, nil
}

func (c *Client) lookupCNAME(ctx context.Context, addr, host string) (string, uint32, bool, error) {
	resp, err := c.exchange(ctx, addr, newQuery(host, dns.TypeCNAME))
	if err != nil {
		return "", 0, false, err
	}
	for _, rr := range resp.Answer {
		cname, ok := rr.(*dns.CNAME)
		if !ok {
			continue
		}
		return trimDot(cname.Target), minRRTTL(resp.Answer), resp.AuthenticatedData, nil
	}
	return "", minRRTTL(resp.Answer), resp.AuthenticatedData, nil
}

func (c *Client) lookupMX(ctx context.Context, addr, host string) ([]MX, uint32, bool, error) {
	resp, err := c.exchange(ctx, addr, newQuery(host, dns.TypeMX))
	if err != nil {
		return nil, 0, false, err
	}
	out := make([]MX, 0, len(resp.Answer))
	for _, rr := range resp.Answer {
		mx, ok := rr.(*dns.MX)
		if !ok {
			continue
		}
		out = append(out, MX{Pref: mx.Preference, Host: trimDot(mx.Mx)})
	}
	return out, minRRTTL(resp.Answer), resp.AuthenticatedData, nil
}

func (c *Client) lookupNS(ctx context.Context, addr, host string) ([]string, uint32, bool, error) {
	resp, err := c.exchange(ctx, addr, newQuery(host, dns.TypeNS))
	if err != nil {
		return nil, 0, false, err
	}
	out := make([]string, 0, len(resp.Answer))
	for _, rr := range resp.Answer {
		ns, ok := rr.(*dns.NS)
		if !ok {
			continue
		}
		out = append(out, trimDot(ns.Ns))
	}
	return out, minRRTTL(resp.Answer), resp.AuthenticatedData, nil
}

func (c *Client) lookupTXT(ctx context.Context, addr, host string) (TXTSummary, uint32, bool, error) {
	resp, err := c.exchange(ctx, addr, newQuery(host, dns.TypeTXT))
	if err != nil {
		return TXTSummary{}, 0, false, err
	}
	var sum TXTSummary
	for _, rr := range resp.Answer {
		t, ok := rr.(*dns.TXT)
		if !ok {
			continue
		}
		// Per-RR concatenate (RFC 7208 §3.3 within-RR rule). Classify
		// the joined-per-RR string; counts accumulate across RRs.
		joined := strings.Join(t.Txt, "")
		classifyTXT(joined, &sum)
	}
	return sum, minRRTTL(resp.Answer), resp.AuthenticatedData, nil
}

func (c *Client) lookupSOA(ctx context.Context, addr, host string) (*SOA, uint32, bool, error) {
	resp, err := c.exchange(ctx, addr, newQuery(host, dns.TypeSOA))
	if err != nil {
		return nil, 0, false, err
	}
	for _, rr := range resp.Answer {
		soa, ok := rr.(*dns.SOA)
		if !ok {
			continue
		}
		return &SOA{
			NS:     trimDot(soa.Ns),
			Mbox:   trimDot(soa.Mbox),
			Serial: soa.Serial,
		}, minRRTTL(resp.Answer), resp.AuthenticatedData, nil
	}
	return nil, minRRTTL(resp.Answer), resp.AuthenticatedData, nil
}

func (c *Client) lookupCAA(ctx context.Context, addr, host string) ([]CAA, uint32, bool, error) {
	resp, err := c.exchange(ctx, addr, newQuery(host, dns.TypeCAA))
	if err != nil {
		return nil, 0, false, err
	}
	out := make([]CAA, 0, len(resp.Answer))
	for _, rr := range resp.Answer {
		caa, ok := rr.(*dns.CAA)
		if !ok {
			continue
		}
		out = append(out, CAA{Flag: caa.Flag, Tag: caa.Tag, Value: caa.Value})
	}
	return out, minRRTTL(resp.Answer), resp.AuthenticatedData, nil
}

func (c *Client) lookupSRV(ctx context.Context, addr, host string) ([]SRV, uint32, bool, error) {
	resp, err := c.exchange(ctx, addr, newQuery(host, dns.TypeSRV))
	if err != nil {
		return nil, 0, false, err
	}
	out := make([]SRV, 0, len(resp.Answer))
	for _, rr := range resp.Answer {
		srv, ok := rr.(*dns.SRV)
		if !ok {
			continue
		}
		out = append(out, SRV{
			Priority: srv.Priority,
			Weight:   srv.Weight,
			Port:     srv.Port,
			Target:   trimDot(srv.Target),
		})
	}
	return out, minRRTTL(resp.Answer), resp.AuthenticatedData, nil
}

// classifyTXT updates sum based on a single TXT RR's joined-chunks
// string. Mutually exclusive prefix match per RR; "anything else" goes
// to OtherCount.
func classifyTXT(s string, sum *TXTSummary) {
	switch {
	case strings.HasPrefix(s, "v=spf1"):
		sum.HasSPF = true
	case strings.HasPrefix(s, "v=DMARC1"):
		sum.HasDMARC = true
	case strings.HasPrefix(s, "v=DKIM1"):
		sum.DKIMCount++
	default:
		for _, p := range verificationPrefixes {
			if strings.HasPrefix(s, p) {
				sum.VerificationCount++
				return
			}
		}
		sum.OtherCount++
	}
}
