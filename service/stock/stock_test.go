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

// fakeLiveTWSE extends fakeTWSE with a canned LiveBreadth so the
// handler's open-market path can be exercised. The live counts are
// independent of fakeTWSE.d so tests can assert that the breadth row
// reflects the live snapshot, not the daily MI_INDEX numbers.
type fakeLiveTWSE struct {
	fakeTWSE
	live    twse.LiveBreadth
	liveErr error
}

func (f fakeLiveTWSE) FetchLiveBreadth(_ context.Context) (twse.LiveBreadth, error) {
	return f.live, f.liveErr
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
		MarginShortTWD:  12_345_678_000,
		MarginLongLots:  1_502_330,
		MarginShortLots: 190_811,
		AdvanceCount:    312,
		DeclineCount:    691,
		UnchangedCount:  63,
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
// fixed UTC instant so the caption date is stable across CI runs. OHLC
// is populated (Open=4460, DayLow=4440, DayHigh=4520, Close=Last=4500.50)
// so HasOHLC() is true and the renderer emits the OHLC range bar — a
// bullish layout with body filled between Open and Close. Name /
// LongName populate the title row so handler tests can assert the
// `<symbol> · <name>` shape without monkey-patching the upstream.
func freshQuote() quote.Quote {
	return quote.Quote{
		Symbol:    "^GSPC",
		Name:      "S&P 500",
		LongName:  "S&P 500 INDEX",
		Last:      4500.50,
		Open:      4460.00,
		PrevClose: 4450.00,
		Currency:  "USD",
		AsOf:      time.Date(2026, 4, 15, 20, 0, 0, 0, time.UTC),
		IsClosed:  false,
		DayHigh:   4520.00,
		DayLow:    4440.00,
		Volume:    3_179_035_000,
	}
}

// newRouter wires the middlewares the handler depends on (RegionDetector
// for CF-IPCountry, ClientDetector for UA-driven mode) plus the stock
// route. Default TWSE provider is the no-op; use newRouterWithTWSE for
// TW-enrichment tests.
func newRouter(p quote.Provider) *gin.Engine {
	r := gin.New()
	r.Use(middleware.TimezoneDetector())
	r.Use(middleware.DateOverrideDetector())
	r.Use(middleware.RegionDetector())
	r.Use(middleware.ClientDetector())
	stock.Register(r, p, stock.NoopTWSE())
	return r
}

// newRouterWithTWSE wires a router that includes the TW market-data
// provider. Used by TW-path tests.
func newRouterWithTWSE(p quote.Provider, tw twse.Provider) *gin.Engine {
	r := gin.New()
	r.Use(middleware.TimezoneDetector())
	r.Use(middleware.DateOverrideDetector())
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
	// OHLC range bar is enabled when the quote carries the full OHLC
	// quartet (freshQuote does). The bar glyphs (O, C, █ for bullish)
	// land in the ASCII surface; the OCP data row carries `O: <open>`
	// to pin the open value alongside C (current close) and P (prev).
	for _, want := range []string{"O", "C", "█", "O: 4,460.00"} {
		if !strings.Contains(body, want) {
			t.Errorf("ASCII body missing OHLC token %q\n--- body ---\n%s", want, body)
		}
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

// histProvider satisfies both quote.Provider and quote.HistoricalProvider
// so the /stock handler picks the GetAt path under ?date= override.
type histProvider struct {
	live quote.Quote
	hist quote.Quote
	err  error

	mu        sync.Mutex
	asOfSeen  time.Time
	getAtCall int
}

func (p *histProvider) Get(_ context.Context, _ string) (quote.Quote, error) {
	return p.live, p.err
}

func (p *histProvider) GetAt(_ context.Context, _ string, asOf time.Time) (quote.Quote, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asOfSeen = asOf
	p.getAtCall++
	return p.hist, p.err
}

// histTWSE satisfies twse.Provider + twse.HistoricalProvider +
// twse.LiveBreadthProvider + twse.PerStockProvider so the handler can
// route across all four branches deterministically.
type histTWSE struct {
	mu           sync.Mutex
	live         twse.LiveBreadth
	dataLive     twse.MarketData
	dataHist     twse.MarketData
	perStock     map[string]twse.StockData
	perStockErr  error
	getAtCall    int
	liveCall     int
	getCall      int
	perStockCall int
}

func (p *histTWSE) Get(_ context.Context) (twse.MarketData, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.getCall++
	return p.dataLive, nil
}

func (p *histTWSE) GetAt(_ context.Context, _ time.Time) (twse.MarketData, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.getAtCall++
	return p.dataHist, nil
}

func (p *histTWSE) FetchLiveBreadth(_ context.Context) (twse.LiveBreadth, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.liveCall++
	return p.live, nil
}

func (p *histTWSE) GetForStock(_ context.Context, stockID string, _ time.Time) (twse.StockData, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.perStockCall++
	if p.perStockErr != nil {
		return twse.StockData{}, p.perStockErr
	}
	d, ok := p.perStock[stockID]
	if !ok {
		return twse.StockData{}, twse.ErrUnavailable
	}
	return d, nil
}

// TestServeDateOverrideUsesGetAt pins the contract that ?date=YYYY-MM-DD
// routes the handler through the HistoricalProvider path with the parsed
// date as asOf — and that the historical Quote (not the live one) lands
// in the rendered output.
func TestServeDateOverrideUsesGetAt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	live := freshQuote()
	live.Last = 9999.99
	hist := freshQuote()
	hist.Last = 1300.42
	hist.AsOf = time.Date(2012, 2, 2, 16, 0, 0, 0, time.UTC)
	hist.IsClosed = true

	p := &histProvider{live: live, hist: hist}
	r := newRouter(p)

	req := httptest.NewRequest(http.MethodGet, "/stock?date=2012-02-02", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if p.getAtCall != 1 {
		t.Errorf("GetAt called %d times, want 1", p.getAtCall)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "1,300.42") {
		t.Errorf("body missing historical Last 1,300.42\n--- body ---\n%s", body)
	}
	if strings.Contains(body, "9,999.99") {
		t.Errorf("body contains live Last 9,999.99 — historical override not honored\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, "2012-02-02") {
		t.Errorf("body missing historical AsOf 2012-02-02\n--- body ---\n%s", body)
	}
	// asOf passed to GetAt must reflect the parsed date.
	if y := p.asOfSeen.Year(); y != 2012 {
		t.Errorf("asOf seen = %v, want year=2012", p.asOfSeen)
	}
}

// TestServeDateOverrideTWPath pins that on the TW path with override:
// (a) twse.GetAt is hit, (b) twse.Get and FetchLiveBreadth are NOT,
// (c) the rendered body shows historical TW enrichment rows.
func TestServeDateOverrideTWPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	live := freshQuote()
	live.IsClosed = false // open market by default
	hist := freshQuote()
	hist.IsClosed = true
	hist.AsOf = time.Date(2024, 1, 10, 16, 0, 0, 0, time.UTC)
	hist.Symbol = "^TWII"
	hist.Currency = "TWD"

	p := &histProvider{live: live, hist: hist}
	tw := &histTWSE{
		dataLive: freshTW(),
		dataHist: freshTW(),
		live: twse.LiveBreadth{
			TSEAdvance: 1, TSEDecline: 2, TSEUnchanged: 3,
			OTCAdvance: 4, OTCDecline: 5, OTCUnchanged: 6,
		},
	}
	r := newRouterWithTWSE(p, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock?date=2024-01-10", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if tw.getAtCall != 1 {
		t.Errorf("twse.GetAt calls = %d, want 1", tw.getAtCall)
	}
	if tw.getCall != 0 {
		t.Errorf("twse.Get calls = %d, want 0 (override should bypass live Get)", tw.getCall)
	}
	if tw.liveCall != 0 {
		t.Errorf("twse.FetchLiveBreadth calls = %d, want 0 (live breadth must be skipped on historical lookup)", tw.liveCall)
	}
	body := rec.Body.String()
	// Historical render uses the closed-market enrichment block, so
	// 漲跌家數 (closed breadth label) appears, and 上市/上櫃 (live labels)
	// do not.
	if !strings.Contains(body, "漲跌家數") {
		t.Errorf("body missing closed-market 漲跌家數 row\n--- body ---\n%s", body)
	}
	if strings.Contains(body, "上市") || strings.Contains(body, "上櫃") {
		t.Errorf("body has live-breadth labels on historical lookup\n--- body ---\n%s", body)
	}
}

// TestServeDateOverrideInvalidFallsThrough pins that an unparseable
// ?date= silently falls through to the live path — no 4xx.
func TestServeDateOverrideInvalidFallsThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	live := freshQuote()
	hist := freshQuote()
	hist.Last = 11.22
	p := &histProvider{live: live, hist: hist}
	r := newRouter(p)

	req := httptest.NewRequest(http.MethodGet, "/stock?date=garbage", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if p.getAtCall != 0 {
		t.Errorf("GetAt called %d times, want 0 on invalid date", p.getAtCall)
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

func TestServeBrowserGetsHTML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{q: freshQuote()})

	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("Cache-Control = %q, want %q", got, "public, max-age=60")
	}
	body := rec.Body.String()
	for _, sub := range []string{"<!DOCTYPE html>", "<svg ", "</svg>", "</html>"} {
		if !strings.Contains(body, sub) {
			t.Errorf("HTML body missing %q\n--- body ---\n%s", sub, body)
		}
	}
}

// TestServeBrowserHTMLHasDateNav pins the date-nav post-processor wiring
// into the HTML response path. The handler must inject the prev/next
// chevron DOM, the help dialog, and the inline behavior <script> on top
// of the BannerBoxes-wrapped body — so /stock browsers get the same
// keyboard navigation surface as the standalone WrapHTMLWithDateNav
// variant exposes for /now and friends.
func TestServeBrowserHTMLHasDateNav(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{q: freshQuote()})

	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
	body := rec.Body.String()
	for _, sub := range []string{"<script>", `data-imagelet-nav="-1"`, `id="imagelet-help"`} {
		if !strings.Contains(body, sub) {
			t.Errorf("HTML body missing date-nav fragment %q\n--- body ---\n%s", sub, body)
		}
	}
}

func TestServeFormatPNG(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{q: freshQuote()})

	req := httptest.NewRequest(http.MethodGet, "/stock?format=png", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
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

func TestServeFormatSVGOverridesUA(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{q: freshQuote()})

	for _, ua := range []string{"curl/8.4.0", "Mozilla/5.0"} {
		t.Run(ua, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/stock?format=svg", nil)
			req.Header.Set("User-Agent", ua)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
				t.Errorf("Content-Type = %q, want image/svg+xml", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60" {
				t.Errorf("Cache-Control = %q, want public, max-age=60", got)
			}
			body := rec.Body.String()
			if !strings.Contains(body, `xmlns="http://www.w3.org/2000/svg"`) {
				t.Errorf("body missing xmlns; got:\n%s", body)
			}
			if !strings.Contains(body, "<svg ") || !strings.Contains(body, "</svg>") {
				t.Errorf("body not bracketed by <svg> tags; got:\n%s", body)
			}
			// OHLC bar tokens survive pylon's SVG path. Pylon coalesces
			// adjacent same-glyph runs into <rect> blocks (the body
			// fill) and packs surrounding text into multi-char <text>
			// elements, so the markers and bottom labels show up as
			// substrings rather than isolated nodes. The price-centered
			// design no longer emits session low/high edge labels.
			for _, want := range []string{"O", "C", "O: 4,460.00"} {
				if !strings.Contains(body, want) {
					t.Errorf("SVG body missing OHLC token %q\n--- body ---\n%s", want, body)
				}
			}
		})
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

func TestTitleFor(t *testing.T) {
	cases := []struct {
		name       string
		symbol     string
		shortName  string
		longName   string
		useEnglish bool
		want       string
	}{
		{"name_present_cn_surface", "2330.TW", "台積電", "Taiwan Semiconductor", false, "2330.TW · 台積電"},
		{"name_present_png_uses_long", "2330.TW", "台積電", "Taiwan Semiconductor", true, "2330.TW · Taiwan Semiconductor"},
		{"png_falls_back_to_short_when_long_missing", "2330.TW", "台積電", "", true, "2330.TW · 台積電"},
		{"cn_falls_back_to_long_when_short_missing", "AAPL", "", "Apple Inc.", false, "AAPL · Apple Inc."},
		{"both_missing_returns_symbol_only", "FOO", "", "", false, "FOO"},
		{"index_with_short_name", "^GSPC", "S&P 500", "S&P 500", false, "^GSPC · S&P 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := quote.Quote{Symbol: tc.symbol, Name: tc.shortName, LongName: tc.longName}
			if got := stock.TitleForTest(tc.symbol, q, tc.useEnglish); got != tc.want {
				t.Errorf("titleFor = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestServeTWPathIncludesEnrichment pins that a TW visitor sees the
// 籌碼面 (positioning) borderless captions and the 信用餘額 (credit)
// row in the ASCII surface, with Chinese labels. Exercises the closed-
// market path: 籌碼面 and 信用餘額 are TWSE end-of-day datasets, so they
// only render when q.IsClosed is true; during open hours those rows
// are gated off (covered by TestServeTWPathOpenMarketHidesEndOfDay).
func TestServeTWPathIncludesEnrichment(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "^TWII"
	q.Name = "加權指數"
	q.LongName = "TSEC weighted index"
	q.Currency = "TWD"
	q.IsClosed = true
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
	for _, want := range []string{
		// Title row carries `<symbol> · <shortName>`.
		"^TWII · 加權指數",
		// Positioning group: compound labels on each row
		"外資籌碼", "投信籌碼", "自營籌碼", "合計籌碼",
		// Breadth row: compound label + raw counts
		"漲跌家數", "漲 312", "跌 691", "平 63",
		// Credit group: compound label on the single row
		"信用餘額", "融資", "融券",
	} {
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

	// Browser UA default is now HTML — the PNG path is reached via
	// explicit ?format=png. Same EN-label rule still applies.
	req := httptest.NewRequest(http.MethodGet, "/stock?format=png", nil)
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

// TestServeTWPathSVGUsesChineseLabels pins the locale-consistency rule:
// SVG carries CN labels for TW visitors, matching the ASCII and
// text/pylon surfaces. Browsers supply CJK glyphs via font fallback,
// so SVG renders 籌碼面 / 外資 / 投信 / 自營 / 合計 / 信用餘額 /
// 融資 / 融券 without tofu. Pins the closed-market path so the full
// TW enrichment is asserted; PNG is the lone EN holdout
// (basicfont.Face7x13 has zero CJK coverage); see
// TestServeTWPathPNGUsesEnglishLabels.
func TestServeTWPathSVGUsesChineseLabels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "^TWII"
	q.Currency = "TWD"
	q.IsClosed = true
	r := newRouterWithTWSE(fakeProvider{q: q}, fakeTWSE{d: freshTW()})

	req := httptest.NewRequest(http.MethodGet, "/stock?format=svg", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", got)
	}
	body := rec.Body.String()
	for _, cn := range []string{
		"漲跌家數",
		"外資籌碼", "投信籌碼", "自營籌碼", "合計籌碼",
		"信用餘額", "融資", "融券",
	} {
		if !strings.Contains(body, cn) {
			t.Errorf("SVG body missing CN label %q\n--- body ---\n%s", cn, body)
		}
	}
	// EN labels MUST NOT leak into the SVG path now that locale drives
	// the label set instead of mode.
	for _, en := range []string{
		"breadth",
		"foreign", "trust", "dealer", "total",
		"balance",
	} {
		if strings.Contains(body, en) {
			t.Errorf("SVG body contains EN label %q (should be CN)\n--- body ---\n%s", en, body)
		}
	}
}

// TestServeTWPathOpenMarketHidesEndOfDay pins the open-market gating:
// every TW enrichment row sources from a TWSE `afterTrading/` endpoint
// that publishes once-per-day after close, so when q.IsClosed is false
// the provider's lookback returns yesterday's numbers. None of the
// three sections (positioning / breadth / credit) may render in that
// state — only the OHLC bar above remains, sourced from Yahoo's intra-
// day chart. Complement to TestServeTWPathIncludesEnrichment which
// pins the closed-market full-enrichment view.
func TestServeTWPathOpenMarketHidesEndOfDay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "^TWII"
	q.Currency = "TWD"
	q.IsClosed = false
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

	// OHLC bar (Yahoo intra-day source) must still render.
	for _, want := range []string{"O", "C"} {
		if !strings.Contains(body, want) {
			t.Errorf("open-market body missing OHLC token %q\n--- body ---\n%s", want, body)
		}
	}
	// All TW enrichment labels must be absent — every row sources from
	// an end-of-day TWSE endpoint.
	banned := []string{
		"外資籌碼", "投信籌碼", "自營籌碼", "合計籌碼",
		"漲跌家數",
		"信用餘額",
	}
	for _, label := range banned {
		if strings.Contains(body, label) {
			t.Errorf("open-market body should not contain end-of-day label %q\n--- body ---\n%s", label, body)
		}
	}
}

// TestServeTWPathOpenMarketRendersLiveBreadth pins the LiveBreadthProvider
// split-rendering path: when the wired TWSE provider implements
// LiveBreadthProvider and the market is open, the breadth section
// renders one row per populated exchange — 上市 from TSE counts, 上櫃
// from OTC counts. Closed-market labels (漲跌家數, 籌碼面, 信用餘額)
// must NOT appear. The OHLC bar above also stays.
func TestServeTWPathOpenMarketRendersLiveBreadth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "^TWII"
	q.Currency = "TWD"
	q.IsClosed = false
	tw := fakeLiveTWSE{
		fakeTWSE: fakeTWSE{d: freshTW()}, // d is irrelevant during open
		live: twse.LiveBreadth{
			TSEAdvance:   444,
			TSEDecline:   555,
			TSEUnchanged: 22,
			OTCAdvance:   333,
			OTCDecline:   222,
			OTCUnchanged: 11,
			AsOf:         time.Date(2026, 4, 28, 10, 30, 0, 0, time.UTC),
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	// Both 上市 and 上櫃 rows present with their live counts.
	for _, want := range []string{
		"上市", "漲 444", "跌 555", "平 22",
		"上櫃", "漲 333", "跌 222", "平 11",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("open-market body missing split-breadth token %q\n--- body ---\n%s", want, body)
		}
	}
	// Closed-market labels must not appear during open hours.
	for _, banned := range []string{"漲跌家數", "外資籌碼", "信用餘額"} {
		if strings.Contains(body, banned) {
			t.Errorf("open-market body should not contain end-of-day label %q\n--- body ---\n%s", banned, body)
		}
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
	if strings.Contains(body, "漲跌家數") ||
		strings.Contains(body, "外資籌碼") ||
		strings.Contains(body, "信用餘額") {
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
	for _, label := range []string{
		"漲跌家數", "breadth",
		"外資籌碼", "foreign",
		"信用餘額", "balance",
	} {
		if strings.Contains(body, label) {
			t.Errorf("non-TW path leaked TW enrichment %q\n--- body ---\n%s", label, body)
		}
	}
}

// --- /stock/:symbol tests --------------------------------------------------

// TestServeSymbolReachesProvider pins that /stock/:symbol forwards the
// uppercased path segment to the provider and renders the result. The
// spy records the symbol(s) it was asked for so we can assert path
// normalization (lowercase → upper).
func TestServeSymbolReachesProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name       string
		path       string
		wantSymbol string
	}{
		{"plain_us_stock", "/stock/aapl", "AAPL"},
		{"already_upper", "/stock/AAPL", "AAPL"},
		{"index_with_caret_encoded", "/stock/%5EGSPC", "^GSPC"},
		{"tw_stock_lowercase_suffix", "/stock/2330.tw", "2330.TW"},
		{"tpex_stock", "/stock/5274.two", "5274.TWO"},
		{"hyphen_class", "/stock/brk-b", "BRK-B"},
		{"fx_pair", "/stock/eurusd=x", "EURUSD=X"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &spyProvider{q: freshQuote()}
			r := newRouter(spy)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("User-Agent", "curl/8.4.0")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body=%q)", rec.Code, rec.Body.String())
			}
			if got := spy.lastSymbol(); got != tc.wantSymbol {
				t.Errorf("provider asked for %q, want %q", got, tc.wantSymbol)
			}
		})
	}
}

// TestServeSymbolInvalidReturns404 pins that a symbol matching gin's
// :symbol slot but failing the allowlist (non-permitted chars, or
// length > symbolMaxLen) returns 404 BEFORE any upstream call.
//
// Note: an empty path segment (`/stock/`) is intercepted by gin's
// RedirectTrailingSlash and 301'd back to `/stock`, so it never
// reaches validSymbol; the empty-string guard inside validSymbol is
// defensive belt-and-braces, not the primary defense.
func TestServeSymbolInvalidReturns404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		path string
	}{
		{"contains_space", "/stock/aa%20pl"},
		{"contains_quote", "/stock/aa%27pl"},
		{"contains_underscore", "/stock/aa_pl"},
		{"too_long", "/stock/abcdefghijklmnopq"}, // 17 chars
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &spyProvider{q: freshQuote()}
			r := newRouter(spy)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("User-Agent", "curl/8.4.0")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404 (body=%q)", rec.Code, rec.Body.String())
			}
			if spy.lastSymbol() != "" {
				t.Errorf("provider was called for invalid symbol %q", spy.lastSymbol())
			}
		})
	}
}

// TestServeSymbolTrailingSlashRedirects documents gin's default
// behavior: `/stock/` → 301 → `/stock`. We don't fight gin here —
// the redirect lands on the region-routed handler which behaves as
// before. Pinned so a future router change doesn't silently regress.
func TestServeSymbolTrailingSlashRedirects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	spy := &spyProvider{q: freshQuote()}
	r := newRouter(spy)

	req := httptest.NewRequest(http.MethodGet, "/stock/", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusMovedPermanently {
		t.Errorf("status = %d, want 301", rec.Code)
	}
}

// TestServeSymbolTWEnrichmentBySuffix pins that .TW / .TWO / ^TWII
// activate the TW enrichment block regardless of CF-IPCountry. The
// existing TW path-test verifies the rendered labels; here we just
// pin that the twse provider was called.
func TestServeSymbolTWEnrichmentBySuffix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name        string
		path        string
		country     string
		wantTWCalls int
	}{
		{"tse_suffix", "/stock/2330.tw", "US", 1},
		{"otc_suffix", "/stock/5274.two", "JP", 1},
		{"taiex_index", "/stock/%5ETWII", "DE", 1},
		{"non_tw_symbol_in_tw_country", "/stock/aapl", "TW", 0},
		{"index_us", "/stock/%5EGSPC", "TW", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := freshQuote()
			q.IsClosed = true // closed-market path so twse.Get is exercised
			tw := &histTWSE{dataLive: freshTW(), dataHist: freshTW()}
			r := newRouterWithTWSE(fakeProvider{q: q}, tw)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("User-Agent", "curl/8.4.0")
			req.Header.Set("CF-IPCountry", tc.country)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if tw.getCall != tc.wantTWCalls {
				t.Errorf("twse.Get calls = %d, want %d (path=%s, country=%s)",
					tw.getCall, tc.wantTWCalls, tc.path, tc.country)
			}
		})
	}
}

// TestServeSymbolPerStockOverlaysMarketWide pins the contract that a
// .TW symbol (e.g. /stock/2330.tw) on the closed-market path routes
// through twse.PerStockProvider — and the rendered 三大法人 rows
// reflect THAT stock's flow, not the market-wide BFI82U numbers.
func TestServeSymbolPerStockOverlaysMarketWide(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	q.Last = 1000.0 // round price → easy NTD conversion

	tw := &histTWSE{
		dataLive: freshTW(), // market-wide; should NOT show in per-stock body
		perStock: map[string]twse.StockData{
			"2330": {
				StockID:    "2330",
				Name:       "台積電",
				ForeignNet: 5_100_000, // 5.1M shares × 1000 NTD = 5.1B NTD
				TrustNet:   1_000_000, // 1.0M × 1000 = 1.0B
				DealerNet:  300_000,   // 0.3M × 1000 = 0.3B
				Net:        6_400_000, // 6.4M × 1000 = 6.4B
			},
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if tw.perStockCall != 1 {
		t.Errorf("PerStock calls = %d, want 1", tw.perStockCall)
	}
	body := rec.Body.String()
	// Per-stock row labels still 籌碼 (same shape as market-wide).
	for _, want := range []string{"外資籌碼", "投信籌碼", "自營籌碼", "合計籌碼"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing per-stock label %q\n--- body ---\n%s", want, body)
		}
	}
	// Per-stock NTD: foreign 5.1B; the market-wide fixture has 43.9B
	// in foreign, so seeing 5.1B confirms the per-stock path won.
	if !strings.Contains(body, "+5.1B") {
		t.Errorf("body missing per-stock foreign +5.1B\n--- body ---\n%s", body)
	}
	if strings.Contains(body, "+43.9B") {
		t.Errorf("body leaked market-wide foreign +43.9B on per-stock path\n--- body ---\n%s", body)
	}
}

// TestServeSymbolPerStockUpstreamMissFallsBack pins that a T86 miss
// (delisted, OTC-only, etc.) gracefully falls back to the market-wide
// positioning rows rather than dropping the entire 三大法人 group.
func TestServeSymbolPerStockUpstreamMissFallsBack(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "9999.TW"
	q.Currency = "TWD"
	q.IsClosed = true

	// Empty perStock map → GetForStock returns ErrUnavailable.
	tw := &histTWSE{dataLive: freshTW()}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/9999.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// Market-wide foreign +43.9B from freshTW should appear.
	if !strings.Contains(body, "+43.9B") {
		t.Errorf("body missing market-wide fallback +43.9B\n--- body ---\n%s", body)
	}
}

// TestServeSymbolDateOverride pins that the symbol route honors
// ?date= and routes through GetAt — same contract as /stock.
func TestServeSymbolDateOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	live := freshQuote()
	live.Last = 9999.99
	hist := freshQuote()
	hist.Last = 1300.42
	hist.AsOf = time.Date(2012, 2, 2, 16, 0, 0, 0, time.UTC)
	hist.IsClosed = true

	p := &histProvider{live: live, hist: hist}
	r := newRouter(p)

	req := httptest.NewRequest(http.MethodGet, "/stock/aapl?date=2012-02-02", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if p.getAtCall != 1 {
		t.Errorf("GetAt called %d times, want 1", p.getAtCall)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "1,300.42") {
		t.Errorf("body missing historical Last 1,300.42\n--- body ---\n%s", body)
	}
}

// TestServeSymbolUpstream503Surfaces pins that an upstream miss on
// the symbol path returns 503 with the same shape as /stock — keeps
// the contract uniform across the two routes.
func TestServeSymbolUpstream503Surfaces(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{err: quote.ErrUnavailable})

	req := httptest.NewRequest(http.MethodGet, "/stock/aapl", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want 60", got)
	}
}

// TestServeCacheControlVariesByMarketState pins the per-context
// Cache-Control TTL ladder consumed by the htmlcache middleware:
// live=60s, closed=300s, historical=86400s. The handler is the
// authoritative source for these values; htmlcache reads them
// off the response, so a regression here silently changes the
// in-process cache TTL too.
func TestServeCacheControlVariesByMarketState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	live := freshQuote()
	hist := freshQuote()
	hist.Last = 1300.42
	hist.AsOf = time.Date(2012, 2, 2, 16, 0, 0, 0, time.UTC)
	hist.IsClosed = true

	cases := []struct {
		name   string
		setup  func() (quote.Provider, string)
		wantCC string
	}{
		{
			name: "live",
			setup: func() (quote.Provider, string) {
				return fakeProvider{q: live}, "/stock"
			},
			wantCC: "public, max-age=60",
		},
		{
			name: "closed",
			setup: func() (quote.Provider, string) {
				closed := freshQuote()
				closed.IsClosed = true
				return fakeProvider{q: closed}, "/stock"
			},
			wantCC: "public, max-age=300",
		},
		{
			name: "historical",
			setup: func() (quote.Provider, string) {
				return &histProvider{live: live, hist: hist}, "/stock?date=2012-02-02"
			},
			wantCC: "public, max-age=86400",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, url := tc.setup()
			r := newRouter(p)
			req := httptest.NewRequest(http.MethodGet, url, nil)
			req.Header.Set("User-Agent", "curl/8.4.0")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.wantCC {
				t.Errorf("Cache-Control = %q, want %q", got, tc.wantCC)
			}
		})
	}
}

// TestRenderHasMAPositionBarWhenMAsPresent asserts the MA position bar
// and its numeric caption render when the upstream supplied MA5 / MA10.
// Strong-uptrend layout (Last > MA5 > MA10) so both arrows are ▲, the
// price marker sits to the right of both MA markers, and the trailing
// trend hint reads `5↗10` (golden-cross territory).
func TestRenderHasMAPositionBarWhenMAsPresent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.MA5 = 575.20
	q.MA10 = 568.40
	q.Last = 583.00
	r := newRouter(fakeProvider{q: q})

	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// The text-marker layout puts literal `M10` / `M5` glyphs on the
	// bar itself (self-identifying — no separate label row), and the
	// caption row combines value, arrow, and trend hint:
	// `M5: ▲<v> · M10: ▲<v> · 5↗10`.
	for _, want := range []string{"M10", "M5", "M5: ▲575.20", "M10: ▲568.40", "5↗10"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestRenderMATrendTokenBranches pins each branch of the trailing
// trend token in the MA caption: golden-cross (MA5 > MA10*1.001),
// death-cross (MA5 < MA10*0.999), and the within-noise-floor neutral
// case (MA5 ≈ MA10). The threshold is 0.1% — values inside ±0.1% of
// MA10 collapse to ≈ regardless of which side they fall on.
func TestRenderMATrendTokenBranches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name      string
		ma5, ma10 float64
		want      string
		banned    string
	}{
		{"golden_cross", 575.20, 568.40, "5↗10", "5↘10"},
		{"death_cross", 568.40, 575.20, "5↘10", "5↗10"},
		{"neutral_equal", 100.00, 100.00, "≈", "5↗10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := freshQuote()
			q.MA5 = tc.ma5
			q.MA10 = tc.ma10
			r := newRouter(fakeProvider{q: q})

			req := httptest.NewRequest(http.MethodGet, "/stock", nil)
			req.Header.Set("User-Agent", "curl/8.4.0")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.want) {
				t.Errorf("body missing trend token %q\n--- body ---\n%s", tc.want, body)
			}
			if tc.banned != "" && strings.Contains(body, tc.banned) {
				t.Errorf("body contains banned trend token %q\n--- body ---\n%s", tc.banned, body)
			}
		})
	}
}

// TestRenderMAReachesHTMLResponse pins that the MA position bar (and
// its caption) survives the HTML pipeline end-to-end: a Mozilla UA
// triggers the HTML response path, the `text/html` content type lands,
// and the MA caption substrings + position-bar / price-marker glyphs
// appear literally in the body. Negative half pins that the gating
// (skip when MA5 == 0) holds through the same pipeline so a regression
// can't quietly leak the MA row into responses where it shouldn't show.
func TestRenderMAReachesHTMLResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Run("present", func(t *testing.T) {
		q := freshQuote()
		q.MA5 = 575.20
		q.MA10 = 568.40
		q.Last = 583.00
		r := newRouter(fakeProvider{q: q})

		req := httptest.NewRequest(http.MethodGet, "/stock", nil)
		req.Header.Set("User-Agent", "Mozilla/5.0")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
		}
		body := rec.Body.String()
		// The MA bar uses literal `M10` / `M5` text markers and a
		// combined caption (`M5: ▲<v> · M10: ▲<v> · <trend>`).
		for _, want := range []string{"M10", "M5", "M10: ▲", "M5: ▲", "C"} {
			if !strings.Contains(body, want) {
				t.Errorf("HTML body missing %q\n--- body ---\n%s", want, body)
			}
		}
	})
	t.Run("absent_when_ma5_zero", func(t *testing.T) {
		q := freshQuote()
		q.MA5 = 0
		q.MA10 = 568.40
		r := newRouter(fakeProvider{q: q})

		req := httptest.NewRequest(http.MethodGet, "/stock", nil)
		req.Header.Set("User-Agent", "Mozilla/5.0")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
		}
		body := rec.Body.String()
		for _, banned := range []string{"M10:", "M5:"} {
			if strings.Contains(body, banned) {
				t.Errorf("HTML body unexpectedly contains MA caption %q (gating regression)\n--- body ---\n%s", banned, body)
			}
		}
	})
}

// TestRenderSkipsMARowWhenInsufficientHistory asserts MA5 == MA10 == 0
// (the upstream's "not enough closed-session history" signal) collapses
// the row entirely. The `M10` / `M5` text markers are unique to the MA
// bar — the OHLC bar uses ─, O, and C — so their absence is a clean
// signal that the MA row was skipped without misclassifying the OHLC
// bar.
func TestRenderSkipsMARowWhenInsufficientHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.MA5 = 0
	q.MA10 = 0
	r := newRouter(fakeProvider{q: q})

	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, unwanted := range []string{"M10:", "M5:", "5↗10", "5↘10"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("body unexpectedly contains %q\n--- body ---\n%s", unwanted, body)
		}
	}
}
