package server_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
	"github.com/cmj0121/imagelet/server"
)

func TestRootReturns200Empty(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := server.New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body length = %d, want 0 (body=%q)", rec.Body.Len(), rec.Body.String())
	}
}

// TestNewInstallsClientDetector pins that server.New preinstalls the
// content-negotiation middleware: a probe handler reading middleware.GetMode
// must observe the mode chosen by ClientDetector based on the request UA.
// Without ClientDetector in the chain, GetMode would always return ModeASCII
// (the safe default), so the Mozilla case would fail.
func TestNewInstallsClientDetector(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := server.New()
	r.GET("/probe", func(c *gin.Context) {
		c.String(http.StatusOK, middleware.GetMode(c).String())
	})

	tests := []struct {
		ua   string
		want render.Mode
	}{
		{"curl/8.4.0", render.ModeASCII},
		{"Mozilla/5.0", render.ModeSVG},
	}

	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set("User-Agent", tc.ua)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if got, want := rec.Body.String(), tc.want.String(); got != want {
			t.Errorf("ua=%q: mode = %q, want %q", tc.ua, got, want)
		}
	}
}
