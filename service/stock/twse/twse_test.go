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
	// Fixture: 融券(交易單位) 今日餘額 = 190,811 張.
	if d.MarginShortLots != 190811 {
		t.Errorf("MarginShortLots = %d, want 190811", d.MarginShortLots)
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

// stubProvider is a hand-rolled twse.Provider stub.
type stubProvider struct {
	getFunc func(ctx context.Context) (twse.MarketData, error)
}

func (s stubProvider) Get(ctx context.Context) (twse.MarketData, error) {
	return s.getFunc(ctx)
}
