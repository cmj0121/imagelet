// Package forecast provides today's-weather retrieval for the /weather service.
//
// Forecast is the value type — current temp, feels-like, WMO weather code,
// wind, daylight flag, today's high/low, sunrise/sunset, units, and asOf.
// Provider is the contract; Get returns (Forecast, error). Implementations
// live in subpackages: openmeteo (calls Open-Meteo's /v1/forecast endpoint)
// and (later) cached (wraps any Provider with TTL + singleflight stampede
// control).
package forecast

import (
	"context"
	"errors"
	"time"
)

// Forecast captures the fields the /weather handler renders. The provider
// layer leaves Location empty — the weather service handler builds the
// human-readable string ("Taipei (TW)") from Cloudflare geo headers with a
// fallback lookup map.
type Forecast struct {
	Location          string    // human-readable place name (e.g., "Taipei (TW)"); empty from upstream providers
	Temperature       float64   // current temperature
	FeelsLike         float64   // apparent temperature
	Humidity          int       // current relative humidity, 0..100; 0 means upstream omitted
	WeatherCode       int       // Open-Meteo WMO weather code
	WindSpeed         float64   // current wind speed in chosen unit
	IsDay             bool      // daylight at observation time
	HighToday         float64   // today's high
	LowToday          float64   // today's low
	UVIndexMax        float64   // today's UV index peak; 0 means upstream omitted
	PrecipProbability int       // today's max precipitation probability, 0..100
	Sunrise           time.Time // local-zone sunrise for today
	Sunset            time.Time // local-zone sunset for today
	TempUnit          string    // "°C" or "°F"
	WindUnit          string    // "km/h" or "mph"
	AsOf              time.Time // observation time (current.time from Open-Meteo)
}

// Unit selects the unit system requested from the upstream. The provider
// translates this into the upstream's parameter vocabulary
// (celsius/fahrenheit, kmh/mph).
type Unit string

const (
	UnitMetric   Unit = "metric"   // Celsius + km/h
	UnitImperial Unit = "imperial" // Fahrenheit + mph
)

// Provider is implemented by anything that can fetch a Forecast for
// (lat, lon, unit). Get must respect ctx cancellation. ErrUnavailable
// indicates the upstream is reachable but doesn't have data for the
// location; transport errors are returned as-is so the caller / cache
// layer can decide on retry and TTL.
type Provider interface {
	Get(ctx context.Context, lat, lon float64, unit Unit) (Forecast, error)
}

// ErrUnavailable signals "the provider answered but has no data for these
// coordinates" — distinct from transport errors. Cached providers may
// treat ErrUnavailable as an extended-TTL failure to avoid hammering the
// upstream when a coordinate is permanently empty.
var ErrUnavailable = errors.New("forecast unavailable")
