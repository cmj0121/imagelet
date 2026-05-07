// Package cached provides a TTL + singleflight wrapper around the DNS
// Resolver interface. Cache is process-local; in-process state survives
// a DNS_RESOLVER env-var change. A resolver swap requires process
// restart to ensure cached entries reflect the new upstream's view —
// different recursive resolvers have different recursive views and
// different DNSSEC chains, so an entry resolved against 1.1.1.1 may
// not match what 8.8.8.8 would return for the same hostname. Operators
// changing DNS_RESOLVER MUST restart the process.
//
// Three outcomes are tracked separately so 404s cache for an hour
// while transient failures cache for a minute:
//
//	stateOK       — successful Lookup, TTL = clamp(MinTTL, 60s, 600s)
//	stateNotFound — ErrNotFound, notFoundTTL (default 1h)
//	stateFail     — ErrUnavailable, failureTTL (default 1m)
//
// ErrSelfThrottled is the fourth pseudo-state "statePassThrough" — it
// is NEVER cached. The rate limiter is process-global; caching the err
// under a hostname key would alias unrelated burst-bucket state onto
// per-hostname behavior. See cache-scope rule below.
//
// On stale + transient-refresh-fail with a previous stateOK entry, the
// cached value is returned alongside the refresh error — the handler
// renders the cached value with a "( stale data )" caption row.
package cached

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/singleflight"

	"github.com/cmj0121/imagelet/service/dns/resolver"
)

// Default TTLs. Overrideable at construction for tests.
const (
	DefaultFailureTTL  = 1 * time.Minute
	DefaultNotFoundTTL = 1 * time.Hour
	SuccessTTLMin      = 60 * time.Second  // R6 floor
	SuccessTTLMax      = 600 * time.Second // R6 ceiling
)

// state is the cached entry classifier. Note: there is NO
// stateSelfThrottled — ErrSelfThrottled is the fourth pseudo-state
// "statePassThrough" handled explicitly in Lookup BEFORE the entries
// map is consulted.
type state uint8

const (
	stateOK state = iota
	stateNotFound
	stateFail
)

type entry struct {
	records  resolver.Records
	err      error
	state    state
	deadline time.Time
}

// Cached wraps a resolver.Resolver with a TTL + singleflight cache.
type Cached struct {
	inner       resolver.Resolver
	failureTTL  time.Duration
	notFoundTTL time.Duration
	now         func() time.Time
	sf          singleflight.Group

	mu      sync.Mutex
	entries map[string]*entry

	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64
}

// New returns a Cached with default TTLs.
func New(inner resolver.Resolver) *Cached {
	return NewWithTTL(inner, DefaultFailureTTL, DefaultNotFoundTTL)
}

// NewWithTTL returns a Cached with custom TTLs. Stateful TTLs:
//
//	stateOK       — clamped per-call to [SuccessTTLMin, SuccessTTLMax]
//	stateNotFound — notFound (e.g. 1h)
//	stateFail     — failure (e.g. 1m)
func NewWithTTL(inner resolver.Resolver, failure, notFound time.Duration) *Cached {
	return &Cached{
		inner:       inner,
		failureTTL:  failure,
		notFoundTTL: notFound,
		now:         time.Now,
		entries:     make(map[string]*entry),
	}
}

// classify reduces an error to the cache state it should drive.
//
//	nil                                 → stateOK
//	errors.Is(resolver.ErrNotFound)     → stateNotFound
//	everything else (including
//	errors.Is(ErrUnavailable))          → stateFail
//
// ErrSelfThrottled is intercepted in Lookup BEFORE classify is called;
// it never appears here.
func classify(err error) state {
	switch {
	case err == nil:
		return stateOK
	case errors.Is(err, resolver.ErrNotFound):
		return stateNotFound
	default:
		return stateFail
	}
}

// successDeadline computes the per-state expiry time for a stateOK
// entry, clamping MinTTL to [SuccessTTLMin, SuccessTTLMax].
func successDeadline(now time.Time, minTTL time.Duration) time.Time {
	ttl := minTTL
	if ttl < SuccessTTLMin {
		ttl = SuccessTTLMin
	}
	if ttl > SuccessTTLMax {
		ttl = SuccessTTLMax
	}
	return now.Add(ttl)
}

// Lookup honors per-record-set MinTTL on stateOK (clamped per R6),
// fixed TTLs on stateNotFound and stateFail. Stale-on-transient: prior
// stateOK + new stateFail returns the cached Records alongside the
// err; handler renders with "( stale data )" caption.
//
// ErrSelfThrottled is statePassThrough — never written to the entries
// map. Singleflight still dedupes concurrent same-host calls during a
// single self-throttle so the limiter only sees one call's worth.
//
// Cache key: the canonical host string (already lowercased + trimmed
// + idna-encoded by the resolver). One cache entry per canonical key.
func (c *Cached) Lookup(ctx context.Context, host string) (resolver.Records, error) {
	now := c.now()
	c.mu.Lock()
	e, ok := c.entries[host]
	c.mu.Unlock()

	if ok && now.Before(e.deadline) {
		c.hits.Add(1)
		switch e.state {
		case stateOK:
			return e.records, nil
		default:
			return resolver.Records{}, e.err
		}
	}

	c.misses.Add(1)
	v, err, _ := c.sf.Do(host, func() (any, error) {
		return c.inner.Lookup(ctx, host)
	})

	// statePassThrough: ErrSelfThrottled is NEVER cached. The limiter
	// is process-global; caching it under a hostname key would alias
	// unrelated burst-bucket state onto per-hostname behavior.
	if errors.Is(err, resolver.ErrSelfThrottled) {
		return resolver.Records{}, err
	}

	s := classify(err)
	c.mu.Lock()
	defer c.mu.Unlock()

	if err != nil {
		// Stale-serve: prior stateOK + transient (stateFail) refresh
		// error → return cached Records + the refresh err. ErrNotFound
		// transitions out of stateOK normally (the entity is gone).
		if e != nil && e.state == stateOK && s == stateFail {
			e.deadline = now.Add(c.failureTTL)
			e.err = err
			c.evictions.Add(1)
			return e.records, err
		}
		if e != nil {
			c.evictions.Add(1)
		}
		c.entries[host] = &entry{
			err:      err,
			state:    s,
			deadline: now.Add(c.ttlFor(s)),
		}
		return resolver.Records{}, err
	}

	r, _ := v.(resolver.Records)
	if e != nil {
		c.evictions.Add(1)
	}
	c.entries[host] = &entry{
		records:  r,
		state:    stateOK,
		deadline: successDeadline(now, r.MinTTL),
	}
	return r, nil
}

// ttlFor returns the deadline-extension duration for a non-OK state.
// stateOK is handled separately via successDeadline (clamped MinTTL).
func (c *Cached) ttlFor(s state) time.Duration {
	switch s {
	case stateNotFound:
		return c.notFoundTTL
	default:
		return c.failureTTL
	}
}

// LogHourly spins a background goroutine that emits hits/misses/
// evictions counters once per hour at info level. Bound to ctx so
// SIGINT/SIGTERM drains cleanly — no goroutine leak.
func (c *Cached) LogHourly(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			log.Info().
				Int64("hits", c.hits.Load()).
				Int64("misses", c.misses.Load()).
				Int64("evictions", c.evictions.Load()).
				Str("kind", "dns").
				Msg("dns cache stats")
		}
	}
}
