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

// liveBreadthServer wires fake STOCK_DAY_ALL (TSE) + tpex_mainboard_quotes
// (OTC) universes and an MIS getStockInfo backend on the same
// httptest.Server. The MIS handler peeks ex_ch prefixes to attribute
// each requested symbol to TSE or OTC, then replies from the matching
// canned quotes map.
type liveQuote struct {
	z, pz, o, y string
}

type liveBreadthServer struct {
	srv          *httptest.Server
	misCalls     int64 // atomic
	universeCall int64 // atomic
}

func newLiveBreadthServer(t *testing.T, tseUniverse, otcUniverse []string, tseQuotes, otcQuotes map[string]liveQuote) *liveBreadthServer {
	t.Helper()
	lbs := &liveBreadthServer{}
	lbs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/STOCK_DAY_ALL"):
			atomic.AddInt64(&lbs.universeCall, 1)
			rows := make([]map[string]string, 0, len(tseUniverse))
			for _, c := range tseUniverse {
				rows = append(rows, map[string]string{"Code": c})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(rows)
		case strings.HasPrefix(r.URL.Path, "/tpex_mainboard_quotes"):
			atomic.AddInt64(&lbs.universeCall, 1)
			rows := make([]map[string]string, 0, len(otcUniverse))
			for _, c := range otcUniverse {
				rows = append(rows, map[string]string{"SecuritiesCompanyCode": c})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(rows)
		case strings.HasPrefix(r.URL.Path, "/getStockInfo.jsp"):
			atomic.AddInt64(&lbs.misCalls, 1)
			ex := r.URL.Query().Get("ex_ch")
			parts := strings.Split(ex, "|")
			msg := make([]map[string]string, 0, len(parts))
			for _, p := range parts {
				var (
					code   string
					quotes map[string]liveQuote
				)
				switch {
				case strings.HasPrefix(p, "tse_"):
					code = strings.TrimSuffix(strings.TrimPrefix(p, "tse_"), ".tw")
					quotes = tseQuotes
				case strings.HasPrefix(p, "otc_"):
					code = strings.TrimSuffix(strings.TrimPrefix(p, "otc_"), ".tw")
					quotes = otcQuotes
				default:
					continue
				}
				q, ok := quotes[code]
				if !ok {
					continue
				}
				msg = append(msg, map[string]string{
					"c": code, "y": q.y, "z": q.z, "pz": q.pz, "o": q.o,
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
		srv.srv.URL+"/tpex_mainboard_quotes",
		srv.srv.URL+"/getStockInfo.jsp",
	)
	return p
}

// TestFetchLiveBreadthBasicClassification pins the up / down / flat
// bucketing on the TSE side: each stock's z is compared against y.
// Three stocks, one of each direction.
func TestFetchLiveBreadthBasicClassification(t *testing.T) {
	tseUniverse := []string{"2330", "2317", "2454"}
	tseQuotes := map[string]liveQuote{
		"2330": {z: "650.0", pz: "650.0", o: "650.0", y: "640.0"}, // up
		"2317": {z: "100.0", pz: "100.0", o: "100.0", y: "105.0"}, // down
		"2454": {z: "500.0", pz: "500.0", o: "500.0", y: "500.0"}, // flat
	}
	srv := newLiveBreadthServer(t, tseUniverse, nil, tseQuotes, nil)
	p := newProviderWithLive(srv)

	got, err := p.FetchLiveBreadth(context.Background())
	if err != nil {
		t.Fatalf("FetchLiveBreadth: %v", err)
	}
	if got.TSEAdvance != 1 || got.TSEDecline != 1 || got.TSEUnchanged != 1 {
		t.Errorf("TSE counts = up:%d down:%d flat:%d, want 1/1/1",
			got.TSEAdvance, got.TSEDecline, got.TSEUnchanged)
	}
	if got.AsOf.IsZero() {
		t.Errorf("AsOf zero, want populated tick time")
	}
}

// TestFetchLiveBreadthSplitsTSEAndOTC pins the per-exchange
// attribution: TSE counts and OTC counts must NOT mix in the
// aggregated result. Two TSE up, three OTC down → 上市 2/0/0,
// 上櫃 0/3/0.
func TestFetchLiveBreadthSplitsTSEAndOTC(t *testing.T) {
	tseUniverse := []string{"2330", "2317"}
	tseQuotes := map[string]liveQuote{
		"2330": {z: "650.0", pz: "650.0", o: "650.0", y: "640.0"}, // up
		"2317": {z: "150.0", pz: "150.0", o: "150.0", y: "145.0"}, // up
	}
	otcUniverse := []string{"3035", "3044", "3105"}
	otcQuotes := map[string]liveQuote{
		"3035": {z: "100.0", pz: "100.0", o: "100.0", y: "110.0"}, // down
		"3044": {z: "200.0", pz: "200.0", o: "200.0", y: "210.0"}, // down
		"3105": {z: "300.0", pz: "300.0", o: "300.0", y: "320.0"}, // down
	}
	srv := newLiveBreadthServer(t, tseUniverse, otcUniverse, tseQuotes, otcQuotes)
	p := newProviderWithLive(srv)

	got, err := p.FetchLiveBreadth(context.Background())
	if err != nil {
		t.Fatalf("FetchLiveBreadth: %v", err)
	}
	if got.TSEAdvance != 2 || got.TSEDecline != 0 || got.TSEUnchanged != 0 {
		t.Errorf("TSE counts = up:%d down:%d flat:%d, want 2/0/0",
			got.TSEAdvance, got.TSEDecline, got.TSEUnchanged)
	}
	if got.OTCAdvance != 0 || got.OTCDecline != 3 || got.OTCUnchanged != 0 {
		t.Errorf("OTC counts = up:%d down:%d flat:%d, want 0/3/0",
			got.OTCAdvance, got.OTCDecline, got.OTCUnchanged)
	}
}

// TestFetchLiveBreadthFiltersUniverseToFourDigit pins the 4-digit
// non-zero-prefix filter — ETFs (00xx) and warrants (5+ digit) drop
// out of the request set so MIS only sees the listed-equity batch.
func TestFetchLiveBreadthFiltersUniverseToFourDigit(t *testing.T) {
	tseUniverse := []string{
		"2330",  // 4-digit stock, kept
		"0050",  // ETF, dropped (starts with 0)
		"00878", // ETF, dropped (5 digit)
		"03001", // warrant, dropped
		"1101",  // 4-digit stock, kept
	}
	tseQuotes := map[string]liveQuote{
		"2330": {z: "650.0", pz: "650.0", o: "650.0", y: "640.0"}, // up
		"1101": {z: "50.0", pz: "50.0", o: "50.0", y: "52.0"},     // down
	}
	srv := newLiveBreadthServer(t, tseUniverse, nil, tseQuotes, nil)
	p := newProviderWithLive(srv)

	got, err := p.FetchLiveBreadth(context.Background())
	if err != nil {
		t.Fatalf("FetchLiveBreadth: %v", err)
	}
	total := got.TSEAdvance + got.TSEDecline + got.TSEUnchanged
	if total != 2 {
		t.Errorf("counted %d stocks, want 2 (4-digit only)", total)
	}
}

// TestFetchLiveBreadthFallsBackToPZ pins the z → pz fallback: when MIS
// returns "-" for the latest trade (between ticks) the previous-trade
// price keeps the stock in its bucket instead of dropping it.
func TestFetchLiveBreadthFallsBackToPZ(t *testing.T) {
	tseUniverse := []string{"2330"}
	tseQuotes := map[string]liveQuote{
		"2330": {z: "-", pz: "650.0", o: "645.0", y: "640.0"}, // pz used → up
	}
	srv := newLiveBreadthServer(t, tseUniverse, nil, tseQuotes, nil)
	p := newProviderWithLive(srv)

	got, err := p.FetchLiveBreadth(context.Background())
	if err != nil {
		t.Fatalf("FetchLiveBreadth: %v", err)
	}
	if got.TSEAdvance != 1 {
		t.Errorf("TSEAdvance = %d, want 1 (pz fallback should classify up)", got.TSEAdvance)
	}
}

// TestFetchLiveBreadthFallsBackToOpen pins the z → pz → o fallback:
// when both z and pz are "-" (the common case for any stock without
// a tick in the current snapshot window) the session open price
// keeps the stock counted instead of silently dropping it. Without
// this fallback the breadth count under-reports by ~85% mid-session.
func TestFetchLiveBreadthFallsBackToOpen(t *testing.T) {
	tseUniverse := []string{"2330", "2317"}
	tseQuotes := map[string]liveQuote{
		"2330": {z: "-", pz: "-", o: "650.0", y: "640.0"}, // o used → up
		"2317": {z: "-", pz: "-", o: "95.0", y: "100.0"},  // o used → down
	}
	srv := newLiveBreadthServer(t, tseUniverse, nil, tseQuotes, nil)
	p := newProviderWithLive(srv)

	got, err := p.FetchLiveBreadth(context.Background())
	if err != nil {
		t.Fatalf("FetchLiveBreadth: %v", err)
	}
	if got.TSEAdvance != 1 || got.TSEDecline != 1 {
		t.Errorf("TSE counts = up:%d down:%d, want 1/1 (o fallback should classify both)",
			got.TSEAdvance, got.TSEDecline)
	}
}

// TestFetchLiveBreadthSkipsUntraded pins the all-"-" case: when z, pz,
// AND o are all missing, the stock has no observable session price
// (halt or new listing) and MUST NOT land in any bucket — silently
// dropped from the count.
func TestFetchLiveBreadthSkipsUntraded(t *testing.T) {
	tseUniverse := []string{"2330", "2317"}
	tseQuotes := map[string]liveQuote{
		"2330": {z: "650.0", pz: "650.0", o: "640.0", y: "640.0"}, // up
		"2317": {z: "-", pz: "-", o: "-", y: "100.0"},             // untraded
	}
	srv := newLiveBreadthServer(t, tseUniverse, nil, tseQuotes, nil)
	p := newProviderWithLive(srv)

	got, err := p.FetchLiveBreadth(context.Background())
	if err != nil {
		t.Fatalf("FetchLiveBreadth: %v", err)
	}
	total := got.TSEAdvance + got.TSEDecline + got.TSEUnchanged
	if total != 1 {
		t.Errorf("counted %d, want 1 (untraded stock should be dropped)", total)
	}
}

// TestFetchLiveBreadthCachesResult pins the 30s TTL: a second call
// returns immediately from the in-process cache without hitting MIS.
func TestFetchLiveBreadthCachesResult(t *testing.T) {
	tseUniverse := []string{"2330"}
	tseQuotes := map[string]liveQuote{
		"2330": {z: "650.0", pz: "650.0", o: "650.0", y: "640.0"},
	}
	srv := newLiveBreadthServer(t, tseUniverse, nil, tseQuotes, nil)
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
// a 73-stock TSE universe should hit MIS twice (50 + 23).
func TestFetchLiveBreadthBatchesUniverse(t *testing.T) {
	tseUniverse := make([]string, 73)
	tseQuotes := make(map[string]liveQuote, 73)
	for i := range tseUniverse {
		code := fmt.Sprintf("%04d", 1100+i)
		tseUniverse[i] = code
		tseQuotes[code] = liveQuote{z: "100.0", pz: "100.0", o: "100.0", y: "100.0"} // all flat
	}
	srv := newLiveBreadthServer(t, tseUniverse, nil, tseQuotes, nil)
	p := newProviderWithLive(srv)

	got, err := p.FetchLiveBreadth(context.Background())
	if err != nil {
		t.Fatalf("FetchLiveBreadth: %v", err)
	}
	if got.TSEUnchanged != 73 {
		t.Errorf("TSEUnchanged = %d, want 73", got.TSEUnchanged)
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
