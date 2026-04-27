package airquality_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmj0121/imagelet/service/weather/airquality"
)

func TestCategoryFor(t *testing.T) {
	cases := []struct {
		aqi  int
		want string
	}{
		{0, "Good"},
		{50, "Good"},
		{51, "Moderate"},
		{100, "Moderate"},
		{101, "USG"},
		{150, "USG"},
		{151, "Unhealthy"},
		{200, "Unhealthy"},
		{201, "Very Unhealthy"},
		{300, "Very Unhealthy"},
		{301, "Hazardous"},
		{500, "Hazardous"},
		{-5, "Good"}, // defensive negative collapses to Good
	}
	for _, tc := range cases {
		if got := airquality.CategoryFor(tc.aqi); got != tc.want {
			t.Errorf("CategoryFor(%d) = %q, want %q", tc.aqi, got, tc.want)
		}
	}
}

func TestHTTPProviderGetSuccess(t *testing.T) {
	body, err := os.ReadFile("testdata/aqi.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("current"); got != "us_aqi" {
			t.Errorf("query[current] = %q, want %q", got, "us_aqi")
		}
		if got := r.URL.Query().Get("latitude"); got != "25.0400" {
			t.Errorf("query[latitude] = %q, want %q", got, "25.0400")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := airquality.NewWithEndpoint(srv.URL, srv.Client())
	got, err := p.Get(context.Background(), 25.04, 121.56)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.USAQI != 74 {
		t.Errorf("USAQI = %d, want 74", got.USAQI)
	}
	if got.Category != "Moderate" {
		t.Errorf("Category = %q, want %q", got.Category, "Moderate")
	}
}

func TestHTTPProviderGetMissingField(t *testing.T) {
	body, err := os.ReadFile("testdata/aqi_missing.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := airquality.NewWithEndpoint(srv.URL, srv.Client())
	_, err = p.Get(context.Background(), 0, 0)
	if !errors.Is(err, airquality.ErrUnavailable) {
		t.Errorf("missing us_aqi err = %v, want ErrUnavailable", err)
	}
}

func TestHTTPProviderGet5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	p := airquality.NewWithEndpoint(srv.URL, srv.Client())
	_, err := p.Get(context.Background(), 0, 0)
	if err == nil {
		t.Error("expected non-nil error for 502")
	}
}

// stubProvider returns a canned (Reading, error) and counts calls so
// Cached's TTL/singleflight tests can assert call coalescing.
type stubProvider struct {
	r     airquality.Reading
	err   error
	calls atomic.Int64
	delay time.Duration
}

func (s *stubProvider) Get(_ context.Context, _, _ float64) (airquality.Reading, error) {
	s.calls.Add(1)
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return s.r, s.err
}

func TestCachedHitWithinTTL(t *testing.T) {
	stub := &stubProvider{r: airquality.Reading{USAQI: 42, Category: "Good"}}
	c := airquality.NewCachedWithTTL(stub, 10*time.Millisecond, 10*time.Millisecond)

	for i := 0; i < 3; i++ {
		got, err := c.Get(context.Background(), 25.04, 121.56)
		if err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
		if got.USAQI != 42 {
			t.Errorf("Get %d USAQI = %d, want 42", i, got.USAQI)
		}
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("upstream called %d times, want 1 (TTL hit)", got)
	}
}

func TestCachedSingleflightStampede(t *testing.T) {
	stub := &stubProvider{
		r:     airquality.Reading{USAQI: 99, Category: "Good"},
		delay: 30 * time.Millisecond,
	}
	c := airquality.NewCachedWithTTL(stub, time.Second, time.Second)

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = c.Get(context.Background(), 25.04, 121.56)
		}()
	}
	wg.Wait()
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("upstream called %d times, want 1 (singleflight)", got)
	}
}

func TestCachedFailureWithinTTL(t *testing.T) {
	stub := &stubProvider{err: errors.New("boom")}
	c := airquality.NewCachedWithTTL(stub, time.Second, 10*time.Millisecond)

	if _, err := c.Get(context.Background(), 0, 0); err == nil {
		t.Fatal("expected error first call")
	}
	if _, err := c.Get(context.Background(), 0, 0); err == nil {
		t.Fatal("expected cached error second call")
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("upstream called %d times, want 1 (failure cached)", got)
	}
}

func TestNoopProviderAlwaysErrUnavailable(t *testing.T) {
	if _, err := airquality.NoopProvider().Get(context.Background(), 0, 0); !errors.Is(err, airquality.ErrUnavailable) {
		t.Errorf("Noop err = %v, want ErrUnavailable", err)
	}
}
