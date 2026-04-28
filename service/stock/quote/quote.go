// Package quote provides stock-quote retrieval for the /stock service.
//
// Quote is the value type — last price, previous-day close, change percent,
// currency, asOf, and isClosed. Provider is the contract; Get returns
// (Quote, error). Two implementations live in subpackages: yahoo (calls
// Yahoo Finance v8 chart API) and cached (wraps any Provider with 60s
// success / 600s failure TTL + singleflight stampede control).
package quote

import (
	"context"
	"errors"
	"time"
)

// Quote captures the fields the /stock handler renders. Decimal-light:
// we hold floats verbatim; the renderer formats with thousands separator
// and 2dp.
//
// DayHigh/DayLow and Week52High/Week52Low are 0 when the upstream did not
// supply them (typically thinly-traded symbols). The renderer treats
// HasDayRange() / Has52WeekRange() as the signal to omit the corresponding
// progress-bar row gracefully — no error, just no bar.
//
// Name / LongName carry Yahoo's shortName / longName. ShortName is
// localized (e.g. CN for TW listings) and renders nicely on CJK-capable
// surfaces; LongName is typically Latin (e.g. "Taiwan Semiconductor
// Manufacturing Company Limited") and is the safe fallback for the PNG
// render path which uses an EN-only basicfont. Both are empty when
// upstream did not supply them; the renderer falls back to the symbol
// alone in that case.
type Quote struct {
	Symbol     string    // canonical symbol, e.g. "^GSPC"
	Name       string    // shortName from upstream (often CN for TW listings)
	LongName   string    // longName from upstream (Latin form for most)
	Last       float64   // last trade / current price (also OHLC's Close)
	Open       float64   // session open; 0 when missing
	PrevClose  float64   // previous trading day's close (for change %)
	Currency   string    // ISO 4217 like "USD", "TWD"
	AsOf       time.Time // server-side time of the quote (regularMarketTime)
	IsClosed   bool      // market is currently closed
	DayHigh    float64   // intraday session high; 0 when missing
	DayLow     float64   // intraday session low; 0 when missing
	Week52High float64   // trailing 52-week high; 0 when missing
	Week52Low  float64   // trailing 52-week low; 0 when missing
	Volume     int64     // session traded shares; 0 when missing
}

// HasDayRange reports whether DayHigh and DayLow are both populated and
// form a meaningful range (Low < High). The renderer uses this to decide
// whether to draw the day-range progress bar.
func (q Quote) HasDayRange() bool {
	return q.DayHigh > 0 && q.DayLow > 0 && q.DayLow < q.DayHigh
}

// HasOHLC reports whether the full OHLC quartet (Open / DayHigh / DayLow
// / Last) is populated and forms a meaningful range. The renderer uses
// this to decide whether to draw the OHLC range bar; degenerate ranges
// (Low == High, missing Open) gracefully omit the bar instead of
// emitting a zero-width or NaN render.
func (q Quote) HasOHLC() bool {
	return q.Open > 0 && q.HasDayRange() && q.Last > 0
}

// Has52WeekRange reports whether Week52High and Week52Low are both
// populated and form a meaningful range. The renderer uses this to decide
// whether to draw the 52-week-range progress bar.
func (q Quote) Has52WeekRange() bool {
	return q.Week52High > 0 && q.Week52Low > 0 && q.Week52Low < q.Week52High
}

// DayPosition returns the current price's position within the intraday
// range as a [0,1] fraction. 0 = at day low, 1 = at day high. Caller
// MUST gate on HasDayRange() — this returns 0 when the range is empty
// to avoid divide-by-zero.
func (q Quote) DayPosition() float64 {
	if !q.HasDayRange() {
		return 0
	}
	return clamp01((q.Last - q.DayLow) / (q.DayHigh - q.DayLow))
}

// Week52Position returns the current price's position within the
// 52-week range as a [0,1] fraction. Same semantics as DayPosition.
func (q Quote) Week52Position() float64 {
	if !q.Has52WeekRange() {
		return 0
	}
	return clamp01((q.Last - q.Week52Low) / (q.Week52High - q.Week52Low))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// ChangePercent computes (Last - PrevClose) / PrevClose * 100 with a
// zero guard. Returns 0 when PrevClose is 0 — which happens for fresh
// listings or bad-data responses, and is safer to render as 0.00% than
// to crash on division.
func (q Quote) ChangePercent() float64 {
	if q.PrevClose == 0 {
		return 0
	}
	return (q.Last - q.PrevClose) / q.PrevClose * 100
}

// Provider is implemented by anything that can fetch a Quote for a symbol.
// Get must respect ctx cancellation. ErrUnavailable indicates the upstream
// is reachable but doesn't have data for the symbol; transport errors are
// returned as-is for caller-side retry / cache decisions.
type Provider interface {
	Get(ctx context.Context, symbol string) (Quote, error)
}

// HistoricalProvider is an optional extension of Provider for fetching
// quotes pinned to a historical date (?date= override path). GetAt
// returns the latest bar at or before asOf — the closest completed
// trading session, with IsClosed forced true since a past bar is by
// definition closed. Optional because not every Provider can answer
// historical queries; the /stock handler type-asserts and falls back
// to ErrUnavailable when the wired provider doesn't implement it.
type HistoricalProvider interface {
	GetAt(ctx context.Context, symbol string, asOf time.Time) (Quote, error)
}

// ErrUnavailable signals "the provider answered but has no data for this
// symbol" — distinct from transport errors. Cached providers may treat
// ErrUnavailable as an extended-TTL failure to avoid hammering the
// upstream when a symbol is permanently unknown.
var ErrUnavailable = errors.New("quote unavailable")
