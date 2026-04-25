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
		{"chrome_browser", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36", render.ModeSVG},
		{"firefox_browser", "Mozilla/5.0 (Windows NT 10.0) Gecko/20100101 Firefox/121.0", render.ModeSVG},
		{"mozilla_lowercase", "mozilla/5.0 weird-bot", render.ModeSVG},
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
