// Package cached wraps a quote.Provider with an in-memory cache and
// singleflight stampede control. Successful fetches cache for 60s;
// errors cache for 600s (so a flaky upstream doesn't hammer Yahoo on
// every miss).
package cached

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/cmj0121/imagelet/service/stock/quote"
)

// Default TTLs. Override at construction for tests.
//
// 60s success TTL keeps the displayed price within one minute of Yahoo's
// own update cadence while throttling our hit rate to ~1/min/symbol — the
// free chart endpoint is unofficial and rate-limit-prone. 10m failure TTL
// gives transient outages enough room to recover without re-pummelling
// upstream and bounds how long a STALE prefix might appear.
const (
	DefaultSuccessTTL = 60 * time.Second
	DefaultFailureTTL = 10 * time.Minute
)

// entry holds either a Quote or an error, plus a deadline. We always
// serve the cached Quote past its deadline if a fresh fetch fails — see
// the Get logic.
type entry struct {
	quote    quote.Quote
	err      error
	deadline time.Time // after this, considered stale
	hasQuote bool      // distinguish "cached err, no Quote ever" from "cached Quote + recent err"
}

// Provider wraps an inner quote.Provider with TTL + singleflight.
type Provider struct {
	inner        quote.Provider
	successTTL   time.Duration
	failureTTL   time.Duration
	now          func() time.Time
	singleflight singleflight.Group

	mu      sync.Mutex
	entries map[string]*entry
}

// New returns a cached Provider with default TTLs.
func New(inner quote.Provider) *Provider {
	return NewWithTTL(inner, DefaultSuccessTTL, DefaultFailureTTL)
}

// NewWithTTL returns a cached Provider with custom TTLs — primarily for tests.
func NewWithTTL(inner quote.Provider, successTTL, failureTTL time.Duration) *Provider {
	return &Provider{
		inner:      inner,
		successTTL: successTTL,
		failureTTL: failureTTL,
		now:        time.Now,
		entries:    make(map[string]*entry),
	}
}

// Get returns a Quote for symbol. Behavior:
//
//   - cache hit, fresh                          → cached Quote, nil
//   - cache hit, stale, refresh succeeds        → fresh Quote, nil
//   - cache hit, stale, refresh fails, hasQuote → cached Quote, refresh err (caller branches on err to set STALE prefix)
//   - cache miss, fetch succeeds                → fresh Quote, nil
//   - cache miss, fetch fails                   → zero Quote, err (cached for failureTTL)
//
// Concurrent misses for the same symbol are coalesced via singleflight,
// so 100 simultaneous /stock requests with empty cache trigger ONE inner call.
func (p *Provider) Get(ctx context.Context, symbol string) (quote.Quote, error) {
	now := p.now()
	p.mu.Lock()
	e, ok := p.entries[symbol]
	p.mu.Unlock()

	if ok && now.Before(e.deadline) {
		if e.hasQuote {
			return e.quote, nil
		}
		// Cached error within failureTTL — propagate without re-fetching.
		return quote.Quote{}, e.err
	}

	// Stale or missing — refresh via singleflight.
	v, err, _ := p.singleflight.Do(symbol, func() (any, error) {
		return p.inner.Get(ctx, symbol)
	})
	if err != nil {
		// On error, extend (or create) the entry. If we already had a
		// good Quote, keep it as the cached value but extend the deadline
		// so we don't refetch for failureTTL.
		p.mu.Lock()
		defer p.mu.Unlock()
		if e != nil && e.hasQuote {
			e.deadline = now.Add(p.failureTTL)
			e.err = err
			return e.quote, err
		}
		p.entries[symbol] = &entry{
			err:      err,
			deadline: now.Add(p.failureTTL),
		}
		return quote.Quote{}, err
	}

	q := v.(quote.Quote)
	p.mu.Lock()
	p.entries[symbol] = &entry{
		quote:    q,
		hasQuote: true,
		deadline: now.Add(p.successTTL),
	}
	p.mu.Unlock()
	return q, nil
}

// GetAt delegates to the inner provider when it implements
// quote.HistoricalProvider, so a cached-wrapped yahoo.Provider in
// production still surfaces the historical-quote pipeline. Historical
// lookups bypass the in-memory cache entirely — they're rare enough
// that re-running upstream is cheaper than reasoning about a date-
// keyed cache (which would also need TTL semantics that don't really
// apply to immutable historical bars). Returns quote.ErrUnavailable
// when the inner provider doesn't support historical fetches.
func (p *Provider) GetAt(ctx context.Context, symbol string, asOf time.Time) (quote.Quote, error) {
	hp, ok := p.inner.(quote.HistoricalProvider)
	if !ok {
		return quote.Quote{}, quote.ErrUnavailable
	}
	return hp.GetAt(ctx, symbol, asOf)
}
