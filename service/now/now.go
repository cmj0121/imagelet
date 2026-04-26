// Package now exposes the /now service: GET /now returns the current time
// in the caller's resolved timezone (via middleware.TimezoneDetector, with
// fallback to the server's local zone) as a pylon banner stacked above a
// date / weekday / zone caption. The wire format is content-negotiated:
//
//   - Accept: text/pylon → raw pylon source (callers render it themselves)
//   - User-Agent contains Mozilla → image/png
//   - everything else → text/plain; charset=utf-8 (ASCII)
//
// The service is reusable: external consumers can either call Register on
// any gin.IRouter (mounting it under any prefix), or invoke Handler directly
// from their own routing layer.
package now

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
)

// Register mounts GET /now -> Handler on r. r is typed as gin.IRouter so the
// service can be installed on either a *gin.Engine or a route group (e.g.
// for prefix-based versioning later).
func Register(r gin.IRouter) {
	r.GET("/now", Handler)
}

// Handler responds with the current time in the caller's timezone (resolved
// by middleware.TimezoneDetector from the CF-Timezone header, falling back
// to the server's local zone when no header is present), banner-rendered
// with a YYYY-MM-DD DAY UTC±H caption underneath. Accept: text/pylon takes
// precedence over the User-Agent-based mode and returns the raw pylon
// source so callers can render it themselves. Cache-Control: no-store is
// set on every response because the body changes every minute.
func Handler(c *gin.Context) {
	t := time.Now().In(middleware.GetLocation(c))
	head := headline(t)
	sub := subtitle(t)

	c.Header("Cache-Control", "no-store")
	if strings.Contains(c.GetHeader("Accept"), "text/pylon") {
		c.Data(http.StatusOK, "text/pylon", []byte(render.BannerSource(head, sub)))
		return
	}

	mode := middleware.GetMode(c)
	body, err := render.Banner(head, sub, mode)
	if err != nil {
		// PNG rendering can fail (font init, encode error). Fall back to
		// ASCII so the caller still gets *something* and the failure is
		// logged.
		log.Error().Err(err).Stringer("mode", mode).Msg("render banner")
		body, _ = render.Banner(head, sub, render.ModeASCII)
		mode = render.ModeASCII
	}
	if mode == render.ModePNG {
		c.Data(http.StatusOK, "image/png", body)
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", body)
}

// headline returns the wall-clock time as HH:MM. render.Banner replaces the
// `:` with a space at its boundary because pylon's banner font has no `:`
// glyph; the gap reads as a clock separator.
func headline(t time.Time) string {
	return t.Format("15:04")
}

// subtitle returns YYYY-MM-DD DAY UTC±H — ISO date, uppercase 3-letter
// weekday, signed integer-hour offset. ASCII-safe (no `/`, no Unicode) so
// it survives intact through every render path including raw pylon source.
func subtitle(t time.Time) string {
	_, off := t.Zone()
	return fmt.Sprintf("%s %s UTC%+d",
		t.Format("2006-01-02"),
		strings.ToUpper(t.Format("Mon")),
		off/3600,
	)
}
