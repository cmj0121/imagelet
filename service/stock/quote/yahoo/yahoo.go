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

	"github.com/cmj0121/imagelet/service/stock/quote"
)

// defaultEndpoint is Yahoo's v8 chart URL template. Override via
// NewWithEndpoint for tests.
const defaultEndpoint = "https://query1.finance.yahoo.com/v8/finance/chart/%s?interval=1d&range=2d"

// Provider satisfies quote.Provider against Yahoo Finance.
type Provider struct {
	endpoint string
	client   *http.Client
}

// New returns a Provider configured against the default Yahoo endpoint
// with a 5-second per-request timeout. Use NewWithEndpoint for tests.
func New() *Provider {
	return NewWithEndpoint(defaultEndpoint, &http.Client{Timeout: 5 * time.Second})
}

// NewWithEndpoint returns a Provider with a custom URL template (containing
// exactly one %s for the symbol) and HTTP client — primarily for tests
// pointed at httptest.Server.
func NewWithEndpoint(endpoint string, client *http.Client) *Provider {
	return &Provider{endpoint: endpoint, client: client}
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
	m := raw.Chart.Result[0].Meta
	if m.RegularMarketPrice == 0 {
		return quote.Quote{}, quote.ErrUnavailable
	}

	return quote.Quote{
		Symbol:     m.Symbol,
		Last:       m.RegularMarketPrice,
		PrevClose:  m.ChartPreviousClose,
		Currency:   m.Currency,
		AsOf:       time.Unix(m.RegularMarketTime, 0),
		IsClosed:   marketClosed(m, time.Now().Unix()),
		DayHigh:    m.RegularMarketDayHigh,
		DayLow:     m.RegularMarketDayLow,
		Week52High: m.FiftyTwoWeekHigh,
		Week52Low:  m.FiftyTwoWeekLow,
	}, nil
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
			Meta chartMeta `json:"meta"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"chart"`
}

type chartMeta struct {
	Symbol               string  `json:"symbol"`
	Currency             string  `json:"currency"`
	RegularMarketPrice   float64 `json:"regularMarketPrice"`
	ChartPreviousClose   float64 `json:"chartPreviousClose"`
	RegularMarketTime    int64   `json:"regularMarketTime"`
	RegularMarketDayHigh float64 `json:"regularMarketDayHigh"`
	RegularMarketDayLow  float64 `json:"regularMarketDayLow"`
	FiftyTwoWeekHigh     float64 `json:"fiftyTwoWeekHigh"`
	FiftyTwoWeekLow      float64 `json:"fiftyTwoWeekLow"`
	CurrentTradingPeriod struct {
		Regular struct {
			Start int64 `json:"start"`
			End   int64 `json:"end"`
		} `json:"regular"`
	} `json:"currentTradingPeriod"`
}
