package twse

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// holdersFixturePath points at the pinned TDCC fixture captured by
// ward during empirical verification. 3 stocks × 17 tiers each = 51
// rows; covers 上市 (2330), 上櫃 (6488), and ETF (0050) so a single
// fixture exercises every coverage class the renderer cares about.
const holdersFixturePath = "testdata/holders_2330_6488_0050_20260430.json"

func newHoldersFixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(holdersFixturePath))
	if err != nil {
		t.Fatalf("fixture read: %v", err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

func newHoldersProvider(t *testing.T, srv *httptest.Server) *HTTPProvider {
	t.Helper()
	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetHoldersEndpoint(srv.URL)
	return p
}

func TestFetchHoldersExact_Fixture(t *testing.T) {
	srv := newHoldersFixtureServer(t)
	defer srv.Close()
	p := newHoldersProvider(t, srv)

	dump, found, err := p.FetchHoldersExact(context.Background())
	if err != nil {
		t.Fatalf("FetchHoldersExact: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true")
	}
	if y, m, d := dump.AsOf.Date(); y != 2026 || m != time.April || d != 30 {
		t.Errorf("AsOf = %s, want 2026-04-30 (Asia/Taipei)", dump.AsOf)
	}

	tsmc, ok := dump.Rows["2330"]
	if !ok {
		t.Fatalf("Rows[\"2330\"] missing")
	}
	if tsmc.StockID != "2330" {
		t.Errorf("2330 StockID = %q, want %q", tsmc.StockID, "2330")
	}
	// Verified against the live response 2026-05-07: tier 15 (>1M shares)
	// holds 1,497 accounts owning 22,193,101,818 shares = 85.58% of the
	// custodied float — the canonical "concentrated institutional" signal.
	if got := tsmc.Tiers[14].Count; got != 1497 {
		t.Errorf("2330 Tiers[14].Count = %d, want 1497", got)
	}
	if got := tsmc.Tiers[14].Share; got != 22193101818 {
		t.Errorf("2330 Tiers[14].Share = %d, want 22193101818", got)
	}
	if got := tsmc.Tiers[14].Pct; got < 85.57 || got > 85.59 {
		t.Errorf("2330 Tiers[14].Pct = %f, want ≈85.58", got)
	}
	if got := tsmc.TotalCount; got != 2519187 {
		t.Errorf("2330 TotalCount = %d, want 2519187", got)
	}
	if got := tsmc.TotalShare; got != 25932524521 {
		t.Errorf("2330 TotalShare = %d, want 25932524521", got)
	}
	if !tsmc.Has() {
		t.Errorf("2330 Has() = false, want true")
	}

	// 6488 is 上櫃 — proves OTC coverage.
	otc, ok := dump.Rows["6488"]
	if !ok {
		t.Fatalf("Rows[\"6488\"] missing — OTC coverage broken")
	}
	if !otc.Has() {
		t.Errorf("6488 Has() = false, want true")
	}
	if otc.TotalCount == 0 {
		t.Errorf("6488 TotalCount = 0, want non-zero")
	}

	// 0050 is an ETF — proves leading-zero / ETF coverage.
	etf, ok := dump.Rows["0050"]
	if !ok {
		t.Fatalf("Rows[\"0050\"] missing — ETF coverage broken")
	}
	if !etf.Has() {
		t.Errorf("0050 Has() = false, want true")
	}
}

func TestFetchHoldersExact_PaddedID(t *testing.T) {
	srv := newHoldersFixtureServer(t)
	defer srv.Close()
	p := newHoldersProvider(t, srv)

	dump, _, err := p.FetchHoldersExact(context.Background())
	if err != nil {
		t.Fatalf("FetchHoldersExact: %v", err)
	}
	// The upstream right-pads ids with spaces ("2330  "). Lookup with
	// the padded id MUST miss; the trimmed id MUST hit.
	if _, ok := dump.Rows["2330  "]; ok {
		t.Errorf("Rows[\"2330  \"] (padded) found — trim-on-parse missed")
	}
	if _, ok := dump.Rows["2330"]; !ok {
		t.Errorf("Rows[\"2330\"] (trimmed) missing — trim-on-parse failed")
	}
}

func TestFetchHoldersExact_BOMKey(t *testing.T) {
	// First confirm the BOM-prefixed key is parsed correctly via the
	// real fixture — the date came through as 2026-04-30 in
	// TestFetchHoldersExact_Fixture, which is the proof. Now verify
	// that a body WITHOUT the BOM trips the schema probe.
	tests := []struct {
		name    string
		dateKey string
		wantErr error
	}{
		{"with BOM (canonical)", bomDateKey, nil},
		{"without BOM (drift)", "資料日期", ErrUnavailable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := map[string]string{
				"證券代號":      "2330  ",
				"持股分級":      "17",
				"人數":        "100",
				"股數":        "1000",
				"占集保庫存數比例%": "100.00",
				tc.dateKey:  "20260430",
			}
			body, err := json.Marshal([]map[string]string{row})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			defer srv.Close()
			p := newHoldersProvider(t, srv)
			_, _, err = p.FetchHoldersExact(context.Background())
			if !errors.Is(err, tc.wantErr) && err != tc.wantErr {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestFetchHoldersExact_SchemaDrift(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantErr error
	}{
		{
			name: "drop expected key",
			mutate: func(row map[string]string) {
				delete(row, "占集保庫存數比例%")
			},
			wantErr: ErrUnavailable,
		},
		{
			name: "add unexpected key",
			mutate: func(row map[string]string) {
				row["新欄位"] = "X"
			},
			wantErr: ErrUnavailable,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := map[string]string{
				"證券代號":      "2330  ",
				"持股分級":      "17",
				"人數":        "100",
				"股數":        "1000",
				"占集保庫存數比例%": "100.00",
				bomDateKey:  "20260430",
			}
			tc.mutate(row)
			body, err := json.Marshal([]map[string]string{row})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			defer srv.Close()
			p := newHoldersProvider(t, srv)
			_, _, err = p.FetchHoldersExact(context.Background())
			if !errors.Is(err, tc.wantErr) && err != tc.wantErr {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestFetchHoldersExact_BodyCapTrips(t *testing.T) {
	// Serve a body padded comfortably past 4 KiB, then shrink the cap
	// to 1 KiB so the parser sees a truncated stream and bails LOUDLY
	// (json error). Silent partial-parse is the failure mode tenth-man
	// flagged; this is the regression test.
	row := map[string]string{
		"證券代號":      "2330  ",
		"持股分級":      "17",
		"人數":        "100",
		"股數":        "1000",
		"占集保庫存數比例%": "100.00",
		bomDateKey:  "20260430",
	}
	rows := make([]map[string]string, 0, 200)
	for i := 0; i < 200; i++ {
		rows = append(rows, row)
	}
	body, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(body) < 4096 {
		t.Fatalf("test body too small (%d bytes); cap test would not trip", len(body))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	prevCap := holdersBodyCap
	holdersBodyCap = 1024
	t.Cleanup(func() { holdersBodyCap = prevCap })

	p := newHoldersProvider(t, srv)
	_, _, err = p.FetchHoldersExact(context.Background())
	if err == nil {
		t.Fatalf("FetchHoldersExact: nil err, want loud failure on cap-trip")
	}
}

func TestFetchHoldersExact_EmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	p := newHoldersProvider(t, srv)
	dump, found, err := p.FetchHoldersExact(context.Background())
	if err != nil {
		t.Errorf("err = %v, want nil (empty publish gap is not an error)", err)
	}
	if found {
		t.Errorf("found = true, want false")
	}
	if len(dump.Rows) != 0 {
		t.Errorf("Rows len = %d, want 0", len(dump.Rows))
	}
}

func TestFetchHoldersExact_HTTP500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "TDCC outage", http.StatusInternalServerError)
	}))
	defer srv.Close()
	p := newHoldersProvider(t, srv)
	_, _, err := p.FetchHoldersExact(context.Background())
	if err == nil {
		t.Fatalf("err = nil, want non-nil for HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want it to mention 500 status", err)
	}
}

func TestFetchHoldersExact_EmptyEndpoint(t *testing.T) {
	// SetHoldersEndpoint("") (or unset) MUST disable the fetcher
	// without hitting the network — matches the t86/twt93u pattern
	// where production wires it via New() and tests can pin to "".
	p := NewWithEndpoints("", "", "", http.DefaultClient)
	_, _, err := p.FetchHoldersExact(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestHoldersFreshFor(t *testing.T) {
	dump := time.Date(2026, 4, 30, 12, 0, 0, 0, twLoc)
	tests := []struct {
		name string
		req  time.Time
		want bool
	}{
		{"zero req (no override)", time.Time{}, true},
		{"same day", dump, true},
		{"13d before", dump.AddDate(0, 0, -13), true},
		{"14d before (boundary, inclusive)", dump.AddDate(0, 0, -14), true},
		{"15d before (just over)", dump.AddDate(0, 0, -15), false},
		{"13d after (future req)", dump.AddDate(0, 0, 13), true},
		{"15d after (future req past window)", dump.AddDate(0, 0, 15), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := HoldersFreshFor(tc.req, dump)
			if got != tc.want {
				t.Errorf("HoldersFreshFor(%v, %v) = %v, want %v", tc.req, dump, got, tc.want)
			}
		})
	}
}

func TestValidateHoldersSchema_Standalone(t *testing.T) {
	good := map[string]string{
		"證券代號":      "2330  ",
		"持股分級":      "1",
		"人數":        "0",
		"股數":        "0",
		"占集保庫存數比例%": "0.00",
		bomDateKey:  "20260430",
	}
	if err := validateHoldersSchema(good); err != nil {
		t.Errorf("validateHoldersSchema(canonical) = %v, want nil", err)
	}
}

// TestHTTPProvider_GetHoldersDistribution_Direct exercises the
// uncached HoldersProvider conformance on HTTPProvider. The cached
// path covers FetchHoldersExact → CachedHolders.GetHoldersDistribution
// transitively; this pins the direct-call path that the Cached
// fallback branch dispatches through for non-Cached test wiring.
func TestHTTPProvider_GetHoldersDistribution_Direct(t *testing.T) {
	srv := newHoldersFixtureServer(t)
	defer srv.Close()
	p := newHoldersProvider(t, srv)

	d, err := p.GetHoldersDistribution(context.Background(), "2330", time.Time{})
	if err != nil {
		t.Fatalf("GetHoldersDistribution: %v", err)
	}
	if got := d.Tiers[14].Count; got != 1497 {
		t.Errorf("Tiers[14].Count = %d, want 1497", got)
	}
	_, err = p.GetHoldersDistribution(context.Background(), "9999", time.Time{})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("unknown stock err = %v, want ErrUnavailable", err)
	}
}

func TestFetchHoldersExact_Tier16Dropped(t *testing.T) {
	// Tier 16 (差異數調整) must not appear in Tiers[..] — it's a
	// settlement adjustment, not a holder bucket. Build a synthetic
	// row set with tiers 1, 16, and 17 only and assert tier 16 leaves
	// no trace in the output.
	mkRow := func(tier string, count int) map[string]string {
		return map[string]string{
			"證券代號":      "2330  ",
			"持股分級":      tier,
			"人數":        strconv.Itoa(count),
			"股數":        "0",
			"占集保庫存數比例%": "0.00",
			bomDateKey:  "20260430",
		}
	}
	body, err := json.Marshal([]map[string]string{
		mkRow("1", 5),
		mkRow("16", 999),
		mkRow("17", 5),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	p := newHoldersProvider(t, srv)
	dump, found, err := p.FetchHoldersExact(context.Background())
	if err != nil || !found {
		t.Fatalf("FetchHoldersExact: err=%v found=%v", err, found)
	}
	dist := dump.Rows["2330"]
	if dist.Tiers[0].Count != 5 {
		t.Errorf("Tiers[0].Count = %d, want 5", dist.Tiers[0].Count)
	}
	// No Tiers slot for tier 16 — the array is sized 15 — and tier 16
	// 人數 (999) must not show up anywhere.
	for i, tier := range dist.Tiers {
		if tier.Count == 999 {
			t.Errorf("Tiers[%d].Count = 999, want tier 16 dropped at parse time", i)
		}
	}
	if dist.TotalCount != 5 {
		t.Errorf("TotalCount = %d, want 5 (from tier 17)", dist.TotalCount)
	}
}
