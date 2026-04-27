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

	t.Run("svg_time", func(t *testing.T) {
		body, err := render.Box("13:45", render.ModeSVG)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		got := string(body)
		// xmlns is the iOS-Safari load-bearing attribute; explicit
		// width/height likewise — viewBox alone is not enough. The
		// GitHub-dark fill markers prove PaintSVG ran.
		for _, sub := range []string{
			"<svg ",
			`xmlns="http://www.w3.org/2000/svg"`,
			`width="`,
			`height="`,
			`fill="#0d1117"`,
			"</svg>",
		} {
			if !strings.Contains(got, sub) {
				t.Errorf("SVG output missing %q\n--- output ---\n%s", sub, got)
			}
		}
		if bytes.HasSuffix(body, []byte("\n")) {
			t.Errorf("SVG output must not end with \\n")
		}
	})

	t.Run("html_time", func(t *testing.T) {
		body, err := render.Box("13:45", render.ModeHTML)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		got := string(body)
		// HTML5 doctype + viewport meta + inline SVG are the load-bearing
		// pieces — without doctype browsers fall to quirks mode, without
		// viewport meta mobile pinches don't behave, without the inline
		// `<svg ` the page is empty.
		for _, sub := range []string{
			"<!DOCTYPE html>",
			`<meta name="viewport"`,
			"<svg ",
			`xmlns="http://www.w3.org/2000/svg"`,
			"</svg>",
			"</html>",
		} {
			if !strings.Contains(got, sub) {
				t.Errorf("HTML output missing %q\n--- output ---\n%s", sub, got)
			}
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
		{render.ModeSVG, "svg"},
		{render.ModeHTML, "html"},
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
	for _, mode := range []render.Mode{render.ModeASCII, render.ModePNG, render.ModeSVG, render.ModeHTML} {
		body, err := render.Box("", mode)
		if err != nil {
			t.Fatalf("Box(\"\", %v) err: %v", mode, err)
		}
		if len(body) == 0 {
			t.Errorf("Box(\"\", %v) returned empty body; expected placeholder substitution", mode)
		}
	}
}

// TestWrapHTML pins the WrapHTML helper's output shape: it must produce a
// valid HTML5 document with the SVG body inlined verbatim. The marker
// string is chosen to be SVG-syntactically harmless and unique enough that
// finding it in the output means the body really was inlined (not, say,
// stripped or escaped).
func TestWrapHTML(t *testing.T) {
	const marker = `<svg id="probe-marker" xmlns="http://www.w3.org/2000/svg"><text>x</text></svg>`
	out := string(render.WrapHTML([]byte(marker)))

	for _, sub := range []string{
		"<!DOCTYPE html>",
		`<meta charset="utf-8">`,
		`<meta name="viewport"`,
		"<title>imagelet</title>",
		`id="probe-marker"`,
		// Body background matches the SVG's painted background so the
		// inline figure reads seamlessly with the surrounding page.
		"background:#0d1117",
		"</body>",
		"</html>",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("WrapHTML output missing %q\n--- output ---\n%s", sub, out)
		}
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("WrapHTML output must end with \\n for clean file writes")
	}
}
