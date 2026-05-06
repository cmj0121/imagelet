package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
	"github.com/cmj0121/imagelet/server"
)

func TestHealthzReturns200Empty(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := server.New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body length = %d, want 0 (body=%q)", rec.Body.Len(), rec.Body.String())
	}
}

// TestRobotsTxt pins the abuse-posture decision (DESIGN.md §9): /github/* is
// disallowed for crawlers so unfurl bots and SEO scrapers don't burn the
// upstream rate-limit budget enumerating GitHub via imagelet.
func TestRobotsTxt(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := server.New()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/robots.txt", nil)

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	for _, sub := range []string{"User-agent: *", "Disallow: /github/"} {
		if !strings.Contains(body, sub) {
			t.Errorf("body missing %q\n--- body ---\n%s", sub, body)
		}
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
		{"Mozilla/5.0", render.ModeHTML},
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

// TestNewInstallsRequestSizeLimiter pins that server.New preinstalls the
// request-size limiter: a probe path well over MaxRequestPathBytes must
// be rejected with HTTP 414 before reaching any registered handler.
// Without RequestSizeLimiter in the chain, gin would route the probe
// to the registered handler and return 200, so this test would fail.
func TestNewInstallsRequestSizeLimiter(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := server.New()
	// NoRoute also runs the engine middleware chain in gin, so any
	// unregistered path still flows through RequestSizeLimiter — which
	// is what we want to assert here.
	r.NoRoute(func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// 2 KiB path: well over MaxRequestPathBytes (1 KiB), well under
	// MaxRequestQueryBytes (4 KiB) so the path branch is what fires.
	path := "/" + strings.Repeat("a", 2<<10)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestURITooLong {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestURITooLong)
	}
}

// TestNewInstallsRegionDetector pins that server.New preinstalls the
// region middleware: a probe handler reading middleware.GetCountry must
// observe the 2-letter country code resolved from the CF-IPCountry header
// (uppercased), or "US" as the safe default when the header is absent.
// Without RegionDetector in the chain, GetCountry would always return "US",
// so the lowercase "tw" and "JP" cases would fail.
func TestNewInstallsRegionDetector(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := server.New()
	r.GET("/probe", func(c *gin.Context) {
		c.String(http.StatusOK, middleware.GetCountry(c))
	})

	tests := []struct {
		header string
		want   string
	}{
		{"", "US"},
		{"tw", "TW"},
		{"JP", "JP"},
	}

	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		if tc.header != "" {
			req.Header.Set("CF-IPCountry", tc.header)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if got := rec.Body.String(); got != tc.want {
			t.Errorf("CF-IPCountry=%q: country = %q, want %q", tc.header, got, tc.want)
		}
	}
}
