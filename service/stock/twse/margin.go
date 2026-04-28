package twse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// defaultMIMARGNStockEndpoint returns the daily 信用交易餘額 per-stock
// table — TWSE's MI_MARGN with `selectType=ALL` carries every listed
// equity's 融資 and 融券 daily flow + standing balance. The per-stock
// view is the natural completion of the market-wide creditRows we
// already render for region-index views. Column layout (verified
// 2026):
//
//	0  證券代號             1  證券名稱
//	2  融資買進             3  融資賣出
//	4  融資現金償還          5  融資前日餘額
//	6  融資今日餘額          7  融資限額
//	8  融券賣出             9  融券買進
//	10 融券現券償還          11 融券前日餘額
//	12 融券今日餘額          13 融券限額
//	14 資券互抵             15 註記
//
// GetStockMargin reads only the standing-balance columns (6, 12) —
// the renderer surfaces 融資餘額 and 融券餘額 on a single combined
// row, parallel to the existing 借券賣出 row but for the more
// commonly cited credit pair. %s = YYYYMMDD CE date.
const defaultMIMARGNStockEndpoint = "https://www.twse.com.tw/rwd/zh/marginTrading/MI_MARGN?date=%s&selectType=ALL&response=json"

// StockMargin captures the per-stock 信用交易 standing balances —
// 融資 (buying on margin) and 融券 (formal short selling) today's
// balance in lots-equivalent shares. The pair is universally cited
// together because their ratio carries the read: 融資 alone signals
// long-leverage stacking, the 券資比 (融券/融資) signals short
// pressure relative to leverage.
//
// Empty (zero) when the upstream had no row for the stock id (delisted
// / pre-listing / OTC) or when the lookback exhausted; the renderer
// gates on Has() to omit the row gracefully.
type StockMargin struct {
	StockID       string    // 證券代號
	Name          string    // 證券名稱 (Chinese name)
	MarginBalance int64     // 融資今日餘額 in 張 (trading units; 1 張 = 1,000 shares)
	ShortBalance  int64     // 融券今日餘額 in 張 (trading units)
	AsOf          time.Time // trading date the upstream supplied
}

// Has reports whether the standing balances were successfully fetched.
// Treats both-zero as "not fetched" — most listings carry non-zero
// 融資 even when 融券 is zero, so this is a practical empty signal.
func (s StockMargin) Has() bool { return s.MarginBalance > 0 || s.ShortBalance > 0 }

// StockMarginProvider is an optional extension for fetching the
// per-stock 融資/融券 standing balances. Same lookback semantics as
// GetSecuritiesLending: walks back day-by-day until a published
// trading day yields a row for the stock id. Optional because not
// every Provider implementation hits TWSE; the handler type-asserts
// and skips the 融資/融券 row when unsupported.
type StockMarginProvider interface {
	GetStockMargin(ctx context.Context, stockID string, asOf time.Time) (StockMargin, error)
}

// SetMIMARGNStockEndpoint overrides the upstream URL used by
// GetStockMargin, so tests can route to a fixture httptest.Server.
// Production callers should rely on New() which wires
// defaultMIMARGNStockEndpoint automatically. Empty disables
// GetStockMargin (returns ErrUnavailable).
func (p *HTTPProvider) SetMIMARGNStockEndpoint(endpoint string) {
	p.miMargnStock = endpoint
}

// GetStockMargin walks back from asOf for the most recent MI_MARGN
// publication containing a row for stockID. Same lookback semantics
// as GetSecuritiesLending — weekends skipped, ErrUnavailable
// propagates after maxLookbackDays — but every probe filters down
// to a single row and surfaces ErrUnavailable when the stock id is
// not present (delisted, OTC, etc).
func (p *HTTPProvider) GetStockMargin(ctx context.Context, stockID string, asOf time.Time) (StockMargin, error) {
	if p.miMargnStock == "" {
		return StockMargin{}, ErrUnavailable
	}
	now := time.Now().In(twLoc)
	if asOf.IsZero() || asOf.After(now) {
		asOf = now
	}
	asOf = asOf.In(twLoc)

	for daysBack := 0; daysBack < maxLookbackDays; daysBack++ {
		probe := asOf.AddDate(0, 0, -daysBack)
		switch probe.Weekday() {
		case time.Saturday, time.Sunday:
			continue
		}
		d, err := p.fetchMIMARGNStock(ctx, stockID, probe.Format("20060102"))
		if err == nil {
			return d, nil
		}
		if !errors.Is(err, ErrUnavailable) {
			return StockMargin{}, err
		}
	}
	return StockMargin{}, ErrUnavailable
}

// fetchMIMARGNStock retrieves the daily MI_MARGN snapshot, finds the
// row for stockID, and returns the parsed StockMargin. Unlike T86 /
// TWT93U the MI_MARGN response wraps multiple tables under a top-level
// `tables` array — the first carries market-wide 融資/融券 totals,
// later tables carry the per-stock 融資融券彙總 grid. The per-stock
// grid is the one with 16-column rows starting with a stock id; we
// pick it by row width rather than by title so the parser stays
// resilient to TWSE renaming sections. ErrUnavailable when stat !=
// "OK" or stock id is not present.
func (p *HTTPProvider) fetchMIMARGNStock(ctx context.Context, stockID, date string) (StockMargin, error) {
	url := fmt.Sprintf(p.miMargnStock, date)
	body, err := p.fetch(ctx, url)
	if err != nil {
		return StockMargin{}, err
	}
	var raw miMargnStockResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return StockMargin{}, err
	}
	if raw.Stat != "OK" {
		return StockMargin{}, ErrUnavailable
	}
	for _, tbl := range raw.Tables {
		for _, row := range tbl.Data {
			if len(row) < 13 {
				continue
			}
			if strings.TrimSpace(row[0]) != stockID {
				continue
			}
			return StockMargin{
				StockID:       stockID,
				Name:          strings.TrimSpace(row[1]),
				MarginBalance: parseTWSENumber(row[6]),
				ShortBalance:  parseTWSENumber(row[12]),
				AsOf:          parseTWSEDate(raw.Date),
			}, nil
		}
	}
	return StockMargin{}, ErrUnavailable
}

// miMargnStockResponse mirrors the relevant subset of TWSE's MI_MARGN
// JSON. The endpoint nests its payload under a `tables` array — the
// first entry is the 信用交易統計 market summary (5-column rows),
// the second is the 融資融券彙總 per-stock grid (16-column rows
// keyed by stock id). The parser walks every table and matches by
// stock id in column 0; off-shape tables are skipped via the
// column-count guard.
type miMargnStockResponse struct {
	Stat   string            `json:"stat"`
	Date   string            `json:"date"`
	Tables []miMargnStockTbl `json:"tables"`
}

type miMargnStockTbl struct {
	Title  string     `json:"title"`
	Fields []string   `json:"fields"`
	Data   [][]string `json:"data"`
}
