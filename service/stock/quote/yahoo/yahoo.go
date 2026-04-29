// Package yahoo implements quote.Provider against Yahoo Finance's v8 chart
// endpoint. The endpoint is unofficial — Yahoo may rate-limit or change
// the response shape. The provider keeps its parsing minimal and surfaces
// structural mismatches as quote.ErrUnavailable so the cache layer can
// back off without serving partial data.
package yahoo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cmj0121/imagelet/internal/safehttp"
	"github.com/cmj0121/imagelet/service/stock/quote"
)

// defaultEndpoint is Yahoo's v8 chart URL template. Override via
// NewWithEndpoint for tests. The 3mo range gives us enough closed
// sessions to compute MA5 / MA10 robustly even across a holiday-cluster
// month (e.g. Lunar New Year + 228) without bloating the response.
const defaultEndpoint = "https://query1.finance.yahoo.com/v8/finance/chart/%s?interval=1d&range=3mo"

// defaultEndpointAt is the v8 chart URL template for a historical window
// query: %s = symbol, %d %d = period1 / period2 Unix seconds. Used by
// GetAt — 90-day window (asOf-90d → asOf+1d) covers enough trading
// sessions to compute MA5 / MA10 even after a holiday-cluster month
// while keeping the response small.
const defaultEndpointAt = "https://query1.finance.yahoo.com/v8/finance/chart/%s?interval=1d&period1=%d&period2=%d"

// historicalLookbackDays is the days-before-asOf window passed as
// period1. 90 calendar days covers ~60 trading sessions — comfortably
// enough to compute MA5 / MA10 from closed-session closes even after
// the longest holiday cluster (Lunar New Year ~5 days).
const historicalLookbackDays = 90

// Provider satisfies quote.Provider against Yahoo Finance.
type Provider struct {
	endpoint   string
	endpointAt string
	client     *http.Client
}

// New returns a Provider configured against the default Yahoo endpoint
// with a 5-second per-request timeout. Use NewWithEndpoint for tests.
func New() *Provider {
	return &Provider{
		endpoint:   defaultEndpoint,
		endpointAt: defaultEndpointAt,
		client:     safehttp.NewClient(5 * time.Second),
	}
}

// NewWithEndpoint returns a Provider with a custom URL template (containing
// exactly one %s for the symbol) and HTTP client — primarily for tests
// pointed at httptest.Server. The historical-window endpoint is set to
// a derivative of `endpoint` so a single fixture server can serve both
// the live and as-of paths; tests that exercise historical paths can
// override via NewWithEndpoints.
func NewWithEndpoint(endpoint string, client *http.Client) *Provider {
	return &Provider{endpoint: endpoint, endpointAt: endpoint, client: client}
}

// NewWithEndpoints lets tests point the live and historical paths at
// different URL templates — e.g. when a fixture server distinguishes
// between range= and period1/period2 queries by routing on the path.
// `endpoint` is the live (range=2d) template; `endpointAt` is the
// historical template containing one %s + two %d slots.
func NewWithEndpoints(endpoint, endpointAt string, client *http.Client) *Provider {
	return &Provider{endpoint: endpoint, endpointAt: endpointAt, client: client}
}

// Get fetches the quote for symbol. Network errors are returned as-is.
// Structural problems (missing meta, no result, no price) collapse to
// quote.ErrUnavailable.
func (p *Provider) Get(ctx context.Context, symbol string) (quote.Quote, error) {
	url := fmt.Sprintf(p.endpoint, symbol)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return quote.Quote{}, err
	}
	// Yahoo returns 401 for some default user-agent strings — Go's default
	// User-Agent is not reliable here; spoof a browser-shaped UA.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; imagelet/0.2)")

	resp, err := p.client.Do(req)
	if err != nil {
		return quote.Quote{}, err
	}
	defer resp.Body.Close()
	safehttp.BoundBody(resp, safehttp.YahooBodyCap)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return quote.Quote{}, fmt.Errorf("yahoo: %s: %s", resp.Status, body)
	}

	var raw chartResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return quote.Quote{}, err
	}
	if raw.Chart.Error != nil && raw.Chart.Error.Description != "" {
		return quote.Quote{}, fmt.Errorf("yahoo: %s", raw.Chart.Error.Description)
	}
	if len(raw.Chart.Result) == 0 {
		return quote.Quote{}, quote.ErrUnavailable
	}
	result := raw.Chart.Result[0]
	m := result.Meta
	if m.RegularMarketPrice == 0 {
		return quote.Quote{}, quote.ErrUnavailable
	}

	q := quote.Quote{
		Symbol:     m.Symbol,
		Name:       m.ShortName,
		LongName:   m.LongName,
		Last:       m.RegularMarketPrice,
		Open:       latestOpen(result.Indicators),
		Currency:   m.Currency,
		AsOf:       time.Unix(m.RegularMarketTime, 0),
		IsClosed:   marketClosed(m, time.Now().Unix()),
		DayHigh:    m.RegularMarketDayHigh,
		DayLow:     m.RegularMarketDayLow,
		Week52High: m.FiftyTwoWeekHigh,
		Week52Low:  m.FiftyTwoWeekLow,
		Volume:     m.RegularMarketVolume,
	}

	// Derive PrevClose from the closes array rather than meta.chartPrevious-
	// Close. The meta field carries the close of the bar immediately before
	// the requested range — which used to be "yesterday" when range=2d, but
	// after we widened the request to range=3mo it now points 3 months back.
	// The closes array already gives us a precise, range-independent answer:
	// the latest bar at len-1 is today (final close when market is closed,
	// intraday partial when open), so walk back from len-2 — yesterday's
	// closed-session value — and skip past any zero-padding Yahoo writes
	// for non-trading calendar days inside the window.
	var closes []float64
	if len(result.Indicators.Quote) > 0 {
		closes = result.Indicators.Quote[0].Close
	}
	for i := len(closes) - 2; i >= 0; i-- {
		if closes[i] > 0 {
			q.PrevClose = closes[i]
			break
		}
	}
	if q.PrevClose == 0 {
		q.PrevClose = m.ChartPreviousClose // fallback when the array is empty/sparse
	}

	// Compute MA5 / MA10 from the closed-session closes. When the market
	// is open the latest bar is today's intraday partial close — drop it
	// so the price-vs-MA arrow doesn't fold today's price into its own
	// reference. When closed, the latest bar is yesterday's close and is
	// safe to include.
	maCloses := closes
	if !q.IsClosed && len(maCloses) > 0 {
		maCloses = maCloses[:len(maCloses)-1]
	}
	q.MA5 = quote.MeanOfLastN(maCloses, 5)
	q.MA10 = quote.MeanOfLastN(maCloses, 10)
	return q, nil
}

// GetAt fetches the latest bar at or before asOf using a narrow
// period1/period2 window over the v8 chart endpoint. Future-dated asOf
// is clamped to "now" defensively so callers can't loop the upstream
// against a never-existing date. Returns quote.ErrUnavailable when the
// fixed lookback window contains no bars (delisted symbol, or symbol
// listed after asOf).
//
// The reconstructed Quote sets IsClosed=true unconditionally — a
// historical bar is by definition closed regardless of what
// currentTradingPeriod.regular reports for "today". 52-week range fields
// are left zero (window too narrow to compute); the renderer's
// Has52WeekRange guard then suppresses the 52w bar gracefully.
func (p *Provider) GetAt(ctx context.Context, symbol string, asOf time.Time) (quote.Quote, error) {
	if asOf.After(time.Now()) {
		asOf = time.Now()
	}
	period1 := asOf.AddDate(0, 0, -historicalLookbackDays).Unix()
	period2 := asOf.AddDate(0, 0, 1).Unix()

	url := fmt.Sprintf(p.endpointAt, symbol, period1, period2)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return quote.Quote{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; imagelet/0.2)")

	resp, err := p.client.Do(req)
	if err != nil {
		return quote.Quote{}, err
	}
	defer resp.Body.Close()
	safehttp.BoundBody(resp, safehttp.YahooBodyCap)

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return quote.Quote{}, fmt.Errorf("yahoo: %s: %s", resp.Status, body)
	}

	var raw chartResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return quote.Quote{}, err
	}
	if raw.Chart.Error != nil && raw.Chart.Error.Description != "" {
		return quote.Quote{}, fmt.Errorf("yahoo: %s", raw.Chart.Error.Description)
	}
	if len(raw.Chart.Result) == 0 {
		return quote.Quote{}, quote.ErrUnavailable
	}

	result := raw.Chart.Result[0]
	if len(result.Timestamp) == 0 || len(result.Indicators.Quote) == 0 {
		return quote.Quote{}, quote.ErrUnavailable
	}
	ind := result.Indicators.Quote[0]

	// Bars come ordered ascending by timestamp; walk backwards looking for
	// the latest bar with t ≤ end-of-asOf-day AND a non-null close. The
	// cutoff is the end of asOf's CALENDAR day in its own location — not
	// `asOf + 24h` — because exchanges stamp daily bars at session-open
	// (e.g. ~14:30 UTC for US markets), so a noon-UTC asOf with a +24h
	// cutoff would pull in the next day's bar. Yahoo occasionally returns
	// null OHLC for a slot during pre-market or data gaps (decoded to 0)
	// — skipping those keeps GetAt honest.
	y, mo, d := asOf.Date()
	asOfCutoff := time.Date(y, mo, d, 23, 59, 59, 0, asOf.Location()).Unix()
	pickIdx := -1
	for i := len(result.Timestamp) - 1; i >= 0; i-- {
		if result.Timestamp[i] > asOfCutoff {
			continue
		}
		if i >= len(ind.Close) || ind.Close[i] <= 0 {
			continue
		}
		pickIdx = i
		break
	}
	if pickIdx < 0 {
		return quote.Quote{}, quote.ErrUnavailable
	}

	prevClose := 0.0
	for i := pickIdx - 1; i >= 0; i-- {
		if i < len(ind.Close) && ind.Close[i] > 0 {
			prevClose = ind.Close[i]
			break
		}
	}

	q := quote.Quote{
		Symbol:    result.Meta.Symbol,
		Name:      result.Meta.ShortName,
		LongName:  result.Meta.LongName,
		Last:      ind.Close[pickIdx],
		Open:      safeAt(ind.Open, pickIdx),
		PrevClose: prevClose,
		Currency:  result.Meta.Currency,
		AsOf:      time.Unix(result.Timestamp[pickIdx], 0),
		IsClosed:  true,
		DayHigh:   safeAt(ind.High, pickIdx),
		DayLow:    safeAt(ind.Low, pickIdx),
		Volume:    safeAtInt(ind.Volume, pickIdx),
	}
	if q.Symbol == "" {
		q.Symbol = symbol
	}

	// Compute MA5 / MA10 from closes up to and including pickIdx — the
	// chosen bar IS the "current" price for the historical view and is
	// closed by definition, so it counts toward the MA. Bars after
	// pickIdx (if any — Yahoo can return a few entries past asOf when
	// the cutoff straddles a session) must be excluded so the historical
	// MA only reflects data the caller would have seen at asOf.
	closes := ind.Close[:pickIdx+1]
	q.MA5 = quote.MeanOfLastN(closes, 5)
	q.MA10 = quote.MeanOfLastN(closes, 10)
	return q, nil
}

// safeAtInt is the int64 sibling of safeAt — Yahoo encodes missing
// volume slots as null which decode to 0 in our slice; the bound
// check + non-negative gate keeps a zero out of the rendered output
// so the caption row gracefully omits the volume token rather than
// reading "0 shares".
func safeAtInt(arr []int64, i int) int64 {
	if i < 0 || i >= len(arr) {
		return 0
	}
	if arr[i] < 0 {
		return 0
	}
	return arr[i]
}

// safeAt returns arr[i] when in-range and positive, else 0. Yahoo
// occasionally returns null OHLC slots during data gaps (decoded as 0
// for float64); the zero acts as a sentinel for the renderer's HasOHLC
// gate so a partial bar gracefully drops the OHLC bar rather than
// rendering noise.
func safeAt(arr []float64, i int) float64 {
	if i < 0 || i >= len(arr) {
		return 0
	}
	if arr[i] < 0 {
		return 0
	}
	return arr[i]
}

// latestOpen returns the most recent non-zero session open from the
// chart's OHLCV history. Yahoo's v8 chart-meta omits regularMarketOpen
// (only the day's High / Low / Last are surfaced there); the session
// open lives in indicators.quote[0].open[], indexed by timestamp[]. The
// last entry corresponds to the current session — but Yahoo can return
// `null` for that slot during rare upstream gaps (which encoding/json
// decodes to 0 for float64), so we walk back from the tail and take
// the first non-zero value. Returns 0 when no value is usable; the
// renderer's HasOHLC() then gracefully omits the bar.
func latestOpen(ind chartIndicators) float64 {
	if len(ind.Quote) == 0 {
		return 0
	}
	opens := ind.Quote[0].Open
	for i := len(opens) - 1; i >= 0; i-- {
		if opens[i] > 0 {
			return opens[i]
		}
	}
	return 0
}

// marketClosed returns true when the request time is outside the
// currentTradingPeriod.regular window. Yahoo advances the regular window
// day-by-day, so on weekends/holidays the .end field is in the past and
// this returns true.
func marketClosed(m chartMeta, nowUnix int64) bool {
	return nowUnix < m.CurrentTradingPeriod.Regular.Start ||
		nowUnix >= m.CurrentTradingPeriod.Regular.End
}

// chartResponse mirrors the relevant subset of Yahoo's v8 chart JSON shape.
// We intentionally don't unmarshal everything — fewer fields = less to
// break when Yahoo wobbles.
type chartResponse struct {
	Chart struct {
		Result []struct {
			Meta       chartMeta       `json:"meta"`
			Indicators chartIndicators `json:"indicators"`
			Timestamp  []int64         `json:"timestamp"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"chart"`
}

// chartIndicators holds the OHLCV history arrays. Yahoo nests them
// under indicators.quote[0] — the [0] is historical (one entry per
// provider; chart endpoints only ever fill one), the inner arrays are
// per-bar OHLCV indexed by the sibling timestamp[] array on Result.
// The live Get path uses only open[] (chart-meta surfaces Last / DayHigh
// / DayLow), but the historical GetAt path needs the full quartet plus
// volume to reconstruct a bar, so all five fields are decoded.
type chartIndicators struct {
	Quote []struct {
		Open   []float64 `json:"open"`
		High   []float64 `json:"high"`
		Low    []float64 `json:"low"`
		Close  []float64 `json:"close"`
		Volume []int64   `json:"volume"`
	} `json:"quote"`
}

type chartMeta struct {
	Symbol               string  `json:"symbol"`
	ShortName            string  `json:"shortName"`
	LongName             string  `json:"longName"`
	Currency             string  `json:"currency"`
	RegularMarketPrice   float64 `json:"regularMarketPrice"`
	ChartPreviousClose   float64 `json:"chartPreviousClose"`
	RegularMarketTime    int64   `json:"regularMarketTime"`
	RegularMarketDayHigh float64 `json:"regularMarketDayHigh"`
	RegularMarketDayLow  float64 `json:"regularMarketDayLow"`
	RegularMarketVolume  int64   `json:"regularMarketVolume"`
	FiftyTwoWeekHigh     float64 `json:"fiftyTwoWeekHigh"`
	FiftyTwoWeekLow      float64 `json:"fiftyTwoWeekLow"`
	CurrentTradingPeriod struct {
		Regular struct {
			Start int64 `json:"start"`
			End   int64 `json:"end"`
		} `json:"regular"`
	} `json:"currentTradingPeriod"`
}
