package render

import (
	"fmt"
	"strings"
	"testing"
)

// fmtPrice is the test stand-in for the production formatPrice helper —
// 2 decimals + thousands comma, lossless across the price ranges the
// bar visualizes.
func fmtPrice(v float64) string {
	return fmt.Sprintf("%.2f", v)
}

// TestMAPositionBar_ZeroInputReturnsEmpty pins the "insufficient data"
// sentinel: any zero / non-positive input collapses all three rows to
// "" so the caller (service/stock buildBlocks) can skip rendering.
func TestMAPositionBar_ZeroInputReturnsEmpty(t *testing.T) {
	cases := []struct {
		name             string
		ma10, ma5, price float64
	}{
		{"ma10 zero", 0, 100, 100},
		{"ma5 zero", 100, 0, 100},
		{"price zero", 100, 100, 0},
		{"ma10 negative", -1, 100, 100},
		{"ma5 negative", 100, -1, 100},
		{"price negative", 100, 100, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			top, bar, caption := MAPositionBar(tc.ma10, tc.ma5, tc.price, 0, 30, fmtPrice)
			if top != "" || bar != "" || caption != "" {
				t.Errorf("got (%q, %q, %q), want all empty", top, bar, caption)
			}
		})
	}
}

// TestMAPositionBar_BarRowEqualsWidth pins that the bar row is rune-
// aligned at the requested width — required for the bar to line up
// column-for-column with the OHLC bar above when stacked. The top row
// also matches; the caption is left-anchored data and is not width-
// constrained.
func TestMAPositionBar_BarRowEqualsWidth(t *testing.T) {
	width := 41
	top, bar, _ := MAPositionBar(100, 102, 101, 99.5, width, fmtPrice)
	if l := len([]rune(top)); l != width {
		t.Errorf("top row width = %d, want %d (%q)", l, width, top)
	}
	if l := len([]rune(bar)); l != width {
		t.Errorf("bar row width = %d, want %d (%q)", l, width, bar)
	}
}

// TestMAPositionBar_PriceAtCenter pins that the C marker (current
// close) always sits at the bar's center column — the price-relative
// axis is anchored on `price`, so col width/2 IS the price column by
// construction.
func TestMAPositionBar_PriceAtCenter(t *testing.T) {
	width := 31
	_, bar, _ := MAPositionBar(100.5, 99.5, 100, 0, width, fmtPrice)
	runes := []rune(bar)
	centerCol := width / 2
	if runes[centerCol] != ohlcMarkerClose {
		t.Errorf("center col %d = %q, want %q (%q)", centerCol, string(runes[centerCol]), string(ohlcMarkerClose), bar)
	}
}

// TestMAPositionBar_TextMarkersPresent pins that the bar carries the
// literal `M10` and `M5` text markers — these are self-identifying so
// the reader doesn't need a separate label row to disambiguate them.
func TestMAPositionBar_TextMarkersPresent(t *testing.T) {
	// MA10 -1.5%, MA5 +1.5% from price → both well inside the band.
	width := 60
	_, bar, _ := MAPositionBar(98.5, 101.5, 100, 0, width, fmtPrice)
	if !strings.Contains(bar, maMarkerMA10) {
		t.Errorf("bar missing %q marker: %q", maMarkerMA10, bar)
	}
	if !strings.Contains(bar, maMarkerMA5) {
		t.Errorf("bar missing %q marker: %q", maMarkerMA5, bar)
	}
}

// TestMAPositionBar_M10WinsOverM5 pins the priority on overlap: when
// the M5 and M10 text spans collide, M10 wins (more stable, longer-
// term reference takes precedence in tight clusters). With both MAs
// near-equal, the M10 text survives intact.
func TestMAPositionBar_M10WinsOverM5(t *testing.T) {
	// Both MAs equal → both placed at the same column; M10 written last.
	_, bar, _ := MAPositionBar(99, 99, 100, 0, 31, fmtPrice)
	if !strings.Contains(bar, maMarkerMA10) {
		t.Errorf("bar missing M10 marker (should win on overlap): %q", bar)
	}
}

// TestMAPositionBar_SaturationRight pins the right-edge ▶ sentinel for
// an MA well above the +3% band, AND that the marker text still shows
// (bumped inward) so the reader sees both glyphs — the off-screen
// indicator at the edge and the text marker just inside it.
func TestMAPositionBar_SaturationRight(t *testing.T) {
	ma10 := 110.0 // +10% — clips
	ma5 := 100.0
	price := 100.0
	width := 31
	_, bar, _ := MAPositionBar(ma10, ma5, price, 0, width, fmtPrice)
	runes := []rune(bar)
	if runes[len(runes)-1] != maClipRight {
		t.Errorf("right edge = %q, want ▶ in %q", string(runes[len(runes)-1]), bar)
	}
	if !strings.Contains(bar, maMarkerMA10) {
		t.Errorf("M10 marker should still be visible alongside ▶ in %q", bar)
	}
}

// TestMAPositionBar_SaturationLeft pins the left-edge ◀ sentinel for
// an MA well below the −3% band, AND that the text marker still shows
// (bumped inward).
func TestMAPositionBar_SaturationLeft(t *testing.T) {
	ma10 := 90.0 // −10% — clips
	ma5 := 100.0
	price := 100.0
	width := 31
	_, bar, _ := MAPositionBar(ma10, ma5, price, 0, width, fmtPrice)
	runes := []rune(bar)
	if runes[0] != maClipLeft {
		t.Errorf("left edge = %q, want ◀ in %q", string(runes[0]), bar)
	}
	if !strings.Contains(bar, maMarkerMA10) {
		t.Errorf("M10 marker should still be visible alongside ◀ in %q", bar)
	}
}

// TestMAPositionBar_WidthClamping pins that out-of-range widths clamp
// to [10, 80] so callers can pass arbitrary values without checking.
func TestMAPositionBar_WidthClamping(t *testing.T) {
	_, bar, _ := MAPositionBar(100, 100, 100, 0, 2, fmtPrice)
	if l := len([]rune(bar)); l != maBarMinWidth {
		t.Errorf("width=2 clamped to %d, want %d (%q)", l, maBarMinWidth, bar)
	}
	_, bar, _ = MAPositionBar(100, 100, 100, 0, 200, fmtPrice)
	if l := len([]rune(bar)); l != maBarMaxWidth {
		t.Errorf("width=200 clamped to %d, want %d", l, maBarMaxWidth)
	}
}

// TestMAPositionBar_PrevCloseMarkerOnTop pins the ▼ overlay on the top
// row: when prevClose > 0, the top row carries a `▼` glyph at the
// prev-close offset column. The numeric value lives on the OHLCBar's
// OCP row above, so this row only conveys position.
func TestMAPositionBar_PrevCloseMarkerOnTop(t *testing.T) {
	width := 60
	prev := 99.0 // -1% from price
	top, _, _ := MAPositionBar(99.5, 100.5, 100, prev, width, fmtPrice)
	if !strings.Contains(top, string(ohlcMarkerPrev)) {
		t.Errorf("top row missing ▼ marker: %q", top)
	}
}

// TestMAPositionBar_PrevCloseZeroOmitsMarker pins that prevClose=0
// produces a blank top row (the "missing" sentinel) — the figure
// gracefully degrades when upstream didn't supply prev-close.
func TestMAPositionBar_PrevCloseZeroOmitsMarker(t *testing.T) {
	top, _, _ := MAPositionBar(99.5, 100.5, 100, 0, 60, fmtPrice)
	if strings.Contains(top, string(ohlcMarkerPrev)) {
		t.Errorf("top row should be blank when prev=0: %q", top)
	}
}

// TestMAPositionBar_CaptionFormat pins the combined caption format —
// `M5: ▲<value> · M10: ▲<value> · <trend>`. Each MA carries a
// directional arrow (▲ when price above the MA, ▼ when below), and
// the trailing token is the canonical golden-cross / death-cross hint.
func TestMAPositionBar_CaptionFormat(t *testing.T) {
	// Price above both MAs → both arrows ▲.
	// MA5(101) > MA10(100) by ~1% → 5↗10.
	_, _, caption := MAPositionBar(100, 101, 102, 0, 60, fmtPrice)
	if !strings.Contains(caption, "M5: ▲"+fmtPrice(101)) {
		t.Errorf("caption missing M5 part: %q", caption)
	}
	if !strings.Contains(caption, "M10: ▲"+fmtPrice(100)) {
		t.Errorf("caption missing M10 part: %q", caption)
	}
	if !strings.Contains(caption, "5↗10") {
		t.Errorf("caption missing 5↗10 trend token: %q", caption)
	}
}

// TestMAPositionBar_CaptionDeathCross pins the bearish trend token:
// MA5 < MA10 by more than 0.1% → `5↘10`.
func TestMAPositionBar_CaptionDeathCross(t *testing.T) {
	_, _, caption := MAPositionBar(101, 100, 99, 0, 60, fmtPrice)
	if !strings.Contains(caption, "5↘10") {
		t.Errorf("caption missing 5↘10 trend token: %q", caption)
	}
}

// TestMAPositionBar_CaptionEqualMAs pins the noise-floor token: when
// MA5 and MA10 are within ±0.1%, the trend token is `≈`.
func TestMAPositionBar_CaptionEqualMAs(t *testing.T) {
	_, _, caption := MAPositionBar(100, 100, 99, 0, 60, fmtPrice)
	if !strings.Contains(caption, "≈") {
		t.Errorf("caption missing ≈ trend token for equal MAs: %q", caption)
	}
}

// TestMAPositionBar_CaptionPriceBelowMA pins the ▼ arrow direction:
// when price sits below an MA, that MA's caption arrow is ▼.
func TestMAPositionBar_CaptionPriceBelowMA(t *testing.T) {
	// Price=98 below both MAs.
	_, _, caption := MAPositionBar(100, 101, 98, 0, 60, fmtPrice)
	if !strings.Contains(caption, "M5: ▼") {
		t.Errorf("caption missing M5 ▼ arrow: %q", caption)
	}
	if !strings.Contains(caption, "M10: ▼") {
		t.Errorf("caption missing M10 ▼ arrow: %q", caption)
	}
}
