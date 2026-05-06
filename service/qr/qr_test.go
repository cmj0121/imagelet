package qr_test

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
	"github.com/cmj0121/imagelet/service/qr"
)

func newRouter() *gin.Engine {
	r := gin.New()
	r.Use(middleware.ClientDetector())
	qr.Register(r)
	return r
}

// do runs a request with the given UA and query, returns the recorder.
func do(t *testing.T, ua, query string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := newRouter()
	path := "/qr"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestDefaultEncodes(t *testing.T) {
	// No UA + no ?format= → unfurl-bot/default branch → PNG.
	rec := do(t, "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if _, err := png.Decode(bytes.NewReader(rec.Body.Bytes())); err != nil {
		t.Errorf("response body is not a valid PNG: %v", err)
	}
	want, err := render.QR(qr.DefaultText, render.QRMedium, render.ModePNG)
	if err != nil {
		t.Fatalf("render.QR: %v", err)
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("default response does not match render.QR(DefaultText, QRMedium, ModePNG); body len=%d want len=%d",
			rec.Body.Len(), len(want))
	}
}

func TestTextParam(t *testing.T) {
	rec := do(t, "curl/8.4.0", "text=foo&format=svg")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", got)
	}
	want, err := render.QR("foo", render.QRMedium, render.ModeSVG)
	if err != nil {
		t.Fatalf("render.QR: %v", err)
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("svg body mismatch; got len=%d want len=%d", rec.Body.Len(), len(want))
	}
}

func TestECLevels(t *testing.T) {
	cases := []struct {
		query string
		ec    render.QRLevel
	}{
		{"ec=l&format=svg", render.QRLow},
		{"ec=L&format=svg", render.QRLow},
		{"ec=m&format=svg", render.QRMedium},
		{"ec=M&format=svg", render.QRMedium},
		{"ec=q&format=svg", render.QRQuartile},
		{"ec=Q&format=svg", render.QRQuartile},
		{"ec=h&format=svg", render.QRHigh},
		{"ec=H&format=svg", render.QRHigh},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			rec := do(t, "curl/8.4.0", c.query)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			want, err := render.QR(qr.DefaultText, c.ec, render.ModeSVG)
			if err != nil {
				t.Fatalf("render.QR: %v", err)
			}
			if !bytes.Equal(rec.Body.Bytes(), want) {
				t.Errorf("ec=%v body mismatch", c.ec)
			}
		})
	}
}

func TestECBadValue(t *testing.T) {
	// ?ec=z (unknown) → ParseQRLevel falls back to QRMedium; no 4xx.
	rec := do(t, "curl/8.4.0", "ec=z&format=svg")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	want, err := render.QR(qr.DefaultText, render.QRMedium, render.ModeSVG)
	if err != nil {
		t.Fatalf("render.QR: %v", err)
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Error("ec=z should fall back to QRMedium")
	}
}

func TestOversizeTextRejected(t *testing.T) {
	big := strings.Repeat("a", qr.MaxQRTextBytes+1)
	rec := do(t, "curl/8.4.0", "text="+big)
	if rec.Code != http.StatusRequestURITooLong {
		t.Errorf("status = %d, want 414", rec.Code)
	}
}

func TestPylonSourceUnsupported(t *testing.T) {
	t.Run("query", func(t *testing.T) {
		rec := do(t, "curl/8.4.0", "format=pylon")
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("status = %d, want 415", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "not supported") {
			t.Errorf("body missing redirect message; got %q", rec.Body.String())
		}
	})
	t.Run("accept", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		r := newRouter()
		req := httptest.NewRequest(http.MethodGet, "/qr", nil)
		req.Header.Set("User-Agent", "curl/8.4.0")
		req.Header.Set("Accept", "text/pylon")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("status = %d, want 415", rec.Code)
		}
	})
}

func TestVaryHeader(t *testing.T) {
	// Every successful response carries Vary: User-Agent — the R1 fix.
	cases := []struct {
		ua, query string
	}{
		{"curl/8.4.0", ""},
		{"Mozilla/5.0", ""},
		{"curl/8.4.0", "format=svg"},
		{"Mozilla/5.0", "format=png"},
	}
	for _, c := range cases {
		t.Run(c.ua+"|"+c.query, func(t *testing.T) {
			rec := do(t, c.ua, c.query)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Vary"); got != "User-Agent" {
				t.Errorf("Vary = %q, want User-Agent", got)
			}
		})
	}
}

func TestCacheControl(t *testing.T) {
	rec := do(t, "curl/8.4.0", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Errorf("Cache-Control = %q, want %q", got, "public, max-age=86400")
	}
}

func TestHTMLFormatHasOG(t *testing.T) {
	rec := do(t, "Mozilla/5.0", "format=html")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html prefix", got)
	}
	body := rec.Body.String()
	for _, sub := range []string{"og:image", "<svg", "</body>"} {
		if !strings.Contains(body, sub) {
			t.Errorf("html body missing %q\n--- body ---\n%s", sub, body)
		}
	}
}

func TestASCIIFormat(t *testing.T) {
	rec := do(t, "curl/8.4.0", "format=ascii")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain prefix", got)
	}
	body := rec.Body.String()
	hasHalfBlock := strings.ContainsRune(body, '█') ||
		strings.ContainsRune(body, '▀') ||
		strings.ContainsRune(body, '▄')
	if !hasHalfBlock {
		t.Errorf("ascii body missing half-block runes\n--- body ---\n%s", body)
	}
}
