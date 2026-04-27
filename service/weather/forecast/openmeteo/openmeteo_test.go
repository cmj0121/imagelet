package openmeteo_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/cmj0121/imagelet/service/weather/forecast"
	"github.com/cmj0121/imagelet/service/weather/forecast/openmeteo"
)

// fixtureServer returns an httptest.Server that serves the named fixture
// from testdata/. Status overrides the response code when non-zero.
func fixtureServer(t *testing.T, name string, status int) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = w.Write(body)
	}))
}

func newProvider(srv *httptest.Server) *openmeteo.Provider {
	return openmeteo.NewWithEndpoint(srv.URL, srv.Client())
}

func TestGetTaipei(t *testing.T) {
	srv := fixtureServer(t, "taipei.json", 0)
	defer srv.Close()

	f, err := newProvider(srv).Get(context.Background(), 25.04, 121.56, forecast.UnitMetric)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if f.Temperature != 19.5 {
		t.Errorf("Temperature = %v, want 19.5", f.Temperature)
	}
	if f.FeelsLike != 20.7 {
		t.Errorf("FeelsLike = %v, want 20.7", f.FeelsLike)
	}
	if f.WeatherCode != 0 {
		t.Errorf("WeatherCode = %v, want 0", f.WeatherCode)
	}
	if f.WindSpeed != 9.0 {
		t.Errorf("WindSpeed = %v, want 9.0", f.WindSpeed)
	}
	if f.IsDay {
		t.Errorf("IsDay = true, want false (fixture is_day=0)")
	}
	if f.HighToday != 27.6 {
		t.Errorf("HighToday = %v, want 27.6", f.HighToday)
	}
	if f.LowToday != 17.5 {
		t.Errorf("LowToday = %v, want 17.5", f.LowToday)
	}
	if f.TempUnit != "°C" {
		t.Errorf("TempUnit = %q, want %q", f.TempUnit, "°C")
	}
	if f.WindUnit != "km/h" {
		t.Errorf("WindUnit = %q, want %q", f.WindUnit, "km/h")
	}

	// AsOf, Sunrise, Sunset should land in Asia/Taipei (UTC+8).
	if zone, _ := f.AsOf.Zone(); zone != "CST" && zone != "GMT+8" {
		// Asia/Taipei abbreviates as CST in Go's tzdata; tolerate either.
		t.Logf("AsOf zone = %q (acceptable: CST/GMT+8)", zone)
	}
	if got := f.AsOf.Format("2006-01-02T15:04"); got != "2026-04-26T20:15" {
		t.Errorf("AsOf wall-clock = %q, want 2026-04-26T20:15", got)
	}
	if got := f.Sunrise.Format("2006-01-02T15:04"); got != "2026-04-26T05:22" {
		t.Errorf("Sunrise wall-clock = %q, want 2026-04-26T05:22", got)
	}
	if got := f.Sunset.Format("2006-01-02T15:04"); got != "2026-04-26T18:21" {
		t.Errorf("Sunset wall-clock = %q, want 2026-04-26T18:21", got)
	}
	// UTC offset should be +08:00.
	if _, off := f.Sunrise.Zone(); off != 8*3600 {
		t.Errorf("Sunrise UTC offset = %d, want %d", off, 8*3600)
	}

	// Provider layer leaves Location empty — handler builds it.
	if f.Location != "" {
		t.Errorf("Location = %q, want empty (handler-built)", f.Location)
	}
}

func TestGetNYCImperial(t *testing.T) {
	srv := fixtureServer(t, "nyc.json", 0)
	defer srv.Close()

	f, err := newProvider(srv).Get(context.Background(), 40.71, -74.01, forecast.UnitImperial)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if f.TempUnit != "°F" {
		t.Errorf("TempUnit = %q, want %q", f.TempUnit, "°F")
	}
	if f.WindUnit != "mph" {
		t.Errorf("WindUnit = %q, want %q (normalized from upstream mp/h)", f.WindUnit, "mph")
	}
	if f.Temperature != 43.9 {
		t.Errorf("Temperature = %v, want 43.9", f.Temperature)
	}
	if f.WindSpeed != 8.2 {
		t.Errorf("WindSpeed = %v, want 8.2", f.WindSpeed)
	}
	if !f.IsDay {
		t.Errorf("IsDay = false, want true (fixture is_day=1)")
	}
	if f.HighToday != 51.2 {
		t.Errorf("HighToday = %v, want 51.2", f.HighToday)
	}
	if f.LowToday != 41.3 {
		t.Errorf("LowToday = %v, want 41.3", f.LowToday)
	}
	// America/New_York is UTC-4 (EDT) on 2026-04-26.
	if _, off := f.AsOf.Zone(); off != -4*3600 {
		t.Errorf("AsOf UTC offset = %d, want %d", off, -4*3600)
	}
}

func TestGetEmptyDailyIsErrUnavailable(t *testing.T) {
	srv := fixtureServer(t, "empty.json", 0)
	defer srv.Close()

	_, err := newProvider(srv).Get(context.Background(), 0, 0, forecast.UnitMetric)
	if !errors.Is(err, forecast.ErrUnavailable) {
		t.Errorf("err = %v, want forecast.ErrUnavailable", err)
	}
}

func TestGetUpstream500ReturnsError(t *testing.T) {
	srv := fixtureServer(t, "taipei.json", http.StatusInternalServerError)
	defer srv.Close()

	_, err := newProvider(srv).Get(context.Background(), 25.04, 121.56, forecast.UnitMetric)
	if err == nil {
		t.Fatalf("err is nil; want non-nil for HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err %q does not mention 500 status", err)
	}
}

func TestGetSendsRightParams(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		// Serve a valid Taipei body so the call succeeds end-to-end.
		body, err := os.ReadFile("testdata/taipei.json")
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	if _, err := newProvider(srv).Get(context.Background(), 25.04, 121.56, forecast.UnitMetric); err != nil {
		t.Fatalf("Get: %v", err)
	}

	want := map[string]string{
		"latitude":         "25.0400",
		"longitude":        "121.5600",
		"current":          "temperature_2m,apparent_temperature,weather_code,wind_speed_10m,is_day",
		"daily":            "temperature_2m_max,temperature_2m_min,sunrise,sunset",
		"temperature_unit": "celsius",
		"wind_speed_unit":  "kmh",
		"timezone":         "auto",
		"forecast_days":    "2",
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("query[%q] = %q, want %q", k, got.Get(k), v)
		}
	}
}

func TestGetImperialSendsCorrectUnits(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		body, err := os.ReadFile("testdata/nyc.json")
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	if _, err := newProvider(srv).Get(context.Background(), 40.71, -74.01, forecast.UnitImperial); err != nil {
		t.Fatalf("Get: %v", err)
	}

	if v := got.Get("temperature_unit"); v != "fahrenheit" {
		t.Errorf("temperature_unit = %q, want fahrenheit", v)
	}
	if v := got.Get("wind_speed_unit"); v != "mph" {
		t.Errorf("wind_speed_unit = %q, want mph", v)
	}
}
