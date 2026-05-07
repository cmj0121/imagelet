package twse

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
)

func TestFetchForeignExact_Fixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "foreign_2330_0050_2317_20260506.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "date=20260506") {
			t.Errorf("missing date= in query: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetForeignEndpoint(srv.URL + "?date=%s&selectType=ALLBUT0999&response=json")

	// 2330 — verified live: 70.65% foreign holdings.
	f, found, err := p.FetchForeignExact(context.Background(), "2330", "20260506")
	if err != nil {
		t.Fatalf("2330 fetch: %v", err)
	}
	if !found {
		t.Fatalf("2330 found = false, want true")
	}
	if f.HoldingPct != 70.65 {
		t.Errorf("2330 HoldingPct = %v, want 70.65", f.HoldingPct)
	}
	if f.AvailablePct != 29.34 {
		t.Errorf("2330 AvailablePct = %v, want 29.34", f.AvailablePct)
	}
	if f.UpperlimitPct != 100.00 {
		t.Errorf("2330 UpperlimitPct = %v, want 100.00", f.UpperlimitPct)
	}
	if f.IssuedShares != 25_932_524_521 {
		t.Errorf("2330 IssuedShares = %d, want 25932524521", f.IssuedShares)
	}

	// 0050 — verified live: 3.56% foreign holdings (ETF).
	f, found, _ = p.FetchForeignExact(context.Background(), "0050", "20260506")
	if !found || f.HoldingPct != 3.56 {
		t.Errorf("0050 HoldingPct = %v found=%v, want 3.56 / true", f.HoldingPct, found)
	}

	// Unknown stock — found=false, no error.
	_, found, err = p.FetchForeignExact(context.Background(), "9999", "20260506")
	if err != nil {
		t.Errorf("9999 err = %v, want nil", err)
	}
	if found {
		t.Errorf("9999 found = true, want false")
	}
}

func TestFetchForeignExact_EmptyEndpoint(t *testing.T) {
	p := &HTTPProvider{}
	_, _, err := p.FetchForeignExact(context.Background(), "2330", "20260506")
	if err != ErrUnavailable {
		t.Errorf("err = %v, want ErrUnavailable when endpoint unset", err)
	}
}

func TestFetchForeignExact_StatNotOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"stat":"查詢日期大於可查詢最大日期，請重新查詢!","total":0}`))
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetForeignEndpoint(srv.URL + "?date=%s")
	_, found, err := p.FetchForeignExact(context.Background(), "2330", "20260507")
	if err != nil {
		t.Errorf("err = %v, want nil on stat != OK (treat as publish gap)", err)
	}
	if found {
		t.Errorf("found = true, want false on stat != OK")
	}
}

// TestCachedForeign_FetchOnceServesMany pins per-(stock,date) cache + walkback.
func TestCachedForeign_FetchOnceServesMany(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "foreign_2330_0050_2317_20260506.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetForeignEndpoint(srv.URL + "?date=%s")
	c := NewCachedForeign(p, 0)

	t0 := time.Date(2026, 5, 6, 12, 0, 0, 0, twLoc)
	c.SetClock(func() time.Time { return t0 })

	ctx := context.Background()
	if _, err := c.GetForeign(ctx, "2330", t0); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Second call same (stock, date) — cache hit, no upstream hit.
	if _, err := c.GetForeign(ctx, "2330", t0); err != nil {
		t.Errorf("second call: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("upstream calls = %d, want 1 (per-stock cache should serve repeat)", got)
	}
	// Different stock at same date — cache miss because per-(stock,date) keying.
	if _, err := c.GetForeign(ctx, "0050", t0); err != nil {
		t.Errorf("0050 call: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("upstream calls after 0050 = %d, want 2", got)
	}
}

// TestCachedForeign_WalksBackOnPublishGap pins the walkback semantic:
// asking for tomorrow's date probes back through "stat != OK" sentinels
// until it finds a real publication, returning ErrUnavailable only after
// maxLookbackDays.
func TestCachedForeign_WalksBackOnPublishGap(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "foreign_2330_0050_2317_20260506.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	pubDate := "20260506"
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if strings.Contains(r.URL.RawQuery, "date="+pubDate) {
			_, _ = w.Write(body)
			return
		}
		// Any other date returns "no data" stat
		_, _ = w.Write([]byte(`{"stat":"查無資料","total":0}`))
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetForeignEndpoint(srv.URL + "?date=%s")
	c := NewCachedForeign(p, 0)

	// Request for 2026-05-07 (gap), walkback should land on 2026-05-06.
	t0 := time.Date(2026, 5, 7, 12, 0, 0, 0, twLoc)
	c.SetClock(func() time.Time { return t0 })
	f, err := c.GetForeign(context.Background(), "2330", t0)
	if err != nil {
		t.Fatalf("walkback: %v", err)
	}
	if f.HoldingPct != 70.65 {
		t.Errorf("walkback HoldingPct = %v, want 70.65", f.HoldingPct)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("upstream calls = %d, want 2 (one for 5-07 gap, one for 5-06 hit)", got)
	}
}

func TestCachedForeign_UnknownStockPropagatesUnavailable(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "foreign_2330_0050_2317_20260506.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetForeignEndpoint(srv.URL + "?date=%s")
	c := NewCachedForeign(p, 0)

	t0 := time.Date(2026, 5, 6, 12, 0, 0, 0, twLoc)
	c.SetClock(func() time.Time { return t0 })
	_, err = c.GetForeign(context.Background(), "9999", t0)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("9999 err = %v, want ErrUnavailable", err)
	}
}
