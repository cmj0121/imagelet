package render_test

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/cmj0121/imagelet/render"
)

// TestCoalesceSVGText_SoloRowsUnchanged pins byte-identity for SVG
// where every y has exactly one <text>. Solo rows don't trigger the
// Chromium copy bug, so the function must not perturb them.
func TestCoalesceSVGText_SoloRowsUnchanged(t *testing.T) {
	in := []byte(`<svg viewBox="0 0 50 50">` +
		`<text x="0" y="10" textLength="20" lengthAdjust="spacingAndGlyphs" xml:space="preserve">aa</text>` +
		`<text x="0" y="23" textLength="20" lengthAdjust="spacingAndGlyphs" xml:space="preserve">bb</text>` +
		`</svg>`)
	out := render.CoalesceSVGText(in)
	if !bytes.Equal(in, out) {
		t.Errorf("solo rows perturbed:\n--- in  ---\n%s\n--- out ---\n%s", in, out)
	}
}

// TestCoalesceSVGText_MultiSegmentCoalesced is the core fix — three
// fragmented same-y <text> nodes collapse to one <text> with three
// <tspan> children, all positional attributes preserved per-tspan.
func TestCoalesceSVGText_MultiSegmentCoalesced(t *testing.T) {
	in := []byte(`<svg>` +
		`<text x="0" y="10" textLength="5" lengthAdjust="spacingAndGlyphs" xml:space="preserve">A</text>` +
		`<text x="5" y="10" textLength="10" lengthAdjust="spacingAndGlyphs" xml:space="preserve">BB</text>` +
		`<text x="15" y="10" textLength="15" lengthAdjust="spacingAndGlyphs" xml:space="preserve">CCC</text>` +
		`</svg>`)
	out := string(render.CoalesceSVGText(in))

	if c := strings.Count(out, "<text "); c != 1 {
		t.Errorf("want exactly 1 <text> after coalesce, got %d:\n%s", c, out)
	}
	if c := strings.Count(out, "<tspan "); c != 3 {
		t.Errorf("want 3 <tspan> children, got %d:\n%s", c, out)
	}
	for _, want := range []string{
		`<text y="10" xml:space="preserve">`,
		`<tspan x="0" textLength="5" lengthAdjust="spacingAndGlyphs">A</tspan>`,
		`<tspan x="5" textLength="10" lengthAdjust="spacingAndGlyphs">BB</tspan>`,
		`<tspan x="15" textLength="15" lengthAdjust="spacingAndGlyphs">CCC</tspan>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestCoalesceSVGText_RectsPreserved pins that <rect> siblings
// interleaved between same-y <text> are left in place verbatim.
// Only text nodes are merged; rects keep both their count and their
// document order.
func TestCoalesceSVGText_RectsPreserved(t *testing.T) {
	in := []byte(`<svg>` +
		`<text x="0" y="10" textLength="5" lengthAdjust="spacingAndGlyphs" xml:space="preserve">A</text>` +
		`<rect x="5" y="0" width="10" height="13" fill="#c9d1d9"/>` +
		`<text x="15" y="10" textLength="5" lengthAdjust="spacingAndGlyphs" xml:space="preserve">B</text>` +
		`<rect x="20" y="0" width="10" height="13" fill="#c9d1d9"/>` +
		`<text x="30" y="10" textLength="5" lengthAdjust="spacingAndGlyphs" xml:space="preserve">C</text>` +
		`</svg>`)
	out := string(render.CoalesceSVGText(in))

	if c := strings.Count(out, "<rect "); c != 2 {
		t.Errorf("want 2 <rect> after coalesce, got %d:\n%s", c, out)
	}
	for _, want := range []string{
		`<rect x="5" y="0" width="10" height="13" fill="#c9d1d9"/>`,
		`<rect x="20" y="0" width="10" height="13" fill="#c9d1d9"/>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rect lost: missing %q:\n%s", want, out)
		}
	}
	// Coalesced text must appear once, both rects must remain.
	if c := strings.Count(out, "<text "); c != 1 {
		t.Errorf("want 1 <text> after coalesce, got %d:\n%s", c, out)
	}
}

// TestCoalesceSVGText_Idempotent — a second pass over already-
// coalesced bytes returns the same bytes. Pylon-shape regex misses
// <tspan>-bearing <text> nodes, so the function is a no-op on its
// own output.
func TestCoalesceSVGText_Idempotent(t *testing.T) {
	in := []byte(`<svg>` +
		`<text x="0" y="10" textLength="5" lengthAdjust="spacingAndGlyphs" xml:space="preserve">A</text>` +
		`<text x="5" y="10" textLength="5" lengthAdjust="spacingAndGlyphs" xml:space="preserve">B</text>` +
		`</svg>`)
	once := render.CoalesceSVGText(in)
	twice := render.CoalesceSVGText(once)
	if !bytes.Equal(once, twice) {
		t.Errorf("CoalesceSVGText not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

// TestCoalesceSVGText_EmptyAndNoText covers the early-out paths:
// nil/empty input round-trips; SVG without <text> nodes round-trips
// byte-identical.
func TestCoalesceSVGText_EmptyAndNoText(t *testing.T) {
	if got := render.CoalesceSVGText(nil); len(got) != 0 {
		t.Errorf("CoalesceSVGText(nil) = %q, want empty", got)
	}
	if got := render.CoalesceSVGText([]byte{}); len(got) != 0 {
		t.Errorf("CoalesceSVGText([]byte{}) = %q, want empty", got)
	}
	in := []byte(`<svg><rect x="0" y="0" width="10" height="10" fill="#000"/></svg>`)
	out := render.CoalesceSVGText(in)
	if !bytes.Equal(in, out) {
		t.Errorf("no-text input perturbed:\n--- in  ---\n%s\n--- out ---\n%s", in, out)
	}
}

// TestCoalesceSVGText_MultipleRowGroups pins that two distinct y
// values, each fragmented, coalesce independently into two <text>
// blocks with their own tspan children.
func TestCoalesceSVGText_MultipleRowGroups(t *testing.T) {
	in := []byte(`<svg>` +
		`<text x="0" y="10" textLength="5" lengthAdjust="spacingAndGlyphs" xml:space="preserve">A</text>` +
		`<text x="5" y="10" textLength="5" lengthAdjust="spacingAndGlyphs" xml:space="preserve">B</text>` +
		`<text x="0" y="23" textLength="5" lengthAdjust="spacingAndGlyphs" xml:space="preserve">C</text>` +
		`<text x="5" y="23" textLength="5" lengthAdjust="spacingAndGlyphs" xml:space="preserve">D</text>` +
		`</svg>`)
	out := string(render.CoalesceSVGText(in))

	if c := strings.Count(out, "<text "); c != 2 {
		t.Errorf("want 2 <text> blocks, got %d:\n%s", c, out)
	}
	if c := strings.Count(out, "<tspan "); c != 4 {
		t.Errorf("want 4 <tspan> children total, got %d:\n%s", c, out)
	}
	for _, want := range []string{
		`<text y="10" xml:space="preserve">`,
		`<text y="23" xml:space="preserve">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing coalesced row %q:\n%s", want, out)
		}
	}
}

// TestCoalesceSVGText_LiveStockSnapshot smoke-tests the transform
// against a real /stock TW capture if available. Pinning exact
// counts would couple the test to upstream pylon output; the
// inequality (fewer <text>, gained <tspan>) is the stable signal.
func TestCoalesceSVGText_LiveStockSnapshot(t *testing.T) {
	body, err := os.ReadFile("/tmp/stock.svg")
	if err != nil {
		t.Skipf("no /tmp/stock.svg snapshot available: %v", err)
	}
	if bytes.Count(body, []byte("<tspan")) != 0 {
		t.Skipf("snapshot already contains <tspan>; expected pylon-shape input")
	}
	textBefore := bytes.Count(body, []byte("<text "))
	out := render.CoalesceSVGText(body)
	textAfter := bytes.Count(out, []byte("<text "))
	tspanAfter := bytes.Count(out, []byte("<tspan "))

	if textAfter >= textBefore {
		t.Errorf("expected <text> count to drop, got before=%d after=%d", textBefore, textAfter)
	}
	if tspanAfter == 0 {
		t.Errorf("expected <tspan> to appear after coalesce, got 0")
	}
}

// TestBannerCoalesceNoSharedY — end-to-end via Banner: after the
// renderAST pipeline the SVG output must not have two <text>
// elements sharing the same y. Asserts the wiring in renderAST
// actually runs CoalesceSVGText on Banner's output.
func TestBannerCoalesceNoSharedY(t *testing.T) {
	out, err := render.Banner("Hello", "subtitle", render.ModeSVG)
	if err != nil {
		t.Fatalf("Banner: %v", err)
	}
	yRe := regexp.MustCompile(`<text [^>]*\by="([0-9.]+)"`)
	matches := yRe.FindAllSubmatch(out, -1)
	seen := map[string]int{}
	for _, m := range matches {
		seen[string(m[1])]++
	}
	for y, n := range seen {
		if n > 1 {
			t.Errorf("y=%s appears in %d <text> elements; coalesce did not merge:\n%s", y, n, out)
		}
	}
}
