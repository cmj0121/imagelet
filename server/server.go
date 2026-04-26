// Package server builds the gin engine for the imagelet HTTP service.
//
// New() returns an engine with gin.Recovery, gin.Logger,
// middleware.TimezoneDetector, middleware.ClientDetector, and
// middleware.RegionDetector preinstalled, plus a no-op root handler.
// Service plugins (service/now, service/stock, ...) mount their own
// routes on top via their own Register functions; server itself stays
// service-agnostic so external consumers can pick which services they want.
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
//  3. middleware.TimezoneDetector — resolves a *time.Location from the
//     CF-Timezone header (Cloudflare) and stashes it on the gin context.
//     Falls back to time.Local when the header is missing or unparseable.
//  4. middleware.ClientDetector — classifies the request by User-Agent and
//     stores the chosen render.Mode on the gin context.
//  5. middleware.RegionDetector — reads the CF-IPCountry header and stashes
//     the visitor's 2-letter country code on the gin context. Lets handlers
//     branch on country (e.g. service/stock picking a regional index) without
//     re-parsing the header on every request.
//
// The root GET / handler is also registered. Callers mount additional
// services with their Register helpers.
func New() *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.Logger())
	r.Use(middleware.TimezoneDetector())
	r.Use(middleware.ClientDetector())
	r.Use(middleware.RegionDetector())
	r.GET("/", rootHandler)
	return r
}

// rootHandler responds with HTTP 200 and an empty body.
func rootHandler(c *gin.Context) {
	c.Status(http.StatusOK)
}
