package render

import "bytes"

// SVG palette constants. GitHub-dark, picked for a tech-leaning rendered
// surface. The default theme is applied via PaintSVG to every SVG body
// imagelet emits (raw `?format=svg` responses and the inline SVG inside
// HTML mode). PNG and ASCII paths are unaffected.
const (
	// SVGBackground is the SVG canvas color painted by PaintSVG. Matches
	// GitHub's `--color-canvas-default` in dark mode.
	SVGBackground = "#0d1117"
	// SVGForeground replaces pylon's default ink in painted output. Matches
	// GitHub's `--color-fg-default` in dark mode.
	SVGForeground = "#c9d1d9"
	// pylonLightInk is the text fill pylon emits under its default
	// (light) theme — the value PaintSVG looks for and swaps. If pylon
	// upstream ever changes this, the swap silently misses; the
	// TestPylonInkSentinel test in svg_theme_test.go pins the value so a
	// pylon update fails the suite loudly rather than rendering un-themed
	// SVG in production.
	pylonLightInk = "#0f1c2d"
)

// svgBackgroundRect is the full-canvas rect that paints the SVG's own
// background. Using `width/height="100%"` instead of literal viewBox
// dimensions means PaintSVG doesn't need to parse the surrounding
// viewBox to size the rect — same bytes work for any SVG.
var svgBackgroundRect = []byte(`<rect width="100%" height="100%" fill="` + SVGBackground + `"/>`)

// PaintSVG returns svgBody with the GitHub-dark palette applied:
//
//  1. A full-canvas rect (fill SVGBackground) is injected immediately
//     after the opening <svg …> tag so the SVG paints its own background
//     instead of depending on whatever surface it lands on (browser
//     default white, HTML body color, terminal preview).
//  2. Pylon's default light-theme ink (pylonLightInk) is replaced with
//     SVGForeground wherever it appears — `<style>` blocks, per-text
//     fill attributes, and any embedded inner-SVG payload.
//
// Idempotent: a second call detects the existing background rect via a
// bytes-contains check on the marker and skips the inject step. The
// color swap is naturally idempotent (the target color is not in pylon
// output). Safe to run on already-painted bytes.
//
// Empty input or input lacking an opening `>` is returned unchanged —
// PaintSVG is a post-processor, not a validator. Malformed SVG would
// fail at the caller's response boundary, not here.
func PaintSVG(svg []byte) []byte {
	if len(svg) == 0 {
		return svg
	}
	out := bytes.ReplaceAll(svg, []byte(pylonLightInk), []byte(SVGForeground))
	if bytes.Contains(out, svgBackgroundRect) {
		return out
	}
	i := bytes.IndexByte(out, '>')
	if i < 0 {
		return out
	}
	rebuilt := make([]byte, 0, len(out)+len(svgBackgroundRect))
	rebuilt = append(rebuilt, out[:i+1]...)
	rebuilt = append(rebuilt, svgBackgroundRect...)
	rebuilt = append(rebuilt, out[i+1:]...)
	return rebuilt
}
