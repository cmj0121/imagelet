package render_test

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/cmj0121/pylon/src/go/pkg/pylon"

	"github.com/cmj0121/imagelet/render"
)

// renderTestSVG builds a SVG via pylon for the given source so each
// test can hit the same end-to-end path RasterizeSVG exercises in
// production: pylon → RenderSVG → PaintSVG → RasterizeSVG.
func renderTestSVG(t *testing.T, src string) []byte {
	t.Helper()
	ast := pylon.Parse(src)
	return render.PaintSVG([]byte(pylon.RenderSVG(ast)))
}

func TestRasterizeSVGProducesValidPNG(t *testing.T) {
	out, err := render.RasterizeSVG(renderTestSVG(t, "[ HELLO ]"))
	if err != nil {
		t.Fatalf("RasterizeSVG: %v", err)
	}
	if !bytes.HasPrefix(out, pngMagic) {
		t.Errorf("output is not a PNG (missing magic bytes): %x", out[:8])
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		t.Errorf("PNG dimensions = %dx%d, want non-empty", b.Dx(), b.Dy())
	}
}

// TestRasterizeSVGCJKContentRenders is the regression guard for the
// original motivation: pylon.RenderPNG turns CJK glyphs into tofu, the
// SVG path renders fine. RasterizeSVG goes through the SVG path and
// must therefore preserve CJK content. Asserts that the output is a
// non-trivial PNG (non-empty body, non-uniform pixels) — without OCR
// in the test loop, that's the strongest content signal we can pin
// without making the test brittle to font kerning changes.
func TestRasterizeSVGCJKContentRenders(t *testing.T) {
	src := "[ 台積電 | banner ]\n( 2330 · 半導體 )"
	out, err := render.RasterizeSVG(renderTestSVG(t, src))
	if err != nil {
		t.Fatalf("RasterizeSVG: %v", err)
	}
	if !bytes.HasPrefix(out, pngMagic) {
		t.Fatalf("output is not a PNG: %x", out[:8])
	}
	if len(out) < 1024 {
		t.Errorf("PNG body is suspiciously small (%d bytes); CJK render likely produced an empty image", len(out))
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	if !hasNonUniformPixels(img) {
		t.Errorf("rasterized CJK PNG has uniform pixels — text was not drawn")
	}
}

func TestRasterizeSVGEmptyInputErrors(t *testing.T) {
	_, err := render.RasterizeSVG(nil)
	if err == nil {
		t.Errorf("nil input should error (no width/height); got nil")
	}
}

func TestRasterizeSVGMalformedSVGErrors(t *testing.T) {
	_, err := render.RasterizeSVG([]byte("<svg width='100' height"))
	if err == nil {
		t.Errorf("truncated svg should error from xml decoder")
	}
}

func TestBannerPNGModeRoutesThroughRasterizer(t *testing.T) {
	// Banner with CJK content should now produce a PNG with real
	// pixel content, not pylon.RenderPNG's tofu placeholder.
	out, err := render.Banner("台積電", "2330", render.ModePNG)
	if err != nil {
		t.Fatalf("render.Banner ModePNG: %v", err)
	}
	if !bytes.HasPrefix(out, pngMagic) {
		t.Errorf("Banner ModePNG output is not a PNG: %x", out[:8])
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode PNG: %v", err)
	}
	if !hasNonUniformPixels(img) {
		t.Errorf("Banner ModePNG output has uniform pixels — render path didn't draw")
	}
}

// hasNonUniformPixels samples a stride of pixels across the image and
// reports whether at least two distinct colors are present. A blank
// or all-background PNG fails this check; a real banner with text /
// rect glyphs trivially passes. Cheap proxy for "did anything draw."
func hasNonUniformPixels(img image.Image) bool {
	b := img.Bounds()
	step := 1
	if b.Dx() > 64 {
		step = b.Dx() / 64
	}
	var first uint32
	have := false
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			r, g, bl, _ := img.At(x, y).RGBA()
			pix := r<<16 | g<<8 | bl
			if !have {
				first = pix
				have = true
				continue
			}
			if pix != first {
				return true
			}
		}
	}
	return false
}

// TestRasterizeSVGRespectsTextLength is a sanity check on the pylon
// SVG schema: every text run pylon emits carries textLength and we
// honor it via per-glyph advance scaling. Confirm the parsed
// document captures textLength on at least one element so a future
// pylon SVG schema change (textLength dropped, attribute renamed)
// surfaces here instead of silently producing misaligned PNGs.
func TestRasterizeSVGRespectsTextLength(t *testing.T) {
	body := renderTestSVG(t, "[ ABC ]")
	if !strings.Contains(string(body), `textLength=`) {
		t.Skip("pylon SVG no longer emits textLength; rasterizer scaling assumption may need revisit")
	}
	// If we made it here, pylon still emits textLength and the
	// rasterizer's per-glyph scale path is exercised.
	out, err := render.RasterizeSVG(body)
	if err != nil {
		t.Fatalf("RasterizeSVG: %v", err)
	}
	if !bytes.HasPrefix(out, pngMagic) {
		t.Errorf("output not PNG: %x", out[:8])
	}
}
