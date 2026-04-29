package twse

import (
	"context"
	"errors"
	"time"
)

// Exact-date fetch interfaces and HTTPProvider methods. The public
// Get* methods walk back ≤7 days internally, which makes them ergonomic
// callers but a poor fit for a date-keyed cache (a request for asOf=today
// resolves to "yesterday's data" inside the loop, so caching at the entry
// point keys on the WRONG date). The Fetch*Exact methods below skip the
// walk-back and report (value, found, err) so a cache wrapper can do its
// own walk-back, keying each probe on the resolved date and remembering
// "no data on this date" as a Found=false sentinel.
//
// found=false means "upstream returned no row / no file for this exact
// date" — a publish gap, not an error. err is reserved for transport /
// parse failures that should bubble up to the caller.

// PerStockExactProvider fetches the T86 row for a single date with no
// walk-back. Implemented by HTTPProvider.
type PerStockExactProvider interface {
	FetchT86Exact(ctx context.Context, stockID, date string) (StockData, bool, error)
}

// SecuritiesLendingExactProvider fetches the TWT93U row for a single
// date with no walk-back.
type SecuritiesLendingExactProvider interface {
	FetchTWT93UExact(ctx context.Context, stockID, date string) (SecuritiesLending, bool, error)
}

// StockMarginExactProvider fetches the per-stock MI_MARGN row for a
// single date with no walk-back.
type StockMarginExactProvider interface {
	FetchMIMARGNStockExact(ctx context.Context, stockID, date string) (StockMargin, bool, error)
}

// RetailFuturesExactProvider fetches the TAIFEX retail futures triple
// (mxfNet, tmfNet) for a single day with no walk-back. Either
// commodity may individually report "not published yet" — found=true
// requires at least one to have data.
type RetailFuturesExactProvider interface {
	FetchTAIFEXFuturesExact(ctx context.Context, day time.Time) (RetailFutures, bool, error)
}

// OptionsPCRExactProvider fetches the TAIFEX PCR for a single day with
// no walk-back.
type OptionsPCRExactProvider interface {
	FetchPCRExact(ctx context.Context, day time.Time) (OptionsPCR, bool, error)
}

// VIXExactProvider fetches the TAIFEX VIX for a single day. The VIX
// upstream is a monthly dump file, so the implementation derives the
// month from `day` and returns the row for that day inside the file.
// Cache wrappers should key on the day, not the month, so requests
// for different days within the same month each get their own entry.
type VIXExactProvider interface {
	FetchVIXExact(ctx context.Context, day time.Time) (VIX, bool, error)
}

// FetchT86Exact retrieves the T86 row for stockID at the given date
// (YYYYMMDD) with no walk-back. Returns found=false (with no error)
// when upstream replied with no row — the cache wrapper above this can
// remember that fact so it doesn't re-probe the same dead date on the
// next request.
func (p *HTTPProvider) FetchT86Exact(ctx context.Context, stockID, date string) (StockData, bool, error) {
	if p.t86 == "" {
		return StockData{}, false, ErrUnavailable
	}
	d, err := p.fetchT86(ctx, stockID, date)
	switch {
	case err == nil:
		return d, true, nil
	case errors.Is(err, ErrUnavailable):
		return StockData{}, false, nil
	}
	return StockData{}, false, err
}

// FetchTWT93UExact retrieves the TWT93U row for stockID at the given
// date (YYYYMMDD) with no walk-back.
func (p *HTTPProvider) FetchTWT93UExact(ctx context.Context, stockID, date string) (SecuritiesLending, bool, error) {
	if p.twt93u == "" {
		return SecuritiesLending{}, false, ErrUnavailable
	}
	d, err := p.fetchTWT93U(ctx, stockID, date)
	switch {
	case err == nil:
		return d, true, nil
	case errors.Is(err, ErrUnavailable):
		return SecuritiesLending{}, false, nil
	}
	return SecuritiesLending{}, false, err
}

// FetchMIMARGNStockExact retrieves the per-stock MI_MARGN row for
// stockID at the given date (YYYYMMDD) with no walk-back.
func (p *HTTPProvider) FetchMIMARGNStockExact(ctx context.Context, stockID, date string) (StockMargin, bool, error) {
	if p.miMargnStock == "" {
		return StockMargin{}, false, ErrUnavailable
	}
	d, err := p.fetchMIMARGNStock(ctx, stockID, date)
	switch {
	case err == nil:
		return d, true, nil
	case errors.Is(err, ErrUnavailable):
		return StockMargin{}, false, nil
	}
	return StockMargin{}, false, err
}

// FetchTAIFEXFuturesExact retrieves the MXF + TMF retail-futures pair
// for the given day with no walk-back. Returns found=true when at
// least one commodity has a published row; found=false when both came
// back empty (typical for non-trading days, holidays, or pre-publish
// hours of a trading day).
func (p *HTTPProvider) FetchTAIFEXFuturesExact(ctx context.Context, day time.Time) (RetailFutures, bool, error) {
	if p.taifexFutures == "" {
		return RetailFutures{}, false, ErrUnavailable
	}
	mxfNet, mxfOk, mxfErr := p.fetchTAIFEXOnce(ctx, commodityMXF, day)
	if mxfErr != nil {
		return RetailFutures{}, false, mxfErr
	}
	tmfNet, tmfOk, tmfErr := p.fetchTAIFEXOnce(ctx, commodityTMF, day)
	if tmfErr != nil {
		return RetailFutures{}, false, tmfErr
	}
	if !mxfOk && !tmfOk {
		return RetailFutures{}, false, nil
	}
	out := RetailFutures{MXFNet: mxfNet, TMFNet: tmfNet, AsOf: day}
	return out, true, nil
}

// FetchPCRExact retrieves the TAIFEX options PCR for the given day
// with no walk-back.
func (p *HTTPProvider) FetchPCRExact(ctx context.Context, day time.Time) (OptionsPCR, bool, error) {
	if p.taifexPCR == "" {
		return OptionsPCR{}, false, ErrUnavailable
	}
	pcr, ok, err := p.fetchPCROnce(ctx, day)
	if err != nil {
		return OptionsPCR{}, false, err
	}
	return pcr, ok, nil
}

// FetchVIXExact retrieves the TAIFEX VIX value for the given day.
// Internally fetches the day's month-stem dump file; the cache wrapper
// keys on day so requests for different days within the same month
// get their own entries (and the underlying month dump is fetched
// fresh for each, since the cache layer doesn't know about the
// upstream's monthly batching).
func (p *HTTPProvider) FetchVIXExact(ctx context.Context, day time.Time) (VIX, bool, error) {
	if p.taifexVIX == "" {
		return VIX{}, false, ErrUnavailable
	}
	month := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, twLoc)
	v, ok, err := p.fetchVIXMonth(ctx, month, day)
	if err != nil {
		return VIX{}, false, err
	}
	return v, ok, nil
}
