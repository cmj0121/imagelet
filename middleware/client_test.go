package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
)

func TestClientDetector(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name string
		ua   string
		want render.Mode
	}{
		{"empty_ua", "", render.ModeASCII},
		{"chrome_browser", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36", render.ModePNG},
		{"firefox_browser", "Mozilla/5.0 (Windows NT 10.0) Gecko/20100101 Firefox/121.0", render.ModePNG},
		{"mozilla_lowercase", "mozilla/5.0 weird-bot", render.ModePNG},
		{"curl", "curl/8.4.0", render.ModeASCII},
		{"wget", "Wget/1.21.4", render.ModeASCII},
		{"go_http_client", "Go-http-client/1.1", render.ModeASCII},
		{"python_requests", "python-requests/2.31.0", render.ModeASCII},
		{"httpie", "HTTPie/3.2.2", render.ModeASCII},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(middleware.ClientDetector())
			r.GET("/probe", func(c *gin.Context) {
				c.String(http.StatusOK, middleware.GetMode(c).String())
			})

			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			if tc.ua != "" {
				req.Header.Set("User-Agent", tc.ua)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got, want := rec.Body.String(), tc.want.String(); got != want {
				t.Errorf("ua=%q: mode = %q, want %q", tc.ua, got, want)
			}
		})
	}
}

func TestGetModeWithoutMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	if got := middleware.GetMode(c); got != render.ModeASCII {
		t.Errorf("GetMode without middleware = %v, want ModeASCII (safe default)", got)
	}
}

func TestResolveMode(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name  string
		ua    string
		query string
		want  render.Mode
	}{
		// Query overrides UA.
		{"format_svg_with_browser_ua", "Mozilla/5.0", "format=svg", render.ModeSVG},
		{"format_png_with_curl_ua", "curl/8.4.0", "format=png", render.ModePNG},
		{"format_svg_with_curl_ua", "curl/8.4.0", "format=svg", render.ModeSVG},

		// Case- and whitespace-insensitive.
		{"format_uppercase", "curl/8.4.0", "format=SVG", render.ModeSVG},
		{"format_padded", "curl/8.4.0", "format=%20png%20", render.ModePNG},

		// Bad / unsupported values silently fall through to UA classification.
		{"format_ascii_falls_through_browser", "Mozilla/5.0", "format=ascii", render.ModePNG},
		{"format_ascii_falls_through_curl", "curl/8.4.0", "format=ascii", render.ModeASCII},
		{"format_garbage_falls_through", "Mozilla/5.0", "format=jpeg", render.ModePNG},
		{"format_empty_falls_through", "Mozilla/5.0", "format=", render.ModePNG},
		{"no_query_falls_through", "Mozilla/5.0", "", render.ModePNG},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(middleware.ClientDetector())
			r.GET("/probe", func(c *gin.Context) {
				c.String(http.StatusOK, middleware.ResolveMode(c).String())
			})

			path := "/probe"
			if tc.query != "" {
				path += "?" + tc.query
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if tc.ua != "" {
				req.Header.Set("User-Agent", tc.ua)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got, want := rec.Body.String(), tc.want.String(); got != want {
				t.Errorf("ua=%q query=%q: mode = %q, want %q", tc.ua, tc.query, got, want)
			}
		})
	}
}

// TestResolveModeWithoutClientDetector pins that ?format= still wins even
// when ClientDetector wasn't installed — the query path is independent of
// the UA-classification middleware.
func TestResolveModeWithoutClientDetector(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.GET("/probe", func(c *gin.Context) {
		c.String(http.StatusOK, middleware.ResolveMode(c).String())
	})

	t.Run("format_svg_no_detector", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/probe?format=svg", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if got := rec.Body.String(); got != "svg" {
			t.Errorf("ResolveMode without ClientDetector: mode = %q, want %q", got, "svg")
		}
	})

	t.Run("no_format_no_detector_defaults_ascii", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if got := rec.Body.String(); got != "ascii" {
			t.Errorf("ResolveMode without ClientDetector + no query: mode = %q, want %q", got, "ascii")
		}
	})
}
