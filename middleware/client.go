// Package middleware provides reusable gin middlewares.
//
// Middlewares here are intended to be installed on any imagelet gin.Engine
// (production or downstream consumer) without coupling to specific routes.
// Currently exposes ClientDetector for ASCII/PNG content negotiation; future
// middlewares (auth, request id, etc.) belong alongside it.
package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/imagelet/render"
)

// modeKey is the gin-context key under which ClientDetector stores the
// chosen render.Mode. The "imagelet." prefix namespaces it so it cannot
// collide with keys set by other middlewares running on the same engine.
const modeKey = "imagelet.render.mode"

// uaLogLimit caps how much of the User-Agent header we put into log lines.
// Real-world UAs from browsers and bots can run hundreds of bytes; truncating
// keeps log volume sane while still leaving enough to identify the client.
const uaLogLimit = 120

// ClientDetector returns a gin middleware that classifies each request by its
// User-Agent header and stores the chosen render.Mode on the gin context.
// Handlers retrieve it with GetMode.
//
// Classification rule:
//   - empty UA                              -> render.ModeASCII (safe default)
//   - UA contains "Mozilla" (case-insens.)  -> render.ModePNG (browsers)
//   - everything else                       -> render.ModeASCII (CLI tools)
//
// The decision is logged at debug level so operators can confirm the rule
// fires correctly during dev. The middleware is stateless and safe to install
// on multiple engines.
func ClientDetector() gin.HandlerFunc {
	return func(c *gin.Context) {
		ua := c.GetHeader("User-Agent")
		mode := classify(ua)
		c.Set(modeKey, mode)

		// Gate the Debug event so truncate() is skipped on the hot path when
		// the runtime log level is above Debug.
		if e := log.Debug(); e.Enabled() {
			e.Str("user_agent", truncate(ua, uaLogLimit)).
				Stringer("mode", mode).
				Msg("client classified")
		}

		c.Next()
	}
}

// GetMode returns the render.Mode chosen by ClientDetector for the current
// request. If ClientDetector was not installed (or the value is missing for
// any other reason) GetMode returns render.ModeASCII as the safe default —
// plain text is universally readable, PNG is not.
func GetMode(c *gin.Context) render.Mode {
	v, ok := c.Get(modeKey)
	if !ok {
		return render.ModeASCII
	}
	mode, ok := v.(render.Mode)
	if !ok {
		return render.ModeASCII
	}
	return mode
}

// classify implements the User-Agent decision rule. Pulled out so tests can
// exercise it directly without spinning up a gin engine.
func classify(ua string) render.Mode {
	if strings.Contains(strings.ToLower(ua), "mozilla") {
		return render.ModePNG
	}
	return render.ModeASCII
}

// truncate returns at most max bytes of s. If truncation occurs an ellipsis
// is appended so log readers can tell the value was cut.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
