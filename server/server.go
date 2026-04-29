// Package server builds the gin engine for the imagelet HTTP service.
//
// New() returns an engine with the imagelet middleware chain
// preinstalled plus a no-op /healthz liveness handler. Service
// plugins (in service/...) mount their own routes via their own
// Register helpers; server itself stays service-agnostic so external
// consumers can pick which services they want.
package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
)

// New returns a gin engine with the imagelet middleware chain preinstalled,
// in this outer-to-inner order:
//
//  1. gin.Recovery — catches panics anywhere downstream and emits a 500.
//  2. gin.Logger — writes a per-request access line (method, path, status,
//     latency) to gin.DefaultWriter. cmd/imagelet wires that writer to the
//     process-wide zerolog stream so the line lands in the same JSON output.
//     Recovery wraps Logger so panics still produce an access entry with
//     status 500.
//  3. middleware.RequestSizeLimiter — rejects requests whose URL path or
//     query string exceed the imagelet caps with HTTP 414, and wraps the
//     body in a MaxBytesReader. Pinned BEFORE every imagelet middleware
//     so oversized inputs never reach a handler that would read c.Query
//     or c.Request.URL.Path (in particular the internal/htmlcache key
//     builder mounted by cmd/imagelet downstream of server.New).
//  4. middleware.TimezoneDetector — resolves a *time.Location from the
//     CF-Timezone header (Cloudflare) and stashes it on the gin context.
//     Falls back to time.Local when the header is missing or unparseable.
//  5. middleware.DateOverrideDetector — parses an optional ?date=YYYY-MM-DD
//     query parameter and stashes the resolved time on the gin context.
//     Pinned AFTER TimezoneDetector so the date string is anchored to the
//     caller's resolved location rather than time.Local. Lets time-aware
//     handlers (/now, /stock) reproject onto a historical date.
//  6. middleware.ClientDetector — classifies the request by User-Agent and
//     stores the chosen render.Mode on the gin context.
//  7. middleware.RegionDetector — reads the CF-IPCountry header and stashes
//     the visitor's 2-letter country code on the gin context. Lets handlers
//     branch on country (e.g. service/stock picking a regional index) without
//     re-parsing the header on every request.
//
// GET /healthz is also registered as the always-empty liveness probe.
// Callers mount additional services (including the rendered GET /
// landing page from service/index) via their Register helpers.
func New() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.Logger())
	r.Use(middleware.RequestSizeLimiter())
	r.Use(middleware.TimezoneDetector())
	r.Use(middleware.DateOverrideDetector())
	r.Use(middleware.ClientDetector())
	r.Use(middleware.RegionDetector())
	r.GET("/healthz", healthzHandler)
	return r
}

// healthzHandler responds with HTTP 200 and an empty body. This is the
// dedicated liveness probe for orchestrators (Kubernetes, Compose,
// Fly): it never renders, never reaches an upstream, never allocates.
// GET / is a rendered landing page now and unsuitable for high-rate
// probes.
func healthzHandler(c *gin.Context) {
	c.Status(http.StatusOK)
}
