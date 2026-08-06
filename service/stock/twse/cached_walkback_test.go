package twse

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeT86Exact records every call and replies with a programmable
// (data, found, err) per call. Lets the cache tests verify the
// upstream is hit at most once per (stockID, date) tuple.
type fakeT86Exact struct {
	calls   int32
	replies map[string]struct {
		d   StockData
		ok  bool
		err error
	}
}

func (f *fakeT86Exact) FetchT86Exact(_ context.Context, stockID, date string) (StockData, bool, error) {
	atomic.AddInt32(&f.calls, 1)
	r := f.replies[stockID+"|"+date]
	return r.d, r.ok, r.err
}

// TestCachedT86HitsUpstreamOnceForResolvedDate pins the load-bearing
// behaviour: a request for asOf=Friday after a holiday week walks back
// to the actual published day, caches under THAT date, and a second
// request for the same week converges on the same cache entry.
func TestCachedT86HitsUpstreamOnceForResolvedDate(t *testing.T) {
	asOf := time.Date(2026, 4, 30, 12, 0, 0, 0, twLoc) // Thursday
	resolved := "20260427"                             // walk-back lands on Monday

	upstream := &fakeT86Exact{replies: map[string]struct {
		d   StockData
		ok  bool
		err error
	}{
		"2330|20260430": {ok: false}, // today: not yet published
		"2330|20260429": {ok: false}, // wed: holiday gap
		"2330|20260428": {ok: false}, // tue: holiday gap
		"2330|20260427": {d: StockData{StockID: "2330", Net: 1234}, ok: true},
	}}

	c := NewCachedT86(upstream, 0)
	c.SetClock(func() time.Time { return asOf })

	// First request: walks back through 4 dates (4 upstream calls).
	d, err := c.GetForStock(context.Background(), "2330", asOf)
	if err != nil {
		t.Fatalf("first call err: %v", err)
	}
	if d.Net != 1234 {
		t.Errorf("first call returned Net=%d, want 1234", d.Net)
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 4 {
		t.Errorf("first call: upstream hit %d times, want 4 (walk-back through 30/29/28/27)", got)
	}

	// Second request for the same asOf: every probe should be a cache
	// hit (3 negative-cache hits + 1 positive). Upstream MUST NOT be
	// called — this is the load-bearing behaviour.
	_, err = c.GetForStock(context.Background(), "2330", asOf)
	if err != nil {
		t.Fatalf("second call err: %v", err)
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 4 {
		t.Errorf("second call: upstream hit %d times, want still 4 (all cache hits)", got)
	}

	// Third request: asOf shifted to a date in the cached-window
	// future direction (asOf=05/01, clock advances by a few hours so
	// the closed-session TTL on 4/27 hasn't yet aged out). The
	// walk-back probes 5/01 (uncached) → 4/30 (today-TTL expired) →
	// 4/29, 4/28, 4/27 (still cached, 24h TTL). So at most 2 new
	// upstream calls land.
	later := asOf.Add(2 * time.Hour) // clock = 4/30 14:00, still within 24h
	tomorrow := time.Date(2026, 5, 1, 12, 0, 0, 0, twLoc)
	c.SetClock(func() time.Time { return later })
	upstream.replies["2330|20260501"] = struct {
		d   StockData
		ok  bool
		err error
	}{ok: false}
	_, err = c.GetForStock(context.Background(), "2330", tomorrow)
	if err != nil {
		t.Fatalf("third call err: %v", err)
	}
	// Hits: 5/01 (cold), 4/30 (today-TTL of 30min expired by 14:00),
	// then 4/29, 4/28, 4/27 cached → ≤ 4+2 = 6 total upstream calls.
	if got := atomic.LoadInt32(&upstream.calls); got > 6 {
		t.Errorf("third call: upstream hit %d times, want ≤ 6 (4/27 cache should still hold)", got)
	}

	_ = resolved // silence linter on the documenting comment
}

// TestCachedT86WalkbackExhaustReturnsErrUnavailable pins the
// no-data-after-7-days fallthrough — same contract as the uncached
// upstream method.
func TestCachedT86WalkbackExhaustReturnsErrUnavailable(t *testing.T) {
	asOf := time.Date(2026, 4, 30, 12, 0, 0, 0, twLoc)
	upstream := &fakeT86Exact{replies: map[string]struct {
		d   StockData
		ok  bool
		err error
	}{}} // every probe returns Found=false (zero replies)

	c := NewCachedT86(upstream, 0)
	c.SetClock(func() time.Time { return asOf })

	_, err := c.GetForStock(context.Background(), "2330", asOf)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

// TestCachedT86PropagatesUpstreamError pins that a transport / parse
// failure short-circuits the walk and bubbles up — we don't silently
// swallow it as "no data". The very first probe returns the error so
// the loop short-circuits before walking past it.
func TestCachedT86PropagatesUpstreamError(t *testing.T) {
	asOf := time.Date(2026, 4, 30, 12, 0, 0, 0, twLoc) // Thursday
	bad := errors.New("boom")
	upstream := &fakeT86Exact{replies: map[string]struct {
		d   StockData
		ok  bool
		err error
	}{
		"2330|20260430": {err: bad}, // first probe errors
	}}

	c := NewCachedT86(upstream, 0)
	c.SetClock(func() time.Time { return asOf })

	_, err := c.GetForStock(context.Background(), "2330", asOf)
	if !errors.Is(err, bad) {
		t.Errorf("err = %v, want %v propagated", err, bad)
	}
}

// TestTTLForAsOfPublishWindow pins the TTL strategy: today before
// publish hour gets a short TTL; today after publish hour gets the
// closed-session TTL; past dates always get the closed-session TTL.
func TestTTLForAsOfPublishWindow(t *testing.T) {
	twAt := func(y int, m time.Month, d, h, min int) time.Time {
		return time.Date(y, m, d, h, min, 0, 0, twLoc)
	}

	cases := []struct {
		name        string
		asOf, now   time.Time
		wantExactly time.Duration // 0 means only the inequality below applies
		wantBelow   time.Duration // 0 means no upper-bound check
	}{
		{"today_before_publish_far", twAt(2026, 4, 30, 10, 0), twAt(2026, 4, 30, 10, 0), minTodayTTL, 0},
		{"today_close_to_publish", twAt(2026, 4, 30, 16, 50), twAt(2026, 4, 30, 16, 50), 0, minTodayTTL},
		{"today_after_publish", twAt(2026, 4, 30, 18, 0), twAt(2026, 4, 30, 18, 0), closedSessionTTL, 0},
		{"yesterday_any_time", twAt(2026, 4, 29, 14, 0), twAt(2026, 4, 30, 12, 0), closedSessionTTL, 0},
		{"week_old", twAt(2026, 4, 23, 10, 0), twAt(2026, 4, 30, 12, 0), closedSessionTTL, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ttlForAsOf(tc.asOf, tc.now)
			if tc.wantExactly != 0 && got != tc.wantExactly {
				t.Errorf("ttl = %v, want exactly %v", got, tc.wantExactly)
			}
			if tc.wantBelow != 0 && got >= tc.wantBelow {
				t.Errorf("ttl = %v, want < %v", got, tc.wantBelow)
			}
		})
	}
}

// failingRetailFuturesExact fails every fetch — the snapshot test
// below uses it to prove the answer came from the restored cache
// entry, not a fresh upstream fetch.
type failingRetailFuturesExact struct{}

func (failingRetailFuturesExact) FetchTAIFEXFuturesExact(context.Context, time.Time) (RetailFutures, bool, error) {
	return RetailFutures{}, false, errors.New("upstream must not be hit")
}

// TestCachedRetailFuturesSnapshotPreTXFLoads pins the snapshot
// compatibility guarantee for the additive TXF fields on
// RetailFutures: a taifex-retail-futures.json written before those
// fields existed (same ttlcache schema version, entry values lacking
// the TXF keys) must load cleanly with the TXF fields zeroed —
// HasTXF() false — instead of tripping ErrSchemaMismatch or
// corrupting the restored retail values.
func TestCachedRetailFuturesSnapshotPreTXFLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, snapshotRetailFutures)
	// expires_at must be in the REAL future — ttlcache.LoadJSON drops
	// already-expired entries against the wall clock at load time.
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	old := fmt.Sprintf(`{
  "schema": 1,
  "saved_at": "2026-04-24T17:30:00+08:00",
  "entries": [
    {
      "key": "20260424",
      "value": {"MXFNet": -1152, "TMFNet": -21646, "AsOf": "2026-04-24T00:00:00+08:00"},
      "found": true,
      "expires_at": %q
    }
  ]
}`, expires)
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	c := NewCachedRetailFutures(failingRetailFuturesExact{}, 0)
	if err := c.loadFrom(path); err != nil {
		t.Fatalf("loadFrom: %v", err)
	}

	// asOf on the snapshotted (past) trading day: the first walk-back
	// probe hits the restored 20260424 entry, so the failing upstream
	// is never consulted.
	asOf := time.Date(2026, 4, 24, 18, 0, 0, 0, twLoc) // Friday
	got, err := c.GetRetailFutures(context.Background(), asOf)
	if err != nil {
		t.Fatalf("GetRetailFutures: %v", err)
	}
	if got.MXFNet != -1152 || got.TMFNet != -21646 {
		t.Errorf("restored retail fields = (%d, %d), want (-1152, -21646)", got.MXFNet, got.TMFNet)
	}
	if got.HasTXF() {
		t.Errorf("HasTXF() = true, want false (TXF fields must restore zeroed from a pre-TXF snapshot)")
	}
	// The Partial key is likewise absent from the old file. JSON leaves
	// it false, i.e. "complete" — the right default for data written by
	// a build that could not produce partials, and the one that keeps
	// the restored entry on its normal TTL.
	if got.Partial {
		t.Errorf("Partial = true, want false (absent key must default to complete)")
	}
}

// stubRetailFuturesExact returns one canned result for every probe and
// counts calls, so the TTL tests can tell a fresh fetch from a cache
// hit.
type stubRetailFuturesExact struct {
	value RetailFutures
	calls int32
}

func (s *stubRetailFuturesExact) FetchTAIFEXFuturesExact(_ context.Context, day time.Time) (RetailFutures, bool, error) {
	atomic.AddInt32(&s.calls, 1)
	v := s.value
	v.AsOf = day
	return v, true, nil
}

// TestCachedRetailFuturesPartialGetsShortTTL is the regression pin for
// the containment bug: a partial (upstream-degraded) result must NOT
// occupy a full closedSessionTTL entry. Probing a past date is the
// worst case — ttlForAsOf hands out 24h — so a partial answer would
// otherwise drop a shipped row from every render for a day with no
// retry.
func TestCachedRetailFuturesPartialGetsShortTTL(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, twLoc) // Monday
	probe := time.Date(2026, 4, 24, 0, 0, 0, 0, twLoc)

	tests := []struct {
		name    string
		partial bool
		wantTTL time.Duration
	}{
		{name: "complete entry keeps the session TTL", partial: false, wantTTL: closedSessionTTL},
		{name: "partial entry is capped", partial: true, wantTTL: partialEntryTTL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &stubRetailFuturesExact{value: RetailFutures{
				TMFNet:  -100,
				Partial: tc.partial,
			}}
			c := NewCachedRetailFutures(upstream, 0)
			c.SetClock(func() time.Time { return now })

			if _, err := c.GetRetailFutures(context.Background(), probe); err != nil {
				t.Fatalf("GetRetailFutures: %v", err)
			}
			e, ok := c.cache.Get(probe.Format("20060102"))
			if !ok {
				t.Fatal("entry not cached")
			}
			if got := e.ExpiresAt.Sub(now); got != tc.wantTTL {
				t.Errorf("entry TTL = %v, want %v", got, tc.wantTTL)
			}
		})
	}
}

// TestCachedRetailFuturesPartialTTLNotRefreshedOnRead pins that the
// TTL fixup runs only on a fresh fetch. Re-stamping the entry on every
// cache hit would keep pushing its expiry forward, so a partial on a
// busy endpoint would never expire — the 24h pin this fix removes,
// reintroduced through the back door.
func TestCachedRetailFuturesPartialTTLNotRefreshedOnRead(t *testing.T) {
	now := time.Date(2026, 4, 27, 12, 0, 0, 0, twLoc)
	probe := time.Date(2026, 4, 24, 0, 0, 0, 0, twLoc)

	upstream := &stubRetailFuturesExact{value: RetailFutures{TMFNet: -100, Partial: true}}
	c := NewCachedRetailFutures(upstream, 0)
	clock := now
	c.SetClock(func() time.Time { return clock })

	if _, err := c.GetRetailFutures(context.Background(), probe); err != nil {
		t.Fatalf("first GetRetailFutures: %v", err)
	}
	e, _ := c.cache.Get(probe.Format("20060102"))
	firstExpiry := e.ExpiresAt

	// Later read, still inside the partial window: served from cache,
	// so the expiry must not move.
	clock = now.Add(partialEntryTTL / 2)
	if _, err := c.GetRetailFutures(context.Background(), probe); err != nil {
		t.Fatalf("second GetRetailFutures: %v", err)
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (second read must be a cache hit)", got)
	}
	e, _ = c.cache.Get(probe.Format("20060102"))
	if !e.ExpiresAt.Equal(firstExpiry) {
		t.Errorf("expiry moved from %v to %v on a cache hit; partial would never self-heal", firstExpiry, e.ExpiresAt)
	}

	// Past the partial window the entry is gone and the upstream is
	// re-consulted — the self-heal this fix exists for.
	clock = now.Add(partialEntryTTL + time.Minute)
	if _, err := c.GetRetailFutures(context.Background(), probe); err != nil {
		t.Fatalf("third GetRetailFutures: %v", err)
	}
	if got := atomic.LoadInt32(&upstream.calls); got != 2 {
		t.Errorf("upstream calls = %d, want 2 (entry must have expired and re-fetched)", got)
	}
}
