// Package ttlcache provides a generic in-memory cache with TTL expiry,
// LRU eviction, singleflight stampede control, and negative-cache support.
//
// The intended use is in front of upstream providers (Yahoo, TWSE, TAIFEX
// HTTP endpoints) so repeated requests for the same (key, date) tuple
// don't fan out duplicate calls. Callers compute a publish-window-aware
// TTL per entry and pass it to GetOrFetch — the cache itself doesn't
// know about wall-clock semantics, just expiry.
//
// Negative caching (Found=false) lets the cache remember that an upstream
// returned no data for a given key, so a walk-back loop probing
// successive dates can skip "we already know this date is empty"
// without re-hitting the network on every request.
package ttlcache

import (
	"container/list"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// DefaultCap is the LRU eviction threshold when callers don't specify
// one. 4096 entries fits comfortably in a few MB even with large value
// types (twse.StockData, twse.RetailFutures), and rolls over slowly
// enough that ticker-heavy traffic doesn't thrash the cache.
const DefaultCap = 4096

// Entry is the public shape a cache lookup returns. Found differentiates
// "upstream returned a real value" from "upstream said no data exists
// for this key" — both are valid answers worth caching, but consumers
// (e.g. walk-back loops) treat them differently.
type Entry[V any] struct {
	Value     V
	Found     bool
	ExpiresAt time.Time
}

type entryNode[K comparable, V any] struct {
	key  K
	e    Entry[V]
	elem *list.Element
}

// Cache is a TTL+LRU+singleflight cache keyed by any comparable type.
// All methods are safe for concurrent use.
type Cache[K comparable, V any] struct {
	mu    sync.Mutex
	items map[K]*entryNode[K, V]
	lru   *list.List // front = most recently used
	cap   int
	sf    singleflight.Group
	now   func() time.Time
}

// New returns a Cache with capacity cap (DefaultCap when cap <= 0).
func New[K comparable, V any](cap int) *Cache[K, V] {
	if cap <= 0 {
		cap = DefaultCap
	}
	return &Cache[K, V]{
		items: make(map[K]*entryNode[K, V]),
		lru:   list.New(),
		cap:   cap,
		now:   time.Now,
	}
}

// SetClock overrides the cache's wall-clock for tests. Production
// callers leave this alone (default is time.Now).
func (c *Cache[K, V]) SetClock(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// Len returns the current number of cached entries (including expired
// ones that haven't been touched yet — an expired entry counts until
// it's evicted on the next Get/Set that walks past it).
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Get returns the entry for key when present AND unexpired. Expired
// entries return (Entry{}, false) but stay in the map — they get
// overwritten on the next Set or evicted via LRU pressure. This keeps
// Get a pure read (no mutation under lock contention).
func (c *Cache[K, V]) Get(key K) (Entry[V], bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, ok := c.items[key]
	if !ok {
		return Entry[V]{}, false
	}
	if !n.e.ExpiresAt.IsZero() && !n.e.ExpiresAt.After(c.now()) {
		return Entry[V]{}, false
	}
	c.lru.MoveToFront(n.elem)
	return n.e, true
}

// Set stores e under key, replacing any existing entry, and evicts the
// least-recently-used entries when over cap.
func (c *Cache[K, V]) Set(key K, e Entry[V]) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n, ok := c.items[key]; ok {
		n.e = e
		c.lru.MoveToFront(n.elem)
		return
	}
	n := &entryNode[K, V]{key: key, e: e}
	n.elem = c.lru.PushFront(n)
	c.items[key] = n
	for c.lru.Len() > c.cap {
		oldest := c.lru.Back()
		if oldest == nil {
			break
		}
		c.lru.Remove(oldest)
		delete(c.items, oldest.Value.(*entryNode[K, V]).key)
	}
}

// All returns a snapshot of every live entry under lock, ordered
// most-recently-used first. Used by the JSON snapshot save path.
// Expired entries are filtered out so a save-on-shutdown doesn't
// persist already-dead entries.
func (c *Cache[K, V]) All() map[K]Entry[V] {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	out := make(map[K]Entry[V], len(c.items))
	for e := c.lru.Front(); e != nil; e = e.Next() {
		n := e.Value.(*entryNode[K, V])
		if !n.e.ExpiresAt.IsZero() && !n.e.ExpiresAt.After(now) {
			continue
		}
		out[n.key] = n.e
	}
	return out
}

// GetOrFetch returns the cached value when present + unexpired, else
// calls fetch under a singleflight slot keyed on `key` so concurrent
// callers for the same key make at most one upstream call. The
// successful result (including Found=false negative-cache hits) is
// stored with the supplied ttl. fetch errors are NOT cached — the
// caller can retry on the next request.
//
// The ttl is computed by the caller per-call (publish-window-aware in
// production) so the cache itself stays oblivious to upstream-specific
// publish cadences.
func (c *Cache[K, V]) GetOrFetch(key K, ttl time.Duration, fetch func() (value V, found bool, err error)) (V, bool, error) {
	if e, ok := c.Get(key); ok {
		return e.Value, e.Found, nil
	}

	type result struct {
		v     V
		found bool
	}
	// singleflight wants a string key; %v handles string / int / struct
	// keys uniformly without forcing the generic K to be string.
	//
	// SECURITY: %v formatting is collision-safe ONLY for primitive key
	// types (string, integer). All current callers pass string keys.
	// If a future caller passes a struct- or map-typed K, two distinct
	// keys could format to the same string (e.g. struct{A,B int}{1,23}
	// and {12,3} both print as "{1 23}"), causing singleflight to
	// dedupe unrelated fetches. Switch to a hash-based key (or constrain
	// K to ~string) before adding non-primitive-keyed caches.
	sfKey := fmt.Sprintf("%v", key)
	out, err, _ := c.sf.Do(sfKey, func() (any, error) {
		// Re-check after acquiring the slot — another caller may have
		// populated the entry while we waited.
		if e, ok := c.Get(key); ok {
			return result{v: e.Value, found: e.Found}, nil
		}
		v, found, err := fetch()
		if err != nil {
			return result{}, err
		}
		c.Set(key, Entry[V]{Value: v, Found: found, ExpiresAt: c.now().Add(ttl)})
		return result{v: v, found: found}, nil
	})
	if err != nil {
		var zero V
		return zero, false, err
	}
	r := out.(result)
	return r.v, r.found, nil
}
