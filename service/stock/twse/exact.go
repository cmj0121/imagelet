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

// RetailFuturesExactProvider fetches the TAIFEX futures positioning
// (MXF/TMF retail nets plus the TXF institutional breakdown) for a
// single day with no walk-back. Any commodity may individually report
// "not published yet" — found=true requires at least one to have data.
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
// and the TXF institutional breakdown for the given day with no
// walk-back. This is the path production actually runs (via
// CachedRetailFutures); HTTPProvider.GetRetailFutures is the uncached
// twin and the two keep an identical failure posture:
//
//   - Per-commodity containment. A failure on one contract zeroes only
//     that contract's fields; the whole fetch fails only when ALL
//     THREE fail. Adding the TXF POST must not make the already-
//     rendered 散戶 MXF/TMF group more fragile than it was before TXF
//     existed — a transient 500 or timeout on TXF alone is not a
//     reason to drop the minis.
//   - found is mini-driven: it reports whether MXF or TMF published,
//     deliberately ignoring TXF. found terminates the caller's
//     walk-back, and a TXF-only day is a day whose HasAny() is false —
//     stopping there would strand the walk on a date with no
//     renderable 散戶 rows when an earlier date has them. Revisit once
//     TXF gets its own render group and can justify a day on its own.
//   - A contained failure sets Partial on the result. Containment
//     alone would be a regression: the caller caches found=true
//     results for a full session, so one 502 on MXF would pin
//     MXFNet=0 (and drop the 小台散戶 row) for 24h with no retry.
//     Partial lets CachedRetailFutures hold the degraded answer only
//     briefly. Each failure is also logged per contract — otherwise a
//     single-contract outage is invisible: HTTP 200, rendered card,
//     missing row, no error anywhere.
func (p *HTTPProvider) FetchTAIFEXFuturesExact(ctx context.Context, day time.Time) (RetailFutures, bool, error) {
	if p.taifexFutures == "" {
		return RetailFutures{}, false, ErrUnavailable
	}
	// A failed fetch yields a zero breakdown, so the corresponding
	// fields stay zero and the Has* predicates report "not fetched".
	mxf, mxfOk, mxfErr := p.fetchTAIFEXOnce(ctx, commodityMXF, day)
	tmf, tmfOk, tmfErr := p.fetchTAIFEXOnce(ctx, commodityTMF, day)
	txf, _, txfErr := p.fetchTAIFEXOnce(ctx, commodityTXF, day)
	if mxfErr != nil && tmfErr != nil && txfErr != nil {
		return RetailFutures{}, false, mxfErr
	}
	logTAIFEXContractFailure(commodityMXF, day, mxfErr)
	logTAIFEXContractFailure(commodityTMF, day, tmfErr)
	logTAIFEXContractFailure(commodityTXF, day, txfErr)
	if !mxfOk && !tmfOk {
		return RetailFutures{}, false, nil
	}
	out := RetailFutures{
		MXFNet:        mxf.retailNet(),
		TMFNet:        tmf.retailNet(),
		TXFDealerNet:  txf.dealer,
		TXFTrustNet:   txf.trust,
		TXFForeignNet: txf.foreign,
		TXFInstNet:    txf.instNet(),
		Partial:       mxfErr != nil || tmfErr != nil || txfErr != nil,
		AsOf:          day,
	}
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
