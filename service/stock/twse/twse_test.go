package twse_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmj0121/imagelet/service/stock/twse"
)

// fixtureServer serves the requested testdata file based on URL path.
// Routes:
//
//	/BFI82U → testdata/bfi82u_open.json
//	/MIMARGN → testdata/mi_margn_open.json
//	/MIINDEX → testdata/mi_index_open.json
//
// Sleep introduces an artificial delay so concurrent-Get tests can
// exercise singleflight.
func fixtureServer(t *testing.T, sleep time.Duration) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var name string
		switch {
		case strings.Contains(r.URL.Path, "BFI82U"):
			name = "bfi82u_open.json"
		case strings.Contains(r.URL.Path, "MIMARGN"):
			name = "mi_margn_open.json"
		case strings.Contains(r.URL.Path, "MIINDEX"):
			name = "mi_index_open.json"
		default:
			http.NotFound(w, r)
			return
		}
		body, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatalf("fixture read: %v", err)
		}
		if sleep > 0 {
			time.Sleep(sleep)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

func newProvider(srv *httptest.Server) *twse.HTTPProvider {
	return twse.NewWithEndpoints(
		srv.URL+"/BFI82U?d=%s",
		srv.URL+"/MIMARGN?d=%s",
		srv.URL+"/MIINDEX?d=%s",
		srv.Client(),
	)
}

func TestGetMergesBothEndpoints(t *testing.T) {
	srv := fixtureServer(t, 0)
	defer srv.Close()
	p := newProvider(srv)
	d, err := p.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !d.HasInstitutional() {
		t.Errorf("HasInstitutional = false, want true (fixture has BFI82U data)")
	}
	if !d.HasMargin() {
		t.Errorf("HasMargin = false, want true (fixture has MI_MARGN data)")
	}
	if d.AsOf.IsZero() {
		t.Errorf("AsOf is zero -- date parsing failed")
	}
	// Fixture: BFI82U 合計 row buy/sell delta = 55,003,318,855 NTD.
	if d.Net != 55003318855 {
		t.Errorf("Net = %d, want 55003318855", d.Net)
	}
	// Foreign + dealer + trust should sum to Net (TWSE's invariant).
	if got := d.ForeignNet + d.TrustNet + d.DealerNet; got != d.Net {
		t.Errorf("Foreign+Trust+Dealer = %d, Net = %d (TWSE: 合計 should equal sum of categories)", got, d.Net)
	}
	// Fixture: 融資金額 今日餘額 = 440,907,083 千元 → 440,907,083,000 NTD.
	if d.MarginLongTWD != 440907083000 {
		t.Errorf("MarginLongTWD = %d, want 440907083000", d.MarginLongTWD)
	}
	// Fixture: 融券金額 今日餘額 = 12,345,678 千元 → 12,345,678,000 NTD.
	if d.MarginShortTWD != 12345678000 {
		t.Errorf("MarginShortTWD = %d, want 12345678000", d.MarginShortTWD)
	}
	// Fixture: 融資(交易單位) 今日餘額 = 8,566,954 張.
	if d.MarginLongLots != 8566954 {
		t.Errorf("MarginLongLots = %d, want 8566954", d.MarginLongLots)
	}
	// Fixture: 融券(交易單位) 今日餘額 = 190,811 張.
	if d.MarginShortLots != 190811 {
		t.Errorf("MarginShortLots = %d, want 190811", d.MarginShortLots)
	}
	// RetailBullBearScore: (8566954 - 190811) / (8566954 + 190811)
	// ≈ +0.9564, well above zero — TW retail margin is structurally
	// long-biased, score reading is meaningful as drift-from-baseline.
	if score := d.RetailBullBearScore(); score < 0.95 || score > 0.96 {
		t.Errorf("RetailBullBearScore = %.4f, want ≈0.9564", score)
	}
	// MI_INDEX 漲跌證券數合計 (股票 column): 上漲 312, 下跌 691, 持平 63.
	// Limit-up/down sub-counts inside `(...)` are stripped during parsing.
	if !d.HasBreadth() {
		t.Errorf("HasBreadth = false, want true (fixture has MI_INDEX data)")
	}
	if d.AdvanceCount != 312 {
		t.Errorf("AdvanceCount = %d, want 312", d.AdvanceCount)
	}
	if d.DeclineCount != 691 {
		t.Errorf("DeclineCount = %d, want 691", d.DeclineCount)
	}
	if d.UnchangedCount != 63 {
		t.Errorf("UnchangedCount = %d, want 63", d.UnchangedCount)
	}
	// BreadthScore = (312 - 691) / (312 + 691) ≈ -0.378.
	// Negative reading: more decliners than advancers — bearish day.
	if score := d.BreadthScore(); score > -0.37 || score < -0.39 {
		t.Errorf("BreadthScore = %.3f, want ≈-0.378", score)
	}
}

func TestGetUnavailableWhenStatNotOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"stat":"很抱歉，沒有符合條件的資料!","data":[]}`))
	}))
	defer srv.Close()
	p := newProvider(srv)
	_, err := p.Get(context.Background())
	if !errors.Is(err, twse.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestGetTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	p := newProvider(srv)
	_, err := p.Get(context.Background())
	if err == nil {
		t.Fatal("err = nil, want transport error")
	}
	if errors.Is(err, twse.ErrUnavailable) {
		t.Errorf("err = ErrUnavailable, want transport error (5xx is not stat-empty)")
	}
}

func TestCachedHitWithinTTL(t *testing.T) {
	var calls int32
	stub := stubProvider{getFunc: func(_ context.Context) (twse.MarketData, error) {
		atomic.AddInt32(&calls, 1)
		return twse.MarketData{Net: 1234567890}, nil
	}}
	c := twse.NewCachedWithTTL(stub, 5*time.Second, 1*time.Second)
	for i := 0; i < 3; i++ {
		d, err := c.Get(context.Background())
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if d.Net != 1234567890 {
			t.Errorf("Net = %d, want 1234567890", d.Net)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("upstream calls = %d, want 1 (within successTTL, should hit cache)", got)
	}
}

func TestCachedSingleflight(t *testing.T) {
	var calls int32
	stub := stubProvider{getFunc: func(_ context.Context) (twse.MarketData, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(50 * time.Millisecond)
		return twse.MarketData{Net: 999}, nil
	}}
	c := twse.NewCachedWithTTL(stub, 1*time.Second, 1*time.Second)
	const goroutines = 30
	done := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			_, err := c.Get(context.Background())
			done <- err
		}()
	}
	for i := 0; i < goroutines; i++ {
		if err := <-done; err != nil {
			t.Errorf("Get: %v", err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("upstream calls = %d, want 1 (singleflight should dedup concurrent Gets)", got)
	}
}

func TestCachedStaleWithDataOnFailure(t *testing.T) {
	var failing atomic.Bool
	stub := stubProvider{getFunc: func(_ context.Context) (twse.MarketData, error) {
		if failing.Load() {
			return twse.MarketData{}, errors.New("upstream down")
		}
		return twse.MarketData{Net: 555}, nil
	}}
	c := twse.NewCachedWithTTL(stub, 10*time.Millisecond, 1*time.Second)

	// Prime the cache with a successful fetch.
	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("priming Get: %v", err)
	}

	// Wait past successTTL so the next Get refreshes.
	time.Sleep(20 * time.Millisecond)
	failing.Store(true)

	d, err := c.Get(context.Background())
	if err == nil {
		t.Fatal("err = nil, want upstream-down err alongside stale data")
	}
	if d.Net != 555 {
		t.Errorf("Net = %d, want 555 (cached value should be returned with err)", d.Net)
	}
}

func TestParseTWSENumberStripsCommas(t *testing.T) {
	// Indirect: route a real fixture through Get and assert the parsed
	// numbers come out as raw int64. parseTWSENumber is unexported -- the
	// fixture roundtrip is the public test surface.
	srv := fixtureServer(t, 0)
	defer srv.Close()
	d, err := newProvider(srv).Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// 55,003,318,855 has commas in upstream JSON; if parsing left the
	// commas in, ParseInt would fail and the field would stay 0.
	if d.Net == 0 {
		t.Errorf("Net = 0; expected commas to be stripped during parse")
	}
}

// TestGetAtWalksBackFromAsOf pins that GetAt starts the lookback at the
// provided asOf date rather than time.Now(). Use a Wednesday asOf so no
// weekend skip interferes with the first probe.
func TestGetAtWalksBackFromAsOf(t *testing.T) {
	var seenDates []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture the date= query, route by path the way fixtureServer does.
		if v := r.URL.Query().Get("d"); v != "" {
			seenDates = append(seenDates, v)
		}
		var name string
		switch {
		case strings.Contains(r.URL.Path, "BFI82U"):
			name = "bfi82u_open.json"
		case strings.Contains(r.URL.Path, "MIMARGN"):
			name = "mi_margn_open.json"
		case strings.Contains(r.URL.Path, "MIINDEX"):
			name = "mi_index_open.json"
		default:
			http.NotFound(w, r)
			return
		}
		body, _ := os.ReadFile(filepath.Join("testdata", name))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := newProvider(srv)
	loc, _ := time.LoadLocation("Asia/Taipei")
	asOf := time.Date(2024, 1, 10, 12, 0, 0, 0, loc) // Wednesday — no weekend skip
	_, err := p.GetAt(context.Background(), asOf)
	if err != nil {
		t.Fatalf("GetAt: %v", err)
	}
	if len(seenDates) == 0 {
		t.Fatalf("upstream not hit")
	}
	if seenDates[0] != "20240110" {
		t.Errorf("first probe date = %q, want 20240110", seenDates[0])
	}
}

// TestGetAtFutureClampedToToday pins that a future-dated asOf does not
// loop the upstream against impossible dates — clamped to today.
func TestGetAtFutureClampedToToday(t *testing.T) {
	var seenDates []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v := r.URL.Query().Get("d"); v != "" {
			seenDates = append(seenDates, v)
		}
		var name string
		switch {
		case strings.Contains(r.URL.Path, "BFI82U"):
			name = "bfi82u_open.json"
		case strings.Contains(r.URL.Path, "MIMARGN"):
			name = "mi_margn_open.json"
		case strings.Contains(r.URL.Path, "MIINDEX"):
			name = "mi_index_open.json"
		default:
			http.NotFound(w, r)
			return
		}
		body, _ := os.ReadFile(filepath.Join("testdata", name))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := newProvider(srv)
	asOf := time.Now().Add(365 * 24 * time.Hour)
	_, err := p.GetAt(context.Background(), asOf)
	if err != nil {
		t.Fatalf("GetAt: %v", err)
	}
	if len(seenDates) == 0 {
		t.Fatalf("upstream not hit")
	}
	// First seen date must be today's (in TW), not 1 year hence.
	twNow := time.Now().In(time.FixedZone("Asia/Taipei", 8*3600)).Format("20060102")
	// Allow ±1 day to handle test running at midnight boundary.
	if !strings.HasPrefix(seenDates[0], twNow[:6]) {
		t.Errorf("first probe %q does not look clamped to today (~%s)", seenDates[0], twNow)
	}
}

// TestCachedGetAtBypassesCache pins that historical lookups skip the
// in-memory single-entry cache (which holds the live aggregate) and
// delegate to the inner provider's HistoricalProvider.
func TestCachedGetAtBypassesCache(t *testing.T) {
	var calls int32
	stub := histStub{
		getFunc: func(_ context.Context) (twse.MarketData, error) {
			return twse.MarketData{Net: 111}, nil
		},
		getAtFunc: func(_ context.Context, _ time.Time) (twse.MarketData, error) {
			atomic.AddInt32(&calls, 1)
			return twse.MarketData{Net: 222}, nil
		},
	}
	c := twse.NewCachedWithTTL(stub, time.Hour, time.Hour)

	// Prime cache with live Get → cached Net=111.
	if _, err := c.Get(context.Background()); err != nil {
		t.Fatalf("priming: %v", err)
	}

	// Historical lookup must NOT return the cached live value.
	d, err := c.GetAt(context.Background(), time.Date(2012, 2, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("GetAt: %v", err)
	}
	if d.Net != 222 {
		t.Errorf("Net = %d, want 222 (historical should bypass live cache)", d.Net)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("inner GetAt calls = %d, want 1", got)
	}
}

// TestCachedGetAtUnavailableWhenNotHistorical confirms a Cached wrapping
// a non-HistoricalProvider returns ErrUnavailable for as-of lookups
// rather than silently falling back to live data.
func TestCachedGetAtUnavailableWhenNotHistorical(t *testing.T) {
	stub := stubProvider{getFunc: func(_ context.Context) (twse.MarketData, error) {
		return twse.MarketData{Net: 1}, nil
	}}
	c := twse.NewCachedWithTTL(stub, time.Hour, time.Hour)
	_, err := c.GetAt(context.Background(), time.Now())
	if !errors.Is(err, twse.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

// stubProvider is a hand-rolled twse.Provider stub.
type stubProvider struct {
	getFunc func(ctx context.Context) (twse.MarketData, error)
}

func (s stubProvider) Get(ctx context.Context) (twse.MarketData, error) {
	return s.getFunc(ctx)
}

// histStub satisfies both Provider and HistoricalProvider for testing
// the Cached.GetAt delegation path.
type histStub struct {
	getFunc   func(ctx context.Context) (twse.MarketData, error)
	getAtFunc func(ctx context.Context, asOf time.Time) (twse.MarketData, error)
}

func (s histStub) Get(ctx context.Context) (twse.MarketData, error) {
	return s.getFunc(ctx)
}

func (s histStub) GetAt(ctx context.Context, asOf time.Time) (twse.MarketData, error) {
	return s.getAtFunc(ctx, asOf)
}
