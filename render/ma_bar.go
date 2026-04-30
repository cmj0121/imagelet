package render

import (
	"fmt"
	"strings"
)

// MA position-bar visual constants. The bar shares its axis with the
// OHLCBar — both are centered on the current quote (`price`) at column
// width/2 with a caller-supplied ±band window on either side (fitted
// per-bar in production, falling back to the legacy ±priceBandScale
// default when the caller passes a non-positive band), so a reader can
// stack the two bars in the figure and decode the columns the same way
// for both.
//
// Glyph vocabulary on this bar:
//
//   - `M10` / `M5` literal text at the MA10 / MA5 offset columns.
//     Self-identifying — no separate label-row needed to disambiguate
//     the two markers, and the marker glyph itself reads as the data.
//   - `C` at the center column = current close (the bar's "you are
//     here", same as OHLCBar).
//   - `▼` overlaid on the top row at the prev-close offset column
//     (position only; numeric value pairs with the OHLCBar's `P:`
//     field on the row above).
//
// Out-of-range values clip to the bar edge with `▶` (right-clip) /
// `◀` (left-clip), mirroring OHLCBar's saturation glyphs so readers
// familiar with one decode the other for free.
const (
	maBarBackground = '─'
	maClipRight     = '▶'
	maClipLeft      = '◀'

	maBarMinWidth = 10
	maBarMaxWidth = 80

	// maMarkerMA10 / maMarkerMA5 are the default English bar-marker
	// strings. Used as the seed for DefaultMALabels and referenced by
	// in-package tests; not exported because callers customize through
	// the MALabels struct, not these constants.
	maMarkerMA10 = "M10"
	maMarkerMA5  = "M5"
)

// MALabels carries the bar-marker text and caption-row tokens for
// MAPositionBar. Pre-translated by the caller so render stays
// locale-agnostic.
//
// M5 / M10 stay Latin across all locales today — visual stability beats
// localization for these markers, and the trend token (5↗10 / 5↘10 / ≈)
// carries the localization signal. internal/i18n's catalog enforces
// this convention at the source-of-truth level.
type MALabels struct {
	M5          string
	M10         string
	GoldenCross string // MA5 > MA10 — bullish bias
	DeathCross  string // MA5 < MA10 — bearish bias
	Flat        string // MA5 ≈ MA10 — no directional signal
	Sep         string // separator between fields ("·")
}

// DefaultMALabels returns the English label set. Values match
// internal/i18n/en.go's catalog byte-for-byte.
func DefaultMALabels() MALabels {
	return MALabels{
		M5:          maMarkerMA5,
		M10:         maMarkerMA10,
		GoldenCross: "5↗10",
		DeathCross:  "5↘10",
		Flat:        "≈",
		Sep:         "·",
	}
}

// MAPositionBar returns three width-cell rows:
//
//   - `top`: a single `▼` glyph at the prev-close offset column, blank
//     elsewhere. The numeric prev-close value lives on the OHLCBar's
//     `P:` field above; this row only conveys position.
//   - `bar`: the bar itself — `M10` / `M5` literal text markers at
//     each MA's offset, `C` at the center column = current close, with
//     wick `─` background. M10 wins over M5 when their text spans
//     overlap (more stable, longer-term reference); C always wins at
//     the center column.
//   - `caption`: a left-anchored data row combining each MA's value,
//     direction arrow, and the trend hint —
//     `M5: ▲<v> · M10: ▲<v> · 5↗10`.
//
// `format` is the caller's price formatter — same one used by the
// surrounding stock view (e.g. `formatPrice`) so the bar labels stay
// consistent with the rest of the figure.
//
// Width is clamped to [10, 80]. Returns three empty strings when any
// of ma10, ma5, price is zero or non-positive (signals "insufficient
// data" to the caller, which should skip rendering all three rows).
//
// `prevClose` is the previous trading day's close. Pass 0 to omit
// the ▼ overlay.
//
// `band` is the half-width pct window of the price axis (e.g. 0.03
// = ±3%). When stacking the MA bar under an OHLCBar pass the same
// band to both so the columns align. Non-positive band falls back
// to priceBandScale.
func MAPositionBar(ma10, ma5, price, prevClose float64, width int, band float64, labels MALabels, format func(float64) string) (top, bar, caption string) {
	if ma10 <= 0 || ma5 <= 0 || price <= 0 {
		return "", "", ""
	}
	if width < maBarMinWidth {
		width = maBarMinWidth
	}
	if width > maBarMaxWidth {
		width = maBarMaxWidth
	}

	centerCol := width / 2
	ma10Col, ma10ClipL, ma10ClipR := priceOffsetCol(ma10, price, width, band)
	ma5Col, ma5ClipL, ma5ClipR := priceOffsetCol(ma5, price, width, band)

	// --- Bar row ---
	barRunes := make([]rune, width)
	for i := range barRunes {
		barRunes[i] = maBarBackground
	}

	// Place text markers in priority order: M5 first (lowest priority),
	// then M10 (overwrites M5 on overlap — the more stable, longer-term
	// reference takes precedence in tight clusters), then C at center
	// (always wins; the price reference is the load-bearing read).
	ma5Start := textMarkerStart(ma5Col, len(labels.M5), ma5ClipL, ma5ClipR, width)
	writeRunes(barRunes, labels.M5, ma5Start)

	ma10Start := textMarkerStart(ma10Col, len(labels.M10), ma10ClipL, ma10ClipR, width)
	writeRunes(barRunes, labels.M10, ma10Start)

	barRunes[centerCol] = ohlcMarkerClose

	// Saturation sentinels — only when at least one MA clipped. Bumping
	// inside textMarkerStart already ensures the text doesn't sit on
	// col 0 / width-1 so the sentinel never erases marker text.
	if ma5ClipR || ma10ClipR {
		barRunes[width-1] = maClipRight
	}
	if ma5ClipL || ma10ClipL {
		barRunes[0] = maClipLeft
	}
	bar = string(barRunes)

	// --- Top row: optional ▼ glyph (position only). ---
	top = strings.Repeat(" ", width)
	if prevClose > 0 {
		prevCol, _, _ := priceOffsetCol(prevClose, price, width, band)
		top = overlayPrevGlyph(top, prevCol, width)
	}

	// --- Caption row: M5 + M10 values with arrows + trend hint. ---
	caption = formatMACaption(ma10, ma5, price, labels, format)
	return
}

// textMarkerStart returns the start column for a text marker centered
// on `col`, with one-cell-inward bumps when the marker clipped a band
// edge — so the saturation sentinel `◀` / `▶` at col 0 / width-1
// doesn't erase the marker text. Falls back to centered placement
// (clamped to bar bounds) when no clip applies.
func textMarkerStart(col, textLen int, clipL, clipR bool, width int) int {
	switch {
	case clipL:
		return 1 // text starts just past the ◀ sentinel
	case clipR:
		return width - 1 - textLen // text ends just before the ▶ sentinel
	}
	return centeredStart(col, textLen, width)
}

// formatMACaption returns the combined MA caption row:
// `M5: ▲<value> · M10: ▲<value> · <trend>`. M5 is listed first to
// match how readers scan short-term-momentum first, long-term-trend
// second; the trailing trend token is the canonical golden-cross /
// death-cross read at a glance.
func formatMACaption(ma10, ma5, price float64, labels MALabels, format func(float64) string) string {
	sep := " " + labels.Sep + " "
	return fmt.Sprintf("%s: %s%s%s%s: %s%s%s%s",
		labels.M5, arrowFor(price, ma5), format(ma5),
		sep,
		labels.M10, arrowFor(price, ma10), format(ma10),
		sep,
		maTrendToken(ma5, ma10, labels),
	)
}

// arrowFor returns the directional glyph used in the MA caption row:
// ▲ when price is above the MA, ▼ when below, ─ on exact equality.
// The flat case is rare (closes seldom round to the MA) but included
// so the caption stays well-formed when it does happen.
func arrowFor(price, ma float64) string {
	switch {
	case price > ma:
		return "▲"
	case price < ma:
		return "▼"
	}
	return "─"
}

// maTrendToken returns the trailing trend hint appended to the MA
// caption — the canonical golden-cross / death-cross read at a glance:
//
//   - MA5 > MA10 * 1.001 → `5↗10` (golden-cross territory: short-term
//     momentum exceeds medium-term, bullish bias).
//   - MA5 < MA10 * 0.999 → `5↘10` (death-cross territory: short-term
//     momentum trails medium-term, bearish bias).
//   - within ±0.1% → `≈` (the two MAs are essentially overlapping; no
//     directional signal worth surfacing).
//
// 0.1% is the noise floor — markers smaller than that are visually
// indistinguishable on the position bar above anyway, so the caption
// reflects the same ambiguity instead of falsely picking a side.
func maTrendToken(ma5, ma10 float64, labels MALabels) string {
	switch {
	case ma5 > ma10*1.001:
		return labels.GoldenCross
	case ma5 < ma10*0.999:
		return labels.DeathCross
	}
	return labels.Flat
}
