package twse

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFetchRevenueExact_TWSEFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "revenue_listed_2330_2317_2454.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetRevenueListedEndpoint(srv.URL)

	dump, found, err := p.FetchRevenueExact(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true")
	}

	tsmc := dump.Rows["2330"]
	if tsmc.Industry != "半導體業" {
		t.Errorf("2330 Industry = %q, want 半導體業", tsmc.Industry)
	}
	if tsmc.YearMonth != "11503" {
		t.Errorf("2330 YearMonth = %q, want 11503", tsmc.YearMonth)
	}
	// 415,191,699 千元 × 1000 = 415,191,699,000 NTD
	if tsmc.CurrentTWD != 415_191_699_000 {
		t.Errorf("2330 CurrentTWD = %d, want 415191699000 (千元 × 1000)", tsmc.CurrentTWD)
	}
	// YoY pre-computed; verify within rounding tolerance.
	if tsmc.YoYPct < 45.0 || tsmc.YoYPct > 46.0 {
		t.Errorf("2330 YoYPct = %v, want ~45.19", tsmc.YoYPct)
	}
}

func TestFetchOTCRevenueExact_Fixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "revenue_otc_6488_5483_3105.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetRevenueOTCEndpoint(srv.URL)

	dump, found, err := p.FetchOTCRevenueExact(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true")
	}
	gw := dump.Rows["6488"]
	if gw.Name != "環球晶" {
		t.Errorf("6488 Name = %q, want 環球晶", gw.Name)
	}
	// 5,445,917 千元 × 1000 = 5,445,917,000 NTD ≈ 54.5億.
	if gw.CurrentTWD != 5_445_917_000 {
		t.Errorf("6488 CurrentTWD = %d, want 5445917000", gw.CurrentTWD)
	}
	// YoY ≈ 0.09% (essentially flat).
	if gw.YoYPct < -1.0 || gw.YoYPct > 1.0 {
		t.Errorf("6488 YoYPct = %v, want ~0.09", gw.YoYPct)
	}
}

func TestFetchRevenueExact_EmptyEndpoints(t *testing.T) {
	p := &HTTPProvider{}
	if _, _, err := p.FetchRevenueExact(context.Background(), time.Now()); err != ErrUnavailable {
		t.Errorf("listed err = %v, want ErrUnavailable", err)
	}
	if _, _, err := p.FetchOTCRevenueExact(context.Background(), time.Now()); err != ErrUnavailable {
		t.Errorf("OTC err = %v, want ErrUnavailable", err)
	}
}

func TestCachedRevenue_TWSEAndOTC(t *testing.T) {
	listedBody, _ := os.ReadFile(filepath.Join("testdata", "revenue_listed_2330_2317_2454.json"))
	otcBody, _ := os.ReadFile(filepath.Join("testdata", "revenue_otc_6488_5483_3105.json"))

	listedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(listedBody)
	}))
	defer listedSrv.Close()
	otcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(otcBody)
	}))
	defer otcSrv.Close()

	p := NewWithEndpoints("", "", "", listedSrv.Client())
	p.SetRevenueListedEndpoint(listedSrv.URL)
	p.SetRevenueOTCEndpoint(otcSrv.URL)

	cListed := NewCachedRevenue(p, 0)
	cOTC := NewCachedOTCRevenue(p, 0)
	ctx := context.Background()

	if _, err := cListed.GetRevenue(ctx, "2330", time.Now()); err != nil {
		t.Errorf("listed 2330: %v", err)
	}
	if _, err := cOTC.GetOTCRevenue(ctx, "6488", time.Now()); err != nil {
		t.Errorf("OTC 6488: %v", err)
	}
	// Cross-listing miss: 6488 isn't in listed file.
	_, err := cListed.GetRevenue(ctx, "6488", time.Now())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("listed 6488 err = %v, want ErrUnavailable (OTC stock missing from TWSE-listed file)", err)
	}
}
