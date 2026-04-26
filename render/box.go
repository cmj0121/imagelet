// Package render provides pylon-backed renderers.
//
// imagelet wraps pylon (github.com/cmj0121/pylon/src/go/pkg/pylon) so individual
// services don't need to know pylon's source-string syntax or theme conventions.
// All current services share a single primitive: a labeled box around a string.
// Future renderers (banner, sparkline, ...) belong in this package alongside Box.
package render

import (
	"fmt"
	"strings"

	"github.com/cmj0121/pylon/src/go/pkg/pylon"
)

// Mode picks the wire format the renderer emits. ASCII is plain text; PNG
// is a pylon-rendered raster image (the browser default).
type Mode int

const (
	// ModeASCII renders to plain text using pylon's `ascii` theme (+ - | glyphs)
	// and appends a trailing newline so the response plays well with shells.
	ModeASCII Mode = iota
	// ModePNG renders to a PNG raster (binary). Pylon embeds JetBrains Mono
	// for glyph metrics, so the output is self-contained.
	ModePNG
)

const emptyPlaceholder = "?"

// String returns a stable lowercase label for the mode ("ascii" or "png").
// Implements fmt.Stringer so render.Mode interpolates cleanly in logs and
// test output. Unknown modes default to "ascii" — the safe rendering
// fallback used elsewhere in this package.
func (m Mode) String() string {
	switch m {
	case ModePNG:
		return "png"
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
	src := fmt.Sprintf("---\ntheme: ascii\n---\n[ %s ]", text)
	return []byte(pylon.RenderASCII(pylon.Parse(src)) + "\n"), nil
}
