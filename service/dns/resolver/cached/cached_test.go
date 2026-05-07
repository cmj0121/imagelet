package cached_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmj0121/imagelet/service/dns/resolver"
	"github.com/cmj0121/imagelet/service/dns/resolver/cached"
)

// stubResolver is a controllable resolver.Resolver. Returns the preset
// (records, err) and increments calls atomically.
type stubResolver struct {
	mu    sync.Mutex
	rec   resolver.Records
	err   error
	delay time.Duration
	calls atomic.Int32
}

func (s *stubResolver) Lookup(_ context.Context, host string) (resolver.Records, error) {
	s.calls.Add(1)
	s.mu.Lock()
	d, r, err := s.delay, s.rec, s.err
	s.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
	if r.Hostname == "" && err == nil {
		r.Hostname = host
	}
	return r, err
}

func (s *stubResolver) setOK(r resolver.Records) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec = r
	s.err = nil
}

func (s *stubResolver) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rec = resolver.Records{}
	s.err = err
}

// ---------------------------------------------------------------------

func TestCached_HitMiss(t *testing.T) {
	s := &stubResolver{}
	s.setOK(resolver.Records{Hostname: "example.com", MinTTL: 5 * time.Minute})
	c := cached.NewWithTTL(s, time.Hour, time.Hour)

	for i := 0; i < 3; i++ {
		r, err := c.Lookup(context.Background(), "example.com")
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if r.Hostname != "example.com" {
			t.Errorf("call %d: Hostname = %q, want example.com", i, r.Hostname)
		}
	}
	if got := s.calls.Load(); got != 1 {
		t.Errorf("inner calls = %d, want 1 (cache hits ignored)", got)
	}
}

func TestCached_TTLClamped(t *testing.T) {
	cases := []struct {
		name         string
		minTTL       time.Duration
		wantDeadline time.Duration
	}{
		{"floor", 1 * time.Second, cached.SuccessTTLMin},
		{"midrange", 200 * time.Second, 200 * time.Second},
		{"ceiling", 24 * time.Hour, cached.SuccessTTLMax},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &stubResolver{}
			s.setOK(resolver.Records{Hostname: "x", MinTTL: tc.minTTL})
			c := cached.NewWithTTL(s, time.Hour, time.Hour)
			if _, err := c.Lookup(context.Background(), "x"); err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			// Re-lookup should hit cache (still within deadline) — exactly
			// one inner call.
			if _, err := c.Lookup(context.Background(), "x"); err != nil {
				t.Fatalf("Lookup #2: %v", err)
			}
			if got := s.calls.Load(); got != 1 {
				t.Errorf("inner calls = %d, want 1 (deadline = %v)", got, tc.wantDeadline)
			}
		})
	}
}

func TestCached_NXDOMAIN1Hour(t *testing.T) {
	s := &stubResolver{}
	s.setErr(resolver.ErrNotFound)
	c := cached.NewWithTTL(s, time.Minute, time.Hour)

	for i := 0; i < 3; i++ {
		_, err := c.Lookup(context.Background(), "missing.example")
		if !errors.Is(err, resolver.ErrNotFound) {
			t.Fatalf("call %d: err = %v, want ErrNotFound", i, err)
		}
	}
	if got := s.calls.Load(); got != 1 {
		t.Errorf("inner calls = %d, want 1 (1h cached)", got)
	}
}

func TestCached_StaleOnTransient(t *testing.T) {
	s := &stubResolver{}
	s.setOK(resolver.Records{Hostname: "example.com", MinTTL: 5 * time.Second})

	c := cached.NewWithTTL(s, time.Minute, time.Hour)

	// Drive `now` via the test seam (see cached_clock_test.go) so we
	// can age the stateOK entry past its (clamped 60s) deadline without
	// real sleeps.
	t0 := time.Now()
	cached.SetNow(c, func() time.Time { return t0 })

	// Prime stateOK.
	if _, err := c.Lookup(context.Background(), "example.com"); err != nil {
		t.Fatalf("prime: %v", err)
	}

	// Advance time past the SuccessTTLMin (60s) clamp.
	cached.SetNow(c, func() time.Time { return t0.Add(2 * time.Minute) })

	// Switch inner to transient err.
	s.setErr(resolver.ErrUnavailable)

	r, err := c.Lookup(context.Background(), "example.com")
	if !errors.Is(err, resolver.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if r.Hostname != "example.com" {
		t.Errorf("stale records dropped: got %+v, want Hostname=example.com", r)
	}
}

func TestCached_SelfThrottledNotCached(t *testing.T) {
	s := &stubResolver{}
	s.setErr(resolver.ErrSelfThrottled)
	c := cached.New(s)

	for i := 0; i < 3; i++ {
		_, err := c.Lookup(context.Background(), "example.com")
		if !errors.Is(err, resolver.ErrSelfThrottled) {
			t.Fatalf("call %d: err = %v, want ErrSelfThrottled", i, err)
		}
	}
	// Each call should hit inner (no caching for self-throttled).
	if got := s.calls.Load(); got != 3 {
		t.Errorf("inner calls = %d, want 3 (self-throttle is statePassThrough)", got)
	}
}

func TestCached_LogHourly_StopsOnContextCancel(t *testing.T) {
	s := &stubResolver{}
	c := cached.New(s)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.LogHourly(ctx)
		close(done)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("LogHourly did not return within 200ms of ctx cancel")
	}
}

func TestCached_SingleflightDedup(t *testing.T) {
	s := &stubResolver{}
	s.setOK(resolver.Records{Hostname: "example.com", MinTTL: time.Hour})
	s.delay = 50 * time.Millisecond
	c := cached.New(s)

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_, _ = c.Lookup(context.Background(), "example.com")
		}()
	}
	wg.Wait()

	got := s.calls.Load()
	if got > 5 { // some race-window allowance
		t.Errorf("inner calls = %d, want close to 1 (singleflight dedup)", got)
	}
}
