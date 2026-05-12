package reqcounter_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware/reqcounter"
)

func newTestRouter(ctr *reqcounter.Counter) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ctr.Middleware())
	r.GET("/foo", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/bar", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

// TestMiddlewareCountsRoutes makes 3 GET requests to /foo and 1 to /bar
// and verifies Snapshot returns [{"/foo",3},{"/bar",1}].
func TestMiddlewareCountsRoutes(t *testing.T) {
	ctr := reqcounter.New()
	r := newTestRouter(ctr)

	for range 3 {
		req := httptest.NewRequest(http.MethodGet, "/foo", nil)
		r.ServeHTTP(httptest.NewRecorder(), req)
	}
	req := httptest.NewRequest(http.MethodGet, "/bar", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	snap := ctr.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("Snapshot() len = %d, want 2; got %+v", len(snap), snap)
	}
	if snap[0].Route != "/foo" || snap[0].Count != 3 {
		t.Errorf("snap[0] = %+v, want {Route:\"/foo\" Count:3}", snap[0])
	}
	if snap[1].Route != "/bar" || snap[1].Count != 1 {
		t.Errorf("snap[1] = %+v, want {Route:\"/bar\" Count:1}", snap[1])
	}
}

// TestMiddlewareNoRoute verifies that a request to an unregistered path is
// counted under the sentinel key "no-route".
func TestMiddlewareNoRoute(t *testing.T) {
	ctr := reqcounter.New()
	r := newTestRouter(ctr)

	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	snap := ctr.Snapshot()
	if len(snap) == 0 {
		t.Fatal("Snapshot() is empty, want entry for \"no-route\"")
	}
	found := false
	for _, rc := range snap {
		if rc.Route == "no-route" && rc.Count == 1 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no \"no-route\" entry with count 1 in snapshot: %+v", snap)
	}
}

// TestSnapshotSortOrder verifies that Snapshot returns routes sorted by Count
// descending (ties break lexicographically ascending by route name).
func TestSnapshotSortOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctr := reqcounter.New()

	r := gin.New()
	r.Use(ctr.Middleware())
	r.GET("/a", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/b", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/c", func(c *gin.Context) { c.Status(http.StatusOK) })

	// Hit /c 5 times, /a 3 times, /b 3 times.
	for range 5 {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/c", nil))
	}
	for range 3 {
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/a", nil))
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/b", nil))
	}

	snap := ctr.Snapshot()
	if len(snap) != 3 {
		t.Fatalf("Snapshot() len = %d, want 3; got %+v", len(snap), snap)
	}
	// /c must be first (highest count)
	if snap[0].Route != "/c" || snap[0].Count != 5 {
		t.Errorf("snap[0] = %+v, want {/c 5}", snap[0])
	}
	// /a and /b both have 3 — tie broken by lex: /a before /b
	if snap[1].Route != "/a" || snap[1].Count != 3 {
		t.Errorf("snap[1] = %+v, want {/a 3}", snap[1])
	}
	if snap[2].Route != "/b" || snap[2].Count != 3 {
		t.Errorf("snap[2] = %+v, want {/b 3}", snap[2])
	}
}

// TestUptimeIncreases verifies that Uptime() returns a positive duration after
// a short sleep following New().
func TestUptimeIncreases(t *testing.T) {
	ctr := reqcounter.New()
	time.Sleep(10 * time.Millisecond)
	if d := ctr.Uptime(); d <= 0 {
		t.Errorf("Uptime() = %v, want > 0", d)
	}
}
