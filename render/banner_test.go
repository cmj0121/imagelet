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

func TestBannerSVG(t *testing.T) {
	body, err := render.Banner("13:45", "2026-04-25 SAT UTC+8", render.ModeSVG)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := string(body)
	// xmlns + explicit width/height are the iOS-Safari load-bearing attrs;
	// the GitHub-dark palette markers prove PaintSVG ran.
	for _, sub := range []string{
		"<svg ",
		`xmlns="http://www.w3.org/2000/svg"`,
		`width="`,
		`height="`,
		`fill="#0d1117"`,
		"#c9d1d9",
		"</svg>",
	} {
		if !strings.Contains(got, sub) {
			t.Errorf("SVG output missing %q\n--- output ---\n%s", sub, got)
		}
	}
	if strings.Contains(got, "#0f1c2d") {
		t.Errorf("SVG output still contains pylon default ink #0f1c2d (PaintSVG miss)\n--- output ---\n%s", got)
	}
	if bytes.HasSuffix(body, []byte("\n")) {
		t.Errorf("SVG output must not end with \\n")
	}
}

func TestBannerSourceMulti(t *testing.T) {
	cases := []struct {
		name     string
		headline string
		lines    []string
		want     string
	}{
		{
			name:     "no_lines_nil",
			headline: "hi",
			lines:    nil,
			want:     "[ hi | banner ]",
		},
		{
			name:     "no_lines_empty_slice",
			headline: "hi",
			lines:    []string{},
			want:     "[ hi | banner ]",
		},
		{
			name:     "one_line",
			headline: "hi",
			lines:    []string{"a"},
			want:     "[ hi | banner ]\n( a )",
		},
		{
			name:     "three_lines",
			headline: "13:45",
			lines:    []string{"a", "b", "c"},
			want:     "[ 13:45 | banner ]\n( a )\n( b )\n( c )",
		},
		{
			name:     "empty_headline_substitutes_placeholder",
			headline: "",
			lines:    []string{"x"},
			want:     "[ ? | banner ]\n( x )",
		},
		{
			name:     "whitespace_headline_substitutes_placeholder",
			headline: "   ",
			lines:    []string{"x"},
			want:     "[ ? | banner ]\n( x )",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := render.BannerSourceMulti(tc.headline, tc.lines); got != tc.want {
				t.Errorf("BannerSourceMulti = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBannerMultiASCII(t *testing.T) {
	body, err := render.BannerMulti("13:45",
		[]string{"2026-04-27 UTC+8", "S <M> T W T F S", "year ██░░ 32%"},
		render.ModeASCII)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := string(body)
	// Pylon native theme frame chars must appear.
	for _, sub := range []string{"┌", "─", "│", "└"} {
		if !strings.Contains(got, sub) {
			t.Errorf("ASCII output missing frame char %q\n--- output ---\n%s", sub, got)
		}
	}
	// Each caption row must reach the rendered output verbatim.
	for _, line := range []string{"2026-04-27 UTC+8", "S <M> T W T F S", "year ██░░ 32%"} {
		if !strings.Contains(got, line) {
			t.Errorf("ASCII output missing caption line %q\n--- output ---\n%s", line, got)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("ASCII output must end with \\n")
	}
}

func TestBannerMultiSVG(t *testing.T) {
	body, err := render.BannerMulti("13:45",
		[]string{"row1", "row2", "row3"}, render.ModeSVG)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := string(body)
	// PaintSVG markers prove the dark-theme post-processor is in the path.
	for _, sub := range []string{
		"<svg ",
		`fill="#0d1117"`,
		"#c9d1d9",
		"</svg>",
	} {
		if !strings.Contains(got, sub) {
			t.Errorf("SVG output missing %q\n--- output ---\n%s", sub, got)
		}
	}
	if strings.Contains(got, "#0f1c2d") {
		t.Errorf("SVG output still contains pylon default ink (PaintSVG miss)\n--- output ---\n%s", got)
	}
}

func TestBannerMultiHTML(t *testing.T) {
	body, err := render.BannerMulti("13:45",
		[]string{"row1", "row2", "row3"}, render.ModeHTML)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	got := string(body)
	for _, sub := range []string{"<!DOCTYPE html>", "<svg ", "</svg>", "</html>"} {
		if !strings.Contains(got, sub) {
			t.Errorf("HTML output missing %q\n--- output ---\n%s", sub, got)
		}
	}
}

func TestBannerMultiPNG(t *testing.T) {
	body, err := render.BannerMulti("13:45",
		[]string{"row1", "row2"}, render.ModePNG)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !bytes.HasPrefix(body, pngMagic) {
		t.Errorf("PNG output missing magic bytes; first 8 = %x", body[:min(8, len(body))])
	}
	if len(body) == 0 {
		t.Errorf("PNG output empty")
	}
}

func TestBannerMultiEmptyLines(t *testing.T) {
	// Zero captions must render cleanly in every mode (banner-only doc).
	for _, mode := range []render.Mode{render.ModeASCII, render.ModePNG, render.ModeSVG, render.ModeHTML} {
		body, err := render.BannerMulti("hi", nil, mode)
		if err != nil {
			t.Errorf("BannerMulti(_, nil, %v) err: %v", mode, err)
		}
		if len(body) == 0 {
			t.Errorf("BannerMulti(_, nil, %v) empty body", mode)
		}
	}
}

func TestBannerMultiEmptyHeadline(t *testing.T) {
	// Empty/whitespace headline must not panic; placeholder substitution
	// kicks in via BannerSourceMulti, mirroring Banner's contract.
	for _, mode := range []render.Mode{render.ModeASCII, render.ModePNG, render.ModeSVG, render.ModeHTML} {
		body, err := render.BannerMulti("", []string{"x"}, mode)
		if err != nil {
			t.Errorf("BannerMulti(\"\", _, %v) err: %v", mode, err)
		}
		if len(body) == 0 {
			t.Errorf("BannerMulti(\"\", _, %v) empty body", mode)
		}
	}
}

func TestBannerSourceBoxes(t *testing.T) {
	cases := []struct {
		name     string
		headline string
		captions []string
		boxes    []render.BoxBlock
		want     string
	}{
		{
			name:     "no_boxes_collapses_to_multi",
			headline: "hi",
			captions: []string{"cap"},
			want:     "[ hi | banner ]\n( cap )",
		},
		{
			name:     "single_box_appends_after_captions",
			headline: "hi",
			captions: []string{"cap"},
			boxes:    []render.BoxBlock{{Rows: []string{"r1", "r2"}}},
			want:     "[ hi | banner ]\n( cap )\n( r1\nr2 )",
		},
		{
			name:     "two_boxes_stack_with_no_divider",
			headline: "hi",
			boxes: []render.BoxBlock{
				{Rows: []string{"a1", "a2"}},
				{Rows: []string{"b1"}},
			},
			want: "[ hi | banner ]\n( a1\na2 )\n( b1 )",
		},
		{
			name:     "absent_boxes_skipped",
			headline: "hi",
			boxes: []render.BoxBlock{
				{Rows: []string{"a1"}},
				{},                              // empty rows, skipped
				{Rows: []string{"   ", "\t"}},   // all-whitespace rows, skipped
				{Rows: []string{"c1"}},
			},
			want: "[ hi | banner ]\n( a1 )\n( c1 )",
		},
		{
			name:     "all_boxes_absent_collapses_to_multi",
			headline: "hi",
			captions: []string{"cap"},
			boxes:    []render.BoxBlock{{}, {Rows: []string{" "}}, {}},
			want:     "[ hi | banner ]\n( cap )",
		},
		{
			name:     "align_left_emits_trailing_dash",
			headline: "hi",
			boxes: []render.BoxBlock{
				{Rows: []string{"r1", "r2"}, Align: render.AlignLeft},
			},
			// Pylon's `\s+-$` regex on box inner content picks up the
			// trailing `-` and switches the box to left alignment.
			want: "[ hi | banner ]\n( r1\nr2 -)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := render.BannerSourceBoxes(tc.headline, tc.captions, tc.boxes)
			if got != tc.want {
				t.Errorf("BannerSourceBoxes mismatch\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestBannerBoxesASCIIRendersBorderlessRows(t *testing.T) {
	body, err := render.BannerBoxes("hi", []string{"cap"},
		[]render.BoxBlock{
			{Rows: []string{"row-alpha", "row-beta"}, Align: render.AlignLeft},
		},
		render.ModeASCII)
	if err != nil {
		t.Fatalf("BannerBoxes err: %v", err)
	}
	got := string(body)
	for _, sub := range []string{"row-alpha", "row-beta"} {
		if !strings.Contains(got, sub) {
			t.Errorf("ASCII output missing %q\n--- output ---\n%s", sub, got)
		}
	}
	// No `<->` edge glyphs and no dashed-divider line — the new design
	// stacks borderless rows directly without any visible separator
	// (callers compose blank rows into the body when they want gaps).
	for _, sub := range []string{"◀", "▶", "------"} {
		if strings.Contains(got, sub) {
			t.Errorf("ASCII output contains forbidden marker %q\n--- output ---\n%s", sub, got)
		}
	}
}

func TestBannerBoxesCollapsesWhenAllBlocksEmpty(t *testing.T) {
	// Empty boxes slice must produce the same output as BannerMulti so
	// non-TW /stock visitors get a banner+captions surface with no
	// trailing dead space or stray divider lines.
	boxes, err := render.BannerBoxes("hi", []string{"cap"}, nil, render.ModeASCII)
	if err != nil {
		t.Fatalf("BannerBoxes err: %v", err)
	}
	multi, err := render.BannerMulti("hi", []string{"cap"}, render.ModeASCII)
	if err != nil {
		t.Fatalf("BannerMulti err: %v", err)
	}
	if !bytes.Equal(boxes, multi) {
		t.Errorf("BannerBoxes(nil) != BannerMulti\n--- boxes ---\n%s\n--- multi ---\n%s", boxes, multi)
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
