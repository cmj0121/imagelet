package render_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cmj0121/pylon/src/go/pkg/pylon"

	"github.com/cmj0121/imagelet/render"
)

// TestPaintSVGInjectsBackgroundRect pins that PaintSVG inserts the
// full-canvas background rect immediately after the opening <svg ...>
// tag. Position matters: a rect placed AFTER siblings would be drawn on
// top of them and hide the figure.
func TestPaintSVGInjectsBackgroundRect(t *testing.T) {
	in := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 50"><text>x</text></svg>`
	out := string(render.PaintSVG([]byte(in)))

	if !strings.Contains(out, `<rect width="100%" height="100%" fill="#0d1117"/>`) {
		t.Errorf("PaintSVG missing background rect:\n%s", out)
	}

	// The rect must appear BEFORE the existing <text> child, otherwise
	// it would paint on top of the figure.
	rectIdx := strings.Index(out, "<rect ")
	textIdx := strings.Index(out, "<text>")
	if rectIdx < 0 || textIdx < 0 {
		t.Fatalf("expected both <rect> and <text> in output:\n%s", out)
	}
	if rectIdx > textIdx {
		t.Errorf("background <rect> at %d appears AFTER <text> at %d (would paint over figure):\n%s", rectIdx, textIdx, out)
	}
}

// TestPaintSVGSwapsPylonInk pins the color swap. Pylon emits #0f1c2d as
// its default text fill; PaintSVG flips it to #c9d1d9 (GitHub-dark ink)
// so text reads on the dark background.
func TestPaintSVGSwapsPylonInk(t *testing.T) {
	in := `<svg viewBox="0 0 50 20"><style>text { fill: #0f1c2d; }</style><text fill="#0f1c2d">x</text></svg>`
	out := string(render.PaintSVG([]byte(in)))

	if strings.Contains(out, "#0f1c2d") {
		t.Errorf("PaintSVG left pylon ink #0f1c2d in output:\n%s", out)
	}
	if c := strings.Count(out, "#c9d1d9"); c != 2 {
		t.Errorf("PaintSVG produced %d occurrences of #c9d1d9, want 2 (style + text):\n%s", c, out)
	}
}

// TestPaintSVGIdempotent pins that running PaintSVG twice gives the same
// result as running it once. Services that pre-render at init AND callers
// that re-render per request must be safe to chain through PaintSVG
// multiple times.
func TestPaintSVGIdempotent(t *testing.T) {
	in := `<svg viewBox="0 0 50 20"><text fill="#0f1c2d">x</text></svg>`
	once := render.PaintSVG([]byte(in))
	twice := render.PaintSVG(once)

	if !bytes.Equal(once, twice) {
		t.Errorf("PaintSVG not idempotent:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
	// Background rect must appear exactly once even after two passes.
	if c := bytes.Count(twice, []byte(`fill="#0d1117"`)); c != 1 {
		t.Errorf("idempotent run produced %d background rects, want 1:\n%s", c, twice)
	}
}

// TestPaintSVGEmptyInput pins that PaintSVG returns empty input
// unchanged — no panic, no spurious bytes. Defensive-only; in practice
// callers wouldn't paint an empty SVG.
func TestPaintSVGEmptyInput(t *testing.T) {
	if got := render.PaintSVG(nil); len(got) != 0 {
		t.Errorf("PaintSVG(nil) = %q, want empty", got)
	}
	if got := render.PaintSVG([]byte{}); len(got) != 0 {
		t.Errorf("PaintSVG([]byte{}) = %q, want empty", got)
	}
}

// TestPaintSVGMalformedInput pins that input lacking an opening `>` is
// returned unchanged (apart from the color swap). PaintSVG is a post-
// processor, not a validator — a malformed SVG would already 5xx at the
// caller's response boundary, and silently inventing one isn't its job.
func TestPaintSVGMalformedInput(t *testing.T) {
	// No tags at all.
	const garbage = "not an svg"
	if got := string(render.PaintSVG([]byte(garbage))); got != garbage {
		t.Errorf("PaintSVG(%q) = %q, want unchanged", garbage, got)
	}
}

// TestPylonInkSentinel pins the assumption PaintSVG depends on: pylon's
// default-theme SVG output contains `#0f1c2d`. If pylon ever changes its
// ink, this fails BEFORE production renders un-themed text — the swap is
// silent on a missed match, so we trip-wire it here instead.
func TestPylonInkSentinel(t *testing.T) {
	raw := pylon.RenderSVG(pylon.Parse("[ probe ]"))
	if !strings.Contains(raw, "#0f1c2d") {
		t.Fatalf("pylon SVG no longer contains the expected #0f1c2d ink — PaintSVG's color swap will silently miss. Update pylonLightInk in render/svg_theme.go.\n--- pylon output ---\n%s", raw)
	}
}

// TestPaintSVGOnRealPylonOutput exercises the full pipeline: render a
// pylon SVG, paint it, assert the post-paint document carries both the
// background rect and the gray ink with no traces of pylon's light ink.
func TestPaintSVGOnRealPylonOutput(t *testing.T) {
	raw := pylon.RenderSVG(pylon.Parse("[ probe ]"))
	out := string(render.PaintSVG([]byte(raw)))

	if !strings.Contains(out, `fill="#0d1117"`) {
		t.Errorf("painted SVG missing GitHub-dark background fill")
	}
	if !strings.Contains(out, "#c9d1d9") {
		t.Errorf("painted SVG missing GitHub-dark foreground ink")
	}
	if strings.Contains(out, "#0f1c2d") {
		t.Errorf("painted SVG still contains pylon's default ink #0f1c2d:\n%s", out)
	}
}
