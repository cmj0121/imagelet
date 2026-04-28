package twse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// defaultTWT93UEndpoint returns the daily 信用額度總量管制餘額表 — a
// composite report containing both 融券 (margin short) and 借券賣出
// (securities lending sold) balances per stock. Column layout (verified
// 2026):
//
//	0  證券代號         1  證券名稱
//	2  融券前日餘額       3  融券當日賣出
//	4  融券當日買進       5  融券當日現券
//	6  融券當日餘額       7  融券次一營業日限額
//	8  借券前日餘額       9  借券當日賣出
//	10 借券當日還券       11 借券當日調整
//	12 借券當日餘額       13 借券次一營業日可限額
//	14 備註
//
// Only column 12 (借券當日餘額) is read by GetSecuritiesLending — the
// renderer surfaces the standing short pressure on a single row, not
// the per-day flows. %s = YYYYMMDD CE date.
const defaultTWT93UEndpoint = "https://www.twse.com.tw/rwd/zh/marginTrading/TWT93U?date=%s&selectType=ALL&response=json"

// SecuritiesLending captures the per-stock 借券賣出當日餘額 — the
// standing balance of shares sold short via borrowed securities. A
// rough proxy for explicit short-side pressure on the listing.
//
// Empty (zero) when the upstream had no row for the stock id (delisted
// / pre-listing / OTC) or when the lookback exhausted; the renderer
// gates on Has() to omit the row gracefully.
type SecuritiesLending struct {
	StockID string    // 證券代號
	Name    string    // 證券名稱 (Chinese name)
	Balance int64     // 借券賣出當日餘額 in shares
	AsOf    time.Time // trading date the upstream supplied
}

// Has reports whether the lending balance was successfully fetched.
// Treats zero balance as "not fetched" — practically the most-traded
// listings always carry non-zero standing balance, and a true zero is
// not actionable enough to warrant a row.
func (s SecuritiesLending) Has() bool { return s.Balance > 0 }

// SecuritiesLendingProvider is an optional extension for fetching the
// 借券賣出當日餘額 per stock. Same lookback semantics as the existing
// per-stock providers (T86): walks back day-by-day until a published
// trading day yields a row for the stock id. Optional because not
// every Provider implementation hits TWSE; the handler type-asserts
// and skips the 借券賣出 row when unsupported.
type SecuritiesLendingProvider interface {
	GetSecuritiesLending(ctx context.Context, stockID string, asOf time.Time) (SecuritiesLending, error)
}

// SetTWT93UEndpoint overrides the upstream URL used by
// GetSecuritiesLending, so tests can route to a fixture httptest.Server.
// Production callers should rely on New() which wires
// defaultTWT93UEndpoint automatically. Empty disables
// GetSecuritiesLending (returns ErrUnavailable).
func (p *HTTPProvider) SetTWT93UEndpoint(endpoint string) {
	p.twt93u = endpoint
}

// GetSecuritiesLending walks back from asOf for the most recent
// TWT93U publication containing a row for stockID. Same lookback
// semantics as GetForStock — weekends skipped, ErrUnavailable
// propagates after maxLookbackDays — but every probe filters down
// to a single row and surfaces ErrUnavailable when the stock id is
// not present (delisted, OTC, etc).
func (p *HTTPProvider) GetSecuritiesLending(ctx context.Context, stockID string, asOf time.Time) (SecuritiesLending, error) {
	if p.twt93u == "" {
		return SecuritiesLending{}, ErrUnavailable
	}
	asOf = clampAsOfTW(asOf)

	for daysBack := 0; daysBack < maxLookbackDays; daysBack++ {
		probe := asOf.AddDate(0, 0, -daysBack)
		switch probe.Weekday() {
		case time.Saturday, time.Sunday:
			continue
		}
		d, err := p.fetchTWT93U(ctx, stockID, probe.Format("20060102"))
		if err == nil {
			return d, nil
		}
		if !errors.Is(err, ErrUnavailable) {
			return SecuritiesLending{}, err
		}
	}
	return SecuritiesLending{}, ErrUnavailable
}

// fetchTWT93U retrieves the daily TWT93U snapshot, finds the row for
// stockID, and returns the parsed SecuritiesLending. The endpoint
// answer carries every listed equity for the day; we filter
// client-side rather than asking for a single row (TWSE's `selectType`
// dropdown doesn't expose per-stock filtering). ErrUnavailable when
// stat != "OK" or stock id is not present.
func (p *HTTPProvider) fetchTWT93U(ctx context.Context, stockID, date string) (SecuritiesLending, error) {
	url := fmt.Sprintf(p.twt93u, date)
	body, err := p.fetch(ctx, url)
	if err != nil {
		return SecuritiesLending{}, err
	}
	var raw twt93uResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return SecuritiesLending{}, err
	}
	if raw.Stat != "OK" || len(raw.Data) == 0 {
		return SecuritiesLending{}, ErrUnavailable
	}
	for _, row := range raw.Data {
		if len(row) < 13 {
			continue
		}
		if strings.TrimSpace(row[0]) != stockID {
			continue
		}
		return SecuritiesLending{
			StockID: stockID,
			Name:    strings.TrimSpace(row[1]),
			Balance: parseTWSENumber(row[12]),
			AsOf:    parseTWSEDate(raw.Date),
		}, nil
	}
	return SecuritiesLending{}, ErrUnavailable
}

// twt93uResponse mirrors the relevant subset of TWSE's TWT93U JSON.
// Same top-level shape as T86 but the rows are wider (15 cols) and
// covered by a `groups` header that splits 融券 from 借券賣出.
type twt93uResponse struct {
	Stat string     `json:"stat"`
	Date string     `json:"date"`
	Data [][]string `json:"data"`
}
