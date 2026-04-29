package twse

import (
	"context"
	"time"

	"github.com/cmj0121/imagelet/internal/ttlcache"
)

// Walk-back caches: each wrapper takes an Exact*Provider (no internal
// walk-back) and exposes the matching public Get* interface. The cache
// is keyed on the resolved date so requests with different `asOf`
// values converge on the same entry once the walk-back finds a
// published day.
//
// TTL is publish-window-aware via ttlForAsOf:
//   - For asOf == today, TTL collapses to "until ~17:00 Asia/Taipei"
//     (capped at 30min as a safety floor), so the cache flips to fresh
//     once afterTrading publishes the day's data.
//   - For asOf in the past, TTL is 24h — historical days don't change
//     upstream, so a longer hold is safe.
//
// Failed upstream fetches are NOT cached (ttlcache.Cache.GetOrFetch
// behaviour); only successful (Found=true) and "no data" (Found=false)
// outcomes persist.

// twPublishHour is the local hour after which TWSE / TAIFEX afterTrading
// data is reliably published for the current session. The Cache's TTL
// shortens any entry covering today's date to expire just after this
// hour, so a request at 14:00 caches a "no data yet" sentinel only
// briefly.
const twPublishHour = 17

// minTodayTTL caps the until-publish TTL window so a clock skew or a
// late-publish day doesn't pin the cache for an unexpectedly long time.
const minTodayTTL = 30 * time.Minute

// closedSessionTTL is the success TTL for any date strictly before
// today. Past trading days are immutable upstream; 24h holds them
// long enough that a steady stream of /stock requests amortises the
// fetch cost across days without piling up on cold restarts.
const closedSessionTTL = 24 * time.Hour

// ttlForAsOf returns the TTL the cache should hold an entry for, given
// the date the entry covers and the wall-clock at insertion time. See
// the package comment for the publish-window logic.
func ttlForAsOf(asOf, now time.Time) time.Duration {
	asOf = asOf.In(twLoc)
	now = now.In(twLoc)

	asOfDay := time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, twLoc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, twLoc)
	if asOfDay.Before(today) {
		return closedSessionTTL
	}

	publish := time.Date(today.Year(), today.Month(), today.Day(), twPublishHour, 0, 0, 0, twLoc)
	if now.Before(publish) {
		untilPublish := publish.Sub(now)
		if untilPublish < minTodayTTL {
			return untilPublish
		}
		return minTodayTTL
	}
	// Past publish hour: today's data is settled. Hold it like any
	// other closed session until tomorrow's request rolls the date
	// forward and re-keys the walk-back.
	return closedSessionTTL
}

// clampAsOfTWAt is the test-friendly twin of clampAsOfTW: instead of
// reading time.Now() it takes the wall-clock from the caller, so a
// cache wrapper using a mock clock can keep the walk-back deterministic.
func clampAsOfTWAt(asOf, now time.Time) time.Time {
	now = now.In(twLoc)
	if asOf.IsZero() || asOf.After(now) {
		return now
	}
	return asOf.In(twLoc)
}

// walkBackTradingDays iterates probe dates from asOf back to
// asOf-maxLookbackDays, skipping weekends. The visit callback returns
// (found, err): a found=true result short-circuits the loop, an error
// short-circuits with that error, otherwise the walk continues.
//
// `now` is the wall-clock used to clamp future-dated asOf values, so
// callers that pin a clock (tests, CachedT86 et al.) get deterministic
// probe sequences.
func walkBackTradingDays(asOf, now time.Time, visit func(probe time.Time) (bool, error)) error {
	asOf = clampAsOfTWAt(asOf, now)
	for daysBack := 0; daysBack < maxLookbackDays; daysBack++ {
		probe := asOf.AddDate(0, 0, -daysBack)
		switch probe.Weekday() {
		case time.Saturday, time.Sunday:
			continue
		}
		stop, err := visit(probe)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	return ErrUnavailable
}

// CachedT86 wraps a PerStockExactProvider with a per-(stockID, date)
// TTL cache and walks back across cached entries to satisfy
// PerStockProvider's GetForStock contract.
type CachedT86 struct {
	upstream PerStockExactProvider
	cache    *ttlcache.Cache[string, StockData]
	now      func() time.Time
}

// NewCachedT86 returns a cached wrapper around upstream. cap is the
// LRU eviction threshold; pass 0 for ttlcache.DefaultCap.
func NewCachedT86(upstream PerStockExactProvider, capacity int) *CachedT86 {
	return &CachedT86{
		upstream: upstream,
		cache:    ttlcache.New[string, StockData](capacity),
		now:      time.Now,
	}
}

// SetClock overrides the wall-clock for tests.
func (c *CachedT86) SetClock(now func() time.Time) {
	c.now = now
	c.cache.SetClock(now)
}

// GetForStock implements PerStockProvider.
func (c *CachedT86) GetForStock(ctx context.Context, stockID string, asOf time.Time) (StockData, error) {
	var out StockData
	err := walkBackTradingDays(asOf, c.now(), func(probe time.Time) (bool, error) {
		date := probe.Format("20060102")
		key := stockID + "|" + date
		v, found, err := c.cache.GetOrFetch(key, ttlForAsOf(probe, c.now()), func() (StockData, bool, error) {
			return c.upstream.FetchT86Exact(ctx, stockID, date)
		})
		if err != nil {
			return false, err
		}
		if found {
			out = v
			return true, nil
		}
		return false, nil
	})
	return out, err
}

// CachedSecuritiesLending wraps a SecuritiesLendingExactProvider.
type CachedSecuritiesLending struct {
	upstream SecuritiesLendingExactProvider
	cache    *ttlcache.Cache[string, SecuritiesLending]
	now      func() time.Time
}

func NewCachedSecuritiesLending(upstream SecuritiesLendingExactProvider, capacity int) *CachedSecuritiesLending {
	return &CachedSecuritiesLending{
		upstream: upstream,
		cache:    ttlcache.New[string, SecuritiesLending](capacity),
		now:      time.Now,
	}
}

func (c *CachedSecuritiesLending) SetClock(now func() time.Time) {
	c.now = now
	c.cache.SetClock(now)
}

func (c *CachedSecuritiesLending) GetSecuritiesLending(ctx context.Context, stockID string, asOf time.Time) (SecuritiesLending, error) {
	var out SecuritiesLending
	err := walkBackTradingDays(asOf, c.now(), func(probe time.Time) (bool, error) {
		date := probe.Format("20060102")
		key := stockID + "|" + date
		v, found, err := c.cache.GetOrFetch(key, ttlForAsOf(probe, c.now()), func() (SecuritiesLending, bool, error) {
			return c.upstream.FetchTWT93UExact(ctx, stockID, date)
		})
		if err != nil {
			return false, err
		}
		if found {
			out = v
			return true, nil
		}
		return false, nil
	})
	return out, err
}

// CachedStockMargin wraps a StockMarginExactProvider.
type CachedStockMargin struct {
	upstream StockMarginExactProvider
	cache    *ttlcache.Cache[string, StockMargin]
	now      func() time.Time
}

func NewCachedStockMargin(upstream StockMarginExactProvider, capacity int) *CachedStockMargin {
	return &CachedStockMargin{
		upstream: upstream,
		cache:    ttlcache.New[string, StockMargin](capacity),
		now:      time.Now,
	}
}

func (c *CachedStockMargin) SetClock(now func() time.Time) {
	c.now = now
	c.cache.SetClock(now)
}

func (c *CachedStockMargin) GetStockMargin(ctx context.Context, stockID string, asOf time.Time) (StockMargin, error) {
	var out StockMargin
	err := walkBackTradingDays(asOf, c.now(), func(probe time.Time) (bool, error) {
		date := probe.Format("20060102")
		key := stockID + "|" + date
		v, found, err := c.cache.GetOrFetch(key, ttlForAsOf(probe, c.now()), func() (StockMargin, bool, error) {
			return c.upstream.FetchMIMARGNStockExact(ctx, stockID, date)
		})
		if err != nil {
			return false, err
		}
		if found {
			out = v
			return true, nil
		}
		return false, nil
	})
	return out, err
}

// CachedRetailFutures wraps a RetailFuturesExactProvider. Walk-back
// probes one day at a time across MXF + TMF combined.
type CachedRetailFutures struct {
	upstream RetailFuturesExactProvider
	cache    *ttlcache.Cache[string, RetailFutures]
	now      func() time.Time
}

func NewCachedRetailFutures(upstream RetailFuturesExactProvider, capacity int) *CachedRetailFutures {
	return &CachedRetailFutures{
		upstream: upstream,
		cache:    ttlcache.New[string, RetailFutures](capacity),
		now:      time.Now,
	}
}

func (c *CachedRetailFutures) SetClock(now func() time.Time) {
	c.now = now
	c.cache.SetClock(now)
}

func (c *CachedRetailFutures) GetRetailFutures(ctx context.Context, asOf time.Time) (RetailFutures, error) {
	var out RetailFutures
	err := walkBackTradingDays(asOf, c.now(), func(probe time.Time) (bool, error) {
		key := probe.Format("20060102")
		v, found, err := c.cache.GetOrFetch(key, ttlForAsOf(probe, c.now()), func() (RetailFutures, bool, error) {
			return c.upstream.FetchTAIFEXFuturesExact(ctx, probe)
		})
		if err != nil {
			return false, err
		}
		if found {
			out = v
			return true, nil
		}
		return false, nil
	})
	return out, err
}

// CachedOptionsPCR wraps an OptionsPCRExactProvider.
type CachedOptionsPCR struct {
	upstream OptionsPCRExactProvider
	cache    *ttlcache.Cache[string, OptionsPCR]
	now      func() time.Time
}

func NewCachedOptionsPCR(upstream OptionsPCRExactProvider, capacity int) *CachedOptionsPCR {
	return &CachedOptionsPCR{
		upstream: upstream,
		cache:    ttlcache.New[string, OptionsPCR](capacity),
		now:      time.Now,
	}
}

func (c *CachedOptionsPCR) SetClock(now func() time.Time) {
	c.now = now
	c.cache.SetClock(now)
}

func (c *CachedOptionsPCR) GetOptionsPCR(ctx context.Context, asOf time.Time) (OptionsPCR, error) {
	var out OptionsPCR
	err := walkBackTradingDays(asOf, c.now(), func(probe time.Time) (bool, error) {
		key := probe.Format("20060102")
		v, found, err := c.cache.GetOrFetch(key, ttlForAsOf(probe, c.now()), func() (OptionsPCR, bool, error) {
			return c.upstream.FetchPCRExact(ctx, probe)
		})
		if err != nil {
			return false, err
		}
		if found {
			out = v
			return true, nil
		}
		return false, nil
	})
	return out, err
}

// CachedVIX wraps a VIXExactProvider. Walk-back probes ~2 months back
// (matching the upstream's monthly dump fallback) by walking days and
// skipping weekends like the other caches.
type CachedVIX struct {
	upstream VIXExactProvider
	cache    *ttlcache.Cache[string, VIX]
	now      func() time.Time
}

func NewCachedVIX(upstream VIXExactProvider, capacity int) *CachedVIX {
	return &CachedVIX{
		upstream: upstream,
		cache:    ttlcache.New[string, VIX](capacity),
		now:      time.Now,
	}
}

func (c *CachedVIX) SetClock(now func() time.Time) {
	c.now = now
	c.cache.SetClock(now)
}

func (c *CachedVIX) GetVIX(ctx context.Context, asOf time.Time) (VIX, error) {
	var out VIX
	err := walkBackTradingDays(asOf, c.now(), func(probe time.Time) (bool, error) {
		key := probe.Format("20060102")
		v, found, err := c.cache.GetOrFetch(key, ttlForAsOf(probe, c.now()), func() (VIX, bool, error) {
			return c.upstream.FetchVIXExact(ctx, probe)
		})
		if err != nil {
			return false, err
		}
		if found {
			out = v
			return true, nil
		}
		return false, nil
	})
	return out, err
}
