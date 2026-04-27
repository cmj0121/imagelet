package index_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/service/index"
)

// newRouter builds a minimal engine with the ClientDetector middleware
// (so PNG/ASCII negotiation works) and index.Register installed at /.
func newRouter(version string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ClientDetector())
	index.Register(r, version)
	return r
}

func TestRootASCIIHasBannerAndCaptions(t *testing.T) {
	r := newRouter("v1.2.3")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain prefix", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Errorf("Cache-Control = %q, want public, max-age=3600", got)
	}

	body := rec.Body.String()
	// Banner frame char (native theme uses Unicode box-drawing).
	if !strings.Contains(body, "│") {
		t.Errorf("body missing banner frame char")
	}
	if !strings.Contains(body, index.DefaultTagline) {
		t.Errorf("body missing tagline %q; got:\n%s", index.DefaultTagline, body)
	}
	if !strings.Contains(body, index.DefaultRepo) {
		t.Errorf("body missing repo URL; got:\n%s", body)
	}
	if !strings.Contains(body, "v1.2.3") {
		t.Errorf("body missing version; got:\n%s", body)
	}
}

func TestRootBrowserGetsHTML(t *testing.T) {
	r := newRouter("v1.2.3")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
	body := rec.Body.String()
	for _, sub := range []string{"<!DOCTYPE html>", "<svg ", "</svg>", "</html>"} {
		if !strings.Contains(body, sub) {
			t.Errorf("HTML body missing %q\n--- body ---\n%s", sub, body)
		}
	}
}

func TestRootFormatPNG(t *testing.T) {
	r := newRouter("v1.2.3")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?format=png", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	body := rec.Body.Bytes()
	if len(body) < 8 {
		t.Fatalf("body too short for PNG (%d bytes)", len(body))
	}
	pngMagic := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	for i, b := range pngMagic {
		if body[i] != b {
			t.Fatalf("byte %d = 0x%02x, want 0x%02x (PNG magic mismatch)", i, body[i], b)
		}
	}
}

func TestRootFormatPylonQueryEqualsAcceptHeader(t *testing.T) {
	// ?format=pylon should produce the same response shape as
	// Accept: text/pylon — header/query parity.
	r := newRouter("v1.2.3")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?format=pylon", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/pylon" {
		t.Errorf("Content-Type = %q, want text/pylon", got)
	}
	if !strings.Contains(rec.Body.String(), "[ IMAGELET | banner ]") {
		t.Errorf("body missing banner source line; got:\n%s", rec.Body.String())
	}
}

func TestRootFormatAscii(t *testing.T) {
	// ?format=ascii overrides Mozilla UA's HTML default.
	r := newRouter("v1.2.3")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?format=ascii", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain prefix", got)
	}
}

func TestRootFormatHTMLOverridesUA(t *testing.T) {
	// ?format=html wins over curl UA (which would default to ASCII).
	r := newRouter("v1.2.3")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?format=html", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
}

func TestRootAcceptPylonReturnsRawSource(t *testing.T) {
	r := newRouter("v1.2.3")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Accept", "text/pylon")

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/pylon" {
		t.Errorf("Content-Type = %q, want text/pylon", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "[ IMAGELET | banner ]") {
		t.Errorf("body missing banner source line; got:\n%s", body)
	}
	if !strings.Contains(body, "( "+index.DefaultTagline+" )") {
		t.Errorf("body missing tagline borderless box; got:\n%s", body)
	}
	if !strings.Contains(body, index.DefaultRepo+" · v1.2.3") {
		t.Errorf("body missing repo · version; got:\n%s", body)
	}
}

func TestRootFormatSVGOverridesUA(t *testing.T) {
	// ?format=svg wins over a non-Mozilla UA (would otherwise resolve to ASCII)
	// and over a Mozilla UA (would otherwise resolve to PNG). Pin both.
	r := newRouter("v1.2.3")

	for _, ua := range []string{"curl/8.4.0", "Mozilla/5.0"} {
		t.Run(ua, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/?format=svg", nil)
			req.Header.Set("User-Agent", ua)
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
				t.Errorf("Content-Type = %q, want image/svg+xml", got)
			}
			body := rec.Body.String()
			if !strings.Contains(body, `xmlns="http://www.w3.org/2000/svg"`) {
				t.Errorf("body missing xmlns; got:\n%s", body)
			}
			if !strings.Contains(body, "<svg ") || !strings.Contains(body, "</svg>") {
				t.Errorf("body not bracketed by <svg> tags; got:\n%s", body)
			}
			// GitHub-dark palette must be applied (PaintSVG runs at init).
			if !strings.Contains(body, `fill="#0d1117"`) {
				t.Errorf("body missing dark background fill; got:\n%s", body)
			}
		})
	}
}

func TestRootDefaultVersionWhenEmpty(t *testing.T) {
	// Edge case: ldflags injection might fail or be skipped. Empty
	// version still renders — just shows nothing after the dot
	// separator, which is a clear "not stamped" signal to the operator.
	r := newRouter("")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
