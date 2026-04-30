// Package favicon serves the imagelet brand mark at the two URLs
// browsers conventionally probe: /favicon.ico (the historical
// default the browser auto-requests on every page load) and
// /favicon.svg (modern, vector, what the HTML wrapper's <link>
// tags point at).
//
// Both assets are baked into the binary via go:embed so the
// service has no filesystem or network dependencies. The .ico is
// a multi-resolution Windows icon (16 + 32 + 48) generated from
// the canonical favicon.svg via rsvg-convert + ImageMagick; the
// SVG is the brand mark itself, a bracketless block "I" on the
// project's dark theme.
package favicon

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed favicon.ico
var icoBody []byte

//go:embed favicon.svg
var svgBody []byte

// cacheControl is shared by both routes. The brand mark is
// content-addressed by the binary (it changes only on a redeploy)
// so a long browser cache is safe; immutable would be even safer
// if the URL were content-hashed, but max-age=86400 keeps the
// behavior compatible with the conventional /favicon.* paths
// while still avoiding the per-page-load refetch.
const cacheControl = "public, max-age=86400"

// Register mounts GET /favicon.ico and GET /favicon.svg on r. The
// router is typed as gin.IRouter so the service can install onto
// either the engine or a route group.
func Register(r gin.IRouter) {
	r.GET("/favicon.ico", func(c *gin.Context) {
		c.Header("Cache-Control", cacheControl)
		c.Data(http.StatusOK, "image/x-icon", icoBody)
	})
	r.GET("/favicon.svg", func(c *gin.Context) {
		c.Header("Cache-Control", cacheControl)
		c.Data(http.StatusOK, "image/svg+xml", svgBody)
	})
}
