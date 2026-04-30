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
// The handler closes over a fixed (tagline, repo, version) tuple per
// supported locale so the rendered body is constant for the binary's
// lifetime within each locale — callers can rely on Cache-Control:
// public, max-age=3600. Liveness probes should use /healthz, not /,
// since this body is non-trivial.
package index

import (
	"fmt"
	"net/http"

	"github.com/cmj0121/pylon/src/go/pkg/pylon"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/imagelet/internal/i18n"
	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
)

// Defaults baked at compile time. Override Repo if you fork; Tagline
// matches the en catalog's IndexTagline byte-for-byte — exposed as a
// constant so tests can pin the legacy value without importing i18n.
const (
	DefaultTagline = "show you should know in single image"
	DefaultRepo    = "github.com/cmj0121/imagelet"
)

// indexBundle holds the pre-rendered surface bytes for one locale.
// Built once per supported locale at registration time so the request
// hot path stays "branch + write" — locale-derived bodies are looked
// up by the resolved Locale from the gin context.
type indexBundle struct {
	asciiBody []byte
	pngBody   []byte
	svgBody   []byte
	htmlBody  []byte
	pylonBody []byte
	tagline   string
}

// supportedLocales lists the locales the index service pre-renders for.
// Mirrors internal/i18n's locale enum; if a locale is added there, this
// slice must be extended (compile-time check via the i18n.Locale switch
// in i18n.For ensures unknown locales render as en).
var supportedLocales = []i18n.Locale{i18n.LocaleEN, i18n.LocaleZhTW, i18n.LocaleZhCN}

// Register mounts GET / to a handler closure rendering "IMAGELET"
// over the tagline and "<repo> · <version>". version is typically
// the binary's main.version (set via -ldflags="-X main.version=…").
// The pylon source is constant for the binary's lifetime per locale,
// so each supported locale pre-renders ASCII / PNG / SVG / HTML / pylon
// bytes once at registration time and the request path becomes
// "lookup-by-locale + write".
func Register(r gin.IRouter, version string) {
	bundles := make(map[i18n.Locale]*indexBundle, len(supportedLocales))
	for _, loc := range supportedLocales {
		bundles[loc] = newIndexBundle(i18n.For(loc).IndexTagline, version)
	}

	r.GET("/", func(c *gin.Context) {
		c.Header("Cache-Control", "public, max-age=3600")
		bundle := bundles[i18n.GetLocale(c)]
		if bundle == nil {
			bundle = bundles[i18n.LocaleEN]
		}

		if middleware.WantsPylonSource(c) {
			c.Data(http.StatusOK, "text/pylon", bundle.pylonBody)
			return
		}
		mode := middleware.ResolveMode(c)
		if mode == render.ModePNG && bundle.pngBody != nil {
			c.Data(http.StatusOK, "image/png", bundle.pngBody)
			return
		}
		if mode == render.ModeSVG {
			c.Data(http.StatusOK, "image/svg+xml", bundle.svgBody)
			return
		}
		if mode == render.ModeHTML {
			og := render.OGMeta{
				Title:       "imagelet",
				Description: bundle.tagline,
				ImageURL:    middleware.AbsoluteURL(c, "format=png"),
				ImageType:   "image/png",
				PageURL:     middleware.AbsoluteURL(c, ""),
			}
			c.Data(http.StatusOK, "text/html; charset=utf-8", render.InjectOGMeta(bundle.htmlBody, og))
			return
		}
		c.Data(http.StatusOK, "text/plain; charset=utf-8", bundle.asciiBody)
	})
}

// newIndexBundle pre-renders the five surface bytes for one (tagline,
// version) tuple. PNG render failures are logged and leave pngBody nil
// — the request handler falls back to ASCII for that surface.
func newIndexBundle(tagline, version string) *indexBundle {
	src := bannerSource(tagline, DefaultRepo, version)
	ast := pylon.Parse(src)
	asciiBody := []byte(pylon.RenderASCII(ast) + "\n")
	pngBody, err := pylon.RenderPNG(ast)
	if err != nil {
		// Failing here at startup is louder and easier to chase than
		// failing per-request. The render is deterministic — if it
		// works once it works forever.
		log.Error().Err(err).Str("tagline", tagline).Msg("pre-render index png")
		pngBody = nil
	}
	svgBody := render.PaintSVG([]byte(pylon.RenderSVG(ast)))
	htmlBody := render.WrapHTML(svgBody)
	pylonBody := []byte(src + "\n")
	return &indexBundle{
		asciiBody: asciiBody,
		pngBody:   pngBody,
		svgBody:   svgBody,
		htmlBody:  htmlBody,
		pylonBody: pylonBody,
		tagline:   tagline,
	}
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
