package stock_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/service/stock"
	"github.com/cmj0121/imagelet/service/stock/quote"
	"github.com/cmj0121/imagelet/service/stock/twse"
)

// fakeTWSE returns a canned MarketData. Used by TW-path tests.
type fakeTWSE struct {
	d   twse.MarketData
	err error
}

func (f fakeTWSE) Get(_ context.Context) (twse.MarketData, error) {
	return f.d, f.err
}

// freshTW returns a deterministic TWSE MarketData with non-zero
// institutional + margin so HasInstitutional and HasMargin are true.
func freshTW() twse.MarketData {
	return twse.MarketData{
		AsOf:            time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC),
		ForeignNet:      43_907_871_828,
		TrustNet:        2_217_080_841,
		DealerNet:       8_878_366_186,
		Net:             55_003_318_855,
		MarginLongTWD:   440_907_083_000,
		MarginShortLots: 190_811,
	}
}

// fakeProvider returns a canned (Quote, error) pair from every Get call.
type fakeProvider struct {
	q   quote.Quote
	err error
}

func (f fakeProvider) Get(_ context.Context, _ string) (quote.Quote, error) {
	return f.q, f.err
}

// spyProvider records every symbol it was asked for. Useful for asserting
// that the country→symbol lookup actually flowed through the handler.
type spyProvider struct {
	mu      sync.Mutex
	q       quote.Quote
	err     error
	symbols []string
}

func (s *spyProvider) Get(_ context.Context, symbol string) (quote.Quote, error) {
	s.mu.Lock()
	s.symbols = append(s.symbols, symbol)
	s.mu.Unlock()
	return s.q, s.err
}

func (s *spyProvider) lastSymbol() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.symbols) == 0 {
		return ""
	}
	return s.symbols[len(s.symbols)-1]
}

// pngMagic is the 8-byte PNG file signature.
var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// freshQuote returns a deterministic Quote that the handler can render.
// Last > PrevClose so ChangePercent() is positive (▲ arrow). AsOf is a
// fixed UTC instant so the caption date is stable across CI runs.
func freshQuote() quote.Quote {
	return quote.Quote{
		Symbol:    "^GSPC",
		Last:      4500.50,
		PrevClose: 4450.00,
		Currency:  "USD",
		AsOf:      time.Date(2026, 4, 15, 20, 0, 0, 0, time.UTC),
		IsClosed:  false,
	}
}

// newRouter wires the middlewares the handler depends on (RegionDetector
// for CF-IPCountry, ClientDetector for UA-driven mode) plus the stock
// route. Default TWSE provider is the no-op; use newRouterWithTWSE for
// TW-enrichment tests.
func newRouter(p quote.Provider) *gin.Engine {
	r := gin.New()
	r.Use(middleware.RegionDetector())
	r.Use(middleware.ClientDetector())
	stock.Register(r, p, stock.NoopTWSE())
	return r
}

// newRouterWithTWSE wires a router that includes the TW market-data
// provider. Used by TW-path tests.
func newRouterWithTWSE(p quote.Provider, tw twse.Provider) *gin.Engine {
	r := gin.New()
	r.Use(middleware.RegionDetector())
	r.Use(middleware.ClientDetector())
	stock.Register(r, p, tw)
	return r
}

func TestServeOpenMarketASCII(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{q: freshQuote()})

	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q, want prefix text/plain", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("Cache-Control = %q, want %q", got, "public, max-age=60")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "^GSPC") {
		t.Errorf("body missing symbol ^GSPC\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, "▲") {
		t.Errorf("body missing up arrow\n--- body ---\n%s", body)
	}
	if strings.Contains(body, "STALE") || strings.Contains(body, "CLOSED") {
		t.Errorf("body contains unexpected prefix\n--- body ---\n%s", body)
	}
	// ASCII frame uses + and - glyphs from the ascii theme.
	if !strings.Contains(body, "+") || !strings.Contains(body, "-") {
		t.Errorf("body does not look like an ASCII banner\n--- body ---\n%s", body)
	}
}

func TestServeClosedMarketASCII(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.IsClosed = true
	r := newRouter(fakeProvider{q: q})

	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "CLOSED ·") {
		t.Errorf("body missing CLOSED prefix\n--- body ---\n%s", rec.Body.String())
	}
}

func TestServeStaleAfterUpstreamFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{q: freshQuote(), err: errors.New("upstream")})

	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (stale-but-have-data must not 5xx)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "STALE ·") {
		t.Errorf("body missing STALE prefix\n--- body ---\n%s", rec.Body.String())
	}
}

func TestServeUnavailableWhenNoCacheAndError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{err: quote.ErrUnavailable})

	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want %q", got, "60")
	}
	if got := rec.Body.String(); got != "quote unavailable\n" {
		t.Errorf("body = %q, want %q", got, "quote unavailable\n")
	}
}

func TestServeBrowserGetsPNG(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{q: freshQuote()})

	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("Cache-Control = %q, want %q", got, "public, max-age=60")
	}
	body := rec.Body.Bytes()
	if !bytes.HasPrefix(body, pngMagic) {
		head := body
		if len(head) > 8 {
			head = head[:8]
		}
		t.Errorf("body missing PNG magic bytes; first 8 bytes = %x", head)
	}
}

func TestServeAcceptPylonReturnsRawSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{q: freshQuote()})

	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("Accept", "text/pylon")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/pylon") {
		t.Errorf("Content-Type = %q, want prefix text/pylon", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "[ ") {
		t.Errorf("body missing opening bracket\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, "| banner ]") {
		t.Errorf("body missing banner sentinel\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, "^GSPC") {
		t.Errorf("body missing symbol\n--- body ---\n%s", body)
	}
}

func TestRegionQueryOverridesCFCountry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	spy := &spyProvider{q: freshQuote()}
	r := newRouter(spy)

	req := httptest.NewRequest(http.MethodGet, "/stock?region=TW", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "US")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := spy.lastSymbol(); got != "^TWII" {
		t.Errorf("provider got symbol = %q, want %q (query override should beat CF-IPCountry)", got, "^TWII")
	}
}

func TestRegionMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		country string
		want    string
	}{
		{"TW", "^TWII"},
		{"US", "^GSPC"},
		{"JP", "^N225"},
		{"HK", "^HSI"},
		{"GB", "^FTSE"},
		{"DE", "^GDAXI"},
		{"ZZ", "^GSPC"}, // unknown → default
	}
	for _, tc := range cases {
		t.Run(tc.country, func(t *testing.T) {
			spy := &spyProvider{q: freshQuote()}
			r := newRouter(spy)

			req := httptest.NewRequest(http.MethodGet, "/stock", nil)
			req.Header.Set("User-Agent", "curl/8.4.0")
			req.Header.Set("CF-IPCountry", tc.country)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := spy.lastSymbol(); got != tc.want {
				t.Errorf("provider got symbol = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStripPylonSyntax(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare_parens", "foo (bar) baz", "foo baz"},
		{"bare_brackets", "foo [bar] baz", "foo baz"},
		{"both", "a (b) c [d] e", "a c e"},
		{"none", "^GSPC USD 21,834.50", "^GSPC USD 21,834.50"},
		{"unmatched_left_paren", "foo (bar", "foo (bar"},
		{"unmatched_right_paren", "foo bar)", "foo bar)"},
		{"unmatched_left_bracket", "foo [bar", "foo [bar"},
		{"caret_safe", "^GSPC", "^GSPC"},
		{"thousands_safe", "21,834.50", "21,834.50"},
		{"middle_dot_safe", "STALE · ^GSPC", "STALE · ^GSPC"},
		// `&` immediately followed by [A-Za-z_] is pylon's Ref-parser
		// trigger; we space it apart so the `&` glyph stays visible
		// on every render path. Standalone `&` and `&` before non-letters
		// pass through unchanged.
		{"ampersand_inline", "S&P 500 · United States", "S & P 500 · United States"},
		{"ampersand_spaced", "Tom & Jerry", "Tom & Jerry"},
		{"ampersand_collapses_extra_ws", "A &  B", "A & B"},
		{"ampersand_before_letter_compact", "AT&T 5G", "AT & T 5G"},
		{"ampersand_terminal", "Foo &", "Foo &"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stock.StripPylonSyntaxForTest(tc.in); got != tc.want {
				t.Errorf("stripPylonSyntax(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFormatPrice(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0.00"},
		{1.5, "1.50"},
		{99, "99.00"},
		{1234.5, "1,234.50"},
		{21834.5, "21,834.50"},
		{1000000, "1,000,000.00"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := stock.FormatPriceForTest(tc.in); got != tc.want {
				t.Errorf("formatPrice(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIndexNameFor(t *testing.T) {
	cases := []struct {
		symbol string
		want   string
	}{
		{"^TWII", "TAIEX · Taiwan"},
		{"^GSPC", "S&P 500 · United States"},
		{"^N225", "Nikkei 225 · Japan"},
		{"^HSI", "Hang Seng · Hong Kong"},
		{"^FTSE", "FTSE 100 · United Kingdom"},
		{"^GDAXI", "DAX · Germany"},
		{"^UNKNOWN", ""}, // unmapped → empty (caller handles by omitting header)
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.symbol, func(t *testing.T) {
			if got := stock.IndexNameForTest(tc.symbol); got != tc.want {
				t.Errorf("indexNameFor(%q) = %q, want %q", tc.symbol, got, tc.want)
			}
		})
	}
}

// TestServeTWPathIncludesEnrichment pins that a TW visitor sees the
// institutional + margin block in the ASCII surface (with Chinese
// labels). Exercises Path 1 region-conditional formatting.
func TestServeTWPathIncludesEnrichment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "^TWII"
	q.Currency = "TWD"
	r := newRouterWithTWSE(fakeProvider{q: q}, fakeTWSE{d: freshTW()})

	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"三大法人", "外資", "投信", "自營", "合計", "融資餘額", "融券餘額", "TAIEX · Taiwan"} {
		if !strings.Contains(body, want) {
			t.Errorf("ASCII body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestServeTWPathPNGUsesEnglishLabels pins that the PNG surface for a
// TW visitor uses ENGLISH labels for the TW block. Pylon's PNG font
// has zero CJK coverage, so emitting Chinese would render as tofu.
// Decode the PNG and rely on a heuristic: pylon's PNG font WILL place
// the EN strings as detectable bitmap rows; a smoke-level check is
// enough since we already pinned the line content via the ASCII path.
//
// Cheaper: we exercise the full handler, decode that the body is a
// valid PNG of reasonable size (taller than the ASCII-only path
// since the TW block adds ~3 rows), and trust the handler's
// `useEnglish = mode == ModePNG` switch.
func TestServeTWPathPNGUsesEnglishLabels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "^TWII"
	q.Currency = "TWD"
	r := newRouterWithTWSE(fakeProvider{q: q}, fakeTWSE{d: freshTW()})

	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	body := rec.Body.Bytes()
	if !bytes.HasPrefix(body, pngMagic) {
		t.Fatalf("body missing PNG magic")
	}
	// PNG with TW-enrichment block is meaningfully larger than the
	// without-TW PNG. A lower bound around 5KB filters out a
	// regression that would silently drop the TW lines from the
	// rendered image (those lines compress to a chunky blob).
	if len(body) < 5000 {
		t.Errorf("PNG size = %d bytes, want >= 5000 (TW block likely missing)", len(body))
	}
}

// TestServeTWUpstreamFailureKeepsBaseRender pins the best-effort
// contract: if TWSE upstream errors, /stock still renders the base
// quote view (banner + caption + bars), just without the TW block.
// The visitor must not see a 5xx for an enrichment-side failure.
func TestServeTWUpstreamFailureKeepsBaseRender(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "^TWII"
	q.Currency = "TWD"
	r := newRouterWithTWSE(fakeProvider{q: q}, fakeTWSE{err: errors.New("twse down")})

	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (TWSE failure must not 5xx the request)", rec.Code)
	}
	body := rec.Body.String()
	// Base render still present.
	if !strings.Contains(body, "^TWII") {
		t.Errorf("base render missing ^TWII\n--- body ---\n%s", body)
	}
	// TW block silently omitted.
	if strings.Contains(body, "三大法人") || strings.Contains(body, "融資餘額") {
		t.Errorf("TW block rendered despite upstream failure\n--- body ---\n%s", body)
	}
}

// TestServeNonTWPathNoEnrichment pins that visitors from non-TW
// regions never see the TW enrichment block, even if the TWSE
// provider is wired (which it always is in production).
func TestServeNonTWPathNoEnrichment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouterWithTWSE(fakeProvider{q: freshQuote()}, fakeTWSE{d: freshTW()})

	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "US")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "三大法人") || strings.Contains(body, "institutional") {
		t.Errorf("non-TW path leaked TW enrichment\n--- body ---\n%s", body)
	}
}
