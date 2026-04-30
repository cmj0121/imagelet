package favicon_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/service/favicon"
)

func newRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	favicon.Register(r)
	return r
}

func TestFaviconICO(t *testing.T) {
	r := newRouter()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/x-icon" {
		t.Errorf("Content-Type = %q, want %q", got, "image/x-icon")
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Errorf("Cache-Control = %q, want public,max-age=86400", got)
	}
	body := rec.Body.Bytes()
	if len(body) == 0 {
		t.Fatal("body is empty")
	}
	// ICO file magic: bytes 0..1 are reserved (0x00 0x00), bytes 2..3 are
	// type=1 (icon, little-endian), bytes 4..5 are image count > 0.
	if !bytes.HasPrefix(body, []byte{0x00, 0x00, 0x01, 0x00}) {
		t.Errorf("body does not start with ICO magic; got % x", body[:4])
	}
}

func TestFaviconSVG(t *testing.T) {
	r := newRouter()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want %q", got, "image/svg+xml")
	}
	body := rec.Body.String()
	if len(body) == 0 {
		t.Fatal("body is empty")
	}
	if !bytes.Contains([]byte(body), []byte("<svg")) {
		t.Errorf("body missing <svg root: %q", body)
	}
}
