package render_test

import (
	"strings"
	"testing"

	"github.com/cmj0121/imagelet/render"
)

func TestBannerASCII(t *testing.T) {
	got := render.Banner("13:45", "2026-04-25 SAT UTC+8", render.ModeASCII)

	wantSubstrings := []string{
		"┌",                    // pylon native frame corner
		"─",                    // pylon native frame edge
		"│",                    // pylon native frame side
		"2026-04-25 SAT UTC+8", // subtitle survives banner-encoding (it is not bannered)
	}
	for _, sub := range wantSubstrings {
		if !strings.Contains(got, sub) {
			t.Errorf("ASCII output missing %q\n--- output ---\n%s", sub, got)
		}
	}

	if !strings.HasSuffix(got, "\n") {
		t.Errorf("ASCII output must end with \\n, got %q", got[max(0, len(got)-5):])
	}

	// At minimum: top frame + 6 banner rows + bottom frame + caption = 9 lines.
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) < 9 {
		t.Errorf("ASCII output has %d lines, want >= 9 (top frame + >=6 banner + bottom frame + caption)\n--- output ---\n%s", len(lines), got)
	}
}

func TestBannerSVG(t *testing.T) {
	got := render.Banner("13:45", "2026-04-25 SAT UTC+8", render.ModeSVG)

	wantSubstrings := []string{
		"<svg",
		"</svg>",
		"2026-04-25 SAT UTC+8",
	}
	for _, sub := range wantSubstrings {
		if !strings.Contains(got, sub) {
			t.Errorf("SVG output missing %q\n--- output (first 200 bytes) ---\n%s", sub, got[:min(200, len(got))])
		}
	}

	if strings.HasSuffix(got, "\n") {
		t.Errorf("SVG output must not end with \\n")
	}
}

// TestBannerColonSubstitution pins the contract that `:` in the headline is
// replaced with a space before reaching pylon, so the rendered output does
// not include the `?`-glyph fallback that pylon's banner font emits for
// unknown characters. Without the substitution, `13:45` would render with a
// `?` shape between the digit pairs.
//
// The check looks for the recognizable `?` glyph row: pylon's ASCII banner
// `?` glyph contains the substring `### ` followed by content that includes
// `?` punctuation in some rows. The cheapest robust pin is to render `13:45`
// vs `13 45` and assert the outputs are identical (the substitution should
// produce the same bytes either way).
func TestBannerColonSubstitution(t *testing.T) {
	withColon := render.Banner("13:45", "x", render.ModeASCII)
	withSpace := render.Banner("13 45", "x", render.ModeASCII)
	if withColon != withSpace {
		t.Errorf("Banner with `:` should match Banner with ` ` after substitution\n--- with colon ---\n%s\n--- with space ---\n%s", withColon, withSpace)
	}
}

func TestBannerEmptyHeadline(t *testing.T) {
	// Empty/whitespace-only headline must not panic; Banner substitutes the
	// shared placeholder, mirroring Box's contract.
	for _, mode := range []render.Mode{render.ModeASCII, render.ModeSVG} {
		got := render.Banner("", "x", mode)
		if got == "" {
			t.Errorf("Banner(\"\", _, %v) returned empty string; expected placeholder substitution", mode)
		}
	}
}
