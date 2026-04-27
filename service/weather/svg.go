package weather

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/cmj0121/pylon/src/go/pkg/pylon"
)

// SVG cell geometry mirrors pylon's internal grid (svgCellW=5, svgCellH=13,
// svgFontPx=10). Keeping the same cell size for our icon and caption rows
// means glyphs line up with the embedded pylon banner without per-element
// metric adjustment. If pylon's grid ever changes upstream we'll see a
// visual seam — caught by the layout-snapshot test below.
const (
	svgCellW  = 5
	svgCellH  = 13
	svgFontPx = 10
)

// pylonSVGSizeRE pulls the viewBox dimensions out of pylon's SVG output.
// Pylon emits exactly `viewBox="0 0 W H"` (verified in pylon/render_svg.go),
// so this regex is sufficient. Falling back to defaults if it ever breaks
// is preferable to panicking — the embedded banner just visually drifts.
var pylonSVGSizeRE = regexp.MustCompile(`viewBox="0 0 (\d+) (\d+)"`)

// composeSVG builds the /weather hero as a single SVG document: ASCII
// condition icon on the left, pylon-rendered temperature banner to its
// right (nested via a translate-group, so the banner's <text>/<rect>
// contents paint at their natural pylon coordinates inside our outer
// canvas), and caption rows below at the banner's left edge. Mirrors
// composePNG's geometry — same padding, gap, vertical-center rules — so
// PNG and SVG look like the same layout in two formats.
//
// XML escape: every text segment goes through escapeSVGText. Icon glyphs
// use single quotes; captions can include user-influenced text (location
// label) and USGS-supplied quake place names. Pylon escapes its own
// banner content, so the embedded inner SVG arrives pre-escaped.
//
// Returns a self-contained `<svg xmlns=… viewBox=… width=… height=…>` doc.
func composeSVG(headline string, icon, captions []string) string {
	bannerSVG := pylon.RenderSVG(pylon.Parse(headlineSource(headline)))
	bannerW, bannerH, ok := parsePylonSVGSize(bannerSVG)
	if !ok {
		// Defensive fallback. Pylon's emitter has emitted viewBox since
		// the SVG renderer landed, but if a future change drops it we'd
		// still want the page to render *something* instead of crashing.
		bannerW = 200
		bannerH = 80
	}
	bannerInner := stripPylonOuterSVG(bannerSVG)

	iconW := iconCells * svgCellW
	iconH := len(icon) * svgCellH

	rowH := bannerH
	if iconH > rowH {
		rowH = iconH
	}

	bannerX := composePadding + iconW + composeGap
	bannerY := composePadding + (rowH-bannerH)/2

	iconY := composePadding + (rowH-iconH)/2

	captionsX := bannerX
	captionsY := composePadding + rowH + composeGap

	canvasW := composePadding + iconW + composeGap + bannerW + composePadding
	canvasH := composePadding + rowH + composeGap + len(captions)*svgCellH + composePadding

	var b strings.Builder
	fmt.Fprintf(&b,
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" width="%d" height="%d">`,
		canvasW, canvasH, canvasW, canvasH)
	b.WriteByte('\n')

	// Outer style. Pylon's inner <style> applies document-wide once
	// embedded, but we set our own here too so direct viewers (curling
	// the SVG without HTML wrapping) get a font-family hint even if the
	// pylon style block ever drops it.
	fmt.Fprintf(&b,
		`<style>text{font-family:"Cascadia Code","JetBrains Mono","Iosevka",ui-monospace,Menlo,Consolas,monospace;font-size:%dpx;fill:#0f1c2d;white-space:pre}</style>`,
		svgFontPx)
	b.WriteByte('\n')

	// Icon rows. Pylon uses baseline = (i+1)*cellH - (cellH-fontPx); we
	// follow the same formula so icon text sits at the same vertical
	// position relative to its cell as pylon's text does.
	for i, row := range icon {
		baseline := iconY + i*svgCellH + svgFontPx
		fmt.Fprintf(&b,
			`<text x="%d" y="%d" textLength="%d" lengthAdjust="spacingAndGlyphs" xml:space="preserve">%s</text>`,
			composePadding, baseline, iconW, escapeSVGText(row))
		b.WriteByte('\n')
	}

	// Banner — embed pylon's inner SVG content in a translate group so
	// its (0,0)-anchored coordinates land at (bannerX, bannerY) in our
	// canvas. The pylon `<style>` rides along inside the group; SVG
	// styles ignore group containment for matching, so it still applies
	// document-wide.
	fmt.Fprintf(&b, `<g transform="translate(%d,%d)">`, bannerX, bannerY)
	b.WriteString(bannerInner)
	b.WriteString(`</g>`)
	b.WriteByte('\n')

	// Captions below.
	for i, line := range captions {
		baseline := captionsY + i*svgCellH + svgFontPx
		cells := utf8.RuneCountInString(line)
		if cells == 0 {
			cells = 1
		}
		fmt.Fprintf(&b,
			`<text x="%d" y="%d" textLength="%d" lengthAdjust="spacingAndGlyphs" xml:space="preserve">%s</text>`,
			captionsX, baseline, cells*svgCellW, escapeSVGText(line))
		b.WriteByte('\n')
	}

	b.WriteString(`</svg>`)
	return b.String()
}

// parsePylonSVGSize extracts the (width, height) from pylon's SVG output
// by reading its viewBox. Returns ok=false on any parse failure so
// composeSVG can fall back to defaults rather than emit a broken doc.
func parsePylonSVGSize(svg string) (w, h int, ok bool) {
	m := pylonSVGSizeRE.FindStringSubmatch(svg)
	if m == nil {
		return 0, 0, false
	}
	w, errW := strconv.Atoi(m[1])
	h, errH := strconv.Atoi(m[2])
	if errW != nil || errH != nil {
		return 0, 0, false
	}
	return w, h, true
}

// stripPylonOuterSVG returns the inner content of a pylon SVG document —
// everything between the opening `<svg …>` and the closing `</svg>`. We
// embed this inside a translate-group in our outer canvas so the banner's
// natural coordinates paint at the right offset.
func stripPylonOuterSVG(svg string) string {
	if i := strings.Index(svg, ">"); i >= 0 {
		svg = svg[i+1:]
	}
	svg = strings.TrimSpace(svg)
	return strings.TrimSuffix(svg, "</svg>")
}

// escapeSVGText escapes the five XML special characters for inclusion in
// `<text>` element content. Matches pylon's internal escape set.
var svgTextEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&apos;",
)

func escapeSVGText(s string) string {
	return svgTextEscaper.Replace(s)
}
