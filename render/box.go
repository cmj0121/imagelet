// Package render provides pylon-backed renderers.
//
// imagelet wraps pylon (github.com/cmj0121/pylon/src/go/pkg/pylon) so individual
// services don't need to know pylon's source-string syntax or theme conventions.
// Today this package exposes:
//
//   - Box, Banner, BannerStack, BannerSource — pylon-source primitives shared
//     by /now, /stock, /weather, /404, and /
//   - Mode (ASCII / PNG / SVG) — wire-format selector resolved from the request
//     UA by middleware.ClientDetector, optionally overridden by ?format= via
//     middleware.ResolveMode
//   - ProgressBar, YearProgress, DayCycle — `█`/`░` text-bar fragments used
//     in subtitles and caption rows
//   - StripPylonSyntax, TrimHour — sanitization and small string helpers
//     services need at the render boundary
//
// New renderers (sparkline, gauge, ...) belong in this package alongside the
// existing primitives.
package render

import (
	"fmt"
	"strings"

	"github.com/cmj0121/pylon/src/go/pkg/pylon"
)

// Mode picks the wire format the renderer emits. ASCII is plain text; PNG
// is a pylon-rendered raster image; SVG is a self-contained pylon-rendered
// SVG document; HTML is a self-contained HTML5 document inlining the SVG
// (the browser default). Pylon source bypasses the renderer entirely and is
// negotiated by middleware.WantsPylonSource, not via this enum.
type Mode int

const (
	// ModeASCII renders to plain text using pylon's `ascii` theme (+ - | glyphs)
	// and appends a trailing newline so the response plays well with shells.
	ModeASCII Mode = iota
	// ModePNG renders to a PNG raster (binary). Pylon embeds JetBrains Mono
	// for glyph metrics, so the output is self-contained.
	ModePNG
	// ModeSVG renders to a self-contained SVG document. Pylon emits xmlns,
	// explicit width/height/viewBox, inline <style>, and uses textLength +
	// lengthAdjust="spacingAndGlyphs" so the cell grid stays aligned even
	// when the browser falls back from Cascadia/JetBrains to a system mono
	// (Menlo on iOS). XML-special chars in text content are escaped by pylon.
	ModeSVG
	// ModeHTML renders to a self-contained HTML5 document with the SVG
	// inlined in the body. Adds a viewport meta and a centering page style
	// so a top-level browser navigation reads cleanly on mobile. The inline
	// SVG itself is identical to what ModeSVG would emit, so URLs that have
	// historically been embedded as `<img src="…">` should switch to
	// `?format=png` (or `?format=svg`) to keep the raw-image contract.
	ModeHTML
)

const emptyPlaceholder = "?"

// String returns a stable lowercase label for the mode ("ascii", "png",
// "svg", "html"). Implements fmt.Stringer so render.Mode interpolates
// cleanly in logs and test output. Unknown modes default to "ascii" — the
// safe rendering fallback used elsewhere in this package.
func (m Mode) String() string {
	switch m {
	case ModePNG:
		return "png"
	case ModeSVG:
		return "svg"
	case ModeHTML:
		return "html"
	default:
		return "ascii"
	}
}

// Box renders text inside a single pylon-bordered box. It is a thin wrapper
// around pylon.Parse + pylon.RenderASCII / pylon.RenderPNG; callers stay
// decoupled from pylon's source syntax and theme selection.
//
// ASCII output uses pylon's `ascii` theme (set via frontmatter) so callers
// always get + - | glyphs, never Unicode box-drawing — important for clients
// that pipe the response into terminals or scripts that don't speak Unicode.
// PNG output uses pylon's native (Unicode + ANSI Shadow) theme — a raster
// looks better with full glyph richness than with the ASCII fallback font.
//
// Empty or whitespace-only input is replaced with a "?" placeholder. pylon's
// parser panics on `[  ]` (whitespace-only bracketed content) — its empty-
// input fallback only fires for a literally empty source, not a bracketed
// empty label. Box absorbs that edge case so callers can pass user-provided
// strings without defensive trimming of their own.
//
// PNG rendering can fail (font init, encode error). ASCII never errors but
// the signature stays consistent so callers can branch on err uniformly.
func Box(text string, mode Mode) ([]byte, error) {
	if strings.TrimSpace(text) == "" {
		text = emptyPlaceholder
	}
	if mode == ModePNG {
		// Native theme for PNG — Unicode frame, no `theme: ascii` frontmatter.
		return pylon.RenderPNG(pylon.Parse(fmt.Sprintf("[ %s ]", text)))
	}
	if mode == ModeSVG {
		// Native theme for SVG so the painted grid matches what a browser
		// shows for the PNG variant.
		return []byte(pylon.RenderSVG(pylon.Parse(fmt.Sprintf("[ %s ]", text)))), nil
	}
	if mode == ModeHTML {
		svg := pylon.RenderSVG(pylon.Parse(fmt.Sprintf("[ %s ]", text)))
		return WrapHTML([]byte(svg)), nil
	}
	src := fmt.Sprintf("---\ntheme: ascii\n---\n[ %s ]", text)
	return []byte(pylon.RenderASCII(pylon.Parse(src)) + "\n"), nil
}
