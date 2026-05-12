// Package reqcounter provides a gin middleware that counts requests per route
// pattern. The counter is safe for concurrent use and designed to be installed
// as the first middleware in the chain (before htmlcache) so both cache-hit
// and cache-miss requests are counted.
package reqcounter

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// Counter tracks per-route-pattern request counts since the Counter was
// created. It is safe for concurrent use; all field access goes through
// sync.Map and atomic operations.
type Counter struct {
	started time.Time
	counts  sync.Map // key: string (route pattern) → value: *atomic.Int64
}

// New allocates and returns a Counter with started set to the current time.
func New() *Counter {
	return &Counter{started: time.Now()}
}

// Middleware returns a gin.HandlerFunc that increments the per-route counter
// for every request. It calls c.Next() first so that the handler runs and
// sets c.FullPath(); after Next returns the full path is read and the
// matching counter is incremented. Requests that do not match any registered
// route (c.FullPath() == "") are counted under the sentinel key "no-route".
func (ctr *Counter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		key := c.FullPath()
		if key == "" {
			key = "no-route"
		}

		v, _ := ctr.counts.LoadOrStore(key, &atomic.Int64{})
		v.(*atomic.Int64).Add(1)
	}
}

// RouteCount is a single entry in a Snapshot: one route pattern and its
// total request count since startup.
type RouteCount struct {
	Route string
	Count int64
}

// Snapshot returns a copy of all per-route counts sorted by Count descending.
// Ties are broken by lexicographic route ascending so the output is stable
// across multiple calls with the same data.
func (ctr *Counter) Snapshot() []RouteCount {
	var out []RouteCount
	ctr.counts.Range(func(k, v any) bool {
		out = append(out, RouteCount{
			Route: k.(string),
			Count: v.(*atomic.Int64).Load(),
		})
		return true
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Route < out[j].Route
	})
	return out
}

// Uptime returns the duration elapsed since the Counter was created.
func (ctr *Counter) Uptime() time.Duration {
	return time.Since(ctr.started)
}
