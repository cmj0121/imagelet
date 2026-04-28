package render

import (
	"math"
	"strings"
)

// OHLC bar visual constants. Marker glyphs sit at the Open and Close
// price columns; body fills the span between them; wicks fill the rest
// to the Low / High edges.
//
// TW/Eastern convention drives the body fill: solid █ for bullish
// (Close >= Open, 紅K), hollow ░ for bearish (Close < Open, 黑K).
// Marker order on the bar (`O...C` vs `C...O`) is the secondary
// direction cue; readers familiar with either convention can decode
// either way.
//
// The prev-close marker (▼) is overlaid on the top edge-label row
// at the prev-close column — pointing down toward the bar — when
// callers supply a positive prev-close. Useful for spotting the
// day's gap (open vs prev-close) and where Close ended relative to
// yesterday at a glance. When the prev-close label would collide
// with the Low / High edge labels, the marker is dropped (edges win).
const (
	ohlcWick        = '─'
	ohlcBodyBull    = '█'
	ohlcBodyBear    = '░'
	ohlcMarkerOpen  = 'O'
	ohlcMarkerClose = 'C'
	ohlcMarkerPrev  = '▼'
)

// OHLCBar renders an OHLC range bar as three equal-width strings: the
// top row carries Low (left-anchored) and High (right-anchored) labels
// at the bar edges with an optional prev-close ▼ marker + label
// overlaid at the prev-close column; the bar row maps [low, high]
// across `width` columns with `O` / `C` glyphs at the Open / Close
// positions and the body filled between them; the bottom row carries
// Open / Close labels centered under their markers.
//
// `format` is supplied by the caller so price formatting (locale,
// precision, thousands separator) lives in one place — typically the
// /stock service's formatPrice. Returns three empty strings when the
// range is degenerate (high <= low) or `width` is too narrow for the
// markers + at least one wick column on each side. Callers that pass
// in a quote should gate on quote.HasOHLC() first.
//
// Bottom-row label collision (Open and Close labels overlap when the
// body is narrow): the label closer to the run-edge wins, the inner
// label is dropped. Doji (Open == Close, markers fuse to `OC`) renders
// a single midpoint label.
//
// `prevClose` is the previous trading day's close. Pass 0 to omit
// the ▼ marker (e.g. when upstream did not supply a prev-close).
// Values outside [low, high] are clamped to the edge — gap-up opens
// pin the marker to the left edge, gap-downs to the right. The
// marker + label is silently dropped if it would collide with the
// Low / High edge labels.
func OHLCBar(open, high, low, last, prevClose float64, width int, format func(float64) string) (top, bar, bottom string) {
	if high <= low || width < 8 {
		return "", "", ""
	}
	span := high - low
	openPos := clampInt(int(math.Round((open-low)/span*float64(width-1))), 0, width-1)
	closePos := clampInt(int(math.Round((last-low)/span*float64(width-1))), 0, width-1)

	top = ohlcEdgeLabels(format(low), format(high), width)
	if prevClose > 0 {
		prevPos := clampInt(int(math.Round((prevClose-low)/span*float64(width-1))), 0, width-1)
		top = overlayPrevMarker(top, format(prevClose), prevPos, len(format(low)), len(format(high)), width)
	}
	bar = ohlcBarRow(openPos, closePos, last >= open, width)
	bottom = ohlcMarkerLabels(format(open), format(last), openPos, closePos, width)
	return
}

// overlayPrevMarker writes `▼ <label>` over `top` at column `prevPos`
// — pointing at the bar's prev-close column. Drops the marker
// silently if its span (▼ + space + label) would collide with the
// Low or High edge labels already on the row, since those are the
// load-bearing axis labels and the prev marker is supplementary.
//
// Layout: ▼ sits at prevPos itself; the label tucks in to the right
// when there's space, otherwise to the left. lowLen / highLen are
// the rendered widths of the existing edge labels and define the
// keep-out zones the overlay must avoid.
func overlayPrevMarker(top, label string, prevPos, lowLen, highLen, width int) string {
	const gap = 1 // space between ▼ and the price label
	span := 1 + gap + len(label)
	// Require a 1-column gap between edge labels and the overlay so a
	// prev-close marker that lands right next to the Low or High label
	// doesn't visually fuse — `4,440.00▼ 4,450.00` reads as one token,
	// `4,440.00 ▼ 4,450.00` reads as two.
	leftEdge := lowLen + 1           // overlay must start at column ≥ lowLen+1
	rightEdge := width - highLen - 1 // overlay must end at column ≤ width-highLen-1
	// Try right-of-marker layout first.
	start := prevPos
	end := start + span
	if start >= leftEdge && end <= rightEdge {
		return writeOverlay(top, prevPos, label, true)
	}
	// Fall back to left-of-marker layout.
	start = prevPos - (gap + len(label))
	end = prevPos + 1
	if start >= leftEdge && end <= rightEdge {
		return writeOverlay(top, prevPos, label, false)
	}
	// Neither side fits without colliding — drop the overlay.
	return top
}

// writeOverlay returns `top` with `▼` placed at prevPos and `label`
// placed adjacent (right side when rightSide=true, else left).
func writeOverlay(top string, prevPos int, label string, rightSide bool) string {
	runes := []rune(top)
	runes[prevPos] = ohlcMarkerPrev
	if rightSide {
		// `▼ <label>` — label starts at prevPos+2.
		writeRunes(runes, label, prevPos+2)
	} else {
		// `<label> ▼` — label ends at prevPos-1, starts at prevPos-1-len(label)+1.
		writeRunes(runes, label, prevPos-1-len(label))
	}
	return string(runes)
}

// ohlcBarRow builds the bar string. Markers are placed at openPos and
// closePos; positions strictly between them carry the body glyph; all
// other positions carry the wick glyph. When openPos == closePos
// (doji), `OC` is fused at adjacent columns (rolling left if openPos
// is at the right edge).
func ohlcBarRow(openPos, closePos int, bullish bool, width int) string {
	body := ohlcBodyBull
	if !bullish {
		body = ohlcBodyBear
	}
	runes := make([]rune, width)
	for i := range runes {
		runes[i] = ohlcWick
	}
	if openPos == closePos {
		if openPos == width-1 {
			runes[openPos-1] = ohlcMarkerOpen
			runes[openPos] = ohlcMarkerClose
		} else {
			runes[openPos] = ohlcMarkerOpen
			runes[openPos+1] = ohlcMarkerClose
		}
		return string(runes)
	}
	leftPos, rightPos := openPos, closePos
	leftMarker, rightMarker := ohlcMarkerOpen, ohlcMarkerClose
	if openPos > closePos {
		leftPos, rightPos = closePos, openPos
		leftMarker, rightMarker = ohlcMarkerClose, ohlcMarkerOpen
	}
	runes[leftPos] = leftMarker
	runes[rightPos] = rightMarker
	for i := leftPos + 1; i < rightPos; i++ {
		runes[i] = body
	}
	return string(runes)
}

// ohlcEdgeLabels returns a width-wide string with `low` left-anchored
// at column 0 and `high` right-anchored at column width-1. Falls back
// to a blank row when the labels would overrun the available width.
func ohlcEdgeLabels(low, high string, width int) string {
	if len(low)+len(high)+1 > width {
		return strings.Repeat(" ", width)
	}
	return low + strings.Repeat(" ", width-len(low)-len(high)) + high
}

// ohlcMarkerLabels centers `open` under openPos and `last` under
// closePos within a width-wide string. Edge clamping keeps both labels
// inside the bar bounds. Collision rule: when the two label spans
// overlap, the label whose marker sits further from the bar interior
// wins (i.e. the one closer to the Low or High edge keeps its column);
// the inner label is dropped to avoid a garbled overlay.
func ohlcMarkerLabels(open, last string, openPos, closePos, width int) string {
	runes := make([]rune, width)
	for i := range runes {
		runes[i] = ' '
	}
	if openPos == closePos {
		// Doji: render a single label at the marker column. Open and
		// last carry the same price, so either is correct.
		writeCentered(runes, open, openPos)
		return string(runes)
	}
	openStart := centeredStart(openPos, len(open), width)
	lastStart := centeredStart(closePos, len(last), width)

	// Collision check (only matters when the two spans overlap).
	openEnd := openStart + len(open)
	lastEnd := lastStart + len(last)
	openLeft := openPos < closePos
	collide := (openLeft && openEnd > lastStart) || (!openLeft && lastEnd > openStart)
	if collide {
		// Drop the inner label — keep the one closer to the bar edge.
		// "Inner" means: when Open sits to the left of Close, Close is
		// inner if it's closer to the right edge less than Open is to
		// the left edge — i.e. the marker with the smaller distance to
		// its own edge wins.
		openEdgeDist := openPos
		if !openLeft {
			openEdgeDist = (width - 1) - openPos
		}
		closeEdgeDist := (width - 1) - closePos
		if !openLeft {
			closeEdgeDist = closePos
		}
		if openEdgeDist <= closeEdgeDist {
			writeRunes(runes, open, openStart)
		} else {
			writeRunes(runes, last, lastStart)
		}
		return string(runes)
	}
	writeRunes(runes, open, openStart)
	writeRunes(runes, last, lastStart)
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

// writeCentered writes `s` centered at `pos` into `runes`, clamping
// to the slice bounds. Out-of-range writes are silently dropped.
func writeCentered(runes []rune, s string, pos int) {
	writeRunes(runes, s, centeredStart(pos, len(s), len(runes)))
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

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
