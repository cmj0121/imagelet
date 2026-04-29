package twse

import (
	"context"
	"errors"
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
	resolved := "20260427"                              // walk-back lands on Monday

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
