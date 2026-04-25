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

// Mode picks the wire format Box emits. The choice is independent of the input —
// any text can be rendered as either ASCII or SVG.
type Mode int

const (
	// ModeASCII renders to plain text using pylon's `ascii` theme (+ - | glyphs)
	// and appends a trailing newline so the response plays well with shells.
	ModeASCII Mode = iota
	// ModeSVG renders to a self-contained <svg> document with no trailing
	// newline.
	ModeSVG
)

const emptyPlaceholder = "?"

// String returns a stable lowercase label for the mode ("ascii" or "svg").
// Implements fmt.Stringer so render.Mode interpolates cleanly in logs and
// test output. Unknown modes default to "ascii" — the safe rendering
// fallback used elsewhere in this package.
func (m Mode) String() string {
	switch m {
	case ModeSVG:
		return "svg"
	default:
		return "ascii"
	}
}

// Box renders text inside a single pylon-bordered box. It is a thin wrapper
// around pylon.Parse + pylon.RenderASCII / pylon.RenderSVG; callers stay
// decoupled from pylon's source syntax and theme selection.
//
// ASCII output uses pylon's `ascii` theme (set via frontmatter) so callers
// always get + - | glyphs, never Unicode box-drawing — important for clients
// that pipe the response into terminals or scripts that don't speak Unicode.
//
// Empty or whitespace-only input is replaced with a "?" placeholder. pylon's
// parser panics on `[  ]` (whitespace-only bracketed content) — its empty-
// input fallback only fires for a literally empty source, not a bracketed
// empty label. Box absorbs that edge case so callers can pass user-provided
// strings without defensive trimming of their own.
func Box(text string, mode Mode) string {
	if strings.TrimSpace(text) == "" {
		text = emptyPlaceholder
	}
	switch mode {
	case ModeSVG:
		src := fmt.Sprintf("[ %s ]", text)
		return pylon.RenderSVG(pylon.Parse(src))
	default:
		src := fmt.Sprintf("---\ntheme: ascii\n---\n[ %s ]", text)
		return pylon.RenderASCII(pylon.Parse(src)) + "\n"
	}
}
