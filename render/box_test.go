package render_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cmj0121/imagelet/render"
)

// pngMagic is the 8-byte PNG file signature.
var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

func TestBox(t *testing.T) {
	t.Run("ascii_time", func(t *testing.T) {
		body, err := render.Box("13:45", render.ModeASCII)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		got := string(body)
		for _, sub := range []string{"+", "-", "|", "13:45"} {
			if !strings.Contains(got, sub) {
				t.Errorf("ASCII output missing %q\n--- output ---\n%s", sub, got)
			}
		}
		if !strings.HasSuffix(got, "\n") {
			t.Errorf("ASCII output must end with \\n")
		}
	})

	t.Run("ascii_letters", func(t *testing.T) {
		body, err := render.Box("abc", render.ModeASCII)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		got := string(body)
		for _, sub := range []string{"+", "-", "|", "abc"} {
			if !strings.Contains(got, sub) {
				t.Errorf("ASCII output missing %q\n--- output ---\n%s", sub, got)
			}
		}
	})

	t.Run("png_time", func(t *testing.T) {
		body, err := render.Box("13:45", render.ModePNG)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !bytes.HasPrefix(body, pngMagic) {
			t.Errorf("PNG output missing magic bytes; first 8 bytes = %x", body[:min(8, len(body))])
		}
		if len(body) == 0 {
			t.Errorf("PNG output is empty")
		}
		if bytes.HasSuffix(body, []byte("\n")) {
			t.Errorf("PNG output must not end with \\n")
		}
	})
}

func TestModeString(t *testing.T) {
	cases := []struct {
		mode render.Mode
		want string
	}{
		{render.ModeASCII, "ascii"},
		{render.ModePNG, "png"},
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
	for _, mode := range []render.Mode{render.ModeASCII, render.ModePNG} {
		body, err := render.Box("", mode)
		if err != nil {
			t.Fatalf("Box(\"\", %v) err: %v", mode, err)
		}
		if len(body) == 0 {
			t.Errorf("Box(\"\", %v) returned empty body; expected placeholder substitution", mode)
		}
	}
}
