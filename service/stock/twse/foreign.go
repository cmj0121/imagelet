package twse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// defaultForeignEndpoint is the legacy TWSE rwd 外資及陸資投資持股
// 統計 daily file — the FULL universe (1,300+ stocks per dump),
// date-pinned via the URL's `date=` query param. JSON shape:
//
//	{"stat":"OK","date":"YYYYMMDD","title":"...","fields":[...],
//	 "data":[[...row...], ...]}
//
// Verified 2026-05-07 against 2026-05-06: 1,337 rows; 2330 carries
// 70.65% foreign holding ratio, 0050 carries 3.56%, 6488 (OTC) is
// absent — confirming the file is TWSE-only and OTC stocks fall
// through to ErrUnavailable.
//
// **Why not the OpenAPI variant**: openapi.twse.com.tw/v1/exchangeReport/MI_QFIIS_sort_20
// exists but caps at TOP-20 by foreign-holding ratio (4 KB response).
// Most user-queried stocks (banks, ETFs, telecoms) silently fall
// through. The legacy rwd endpoint is full-universe at the cost of
// date-pinning + walkback.
//
// **Schema quirk**: data row cells are mixed-type — share counts are
// comma-formatted strings (`"25,932,524,521"`) but ratio percentages
// are unquoted JSON floats (`70.65`). Parsed via `[][]json.RawMessage`
// + per-cell decode rather than `[][]string`.
//
// %s = YYYYMMDD CE date.
const defaultForeignEndpoint = "https://www.twse.com.tw/rwd/zh/fund/MI_QFIIS?date=%s&selectType=ALLBUT0999&response=json"

// Foreign holdings field-index constants for the rwd MI_QFIIS data
// row layout (verified 2026-05-06):
//
//	0  證券代號         1  證券名稱
//	2  國際證券編碼      3  發行股數
//	4  外資及陸資尚可投資股數
//	5  全體外資及陸資持有股數
//	6  外資尚可投資比率%   (JSON float)
//	7  全體外資及陸資持股比率%  (JSON float — the headline)
//	8  法定上限比率%
//	9  流通在外比率%
//	10 (empty)
//	11 上次調整日期 (ROC date string)
const (
	foreignColCode             = 0
	foreignColName             = 1
	foreignColIssuedShares     = 3
	foreignColAvailableShares  = 4
	foreignColForeignHeld      = 5
	foreignColAvailablePct     = 6
	foreignColHoldingPct       = 7
	foreignColUpperlimitPct    = 8
	foreignMinCols             = 8 // we read up through index 7
)

// Foreign captures per-stock 外資及陸資投資持股 — total foreign + PRC
// investor ownership ratio. The "headline" number rendered on the
// stock card is HoldingPct (the 全體外資及陸資持股比率%).
//
// Empty (zero StockID) when the upstream had no row for the stock id
// (delisted / OTC). The renderer gates on Has() to omit gracefully.
type Foreign struct {
	StockID        string    // 證券代號
	Name           string    // 證券名稱 — Chinese short name from upstream
	HoldingPct     float64   // 全體外資及陸資持股比率% — the headline number
	AvailablePct   float64   // 外資尚可投資比率% — remaining-capacity proxy
	UpperlimitPct  float64   // 法定上限比率% — usually 100% on TW listings
	IssuedShares   int64     // 發行股數
	ForeignHeld    int64     // 全體外資及陸資持有股數
	AsOf           time.Time // resolved trading date the upstream supplied
}

// Has reports whether the upstream produced a non-empty row.
// StockID == "" means "not found" — every entry has non-empty 證券代號.
func (f Foreign) Has() bool { return f.StockID != "" }

// ForeignProvider is implemented by anything that can answer per-stock
// foreign-holdings queries. asOf walks back day-by-day until a
// published trading day yields a row for the stockID.
type ForeignProvider interface {
	GetForeign(ctx context.Context, stockID string, asOf time.Time) (Foreign, error)
}

// ForeignExactProvider is the cache-friendly twin: fetches one specific
// date with no walkback so the caching wrapper can do its own walkback
// across cached entries. Same shape as the existing per-stock daily
// providers (T86, TWT93U, MI_MARGN per-stock).
type ForeignExactProvider interface {
	FetchForeignExact(ctx context.Context, stockID, date string) (Foreign, bool, error)
}

// SetForeignEndpoint overrides the upstream URL template. Production
// callers rely on New() which wires defaultForeignEndpoint
// automatically. Empty disables the provider (returns ErrUnavailable).
func (p *HTTPProvider) SetForeignEndpoint(endpoint string) {
	p.foreign = endpoint
}

// FetchForeignExact pulls the rwd MI_QFIIS snapshot for one date and
// returns the parsed row for stockID. found=false means the upstream
// answered with stat=OK but no row matched the stockID (delisted /
// OTC / non-existent). err is reserved for transport / parse failures.
func (p *HTTPProvider) FetchForeignExact(ctx context.Context, stockID, date string) (Foreign, bool, error) {
	if p.foreign == "" {
		return Foreign{}, false, ErrUnavailable
	}
	url := fmt.Sprintf(p.foreign, date)
	body, err := p.fetch(ctx, url)
	if err != nil {
		return Foreign{}, false, err
	}

	var raw foreignResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return Foreign{}, false, fmt.Errorf("twse: foreign unmarshal: %w", err)
	}
	if raw.Stat != "OK" {
		return Foreign{}, false, nil
	}

	stockID = strings.TrimSpace(stockID)
	for _, row := range raw.Data {
		if len(row) <= foreignMinCols {
			continue
		}
		code := decodeForeignString(row[foreignColCode])
		if strings.TrimSpace(code) != stockID {
			continue
		}
		return Foreign{
			StockID:       stockID,
			Name:          strings.TrimSpace(decodeForeignString(row[foreignColName])),
			HoldingPct:    decodeForeignFloat(row[foreignColHoldingPct]),
			AvailablePct:  decodeForeignFloat(row[foreignColAvailablePct]),
			UpperlimitPct: decodeForeignFloat(row[foreignColUpperlimitPct]),
			IssuedShares:  parseTWSENumber(decodeForeignString(row[foreignColIssuedShares])),
			ForeignHeld:   parseTWSENumber(decodeForeignString(row[foreignColForeignHeld])),
			AsOf:          parseTWSEDate(raw.Date),
		}, true, nil
	}
	return Foreign{}, false, nil
}

// GetForeign walks back from asOf for the most recent published date
// containing a row for stockID. Same lookback semantics as
// GetSecuritiesLending — weekends skipped, ErrUnavailable propagates
// after maxLookbackDays.
func (p *HTTPProvider) GetForeign(ctx context.Context, stockID string, asOf time.Time) (Foreign, error) {
	var out Foreign
	err := walkBackTradingDays(asOf, time.Now(), func(probe time.Time) (bool, error) {
		date := probe.Format("20060102")
		f, found, err := p.FetchForeignExact(ctx, stockID, date)
		if err != nil {
			if errors.Is(err, ErrUnavailable) {
				return false, nil
			}
			return false, err
		}
		if found {
			out = f
			return true, nil
		}
		return false, nil
	})
	return out, err
}

// foreignResponse mirrors the rwd MI_QFIIS shape. Data is
// `[][]json.RawMessage` because cells are mixed-type (strings for
// share counts, floats for percentages); we decode per-cell rather
// than declaring a single type for the whole row.
type foreignResponse struct {
	Stat string              `json:"stat"`
	Date string              `json:"date"`
	Data [][]json.RawMessage `json:"data"`
}

// decodeForeignString returns the JSON-encoded cell as a Go string.
// Handles the case where a cell happens to be a JSON number (rare in
// the string columns but defends against upstream schema drift) by
// falling back to the raw bytes.
func decodeForeignString(cell json.RawMessage) string {
	var s string
	if err := json.Unmarshal(cell, &s); err == nil {
		return s
	}
	return strings.Trim(string(cell), `"`)
}

// decodeForeignFloat returns the JSON-encoded cell as a float64.
// Accepts both unquoted JSON numbers (the percent columns) and
// quoted string numerics (defensive for upstream changes).
func decodeForeignFloat(cell json.RawMessage) float64 {
	var f float64
	if err := json.Unmarshal(cell, &f); err == nil {
		return f
	}
	var s string
	if err := json.Unmarshal(cell, &s); err == nil {
		return parseTWSEFloat(s)
	}
	return 0
}
