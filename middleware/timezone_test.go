package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
)

func TestTimezoneDetector(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		cfHeader string
		want     string // expected *time.Location.String()
	}{
		{"no_header", "", time.Local.String()},
		{"valid_taipei", "Asia/Taipei", "Asia/Taipei"},
		{"valid_la", "America/Los_Angeles", "America/Los_Angeles"},
		{"valid_utc", "UTC", "UTC"},
		{"unparseable_falls_back", "Mars/Olympus_Mons", time.Local.String()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(middleware.TimezoneDetector())
			r.GET("/probe", func(c *gin.Context) {
				c.String(http.StatusOK, middleware.GetLocation(c).String())
			})

			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			if tc.cfHeader != "" {
				req.Header.Set("CF-Timezone", tc.cfHeader)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if got := rec.Body.String(); got != tc.want {
				t.Errorf("CF-Timezone=%q: location = %q, want %q", tc.cfHeader, got, tc.want)
			}
		})
	}
}

func TestGetLocationWithoutMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	if got := middleware.GetLocation(c); got != time.Local {
		t.Errorf("GetLocation without middleware = %v, want time.Local", got)
	}
}
