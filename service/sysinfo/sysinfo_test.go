package sysinfo_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/service/sysinfo"
)

// fakeCollector is a test double that returns a fixed SysInfo snapshot.
type fakeCollector struct {
	info sysinfo.SysInfo
}

func (f *fakeCollector) Collect() (*sysinfo.SysInfo, error) {
	cp := f.info
	return &cp, nil
}

// errCollector is a test double that always returns an error from Collect.
type errCollector struct{}

func (e *errCollector) Collect() (*sysinfo.SysInfo, error) {
	return nil, errors.New("collect: simulated failure")
}

func newTestRouter(c sysinfo.Collector) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ClientDetector())
	sysinfo.Register(r, c)
	return r
}

func newFakeCollector() *fakeCollector {
	return &fakeCollector{
		info: sysinfo.SysInfo{
			Hostname:      "testhost.example.com",
			OSName:        "TestOS 1.0",
			KernelVersion: "5.15.0-test",
			CPUModel:      "Test CPU Model",
			CPUCores:      4,
			TotalRAMBytes: 16 * 1024 * 1024 * 1024, // 16 GiB
			UptimeSeconds: 90061,                    // 1d 1h 1m
			LoadAvg1:      0.10,
			LoadAvg5:      0.20,
			LoadAvg15:     0.30,
		},
	}
}

// TestHandlerReturnsOK verifies that a GET /sysinfo?format=ascii returns 200
// with a body containing the "SYSINFO" headline area, the hostname, OS name,
// and CPU core count.
func TestHandlerReturnsOK(t *testing.T) {
	r := newTestRouter(newFakeCollector())

	req := httptest.NewRequest(http.MethodGet, "/sysinfo?format=ascii", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	// The "SYSINFO" headline is rendered as Unicode block-letter glyphs by
	// pylon's banner renderer, so we match the caption rows instead of the
	// banner characters. The hostname, OS name, and core count all appear
	// in the borderless caption lines as plain text.
	for _, want := range []string{
		"testhost.example.com",
		"TestOS 1.0",
		"x4", // CPU cores appear as "Test CPU Model x4"
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}

// TestHandlerHTMLIncludesOGTitle verifies that GET /sysinfo?format=html
// returns an HTML page whose og:title meta tag includes the hostname.
func TestHandlerHTMLIncludesOGTitle(t *testing.T) {
	r := newTestRouter(newFakeCollector())

	req := httptest.NewRequest(http.MethodGet, "/sysinfo?format=html", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	body := rec.Body.String()
	// og:title should contain the hostname
	if !strings.Contains(body, "testhost.example.com") {
		t.Errorf("og:title missing hostname\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, `og:title`) {
		t.Errorf("body missing og:title meta tag\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, "SYSINFO") {
		t.Errorf("body missing SYSINFO in og:title\n--- body ---\n%s", body)
	}
}

// TestHandlerCacheControl verifies that every /sysinfo response carries
// Cache-Control: max-age=5.
func TestHandlerCacheControl(t *testing.T) {
	r := newTestRouter(newFakeCollector())

	req := httptest.NewRequest(http.MethodGet, "/sysinfo", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get("Cache-Control"); got != "max-age=5" {
		t.Errorf("Cache-Control = %q, want %q", got, "max-age=5")
	}
}

// TestFormatRAM verifies the formatRAM helper via the exported test alias.
func TestFormatRAM(t *testing.T) {
	cases := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1073741824, "1.0 GiB"},
		{17179869184, "16.0 GiB"},
	}
	for _, tc := range cases {
		got := sysinfo.FormatRAMForTest(tc.bytes)
		if got != tc.want {
			t.Errorf("formatRAM(%d) = %q, want %q", tc.bytes, got, tc.want)
		}
	}
}

// TestFormatUptime verifies the formatUptime helper via the exported test alias.
func TestFormatUptime(t *testing.T) {
	cases := []struct {
		seconds int64
		want    string
	}{
		{0, "0m"},
		{60, "1m"},
		{3661, "1h 1m"},
		{90061, "1d 1h 1m"},
	}
	for _, tc := range cases {
		got := sysinfo.FormatUptimeForTest(tc.seconds)
		if got != tc.want {
			t.Errorf("formatUptime(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

// TestFormatRAM_MiB_TiB extends TestFormatRAM with MiB and TiB boundary values.
func TestFormatRAM_MiB_TiB(t *testing.T) {
	cases := []struct {
		bytes int64
		want  string
	}{
		{1048576, "1.0 MiB"},        // 1 MiB exactly
		{1073741824, "1.0 GiB"},     // 1 GiB exactly
		{1099511627776, "1.0 TiB"},  // 1 TiB exactly
	}
	for _, tc := range cases {
		got := sysinfo.FormatRAMForTest(tc.bytes)
		if got != tc.want {
			t.Errorf("formatRAM(%d) = %q, want %q", tc.bytes, got, tc.want)
		}
	}
}

// TestHandlerInitialCollectFails verifies that Register does not panic when
// the initial Collect returns an error, and that subsequent GET /sysinfo
// requests return 200 (served from the zeroed fallback snapshot).
func TestHandlerInitialCollectFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ClientDetector())
	// Register with a collector that always fails — must not panic.
	sysinfo.Register(r, &errCollector{})

	req := httptest.NewRequest(http.MethodGet, "/sysinfo?format=ascii", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (handler should serve zero fallback)\nbody: %s", rec.Code, rec.Body.String())
	}
}

// TestHandlerPylonSource verifies that GET /sysinfo with Accept: text/pylon
// returns Content-Type text/pylon and status 200.
func TestHandlerPylonSource(t *testing.T) {
	r := newTestRouter(newFakeCollector())

	req := httptest.NewRequest(http.MethodGet, "/sysinfo", nil)
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

// TestHandlerSVG verifies that GET /sysinfo?format=svg returns
// Content-Type image/svg+xml and status 200.
func TestHandlerSVG(t *testing.T) {
	r := newTestRouter(newFakeCollector())

	req := httptest.NewRequest(http.MethodGet, "/sysinfo?format=svg", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "image/svg+xml") {
		t.Errorf("Content-Type = %q, want \"image/svg+xml\"", ct)
	}
}
