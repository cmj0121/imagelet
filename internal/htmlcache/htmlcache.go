// Package htmlcache is an in-process gin middleware that caches text/html
// responses keyed by request signature. Cache TTL is read from the
// handler's Cache-Control: max-age response header — no per-route
// configuration needed. Standard Cache-Control directives are honored:
//
//   - max-age=N   → cache for N seconds
//   - no-store    → bypass (response not stored)
//   - no-cache    → bypass
//   - private     → bypass
//   - max-age=0   → bypass
//
// Only 2xx + text/html responses are cached. Non-text/html responses
// (image/svg+xml, image/png, text/plain ASCII, text/pylon) flow through
// untouched, so format-negotiated routes can share one middleware.
//
// Eviction is LRU bounded at the configured capacity; expired entries
// are dropped lazily on lookup. Singleflight serializes concurrent
// renders for the same key — N simultaneous cold-cache requests
// trigger ONE downstream handler, the rest wait on the in-flight
// render and serve from the now-warm cache.
package htmlcache

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// DefaultMaxEntries is the LRU cap used when New is called with 0.
// 256 covers hundreds of distinct symbols on /stock/:symbol without
// unbounded memory growth; bump explicitly via NewWithCapacity for
// hotter routes.
const DefaultMaxEntries = 256

// Cache is a bounded LRU+TTL store of captured text/html responses.
// Safe for concurrent use; one Cache backs one middleware instance.
//
// Each entry persists status code, a small set of replay-safe headers,
// and the response body. Headers we'd otherwise echo back unchanged
// (Date, ETag, transfer-coding) are left to gin's default handling on
// replay so they reflect the serving moment, not the cache-fill moment.
type Cache struct {
	mu     sync.Mutex
	list   *list.List               // *entry, front = MRU
	items  map[string]*list.Element // key -> list element
	flight map[string]chan struct{} // singleflight per key
	max    int
	nowFn  func() time.Time
}

// entry holds a single cached response. headers is a defensive copy —
// the original gin response writer's header map keeps mutating after
// we capture it, so we snapshot at cache-fill time.
type entry struct {
	key      string
	status   int
	headers  http.Header
	body     []byte
	deadline time.Time
}

// New returns a Cache with DefaultMaxEntries capacity and time.Now as
// the clock. Use NewWithCapacity to pick a different bound; tests can
// inject a clock via SetNow for deterministic expiry assertions.
func New() *Cache {
	return NewWithCapacity(DefaultMaxEntries)
}

// NewWithCapacity returns a Cache with the given LRU bound. A
// non-positive capacity falls back to DefaultMaxEntries — disabling
// the cache entirely is done by simply not registering the middleware.
func NewWithCapacity(max int) *Cache {
	if max <= 0 {
		max = DefaultMaxEntries
	}
	return &Cache{
		list:   list.New(),
		items:  make(map[string]*list.Element),
		flight: make(map[string]chan struct{}),
		max:    max,
		nowFn:  time.Now,
	}
}

// SetNow swaps the cache's clock. Tests use this to advance simulated
// time across hit / miss / expiry transitions without sleeping.
func (c *Cache) SetNow(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nowFn = now
}

// Len returns the number of live entries — primarily for tests
// asserting eviction. Includes entries that may be expired but not
// yet swept (lazy expiry).
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.list.Len()
}

// Middleware returns a gin.HandlerFunc that consults this cache for
// GET requests. Non-GET methods pass through untouched (caching
// mutating verbs would be incorrect).
//
// On hit: writes the cached status, headers, and body, sets X-Cache:
// HIT, and aborts the handler chain.
//
// On miss: claims a singleflight slot for the key, runs the next
// handler with a capturing writer, then conditionally stores the
// captured response.
func (c *Cache) Middleware() gin.HandlerFunc {
	return func(g *gin.Context) {
		if g.Request.Method != http.MethodGet {
			g.Next()
			return
		}
		key := cacheKey(g)
		now := c.nowFn()

		if e, ok := c.lookup(key, now); ok {
			serve(g, e)
			return
		}

		// Singleflight: at most one render per key in flight. Waiters
		// block on the leader's channel, then re-check the cache.
		ch, weAreLeader := c.acquireFlight(key)
		if !weAreLeader {
			<-ch
			if e, ok := c.lookup(key, c.nowFn()); ok {
				serve(g, e)
				return
			}
			// Leader's response wasn't cacheable; fall through and
			// render fresh in this goroutine. This is the rare path
			// (only triggers for non-2xx / non-html / no-store
			// responses on the very route configured to cache).
		}
		if weAreLeader {
			defer c.releaseFlight(key)
		}

		cw := &captureWriter{ResponseWriter: g.Writer, body: &bytes.Buffer{}}
		g.Writer = cw
		g.Next()

		// Restore the original writer so any code that runs after the
		// middleware (gin's recovery, etc.) writes to the real one.
		g.Writer = cw.ResponseWriter

		if !weAreLeader {
			return
		}
		ttl, ok := shouldStore(cw)
		if !ok {
			return
		}
		c.store(&entry{
			key:      key,
			status:   cw.statusCode,
			headers:  copyHeaders(cw.Header()),
			body:     append([]byte(nil), cw.body.Bytes()...),
			deadline: now.Add(ttl),
		})
	}
}

// lookup fetches a non-expired entry for key, promoting it to the MRU
// position. Expired entries are dropped lazily and reported as miss.
func (c *Cache) lookup(key string, now time.Time) (*entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*entry)
	if !now.Before(e.deadline) {
		c.list.Remove(el)
		delete(c.items, key)
		return nil, false
	}
	c.list.MoveToFront(el)
	return e, true
}

// store inserts or refreshes an entry, evicting the LRU tail when
// capacity is exceeded.
func (c *Cache) store(e *entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[e.key]; ok {
		el.Value = e
		c.list.MoveToFront(el)
		return
	}
	el := c.list.PushFront(e)
	c.items[e.key] = el
	for c.list.Len() > c.max {
		tail := c.list.Back()
		if tail == nil {
			break
		}
		c.list.Remove(tail)
		delete(c.items, tail.Value.(*entry).key)
	}
}

// acquireFlight returns the in-flight channel for key. If we're the
// leader (no existing channel), we create one and return weAreLeader=
// true; the caller must call releaseFlight when done. Waiters get
// the existing channel and weAreLeader=false.
func (c *Cache) acquireFlight(key string) (chan struct{}, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.flight[key]; ok {
		return ch, false
	}
	ch := make(chan struct{})
	c.flight[key] = ch
	return ch, true
}

// releaseFlight unblocks waiters and clears the in-flight slot.
func (c *Cache) releaseFlight(key string) {
	c.mu.Lock()
	ch, ok := c.flight[key]
	delete(c.flight, key)
	c.mu.Unlock()
	if ok {
		close(ch)
	}
}

// captureWriter shadows gin.ResponseWriter to record everything the
// downstream handler writes. It tees writes through to the underlying
// writer so the response still goes to the client unaltered, while
// also building a buffer + status snapshot for the cache.
type captureWriter struct {
	gin.ResponseWriter
	body       *bytes.Buffer
	statusCode int
	wroteOnce  bool
}

func (w *captureWriter) WriteHeader(status int) {
	if !w.wroteOnce {
		w.statusCode = status
		w.wroteOnce = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *captureWriter) Write(b []byte) (int, error) {
	if !w.wroteOnce {
		// gin.Context.Data calls Write before WriteHeader; capture the
		// implicit-200 path the same way net/http does.
		w.statusCode = http.StatusOK
		w.wroteOnce = true
	}
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *captureWriter) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

// shouldStore inspects the captured response and decides whether to
// cache it. Returns the chosen TTL on a positive decision.
//
// Gates (in order): status 2xx, Content-Type text/html, Cache-Control
// allows storage, max-age > 0. Any failure short-circuits to "don't
// cache" so non-html / error responses don't pollute the cache.
func shouldStore(cw *captureWriter) (time.Duration, bool) {
	status := cw.statusCode
	if status == 0 {
		status = http.StatusOK
	}
	if status < 200 || status >= 300 {
		return 0, false
	}
	if !strings.HasPrefix(cw.Header().Get("Content-Type"), "text/html") {
		return 0, false
	}
	cc := cw.Header().Get("Cache-Control")
	ttl, ok := parseMaxAge(cc)
	if !ok {
		return 0, false
	}
	return ttl, true
}

// parseMaxAge pulls a positive max-age out of a Cache-Control header.
// Returns (0, false) on no-store, no-cache, private, missing max-age,
// or max-age=0 — anything that signals "do not store this response".
func parseMaxAge(cc string) (time.Duration, bool) {
	if cc == "" {
		return 0, false
	}
	var maxAge time.Duration
	haveMaxAge := false
	for _, raw := range strings.Split(cc, ",") {
		tok := strings.TrimSpace(raw)
		low := strings.ToLower(tok)
		switch {
		case low == "no-store", low == "no-cache", low == "private":
			return 0, false
		case strings.HasPrefix(low, "max-age="):
			n, err := strconv.Atoi(strings.TrimPrefix(low, "max-age="))
			if err != nil || n <= 0 {
				return 0, false
			}
			maxAge = time.Duration(n) * time.Second
			haveMaxAge = true
		}
	}
	if !haveMaxAge {
		return 0, false
	}
	return maxAge, true
}

// serve writes a cached entry to the gin context. Adds X-Cache: HIT
// for observability — operators can grep access logs for cache
// behavior without instrumenting metrics.
func serve(g *gin.Context, e *entry) {
	dst := g.Writer.Header()
	for k, vs := range e.headers {
		dst[k] = append(dst[k][:0], vs...)
	}
	g.Writer.Header().Set("X-Cache", "HIT")
	g.Writer.WriteHeader(e.status)
	_, _ = g.Writer.Write(e.body)
	g.Abort()
}

// copyHeaders snapshots the headers we want to replay. Filters out
// hop-by-hop and connection-specific headers; cache-fill timestamps
// (Date, ETag) are intentionally omitted so a stale entry doesn't
// claim freshness it doesn't have.
func copyHeaders(src http.Header) http.Header {
	keep := map[string]struct{}{
		"Content-Type":  {},
		"Cache-Control": {},
		"Vary":          {},
		"Link":          {},
	}
	out := make(http.Header, len(keep))
	for k, vs := range src {
		if _, ok := keep[http.CanonicalHeaderKey(k)]; !ok {
			continue
		}
		dup := make([]string, len(vs))
		copy(dup, vs)
		out[http.CanonicalHeaderKey(k)] = dup
	}
	return out
}

// cacheKey derives the lookup key. Captures everything that affects
// the rendered HTML so two requests producing different bodies don't
// collide:
//
//   - method (GET only enters here; pinned for safety)
//   - path (incl. :symbol after gin's router fills it in)
//   - sorted query string (so ?a=1&b=2 == ?b=2&a=1)
//   - Accept header (content negotiation)
//   - User-Agent class — Mozilla vs other (the existing handlers map
//     Mozilla → HTML, so the bit suffices to separate browsers from
//     curl/programmatic callers without keying on full UA strings)
//
// The string is hashed at the end so the key fits in a map entry
// without per-request memory blow-up on long URLs.
func cacheKey(g *gin.Context) string {
	q := g.Request.URL.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString(g.Request.Method)
	sb.WriteByte(' ')
	sb.WriteString(g.Request.URL.Path)
	sb.WriteByte('?')
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte('&')
		}
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(strings.Join(q[k], ","))
	}
	sb.WriteByte('|')
	sb.WriteString(g.GetHeader("Accept"))
	sb.WriteByte('|')
	if isMozilla(g.GetHeader("User-Agent")) {
		sb.WriteString("ua=mozilla")
	} else {
		sb.WriteString("ua=other")
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:16])
}

// isMozilla mirrors the UA check that drives HTML mode in the
// handlers — anything starting with "Mozilla/" is treated as a
// browser. Case-insensitive prefix match because some bots forge
// odd casing.
func isMozilla(ua string) bool {
	if len(ua) < len("Mozilla/") {
		return false
	}
	return strings.EqualFold(ua[:len("Mozilla/")], "Mozilla/")
}
