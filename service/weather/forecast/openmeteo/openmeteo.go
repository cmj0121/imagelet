// Package openmeteo implements forecast.Provider against Open-Meteo's
// /v1/forecast endpoint. The endpoint is open and key-less but rate-limited
// per IP; the cache layer (Unit 2) carries the load. We fetch current
// conditions plus today's daily extremes and sunrise/sunset in one round
// trip, then surface structural mismatches as forecast.ErrUnavailable so
// the caller can back off without serving partial data.
package openmeteo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/cmj0121/imagelet/service/weather/forecast"
)

// defaultEndpoint is Open-Meteo's forecast URL. Override via NewWithEndpoint
// for tests pointed at httptest.Server.
const defaultEndpoint = "https://api.open-meteo.com/v1/forecast"

// userAgent is sent on every request. Open-Meteo doesn't require it, but a
// honest UA helps operators identify our traffic if they need to.
const userAgent = "imagelet/0.2 (+https://github.com/cmj0121/imagelet)"

// timeLayout matches Open-Meteo's ISO-like timestamps without seconds or
// timezone — those come implicitly from the response's timezone field.
const timeLayout = "2006-01-02T15:04"

// Provider satisfies forecast.Provider against Open-Meteo.
type Provider struct {
	endpoint string
	client   *http.Client
}

// New returns a Provider configured against the default Open-Meteo endpoint
// with a 5-second per-request timeout. Use NewWithEndpoint for tests.
func New() *Provider {
	return NewWithEndpoint(defaultEndpoint, &http.Client{Timeout: 5 * time.Second})
}

// NewWithEndpoint returns a Provider with a custom base URL (no query string)
// and HTTP client — primarily for tests pointed at httptest.Server.
func NewWithEndpoint(endpoint string, client *http.Client) *Provider {
	return &Provider{endpoint: endpoint, client: client}
}

// Get fetches the forecast for (lat, lon) in the requested unit. Network
// and HTTP-level errors are returned as-is. Structural problems (empty
// daily series, unparseable timestamps) collapse to forecast.ErrUnavailable.
func (p *Provider) Get(ctx context.Context, lat, lon float64, unit forecast.Unit) (forecast.Forecast, error) {
	tempUnit, windUnit := upstreamUnits(unit)

	// Build params explicitly so test assertions can introspect the request.
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(lat, 'f', 4, 64))
	q.Set("longitude", strconv.FormatFloat(lon, 'f', 4, 64))
	q.Set("current", "temperature_2m,relative_humidity_2m,apparent_temperature,weather_code,wind_speed_10m,is_day")
	q.Set("daily", "temperature_2m_max,temperature_2m_min,sunrise,sunset,uv_index_max,precipitation_probability_max")
	q.Set("temperature_unit", tempUnit)
	q.Set("wind_speed_unit", windUnit)
	q.Set("timezone", "auto")
	q.Set("forecast_days", "2")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return forecast.Forecast{}, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return forecast.Forecast{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return forecast.Forecast{}, fmt.Errorf("openmeteo: %s: %s", resp.Status, body)
	}

	var raw forecastResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return forecast.Forecast{}, err
	}

	// An empty daily series means the upstream has no data for "today" — we
	// can't render high/low/sunrise/sunset, so treat the whole response as
	// unavailable rather than returning a half-filled Forecast.
	if len(raw.Daily.Time) == 0 ||
		len(raw.Daily.TempMax) == 0 || len(raw.Daily.TempMin) == 0 ||
		len(raw.Daily.Sunrise) == 0 || len(raw.Daily.Sunset) == 0 {
		return forecast.Forecast{}, forecast.ErrUnavailable
	}

	// Open-Meteo returns local-wall timestamps (no offset) interpreted in the
	// response's timezone field. Load that zone once and parse all three
	// times against it so AsOf, Sunrise, Sunset are zone-correct.
	loc, err := time.LoadLocation(raw.Timezone)
	if err != nil {
		// Fallback to a fixed offset if the runtime tzdata is missing — rare
		// on Linux but possible in stripped containers.
		loc = time.FixedZone(raw.Timezone, raw.UTCOffsetSeconds)
	}

	asOf, err := time.ParseInLocation(timeLayout, raw.Current.Time, loc)
	if err != nil {
		return forecast.Forecast{}, forecast.ErrUnavailable
	}
	sunrise, err := time.ParseInLocation(timeLayout, raw.Daily.Sunrise[0], loc)
	if err != nil {
		return forecast.Forecast{}, forecast.ErrUnavailable
	}
	sunset, err := time.ParseInLocation(timeLayout, raw.Daily.Sunset[0], loc)
	if err != nil {
		return forecast.Forecast{}, forecast.ErrUnavailable
	}

	// Optional daily extras — empty arrays mean the upstream skipped the
	// field for this run; surface zero values so the caller can branch on
	// "0 means absent" via the Has* helpers in render path.
	uvMax := 0.0
	if len(raw.Daily.UVIndexMax) > 0 {
		uvMax = raw.Daily.UVIndexMax[0]
	}
	precipProb := 0
	if len(raw.Daily.PrecipProbabilityMax) > 0 {
		precipProb = raw.Daily.PrecipProbabilityMax[0]
	}

	return forecast.Forecast{
		Temperature:       raw.Current.Temperature,
		FeelsLike:         raw.Current.ApparentTemperature,
		Humidity:          raw.Current.RelativeHumidity,
		WeatherCode:       raw.Current.WeatherCode,
		WindSpeed:         raw.Current.WindSpeed,
		IsDay:             raw.Current.IsDay == 1,
		HighToday:         raw.Daily.TempMax[0],
		LowToday:          raw.Daily.TempMin[0],
		UVIndexMax:        uvMax,
		PrecipProbability: precipProb,
		Sunrise:           sunrise,
		Sunset:            sunset,
		TempUnit:          raw.CurrentUnits.Temperature,
		WindUnit:          normalizeWindUnit(raw.CurrentUnits.WindSpeed),
		AsOf:              asOf,
	}, nil
}

// upstreamUnits maps our Unit vocabulary onto Open-Meteo's query params.
// Anything other than imperial falls back to metric — defensive default for
// future Unit values that haven't taught the provider yet.
func upstreamUnits(u forecast.Unit) (temp, wind string) {
	if u == forecast.UnitImperial {
		return "fahrenheit", "mph"
	}
	return "celsius", "kmh"
}

// normalizeWindUnit collapses Open-Meteo's "mp/h" reply into the canonical
// "mph" we promise in Forecast. Other values pass through verbatim.
func normalizeWindUnit(s string) string {
	if s == "mp/h" {
		return "mph"
	}
	return s
}

// forecastResponse mirrors the relevant subset of Open-Meteo's JSON shape.
// We intentionally don't unmarshal everything — fewer fields = less to
// break when the upstream wobbles.
type forecastResponse struct {
	Timezone         string `json:"timezone"`
	UTCOffsetSeconds int    `json:"utc_offset_seconds"`
	CurrentUnits     struct {
		Temperature string `json:"temperature_2m"`
		WindSpeed   string `json:"wind_speed_10m"`
	} `json:"current_units"`
	Current struct {
		Time                string  `json:"time"`
		Temperature         float64 `json:"temperature_2m"`
		RelativeHumidity    int     `json:"relative_humidity_2m"`
		ApparentTemperature float64 `json:"apparent_temperature"`
		WeatherCode         int     `json:"weather_code"`
		WindSpeed           float64 `json:"wind_speed_10m"`
		IsDay               int     `json:"is_day"`
	} `json:"current"`
	Daily struct {
		Time                 []string  `json:"time"`
		TempMax              []float64 `json:"temperature_2m_max"`
		TempMin              []float64 `json:"temperature_2m_min"`
		Sunrise              []string  `json:"sunrise"`
		Sunset               []string  `json:"sunset"`
		UVIndexMax           []float64 `json:"uv_index_max"`
		PrecipProbabilityMax []int     `json:"precipitation_probability_max"`
	} `json:"daily"`
}
