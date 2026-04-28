// Package render — PNG rasterization of pylon SVG output.
//
// Pylon's RenderPNG embeds JetBrains Mono (no CJK glyphs), so any source
// containing 中文 / 한글 / 日本語 rasterizes to tofu on the PNG path. We
// route /, /now, /stock through SVG → PNG rasterization instead so the
// PNG faithfully mirrors the SVG view (CJK labels included). The
// rasterizer parses the narrow subset of SVG that pylon emits: <svg>
// with viewBox/width/height, <text x y textLength>...</text>, and
// <rect x y width height fill="..."/>. Anything else is ignored.
//
// A subsetted Sarasa Mono SC TTF is embedded for CJK + Latin coverage
// at ~6.6 MB. License: SIL Open Font License v1.1 (see
// assets/fonts/Sarasa-OFL-LICENSE.txt).

package render

import (
	"bytes"
	_ "embed"
	"encoding/xml"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

//go:embed assets/fonts/SarasaMonoSC-Subset.ttf
var cjkFontBytes []byte

// Pylon's SVG cell grid is 5×13 px per ASCII cell (CJK glyphs are
// laid out across two cells horizontally — pylon does this in its
// SVG layout pass). The PNG output upscales by pngScale so the
// rasterized text is crisp on retina-class displays and OG card
// thumbnails. Total PNG cell size = pngScale × svgCellW/H.
const (
	svgCellW  = 5
	svgCellH  = 13
	svgFontPx = 10
	pngScale  = 4
)

// fontMu guards the lazy-parsed sfnt.Font; opentype.NewFace itself is
// not safe across goroutines (it caches glyph rasters), so each call
// site builds its own face from the shared parsed font.
var (
	fontMu     sync.Mutex
	parsedFont *sfnt.Font
)

// loadFont parses the embedded Sarasa subset once and caches the
// result. Returns the same *sfnt.Font on every call; callers create a
// per-call font.Face from it.
func loadFont() (*sfnt.Font, error) {
	fontMu.Lock()
	defer fontMu.Unlock()
	if parsedFont != nil {
		return parsedFont, nil
	}
	ft, err := sfnt.Parse(cjkFontBytes)
	if err != nil {
		return nil, fmt.Errorf("parse embedded cjk font: %w", err)
	}
	parsedFont = ft
	return ft, nil
}

// RasterizeSVG converts a pylon-shaped SVG body into a PNG raster.
// The returned image dimensions are pngScale × the SVG's declared
// width/height in pixels. SVG features outside pylon's emit set are
// silently ignored — callers can rely on this for safety since the
// only known caller is render.renderAST piping pylon.RenderSVG output
// in. For non-pylon SVG sources, behavior is unspecified but never
// panics: missing attributes default to zero, unknown elements are
// skipped, malformed XML returns an error from the underlying
// xml.Decoder.
func RasterizeSVG(svgBody []byte) ([]byte, error) {
	doc, err := parseSVG(svgBody)
	if err != nil {
		return nil, err
	}
	if doc.Width <= 0 || doc.Height <= 0 {
		return nil, fmt.Errorf("svg: missing or non-positive width/height")
	}
	ft, err := loadFont()
	if err != nil {
		return nil, err
	}

	imgW := doc.Width * pngScale
	imgH := doc.Height * pngScale
	img := image.NewRGBA(image.Rect(0, 0, imgW, imgH))
	bg := parseHexColor(doc.Background, color.RGBA{R: 13, G: 17, B: 23, A: 255})
	draw.Draw(img, img.Bounds(), &image.Uniform{C: bg}, image.Point{}, draw.Src)

	face, err := opentype.NewFace(ft, &opentype.FaceOptions{
		Size:    float64(svgFontPx * pngScale),
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, fmt.Errorf("build font face: %w", err)
	}
	defer face.Close()

	for _, r := range doc.Rects {
		drawRect(img, r)
	}
	for _, t := range doc.Texts {
		drawText(img, face, t, doc.Foreground)
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}
	return buf.Bytes(), nil
}

// drawRect fills the SVG-coordinate rectangle into the (already
// scaled) image. Pylon emits rects only for inverse-video text cells
// (banner glyphs, signed-bar fills) so the fill color always reads
// as the text-foreground color of the surrounding theme.
func drawRect(img *image.RGBA, r svgRect) {
	x0 := r.X * pngScale
	y0 := r.Y * pngScale
	x1 := (r.X + r.Width) * pngScale
	y1 := (r.Y + r.Height) * pngScale
	c := parseHexColor(r.Fill, color.RGBA{R: 230, G: 223, B: 200, A: 255})
	draw.Draw(img, image.Rect(x0, y0, x1, y1), &image.Uniform{C: c}, image.Point{}, draw.Src)
}

// drawText rasterizes one pylon text run. Pylon's <text> carries
// textLength = displayWidth(content) × svgCellW so the natural Latin
// monospace advance lines up with cell boundaries. We honor that by
// scaling each glyph's advance to fit the declared textLength —
// when natural width matches (Latin-only runs), the scale is a
// no-op; on CJK-heavy runs where Sarasa's full-width glyphs already
// occupy 2× the cell, the scale corrects for any drift introduced
// by font-metric quirks.
func drawText(img *image.RGBA, face font.Face, t svgText, fallbackHex string) {
	c := parseHexColor(t.Fill, parseHexColor(fallbackHex, color.RGBA{R: 230, G: 223, B: 200, A: 255}))
	// Pylon's text y is the baseline in SVG units; scale to PNG pixels.
	baseline := t.Y * pngScale
	startX := t.X * pngScale

	natural := measureText(face, t.Content)
	target := t.TextLength * pngScale
	if target <= 0 {
		target = natural
	}

	// scale factor for advance widths so the run fits target px.
	var scale float64 = 1
	if natural > 0 && target > 0 {
		scale = float64(target) / float64(natural)
	}

	d := &font.Drawer{
		Dst:  img,
		Src:  &image.Uniform{C: c},
		Face: face,
	}
	x := fixed.I(startX)
	d.Dot.Y = fixed.I(baseline)
	for _, r := range t.Content {
		d.Dot.X = x
		d.DrawString(string(r))
		adv, _ := face.GlyphAdvance(r)
		// Apply the layout scale so total advance ≈ target px.
		x += fixed.Int26_6(float64(adv) * scale)
	}
}

// measureText returns the natural pixel width of s with face. Used
// to compute the textLength scale factor; falls back to len(s)*cellW
// on error so a degenerate face doesn't divide by zero.
func measureText(face font.Face, s string) int {
	var total fixed.Int26_6
	for _, r := range s {
		adv, ok := face.GlyphAdvance(r)
		if !ok {
			adv = fixed.I(svgCellW * pngScale)
		}
		total += adv
	}
	return total.Round()
}

// svgDoc is the trimmed-down view of pylon's SVG output we care
// about. parseSVG walks the XML stream and fills these slices.
type svgDoc struct {
	Width      int
	Height     int
	Background string // injected by PaintSVG (<rect> at index 0 with theme bg)
	Foreground string // text fill from the inline style; falls back per-element
	Texts      []svgText
	Rects      []svgRect
}

type svgText struct {
	X          int
	Y          int
	TextLength int
	Fill       string
	Content    string
}

type svgRect struct {
	X      int
	Y      int
	Width  int
	Height int
	Fill   string
}

// parseSVG is a SAX-style walker — pylon's output is small (a few KB
// even for a 30-row /stock panel), so building the full DOM via
// xml.Unmarshal would also work but the streaming form keeps memory
// flat and lets us pull out the viewBox attributes from a single
// pass.
func parseSVG(body []byte) (*svgDoc, error) {
	d := &svgDoc{
		Foreground: "#0f1c2d", // pylon light-theme default
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "svg":
			d.Width = pickIntAttr(se, "width")
			d.Height = pickIntAttr(se, "height")
		case "style":
			// Pull a `fill: #xxxxxx` from the embedded CSS so the
			// default text color matches whatever PaintSVG applied.
			var css string
			if err := dec.DecodeElement(&css, &se); err == nil {
				if c := extractCSSFill(css); c != "" {
					d.Foreground = c
				}
			}
		case "rect":
			r := svgRect{
				X:      pickIntAttr(se, "x"),
				Y:      pickIntAttr(se, "y"),
				Width:  pickIntAttr(se, "width"),
				Height: pickIntAttr(se, "height"),
				Fill:   pickAttr(se, "fill"),
			}
			// PaintSVG injects the canvas-background rect as the FIRST
			// <rect> spanning the entire viewBox. Capture its fill as
			// the background color and skip over-painting it again
			// later (RasterizeSVG draws bg from this value before any
			// other content).
			if d.Background == "" && r.X == 0 && r.Y == 0 && r.Width == d.Width && r.Height == d.Height {
				d.Background = r.Fill
				continue
			}
			d.Rects = append(d.Rects, r)
		case "text":
			t := svgText{
				X:          pickIntAttr(se, "x"),
				Y:          pickIntAttr(se, "y"),
				TextLength: pickIntAttr(se, "textLength"),
				Fill:       pickAttr(se, "fill"),
			}
			var content string
			if err := dec.DecodeElement(&content, &se); err == nil {
				t.Content = content
			}
			d.Texts = append(d.Texts, t)
		}
	}
	return d, nil
}

func pickAttr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func pickIntAttr(se xml.StartElement, name string) int {
	v := pickAttr(se, name)
	if v == "" {
		return 0
	}
	n, _ := strconv.Atoi(v)
	return n
}

// extractCSSFill pulls a `fill: #xxxxxx` declaration out of pylon's
// inline <style> block. Pylon emits exactly one rule (`text { ... }`)
// so a substring match is enough — no full CSS parse needed.
func extractCSSFill(css string) string {
	i := strings.Index(css, "fill:")
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(css[i+len("fill:"):])
	end := strings.IndexAny(rest, ";}\n")
	if end < 0 {
		end = len(rest)
	}
	return strings.TrimSpace(rest[:end])
}

// parseHexColor accepts `#rgb` or `#rrggbb`. Returns def on any parse
// failure or empty input.
func parseHexColor(s string, def color.RGBA) color.RGBA {
	s = strings.TrimSpace(s)
	if s == "" || s[0] != '#' {
		return def
	}
	hex := s[1:]
	switch len(hex) {
	case 3:
		// expand #rgb → #rrggbb
		var b strings.Builder
		for _, r := range hex {
			b.WriteRune(r)
			b.WriteRune(r)
		}
		hex = b.String()
	case 6:
		// already long form
	default:
		return def
	}
	r, err1 := strconv.ParseUint(hex[0:2], 16, 8)
	g, err2 := strconv.ParseUint(hex[2:4], 16, 8)
	bl, err3 := strconv.ParseUint(hex[4:6], 16, 8)
	if err1 != nil || err2 != nil || err3 != nil {
		return def
	}
	return color.RGBA{R: uint8(r), G: uint8(g), B: uint8(bl), A: 255}
}
