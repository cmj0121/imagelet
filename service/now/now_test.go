package now_test

import (
	"bytes"
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
	r.Use(middleware.DateOverrideDetector())
	r.Use(middleware.ClientDetector())
	now.Register(r)
	return r
}

// asciiShapeRe matches the banner-stack output rendered with pylon's native
// theme: top frame uses Unicode box-drawing (┌─┐), >= 6 banner content rows
// in │...│, bottom frame └─┘, then TWO borderless caption lines, trailing
// newline. The {2} anchor pins the metadata stack: row 1 = date+UTC ·
// weekday strip; row 2 = year-progress. Extra/missing rows fail.
var asciiShapeRe = regexp.MustCompile(`(?s)\A\s*┌[─]+┐\s*\n(?:\s*│[^\n]*│\s*\n){6,}\s*└[─]+┘\s*\n(?:[^\n]*\n){2}\z`)

// asciiCombinedRowRe pins row 1: ISO date + signed integer-hour UTC offset
// on the left, then a `·` middle-dot separator, then the 7-letter
// Sunday-first weekday strip with exactly one day wrapped in angle
// brackets. The textual weekday name (`MON`) is intentionally absent —
// the WeekStrip in this same row is the visual replacement.
var asciiCombinedRowRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2} UTC[+-]\d+ · (?:<[SMTWFS]> [SMTWFS](?: [SMTWFS]){5}|[SMTWFS] (?:[SMTWFS] ){0,5}<[SMTWFS]>(?: [SMTWFS])*)`)

// asciiYearProgressRe pins row 2: `year` + 20-cell `█`/`░` bar + percent.
var asciiYearProgressRe = regexp.MustCompile(`year [█░]{20} \d{1,3}%`)

// pylonSourceRe matches the three-element pylon source render.BannerSourceMulti
// emits: `[ HH:MM | banner ]\n( YYYY-MM-DD UTC±H · S <X> T W T F S )\n( year █░ NN% )`.
// Anchored. Pylon v0.2's default banner font has a `:` glyph, so the
// headline reaches pylon untouched. The weekday strip uses angle brackets
// because pylon's parser would treat literal `[X]` as a nested bordered-box.
var pylonSourceRe = regexp.MustCompile(`\A\[ \d{2}:\d{2} \| banner \]\n\( \d{4}-\d{2}-\d{2} UTC[+-]\d+ · (?:<[SMTWFS]> [SMTWFS](?: [SMTWFS]){5}|[SMTWFS] (?:[SMTWFS] ){0,5}<[SMTWFS]>(?: [SMTWFS])*) \)\n\( year [█░]{20} \d{1,3}% \)\z`)

// pngMagic is the 8-byte PNG file signature.
var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

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
		name      string
		ua        string
		accept    string
		query     string
		ctTypePfx string
		// bodyMatch is one of: a regexp (text body), pngMagic sentinel, or nil.
		// asciiSubtitle is true when the body is a text path that should also
		// match the subtitle regex.
		bodyPattern *regexp.Regexp
		bodyIsPNG   bool
		bodyIsHTML  bool
		asciiRows   bool
	}{
		{
			name:        "cli_returns_ascii_banner",
			ua:          "curl/8.4.0",
			ctTypePfx:   "text/plain",
			bodyPattern: asciiShapeRe,
			asciiRows:   true,
		},
		{
			name:       "browser_returns_html",
			ua:         "Mozilla/5.0",
			ctTypePfx:  "text/html",
			bodyIsHTML: true,
		},
		{
			name:      "format_png_returns_png",
			ua:        "curl/8.4.0",
			query:     "format=png",
			ctTypePfx: "image/png",
			bodyIsPNG: true,
		},
		{
			name:        "accept_text_pylon_returns_source",
			ua:          "curl/8.4.0",
			accept:      "text/pylon",
			ctTypePfx:   "text/pylon",
			bodyPattern: pylonSourceRe,
		},
		{
			name:        "format_pylon_query_returns_source",
			ua:          "Mozilla/5.0",
			query:       "format=pylon",
			ctTypePfx:   "text/pylon",
			bodyPattern: pylonSourceRe,
		},
	}

	r := newRouter()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := "/now"
			if tc.query != "" {
				path += "?" + tc.query
			}
			req := httptest.NewRequest(http.MethodGet, path, nil)
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

			body := rec.Body.Bytes()
			if tc.bodyIsPNG {
				if !bytes.HasPrefix(body, pngMagic) {
					t.Errorf("body missing PNG magic bytes; first 8 bytes = %x", body[:min(8, len(body))])
				}
				if len(body) == 0 {
					t.Errorf("body is empty")
				}
				return
			}
			if tc.bodyIsHTML {
				bodyStr := string(body)
				for _, sub := range []string{"<!DOCTYPE html>", "<svg ", "</svg>", "</html>"} {
					if !strings.Contains(bodyStr, sub) {
						t.Errorf("HTML body missing %q\n--- body ---\n%s", sub, bodyStr)
					}
				}
				return
			}
			if tc.bodyPattern != nil && !tc.bodyPattern.Match(body) {
				t.Errorf("body does not match %s\n--- body ---\n%s", tc.bodyPattern, body)
			}
			if tc.asciiRows {
				for _, re := range []*regexp.Regexp{asciiCombinedRowRe, asciiYearProgressRe} {
					if !re.Match(body) {
						t.Errorf("body missing metadata row pattern %s\n--- body ---\n%s", re, body)
					}
				}
			}
		})
	}
}

// TestNowDateOverride pins the contract that ?date=YYYY-MM-DD shifts the
// caption rows onto the requested calendar day while leaving the headline
// (real wall-clock HH:MM) untouched. Both the date and the year-progress
// percent reflect the override; an old historical date should produce a
// 100% bar (the year is fully past) — testing 2012 because it's far enough
// back that a regression on the override path can't accidentally pass by
// reading "today".
func TestNowDateOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()

	req := httptest.NewRequest(http.MethodGet, "/now?date=2012-02-02", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-Timezone", "UTC")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "2012-02-02") {
		t.Errorf("body missing override date 2012-02-02\n--- body ---\n%s", body)
	}
	// 2012-02-02 is a Thursday — the weekday strip should bracket "T".
	// The 7-letter Sunday-first strip is `S M T W T F S`; the bracketed
	// position must be column 4 (0-indexed: index 4) — the second T.
	if !strings.Contains(body, "<T>") {
		t.Errorf("body missing <T> bracket for Thursday\n--- body ---\n%s", body)
	}
	// year-progress reads the override date's day-of-year; Feb 2 = 9%
	// (33 / 366 in 2012). Confirms the year-progress bar tracks the
	// override, not "today".
	if !strings.Contains(body, "9%") {
		t.Errorf("body missing year-progress 9%% for 2012-02-02\n--- body ---\n%s", body)
	}
}

// TestNowDateOverrideInvalidFallsThrough pins that an unparseable ?date=
// value silently falls through to today (no 4xx) — matches the project's
// fall-through pattern for ?format= and ?region=.
func TestNowDateOverrideInvalidFallsThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()

	req := httptest.NewRequest(http.MethodGet, "/now?date=not-a-date", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-Timezone", "UTC")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "0001-01-01") {
		t.Errorf("invalid date stamped through as zero time\n--- body ---\n%s", body)
	}
}

func TestNowFormatSVG(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()

	// ?format=svg overrides UA in both directions: a curl UA gets SVG, a
	// browser UA also gets SVG (the explicit query wins over the default
	// PNG path).
	for _, ua := range []string{"curl/8.4.0", "Mozilla/5.0"} {
		t.Run(ua, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/now?format=svg", nil)
			req.Header.Set("User-Agent", ua)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
				t.Errorf("Content-Type = %q, want image/svg+xml", got)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			body := rec.Body.String()
			if !strings.Contains(body, `xmlns="http://www.w3.org/2000/svg"`) {
				t.Errorf("body missing xmlns; got:\n%s", body)
			}
			if !strings.Contains(body, "<svg ") || !strings.Contains(body, "</svg>") {
				t.Errorf("body not bracketed by <svg> tags; got:\n%s", body)
			}
		})
	}
}
