package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/middleware/reqcounter"
	"github.com/cmj0121/imagelet/service/metrics"
)

// newTestRouter builds a gin engine with the reqcounter middleware and /metrics
// registered on it. The router also wires a couple of stub GET routes so the
// counter has something to snapshot when /metrics is exercised.
func newTestRouter(ctr *reqcounter.Counter) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ClientDetector())
	r.Use(ctr.Middleware())
	r.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/hello", func(c *gin.Context) { c.Status(http.StatusOK) })
	metrics.Register(r, ctr)
	return r
}

// seedRoutes drives a few requests so the counter has non-zero entries before
// /metrics is inspected.
func seedRoutes(r *gin.Engine) {
	for range 3 {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ping", nil))
	}
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/hello", nil))
}

// TestHandlerReturnsOK verifies that GET /metrics?format=ascii returns 200
// and that the body contains at least one of the seeded route names.
func TestHandlerReturnsOK(t *testing.T) {
	ctr := reqcounter.New()
	r := newTestRouter(ctr)
	seedRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/metrics?format=ascii", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/ping") {
		t.Errorf("body missing \"/ping\"\n--- body ---\n%s", body)
	}
}

// TestHandlerCacheControlNoStore verifies that every /metrics response carries
// Cache-Control: no-store.
func TestHandlerCacheControlNoStore(t *testing.T) {
	ctr := reqcounter.New()
	r := newTestRouter(ctr)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want \"no-store\"", got)
	}
}

// TestHandlerHTMLHasOGTitle verifies that GET /metrics?format=html returns an
// HTML page whose og:title meta tag contains "METRICS".
func TestHandlerHTMLHasOGTitle(t *testing.T) {
	ctr := reqcounter.New()
	r := newTestRouter(ctr)
	seedRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/metrics?format=html", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "og:title") {
		t.Errorf("body missing og:title meta tag\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, "METRICS") {
		t.Errorf("og:title does not contain \"METRICS\"\n--- body ---\n%s", body)
	}
}

// TestHandlerPylonSource verifies that a request with Accept: text/pylon
// receives Content-Type text/pylon and status 200.
func TestHandlerPylonSource(t *testing.T) {
	ctr := reqcounter.New()
	r := newTestRouter(ctr)
	seedRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Accept", "text/pylon")
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/pylon") {
		t.Errorf("Content-Type = %q, want \"text/pylon\"", ct)
	}
}

// TestFormatCount verifies the formatCount helper via the exported test alias.
func TestFormatCount(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{1234567, "1,234,567"},
	}
	for _, tc := range cases {
		got := metrics.FormatCountForTest(tc.n)
		if got != tc.want {
			t.Errorf("formatCount(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestFormatUptime verifies the formatUptime helper via the exported test alias.
func TestFormatUptime(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "< 1m"},
		{65 * time.Second, "1m"},
		{3661 * time.Second, "1h 1m"},
		{90061 * time.Second, "25h 1m"},
	}
	for _, tc := range cases {
		got := metrics.FormatUptimeForTest(tc.d)
		if got != tc.want {
			t.Errorf("formatUptime(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
