package twse_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/imagelet/service/stock/twse"
)

func newPCRServer(t *testing.T, byDate map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		date := r.FormValue("queryStartDate")
		csv, ok := byDate[date]
		if !ok {
			w.Header().Set("Content-Type", "text/html;charset=UTF-8")
			_, _ = w.Write([]byte(`<html><body><script>alert("查無資料");</script></body></html>`))
			return
		}
		w.Header().Set("Content-Type", "text/csv;charset=ISO-8859-1")
		_, _ = w.Write([]byte(csv))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGetOptionsPCRReturnsRow(t *testing.T) {
	body := "header\r\n2026/04/24,521044,434960,119.79,73266,41676,175.80,\r\n"
	srv := newPCRServer(t, map[string]string{"2026/04/24": body})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXMarketEndpoints(srv.URL, "")

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")
	got, err := p.GetOptionsPCR(context.Background(), asOf)
	if err != nil {
		t.Fatalf("GetOptionsPCR: %v", err)
	}
	if got.OIRatio != 175.80 {
		t.Errorf("OIRatio = %v, want 175.80", got.OIRatio)
	}
	if got.VolumeRatio != 119.79 {
		t.Errorf("VolumeRatio = %v, want 119.79", got.VolumeRatio)
	}
	if !got.Has() {
		t.Error("Has() should be true")
	}
}

func TestGetOptionsPCRLooksBackOnNoData(t *testing.T) {
	body := "header\r\n2026/04/24,1,1,100.00,1,1,200.00,\r\n"
	// Sun 04/26 + Sat 04/25 are no-data; Fri 04/24 has the row.
	srv := newPCRServer(t, map[string]string{"2026/04/24": body})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXMarketEndpoints(srv.URL, "")

	asOf, _ := time.Parse("2006-01-02", "2026-04-26")
	got, err := p.GetOptionsPCR(context.Background(), asOf)
	if err != nil {
		t.Fatalf("GetOptionsPCR: %v", err)
	}
	if got.OIRatio != 200.00 {
		t.Errorf("OIRatio = %v, want 200.00", got.OIRatio)
	}
}

func TestGetOptionsPCRDisabledWhenEndpointUnset(t *testing.T) {
	p := twse.NewWithEndpoints("", "", "", &http.Client{})
	_, err := p.GetOptionsPCR(context.Background(), time.Now())
	if !errors.Is(err, twse.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable when PCR endpoint unset", err)
	}
}

// vixServer serves monthly VIX dumps keyed by YYYYMM. Missing months
// 404, mirroring TAIFEX's behavior at the start of a fresh year before
// the first trading day fills the file.
func newVIXServer(t *testing.T, byMonth map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// path looks like /file/.../202604new.txt — extract the YYYYMM stem.
		i := strings.LastIndex(r.URL.Path, "/")
		j := strings.Index(r.URL.Path[i+1:], "new.txt")
		if i < 0 || j < 0 {
			http.NotFound(w, r)
			return
		}
		stem := r.URL.Path[i+1 : i+1+j]
		body, ok := byMonth[stem]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGetVIXReturnsLatestRowAtOrBeforeAsOf(t *testing.T) {
	body := "header line\r\n--------\r\n" +
		"20260401\t13450000\t34.85\t34.85\r\n" +
		"20260402\t13450000\t36.45\t36.45\r\n" +
		"20260403\t13450000\t29.50\t29.49\r\n"
	srv := newVIXServer(t, map[string]string{"202604": body})
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXMarketEndpoints("", srv.URL+"/%snew.txt")

	asOf, _ := time.Parse("2006-01-02", "2026-04-02")
	got, err := p.GetVIX(context.Background(), asOf)
	if err != nil {
		t.Fatalf("GetVIX: %v", err)
	}
	if got.Value != 36.45 {
		t.Errorf("Value = %v, want 36.45 (latest row ≤ asOf)", got.Value)
	}
}

func TestGetVIXFallsBackToPreviousMonth(t *testing.T) {
	prevBody := "header\r\n--------\r\n" +
		"20260331\t13450000\t40.20\t40.20\r\n"
	srv := newVIXServer(t, map[string]string{"202603": prevBody}) // 202604 missing
	p := twse.NewWithEndpoints("", "", "", srv.Client())
	p.SetTAIFEXMarketEndpoints("", srv.URL+"/%snew.txt")

	asOf, _ := time.Parse("2006-01-02", "2026-04-01")
	got, err := p.GetVIX(context.Background(), asOf)
	if err != nil {
		t.Fatalf("GetVIX: %v", err)
	}
	if got.Value != 40.20 {
		t.Errorf("Value = %v, want 40.20 (rolled back to previous month)", got.Value)
	}
}

func TestGetVIXDisabledWhenTemplateUnset(t *testing.T) {
	p := twse.NewWithEndpoints("", "", "", &http.Client{})
	_, err := p.GetVIX(context.Background(), time.Now())
	if !errors.Is(err, twse.ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable when VIX template unset", err)
	}
}

func TestCachedPCRAndVIXPassthrough(t *testing.T) {
	pcrBody := "header\r\n2026/04/24,1,1,100.00,1,1,150.00,\r\n"
	vixBody := "header\r\n--------\r\n20260424\t13450000\t25.50\t25.50\r\n"
	pcrSrv := newPCRServer(t, map[string]string{"2026/04/24": pcrBody})
	vixSrv := newVIXServer(t, map[string]string{"202604": vixBody})

	inner := twse.NewWithEndpoints("", "", "", pcrSrv.Client())
	inner.SetTAIFEXMarketEndpoints(pcrSrv.URL, vixSrv.URL+"/%snew.txt")
	cached := twse.NewCached(inner)

	asOf, _ := time.Parse("2006-01-02", "2026-04-24")

	pcr, err := cached.GetOptionsPCR(context.Background(), asOf)
	if err != nil {
		t.Fatalf("Cached.GetOptionsPCR: %v", err)
	}
	if pcr.OIRatio != 150.0 {
		t.Errorf("OIRatio = %v, want 150.0", pcr.OIRatio)
	}

	vix, err := cached.GetVIX(context.Background(), asOf)
	if err != nil {
		t.Fatalf("Cached.GetVIX: %v", err)
	}
	if vix.Value != 25.5 {
		t.Errorf("Value = %v, want 25.5", vix.Value)
	}
}
