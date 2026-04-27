package render_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
