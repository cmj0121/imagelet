package twse

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchListingInfoExact_Fixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "listinginfo_2330_0050_2317.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetListingInfoEndpoint(srv.URL)

	dump, found, err := p.FetchListingInfoExact(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true")
	}

	tsmc := dump.Rows["2330"]
	if tsmc.IndustryCode != "24" {
		t.Errorf("2330 IndustryCode = %q, want 24", tsmc.IndustryCode)
	}
	if tsmc.IndustryName != "半導體業" {
		t.Errorf("2330 IndustryName = %q, want 半導體業", tsmc.IndustryName)
	}
	if tsmc.Name != "台積電" {
		t.Errorf("2330 Name = %q, want 台積電", tsmc.Name)
	}
	if got := tsmc.ListingDate.Format("2006-01-02"); got != "1994-09-05" {
		t.Errorf("2330 ListingDate = %s, want 1994-09-05", got)
	}

	hh := dump.Rows["2317"]
	if hh.IndustryCode != "31" || hh.IndustryName != "其他電子業" {
		t.Errorf("2317 IndustryCode/Name = %q/%q, want 31/其他電子業", hh.IndustryCode, hh.IndustryName)
	}

	// 0050 is an ETF — not in the company-info list. Lookup falls
	// through to ErrUnavailable via the cache wrapper below.
	if _, ok := dump.Rows["0050"]; ok {
		t.Errorf("0050 unexpectedly present in t187ap03_L (ETFs should be absent)")
	}
}

func TestFetchListingInfoExact_EmptyEndpoint(t *testing.T) {
	p := &HTTPProvider{}
	_, _, err := p.FetchListingInfoExact(context.Background(), time.Now())
	if err != ErrUnavailable {
		t.Errorf("err = %v, want ErrUnavailable when endpoint unset", err)
	}
}

// TestIndustryNameOf pins the 33-row map: every code that appears in
// production fixtures must resolve, with a few representative spot-checks.
func TestIndustryNameOf(t *testing.T) {
	cases := map[string]string{
		"24": "半導體業",
		"17": "金融保險業",
		"01": "水泥工業",
		"91": "存託憑證",
		"99": "", // unmapped → empty fallthrough
		"":   "", // empty → empty
	}
	for code, want := range cases {
		if got := industryNameOf(code); got != want {
			t.Errorf("industryNameOf(%q) = %q, want %q", code, got, want)
		}
	}
}

// TestCachedListingInfo_FetchOnceServesMany pins singleflight + cache.
func TestCachedListingInfo_FetchOnceServesMany(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "listinginfo_2330_0050_2317.json"))
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
	p.SetListingInfoEndpoint(srv.URL)
	c := NewCachedListingInfo(p, 0)

	ctx := context.Background()
	if _, err := c.GetListingInfo(ctx, "2330", time.Now()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := c.GetListingInfo(ctx, "2317", time.Now()); err != nil {
		t.Errorf("second call: %v", err)
	}
	_, err = c.GetListingInfo(ctx, "0050", time.Now())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("0050 (ETF) lookup err = %v, want ErrUnavailable", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("upstream calls = %d, want 1 (cache should serve repeat lookups)", got)
	}
}
