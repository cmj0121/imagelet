package twse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// defaultBlockTradesEndpoint is the TWSE OpenAPI v1 BFIAUU dataset —
// a JSON array of block-trade rows for the most recent published
// trading day (typically the same business day after ~17:00 Asia/Taipei,
// the previous trading day before that). The endpoint serves the
// latest snapshot only; no per-date filter is exposed, so we cache by
// a single sentinel like the holders dump.
//
// Verified 2026-05-07 against live: returns ~50-200 rows on a typical
// day (one row per block-trade event, multi-row for stocks with
// several events). Stock 2301 sample row shape:
//
//	{"Code":"2301","Name":"光寶科","Classn":"配對交易",
//	 "TradePrice":"177.75","TradeVolume":"1501000",
//	 "TradeValue":"266802750"}
const defaultBlockTradesEndpoint = "https://openapi.twse.com.tw/v1/exchangeReport/BFIAUU"

// BlockTrade captures one row of the daily 大宗交易/配對交易 report.
// All numeric fields are parsed from upstream string values via
// parseTWSENumber (TWSE OpenAPI emits everything as strings).
type BlockTrade struct {
	StockID    string  // 證券代號
	Name       string  // 證券名稱 — Chinese short name from upstream
	Class      string  // Classn — typically 配對交易 (matched), occasionally 鉅額逐筆
	TradePrice float64 // 成交價 (TWD)
	TradeVolume int64  // 成交股數
	TradeValue  int64  // 成交金額 (TWD)
}

// BlockTradesDay carries all block-trade rows from one BFIAUU snapshot
// keyed by trimmed stock id. Cached as a single per-process map; the
// renderer pulls the per-stock slice on demand via GetBlockTrades.
//
// AsOf is the resolved publication date. Because the upstream JSON
// carries no per-row date and no header date field, we stamp AsOf at
// fetch time using the caller's wall-clock (clamped to TWSE trading
// hours via the caching layer's now func). This is acceptable for
// the same-day rendering use case — block trades are most actionable
// in the trading-day window where they were reported, and the
// publish-window-aware TTL bounds staleness automatically.
type BlockTradesDay struct {
	AsOf time.Time
	Rows map[string][]BlockTrade
}

// BlockTradesProvider is implemented by anything that can answer
// per-stock 大宗交易 queries for a single day. Empty []BlockTrade
// (length 0) is a valid answer meaning "no block trades for this
// stock today" — far more common than the populated case.
type BlockTradesProvider interface {
	GetBlockTrades(ctx context.Context, stockID string, asOf time.Time) (BlockTradesDay, []BlockTrade, error)
}

// BlockTradesExactProvider is the cache-friendly twin: pulls the live
// snapshot once per call with no walk-back, so a caching wrapper above
// can hold the parsed-once map and skip the network for the rest of
// the publish window. found=false reserved for "upstream answered
// with an empty array" (off-trading-day or pre-publish window);
// transport / parse failures return err.
type BlockTradesExactProvider interface {
	FetchBlockTradesExact(ctx context.Context, asOf time.Time) (BlockTradesDay, bool, error)
}

// SetBlockTradesEndpoint overrides the upstream URL used by
// FetchBlockTradesExact, so tests can route to a fixture httptest.Server.
// Production callers should rely on New() which wires
// defaultBlockTradesEndpoint automatically. Empty disables
// FetchBlockTradesExact (returns ErrUnavailable).
func (p *HTTPProvider) SetBlockTradesEndpoint(endpoint string) {
	p.blockTrades = endpoint
}

// FetchBlockTradesExact pulls the BFIAUU snapshot and returns the parsed
// per-stock map. The dump itself has no per-date variation in the URL
// (the endpoint always serves the latest), so the caching wrapper
// keys on a single sentinel rather than on asOf. The returned
// BlockTradesDay carries the asOf the caller passed in — used for
// publish-window TTL accounting and for the renderer's stamp.
func (p *HTTPProvider) FetchBlockTradesExact(ctx context.Context, asOf time.Time) (BlockTradesDay, bool, error) {
	if p.blockTrades == "" {
		return BlockTradesDay{}, false, ErrUnavailable
	}
	body, err := p.fetch(ctx, p.blockTrades)
	if err != nil {
		return BlockTradesDay{}, false, err
	}

	var raw []map[string]string
	if err := json.Unmarshal(body, &raw); err != nil {
		return BlockTradesDay{}, false, fmt.Errorf("twse: blocktrades unmarshal: %w", err)
	}
	if len(raw) == 0 {
		return BlockTradesDay{AsOf: asOf}, false, nil
	}

	out := BlockTradesDay{AsOf: asOf, Rows: make(map[string][]BlockTrade, len(raw))}
	for _, row := range raw {
		stockID := strings.TrimSpace(row["Code"])
		if stockID == "" {
			continue
		}
		bt := BlockTrade{
			StockID:     stockID,
			Name:        strings.TrimSpace(row["Name"]),
			Class:       strings.TrimSpace(row["Classn"]),
			TradePrice:  parseTWSEFloat(row["TradePrice"]),
			TradeVolume: parseTWSENumber(row["TradeVolume"]),
			TradeValue:  parseTWSENumber(row["TradeValue"]),
		}
		out.Rows[stockID] = append(out.Rows[stockID], bt)
	}
	return out, true, nil
}

// GetBlockTrades returns the per-stock block-trade list from an
// uncached HTTPProvider. The wrapper around this in cached_walkback.go
// is the production path; the bare HTTPProvider implementation exists
// for tests and for symmetry with the other providers.
func (p *HTTPProvider) GetBlockTrades(ctx context.Context, stockID string, asOf time.Time) (BlockTradesDay, []BlockTrade, error) {
	day, found, err := p.FetchBlockTradesExact(ctx, asOf)
	if err != nil {
		return BlockTradesDay{}, nil, err
	}
	if !found {
		return day, nil, nil
	}
	return day, day.Rows[strings.TrimSpace(stockID)], nil
}

// parseTWSEFloat parses a TWSE OpenAPI string-encoded float (e.g.
// "177.75") to float64. Leading/trailing whitespace and commas are
// stripped to match the existing parseTWSENumber tolerance. Returns
// 0 on parse failure — the renderer treats 0 as "missing" and skips
// downstream display.
func parseTWSEFloat(s string) float64 {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return 0
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil {
		return 0
	}
	return f
}
