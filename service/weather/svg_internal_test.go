package weather

import (
	"encoding/xml"
	"strings"
	"testing"
)

// TestComposeSVGStructure pins the three coordinate regions of the
// composed SVG: outer wrapper, embedded pylon banner, icon text rows,
// caption text rows. If any region disappears the visual reads as
// broken — this test catches the regression before the eye does.
func TestComposeSVGStructure(t *testing.T) {
	icon := []string{
		`    \  |  / `,
		`     \ | /  `,
		`  ----O---- `,
		`     / | \  `,
		`    /  |  \ `,
	}
	captions := []string{
		"clear in Taipei (TW)",
		"feels 23C  wind 12 km/h",
		"high 25C / low 18C",
	}

	got := composeSVG("23.4", icon, captions)

	// Outer wrapper.
	for _, sub := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg"`,
		`viewBox="0 0 `,
		`width="`,
		`height="`,
		"</svg>",
	} {
		if !strings.Contains(got, sub) {
			t.Errorf("composeSVG missing %q\n--- output ---\n%s", sub, got)
		}
	}

	// Embedded pylon banner — the translate-group anchors it to the
	// composed canvas's icon-right offset.
	if !strings.Contains(got, `<g transform="translate(`) {
		t.Errorf("composeSVG missing translate-group for embedded banner\n--- output ---\n%s", got)
	}

	// Icon and caption text — must produce more <text> elements than the
	// banner alone would. 5 icon rows + 3 caption rows = 8, plus pylon's
	// own banner rows.
	if n := strings.Count(got, "<text "); n < 8 {
		t.Errorf("composeSVG has %d <text> elements, want >= 8 (5 icon + 3 caption + banner)\n--- output ---\n%s", n, got)
	}

	// Captions must reach the SVG.
	for _, line := range captions {
		if !strings.Contains(got, line) {
			t.Errorf("composeSVG missing caption %q\n--- output ---\n%s", line, got)
		}
	}
}

// TestComposeSVGWellFormed parses the composed SVG through encoding/xml.
// Strict parsing catches mismatched nesting, malformed attributes, or
// unescaped specials — a class of bugs the substring tests above can't
// see. SVG with stray `<` in `<text>` content would parse-fail here.
func TestComposeSVGWellFormed(t *testing.T) {
	icon := iconTable[BucketRain] // contains apostrophes; exercises XML escape
	captions := []string{
		"rain in Taipei (TW)",
		"feels 22C  wind 8 km/h",
	}
	got := composeSVG("18.5", icon, captions)

	dec := xml.NewDecoder(strings.NewReader(got))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				return
			}
			t.Fatalf("composeSVG produced malformed XML: %v\n--- output ---\n%s", err, got)
		}
	}
}

// TestComposeSVGEscapesXML pins that user-influenced caption text can't
// inject markup. `<script>` reaching SVG `<text>` content unescaped would
// be a stored XSS vector for top-level navigation viewers (sandboxed in
// `<img>` but not in iframes or direct loads).
func TestComposeSVGEscapesXML(t *testing.T) {
	captions := []string{
		"<script>alert(1)</script>",
	}
	got := composeSVG("99.9", iconTable[BucketCloudy], captions)

	if strings.Contains(got, "<script>") {
		t.Errorf("composeSVG leaked literal <script> — XML escape failed\n--- output ---\n%s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("composeSVG missing escaped &lt;script&gt;\n--- output ---\n%s", got)
	}
}

// TestComposeSVGHandlesIconApostrophes pins that the apostrophe-bearing
// icons (cloud, drizzle, rain, snow, …) emit valid XML — `'` in `<text>`
// content needs escaping or some XML parsers will flag the doc.
func TestComposeSVGHandlesIconApostrophes(t *testing.T) {
	for bucket, rows := range iconTable {
		hasApos := false
		for _, r := range rows {
			if strings.Contains(r, "'") {
				hasApos = true
				break
			}
		}
		if !hasApos {
			continue
		}
		got := composeSVG("20.0", rows, []string{"caption"})
		// Literal apostrophes in <text> content are technically legal,
		// but pylon and the rest of imagelet escape them — be consistent.
		if strings.Contains(got, "'") {
			t.Errorf("bucket %v: composeSVG leaked literal ' (apostrophe) in <text>\n--- output ---\n%s", bucket, got)
		}
		if !strings.Contains(got, "&apos;") {
			t.Errorf("bucket %v: composeSVG missing escaped &apos;\n--- output ---\n%s", bucket, got)
		}
	}
}

// TestParsePylonSVGSize pins that the regex pulls the viewBox dimensions
// out of pylon's actual output. If pylon ever changes its viewBox format
// composeSVG falls back to defaults (still emits an SVG), but we want the
// test to flag the change loudly so we can update the parser.
func TestParsePylonSVGSize(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantW   int
		wantH   int
		wantOk  bool
	}{
		{
			name:   "pylon_typical",
			input:  `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 150 80" width="150" height="80">`,
			wantW:  150,
			wantH:  80,
			wantOk: true,
		},
		{
			name:   "no_viewbox",
			input:  `<svg xmlns="http://www.w3.org/2000/svg" width="100" height="50">`,
			wantOk: false,
		},
		{
			name:   "non_zero_origin",
			input:  `<svg viewBox="10 20 100 50">`,
			wantOk: false, // we only match `0 0 W H`; if pylon ever offsets, fail loudly
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotW, gotH, ok := parsePylonSVGSize(tc.input)
			if ok != tc.wantOk {
				t.Errorf("ok = %v, want %v", ok, tc.wantOk)
			}
			if ok && (gotW != tc.wantW || gotH != tc.wantH) {
				t.Errorf("(w,h) = (%d,%d), want (%d,%d)", gotW, gotH, tc.wantW, tc.wantH)
			}
		})
	}
}

// TestStripPylonOuterSVG pins the inner-content extraction. composeSVG
// embeds the result inside a translate-group, so the extracted body
// must NOT carry the outer `<svg>` tags or the nested-doc-inside-group
// hits weird parser behavior in some renderers.
func TestStripPylonOuterSVG(t *testing.T) {
	in := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 60 26">
<style>text{font-family:mono}</style>
<text x="0" y="10">hi</text>
</svg>`
	got := stripPylonOuterSVG(in)
	if strings.Contains(got, "<svg ") || strings.Contains(got, "</svg>") {
		t.Errorf("stripped output still contains <svg> tags:\n%s", got)
	}
	if !strings.Contains(got, "<style>") || !strings.Contains(got, `<text x="0"`) {
		t.Errorf("stripped output missing inner content:\n%s", got)
	}
}
