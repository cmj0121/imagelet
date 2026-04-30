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

// TestOHLCBarBullish pins the bullish layout: last > open, body fill
// is solid `█`, and the bar reads `O…C` left-to-right because open
// sits below the current close (last). With width=21, Last=100,
// Open=99 (-1%), and no session range supplied, priceOffsetCol
// places open at col 7 and the C marker at the center col 10. Body
// fills cols 8-9 with `█` and the wick `─` spans the rest (legacy
// fallback when high/low are 0).
func TestOHLCBarBullish(t *testing.T) {
	top, bar, ocp, hl := OHLCBar(99, 0, 0, 100, 0, 21, priceBandScale, DefaultOHLCLabels(), plain)
	wantBar := "───────O██C──────────"
	if bar != wantBar {
		t.Errorf("bar:\n got %q\nwant %q", bar, wantBar)
	}
	if strings.TrimSpace(top) != "" {
		t.Errorf("top row should be blank when prev=0: %q", top)
	}
	if hl != "" {
		t.Errorf("hl row should be empty when no range supplied: %q", hl)
	}
	if !strings.Contains(ocp, "O: 99") || !strings.Contains(ocp, "C: 100") {
		t.Errorf("ocp row missing O/C labels: %q", ocp)
	}
	if len(top) != 21 || len([]rune(bar)) != 21 {
		t.Errorf("top/bar not width-aligned: top=%d bar=%d (want 21)",
			len(top), len([]rune(bar)))
	}
}

// TestOHLCBarHLBrackets pins the session-range markers: `L` at the
// low offset column, `H` at the high offset column overlaid on the
// wick. Last=100, Open=99 (-1%), High=102 (+2%), Low=98 (-2%) →
// with width=21 and ±3% band, low at col 3, high at col 17, open
// at col 7, close at center col 10. Wick `─` fills the rest. Letter
// glyphs (not literal `[` / `]`) avoid conflicting with pylon's
// framed-box parser.
func TestOHLCBarHLBrackets(t *testing.T) {
	_, bar, _, hl := OHLCBar(99, 102, 98, 100, 0, 21, priceBandScale, DefaultOHLCLabels(), plain)
	wantBar := "───L───O██C──────H───"
	if bar != wantBar {
		t.Errorf("bar:\n got %q\nwant %q", bar, wantBar)
	}
	if !strings.Contains(hl, "H: 102") || !strings.Contains(hl, "L: 98") {
		t.Errorf("hl row missing H/L labels: %q", hl)
	}
}

// TestOHLCBarHLBracketsClipped pins the saturation behaviour for
// session-range markers: when low/high exceed the ±3% band, the
// `[` / `]` glyphs bump one column inward and the edge column
// carries `◀` / `▶`. Last=100, Open=100 (doji at center), High=110
// (+10%), Low=90 (-10%).
func TestOHLCBarHLBracketsClipped(t *testing.T) {
	_, bar, _, _ := OHLCBar(100, 110, 90, 100, 0, 21, priceBandScale, DefaultOHLCLabels(), plain)
	runes := []rune(bar)
	if runes[0] != maClipLeft {
		t.Errorf("col 0 = %q, want ◀", string(runes[0]))
	}
	if runes[1] != ohlcMarkerLow {
		t.Errorf("col 1 = %q, want L (bumped low marker)", string(runes[1]))
	}
	if runes[19] != ohlcMarkerHigh {
		t.Errorf("col 19 = %q, want H (bumped high marker)", string(runes[19]))
	}
	if runes[20] != maClipRight {
		t.Errorf("col 20 = %q, want ▶", string(runes[20]))
	}
}

// TestOHLCBarBearish flips the body fill to hollow `░` when last <
// open, and the bar reads `C…O` because Close (= price = center)
// sits to the left of Open on the price axis.
func TestOHLCBarBearish(t *testing.T) {
	// Last=100, Open=101 (+1%) → openCol=13, body cols 11-12 hollow.
	_, bar, _, _ := OHLCBar(101, 0, 0, 100, 0, 21, priceBandScale, DefaultOHLCLabels(), plain)
	wantBar := "──────────C░░O───────"
	if bar != wantBar {
		t.Errorf("bar:\n got %q\nwant %q", bar, wantBar)
	}
}

// TestOHLCBarDoji pins the equal-Open-Close case: open == close
// → both markers fuse at the center column. Bar shows a single `C`
// (open and close coincide), and the OCP row carries both values
// (numerically identical here).
func TestOHLCBarDoji(t *testing.T) {
	_, bar, ocp, _ := OHLCBar(100, 0, 0, 100, 0, 21, priceBandScale, DefaultOHLCLabels(), plain)
	// Center col 10 holds C; rest is wicks. No `O` glyph because open
	// and close share the same column.
	wantBar := "──────────C──────────"
	if bar != wantBar {
		t.Errorf("doji bar:\n got %q\nwant %q", bar, wantBar)
	}
	if !strings.Contains(ocp, "O: 100") || !strings.Contains(ocp, "C: 100") {
		t.Errorf("doji ocp should carry O: 100 / C: 100 labels, got: %q", ocp)
	}
}

// TestOHLCBarLastNonPositive returns four empty strings when last
// is zero or negative — the price-centered axis can't divide by it,
// and the upstream gate (quote.HasOHLC) should already exclude this
// case, but the helper still fails closed.
func TestOHLCBarLastNonPositive(t *testing.T) {
	top, bar, ocp, hl := OHLCBar(100, 0, 0, 0, 0, 21, priceBandScale, DefaultOHLCLabels(), plain)
	if top != "" || bar != "" || ocp != "" || hl != "" {
		t.Errorf("last<=0 should return empties, got %q / %q / %q / %q", top, bar, ocp, hl)
	}
}

// TestOHLCBarTooNarrow rejects widths under 8 — there is not enough
// room for the markers + at least one wick column on each side.
func TestOHLCBarTooNarrow(t *testing.T) {
	top, bar, ocp, hl := OHLCBar(99, 0, 0, 100, 0, 7, priceBandScale, DefaultOHLCLabels(), plain)
	if top != "" || bar != "" || ocp != "" || hl != "" {
		t.Errorf("narrow width should return empties, got %q / %q / %q / %q", top, bar, ocp, hl)
	}
}

// TestOHLCBarOpenSaturatesLeft pins gap-up behaviour: when today's
// open is far below current price (a big intraday rally), the `◀`
// sentinel lands at col 0 and the `O` marker bumps one column inward
// to col 1 so both glyphs remain visible. The body extends from col
// 2 to centerCol-1 with bullish fill.
func TestOHLCBarOpenSaturatesLeft(t *testing.T) {
	// Open=90 → -10%, well below the -3% band.
	_, bar, _, _ := OHLCBar(90, 0, 0, 100, 0, 21, priceBandScale, DefaultOHLCLabels(), plain)
	runes := []rune(bar)
	if runes[0] != maClipLeft {
		t.Errorf("left edge = %q, want ◀ in %q", string(runes[0]), bar)
	}
	if runes[1] != ohlcMarkerOpen {
		t.Errorf("col 1 = %q, want O (bumped from clipped col) in %q",
			string(runes[1]), bar)
	}
	if runes[10] != ohlcMarkerClose {
		t.Errorf("center col = %q, want C in %q", string(runes[10]), bar)
	}
}

// TestOHLCBarOpenSaturatesRight pins gap-down behaviour: when today's
// open is far above current price (sell-off after open), `▶` lands
// at col width-1 and the `O` marker bumps inward to col width-2.
func TestOHLCBarOpenSaturatesRight(t *testing.T) {
	// Open=110 → +10%, well above the +3% band.
	_, bar, _, _ := OHLCBar(110, 0, 0, 100, 0, 21, priceBandScale, DefaultOHLCLabels(), plain)
	runes := []rune(bar)
	if runes[20] != maClipRight {
		t.Errorf("right edge = %q, want ▶ in %q", string(runes[20]), bar)
	}
	if runes[19] != ohlcMarkerOpen {
		t.Errorf("col 19 = %q, want O (bumped from clipped col) in %q",
			string(runes[19]), bar)
	}
}

// TestOHLCBarBarRowEqualWidth pins the column-alignment invariant:
// the top + bar rows are exactly `width` runes long (counting `█` /
// `░` / `─` as single columns). This is what lets pylon's monospace
// SVG render align the rows visually. OCP / HL are left-anchored
// data rows and not width-padded.
func TestOHLCBarBarRowEqualWidth(t *testing.T) {
	top, bar, _, _ := OHLCBar(21180, 0, 0, 21234, 0, 45, priceBandScale, DefaultOHLCLabels(), plain)
	if got := runeLen(top); got != 45 {
		t.Errorf("top row width = %d, want 45 (%q)", got, top)
	}
	if got := runeLen(bar); got != 45 {
		t.Errorf("bar row width = %d, want 45 (%q)", got, bar)
	}
}

// TestOHLCBarPrevCloseMarker pins the ▼ overlay on the top row at
// the prev-close offset column. With Last=100 and prev=99.5 across
// width=45, prevCol = centerCol(22) + round(-0.005/0.03 * 22) = 18.
// The marker sits at column 18; the numeric value lives on the OCP
// row as `P: 99` (no longer alongside the glyph).
func TestOHLCBarPrevCloseMarker(t *testing.T) {
	top, _, ocp, _ := OHLCBar(99, 0, 0, 100, 99.5, 45, priceBandScale, DefaultOHLCLabels(), plain)
	if !strings.Contains(top, string(ohlcMarkerPrev)) {
		t.Errorf("top row missing ▼ marker: %q", top)
	}
	if !strings.Contains(ocp, "P: 99") {
		t.Errorf("ocp row missing prev-close P: label: %q", ocp)
	}
}

// TestOHLCBarPrevCloseZeroSkipsMarker pins that prevClose=0 (the
// "missing" sentinel) renders a blank top row — no ▼ overlay — and
// drops the `P:` field from the OCP row.
func TestOHLCBarPrevCloseZeroSkipsMarker(t *testing.T) {
	top, _, ocp, _ := OHLCBar(99, 0, 0, 100, 0, 45, priceBandScale, DefaultOHLCLabels(), plain)
	if strings.Contains(top, string(ohlcMarkerPrev)) {
		t.Errorf("top row should have no ▼ when prev=0: %q", top)
	}
	if strings.TrimSpace(top) != "" {
		t.Errorf("top row should be all blank when prev=0: %q", top)
	}
	if strings.Contains(ocp, "P:") {
		t.Errorf("ocp row should drop P: field when prev=0: %q", ocp)
	}
}

// TestOHLCBarPrevCloseSaturates pins the saturation behaviour: a
// prev close that's >3% off the current price clips to a bar edge.
// The ▼ glyph still appears on the top row.
func TestOHLCBarPrevCloseSaturates(t *testing.T) {
	// Prev=110 → +10%, way past the +3% band → clips to col width-1.
	top, _, _, _ := OHLCBar(99, 0, 0, 100, 110, 45, priceBandScale, DefaultOHLCLabels(), plain)
	if !strings.Contains(top, string(ohlcMarkerPrev)) {
		t.Errorf("clipped prev should still draw ▼: %q", top)
	}
}

// TestOHLCBarPrevCloseInsideBody pins that the prev marker lives on
// the top row only — the bar row is unaffected even when prevClose
// would land on the body fill column.
func TestOHLCBarPrevCloseInsideBody(t *testing.T) {
	_, bar, _, _ := OHLCBar(99, 0, 0, 100, 99.5, 45, priceBandScale, DefaultOHLCLabels(), plain)
	if strings.Contains(bar, string(ohlcMarkerPrev)) {
		t.Errorf("bar row must not carry ▼: %q", bar)
	}
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
