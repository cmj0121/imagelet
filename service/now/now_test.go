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
	r.Use(middleware.TimezoneDetector())
	r.Use(middleware.ClientDetector())
	now.Register(r)
	return r
}

// asciiShapeRe matches the banner-stack output rendered with pylon's native
// theme: top frame uses Unicode box-drawing (┌─┐), >= 6 banner content rows
// in │...│, bottom frame └─┘, then a borderless caption line, trailing
// newline. Anchored so any extra trailing bytes would fail.
var asciiShapeRe = regexp.MustCompile(`(?s)\A\s*┌[─]+┐\s*\n(?:\s*│[^\n]*│\s*\n){6,}\s*└[─]+┘\s*\n[^\n]*\n\z`)

// asciiSubtitleRe pins the caption format: ISO date, uppercase 3-letter
// weekday, signed integer-hour UTC offset. Survives DST because the offset
// is read at request time.
var asciiSubtitleRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2} [A-Z]{3} UTC[+-]\d+`)

// svgRe matches an <svg> document containing the same caption format. The
// caption is in a non-bannered borderless box, so the literal text survives
// pylon's SVG path as a <text> element.
var svgRe = regexp.MustCompile(`(?s)<svg[^>]*>.*\d{4}-\d{2}-\d{2} [A-Z]{3} UTC[+-]\d+.*</svg>`)

// pylonSourceRe matches the exact two-element pylon source render.BannerSource
// emits: `[ HH MM | banner ]\n( YYYY-MM-DD DAY UTC±H )`. Anchored.
var pylonSourceRe = regexp.MustCompile(`\A\[ \d{2} \d{2} \| banner \]\n\( \d{4}-\d{2}-\d{2} [A-Z]{3} UTC[+-]\d+ \)\z`)

// TestNowRespectsCFTimezone pins the contract that /now's subtitle reflects
// the timezone resolved by middleware.TimezoneDetector from the CF-Timezone
// header. UTC+0 vs UTC+8 is a wide enough gap that no plausible host zone
// can produce both offsets, so this test is robust regardless of where it
// runs.
func TestNowRespectsCFTimezone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()

	cases := []struct {
		cfHeader string
		want     string
	}{
		{"UTC", "UTC+0"},
		{"Asia/Taipei", "UTC+8"},
	}
	for _, tc := range cases {
		t.Run(tc.cfHeader, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/now", nil)
			req.Header.Set("User-Agent", "curl/8.4.0")
			req.Header.Set("CF-Timezone", tc.cfHeader)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("subtitle does not contain %q\n--- body ---\n%s", tc.want, rec.Body.String())
			}
		})
	}
}

func TestNow(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		ua          string
		accept      string
		ctTypePfx   string
		bodyPattern *regexp.Regexp
		matchSubtitleSeparately bool
	}{
		{
			name:                    "cli_returns_ascii_banner",
			ua:                      "curl/8.4.0",
			ctTypePfx:               "text/plain",
			bodyPattern:             asciiShapeRe,
			matchSubtitleSeparately: true,
		},
		{
			name:                    "browser_returns_svg",
			ua:                      "Mozilla/5.0",
			ctTypePfx:               "image/svg+xml",
			bodyPattern:             svgRe,
			matchSubtitleSeparately: true,
		},
		{
			name:        "accept_text_pylon_returns_source",
			ua:          "curl/8.4.0",
			accept:      "text/pylon",
			ctTypePfx:   "text/pylon",
			bodyPattern: pylonSourceRe,
		},
	}

	r := newRouter()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/now", nil)
			req.Header.Set("User-Agent", tc.ua)
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
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

			body := rec.Body.String()
			if !tc.bodyPattern.MatchString(body) {
				t.Errorf("body does not match %s\n--- body ---\n%s", tc.bodyPattern, body)
			}
			// For ASCII / SVG paths the subtitle format is checked separately
			// because the body regex is structural; the text/pylon source
			// already pins the subtitle format.
			if tc.matchSubtitleSeparately && !asciiSubtitleRe.MatchString(body) {
				t.Errorf("body missing subtitle pattern %s\n--- body ---\n%s", asciiSubtitleRe, body)
			}
		})
	}
}
