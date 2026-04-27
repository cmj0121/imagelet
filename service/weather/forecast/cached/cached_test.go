package cached_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmj0121/imagelet/service/weather/forecast"
	"github.com/cmj0121/imagelet/service/weather/forecast/cached"
)

// stubProvider is a controllable inner provider for tests. It returns the
// preset (f, err) and increments calls atomically. Use setOK / setErr to
// flip behavior between calls.
type stubProvider struct {
	mu    sync.Mutex
	f     forecast.Forecast
	err   error
	delay time.Duration // optional sleep so concurrent callers actually pile up
	calls atomic.Int32
}

func (s *stubProvider) Get(_ context.Context, lat, lon float64, unit forecast.Unit) (forecast.Forecast, error) {
	s.calls.Add(1)
	s.mu.Lock()
	d, f, err := s.delay, s.f, s.err
	s.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
	return f, err
}

func (s *stubProvider) setOK(f forecast.Forecast) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f = f
	s.err = nil
}

func (s *stubProvider) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.f = forecast.Forecast{}
	s.err = err
}

// sampleForecast is a non-zero Forecast used across tests. Location is
// non-empty and Temperature is non-zero so the "is this the cached value
// or a zero value?" check in TestZeroForecastWhenUpstreamFailsPastFailureTTL
// has something to assert against.
func sampleForecast() forecast.Forecast {
	return forecast.Forecast{
		Location:    "Taipei (TW)",
		Temperature: 23.4,
		FeelsLike:   24.1,
		WeatherCode: 2,
		WindSpeed:   12.0,
		IsDay:       true,
		HighToday:   26,
		LowToday:    19,
		TempUnit:    "°C",
		WindUnit:    "km/h",
		AsOf:        time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
	}
}

func TestCacheHitWithinTTL(t *testing.T) {
	stub := &stubProvider{}
	stub.setOK(sampleForecast())
	p := cached.NewWithTTL(stub, 1*time.Hour, 1*time.Hour)

	for i := 0; i < 3; i++ {
		f, err := p.Get(context.Background(), 25.0, 121.5, forecast.UnitMetric)
		if err != nil {
			t.Fatalf("Get #%d: %v", i, err)
		}
		if f.Temperature != 23.4 {
			t.Errorf("Get #%d: Temperature = %v, want 23.4", i, f.Temperature)
		}
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("inner calls = %d, want 1 (cache hit)", got)
	}
}

func TestCacheMissAfterTTL(t *testing.T) {
	stub := &stubProvider{}
	stub.setOK(sampleForecast())
	p := cached.NewWithTTL(stub, 10*time.Millisecond, 1*time.Hour)

	if _, err := p.Get(context.Background(), 25.0, 121.5, forecast.UnitMetric); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // exceed successTTL
	if _, err := p.Get(context.Background(), 25.0, 121.5, forecast.UnitMetric); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("inner calls = %d, want 2 (cache miss after TTL)", got)
	}
}

func TestSingleflightStampede(t *testing.T) {
	// Hold the inner Get for 50ms so all concurrent callers arrive while
	// the first call is still in-flight; without a delay, the first call
	// returns instantly and later callers race against the cache-store +
	// see fresh misses, scoring multiple inner calls.
	stub := &stubProvider{delay: 50 * time.Millisecond}
	stub.setOK(sampleForecast())
	p := cached.NewWithTTL(stub, 1*time.Hour, 1*time.Hour)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, _ = p.Get(context.Background(), 25.0, 121.5, forecast.UnitMetric)
		}()
	}
	close(start)
	wg.Wait()

	// Singleflight should collapse all concurrent misses to 1 inner call.
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("inner calls = %d, want 1 (singleflight collapse)", got)
	}
}

func TestFailureCachedForFailureTTL(t *testing.T) {
	stub := &stubProvider{}
	upstreamErr := errors.New("openmeteo: 500 boom")
	stub.setErr(upstreamErr)
	p := cached.NewWithTTL(stub, 1*time.Hour, 50*time.Millisecond)

	// First Get triggers an upstream call and caches the error.
	if _, err := p.Get(context.Background(), 25.0, 121.5, forecast.UnitMetric); err == nil {
		t.Fatalf("first Get: want err, got nil")
	}
	// Within failureTTL, second Get returns cached error WITHOUT calling inner.
	if _, err := p.Get(context.Background(), 25.0, 121.5, forecast.UnitMetric); err == nil {
		t.Fatalf("second Get: want err, got nil")
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("inner calls = %d, want 1 (cached failure)", got)
	}

	// After failureTTL, the next Get retries.
	time.Sleep(80 * time.Millisecond)
	if _, err := p.Get(context.Background(), 25.0, 121.5, forecast.UnitMetric); err == nil {
		t.Fatalf("third Get: want err, got nil")
	}
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("inner calls after TTL = %d, want 2 (retry)", got)
	}
}

func TestStaleForecastWhenUpstreamFailsWithinFailureTTL(t *testing.T) {
	stub := &stubProvider{}
	stub.setOK(sampleForecast())
	// successTTL=10ms, failureTTL=1h — refresh fires after 20ms, but the
	// cached forecast is still well within the failureTTL window.
	p := cached.NewWithTTL(stub, 10*time.Millisecond, 1*time.Hour)

	// Cache a successful Forecast.
	f, err := p.Get(context.Background(), 25.0, 121.5, forecast.UnitMetric)
	if err != nil {
		t.Fatalf("seed Get: %v", err)
	}
	if f.Temperature != 23.4 {
		t.Fatalf("seed Temperature = %v, want 23.4", f.Temperature)
	}

	// Flip stub to error and exceed successTTL.
	upstreamErr := errors.New("openmeteo: timeout")
	stub.setErr(upstreamErr)
	time.Sleep(20 * time.Millisecond)

	// Stale refresh fails — caller gets the cached Forecast AND the upstream err.
	f, err = p.Get(context.Background(), 25.0, 121.5, forecast.UnitMetric)
	if err == nil {
		t.Fatalf("expected upstream err for stale-failure path, got nil")
	}
	if f.Temperature != 23.4 {
		t.Errorf("stale Forecast Temperature = %v, want cached 23.4", f.Temperature)
	}
	if f.Location != "Taipei (TW)" {
		t.Errorf("stale Forecast Location = %q, want cached %q", f.Location, "Taipei (TW)")
	}
}

func TestZeroForecastWhenUpstreamFailsPastFailureTTL(t *testing.T) {
	stub := &stubProvider{}
	stub.setOK(sampleForecast())
	// Both TTLs short. After 30ms we are past BOTH successTTL and
	// failureTTL — too stale to serve the cached value.
	p := cached.NewWithTTL(stub, 10*time.Millisecond, 20*time.Millisecond)

	// Seed cache with a successful forecast.
	if _, err := p.Get(context.Background(), 25.0, 121.5, forecast.UnitMetric); err != nil {
		t.Fatalf("seed Get: %v", err)
	}

	// Flip stub to error and sleep past failureTTL since last success.
	upstreamErr := errors.New("openmeteo: down")
	stub.setErr(upstreamErr)
	time.Sleep(30 * time.Millisecond)

	f, err := p.Get(context.Background(), 25.0, 121.5, forecast.UnitMetric)
	if err == nil {
		t.Fatalf("expected upstream err for too-stale path, got nil")
	}
	// Forecast must be zero — handler relies on this to trigger 503.
	if f.Location != "" {
		t.Errorf("Forecast.Location = %q, want empty (too-stale forces zero)", f.Location)
	}
	if f.Temperature != 0 {
		t.Errorf("Forecast.Temperature = %v, want 0 (too-stale forces zero)", f.Temperature)
	}
	if f.WeatherCode != 0 {
		t.Errorf("Forecast.WeatherCode = %v, want 0 (too-stale forces zero)", f.WeatherCode)
	}
}

func TestKeyNormalization(t *testing.T) {
	stub := &stubProvider{}
	stub.setOK(sampleForecast())
	p := cached.NewWithTTL(stub, 1*time.Hour, 1*time.Hour)

	// Both round to (25.0, 121.6).
	if _, err := p.Get(context.Background(), 25.046, 121.563, forecast.UnitMetric); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if _, err := p.Get(context.Background(), 25.041, 121.567, forecast.UnitMetric); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("inner calls = %d, want 1 (both round to same cell)", got)
	}
}

func TestUnitInKey(t *testing.T) {
	stub := &stubProvider{}
	stub.setOK(sampleForecast())
	p := cached.NewWithTTL(stub, 1*time.Hour, 1*time.Hour)

	if _, err := p.Get(context.Background(), 25.0, 121.5, forecast.UnitMetric); err != nil {
		t.Fatalf("metric Get: %v", err)
	}
	if _, err := p.Get(context.Background(), 25.0, 121.5, forecast.UnitImperial); err != nil {
		t.Fatalf("imperial Get: %v", err)
	}
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("inner calls = %d, want 2 (different units = different keys)", got)
	}
}
