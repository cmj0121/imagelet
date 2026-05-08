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

	"github.com/cmj0121/imagelet/internal/i18n"
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
// for CF-IPCountry, LocaleDetector for ?lang=/CF-IPCountry-driven locale,
// ClientDetector for UA-driven mode) plus the stock route. Default TWSE
// provider is the no-op; use newRouterWithTWSE for TW-enrichment tests.
//
// LocaleDetector is installed AFTER RegionDetector so the CF-IPCountry-
// based fallback step sees the country code. Tests that need a specific
// locale set ?lang=… or CF-IPCountry: TW on the individual request —
// not via a global default — so each test self-documents its locale
// assumption.
func newRouter(p quote.Provider) *gin.Engine {
	r := gin.New()
	r.Use(middleware.TimezoneDetector())
	r.Use(middleware.DateOverrideDetector())
	r.Use(middleware.RegionDetector())
	r.Use(i18n.LocaleDetector())
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
	r.Use(i18n.LocaleDetector())
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
	mu              sync.Mutex
	live            twse.LiveBreadth
	dataLive        twse.MarketData
	dataHist        twse.MarketData
	perStock        map[string]twse.StockData
	perStockErr     error
	holders         map[string]twse.HoldersDistribution
	holdersErr      error
	blockTrades      map[string][]twse.BlockTrade
	blockTradesErr   error
	fundamentals     map[string]twse.Fundamentals
	fundamentalsErr  error
	listingInfo         map[string]twse.ListingInfo
	listingInfoErr      error
	otcListingInfo      map[string]twse.ListingInfo
	otcListingInfoErr   error
	foreign             map[string]twse.Foreign
	foreignErr          error
	industryForeign     map[string]twse.IndustryForeign
	industryForeignErr  error
	revenue             map[string]twse.Revenue
	revenueErr          error
	otcRevenue          map[string]twse.Revenue
	otcRevenueErr       error
	getAtCall           int
	liveCall            int
	getCall             int
	perStockCall        int
	holdersCall         int
	blockTradesCall     int
	fundamentalsCall    int
	listingInfoCall     int
	otcListingInfoCall  int
	foreignCall         int
	industryForeignCall int
	revenueCall         int
	otcRevenueCall      int
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

// GetHoldersDistribution implements twse.HoldersProvider so the
// /stock/:symbol handler can pull TDCC dispersion through the same
// fake. Empty map → ErrUnavailable for all stocks (matches the
// expected behaviour for tests that don't pre-populate holders).
func (p *histTWSE) GetHoldersDistribution(_ context.Context, stockID string, _ time.Time) (twse.HoldersDistribution, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.holdersCall++
	if p.holdersErr != nil {
		return twse.HoldersDistribution{}, p.holdersErr
	}
	d, ok := p.holders[stockID]
	if !ok {
		return twse.HoldersDistribution{}, twse.ErrUnavailable
	}
	return d, nil
}

// GetBlockTrades implements twse.BlockTradesProvider. Empty map →
// nil-slice + nil-error (matches "no block trades for this stock"
// production behaviour, which the renderer treats as "no row").
func (p *histTWSE) GetBlockTrades(_ context.Context, stockID string, _ time.Time) (twse.BlockTradesDay, []twse.BlockTrade, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blockTradesCall++
	if p.blockTradesErr != nil {
		return twse.BlockTradesDay{}, nil, p.blockTradesErr
	}
	return twse.BlockTradesDay{}, p.blockTrades[stockID], nil
}

// GetFundamentals implements twse.FundamentalsProvider. Missing stock
// → ErrUnavailable (matches "stock not in dump" production behaviour).
func (p *histTWSE) GetFundamentals(_ context.Context, stockID string, _ time.Time) (twse.Fundamentals, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fundamentalsCall++
	if p.fundamentalsErr != nil {
		return twse.Fundamentals{}, p.fundamentalsErr
	}
	f, ok := p.fundamentals[stockID]
	if !ok {
		return twse.Fundamentals{}, twse.ErrUnavailable
	}
	return f, nil
}

// GetListingInfo implements twse.ListingInfoProvider.
func (p *histTWSE) GetListingInfo(_ context.Context, stockID string, _ time.Time) (twse.ListingInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.listingInfoCall++
	if p.listingInfoErr != nil {
		return twse.ListingInfo{}, p.listingInfoErr
	}
	li, ok := p.listingInfo[stockID]
	if !ok {
		return twse.ListingInfo{}, twse.ErrUnavailable
	}
	return li, nil
}

// GetForeign implements twse.ForeignProvider.
func (p *histTWSE) GetForeign(_ context.Context, stockID string, _ time.Time) (twse.Foreign, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.foreignCall++
	if p.foreignErr != nil {
		return twse.Foreign{}, p.foreignErr
	}
	f, ok := p.foreign[stockID]
	if !ok {
		return twse.Foreign{}, twse.ErrUnavailable
	}
	return f, nil
}

// GetOTCListingInfo implements twse.OTCListingInfoProvider.
func (p *histTWSE) GetOTCListingInfo(_ context.Context, stockID string, _ time.Time) (twse.ListingInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.otcListingInfoCall++
	if p.otcListingInfoErr != nil {
		return twse.ListingInfo{}, p.otcListingInfoErr
	}
	li, ok := p.otcListingInfo[stockID]
	if !ok {
		return twse.ListingInfo{}, twse.ErrUnavailable
	}
	return li, nil
}

// GetIndustryForeign implements twse.IndustryForeignProvider.
func (p *histTWSE) GetIndustryForeign(_ context.Context, industry string, _ time.Time) (twse.IndustryForeign, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.industryForeignCall++
	if p.industryForeignErr != nil {
		return twse.IndustryForeign{}, p.industryForeignErr
	}
	f, ok := p.industryForeign[industry]
	if !ok {
		return twse.IndustryForeign{}, twse.ErrUnavailable
	}
	return f, nil
}

// GetRevenue implements twse.RevenueProvider.
func (p *histTWSE) GetRevenue(_ context.Context, stockID string, _ time.Time) (twse.Revenue, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.revenueCall++
	if p.revenueErr != nil {
		return twse.Revenue{}, p.revenueErr
	}
	r, ok := p.revenue[stockID]
	if !ok {
		return twse.Revenue{}, twse.ErrUnavailable
	}
	return r, nil
}

// GetOTCRevenue implements twse.OTCRevenueProvider.
func (p *histTWSE) GetOTCRevenue(_ context.Context, stockID string, _ time.Time) (twse.Revenue, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.otcRevenueCall++
	if p.otcRevenueErr != nil {
		return twse.Revenue{}, p.otcRevenueErr
	}
	r, ok := p.otcRevenue[stockID]
	if !ok {
		return twse.Revenue{}, twse.ErrUnavailable
	}
	return r, nil
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
		name      string
		symbol    string
		shortName string
		longName  string
		want      string
	}{
		{"name_present_uses_short", "2330.TW", "台積電", "Taiwan Semiconductor", "2330.TW · 台積電"},
		{"falls_back_to_long_when_short_missing", "AAPL", "", "Apple Inc.", "AAPL · Apple Inc."},
		{"both_missing_returns_symbol_only", "FOO", "", "", "FOO"},
		{"index_with_short_name", "^GSPC", "S&P 500", "S&P 500", "^GSPC · S&P 500"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := quote.Quote{Symbol: tc.symbol, Name: tc.shortName, LongName: tc.longName}
			if got := stock.TitleForTest(tc.symbol, q); got != tc.want {
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
// Render path: SVG → embedded Sarasa Mono SC subset (see render/png.go),
// which carries full CJK coverage. There is no separate font path for
// PNG vs SVG, so the PNG body inherits the same CN labels the SVG path
// pins via TestServeTWPathSVGUsesChineseLabels.
//
// Cheaper than glyph-extracting the bitmap: we exercise the full
// handler, confirm the body is a valid PNG of meaningful size, and
// rely on the SVG-label test to pin the actual label content.
func TestServeTWPathPNGRendersTWBlock(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "^TWII"
	q.Currency = "TWD"
	r := newRouterWithTWSE(fakeProvider{q: q}, fakeTWSE{d: freshTW()})

	// Browser UA default is now HTML — the PNG path is reached via
	// explicit ?format=png.
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
// TW enrichment is asserted; PNG inherits the same labels via the
// SVG → Sarasa Mono SC raster path (see TestServeTWPathPNGRendersTWBlock).
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

	// OHLC bar (Yahoo intra-day source) must still render. The `O`
	// marker stays at today's open offset, but the `C` glyph and its
	// price string are suppressed because the close hasn't finalized
	// — the OCP row now carries `收: -` (zh-TW close label) in place
	// of a live price.
	if !strings.Contains(body, "O") {
		t.Errorf("open-market body missing OHLC `O` marker\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, "收: -") {
		t.Errorf("open-market body should suppress close price as `收: -`\n--- body ---\n%s", body)
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

// TestServeSymbolTWEnrichmentBySuffix pins the two-axis gate on the TW
// enrichment block: (1) the symbol must be TW (`.TW`/`.TWO`/`^TWII`),
// and (2) the resolved locale must NOT be en (en strips TWSE rows
// entirely — see service/stock's showTWSEEnrichment policy). Tests
// set CF-IPCountry explicitly so the locale assumption is part of
// each row's intent.
func TestServeSymbolTWEnrichmentBySuffix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name        string
		path        string
		country     string // drives both region routing and locale fallback
		wantTWCalls int
	}{
		// TW symbols + TW visitor → enrichment.
		{"tse_suffix_tw_visitor", "/stock/2330.tw", "TW", 1},
		{"otc_suffix_tw_visitor", "/stock/5274.two", "TW", 1},
		{"taiex_index_tw_visitor", "/stock/%5ETWII", "TW", 1},
		// TW symbols + en visitor → enrichment stripped (locale gate).
		{"tse_suffix_en_visitor", "/stock/2330.tw", "US", 0},
		{"otc_suffix_en_visitor", "/stock/5274.two", "JP", 0},
		{"taiex_index_en_visitor", "/stock/%5ETWII", "DE", 0},
		// Non-TW symbols → no enrichment regardless of locale.
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
	// Per-stock T86 rows are gated behind the TWSE enrichment block,
	// which en strips. Set CF-IPCountry: TW so the locale resolves to
	// zh-TW and the rows render.
	req.Header.Set("CF-IPCountry", "TW")
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

// TestServeSymbolPerStockUpstreamMissOmitsBlock pins the post-Tier-3
// behaviour: when T86 has no row for a per-stock view (delisted /
// OTC-only / 9999), the 三大法人 group is OMITTED entirely rather
// than falling through to the market-wide TSE-aggregate numbers
// labelled identically. The previous fallback was misleading on
// OTC stocks (6488.TWO would show +46.4B labelled "外資籌碼" — the
// TSE-wide total, not the per-stock figure).
func TestServeSymbolPerStockUpstreamMissOmitsBlock(t *testing.T) {
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
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// The market-wide TSE-aggregate +43.9B foreign net MUST NOT
	// appear on a per-stock view that has no per-stock data — that
	// was the misleading-fallback bug. Per-stock card omits the
	// group entirely.
	if strings.Contains(body, "+43.9B") {
		t.Errorf("market-wide fallback unexpectedly rendered on per-stock view\n--- body ---\n%s", body)
	}
	// Sanity: the rest of the card still renders.
	if !strings.Contains(body, "9999.TW") {
		t.Errorf("symbol missing — card collapsed entirely")
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

// fakeHoldersDist returns a deterministic HoldersDistribution
// resembling TSMC: tier 15 (>1M shares) holds 1,497 accounts owning
// 85.58% of the float, total 2,519,187 holders. The handler tests use
// it to assert the rendered 大戶 / 總戶數 lines without depending on
// the pinned TDCC fixture (kept under service/stock/twse/testdata).
func fakeHoldersDist() twse.HoldersDistribution {
	d := twse.HoldersDistribution{
		StockID:    "2330",
		AsOf:       time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		TotalCount: 2519187,
		TotalShare: 25932524521,
	}
	// Tier 14 (800k-1M shares): 224 accounts, 0.77% of float.
	d.Tiers[13] = twse.HoldersTier{Count: 224, Share: 201422294, Pct: 0.77}
	// Tier 15 (>1M shares): 1497 accounts, 85.58% of float.
	d.Tiers[14] = twse.HoldersTier{Count: 1497, Share: 22193101818, Pct: 85.58}
	return d
}

// TestServeHoldersRendersZhTW pins that the holders rows render
// correctly on a per-stock TW path with zh-TW locale: 大戶 line shows
// the tier 14+15 bucket count + percentages, 總戶數 line shows the
// tier 17 total. Numbers carry thousands-separator commas.
func TestServeHoldersRendersZhTW(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		dataLive: freshTW(),
		holders: map[string]twse.HoldersDistribution{
			"2330": fakeHoldersDist(),
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if tw.holdersCall != 1 {
		t.Errorf("holdersCall = %d, want 1", tw.holdersCall)
	}
	body := rec.Body.String()
	for _, want := range []string{
		// 大戶 line: 224+1497 = 1,721 accounts, 0.07% accounts, 86.35% shares
		"大戶",
		"1,721 戶",
		"持股",
		// 總戶數 line: 2,519,187 (with commas)
		"總戶數",
		"2,519,187",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestServeHoldersStripsOnEn pins that the en locale strips the
// holders rows entirely — the showTWSEEnrichment(loc) gate at
// buildBlocks short-circuits the whole TW block before holdersRows is
// called. Verifies the existing locale-by-gate idiom still applies.
func TestServeHoldersStripsOnEn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		holders: map[string]twse.HoldersDistribution{
			"2330": fakeHoldersDist(),
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	// Default locale fallback for an unset CF-IPCountry is en.
	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, unwanted := range []string{"大戶", "大户", "總戶數", "总户数"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("en body unexpectedly contains %q (TWSE strip broken)\n--- body ---\n%s", unwanted, body)
		}
	}
}

// TestServeHoldersStripsOnStaleDate pins the staleness gate: when
// ?date= pins an OHLC bar more than 14 days off the dump's AsOf, the
// holders rows are suppressed even though the upstream returned a
// valid distribution.
func TestServeHoldersStripsOnStaleDate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	// histProvider satisfies HistoricalProvider so the ?date= path
	// resolves; fakeProvider would error with ErrUnavailable on GetAt.
	p := &histProvider{live: q, hist: q}
	tw := &histTWSE{
		dataHist: freshTW(),
		holders: map[string]twse.HoldersDistribution{
			"2330": fakeHoldersDist(), // dump.AsOf = 2026-04-30
		},
	}
	r := newRouterWithTWSE(p, tw)

	// ?date=2026-01-15 is ~3 months before the dump's AsOf — well past
	// the 14-day staleness window.
	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw?date=2026-01-15", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, unwanted := range []string{"大戶", "總戶數"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("stale-date body unexpectedly contains %q (staleness gate broken)\n--- body ---\n%s", unwanted, body)
		}
	}
}

// TestServeHoldersOmitsWhenNoData pins that an upstream returning
// ErrUnavailable for the holders fetch leaves the rest of the card
// intact and just drops the holders rows.
func TestServeHoldersOmitsWhenNoData(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		dataLive: freshTW(),
		// holders map is nil → all stocks return ErrUnavailable
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, unwanted := range []string{"大戶", "總戶數"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("no-data body unexpectedly contains %q\n--- body ---\n%s", unwanted, body)
		}
	}
	// The rest of the card still renders.
	if !strings.Contains(body, "2330.TW") {
		t.Errorf("symbol missing from body — card collapsed entirely")
	}
}

// fakeHoldersDistWithPrev returns the same shape as fakeHoldersDist
// but with prior-week fields populated so the renderer emits the Δ
// pill on the 大戶 line. Tier 14+15 share went from 22.0B → 22.394B
// (current = 22193101818 + 201422294, prev = 22B), totals same: drift
// = (22.394/25.933) - (22.0/25.933) ≈ 1.52pp upward.
func fakeHoldersDistWithPrev() twse.HoldersDistribution {
	d := fakeHoldersDist()
	d.PrevAsOf = time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	d.PrevTotalCount = 2519187
	d.PrevTotalShare = 25932524521
	// Prev tier 14: same. Prev tier 15: 22.0B (was 22.193B current).
	d.PrevTiers[13] = twse.HoldersTier{Count: 224, Share: 201422294, Pct: 0.77}
	d.PrevTiers[14] = twse.HoldersTier{Count: 1497, Share: 22_000_000_000, Pct: 84.83}
	return d
}

// TestServeHoldersRendersDeltaPill pins the WoW drift pill on the
// 大戶 line. Current concentration = 86.36%, prev = 85.62% → Δ ≈
// +0.74pp → ▲ pill. Verify the body carries ▲ + the magnitude.
func TestServeHoldersRendersDeltaPill(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		dataLive: freshTW(),
		holders: map[string]twse.HoldersDistribution{
			"2330": fakeHoldersDistWithPrev(),
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "▲") {
		t.Errorf("body missing ▲ pill\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, "pp") {
		t.Errorf("body missing pp suffix on Δ pill\n--- body ---\n%s", body)
	}
}

// TestServeHoldersOmitsPillOnColdStart pins the warm-up window: a
// distribution without Prev* fields renders the 大戶 line WITHOUT a
// pill. ≈ alone (the no-drift token) is acceptable, but ▲/▼ + pp
// must be absent — the pill should appear only when there's a
// prior-week baseline to diff against.
func TestServeHoldersOmitsPillOnColdStart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		dataLive: freshTW(),
		holders: map[string]twse.HoldersDistribution{
			"2330": fakeHoldersDist(), // no Prev* — cold start
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "pp") {
		t.Errorf("cold-start body unexpectedly contains pp Δ suffix\n--- body ---\n%s", body)
	}
	// Sanity: 大戶 line still renders, just without the pill suffix.
	if !strings.Contains(body, "大戶") {
		t.Errorf("大戶 missing — holders rows should still render without pill\n--- body ---\n%s", body)
	}
}

// TestServeBlockTradesRendersZhTW pins that a stock with one or more
// block trades on the latest BFIAUU snapshot renders the 大宗交易 row
// with count + 張 + 億 aggregates. Tests the "single trade" case for
// 2330 with a simple verifiable aggregate.
func TestServeBlockTradesRendersZhTW(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		dataLive: freshTW(),
		blockTrades: map[string][]twse.BlockTrade{
			// Two events: 1.5M shares (1500 張) at 2200 TWD = 3.3B TWD = 33 億
			//             0.5M shares (500 張) at 2210 TWD = 1.105B TWD ≈ 11.05 億
			// Aggregates: 2 筆, 2,000 張, 44.05 億
			"2330": {
				{StockID: "2330", TradePrice: 2200.0, TradeVolume: 1_500_000, TradeValue: 3_300_000_000},
				{StockID: "2330", TradePrice: 2210.0, TradeVolume: 500_000, TradeValue: 1_105_000_000},
			},
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"大宗交易", "2 筆", "2,000 張", "44.05 億"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestServeBlockTradesOmitsRowWhenEmpty pins that a stock with no
// block trades renders the rest of the card normally without any
// 大宗交易 line — most days, most stocks fall in this branch.
func TestServeBlockTradesOmitsRowWhenEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		dataLive:    freshTW(),
		blockTrades: map[string][]twse.BlockTrade{}, // 2330 not present → nil slice
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "大宗交易") {
		t.Errorf("empty-trades body unexpectedly contains 大宗交易\n--- body ---\n%s", body)
	}
}

// TestServeFundamentalsRendersZhTW pins the 殖利率 / PER / PBR row
// for a stock with all three metrics populated (TSMC live shape).
func TestServeFundamentalsRendersZhTW(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		dataLive: freshTW(),
		fundamentals: map[string]twse.Fundamentals{
			"2330": {
				StockID:       "2330",
				Name:          "台積電",
				DividendYield: 0.98,
				PERatio:       33.97,
				PBRatio:       10.77,
			},
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"殖利率 0.98%", "PER 33.97", "PBR 10.77"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestServeFundamentalsSkipsZeroSegments pins the per-segment skip
// path: a non-dividend-paying stock (DividendYield == 0) renders the
// row with PER + PBR but without the 殖利率 prefix. Same for a
// loss-maker with PER == 0.
func TestServeFundamentalsSkipsZeroSegments(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		dataLive: freshTW(),
		fundamentals: map[string]twse.Fundamentals{
			"2330": {
				StockID:       "2330",
				Name:          "台積電",
				DividendYield: 0,    // no dividend
				PERatio:       0,    // loss-maker
				PBRatio:       2.34, // book value still publishable
			},
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "PBR 2.34") {
		t.Errorf("body missing PBR 2.34\n--- body ---\n%s", body)
	}
	if strings.Contains(body, "殖利率") {
		t.Errorf("殖利率 unexpectedly rendered when DividendYield == 0\n--- body ---\n%s", body)
	}
	if strings.Contains(body, "PER") {
		t.Errorf("PER unexpectedly rendered when PERatio == 0\n--- body ---\n%s", body)
	}
}

// TestServeContextRowRendersZhTW pins the per-stock context row with
// all three signals (sector + listing year + foreign holdings).
func TestServeContextRowRendersZhTW(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		dataLive: freshTW(),
		listingInfo: map[string]twse.ListingInfo{
			"2330": {
				StockID:      "2330",
				Name:         "台積電",
				IndustryCode: "24",
				IndustryName: "半導體業",
				ListingDate:  time.Date(1994, 9, 5, 0, 0, 0, 0, time.UTC),
			},
		},
		foreign: map[string]twse.Foreign{
			"2330": {
				StockID:    "2330",
				Name:       "台積電",
				HoldingPct: 70.65,
			},
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"半導體業", "上市 1994", "外資持股 70.65%"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestServeContextRowSkipsMissingSegments pins per-segment skip:
// listing-info-only or foreign-only stocks render the row with just
// the available segment(s).
func TestServeContextRowSkipsMissingSegments(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		dataLive: freshTW(),
		// Only foreign data — no listing-info (e.g. ETF case).
		foreign: map[string]twse.Foreign{
			"2330": {StockID: "2330", HoldingPct: 70.65},
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "外資持股 70.65%") {
		t.Errorf("body missing foreign segment\n--- body ---\n%s", body)
	}
	if strings.Contains(body, "半導體業") {
		t.Errorf("body unexpectedly contains 半導體業 when listing-info absent\n--- body ---\n%s", body)
	}
	if strings.Contains(body, "上市 ") {
		t.Errorf("body unexpectedly contains 上市 prefix when listing-info absent\n--- body ---\n%s", body)
	}
}

// TestServeContextRowOmitsRowWhenAllAbsent pins the all-empty path:
// row is omitted entirely (other rows still render).
func TestServeContextRowOmitsRowWhenAllAbsent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{dataLive: freshTW()}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "外資持股") || strings.Contains(body, "上市 ") {
		t.Errorf("context row unexpectedly rendered when all upstreams empty\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, "2330.TW") {
		t.Errorf("symbol missing — card collapsed entirely")
	}
}

// TestServeContextRowWithIndustryOverlay pins the 業均 segment: when
// per-stock 外資持股 is present AND the industry-foreign overlay is
// available, the row gets a fourth segment.
func TestServeContextRowWithIndustryOverlay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		dataLive: freshTW(),
		listingInfo: map[string]twse.ListingInfo{
			"2330": {
				StockID:      "2330",
				Name:         "台積電",
				IndustryCode: "24",
				IndustryName: "半導體業",
				ListingDate:  time.Date(1994, 9, 5, 0, 0, 0, 0, time.UTC),
			},
		},
		foreign: map[string]twse.Foreign{
			"2330": {StockID: "2330", HoldingPct: 70.65},
		},
		industryForeign: map[string]twse.IndustryForeign{
			"半導體業": {Industry: "半導體業", HoldingPct: 43.10},
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"半導體業", "外資持股 70.65%", "業均 43.10%"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestServeContextRowSkipsIndustryOverlayWhenNoForeign pins that
// 業均 is hidden when the per-stock 外資持股 segment is absent (the
// comparison is meaningless without a comparand).
func TestServeContextRowSkipsIndustryOverlayWhenNoForeign(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		dataLive: freshTW(),
		listingInfo: map[string]twse.ListingInfo{
			"2330": {StockID: "2330", IndustryName: "半導體業"},
		},
		// No per-stock foreign data; industry data is present but should be hidden.
		industryForeign: map[string]twse.IndustryForeign{
			"半導體業": {Industry: "半導體業", HoldingPct: 43.10},
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "業均") {
		t.Errorf("業均 unexpectedly rendered without per-stock foreign\n--- body ---\n%s", body)
	}
}

// TestServeRevenueRowRendersZhTW pins the monthly revenue row.
func TestServeRevenueRowRendersZhTW(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		dataLive: freshTW(),
		revenue: map[string]twse.Revenue{
			"2330": {
				StockID:    "2330",
				Name:       "台積電",
				Industry:   "半導體業",
				YearMonth:  "11503",
				CurrentTWD: 415_191_699_000,
				YoYPct:     45.19,
				MoMPct:     30.70,
			},
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"2026/03", "月營收", "4151.92 億", "▲45.19%"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestServeRevenueRowNegativeYoY pins the ▼ branch for negative YoY.
func TestServeRevenueRowNegativeYoY(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "2330.TW"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		dataLive: freshTW(),
		revenue: map[string]twse.Revenue{
			"2330": {
				StockID:    "2330",
				YearMonth:  "11503",
				CurrentTWD: 100_000_000_000,
				YoYPct:     -8.5,
			},
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)
	req := httptest.NewRequest(http.MethodGet, "/stock/2330.tw", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "▼8.50%") {
		t.Errorf("body missing ▼8.50%% for negative YoY\n--- body ---\n%s", body)
	}
}

// TestServeOTCRoutesToTPEx pins that .TWO symbols hit the OTC
// listing-info + revenue providers, NOT the TWSE-listed ones.
func TestServeOTCRoutesToTPEx(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "6488.TWO"
	q.Currency = "TWD"
	q.IsClosed = true
	tw := &histTWSE{
		dataLive: freshTW(),
		// Note: listingInfo (TWSE) is empty for 6488 — TPEx-only.
		otcListingInfo: map[string]twse.ListingInfo{
			"6488": {
				StockID:      "6488",
				Name:         "環球晶",
				IndustryCode: "24",
				IndustryName: "半導體業",
				ListingDate:  time.Date(2015, 9, 25, 0, 0, 0, 0, time.UTC),
			},
		},
		otcRevenue: map[string]twse.Revenue{
			"6488": {
				StockID:    "6488",
				YearMonth:  "11503",
				CurrentTWD: 5_445_917_000,
				YoYPct:     0.09,
			},
		},
	}
	r := newRouterWithTWSE(fakeProvider{q: q}, tw)

	req := httptest.NewRequest(http.MethodGet, "/stock/6488.two", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"半導體業", "上市 2015", "月營收"} {
		if !strings.Contains(body, want) {
			t.Errorf("OTC body missing %q\n--- body ---\n%s", want, body)
		}
	}
	if tw.otcListingInfoCall == 0 {
		t.Errorf("otcListingInfoCall = 0 — expected OTC route to invoke OTC provider")
	}
	if tw.listingInfoCall != 0 {
		t.Errorf("listingInfoCall = %d — TWSE provider invoked for .TWO symbol", tw.listingInfoCall)
	}
}

// TestEnCatalogContextFieldsEmpty pins the catalog invariant: en must
// leave TWSEListingPrefix and TWSEForeignHolding empty so the
// defence-in-depth check inside contextRow() short-circuits cleanly.
func TestEnCatalogContextFieldsEmpty(t *testing.T) {
	cat := i18n.For(i18n.LocaleEN)
	if cat.TWSEListingPrefix != "" {
		t.Errorf("en TWSEListingPrefix = %q, want empty", cat.TWSEListingPrefix)
	}
	if cat.TWSEForeignHolding != "" {
		t.Errorf("en TWSEForeignHolding = %q, want empty", cat.TWSEForeignHolding)
	}
}

// TestEnCatalogHoldersFieldsEmpty pins the catalog invariant: en
// must leave the new TWSE Holders fields empty, matching the existing
// pattern. Catches a future contributor accidentally populating en
// values that would then leak into PNG / SVG renders.
func TestEnCatalogHoldersFieldsEmpty(t *testing.T) {
	cat := i18n.For(i18n.LocaleEN)
	for name, got := range map[string]string{
		"TWSEHoldersBig":  cat.TWSEHoldersBig,
		"TWSEHoldersAll":  cat.TWSEHoldersAll,
		"TWSEHoldersHold": cat.TWSEHoldersHold,
		"TWSEHoldersUnit": cat.TWSEHoldersUnit,
	} {
		if got != "" {
			t.Errorf("en catalog %s = %q, want empty", name, got)
		}
	}
}
