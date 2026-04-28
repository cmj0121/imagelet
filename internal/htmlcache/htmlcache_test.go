package htmlcache_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmj0121/imagelet/internal/htmlcache"
	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// newRouter builds a tiny gin engine wrapping handler with a fresh
// htmlcache. The cache is configured with a fixed clock so tests can
// step time deterministically. Returns the engine, cache, and a
// counter the handler increments per invocation — checking the
// counter is how tests prove cache hits skipped the handler.
func newRouter(t *testing.T, handler gin.HandlerFunc, capacity int) (*gin.Engine, *htmlcache.Cache, *int64) {
	t.Helper()
	c := htmlcache.NewWithCapacity(capacity)
	r := gin.New()
	r.GET("/x", c.Middleware(), handler)
	r.GET("/x/:id", c.Middleware(), handler)
	var calls int64
	wrapped := func(g *gin.Context) {
		atomic.AddInt64(&calls, 1)
		handler(g)
	}
	r2 := gin.New()
	r2.GET("/x", c.Middleware(), wrapped)
	r2.GET("/x/:id", c.Middleware(), wrapped)
	return r2, c, &calls
}

// htmlHandler emits a deterministic text/html body with the supplied
// Cache-Control. The body embeds the request URL so tests can detect
// stale-response cross-contamination across keys.
func htmlHandler(cacheControl string) gin.HandlerFunc {
	return func(g *gin.Context) {
		g.Header("Cache-Control", cacheControl)
		g.Data(http.StatusOK, "text/html; charset=utf-8",
			[]byte("<html>"+g.Request.URL.String()+"</html>"))
	}
}

func mustGet(t *testing.T, r *gin.Engine, url string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCacheHitOnSecondRequest(t *testing.T) {
	r, _, calls := newRouter(t, htmlHandler("public, max-age=60"), 8)

	w1 := mustGet(t, r, "/x")
	if w1.Code != 200 || !strings.Contains(w1.Body.String(), "<html>") {
		t.Fatalf("first request body=%q code=%d", w1.Body.String(), w1.Code)
	}
	if w1.Header().Get("X-Cache") == "HIT" {
		t.Errorf("first request marked HIT")
	}

	w2 := mustGet(t, r, "/x")
	if w2.Code != 200 || w2.Body.String() != w1.Body.String() {
		t.Fatalf("second request body mismatch: got %q want %q", w2.Body.String(), w1.Body.String())
	}
	if got := w2.Header().Get("X-Cache"); got != "HIT" {
		t.Errorf("X-Cache = %q, want HIT", got)
	}
	if *calls != 1 {
		t.Errorf("handler invoked %d times, want 1", *calls)
	}
}

func TestCacheBypassOnNoStore(t *testing.T) {
	r, _, calls := newRouter(t, htmlHandler("no-store"), 8)
	mustGet(t, r, "/x")
	mustGet(t, r, "/x")
	if *calls != 2 {
		t.Errorf("handler invoked %d times, want 2 (no-store should bypass)", *calls)
	}
}

func TestCacheBypassOnNoCache(t *testing.T) {
	r, _, calls := newRouter(t, htmlHandler("no-cache, max-age=60"), 8)
	mustGet(t, r, "/x")
	mustGet(t, r, "/x")
	if *calls != 2 {
		t.Errorf("handler invoked %d times, want 2 (no-cache should bypass)", *calls)
	}
}

func TestCacheBypassOnPrivate(t *testing.T) {
	r, _, calls := newRouter(t, htmlHandler("private, max-age=60"), 8)
	mustGet(t, r, "/x")
	mustGet(t, r, "/x")
	if *calls != 2 {
		t.Errorf("handler invoked %d times, want 2 (private should bypass)", *calls)
	}
}

func TestCacheBypassOnMissingCacheControl(t *testing.T) {
	r, _, calls := newRouter(t, htmlHandler(""), 8)
	mustGet(t, r, "/x")
	mustGet(t, r, "/x")
	if *calls != 2 {
		t.Errorf("handler invoked %d times, want 2 (no Cache-Control should bypass)", *calls)
	}
}

func TestCacheBypassOnNonHTMLContentType(t *testing.T) {
	called := int64(0)
	c := htmlcache.NewWithCapacity(8)
	r := gin.New()
	r.GET("/x", c.Middleware(), func(g *gin.Context) {
		atomic.AddInt64(&called, 1)
		g.Header("Cache-Control", "max-age=60")
		g.Data(http.StatusOK, "image/svg+xml", []byte("<svg/>"))
	})
	mustGet(t, r, "/x")
	mustGet(t, r, "/x")
	if called != 2 {
		t.Errorf("handler invoked %d times, want 2 (non-html should bypass)", called)
	}
}

func TestCacheBypassOnNon2xx(t *testing.T) {
	called := int64(0)
	c := htmlcache.NewWithCapacity(8)
	r := gin.New()
	r.GET("/x", c.Middleware(), func(g *gin.Context) {
		atomic.AddInt64(&called, 1)
		g.Header("Cache-Control", "max-age=60")
		g.Data(http.StatusInternalServerError, "text/html", []byte("oops"))
	})
	mustGet(t, r, "/x")
	mustGet(t, r, "/x")
	if called != 2 {
		t.Errorf("handler invoked %d times, want 2 (5xx should bypass)", called)
	}
}

func TestCacheExpiryDropsAfterTTL(t *testing.T) {
	r, cache, calls := newRouter(t, htmlHandler("public, max-age=60"), 8)
	t0 := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	cur := t0
	cache.SetNow(func() time.Time { return cur })

	mustGet(t, r, "/x")
	cur = t0.Add(59 * time.Second)
	mustGet(t, r, "/x")
	if *calls != 1 {
		t.Errorf("at t=59s, calls=%d want 1", *calls)
	}
	cur = t0.Add(61 * time.Second)
	mustGet(t, r, "/x")
	if *calls != 2 {
		t.Errorf("at t=61s, calls=%d want 2 (entry should have expired)", *calls)
	}
}

func TestCacheKeyDistinguishesQuery(t *testing.T) {
	r, _, calls := newRouter(t, htmlHandler("public, max-age=60"), 8)
	mustGet(t, r, "/x?a=1")
	mustGet(t, r, "/x?a=2")
	if *calls != 2 {
		t.Errorf("calls=%d, want 2 (different queries should not collide)", *calls)
	}
}

func TestCacheKeyCanonicalizesQueryOrder(t *testing.T) {
	r, _, calls := newRouter(t, htmlHandler("public, max-age=60"), 8)
	mustGet(t, r, "/x?a=1&b=2")
	mustGet(t, r, "/x?b=2&a=1")
	if *calls != 1 {
		t.Errorf("calls=%d, want 1 (reordered queries should hit)", *calls)
	}
}

func TestCacheKeyDistinguishesPath(t *testing.T) {
	r, _, calls := newRouter(t, htmlHandler("public, max-age=60"), 8)
	mustGet(t, r, "/x/2330")
	mustGet(t, r, "/x/2317")
	if *calls != 2 {
		t.Errorf("calls=%d, want 2 (different :id values should not collide)", *calls)
	}
}

func TestCacheKeyDistinguishesUserAgent(t *testing.T) {
	c := htmlcache.NewWithCapacity(8)
	calls := int64(0)
	r := gin.New()
	r.GET("/x", c.Middleware(), func(g *gin.Context) {
		atomic.AddInt64(&calls, 1)
		g.Header("Cache-Control", "max-age=60")
		g.Data(http.StatusOK, "text/html", []byte("<html/>"))
	})

	send := func(ua string) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("User-Agent", ua)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
	send("Mozilla/5.0")
	send("Mozilla/5.0") // 2nd Mozilla → cache hit
	send("curl/8.0")    // different UA class → cache miss
	if calls != 2 {
		t.Errorf("calls=%d, want 2 (one Mozilla render + one curl render)", calls)
	}
}

func TestCacheLRUEvictsOldest(t *testing.T) {
	r, cache, _ := newRouter(t, htmlHandler("public, max-age=60"), 2)
	mustGet(t, r, "/x?n=1")
	mustGet(t, r, "/x?n=2")
	if cache.Len() != 2 {
		t.Errorf("len=%d, want 2", cache.Len())
	}
	mustGet(t, r, "/x?n=3") // forces eviction of n=1
	if cache.Len() != 2 {
		t.Errorf("len=%d, want 2 after eviction", cache.Len())
	}
	// n=1 should now be a miss again
	mustGet(t, r, "/x?n=1")
	w := mustGet(t, r, "/x?n=1") // second n=1 should be a hit again
	if w.Header().Get("X-Cache") != "HIT" {
		t.Errorf("re-fetched n=1 not a HIT after eviction+repopulation")
	}
}

func TestCacheNonGETPassesThrough(t *testing.T) {
	c := htmlcache.NewWithCapacity(8)
	calls := int64(0)
	r := gin.New()
	r.POST("/x", c.Middleware(), func(g *gin.Context) {
		atomic.AddInt64(&calls, 1)
		g.Header("Cache-Control", "max-age=60")
		g.Data(http.StatusOK, "text/html", []byte("<html/>"))
	})
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
	if calls != 3 {
		t.Errorf("POST invoked %d times, want 3 (cache must skip mutating verbs)", calls)
	}
}

// TestCacheStampedeCollapsesToSingleHandlerCall asserts that N
// concurrent cold-cache requests for the same key trigger only ONE
// downstream handler call — the singleflight gate. The handler holds
// for a moment to maximize contention so all N goroutines line up
// behind the leader before the first cache fill completes.
func TestCacheStampedeCollapsesToSingleHandlerCall(t *testing.T) {
	c := htmlcache.NewWithCapacity(8)
	calls := int64(0)
	gate := make(chan struct{})
	r := gin.New()
	r.GET("/x", c.Middleware(), func(g *gin.Context) {
		atomic.AddInt64(&calls, 1)
		<-gate // park the leader so waiters can pile up
		g.Header("Cache-Control", "max-age=60")
		g.Data(http.StatusOK, "text/html", []byte("<html/>"))
	})

	const N = 16
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			mustGet(t, r, "/x")
		}()
	}
	// Give all N goroutines time to enter the middleware and queue
	// behind the leader.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	if calls != 1 {
		t.Errorf("handler invoked %d times, want 1 (singleflight should collapse stampede)", calls)
	}
}
