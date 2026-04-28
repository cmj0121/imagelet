package twse_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cmj0121/imagelet/service/stock/twse"
)

// liveBreadthServer wires a fake STOCK_DAY_ALL universe + MIS
// getStockInfo backend on the same httptest.Server. The handler
// branches on path: `/STOCK_DAY_ALL` returns the canned universe;
// `/getStockInfo.jsp` parses ex_ch and replies with one entry per
// requested symbol from the canned quotes map.
type liveQuote struct {
	z, pz, y string
}

type liveBreadthServer struct {
	srv          *httptest.Server
	misCalls     int64 // atomic
	universeCall int64 // atomic
}

func newLiveBreadthServer(t *testing.T, universe []string, quotes map[string]liveQuote) *liveBreadthServer {
	t.Helper()
	lbs := &liveBreadthServer{}
	lbs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/STOCK_DAY_ALL"):
			atomic.AddInt64(&lbs.universeCall, 1)
			rows := make([]map[string]string, 0, len(universe))
			for _, c := range universe {
				rows = append(rows, map[string]string{"Code": c})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(rows)
		case strings.HasPrefix(r.URL.Path, "/getStockInfo.jsp"):
			atomic.AddInt64(&lbs.misCalls, 1)
			ex := r.URL.Query().Get("ex_ch")
			parts := strings.Split(ex, "|")
			msg := make([]map[string]string, 0, len(parts))
			for _, p := range parts {
				code := strings.TrimSuffix(strings.TrimPrefix(p, "tse_"), ".tw")
				q, ok := quotes[code]
				if !ok {
					continue
				}
				msg = append(msg, map[string]string{
					"c": code, "y": q.y, "z": q.z, "pz": q.pz,
					"d": "20260428", "t": "10:30:00",
				})
			}
			body := map[string]any{
				"rtcode":   "0000",
				"msgArray": msg,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(lbs.srv.Close)
	return lbs
}

func newProviderWithLive(srv *liveBreadthServer) *twse.HTTPProvider {
	p := twse.NewWithEndpoints(
		srv.srv.URL+"/BFI82U?d=%s",
		srv.srv.URL+"/MIMARGN?d=%s",
		srv.srv.URL+"/MIINDEX?d=%s",
		srv.srv.Client(),
	)
	p.SetLiveBreadthEndpoints(
		srv.srv.URL+"/STOCK_DAY_ALL",
		srv.srv.URL+"/getStockInfo.jsp",
	)
	return p
}

// TestFetchLiveBreadthBasicClassification pins the up / down / flat
// bucketing: each stock's z (current trade) is compared against y
// (prev close). Three stocks, one of each direction.
func TestFetchLiveBreadthBasicClassification(t *testing.T) {
	universe := []string{"2330", "2317", "2454"}
	quotes := map[string]liveQuote{
		"2330": {z: "650.0", pz: "650.0", y: "640.0"}, // up
		"2317": {z: "100.0", pz: "100.0", y: "105.0"}, // down
		"2454": {z: "500.0", pz: "500.0", y: "500.0"}, // flat
	}
	srv := newLiveBreadthServer(t, universe, quotes)
	p := newProviderWithLive(srv)

	got, err := p.FetchLiveBreadth(context.Background())
	if err != nil {
		t.Fatalf("FetchLiveBreadth: %v", err)
	}
	if got.AdvanceCount != 1 || got.DeclineCount != 1 || got.UnchangedCount != 1 {
		t.Errorf("counts = up:%d down:%d flat:%d, want 1/1/1",
			got.AdvanceCount, got.DeclineCount, got.UnchangedCount)
	}
	if got.AsOf.IsZero() {
		t.Errorf("AsOf zero, want populated tick time")
	}
}

// TestFetchLiveBreadthFiltersUniverseToFourDigit pins the 4-digit
// non-zero-prefix filter — ETFs (00xx) and warrants (5+ digit) drop
// out of the request set so MIS only sees the listed-equity batch.
func TestFetchLiveBreadthFiltersUniverseToFourDigit(t *testing.T) {
	universe := []string{
		"2330",  // 4-digit stock, kept
		"0050",  // ETF, dropped (starts with 0)
		"00878", // ETF, dropped (5 digit)
		"03001", // warrant, dropped
		"1101",  // 4-digit stock, kept
	}
	quotes := map[string]liveQuote{
		"2330": {z: "650.0", pz: "650.0", y: "640.0"}, // up
		"1101": {z: "50.0", pz: "50.0", y: "52.0"},    // down
	}
	srv := newLiveBreadthServer(t, universe, quotes)
	p := newProviderWithLive(srv)

	got, err := p.FetchLiveBreadth(context.Background())
	if err != nil {
		t.Fatalf("FetchLiveBreadth: %v", err)
	}
	total := got.AdvanceCount + got.DeclineCount + got.UnchangedCount
	if total != 2 {
		t.Errorf("counted %d stocks, want 2 (4-digit only)", total)
	}
}

// TestFetchLiveBreadthFallsBackToPZ pins the z → pz fallback: when MIS
// returns "-" for the latest trade (between ticks) the previous-trade
// price keeps the stock in its bucket instead of dropping it.
func TestFetchLiveBreadthFallsBackToPZ(t *testing.T) {
	universe := []string{"2330"}
	quotes := map[string]liveQuote{
		"2330": {z: "-", pz: "650.0", y: "640.0"}, // pz used → up
	}
	srv := newLiveBreadthServer(t, universe, quotes)
	p := newProviderWithLive(srv)

	got, err := p.FetchLiveBreadth(context.Background())
	if err != nil {
		t.Fatalf("FetchLiveBreadth: %v", err)
	}
	if got.AdvanceCount != 1 {
		t.Errorf("AdvanceCount = %d, want 1 (pz fallback should classify up)", got.AdvanceCount)
	}
}

// TestFetchLiveBreadthSkipsUntraded pins the dual-"-" case: when both
// z and pz are missing, the stock has no observable tick and MUST NOT
// land in any bucket — silently dropped from the count.
func TestFetchLiveBreadthSkipsUntraded(t *testing.T) {
	universe := []string{"2330", "2317"}
	quotes := map[string]liveQuote{
		"2330": {z: "650.0", pz: "650.0", y: "640.0"}, // up
		"2317": {z: "-", pz: "-", y: "100.0"},         // untraded
	}
	srv := newLiveBreadthServer(t, universe, quotes)
	p := newProviderWithLive(srv)

	got, err := p.FetchLiveBreadth(context.Background())
	if err != nil {
		t.Fatalf("FetchLiveBreadth: %v", err)
	}
	total := got.AdvanceCount + got.DeclineCount + got.UnchangedCount
	if total != 1 {
		t.Errorf("counted %d, want 1 (untraded stock should be dropped)", total)
	}
}

// TestFetchLiveBreadthCachesResult pins the 30s TTL: a second call
// returns immediately from the in-process cache without hitting MIS.
func TestFetchLiveBreadthCachesResult(t *testing.T) {
	universe := []string{"2330"}
	quotes := map[string]liveQuote{
		"2330": {z: "650.0", pz: "650.0", y: "640.0"},
	}
	srv := newLiveBreadthServer(t, universe, quotes)
	p := newProviderWithLive(srv)

	for i := 0; i < 3; i++ {
		if _, err := p.FetchLiveBreadth(context.Background()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	misCalls := atomic.LoadInt64(&srv.misCalls)
	if misCalls != 1 {
		t.Errorf("MIS calls = %d, want 1 (cache should suppress repeats)", misCalls)
	}
}

// TestFetchLiveBreadthBatchesUniverse pins the 50-symbol batch cap:
// a 73-stock universe should hit MIS twice (50 + 23).
func TestFetchLiveBreadthBatchesUniverse(t *testing.T) {
	universe := make([]string, 73)
	quotes := make(map[string]liveQuote, 73)
	for i := range universe {
		code := fmt.Sprintf("%04d", 1100+i)
		universe[i] = code
		quotes[code] = liveQuote{z: "100.0", pz: "100.0", y: "100.0"} // all flat
	}
	srv := newLiveBreadthServer(t, universe, quotes)
	p := newProviderWithLive(srv)

	got, err := p.FetchLiveBreadth(context.Background())
	if err != nil {
		t.Fatalf("FetchLiveBreadth: %v", err)
	}
	if got.UnchangedCount != 73 {
		t.Errorf("UnchangedCount = %d, want 73", got.UnchangedCount)
	}
	if calls := atomic.LoadInt64(&srv.misCalls); calls != 2 {
		t.Errorf("MIS calls = %d, want 2 (50 + 23 batches)", calls)
	}
}

// TestFetchLiveBreadthDisabledWhenEndpointsUnset pins the gate: a
// provider built via NewWithEndpoints (no SetLiveBreadthEndpoints
// follow-up) returns ErrUnavailable rather than hitting the network.
func TestFetchLiveBreadthDisabledWhenEndpointsUnset(t *testing.T) {
	p := twse.NewWithEndpoints("", "", "", &http.Client{})
	_, err := p.FetchLiveBreadth(context.Background())
	if err == nil {
		t.Fatalf("FetchLiveBreadth should error when endpoints unset")
	}
}
