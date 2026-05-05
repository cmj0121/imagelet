package render

import (
	"bytes"
	"regexp"
)

// pylonTextNode matches one of pylon's bare <text> nodes — the exact
// five-attribute order pylon emits (x, y, textLength, lengthAdjust,
// xml:space) followed by a chardata-only body. Already-coalesced
// <text>s carry <tspan> children and contain `<` in their body, so
// the `[^<]*` content class makes those non-matching automatically —
// that's how CoalesceSVGText stays idempotent without a separate
// guard.
var pylonTextNode = regexp.MustCompile(
	`<text x="([0-9.]+)" y="([0-9.]+)" textLength="([0-9.]+)" lengthAdjust="([^"]+)" xml:space="([^"]+)">([^<]*)</text>`,
)

// CoalesceSVGText returns svgBody with same-row <text> siblings merged
// into one <text> per row whose segments live inside <tspan> children.
//
// Chromium's clipboard serializer inserts a newline between sibling
// <text> nodes when the user copies an SVG selection; <tspan>
// siblings inside one <text> serialize as a single text run.
// Same-y <text> siblings appear when pylon splits a row across
// <rect> block-glyph runs (banner headlines), so without coalescing
// a single visual row pastes as N stacked lines.
//
// Idempotent: SVG that already has <tspan>-coalesced text (or no
// fragmented rows) is returned unchanged. Solo rows (one <text> per
// y) stay untouched, preserving byte-identity for SVG outputs that
// don't trigger the bug.
//
// Empty input or input without a matching <text> node is returned
// as-is — CoalesceSVGText is a post-processor, not a validator.
func CoalesceSVGText(svgBody []byte) []byte {
	if len(svgBody) == 0 {
		return svgBody
	}
	matches := pylonTextNode.FindAllSubmatchIndex(svgBody, -1)
	if len(matches) == 0 {
		return svgBody
	}

	// Group match indices by y attribute value, preserving document
	// order so the splice below walks left-to-right in one pass.
	type group struct {
		y    string
		idxs []int
	}
	order := []*group{}
	byY := map[string]*group{}
	for i, m := range matches {
		y := string(svgBody[m[4]:m[5]])
		g, ok := byY[y]
		if !ok {
			g = &group{y: y}
			byY[y] = g
			order = append(order, g)
		}
		g.idxs = append(g.idxs, i)
	}

	// Build a per-match replacement plan: groups of size 1 stay
	// untouched, groups of size >=2 collapse to one coalesced
	// <text> at the first index plus empty replacements at the
	// rest. Skipping size-1 groups is what keeps non-fragmented
	// rows byte-identical.
	type repl struct {
		start, end int
		body       []byte
	}
	plan := make([]repl, 0, len(matches))
	for _, g := range order {
		if len(g.idxs) < 2 {
			continue
		}
		first := matches[g.idxs[0]]
		xs := svgBody[first[10]:first[11]]
		var b bytes.Buffer
		b.WriteString(`<text y="`)
		b.WriteString(g.y)
		b.WriteString(`" xml:space="`)
		b.Write(xs)
		b.WriteString(`">`)
		for _, mi := range g.idxs {
			m := matches[mi]
			b.WriteString(`<tspan x="`)
			b.Write(svgBody[m[2]:m[3]])
			b.WriteString(`" textLength="`)
			b.Write(svgBody[m[6]:m[7]])
			b.WriteString(`" lengthAdjust="`)
			b.Write(svgBody[m[8]:m[9]])
			b.WriteString(`">`)
			b.Write(svgBody[m[12]:m[13]])
			b.WriteString(`</tspan>`)
		}
		b.WriteString(`</text>`)
		plan = append(plan, repl{start: first[0], end: first[1], body: b.Bytes()})
		for _, mi := range g.idxs[1:] {
			m := matches[mi]
			plan = append(plan, repl{start: m[0], end: m[1]})
		}
	}
	if len(plan) == 0 {
		return svgBody
	}

	// Splice in document order. The plan is built per-group so
	// sort by start offset before walking.
	for i := 1; i < len(plan); i++ {
		j := i
		for j > 0 && plan[j-1].start > plan[j].start {
			plan[j-1], plan[j] = plan[j], plan[j-1]
			j--
		}
	}
	out := make([]byte, 0, len(svgBody))
	cursor := 0
	for _, r := range plan {
		out = append(out, svgBody[cursor:r.start]...)
		out = append(out, r.body...)
		cursor = r.end
	}
	out = append(out, svgBody[cursor:]...)
	return out
}
