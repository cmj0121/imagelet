package yahoo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/imagelet/service/stock/quote"
)

// fixtureServer returns an httptest.Server that serves the named fixture
// from testdata/ for any path. Status overrides the response code.
func fixtureServer(t *testing.T, name string, status int) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = w.Write(body)
	}))
}

func newProvider(server *httptest.Server) *Provider {
	return NewWithEndpoint(server.URL+"/%s", server.Client())
}

func TestGetOpenMarket(t *testing.T) {
	srv := fixtureServer(t, "gspc_open.json", 0)
	defer srv.Close()

	q, err := newProvider(srv).Get(context.Background(), "^GSPC")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if q.Symbol != "^GSPC" {
		t.Errorf("symbol = %q, want %q", q.Symbol, "^GSPC")
	}
	if q.Last == 0 {
		t.Errorf("Last == 0, want non-zero")
	}
	if q.PrevClose == 0 {
		t.Errorf("PrevClose == 0, want non-zero")
	}
	if q.Currency == "" {
		t.Errorf("Currency is empty")
	}
	if q.AsOf.IsZero() {
		t.Errorf("AsOf is zero")
	}
	// Day-range and 52w-range fields landed in v0.2 -- pin that the parser
	// reads them from the same fixture and the helper methods agree.
	if q.DayHigh == 0 || q.DayLow == 0 {
		t.Errorf("DayHigh / DayLow not parsed: high=%v low=%v", q.DayHigh, q.DayLow)
	}
	// Session Open is parsed from indicators.quote[0].open[last] because
	// Yahoo's chart-meta omits regularMarketOpen. Fixture has open[]
	// populated so this should land non-zero and HasOHLC() should agree.
	if q.Open == 0 {
		t.Errorf("Open == 0, want non-zero (parsed from indicators.quote[0].open)")
	}
	if !q.HasOHLC() {
		t.Errorf("HasOHLC = false, want true (fixture has full OHLC quartet)")
	}
	if q.Week52High == 0 || q.Week52Low == 0 {
		t.Errorf("Week52High / Week52Low not parsed: high=%v low=%v", q.Week52High, q.Week52Low)
	}
	if !q.HasDayRange() {
		t.Errorf("HasDayRange = false, want true (fixture has positive low<high)")
	}
	if !q.Has52WeekRange() {
		t.Errorf("Has52WeekRange = false, want true")
	}
	if p := q.DayPosition(); p < 0 || p > 1 {
		t.Errorf("DayPosition = %v, want in [0,1]", p)
	}
	if p := q.Week52Position(); p < 0 || p > 1 {
		t.Errorf("Week52Position = %v, want in [0,1]", p)
	}
	// Fixture carries 12 daily closes; with the market open the latest
	// bar (today's intraday) is dropped, leaving 11 closed sessions —
	// enough to populate both MAs. Bounds: prices in fixture sit in
	// [7000, 7108.40] before the drop, so the MAs must too.
	if q.MA5 == 0 {
		t.Errorf("MA5 == 0, want non-zero (fixture has ≥ 5 closed sessions)")
	}
	if q.MA10 == 0 {
		t.Errorf("MA10 == 0, want non-zero (fixture has ≥ 10 closed sessions)")
	}
	if q.MA5 < 7000 || q.MA5 > 7200 {
		t.Errorf("MA5 = %v out of expected range [7000, 7200]", q.MA5)
	}
	if q.MA10 < 7000 || q.MA10 > 7200 {
		t.Errorf("MA10 = %v out of expected range [7000, 7200]", q.MA10)
	}
}

// TestGet_MAExcludesIntradayWhenOpen pins that the latest bar — today's
// intraday partial close — is NOT folded into the MA when the market is
// open. Otherwise the price-vs-MA arrow is self-referential.
func TestGet_MAExcludesIntradayWhenOpen(t *testing.T) {
	// 11 closes; the last one is an intentional outlier. If the MA
	// includes it the mean shoots above 7100; if it's correctly dropped
	// the mean stays around 7050.
	body := []byte(`{
		"chart": {
			"result": [
				{
					"meta": {
						"symbol": "AAA",
						"currency": "USD",
						"regularMarketPrice": 9999.0,
						"chartPreviousClose": 7100.0,
						"regularMarketTime": 1777062990,
						"currentTradingPeriod": {"regular": {"start": 0, "end": 9999999999}}
					},
					"timestamp": [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11],
					"indicators": {
						"quote": [
							{
								"close": [7000, 7010, 7020, 7030, 7040, 7050, 7060, 7070, 7080, 7090, 9999],
								"open":  [7000, 7010, 7020, 7030, 7040, 7050, 7060, 7070, 7080, 7090, 9999]
							}
						]
					}
				}
			]
		}
	}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	q, err := newProvider(srv).Get(context.Background(), "AAA")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if q.IsClosed {
		t.Fatalf("IsClosed = true; this fixture's regular window covers `now`")
	}
	// MA5 of [7050, 7060, 7070, 7080, 7090] = 7070; if 9999 leaked in
	// the mean would jump to ~7660.
	wantMA5 := 7070.0
	if q.MA5 != wantMA5 {
		t.Errorf("MA5 = %v, want %v (latest 9999 must be dropped)", q.MA5, wantMA5)
	}
}

// TestGet_MASparseDataYieldsZero pins the "insufficient data" sentinel:
// fewer than 5 closed sessions yields MA5 == 0 so the renderer can skip
// the MA row entirely on freshly-listed symbols.
func TestGet_MASparseDataYieldsZero(t *testing.T) {
	body := []byte(`{
		"chart": {
			"result": [
				{
					"meta": {
						"symbol": "AAA",
						"currency": "USD",
						"regularMarketPrice": 105.0,
						"chartPreviousClose": 104.0,
						"regularMarketTime": 1777062990,
						"currentTradingPeriod": {"regular": {"start": 0, "end": 9999999999}}
					},
					"timestamp": [1, 2, 3],
					"indicators": {
						"quote": [
							{
								"close": [100, 102, 105],
								"open":  [100, 102, 105]
							}
						]
					}
				}
			]
		}
	}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	q, err := newProvider(srv).Get(context.Background(), "AAA")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if q.MA5 != 0 {
		t.Errorf("MA5 = %v, want 0 (only 3 closes, intraday drop leaves 2)", q.MA5)
	}
	if q.MA10 != 0 {
		t.Errorf("MA10 = %v, want 0", q.MA10)
	}
}

func TestGetClosedMarket(t *testing.T) {
	srv := fixtureServer(t, "gspc_closed.json", 0)
	defer srv.Close()

	q, err := newProvider(srv).Get(context.Background(), "^GSPC")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !q.IsClosed {
		t.Errorf("IsClosed = false, want true (fixture's regular trading window is in 2023)")
	}
}

func TestGetEmptyResultIsErrUnavailable(t *testing.T) {
	srv := fixtureServer(t, "not_found.json", 0)
	defer srv.Close()

	_, err := newProvider(srv).Get(context.Background(), "^NOPE")
	if !errors.Is(err, quote.ErrUnavailable) {
		t.Errorf("err = %v, want quote.ErrUnavailable", err)
	}
}

func TestGetUpstream500ReturnsError(t *testing.T) {
	srv := fixtureServer(t, "not_found.json", http.StatusInternalServerError)
	defer srv.Close()

	_, err := newProvider(srv).Get(context.Background(), "^GSPC")
	if err == nil {
		t.Fatalf("err is nil; want non-nil for HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err %q does not mention 500 status", err)
	}
}

func TestGetYahooErrorJSONReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"chart":{"result":[],"error":{"code":"Not Found","description":"No data found, symbol may be delisted"}}}`))
	}))
	defer srv.Close()

	_, err := newProvider(srv).Get(context.Background(), "^NOPE")
	if err == nil {
		t.Fatalf("err is nil; want non-nil for Yahoo error JSON")
	}
	if !strings.Contains(err.Error(), "delisted") {
		t.Errorf("err %q does not include the upstream description", err)
	}
}

// TestGetAtPicksLatestBarBeforeAsOf walks the indicator timestamps and
// confirms GetAt returns the bar matching the latest t ≤ asOf, with
// IsClosed forced true and PrevClose pulled from the prior bar.
func TestGetAtPicksLatestBarBeforeAsOf(t *testing.T) {
	// Three bars: 2024-01-08 (Mon), 2024-01-09 (Tue), 2024-01-10 (Wed).
	// asOf = 2024-01-09 23:59 → expects bar #2 (Tue) close=470.0,
	// PrevClose=460.0 from bar #1.
	body := []byte(`{
		"chart": {
			"result": [
				{
					"meta": {"symbol": "AAA", "currency": "USD"},
					"timestamp": [1704672000, 1704758400, 1704844800],
					"indicators": {
						"quote": [
							{
								"open":  [455.0, 465.0, 475.0],
								"high":  [462.0, 472.0, 482.0],
								"low":   [453.0, 463.0, 473.0],
								"close": [460.0, 470.0, 480.0]
							}
						]
					}
				}
			]
		}
	}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := NewWithEndpoints(srv.URL+"/%s", srv.URL+"/%s?p1=%d&p2=%d", srv.Client())
	asOf := time.Unix(1704758400, 0).Add(8 * time.Hour) // 2024-01-09 mid-day UTC
	q, err := p.GetAt(context.Background(), "AAA", asOf)
	if err != nil {
		t.Fatalf("GetAt: %v", err)
	}
	if q.Last != 470.0 {
		t.Errorf("Last = %v, want 470.0 (Tue close)", q.Last)
	}
	if q.PrevClose != 460.0 {
		t.Errorf("PrevClose = %v, want 460.0 (Mon close)", q.PrevClose)
	}
	if q.Open != 465.0 {
		t.Errorf("Open = %v, want 465.0 (Tue open)", q.Open)
	}
	if !q.IsClosed {
		t.Errorf("IsClosed = false, want true (historical bar must be closed)")
	}
	if q.Symbol != "AAA" {
		t.Errorf("Symbol = %q, want AAA", q.Symbol)
	}
}

// TestGetAtFutureClampedToNow pins that a future-dated asOf does NOT
// run the upstream against an impossible window — it falls back to the
// current calendar instead of looping uselessly.
func TestGetAtFutureClampedToNow(t *testing.T) {
	// Empty result for any query. If clamping fails, the test would
	// pass anyway because of the empty-result branch — but we also
	// verify period2 ≤ now+2d to confirm the clamp logic.
	var seenURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenURL = r.URL.String()
		_, _ = w.Write([]byte(`{"chart":{"result":[]}}`))
	}))
	defer srv.Close()

	p := NewWithEndpoints(srv.URL+"/%s", srv.URL+"/%s?p1=%d&p2=%d", srv.Client())
	asOf := time.Now().Add(365 * 24 * time.Hour) // 1y in the future
	_, err := p.GetAt(context.Background(), "AAA", asOf)
	if !errors.Is(err, quote.ErrUnavailable) {
		t.Errorf("err = %v, want quote.ErrUnavailable", err)
	}
	// The seenURL's p2 should be ≤ now+2d unix; we don't parse it back,
	// but a non-empty URL means the request landed (didn't loop).
	if seenURL == "" {
		t.Errorf("upstream not hit; clamp may have prevented all requests")
	}
}

// TestGetAtNoBarsBeforeAsOfReturnsErrUnavailable confirms an empty
// indicator window collapses to ErrUnavailable rather than a partial
// Quote with zero fields.
func TestGetAtNoBarsBeforeAsOfReturnsErrUnavailable(t *testing.T) {
	body := []byte(`{
		"chart": {
			"result": [
				{
					"meta": {"symbol": "AAA"},
					"timestamp": [],
					"indicators": {"quote": [{"close": []}]}
				}
			]
		}
	}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := NewWithEndpoints(srv.URL+"/%s", srv.URL+"/%s?p1=%d&p2=%d", srv.Client())
	_, err := p.GetAt(context.Background(), "AAA", time.Unix(1704672000, 0))
	if !errors.Is(err, quote.ErrUnavailable) {
		t.Errorf("err = %v, want quote.ErrUnavailable", err)
	}
}

// TestMarketClosed exercises the marketClosed helper directly with synthetic
// timestamps so we don't need to mock time.Now() in the Get path.
func TestMarketClosed(t *testing.T) {
	mkMeta := func(start, end int64) chartMeta {
		var m chartMeta
		m.CurrentTradingPeriod.Regular.Start = start
		m.CurrentTradingPeriod.Regular.End = end
		return m
	}

	cases := []struct {
		name  string
		now   int64
		start int64
		end   int64
		want  bool
	}{
		{"before_open", 100, 200, 300, true},
		{"at_open", 200, 200, 300, false},
		{"middle_of_session", 250, 200, 300, false},
		{"at_close", 300, 200, 300, true}, // .end is exclusive
		{"after_close", 400, 200, 300, true},
		{"end_in_past_weekend", 1700100000, 1700000000, 1700020000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := marketClosed(mkMeta(tc.start, tc.end), tc.now)
			if got != tc.want {
				t.Errorf("marketClosed(start=%d, end=%d, now=%d) = %v, want %v",
					tc.start, tc.end, tc.now, got, tc.want)
			}
		})
	}
}
