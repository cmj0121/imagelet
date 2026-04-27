// Package earthquake provides recent-significant-quake retrieval keyed
// on (lat, lon). Best-effort enrichment for /weather: failures must NOT
// 5xx the base render, and "no recent quake" returns ErrUnavailable so
// the caller drops the row.
//
// Three layers, mirroring service/weather/airquality:
//
//   - Provider interface + Event value type
//   - HTTPProvider hitting USGS fdsnws/event/1/query (geojson)
//   - Cached wrapper with TTL + singleflight
//
// USGS is free, key-less, globally applicable, and the de-facto canonical
// source for earthquake data — no new vendor risk.
package earthquake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrUnavailable signals "USGS answered but no qualifying event near
// the visitor." Distinct from transport errors so callers can pick
// different TTLs (transient vs "this region just isn't seismic").
var ErrUnavailable = errors.New("earthquake unavailable")

// Event is the single most-recent qualifying event the renderer cares
// about. Place is USGS's human-readable label ("9 km ENE of Yilan").
// Magnitude is the moment-tensor magnitude (Mw); we don't expose the
// magnitude type since it varies by reporting agency and isn't
// glance-worthy in a caption.
type Event struct {
	Magnitude float64
	Place     string
	Time      time.Time
	ID        string
}

// Provider is implemented by anything that returns the most-recent
// significant earthquake near (lat, lon). Get must respect ctx.
type Provider interface {
	Get(ctx context.Context, lat, lon float64) (Event, error)
}

// HTTPProvider hits USGS's fdsnws geojson endpoint.
type HTTPProvider struct {
	endpoint string
	client   *http.Client

	// Tunables — exposed for tests / future per-region tuning. RadiusKM
	// is the search radius around (lat, lon); MinMagnitude filters out
	// imperceptible events; LookbackDays caps how far back we search.
	RadiusKM     float64
	MinMagnitude float64
	LookbackDays int
}

const (
	defaultEndpoint = "https://earthquake.usgs.gov/fdsnws/event/1/query"
	userAgent       = "imagelet/0.2 (+https://github.com/cmj0121/imagelet)"

	defaultRadiusKM     = 300.0
	defaultMinMagnitude = 4.0
	defaultLookbackDays = 7
)

// New returns an HTTPProvider with default tunables and a 5-second
// per-request timeout.
func New() *HTTPProvider {
	return NewWithEndpoint(defaultEndpoint, &http.Client{Timeout: 5 * time.Second})
}

// NewWithEndpoint returns an HTTPProvider with a custom base URL and
// HTTP client — primarily for tests pointed at httptest.Server.
func NewWithEndpoint(endpoint string, client *http.Client) *HTTPProvider {
	return &HTTPProvider{
		endpoint:     endpoint,
		client:       client,
		RadiusKM:     defaultRadiusKM,
		MinMagnitude: defaultMinMagnitude,
		LookbackDays: defaultLookbackDays,
	}
}

// Get fetches the single most-recent qualifying event near (lat, lon).
// Returns ErrUnavailable when USGS responds with zero features.
func (p *HTTPProvider) Get(ctx context.Context, lat, lon float64) (Event, error) {
	q := url.Values{}
	q.Set("format", "geojson")
	q.Set("latitude", strconv.FormatFloat(lat, 'f', 4, 64))
	q.Set("longitude", strconv.FormatFloat(lon, 'f', 4, 64))
	q.Set("maxradiuskm", strconv.FormatFloat(p.RadiusKM, 'f', 0, 64))
	q.Set("minmagnitude", strconv.FormatFloat(p.MinMagnitude, 'f', 1, 64))
	q.Set("starttime", time.Now().UTC().AddDate(0, 0, -p.LookbackDays).Format("2006-01-02"))
	q.Set("orderby", "time")
	q.Set("limit", "1")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return Event{}, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return Event{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return Event{}, fmt.Errorf("earthquake: %s: %s", resp.Status, body)
	}

	var raw struct {
		Features []struct {
			ID         string `json:"id"`
			Properties struct {
				Mag   float64 `json:"mag"`
				Place string  `json:"place"`
				Time  int64   `json:"time"` // millis since epoch
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Event{}, err
	}
	if len(raw.Features) == 0 {
		return Event{}, ErrUnavailable
	}

	f := raw.Features[0]
	return Event{
		Magnitude: f.Properties.Mag,
		Place:     f.Properties.Place,
		Time:      time.UnixMilli(f.Properties.Time).UTC(),
		ID:        f.ID,
	}, nil
}

// Default cache TTLs. Earthquake catalogues update within minutes of
// an event; 15m success keeps captions reasonably current without
// hammering USGS. 5m failure TTL is short — the upstream is reliable.
const (
	DefaultSuccessTTL = 15 * time.Minute
	DefaultFailureTTL = 5 * time.Minute
)

// Cached wraps a Provider with TTL + singleflight. Same shape as
// airquality.Cached. ErrUnavailable is cached as a "miss" — the
// "no recent quake" outcome is stable enough to cache.
type Cached struct {
	inner        Provider
	successTTL   time.Duration
	failureTTL   time.Duration
	now          func() time.Time
	singleflight singleflight.Group

	mu      sync.Mutex
	entries map[cacheKey]*entry
}

type cacheKey struct{ lat, lon float64 }

type entry struct {
	event    Event
	err      error
	deadline time.Time
	hasOK    bool
}

// NewCached returns a Cached with default TTLs.
func NewCached(inner Provider) *Cached {
	return NewCachedWithTTL(inner, DefaultSuccessTTL, DefaultFailureTTL)
}

// NewCachedWithTTL returns a Cached with custom TTLs — primarily for tests.
func NewCachedWithTTL(inner Provider, successTTL, failureTTL time.Duration) *Cached {
	return &Cached{
		inner:      inner,
		successTTL: successTTL,
		failureTTL: failureTTL,
		now:        time.Now,
		entries:    make(map[cacheKey]*entry),
	}
}

// Get fetches a recent significant event near (lat, lon). Cache hits
// within deadline serve the cached value. Misses go via singleflight.
func (c *Cached) Get(ctx context.Context, lat, lon float64) (Event, error) {
	key := keyFor(lat, lon)
	now := c.now()

	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()

	if ok && now.Before(e.deadline) {
		if e.err != nil {
			return Event{}, e.err
		}
		if e.hasOK {
			return e.event, nil
		}
	}

	sfKey := strconv.FormatFloat(key.lat, 'f', 1, 64) + "|" +
		strconv.FormatFloat(key.lon, 'f', 1, 64)
	v, err, _ := c.singleflight.Do(sfKey, func() (any, error) {
		return c.inner.Get(ctx, lat, lon)
	})
	if err != nil {
		c.mu.Lock()
		c.entries[key] = &entry{err: err, deadline: now.Add(c.failureTTL)}
		c.mu.Unlock()
		return Event{}, err
	}

	ev := v.(Event)
	c.mu.Lock()
	c.entries[key] = &entry{event: ev, hasOK: true, deadline: now.Add(c.successTTL)}
	c.mu.Unlock()
	return ev, nil
}

// keyFor normalizes (lat, lon) to a 0.5°-grid cache key (~50km cells)
// — coarser than airquality since seismic sources are large enough
// that nearby visitors should agree on "is there a recent quake".
func keyFor(lat, lon float64) cacheKey {
	return cacheKey{
		lat: math.Round(lat*2) / 2,
		lon: math.Round(lon*2) / 2,
	}
}

// noopProvider returns ErrUnavailable for every call. Pass NoopProvider()
// to weather.Register in tests/dev when the earthquake surface is irrelevant.
type noopProvider struct{}

func (noopProvider) Get(_ context.Context, _, _ float64) (Event, error) {
	return Event{}, ErrUnavailable
}

// NoopProvider returns a Provider that always errors with ErrUnavailable.
// Used by tests and as a stand-in when the operator wants to disable
// earthquake enrichment without touching the handler.
func NoopProvider() Provider { return noopProvider{} }
