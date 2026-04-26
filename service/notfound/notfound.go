// Package notfound exposes the 404 fallback for unmatched routes:
// gin's r.NoRoute(Handler) wires it in. The response is a pylon-
// rendered "404" banner stacked above a fake Python-style traceback
// with the requested path injected. The wire format is content-
// negotiated:
//
//   - Accept: text/pylon → raw banner source (no traceback; the
//     traceback isn't pylon syntax)
//   - User-Agent contains Mozilla → image/png with banner AND
//     traceback (composed locally — pylon's parser eats parens, so
//     we render the banner via pylon and draw the literal traceback
//     text below it with basicfont)
//   - everything else → text/plain; charset=utf-8 with banner +
//     traceback concatenated
//
// The traceback is theatre: file paths, line numbers, hex addresses,
// and the chained KeyError/RouteNotFound exceptions don't correspond
// to anything in the binary. The requested path is the only real
// signal — it appears in the panic message and the trailing field.
package notfound

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"strings"

	"github.com/cmj0121/pylon/src/go/pkg/pylon"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
)

// bannerSource is the pylon source for the 404 headline. Banner mode
// renders the digits as ANSI Shadow block letters across 6 rows,
// framed in pylon's native Unicode box (or `+ - |` under theme:ascii).
const bannerSource = "[ 404 | banner ]"

// Register installs Handler as gin's NoRoute fallback. Must take
// *gin.Engine (not gin.IRouter) because NoRoute is engine-level —
// you can't scope it to a route group.
func Register(r *gin.Engine) {
	r.NoRoute(Handler)
}

// Handler renders the 404 page. Status is always 404. See the
// package doc for the negotiation rules.
func Handler(c *gin.Context) {
	path := sanitizePath(c.Request.URL.Path)
	method := c.Request.Method

	c.Header("Cache-Control", "no-store")

	if strings.Contains(c.GetHeader("Accept"), "text/pylon") {
		c.Data(http.StatusNotFound, "text/pylon", []byte(bannerSource+"\n"))
		return
	}

	mode := middleware.GetMode(c)
	traceback := pythonTraceback(path, method)

	if mode == render.ModePNG {
		body, err := composePNG(traceback)
		if err == nil {
			c.Data(http.StatusNotFound, "image/png", body)
			return
		}
		// Composition failure (font init, encode error, banner PNG
		// decode) falls through to ASCII so the caller still gets
		// *something* — and the operator gets a log line to chase.
		log.Error().Err(err).Msg("compose 404 png")
	}

	bannerASCII := pylon.RenderASCII(pylon.Parse(bannerSource))
	body := bannerASCII + "\n" + traceback + "\n"
	c.Data(http.StatusNotFound, "text/plain; charset=utf-8", []byte(body))
}

// composePNG renders the banner via pylon, decodes the resulting PNG,
// and draws the literal traceback text below it on a single canvas so
// the Mozilla path matches the ASCII path's content (banner + every
// line of the Python trace). Pylon's parser would shred the trace's
// parens / brackets if we fed it through pylon directly — local
// composition with basicfont sidesteps that.
//
// Layout:
//
//	┌──────────────────────────┐
//	│   pylon banner image     │   ← decoded as-is, centered if
//	└──────────────────────────┘     narrower than canvas width
//	  Traceback (...)               ← drawn with basicfont.Face7x13
//	    File "...", line ...        ← left-padded by composePadding px
//	    ...                          (light background, navy ink to
//	  KeyError: ...                  match pylon's default theme)
func composePNG(traceback string) ([]byte, error) {
	bannerBytes, err := pylon.RenderPNG(pylon.Parse(bannerSource))
	if err != nil {
		return nil, fmt.Errorf("render banner: %w", err)
	}
	bannerImg, err := png.Decode(bytes.NewReader(bannerBytes))
	if err != nil {
		return nil, fmt.Errorf("decode banner png: %w", err)
	}

	face := basicfont.Face7x13
	cellW := face.Advance
	lineH := face.Height
	ascent := face.Ascent

	lines := strings.Split(traceback, "\n")
	maxCols := 0
	for _, l := range lines {
		if w := len(l); w > maxCols {
			maxCols = w
		}
	}

	bannerW := bannerImg.Bounds().Dx()
	bannerH := bannerImg.Bounds().Dy()
	textW := maxCols*cellW + 2*composePadding
	textH := len(lines)*lineH + composePadding

	canvasW := bannerW
	if textW > canvasW {
		canvasW = textW
	}
	canvasH := bannerH + composeGap + textH

	canvas := image.NewRGBA(image.Rect(0, 0, canvasW, canvasH))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{composeBG}, image.Point{}, draw.Src)

	// Center the banner horizontally; clamp to 0 if it's wider than
	// canvas (won't happen with current sizes but guards future tweaks).
	bannerX := (canvasW - bannerW) / 2
	if bannerX < 0 {
		bannerX = 0
	}
	draw.Draw(canvas,
		image.Rect(bannerX, 0, bannerX+bannerW, bannerH),
		bannerImg, image.Point{}, draw.Over)

	drawer := &font.Drawer{
		Dst:  canvas,
		Src:  &image.Uniform{composeInk},
		Face: face,
	}
	for i, line := range lines {
		drawer.Dot = fixed.Point26_6{
			X: fixed.I(composePadding),
			Y: fixed.I(bannerH + composeGap + ascent + i*lineH),
		}
		drawer.DrawString(line)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}
	return buf.Bytes(), nil
}

// Composition constants. composeBG and composeInk match pylon's
// default light theme so the banner blends into the canvas.
const (
	composePadding = 16
	composeGap     = 12
)

var (
	composeBG  = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	composeInk = color.RGBA{R: 0x0f, G: 0x1c, B: 0x2d, A: 0xff}
)

// pythonTraceback returns the literal Python-style traceback body
// with path injected into the panic messages and the trailing
// field. The traceback is prose, not pylon syntax — pylon would
// shred the parens and brackets, so we render the banner separately
// and concatenate this text after.
func pythonTraceback(path, method string) string {
	var b strings.Builder
	b.WriteString("Traceback (most recent call last):\n")
	b.WriteString("  File \"/imagelet/server.py\", line 88, in serve\n")
	b.WriteString("    response = router.dispatch(request)\n")
	b.WriteString("               ^^^^^^^^^^^^^^^^^^^^^^^^^\n")
	b.WriteString("  File \"/imagelet/router.py\", line 127, in dispatch\n")
	b.WriteString("    return self._routes[path](request)\n")
	b.WriteString("                       ~~~~~~~~~~~~^^^\n")
	fmt.Fprintf(&b, "KeyError: '%s'\n", path)
	b.WriteString("\n")
	b.WriteString("The above exception was the direct cause of the following exception:\n")
	b.WriteString("\n")
	b.WriteString("Traceback (most recent call last):\n")
	b.WriteString("  File \"/imagelet/__main__.py\", line 8, in <module>\n")
	b.WriteString("    app.run(host=\"0.0.0.0\", port=8080)\n")
	fmt.Fprintf(&b, "imagelet.errors.RouteNotFound: no handler for '%s'\n", path)
	b.WriteString("\n")
	fmt.Fprintf(&b, "path:   %s\n", path)
	fmt.Fprintf(&b, "method: %s\n", method)
	b.WriteString("status: 404")
	return b.String()
}

// sanitizePath returns the URL path with control characters stripped
// so a hostile or malformed request can't smuggle ANSI escapes /
// newlines / NULs into the literal-text traceback. URL-encoded
// values are passed through as-is — gin has already decoded them
// to runes by the time we read URL.Path.
func sanitizePath(p string) string {
	if p == "" {
		return "/"
	}
	var b strings.Builder
	b.Grow(len(p))
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if out == "" {
		return "/"
	}
	return out
}
