package twse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fundamentalsFixture mimics a 3-row BWIBBU_d response covering the
// canonical TSMC sample plus a loss-maker + a no-dividend stock to
// exercise the parseTWSEFloat "-" → 0 branch.
const fundamentalsFixture = `[
  {"Date":"20260506","Code":"2330","Name":"台積電",
   "ClosePrice":"2250.00","DividendYield":"0.98","DividendYear":"114",
   "PEratio":"33.97","PBratio":"10.77","FiscalYearQuarter":"2025Q4"},
  {"Date":"20260506","Code":"2618","Name":"長榮航",
   "ClosePrice":"45.30","DividendYield":"-","DividendYear":"-",
   "PEratio":"-","PBratio":"1.42","FiscalYearQuarter":"2025Q4"},
  {"Date":"20260506","Code":"0050","Name":"元大台灣50",
   "ClosePrice":"185.55","DividendYield":"4.21","DividendYear":"114",
   "PEratio":"22.10","PBratio":"3.45","FiscalYearQuarter":"2025Q4"}
]`

func TestFetchFundamentalsExact_Fixture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fundamentalsFixture))
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetFundamentalsEndpoint(srv.URL)

	dump, found, err := p.FetchFundamentalsExact(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true")
	}
	if got := len(dump.Rows); got != 3 {
		t.Errorf("len(Rows) = %d, want 3", got)
	}

	tsmc := dump.Rows["2330"]
	if tsmc.DividendYield != 0.98 || tsmc.PERatio != 33.97 || tsmc.PBRatio != 10.77 {
		t.Errorf("2330 metrics wrong: %+v", tsmc)
	}
	if tsmc.FiscalYearQuarter != "2025Q4" {
		t.Errorf("2330 FiscalYearQuarter = %q, want 2025Q4", tsmc.FiscalYearQuarter)
	}

	// Loss-maker / non-dividend stock: "-" → 0 (parseTWSEFloat tolerance).
	eva := dump.Rows["2618"]
	if eva.DividendYield != 0 || eva.PERatio != 0 {
		t.Errorf("2618 should have DividendYield=0 PERatio=0 from %q upstream, got %+v", "-", eva)
	}
	if eva.PBRatio != 1.42 {
		t.Errorf("2618 PBRatio = %v, want 1.42", eva.PBRatio)
	}
}

func TestFetchFundamentalsExact_EmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetFundamentalsEndpoint(srv.URL)

	_, found, err := p.FetchFundamentalsExact(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if found {
		t.Errorf("found = true, want false on empty array")
	}
}

func TestCachedFundamentals_FetchOnceServesMany(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fundamentalsFixture))
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetFundamentalsEndpoint(srv.URL)
	c := NewCachedFundamentals(p, 0)

	ctx := context.Background()
	if _, err := c.GetFundamentals(ctx, "2330", time.Now()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := c.GetFundamentals(ctx, "0050", time.Now()); err != nil {
		t.Errorf("0050 lookup: %v", err)
	}
	if _, err := c.GetFundamentals(ctx, "9999", time.Now()); err != ErrUnavailable {
		t.Errorf("9999 lookup err = %v, want ErrUnavailable", err)
	}
	if calls != 1 {
		t.Errorf("upstream calls = %d, want 1", calls)
	}
}
