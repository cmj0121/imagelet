// Package dns exposes the /dns/:hostname banner-card route. Handler
// shape mirrors service/github (WantsPylonSource short-circuit + a
// per-mode dispatch in writeBanner). Banner cards summarize DNS
// records; pylon source is NOT supported on this route — no honest
// pylon representation, so the handler returns 415 instead of falling
// through.
//
// Hostnames are canonicalized inline (lowercase + trim trailing dot +
// idna.Lookup.ToASCII) so cache keys / wire queries share one form
// across `EXAMPLE.COM`, `example.com.`, and `exämple.com`. The
// canonicalization is duplicated from service/dns/resolver because the
// resolver-package helper is unexported (DESIGN.md §6).
//
// Error mapping (DESIGN.md §2):
//
//	nil err                              → 200, max-age=300
//	ErrNotFound                          → 404, max-age=3600, "NOT FOUND" banner
//	ErrSelfThrottled                     → 503, max-age=60, "RATE LIMITED" banner
//	stale value + ErrUnavailable         → 200, max-age=60, "( stale data )" caption appended
//	ErrUnavailable (no value)            → 502, no body
//	WantsPylonSource                     → 415, plain-text redirect message
//	invalid hostname                     → 400, plain-text "invalid hostname\n"
//
// Vary: User-Agent is set on EVERY response — including 400 / 415 — so
// a downstream CDN does not serve one UA-class's body to another.
package dns

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
	"golang.org/x/net/idna"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
	"github.com/cmj0121/imagelet/service/dns/resolver"
)

// pylonUnsupportedMsg is the body returned for ?format=pylon /
// Accept: text/pylon on /dns. Mirrors the github handler.
const pylonUnsupportedMsg = "pylon source is not supported on /dns; try ?format=png|svg|html|ascii"

// hostnameMaxLen is the RFC 1035 limit on a fully-qualified hostname.
const hostnameMaxLen = 253

// hostRe allows leading underscore per label (RFC 8552 underscored
// attribute leaves; DKIM, DMARC, SRV use them). Each label must start
// and end with an alphanumeric (with optional leading underscore on
// the first char per RFC 8552); interior hyphens are allowed,
// including consecutive hyphens so IDN punycode `xn--…` labels pass.
// Empty labels are rejected; trailing dot is allowed (the
// canonicalizer strips it before the regex matches, but we leave the
// optional `\.?` so a fast-path regex check on raw input still passes).
var hostRe = regexp.MustCompile(
	`^(_?[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?\.)*_?[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?\.?$`,
)

// refusedSuffixes are reserved / private TLDs (RFC 6762 + RFC 2606 +
// RFC 6761). Trailing dot already stripped before the suffix check.
var refusedSuffixes = []string{
	".local",
	".localhost",
	".internal",
	".lan",
	".example",
	".test",
	".invalid",
	".arpa",
}

// captionBudget caps a caption row's rendered rune count. Mirrors /github.
const captionBudget = 60

// truncateSuffix is appended when a caption is shortened. U+2026
// HORIZONTAL ELLIPSIS — same fallback policy as /github.
const truncateSuffix = "…"

// Per-state Cache-Control max-ages (DESIGN.md §2 / R9). Handler-level
// values; the resolver-level positive cache is independent and honors
// the upstream-clamped TTL (R6).
const (
	okMaxAge        = 300  // 5 min — success path
	notFoundMaxAge  = 3600 // 1 h — negative cache for typo-floods
	rateLimitMaxAge = 60   // 1 min — rate-limited / stale-served paths
	staleMaxAge     = 60   // 1 min — alias for clarity at call sites
)

// sanitizerReplacer is the same forbidden-char substitution table
// /github uses. Headlines bypass it (hostRe constrains the headline
// charset to render-safe characters); captions go through sanitize()
// at the point of access.
var sanitizerReplacer = strings.NewReplacer(
	":", ";",
	"%", "pct",
	",", " ",
	"▲", " ",
	"▼", " ",
)

// sanitize applies the forbidden-char table, runs render.StripPylonSyntax
// as a belt-and-braces guard, and truncates to captionBudget runes
// (multi-byte safe — rune count, not byte count, is the budget).
func sanitize(s string) string {
	s = sanitizerReplacer.Replace(s)
	s = render.StripPylonSyntax(s)
	if utf8.RuneCountInString(s) > captionBudget {
		s = truncateRunes(s, captionBudget-1) + truncateSuffix
	}
	return s
}

// truncateRunes returns s shortened to at most n runes. Slices on a
// rune boundary so multi-byte UTF-8 stays valid.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for idx := range s {
		if i == n {
			return s[:idx]
		}
		i++
	}
	return s
}

// handler holds the per-route Resolver dependency. The field is the
// narrow resolver.Resolver interface — *cached.Cached implements it,
// but tests can substitute a fake without dragging in the cache
// machinery or mockdns.
type handler struct {
	res resolver.Resolver
}

// Register mounts /dns/:hostname on r. r is gin.IRouter so cmd/imagelet
// can wrap it in a per-IP rate-limit group (DESIGN.md §7).
func Register(r gin.IRouter, res resolver.Resolver) {
	h := &handler{res: res}
	r.GET("/dns/:hostname", h.serveDNS)
}

// RateLimitedHandler is the exported shim invoked by middleware/iplimit
// when a per-IP token bucket exhausts. Same shape as github.RateLimitedHandler
// so iplimit doesn't need a circular import. NOTE: this is a per-IP
// bucket rejection — distinct from the process-global self-throttle
// path which renders 503; per-IP is 200 + max-age=60 to match /github
// (CDNs honour max-age on 200; not on 429).
func RateLimitedHandler(c *gin.Context) {
	renderRateLimited(c)
}

func (h *handler) serveDNS(c *gin.Context) {
	c.Header("Vary", "User-Agent")
	if middleware.WantsPylonSource(c) {
		c.Data(http.StatusUnsupportedMediaType, "text/plain; charset=utf-8", []byte(pylonUnsupportedMsg))
		return
	}
	raw := strings.TrimSpace(c.Param("hostname"))
	host, ok := validateAndCanonicalize(raw)
	if !ok {
		c.Header("Cache-Control", "no-store")
		c.String(http.StatusBadRequest, "invalid hostname\n")
		return
	}
	recs, err := h.res.Lookup(c.Request.Context(), host)
	h.render(c, host, recs, err)
}

// validateAndCanonicalize runs (in order): empty/length check,
// trailing-dot trim, IP-literal reject, idna.Lookup.ToASCII (only when
// the input has non-ASCII runes), regex, refused-suffix check,
// all-numeric-rightmost-label reject. Returns ok=false on any failure;
// the caller emits a uniform 400.
//
// idna.Lookup is strict and rejects RFC 8552 underscored labels
// (_dmarc, _spf, ...) — but underscore-bearing hostnames are by
// construction pure ASCII, so we short-circuit IDN translation when
// the input has no non-ASCII runes. Same shape as the resolver
// package's unexported canonicalize helper; cache keys and wire
// queries land on the same string.
//
// Order matters: IP-literal check happens AFTER trailing-dot trim so
// `1.1.1.1.` (trailing-dot IP literal) is also rejected. The
// all-numeric rightmost-label check rejects strings that could only be
// IPv4 fragments (no all-numeric TLDs exist per ICANN).
func validateAndCanonicalize(raw string) (string, bool) {
	if raw == "" || len(raw) > hostnameMaxLen {
		return "", false
	}
	host := strings.ToLower(strings.TrimSuffix(raw, "."))
	if host == "" {
		return "", false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return "", false
	}
	if !isASCII(host) {
		var err error
		host, err = idna.Lookup.ToASCII(host)
		if err != nil {
			return "", false
		}
	}
	if host == "" || len(host) > hostnameMaxLen || !hostRe.MatchString(host) {
		return "", false
	}
	for _, suf := range refusedSuffixes {
		if strings.HasSuffix(host, suf) {
			return "", false
		}
	}
	last := host
	if i := strings.LastIndexByte(host, '.'); i >= 0 {
		last = host[i+1:]
	}
	if allDigits(last) {
		return "", false
	}
	return host, true
}

// allDigits reports whether s is non-empty and consists only of '0'–'9'.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isASCII reports whether s consists entirely of bytes < 0x80.
// Mirrors the resolver-package helper; underscore-bearing hostnames
// are pure ASCII by construction so they bypass idna's strict rules.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// render dispatches on the resolver error class and emits the right
// status / max-age / body. Stale-serve detection mirrors /github:
// recs.Hostname distinguishes "stale value present" from "no value at
// all" without a third return value.
func (h *handler) render(c *gin.Context, host string, recs resolver.Records, err error) {
	headline := dnsHeadline(host)
	switch {
	case err == nil:
		writeBanner(c, headline, dnsCaptions(host, recs), http.StatusOK, okMaxAge)
	case errors.Is(err, resolver.ErrNotFound):
		renderNotFound(c, host)
	case errors.Is(err, resolver.ErrSelfThrottled):
		renderSelfThrottled(c)
	default:
		// Stale-serve: cached.Lookup can return a populated Records +
		// transient err (ErrUnavailable). recs.Hostname distinguishes
		// "stale value present" from "no value at all".
		if recs.Hostname != "" {
			captions := append(dnsCaptions(host, recs), "( stale data )")
			writeBanner(c, headline, captions, http.StatusOK, staleMaxAge)
			return
		}
		log.Warn().Err(err).Str("path", c.Request.URL.Path).Msg("dns upstream failed")
		c.Header("Cache-Control", "no-store")
		c.AbortWithStatus(http.StatusBadGateway)
	}
}

// renderNotFound emits a 404 banner with hostname as the single
// caption. host is already validated by hostRe so passing through
// without sanitisation is safe.
func renderNotFound(c *gin.Context, host string) {
	writeBanner(c, "NOT FOUND", []string{host}, http.StatusNotFound, notFoundMaxAge)
}

// renderRateLimited is invoked by iplimit's per-IP short-circuit path.
// Status 200 keeps CDN/unfurl-bot caches honouring max-age=60 (RFC 7234
// doesn't list 429 as cacheable by default; same posture as /github).
func renderRateLimited(c *gin.Context) {
	writeBanner(c, "RATE LIMITED", []string{"try again later"}, http.StatusOK, rateLimitMaxAge)
}

// renderSelfThrottled is invoked when the resolver returns
// ErrSelfThrottled (process-global egress gate denied the query). 503
// is semantically honest: WE self-throttled (not the upstream), so the
// caller learns the route is overloaded server-side rather than that
// the queried hostname misbehaved. max-age=60 keeps the response
// CDN-cacheable so a botnet flood cannot re-hit the origin every
// request — the CDN absorbs duplicates for a minute, then we probe
// again.
func renderSelfThrottled(c *gin.Context) {
	writeBanner(c, "RATE LIMITED", []string{"TRY LATER"}, http.StatusServiceUnavailable, rateLimitMaxAge)
}

// writeBanner is the per-mode dispatch shared by every successful
// response path (success, stale, 404, rate-limited, self-throttled).
// Always emits Cache-Control + Vary BEFORE the body so even error
// paths get cacheable headers. Byte-pattern identical to /github's
// writeBanner — the OG title prefix is the same.
func writeBanner(c *gin.Context, headline string, captions []string, status, maxAge int) {
	c.Header("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
	c.Header("Vary", "User-Agent")
	mode := middleware.ResolveMode(c)
	body, err := render.BannerMulti(headline, captions, mode)
	if err != nil {
		log.Error().Err(err).Str("headline", headline).Stringer("mode", mode).Msg("dns banner render")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	switch mode {
	case render.ModePNG:
		c.Data(status, "image/png", body)
	case render.ModeSVG:
		c.Data(status, "image/svg+xml", body)
	case render.ModeHTML:
		og := render.OGMeta{
			Title:     "imagelet · " + headline,
			ImageURL:  middleware.AbsoluteURL(c, "format=png"),
			ImageType: "image/png",
			PageURL:   middleware.AbsoluteURL(c, ""),
		}
		c.Data(status, "text/html; charset=utf-8", render.InjectOGMeta(body, og))
	default:
		c.Data(status, "text/plain; charset=utf-8", body)
	}
}

// dnsHeadline returns the banner headline for host. Delegates to
// firstAlphaNumLabel so SRV-shaped names (`_dmarc.example.com`) skip
// the underscored leaf and surface the parent zone (`EXAMPLE`). Bare
// TLDs (`com`) work too.
func dnsHeadline(host string) string {
	return strings.ToUpper(firstAlphaNumLabel(host))
}

// firstAlphaNumLabel returns the first dot-separated label of host
// that consists entirely of [A-Za-z0-9] (no underscores, no hyphens —
// figlet-safe at banner scale). Returns "?" if no label qualifies
// (defensive: validation already rejects empty labels, so this only
// triggers for pathological all-underscored names which slipped past
// underscored-attribute validation).
func firstAlphaNumLabel(host string) string {
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			continue
		}
		ok := true
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
				ok = false
				break
			}
		}
		if ok {
			return label
		}
	}
	return "?"
}

// dnsCaptions builds the ordered caption rows. Empty record types drop
// (don't render as blank). The first row is always the canonical FQDN
// so the headline-is-first-label fallback doesn't lose the full
// hostname. Subsequent rows follow PLAN.md R8 / DESIGN.md §6: A, AAAA,
// CNAME, MX, NS, TXT, SOA, CAA, SRV, then DNSSEC badge.
func dnsCaptions(host string, r resolver.Records) []string {
	out := []string{sanitize(host)}
	if s := joinAddrs(r.A); s != "" {
		out = append(out, s)
	}
	if s := joinAddrs(r.AAAA); s != "" {
		out = append(out, s)
	}
	if r.CNAME != "" {
		if s := sanitize(r.CNAME); s != "" {
			out = append(out, s)
		}
	}
	if s := formatMX(r.MX); s != "" {
		out = append(out, s)
	}
	if s := formatNS(r.NS); s != "" {
		out = append(out, s)
	}
	if s := formatTXT(r.TXT); s != "" {
		out = append(out, s)
	}
	if s := formatSOA(r.SOA); s != "" {
		out = append(out, s)
	}
	if s := formatCAA(r.CAA); s != "" {
		out = append(out, s)
	}
	if s := formatSRV(r.SRV); s != "" {
		out = append(out, s)
	}
	if r.DNSSECVerified {
		out = append(out, "DNSSEC ✓")
	}
	return out
}

// joinAddrs renders an []netip.Addr as "1.2.3.4 · 5.6.7.8". Already
// private-filtered upstream; numeric output bypasses sanitize.
func joinAddrs(addrs []netip.Addr) string {
	if len(addrs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(addrs))
	for _, a := range addrs {
		parts = append(parts, a.String())
	}
	return strings.Join(parts, " · ")
}

// formatMX renders []MX as "10 mx1.example.com · 20 mx2.example.com"
// (the `·` is the row separator, not a sanitizer target). Hosts pass
// through sanitize at append time.
func formatMX(mxs []resolver.MX) string {
	if len(mxs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(mxs))
	for _, m := range mxs {
		host := sanitize(m.Host)
		if host == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d %s", m.Pref, host))
	}
	return strings.Join(parts, " · ")
}

// formatNS renders []string as "ns1.example.com · ns2.example.com".
// Hosts pass through sanitize at append time.
func formatNS(nss []string) string {
	if len(nss) == 0 {
		return ""
	}
	parts := make([]string, 0, len(nss))
	for _, n := range nss {
		s := sanitize(n)
		if s == "" {
			continue
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " · ")
}

// formatTXT renders TXTSummary as "spf · dmarc · 3 verifications · 1 other".
// Empty fields drop; full empty struct returns "".
func formatTXT(t resolver.TXTSummary) string {
	var parts []string
	if t.HasSPF {
		parts = append(parts, "spf")
	}
	if t.HasDMARC {
		parts = append(parts, "dmarc")
	}
	if t.DKIMCount > 0 {
		if t.DKIMCount == 1 {
			parts = append(parts, "dkim")
		} else {
			parts = append(parts, fmt.Sprintf("%d dkim", t.DKIMCount))
		}
	}
	if t.VerificationCount > 0 {
		if t.VerificationCount == 1 {
			parts = append(parts, "1 verification")
		} else {
			parts = append(parts, fmt.Sprintf("%d verifications", t.VerificationCount))
		}
	}
	if t.OtherCount > 0 {
		if t.OtherCount == 1 {
			parts = append(parts, "1 other")
		} else {
			parts = append(parts, fmt.Sprintf("%d other", t.OtherCount))
		}
	}
	return strings.Join(parts, " · ")
}

// formatSOA renders *SOA as "<ns> <mbox> <serial>". nil → "".
func formatSOA(s *resolver.SOA) string {
	if s == nil {
		return ""
	}
	ns := sanitize(s.NS)
	mbox := sanitize(s.Mbox)
	if ns == "" && mbox == "" && s.Serial == 0 {
		return ""
	}
	return fmt.Sprintf("%s %s %d", ns, mbox, s.Serial)
}

// formatCAA renders []CAA as "0 issue letsencrypt.org · 0 iodef mailto;sec@..."
// (note the `:` → `;` substitution in iodef values applies via sanitize).
func formatCAA(caa []resolver.CAA) string {
	if len(caa) == 0 {
		return ""
	}
	parts := make([]string, 0, len(caa))
	for _, r := range caa {
		val := sanitize(r.Value)
		tag := sanitize(r.Tag)
		parts = append(parts, fmt.Sprintf("%d %s %s", r.Flag, tag, val))
	}
	return strings.Join(parts, " · ")
}

// formatSRV renders []SRV as "<prio> <weight> <port> <target>" for the
// first record, plus " · +N more" suffix when len > 1. Banner cards
// summarize.
func formatSRV(srvs []resolver.SRV) string {
	if len(srvs) == 0 {
		return ""
	}
	first := srvs[0]
	target := sanitize(first.Target)
	out := fmt.Sprintf("%d %d %d %s", first.Priority, first.Weight, first.Port, target)
	if len(srvs) > 1 {
		out += fmt.Sprintf(" · +%d more", len(srvs)-1)
	}
	return out
}
