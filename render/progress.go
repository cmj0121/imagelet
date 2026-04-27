package render

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// ProgressBar returns a `█`-and-`░` text bar of the given total width with the
// filled portion proportional to pct (clamped to [0, 1]). It does NOT append
// a percentage label — callers compose the final caption. Both glyphs are
// EAW-ambiguous so a CJK terminal renders them at uniform 2-cell width and
// the bar visually stays uniform.
//
// Used by `/now` for year-progress, `/stock` for day/52w-range bars, and
// `/weather` for the day-cycle bar — same visual language across services.
func ProgressBar(pct float64, width int) string {
	if width <= 0 {
		return ""
	}
	if pct < 0 {
		pct = 0
	} else if pct > 1 {
		pct = 1
	}
	filled := int(math.Round(pct * float64(width)))
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

// YearProgress returns a fragment in the form `year ██████░░░░░░░░░░░░░░ 32%`
// representing how far through the calendar year `t` is. Width is fixed at
// 20 cells. Day-of-year is taken in `t`'s location so a visitor whose
// timezone has rolled into Jan 1 sees the new year's progress.
//
// The percent is rounded to the nearest whole; day 1 shows 0%, day 365/366
// shows 100%, and a non-leap year's day 117 (April 27 in 2026) shows 32%.
func YearProgress(t time.Time) string {
	const width = 20
	daysInYear := time.Date(t.Year(), 12, 31, 0, 0, 0, 0, t.Location()).YearDay()
	pct := float64(t.YearDay()) / float64(daysInYear)
	return fmt.Sprintf("year %s %d%%", ProgressBar(pct, width), int(math.Round(pct*100)))
}
