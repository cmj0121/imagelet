package render

import (
	"math"
	"strings"
)

// OHLCLabels carries the data-row label prefixes for OHLCBar's `ocp`
// and `hl` rows. Pre-translated by the caller — render itself stays
// locale-agnostic so tests and ad-hoc invocations don't take on an
// i18n dependency.
//
// Note: the bar-drawing markers (the single-rune `O`/`H`/`L`/`C`/`▼`
// glyphs written into the bar at marker columns) are NOT driven from
// this struct. Pylon's ASCII bar uses 1-cell rune positioning, and a
// 2-cell CJK glyph would misalign the bar; the markers stay Latin
// even when the data-row labels localize. Readers bridge the bar
// glyph and the data-row prefix mentally.
type OHLCLabels struct {
	Open  string
	High  string
	Low   string
	Close string
	Prev  string
	Sep   string // separator between fields ("·")
}

// DefaultOHLCLabels returns the English label set. The values match
// internal/i18n/en.go's catalog byte-for-byte — render and the i18n
// catalog must agree on the en defaults so a caller that hasn't
// wired through a locale produces output identical to the previous
// pre-i18n behavior.
func DefaultOHLCLabels() OHLCLabels {
	return OHLCLabels{
		Open:  "O",
		High:  "H",
		Low:   "L",
		Close: "C",
		Prev:  "P",
		Sep:   "·",
	}
}

// OHLC bar visual constants. The bar centers on the current quote
// (`last` = today's close, or the intraday partial when the market
// is open) at column width/2 and spans a ±priceBandScale window
// (3% by default) on either side. Marker glyphs sit at:
//
//   - `C` at the center column = current close (= the price the
//     headline caption is reporting; the bar's "you are here").
//   - `O` at today's open offset, where the offset is computed from
//     (open − last) / last clipped into ±priceBandScale.
//   - `▼` overlaid on the top row at the prev-close offset, with a
//     `P:<price>` label tucked next to it when there's room.
//
// The bar body fills the span between O and C: solid `█` for bullish
// (last >= open, 紅K), hollow `░` for bearish (last < open, 黑K).
// TW/Eastern reading convention drives the fill choice; the marker
// order on the bar (`O…C` vs `C…O`) provides the secondary cue —
// readers familiar with either decode the other for free.
//
// High and low are NOT visualized on the bar — readers have them in
// the volume / caption rows above. Centering on price (instead of
// the day's range midpoint) keeps the OHLC bar directly comparable
// to the MA position bar that stacks below: both share the same
// price-relative axis fitted to their own markers, so a quick scan
// reveals where today's open / prev close / MA5 / MA10 all sit
// relative to the current quote.
const (
	ohlcWick        = '─'
	ohlcBodyBull    = '█'
	ohlcBodyBear    = '░'
	ohlcMarkerOpen  = 'O'
	ohlcMarkerClose = 'C'
	ohlcMarkerPrev  = '▼'
	// Letter glyphs at the session-low / session-high offset columns —
	// self-identifying like `O` / `C`, so the reader doesn't need a
	// separate label row to disambiguate them. Pylon's `[...]` framed-
	// box parser ruled out literal `[` / `]` brackets; plain letters
	// have no special meaning in pylon source either.
	ohlcMarkerLow  = 'L'
	ohlcMarkerHigh = 'H'

	// priceBandScale is a conservative default half-width (±3%) for
	// callers that don't compute an adaptive band via PriceBandFor.
	// Production callers (service/stock) pass an adaptive band fitted
	// to the actual marker values so quiet days fill the bar; this
	// constant remains the fallback for tests and ad-hoc invocations.
	priceBandScale = 0.03

	// minPriceBand floors the adaptive band so a degenerate input
	// (all markers equal to price) still yields a usable bar instead
	// of dividing by zero.
	minPriceBand = 0.005
	// maxPriceBand caps the adaptive band so a single far-off marker
	// (typically MA10 in a strongly trending stock) doesn't widen the
	// scale enough to cluster every other marker at the center.
	// Markers beyond ±5% clip to the edge with ▶ / ◀ sentinels.
	maxPriceBand = 0.05
)

// OHLCBar renders a price-relative range bar as four equal-width
// strings:
//
//   - `top`: the prev-close marker row — a single `▼` glyph at the
//     prev-close offset column, blank elsewhere. The numeric value
//     lives on the OCP row below (`P: <price>`); the marker on this
//     row only conveys position relative to the bar.
//   - `bar`: the bar itself — `L` / `H` letters at the day's low /
//     high, `O` at today's open, `C` at the center column = current
//     close, body `█` (bullish) / `░` (bearish) between O and C, and
//     wick `─` everywhere else.
//   - `ocp`: a left-anchored data row — `O: <open> · C: <close> · P: <prev>`.
//     The P field drops when prevClose is 0.
//   - `hl`: a left-anchored data row — `H: <high> · L: <low>`. Empty
//     when the caller didn't supply a meaningful range.
//
// Glyph vocabulary (shared with MAPositionBar so both bars decode
// identically):
//
//   - `▼` previous close (position only; value in OCP row).
//   - `C` current close (always at the bar's center column).
//   - `O` today's open (offset from C by % difference).
//   - `L` / `H` session low / high.
//   - `█` / `░` body fill, bullish / bearish.
//   - `▶` / `◀` saturation: marker exceeds the fitted band edge.
//
// `format` is supplied by the caller so price formatting (locale,
// precision, thousands separator) lives in one place — typically the
// /stock service's formatPrice. Returns four empty strings when
// `last` is non-positive or `width` is too narrow for the markers
// (< 8 columns).
//
// `prevClose` is the previous trading day's close. Pass 0 to omit
// the ▼ marker AND the `P:` field on the OCP row.
// `high` / `low` (when both > 0 with low <= high) drive the bracket
// markers and the HL data row. Pass 0 for either to omit.
//
// `band` is the half-width pct window of the bar's price axis (e.g.
// 0.03 = ±3%). Pass priceBandScale for the legacy fixed window;
// production callers compute an adaptive band via PriceBandFor so
// markers fill the bar on quiet days. Non-positive band falls back
// to priceBandScale.
func OHLCBar(open, high, low, last, prevClose float64, width int, band float64, labels OHLCLabels, format func(float64) string) (top, bar, ocp, hl string) {
	if last <= 0 || width < 8 {
		return "", "", "", ""
	}
	centerCol := width / 2
	openCol, openClipL, openClipR := priceOffsetCol(open, last, width, band)

	hasRange := high > 0 && low > 0 && low <= high
	var lowCol, highCol int
	var lowClipL, highClipR bool
	if hasRange {
		lowCol, lowClipL, _ = priceOffsetCol(low, last, width, band)
		highCol, _, highClipR = priceOffsetCol(high, last, width, band)
	}

	bar = ohlcBarRow(openCol, centerCol, lowCol, highCol, hasRange,
		last >= open, openClipL, openClipR, lowClipL, highClipR, width)

	top = strings.Repeat(" ", width)
	if prevClose > 0 {
		prevCol, _, _ := priceOffsetCol(prevClose, last, width, band)
		top = overlayPrevGlyph(top, prevCol, width)
	}

	sep := " " + labels.Sep + " "
	ocp = formatOCPRow(open, last, prevClose, labels, sep, format)
	if hasRange {
		hl = labels.High + ": " + format(high) + sep + labels.Low + ": " + format(low)
	}
	return
}

// formatOCPRow returns the left-anchored OCP data row. The P field
// drops silently when prevClose is 0 — upstream didn't supply a
// previous close (typical for newly-listed symbols or when the chart
// range was too short to walk back to a prior session).
func formatOCPRow(open, last, prevClose float64, labels OHLCLabels, sep string, format func(float64) string) string {
	row := labels.Open + ": " + format(open) + sep + labels.Close + ": " + format(last)
	if prevClose > 0 {
		row += sep + labels.Prev + ": " + format(prevClose)
	}
	return row
}

// priceOffsetCol maps `value` onto a width-cell bar centered on
// `price` at column `width/2`, with the bar spanning ±band around
// price. Returns the column index plus clip-left / clip-right flags
// when the offset exceeds the band so the caller can swap in a ▶ /
// ◀ saturation sentinel at the edge.
//
// Non-positive `price` returns the center column with no clipping —
// the caller is expected to gate on `price > 0` before drawing, but
// this conservative fallback keeps the helper from dividing by zero
// on a degenerate input. Non-positive `band` falls back to the
// priceBandScale default.
func priceOffsetCol(value, price float64, width int, band float64) (col int, clipLeft, clipRight bool) {
	centerCol := width / 2
	if price <= 0 {
		return centerCol, false, false
	}
	if band <= 0 {
		band = priceBandScale
	}
	pctOffset := (value - price) / price
	raw := float64(centerCol) + pctOffset/band*float64(centerCol)
	col = int(math.Round(raw))
	if col < 0 {
		return 0, true, false
	}
	if col > width-1 {
		return width - 1, false, true
	}
	return col, false, false
}

// PriceBandFor returns an adaptive half-width pct band fitted to
// `values` relative to `price`: the largest absolute pct offset
// across the non-zero values, plus a 10% padding margin so the
// widest marker doesn't sit at the very edge. Floored at minPriceBand
// (so degenerate all-equal-to-price inputs still yield a usable bar)
// and capped at maxPriceBand (so a single far-off MA doesn't widen
// the scale enough to cluster every other marker at center; values
// beyond the cap clip to the edge with ▶ / ◀).
//
// Pass the values that should drive the spread — typically the
// union of OHLC + prev-close + MA markers when sharing the band
// across stacked bars so they decode column-by-column.
func PriceBandFor(price float64, values ...float64) float64 {
	if price <= 0 {
		return priceBandScale
	}
	var maxAbs float64
	for _, v := range values {
		if v <= 0 {
			continue
		}
		abs := math.Abs((v - price) / price)
		if abs > maxAbs {
			maxAbs = abs
		}
	}
	band := maxAbs * 1.10
	if band < minPriceBand {
		return minPriceBand
	}
	if band > maxPriceBand {
		return maxPriceBand
	}
	return band
}

// overlayPrevGlyph writes `▼` over `top` at column `prevCol`. The
// numeric value lives on the OCP data row below (`P: <price>`), so
// this top-row marker only conveys position. Bumps one column inward
// when the offset would clip the bar edge so the ▼ is never erased
// by a `◀` / `▶` saturation sentinel; falls back to no-op when
// prevCol is somehow out of range.
func overlayPrevGlyph(top string, prevCol, width int) string {
	if prevCol < 0 || prevCol >= width {
		return top
	}
	runes := []rune(top)
	runes[prevCol] = ohlcMarkerPrev
	return string(runes)
}

// ohlcBarRow builds the bar string. The close glyph (`C`) is always at
// `centerCol`; `O` sits at the open's offset column. Plain letters
// are used for L/H instead of `[...]` because pylon parses literal
// brackets as a framed box and would re-frame the row.
//
// On doji (openCol == centerCol) the row shows a single `C` glyph —
// O and C coincide; the OCP data row still carries both identities.
//
// Saturation: when an offset exceeds ±band, the marker bumps
// one column inward and the edge column carries `◀` / `▶` so the
// reader sees both the marker AND the off-screen indicator. Marker
// priority on shared columns: `O` / `C` win over `L` / `H` — open and
// close are the load-bearing OHLC values; L / H only bound the wick.
func ohlcBarRow(openCol, centerCol, lowCol, highCol int, hasRange, bullish, openClipL, openClipR, lowClipL, highClipR bool, width int) string {
	body := ohlcBodyBull
	if !bullish {
		body = ohlcBodyBear
	}
	runes := make([]rune, width)
	for i := range runes {
		runes[i] = ohlcWick
	}

	// Bump clipped open marker inward so the saturation sentinel
	// doesn't erase it.
	openBarCol := openCol
	switch {
	case openClipL:
		openBarCol = 1
	case openClipR:
		openBarCol = width - 2
	}

	// Body span = strictly between min(open, close) and max(open, close).
	leftCol, rightCol := openBarCol, centerCol
	if openBarCol > centerCol {
		leftCol, rightCol = centerCol, openBarCol
	}
	for i := leftCol + 1; i < rightCol; i++ {
		runes[i] = body
	}

	// Range brackets — only drawn when they sit OUTSIDE the OC body
	// span; otherwise the O/C marker covers the same column and wins
	// (the OC pair carries the more important read).
	if hasRange {
		lowBarCol, highBarCol := lowCol, highCol
		if lowClipL {
			lowBarCol = 1
		}
		if highClipR {
			highBarCol = width - 2
		}
		if lowBarCol < leftCol {
			runes[lowBarCol] = ohlcMarkerLow
		}
		if highBarCol > rightCol {
			runes[highBarCol] = ohlcMarkerHigh
		}
	}

	// O / C markers (top priority — written last).
	runes[centerCol] = ohlcMarkerClose
	if openBarCol != centerCol {
		runes[openBarCol] = ohlcMarkerOpen
	}

	// Saturation sentinels at the bar edges.
	if openClipR || highClipR {
		runes[width-1] = maClipRight
	}
	if openClipL || lowClipL {
		runes[0] = maClipLeft
	}
	return string(runes)
}

// centeredStart returns the start column for a label of `n` runes
// centered at `pos`, clamped to [0, width-n].
func centeredStart(pos, n, width int) int {
	start := pos - n/2
	if start < 0 {
		return 0
	}
	if start+n > width {
		return width - n
	}
	return start
}

// writeRunes copies the runes of `s` into `runes` starting at `start`,
// stopping at the slice end. ASCII-only callers (price digits, comma,
// dot) make len(s) == rune count safe to assume here.
func writeRunes(runes []rune, s string, start int) {
	for i, r := range s {
		idx := start + i
		if idx < 0 || idx >= len(runes) {
			continue
		}
		runes[idx] = r
	}
}
