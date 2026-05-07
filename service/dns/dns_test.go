package dns_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/service/dns"
	"github.com/cmj0121/imagelet/service/dns/resolver"
)

// pngMagic is the 8-byte PNG file signature.
var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// fakeResolver implements resolver.Resolver with canned responses + a
// per-call counter so tests can assert "no upstream call" for the 400
// validation paths and capture the canonicalized hostname the handler
// passed in (so canonicalization tests can pin the inner-call shape).
type fakeResolver struct {
	mu        sync.Mutex
	recs      resolver.Records
	err       error
	calls     int
	lastHost  string
	hostsSeen []string
}

func (f *fakeResolver) Lookup(_ context.Context, host string) (resolver.Records, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastHost = host
	f.hostsSeen = append(f.hostsSeen, host)
	return f.recs, f.err
}

// freshRecords returns a deterministic Records value with every record
// type populated so caption-row assertions don't depend on optional
// fields.
func freshRecords() resolver.Records {
	return resolver.Records{
		Hostname: "example.com",
		A:        []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("1.1.1.1")},
		AAAA:     []netip.Addr{netip.MustParseAddr("2606:2800:220:1::248:1893")},
		CNAME:    "",
		MX: []resolver.MX{
			{Pref: 10, Host: "mx1.example.com"},
			{Pref: 20, Host: "mx2.example.com"},
		},
		NS: []string{"a.iana-servers.net", "b.iana-servers.net"},
		TXT: resolver.TXTSummary{
			HasSPF:            true,
			HasDMARC:          true,
			DKIMCount:         0,
			VerificationCount: 3,
			OtherCount:        0,
		},
		SOA: &resolver.SOA{
			NS:     "ns.icann.org",
			Mbox:   "noc.dns.icann.org",
			Serial: 2024010101,
		},
		CAA: []resolver.CAA{
			{Flag: 0, Tag: "issue", Value: "letsencrypt.org"},
		},
		SRV: []resolver.SRV{
			{Priority: 10, Weight: 5, Port: 5060, Target: "sip.example.com"},
		},
		DNSSECVerified: true,
	}
}

// newRouter wires the middleware the handler depends on (ClientDetector
// for UA-derived mode resolution) plus the dns route at the engine root.
func newRouter(res resolver.Resolver) *gin.Engine {
	r := gin.New()
	r.Use(middleware.ClientDetector())
	dns.Register(r, res)
	return r
}

// do is a small helper that sends a request through r and returns the
// recorder.
func do(r *gin.Engine, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// --- success path ----------------------------------------------------------

func TestDNS_OK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(&fakeResolver{recs: freshRecords()})
	rec := do(r, http.MethodGet, "/dns/example.com", map[string]string{
		"User-Agent": "curl/8.4.0",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=300" {
		t.Errorf("Cache-Control = %q, want public, max-age=300", got)
	}
	if got := rec.Header().Get("Vary"); got != "User-Agent" {
		t.Errorf("Vary = %q, want User-Agent", got)
	}
}

// TestDNS_CaptionRows asserts the SVG body carries the populated
// caption rows in the documented order.
func TestDNS_CaptionRows(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(&fakeResolver{recs: freshRecords()})
	rec := do(r, http.MethodGet, "/dns/example.com?format=svg", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// joinAddrs bypasses sanitize (numeric output) — the AAAA row
	// keeps raw colons. Other free-text rows pass through sanitize
	// where colons would substitute to `;`, but MX/NS/TXT in this
	// fixture are all alphanumeric.
	wants := []string{
		"A       93.184.216.34 · 1.1.1.1",                       // A row
		"AAAA    2606:2800:220:1::248:1893",                     // AAAA row
		"MX      10 mx1.example.com · 20 mx2.example.com",       // MX row
		"NS      a.iana-servers.net · b.iana-servers.net",       // NS row
		"TXT     spf · dmarc · 3 verifications",                 // TXT row
		"DNSSEC  ✓",                                             // DNSSEC badge
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("body missing caption row %q\n--- body (first 1500) ---\n%.1500s", w, body)
		}
	}
}

// --- 415 pylon -------------------------------------------------------------

func TestDNS_PylonUnsupported(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fr := &fakeResolver{recs: freshRecords()}
	r := newRouter(fr)
	rec := do(r, http.MethodGet, "/dns/example.com?format=pylon", nil)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "pylon source is not supported") {
		t.Errorf("415 body missing redirect message\n--- body ---\n%s", rec.Body.String())
	}
	// Pylon short-circuit happens BEFORE Lookup; resolver must not be
	// invoked.
	if fr.calls != 0 {
		t.Errorf("pylon path hit resolver %d times, want 0", fr.calls)
	}
	// Vary header MUST still be present (set BEFORE the pylon check).
	if got := rec.Header().Get("Vary"); got != "User-Agent" {
		t.Errorf("Vary = %q, want User-Agent on 415", got)
	}
}

// --- 404 -------------------------------------------------------------------

func TestDNS_404Banner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(&fakeResolver{err: resolver.ErrNotFound})
	rec := do(r, http.MethodGet, "/dns/example.com", map[string]string{
		"User-Agent": "curl/8.4.0",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Errorf("Cache-Control = %q, want public, max-age=3600", got)
	}
	if !strings.Contains(rec.Body.String(), "example.com") {
		t.Errorf("404 body missing hostname caption\n--- body ---\n%s", rec.Body.String())
	}
}

// --- 502 unavailable -------------------------------------------------------

func TestDNS_502OnUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// Empty Records (Hostname=="") + ErrUnavailable → 502, no body.
	r := newRouter(&fakeResolver{err: resolver.ErrUnavailable})
	rec := do(r, http.MethodGet, "/dns/example.com", map[string]string{
		"User-Agent": "curl/8.4.0",
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// --- 503 self-throttled ----------------------------------------------------

func TestDNS_503OnSelfThrottled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(&fakeResolver{err: resolver.ErrSelfThrottled})
	rec := do(r, http.MethodGet, "/dns/example.com", map[string]string{
		"User-Agent": "curl/8.4.0",
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("Cache-Control = %q, want public, max-age=60", got)
	}
	body := rec.Body.String()
	// "RATE LIMITED" / "TRY LATER" copy. Headlines render as block
	// glyphs in the ASCII banner (so the literal "RATE LIMITED" is not
	// in the body); the caption "TRY LATER" IS rendered as literal text.
	if !strings.Contains(body, "TRY LATER") {
		t.Errorf("503 body missing 'TRY LATER' caption\n--- body ---\n%s", body)
	}
}

// --- stale -----------------------------------------------------------------

func TestDNS_StaleOnTransient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// Cached can return a populated Records + a transient err
	// (ErrUnavailable). The handler renders the cached value with a
	// "( stale data )" caption row appended.
	r := newRouter(&fakeResolver{recs: freshRecords(), err: resolver.ErrUnavailable})
	rec := do(r, http.MethodGet, "/dns/example.com", map[string]string{
		"User-Agent": "curl/8.4.0",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (stale-but-have-data must not 5xx)", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("Cache-Control = %q, want public, max-age=60", got)
	}
	if !strings.Contains(rec.Body.String(), "stale data") {
		t.Errorf("stale body missing 'stale data' caption\n--- body ---\n%s", rec.Body.String())
	}
}

// --- Vary header on every status ------------------------------------------

func TestDNS_VaryHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		fr   *fakeResolver
		path string
	}{
		{"ok", &fakeResolver{recs: freshRecords()}, "/dns/example.com"},
		{"notfound", &fakeResolver{err: resolver.ErrNotFound}, "/dns/example.com"},
		{"selfthrottled", &fakeResolver{err: resolver.ErrSelfThrottled}, "/dns/example.com"},
		{"unavailable_502", &fakeResolver{err: resolver.ErrUnavailable}, "/dns/example.com"},
		{"stale_200", &fakeResolver{recs: freshRecords(), err: resolver.ErrUnavailable}, "/dns/example.com"},
		{"invalid_400", &fakeResolver{}, "/dns/.."},
		{"refused_suffix_400", &fakeResolver{}, "/dns/foo.local"},
		{"pylon_415", &fakeResolver{recs: freshRecords()}, "/dns/example.com?format=pylon"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRouter(tc.fr)
			rec := do(r, http.MethodGet, tc.path, map[string]string{"User-Agent": "curl/8.4.0"})
			if got := rec.Header().Get("Vary"); got != "User-Agent" {
				t.Errorf("Vary = %q, want User-Agent (status=%d)", got, rec.Code)
			}
		})
	}
}

// --- per-status max-age table ---------------------------------------------

func TestDNS_CacheControl(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name      string
		fr        *fakeResolver
		path      string
		wantCache string
	}{
		{"ok", &fakeResolver{recs: freshRecords()}, "/dns/example.com", "public, max-age=300"},
		{"notfound", &fakeResolver{err: resolver.ErrNotFound}, "/dns/example.com", "public, max-age=3600"},
		{"selfthrottled", &fakeResolver{err: resolver.ErrSelfThrottled}, "/dns/example.com", "public, max-age=60"},
		{"stale", &fakeResolver{recs: freshRecords(), err: resolver.ErrUnavailable}, "/dns/example.com", "public, max-age=60"},
		{"unavailable_502", &fakeResolver{err: resolver.ErrUnavailable}, "/dns/example.com", "no-store"},
		{"invalid_400", &fakeResolver{}, "/dns/foo.local", "no-store"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRouter(tc.fr)
			rec := do(r, http.MethodGet, tc.path, map[string]string{"User-Agent": "curl/8.4.0"})
			if got := rec.Header().Get("Cache-Control"); got != tc.wantCache {
				t.Errorf("Cache-Control = %q, want %q (status=%d)", got, tc.wantCache, rec.Code)
			}
		})
	}
}

// --- validation: leading underscore allowed -------------------------------

func TestDNS_LeadingUnderscoreAccepted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fr := &fakeResolver{recs: freshRecords()}
	r := newRouter(fr)
	rec := do(r, http.MethodGet, "/dns/_dmarc.example.com", map[string]string{
		"User-Agent": "curl/8.4.0",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (leading-underscore label must validate)", rec.Code)
	}
	if fr.calls != 1 {
		t.Errorf("resolver invoked %d times, want 1", fr.calls)
	}
	if fr.lastHost != "_dmarc.example.com" {
		t.Errorf("inner host = %q, want _dmarc.example.com", fr.lastHost)
	}
}

// --- validation: refused suffixes -----------------------------------------

func TestDNS_RefusedSuffixes400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	suffixes := []string{
		"foo.local",
		"foo.localhost",
		"foo.internal",
		"foo.lan",
		"foo.example",
		"foo.test",
		"foo.invalid",
		"foo.arpa",
	}
	for _, h := range suffixes {
		t.Run(h, func(t *testing.T) {
			fr := &fakeResolver{}
			r := newRouter(fr)
			rec := do(r, http.MethodGet, "/dns/"+h, map[string]string{"User-Agent": "curl/8.4.0"})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "invalid hostname") {
				t.Errorf("400 body missing 'invalid hostname'\n--- body ---\n%s", rec.Body.String())
			}
			if fr.calls != 0 {
				t.Errorf("resolver invoked %d times for refused suffix, want 0", fr.calls)
			}
		})
	}
}

// --- validation: IP literals -----------------------------------------------

func TestDNS_IPLiteralRejected400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []string{
		"1.1.1.1",
		"1.1.1.1.", // trailing-dot IP literal — IP-check happens AFTER trailing-dot trim
		"2606:4700::1111",
	}
	for _, h := range cases {
		t.Run(h, func(t *testing.T) {
			fr := &fakeResolver{}
			r := newRouter(fr)
			rec := do(r, http.MethodGet, "/dns/"+h, map[string]string{"User-Agent": "curl/8.4.0"})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s status = %d, want 400", h, rec.Code)
			}
			if fr.calls != 0 {
				t.Errorf("resolver invoked %d times for IP literal, want 0", fr.calls)
			}
		})
	}
}

// --- validation: all-numeric rightmost label ------------------------------

func TestDNS_AllNumericTLDRejected400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// `1.2.3` is not a valid IP, but its rightmost label `3` is all
	// digits — almost certainly a typo for an IPv4 fragment.
	fr := &fakeResolver{}
	r := newRouter(fr)
	rec := do(r, http.MethodGet, "/dns/1.2.3", map[string]string{"User-Agent": "curl/8.4.0"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if fr.calls != 0 {
		t.Errorf("resolver invoked %d times for all-numeric TLD, want 0", fr.calls)
	}
}

// --- canonicalization: lowercase + trailing-dot trim + idna ---------------

func TestDNS_IDNCanonicalize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		path string
		want string
	}{
		{"uppercase_trailing_dot", "/dns/Example.com.", "example.com"},
		{"unicode_idn", "/dns/exämple.com", "xn--exmple-cua.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeResolver{recs: freshRecords()}
			r := newRouter(fr)
			rec := do(r, http.MethodGet, tc.path, map[string]string{"User-Agent": "curl/8.4.0"})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 — body: %s", rec.Code, rec.Body.String())
			}
			if fr.lastHost != tc.want {
				t.Errorf("inner host = %q, want %q", fr.lastHost, tc.want)
			}
		})
	}
}

// --- headline: hostname-as-banner -----------------------------------------

// TestDNS_HeadlineHostname pins that the banner headline is the
// hostname itself (not a single first-label). Underscored leaves and
// bare TLDs both render without a 4xx/5xx; the precise figlet output
// is asserted via live smoke (banner glyphs land as path elements,
// not literal text in the SVG).
func TestDNS_HeadlineHostname(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		path string
	}{
		{"underscored_leaf", "/dns/_dmarc.example.com"},
		{"bare_tld", "/dns/com"},
		{"multilabel", "/dns/sub.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeResolver{recs: freshRecords()}
			r := newRouter(fr)
			rec := do(r, http.MethodGet, tc.path+"?format=svg", map[string]string{"User-Agent": "curl/8.4.0"})
			if rec.Code != http.StatusOK {
				t.Fatalf("%s status = %d, want 200 — body: %s", tc.path, rec.Code, rec.Body.String())
			}
			// Body must carry at least one labeled row from freshRecords;
			// proves the banner+caption pipeline didn't blow up on the
			// hostname-as-headline shape.
			if !strings.Contains(rec.Body.String(), "A       93.184.216.34") {
				t.Errorf("body missing A row for %s", tc.path)
			}
		})
	}
}

// --- format negotiation: PNG / SVG / ASCII / HTML+OG ----------------------

func TestDNS_FormatPNG_SVG_ASCII_HTML(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("png", func(t *testing.T) {
		r := newRouter(&fakeResolver{recs: freshRecords()})
		rec := do(r, http.MethodGet, "/dns/example.com", map[string]string{
			"User-Agent": "facebookexternalhit/1.1", // unfurl bot → PNG
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != "image/png" {
			t.Errorf("Content-Type = %q, want image/png", got)
		}
		if !bytes.HasPrefix(rec.Body.Bytes(), pngMagic) {
			t.Errorf("body does not start with PNG magic")
		}
	})

	t.Run("svg", func(t *testing.T) {
		r := newRouter(&fakeResolver{recs: freshRecords()})
		rec := do(r, http.MethodGet, "/dns/example.com?format=svg", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
			t.Errorf("Content-Type = %q, want image/svg+xml", got)
		}
		if !strings.Contains(rec.Body.String(), "<svg") {
			t.Errorf("SVG body missing <svg root\n--- body (first 200) ---\n%.200s", rec.Body.String())
		}
	})

	t.Run("ascii", func(t *testing.T) {
		r := newRouter(&fakeResolver{recs: freshRecords()})
		rec := do(r, http.MethodGet, "/dns/example.com", map[string]string{
			"User-Agent": "curl/8.4.0",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
			t.Errorf("Content-Type = %q, want text/plain prefix", got)
		}
	})

	t.Run("html_og", func(t *testing.T) {
		r := newRouter(&fakeResolver{recs: freshRecords()})
		rec := do(r, http.MethodGet, "/dns/example.com?format=html", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
			t.Errorf("Content-Type = %q, want text/html prefix", got)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `property="og:image"`) {
			t.Errorf("HTML body missing og:image\n--- body (first 400) ---\n%.400s", body)
		}
		if !strings.Contains(body, "format=png") {
			t.Errorf("HTML body missing og:image URL with format=png\n--- body (first 400) ---\n%.400s", body)
		}
		if !strings.Contains(body, `property="og:url"`) {
			t.Errorf("HTML body missing og:url\n--- body (first 400) ---\n%.400s", body)
		}
	})
}

// --- validation: max length -------------------------------------------------

func TestDNS_OverMaxLen400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// 254 chars > hostnameMaxLen (253). The route param can carry it
	// since `:hostname` segment matches everything that's not `/`.
	long := strings.Repeat("a", 250) + ".com" // 254 chars
	fr := &fakeResolver{}
	r := newRouter(fr)
	rec := do(r, http.MethodGet, "/dns/"+long, map[string]string{"User-Agent": "curl/8.4.0"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if fr.calls != 0 {
		t.Errorf("resolver invoked %d times for over-length host, want 0", fr.calls)
	}
}

// --- RateLimitedHandler shim ------------------------------------------------

// TestRateLimitedHandler pins the iplimit short-circuit shape: 200 +
// max-age=60 + "try again later" caption (distinct from
// renderSelfThrottled which is 503).
func TestRateLimitedHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ClientDetector())
	r.GET("/blocked", dns.RateLimitedHandler)

	rec := do(r, http.MethodGet, "/blocked", map[string]string{"User-Agent": "curl/8.4.0"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (per-IP iplimit short-circuit returns 200)", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("Cache-Control = %q, want public, max-age=60", got)
	}
	if !strings.Contains(rec.Body.String(), "try again later") {
		t.Errorf("body missing 'try again later' caption\n--- body ---\n%s", rec.Body.String())
	}
}
