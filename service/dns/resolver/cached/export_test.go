package cached

import "time"

// SetNow swaps the cache's clock function. Test-only — exposed via
// _test.go so production code never sees it.
func SetNow(c *Cached, now func() time.Time) {
	c.now = now
}
