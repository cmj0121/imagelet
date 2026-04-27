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
// Used by `/now` for year-progress and `/weather` for the day-cycle bar —
// same visual language across services.
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

// DayCycle returns a fragment in the form `day ████████░░░░░░░░░░░░ 5:42-18:24`
// representing how far through the daylight window (sunrise → sunset) `asOf`
// has advanced. Width is fixed at 20 cells. Before sunrise the bar shows 0%
// (label still pinned); after sunset it shows 100% — both correct readings
// at night. Empty/inverted windows (sunset <= sunrise, e.g., polar night)
// return "" so the caller can drop the row entirely.
//
// The HH:MM endpoints are rendered with a single leading-zero strip (so 05:42
// reads as 5:42) — matching the sunrise/sunset caption format /weather already
// uses, so the two rows align visually.
func DayCycle(asOf, sunrise, sunset time.Time) string {
	const width = 20
	span := sunset.Sub(sunrise).Seconds()
	if span <= 0 {
		return ""
	}
	elapsed := asOf.Sub(sunrise).Seconds()
	pct := elapsed / span
	return fmt.Sprintf("day %s %s-%s",
		ProgressBar(pct, width),
		TrimHour(sunrise.Format("15:04")),
		TrimHour(sunset.Format("15:04")))
}

// TrimHour strips a single leading zero from "HH:MM" so 05:42 reads 5:42.
// Times >= 10:00 pass through unchanged.
func TrimHour(s string) string {
	return strings.TrimPrefix(s, "0")
}

// weekdayLetters is the Sunday-first single-letter labels WeekStrip uses.
// Sun and Sat both reduce to "S", Tue and Thu both to "T" — the bracket
// position is the sole disambiguator. Indexed by Go's `time.Weekday()`
// where Sunday=0.
var weekdayLetters = [7]string{"S", "M", "T", "W", "T", "F", "S"}

// WeekStrip returns a fragment in the form `S <M> T W T F S` representing
// the seven days of the week (Sunday-first) with the current day wrapped
// in angle brackets. Used in /now's metadata stack as a glanceable visual
// alternative to the textual `MON` / `TUE` / ... weekday name.
//
// Letters are joined with single spaces. Two letters appear twice
// (Sun/Sat as "S", Tue/Thu as "T") — that's intentional, and the bracket
// position disambiguates them. Output length varies between 13 chars (no
// brackets, never produced) and 15 chars (one bracketed day, the only
// case this function emits).
//
// Angle brackets are used instead of square brackets because pylon parses
// literal `[M]` inside a borderless `( ... )` caption box as a nested
// bracketed-box, shredding the layout. `<M>` survives pylon's parser
// untouched — verified by the round-trip test in this package.
func WeekStrip(t time.Time) string {
	idx := int(t.Weekday())
	parts := make([]string, 7)
	for i, letter := range weekdayLetters {
		if i == idx {
			parts[i] = "<" + letter + ">"
		} else {
			parts[i] = letter
		}
	}
	return strings.Join(parts, " ")
}
