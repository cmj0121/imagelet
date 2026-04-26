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
)

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
// route. Mirrors what server.New() will do in Unit 5, scoped to the
// service under test.
func newRouter(p quote.Provider) *gin.Engine {
	r := gin.New()
	r.Use(middleware.RegionDetector())
	r.Use(middleware.ClientDetector())
	stock.Register(r, p)
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
