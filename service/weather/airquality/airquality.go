// Package airquality provides current US-EPA AQI retrieval keyed on
// (lat, lon). It is a best-effort enrichment for /weather: failures
// must NOT 5xx the base render, so the handler treats every error as
// "drop the AQI caption row" rather than as a hard failure.
//
// Three layers, mirroring service/stock/twse:
//
//   - Provider interface + Reading value type
//   - HTTPProvider hitting Open-Meteo's /v1/air-quality endpoint
//   - Cached wrapper with TTL + singleflight
//
// Why Open-Meteo: the endpoint is free, key-less, globally applicable
// (CAMS-derived for EU + Geos5 for US), and reuses the operator's
// existing tolerance for the openmeteo upstream — no new vendor.
package airquality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrUnavailable signals "the upstream answered but has no data here."
// Distinct from transport errors so callers / cache layers can pick
// different TTLs (transient network blip vs ocean-coordinate gap).
var ErrUnavailable = errors.New("air quality unavailable")

// Reading is the value type the handler renders. USAQI is the US EPA
// 0-500 scale; Category is the EPA-standard label ("Good", "Moderate",
// etc.) derived from USAQI. AsOf is the upstream observation time.
type Reading struct {
	USAQI    int
	Category string
	AsOf     time.Time
}

// Provider is implemented by anything that returns a Reading for
// (lat, lon). Get must respect ctx cancellation.
type Provider interface {
	Get(ctx context.Context, lat, lon float64) (Reading, error)
}

// HTTPProvider hits Open-Meteo's /v1/air-quality endpoint.
type HTTPProvider struct {
	endpoint string
	client   *http.Client
}

const (
	defaultEndpoint = "https://air-quality-api.open-meteo.com/v1/air-quality"
	userAgent       = "imagelet/0.2 (+https://github.com/cmj0121/imagelet)"
	timeLayout      = "2006-01-02T15:04"
)

// New returns an HTTPProvider with a 5-second per-request timeout.
func New() *HTTPProvider {
	return NewWithEndpoint(defaultEndpoint, &http.Client{Timeout: 5 * time.Second})
}

// NewWithEndpoint returns an HTTPProvider with a custom base URL and
// HTTP client — primarily for tests pointed at httptest.Server.
func NewWithEndpoint(endpoint string, client *http.Client) *HTTPProvider {
	return &HTTPProvider{endpoint: endpoint, client: client}
}

// Get fetches the current US AQI for (lat, lon). Returns ErrUnavailable
// when the upstream is reachable but returns no data; transport errors
// pass through.
func (p *HTTPProvider) Get(ctx context.Context, lat, lon float64) (Reading, error) {
	url := fmt.Sprintf("%s?latitude=%s&longitude=%s&current=us_aqi&timezone=auto",
		p.endpoint,
		strconv.FormatFloat(lat, 'f', 4, 64),
		strconv.FormatFloat(lon, 'f', 4, 64))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Reading{}, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return Reading{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return Reading{}, fmt.Errorf("airquality: %s: %s", resp.Status, body)
	}

	var raw struct {
		Current struct {
			Time   string `json:"time"`
			USAQI  *int   `json:"us_aqi"`
		} `json:"current"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Reading{}, err
	}
	if raw.Current.USAQI == nil {
		return Reading{}, ErrUnavailable
	}
	asOf, _ := time.Parse(timeLayout, raw.Current.Time)
	return Reading{
		USAQI:    *raw.Current.USAQI,
		Category: CategoryFor(*raw.Current.USAQI),
		AsOf:     asOf,
	}, nil
}

// CategoryFor returns the EPA-standard label for a US-AQI value.
// Boundaries match airnow.gov: 0-50 Good, 51-100 Moderate, 101-150
// USG, 151-200 Unhealthy, 201-300 Very Unhealthy, 301+ Hazardous.
// Negative inputs (defensive — upstream shouldn't emit) collapse
// to "Good" so the caption never reads as a sentinel value.
func CategoryFor(aqi int) string {
	switch {
	case aqi <= 50:
		return "Good"
	case aqi <= 100:
		return "Moderate"
	case aqi <= 150:
		return "USG"
	case aqi <= 200:
		return "Unhealthy"
	case aqi <= 300:
		return "Very Unhealthy"
	default:
		return "Hazardous"
	}
}

// Default cache TTLs. AQI data rolls over hourly upstream so 30m
// success keeps us within one observation cycle. Failure TTL is
// short — a transient blip should re-try sooner than success.
const (
	DefaultSuccessTTL = 30 * time.Minute
	DefaultFailureTTL = 5 * time.Minute
)

// Cached wraps a Provider with TTL + singleflight stampede control.
// Same shape as forecast/cached and stock/quote/cached.
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
	reading  Reading
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

// Get fetches the AQI for (lat, lon). Cache hits within deadline serve
// the cached value (success or failure). On miss/stale, refresh via
// singleflight. No stale-on-failure serving — AQI staleness is a soft
// concern for callers, who treat any error as "skip the row."
func (c *Cached) Get(ctx context.Context, lat, lon float64) (Reading, error) {
	key := keyFor(lat, lon)
	now := c.now()

	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()

	if ok && now.Before(e.deadline) {
		if e.err != nil {
			return Reading{}, e.err
		}
		if e.hasOK {
			return e.reading, nil
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
		return Reading{}, err
	}

	r := v.(Reading)
	c.mu.Lock()
	c.entries[key] = &entry{reading: r, hasOK: true, deadline: now.Add(c.successTTL)}
	c.mu.Unlock()
	return r, nil
}

// keyFor normalizes (lat, lon) to a 0.1°-grid cache key (~10km cells)
// so visitors in the same neighborhood share an entry.
func keyFor(lat, lon float64) cacheKey {
	return cacheKey{
		lat: math.Round(lat*10) / 10,
		lon: math.Round(lon*10) / 10,
	}
}

// noopProvider returns ErrUnavailable for every call. Pass NoopProvider()
// to weather.Register in tests/dev when the AQI surface is irrelevant —
// the handler treats it as "skip the AQI row" without 5xx-ing.
type noopProvider struct{}

func (noopProvider) Get(_ context.Context, _, _ float64) (Reading, error) {
	return Reading{}, ErrUnavailable
}

// NoopProvider returns a Provider that always errors with ErrUnavailable.
// Used by tests and as a stand-in when the operator wants to disable AQI
// enrichment without touching the handler.
func NoopProvider() Provider { return noopProvider{} }
