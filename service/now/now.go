// Package now exposes the /now service: GET /now returns the current time
// in the caller's resolved timezone (via middleware.TimezoneDetector, with
// fallback to the server's local zone) as a pylon banner stacked above
// two borderless caption rows:
//
//  1. `YYYY-MM-DD UTC±H · S <M> T W T F S` — date + UTC offset on the
//     left, Sunday-first weekday strip with the current day in angle
//     brackets on the right, joined by a `·` middle-dot separator. The
//     visual strip replaces the textual `MON` weekday name.
//  2. `year ██████░░░░░░░░░░░░░░ NN%` — 20-cell year-progress meter.
//
// The wire format is content-negotiated:
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
// with two borderless caption rows underneath (date+UTC · weekday strip,
// year-progress). Accept: text/pylon (or ?format=pylon) takes precedence
// over the User-Agent-based mode and returns the raw pylon source so
// callers can render it themselves. Cache-Control: no-store is set on
// every response because the body changes every minute.
func Handler(c *gin.Context) {
	t := time.Now().In(middleware.GetLocation(c))
	head := headline(t)
	lines := metadataLines(t)

	c.Header("Cache-Control", "no-store")
	if middleware.WantsPylonSource(c) {
		c.Data(http.StatusOK, "text/pylon", []byte(render.BannerSourceMulti(head, lines)))
		return
	}

	mode := middleware.ResolveMode(c)
	body, err := render.BannerMulti(head, lines, mode)
	if err != nil {
		// PNG rendering can fail (font init, encode error). Fall back to
		// ASCII so the caller still gets *something* and the failure is
		// logged. SVG and HTML rendering are pure string assembly and
		// never error.
		log.Error().Err(err).Stringer("mode", mode).Msg("render banner")
		body, _ = render.BannerMulti(head, lines, render.ModeASCII)
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

// metadataLines returns the two borderless caption rows shown under the
// time banner. Row 1 carries date + UTC offset on the left and the
// weekday strip on the right, joined by a `·` middle-dot separator —
// the same field-separator glyph the codebase uses elsewhere (e.g.
// /stock's STALE prefix). Row 2 is the year-progress meter on its own
// line. The textual weekday name (`MON`) is dropped; WeekStrip is its
// visual replacement.
func metadataLines(t time.Time) []string {
	_, off := t.Zone()
	return []string{
		fmt.Sprintf("%s UTC%+d · %s",
			t.Format("2006-01-02"), off/3600, render.WeekStrip(t)),
		render.YearProgress(t),
	}
}
