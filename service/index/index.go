// Package index exposes the GET / handler: a pylon-rendered "IMAGELET"
// banner above two centered captions — the project tagline and the
// repo URL paired with the build version. The wire format is content-
// negotiated like /now and /stock:
//
//   - Accept: text/pylon OR ?format=pylon → raw banner source
//   - ?format=html or User-Agent contains Mozilla → text/html (inline SVG)
//   - ?format=svg → image/svg+xml
//   - ?format=png → image/png
//   - ?format=ascii → text/plain; charset=utf-8 (ASCII)
//   - everything else → text/plain; charset=utf-8 (ASCII)
//
// The handler closes over a fixed (tagline, repo, version) tuple so
// the rendered body is constant for the binary's lifetime — callers
// can rely on Cache-Control: public, max-age=3600. Liveness probes
// should use /healthz, not /, since this body is non-trivial.
package index

import (
	"fmt"
	"net/http"

	"github.com/cmj0121/pylon/src/go/pkg/pylon"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
)

// Defaults baked at compile time. Override Repo if you fork; Tagline
// matches the README's one-liner.
const (
	DefaultTagline = "show you should know in single image"
	DefaultRepo    = "github.com/cmj0121/imagelet"
)

// Register mounts GET / to a handler closure rendering "IMAGELET"
// over the tagline and "<repo> · <version>". version is typically
// the binary's main.version (set via -ldflags="-X main.version=…").
// The pylon source is constant for the binary's lifetime, so the
// handler pre-renders ASCII bytes and PNG bytes once at registration
// time and the request path becomes "branch + write".
func Register(r gin.IRouter, version string) {
	src := bannerSource(DefaultTagline, DefaultRepo, version)
	ast := pylon.Parse(src)
	asciiBody := []byte(pylon.RenderASCII(ast) + "\n")
	pngBody, err := pylon.RenderPNG(ast)
	if err != nil {
		// Failing here at startup is louder and easier to chase than
		// failing per-request. The render is deterministic — if it
		// works once it works forever.
		log.Error().Err(err).Msg("pre-render index png")
		pngBody = nil
	}
	svgBody := render.PaintSVG([]byte(pylon.RenderSVG(ast)))
	htmlBody := render.WrapHTML(svgBody)
	pylonBody := []byte(src + "\n")

	r.GET("/", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")

		if middleware.WantsPylonSource(c) {
			c.Data(http.StatusOK, "text/pylon", pylonBody)
			return
		}
		mode := middleware.ResolveMode(c)
		if mode == render.ModePNG && pngBody != nil {
			c.Data(http.StatusOK, "image/png", pngBody)
			return
		}
		if mode == render.ModeSVG {
			c.Data(http.StatusOK, "image/svg+xml", svgBody)
			return
		}
		if mode == render.ModeHTML {
			og := render.OGMeta{
				Title:       "imagelet",
				Description: DefaultTagline,
				ImageURL:    middleware.AbsoluteURL(c, "format=svg"),
				PageURL:     middleware.AbsoluteURL(c, ""),
			}
			c.Data(http.StatusOK, "text/html; charset=utf-8", render.InjectOGMeta(htmlBody, og))
			return
		}
		c.Data(http.StatusOK, "text/plain; charset=utf-8", asciiBody)
	})
}

// bannerSource composes the pylon source: a banner-rendered IMAGELET
// stacked above two borderless centered captions. Each caption is its
// own top-level box so pylon's natural multi-element stacking handles
// the layout without any frame between the lines.
func bannerSource(tagline, repo, version string) string {
	return fmt.Sprintf(
		"[ IMAGELET | banner ]\n( %s )\n( %s · %s )",
		tagline, repo, version,
	)
}
