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

func TestFetchOTCListingInfoExact_Fixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "listinginfo_otc_6488_5483_3105.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetOTCListingInfoEndpoint(srv.URL)

	dump, found, err := p.FetchOTCListingInfoExact(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true")
	}

	// 6488 環球晶 — semiconductor (industry 24), listed 2015-09-25.
	gw := dump.Rows["6488"]
	if gw.IndustryCode != "24" {
		t.Errorf("6488 IndustryCode = %q, want 24", gw.IndustryCode)
	}
	if gw.IndustryName != "半導體業" {
		t.Errorf("6488 IndustryName = %q, want 半導體業 (reusing TWSE static map)", gw.IndustryName)
	}
	if gw.Name != "環球晶" {
		t.Errorf("6488 Name = %q, want 環球晶", gw.Name)
	}
	if got := gw.ListingDate.Format("2006-01-02"); got != "2015-09-25" {
		t.Errorf("6488 ListingDate = %s, want 2015-09-25", got)
	}
}

func TestFetchOTCListingInfoExact_EmptyEndpoint(t *testing.T) {
	p := &HTTPProvider{}
	_, _, err := p.FetchOTCListingInfoExact(context.Background(), time.Now())
	if err != ErrUnavailable {
		t.Errorf("err = %v, want ErrUnavailable when endpoint unset", err)
	}
}

func TestCachedOTCListingInfo_FetchOnceServesMany(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "listinginfo_otc_6488_5483_3105.json"))
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
	p.SetOTCListingInfoEndpoint(srv.URL)
	c := NewCachedOTCListingInfo(p, 0)

	ctx := context.Background()
	if _, err := c.GetOTCListingInfo(ctx, "6488", time.Now()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if _, err := c.GetOTCListingInfo(ctx, "5483", time.Now()); err != nil {
		t.Errorf("second call: %v", err)
	}
	_, err = c.GetOTCListingInfo(ctx, "9999", time.Now())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("9999 (unknown OTC) err = %v, want ErrUnavailable", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("upstream calls = %d, want 1 (cache should serve repeat lookups)", got)
	}
}
