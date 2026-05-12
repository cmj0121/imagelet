// Package metrics exposes the /metrics service: GET /metrics returns
// per-route request counts since startup rendered as a pylon banner card.
package metrics

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/middleware/reqcounter"
	"github.com/cmj0121/imagelet/render"
)

// Register mounts GET /metrics on r. ctr is the reqcounter.Counter that has
// been installed (via ctr.Middleware()) earlier in the chain; its Snapshot
// and Uptime are read on every request so the page always shows live counts.
func Register(r gin.IRouter, ctr *reqcounter.Counter) {
	r.GET("/metrics", func(c *gin.Context) {
		handler(c, ctr)
	})
}

// handler is the internal gin handler for /metrics.
func handler(c *gin.Context, ctr *reqcounter.Counter) {
	snap := ctr.Snapshot()
	uptime := ctr.Uptime()

	headline := "METRICS"
	captions := []string{
		fmt.Sprintf("%d routes · up %s", len(snap), formatUptime(uptime)),
	}
	boxes := buildBoxes(snap)

	c.Header("Cache-Control", "no-store")

	if middleware.WantsPylonSource(c) {
		src := render.BannerSourceBoxes(headline, "", captions, boxes)
		c.Data(http.StatusOK, "text/pylon", []byte(src))
		return
	}

	mode := middleware.ResolveMode(c)
	body, err := render.BannerBoxes(headline, "", captions, boxes, mode)
	if err != nil {
		log.Error().Err(err).Stringer("mode", mode).Msg("metrics: render banner")
		body, _ = render.BannerBoxes(headline, "", captions, boxes, render.ModeASCII)
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
		og := render.OGMeta{
			Title:       fmt.Sprintf("METRICS · %d routes", len(snap)),
			Description: "up " + formatUptime(uptime),
			ImageURL:    middleware.AbsoluteURL(c, "format=png"),
			ImageType:   "image/png",
			PageURL:     middleware.AbsoluteURL(c, ""),
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", render.InjectOGMeta(body, og))
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", body)
}

// buildBoxes assembles the single AlignLeft borderless box that holds the
// per-route count table. Returns nil when snap is empty (no routes yet).
func buildBoxes(snap []reqcounter.RouteCount) []render.BoxBlock {
	if len(snap) == 0 {
		return nil
	}

	// Determine the longest route name (after stripping pylon syntax) so
	// every row can be padded to the same width for tabular alignment.
	maxLen := 0
	for _, rc := range snap {
		name := render.StripPylonSyntax(rc.Route)
		if len(name) > maxLen {
			maxLen = len(name)
		}
	}

	rows := make([]string, 0, len(snap))
	for _, rc := range snap {
		name := render.StripPylonSyntax(rc.Route)
		// Pad route name to maxLen+2 spaces, then right-justify count in 8 chars.
		row := fmt.Sprintf("%-*s%8s", maxLen+2, name, formatCount(rc.Count))
		rows = append(rows, row)
	}

	return []render.BoxBlock{{Rows: rows, Align: render.AlignLeft}}
}

// formatCount formats n with thousands separators (e.g. 1234567 → "1,234,567").
func formatCount(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	// Insert commas every three digits from the right.
	var b strings.Builder
	rem := len(s) % 3
	if rem > 0 {
		b.WriteString(s[:rem])
	}
	for i := rem; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// formatUptime formats a time.Duration as "Xh Xm". Leading zero components
// are omitted. Returns "< 1m" when d is less than one minute.
func formatUptime(d time.Duration) string {
	total := int64(d.Seconds())
	h := total / 3600
	m := (total % 3600) / 60

	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case h > 0:
		return fmt.Sprintf("%dh", h)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	default:
		return "< 1m"
	}
}
