// Package resolver provides public DNS record retrieval for the /dns
// service. The package layout mirrors service/github/profile: a value
// type (Records), a narrow Resolver interface, a transport+config
// Client (client.go) with the per-Lookup fan-out in lookup.go, and a
// TTL+singleflight cache wrapper in the cached/ subpackage.
//
// Errors collapse to three sentinel values — ErrNotFound, ErrUnavailable,
// ErrSelfThrottled — so the handler can switch deterministically and
// the cached layer can apply per-state TTLs.
package resolver

import (
	"context"
	"errors"
	"net/netip"
	"time"
)

// Records is the typed aggregate returned by Lookup. Empty slices /
// zero values indicate the record type was absent or filtered; the
// handler drops empty rows from the rendered banner.
type Records struct {
	Hostname string       // canonical: lowercased + trailing-dot trimmed + idna.Lookup.ToASCII
	A        []netip.Addr // private / link-local / loopback / unspecified / multicast already filtered (R5)
	AAAA     []netip.Addr // ditto
	CNAME    string       // single value; "" when absent
	MX       []MX
	NS       []string
	TXT      TXTSummary // prefix-classified per R8 — never the raw strings
	SOA      *SOA       // nil when absent
	CAA      []CAA
	SRV      []SRV
	// DNSSECVerified is the OR-aggregated AD bit across per-type
	// responses (R14); true means the banner shows the badge.
	DNSSECVerified bool
	// MinTTL is min(rr.Header().Ttl) across every populated RRset; used
	// for cache clamping (R6). Initialized to math.MaxUint32 (as a
	// time.Duration of seconds) by Lookup; replaced as each RRset
	// populates. If no RRsets populate, stays at sentinel; the cached
	// layer's clamp [60s, 600s] catches both the sentinel and the
	// all-zero-TTL case (any input below 60s lands at the floor).
	MinTTL time.Duration
}

// MX is one mail-exchanger record.
type MX struct {
	Pref uint16
	Host string
}

// SOA is the start-of-authority record. Only the three fields rendered
// on the banner are kept; refresh / retry / expire are dropped.
type SOA struct {
	NS     string // mname
	Mbox   string // rname
	Serial uint32
}

// CAA is one certificate-authority-authorization record.
type CAA struct {
	Flag  uint8
	Tag   string
	Value string
}

// SRV is one service-locator record.
type SRV struct {
	Priority uint16
	Weight   uint16
	Port     uint16
	Target   string
}

// TXTSummary is the prefix-classified summary of every TXT record at
// the host. Banner cards summarize, NOT truncate raw text (R8).
//
// Classification is per-RR (NOT across RRs). For each TXT RR: join its
// own multi-string chunks (RFC 7208 §3.3 within-RR rule), then match
// the joined-per-RR string against the prefix table. Counts accumulate
// across RRs; flags OR across RRs.
//
//	v=spf1                                                       → HasSPF
//	v=DMARC1                                                     → HasDMARC
//	v=DKIM1                                                      → DKIMCount++
//	<vendor>-(site|domain)-verification=, MS=, _atproto, stripe-verification=
//	                                                             → VerificationCount++
//	anything else                                                → OtherCount++
type TXTSummary struct {
	HasSPF            bool
	HasDMARC          bool
	DKIMCount         int
	VerificationCount int
	OtherCount        int
}

// Resolver is the narrow interface the handler depends on. Both
// *Client and *cached.Cached satisfy it.
type Resolver interface {
	Lookup(ctx context.Context, hostname string) (Records, error)
}

// Sentinel errors. The handler switches on these via errors.Is and
// the cached layer applies per-state TTLs.
var (
	// ErrNotFound — the NS existence-probe query returned NXDOMAIN
	// (or NS-records-empty-on-success). Maps to HTTP 404 +
	// Cache-Control: max-age=3600.
	ErrNotFound = errors.New("dns: not found")

	// ErrUnavailable — every fallback resolver was exhausted with
	// connection-class errors (refused / TLS handshake / deadline /
	// FORMERR / SERVFAIL on the NS probe). Maps to HTTP 502 +
	// Cache-Control: no-store, OR 200 + max-age=60 + "( stale data )"
	// caption when a prior stateOK entry exists in cache.
	ErrUnavailable = errors.New("dns: upstream unavailable")

	// ErrSelfThrottled — process-global rate gate (rate.Limiter)
	// denied the query before it left the box. Distinct from upstream
	// throttle which we never see (Cloudflare 1.1.1.1 doesn't surface a
	// clean throttle code). Maps to HTTP 503 + Cache-Control: max-age=60.
	ErrSelfThrottled = errors.New("dns: self-throttled")
)
