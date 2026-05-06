// Package iplimit provides a per-IP token-bucket gin middleware (R10).
//
// The middleware is mounted on a route group (e.g. /github/*) so its
// budget enforcement is scoped to abuse-prone routes only — the public
// landing page and /healthz are unaffected.
//
// # Source IP keying
//
// The middleware deliberately bypasses gin's c.ClientIP(): gin's default
// TrustedProxies is [0.0.0.0/0] (it walks ALL X-Forwarded-For values),
// which makes it spoofable by any caller setting that header. imagelet's
// server.New does NOT call SetTrustedProxies, so the permissive default
// is in force. This middleware reads CF-Connecting-IP first (Cloudflare
// strips it from inbound requests before adding its own — unspoofable
// when fronted) and falls back to RemoteAddr's host portion (the
// unspoofable TCP peer when deployed bare). X-Forwarded-For is
// deliberately ignored. See clientIP for details and DESIGN.md §6.
//
// # Block response shape
//
// When a per-IP bucket is exhausted, the middleware does NOT emit a
// bare 429 — it short-circuits to a caller-supplied gin.HandlerFunc
// (typically github.RateLimitedHandler) so the body, status, and
// headers match the upstream-rate-limited path: status 200,
// Cache-Control: public, max-age=60, deterministic banner body.
// CDN / unfurl-bot caches honour the max-age=60 header (RFC 7234
// doesn't list 429 as cacheable by default), capping how often
// throttled clients re-hit the origin.
package iplimit

import (
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

// cfConnectingIPHeader is the trusted-proxy header Cloudflare sets when
// fronting imagelet. Mirrors RegionDetector's CF-IPCountry contract:
// trust the header when present, else fall back.
const cfConnectingIPHeader = "CF-Connecting-IP"

// DefaultCleanup is how stale a bucket can be before the opportunistic
// GC drops it. 5 min is well past the per-IP window (30 req/min/IP)
// so an active client never gets evicted mid-burst.
const DefaultCleanup = 5 * time.Minute

// bucket is one client's token-bucket state. Wraps x/time/rate.Limiter
// so we get tested-elsewhere refill maths instead of hand-rolling.
type bucket struct {
	lim      *rate.Limiter
	lastSeen time.Time
}

// Limiter holds a token bucket per IP. Stale buckets (older than
// cleanup) are GC'd on each allow call so memory stays bounded under
// abusive cardinality. An unbounded map is a memory-DoS vector — the
// opportunistic prune defends against it without requiring a separate
// goroutine.
type Limiter struct {
	rateLimit rate.Limit    // tokens per second
	burst     int           // bucket capacity
	cleanup   time.Duration // how stale before a bucket is GC'd
	now       func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
}

// New returns a Limiter configured for perMinute requests/min/IP with
// the given burst size. The cleanup TTL defaults to DefaultCleanup.
//
// Production wiring: New(30, 30) yields the 30 req/min/IP target
// documented in PLAN.md R10 and DESIGN.md §6.
func New(perMinute, burst int) *Limiter {
	return &Limiter{
		rateLimit: rate.Limit(float64(perMinute) / 60.0),
		burst:     burst,
		cleanup:   DefaultCleanup,
		now:       time.Now,
		buckets:   make(map[string]*bucket),
	}
}

// New30PerMin returns a Limiter at 30 requests/min/IP with a burst of 30.
// Convenience constructor matching the DESIGN.md §6 default; equivalent
// to New(30, 30).
func New30PerMin() *Limiter {
	return New(30, 30)
}

// Middleware returns a gin.HandlerFunc that, when the per-IP bucket is
// empty, short-circuits to the caller-supplied rateLimitedHandler
// (typically github.RateLimitedHandler). The two paths — per-IP block
// and upstream rate-limit — are deliberately indistinguishable to the
// caller; only an internal log line distinguishes them.
//
// rateLimitedHandler is dependency-injected so middleware/iplimit stays
// free of a circular import on service/github. cmd/imagelet wires the
// concrete handler at startup time (see cmd/imagelet/main.go).
func (l *Limiter) Middleware(rateLimitedHandler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := clientIP(c)
		if !l.allow(ip) {
			rateLimitedHandler(c)
			c.Abort()
			return
		}
		c.Next()
	}
}

// clientIP picks the source IP for rate-limit keying. Order:
//
//  1. CF-Connecting-IP (trim) when present and non-empty
//  2. RemoteAddr (host portion) — verbatim, NOT c.ClientIP()
//
// We deliberately bypass gin's c.ClientIP(): gin's default
// TrustedProxies is [0.0.0.0/0] (it walks ALL X-Forwarded-For
// values), which makes it spoofable by any caller setting the header.
// imagelet's server.New does NOT call SetTrustedProxies, so this
// permissive default is in force. CF-Connecting-IP is the trusted
// header (Cloudflare strips it from inbound requests before adding
// its own); RemoteAddr is the unspoofable TCP peer. Together they
// cover the deployed-behind-Cloudflare case AND the deployed-bare
// case without trusting an attacker-supplied X-Forwarded-For.
//
// Returns "" when neither resolves (e.g. a unix-socket peer); allow()
// treats "" as "always allow" so health-probe-shaped requests don't
// trip the limit.
func clientIP(c *gin.Context) string {
	if v := strings.TrimSpace(c.GetHeader(cfConnectingIPHeader)); v != "" {
		return v
	}
	if c.Request == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		return c.Request.RemoteAddr
	}
	return host
}

// allow consumes one token from ip's bucket. New IPs get a fresh
// bucket at full burst. Stale buckets (>cleanup since last seen) are
// dropped opportunistically during the lookup so map growth stays
// bounded under abusive cardinality. Returning true means the caller
// should proceed; false means the bucket is empty.
//
// allow always returns true for the empty-string IP. That covers the
// edge case where clientIP can't resolve anything (unix-socket peer,
// test-injected blank); we'd rather let those through than block a
// health probe of unknown shape.
func (l *Limiter) allow(ip string) bool {
	if ip == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	// GC stale buckets opportunistically — cheap because we already hold the lock.
	for k, b := range l.buckets {
		if now.Sub(b.lastSeen) > l.cleanup {
			delete(l.buckets, k)
		}
	}
	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(l.rateLimit, l.burst)}
		l.buckets[ip] = b
	}
	b.lastSeen = now
	return b.lim.AllowN(now, 1)
}
