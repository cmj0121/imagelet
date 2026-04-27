package weather_test

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/service/weather"
	"github.com/cmj0121/imagelet/service/weather/airquality"
	"github.com/cmj0121/imagelet/service/weather/earthquake"
	"github.com/cmj0121/imagelet/service/weather/forecast"
)

// pngMagic is the 8-byte PNG file signature.
var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// fakeProvider returns a canned (Forecast, error) pair from every Get call.
type fakeProvider struct {
	f   forecast.Forecast
	err error
}

func (p fakeProvider) Get(_ context.Context, _, _ float64, _ forecast.Unit) (forecast.Forecast, error) {
	return p.f, p.err
}

// spyProvider records every (lat, lon, unit) it was called with so tests
// can assert that location resolution and unit selection actually flowed
// through the handler.
type spyProvider struct {
	mu    sync.Mutex
	f     forecast.Forecast
	err   error
	calls []spyCall
}

type spyCall struct {
	lat, lon float64
	unit     forecast.Unit
}

func (s *spyProvider) Get(_ context.Context, lat, lon float64, unit forecast.Unit) (forecast.Forecast, error) {
	s.mu.Lock()
	s.calls = append(s.calls, spyCall{lat, lon, unit})
	s.mu.Unlock()
	return s.f, s.err
}

func (s *spyProvider) last() (spyCall, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		return spyCall{}, false
	}
	return s.calls[len(s.calls)-1], true
}

// freshForecast returns a deterministic Forecast in Asia/Taipei. Sample
// values match PLAN: 24.3C, feels 23.0, code 2 (partly cloudy), wind 12,
// high 26 / low 19, fixed sunrise/sunset times. Humidity/UV/precip set
// so the extras-line is rendered (omitted-when-all-zero is exercised by
// TestExtrasLineOmittedWhenAbsent).
func freshForecast() forecast.Forecast {
	loc, _ := time.LoadLocation("Asia/Taipei")
	return forecast.Forecast{
		Temperature:       24.3,
		FeelsLike:         23.0,
		Humidity:          47,
		WeatherCode:       2,
		WindSpeed:         12.0,
		IsDay:             true,
		HighToday:         26.0,
		LowToday:          19.0,
		UVIndexMax:        9.0,
		PrecipProbability: 20,
		Sunrise:           time.Date(2026, 4, 26, 5, 42, 0, 0, loc),
		Sunset:            time.Date(2026, 4, 26, 18, 24, 0, 0, loc),
		TempUnit:          "°C",
		WindUnit:          "km/h",
		AsOf:              time.Date(2026, 4, 26, 12, 0, 0, 0, loc),
	}
}

// fakeAQI is a deterministic airquality.Provider for tests.
type fakeAQI struct {
	r   airquality.Reading
	err error
}

func (a fakeAQI) Get(_ context.Context, _, _ float64) (airquality.Reading, error) {
	return a.r, a.err
}

// fakeQuake is a deterministic earthquake.Provider for tests.
type fakeQuake struct {
	ev  earthquake.Event
	err error
}

func (q fakeQuake) Get(_ context.Context, _, _ float64) (earthquake.Event, error) {
	return q.ev, q.err
}

// freshAQI returns the stock AQI sample (74 / Moderate) the tests assert against.
func freshAQI() airquality.Reading {
	return airquality.Reading{USAQI: 74, Category: "Moderate"}
}

// freshQuake returns a fixed M4.1 Yilan event 3h before AsOf so the
// caption row formats deterministically across test runs.
func freshQuake() earthquake.Event {
	return earthquake.Event{
		Magnitude: 4.1,
		Place:     "9 km ENE of Yilan, Taiwan",
		Time:      time.Now().Add(-3 * time.Hour),
		ID:        "us6000sss5",
	}
}

// newRouter wires the same middlewares production will, scoped to /weather.
// Enrichment providers default to fresh samples so happy-path tests see
// every row; specific tests override via newRouterFull when they need
// to exercise the failure paths.
func newRouter(p forecast.Provider) *gin.Engine {
	return newRouterFull(p, fakeAQI{r: freshAQI()}, fakeQuake{ev: freshQuake()})
}

// newRouterFull is newRouter with explicit enrichment providers for tests
// that need to pin specific behavior (success / failure / noop).
func newRouterFull(p forecast.Provider, aq airquality.Provider, eq earthquake.Provider) *gin.Engine {
	r := gin.New()
	r.Use(middleware.RegionDetector())
	r.Use(middleware.ClientDetector())
	weather.Register(r, p, aq, eq)
	return r
}

func TestServeASCII(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{f: freshForecast()})

	req := httptest.NewRequest(http.MethodGet, "/weather", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q, want prefix text/plain", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=600" {
		t.Errorf("Cache-Control = %q, want %q", got, "public, max-age=600")
	}
	body := rec.Body.String()
	for _, want := range []string{"Taipei", "feels", "wind", "high", "low", "°C",
		"humidity 47%", "UV 9", "rain 20%",
		"AQI 74 (Moderate)",
		"M4.1 quake 9 km ENE of Yilan, Taiwan",
		"day ", "5:42-18:24"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
	if strings.Contains(body, "STALE") {
		t.Errorf("fresh forecast must not show STALE\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, "│") {
		t.Errorf("body missing pylon banner frame char │\n--- body ---\n%s", body)
	}
}

func TestServeBrowserGetsPNG(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{f: freshForecast()})

	req := httptest.NewRequest(http.MethodGet, "/weather", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	body := rec.Body.Bytes()
	if !bytes.HasPrefix(body, pngMagic) {
		head := body
		if len(head) > 8 {
			head = head[:8]
		}
		t.Fatalf("body missing PNG magic bytes; first 8 = %x", head)
	}
	img, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	w := img.Bounds().Dx()
	h := img.Bounds().Dy()
	if w < 200 {
		t.Errorf("png width = %d, want >= 200 (icon + banner side-by-side)", w)
	}
	// Captions row adds at least 4*13 = 52 px under the banner; banner
	// alone is ~140 px tall. >=180 is a safe lower bound.
	if h < 180 {
		t.Errorf("png height = %d, want >= 180 (banner + captions row)", h)
	}
}

func TestServeAcceptPylonReturnsBareBannerSource(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{f: freshForecast()})

	req := httptest.NewRequest(http.MethodGet, "/weather", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("Accept", "text/pylon")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/pylon") {
		t.Errorf("Content-Type = %q, want prefix text/pylon", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "[ 24.3 | banner:mini ]") {
		t.Errorf("body missing bare banner source\n--- body ---\n%s", body)
	}
	for _, unwanted := range []string{"feels", "wind", "sunrise", "Taipei", "STALE"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("pylon source must not contain %q\n--- body ---\n%s", unwanted, body)
		}
	}
}

func TestServeStalePrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{f: freshForecast(), err: errors.New("upstream")})

	req := httptest.NewRequest(http.MethodGet, "/weather", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (stale-with-data must not 5xx)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "STALE ·") {
		t.Errorf("body missing STALE prefix\n--- body ---\n%s", rec.Body.String())
	}
}

func TestServeUnavailableWhenNoCacheAndError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{err: forecast.ErrUnavailable})

	req := httptest.NewRequest(http.MethodGet, "/weather", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != "60" {
		t.Errorf("Retry-After = %q, want %q", got, "60")
	}
	if got := rec.Body.String(); got != "weather unavailable\n" {
		t.Errorf("body = %q, want %q", got, "weather unavailable\n")
	}
}

func TestRegionFallbackWhenNoLatLon(t *testing.T) {
	gin.SetMode(gin.TestMode)
	spy := &spyProvider{f: freshForecast()}
	r := newRouter(spy)

	req := httptest.NewRequest(http.MethodGet, "/weather", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "JP")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got, ok := spy.last()
	if !ok {
		t.Fatal("provider was never called")
	}
	if got.lat != 35.68 || got.lon != 139.69 {
		t.Errorf("provider got (lat, lon) = (%v, %v), want (35.68, 139.69) [Tokyo]", got.lat, got.lon)
	}
}

func TestQueryLatLonOverridesCFHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)
	spy := &spyProvider{f: freshForecast()}
	r := newRouter(spy)

	req := httptest.NewRequest(http.MethodGet, "/weather?lat=51.51&lon=-0.13", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPLatitude", "25.04")
	req.Header.Set("CF-IPLongitude", "121.56")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	got, ok := spy.last()
	if !ok {
		t.Fatal("provider was never called")
	}
	if got.lat != 51.51 {
		t.Errorf("provider got lat = %v, want 51.51 (?lat= must beat CF-IPLatitude)", got.lat)
	}
}

func TestQueryLatLonInvalidFallsThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	spy := &spyProvider{f: freshForecast()}
	r := newRouter(spy)

	req := httptest.NewRequest(http.MethodGet, "/weather?lat=200&lon=foo", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (bad coords must never 4xx)", rec.Code)
	}
	got, ok := spy.last()
	if !ok {
		t.Fatal("provider was never called")
	}
	if got.lat != 25.04 || got.lon != 121.56 {
		t.Errorf("provider got (lat, lon) = (%v, %v), want (25.04, 121.56) [Taipei fallback]", got.lat, got.lon)
	}
}

func TestUnitFallbackByCountry(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name    string
		path    string
		country string
		want    forecast.Unit
	}{
		{"US_imperial", "/weather", "US", forecast.UnitImperial},
		{"TW_metric", "/weather", "TW", forecast.UnitMetric},
		{"explicit_f_overrides_metric", "/weather?unit=f", "TW", forecast.UnitImperial},
		{"explicit_c_overrides_imperial", "/weather?unit=c", "US", forecast.UnitMetric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := &spyProvider{f: freshForecast()}
			r := newRouter(spy)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("User-Agent", "curl/8.4.0")
			req.Header.Set("CF-IPCountry", tc.country)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			got, ok := spy.last()
			if !ok {
				t.Fatal("provider was never called")
			}
			if got.unit != tc.want {
				t.Errorf("provider got unit = %q, want %q", got.unit, tc.want)
			}
		})
	}
}

// TestSTALEWinsOverNothing pins the negotiation order: STALE prefix is a
// rendered-caption concept, never bleeds into the bare pylon source. Even
// when serving stale data, ?Accept: text/pylon must return only the bare
// banner source.
func TestSTALEWinsOverNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{f: freshForecast(), err: errors.New("upstream")})

	req := httptest.NewRequest(http.MethodGet, "/weather", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("Accept", "text/pylon")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "[ 24.3 | banner:mini ]") {
		t.Errorf("body missing bare banner source\n--- body ---\n%s", body)
	}
	if strings.Contains(body, "STALE") {
		t.Errorf("STALE leaked into pylon source\n--- body ---\n%s", body)
	}
}

// TestAQIFailureOmitsRow confirms an airquality upstream error drops the
// AQI caption row but never 5xx-es the base render — AQI is enrichment,
// not a hard dependency.
func TestAQIFailureOmitsRow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouterFull(fakeProvider{f: freshForecast()},
		fakeAQI{err: airquality.ErrUnavailable},
		fakeQuake{ev: freshQuake()})

	req := httptest.NewRequest(http.MethodGet, "/weather", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (AQI failure must not 5xx)", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "AQI") {
		t.Errorf("AQI row leaked despite upstream error\n--- body ---\n%s", body)
	}
	// Sanity: forecast captions still render.
	if !strings.Contains(body, "feels") || !strings.Contains(body, "day ") {
		t.Errorf("base captions missing — AQI failure should not have dropped them\n--- body ---\n%s", body)
	}
}

// TestQuakeFailureOmitsRow confirms an earthquake upstream error drops
// the quake caption row but never 5xx-es the base render. Mirrors the
// AQI-failure contract: enrichment surfaces are best-effort.
func TestQuakeFailureOmitsRow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouterFull(fakeProvider{f: freshForecast()},
		fakeAQI{r: freshAQI()},
		fakeQuake{err: earthquake.ErrUnavailable})

	req := httptest.NewRequest(http.MethodGet, "/weather", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (quake failure must not 5xx)", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "quake") || strings.Contains(body, "M4.1") {
		t.Errorf("quake row leaked despite upstream error\n--- body ---\n%s", body)
	}
	// Sanity: AQI + base captions still render.
	if !strings.Contains(body, "AQI 74") || !strings.Contains(body, "feels") {
		t.Errorf("base/AQI captions missing — quake failure should not have dropped them\n--- body ---\n%s", body)
	}
}

// TestExtrasLineOmittedWhenAbsent confirms the humidity/UV/precip caption
// row is dropped entirely when the upstream supplied none of the three —
// a row of three empty segments would look broken, an absent row reads
// as "Open-Meteo skipped these today" without leaking the implementation.
func TestExtrasLineOmittedWhenAbsent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	f := freshForecast()
	f.Humidity = 0
	f.UVIndexMax = 0
	f.PrecipProbability = 0
	r := newRouter(fakeProvider{f: f})

	req := httptest.NewRequest(http.MethodGet, "/weather", nil)
	req.Header.Set("User-Agent", "curl/8.4.0")
	req.Header.Set("CF-IPCountry", "TW")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, unwanted := range []string{"humidity", "UV", "rain"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("absent extras row leaked %q\n--- body ---\n%s", unwanted, body)
		}
	}
	// Sanity: the still-required rows are intact (day-cycle replaces the
	// standalone sunrise/sunset row).
	for _, want := range []string{"feels", "high", "day "} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n--- body ---\n%s", want, body)
		}
	}
}
