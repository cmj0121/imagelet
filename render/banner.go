package render

import (
	"fmt"
	"strings"

	"github.com/cmj0121/pylon/src/go/pkg/pylon"
)

// Banner renders BannerSource(headline, subtitle) through pylon — the source
// is `[ headline | banner ]` over a borderless `( subtitle )` caption, so a
// single render call produces a banner frame stacked over a centered caption.
// All four render paths use pylon's native default theme — Unicode box-
// drawing frame plus ANSI Shadow block-letter banner glyphs — so a UTF-8-
// capable terminal, a browser, and an SVG-capable viewer see the same
// visual. ASCII appends a trailing newline; PNG, SVG, and HTML do not.
// Empty-headline normalization lives in BannerSource.
//
// SVG output is post-processed through PaintSVG so the rendered surface
// uses the GitHub-dark palette by default (background rect + ink swap).
// HTML mode wraps the painted SVG, so its inline figure inherits the same
// theme.
//
// PNG rendering can fail (font init, encode error). Callers should branch
// on err and fall back to ASCII to keep the response from 5xx-ing on a
// transient pylon problem. SVG and HTML rendering are pure string assembly
// and never error; the wrapper keeps the (body, err) shape for caller
// uniformity.
func Banner(headline, subtitle string, mode Mode) ([]byte, error) {
	ast := pylon.Parse(BannerSource(headline, subtitle))
	if mode == ModePNG {
		return pylon.RenderPNG(ast)
	}
	if mode == ModeSVG {
		return PaintSVG([]byte(pylon.RenderSVG(ast))), nil
	}
	if mode == ModeHTML {
		return WrapHTML(PaintSVG([]byte(pylon.RenderSVG(ast)))), nil
	}
	return []byte(pylon.RenderASCII(ast) + "\n"), nil
}

// BannerSource returns the pylon source string Banner parses and renders.
// Callers that want to ship the raw source to clients (so the client can
// render it themselves) can use this directly without going through
// pylon.Parse / RenderASCII / RenderPNG.
//
// Empty or whitespace-only headline is replaced with the shared `?`
// placeholder; pylon panics on bracketed whitespace-only labels.
func BannerSource(headline, subtitle string) string {
	if strings.TrimSpace(headline) == "" {
		headline = emptyPlaceholder
	}
	return fmt.Sprintf("[ %s | banner ]\n( %s )", headline, subtitle)
}

// BannerStack renders BannerStackSource through pylon -- a banner stacked
// over a multi-line bordered box. Used by /stock and /weather V1 layouts
// where multiple caption rows (header / data / bars / divider / TW-block)
// share a single outer box. Empty `lines` falls back to plain Banner with
// no subtitle (renders cleanly).
//
// Trailing newline appended for ASCII; PNG, SVG, and HTML keep raw bytes.
// SVG goes through PaintSVG; HTML wraps the painted SVG. Same error
// handling as Banner.
func BannerStack(headline string, lines []string, mode Mode) ([]byte, error) {
	ast := pylon.Parse(BannerStackSource(headline, lines))
	if mode == ModePNG {
		return pylon.RenderPNG(ast)
	}
	if mode == ModeSVG {
		return PaintSVG([]byte(pylon.RenderSVG(ast))), nil
	}
	if mode == ModeHTML {
		return WrapHTML(PaintSVG([]byte(pylon.RenderSVG(ast)))), nil
	}
	return []byte(pylon.RenderASCII(ast) + "\n"), nil
}

// BannerStackSource builds the pylon source `[ headline | banner ]\n[ line1\nline2\n... ]`.
// Empty `lines` collapses to a banner-only source. Empty headline falls
// back to the shared `?` placeholder (mirroring BannerSource).
func BannerStackSource(headline string, lines []string) string {
	if strings.TrimSpace(headline) == "" {
		headline = emptyPlaceholder
	}
	if len(lines) == 0 {
		return fmt.Sprintf("[ %s | banner ]", headline)
	}
	return fmt.Sprintf("[ %s | banner ]\n[ %s ]", headline, strings.Join(lines, "\n"))
}
