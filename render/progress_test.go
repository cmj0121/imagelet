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

// TestDayCycleBoundaries pins the bar position and label for a known
// daylight window. Sunrise 5:42, sunset 18:24 (Asia/Taipei). At noon
// the elapsed fraction is ~0.495, so the 20-cell bar fills 10 cells.
// Before sunrise → 0 cells (clamped); after sunset → 20 cells (clamped).
// An inverted window (sunset before sunrise) returns "".
func TestDayCycleBoundaries(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Taipei")
	sunrise := time.Date(2026, 4, 26, 5, 42, 0, 0, loc)
	sunset := time.Date(2026, 4, 26, 18, 24, 0, 0, loc)

	cases := []struct {
		name       string
		asOf       time.Time
		wantFilled int
		wantSuffix string
	}{
		{"sunrise_exact", sunrise, 0, " 5:42-18:24"},
		{"midday", time.Date(2026, 4, 26, 12, 3, 0, 0, loc), 10, " 5:42-18:24"},
		{"sunset_exact", sunset, 20, " 5:42-18:24"},
		{"pre_sunrise", time.Date(2026, 4, 26, 3, 0, 0, 0, loc), 0, " 5:42-18:24"},
		{"post_sunset", time.Date(2026, 4, 26, 22, 0, 0, 0, loc), 20, " 5:42-18:24"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := render.DayCycle(tc.asOf, sunrise, sunset)
			if !strings.HasPrefix(got, "day ") {
				t.Errorf("DayCycle = %q, want prefix 'day '", got)
			}
			if !strings.HasSuffix(got, tc.wantSuffix) {
				t.Errorf("DayCycle = %q, want suffix %q", got, tc.wantSuffix)
			}
			if gotFilled := strings.Count(got, "█"); gotFilled != tc.wantFilled {
				t.Errorf("DayCycle filled cells = %d, want %d (got %q)",
					gotFilled, tc.wantFilled, got)
			}
		})
	}

	if got := render.DayCycle(sunrise, sunset, sunrise); got != "" {
		t.Errorf("DayCycle inverted-window = %q, want empty", got)
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
		got := render.YearProgress(tc.date)
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
