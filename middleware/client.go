// Package middleware provides reusable gin middlewares.
//
// Middlewares here are intended to be installed on any imagelet gin.Engine
// (production or downstream consumer) without coupling to specific routes.
// Currently exposes ClientDetector for UA-based ASCII/HTML content
// negotiation, ResolveMode for ?format= query overrides, and
// WantsPylonSource for the raw-source pass-through; future middlewares
// (auth, request id, etc.) belong alongside them.
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
//   - UA contains "Mozilla" (case-insens.)  -> render.ModeHTML (browsers)
//   - everything else                       -> render.ModeASCII (CLI tools)
//
// Browsers default to HTML so a top-level navigation to / or /now reads
// like a page (title, viewport, centered SVG) rather than a raw image.
// Third-party sites embedding imagelet via `<img src="…">` should append
// `?format=png` (or `?format=svg`) to keep the raw-image contract — the
// UA classifier alone can't distinguish a top-level navigation from an
// image sub-request.
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

// ResolveMode returns the render.Mode for the request, honoring an explicit
// ?format= query parameter when present and falling back to the UA-derived
// GetMode otherwise. Precedence (highest first):
//
//  1. ?format=html  → render.ModeHTML
//  2. ?format=svg   → render.ModeSVG
//  3. ?format=png   → render.ModePNG
//  4. ?format=ascii → render.ModeASCII
//  5. UA classification (GetMode)
//
// Bad or missing values silently fall through to GetMode — never 4xx.
// ?format=pylon is NOT a render.Mode — pylon source bypasses the renderer
// and is negotiated by WantsPylonSource, which handlers must check before
// ResolveMode. The header-based Accept: text/pylon path runs through the
// same WantsPylonSource helper.
func ResolveMode(c *gin.Context) render.Mode {
	if q := strings.ToLower(strings.TrimSpace(c.Query("format"))); q != "" {
		switch q {
		case "html":
			return render.ModeHTML
		case "svg":
			return render.ModeSVG
		case "png":
			return render.ModePNG
		case "ascii":
			return render.ModeASCII
		}
	}
	return GetMode(c)
}

// WantsPylonSource reports whether the request is asking for raw pylon
// source — either via Accept: text/pylon (legacy header path) or via
// ?format=pylon (query-param parity). Handlers should call this BEFORE
// ResolveMode and short-circuit with `c.Data(200, "text/pylon", source)`
// when it returns true. Both spellings are accepted so curl callers using
// `-H "Accept: text/pylon"` and browser-bookmark callers using
// `?format=pylon` get the same answer.
func WantsPylonSource(c *gin.Context) bool {
	if strings.Contains(c.GetHeader("Accept"), "text/pylon") {
		return true
	}
	if q := strings.ToLower(strings.TrimSpace(c.Query("format"))); q == "pylon" {
		return true
	}
	return false
}

// classify implements the User-Agent decision rule. Pulled out so tests can
// exercise it directly without spinning up a gin engine.
func classify(ua string) render.Mode {
	if strings.Contains(strings.ToLower(ua), "mozilla") {
		return render.ModeHTML
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
