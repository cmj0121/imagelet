package twse

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cmj0121/imagelet/internal/safehttp"

	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

// defaultTAIFEXFuturesEndpoint returns the daily 三大法人區分各期貨契約
// CSV download for one commodity. The endpoint accepts a POST form
// (queryStartDate, queryEndDate, commodityId) and returns BIG5-encoded
// CSV with 4 lines: header + one row per institutional category
// (自營商, 投信, 外資及陸資). The retail position is derived as
// `-(institutional_net_oi)` since for any futures contract
// total_long_oi ≡ total_short_oi, hence retail_net = -(inst_long - inst_short).
//
// Empty / no-data days return an HTML "查無資料" alert page rather than
// CSV — the lookback walks back day-by-day until a real CSV lands.
const defaultTAIFEXFuturesEndpoint = "https://www.taifex.com.tw/cht/3/futContractsDateDown"

// commodityMXF / commodityTMF are the TAIFEX contract codes for the
// two retail-favored mini index futures: 小型臺指期貨 (MXF, 1/4 size of
// the standard TXF) and 微型臺指期貨 (TMF, 1/10 of MXF). Retail
// concentration is highest in these two contracts because the smaller
// notional size keeps margin requirements within retail reach.
const (
	commodityMXF = "MXF"
	commodityTMF = "TMF"
)

// RetailFutures captures the retail-derived net OI for the two mini
// index futures TAIFEX publishes. Each value is in lots (口數) and
// signed: positive = retail net long, negative = retail net short.
//
// Derivation: TAIFEX publishes per-contract OI broken down by the
// three institutional categories (自營商 / 投信 / 外資及陸資). For any
// futures contract total_long_oi equals total_short_oi by definition,
// so retail net OI = -(institutional net OI). We fetch only the
// institutional CSV and flip the sign rather than fetching a separate
// total-OI endpoint — one HTTP call per contract instead of two.
//
// Empty fields (zero) when the upstream had no data or the lookback
// exhausted; the renderer treats zero as "no row" via HasMXF/HasTMF.
type RetailFutures struct {
	MXFNet int64     // 小台 retail net OI in lots
	TMFNet int64     // 微台 retail net OI in lots
	AsOf   time.Time // trading date the upstream supplied
}

// HasMXF reports whether the MXF (小台) figure was successfully fetched.
// Distinguishes "0 lots after a real fetch" (rare but legal) from
// "endpoint failed entirely" — the renderer wants to omit the row in
// the latter case but still show a true 0 in the former. Practically
// the OI is never exactly 0 across all three institutional categories
// in a real session, so 0 == not fetched is a safe heuristic.
func (r RetailFutures) HasMXF() bool { return r.MXFNet != 0 }

// HasTMF reports whether the TMF (微台) figure was successfully fetched.
// Same semantics as HasMXF.
func (r RetailFutures) HasTMF() bool { return r.TMFNet != 0 }

// HasAny reports whether either side has data — the handler skips the
// 散戶 group entirely when both sides are empty so non-TW closed-market
// renders don't show a stray label-only row.
func (r RetailFutures) HasAny() bool { return r.HasMXF() || r.HasTMF() }

// RetailFuturesProvider is an optional extension for fetching the
// retail futures positioning derived from TAIFEX 三大法人 OI. Get
// walks back from asOf using a per-day lookback, returning the latest
// published trading day at or before the requested date with both
// MXF and TMF rows. Optional because not every Provider implementation
// can answer this query (the TAIFEX endpoint requires its own scraper);
// the handler type-asserts and falls back to skipping the 散戶 rows
// when the wired provider doesn't implement it.
type RetailFuturesProvider interface {
	GetRetailFutures(ctx context.Context, asOf time.Time) (RetailFutures, error)
}

// SetTAIFEXFuturesEndpoint overrides the upstream URL used by
// GetRetailFutures, so tests can route to a fixture httptest.Server.
// Production callers should rely on New() which wires
// defaultTAIFEXFuturesEndpoint automatically. Empty disables
// GetRetailFutures (returns ErrUnavailable).
func (p *HTTPProvider) SetTAIFEXFuturesEndpoint(endpoint string) {
	p.taifexFutures = endpoint
}

// GetRetailFutures fetches the retail-derived net OI for MXF (小台)
// and TMF (微台) on the latest published trading day at or before asOf.
// Each contract is fetched independently with the same lookback walk;
// a per-contract failure is logged-and-zeroed rather than failing the
// whole call so a partial answer (e.g. MXF only) still surfaces.
//
// Returns ErrUnavailable when the endpoint is unset (production wires
// it via New(); tests opt in via SetTAIFEXFuturesEndpoint) or when
// both contracts came back empty after the full lookback.
func (p *HTTPProvider) GetRetailFutures(ctx context.Context, asOf time.Time) (RetailFutures, error) {
	if p.taifexFutures == "" {
		return RetailFutures{}, ErrUnavailable
	}
	if asOf.IsZero() {
		asOf = time.Now()
	}

	mxfNet, mxfDate, mxfErr := p.walkBackTAIFEX(ctx, commodityMXF, asOf)
	tmfNet, tmfDate, tmfErr := p.walkBackTAIFEX(ctx, commodityTMF, asOf)
	if mxfErr != nil && tmfErr != nil {
		return RetailFutures{}, mxfErr
	}

	out := RetailFutures{MXFNet: mxfNet, TMFNet: tmfNet}
	switch {
	case !mxfDate.IsZero():
		out.AsOf = mxfDate
	case !tmfDate.IsZero():
		out.AsOf = tmfDate
	}
	return out, nil
}

// walkBackTAIFEX issues per-day POST requests for one commodity,
// walking back from asOf up to maxLookbackDays. Returns the parsed
// retail net OI (sign-flipped institutional net) for the first day
// that yields a CSV with rows. Returns ErrUnavailable if the lookback
// exhausts.
func (p *HTTPProvider) walkBackTAIFEX(ctx context.Context, commodity string, asOf time.Time) (int64, time.Time, error) {
	day := asOf
	for i := 0; i < maxLookbackDays; i++ {
		net, ok, err := p.fetchTAIFEXOnce(ctx, commodity, day)
		if err != nil {
			return 0, time.Time{}, err
		}
		if ok {
			return net, day, nil
		}
		day = day.AddDate(0, 0, -1)
	}
	return 0, time.Time{}, ErrUnavailable
}

// fetchTAIFEXOnce issues one POST against the TAIFEX endpoint for a
// single (commodity, date) pair. Returns (retailNet, true, nil) on a
// usable CSV, (0, false, nil) on the no-data alert HTML page, or
// (0, false, err) on transport / decode failures.
func (p *HTTPProvider) fetchTAIFEXOnce(ctx context.Context, commodity string, day time.Time) (int64, bool, error) {
	dateStr := day.Format("2006/01/02")
	form := url.Values{
		"queryStartDate": []string{dateStr},
		"queryEndDate":   []string{dateStr},
		"commodityId":    []string{commodity},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.taifexFutures, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; imagelet/0.3)")

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, false, err
	}
	defer resp.Body.Close()
	safehttp.BoundBody(resp, safehttp.DefaultBodyCap)
	if resp.StatusCode != http.StatusOK {
		return 0, false, fmt.Errorf("taifex: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, false, err
	}
	// Empty-day responses come back as a tiny HTML alert page with a
	// `查無資料` script call rather than a CSV. The download button on
	// the live site triggers a form re-submit on success that returns
	// CSV; a missing day re-renders the form. CSVs always start with
	// the BIG5-encoded header byte sequence — bytes.HasPrefix on the
	// `<` opening of any HTML response is the cheapest discriminator.
	if bytes.HasPrefix(bytes.TrimLeft(body, " \t\r\n"), []byte("<")) {
		return 0, false, nil
	}

	utf8Body, err := decodeBig5(body)
	if err != nil {
		return 0, false, err
	}
	net, err := parseTAIFEXFuturesCSV(utf8Body)
	if err != nil {
		return 0, false, err
	}
	return net, true, nil
}

// decodeBig5 transcodes a BIG5-encoded byte slice to UTF-8. TAIFEX
// CSVs are still served in BIG5 even when the parent HTTP response
// header advertises UTF-8 charset.
func decodeBig5(b []byte) ([]byte, error) {
	r := transform.NewReader(bytes.NewReader(b), traditionalchinese.Big5.NewDecoder())
	return io.ReadAll(r)
}

// parseTAIFEXFuturesCSV reads the 三大法人區分各期貨契約 CSV and
// returns the retail-derived net OI: -(sum of institutional net OI).
// Expected columns include "多空未平倉口數淨額" at index 13 (the upstream
// per-row signed long-minus-short OI delta). Other columns are ignored.
//
// Returns 0 with no error when the CSV has only a header row (rare —
// upstream usually emits 3 institutional rows even on quiet days,
// possibly all zero). Returns an error on malformed input.
func parseTAIFEXFuturesCSV(body []byte) (int64, error) {
	r := csv.NewReader(bytes.NewReader(body))
	r.FieldsPerRecord = -1 // tolerate trailing empty fields
	rows, err := r.ReadAll()
	if err != nil {
		return 0, err
	}
	if len(rows) < 2 {
		return 0, nil
	}

	// Index of the per-row signed OI delta. The upstream column order
	// has been stable for years; we still pin the column by header
	// match so a future TAIFEX layout shuffle fails loudly rather than
	// silently mis-attributing OIs.
	netCol, err := findColumn(rows[0], "多空未平倉口數淨額")
	if err != nil {
		return 0, err
	}

	var instNet int64
	for _, row := range rows[1:] {
		if len(row) <= netCol {
			continue
		}
		v, perr := parseInt(row[netCol])
		if perr != nil {
			return 0, fmt.Errorf("taifex: parse net col: %w", perr)
		}
		instNet += v
	}
	// Retail net = -(institutional net) by the OI identity.
	return -instNet, nil
}

// findColumn returns the index of a header cell matching needle. The
// upstream may emit Trim-able whitespace inside cells; trim before
// comparing. Returns an error when the header is not found so the
// parser fails loudly on a layout change.
func findColumn(header []string, needle string) (int, error) {
	for i, cell := range header {
		if strings.TrimSpace(cell) == needle {
			return i, nil
		}
	}
	return -1, fmt.Errorf("taifex: column %q not in header %v", needle, header)
}

// parseInt strips upstream "," thousands separators and parses the
// remaining digits as an int64. Empty / "-" cells map to 0 (TAIFEX
// uses "-" as a no-trade marker on rare zero rows).
func parseInt(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return 0, nil
	}
	s = strings.ReplaceAll(s, ",", "")
	return strconv.ParseInt(s, 10, 64)
}
