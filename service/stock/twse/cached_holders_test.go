package twse

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeHoldersExact records every call and replies from a programmable
// (dump, found, err) tuple. Lets the cache tests verify the upstream
// is hit at most once per cache TTL window.
type fakeHoldersExact struct {
	calls int32
	dump  holdersDump
	found bool
	err   error
}

func (f *fakeHoldersExact) FetchHoldersExact(_ context.Context) (holdersDump, bool, error) {
	atomic.AddInt32(&f.calls, 1)
	return f.dump, f.found, f.err
}

// twoStockDump returns a small synthetic dump with stocks "2330" and
// "0050", enough for cache-hit / per-stock-lookup tests without
// dragging in the 7.4 KB pinned fixture.
func twoStockDump() holdersDump {
	return holdersDump{
		AsOf: time.Date(2026, 4, 30, 12, 0, 0, 0, twLoc),
		Rows: map[string]HoldersDistribution{
			"2330": {StockID: "2330", TotalCount: 2519187, TotalShare: 25932524521},
			"0050": {StockID: "0050", TotalCount: 1234567, TotalShare: 9876543210},
		},
	}
}

// TestCachedHolders_FetchOnceServesMany pins the load-bearing
// behaviour: 50 concurrent GetHoldersDistribution calls converge on a
// single upstream fetch via ttlcache singleflight + cache.
func TestCachedHolders_FetchOnceServesMany(t *testing.T) {
	upstream := &fakeHoldersExact{dump: twoStockDump(), found: true}
	c := NewCachedHolders(upstream, 0)

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			d, err := c.GetHoldersDistribution(context.Background(), "2330", time.Time{})
			if err != nil {
				errs <- err
				return
			}
			if d.TotalCount != 2519187 {
				errs <- errors.New("wrong dist")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent call: %v", err)
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 1 {
		t.Errorf("upstream.calls = %d, want 1 (singleflight + cache)", got)
	}
}

// TestCachedHolders_TTLExpires advances the clock past holdersSuccessTTL
// between calls and asserts the second call DOES re-fetch.
func TestCachedHolders_TTLExpires(t *testing.T) {
	upstream := &fakeHoldersExact{dump: twoStockDump(), found: true}
	c := NewCachedHolders(upstream, 0)
	t0 := time.Date(2026, 5, 1, 9, 0, 0, 0, twLoc)
	now := t0
	c.SetClock(func() time.Time { return now })

	if _, err := c.GetHoldersDistribution(context.Background(), "2330", time.Time{}); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 1 {
		t.Fatalf("after first: calls = %d, want 1", got)
	}
	// Advance just past the 24h TTL.
	now = t0.Add(holdersSuccessTTL + time.Minute)
	if _, err := c.GetHoldersDistribution(context.Background(), "2330", time.Time{}); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 2 {
		t.Errorf("after second: calls = %d, want 2 (TTL expired)", got)
	}
}

// TestCachedHolders_StockMiss asserts that a request for an unknown
// stock id returns ErrUnavailable WITHOUT triggering an extra upstream
// fetch — the dump is cached and the per-stock lookup is a map hit.
func TestCachedHolders_StockMiss(t *testing.T) {
	upstream := &fakeHoldersExact{dump: twoStockDump(), found: true}
	c := NewCachedHolders(upstream, 0)

	if _, err := c.GetHoldersDistribution(context.Background(), "2330", time.Time{}); err != nil {
		t.Fatalf("known-id call: %v", err)
	}
	_, err := c.GetHoldersDistribution(context.Background(), "9999", time.Time{})
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("unknown-id err = %v, want ErrUnavailable", err)
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 1 {
		t.Errorf("upstream.calls = %d, want 1 (per-stock miss must not refetch)", got)
	}
}

// TestCachedHolders_PublishGap asserts that a found=false upstream
// reply (TDCC publish gap) is cached for the success TTL — see the
// holdersSuccessTTL doc comment for why dual-TTL was collapsed to a
// single window. We verify the negative result is HELD, not
// re-fetched on every request.
func TestCachedHolders_PublishGap(t *testing.T) {
	upstream := &fakeHoldersExact{found: false}
	c := NewCachedHolders(upstream, 0)

	for i := 0; i < 5; i++ {
		_, err := c.GetHoldersDistribution(context.Background(), "2330", time.Time{})
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("call %d err = %v, want ErrUnavailable", i, err)
		}
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 1 {
		t.Errorf("upstream.calls = %d, want 1 (publish gap should cache)", got)
	}
}

// TestCachedHolders_UpstreamError pins that transport / parse errors
// from upstream propagate (and are NOT cached, so the next request
// retries — matching ttlcache.GetOrFetch's error semantics).
func TestCachedHolders_UpstreamError(t *testing.T) {
	upstream := &fakeHoldersExact{err: errors.New("boom")}
	c := NewCachedHolders(upstream, 0)

	_, err := c.GetHoldersDistribution(context.Background(), "2330", time.Time{})
	if err == nil || err.Error() != "boom" {
		t.Errorf("first err = %v, want boom", err)
	}
	_, err = c.GetHoldersDistribution(context.Background(), "2330", time.Time{})
	if err == nil {
		t.Errorf("second err = nil, want boom (errors are NOT cached by ttlcache)")
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 2 {
		t.Errorf("upstream.calls = %d, want 2 (error retries)", got)
	}
}

// TestCachedHolders_SnapshotRoundtrip writes the cache to a tmp file,
// loads it into a fresh wrapper, and asserts the lookup serves
// without another upstream fetch.
func TestCachedHolders_SnapshotRoundtrip(t *testing.T) {
	upstream := &fakeHoldersExact{dump: twoStockDump(), found: true}
	src := NewCachedHolders(upstream, 0)
	if _, err := src.GetHoldersDistribution(context.Background(), "2330", time.Time{}); err != nil {
		t.Fatalf("populate: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, snapshotHolders)
	if err := src.saveTo(path); err != nil {
		t.Fatalf("saveTo: %v", err)
	}

	// Fresh wrapper with a DIFFERENT upstream that would error if
	// called — proves the load actually populated the cache.
	stranger := &fakeHoldersExact{err: errors.New("must not be called")}
	dst := NewCachedHolders(stranger, 0)
	if err := dst.loadFrom(path); err != nil {
		t.Fatalf("loadFrom: %v", err)
	}
	d, err := dst.GetHoldersDistribution(context.Background(), "2330", time.Time{})
	if err != nil {
		t.Fatalf("post-load lookup: %v", err)
	}
	if d.TotalCount != 2519187 {
		t.Errorf("post-load TotalCount = %d, want 2519187", d.TotalCount)
	}
	if got := atomic.LoadInt32(&stranger.calls); got != 0 {
		t.Errorf("stranger upstream.calls = %d, want 0 (snapshot should serve)", got)
	}
}

// TestCachedConstructorWiresHolders proves the type-assertion in
// NewCachedWithTTL recognises *HTTPProvider's HoldersExactProvider
// method set and attaches the cache wrapper.
func TestCachedConstructorWiresHolders(t *testing.T) {
	inner := New()
	c := NewCached(inner)
	if c.cachedHolders == nil {
		t.Errorf("cachedHolders = nil, want non-nil (HTTPProvider implements HoldersExactProvider)")
	}
}

// TestCachedGetHoldersDistribution_FixtureViaServer pins the production
// path end-to-end: an HTTPProvider pointed at an httptest server
// serving the pinned TDCC fixture, wrapped in a Cached, called via
// the public Cached.GetHoldersDistribution. Asserts cache + lookup
// produce the verified-from-live numbers for 2330.
func TestCachedGetHoldersDistribution_FixtureViaServer(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "holders_2330_6488_0050_20260430.json"))
	if err != nil {
		t.Fatalf("fixture read: %v", err)
	}
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	inner := NewWithEndpoints("", "", "", srv.Client())
	inner.SetHoldersEndpoint(srv.URL)
	c := NewCached(inner)
	if c.cachedHolders == nil {
		t.Fatalf("cachedHolders nil after NewCached")
	}

	d, err := c.GetHoldersDistribution(context.Background(), "2330", time.Time{})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if d.Tiers[14].Count != 1497 {
		t.Errorf("Tiers[14].Count = %d, want 1497", d.Tiers[14].Count)
	}
	if d.TotalCount != 2519187 {
		t.Errorf("TotalCount = %d, want 2519187", d.TotalCount)
	}
	// Second call for a different stock — no extra HTTP hit (dump cached).
	if _, err := c.GetHoldersDistribution(context.Background(), "6488", time.Time{}); err != nil {
		t.Errorf("6488 lookup: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hits = %d, want 1 (cached dump should serve both lookups)", got)
	}
}

// TestCachedGetHoldersDistribution_FallbackToInner proves the
// uncached fallback path: Cached wrapping a Provider that implements
// HoldersProvider directly but NOT HoldersExactProvider should still
// resolve holders via inner.GetHoldersDistribution.
func TestCachedGetHoldersDistribution_FallbackToInner(t *testing.T) {
	inner := &holdersProviderOnly{
		dist: HoldersDistribution{
			StockID:    "2330",
			TotalCount: 100,
			TotalShare: 1000,
		},
	}
	c := NewCached(inner)
	if c.cachedHolders != nil {
		t.Fatalf("cachedHolders should be nil for non-Exact provider")
	}
	d, err := c.GetHoldersDistribution(context.Background(), "2330", time.Time{})
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if d.TotalCount != 100 {
		t.Errorf("TotalCount = %d, want 100", d.TotalCount)
	}
}

// holdersProviderOnly is a Provider that implements HoldersProvider
// but NOT HoldersExactProvider — exercises the Cached fallback path.
type holdersProviderOnly struct {
	dist HoldersDistribution
}

func (h *holdersProviderOnly) Get(_ context.Context) (MarketData, error) {
	return MarketData{}, ErrUnavailable
}

func (h *holdersProviderOnly) GetHoldersDistribution(_ context.Context, _ string, _ time.Time) (HoldersDistribution, error) {
	return h.dist, nil
}
