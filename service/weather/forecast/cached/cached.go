// Package cached wraps a forecast.Provider with an in-memory cache and
// singleflight stampede control. Successful fetches cache for 600s;
// errors cache for 600s. Unlike /stock/quote/cached, the failure TTL is
// short (matching success TTL) because weather staleness is a correctness
// hazard — past failureTTL since the last good fetch we return a zero
// Forecast plus the upstream error so the handler can emit 503 instead
// of perpetually serving misleading STALE data.
//
// Constructor naming. Two cache shapes coexist in this codebase: the
// subpackage form `cached.New(inner)` used here and in /stock/quote/cached,
// and the inline form `Provider.NewCached(inner)` used in airquality,
// earthquake, and twse where the cache type lives in the same package as
// the HTTPProvider. Both styles are intentional — subpackage isolation pays
// off when the cache logic is large (e.g. the cap-stale-on-failure branch
// here) and inline placement is simpler when the cache is a thin wrapper.
// New cache layers should pick the shape that matches their complexity.
package cached

import (
	"context"
	"math"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/cmj0121/imagelet/service/weather/forecast"
)

// Default TTLs. Override at construction for tests.
//
// 10m success TTL aligns with Open-Meteo's hourly model output cadence
// while keeping the displayed temperature within one model step. 10m
// failure TTL is intentionally identical and short — past that window we
// stop serving stale forecasts and let the handler 503 instead, because
// weather staleness is a correctness hazard (a frozen "sunny" caption
// during a developing storm is worse than a 503).
const (
	DefaultSuccessTTL = 10 * time.Minute
	DefaultFailureTTL = 10 * time.Minute
)

// cacheKey identifies a forecast cell. Lat/lon are rounded to 1 decimal
// (~10km grid) so visitors in the same building share a cache entry; unit
// is part of the key so metric and imperial don't collide.
type cacheKey struct {
	lat, lon float64
	unit     forecast.Unit
}

// entry holds either a Forecast or an error, plus deadline/storedAt.
// deadline marks when the entry stops being fresh; storedAt records the
// last successful fetch so we can cap stale-on-failure serving at
// failureTTL since the last good observation.
type entry struct {
	forecast    forecast.Forecast
	err         error
	deadline    time.Time // after this, considered stale
	storedAt    time.Time // when the cached forecast was last successful
	hasForecast bool      // true when forecast is a real Forecast (vs zero-value)
}

// Provider wraps an inner forecast.Provider with TTL + singleflight.
type Provider struct {
	inner        forecast.Provider
	successTTL   time.Duration
	failureTTL   time.Duration
	now          func() time.Time
	singleflight singleflight.Group

	mu      sync.Mutex
	entries map[cacheKey]*entry
}

// New returns a cached Provider with default TTLs.
func New(inner forecast.Provider) *Provider {
	return NewWithTTL(inner, DefaultSuccessTTL, DefaultFailureTTL)
}

// NewWithTTL returns a cached Provider with custom TTLs — primarily for tests.
func NewWithTTL(inner forecast.Provider, successTTL, failureTTL time.Duration) *Provider {
	return &Provider{
		inner:      inner,
		successTTL: successTTL,
		failureTTL: failureTTL,
		now:        time.Now,
		entries:    make(map[cacheKey]*entry),
	}
}

// keyFor normalizes (lat, lon, unit) into a cache key. Lat/lon are
// rounded to 1 decimal — a meteorologically-meaningful 10km grid cell.
func keyFor(lat, lon float64, unit forecast.Unit) cacheKey {
	return cacheKey{
		lat:  math.Round(lat*10) / 10,
		lon:  math.Round(lon*10) / 10,
		unit: unit,
	}
}

// Get returns a Forecast for (lat, lon, unit). Behavior:
//
//   - cache hit, fresh (no err)                                       → cached Forecast, nil
//   - cache hit, error within failureTTL                              → zero Forecast, cached err
//   - cache hit, stale, refresh succeeds                              → fresh Forecast, nil
//   - cache hit, stale, refresh fails, age <= failureTTL since success → cached Forecast, refresh err (STALE-serve)
//   - cache hit, stale, refresh fails, age >  failureTTL since success → zero Forecast, refresh err (handler must 503)
//   - cache miss, fetch succeeds                                      → fresh Forecast, nil
//   - cache miss, fetch fails                                         → zero Forecast, err (cached for failureTTL)
//
// Concurrent misses for the same key are coalesced via singleflight.
func (p *Provider) Get(ctx context.Context, lat, lon float64, unit forecast.Unit) (forecast.Forecast, error) {
	key := keyFor(lat, lon, unit)
	now := p.now()

	p.mu.Lock()
	e, ok := p.entries[key]
	p.mu.Unlock()

	if ok && now.Before(e.deadline) {
		if e.err != nil {
			// Cached error within failureTTL — propagate without re-fetching.
			return forecast.Forecast{}, e.err
		}
		if e.hasForecast {
			return e.forecast, nil
		}
	}

	// Stale or missing — refresh via singleflight. singleflight.Do takes
	// a string; encode the cacheKey deterministically using the same
	// 1-decimal rounding as the map key so concurrent misses for nearby
	// coords collapse to the same flight.
	sfKey := strconv.FormatFloat(key.lat, 'f', 1, 64) + "|" +
		strconv.FormatFloat(key.lon, 'f', 1, 64) + "|" +
		string(key.unit)
	v, err, _ := p.singleflight.Do(sfKey, func() (any, error) {
		return p.inner.Get(ctx, lat, lon, unit)
	})
	if err != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		if e != nil && e.hasForecast {
			if now.Sub(e.storedAt) <= p.failureTTL {
				// Within window — STALE serve OK.
				e.err = err
				e.deadline = now.Add(p.failureTTL)
				return e.forecast, err
			}
			// Past failureTTL since last success — too stale to serve.
			p.entries[key] = &entry{
				err:      err,
				deadline: now.Add(p.failureTTL),
			}
			return forecast.Forecast{}, err
		}
		// No prior success — cache the error.
		p.entries[key] = &entry{
			err:      err,
			deadline: now.Add(p.failureTTL),
		}
		return forecast.Forecast{}, err
	}

	f := v.(forecast.Forecast)
	p.mu.Lock()
	p.entries[key] = &entry{
		forecast:    f,
		hasForecast: true,
		deadline:    now.Add(p.successTTL),
		storedAt:    now,
	}
	p.mu.Unlock()
	return f, nil
}
