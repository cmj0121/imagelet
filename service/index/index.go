// Package index exposes the GET / handler: a pylon-rendered "IMAGELET"
// banner above two centered captions — the project tagline and the
// repo URL paired with the build version. The wire format is content-
// negotiated like /now and /stock:
//
//   - Accept: text/pylon → raw banner source
//   - ?format=svg → image/svg+xml; charset=utf-8
//   - ?format=png → image/png
//   - User-Agent contains Mozilla → image/png
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
	"strings"

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
	svgBody := []byte(pylon.RenderSVG(ast))
	pylonBody := []byte(src + "\n")

	r.GET("/", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")

		if strings.Contains(c.GetHeader("Accept"), "text/pylon") {
			c.Data(http.StatusOK, "text/pylon", pylonBody)
			return
		}
		mode := middleware.ResolveMode(c)
		if mode == render.ModePNG && pngBody != nil {
			c.Data(http.StatusOK, "image/png", pngBody)
			return
		}
		if mode == render.ModeSVG {
			c.Data(http.StatusOK, "image/svg+xml; charset=utf-8", svgBody)
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
