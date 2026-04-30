// Package safehttp centralizes outbound HTTP plumbing with SSRF
// guards (CheckRedirect refuses private/loopback/link-local
// destinations and caps redirect hops) and a body-cap helper.
//
// All upstream providers (yahoo, twse) construct their *http.Client
// via NewClient, and every successful resp.Body is wrapped via
// BoundBody so a malicious or runaway upstream response can't OOM
// the process — both the success-path JSON decoder and the error-
// path io.ReadAll inherit the cap.
package safehttp

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/publicsuffix"
)

// DefaultRedirectCap bounds the number of redirect hops the shared
// CheckRedirect policy will follow before refusing.
const DefaultRedirectCap = 5

// DefaultBodyCap is the default upper bound applied to upstream
// response bodies. Sized for the largest legitimate TWSE responses
// (full T86 / breadth dumps land well under 8 MiB).
const DefaultBodyCap = 8 << 20 // 8 MiB

// YahooBodyCap is the upper bound applied to Yahoo chart responses —
// the v8 chart endpoint returns ≤ a few hundred KiB even for the
// 3-month range, so 1 MiB is comfortably above legitimate traffic.
const YahooBodyCap = 1 << 20 // 1 MiB

// DefaultUserAgent is the User-Agent string sent by every outbound
// imagelet HTTP request. Yahoo rejects Go's default UA with 401, and
// TWSE/TAIFEX endpoints expect a browser-shaped UA; a single shared
// constant keeps the version in lockstep across providers.
const DefaultUserAgent = "Mozilla/5.0 (compatible; imagelet/0.3)"

// NewClient returns an http.Client with the given timeout and the
// shared CheckRedirect policy.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: RedirectPolicy,
	}
}

// RedirectPolicy is the *http.Client.CheckRedirect implementation:
//   - rejects requests after DefaultRedirectCap hops
//   - allows redirects to a host within the same eTLD+1 as the
//     prior request (e.g. query1.finance.yahoo.com → query2.finance.yahoo.com)
//   - rejects redirects whose destination host (or any DNS-resolved
//     IP) is private / loopback / link-local / unspecified / multicast
func RedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= DefaultRedirectCap {
		return errors.New("safehttp: too many redirects")
	}

	dstHost := hostOnly(req.URL.Host)

	// Same-eTLD+1 fast path: if both prior and new resolve to the
	// same registered domain, allow without DNS. This is purely
	// string-based on the public-suffix list.
	if len(via) > 0 {
		prevHost := hostOnly(via[len(via)-1].URL.Host)
		if sameRegisteredDomain(prevHost, dstHost) {
			return nil
		}
	}

	// Different eTLD+1 (or one side is an IP literal). Resolve the
	// destination and refuse if any IP lands in a reserved range.
	if ip := net.ParseIP(dstHost); ip != nil {
		if isUnsafeIP(ip) {
			return fmt.Errorf("safehttp: redirect to unsafe host %q (resolved IP=%s)", req.URL.Host, ip)
		}
		return nil
	}

	ips, err := net.LookupIP(dstHost)
	if err != nil {
		return fmt.Errorf("safehttp: redirect host %q lookup: %w", req.URL.Host, err)
	}
	for _, ip := range ips {
		if isUnsafeIP(ip) {
			return fmt.Errorf("safehttp: redirect to unsafe host %q (resolved IP=%s)", req.URL.Host, ip)
		}
	}
	return nil
}

// BoundBody wraps resp.Body with http.MaxBytesReader so subsequent
// reads (success path or error path) cannot exceed capBytes. Mutates
// resp.Body in place. Pass nil for the ResponseWriter — this is a
// client-side cap.
func BoundBody(resp *http.Response, capBytes int64) {
	if resp == nil || resp.Body == nil {
		return
	}
	resp.Body = http.MaxBytesReader(nil, resp.Body, capBytes)
}

// hostOnly strips an optional :port suffix from host. Hosts without
// a port are returned unchanged.
func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// sameRegisteredDomain reports whether a and b share the same eTLD+1
// (registered domain) per the public-suffix list. Returns false when
// either side is an IP literal or eTLD+1 extraction fails.
func sameRegisteredDomain(a, b string) bool {
	if net.ParseIP(a) != nil || net.ParseIP(b) != nil {
		return false
	}
	aRoot, err := publicsuffix.EffectiveTLDPlusOne(a)
	if err != nil {
		return false
	}
	bRoot, err := publicsuffix.EffectiveTLDPlusOne(b)
	if err != nil {
		return false
	}
	return aRoot == bRoot
}

// isUnsafeIP returns true for IPs in reserved ranges that callers
// of an outbound HTTP service must never reach: loopback,
// link-local, RFC1918 private, unspecified (0.0.0.0 / ::), and
// multicast. IsPrivate has been in stdlib since Go 1.17.
func isUnsafeIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()
}
