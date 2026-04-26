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

func TestRootBrowserGetsPNG(t *testing.T) {
	r := newRouter("v1.2.3")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")

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
