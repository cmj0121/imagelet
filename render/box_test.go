package render_test

import (
	"strings"
	"testing"

	"github.com/cmj0121/imagelet/render"
)

func TestBox(t *testing.T) {
	tests := []struct {
		name string
		text string
		mode render.Mode
		want []string // substrings the output must contain
	}{
		{
			name: "ascii_time",
			text: "13:45",
			mode: render.ModeASCII,
			want: []string{"+", "-", "|", "13:45"},
		},
		{
			name: "ascii_letters",
			text: "abc",
			mode: render.ModeASCII,
			want: []string{"+", "-", "|", "abc"},
		},
		{
			name: "svg_time",
			text: "13:45",
			mode: render.ModeSVG,
			want: []string{"<svg", "</svg>", "13:45"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := render.Box(tc.text, tc.mode)
			for _, sub := range tc.want {
				if !strings.Contains(got, sub) {
					t.Errorf("output missing %q\n--- output ---\n%s", sub, got)
				}
			}
			if tc.mode == render.ModeASCII && !strings.HasSuffix(got, "\n") {
				t.Errorf("ASCII output must end with \\n, got %q", got[max(0, len(got)-5):])
			}
			if tc.mode == render.ModeSVG && strings.HasSuffix(got, "\n") {
				t.Errorf("SVG output must not end with \\n")
			}
		})
	}
}

func TestModeString(t *testing.T) {
	cases := []struct {
		mode render.Mode
		want string
	}{
		{render.ModeASCII, "ascii"},
		{render.ModeSVG, "svg"},
		{render.Mode(99), "ascii"},
	}
	for _, tc := range cases {
		if got := tc.mode.String(); got != tc.want {
			t.Errorf("Mode(%d).String() = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func TestBoxEmptyInput(t *testing.T) {
	// Box substitutes a placeholder for empty/whitespace-only input because
	// pylon.Parse panics on `[  ]`. This test pins that contract.
	for _, mode := range []render.Mode{render.ModeASCII, render.ModeSVG} {
		got := render.Box("", mode)
		if got == "" {
			t.Errorf("Box(\"\", %v) returned empty string; expected placeholder substitution", mode)
		}
	}
}
