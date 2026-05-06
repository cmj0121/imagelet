// Package render — QR code rendering.
//
// Unlike the rest of this package which routes through pylon, /qr's
// figures are encoded by github.com/skip2/go-qrcode and rasterized
// in-package. The library exposes Bitmap() [][]bool which we render
// to four output surfaces ourselves so visual parameters (quiet zone,
// module pixel size, foreground/background) are owned here, not by
// skip2's own PNG/SVG paths.
//
// QR figures use canonical black-on-white for scanner reliability,
// not the GitHub-dark theme that PaintSVG applies to pylon output
// elsewhere. The HTML wrapper's body still inherits SVGBackground so
// the QR figure reads as a white card on a dark page.
//
// ASCII output uses Unicode half-block characters (U+2580/U+2584/
// U+2588). Despite the "ASCII" mode name (kept for project-wide
// consistency with /now, /stock, /index), this is a Unicode-art
// representation. Scannability via phone camera over a terminal
// screencap is best-effort — terminal anti-aliasing, theme inversion,
// and font ligatures all affect it. Callers wanting guaranteed
// scannability should use ?format=png.

package render

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"

	"github.com/skip2/go-qrcode"
)

// QRLevel selects the QR error-correction level.
type QRLevel int

const (
	QRLow      QRLevel = iota // L: ~7%  recovery
	QRMedium                  // M: ~15% recovery (default)
	QRQuartile                // Q: ~25% recovery
	QRHigh                    // H: ~30% recovery
)

// String returns the canonical single-letter label ("L"/"M"/"Q"/"H")
// the API exposes externally, even though skip2's own constants are
// named differently. Implements fmt.Stringer for log lines.
func (l QRLevel) String() string {
	switch l {
	case QRLow:
		return "L"
	case QRQuartile:
		return "Q"
	case QRHigh:
		return "H"
	default:
		return "M"
	}
}

// Visual parameters — see DESIGN.md section I. Foreground/background
// are canonical black-on-white for scanner reliability, NOT the
// GitHub-dark theme PaintSVG applies elsewhere.
const (
	qrModulePxSize     = 8
	qrQuietZoneModules = 4
	qrASCIIQuietZone   = 1
	qrForegroundHex    = "#000000"
	qrBackgroundHex    = "#ffffff"
)

// PNG fill sources are package-level so the per-request handler skips
// two allocations on every PNG render.
var (
	qrPNGBackground = &image.Uniform{C: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}}
	qrPNGForeground = &image.Uniform{C: color.RGBA{R: 0x00, G: 0x00, B: 0x00, A: 0xff}}
)

// qrEmptyPlaceholder substitutes for an empty input to keep the
// renderer panic-free without rejecting the request. Mirrors the
// emptyPlaceholder ("?") pattern used by render.Banner / render.Box;
// a single space is chosen here over "?" so the encoded QR points
// nowhere instead of pretending to encode a meaningful payload.
const qrEmptyPlaceholder = " "

// ParseQRLevel maps a case-insensitive single-letter level string to
// QRLevel. "L"/"M"/"Q"/"H" map to the obvious; anything else (empty,
// multi-character, surrounding whitespace, unknown letter) falls back
// to QRMedium so a bad query never produces a 4xx — matches the
// project-wide "bad query → no 4xx" convention for ?format= and
// ?region=.
func ParseQRLevel(s string) QRLevel {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "L":
		return QRLow
	case "Q":
		return QRQuartile
	case "H":
		return QRHigh
	default:
		return QRMedium
	}
}

// QRMatrix encodes text at the requested error-correction level and
// returns the raw module matrix (true = dark). Quiet zone is NOT
// included — the per-mode renderers add their own. Empty text falls
// back to qrEmptyPlaceholder so skip2 doesn't see an empty payload.
//
// Exported for tests asserting matrix dimensions / version selection.
func QRMatrix(text string, ec QRLevel) ([][]bool, error) {
	if text == "" {
		text = qrEmptyPlaceholder
	}
	q, err := qrcode.New(text, toSkip2Level(ec))
	if err != nil {
		return nil, fmt.Errorf("qr encode: %w", err)
	}
	return q.Bitmap(), nil
}

// QR encodes text as a QR code in mode and returns the rendered bytes.
// Empty text falls back to qrEmptyPlaceholder. Length capping is the
// caller's responsibility — render does not enforce it; service/qr
// handles HTTP 414 at the request boundary.
func QR(text string, ec QRLevel, mode Mode) ([]byte, error) {
	m, err := QRMatrix(text, ec)
	if err != nil {
		return nil, err
	}
	switch mode {
	case ModePNG:
		return qrToPNG(m)
	case ModeSVG:
		return qrToSVG(m), nil
	case ModeHTML:
		return qrToHTML(m), nil
	default:
		return qrToASCII(m), nil
	}
}

// toSkip2Level maps imagelet's L/M/Q/H taxonomy onto skip2's
// Low/Medium/High/Highest naming. The mismatch is real: skip2 uses
// "High" to mean Quartile (~25%) and "Highest" to mean H (~30%),
// while every QR-spec source and end user reading our docs uses L/M/Q/H.
// Keep the public API spec-aligned and absorb the rename here.
func toSkip2Level(l QRLevel) qrcode.RecoveryLevel {
	switch l {
	case QRLow:
		return qrcode.Low
	case QRQuartile:
		return qrcode.High
	case QRHigh:
		return qrcode.Highest
	default:
		return qrcode.Medium
	}
}

// qrToASCII renders the matrix with a 1-module quiet zone, packing
// two module rows per character row via the U+2580/U+2584/U+2588
// half-block trick. Trailing newline appended to match the rest of
// the package's ASCII paths.
//
// Odd matrix heights are rounded up by appending one all-light module
// row before the fold — the bottom half of the final ASCII row reads
// as background.
func qrToASCII(m [][]bool) []byte {
	if len(m) == 0 {
		return []byte("\n")
	}
	const quiet = qrASCIIQuietZone
	w := len(m[0])
	padW := w + 2*quiet

	// Build a padded view: quiet rows above + below the matrix, quiet
	// columns left + right of each row. modAt(r, c) returns true if
	// the padded coordinate is a dark module.
	totalRows := len(m) + 2*quiet
	modAt := func(r, c int) bool {
		if r < quiet || r >= len(m)+quiet {
			return false
		}
		if c < quiet || c >= w+quiet {
			return false
		}
		return m[r-quiet][c-quiet]
	}

	var b bytes.Buffer
	for r := 0; r < totalRows; r += 2 {
		for c := 0; c < padW; c++ {
			top := modAt(r, c)
			var bot bool
			if r+1 < totalRows {
				bot = modAt(r+1, c)
			}
			switch {
			case top && bot:
				b.WriteRune('█')
			case top:
				b.WriteRune('▀')
			case bot:
				b.WriteRune('▄')
			default:
				b.WriteByte(' ')
			}
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// forEachDarkModule walks m and invokes fn with the top-left pixel
// coordinate of each dark module after applying the quiet-zone offset
// q (in modules) and module pixel size s. Shared by qrToSVG and
// qrToPNG so the dark-module iteration shape lives in one place.
func forEachDarkModule(m [][]bool, q, s int, fn func(x, y int)) {
	for r := 0; r < len(m); r++ {
		for c := 0; c < len(m[0]); c++ {
			if !m[r][c] {
				continue
			}
			fn((c+q)*s, (r+q)*s)
		}
	}
}

// qrToSVG renders the matrix to a self-contained SVG document. Dark
// modules are emitted as a single <path> with one M{x} {y}h{s}v{s}h-{s}z
// segment per dark module — keeps file size tractable on dense
// (Version 40) matrices where a per-module <rect> would emit thousands
// of elements.
//
// The background <rect> stays separate so PaintSVG-style theming
// (which we deliberately do NOT apply here) could be retrofitted
// without rewriting the dark-module emit.
func qrToSVG(m [][]bool) []byte {
	const s = qrModulePxSize
	const q = qrQuietZoneModules
	if len(m) == 0 {
		return []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="0" height="0"/>`)
	}
	pxW := (len(m[0]) + 2*q) * s
	pxH := (len(m) + 2*q) * s

	var d strings.Builder
	forEachDarkModule(m, q, s, func(x, y int) {
		fmt.Fprintf(&d, "M%d %dh%dv%dh-%dz", x, y, s, s, s)
	})

	var b bytes.Buffer
	fmt.Fprintf(&b,
		`<?xml version="1.0" encoding="UTF-8"?>`+
			`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">`+
			`<rect width="100%%" height="100%%" fill="%s"/>`+
			`<path d="%s" fill="%s"/>`+
			`</svg>`,
		pxW, pxH, pxW, pxH, qrBackgroundHex, d.String(), qrForegroundHex)
	return b.Bytes()
}

// qrToPNG rasterizes the matrix directly (no SVG round-trip) at
// qrModulePxSize px per module with a qrQuietZoneModules-wide quiet
// zone. White background, black dark modules.
func qrToPNG(m [][]bool) ([]byte, error) {
	const s = qrModulePxSize
	const q = qrQuietZoneModules
	if len(m) == 0 {
		return nil, fmt.Errorf("qr: empty matrix")
	}
	pxW := (len(m[0]) + 2*q) * s
	pxH := (len(m) + 2*q) * s
	img := image.NewRGBA(image.Rect(0, 0, pxW, pxH))

	draw.Draw(img, img.Bounds(), qrPNGBackground, image.Point{}, draw.Src)
	forEachDarkModule(m, q, s, func(x, y int) {
		draw.Draw(img, image.Rect(x, y, x+s, y+s), qrPNGForeground, image.Point{}, draw.Src)
	})

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}
	return buf.Bytes(), nil
}

// qrToHTML wraps the SVG body via render.WrapHTML — same wrapper used
// by /now and /index so the HTML page bg (SVGBackground, GitHub-dark)
// shows around the QR figure's white card.
func qrToHTML(m [][]bool) []byte {
	return WrapHTML(qrToSVG(m))
}
