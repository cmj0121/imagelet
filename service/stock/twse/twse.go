// Package twse provides Taiwan-specific market enrichment for the /stock
// service: aggregate institutional buy/sell flow (三大法人 BFI82U) and
// market-wide margin balance (融資融券 MI_MARGN aggregate).
//
// Both endpoints are served by the legacy TWSE web data API at
// www.twse.com.tw -- key-free, no auth, JSON. The data updates once
// per trading day (~16:00 Asia/Taipei) so a 4h success TTL keeps the
// service fresh without hammering upstream. Both endpoints return CE
// dates (YYYYMMDD), not ROC, despite some adjacent endpoints using ROC.
//
// Used by /stock's TW-only enrichment block. Other regions skip this
// provider entirely.
package twse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cmj0121/imagelet/internal/safehttp"

	"golang.org/x/sync/singleflight"
)

// twLoc is Asia/Taipei resolved once at package init. tzdata is bundled via
// the time/tzdata import in cmd/imagelet/main.go so LoadLocation should not
// fail in any deploy target; the FixedZone fallback covers stripped images.
var twLoc = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		return time.FixedZone("Asia/Taipei", 8*3600)
	}
	return loc
}()

const (
	// defaultBFI82UEndpoint returns the daily aggregate three-institutional
	// flow with one row per category (自營商 self-trade, 自營商 hedge, 投信,
	// 外資及陸資, 外資自營商, 合計). %s = YYYYMMDD CE date.
	defaultBFI82UEndpoint = "https://www.twse.com.tw/rwd/zh/fund/BFI82U?dayDate=%s&type=day&response=json"

	// defaultMIMARGNEndpoint returns market-wide aggregate margin balance:
	// 融資 (long) + 融券 (short) in trading units (張), and 融資金額 in
	// NTD thousands. %s = YYYYMMDD CE date. selectType=MS gives the
	// aggregated market summary (single tables[0] block); per-stock
	// breakdown lives at a different selectType.
	defaultMIMARGNEndpoint = "https://www.twse.com.tw/rwd/zh/marginTrading/MI_MARGN?date=%s&selectType=MS&response=json"

	// defaultMIINDEXEndpoint returns daily TAIEX statistics including the
	// 漲跌證券數合計 breadth table (上漲 / 下跌 / 持平 / 未成交 counts
	// split into 整體市場 vs 股票 columns). %s = YYYYMMDD CE date.
	// type=ALLBUT0999 returns the all-securities-but-warrants summary
	// which is the conventional source for headline breadth metrics.
	defaultMIINDEXEndpoint = "https://www.twse.com.tw/rwd/zh/afterTrading/MI_INDEX?date=%s&type=ALLBUT0999&response=json"

	// defaultT86Endpoint returns the per-stock 三大法人買賣超 (institutional
	// flow per individual stock) for one trading day — counts in SHARES
	// (not NTD, unlike the market-wide BFI82U). %s = YYYYMMDD CE date.
	// Each row carries one stock's buy / sell / net broken out across
	// the four institutional categories (外陸資 + 外資自營商 → foreign,
	// 投信 → trust, 自營商 → dealer, 合計 → total). selectType=ALLBUT0999
	// returns all listed equities except warrants — the canonical view
	// matching MI_INDEX.
	defaultT86Endpoint = "https://www.twse.com.tw/rwd/zh/fund/T86?date=%s&selectType=ALLBUT0999&response=json"
)

// MarketData captures the TW-specific daily aggregates the /stock TW
// path renders. Values are in raw NTD (not 億 / 萬); the renderer formats
// for display. AsOf is the trading date the upstream supplied.
//
// Net = Buy - Sell. Positive net means net inflow (institutional buying);
// negative means net outflow.
type MarketData struct {
	AsOf time.Time

	// 三大法人買賣金額統計表 (BFI82U) -- daily net buy/sell in raw NTD.
	// `Net` is the 合計 row directly from upstream (sum of the four
	// component rows); we surface it as authoritative rather than
	// recomputing locally.
	ForeignNet int64 // 外資及陸資 (incl. foreign self-trade row, summed)
	TrustNet   int64 // 投信
	DealerNet  int64 // 自營商 (self-trade + hedge, summed)
	Net        int64 // 合計 (entire-market net)

	// 信用交易統計 (MI_MARGN selectType=MS) -- aggregate market margin.
	// Values use upstream's units verbatim:
	//   - MarginLongTWD: 融資金額(仟元) 今日餘額 × 1000 = NTD raw.
	//   - MarginShortTWD: 融券金額(仟元) 今日餘額 × 1000 = NTD raw.
	//   - MarginLongLots: 融資(交易單位) 今日餘額 in 張.
	//   - MarginShortLots: 融券(交易單位) 今日餘額 in 張 (1 張 = 1000 shares).
	//
	// Long+Short pairs in matching units enable the lots-based retail
	// bull/bear ratio rendered in the credit-box section: positive
	// (longLots - shortLots) / (longLots + shortLots) -> retail leans
	// long-leveraged. The TWD pair is kept for the headline 融資餘額 /
	// 融券餘額 display.
	MarginLongTWD   int64
	MarginShortTWD  int64
	MarginLongLots  int64
	MarginShortLots int64

	// 大盤統計資訊 (MI_INDEX type=ALLBUT0999) — daily breadth from the
	// 漲跌證券數合計 table, "股票" column only (excludes ETFs / warrants
	// / bonds). Limit-up / limit-down counts are folded INTO Advance and
	// Decline respectively (TWSE reports them as `312(24)` "total(of
	// which limit)"); we keep just the totals to avoid sub-cell clutter.
	AdvanceCount   int64 // 上漲家數
	DeclineCount   int64 // 下跌家數
	UnchangedCount int64 // 持平家數
}

// HasInstitutional reports whether the BFI82U fetch produced any
// non-zero institutional flow. Renderer uses this to decide whether to
// draw the institutional row at all.
func (d MarketData) HasInstitutional() bool {
	return d.ForeignNet != 0 || d.TrustNet != 0 || d.DealerNet != 0 || d.Net != 0
}

// HasMargin reports whether MI_MARGN produced any non-zero margin
// balance. Same gate-renderer pattern as HasInstitutional.
func (d MarketData) HasMargin() bool {
	return d.MarginLongTWD != 0 || d.MarginShortTWD != 0 ||
		d.MarginLongLots != 0 || d.MarginShortLots != 0
}

// HasBreadth reports whether MI_INDEX produced a non-empty breadth
// reading (advance / decline / unchanged counts). Renderer gate.
func (d MarketData) HasBreadth() bool {
	return d.AdvanceCount != 0 || d.DeclineCount != 0 || d.UnchangedCount != 0
}

// BreadthScore returns a [-1, 1] score derived from advance vs decline
// counts. Unchanged is excluded from the denominator so the bar reflects
// directional bias of the day's actually-moving stocks. Returns 0 when
// the day produced no movers (avoids divide-by-zero).
func (d MarketData) BreadthScore() float64 {
	moving := d.AdvanceCount + d.DeclineCount
	if moving == 0 {
		return 0
	}
	return float64(d.AdvanceCount-d.DeclineCount) / float64(moving)
}

// RetailBullBearScore returns a [-1, 1] sentiment score from the
// margin-trade lots: +1 = entirely long, -1 = entirely short, 0 =
// balanced. Returns 0 when no lots are reported (avoids divide-by-zero
// and gives a neutral reading rather than a bogus extreme).
//
// Caveat: TW retail margin is structurally long-biased (融券 supply is
// constrained by 借券 availability), so the score sits well above zero
// most days. The bar is most useful for spotting drift relative to the
// usual baseline rather than as an absolute reading.
func (d MarketData) RetailBullBearScore() float64 {
	total := d.MarginLongLots + d.MarginShortLots
	if total == 0 {
		return 0
	}
	return float64(d.MarginLongLots-d.MarginShortLots) / float64(total)
}

// Provider abstracts the TWSE fetch so tests can swap in a fake.
// Get takes no symbol -- TWSE data is market-wide.
type Provider interface {
	Get(ctx context.Context) (MarketData, error)
}

// HistoricalProvider is an optional extension of Provider for fetching
// market-wide TWSE aggregates pinned to a historical date (?date=
// override path). GetAt walks back from asOf using the same lookback
// loop as Get, returning the latest published trading day at or before
// the requested date. Optional because not every Provider implementation
// can answer historical queries; the /stock handler type-asserts and
// surfaces ErrUnavailable when the wired provider doesn't implement it.
type HistoricalProvider interface {
	GetAt(ctx context.Context, asOf time.Time) (MarketData, error)
}

// StockData captures the per-stock 三大法人 net flow (in SHARES) for
// one trading day. Distinct from MarketData (market-wide aggregates in
// NTD) because:
//
//   - the source endpoint is different (T86 vs BFI82U),
//   - the units are different (shares per-stock vs NTD market-wide),
//   - the renderer presents them as alternate, not overlapping,
//     groups under the price box.
//
// Foreign sums two upstream columns: 外陸資 (excluding 外資自營商) +
// 外資自營商 — the renderer wants foreign flow holistically, not
// split by sub-category. Dealer sums 自營商(自行買賣) + 自營商(避險)
// via T86's pre-summed 自營商買賣超股數 column.
type StockData struct {
	StockID    string    // 證券代號 (e.g. "2330")
	Name       string    // 證券名稱 (Chinese name from upstream, trimmed)
	ForeignNet int64     // 外陸資 + 外資自營商 buy-sell delta, in shares
	TrustNet   int64     // 投信 buy-sell delta, in shares
	DealerNet  int64     // 自營商 buy-sell delta (self-trade + hedge), in shares
	Net        int64     // 三大法人 total buy-sell delta, in shares
	AsOf       time.Time // trading date the upstream supplied
}

// HasFlow reports whether the T86 fetch produced any non-zero
// per-stock institutional flow. Renderer gate.
func (d StockData) HasFlow() bool {
	return d.ForeignNet != 0 || d.TrustNet != 0 || d.DealerNet != 0 || d.Net != 0
}

// PerStockProvider is an optional extension for fetching per-stock
// 三大法人 flow keyed by TWSE stock ID (e.g. "2330"). GetForStock
// walks back from asOf the same way Get does, returning the latest
// published trading day with data for the requested stock. Returns
// ErrUnavailable when the lookback exhausts or the stock id is not
// found in any of the probed days. Stock IDs not listed on TWSE 上市
// (e.g. 上櫃 OTC) are not covered by T86 and surface ErrUnavailable
// — the handler renders without per-stock enrichment in that case.
type PerStockProvider interface {
	GetForStock(ctx context.Context, stockID string, asOf time.Time) (StockData, error)
}

// NameProvider is an optional extension for resolving the Chinese
// short name of a TW listing — Yahoo's chart endpoint returns a Latin-
// only shortName for `<id>.TW` symbols, so we fall back to TWSE's MIS
// getStockInfo endpoint which carries the localized 證券簡稱 in the
// `n` field. isOTC selects the MIS prefix (`tse_` for 上市, `otc_` for
// 上櫃). Returns ErrUnavailable when MIS has no row for the symbol or
// when the wired provider doesn't implement NameProvider; the /stock
// handler logs and falls back to the upstream Latin name.
type NameProvider interface {
	LookupName(ctx context.Context, stockID string, isOTC bool) (string, error)
}

// LiveBreadth is the intra-day breadth snapshot computed by polling
// MIS's per-stock real-time API and aggregating across the listed
// equity universes — split per exchange because TWSE 上市 (TSE main)
// and 上櫃 (TPEx OTC) are distinct markets with separate symbol
// universes (~1075 + ~883 4-digit stocks respectively) and trading
// participants tend to read them as separate signals.
//
// Distinct from MarketData.{Advance,Decline,Unchanged}Count — those
// are MI_INDEX afterTrading post-close totals (TSE only); LiveBreadth
// updates throughout the trading session as ticks land.
type LiveBreadth struct {
	// 上市 (TSE main board, queried via MIS tse_XXXX.tw)
	TSEAdvance   int64
	TSEDecline   int64
	TSEUnchanged int64

	// 上櫃 (TPEx OTC, queried via MIS otc_XXXX.tw)
	OTCAdvance   int64
	OTCDecline   int64
	OTCUnchanged int64

	AsOf time.Time // latest tick time observed across both universes
}

// HasTSEBreadth reports whether the live fetch produced any TSE
// (上市) counts. Renderer gates the 上市 row on this.
func (b LiveBreadth) HasTSEBreadth() bool {
	return b.TSEAdvance != 0 || b.TSEDecline != 0 || b.TSEUnchanged != 0
}

// HasOTCBreadth reports whether the live fetch produced any TPEx
// (上櫃) counts. Renderer gates the 上櫃 row on this.
func (b LiveBreadth) HasOTCBreadth() bool {
	return b.OTCAdvance != 0 || b.OTCDecline != 0 || b.OTCUnchanged != 0
}

// HasBreadth reports whether either universe produced counts.
// Renderer gate matching MarketData.HasBreadth so callers can branch
// uniformly.
func (b LiveBreadth) HasBreadth() bool {
	return b.HasTSEBreadth() || b.HasOTCBreadth()
}

// LiveBreadthProvider is implemented by providers that can compute
// intra-day breadth snapshots. Optional — Provider does not embed it.
// The /stock handler type-asserts and uses live breadth during open
// hours when the provider supports it; closed-hours render falls back
// to MI_INDEX afterTrading via Provider.Get.
type LiveBreadthProvider interface {
	FetchLiveBreadth(ctx context.Context) (LiveBreadth, error)
}

// ErrUnavailable signals "upstream answered but data is empty / no
// trading session" -- distinct from transport errors. Cache layer can
// extend backoff for this signal.
var ErrUnavailable = errors.New("twse: market data unavailable")

// HTTPProvider hits the legacy TWSE endpoints. UA is spoofed because Go's
// default User-Agent occasionally hits CDN gates.
type HTTPProvider struct {
	bfi82u        string
	miMargn       string
	miIndex       string
	t86           string // T86 per-stock 三大法人 endpoint (production-only; empty disables GetForStock)
	twt93u        string // TWT93U per-stock 信用額度總量管制 (production-only; empty disables GetSecuritiesLending)
	miMargnStock  string // MI_MARGN per-stock 融資/融券 (production-only; empty disables GetStockMargin)
	taifexFutures string // TAIFEX 三大法人區分各期貨契約 download (production-only; empty disables GetRetailFutures)
	taifexPCR     string // TAIFEX 臺指選擇權 Put/Call ratio (production-only; empty disables GetOptionsPCR)
	taifexVIX     string // TAIFEX VIX monthly dump URL template — must contain a single %s for YYYYMM
	holders       string // TDCC OpenAPI 1-5 集保戶股權分散表 (production-only; empty disables FetchHoldersExact)
	client        *http.Client

	// Live breadth state — separate plane from the daily Get() pipeline.
	// Empty endpoints disable FetchLiveBreadth; New() populates them with
	// production URLs, NewWithEndpoints leaves them empty so existing
	// fixture-server tests don't accidentally hit live infrastructure.
	universeEndpoint    string // STOCK_DAY_ALL — TSE 上市 universe
	otcUniverseEndpoint string // tpex_mainboard_quotes — 上櫃 universe
	misInfoEndpoint     string

	universeMu      sync.Mutex
	universeData    []string
	universeAt      time.Time
	otcUniverseData []string
	otcUniverseAt   time.Time

	liveMu     sync.Mutex
	liveData   LiveBreadth
	liveAt     time.Time
	liveErr    error
	liveHasOK  bool
	liveFlight singleflight.Group

	// nameCache memoizes per-stock CN name lookups across requests.
	// Names are stable enough that we cache them forever — TWSE does
	// rename listings occasionally (corporate actions) but at a cadence
	// that's fine to absorb on process restart. Keys are stockID
	// strings (e.g. "2330"), values are the trimmed CN short name.
	nameCache sync.Map
}

// New returns an HTTPProvider with a 5s per-request timeout against the
// production endpoints, including the live breadth pipeline and the
// T86 per-stock 三大法人 endpoint.
func New() *HTTPProvider {
	p := NewWithEndpoints(
		defaultBFI82UEndpoint,
		defaultMIMARGNEndpoint,
		defaultMIINDEXEndpoint,
		safehttp.NewClient(5*time.Second),
	)
	p.universeEndpoint = defaultStockUniverseEndpoint
	p.otcUniverseEndpoint = defaultOTCUniverseEndpoint
	p.misInfoEndpoint = defaultMISInfoEndpoint
	p.t86 = defaultT86Endpoint
	p.twt93u = defaultTWT93UEndpoint
	p.miMargnStock = defaultMIMARGNStockEndpoint
	p.taifexFutures = defaultTAIFEXFuturesEndpoint
	p.taifexPCR = defaultTAIFEXPCREndpoint
	p.taifexVIX = defaultTAIFEXVIXURLTemplate
	p.holders = defaultHoldersEndpoint
	return p
}

// NewWithEndpoints lets tests point the provider at httptest.Server URLs.
// Each template MUST contain exactly one %s for the YYYYMMDD CE date.
// The live breadth endpoints are NOT set here — call SetLiveBreadthEndpoints
// to enable that pipeline against a separate fixture server.
func NewWithEndpoints(bfi82u, miMargn, miIndex string, client *http.Client) *HTTPProvider {
	return &HTTPProvider{bfi82u: bfi82u, miMargn: miMargn, miIndex: miIndex, client: client}
}

// SetT86Endpoint overrides the upstream URL used by GetForStock, so
// tests can route to a fixture httptest.Server. Production callers
// should rely on New() which wires defaultT86Endpoint automatically.
// Empty disables GetForStock (returns ErrUnavailable).
func (p *HTTPProvider) SetT86Endpoint(endpoint string) {
	p.t86 = endpoint
}

// SetLiveBreadthEndpoints overrides the upstream URLs used by
// FetchLiveBreadth: `tseUniverse` is the TWSE OpenAPI STOCK_DAY_ALL
// feed for the 上市 universe, `otcUniverse` is the TPEx OpenAPI
// tpex_mainboard_quotes feed for 上櫃 (empty disables the OTC half),
// `misInfo` is the MIS getStockInfo endpoint accepting `?ex_ch=...`
// queries built per-call. Tests point these at httptest.Server URLs
// to exercise the live pipeline without touching production.
func (p *HTTPProvider) SetLiveBreadthEndpoints(tseUniverse, otcUniverse, misInfo string) {
	p.universeEndpoint = tseUniverse
	p.otcUniverseEndpoint = otcUniverse
	p.misInfoEndpoint = misInfo
}

// clampAsOfTW normalizes asOf to Asia/Taipei, defaulting to the current
// TW wall-clock when asOf is zero or future-dated. Shared by the daily-
// lookback fetchers so a single point governs the live-calendar guard.
func clampAsOfTW(asOf time.Time) time.Time {
	now := time.Now().In(twLoc)
	if asOf.IsZero() || asOf.After(now) {
		return now
	}
	return asOf.In(twLoc)
}

// maxLookbackDays caps the walk-back when probing for the most recent
// trading day with published data. 7 days covers any weekend plus
// 1–2 consecutive Taiwanese holidays (the longest in the TWSE
// calendar — Lunar New Year — runs ~5 days). Past this we surface
// ErrUnavailable so the cache layer can keep prior data fresh.
const maxLookbackDays = 7

// Get fetches both daily aggregates for the most recent completed
// trading day and merges them. TWSE publishes today's data only
// after market close (~16:00 Asia/Taipei), so a request before
// that — or on a weekend / holiday — must walk back to the last
// session that did publish. We probe today first, then yesterday,
// up to maxLookbackDays back; the first date that produces a
// non-empty BFI82U or MI_MARGN response wins. Saturdays and
// Sundays are skipped on the way down since TWSE is always closed.
//
// If either endpoint reports a transport error (vs ErrUnavailable
// no-data), we surface it immediately — that's a "service is sick"
// signal the cache layer should respect, not a "wrong date"
// fallback. Partial-day data (BFI present, MARGN missing or vice
// versa) is returned as-is; the renderer gates on Has* per-row.
func (p *HTTPProvider) Get(ctx context.Context) (MarketData, error) {
	return p.GetAt(ctx, time.Now())
}

// GetAt fetches both daily aggregates for the most recent published
// trading day at or before asOf. Same lookback semantics as Get —
// weekends are skipped and ErrUnavailable propagates through up to
// maxLookbackDays — but the walk starts from asOf rather than "now".
// Future-dated asOf is clamped to today to keep the loop bounded
// against the live calendar.
func (p *HTTPProvider) GetAt(ctx context.Context, asOf time.Time) (MarketData, error) {
	asOf = clampAsOfTW(asOf)

	for daysBack := 0; daysBack < maxLookbackDays; daysBack++ {
		probe := asOf.AddDate(0, 0, -daysBack)
		// TWSE is closed on weekends; skipping shaves up to 2
		// guaranteed-empty round trips per call.
		switch probe.Weekday() {
		case time.Saturday, time.Sunday:
			continue
		}
		merged, err := p.fetchForDate(ctx, probe.Format("20060102"))
		if err == nil {
			return merged, nil
		}
		if !errors.Is(err, ErrUnavailable) {
			return MarketData{}, err
		}
	}
	return MarketData{}, ErrUnavailable
}

// fetchForDate runs the three TWSE endpoints concurrently for a single
// YYYYMMDD probe. Returns ErrUnavailable when every endpoint responds
// with its no-data sentinel; otherwise returns the merged data
// (any single endpoint succeeding is enough for that row to render).
func (p *HTTPProvider) fetchForDate(ctx context.Context, date string) (MarketData, error) {
	type fetchResult struct {
		data MarketData
		err  error
	}
	bfiCh := make(chan fetchResult, 1)
	margnCh := make(chan fetchResult, 1)
	indexCh := make(chan fetchResult, 1)
	go func() {
		d, err := p.fetchBFI82U(ctx, date)
		bfiCh <- fetchResult{data: d, err: err}
	}()
	go func() {
		d, err := p.fetchMIMARGN(ctx, date)
		margnCh <- fetchResult{data: d, err: err}
	}()
	go func() {
		d, err := p.fetchMIIndex(ctx, date)
		indexCh <- fetchResult{data: d, err: err}
	}()
	bfi := <-bfiCh
	margn := <-margnCh
	index := <-indexCh

	if bfi.err != nil && !errors.Is(bfi.err, ErrUnavailable) {
		return MarketData{}, bfi.err
	}
	if margn.err != nil && !errors.Is(margn.err, ErrUnavailable) {
		return MarketData{}, margn.err
	}
	if index.err != nil && !errors.Is(index.err, ErrUnavailable) {
		return MarketData{}, index.err
	}

	merged := MarketData{
		AsOf:            bfi.data.AsOf,
		ForeignNet:      bfi.data.ForeignNet,
		TrustNet:        bfi.data.TrustNet,
		DealerNet:       bfi.data.DealerNet,
		Net:             bfi.data.Net,
		MarginLongTWD:   margn.data.MarginLongTWD,
		MarginShortTWD:  margn.data.MarginShortTWD,
		MarginLongLots:  margn.data.MarginLongLots,
		MarginShortLots: margn.data.MarginShortLots,
		AdvanceCount:    index.data.AdvanceCount,
		DeclineCount:    index.data.DeclineCount,
		UnchangedCount:  index.data.UnchangedCount,
	}
	if merged.AsOf.IsZero() {
		merged.AsOf = margn.data.AsOf
	}
	if merged.AsOf.IsZero() {
		merged.AsOf = index.data.AsOf
	}
	if !merged.HasInstitutional() && !merged.HasMargin() && !merged.HasBreadth() {
		return MarketData{}, ErrUnavailable
	}
	return merged, nil
}

func (p *HTTPProvider) fetchBFI82U(ctx context.Context, date string) (MarketData, error) {
	url := fmt.Sprintf(p.bfi82u, date)
	body, err := p.fetch(ctx, url)
	if err != nil {
		return MarketData{}, err
	}
	var raw bfi82uResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return MarketData{}, err
	}
	if raw.Stat != "OK" || len(raw.Data) == 0 {
		return MarketData{}, ErrUnavailable
	}
	out := MarketData{AsOf: parseTWSEDate(raw.Date)}
	for _, row := range raw.Data {
		if len(row) < 4 {
			continue
		}
		net := parseTWSENumber(row[3])
		switch normalizeBFICategory(row[0]) {
		case "外資":
			out.ForeignNet += net
		case "投信":
			out.TrustNet += net
		case "自營商":
			out.DealerNet += net
		case "合計":
			out.Net = net
		}
	}
	return out, nil
}

func (p *HTTPProvider) fetchMIMARGN(ctx context.Context, date string) (MarketData, error) {
	url := fmt.Sprintf(p.miMargn, date)
	body, err := p.fetch(ctx, url)
	if err != nil {
		return MarketData{}, err
	}
	var raw miMargnResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return MarketData{}, err
	}
	if raw.Stat != "OK" || len(raw.Tables) == 0 {
		return MarketData{}, ErrUnavailable
	}
	out := MarketData{AsOf: parseTWSEDate(raw.Date)}
	for _, row := range raw.Tables[0].Data {
		if len(row) < 6 {
			continue
		}
		// Column 5 (index) = 今日餘額 (today balance).
		balance := parseTWSENumber(row[5])
		switch {
		case strings.HasPrefix(row[0], "融資金額"):
			// "融資金額(仟元)" -- multiply by 1000 to get raw NTD.
			out.MarginLongTWD = balance * 1000
		case strings.HasPrefix(row[0], "融券金額"):
			out.MarginShortTWD = balance * 1000
		case strings.HasPrefix(row[0], "融資(交易單位)"):
			out.MarginLongLots = balance
		case strings.HasPrefix(row[0], "融券(交易單位)"):
			out.MarginShortLots = balance
		}
	}
	return out, nil
}

// fetchMIIndex pulls the daily 漲跌證券數合計 breadth table from MI_INDEX
// and lands counts in the 股票 column (excludes ETFs / warrants / bonds
// / corporate bonds — only listed common stocks). TWSE renders limit-up
// and limit-down counts inline as `total(limit)` (e.g. `312(24)` means
// 312 advancing of which 24 hit limit-up); we strip the parens and keep
// just the headline total.
//
// Looks up the breadth table by title prefix rather than index because
// MI_INDEX's tables array order isn't stable — TWSE has reshuffled it
// before. Missing table => ErrUnavailable, same as the other fetchers.
func (p *HTTPProvider) fetchMIIndex(ctx context.Context, date string) (MarketData, error) {
	url := fmt.Sprintf(p.miIndex, date)
	body, err := p.fetch(ctx, url)
	if err != nil {
		return MarketData{}, err
	}
	var raw miIndexResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return MarketData{}, err
	}
	if len(raw.Tables) == 0 {
		return MarketData{}, ErrUnavailable
	}
	out := MarketData{AsOf: parseTWSEDate(raw.Date)}
	for _, table := range raw.Tables {
		if !strings.HasPrefix(table.Title, "漲跌證券數合計") {
			continue
		}
		for _, row := range table.Data {
			// Row shape: [類型, 整體市場, 股票]. We use 股票 (index 2).
			if len(row) < 3 {
				continue
			}
			count := parseTWSENumber(stripLimitSubcount(row[2]))
			switch {
			case strings.HasPrefix(row[0], "上漲"):
				out.AdvanceCount = count
			case strings.HasPrefix(row[0], "下跌"):
				out.DeclineCount = count
			case strings.HasPrefix(row[0], "持平"):
				out.UnchangedCount = count
			}
		}
		break
	}
	if !out.HasBreadth() {
		return MarketData{}, ErrUnavailable
	}
	return out, nil
}

// GetForStock walks back from asOf for the most recent T86 publication
// containing the requested stock id. Same lookback semantics as Get —
// weekends skipped, ErrUnavailable propagates up to maxLookbackDays —
// but every probe filters down to the single requested stock and
// surfaces ErrUnavailable when the stock id is not present in the
// response (e.g. delisted, OTC-only, or warrant). Future-dated asOf
// is clamped to today.
func (p *HTTPProvider) GetForStock(ctx context.Context, stockID string, asOf time.Time) (StockData, error) {
	if p.t86 == "" {
		return StockData{}, ErrUnavailable
	}
	asOf = clampAsOfTW(asOf)

	for daysBack := 0; daysBack < maxLookbackDays; daysBack++ {
		probe := asOf.AddDate(0, 0, -daysBack)
		switch probe.Weekday() {
		case time.Saturday, time.Sunday:
			continue
		}
		d, err := p.fetchT86(ctx, stockID, probe.Format("20060102"))
		if err == nil {
			return d, nil
		}
		if !errors.Is(err, ErrUnavailable) {
			return StockData{}, err
		}
	}
	return StockData{}, ErrUnavailable
}

// fetchT86 retrieves the T86 daily snapshot, walks the rows for the
// requested stock id, and returns its parsed StockData. T86 has no
// per-stock endpoint — the response carries every listed equity for
// the day, so we filter client-side. ErrUnavailable when stat != "OK"
// or the stock id is not present (handles delisted / pre-listing /
// non-TSE securities the same way).
//
// Column index map (T86 ALLBUT0999 schema, validated 2026):
//
//	 0 證券代號                   1 證券名稱
//	 2 外陸資買進  3 外陸資賣出   4 外陸資買賣超
//	 5 外資自營商買進  6 外資自營商賣出  7 外資自營商買賣超
//	 8 投信買進    9 投信賣出    10 投信買賣超
//	11 自營商買賣超(合計)
//	12 自營商(自行買賣)買進  13 ..賣出  14 ..買賣超
//	15 自營商(避險)買進  16 ..賣出  17 ..買賣超
//	18 三大法人買賣超
//
// Foreign net is sum(col 4, col 7) — TWSE separates 外陸資 from 外資
// 自營商 in T86 but the renderer wants foreign flow holistically.
func (p *HTTPProvider) fetchT86(ctx context.Context, stockID, date string) (StockData, error) {
	url := fmt.Sprintf(p.t86, date)
	body, err := p.fetch(ctx, url)
	if err != nil {
		return StockData{}, err
	}
	var raw t86Response
	if err := json.Unmarshal(body, &raw); err != nil {
		return StockData{}, err
	}
	if raw.Stat != "OK" || len(raw.Data) == 0 {
		return StockData{}, ErrUnavailable
	}
	for _, row := range raw.Data {
		if len(row) < 19 {
			continue
		}
		if strings.TrimSpace(row[0]) != stockID {
			continue
		}
		return StockData{
			StockID:    stockID,
			Name:       strings.TrimSpace(row[1]),
			ForeignNet: parseTWSENumber(row[4]) + parseTWSENumber(row[7]),
			TrustNet:   parseTWSENumber(row[10]),
			DealerNet:  parseTWSENumber(row[11]),
			Net:        parseTWSENumber(row[18]),
			AsOf:       parseTWSEDate(raw.Date),
		}, nil
	}
	return StockData{}, ErrUnavailable
}

// t86Response mirrors the relevant subset of TWSE's T86 JSON. Same
// shape as BFI82U at the top level but the data rows have a different
// column count + meaning.
type t86Response struct {
	Stat string     `json:"stat"`
	Date string     `json:"date"`
	Data [][]string `json:"data"`
}

// stripLimitSubcount drops the parenthesized limit-up/limit-down count
// from a TWSE breadth cell. Input examples: `312(24)` -> `312`,
// `673` -> `673`. Whitespace tolerated.
func stripLimitSubcount(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "("); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func (p *HTTPProvider) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", safehttp.DefaultUserAgent)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	safehttp.BoundBody(resp, safehttp.DefaultBodyCap)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("twse: %s: %s", resp.Status, body)
	}
	return io.ReadAll(resp.Body)
}

// parseTWSENumber strips thousand-separator commas and parses as int64.
// Empty or unparseable values return 0 (silent), which lets a partial
// row degrade gracefully instead of failing the whole fetch.
func parseTWSENumber(s string) int64 {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if s == "" {
		return 0
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// parseTWSEDate accepts "YYYYMMDD" and returns the corresponding date
// at noon Asia/Taipei. Used so AsOf has a sensible local-time anchor
// even though the upstream only ships the date.
func parseTWSEDate(s string) time.Time {
	if len(s) != 8 {
		return time.Time{}
	}
	t, err := time.ParseInLocation("20060102", s, twLoc)
	if err != nil {
		return time.Time{}
	}
	return t.Add(12 * time.Hour)
}

// normalizeBFICategory reduces upstream's verbose category names to
// stable canonical buckets. Upstream emits two dealer rows
// (自營商(自行買賣) + 自營商(避險)) and one foreign row that may or
// may not include the foreign-self-trade subcategory; we sum into
// three buckets matching how /stock displays them.
func normalizeBFICategory(s string) string {
	switch {
	case strings.HasPrefix(s, "自營商"):
		return "自營商"
	case strings.HasPrefix(s, "投信"):
		return "投信"
	case strings.HasPrefix(s, "外資"):
		return "外資"
	case strings.HasPrefix(s, "合計"):
		return "合計"
	}
	return ""
}

// bfi82uResponse mirrors the relevant subset of TWSE's BFI82U JSON.
type bfi82uResponse struct {
	Stat string     `json:"stat"`
	Date string     `json:"date"`
	Data [][]string `json:"data"`
}

// miMargnResponse mirrors TWSE MI_MARGN's tables-array shape (one
// element per requested view; aggregate selectType returns one).
type miMargnResponse struct {
	Stat   string `json:"stat"`
	Date   string `json:"date"`
	Tables []struct {
		Data [][]string `json:"data"`
	} `json:"tables"`
}

// miIndexResponse mirrors TWSE MI_INDEX's tables-array shape. The
// 漲跌證券數合計 table is identified by its title (table order isn't
// stable across TWSE updates, so we match by title rather than index).
// MI_INDEX doesn't carry a top-level `stat` like BFI82U — emptiness is
// signaled by an empty tables array.
type miIndexResponse struct {
	Date   string `json:"date"`
	Tables []struct {
		Title string     `json:"title"`
		Data  [][]string `json:"data"`
	} `json:"tables"`
}

// Cached wraps a Provider with a single-entry TTL cache + singleflight,
// dedupping concurrent fetches. TWSE updates once per trading day, so
// successTTL is generous (4h) and failureTTL short (30 min) to recover
// quickly from a missed publication window.
type Cached struct {
	inner       Provider
	successTTL  time.Duration
	failureTTL  time.Duration
	now         func() time.Time
	mu          sync.Mutex
	cached      MarketData
	cachedAt    time.Time
	cachedErr   error
	cachedHasOK bool
	flight      singleflight.Group

	// Per-stock + TAIFEX cache layers. Populated automatically when
	// `inner` exposes the matching Exact*Provider interface; nil
	// fallthrough drops to the legacy bypass path (raw inner walk-back,
	// no caching). HTTPProvider implements all six.
	perStockT86     *CachedT86
	perStockLending *CachedSecuritiesLending
	perStockMargin  *CachedStockMargin
	taifexFutures   *CachedRetailFutures
	taifexPCR       *CachedOptionsPCR
	taifexVIX       *CachedVIX
}

// NewCached returns a Cached wrapper using the default TTLs (4h / 30m)
// for the market-wide aggregate. Per-stock and TAIFEX walk-back caches
// are attached automatically when `inner` exposes the matching
// Exact*Provider interfaces.
func NewCached(inner Provider) *Cached {
	return NewCachedWithTTL(inner, 4*time.Hour, 30*time.Minute)
}

// NewCachedWithTTL lets tests pick the market-wide TTLs.
func NewCachedWithTTL(inner Provider, successTTL, failureTTL time.Duration) *Cached {
	c := &Cached{
		inner:      inner,
		successTTL: successTTL,
		failureTTL: failureTTL,
		now:        time.Now,
	}
	if e, ok := inner.(PerStockExactProvider); ok {
		c.perStockT86 = NewCachedT86(e, 0)
	}
	if e, ok := inner.(SecuritiesLendingExactProvider); ok {
		c.perStockLending = NewCachedSecuritiesLending(e, 0)
	}
	if e, ok := inner.(StockMarginExactProvider); ok {
		c.perStockMargin = NewCachedStockMargin(e, 0)
	}
	if e, ok := inner.(RetailFuturesExactProvider); ok {
		c.taifexFutures = NewCachedRetailFutures(e, 0)
	}
	if e, ok := inner.(OptionsPCRExactProvider); ok {
		c.taifexPCR = NewCachedOptionsPCR(e, 0)
	}
	if e, ok := inner.(VIXExactProvider); ok {
		c.taifexVIX = NewCachedVIX(e, 0)
	}
	return c
}

// Get returns the cached entry if fresh; otherwise refreshes via the
// inner provider, dedupping concurrent callers via singleflight. On
// upstream failure, returns the previous successful entry (if any) +
// the err so the caller can render with a STALE prefix; if no prior
// success, returns the err alone.
func (c *Cached) Get(ctx context.Context) (MarketData, error) {
	c.mu.Lock()
	cached, cachedAt, cachedErr, cachedHasOK := c.cached, c.cachedAt, c.cachedErr, c.cachedHasOK
	c.mu.Unlock()

	now := c.now()
	if cachedHasOK && now.Sub(cachedAt) < c.successTTL {
		return cached, nil
	}
	if !cachedHasOK && cachedErr != nil && now.Sub(cachedAt) < c.failureTTL {
		return MarketData{}, cachedErr
	}

	val, err, _ := c.flight.Do("twse", func() (any, error) {
		v, e := c.inner.Get(ctx)
		c.mu.Lock()
		c.cachedAt = c.now()
		if e == nil {
			c.cached = v
			c.cachedErr = nil
			c.cachedHasOK = true
		} else {
			c.cachedErr = e
			// Note: c.cachedHasOK preserved verbatim so a prior successful
			// fetch keeps acting as the stale-fallback for callers.
		}
		c.mu.Unlock()
		return v, e
	})

	if err != nil {
		if cachedHasOK {
			return cached, err // stale-with-data
		}
		return MarketData{}, err
	}
	return val.(MarketData), nil
}

// FetchLiveBreadth delegates to the inner provider when it implements
// LiveBreadthProvider, so a Cached-wrapped HTTPProvider in production
// still surfaces the live-breadth pipeline. We don't add an outer TTL
// here — the inner HTTPProvider already has its own 30s success / 60s
// failure cache for live breadth with singleflight stampede control,
// so wrapping again would only add latency without changing semantics.
// Returns ErrUnavailable when the inner provider doesn't support it.
func (c *Cached) FetchLiveBreadth(ctx context.Context) (LiveBreadth, error) {
	lbp, ok := c.inner.(LiveBreadthProvider)
	if !ok {
		return LiveBreadth{}, ErrUnavailable
	}
	return lbp.FetchLiveBreadth(ctx)
}

// GetAt delegates to the inner provider when it implements
// HistoricalProvider. Historical lookups bypass the single-entry cache
// entirely — that cache holds today's market-wide aggregate and is
// indistinguishable per-key, so a date-pinned response would either
// poison the cache for the live path or require a parallel keyed cache
// that adds complexity for a rare path. Re-running upstream is fine.
// Returns ErrUnavailable when the inner provider doesn't support it.
func (c *Cached) GetAt(ctx context.Context, asOf time.Time) (MarketData, error) {
	hp, ok := c.inner.(HistoricalProvider)
	if !ok {
		return MarketData{}, ErrUnavailable
	}
	return hp.GetAt(ctx, asOf)
}

// GetForStock prefers the per-(stockID, date) walk-back cache when
// inner exposes FetchT86Exact; otherwise falls back to inner's raw
// PerStockProvider (no caching). The cached path keys on the resolved
// trading date so repeat asOf values converge on the same entry.
func (c *Cached) GetForStock(ctx context.Context, stockID string, asOf time.Time) (StockData, error) {
	if c.perStockT86 != nil {
		return c.perStockT86.GetForStock(ctx, stockID, asOf)
	}
	psp, ok := c.inner.(PerStockProvider)
	if !ok {
		return StockData{}, ErrUnavailable
	}
	return psp.GetForStock(ctx, stockID, asOf)
}

// LookupName delegates to the inner provider when it implements
// NameProvider. The inner HTTPProvider memoizes results forever in a
// process-local sync.Map, so this passthrough doesn't add a separate
// cache layer. Returns ErrUnavailable when the inner provider doesn't
// support it.
func (c *Cached) LookupName(ctx context.Context, stockID string, isOTC bool) (string, error) {
	np, ok := c.inner.(NameProvider)
	if !ok {
		return "", ErrUnavailable
	}
	return np.LookupName(ctx, stockID, isOTC)
}

// GetOptionsPCR prefers the date-keyed walk-back cache when inner
// exposes FetchPCRExact; otherwise falls back to the raw provider.
func (c *Cached) GetOptionsPCR(ctx context.Context, asOf time.Time) (OptionsPCR, error) {
	if c.taifexPCR != nil {
		return c.taifexPCR.GetOptionsPCR(ctx, asOf)
	}
	op, ok := c.inner.(OptionsPCRProvider)
	if !ok {
		return OptionsPCR{}, ErrUnavailable
	}
	return op.GetOptionsPCR(ctx, asOf)
}

// GetVIX prefers the date-keyed walk-back cache when inner exposes
// FetchVIXExact; otherwise falls back to the raw provider.
func (c *Cached) GetVIX(ctx context.Context, asOf time.Time) (VIX, error) {
	if c.taifexVIX != nil {
		return c.taifexVIX.GetVIX(ctx, asOf)
	}
	vp, ok := c.inner.(VIXProvider)
	if !ok {
		return VIX{}, ErrUnavailable
	}
	return vp.GetVIX(ctx, asOf)
}

// GetSecuritiesLending prefers the per-(stockID, date) walk-back cache
// when inner exposes FetchTWT93UExact; otherwise falls back to the raw
// provider.
func (c *Cached) GetSecuritiesLending(ctx context.Context, stockID string, asOf time.Time) (SecuritiesLending, error) {
	if c.perStockLending != nil {
		return c.perStockLending.GetSecuritiesLending(ctx, stockID, asOf)
	}
	slp, ok := c.inner.(SecuritiesLendingProvider)
	if !ok {
		return SecuritiesLending{}, ErrUnavailable
	}
	return slp.GetSecuritiesLending(ctx, stockID, asOf)
}

// GetStockMargin prefers the per-(stockID, date) walk-back cache when
// inner exposes FetchMIMARGNStockExact; otherwise falls back to the
// raw provider.
func (c *Cached) GetStockMargin(ctx context.Context, stockID string, asOf time.Time) (StockMargin, error) {
	if c.perStockMargin != nil {
		return c.perStockMargin.GetStockMargin(ctx, stockID, asOf)
	}
	smp, ok := c.inner.(StockMarginProvider)
	if !ok {
		return StockMargin{}, ErrUnavailable
	}
	return smp.GetStockMargin(ctx, stockID, asOf)
}

// GetRetailFutures prefers the date-keyed walk-back cache when inner
// exposes FetchTAIFEXFuturesExact; otherwise falls back to the raw
// provider.
func (c *Cached) GetRetailFutures(ctx context.Context, asOf time.Time) (RetailFutures, error) {
	if c.taifexFutures != nil {
		return c.taifexFutures.GetRetailFutures(ctx, asOf)
	}
	rfp, ok := c.inner.(RetailFuturesProvider)
	if !ok {
		return RetailFutures{}, ErrUnavailable
	}
	return rfp.GetRetailFutures(ctx, asOf)
}
