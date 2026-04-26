package render

import (
	"fmt"
	"strings"

	"github.com/cmj0121/pylon/src/go/pkg/pylon"
)

// Banner renders BannerSource(headline, subtitle) through pylon — the source
// is `[ headline | banner ]` over a borderless `( subtitle )` caption, so a
// single render call produces a banner frame stacked over a centered caption.
// Both render paths use pylon's native default theme — Unicode box-drawing
// frame plus ANSI Shadow block-letter banner glyphs — so a UTF-8-capable
// terminal and a browser see the same visual. ASCII appends a trailing
// newline; PNG does not. Input normalization (empty fallback, colon
// substitution) lives in BannerSource.
//
// PNG rendering can fail (font init, encode error). Callers should branch
// on err and fall back to ASCII to keep the response from 5xx-ing on a
// transient pylon problem.
func Banner(headline, subtitle string, mode Mode) ([]byte, error) {
	ast := pylon.Parse(BannerSource(headline, subtitle))
	if mode == ModePNG {
		return pylon.RenderPNG(ast)
	}
	return []byte(pylon.RenderASCII(ast) + "\n"), nil
}

// BannerSource returns the pylon source string Banner parses and renders.
// Callers that want to ship the raw source to clients (so the client can
// render it themselves) can use this directly without going through
// pylon.Parse / RenderASCII / RenderPNG.
//
// Two boundary normalizations apply:
//
//   - Empty or whitespace-only headline is replaced with the shared `?`
//     placeholder; pylon panics on bracketed whitespace-only labels.
//   - `:` in the headline is substituted with a space; pylon's banner font
//     has no `:` glyph, so without this the colon would fall back to a `?`
//     shape between the digit pairs. The eye reads the resulting gap as a
//     clock separator.
func BannerSource(headline, subtitle string) string {
	if strings.TrimSpace(headline) == "" {
		headline = emptyPlaceholder
	}
	headline = strings.ReplaceAll(headline, ":", " ")
	return fmt.Sprintf("[ %s | banner ]\n( %s )", headline, subtitle)
}
