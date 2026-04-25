package now_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/service/now"
)

func newRouter() *gin.Engine {
	r := gin.New()
	r.Use(middleware.ClientDetector())
	now.Register(r)
	return r
}

// asciiBoxRe matches the full Option-B ASCII frame (corners, dashes,
// vertical bars, trailing newline) with an HH:MM substring on the middle
// line. Anchored so any extra trailing bytes would fail.
var asciiBoxRe = regexp.MustCompile(`(?s)\A\+[-+]+\+\n\|.*\d{2}:\d{2}.*\|\n\+[-+]+\+\n\z`)

// svgBoxRe matches an <svg>...</svg> document containing an HH:MM substring.
var svgBoxRe = regexp.MustCompile(`(?s)<svg[^>]*>.*\d{2}:\d{2}.*</svg>`)

func TestNow(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		ua          string
		ctTypePfx   string
		bodyPattern *regexp.Regexp
	}{
		{
			name:        "cli_returns_ascii",
			ua:          "curl/8.4.0",
			ctTypePfx:   "text/plain",
			bodyPattern: asciiBoxRe,
		},
		{
			name:        "browser_returns_svg",
			ua:          "Mozilla/5.0",
			ctTypePfx:   "image/svg+xml",
			bodyPattern: svgBoxRe,
		},
	}

	r := newRouter()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/now", nil)
			req.Header.Set("User-Agent", tc.ua)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want %q", got, "no-store")
			}
			if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.ctTypePfx) {
				t.Errorf("Content-Type = %q, want prefix %q", got, tc.ctTypePfx)
			}
			if body := rec.Body.String(); !tc.bodyPattern.MatchString(body) {
				t.Errorf("body does not match %s\n--- body ---\n%s", tc.bodyPattern, body)
			}
		})
	}
}
