package render

import (
	"regexp"
	"strings"
)

// pylonBracketRe matches a complete pair of round or square brackets and
// their contents. Pylon parses `(...)` as a borderless box and `[...]` as
// a framed box; an unsanitized caption containing either pair would smuggle
// a nested element into the source and break the rendered output. Unmatched
// brackets are deliberately left alone (we only strip *complete* pairs).
var pylonBracketRe = regexp.MustCompile(`\(([^()]*)\)|\[([^\[\]]*)\]`)

// pylonAmpRefRe matches `&` immediately followed by a Ref-trigger char
// (`[A-Za-z_]`, mirroring pylon's parser.go refRe). Without intervention
// pylon parses `&P` as an inline Ref node and fragments the row -- "S&P
// 500" renders as 3 stacked rows. The fix is a regex-driven space insert
// (" & "): pylon's regex stops matching, the `&` glyph itself stays
// visible, and every render path (terminal + JetBrains Mono PNG +
// text/pylon source) shows the literal "S & P 500".
//
// Invisible substitutes were tried first and rejected: U+FF06 fullwidth
// `＆` and U+200C ZWNJ are both absent from JetBrains Mono and render as
// `?` tofu in the PNG path. A regular space is the only universally-
// rendered separator.
var pylonAmpRefRe = regexp.MustCompile(`&([A-Za-z_])`)

// StripPylonSyntax removes complete `(...)` and `[...]` pairs (and their
// contents) from s, neutralizes any `&[A-Za-z_]` that would otherwise
// parse as a pylon Ref node by spacing the `&` away from the following
// letter, and collapses the resulting whitespace. The literal `&` glyph
// survives, so "S&P 500" renders as "S & P 500" — brand-recognizable on
// every surface (terminal + JetBrains Mono PNG + text/pylon source).
//
// Standalone `&` ("A & B", "Tom & Jerry") and `&` before non-letters
// pass through unchanged. strings.Fields collapses any doubled spaces
// the substitution produces.
//
// The `^` in `^GSPC` etc. is pylon-safe and left alone; the `·` prefix
// separator is also preserved.
func StripPylonSyntax(s string) string {
	cleaned := pylonBracketRe.ReplaceAllString(s, "")
	cleaned = pylonAmpRefRe.ReplaceAllString(cleaned, " & $1")
	return strings.Join(strings.Fields(cleaned), " ")
}
