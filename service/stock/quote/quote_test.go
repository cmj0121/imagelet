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

// TestMeanOfLastN_BasicAverage pins the basic arithmetic mean over the
// last n entries with no zeros to filter.
func TestMeanOfLastN_BasicAverage(t *testing.T) {
	closes := []float64{100, 102, 104, 106, 108}
	if got := quote.MeanOfLastN(closes, 5); got != 104 {
		t.Errorf("MeanOfLastN = %v, want 104", got)
	}
}

// TestMeanOfLastN_FiltersZeroes pins that zero entries are skipped — they
// represent holiday/weekend gaps in the close array — so the average is
// taken over the last n non-zero values.
func TestMeanOfLastN_FiltersZeroes(t *testing.T) {
	closes := []float64{0, 100, 0, 102, 104, 106, 108}
	if got := quote.MeanOfLastN(closes, 5); got != 104 {
		t.Errorf("MeanOfLastN = %v, want 104", got)
	}
}

// TestMeanOfLastN_InsufficientReturnsZero pins the "insufficient data"
// sentinel: fewer than n non-zero entries → 0.
func TestMeanOfLastN_InsufficientReturnsZero(t *testing.T) {
	closes := []float64{100, 102, 104}
	if got := quote.MeanOfLastN(closes, 5); got != 0 {
		t.Errorf("MeanOfLastN = %v, want 0", got)
	}
}

// TestMeanOfLastN_ZeroOrNegativeN pins the n<=0 guard.
func TestMeanOfLastN_ZeroOrNegativeN(t *testing.T) {
	closes := []float64{100, 102, 104}
	if got := quote.MeanOfLastN(closes, 0); got != 0 {
		t.Errorf("n=0: MeanOfLastN = %v, want 0", got)
	}
	if got := quote.MeanOfLastN(closes, -1); got != 0 {
		t.Errorf("n=-1: MeanOfLastN = %v, want 0", got)
	}
}

// TestMeanOfLastN_NilInput pins the nil-slice guard.
func TestMeanOfLastN_NilInput(t *testing.T) {
	if got := quote.MeanOfLastN(nil, 5); got != 0 {
		t.Errorf("MeanOfLastN(nil) = %v, want 0", got)
	}
}

// TestMeanOfLastN_TakesLastN pins that only the trailing n non-zero
// entries are averaged — older entries don't bias the mean.
func TestMeanOfLastN_TakesLastN(t *testing.T) {
	closes := []float64{1, 2, 3, 4, 5, 6, 7, 100, 200, 300}
	want := (100.0 + 200.0 + 300.0) / 3.0
	if got := quote.MeanOfLastN(closes, 3); got != want {
		t.Errorf("MeanOfLastN = %v, want %v", got, want)
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
