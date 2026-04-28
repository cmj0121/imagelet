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
	return renderAST(pylon.Parse(BannerSource(headline, subtitle)), mode)
}

// renderAST dispatches a parsed pylon AST to the requested render mode,
// applying the GitHub-dark PaintSVG post-process to SVG/HTML output and
// the trailing newline to ASCII. Shared by the Banner / BannerMulti /
// BannerBoxes wrappers — every banner-shaped source uses pylon's native
// theme so the per-mode dispatch is identical.
func renderAST(ast pylon.AST, mode Mode) ([]byte, error) {
	switch mode {
	case ModePNG:
		return pylon.RenderPNG(ast)
	case ModeSVG:
		return PaintSVG([]byte(pylon.RenderSVG(ast))), nil
	case ModeHTML:
		return WrapHTML(PaintSVG([]byte(pylon.RenderSVG(ast)))), nil
	default:
		return []byte(pylon.RenderASCII(ast) + "\n"), nil
	}
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

// BannerMulti renders BannerSourceMulti through pylon -- a banner stacked
// over N borderless caption rows. Used by /now's multi-row metadata
// layout. ASCII appends a trailing newline; PNG, SVG, and HTML keep raw
// bytes. SVG goes through PaintSVG; HTML wraps the painted SVG. Same
// error handling as Banner.
func BannerMulti(headline string, lines []string, mode Mode) ([]byte, error) {
	return renderAST(pylon.Parse(BannerSourceMulti(headline, lines)), mode)
}

// BannerSourceMulti returns the pylon source for a banner stacked over N
// borderless caption rows: `[ headline | banner ]\n( line1 )\n( line2 )\n...`.
// Used by /now's three-row metadata layout where each row is a distinct
// piece of metadata (date, weekday strip, year-progress).
//
// Empty `lines` collapses to banner-only source (`[ headline | banner ]`).
// Empty headline falls back to the shared `?` placeholder, mirroring
// BannerSource.
//
// Caller is responsible for keeping caption content pylon-safe — literal
// `[`, `]`, `(`, `)`, and `|` will be parsed as syntax. /now uses angle
// brackets in WeekStrip for this reason.
func BannerSourceMulti(headline string, lines []string) string {
	if strings.TrimSpace(headline) == "" {
		headline = emptyPlaceholder
	}
	if len(lines) == 0 {
		return fmt.Sprintf("[ %s | banner ]", headline)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[ %s | banner ]", headline)
	for _, line := range lines {
		b.WriteString("\n( ")
		b.WriteString(line)
		b.WriteString(" )")
	}
	return b.String()
}

// AlignLeft requests left alignment on a BoxBlock. Empty Align (the
// zero value) keeps pylon's default center alignment. Pylon's grammar
// uses a trailing `-` inside the parentheses to flag left alignment;
// the constant lets callers reach for a name instead of the marker.
const AlignLeft = "left"

// BoxBlock is a borderless multi-row section: a list of body rows that
// renders as a single multi-line pylon box. An empty Rows slice (or
// one containing only blank rows) marks the block as absent so callers
// can pass a zero-value BoxBlock to skip it. Align controls the
// section-wide alignment (currently AlignLeft is supported; empty
// defaults to pylon's center). /stock packs all of its TW data rows
// into a single BoxBlock with ZWSP-only separator rows for visual
// grouping and hands it to BannerSourceBoxes / BannerBoxes.
type BoxBlock struct {
	Rows  []string
	Align string
}

func (b BoxBlock) present() bool {
	for _, r := range b.Rows {
		if strings.TrimSpace(r) != "" {
			return true
		}
	}
	return false
}

// BannerSourceBoxes extends BannerSourceMulti with N stacked borderless
// sections after the borderless captions:
//
//	[ headline | banner ]
//	( cap1 )
//	( capN )
//	( boxes[0].Rows[0]\n... )
//	( boxes[1].Rows[0]\n... )
//	...
//
// When bannerTitle is non-empty, the banner box is widened into a
// bordered multi-row container that holds the title row above the
// banner-rendered headline:
//
//	[
//	<bannerTitle>
//	( headline | banner )
//	]
//	( cap1 )
//	...
//
// The borderless `( ... | banner )` inside the outer `[ ]` keeps the
// banner glyphs frame-free so only the outer frame shows — pylon
// v0.5 renders this layout cleanly with CJK content where a fully
// nested `[ X | banner ]` inside `[ ]` would emit a double frame.
// Empty bannerTitle falls back to the single-line `[ headline |
// banner ]` source so /now and other callers keep their look.
//
// Empty (zero-value) sections are skipped; an all-absent slice
// collapses to the banner+captions output. Sections stack with no
// visible separator line — callers that want a visual gap between
// groups can fold them into one BoxBlock with a blank / ZWSP-only
// row at each group boundary; the AlignLeft trailing-dash trick
// keeps every row in the merged block aligned to the same column.
// Caller is responsible for keeping content pylon-safe — literal
// `[`, `]`, `(`, `)`, and `|` are parsed as syntax. /stock pre-
// strips caller-supplied content via render.StripPylonSyntax.
//
// Bordered `[...]` sections and the `[A] <-> [B]` side-by-side Row
// were earlier designs but pylon v0.5 mis-renders both around CJK
// content in SVG mode; borderless multi-row sections are the safe
// fallback until pylon ships fixes.
func BannerSourceBoxes(headline, bannerTitle string, captions []string, boxes []BoxBlock) string {
	var b strings.Builder
	if strings.TrimSpace(bannerTitle) == "" {
		b.WriteString(BannerSourceMulti(headline, captions))
	} else {
		writeTitledBannerBox(&b, headline, bannerTitle)
		for _, line := range captions {
			b.WriteString("\n( ")
			b.WriteString(line)
			b.WriteString(" )")
		}
	}
	for _, box := range boxes {
		if !box.present() {
			continue
		}
		b.WriteByte('\n')
		writeBoxInline(&b, box)
	}
	return b.String()
}

// writeTitledBannerBox emits a bordered box containing the bannerTitle
// row above a borderless `( <headline> | banner )` directive. The
// borderless inner box keeps the banner glyphs frame-free so only the
// outer frame shows. Empty headline normalizes to the shared `?`
// placeholder, mirroring BannerSource.
func writeTitledBannerBox(b *strings.Builder, headline, bannerTitle string) {
	if strings.TrimSpace(headline) == "" {
		headline = emptyPlaceholder
	}
	b.WriteString("[\n")
	b.WriteString(bannerTitle)
	b.WriteString("\n( ")
	b.WriteString(headline)
	b.WriteString(" | banner )\n]")
}

// writeBoxInline emits `( Row1\nRow2\n... )` to b — a borderless
// pylon box. AlignLeft is signalled via a trailing `-` inside the
// parentheses (`( ... -)`) which pylon's `\s+-$` parser regex picks
// up; that shifts every row in the section flush-left so labels and
// bars across rows line up vertically.
func writeBoxInline(b *strings.Builder, blk BoxBlock) {
	b.WriteString("( ")
	for i, row := range blk.Rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(row)
	}
	if blk.Align == AlignLeft {
		b.WriteString(" -)")
	} else {
		b.WriteString(" )")
	}
}

// BannerBoxes renders BannerSourceBoxes through pylon. Same per-mode
// behavior as BannerMulti (PaintSVG on SVG/HTML, ASCII trailing newline).
// bannerTitle is the optional row that sits inside the banner box above
// the headline; pass "" for the legacy single-line banner source.
func BannerBoxes(headline, bannerTitle string, captions []string, boxes []BoxBlock, mode Mode) ([]byte, error) {
	return renderAST(pylon.Parse(BannerSourceBoxes(headline, bannerTitle, captions, boxes)), mode)
}
