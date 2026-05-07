package twse

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"

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

// holdersSuccessTTL is the cache hold time for a successful TDCC dump
// fetch. TDCC publishes once per week, so a 24h TTL keeps the cache
// fresh without re-downloading the 9.5 MiB dump on every request.
//
// Single TTL (vs the dual-TTL design hint of 24h success / 1h failure):
// ttlcache.GetOrFetch takes one TTL per call, decided BEFORE the fetch
// returns. Splitting based on found=true vs found=false would require
// either bypassing GetOrFetch's singleflight (loses stampede control)
// or duplicating it (extra plumbing for marginal benefit). TDCC publish
// gaps are rare (TDCC has shipped weekly since the 集保 system began);
// holding a found=false sentinel for 24h instead of 1h adds at most a
// 23h window where holders rows go missing on a stock — acceptable
// given holders is non-load-bearing for the rest of /stock/:symbol.
const holdersSuccessTTL = 24 * time.Hour

// CachedHolders wraps a HoldersExactProvider with a single-key TTL
// cache + singleflight (via ttlcache). Unlike the other Cached* types,
// there is no walkback — the TDCC OpenAPI 1-5 endpoint always serves
// the latest weekly snapshot, so one cache key covers all asOf inputs.
// Per-stock lookup is a map hit on the parsed dump; concurrent /stock
// requests for different stocks during the same week converge on a
// single 9.5 MiB fetch.
//
// Concentration-drift rendering (the ▲/▼ pp pill on the 大戶 line)
// requires the prior week's dump for diff. The cache layer keeps a
// rotation buffer in-memory: lastDump shadows the latest successful
// fetch; previous holds whatever lastDump held when a fresh AsOf
// supersedes it. On cold start (or pod restart without snapshot
// previous), previous stays zero and the renderer omits the pill —
// option (b): warm-up rather than fake a baseline.
type CachedHolders struct {
	upstream HoldersExactProvider
	cache    *ttlcache.Cache[string, holdersDump]
	now      func() time.Time

	mu       sync.Mutex
	lastDump holdersDump // mirror of latest successful fetch (or restored snapshot)
	previous holdersDump // rotated-out predecessor; zero until a fresh AsOf supersedes lastDump
}

// NewCachedHolders returns a cached wrapper around upstream. cap is
// the LRU eviction threshold; pass 0 for ttlcache.DefaultCap. Only one
// entry is ever held (key=holdersCacheKey), so cap mostly governs the
// LRU bookkeeping overhead — the default is fine.
func NewCachedHolders(upstream HoldersExactProvider, capacity int) *CachedHolders {
	return &CachedHolders{
		upstream: upstream,
		cache:    ttlcache.New[string, holdersDump](capacity),
		now:      time.Now,
	}
}

// SetClock overrides the wall-clock for tests.
func (c *CachedHolders) SetClock(now func() time.Time) {
	c.now = now
	c.cache.SetClock(now)
}

// holdersCacheKey is the single key under which the parsed dump lives.
// The dump is universe-wide and date-pinned by upstream, so caching at
// any other granularity (per-stock, per-asOf) would just waste memory.
const holdersCacheKey = "latest"

// GetHoldersDistribution implements HoldersProvider via the cached
// dump. Concurrent callers for the same week converge on one fetch
// (ttlcache singleflight); per-stock lookup is a map hit on the
// shared dump. asOf is informational here — staleness gating against
// the dump's published AsOf is the renderer's job (see HoldersFreshFor
// in holders.go).
//
// On a fresh fetch with a NEW AsOf (different from lastDump.AsOf), the
// outgoing lastDump rotates into previous so the renderer can compute
// week-over-week concentration drift. The rotation runs only inside
// the fetch closure, so a sustained burst of cache hits doesn't churn
// the previous slot.
func (c *CachedHolders) GetHoldersDistribution(ctx context.Context, stockID string, _ time.Time) (HoldersDistribution, error) {
	dump, found, err := c.cache.GetOrFetch(holdersCacheKey, holdersSuccessTTL, func() (holdersDump, bool, error) {
		d, ok, ferr := c.upstream.FetchHoldersExact(ctx)
		if ferr != nil || !ok {
			return d, ok, ferr
		}
		c.mu.Lock()
		if !c.lastDump.AsOf.IsZero() && !c.lastDump.AsOf.Equal(d.AsOf) {
			c.previous = c.lastDump
		}
		c.lastDump = d
		c.mu.Unlock()
		return d, true, nil
	})
	if err != nil {
		return HoldersDistribution{}, err
	}
	if !found {
		return HoldersDistribution{}, ErrUnavailable
	}

	out, err := holdersLookup(dump, stockID)
	if err != nil {
		return out, err
	}

	c.mu.Lock()
	prev := c.previous
	c.mu.Unlock()
	if !prev.AsOf.IsZero() {
		if pr, ok := prev.Rows[stockID]; ok {
			out.PrevAsOf = prev.AsOf
			out.PrevTiers = pr.Tiers
			out.PrevTotalCount = pr.TotalCount
			out.PrevTotalShare = pr.TotalShare
		}
	}
	return out, nil
}

// CachedBlockTrades wraps a BlockTradesExactProvider with a single-key
// TTL cache + singleflight (via ttlcache). Like CachedHolders, there
// is no walkback — the BFIAUU OpenAPI endpoint always serves the
// latest published snapshot, so one cache key covers all asOf inputs.
// TTL uses the same publish-window-aware logic as the per-stock daily
// caches: today before ~17:00 caps to 30 minutes (so the cache flips
// fresh once afterTrading lands), past dates / post-publish hold for
// 24h.
type CachedBlockTrades struct {
	upstream BlockTradesExactProvider
	cache    *ttlcache.Cache[string, BlockTradesDay]
	now      func() time.Time
}

// NewCachedBlockTrades returns a cached wrapper around upstream. cap
// is the LRU eviction threshold; pass 0 for ttlcache.DefaultCap. Only
// one entry is ever held (key=blockTradesCacheKey).
func NewCachedBlockTrades(upstream BlockTradesExactProvider, capacity int) *CachedBlockTrades {
	return &CachedBlockTrades{
		upstream: upstream,
		cache:    ttlcache.New[string, BlockTradesDay](capacity),
		now:      time.Now,
	}
}

// SetClock overrides the wall-clock for tests.
func (c *CachedBlockTrades) SetClock(now func() time.Time) {
	c.now = now
	c.cache.SetClock(now)
}

// blockTradesCacheKey is the single key under which the parsed snapshot
// lives. The dump is universe-wide and date-pinned by upstream, so
// caching at any other granularity would just waste memory.
const blockTradesCacheKey = "latest"

// GetBlockTrades implements BlockTradesProvider via the cached snapshot.
// Concurrent callers for the same publish window converge on one
// fetch; per-stock lookup is a slice copy from the parsed map.
// Returns an empty slice (not error) when the stock has no block
// trades on the resolved date — the renderer treats len(trades) == 0
// as "no row to render," same as a missing-data case.
func (c *CachedBlockTrades) GetBlockTrades(ctx context.Context, stockID string, asOf time.Time) (BlockTradesDay, []BlockTrade, error) {
	asOf = clampAsOfTWAt(asOf, c.now())
	day, found, err := c.cache.GetOrFetch(blockTradesCacheKey, ttlForAsOf(asOf, c.now()), func() (BlockTradesDay, bool, error) {
		return c.upstream.FetchBlockTradesExact(ctx, asOf)
	})
	if err != nil {
		return BlockTradesDay{}, nil, err
	}
	if !found {
		return day, nil, nil
	}
	return day, day.Rows[strings.TrimSpace(stockID)], nil
}

// CachedForeign wraps a ForeignExactProvider with a per-(stockID,
// date) walk-back cache, mirroring CachedSecuritiesLending /
// CachedStockMargin. The rwd MI_QFIIS endpoint is date-pinned (not
// latest-only like the OpenAPI providers), so walk-back across
// publish gaps is required — most weekly publish cycles emit data
// for every trading day, but holiday windows can skip several
// consecutive dates.
type CachedForeign struct {
	upstream ForeignExactProvider
	cache    *ttlcache.Cache[string, Foreign]
	now      func() time.Time
}

func NewCachedForeign(upstream ForeignExactProvider, capacity int) *CachedForeign {
	return &CachedForeign{
		upstream: upstream,
		cache:    ttlcache.New[string, Foreign](capacity),
		now:      time.Now,
	}
}

func (c *CachedForeign) SetClock(now func() time.Time) {
	c.now = now
	c.cache.SetClock(now)
}

func (c *CachedForeign) GetForeign(ctx context.Context, stockID string, asOf time.Time) (Foreign, error) {
	var out Foreign
	err := walkBackTradingDays(asOf, c.now(), func(probe time.Time) (bool, error) {
		date := probe.Format("20060102")
		key := stockID + "|" + date
		v, found, err := c.cache.GetOrFetch(key, ttlForAsOf(probe, c.now()), func() (Foreign, bool, error) {
			return c.upstream.FetchForeignExact(ctx, stockID, date)
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

// CachedListingInfo wraps a ListingInfoExactProvider with a single-key
// 24h cache. The t187ap03_L upstream is essentially static (only
// changes on IPOs / renames), so cache TTL is fixed rather than
// publish-window-aware. No walkback — the upstream serves the
// latest snapshot only.
type CachedListingInfo struct {
	upstream ListingInfoExactProvider
	cache    *ttlcache.Cache[string, ListingInfoDump]
	now      func() time.Time
}

func NewCachedListingInfo(upstream ListingInfoExactProvider, capacity int) *CachedListingInfo {
	return &CachedListingInfo{
		upstream: upstream,
		cache:    ttlcache.New[string, ListingInfoDump](capacity),
		now:      time.Now,
	}
}

func (c *CachedListingInfo) SetClock(now func() time.Time) {
	c.now = now
	c.cache.SetClock(now)
}

const listingInfoCacheKey = "latest"
const listingInfoSuccessTTL = 24 * time.Hour

func (c *CachedListingInfo) GetListingInfo(ctx context.Context, stockID string, asOf time.Time) (ListingInfo, error) {
	dump, found, err := c.cache.GetOrFetch(listingInfoCacheKey, listingInfoSuccessTTL, func() (ListingInfoDump, bool, error) {
		return c.upstream.FetchListingInfoExact(ctx, asOf)
	})
	if err != nil {
		return ListingInfo{}, err
	}
	if !found {
		return ListingInfo{}, ErrUnavailable
	}
	info, ok := dump.Rows[strings.TrimSpace(stockID)]
	if !ok {
		return ListingInfo{}, ErrUnavailable
	}
	return info, nil
}

// CachedOTCListingInfo wraps an OTCListingInfoExactProvider — the
// TPEx parallel to CachedListingInfo. Same shape: single-key 24h TTL.
// Distinct cache instance because the underlying upstream is a
// different domain + schema; LRU is independent so an evicted TWSE
// entry doesn't churn an OTC entry.
type CachedOTCListingInfo struct {
	upstream OTCListingInfoExactProvider
	cache    *ttlcache.Cache[string, ListingInfoDump]
	now      func() time.Time
}

func NewCachedOTCListingInfo(upstream OTCListingInfoExactProvider, capacity int) *CachedOTCListingInfo {
	return &CachedOTCListingInfo{
		upstream: upstream,
		cache:    ttlcache.New[string, ListingInfoDump](capacity),
		now:      time.Now,
	}
}

func (c *CachedOTCListingInfo) SetClock(now func() time.Time) {
	c.now = now
	c.cache.SetClock(now)
}

const otcListingInfoCacheKey = "latest"

func (c *CachedOTCListingInfo) GetOTCListingInfo(ctx context.Context, stockID string, asOf time.Time) (ListingInfo, error) {
	dump, found, err := c.cache.GetOrFetch(otcListingInfoCacheKey, listingInfoSuccessTTL, func() (ListingInfoDump, bool, error) {
		return c.upstream.FetchOTCListingInfoExact(ctx, asOf)
	})
	if err != nil {
		return ListingInfo{}, err
	}
	if !found {
		return ListingInfo{}, ErrUnavailable
	}
	info, ok := dump.Rows[strings.TrimSpace(stockID)]
	if !ok {
		return ListingInfo{}, ErrUnavailable
	}
	return info, nil
}

// CachedIndustryForeign wraps an IndustryForeignExactProvider with a
// single-key 24h cache. The MI_QFIIS_cat upstream is daily; one fetch
// covers all 36 industry rows. Render path uses the resolved industry
// NAME from listingInfo.IndustryName (resolved via twseIndustryNames)
// as the lookup key — a stock id alone is not enough.
type CachedIndustryForeign struct {
	upstream IndustryForeignExactProvider
	cache    *ttlcache.Cache[string, IndustryForeignDump]
	now      func() time.Time
}

func NewCachedIndustryForeign(upstream IndustryForeignExactProvider, capacity int) *CachedIndustryForeign {
	return &CachedIndustryForeign{
		upstream: upstream,
		cache:    ttlcache.New[string, IndustryForeignDump](capacity),
		now:      time.Now,
	}
}

func (c *CachedIndustryForeign) SetClock(now func() time.Time) {
	c.now = now
	c.cache.SetClock(now)
}

const industryForeignCacheKey = "latest"
const industryForeignSuccessTTL = 24 * time.Hour

func (c *CachedIndustryForeign) GetIndustryForeign(ctx context.Context, industryName string, asOf time.Time) (IndustryForeign, error) {
	dump, found, err := c.cache.GetOrFetch(industryForeignCacheKey, industryForeignSuccessTTL, func() (IndustryForeignDump, bool, error) {
		return c.upstream.FetchIndustryForeignExact(ctx, asOf)
	})
	if err != nil {
		return IndustryForeign{}, err
	}
	if !found {
		return IndustryForeign{}, ErrUnavailable
	}
	row, ok := lookupIndustryForeign(dump, industryName)
	if !ok {
		return IndustryForeign{}, ErrUnavailable
	}
	return row, nil
}

// lookupIndustryForeign resolves a t187ap03_L industry name (from
// twseIndustryNames, e.g. "金融保險業") to a MI_QFIIS_cat row keyed
// by a slightly different naming convention. The aggregate file
// drops the 業 suffix on a few industries ("金融保險" not "金融保險業")
// and consolidates a few smaller categories (電子商務, 文化創意業,
// 農業科技業 are absent from the aggregate). Tries exact match first,
// then strips a trailing 業 character — covers the known mismatches
// without a separate translation table.
func lookupIndustryForeign(dump IndustryForeignDump, industryName string) (IndustryForeign, bool) {
	industryName = strings.TrimSpace(industryName)
	if row, ok := dump.Rows[industryName]; ok {
		return row, true
	}
	// MI_QFIIS_cat sometimes drops the 業 suffix that t187ap03_L
	// includes (金融保險 vs 金融保險業 is the canonical case). Try
	// stripping it once.
	if stripped := strings.TrimSuffix(industryName, "業"); stripped != industryName {
		if row, ok := dump.Rows[stripped]; ok {
			return row, true
		}
	}
	return IndustryForeign{}, false
}

// CachedFundamentals wraps a FundamentalsExactProvider with a
// single-key TTL cache + singleflight (via ttlcache). Same shape as
// CachedBlockTrades — BWIBBU_d serves only the latest publication, so
// one cache key covers all asOf inputs and TTL follows the
// publish-window-aware logic.
type CachedFundamentals struct {
	upstream FundamentalsExactProvider
	cache    *ttlcache.Cache[string, FundamentalsDump]
	now      func() time.Time
}

func NewCachedFundamentals(upstream FundamentalsExactProvider, capacity int) *CachedFundamentals {
	return &CachedFundamentals{
		upstream: upstream,
		cache:    ttlcache.New[string, FundamentalsDump](capacity),
		now:      time.Now,
	}
}

func (c *CachedFundamentals) SetClock(now func() time.Time) {
	c.now = now
	c.cache.SetClock(now)
}

const fundamentalsCacheKey = "latest"

func (c *CachedFundamentals) GetFundamentals(ctx context.Context, stockID string, asOf time.Time) (Fundamentals, error) {
	asOf = clampAsOfTWAt(asOf, c.now())
	dump, found, err := c.cache.GetOrFetch(fundamentalsCacheKey, ttlForAsOf(asOf, c.now()), func() (FundamentalsDump, bool, error) {
		return c.upstream.FetchFundamentalsExact(ctx, asOf)
	})
	if err != nil {
		return Fundamentals{}, err
	}
	if !found {
		return Fundamentals{}, ErrUnavailable
	}
	f, ok := dump.Rows[strings.TrimSpace(stockID)]
	if !ok {
		return Fundamentals{}, ErrUnavailable
	}
	return f, nil
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

// Per-provider snapshot filenames under the cache dir. Each cache
// owns its own file so a schema change to one value type doesn't
// invalidate the others.
const (
	snapshotT86           = "twse-t86.json"
	snapshotLending       = "twse-securities-lending.json"
	snapshotMargin        = "twse-stock-margin.json"
	snapshotRetailFutures = "taifex-retail-futures.json"
	snapshotOptionsPCR    = "taifex-options-pcr.json"
	snapshotVIX           = "taifex-vix.json"
	snapshotHolders       = "tdcc-holders.json"
	snapshotBlockTrades   = "twse-block-trades.json"
	snapshotFundamentals  = "twse-fundamentals.json"
)

// LoadSnapshots restores cached entries from JSON files under dir.
// Missing files are no-ops (cold start). Schema-mismatched files
// are deleted so the next save writes a current-schema replacement.
// Other read errors are logged but don't block startup.
func (c *Cached) LoadSnapshots(dir string) {
	if dir == "" || c == nil {
		return
	}
	loadOne(c.perStockT86, dir, snapshotT86)
	loadOne(c.perStockLending, dir, snapshotLending)
	loadOne(c.perStockMargin, dir, snapshotMargin)
	loadOne(c.taifexFutures, dir, snapshotRetailFutures)
	loadOne(c.taifexPCR, dir, snapshotOptionsPCR)
	loadOne(c.taifexVIX, dir, snapshotVIX)
	loadOne(c.cachedHolders, dir, snapshotHolders)
	loadOne(c.cachedBlockTrades, dir, snapshotBlockTrades)
	loadOne(c.cachedFundamentals, dir, snapshotFundamentals)
}

// SaveSnapshots writes each cache's live entries to dir as JSON.
// Failures are logged per-cache but don't stop the rest from saving;
// disk-cache benefits are best-effort by design (SIGKILL / OOM /
// panic still skip the save).
func (c *Cached) SaveSnapshots(dir string) {
	if dir == "" || c == nil {
		return
	}
	saveOne(c.perStockT86, dir, snapshotT86)
	saveOne(c.perStockLending, dir, snapshotLending)
	saveOne(c.perStockMargin, dir, snapshotMargin)
	saveOne(c.taifexFutures, dir, snapshotRetailFutures)
	saveOne(c.taifexPCR, dir, snapshotOptionsPCR)
	saveOne(c.taifexVIX, dir, snapshotVIX)
	saveOne(c.cachedHolders, dir, snapshotHolders)
	saveOne(c.cachedBlockTrades, dir, snapshotBlockTrades)
	saveOne(c.cachedFundamentals, dir, snapshotFundamentals)
}

// snapshotter is the minimal interface every CachedFoo exposes for
// disk persistence. Each wrapper's Save/Load just delegate to the
// underlying ttlcache.
type snapshotter interface {
	loadFrom(path string) error
	saveTo(path string) error
}

func loadOne(s snapshotter, dir, filename string) {
	if s == nil {
		return
	}
	path := filepath.Join(dir, filename)
	if err := s.loadFrom(path); err != nil {
		if errors.Is(err, ttlcache.ErrSchemaMismatch) {
			log.Warn().Err(err).Str("path", path).Msg("ttlcache: dropping stale snapshot (schema mismatch)")
			_ = removeFile(path)
			return
		}
		if errors.Is(err, ttlcache.ErrSnapshotTooLarge) {
			log.Warn().Err(err).Str("path", path).Msg("ttlcache: dropping oversize snapshot")
			_ = removeFile(path)
			return
		}
		log.Warn().Err(err).Str("path", path).Msg("ttlcache: snapshot load failed; cold-starting")
	}
}

func saveOne(s snapshotter, dir, filename string) {
	if s == nil {
		return
	}
	path := filepath.Join(dir, filename)
	if err := s.saveTo(path); err != nil {
		log.Warn().Err(err).Str("path", path).Msg("ttlcache: snapshot save failed")
	}
}

// removeFile is split out so tests can replace it via package-level
// rebinding if needed; production calls os.Remove with a best-effort
// failure mode.
var removeFile = os.Remove

// Per-wrapper snapshotter conformance — each delegates to the
// underlying ttlcache via SaveJSONFile / LoadJSONFile.
func (c *CachedT86) loadFrom(path string) error { return ttlcache.LoadJSONFile(c.cache, path) }
func (c *CachedT86) saveTo(path string) error   { return ttlcache.SaveJSONFile(c.cache, path) }
func (c *CachedSecuritiesLending) loadFrom(path string) error {
	return ttlcache.LoadJSONFile(c.cache, path)
}
func (c *CachedSecuritiesLending) saveTo(path string) error {
	return ttlcache.SaveJSONFile(c.cache, path)
}
func (c *CachedStockMargin) loadFrom(path string) error { return ttlcache.LoadJSONFile(c.cache, path) }
func (c *CachedStockMargin) saveTo(path string) error   { return ttlcache.SaveJSONFile(c.cache, path) }
func (c *CachedRetailFutures) loadFrom(path string) error {
	return ttlcache.LoadJSONFile(c.cache, path)
}
func (c *CachedRetailFutures) saveTo(path string) error { return ttlcache.SaveJSONFile(c.cache, path) }
func (c *CachedOptionsPCR) loadFrom(path string) error  { return ttlcache.LoadJSONFile(c.cache, path) }
func (c *CachedOptionsPCR) saveTo(path string) error    { return ttlcache.SaveJSONFile(c.cache, path) }
func (c *CachedVIX) loadFrom(path string) error         { return ttlcache.LoadJSONFile(c.cache, path) }
func (c *CachedVIX) saveTo(path string) error           { return ttlcache.SaveJSONFile(c.cache, path) }
// CachedHolders.loadFrom mirrors the restored cache entry into
// lastDump so a subsequent fresh fetch (with a new AsOf) rotates the
// pre-restart dump into previous. Without this mirror, a pod restart
// would silently reset Δ tracking — the next fresh fetch would have
// no lastDump to rotate, and the warm-up window would extend across
// two publish cycles instead of one.
func (c *CachedHolders) loadFrom(path string) error {
	if err := ttlcache.LoadJSONFile(c.cache, path); err != nil {
		return err
	}
	if e, ok := c.cache.Get(holdersCacheKey); ok && e.Found {
		c.mu.Lock()
		c.lastDump = e.Value
		c.mu.Unlock()
	}
	return nil
}
func (c *CachedHolders) saveTo(path string) error { return ttlcache.SaveJSONFile(c.cache, path) }
func (c *CachedBlockTrades) loadFrom(path string) error {
	return ttlcache.LoadJSONFile(c.cache, path)
}
func (c *CachedBlockTrades) saveTo(path string) error { return ttlcache.SaveJSONFile(c.cache, path) }
func (c *CachedFundamentals) loadFrom(path string) error {
	return ttlcache.LoadJSONFile(c.cache, path)
}
func (c *CachedFundamentals) saveTo(path string) error {
	return ttlcache.SaveJSONFile(c.cache, path)
}
