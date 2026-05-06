// Package cached wraps profile.UserProvider / profile.RepoProvider with
// an in-memory TTL cache and singleflight stampede control. Three
// outcomes are tracked separately so 404s cache for an hour while
// transient failures cache for a minute:
//
//	stateOK       — successful fetch, successTTL (default 5m)
//	stateNotFound — ErrNotFound, notFoundTTL (default 1h)
//	stateFail     — ErrUnavailable / ErrRateLimited, failureTTL (default 1m)
//
// On stale + refresh-fail with a previous stateOK entry, the cached
// value is returned alongside the refresh error — the handler can render
// the cached value with a "stale data" caption row appended.
//
// Hit / miss / eviction counters are exposed via LogHourly(ctx) so
// cmd/imagelet can emit one-per-hour info-level summaries that drain
// cleanly on SIGINT/SIGTERM (R11).
package cached

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/singleflight"

	"github.com/cmj0121/imagelet/service/github/profile"
)

// Default TTLs. Override at construction for tests.
const (
	DefaultSuccessTTL  = 5 * time.Minute
	DefaultFailureTTL  = 1 * time.Minute
	DefaultNotFoundTTL = 1 * time.Hour
)

// state distinguishes the three cacheable outcomes; each has its own TTL.
type state uint8

const (
	stateOK state = iota
	stateNotFound
	stateFail
)

// classify reduces an error to the cache state it should drive. nil →
// stateOK; ErrNotFound → stateNotFound; everything else → stateFail.
func classify(err error) state {
	switch {
	case err == nil:
		return stateOK
	case errors.Is(err, profile.ErrNotFound):
		return stateNotFound
	default:
		return stateFail
	}
}

// ttlFor returns the deadline-extension duration for a given state.
func (c *UserCached) ttlFor(s state) time.Duration {
	switch s {
	case stateOK:
		return c.successTTL
	case stateNotFound:
		return c.notFoundTTL
	default:
		return c.failureTTL
	}
}

func (c *RepoCached) ttlFor(s state) time.Duration {
	switch s {
	case stateOK:
		return c.successTTL
	case stateNotFound:
		return c.notFoundTTL
	default:
		return c.failureTTL
	}
}

// userEntry holds one cached user lookup. profile is zero-valued when
// state != stateOK.
type userEntry struct {
	profile  profile.Profile
	err      error
	state    state
	deadline time.Time
}

// UserCached wraps a profile.UserProvider with TTL + singleflight.
type UserCached struct {
	inner       profile.UserProvider
	successTTL  time.Duration
	failureTTL  time.Duration
	notFoundTTL time.Duration
	now         func() time.Time
	sf          singleflight.Group

	mu      sync.Mutex
	entries map[string]*userEntry

	// R11: hit/miss/eviction counters logged hourly.
	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64
}

// NewUser returns a UserCached with default TTLs.
func NewUser(inner profile.UserProvider) *UserCached {
	return NewUserWithTTL(inner, DefaultSuccessTTL, DefaultFailureTTL, DefaultNotFoundTTL)
}

// NewUserWithTTL returns a UserCached with custom TTLs — primarily for
// tests.
func NewUserWithTTL(inner profile.UserProvider, success, failure, notFound time.Duration) *UserCached {
	return &UserCached{
		inner:       inner,
		successTTL:  success,
		failureTTL:  failure,
		notFoundTTL: notFound,
		now:         time.Now,
		entries:     make(map[string]*userEntry),
	}
}

// User obeys the same Get-style logic as service/stock/quote/cached.Provider:
//
//   - cache hit, fresh, stateOK       → cached profile, nil
//   - cache hit, fresh, stateNotFound → zero, ErrNotFound
//   - cache hit, fresh, stateFail     → zero, cached err (ErrUnavailable / ErrRateLimited)
//   - cache miss / stale              → singleflight inner.User
//   - nil err                         → cache stateOK successTTL
//   - errors.Is(ErrNotFound)          → cache stateNotFound notFoundTTL
//   - any other error                 → cache stateFail failureTTL
//
// Stale-serve policy (mirrors service/stock/quote/cached): on stale +
// refresh-fail with a previous stateOK entry, return the cached Profile
// + the refresh error (e.g. ErrUnavailable). The handler renders the
// cached value with a "stale data" caption row appended.
func (c *UserCached) User(ctx context.Context, login string) (profile.Profile, error) {
	now := c.now()
	c.mu.Lock()
	e, ok := c.entries[login]
	c.mu.Unlock()

	if ok && now.Before(e.deadline) {
		c.hits.Add(1)
		switch e.state {
		case stateOK:
			return e.profile, nil
		default:
			return profile.Profile{}, e.err
		}
	}

	c.misses.Add(1)
	v, err, _ := c.sf.Do(login, func() (any, error) {
		return c.inner.User(ctx, login)
	})

	s := classify(err)
	c.mu.Lock()
	defer c.mu.Unlock()

	if err != nil {
		// Stale-serve: prior stateOK + transient (stateFail) refresh
		// error → return cached profile + the refresh err. ErrNotFound
		// transitions out of stateOK normally (the entity is gone).
		if e != nil && e.state == stateOK && s == stateFail {
			e.deadline = now.Add(c.failureTTL)
			e.err = err
			c.evictions.Add(1)
			return e.profile, err
		}
		// Replace the entry. evictions counts the prior entry being
		// overwritten — gives the operator a meaningful gauge before
		// any explicit LRU is added.
		if e != nil {
			c.evictions.Add(1)
		}
		c.entries[login] = &userEntry{
			err:      err,
			state:    s,
			deadline: now.Add(c.ttlFor(s)),
		}
		return profile.Profile{}, err
	}

	p := v.(profile.Profile)
	if e != nil {
		c.evictions.Add(1)
	}
	c.entries[login] = &userEntry{
		profile:  p,
		state:    stateOK,
		deadline: now.Add(c.successTTL),
	}
	return p, nil
}

// LogHourly spins a background goroutine that emits hits/misses/evictions
// counters once per hour at info level. Bound to ctx so SIGINT/SIGTERM
// drains cleanly — no goroutine leak.
func (c *UserCached) LogHourly(ctx context.Context) {
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
				Str("kind", "user").
				Msg("github cache stats")
		}
	}
}

// repoEntry holds one cached repo lookup. repo is zero-valued when
// state != stateOK.
type repoEntry struct {
	repo     profile.Repo
	err      error
	state    state
	deadline time.Time
}

// RepoCached mirrors UserCached for the Repo path, keyed by "owner/name".
type RepoCached struct {
	inner       profile.RepoProvider
	successTTL  time.Duration
	failureTTL  time.Duration
	notFoundTTL time.Duration
	now         func() time.Time
	sf          singleflight.Group

	mu      sync.Mutex
	entries map[string]*repoEntry

	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64
}

// NewRepo returns a RepoCached with default TTLs.
func NewRepo(inner profile.RepoProvider) *RepoCached {
	return NewRepoWithTTL(inner, DefaultSuccessTTL, DefaultFailureTTL, DefaultNotFoundTTL)
}

// NewRepoWithTTL returns a RepoCached with custom TTLs — primarily for
// tests.
func NewRepoWithTTL(inner profile.RepoProvider, success, failure, notFound time.Duration) *RepoCached {
	return &RepoCached{
		inner:       inner,
		successTTL:  success,
		failureTTL:  failure,
		notFoundTTL: notFound,
		now:         time.Now,
		entries:     make(map[string]*repoEntry),
	}
}

// Repo dispatches the same logic as UserCached.User above; key is
// owner+"/"+name.
func (c *RepoCached) Repo(ctx context.Context, owner, name string) (profile.Repo, error) {
	key := owner + "/" + name
	now := c.now()
	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()

	if ok && now.Before(e.deadline) {
		c.hits.Add(1)
		switch e.state {
		case stateOK:
			return e.repo, nil
		default:
			return profile.Repo{}, e.err
		}
	}

	c.misses.Add(1)
	v, err, _ := c.sf.Do(key, func() (any, error) {
		return c.inner.Repo(ctx, owner, name)
	})

	s := classify(err)
	c.mu.Lock()
	defer c.mu.Unlock()

	if err != nil {
		if e != nil && e.state == stateOK && s == stateFail {
			e.deadline = now.Add(c.failureTTL)
			e.err = err
			c.evictions.Add(1)
			return e.repo, err
		}
		if e != nil {
			c.evictions.Add(1)
		}
		c.entries[key] = &repoEntry{
			err:      err,
			state:    s,
			deadline: now.Add(c.ttlFor(s)),
		}
		return profile.Repo{}, err
	}

	r := v.(profile.Repo)
	if e != nil {
		c.evictions.Add(1)
	}
	c.entries[key] = &repoEntry{
		repo:     r,
		state:    stateOK,
		deadline: now.Add(c.successTTL),
	}
	return r, nil
}

// LogHourly mirrors UserCached.LogHourly; logs with Str("kind", "repo").
func (c *RepoCached) LogHourly(ctx context.Context) {
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
				Str("kind", "repo").
				Msg("github cache stats")
		}
	}
}
