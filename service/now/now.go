// Package now exposes the /now service: GET /now returns the current time
// in the caller's resolved timezone (via middleware.TimezoneDetector, with
// fallback to the server's local zone) as a pylon banner stacked above a
// `YYYY-MM-DD DAY UTC±H · year █░ NN%` caption. The trailing year-progress
// bar is a 20-cell `█`/`░` meter showing how far through the calendar year
// the visitor is. The wire format is content-negotiated:
//
//   - Accept: text/pylon OR ?format=pylon → raw pylon source
//   - ?format=html or User-Agent contains Mozilla → text/html (inline SVG)
//   - ?format=svg → image/svg+xml
//   - ?format=png → image/png
//   - ?format=ascii → text/plain; charset=utf-8 (ASCII)
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
	if middleware.WantsPylonSource(c) {
		c.Data(http.StatusOK, "text/pylon", []byte(render.BannerSource(head, sub)))
		return
	}

	mode := middleware.ResolveMode(c)
	body, err := render.Banner(head, sub, mode)
	if err != nil {
		// PNG rendering can fail (font init, encode error). Fall back to
		// ASCII so the caller still gets *something* and the failure is
		// logged. SVG and HTML rendering are pure string assembly and
		// never error.
		log.Error().Err(err).Stringer("mode", mode).Msg("render banner")
		body, _ = render.Banner(head, sub, render.ModeASCII)
		mode = render.ModeASCII
	}
	if mode == render.ModePNG {
		c.Data(http.StatusOK, "image/png", body)
		return
	}
	if mode == render.ModeSVG {
		c.Data(http.StatusOK, "image/svg+xml", body)
		return
	}
	if mode == render.ModeHTML {
		c.Data(http.StatusOK, "text/html; charset=utf-8", body)
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", body)
}

// headline returns the wall-clock time as HH:MM.
func headline(t time.Time) string {
	return t.Format("15:04")
}

// subtitle returns `YYYY-MM-DD DAY UTC±H · year █░ NN%` — ISO date,
// uppercase 3-letter weekday, signed integer-hour offset, and a 20-cell
// year-progress bar showing how far through the calendar year the visitor
// is. The progress fragment is appended after a `·` separator so the date
// and the year-meter share one subtitle box (option C from the design
// session). The bar uses `█` and `░`, both EAW-ambiguous so the row stays
// uniform-width in CJK terminals.
func subtitle(t time.Time) string {
	_, off := t.Zone()
	return fmt.Sprintf("%s %s UTC%+d · %s",
		t.Format("2006-01-02"),
		strings.ToUpper(t.Format("Mon")),
		off/3600,
		render.YearProgress(t),
	)
}
