package cached_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cmj0121/imagelet/service/stock/quote"
	"github.com/cmj0121/imagelet/service/stock/quote/cached"
)

// stubProvider is a controllable inner provider for tests. It returns the
// preset (q, err) and increments calls atomically. Use setOK / setErr to
// flip behavior between calls.
type stubProvider struct {
	mu    sync.Mutex
	q     quote.Quote
	err   error
	delay time.Duration // optional sleep so concurrent callers actually pile up
	calls atomic.Int32
}

func (s *stubProvider) Get(_ context.Context, symbol string) (quote.Quote, error) {
	s.calls.Add(1)
	s.mu.Lock()
	d, q, err := s.delay, s.q, s.err
	s.mu.Unlock()
	if d > 0 {
		time.Sleep(d)
	}
	if q.Symbol == "" {
		q.Symbol = symbol
	}
	return q, err
}

func (s *stubProvider) setOK(q quote.Quote) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.q = q
	s.err = nil
}

func (s *stubProvider) setErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.q = quote.Quote{}
	s.err = err
}

func TestCacheHitWithinTTL(t *testing.T) {
	stub := &stubProvider{}
	stub.setOK(quote.Quote{Symbol: "^GSPC", Last: 100, PrevClose: 99})
	p := cached.NewWithTTL(stub, 1*time.Hour, 1*time.Hour)

	for i := 0; i < 3; i++ {
		q, err := p.Get(context.Background(), "^GSPC")
		if err != nil {
			t.Fatalf("Get #%d: %v", i, err)
		}
		if q.Last != 100 {
			t.Errorf("Get #%d: Last = %v, want 100", i, q.Last)
		}
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("inner calls = %d, want 1 (cache hit)", got)
	}
}

func TestCacheMissAfterTTL(t *testing.T) {
	stub := &stubProvider{}
	stub.setOK(quote.Quote{Symbol: "^GSPC", Last: 100})
	p := cached.NewWithTTL(stub, 10*time.Millisecond, 1*time.Hour)

	if _, err := p.Get(context.Background(), "^GSPC"); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	time.Sleep(20 * time.Millisecond) // exceed successTTL
	if _, err := p.Get(context.Background(), "^GSPC"); err != nil {
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
	stub.setOK(quote.Quote{Symbol: "^GSPC", Last: 100})
	p := cached.NewWithTTL(stub, 1*time.Hour, 1*time.Hour)

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, _ = p.Get(context.Background(), "^GSPC")
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
	upstreamErr := errors.New("yahoo: 500 boom")
	stub.setErr(upstreamErr)
	p := cached.NewWithTTL(stub, 1*time.Hour, 50*time.Millisecond)

	// First Get triggers an upstream call and caches the error.
	if _, err := p.Get(context.Background(), "^GSPC"); err == nil {
		t.Fatalf("first Get: want err, got nil")
	}
	// Within failureTTL, second Get returns cached error WITHOUT calling inner.
	if _, err := p.Get(context.Background(), "^GSPC"); err == nil {
		t.Fatalf("second Get: want err, got nil")
	}
	if got := stub.calls.Load(); got != 1 {
		t.Errorf("inner calls = %d, want 1 (cached failure)", got)
	}

	// After failureTTL, the next Get retries.
	time.Sleep(80 * time.Millisecond)
	if _, err := p.Get(context.Background(), "^GSPC"); err == nil {
		t.Fatalf("third Get: want err, got nil")
	}
	if got := stub.calls.Load(); got != 2 {
		t.Errorf("inner calls after TTL = %d, want 2 (retry)", got)
	}
}

func TestStaleQuoteWhenUpstreamFails(t *testing.T) {
	stub := &stubProvider{}
	stub.setOK(quote.Quote{Symbol: "^GSPC", Last: 100, PrevClose: 99})
	p := cached.NewWithTTL(stub, 10*time.Millisecond, 1*time.Hour)

	// Cache a successful Quote.
	q, err := p.Get(context.Background(), "^GSPC")
	if err != nil {
		t.Fatalf("seed Get: %v", err)
	}
	if q.Last != 100 {
		t.Fatalf("seed Last = %v, want 100", q.Last)
	}

	// Flip stub to error and exceed successTTL.
	stub.setErr(errors.New("yahoo: timeout"))
	time.Sleep(20 * time.Millisecond)

	// Stale refresh fails — caller gets the cached Quote AND the upstream err.
	q, err = p.Get(context.Background(), "^GSPC")
	if err == nil {
		t.Fatalf("expected upstream err for stale-failure path, got nil")
	}
	if q.Last != 100 {
		t.Errorf("stale Quote Last = %v, want cached 100", q.Last)
	}
}
