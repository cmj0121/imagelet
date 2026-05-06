package render_test

import (
	"bytes"
	"image/png"
	"strings"
	"testing"

	"github.com/cmj0121/imagelet/render"
)

func TestParseQRLevel(t *testing.T) {
	cases := []struct {
		in   string
		want render.QRLevel
	}{
		{"L", render.QRLow},
		{"l", render.QRLow},
		{"M", render.QRMedium},
		{"m", render.QRMedium},
		{"Q", render.QRQuartile},
		{"q", render.QRQuartile},
		{"H", render.QRHigh},
		{"h", render.QRHigh},
		{" m ", render.QRMedium}, // trim
		{"", render.QRMedium},    // empty → default
		{"x", render.QRMedium},   // unknown letter
		{"ll", render.QRMedium},  // multi-char
		{"LOW", render.QRMedium}, // word, not single letter
	}
	for _, c := range cases {
		if got := render.ParseQRLevel(c.in); got != c.want {
			t.Errorf("ParseQRLevel(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestQRLevelString(t *testing.T) {
	cases := []struct {
		in   render.QRLevel
		want string
	}{
		{render.QRLow, "L"},
		{render.QRMedium, "M"},
		{render.QRQuartile, "Q"},
		{render.QRHigh, "H"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Errorf("QRLevel(%d).String() = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestQRMatrixDimensions(t *testing.T) {
	m, err := render.QRMatrix("https://example.com", render.QRMedium)
	if err != nil {
		t.Fatalf("QRMatrix: %v", err)
	}
	if len(m) == 0 {
		t.Fatal("QRMatrix returned empty matrix")
	}
	if len(m) != len(m[0]) {
		t.Errorf("matrix not square: %dx%d", len(m), len(m[0]))
	}
	// Version 1 is 21x21; Version 40 is 177x177. Any valid encoding sits
	// inside that envelope; we don't pin a specific version because skip2
	// picks the smallest fitting one.
	if len(m) < 21 || len(m) > 177 {
		t.Errorf("matrix size %d out of QR spec range [21..177]", len(m))
	}
}

func TestQRMatrixEmptyText(t *testing.T) {
	m, err := render.QRMatrix("", render.QRMedium)
	if err != nil {
		t.Fatalf("QRMatrix(\"\"): %v", err)
	}
	if len(m) == 0 {
		t.Fatal("empty-text matrix is empty")
	}
	if len(m) != len(m[0]) {
		t.Errorf("empty-text matrix not square: %dx%d", len(m), len(m[0]))
	}
}

func TestQRAllModes(t *testing.T) {
	const text = "https://imglet.sh"
	for _, mode := range []render.Mode{render.ModeASCII, render.ModeSVG, render.ModePNG, render.ModeHTML} {
		body, err := render.QR(text, render.QRMedium, mode)
		if err != nil {
			t.Errorf("QR(mode=%v): %v", mode, err)
			continue
		}
		if len(body) == 0 {
			t.Errorf("QR(mode=%v) returned empty body", mode)
		}
	}
}

func TestQRSVGStructure(t *testing.T) {
	body, err := render.QR("hello", render.QRMedium, render.ModeSVG)
	if err != nil {
		t.Fatalf("QR svg: %v", err)
	}
	s := string(body)
	if !strings.HasPrefix(s, "<?xml") && !strings.HasPrefix(s, "<svg") {
		t.Errorf("svg body does not start with <?xml or <svg; got %q", s[:min(len(s), 30)])
	}
	if !strings.Contains(s, "#000000") {
		t.Error("svg missing foreground hex #000000")
	}
	if !strings.Contains(s, "#ffffff") {
		t.Error("svg missing background hex #ffffff")
	}
	// Dark modules emitted as a single <path>; background as <rect>. Either
	// element existing means dark cells were drawn.
	if !strings.Contains(s, "<path") && !strings.Contains(s, "<rect") {
		t.Error("svg has no <rect> or <path> element")
	}
}

func TestQRPNGDecodes(t *testing.T) {
	body, err := render.QR("hello", render.QRMedium, render.ModePNG)
	if err != nil {
		t.Fatalf("QR png: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("png.Decode: %v", err)
	}
	// Confirm the decoded image dimensions are consistent with the
	// matrix dimensions + 4-module quiet zone × 8 px/module.
	m, err := render.QRMatrix("hello", render.QRMedium)
	if err != nil {
		t.Fatalf("QRMatrix: %v", err)
	}
	const s, q = 8, 4
	wantPx := (len(m[0]) + 2*q) * s
	if img.Bounds().Dx() != wantPx || img.Bounds().Dy() != wantPx {
		t.Errorf("png dimensions = %dx%d, want %dx%d",
			img.Bounds().Dx(), img.Bounds().Dy(), wantPx, wantPx)
	}
}

func TestQRHTMLWraps(t *testing.T) {
	body, err := render.QR("hello", render.QRMedium, render.ModeHTML)
	if err != nil {
		t.Fatalf("QR html: %v", err)
	}
	s := string(body)
	if !strings.HasPrefix(s, "<!DOCTYPE html>") {
		t.Errorf("html body does not start with DOCTYPE; got %q", s[:min(len(s), 30)])
	}
	if !strings.Contains(s, "<svg") {
		t.Error("html body missing <svg>")
	}
	if !strings.Contains(s, "</body>") {
		t.Error("html body missing </body>")
	}
}

func TestQRASCIIShape(t *testing.T) {
	body, err := render.QR("hi", render.QRMedium, render.ModeASCII)
	if err != nil {
		t.Fatalf("QR ascii: %v", err)
	}
	s := string(body)
	if len(s) == 0 {
		t.Fatal("ascii body empty")
	}
	// Confirm only the four expected runes (plus newlines).
	allowed := map[rune]bool{'█': true, '▀': true, '▄': true, ' ': true, '\n': true}
	for _, r := range s {
		if !allowed[r] {
			t.Errorf("unexpected rune %q (U+%04X) in ascii output", r, r)
			break
		}
	}
	// Row count = ceil((matrix_h + 2*1-mod-quiet) / 2).
	m, err := render.QRMatrix("hi", render.QRMedium)
	if err != nil {
		t.Fatalf("QRMatrix: %v", err)
	}
	totalModRows := len(m) + 2 // 1-mod quiet on top + bottom
	wantRows := (totalModRows + 1) / 2 // ceil
	gotRows := strings.Count(s, "\n")
	if gotRows != wantRows {
		t.Errorf("ascii row count = %d, want %d (matrix_h=%d)", gotRows, wantRows, len(m))
	}
}
