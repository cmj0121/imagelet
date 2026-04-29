package middleware

import (
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// Note: c.Request.Host (read below) carries the same proxy-trust property
// as X-Forwarded-Proto — when a CDN passes the Host header through verbatim
// it is similarly attacker-influenceable. We are NOT patching it in this
// PR; flagged here for a future hardening pass (host allowlist).

// xForwardedProtoHeader is the de-facto standard reverse-proxy header
// carrying the original client-facing scheme. Cloudflare, Caddy, nginx,
// and Fly.io all set it. We trust it because imagelet runs behind a CDN
// in production — direct external access (where a client could spoof
// the header) is not part of the deployment topology.
const xForwardedProtoHeader = "X-Forwarded-Proto"

// AbsoluteURL returns an absolute URL for the current request, suitable
// for og:url / og:image / canonical-link values. The shape is
// `<scheme>://<host><path>?<query>`.
//
// Scheme picked in order:
//
//  1. X-Forwarded-Proto request header — IF set to "http" or "https" (case-insensitive). Other values are ignored to prevent javascript:/data:/file: schemes from flowing into og:url emitters.
//  2. "https" when the request itself terminated TLS at this server
//  3. "http" otherwise
//
// Host comes from c.Request.Host (gin populates this from the HTTP
// request line / Host header). Path comes from c.Request.URL.Path.
//
// queryOverride is a partial query string (no leading `?` — caller
// passes "format=svg") whose keys are MERGED into the request's own
// query. Override keys win when both sides set the same key; non-
// overlapping keys from either side are preserved. Empty queryOverride
// keeps the request's query unchanged.
//
// Examples (path elided):
//
//	current=""                 + override=""          → ""
//	current="region=tw"        + override=""          → "region=tw"   (canonical og:url)
//	current="region=tw"        + override="format=svg"→ "region=tw&format=svg"
//	current="format=html"      + override="format=svg"→ "format=svg"  (override wins)
//	current="date=2012-02-02"  + override="format=svg"→ "date=2012-02-02&format=svg"
//
// The merged query is encoded via url.Values.Encode, which sorts keys
// alphabetically. That sort is the price of using the stdlib's safe
// query builder; it doesn't affect HTTP semantics (servers MUST treat
// query parameters as unordered) but it does mean `?b=2&a=1` becomes
// `?a=1&b=2` after a round-trip.
func AbsoluteURL(c *gin.Context, queryOverride string) string {
	scheme := "http"
	if proto := strings.ToLower(strings.TrimSpace(c.GetHeader(xForwardedProtoHeader))); proto == "http" || proto == "https" {
		scheme = proto
	} else if c.Request != nil && c.Request.TLS != nil {
		scheme = "https"
	}

	var host, path, rawQuery string
	if c.Request != nil {
		host = c.Request.Host
		if c.Request.URL != nil {
			path = c.Request.URL.Path
			rawQuery = c.Request.URL.RawQuery
		}
	}

	mergedQuery := mergeQuery(rawQuery, queryOverride)

	var b strings.Builder
	b.Grow(len(scheme) + 3 + len(host) + len(path) + 1 + len(mergedQuery))
	b.WriteString(scheme)
	b.WriteString("://")
	b.WriteString(host)
	b.WriteString(path)
	if mergedQuery != "" {
		b.WriteByte('?')
		b.WriteString(mergedQuery)
	}
	return b.String()
}

// mergeQuery merges two URL-encoded query strings. Keys present in
// override replace the same key in current; keys only in one side
// pass through. Returns the encoded result (no leading `?`). When
// override is empty current is returned verbatim — preserves the
// original key ordering for the common og:url case.
func mergeQuery(current, override string) string {
	if override == "" {
		return current
	}
	cur, _ := url.ParseQuery(current)
	ovr, _ := url.ParseQuery(override)
	for k, v := range ovr {
		cur[k] = v
	}
	return cur.Encode()
}
