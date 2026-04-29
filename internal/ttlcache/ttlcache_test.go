package ttlcache

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCacheGetSet(t *testing.T) {
	c := New[string, int](16)
	c.Set("k", Entry[int]{Value: 42, Found: true, ExpiresAt: time.Now().Add(time.Hour)})

	e, ok := c.Get("k")
	if !ok {
		t.Fatal("Get after Set returned !ok")
	}
	if e.Value != 42 || !e.Found {
		t.Errorf("entry = %+v, want Value=42 Found=true", e)
	}
}

func TestCacheExpiry(t *testing.T) {
	c := New[string, int](16)
	now := time.Now()
	c.SetClock(func() time.Time { return now })
	c.Set("k", Entry[int]{Value: 1, Found: true, ExpiresAt: now.Add(time.Second)})

	if _, ok := c.Get("k"); !ok {
		t.Fatal("entry should be live")
	}

	now = now.Add(2 * time.Second)
	if _, ok := c.Get("k"); ok {
		t.Errorf("entry should be expired (ExpiresAt < now)")
	}
}

// TestCacheNegativeCaching pins the Found=false sentinel: callers can
// remember that an upstream returned no data for a key, and the cache
// will hand that fact back without re-hitting fetch on the next call.
func TestCacheNegativeCaching(t *testing.T) {
	c := New[string, string](16)
	calls := int32(0)
	fetch := func() (string, bool, error) {
		atomic.AddInt32(&calls, 1)
		return "", false, nil // negative — upstream said no data
	}

	for i := 0; i < 3; i++ {
		v, found, err := c.GetOrFetch("k", time.Hour, fetch)
		if err != nil {
			t.Fatalf("call %d: err=%v", i, err)
		}
		if v != "" || found {
			t.Errorf("call %d: got (%q, %v), want negative-cache hit", i, v, found)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("fetch invoked %d times, want 1 (rest should be cache hits)", got)
	}
}

// TestCacheLRUEviction pins the hard size cap: once cap is exceeded the
// oldest unused entry is dropped so per-key memory can't grow unbounded
// for ticker-heavy traffic.
func TestCacheLRUEviction(t *testing.T) {
	c := New[int, int](2)
	exp := time.Now().Add(time.Hour)
	c.Set(1, Entry[int]{Value: 1, Found: true, ExpiresAt: exp})
	c.Set(2, Entry[int]{Value: 2, Found: true, ExpiresAt: exp})
	c.Set(3, Entry[int]{Value: 3, Found: true, ExpiresAt: exp})

	if _, ok := c.Get(1); ok {
		t.Errorf("entry 1 should have been evicted (oldest)")
	}
	if _, ok := c.Get(2); !ok {
		t.Errorf("entry 2 should survive")
	}
	if _, ok := c.Get(3); !ok {
		t.Errorf("entry 3 should survive")
	}
}

// TestCacheSingleflight pins the stampede-control invariant: N concurrent
// callers asking for the same missing key produce exactly ONE fetch
// invocation, and all N receive the result.
func TestCacheSingleflight(t *testing.T) {
	c := New[string, int](16)
	calls := int32(0)
	fetch := func() (int, bool, error) {
		atomic.AddInt32(&calls, 1)
		time.Sleep(10 * time.Millisecond) // let other callers pile up
		return 42, true, nil
	}

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			v, found, err := c.GetOrFetch("k", time.Hour, fetch)
			if err != nil || !found || v != 42 {
				t.Errorf("got (%v, %v, %v), want (42, true, nil)", v, found, err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("fetch invoked %d times, want exactly 1 (stampede)", got)
	}
}

// TestCacheFetchErrorsNotCached pins that an upstream error doesn't
// poison the cache — the next call retries instead of holding the
// failure for the TTL window. (Deliberate: we're caching SUCCESSFUL
// reads, not negative outcomes from upstream errors.)
func TestCacheFetchErrorsNotCached(t *testing.T) {
	c := New[string, int](16)
	calls := int32(0)
	fetch := func() (int, bool, error) {
		atomic.AddInt32(&calls, 1)
		return 0, false, errors.New("upstream down")
	}

	for i := 0; i < 3; i++ {
		_, _, err := c.GetOrFetch("k", time.Hour, fetch)
		if err == nil {
			t.Errorf("call %d: expected error, got nil", i)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("fetch invoked %d times, want 3 (errors not cached)", got)
	}
}

// TestCacheLenTracksItems pins Len() — useful for ops dashboards and
// for verifying eviction behaviour in tests.
func TestCacheLenTracksItems(t *testing.T) {
	c := New[int, int](16)
	exp := time.Now().Add(time.Hour)
	for i := 0; i < 5; i++ {
		c.Set(i, Entry[int]{Value: i, Found: true, ExpiresAt: exp})
	}
	if got := c.Len(); got != 5 {
		t.Errorf("Len = %d, want 5", got)
	}
}
