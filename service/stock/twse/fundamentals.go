package twse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// defaultFundamentalsEndpoint is the TWSE OpenAPI v1 BWIBBU_d dataset
// — a daily snapshot of per-stock 殖利率 (dividend yield), 本益比 (PER),
// and 股價淨值比 (PBR) for every listed equity. JSON array of flat
// objects; one row per stock, ~3,500 rows on a normal trading day.
//
// Verified 2026-05-07 against live: returns the most-recent published
// trading day. Sample row for 2330:
//
//	{"Date":"20260506","Code":"2330","Name":"台積電",
//	 "ClosePrice":"2250.00","DividendYield":"0.98",
//	 "DividendYear":"114","PEratio":"33.97","PBratio":"10.77",
//	 "FiscalYearQuarter":"2025Q4"}
//
// The endpoint serves only the latest snapshot, no per-date filter —
// same single-key cache pattern as BFIAUU and TDCC holders.
const defaultFundamentalsEndpoint = "https://openapi.twse.com.tw/v1/exchangeReport/BWIBBU_d"

// Fundamentals captures per-stock 殖利率 / 本益比 / 股價淨值比 for one
// TWSE listing. Empty (zero StockID) when the upstream had no row for
// the stock id (delisted / pre-listing / OTC / non-TW); the renderer
// gates on Has() to omit the row gracefully.
//
// Numeric zero values are legal — a non-dividend-paying stock has
// DividendYield = 0, a loss-making stock may have PER = 0 (TWSE
// emits "-" which parses to 0). The renderer treats individual
// zero metrics as "missing" and skips that segment of the row.
type Fundamentals struct {
	StockID           string    // 證券代號
	Name              string    // 證券名稱 — Chinese short name from upstream
	AsOf              time.Time // Date — YYYYMMDD parsed
	ClosePrice        float64   // ClosePrice — published close on AsOf
	DividendYield     float64   // 殖利率 % (e.g. 0.98 means 0.98%)
	PERatio           float64   // 本益比 (P/E ratio, x multiplier)
	PBRatio           float64   // 股價淨值比 (P/B ratio, x multiplier)
	FiscalYearQuarter string    // FiscalYearQuarter — e.g. "2025Q4"
}

// Has reports whether the upstream produced a row for this stock.
// Treats StockID == "" as "not found" — every row in the dump has
// a non-empty Code; the only zero-StockID case is a missing-stock
// lookup. This decouples row presence from individual metric
// availability (a no-dividend stock still has Has() == true).
func (f Fundamentals) Has() bool { return f.StockID != "" }

// FundamentalsProvider is implemented by anything that can answer
// per-stock fundamentals queries. asOf is informational (the upstream
// dump pins its own publication date); pass the request's clamped
// asOf for publish-window-aware TTL.
type FundamentalsProvider interface {
	GetFundamentals(ctx context.Context, stockID string, asOf time.Time) (Fundamentals, error)
}

// FundamentalsExactProvider is the cache-friendly twin: pulls the live
// snapshot once per call with no walk-back, so a caching wrapper above
// can hold the parsed-once map and skip the network for the rest of
// the publish window. found=false reserved for "upstream returned
// empty array" (off-trading-day or pre-publish window); transport /
// parse failures return err.
type FundamentalsExactProvider interface {
	FetchFundamentalsExact(ctx context.Context, asOf time.Time) (FundamentalsDump, bool, error)
}

// FundamentalsDump is the parsed per-stock map from one BWIBBU_d
// snapshot. Cached as a single per-process map; the renderer pulls
// the per-stock row on demand via GetFundamentals.
type FundamentalsDump struct {
	AsOf time.Time
	Rows map[string]Fundamentals
}

// SetFundamentalsEndpoint overrides the upstream URL used by
// FetchFundamentalsExact, so tests can route to a fixture httptest.Server.
// Production callers should rely on New() which wires the default
// automatically. Empty disables FetchFundamentalsExact (returns
// ErrUnavailable).
func (p *HTTPProvider) SetFundamentalsEndpoint(endpoint string) {
	p.fundamentals = endpoint
}

// FetchFundamentalsExact pulls the BWIBBU_d snapshot and returns the
// parsed per-stock map. Like the other OpenAPI providers, the dump
// has no per-date variation in the URL (latest-only), so the caching
// wrapper keys on a single sentinel.
func (p *HTTPProvider) FetchFundamentalsExact(ctx context.Context, _ time.Time) (FundamentalsDump, bool, error) {
	if p.fundamentals == "" {
		return FundamentalsDump{}, false, ErrUnavailable
	}
	body, err := p.fetch(ctx, p.fundamentals)
	if err != nil {
		return FundamentalsDump{}, false, err
	}

	var raw []map[string]string
	if err := json.Unmarshal(body, &raw); err != nil {
		return FundamentalsDump{}, false, fmt.Errorf("twse: fundamentals unmarshal: %w", err)
	}
	if len(raw) == 0 {
		return FundamentalsDump{}, false, nil
	}

	asOf := parseTWSEDate(strings.TrimSpace(raw[0]["Date"]))
	out := FundamentalsDump{AsOf: asOf, Rows: make(map[string]Fundamentals, len(raw))}
	for _, row := range raw {
		stockID := strings.TrimSpace(row["Code"])
		if stockID == "" {
			continue
		}
		out.Rows[stockID] = Fundamentals{
			StockID:           stockID,
			Name:              strings.TrimSpace(row["Name"]),
			AsOf:              asOf,
			ClosePrice:        parseTWSEFloat(row["ClosePrice"]),
			DividendYield:     parseTWSEFloat(row["DividendYield"]),
			PERatio:           parseTWSEFloat(row["PEratio"]),
			PBRatio:           parseTWSEFloat(row["PBratio"]),
			FiscalYearQuarter: strings.TrimSpace(row["FiscalYearQuarter"]),
		}
	}
	return out, true, nil
}

// GetFundamentals returns per-stock fundamentals from an uncached
// HTTPProvider. The wrapper around this in cached_walkback.go is the
// production path; the bare HTTPProvider implementation exists for
// tests and for symmetry with the other providers.
func (p *HTTPProvider) GetFundamentals(ctx context.Context, stockID string, asOf time.Time) (Fundamentals, error) {
	dump, found, err := p.FetchFundamentalsExact(ctx, asOf)
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
