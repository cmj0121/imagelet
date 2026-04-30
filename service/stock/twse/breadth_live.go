package twse

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cmj0121/imagelet/internal/safehttp"

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
	// security that traded on the previous session (上市). Filtered to
	// 4-digit non-zero-prefix codes for the breadth count.
	defaultStockUniverseEndpoint = "https://openapi.twse.com.tw/v1/exchangeReport/STOCK_DAY_ALL"

	// defaultOTCUniverseEndpoint is the TPEx OpenAPI feed listing every
	// 上櫃 security. Same 4-digit filter as the TSE side; the field
	// carrying the code differs (SecuritiesCompanyCode vs Code) so we
	// decode into a separate row shape.
	defaultOTCUniverseEndpoint = "https://www.tpex.org.tw/openapi/v1/tpex_mainboard_quotes"

	// defaultMISInfoEndpoint is TWSE's MIS real-time per-stock backend
	// (also serves OTC quotes). Build per-call queries with
	// `?ex_ch=tse_XXXX.tw|otc_XXXX.tw|…&json=1`.
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
// polling MIS across both the TSE (上市) and TPEx (上櫃) listed-equity
// universes. Counts are kept separate per exchange. Cached 30s on
// success, 60s on failure; concurrent callers during a miss are
// deduped via singleflight.
//
// Returns ErrUnavailable when the live pipeline is disabled (TSE
// universe or MIS endpoint unset). The OTC universe is optional —
// when its endpoint is empty the function still returns TSE-only
// breadth instead of erroring, so callers can opt into OTC by
// setting the TPEx endpoint and not break otherwise.
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

// fetchLiveBreadth runs the universe + MIS pipeline once per exchange.
// TSE (上市) is required — its absence is an error; OTC (上櫃) is
// optional and skipped silently when the universe endpoint is unset
// or fails. Universes are re-used from the in-process caches when
// fresh; MIS batches are fanned out with bounded concurrency across
// both exchanges combined.
func (p *HTTPProvider) fetchLiveBreadth(ctx context.Context) (LiveBreadth, error) {
	tseUniverse, err := p.fetchUniverse(ctx, exchangeTSE)
	if err != nil {
		return LiveBreadth{}, err
	}
	if len(tseUniverse) == 0 {
		return LiveBreadth{}, ErrUnavailable
	}

	var otcUniverse []string
	if p.otcUniverseEndpoint != "" {
		// OTC fetch is best-effort: a TPEx outage shouldn't drop the
		// 上市 row too. Log silently via empty universe → no OTC counts.
		if u, err := p.fetchUniverse(ctx, exchangeOTC); err == nil {
			otcUniverse = u
		}
	}

	tseBatches := chunkSymbols(tseUniverse, misBatchSize)
	otcBatches := chunkSymbols(otcUniverse, misBatchSize)
	type batchResult struct {
		exchange exchange
		quotes   []misStockInfo
		err      error
	}
	results := make([]batchResult, len(tseBatches)+len(otcBatches))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(misConcurrency)
	emit := func(idx int, ex exchange, batch []string) {
		g.Go(func() error {
			quotes, err := p.fetchMISBatch(gctx, ex, batch)
			results[idx] = batchResult{exchange: ex, quotes: quotes, err: err}
			// Per-batch errors don't fail the whole fetch; partial
			// breadth from the surviving batches is still useful and
			// MIS can flap on individual chunks.
			return nil
		})
	}
	for i, b := range tseBatches {
		emit(i, exchangeTSE, b)
	}
	for i, b := range otcBatches {
		emit(len(tseBatches)+i, exchangeOTC, b)
	}
	if err := g.Wait(); err != nil {
		return LiveBreadth{}, err
	}

	var out LiveBreadth
	var latestTick time.Time
	tseOK := false
	for _, r := range results {
		if r.err != nil {
			continue
		}
		if r.exchange == exchangeTSE {
			tseOK = true
		}
		for _, q := range r.quotes {
			direction, tick := classifyTick(q)
			switch r.exchange {
			case exchangeTSE:
				switch direction {
				case dirUp:
					out.TSEAdvance++
				case dirDown:
					out.TSEDecline++
				case dirFlat:
					out.TSEUnchanged++
				}
			case exchangeOTC:
				switch direction {
				case dirUp:
					out.OTCAdvance++
				case dirDown:
					out.OTCDecline++
				case dirFlat:
					out.OTCUnchanged++
				}
			}
			if tick.After(latestTick) {
				latestTick = tick
			}
		}
	}
	// At minimum the TSE pipeline must have produced something; an
	// all-OTC-only render isn't useful and likely indicates a TSE
	// outage we should surface.
	if !tseOK {
		return LiveBreadth{}, ErrUnavailable
	}
	out.AsOf = latestTick
	return out, nil
}

// exchange enumerates the two universes the live pipeline polls.
type exchange int

const (
	exchangeTSE exchange = iota // 上市 / TSE main board
	exchangeOTC                 // 上櫃 / TPEx OTC
)

// misPrefix returns the MIS ex_ch prefix for the exchange — `tse_`
// for TSE main, `otc_` for TPEx. MIS multiplexes both behind the
// same getStockInfo endpoint, distinguished by prefix.
func (e exchange) misPrefix() string {
	switch e {
	case exchangeOTC:
		return "otc_"
	default:
		return "tse_"
	}
}

// fetchUniverse pulls the listed-equity symbols for `ex` and filters
// to 4-digit non-zero-prefix codes. TSE reads STOCK_DAY_ALL with the
// "Code" field; TPEx reads tpex_mainboard_quotes with the
// "SecuritiesCompanyCode" field. Both cached 4h independently;
// concurrent misses share the in-flight refresh under universeMu.
func (p *HTTPProvider) fetchUniverse(ctx context.Context, ex exchange) ([]string, error) {
	p.universeMu.Lock()
	defer p.universeMu.Unlock()

	endpoint, cached, cachedAt := p.universeFor(ex)
	if len(cached) > 0 && time.Since(cachedAt) < universeRefreshTTL {
		return cached, nil
	}

	body, err := p.fetch(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	var raw []universeRow
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	symbols := make([]string, 0, len(raw))
	for _, r := range raw {
		code := r.Code
		if code == "" {
			code = r.SecuritiesCompanyCode
		}
		if stockCodeRegexp.MatchString(code) {
			symbols = append(symbols, code)
		}
	}
	p.storeUniverse(ex, symbols)
	return symbols, nil
}

// universeFor returns the endpoint URL plus the current cache snapshot
// for the given exchange. universeMu must be held.
func (p *HTTPProvider) universeFor(ex exchange) (endpoint string, cached []string, cachedAt time.Time) {
	switch ex {
	case exchangeOTC:
		return p.otcUniverseEndpoint, p.otcUniverseData, p.otcUniverseAt
	default:
		return p.universeEndpoint, p.universeData, p.universeAt
	}
}

// storeUniverse writes the freshly-fetched symbol list back to the
// appropriate cache slot. universeMu must be held.
func (p *HTTPProvider) storeUniverse(ex exchange, symbols []string) {
	now := time.Now()
	switch ex {
	case exchangeOTC:
		p.otcUniverseData = symbols
		p.otcUniverseAt = now
	default:
		p.universeData = symbols
		p.universeAt = now
	}
}

// fetchMISBatch issues one MIS getStockInfo call carrying up to
// misBatchSize symbols on the given exchange. The MIS endpoint
// requires a Referer header matching its own host or it returns
// rtcode 9999. Exchange picks the prefix (tse_ for 上市, otc_ for
// 上櫃); MIS multiplexes both behind the same handler.
func (p *HTTPProvider) fetchMISBatch(ctx context.Context, ex exchange, symbols []string) ([]misStockInfo, error) {
	if len(symbols) == 0 {
		return nil, nil
	}
	prefix := ex.misPrefix()
	parts := make([]string, len(symbols))
	for i, s := range symbols {
		parts[i] = prefix + s + ".tw"
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
	req.Header.Set("User-Agent", safehttp.DefaultUserAgent)
	req.Header.Set("Referer", "https://mis.twse.com.tw/stock/")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	safehttp.BoundBody(resp, safehttp.DefaultBodyCap)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mis: %s", resp.Status)
	}
	var raw misResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
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

// LookupName returns the Chinese short name of a TW listing — Yahoo's
// chart endpoint serves Latin names for `<id>.TW` symbols, so we use
// MIS getStockInfo's `n` field as the localized override. isOTC
// selects the MIS ex_ch prefix (`otc_` for 上櫃, `tse_` for 上市).
//
// Successful lookups are memoized in p.nameCache forever — names are
// stable enough that a process-lifetime cache is appropriate, with
// process restart absorbing any rare TWSE renames. Empty `n` (MIS
// returned a row but without a name field) and missing rows both
// surface as ErrUnavailable so callers fall back to the upstream Latin
// name. The MIS endpoint must be configured (default in New(), opt-in
// via SetLiveBreadthEndpoints in fixture tests); otherwise returns
// ErrUnavailable.
func (p *HTTPProvider) LookupName(ctx context.Context, stockID string, isOTC bool) (string, error) {
	if p.misInfoEndpoint == "" {
		return "", ErrUnavailable
	}
	if v, ok := p.nameCache.Load(stockID); ok {
		return v.(string), nil
	}
	ex := exchangeTSE
	if isOTC {
		ex = exchangeOTC
	}
	quotes, err := p.fetchMISBatch(ctx, ex, []string{stockID})
	if err != nil {
		return "", err
	}
	if len(quotes) == 0 {
		return "", ErrUnavailable
	}
	name := strings.TrimSpace(quotes[0].N)
	if name == "" {
		return "", ErrUnavailable
	}
	p.nameCache.Store(stockID, name)
	return name, nil
}

// misStockInfo mirrors the per-symbol payload returned by MIS
// getStockInfo. We pull only the fields needed for breadth + name
// lookup: code, name, previous close, latest trade, previous trade,
// session open, plus tick time.
type misStockInfo struct {
	Code string `json:"c"`  // stock code, e.g. "2330"
	N    string `json:"n"`  // Chinese short name, e.g. "台積電"
	NF   string `json:"nf"` // Chinese full name, e.g. "台灣積體電路製造股份有限公司"
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

// universeRow captures the symbol field across both universe feeds.
// TSE STOCK_DAY_ALL uses "Code"; TPEx tpex_mainboard_quotes uses
// "SecuritiesCompanyCode". Decoding both fields lets one struct
// serve both feeds — only one is populated per row.
type universeRow struct {
	Code                  string `json:"Code"`
	SecuritiesCompanyCode string `json:"SecuritiesCompanyCode"`
}
