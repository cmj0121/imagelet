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

	"github.com/rs/zerolog/log"
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
//
// commodityTXF is the full-size 臺股期貨 (大台) — the canonical
// reference for 三大法人 futures positioning. Institutional hedging
// and arbitrage flows concentrate in the full-size contract (its
// notional keeps it out of most retail hands), so the per-身份別 net
// OI breakdown is read from TXF, while the retail derivation keeps
// using the retail-dominated minis above.
const (
	commodityMXF = "MXF"
	commodityTMF = "TMF"
	commodityTXF = "TXF"
)

// RetailFutures captures the retail-derived net OI for the two mini
// index futures TAIFEX publishes, plus the per-身份別 institutional
// breakdown for the full-size TXF (大台). Each value is in lots (口數)
// and signed: positive = net long, negative = net short.
//
// Derivation: TAIFEX publishes per-contract OI broken down by the
// three institutional categories (自營商 / 投信 / 外資及陸資). For any
// futures contract total_long_oi equals total_short_oi by definition,
// so retail net OI = -(institutional net OI). We fetch only the
// institutional CSV and flip the sign rather than fetching a separate
// total-OI endpoint — one HTTP call per contract instead of two.
//
// Empty fields (zero) when the upstream had no data or the lookback
// exhausted; the renderer treats zero as "no row" via
// HasMXF/HasTMF/HasTXF.
type RetailFutures struct {
	MXFNet int64 // 小台 retail net OI in lots
	TMFNet int64 // 微台 retail net OI in lots

	// TXF (大台) institutional net OI per 身份別, in lots and signed
	// (positive = net long). Purely additive fields: an older
	// taifex-retail-futures.json snapshot (written before these fields
	// existed) still loads under the same ttlcache schema version and
	// simply leaves them zero, which HasTXF treats as "not fetched".
	TXFDealerNet  int64 // 自營商 net OI in lots
	TXFTrustNet   int64 // 投信 net OI in lots
	TXFForeignNet int64 // 外資及陸資 net OI in lots
	TXFInstNet    int64 // dealer + trust + foreign combined

	// Partial reports that at least one contract's fetch FAILED (not
	// merely "not published") while others succeeded, so some fields
	// are zero because of an upstream error rather than because the
	// market said zero. It exists so the cache can hold a degraded
	// answer briefly instead of pinning it for a full session — see
	// partialEntryTTL in cached_walkback.go.
	//
	// Also additive for snapshot purposes: an older file has no such
	// key, JSON leaves it false, and "complete" is the correct default
	// for data written by a build that could not produce partials.
	Partial bool

	AsOf time.Time // trading date the upstream supplied
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

// HasTXF reports whether the TXF (大台) institutional breakdown was
// successfully fetched. Checks the three per-category fields rather
// than TXFInstNet alone — the categories can legally offset to a zero
// total while still carrying real per-party data. Same zero-means-
// not-fetched heuristic as HasMXF.
func (r RetailFutures) HasTXF() bool {
	return r.TXFDealerNet != 0 || r.TXFTrustNet != 0 || r.TXFForeignNet != 0
}

// HasAny reports whether either retail-derived side has data — the
// handler skips the 散戶 group entirely when both sides are empty so
// non-TW closed-market renders don't show a stray label-only row.
// Deliberately excludes the TXF fields: the institutional breakdown
// renders under its own group (gated by HasTXF), so widening HasAny
// would make a TXF-only day render a label-only 散戶 group with no
// rows in it.
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
// and TMF (微台) plus the TXF (大台) institutional breakdown, each on
// the latest published trading day at or before asOf. Every contract
// is fetched independently with the same lookback walk; a per-contract
// failure is zeroed rather than failing the whole call, so a TXF-only
// failure still surfaces the MXF/TMF answer (and vice versa).
//
// This is the uncached twin of FetchTAIFEXFuturesExact, which is what
// production runs through CachedRetailFutures; the two share the
// per-commodity containment posture documented there.
//
// Returns ErrUnavailable when the endpoint is unset (production wires
// it via New(); tests opt in via SetTAIFEXFuturesEndpoint) or when
// all three contracts came back empty after the full lookback.
func (p *HTTPProvider) GetRetailFutures(ctx context.Context, asOf time.Time) (RetailFutures, error) {
	if p.taifexFutures == "" {
		return RetailFutures{}, ErrUnavailable
	}
	if asOf.IsZero() {
		asOf = time.Now()
	}

	mxf, mxfDate, mxfErr := p.walkBackTAIFEX(ctx, commodityMXF, asOf)
	tmf, tmfDate, tmfErr := p.walkBackTAIFEX(ctx, commodityTMF, asOf)
	txf, txfDate, txfErr := p.walkBackTAIFEX(ctx, commodityTXF, asOf)
	if mxfErr != nil && tmfErr != nil && txfErr != nil {
		return RetailFutures{}, mxfErr
	}
	logTAIFEXContractFailure(commodityMXF, asOf, mxfErr)
	logTAIFEXContractFailure(commodityTMF, asOf, tmfErr)
	logTAIFEXContractFailure(commodityTXF, asOf, txfErr)

	// A failed walk returned a zero breakdown, so the corresponding
	// fields stay zero and the Has* predicates report "not fetched".
	out := RetailFutures{
		MXFNet:        mxf.retailNet(),
		TMFNet:        tmf.retailNet(),
		TXFDealerNet:  txf.dealer,
		TXFTrustNet:   txf.trust,
		TXFForeignNet: txf.foreign,
		TXFInstNet:    txf.instNet(),
		Partial:       mxfErr != nil || tmfErr != nil || txfErr != nil,
	}
	switch {
	case !mxfDate.IsZero():
		out.AsOf = mxfDate
	case !tmfDate.IsZero():
		out.AsOf = tmfDate
	case !txfDate.IsZero():
		out.AsOf = txfDate
	}
	return out, nil
}

// logTAIFEXContractFailure emits one warning per contract whose fetch
// failed while others survived. Without it a single-contract outage is
// completely silent: the handler still returns HTTP 200 with a
// rendered card, just missing a row, and nothing upstream of here sees
// an error (the caller's all-three gate swallows it). No-op on a nil
// err so callers can pass results unconditionally.
func logTAIFEXContractFailure(commodity string, day time.Time, err error) {
	if err == nil {
		return
	}
	log.Warn().
		Err(err).
		Str("commodity", commodity).
		Str("day", day.Format("2006-01-02")).
		Msg("taifex: futures contract fetch failed; zeroing its fields")
}

// walkBackTAIFEX issues per-day POST requests for one commodity,
// walking back from asOf up to maxLookbackDays. Returns the parsed
// per-身份別 breakdown for the first day that yields a CSV with rows.
// Returns ErrUnavailable if the lookback exhausts.
func (p *HTTPProvider) walkBackTAIFEX(ctx context.Context, commodity string, asOf time.Time) (taifexFuturesBreakdown, time.Time, error) {
	day := asOf
	for i := 0; i < maxLookbackDays; i++ {
		bd, ok, err := p.fetchTAIFEXOnce(ctx, commodity, day)
		if err != nil {
			return taifexFuturesBreakdown{}, time.Time{}, err
		}
		if ok {
			return bd, day, nil
		}
		day = day.AddDate(0, 0, -1)
	}
	return taifexFuturesBreakdown{}, time.Time{}, ErrUnavailable
}

// fetchTAIFEXOnce issues one POST against the TAIFEX endpoint for a
// single (commodity, date) pair. Returns (breakdown, true, nil) on a
// usable CSV, (zero, false, nil) on the no-data alert HTML page, or
// (zero, false, err) on transport / decode failures.
func (p *HTTPProvider) fetchTAIFEXOnce(ctx context.Context, commodity string, day time.Time) (taifexFuturesBreakdown, bool, error) {
	dateStr := day.Format("2006/01/02")
	form := url.Values{
		"queryStartDate": []string{dateStr},
		"queryEndDate":   []string{dateStr},
		"commodityId":    []string{commodity},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.taifexFutures, strings.NewReader(form.Encode()))
	if err != nil {
		return taifexFuturesBreakdown{}, false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", safehttp.DefaultUserAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return taifexFuturesBreakdown{}, false, err
	}
	defer resp.Body.Close()
	safehttp.BoundBody(resp, safehttp.DefaultBodyCap)
	if resp.StatusCode != http.StatusOK {
		return taifexFuturesBreakdown{}, false, fmt.Errorf("taifex: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return taifexFuturesBreakdown{}, false, err
	}
	// Empty-day responses come back as a tiny HTML alert page with a
	// `查無資料` script call rather than a CSV. The download button on
	// the live site triggers a form re-submit on success that returns
	// CSV; a missing day re-renders the form. CSVs always start with
	// the BIG5-encoded header byte sequence — bytes.HasPrefix on the
	// `<` opening of any HTML response is the cheapest discriminator.
	if bytes.HasPrefix(bytes.TrimLeft(body, " \t\r\n"), []byte("<")) {
		return taifexFuturesBreakdown{}, false, nil
	}

	utf8Body, err := decodeBig5(body)
	if err != nil {
		return taifexFuturesBreakdown{}, false, err
	}
	bd, err := parseTAIFEXFuturesCSV(utf8Body)
	if err != nil {
		return taifexFuturesBreakdown{}, false, err
	}
	return bd, true, nil
}

// decodeBig5 transcodes a BIG5-encoded byte slice to UTF-8. TAIFEX
// CSVs are still served in BIG5 even when the parent HTTP response
// header advertises UTF-8 charset.
func decodeBig5(b []byte) ([]byte, error) {
	r := transform.NewReader(bytes.NewReader(b), traditionalchinese.Big5.NewDecoder())
	return io.ReadAll(r)
}

// taifexFuturesBreakdown carries the per-身份別 net OI parsed from one
// 三大法人區分各期貨契約 CSV, in lots and signed (positive = net long).
// Unexported: the public surface exposes either the retail derivation
// (MXF/TMF) or the flattened TXF fields on RetailFutures, never the
// raw triple.
type taifexFuturesBreakdown struct {
	dealer  int64 // 自營商 (incl. suffixed variants upstream emits)
	trust   int64 // 投信
	foreign int64 // 外資 / 外資及陸資 (renamed upstream over the years)
}

// instNet is the combined institutional net OI across all three
// categories.
func (b taifexFuturesBreakdown) instNet() int64 { return b.dealer + b.trust + b.foreign }

// retailNet derives the retail net OI from the OI identity: for any
// futures contract total_long_oi ≡ total_short_oi, hence
// retail net = -(institutional net).
func (b taifexFuturesBreakdown) retailNet() int64 { return -b.instNet() }

// parseTAIFEXFuturesCSV reads the 三大法人區分各期貨契約 CSV and
// returns the per-身份別 breakdown of 多空未平倉口數淨額 (the upstream
// per-row signed long-minus-short OI delta). Other columns are ignored.
//
// Rows are attributed by the 身份別 column, not by position, and the
// party name is matched EXACTLY against the alias table below —
// canonical names plus the historical 外資 → 外資及陸資 rename.
//
// Deliberately not prefix matching: futContractsDateDown emits exactly
// three unsuffixed rows. Suffixed parties (自營商(避險) and friends)
// belong to the OPTIONS file (callsAndPutsDate), not this one. Under a
// prefix rule a category subtotal emitted alongside sub-rows would be
// summed into the same field and silently double-count, corrupting the
// retail derivation — the exact failure the fail-loud default below
// exists to prevent. If TAIFEX ever does start suffixing rows here,
// the default branch fires and we handle the new layout deliberately.
// Do not "helpfully" restore prefix matching.
//
// Anything the table doesn't cover is an error rather than a silent
// drop, as is a repeated party: both would leave the breakdown
// incomplete or inflated, and the retail derivation assumes it is
// exactly complete.
//
// Returns a zero breakdown with no error when the CSV has only a
// header row (rare — upstream usually emits 3 institutional rows even
// on quiet days, possibly all zero). Returns an error on malformed
// input.
func parseTAIFEXFuturesCSV(body []byte) (taifexFuturesBreakdown, error) {
	var out taifexFuturesBreakdown
	r := csv.NewReader(bytes.NewReader(body))
	r.FieldsPerRecord = -1 // tolerate trailing empty fields
	rows, err := r.ReadAll()
	if err != nil {
		return taifexFuturesBreakdown{}, err
	}
	if len(rows) < 2 {
		return out, nil
	}

	// Pin both columns by header match. The upstream column order has
	// been stable for years; we still resolve by name so a future
	// TAIFEX layout shuffle fails loudly rather than silently
	// mis-attributing OIs.
	partyCol, err := findColumn(rows[0], "身份別")
	if err != nil {
		return taifexFuturesBreakdown{}, err
	}
	netCol, err := findColumn(rows[0], "多空未平倉口數淨額")
	if err != nil {
		return taifexFuturesBreakdown{}, err
	}

	seen := make(map[string]bool, 3)
	for _, row := range rows[1:] {
		// Padding lines (all-empty cells, e.g. a trailing ",,,,") carry
		// no data and predate this parser — the old sum-everything code
		// added their 0 harmlessly. Skip them before the fail-loud
		// default so they don't surface as `unknown 身份別 ""`.
		if blankRow(row) {
			continue
		}
		// A short non-blank row means the layout changed under us.
		// Errors rather than `continue`: silently dropping a row that
		// carries real data corrupts the retail derivation exactly the
		// way an unattributed party would, so both take the same
		// fail-loud posture.
		if len(row) <= netCol || len(row) <= partyCol {
			return taifexFuturesBreakdown{}, fmt.Errorf("taifex: row has %d columns, want > %d", len(row), max(netCol, partyCol))
		}
		v, perr := parseInt(row[netCol])
		if perr != nil {
			return taifexFuturesBreakdown{}, fmt.Errorf("taifex: parse net col: %w", perr)
		}

		// The alias table. canonical is the dedupe key so the two
		// foreign spellings can't both land in one CSV unnoticed.
		var (
			canonical string
			target    *int64
		)
		switch party := strings.TrimSpace(row[partyCol]); party {
		case "自營商":
			canonical, target = "自營商", &out.dealer
		case "投信":
			canonical, target = "投信", &out.trust
		case "外資", "外資及陸資":
			canonical, target = "外資及陸資", &out.foreign
		default:
			return taifexFuturesBreakdown{}, fmt.Errorf("taifex: unknown 身份別 %q", party)
		}
		if seen[canonical] {
			return taifexFuturesBreakdown{}, fmt.Errorf("taifex: duplicate 身份別 %q", canonical)
		}
		seen[canonical] = true
		*target = v
	}
	return out, nil
}

// blankRow reports whether every cell in row is empty after trimming —
// a padding line rather than a data row.
func blankRow(row []string) bool {
	for _, cell := range row {
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
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
