package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
)

func TestRegionDetector(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		cfHeader string
		want     string
	}{
		{"no_header", "", "US"},
		{"valid_TW", "TW", "TW"},
		{"valid_US", "US", "US"},
		{"lowercase_normalized", "tw", "TW"},
		{"padded_whitespace", "  JP  ", "JP"},
		{"cloudflare_unknown", "XX", "XX"},
		{"three_letter_passthrough", "USA", "USA"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			r.Use(middleware.RegionDetector())
			r.GET("/probe", func(c *gin.Context) {
				c.String(http.StatusOK, middleware.GetCountry(c))
			})

			req := httptest.NewRequest(http.MethodGet, "/probe", nil)
			if tc.cfHeader != "" {
				req.Header.Set("CF-IPCountry", tc.cfHeader)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if got := rec.Body.String(); got != tc.want {
				t.Errorf("CF-IPCountry=%q: country = %q, want %q", tc.cfHeader, got, tc.want)
			}
		})
	}
}

func TestGetCountryWithoutMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	if got := middleware.GetCountry(c); got != "US" {
		t.Errorf("GetCountry without middleware = %q, want %q", got, "US")
	}
}
