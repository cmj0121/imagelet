// Package notfound exposes the 404 fallback for unmatched routes via
// gin's r.NoRoute. Same content negotiation as /now and /stock:
// Accept: text/pylon (or ?format=pylon) → bare banner source;
// ?format=html or Mozilla UA → text/html with banner-only inline SVG;
// ?format=svg → bare banner SVG; ?format=png → composed image/png;
// ?format=ascii or otherwise → text/plain. The PNG and ASCII paths
// show the same banner + traceback content; PNG composes locally
// because pylon's parser would shred the trace's parens and brackets.
// The SVG and HTML paths emit the banner only — the traceback can't
// be pylon-parsed, and a basicfont SVG compositor isn't built (v1
// limitation, lower priority than the per-route format support).
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

	"github.com/cmj0121/imagelet/internal/i18n"
	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
)

const bannerSource = "[ 404 | banner ]"

// composeCanvasW is wide enough for the bounded traceback at
// basicfont 7 px/cell — the longest fixed line ("    response =
// router.dispatch(request)" plus the carets) is ~50 cells, and
// pathological URL paths are capped well below 80. Hardcoding
// removes a per-request width-measurement loop.
const (
	composeCanvasW = 600
	composePadding = 16
	composeGap     = 12
)

var (
	composeBG  = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	composeInk = color.RGBA{R: 0x0f, G: 0x1c, B: 0x2d, A: 0xff}
)

// Pre-rendered banner artifacts. Source is a constant, so the parsed
// AST and rendered byte streams never change — building them once at
// init keeps every request to the "branch + memcpy + write" hot path.
// bannerImg may be nil if PNG render or decode fails at init; the
// PNG handler falls back to ASCII in that case.
var (
	bannerASCIIBody = []byte(pylon.RenderASCII(pylon.Parse(bannerSource)) + "\n")
	bannerPylonBody = []byte(bannerSource + "\n")
	bannerSVGBody   = render.PaintSVG([]byte(pylon.RenderSVG(pylon.Parse(bannerSource))))
	bannerHTMLBody  = render.WrapHTML(bannerSVGBody)
	bannerImg       image.Image
)

func init() {
	pngBytes, err := pylon.RenderPNG(pylon.Parse(bannerSource))
	if err != nil {
		log.Error().Err(err).Msg("pre-render 404 banner png")
		return
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		log.Error().Err(err).Msg("decode 404 banner png")
		return
	}
	bannerImg = img
}

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

	if middleware.WantsPylonSource(c) {
		c.Data(http.StatusNotFound, "text/pylon", bannerPylonBody)
		return
	}

	mode := middleware.ResolveMode(c)

	if mode == render.ModeSVG {
		// Banner-only — same trade-off as the text/pylon short-circuit.
		c.Data(http.StatusNotFound, "image/svg+xml", bannerSVGBody)
		return
	}
	if mode == render.ModeHTML {
		// Banner-only HTML — traceback path stays PNG/ASCII-exclusive
		// (see package doc). OG title localizes via the catalog so a
		// chat-preview unfurl reads in the visitor's language even
		// though the banner glyphs are Latin-only.
		cat := i18n.For(i18n.GetLocale(c))
		og := render.OGMeta{
			Title:     cat.NotFoundTitle,
			ImageURL:  middleware.AbsoluteURL(c, "format=png"),
			ImageType: "image/png",
			PageURL:   middleware.AbsoluteURL(c, ""),
		}
		c.Data(http.StatusNotFound, "text/html; charset=utf-8", render.InjectOGMeta(bannerHTMLBody, og))
		return
	}

	traceback := pythonTraceback(path, method)

	if mode == render.ModePNG && bannerImg != nil {
		body, err := composePNG(traceback)
		if err == nil {
			c.Data(http.StatusNotFound, "image/png", body)
			return
		}
		log.Error().Err(err).Msg("compose 404 png")
	}

	body := append(append([]byte{}, bannerASCIIBody...), []byte(traceback+"\n")...)
	c.Data(http.StatusNotFound, "text/plain; charset=utf-8", body)
}

// composePNG draws the literal traceback below the pre-rendered
// banner image on a fresh canvas. basicfont.Face7x13 is fixed-width
// ASCII; the traceback is all ASCII so glyph fallback never trips.
func composePNG(traceback string) ([]byte, error) {
	face := basicfont.Face7x13
	lineH := face.Height
	ascent := face.Ascent

	lines := strings.Split(traceback, "\n")
	bannerW := bannerImg.Bounds().Dx()
	bannerH := bannerImg.Bounds().Dy()
	textH := len(lines)*lineH + composePadding
	canvasH := bannerH + composeGap + textH

	canvas := image.NewRGBA(image.Rect(0, 0, composeCanvasW, canvasH))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{composeBG}, image.Point{}, draw.Src)

	bannerX := (composeCanvasW - bannerW) / 2
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

// pythonTraceback returns the literal Python-style traceback body
// with path injected into the panic messages and the trailing field.
// The traceback is prose, not pylon syntax — pylon would shred the
// parens and brackets, so we render the banner separately and
// concatenate this text after.
func pythonTraceback(path, method string) string {
	return fmt.Sprintf(tracebackTemplate, path, method)
}

const tracebackTemplate = `Traceback (most recent call last):
  File "/imagelet/server.py", line 88, in serve
    response = router.dispatch(request)
               ^^^^^^^^^^^^^^^^^^^^^^^^^
  File "/imagelet/router.py", line 127, in dispatch
    return self._routes[path](request)
                       ~~~~~~~~~~~~^^^
KeyError: '%[1]s'

The above exception was the direct cause of the following exception:

Traceback (most recent call last):
  File "/imagelet/__main__.py", line 8, in <module>
    app.run(host="0.0.0.0", port=8080)
imagelet.errors.RouteNotFound: no handler for '%[1]s'

path:   %[1]s
method: %[2]s
status: 404`

// sanitizePath returns the URL path with control characters stripped
// so a hostile or malformed request can't smuggle ANSI escapes /
// newlines / NULs into the literal-text traceback. URL-encoded values
// are passed through as-is — gin has already decoded them to runes
// by the time we read URL.Path.
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
