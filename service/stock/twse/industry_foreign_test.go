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

func TestFetchIndustryForeignExact_Fixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "qfiis_cat.json"))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := NewWithEndpoints("", "", "", srv.Client())
	p.SetIndustryForeignEndpoint(srv.URL)

	dump, found, err := p.FetchIndustryForeignExact(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !found {
		t.Fatalf("found = false, want true")
	}
	if got := len(dump.Rows); got < 30 {
		t.Errorf("len(Rows) = %d, want >=30 (live has 36 industries + ETF)", got)
	}

	// Spot-check the semiconductor row — heavy foreign concentration.
	semi := dump.Rows["半導體業"]
	if semi.Industry != "半導體業" {
		t.Errorf("半導體業 row missing or mis-keyed: %+v", semi)
	}
	if semi.HoldingPct < 30 || semi.HoldingPct > 70 {
		t.Errorf("半導體業 HoldingPct = %v, want roughly 30-70 (sanity range)", semi.HoldingPct)
	}

	// ETF aggregate is a separate row by industry name "ETF".
	etf := dump.Rows["ETF"]
	if !etf.Has() {
		t.Errorf("ETF aggregate row missing")
	}
}

func TestFetchIndustryForeignExact_EmptyEndpoint(t *testing.T) {
	p := &HTTPProvider{}
	_, _, err := p.FetchIndustryForeignExact(context.Background(), time.Now())
	if err != ErrUnavailable {
		t.Errorf("err = %v, want ErrUnavailable when endpoint unset", err)
	}
}

func TestCachedIndustryForeign_FetchOnceServesMany(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "qfiis_cat.json"))
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
	p.SetIndustryForeignEndpoint(srv.URL)
	c := NewCachedIndustryForeign(p, 0)

	ctx := context.Background()
	// Direct match against the canonical name.
	if _, err := c.GetIndustryForeign(ctx, "半導體業", time.Now()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Pin the 業-suffix fallback path: "金融保險業" (our canonical
	// name) → "金融保險" (MI_QFIIS_cat name, no 業 suffix).
	if _, err := c.GetIndustryForeign(ctx, "金融保險業", time.Now()); err != nil {
		t.Errorf("業-suffix fallback: %v", err)
	}
	_, err = c.GetIndustryForeign(ctx, "存在嗎不存在", time.Now())
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("unknown industry err = %v, want ErrUnavailable", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("upstream calls = %d, want 1 (cache should serve repeat lookups)", got)
	}
}
