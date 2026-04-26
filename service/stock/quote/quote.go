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
type Quote struct {
	Symbol    string    // canonical symbol, e.g. "^GSPC"
	Last      float64   // last trade / current price
	PrevClose float64   // previous trading day's close (for change %)
	Currency  string    // ISO 4217 like "USD", "TWD"
	AsOf      time.Time // server-side time of the quote (regularMarketTime)
	IsClosed  bool      // market is currently closed
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

// ErrUnavailable signals "the provider answered but has no data for this
// symbol" — distinct from transport errors. Cached providers may treat
// ErrUnavailable as an extended-TTL failure to avoid hammering the
// upstream when a symbol is permanently unknown.
var ErrUnavailable = errors.New("quote unavailable")
