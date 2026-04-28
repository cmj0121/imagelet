package render

import (
	"strconv"
	"strings"
	"testing"
)

// plain is a minimal price formatter used by the OHLC tests so the
// tests don't depend on the /stock package's formatPrice (which lives
// in service/stock to keep render free of pricing concerns). Prices
// are rendered as integers in these fixtures so column-position math
// is easy to read.
func plain(v float64) string {
	return strconv.Itoa(int(v))
}

// TestOHLCBarBullish pins the bullish layout: Close > Open, so the
// body fill is `█` and the bar reads `O…C` left-to-right. With Low=100
// / Open=120 / Close=180 / High=200 across width=21, openPos=4 and
// closePos=16, the bar pattern locks down at three runs of
// 4 wicks + O + 11 body + C + 4 wicks.
func TestOHLCBarBullish(t *testing.T) {
	top, bar, bottom := OHLCBar(120, 200, 100, 180, 21, plain)
	wantBar := "────O███████████C────"
	if bar != wantBar {
		t.Errorf("bar:\n got %q\nwant %q", bar, wantBar)
	}
	if !strings.HasPrefix(top, "100") || !strings.HasSuffix(top, "200") {
		t.Errorf("top edge labels missing: %q", top)
	}
	if len(top) != 21 || len(bottom) != 21 {
		t.Errorf("rows not width-aligned: top=%d bottom=%d (want 21)", len(top), len(bottom))
	}
}

// TestOHLCBarBearish flips the body fill to hollow `░` when Close <
// Open, and the bar reads `C…O` because Close sits to the left of
// Open on the price axis.
func TestOHLCBarBearish(t *testing.T) {
	_, bar, _ := OHLCBar(180, 200, 100, 120, 21, plain)
	wantBar := "────C░░░░░░░░░░░O────"
	if bar != wantBar {
		t.Errorf("bar:\n got %q\nwant %q", bar, wantBar)
	}
}

// TestOHLCBarDoji pins the equal-Open-Close case: a single midpoint
// label appears in the bottom row, and the bar fuses `OC` at adjacent
// columns.
func TestOHLCBarDoji(t *testing.T) {
	_, bar, bottom := OHLCBar(150, 200, 100, 150, 21, plain)
	if !strings.Contains(bar, "OC") {
		t.Errorf("doji bar missing OC fusion: %q", bar)
	}
	if strings.Count(bottom, "150") != 1 {
		t.Errorf("doji bottom should carry one label, got: %q", bottom)
	}
}

// TestOHLCBarDegenerateRange returns three empty strings when High <=
// Low (caller is expected to gate on HasOHLC, but the helper still
// fails closed).
func TestOHLCBarDegenerateRange(t *testing.T) {
	top, bar, bottom := OHLCBar(100, 100, 100, 100, 21, plain)
	if top != "" || bar != "" || bottom != "" {
		t.Errorf("degenerate range should return empties, got %q / %q / %q", top, bar, bottom)
	}
}

// TestOHLCBarTooNarrow rejects widths under 8 — there is not enough
// room for L/H labels on the top row plus markers on the bar.
func TestOHLCBarTooNarrow(t *testing.T) {
	top, bar, bottom := OHLCBar(120, 200, 100, 180, 7, plain)
	if top != "" || bar != "" || bottom != "" {
		t.Errorf("narrow width should return empties, got %q / %q / %q", top, bar, bottom)
	}
}

// TestOHLCBarOpenAtLow places Open at the exact Low — marker sits at
// column 0, no left wick. The bar fills body from column 1 onward.
func TestOHLCBarOpenAtLow(t *testing.T) {
	_, bar, _ := OHLCBar(100, 200, 100, 150, 21, plain)
	if !strings.HasPrefix(bar, "O") {
		t.Errorf("Open-at-low should put O at column 0: %q", bar)
	}
}

// TestOHLCBarCloseAtHigh places Close at the exact High — marker sits
// at column width-1, no right wick.
func TestOHLCBarCloseAtHigh(t *testing.T) {
	_, bar, _ := OHLCBar(120, 200, 100, 200, 21, plain)
	if !strings.HasSuffix(bar, "C") {
		t.Errorf("Close-at-high should put C at last column: %q", bar)
	}
}

// TestOHLCBarLabelCollision exercises the bottom-row collision
// collapse: a narrow body (Open=148, Close=152, High=200, Low=100)
// puts Open and Close labels overlapping. The helper drops the inner
// label and keeps the outer.
func TestOHLCBarLabelCollision(t *testing.T) {
	_, _, bottom := OHLCBar(148, 200, 100, 152, 21, plain)
	hasOpen := strings.Contains(bottom, "148")
	hasClose := strings.Contains(bottom, "152")
	if hasOpen && hasClose {
		t.Errorf("collision should drop one label, got both: %q", bottom)
	}
	if !hasOpen && !hasClose {
		t.Errorf("collision should keep one label, got neither: %q", bottom)
	}
}

// TestOHLCBarRowsEqualWidth pins the column-alignment invariant: all
// three rows are exactly `width` runes long (counting `█` / `░` /
// `─` as single columns). This is what lets pylon's monospace SVG
// render align the rows visually.
func TestOHLCBarRowsEqualWidth(t *testing.T) {
	top, bar, bottom := OHLCBar(21180, 21290, 21150, 21234, 45, plain)
	for name, row := range map[string]string{"top": top, "bar": bar, "bottom": bottom} {
		if got := runeLen(row); got != 45 {
			t.Errorf("%s row width = %d, want 45 (%q)", name, got, row)
		}
	}
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
