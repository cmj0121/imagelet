package render_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cmj0121/imagelet/render"
)

func TestBannerASCII(t *testing.T) {
	body, err := render.Banner("13:45", "2026-04-25 SAT UTC+8", render.ModeASCII)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := string(body)

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

func TestBannerPNG(t *testing.T) {
	body, err := render.Banner("13:45", "2026-04-25 SAT UTC+8", render.ModePNG)
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
}

// TestBannerColonRendersAsGlyph pins that `:` in the headline reaches pylon
// untouched and renders as the `:` banner glyph (two stacked block dots) —
// not as the `?`-glyph fallback for unknown chars. Pylon v0.2 added the
// printable-ASCII symbol set to the default banner font, including `:`,
// so the historical colon-to-space substitution is no longer needed. The
// regression we guard against: a future change re-introducing the
// substitution and silently breaking clock-style headlines like `13:45`.
//
// Concrete check: `13:45` and `13 45` must NOT render identically anymore;
// the colon variant carries an extra block of glyph rows where the space
// variant carries blank rows. Comparing byte length is the cheapest signal.
func TestBannerColonRendersAsGlyph(t *testing.T) {
	withColon, err := render.Banner("13:45", "x", render.ModeASCII)
	if err != nil {
		t.Fatalf("withColon err: %v", err)
	}
	withSpace, err := render.Banner("13 45", "x", render.ModeASCII)
	if err != nil {
		t.Fatalf("withSpace err: %v", err)
	}
	if bytes.Equal(withColon, withSpace) {
		t.Errorf("Banner(`13:45`) should differ from Banner(`13 45`) — colon should render as glyph, not be substituted to space\n--- output ---\n%s", withColon)
	}
}

func TestBannerEmptyHeadline(t *testing.T) {
	// Empty/whitespace-only headline must not panic; Banner substitutes the
	// shared placeholder, mirroring Box's contract.
	for _, mode := range []render.Mode{render.ModeASCII, render.ModePNG, render.ModeSVG, render.ModeHTML} {
		body, err := render.Banner("", "x", mode)
		if err != nil {
			t.Fatalf("Banner(\"\", _, %v) err: %v", mode, err)
		}
		if len(body) == 0 {
			t.Errorf("Banner(\"\", _, %v) returned empty body; expected placeholder substitution", mode)
		}
	}
}

func TestBannerHTML(t *testing.T) {
	body, err := render.Banner("13:45", "2026-04-25 SAT UTC+8", render.ModeHTML)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := string(body)
	for _, sub := range []string{
		"<!DOCTYPE html>",
		"<title>imagelet</title>",
		"<svg ",
		`xmlns="http://www.w3.org/2000/svg"`,
		"2026-04-25 SAT UTC+8",
		"</svg>",
		"</html>",
	} {
		if !strings.Contains(got, sub) {
			t.Errorf("HTML output missing %q\n--- output ---\n%s", sub, got)
		}
	}
}

func TestBannerStackHTML(t *testing.T) {
	body, err := render.BannerStack("S&P 500", []string{"row one", "row two"}, render.ModeHTML)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, "<!DOCTYPE html>") {
		t.Errorf("BannerStack HTML missing doctype\n--- output ---\n%s", got)
	}
	if !strings.Contains(got, "<svg ") {
		t.Errorf("BannerStack HTML missing inline <svg\n--- output ---\n%s", got)
	}
	// Caption rows must reach the inline SVG as <text> elements.
	if n := strings.Count(got, "<text "); n < 2 {
		t.Errorf("BannerStack HTML has %d <text> elements, want >= 2\n--- output ---\n%s", n, got)
	}
}

func TestBannerSVG(t *testing.T) {
	body, err := render.Banner("13:45", "2026-04-25 SAT UTC+8", render.ModeSVG)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := string(body)
	// xmlns + explicit width/height are the iOS-Safari load-bearing attrs.
	for _, sub := range []string{
		"<svg ",
		`xmlns="http://www.w3.org/2000/svg"`,
		`width="`,
		`height="`,
		"</svg>",
	} {
		if !strings.Contains(got, sub) {
			t.Errorf("SVG output missing %q\n--- output ---\n%s", sub, got)
		}
	}
	if bytes.HasSuffix(body, []byte("\n")) {
		t.Errorf("SVG output must not end with \\n")
	}
}

func TestBannerStackSVG(t *testing.T) {
	body, err := render.BannerStack("S&P 500", []string{"row one", "row two"}, render.ModeSVG)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, "<svg ") {
		t.Errorf("BannerStack SVG missing <svg root\n--- output ---\n%s", got)
	}
	// Multi-row stack must produce more than one <text> element so callers
	// know caption rows actually rendered.
	if n := strings.Count(got, "<text "); n < 2 {
		t.Errorf("BannerStack SVG has %d <text> elements, want >= 2\n--- output ---\n%s", n, got)
	}
}

// TestBannerSVGEscapesXML pins that pylon (or imagelet) escapes XML special
// chars in text content. /404 injects the requested path into the response;
// `/<script>` reaching an SVG `<text>` element unescaped would be a stored
// XSS vector for direct-navigation viewers (sandboxed in <img>, but not in
// iframes or top-level loads).
func TestBannerSVGEscapesXML(t *testing.T) {
	body, err := render.Banner("hi", "<script>alert(1)</script>", render.ModeSVG)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := string(body)
	if strings.Contains(got, "<script>") {
		t.Errorf("SVG contains literal <script> — XML escape failed\n--- output ---\n%s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("SVG missing escaped &lt;script&gt;\n--- output ---\n%s", got)
	}
}
