package render_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cmj0121/pylon/src/go/pkg/pylon"

	"github.com/cmj0121/imagelet/render"
)

func TestProgressBarBoundaries(t *testing.T) {
	cases := []struct {
		pct        float64
		width      int
		wantFilled int
	}{
		{0.0, 20, 0},
		{0.5, 20, 10},
		{1.0, 20, 20},
		{-0.5, 20, 0},  // clamped to 0
		{1.5, 20, 20},  // clamped to 1
		{0.32, 20, 6},  // year-progress example: 32% of 20 = 6.4 → round 6
		{0.78, 20, 16}, // 52w-range example: 78% of 20 = 15.6 → round 16
	}
	for _, tc := range cases {
		got := render.ProgressBar(tc.pct, tc.width)
		gotFilled := strings.Count(got, "█")
		gotEmpty := strings.Count(got, "░")
		if gotFilled != tc.wantFilled {
			t.Errorf("ProgressBar(%.2f, %d) filled = %d, want %d (got %q)",
				tc.pct, tc.width, gotFilled, tc.wantFilled, got)
		}
		if gotFilled+gotEmpty != tc.width {
			t.Errorf("ProgressBar(%.2f, %d) total cells = %d, want %d",
				tc.pct, tc.width, gotFilled+gotEmpty, tc.width)
		}
	}
}

func TestProgressBarZeroWidth(t *testing.T) {
	if got := render.ProgressBar(0.5, 0); got != "" {
		t.Errorf("ProgressBar(0.5, 0) = %q, want empty", got)
	}
}

// TestSignedBar pins the center-split layout: negatives grow leftward
// from the center, positives grow rightward, the center is U+2502, and
// the total width is exactly 2*halfWidth + 1 cells.
func TestSignedBar(t *testing.T) {
	cases := []struct {
		name      string
		value     float64
		max       float64
		halfWidth int
		want      string
	}{
		{"zero_value", 0, 100, 5, "░░░░░│░░░░░"},
		{"max_negative", -100, 100, 5, "█████│░░░░░"},
		{"max_positive", 100, 100, 5, "░░░░░│█████"},
		{"half_negative", -50, 100, 10, "░░░░░█████│░░░░░░░░░░"},
		{"half_positive", 50, 100, 10, "░░░░░░░░░░│█████░░░░░"},
		{"clamp_over", 200, 100, 4, "░░░░│████"},
		{"clamp_under", -200, 100, 4, "████│░░░░"},
		{"max_zero_returns_empty", 50, 0, 3, "░░░│░░░"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := render.SignedBar(tc.value, tc.max, tc.halfWidth)
			if got != tc.want {
				t.Errorf("SignedBar(%v, %v, %d) = %q, want %q",
					tc.value, tc.max, tc.halfWidth, got, tc.want)
			}
			wantWidth := 2*tc.halfWidth + 1
			gotWidth := strings.Count(got, "█") + strings.Count(got, "░") + 1
			if gotWidth != wantWidth {
				t.Errorf("SignedBar width = %d cells, want %d", gotWidth, wantWidth)
			}
		})
	}
}

// TestSignedBarSeparator pins that the separator is U+2502 (a distinct
// glyph from pylon's ASCII pipe `|`), so the bar survives pylon parsing
// inside borderless captions where `|` would be syntactically loaded.
func TestSignedBarSeparator(t *testing.T) {
	got := render.SignedBar(0, 100, 5)
	if !strings.Contains(got, "│") {
		t.Errorf("SignedBar must contain U+2502 separator; got %q", got)
	}
	if strings.Contains(got, "|") {
		t.Errorf("SignedBar must NOT contain ASCII pipe; got %q", got)
	}
}

// TestWeekStripAllWeekdays pins the exact output for each weekday. The
// duplicate-letter cases (Sun=S vs Sat=S, Tue=T vs Thu=T) are listed
// explicitly so the bracket-position-as-disambiguator behavior is
// captured by the test, not just by reading.
func TestWeekStripAllWeekdays(t *testing.T) {
	// Anchor on a known-Sunday date in 2026: 2026-01-04 is a Sunday.
	loc := time.UTC
	sunday := time.Date(2026, 1, 4, 12, 0, 0, 0, loc)

	cases := []struct {
		name string
		t    time.Time
		want string
	}{
		{"sunday", sunday, "<S> M T W T F S"},
		{"monday", sunday.AddDate(0, 0, 1), "S <M> T W T F S"},
		{"tuesday", sunday.AddDate(0, 0, 2), "S M <T> W T F S"},
		{"wednesday", sunday.AddDate(0, 0, 3), "S M T <W> T F S"},
		{"thursday", sunday.AddDate(0, 0, 4), "S M T W <T> F S"},
		{"friday", sunday.AddDate(0, 0, 5), "S M T W T <F> S"},
		{"saturday", sunday.AddDate(0, 0, 6), "S M T W T F <S>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := render.WeekStrip(tc.t); got != tc.want {
				t.Errorf("WeekStrip(%s) = %q, want %q", tc.t.Weekday(), got, tc.want)
			}
		})
	}
}

// TestWeekStripPylonRoundTrip is the tripwire for pylon's parser semantics.
// WeekStrip uses angle brackets specifically because square brackets are
// pylon's bracketed-box syntax — `[M]` inside a `( ... )` borderless
// caption gets parsed as a nested bordered box, shredding the layout. If
// pylon ever changes how it handles angle brackets in caption text, this
// test fails LOUDLY before the broken layout reaches production /now
// renders.
func TestWeekStripPylonRoundTrip(t *testing.T) {
	monday := time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC) // a Monday
	strip := render.WeekStrip(monday)
	if !strings.Contains(strip, "<M>") {
		t.Fatalf("precondition: expected WeekStrip(monday) to contain <M>, got %q", strip)
	}

	src := fmt.Sprintf("[ probe | banner ]\n( %s )", strip)
	rendered := pylon.RenderASCII(pylon.Parse(src))

	if !strings.Contains(rendered, "<M>") {
		t.Errorf("pylon shredded <M> from caption — angle brackets no longer survive the parser. Update WeekStrip's bracket choice or the helper's docstring. Rendered output:\n%s", rendered)
	}
	// Pylon must NOT have introduced a nested bordered-box around the
	// bracketed letter (which is what literal [M] would trigger).
	// A nested box would emit a fresh ┌─┐ / └─┘ pair around the letter.
	if strings.Contains(rendered, "┌─") && strings.Count(rendered, "┌─") > 1 {
		t.Errorf("pylon may have nested-boxed <M> — multiple ┌─ pairs in output. Rendered:\n%s", rendered)
	}
}

// TestYearProgressBoundaries pins the format and percent calculation for
// known dates. Year-day 1 → 0% (Jan 1), year-day 365 → 100% (Dec 31 non-leap).
// 2026 is non-leap (not div-by-4); 2024 was a leap year.
func TestYearProgressBoundaries(t *testing.T) {
	cases := []struct {
		date    time.Time
		wantPct int
	}{
		{time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), 0},     // 1/365 → 0.27% → 0
		{time.Date(2026, 4, 27, 12, 0, 0, 0, time.UTC), 32},   // 117/365 → 32.05% → 32
		{time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC), 50},    // 183/365 → 50.13% → 50
		{time.Date(2026, 12, 31, 12, 0, 0, 0, time.UTC), 100}, // 365/365 → 100
		{time.Date(2024, 12, 31, 12, 0, 0, 0, time.UTC), 100}, // leap year still 100%
	}
	for _, tc := range cases {
		got := render.YearProgress(tc.date, "year")
		if !strings.HasPrefix(got, "year ") {
			t.Errorf("YearProgress(%s) = %q, want prefix 'year '", tc.date, got)
		}
		wantSuffix := fmt.Sprintf(" %d%%", tc.wantPct)
		if !strings.HasSuffix(got, wantSuffix) {
			t.Errorf("YearProgress(%s) = %q, want suffix %q", tc.date, got, wantSuffix)
		}
		// Bar width = 20: count `█` + `░` should be 20.
		gotFilled := strings.Count(got, "█")
		gotEmpty := strings.Count(got, "░")
		if gotFilled+gotEmpty != 20 {
			t.Errorf("YearProgress(%s) bar cells = %d, want 20 (got %q)",
				tc.date, gotFilled+gotEmpty, got)
		}
	}
}
