package quote_test

import (
	"testing"

	"github.com/cmj0121/imagelet/service/stock/quote"
)

// TestHasDayRangeAndPosition pins the contract for graceful absence: when
// DayHigh / DayLow are missing, the renderer's HasDayRange()/DayPosition()
// guards must keep it from drawing a meaningless bar.
func TestHasDayRangeAndPosition(t *testing.T) {
	cases := []struct {
		name     string
		q        quote.Quote
		hasRange bool
		position float64
	}{
		{"missing both", quote.Quote{Last: 100}, false, 0},
		{"missing high", quote.Quote{Last: 100, DayLow: 90}, false, 0},
		{"missing low", quote.Quote{Last: 100, DayHigh: 110}, false, 0},
		{"low >= high (zero range)", quote.Quote{Last: 100, DayHigh: 90, DayLow: 90}, false, 0},
		{"at low", quote.Quote{Last: 90, DayHigh: 110, DayLow: 90}, true, 0},
		{"at high", quote.Quote{Last: 110, DayHigh: 110, DayLow: 90}, true, 1},
		{"midpoint", quote.Quote{Last: 100, DayHigh: 110, DayLow: 90}, true, 0.5},
		{"clamped above high", quote.Quote{Last: 200, DayHigh: 110, DayLow: 90}, true, 1},
		{"clamped below low", quote.Quote{Last: 0, DayHigh: 110, DayLow: 90}, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.q.HasDayRange(); got != tc.hasRange {
				t.Errorf("HasDayRange = %v, want %v", got, tc.hasRange)
			}
			if got := tc.q.DayPosition(); got != tc.position {
				t.Errorf("DayPosition = %v, want %v", got, tc.position)
			}
		})
	}
}

// TestHas52WeekRangeAndPosition mirrors TestHasDayRangeAndPosition for
// the 52-week fields. Same code path, different scale -- the sanity is
// that the helpers don't share state between them.
func TestHas52WeekRangeAndPosition(t *testing.T) {
	cases := []struct {
		name     string
		q        quote.Quote
		hasRange bool
		position float64
	}{
		{"missing", quote.Quote{Last: 5000}, false, 0},
		{"midpoint", quote.Quote{Last: 6000, Week52High: 7000, Week52Low: 5000}, true, 0.5},
		{"near high", quote.Quote{Last: 6800, Week52High: 7000, Week52Low: 5000}, true, 0.9},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.q.Has52WeekRange(); got != tc.hasRange {
				t.Errorf("Has52WeekRange = %v, want %v", got, tc.hasRange)
			}
			if got := tc.q.Week52Position(); got != tc.position {
				t.Errorf("Week52Position = %v, want %v", got, tc.position)
			}
		})
	}
}
