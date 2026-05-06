package iplimit_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware/iplimit"
)

// rateLimitedBody is the body the test handler emits when the limiter
// short-circuits a request. Mirrors the shape of the github
// RateLimitedHandler — status 200, max-age=60, distinctive token in
// the body. Tests assert on these without importing service/github
// (which would create a test-time import cycle).
const rateLimitedBody = "RATE LIMITED: try again later"

// rateLimitedHandler is the test-injected dependency the middleware
// invokes when a bucket is empty. Real wiring passes
// github.RateLimitedHandler here.
func rateLimitedHandler(c *gin.Context) {
	c.Header("Cache-Control", "public, max-age=60")
	c.String(http.StatusOK, rateLimitedBody)
}

// newRouter builds a gin engine with iplimit.Middleware(rateLimitedHandler)
// wrapping a single GET /probe handler that returns 200 OK on success.
func newRouter(l *iplimit.Limiter) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(l.Middleware(rateLimitedHandler))
	r.GET("/probe", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	return r
}

// doFrom issues a GET /probe with the given RemoteAddr and headers.
// Setting RemoteAddr is the test analogue of "this peer connected from
// 1.2.3.4:5678" — the middleware's clientIP fallback path reads it.
func doFrom(t *testing.T, r *gin.Engine, remoteAddr string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestRateLimit_Allows asserts that the first request from a fresh IP
// passes — the bucket starts at full burst capacity.
func TestRateLimit_Allows(t *testing.T) {
	l := iplimit.New(30, 30)
	r := newRouter(l)

	rec := doFrom(t, r, "1.2.3.4:1000", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "ok" {
		t.Errorf("body = %q, want %q", got, "ok")
	}
}

// TestRateLimit_BlocksAfterLimit asserts that once the bucket is
// exhausted, the middleware short-circuits to the rate-limited handler
// (status 200, max-age=60, banner body) — NOT a bare 429. Per Fix 1
// and DESIGN.md §5.3 the locked decision is 200 so CDN / unfurl-bot
// caches honour the max-age=60 header.
func TestRateLimit_BlocksAfterLimit(t *testing.T) {
	l := iplimit.New(30, 30)
	r := newRouter(l)

	// Burn through the entire burst first — every request must succeed.
	for i := 0; i < 30; i++ {
		rec := doFrom(t, r, "1.2.3.4:1000", nil)
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Fatalf("request %d/30 unexpectedly blocked: status=%d body=%q", i+1, rec.Code, rec.Body.String())
		}
	}

	// 31st request: bucket empty → middleware short-circuits.
	rec := doFrom(t, r, "1.2.3.4:1000", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("blocked status = %d, want 200 (rate-limited returns 200, not 429, per DESIGN.md §5.3)", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("Cache-Control = %q, want public, max-age=60", got)
	}
	if !strings.Contains(rec.Body.String(), "RATE LIMITED") {
		t.Errorf("blocked body missing 'RATE LIMITED'\n--- body ---\n%s", rec.Body.String())
	}
}

// TestRateLimit_PerIP_Independent asserts that one IP exhausting its
// bucket does not affect a different IP — each peer gets its own
// bucket.
func TestRateLimit_PerIP_Independent(t *testing.T) {
	l := iplimit.New(30, 30)
	r := newRouter(l)

	// Exhaust IP A's bucket.
	for i := 0; i < 30; i++ {
		_ = doFrom(t, r, "10.0.0.1:1000", nil)
	}
	// Verify A is now blocked.
	rec := doFrom(t, r, "10.0.0.1:1000", nil)
	if !strings.Contains(rec.Body.String(), "RATE LIMITED") {
		t.Fatalf("IP A: expected RATE LIMITED body, got %q", rec.Body.String())
	}

	// IP B must still pass — independent bucket.
	rec = doFrom(t, r, "10.0.0.2:1000", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("IP B unexpectedly blocked: status=%d body=%q (per-IP buckets must be independent)", rec.Code, rec.Body.String())
	}
}

// TestRateLimit_CFConnectingIP asserts that when CF-Connecting-IP is
// set, the middleware keys the bucket by that IP — not by RemoteAddr.
// Two requests from the same RemoteAddr but different
// CF-Connecting-IPs share NO bucket state.
func TestRateLimit_CFConnectingIP(t *testing.T) {
	l := iplimit.New(30, 30)
	r := newRouter(l)

	// Same RemoteAddr, two different CF-Connecting-IP values. Burn
	// through IP A's full bucket via CF-Connecting-IP.
	for i := 0; i < 30; i++ {
		_ = doFrom(t, r, "127.0.0.1:1000", map[string]string{"CF-Connecting-IP": "1.2.3.4"})
	}
	// IP A is blocked.
	rec := doFrom(t, r, "127.0.0.1:1000", map[string]string{"CF-Connecting-IP": "1.2.3.4"})
	if !strings.Contains(rec.Body.String(), "RATE LIMITED") {
		t.Fatalf("CF-IP 1.2.3.4: expected RATE LIMITED, got %q", rec.Body.String())
	}

	// IP B (CF-Connecting-IP: 5.6.7.8, same RemoteAddr) must still
	// pass — proves the limiter keys by CF-Connecting-IP, not the
	// RemoteAddr.
	rec = doFrom(t, r, "127.0.0.1:1000", map[string]string{"CF-Connecting-IP": "5.6.7.8"})
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("CF-IP 5.6.7.8 unexpectedly blocked (limiter must key by CF-Connecting-IP, not RemoteAddr): status=%d body=%q", rec.Code, rec.Body.String())
	}
}

// TestRateLimit_RefillOverTime asserts that an injected clock advance
// past the per-token interval refills the bucket. Burns the entire
// burst, confirms a block, advances time by the per-token interval
// (1/rate), then asserts the next request passes.
func TestRateLimit_RefillOverTime(t *testing.T) {
	// 60 req/min = 1 req/s; burst 1 means each request must wait 1s.
	l := iplimit.New(60, 1)

	// Inject a controllable clock. Test-only setter via reflect would
	// be brittle; instead test against the underlying allow() through
	// repeated calls separated by real-time waits would be slow. Use
	// a low burst to keep the test fast: a single request consumes
	// the only token, the second blocks, sleep ~1.1s for refill,
	// third request must pass.
	r := newRouter(l)
	if rec := doFrom(t, r, "1.2.3.4:1000", nil); rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("first request unexpectedly blocked: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec := doFrom(t, r, "1.2.3.4:1000", nil); !strings.Contains(rec.Body.String(), "RATE LIMITED") {
		t.Fatalf("second request expected to be rate-limited: body=%q", rec.Body.String())
	}
	// Refill: at 1 req/s, 1.1s of wall time replenishes one token.
	time.Sleep(1100 * time.Millisecond)
	if rec := doFrom(t, r, "1.2.3.4:1000", nil); rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("third request after refill unexpectedly blocked: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

// TestRateLimit_EmptyIPAlwaysAllows asserts that when clientIP returns
// "" (no CF-Connecting-IP, RemoteAddr empty), the middleware passes
// the request through. Health probes from unix sockets or odd
// transports must not trip the limiter.
func TestRateLimit_EmptyIPAlwaysAllows(t *testing.T) {
	l := iplimit.New(1, 1) // burst 1: ANY second request would normally block
	r := newRouter(l)

	// RemoteAddr empty → clientIP returns ""; allow() returns true
	// unconditionally for "". Issue 5 such requests in a row: all
	// must pass.
	for i := 0; i < 5; i++ {
		rec := doFrom(t, r, "", nil)
		if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
			t.Errorf("empty-IP request %d/5 unexpectedly blocked: status=%d body=%q", i+1, rec.Code, rec.Body.String())
		}
	}
}

// TestRateLimit_RemoteAddrFallback asserts that without
// CF-Connecting-IP, two distinct RemoteAddr peers each get their own
// bucket — proves the fallback path keys by RemoteAddr's host
// portion, not the full host:port string.
func TestRateLimit_RemoteAddrFallback(t *testing.T) {
	l := iplimit.New(30, 30)
	r := newRouter(l)

	// Burn IP A.
	for i := 0; i < 30; i++ {
		_ = doFrom(t, r, "203.0.113.10:1000", nil)
	}
	rec := doFrom(t, r, "203.0.113.10:1000", nil)
	if !strings.Contains(rec.Body.String(), "RATE LIMITED") {
		t.Fatalf("RemoteAddr A: expected RATE LIMITED, got %q", rec.Body.String())
	}

	// IP B (different host portion) must pass.
	rec = doFrom(t, r, "203.0.113.11:1000", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("RemoteAddr B unexpectedly blocked: status=%d body=%q", rec.Code, rec.Body.String())
	}

	// Same host portion, different port — must SHARE bucket A. Port
	// is not part of the key (a single client typically holds many
	// ports open behind a NAT).
	rec = doFrom(t, r, "203.0.113.10:2000", nil)
	if !strings.Contains(rec.Body.String(), "RATE LIMITED") {
		t.Errorf("same host different port: expected to share bucket with IP A (got status=%d body=%q)", rec.Code, rec.Body.String())
	}
}

// TestRateLimit_XFFNotTrusted asserts that X-Forwarded-For is ignored
// by the middleware — the gin-default-trustedproxies spoof MUST NOT
// affect bucket keying. With no CF-Connecting-IP and an
// attacker-supplied XFF, the limiter falls through to RemoteAddr.
//
// Construction: send 30 requests with RemoteAddr=A and
// X-Forwarded-For=B. If the middleware (incorrectly) honoured XFF,
// it would key by B, exhausting B's bucket. We then send a 31st
// request with RemoteAddr=A and a DIFFERENT XFF=C. A correctly-
// implemented limiter keyed only by RemoteAddr would block (A's
// bucket is exhausted regardless of XFF). A buggy implementation
// keyed by XFF would pass (C's bucket is fresh). The test asserts
// the BLOCK — proving XFF is not trusted.
func TestRateLimit_XFFNotTrusted(t *testing.T) {
	l := iplimit.New(30, 30)
	r := newRouter(l)

	for i := 0; i < 30; i++ {
		_ = doFrom(t, r, "198.51.100.5:1000", map[string]string{"X-Forwarded-For": "1.1.1.1"})
	}

	// 31st request from same RemoteAddr but a different XFF. If the
	// middleware (incorrectly) trusted XFF, this would pass — fresh
	// bucket for "2.2.2.2". A correct implementation falls through
	// to RemoteAddr (already exhausted) and blocks.
	rec := doFrom(t, r, "198.51.100.5:1000", map[string]string{"X-Forwarded-For": "2.2.2.2"})
	if !strings.Contains(rec.Body.String(), "RATE LIMITED") {
		t.Errorf("XFF must not be trusted: bucket should still be exhausted via RemoteAddr (got status=%d body=%q)", rec.Code, rec.Body.String())
	}
}

// TestRateLimit_Concurrent_PerIPBurst asserts that under concurrent
// load from a single IP, exactly burst requests succeed and the rest
// are rate-limited. Documents that the limiter is goroutine-safe.
func TestRateLimit_Concurrent_PerIPBurst(t *testing.T) {
	l := iplimit.New(30, 30)
	r := newRouter(l)

	const concurrent = 100
	var wg sync.WaitGroup
	wg.Add(concurrent)

	var muOK, muBlocked sync.Mutex
	var ok, blocked int

	for i := 0; i < concurrent; i++ {
		go func() {
			defer wg.Done()
			rec := doFrom(t, r, "1.2.3.4:1000", nil)
			if rec.Body.String() == "ok" {
				muOK.Lock()
				ok++
				muOK.Unlock()
			} else if strings.Contains(rec.Body.String(), "RATE LIMITED") {
				muBlocked.Lock()
				blocked++
				muBlocked.Unlock()
			}
		}()
	}
	wg.Wait()

	// At wall-clock zero, exactly `burst` (30) tokens are available;
	// rate.Limiter may grant 1 extra during the test window if
	// scheduling spans >2s — guard with a small tolerance.
	if ok < 30 || ok > 32 {
		t.Errorf("ok = %d, want approximately 30 (burst)", ok)
	}
	if ok+blocked != concurrent {
		t.Errorf("ok+blocked = %d, want %d (no requests lost)", ok+blocked, concurrent)
	}
}
