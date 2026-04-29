package middleware

// RequestSizeLimiter caps the three attacker-controlled inputs on every
// HTTP request — URL path, query string, and body — so abusive callers
// cannot inflate per-request memory or downstream cache keys before
// reaching the handler chain.
//
// imagelet is GET-only today, so the realistic attack vector is the
// URL path + query string: internal/htmlcache (mounted in cmd/imagelet)
// derives its response cache key from the full query, so a multi-MB
// query inflates both the request allocation and the cache entry. The
// path / query caps fail oversized requests with HTTP 414 (URI Too
// Long) before any downstream handler reads c.Query or c.Request.URL.
// The body cap is defense-in-depth against a future POST endpoint
// inheriting an unbounded reader.
//
// The constants below are sized for imagelet's flat route set; bump
// them if the route surface grows enough to need it. RequestSizeLimiter
// must be installed early in the chain — see server.New for the wiring.

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// MaxRequestPathBytes caps the length of the request URL path.
// 1 KiB is generous for imagelet's flat route set yet tight enough
// to refuse abusive paths well before the cache-key construction
// in internal/htmlcache.
const MaxRequestPathBytes = 1 << 10

// MaxRequestQueryBytes caps the length of the request query string.
// 4 KiB tolerates the longest legitimate query (multi-symbol /stock
// requests, multi-key debug overrides) while bounding the htmlcache
// key derived from it.
const MaxRequestQueryBytes = 4 << 10

// MaxRequestBodyBytes caps the request body size. imagelet is a
// GET-only service today so bodies are normally empty; this is
// defense-in-depth against future endpoints accidentally inheriting
// an unbounded reader.
const MaxRequestBodyBytes = 64 << 10

// RequestSizeLimiter rejects requests with oversized URL path or
// query string with HTTP 414 (URI Too Long) and wraps the body in
// http.MaxBytesReader so handlers reading c.Request.Body see a
// bounded reader.
//
// MUST run early in the chain — before any handler reads c.Query
// or c.Request.URL.Path — so rejection lands before downstream
// allocates cache keys from attacker-supplied values.
func RequestSizeLimiter() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request != nil && c.Request.URL != nil {
			if len(c.Request.URL.Path) > MaxRequestPathBytes {
				c.AbortWithStatus(http.StatusRequestURITooLong)
				return
			}
			if len(c.Request.URL.RawQuery) > MaxRequestQueryBytes {
				c.AbortWithStatus(http.StatusRequestURITooLong)
				return
			}
		}
		if c.Request != nil && c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxRequestBodyBytes)
		}
		c.Next()
	}
}
