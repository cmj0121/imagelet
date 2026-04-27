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

	"golang.org/x/sync/singleflight"
)

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
	//   - MarginShortLots: 融券(交易單位) 今日餘額 in 張 (1 張 = 1000 shares).
	MarginLongTWD   int64
	MarginShortLots int64
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
	return d.MarginLongTWD != 0 || d.MarginShortLots != 0
}

// Provider abstracts the TWSE fetch so tests can swap in a fake.
// Get takes no symbol -- TWSE data is market-wide.
type Provider interface {
	Get(ctx context.Context) (MarketData, error)
}

// ErrUnavailable signals "upstream answered but data is empty / no
// trading session" -- distinct from transport errors. Cache layer can
// extend backoff for this signal.
var ErrUnavailable = errors.New("twse: market data unavailable")

// HTTPProvider hits the legacy TWSE endpoints. UA is spoofed because Go's
// default User-Agent occasionally hits CDN gates.
type HTTPProvider struct {
	bfi82u  string
	miMargn string
	client  *http.Client
}

// New returns an HTTPProvider with a 5s per-request timeout against the
// production endpoints.
func New() *HTTPProvider {
	return NewWithEndpoints(defaultBFI82UEndpoint, defaultMIMARGNEndpoint, &http.Client{Timeout: 5 * time.Second})
}

// NewWithEndpoints lets tests point the provider at httptest.Server URLs.
// Each template MUST contain exactly one %s for the YYYYMMDD CE date.
func NewWithEndpoints(bfi82u, miMargn string, client *http.Client) *HTTPProvider {
	return &HTTPProvider{bfi82u: bfi82u, miMargn: miMargn, client: client}
}

// Get fetches both daily aggregates concurrently and merges them. If
// either upstream returns no data (empty stat / weekend / holiday), the
// merged result returns ErrUnavailable. Partial successes (one endpoint
// up, the other down with a transport error) propagate the transport
// error -- the caller's cache decides whether to serve stale.
func (p *HTTPProvider) Get(ctx context.Context) (MarketData, error) {
	// TWSE publishes for the most recent completed trading day. We probe
	// today's date in Taipei first; if upstream signals no-session
	// (typical on weekends/holidays), the caller reads `ErrUnavailable`
	// and the cache layer keeps yesterday's data fresh.
	tw, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		tw = time.FixedZone("Asia/Taipei", 8*3600)
	}
	date := time.Now().In(tw).Format("20060102")

	type fetchResult struct {
		data MarketData
		err  error
	}
	bfiCh := make(chan fetchResult, 1)
	margnCh := make(chan fetchResult, 1)
	go func() {
		d, err := p.fetchBFI82U(ctx, date)
		bfiCh <- fetchResult{data: d, err: err}
	}()
	go func() {
		d, err := p.fetchMIMARGN(ctx, date)
		margnCh <- fetchResult{data: d, err: err}
	}()
	bfi := <-bfiCh
	margn := <-margnCh

	if bfi.err != nil && !errors.Is(bfi.err, ErrUnavailable) {
		return MarketData{}, bfi.err
	}
	if margn.err != nil && !errors.Is(margn.err, ErrUnavailable) {
		return MarketData{}, margn.err
	}

	merged := MarketData{
		AsOf:            bfi.data.AsOf,
		ForeignNet:      bfi.data.ForeignNet,
		TrustNet:        bfi.data.TrustNet,
		DealerNet:       bfi.data.DealerNet,
		Net:             bfi.data.Net,
		MarginLongTWD:   margn.data.MarginLongTWD,
		MarginShortLots: margn.data.MarginShortLots,
	}
	if merged.AsOf.IsZero() {
		merged.AsOf = margn.data.AsOf
	}
	if !merged.HasInstitutional() && !merged.HasMargin() {
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
		case strings.HasPrefix(row[0], "融券(交易單位)"):
			out.MarginShortLots = balance
		}
	}
	return out, nil
}

func (p *HTTPProvider) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; imagelet/0.2)")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
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
	tw, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		tw = time.FixedZone("Asia/Taipei", 8*3600)
	}
	t, err := time.ParseInLocation("20060102", s, tw)
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
}

// NewCached returns a Cached wrapper using the default TTLs (4h / 30m).
func NewCached(inner Provider) *Cached {
	return NewCachedWithTTL(inner, 4*time.Hour, 30*time.Minute)
}

// NewCachedWithTTL lets tests pick TTLs.
func NewCachedWithTTL(inner Provider, successTTL, failureTTL time.Duration) *Cached {
	return &Cached{
		inner:      inner,
		successTTL: successTTL,
		failureTTL: failureTTL,
		now:        time.Now,
	}
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
