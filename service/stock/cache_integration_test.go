package stock_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/internal/htmlcache"
	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/service/stock"
	"github.com/cmj0121/imagelet/service/stock/quote"
)

// countingProvider is a quote.Provider that returns a canned Quote
// and increments a call counter on every Get. Tests assert against
// the counter to prove cache hits actually skipped the handler.
type countingProvider struct {
	q     quote.Quote
	calls int
}

func (p *countingProvider) Get(_ context.Context, _ string) (quote.Quote, error) {
	p.calls++
	return p.q, nil
}

// TestStockHTMLResponseCachesOnSecondRequest is the wiring-level
// integration test for the htmlcache middleware sitting in front of
// the /stock handler. It confirms that the two pieces compose
// correctly end-to-end:
//
//   - First request → MISS, handler runs, browser gets HTML.
//   - Second identical request → HIT, X-Cache: HIT marker, handler
//     does NOT run again (asserted via the provider's call counter).
//
// Tests in internal/htmlcache prove the middleware behaves correctly
// against a synthetic handler; this test proves the real /stock
// handler's response (with its actual Cache-Control: max-age=N
// header) flows through the middleware as expected.
func TestStockHTMLResponseCachesOnSecondRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := &countingProvider{q: freshQuote()}

	r := gin.New()
	r.Use(middleware.TimezoneDetector())
	r.Use(middleware.DateOverrideDetector())
	r.Use(middleware.RegionDetector())
	r.Use(middleware.ClientDetector())
	r.Use(htmlcache.New().Middleware())
	stock.Register(r, p, stock.NoopTWSE())

	send := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/stock", nil)
		req.Header.Set("User-Agent", "Mozilla/5.0")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w1 := send()
	if w1.Code != 200 {
		t.Fatalf("first request status=%d, want 200", w1.Code)
	}
	if w1.Header().Get("X-Cache") == "HIT" {
		t.Errorf("first request marked X-Cache: HIT (should be MISS)")
	}
	if got := w1.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("first response Cache-Control = %q, want public, max-age=60", got)
	}

	w2 := send()
	if w2.Code != 200 {
		t.Fatalf("second request status=%d, want 200", w2.Code)
	}
	if got := w2.Header().Get("X-Cache"); got != "HIT" {
		t.Errorf("second request X-Cache = %q, want HIT", got)
	}
	if w2.Body.String() != w1.Body.String() {
		t.Errorf("cached body diverges from first body")
	}
	if p.calls != 1 {
		t.Errorf("provider called %d times, want 1 (cache hit must skip handler)", p.calls)
	}
}

// TestStockSVGResponseDoesNotCache asserts that non-html variants
// flow through the middleware untouched. ?format=svg returns
// image/svg+xml — the middleware should leave it alone, the handler
// runs every time, no X-Cache header.
func TestStockSVGResponseDoesNotCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	p := &countingProvider{q: freshQuote()}

	r := gin.New()
	r.Use(middleware.TimezoneDetector())
	r.Use(middleware.DateOverrideDetector())
	r.Use(middleware.RegionDetector())
	r.Use(middleware.ClientDetector())
	r.Use(htmlcache.New().Middleware())
	stock.Register(r, p, stock.NoopTWSE())

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/stock?format=svg", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Header().Get("X-Cache") == "HIT" {
			t.Errorf("iter %d: SVG marked X-Cache HIT (middleware should bypass non-html)", i)
		}
	}
	if p.calls != 2 {
		t.Errorf("provider called %d times, want 2 (svg path must not cache)", p.calls)
	}
}
