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
// Three-bucket classification rule:
//
//	┌────────────────────────────────────────┬──────────────────┐
//	│ User-Agent                             │ render.Mode      │
//	├────────────────────────────────────────┼──────────────────┤
//	│ CLI / scripting client (curl, wget,    │ render.ModeASCII │
//	│ HTTPie, go-http-client, python-        │                  │
//	│ requests, libwww, okhttp, …)           │                  │
//	├────────────────────────────────────────┼──────────────────┤
//	│ Real browser (Mozilla in UA, not       │ render.ModeHTML  │
//	│ also a known unfurl bot)               │                  │
//	├────────────────────────────────────────┼──────────────────┤
//	│ Everything else: link-unfurl bots      │ render.ModePNG   │
//	│ (TelegramBot, Slackbot, Twitterbot,    │                  │
//	│ facebookexternalhit, Discordbot, …),   │                  │
//	│ unknown clients, empty UA              │                  │
//	└────────────────────────────────────────┴──────────────────┘
//
// Why PNG by default for non-browser non-CLI clients: an image service
// should serve images. Unfurl bots that parse OG meta from HTML keep
// working through the explicit og:image URL (which carries ?format=png
// to force the image path on the second hop), but bots that simply
// embed whatever the link returns now get a usable image directly
// instead of falling back to a bare URL.
//
// Some unfurl bots (Discordbot, LinkedInBot) embed "Mozilla" in their
// UA to bypass crude bot filters; the unfurlBots list is checked before
// the Mozilla → HTML branch so they land on PNG with the rest.
//
// Third-party sites embedding imagelet via `<img src="…">` should still
// append `?format=png` (or `?format=svg`) explicitly — the UA classifier
// alone can't tell a top-level browser navigation from an image
// sub-request, and most browsers send the same UA for both.
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

// cliTools are case-insensitive substrings that mark an HTTP client as a
// CLI / scripting tool whose user wants plain text rather than HTML or a
// PNG dumped into the terminal. Entries MUST be lowercase — the
// classify() loop compares against a lowercased UA.
//
// Each pattern is anchored where useful (`curl/`, `java/`, `line/`) so
// brand names embedded in unrelated UAs don't accidentally route a
// browser or bot into the ASCII bucket.
var cliTools = []string{
	"curl/",
	"wget",
	"httpie",
	"go-http-client",
	"python-requests",
	"python-urllib",
	"libwww-perl",
	"lwp-",
	"okhttp",
	"java/",
	"powershell",
}

// unfurlBots are case-insensitive substrings for bots that fetch a URL
// to render a link preview. The list exists primarily because some of
// them (Discordbot, LinkedInBot, Applebot) embed "Mozilla" in their UA
// to bypass crude bot filters — without this list they'd fall into the
// Mozilla → HTML branch, but we want them on PNG so the preview embeds
// the image directly. Mozilla-free bots (TelegramBot, facebookexternalhit)
// would hit the default-PNG branch anyway; they're listed here to keep
// the classifier explicit about who's a bot vs. an unknown client.
//
// Curated rather than regex'd against generic "bot" / "crawler" —
// those would over-match search-engine crawlers (Googlebot, Bingbot)
// where HTML is fine and arguably preferred for indexing.
var unfurlBots = []string{
	"facebookexternalhit",
	"twitterbot",
	"slackbot",
	"linkedinbot",
	"linkedininspector",
	"discordbot",
	"telegrambot",
	"whatsapp",
	"pinterestbot",
	"redditbot",
	"embedly",
	"vkshare",
	"applebot",
	"skypeuripreview",
	"line/",
}

// classify implements the User-Agent decision rule.
//
// Order matters:
//  1. CLI tools beat everything (Mozilla-bearing CLI wrappers exist in
//     the wild — `httpie` ships one — so this branch must run first).
//  2. Unfurl bots beat the Mozilla branch (Discordbot etc. fake
//     Mozilla; without this they'd land on HTML).
//  3. Mozilla → HTML covers real browsers.
//  4. Everything else falls through to PNG.
func classify(ua string) render.Mode {
	low := strings.ToLower(ua)
	switch {
	case containsAny(low, cliTools):
		return render.ModeASCII
	case containsAny(low, unfurlBots):
		return render.ModePNG
	case strings.Contains(low, "mozilla"):
		return render.ModeHTML
	default:
		return render.ModePNG
	}
}

// containsAny reports whether s contains any of the given substrings.
// Caller is responsible for case-folding s and needles consistently.
func containsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// truncate returns at most max bytes of s. If truncation occurs an ellipsis
// is appended so log readers can tell the value was cut.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
