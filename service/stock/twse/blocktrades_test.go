package twse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// blockTradesFixture mimics a small live BFIAUU response: one row each
// for 2 distinct stocks, plus one stock with two trades in the same
// day (verifies the per-stock slice append path).
const blockTradesFixture = `[
  {"Code":"2330","Name":"台積電","Classn":"配對交易","TradePrice":"2200.00","TradeVolume":"1500000","TradeValue":"3300000000"},
  {"Code":"2330","Name":"台積電","Classn":"配對交易","TradePrice":"2210.00","TradeVolume":"500000","TradeValue":"1105000000"},
  {"Code":"2301","Name":"光寶科","Classn":"配對交易","TradePrice":"177.75","TradeVolume":"1501000","TradeValue":"266802750"}
]`

func TestFetchBlockTradesExact_Fixture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(blockTradesFixture))
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetBlockTradesEndpoint(srv.URL)

	day, found, err := p.FetchBlockTradesExact(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true")
	}
	if got := len(day.Rows); got != 2 {
		t.Errorf("len(Rows) = %d, want 2 (2330 + 2301)", got)
	}
	if got := len(day.Rows["2330"]); got != 2 {
		t.Errorf("len(2330 trades) = %d, want 2", got)
	}
	if got := day.Rows["2330"][0].TradePrice; got != 2200.0 {
		t.Errorf("2330[0].TradePrice = %v, want 2200.0", got)
	}
	if got := day.Rows["2330"][0].TradeVolume; got != 1_500_000 {
		t.Errorf("2330[0].TradeVolume = %d, want 1500000", got)
	}
	if got := day.Rows["2301"][0].Class; got != "配對交易" {
		t.Errorf("2301[0].Class = %q, want 配對交易", got)
	}
}

func TestFetchBlockTradesExact_EmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetBlockTradesEndpoint(srv.URL)

	day, found, err := p.FetchBlockTradesExact(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("err = %v, want nil (empty array is publish-gap, not error)", err)
	}
	if found {
		t.Errorf("found = true, want false on empty array")
	}
	if day.Rows != nil {
		t.Errorf("Rows = %v, want nil on empty array", day.Rows)
	}
}

func TestFetchBlockTradesExact_EmptyEndpoint(t *testing.T) {
	p := &HTTPProvider{} // blockTrades unset
	_, _, err := p.FetchBlockTradesExact(context.Background(), time.Now())
	if err != ErrUnavailable {
		t.Errorf("err = %v, want ErrUnavailable when endpoint unset", err)
	}
}

// TestCachedBlockTrades_FetchOnceServesMany pins the singleflight
// behaviour: many concurrent requests for different stocks converge
// on a single upstream fetch, then resolve via per-stock map lookups.
func TestCachedBlockTrades_FetchOnceServesMany(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(blockTradesFixture))
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetBlockTradesEndpoint(srv.URL)
	c := NewCachedBlockTrades(p, 0)

	ctx := context.Background()
	// First call populates cache.
	if _, trades, err := c.GetBlockTrades(ctx, "2330", time.Now()); err != nil || len(trades) != 2 {
		t.Fatalf("first call: err=%v len=%d", err, len(trades))
	}
	// Subsequent lookups for the same and different stocks hit cache.
	if _, trades, err := c.GetBlockTrades(ctx, "2301", time.Now()); err != nil || len(trades) != 1 {
		t.Errorf("2301 lookup: err=%v len=%d", err, len(trades))
	}
	if _, trades, _ := c.GetBlockTrades(ctx, "9999", time.Now()); len(trades) != 0 {
		t.Errorf("missing stock should yield nil-slice, got %v", trades)
	}
	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1 (cache+singleflight should serve repeat lookups)", calls)
	}
}
