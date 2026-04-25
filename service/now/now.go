// Package now exposes the /now service: GET /now returns the current
// server-local time in HH:MM format inside a pylon-rendered box. The wire
// format (ASCII text or SVG) is chosen by middleware.ClientDetector based
// on the request's User-Agent.
//
// The service is reusable: external consumers can either call Register on
// any gin.IRouter (mounting it under any prefix), or invoke Handler directly
// from their own routing layer.
package now

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
)

// timeFormat is the wall-clock layout used in the response body. Go's
// reference time encodes HH:MM as "15:04".
const timeFormat = "15:04"

// Register mounts GET /now -> Handler on r. r is typed as gin.IRouter so the
// service can be installed on either a *gin.Engine or a route group (e.g.
// for prefix-based versioning later).
func Register(r gin.IRouter) {
	r.GET("/now", Handler)
}

// Handler responds with the current server-local time framed by render.Box.
// The render mode is whatever middleware.ClientDetector decided for this
// request — handlers do not duplicate the User-Agent rule. Cache-Control:
// no-store is set on every response because the body changes every minute.
func Handler(c *gin.Context) {
	mode := middleware.GetMode(c)
	body := render.Box(time.Now().Format(timeFormat), mode)
	c.Header("Cache-Control", "no-store")
	switch mode {
	case render.ModeSVG:
		c.Data(http.StatusOK, "image/svg+xml", []byte(body))
	case render.ModeASCII:
		c.String(http.StatusOK, "%s", body)
	default:
		// Unknown modes fall back to ASCII — plain text is universally readable.
		c.String(http.StatusOK, "%s", body)
	}
}
