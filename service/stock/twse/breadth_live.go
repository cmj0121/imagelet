package twse

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

// Live breadth pipeline: TWSE's afterTrading/MI_INDEX is post-close
// only, and there is no documented intra-day breadth endpoint. The
// fallback is to compute breadth ourselves by polling MIS's per-stock
// real-time API across the listed-equity universe and tallying up /
// down / unchanged from each stock's current vs prev-close prices.
//
// Two upstream calls feed this:
//   1. STOCK_DAY_ALL — published once per trading day, lists every
//      security that traded yesterday. We filter to 4-digit-code
//      stocks (1075-ish entries) — ETFs (00xxx), warrants (5+ digit),
//      and TDRs are excluded so the count matches MI_INDEX's "股票"
//      column. Cached 4h since the listed universe rarely changes.
//   2. MIS getStockInfo — the same backend that powers TWSE's live
//      ticker pages. Accepts batched queries
//      `?ex_ch=tse_2330.tw|tse_2317.tw|…` capped at ~50 symbols/call
//      (TWSE's UI uses similar limits). Each entry returns `z`
//      (current price), `pz` (previous trade price, fallback when `z`
//      is "-" between ticks), and `y` (prev-day close). We compare
//      the most-recent-trade price against `y` to classify direction.
//
// The aggregated result is cached for 30 seconds; concurrent callers
// during cache misses are deduped via singleflight so a burst of /stock
// requests collapses to one fan-out.

const (
	// defaultStockUniverseEndpoint is the TWSE OpenAPI feed listing every
	// security that traded on the previous session. Filtered to 4-digit
	// non-zero-prefix codes for the breadth count.
	defaultStockUniverseEndpoint = "https://openapi.twse.com.tw/v1/exchangeReport/STOCK_DAY_ALL"

	// defaultMISInfoEndpoint is TWSE's MIS real-time per-stock backend.
	// Build the query string per call: `?ex_ch=tse_XXXX.tw|…&json=1`.
	defaultMISInfoEndpoint = "https://mis.twse.com.tw/stock/api/getStockInfo.jsp"

	// misBatchSize caps the number of symbols per MIS request. TWSE's
	// own UI uses similar batching; going wider risks 4xx or silent
	// truncation. 50 keeps the universe at ~22 batches.
	misBatchSize = 50

	// misConcurrency bounds the number of in-flight MIS batches. Five
	// gives ~5× speedup vs sequential while staying conservative on
	// MIS's per-IP rate limit.
	misConcurrency = 5

	// universeRefreshTTL is how long we hold the listed-equity universe
	// before re-fetching STOCK_DAY_ALL. The list churns by 1–2 entries
	// per quarter (new IPOs, delistings); 4h is generous and keeps the
	// fast path stamp-free across a trading session.
	universeRefreshTTL = 4 * time.Hour

	// liveBreadthSuccessTTL caps how stale a successful breadth snapshot
	// can be before we re-fetch. The user's tolerance is "5 min or so"
	// — 30s sits well inside that and naturally batches concurrent
	// /stock readers.
	liveBreadthSuccessTTL = 30 * time.Second

	// liveBreadthFailureTTL backs off briefly after an upstream error
	// so a flapping MIS doesn't stampede our outbound.
	liveBreadthFailureTTL = 60 * time.Second
)

// stockCodeRegexp matches 4-digit non-zero-prefix codes — the
// conventional TWSE listed-equity range (1101 / 2330 / 8069 / etc.)
// excluding ETFs (00xx, 0050) and warrants (5+ digit).
var stockCodeRegexp = regexp.MustCompile(`^[1-9][0-9]{3}$`)

// FetchLiveBreadth returns an intra-day breadth snapshot computed by
// polling MIS across the TWSE-listed equity universe. Cached 30s on
// success, 60s on failure; concurrent callers during a miss are
// deduped via singleflight.
//
// Returns ErrUnavailable when the live pipeline is disabled (universe
// or MIS endpoint unset — e.g. NewWithEndpoints without a follow-up
// SetLiveBreadthEndpoints call). Transport errors propagate verbatim
// for caller-side fallback / cache decisions.
func (p *HTTPProvider) FetchLiveBreadth(ctx context.Context) (LiveBreadth, error) {
	if p.universeEndpoint == "" || p.misInfoEndpoint == "" {
		return LiveBreadth{}, ErrUnavailable
	}

	p.liveMu.Lock()
	cached, cachedAt, cachedErr, hasOK := p.liveData, p.liveAt, p.liveErr, p.liveHasOK
	p.liveMu.Unlock()

	now := time.Now()
	if hasOK && now.Sub(cachedAt) < liveBreadthSuccessTTL {
		return cached, nil
	}
	if !hasOK && cachedErr != nil && now.Sub(cachedAt) < liveBreadthFailureTTL {
		return LiveBreadth{}, cachedErr
	}

	val, err, _ := p.liveFlight.Do("live-breadth", func() (any, error) {
		v, e := p.fetchLiveBreadth(ctx)
		p.liveMu.Lock()
		p.liveAt = time.Now()
		if e == nil {
			p.liveData = v
			p.liveErr = nil
			p.liveHasOK = true
		} else {
			p.liveErr = e
		}
		p.liveMu.Unlock()
		return v, e
	})
	if err != nil {
		return LiveBreadth{}, err
	}
	return val.(LiveBreadth), nil
}

// fetchLiveBreadth runs the universe + MIS pipeline once. Universe is
// re-used from the in-process cache when fresh; MIS batches are fanned
// out with bounded concurrency.
func (p *HTTPProvider) fetchLiveBreadth(ctx context.Context) (LiveBreadth, error) {
	universe, err := p.fetchUniverse(ctx)
	if err != nil {
		return LiveBreadth{}, err
	}
	if len(universe) == 0 {
		return LiveBreadth{}, ErrUnavailable
	}

	batches := chunkSymbols(universe, misBatchSize)
	type batchResult struct {
		quotes []misStockInfo
		err    error
	}
	results := make([]batchResult, len(batches))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(misConcurrency)
	for i, batch := range batches {
		i, batch := i, batch
		g.Go(func() error {
			quotes, err := p.fetchMISBatch(gctx, batch)
			results[i] = batchResult{quotes: quotes, err: err}
			// Per-batch errors don't fail the whole fetch; partial
			// breadth from the surviving batches is still useful and
			// MIS can flap on individual chunks.
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return LiveBreadth{}, err
	}

	var out LiveBreadth
	var latestTick time.Time
	anySuccess := false
	for _, r := range results {
		if r.err != nil {
			continue
		}
		anySuccess = true
		for _, q := range r.quotes {
			direction, tick := classifyTick(q)
			switch direction {
			case dirUp:
				out.AdvanceCount++
			case dirDown:
				out.DeclineCount++
			case dirFlat:
				out.UnchangedCount++
			}
			if tick.After(latestTick) {
				latestTick = tick
			}
		}
	}
	if !anySuccess {
		return LiveBreadth{}, ErrUnavailable
	}
	out.AsOf = latestTick
	return out, nil
}

// fetchUniverse pulls the TWSE-listed equity symbols from STOCK_DAY_ALL
// and filters to 4-digit non-zero-prefix codes. Cached 4h; concurrent
// misses share the in-flight refresh under universeMu.
func (p *HTTPProvider) fetchUniverse(ctx context.Context) ([]string, error) {
	p.universeMu.Lock()
	defer p.universeMu.Unlock()

	if len(p.universeData) > 0 && time.Since(p.universeAt) < universeRefreshTTL {
		return p.universeData, nil
	}

	body, err := p.fetch(ctx, p.universeEndpoint)
	if err != nil {
		return nil, err
	}
	var raw []universeRow
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	symbols := make([]string, 0, len(raw))
	for _, r := range raw {
		if stockCodeRegexp.MatchString(r.Code) {
			symbols = append(symbols, r.Code)
		}
	}
	p.universeData = symbols
	p.universeAt = time.Now()
	return symbols, nil
}

// fetchMISBatch issues one MIS getStockInfo call carrying up to
// misBatchSize symbols. The MIS endpoint requires a Referer header
// matching its own host or it returns rtcode 9999.
func (p *HTTPProvider) fetchMISBatch(ctx context.Context, symbols []string) ([]misStockInfo, error) {
	if len(symbols) == 0 {
		return nil, nil
	}
	parts := make([]string, len(symbols))
	for i, s := range symbols {
		parts[i] = "tse_" + s + ".tw"
	}
	q := url.Values{
		"ex_ch": []string{strings.Join(parts, "|")},
		"json":  []string{"1"},
	}
	endpoint := p.misInfoEndpoint + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; imagelet/0.2)")
	req.Header.Set("Referer", "https://mis.twse.com.tw/stock/")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mis: %s", resp.Status)
	}
	var raw misResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	if raw.RTCode != "" && raw.RTCode != "0000" {
		return nil, fmt.Errorf("mis rtcode %s: %s", raw.RTCode, raw.RTMessage)
	}
	return raw.MsgArray, nil
}

// chunkSymbols splits the universe into equal-sized batches; the last
// batch is the remainder. Returns nil for an empty input.
func chunkSymbols(symbols []string, size int) [][]string {
	if size <= 0 || len(symbols) == 0 {
		return nil
	}
	batches := make([][]string, 0, (len(symbols)+size-1)/size)
	for i := 0; i < len(symbols); i += size {
		end := i + size
		if end > len(symbols) {
			end = len(symbols)
		}
		batches = append(batches, symbols[i:end])
	}
	return batches
}

// direction is the per-stock classification used to bucket each quote
// into advance / decline / unchanged. Untraded stocks (no recent tick)
// fall outside all three buckets and are skipped.
type direction int

const (
	dirUntraded direction = iota
	dirUp
	dirDown
	dirFlat
)

// classifyTick decides which breadth bucket a stock falls into and
// returns the trade-time of the observed tick (used to track
// LiveBreadth.AsOf as the latest tick across the batch). Falls back
// from `z` (latest trade) to `pz` (previous trade) and finally to
// `o` (session open) — MIS resets z/pz to "-" between snapshot
// windows, which can be most stocks at any given second; the open
// price is a stable per-day reference that keeps lightly-traded
// stocks in the count instead of silently dropping them.
func classifyTick(q misStockInfo) (direction, time.Time) {
	prevClose, err := strconv.ParseFloat(q.Y, 64)
	if err != nil || prevClose <= 0 {
		return dirUntraded, time.Time{}
	}
	current, ok := pickPrice(q.Z, q.PZ, q.O)
	if !ok {
		return dirUntraded, time.Time{}
	}
	tick := parseMISTime(q.D, q.T)
	switch {
	case current > prevClose:
		return dirUp, tick
	case current < prevClose:
		return dirDown, tick
	default:
		return dirFlat, tick
	}
}

// pickPrice walks the z → pz → o fallback chain. MIS uses "-" as the
// "no value yet" sentinel; z and pz reset between snapshot windows
// even for actively-trading stocks, so without the o fallback the
// breadth count drops the bulk of the universe at any given moment.
// All three "-" simultaneously means the stock genuinely has no
// session price (halt, new listing) — caller treats as untraded.
func pickPrice(z, pz, o string) (float64, bool) {
	for _, candidate := range []string{z, pz, o} {
		if candidate == "" || candidate == "-" {
			continue
		}
		v, err := strconv.ParseFloat(candidate, 64)
		if err == nil && v > 0 {
			return v, true
		}
	}
	return 0, false
}

// parseMISTime combines MIS's d (YYYYMMDD) and t (HH:MM:SS) fields
// into a Taipei-local time.Time. Returns the zero value on parse
// failure — caller treats that as "no tick" for AsOf tracking.
func parseMISTime(d, t string) time.Time {
	if d == "" || t == "" {
		return time.Time{}
	}
	out, err := time.ParseInLocation("20060102 15:04:05", d+" "+t, twLoc)
	if err != nil {
		return time.Time{}
	}
	return out
}

// misStockInfo mirrors the per-symbol payload returned by MIS
// getStockInfo. We pull only the fields needed for breadth: code,
// previous close, latest trade, previous trade, session open, plus
// tick time.
type misStockInfo struct {
	Code string `json:"c"`  // stock code, e.g. "2330"
	Y    string `json:"y"`  // previous-day close
	Z    string `json:"z"`  // latest trade price ("-" when no recent tick)
	PZ   string `json:"pz"` // previous trade price (fallback for Z)
	O    string `json:"o"`  // session open price (final fallback)
	D    string `json:"d"`  // tick date YYYYMMDD
	T    string `json:"t"`  // tick time HH:MM:SS
}

// misResponse mirrors the top-level shape of MIS getStockInfo.
type misResponse struct {
	RTCode    string         `json:"rtcode"`
	RTMessage string         `json:"rtmessage"`
	MsgArray  []misStockInfo `json:"msgArray"`
}

// universeRow captures the only field we read from STOCK_DAY_ALL.
type universeRow struct {
	Code string `json:"Code"`
}
