package earthquake_test

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

	"github.com/cmj0121/imagelet/service/weather/earthquake"
)

func TestHTTPProviderGetSuccess(t *testing.T) {
	body, err := os.ReadFile("testdata/yilan.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// Pin the request shape so a future regression in HTTPProvider
		// surfaces here rather than at the upstream.
		want := map[string]string{
			"format":       "geojson",
			"latitude":     "23.5000",
			"longitude":    "121.0000",
			"maxradiuskm":  "300",
			"minmagnitude": "4.0",
			"orderby":      "time",
			"limit":        "1",
		}
		for k, v := range want {
			if got := q.Get(k); got != v {
				t.Errorf("query[%q] = %q, want %q", k, got, v)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := earthquake.NewWithEndpoint(srv.URL, srv.Client())
	got, err := p.Get(context.Background(), 23.5, 121.0)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Magnitude != 4.1 {
		t.Errorf("Magnitude = %v, want 4.1", got.Magnitude)
	}
	if got.Place != "9 km ENE of Yilan, Taiwan" {
		t.Errorf("Place = %q, want %q", got.Place, "9 km ENE of Yilan, Taiwan")
	}
	if got.ID != "us6000sss5" {
		t.Errorf("ID = %q, want %q", got.ID, "us6000sss5")
	}
	wantTime := time.UnixMilli(1777101394020).UTC()
	if !got.Time.Equal(wantTime) {
		t.Errorf("Time = %v, want %v", got.Time, wantTime)
	}
}

func TestHTTPProviderGetEmptyFeatures(t *testing.T) {
	body, err := os.ReadFile("testdata/empty.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	p := earthquake.NewWithEndpoint(srv.URL, srv.Client())
	_, err = p.Get(context.Background(), 0, 0)
	if !errors.Is(err, earthquake.ErrUnavailable) {
		t.Errorf("empty features err = %v, want ErrUnavailable", err)
	}
}

func TestHTTPProviderGet5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := earthquake.NewWithEndpoint(srv.URL, srv.Client())
	if _, err := p.Get(context.Background(), 0, 0); err == nil {
		t.Error("expected non-nil error for 500")
	}
}

type stubProvider struct {
	ev    earthquake.Event
	err   error
	calls atomic.Int64
	delay time.Duration
}

func (s *stubProvider) Get(_ context.Context, _, _ float64) (earthquake.Event, error) {
	s.calls.Add(1)
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	return s.ev, s.err
}

func TestCachedHitWithinTTL(t *testing.T) {
	stub := &stubProvider{ev: earthquake.Event{Magnitude: 5.2, Place: "Taiwan"}}
	c := earthquake.NewCachedWithTTL(stub, 10*time.Millisecond, 10*time.Millisecond)

	for i := 0; i < 3; i++ {
		got, err := c.Get(context.Background(), 23.5, 121.0)
		if err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
		if got.Magnitude != 5.2 {
			t.Errorf("Get %d Magnitude = %v, want 5.2", i, got.Magnitude)
		}
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("upstream called %d times, want 1 (TTL hit)", got)
	}
}

func TestCachedSingleflightStampede(t *testing.T) {
	stub := &stubProvider{
		ev:    earthquake.Event{Magnitude: 6.0, Place: "Honshu"},
		delay: 30 * time.Millisecond,
	}
	c := earthquake.NewCachedWithTTL(stub, time.Second, time.Second)

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = c.Get(context.Background(), 23.5, 121.0)
		}()
	}
	wg.Wait()
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("upstream called %d times, want 1 (singleflight)", got)
	}
}

func TestCachedFailureWithinTTL(t *testing.T) {
	stub := &stubProvider{err: errors.New("boom")}
	c := earthquake.NewCachedWithTTL(stub, time.Second, 10*time.Millisecond)

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
	if _, err := earthquake.NoopProvider().Get(context.Background(), 0, 0); !errors.Is(err, earthquake.ErrUnavailable) {
		t.Errorf("Noop err = %v, want ErrUnavailable", err)
	}
}
