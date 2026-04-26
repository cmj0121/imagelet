package notfound_test

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/service/notfound"
)

// newRouter builds a minimal engine with the ClientDetector middleware
// (so the PNG/ASCII negotiation works) and notfound.Register installed.
// Tests intentionally don't pull in server.New() — that would couple
// service tests to the full middleware chain.
func newRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ClientDetector())
	notfound.Register(r)
	return r
}

func TestUnknownPathReturns404(t *testing.T) {
	r := newRouter()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/no-such-route", nil)

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store")
	}
}

func TestASCIIBodyContainsBannerAndTraceback(t *testing.T) {
	r := newRouter()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing/page", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")

	r.ServeHTTP(rec, req)

	body := rec.Body.String()
	ct := rec.Header().Get("Content-Type")

	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain prefix", ct)
	}
	// Banner is rendered with pylon's native Unicode frame in the default
	// theme. Pin a recognizable structural fragment.
	if !strings.Contains(body, "│") {
		t.Errorf("body missing banner frame char")
	}
	// Traceback shape — pin the canonical first line.
	if !strings.Contains(body, "Traceback (most recent call last):") {
		t.Errorf("body missing traceback header; got:\n%s", body)
	}
	// Path injection — the requested path must appear in both the
	// KeyError message and the trailing path: field.
	if !strings.Contains(body, "KeyError: '/missing/page'") {
		t.Errorf("body missing KeyError with path; got:\n%s", body)
	}
	if !strings.Contains(body, "path:   /missing/page") {
		t.Errorf("body missing trailing path field; got:\n%s", body)
	}
	// Modern Python source-pointer hint.
	if !strings.Contains(body, "^^^^^^^^^^^^^^^^^^^^^^^^^") {
		t.Errorf("body missing source-pointer hint")
	}
}

func TestBrowserGetsPNGWithBannerAndTraceback(t *testing.T) {
	r := newRouter()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
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

	// Pin that the PNG is the composed banner+traceback variant, not
	// the older banner-only one. Pylon's bare 404 banner renders at
	// ~140 px tall; the composed canvas adds the traceback below, so
	// any height clearly above the banner-only baseline confirms the
	// composition pass ran. 300 px is a comfortable threshold.
	cfg, err := png.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decode png config: %v", err)
	}
	if cfg.Height < 300 {
		t.Errorf("png height = %d, want >= 300 (banner-only is ~140; composed should clear it)", cfg.Height)
	}
}

func TestAcceptPylonReturnsBareSource(t *testing.T) {
	r := newRouter()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	req.Header.Set("Accept", "text/pylon")

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/pylon" {
		t.Errorf("Content-Type = %q, want text/pylon", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "[ 404 | banner ]") {
		t.Errorf("body = %q, want banner source", body)
	}
	// The pylon path is bare banner — no traceback. Pin that.
	if strings.Contains(body, "Traceback") {
		t.Errorf("pylon body should not contain traceback prose; got:\n%s", body)
	}
}

func TestRootPathFallback(t *testing.T) {
	// Edge: empty / root-ish paths — gin always passes "/" or longer for
	// HTTP requests, but the sanitizer also handles a literally empty
	// URL.Path defensively.
	r := newRouter()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")

	// Note: GET / IS a registered route in the real server.New() chain
	// (returns 200), but in this isolated test newRouter() doesn't
	// register it, so / falls through to NoRoute.
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "path:   /") {
		t.Errorf("body missing path: / field; got:\n%s", rec.Body.String())
	}
}

func TestControlCharsStrippedFromPath(t *testing.T) {
	// ANSI-escape / NUL injection guard. The httptest router decodes
	// %-encoded bytes back to runes before URL.Path is read; pin that
	// nothing escapes into the rendered traceback.
	r := newRouter()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/foo%1bbar", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")

	r.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "\x1b") {
		t.Errorf("body contains raw ESC byte (should be stripped)")
	}
	// The visible chars survive.
	if !strings.Contains(body, "/foobar") {
		t.Errorf("body missing sanitized path; got:\n%s", body)
	}
}

func TestMethodReflectedInTraceback(t *testing.T) {
	r := newRouter()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/missing", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")

	r.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "method: POST") {
		t.Errorf("body missing method: POST; got:\n%s", rec.Body.String())
	}
}
