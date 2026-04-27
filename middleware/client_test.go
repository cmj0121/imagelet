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
		{"chrome_browser", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36", render.ModeHTML},
		{"firefox_browser", "Mozilla/5.0 (Windows NT 10.0) Gecko/20100101 Firefox/121.0", render.ModeHTML},
		{"mozilla_lowercase", "mozilla/5.0 weird-bot", render.ModeHTML},
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
		{"format_html_with_curl_ua", "curl/8.4.0", "format=html", render.ModeHTML},
		{"format_svg_with_browser_ua", "Mozilla/5.0", "format=svg", render.ModeSVG},
		{"format_png_with_curl_ua", "curl/8.4.0", "format=png", render.ModePNG},
		{"format_png_with_browser_ua", "Mozilla/5.0", "format=png", render.ModePNG},
		{"format_svg_with_curl_ua", "curl/8.4.0", "format=svg", render.ModeSVG},
		{"format_ascii_with_browser_ua", "Mozilla/5.0", "format=ascii", render.ModeASCII},

		// Case- and whitespace-insensitive.
		{"format_uppercase", "curl/8.4.0", "format=SVG", render.ModeSVG},
		{"format_uppercase_html", "curl/8.4.0", "format=HTML", render.ModeHTML},
		{"format_padded", "curl/8.4.0", "format=%20png%20", render.ModePNG},

		// Bad / unsupported values silently fall through to UA classification.
		// Browser UA default is now HTML; curl UA stays ASCII.
		{"format_garbage_falls_through_browser", "Mozilla/5.0", "format=jpeg", render.ModeHTML},
		{"format_garbage_falls_through_curl", "curl/8.4.0", "format=jpeg", render.ModeASCII},
		{"format_empty_falls_through", "Mozilla/5.0", "format=", render.ModeHTML},
		{"no_query_falls_through_browser", "Mozilla/5.0", "", render.ModeHTML},
		{"no_query_falls_through_curl", "curl/8.4.0", "", render.ModeASCII},

		// ?format=pylon is NOT a render mode — handled by WantsPylonSource.
		// ResolveMode falls through to UA classification when given pylon.
		{"format_pylon_falls_through_browser", "Mozilla/5.0", "format=pylon", render.ModeHTML},
		{"format_pylon_falls_through_curl", "curl/8.4.0", "format=pylon", render.ModeASCII},
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

// TestWantsPylonSource exercises both negotiation paths — the legacy
// Accept: text/pylon header AND the ?format=pylon query parameter — plus
// representative falsy cases. Both spellings exist so curl callers (-H)
// and browser-bookmark callers (?…) can request the same content.
func TestWantsPylonSource(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name   string
		accept string
		query  string
		want   bool
	}{
		{"accept_pylon", "text/pylon", "", true},
		{"accept_pylon_among_others", "text/html, text/pylon, */*", "", true},
		{"format_pylon", "", "format=pylon", true},
		{"format_pylon_uppercase", "", "format=PYLON", true},
		{"format_pylon_padded", "", "format=%20pylon%20", true},
		{"both_accept_and_format", "text/pylon", "format=pylon", true},

		{"no_signal", "", "", false},
		{"format_html", "", "format=html", false},
		{"accept_html", "text/html", "", false},
		{"format_garbage", "", "format=jpeg", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.GET("/probe", func(c *gin.Context) {
				if middleware.WantsPylonSource(c) {
					c.String(http.StatusOK, "yes")
					return
				}
				c.String(http.StatusOK, "no")
			})

			path := "/probe"
			if tc.query != "" {
				path += "?" + tc.query
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			got := rec.Body.String() == "yes"
			if got != tc.want {
				t.Errorf("accept=%q query=%q: WantsPylonSource = %v, want %v", tc.accept, tc.query, got, tc.want)
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
