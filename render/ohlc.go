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
	// separate label row. Pylon parses literal `[...]` as a framed-box
	// element, so plain letters and the U+27E6 / U+27E7 brackets below
	// are used in their place throughout the bar row.
	ohlcMarkerLow  = 'L'
	ohlcMarkerHigh = 'H'

	// Range-frame brackets for the open-market path, where the body
	// fill is suppressed and the L..H span needs visual framing.
	ohlcRangeOpen  = '⟦'
	ohlcRangeClose = '⟧'

	// priceBandScale is a conservative default half-width (±3%) for
	// callers that don't compute an adaptive band via PriceBandFor.
	// Production callers (service/stock) pass an adaptive band fitted
	// to the actual marker values so quiet days fill the bar; this
	// constant remains the fallback for tests and ad-hoc invocations.
	priceBandScale = 0.03

	// maxPriceBand caps the adaptive band so a single far-off marker
	// (typically MA10 in a strongly trending stock) doesn't widen the
	// scale enough to cluster every other marker at the center.
	// Markers beyond ±5% clip to the edge with ▶ / ◀ sentinels.
	maxPriceBand = 0.05
)

// PriceBand is the per-side half-width of the bar's price axis,
// expressed as a fractional offset from `last`. `Lower` covers values
// below `last` (open / low / negative-side MAs); `Upper` covers values
// above (high / positive-side MAs). Splitting the band per side lets
// each half of the bar fit its own widest marker, so a gap-down day
// (low far below, high modestly above) uses the bar's full width on
// both sides instead of compressing the smaller side toward center.
type PriceBand struct {
	Lower float64
	Upper float64
}

// SymmetricBand returns a PriceBand with the same half-width on both
// sides. Useful for tests and ad-hoc invocations that don't care about
// per-side fit.
func SymmetricBand(b float64) PriceBand {
	return PriceBand{Lower: b, Upper: b}
}

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
// `band` is the per-side half-width window of the bar's price axis
// (e.g. `SymmetricBand(0.03)` for ±3%). Production callers compute
// an asymmetric band via PriceBandFor so each half of the bar fits
// its own widest marker — gap-style days no longer compress the
// quieter side toward center. A non-positive side falls back to
// priceBandScale.
//
// `closed` reports whether the trading session is finalized. When
// false (market still open), the close hasn't actually happened yet:
// the `C` glyph and the bullish/bearish body fill are suppressed
// from the bar, and the OCP row renders `C: -` in place of the
// price. The bar still mathematically centers on `last` (so O / H /
// L / ▼ stay positioned correctly relative to the live price). With
// no body fill to frame the day's range, the L / H markers gain
// `⟦` / `⟧` brackets at the columns immediately outside them so the
// session range reads as a single bracketed span; the brackets are
// pylon-safe substitutes for literal `[` / `]`.
func OHLCBar(open, high, low, last, prevClose float64, closed bool, width int, band PriceBand, labels OHLCLabels, format func(float64) string) (top, bar, ocp, hl string) {
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

	bar = ohlcBarRow(openCol, centerCol, lowCol, highCol, hasRange, closed,
		last >= open, openClipL, openClipR, lowClipL, highClipR, width)

	top = strings.Repeat(" ", width)
	if prevClose > 0 {
		prevCol, _, _ := priceOffsetCol(prevClose, last, width, band)
		top = overlayPrevGlyph(top, prevCol, width)
	}

	sep := " " + labels.Sep + " "
	ocp = formatOCPRow(open, last, prevClose, closed, labels, sep, format)
	if hasRange {
		hl = labels.High + ": " + format(high) + sep + labels.Low + ": " + format(low)
	}
	return
}

// formatOCPRow returns the left-anchored OCP data row. The P field
// drops silently when prevClose is 0 — upstream didn't supply a
// previous close (typical for newly-listed symbols or when the chart
// range was too short to walk back to a prior session).
//
// When closed is false, the close field renders `<Close>: -` instead
// of the live price — the trading session hasn't finalized so there
// is no real close yet.
func formatOCPRow(open, last, prevClose float64, closed bool, labels OHLCLabels, sep string, format func(float64) string) string {
	closeStr := format(last)
	if !closed {
		closeStr = "-"
	}
	row := labels.Open + ": " + format(open) + sep + labels.Close + ": " + closeStr
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
// on a degenerate input. The relevant side of `band` (Lower for
// values below price, Upper for values above) is consulted; a non-
// positive side falls back to the priceBandScale default.
func priceOffsetCol(value, price float64, width int, band PriceBand) (col int, clipLeft, clipRight bool) {
	centerCol := width / 2
	if price <= 0 {
		return centerCol, false, false
	}
	pctOffset := (value - price) / price
	side := band.Upper
	if pctOffset < 0 {
		side = band.Lower
	}
	if side <= 0 {
		side = priceBandScale
	}
	raw := float64(centerCol) + pctOffset/side*float64(centerCol)
	col = int(math.Round(raw))
	if col < 0 {
		return 0, true, false
	}
	if col > width-1 {
		return width - 1, false, true
	}
	return col, false, false
}

// PriceBandFor returns an adaptive PriceBand fitted to `values`
// relative to `price`: each side independently fit to its widest
// abs offset × 1.10 (a 10% padding margin so the widest marker on
// that side doesn't sit at the very edge). Capped at maxPriceBand
// per side — values beyond the cap clip to the edge with ▶ / ◀.
// No floor: the band shrinks to fit even tight days, so a 0.1% high
// lands near the right edge instead of clustering near center.
// Sides with no values fall through `priceOffsetCol`'s zero-band
// fallback to priceBandScale.
//
// Splitting per side keeps the bar visually balanced when the data
// is asymmetric — e.g. a gap-down day where low / open are far
// below `last` but high is only modestly above. Each half of the
// bar uses its full width independently. Columns are no longer
// linear in price across the whole bar, but each half is internally
// linear, and the bar is visually anchored at the center column =
// `last`.
//
// Pass the values that should drive the spread — typically the
// non-`last` OHLC + MA markers per bar.
func PriceBandFor(price float64, values ...float64) PriceBand {
	if price <= 0 {
		return PriceBand{Lower: priceBandScale, Upper: priceBandScale}
	}
	var maxLower, maxUpper float64
	for _, v := range values {
		if v <= 0 {
			continue
		}
		offset := (v - price) / price
		if offset < 0 {
			if abs := -offset; abs > maxLower {
				maxLower = abs
			}
		} else if offset > maxUpper {
			maxUpper = offset
		}
	}
	return PriceBand{
		Lower: capSide(maxLower * 1.10),
		Upper: capSide(maxUpper * 1.10),
	}
}

// capSide caps a fitted band side at maxPriceBand so a single far-
// off marker still triggers the ▶ / ◀ saturation sentinel instead of
// silently widening the scale. Values <= 0 pass through; the caller
// (priceOffsetCol) folds zero-band sides into priceBandScale.
func capSide(band float64) float64 {
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

// ohlcBarRow builds the bar string. Marker priority on shared columns
// is closed-state dependent. Closed: `O` / `C` win over `L` / `H` —
// body fill already carries the day's range, the OC pair carries the
// more important read. Open: `L` / `H` win over `O` so a hidden `L`
// doesn't leave the `⟦` bracket pointing at nothing; the `O` value
// stays readable from the OCP data row. On doji (openCol == centerCol)
// the row shows a single `O` (O wins over C on the shared column).
func ohlcBarRow(openCol, centerCol, lowCol, highCol int, hasRange, closed, bullish, openClipL, openClipR, lowClipL, highClipR bool, width int) string {
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
	if closed {
		for i := leftCol + 1; i < rightCol; i++ {
			runes[i] = body
		}
	}

	lowBarCol, highBarCol := lowCol, highCol
	if hasRange {
		if lowClipL {
			lowBarCol = 1
		}
		if highClipR {
			highBarCol = width - 2
		}
	}

	// Closed-market: L / H draw only OUTSIDE the OC body span — the
	// body fill is itself the cue that L / H sits within open..close.
	if closed && hasRange {
		if lowBarCol < leftCol {
			runes[lowBarCol] = ohlcMarkerLow
		}
		if highBarCol > rightCol {
			runes[highBarCol] = ohlcMarkerHigh
		}
	}

	if closed {
		runes[centerCol] = ohlcMarkerClose
	}
	runes[openBarCol] = ohlcMarkerOpen

	// Open-market: no body fill, so L / H always draw and gain a
	// `⟦` / `⟧` frame to mark the day's range. Brackets skip when
	// they'd land on a saturation sentinel or clobber O.
	if !closed && hasRange {
		runes[lowBarCol] = ohlcMarkerLow
		runes[highBarCol] = ohlcMarkerHigh
		if !lowClipL && lowBarCol-1 >= 0 && lowBarCol-1 != openBarCol {
			runes[lowBarCol-1] = ohlcRangeOpen
		}
		if !highClipR && highBarCol+1 < width && highBarCol+1 != openBarCol {
			runes[highBarCol+1] = ohlcRangeClose
		}
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
