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
)

// defaultTAIFEXPCREndpoint downloads the daily Put/Call ratio CSV for
// TAIEX options. POST form (queryStartDate, queryEndDate); response is
// ISO-8859 / Latin-1 (no high bytes — header is plain ASCII Chinese
// Big5-decoded server-side already, so the body is single-byte safe to
// read as UTF-8 / ASCII). Header columns:
//
//	0 日期  1 賣權成交量  2 買權成交量  3 買賣權成交量比率%
//	4 賣權未平倉量  5 買權未平倉量  6 買賣權未平倉量比率%
//
// The renderer surfaces column 6 (OI PCR) as the headline — it's the
// "what positioning looks like at the close" gauge, more meaningful
// than the day-only volume PCR.
const defaultTAIFEXPCREndpoint = "https://www.taifex.com.tw/cht/3/pcRatioDown"

// defaultTAIFEXVIXURLTemplate is the monthly VIX dump URL pattern.
// TAIFEX publishes one .txt per calendar month at this path; the
// filename carries the YYYYMM stem. Format is tab-separated:
//
//	YYYYMMDD<tab>HHMMSS<tab>collapse_vix<tab>1min_avg_vix
//
// The renderer reads the latest row at or before asOf — column 2
// (collapse VIX, the close-of-session figure). The %s placeholder
// is the YYYYMM month stem.
const defaultTAIFEXVIXURLTemplate = "https://www.taifex.com.tw/file/taifex/Dailydownload/vix/log2data/%snew.txt"

// OptionsPCR captures the daily Put/Call ratio for TAIEX options —
// the most-watched options sentiment gauge in the TW market. Both
// volume and OI ratios are surfaced; the renderer picks one (OI as
// the headline, volume as alt). Values are percentages (175.80 means
// PCR = 175.8% = puts dominate calls 1.758×).
type OptionsPCR struct {
	VolumeRatio float64   // 買賣權成交量比率% (intraday flow)
	OIRatio     float64   // 買賣權未平倉量比率% (standing positioning — headline)
	AsOf        time.Time // trading date the upstream supplied
}

// Has reports whether the PCR fetch yielded a usable reading. OI
// ratio 0 in a real session is impossible (calls and puts both have
// non-zero OI on a normal day), so it doubles as the freshness flag.
func (p OptionsPCR) Has() bool { return p.OIRatio > 0 }

// VIX captures the daily Taiwan VIX index — the TX-options-derived
// implied-volatility gauge, computed by TAIFEX using the CBOE VIX
// formula adapted to TXO. Higher = more fear in the options market;
// readings ~30+ historically mark stressed sessions.
type VIX struct {
	Value float64   // close-of-session VIX
	AsOf  time.Time // trading date the upstream supplied
}

// Has reports whether the VIX fetch yielded a usable reading.
func (v VIX) Has() bool { return v.Value > 0 }

// OptionsPCRProvider is an optional extension for fetching the daily
// TAIEX-options Put/Call ratio. Same lookback semantics as the TAIFEX
// futures fetcher — POST per-day with a `查無資料` HTML alert page on
// empty days, walks back until a real CSV lands.
type OptionsPCRProvider interface {
	GetOptionsPCR(ctx context.Context, asOf time.Time) (OptionsPCR, error)
}

// VIXProvider is an optional extension for fetching the daily Taiwan
// VIX. The upstream is monthly: one .txt file per calendar month
// containing every trading day's VIX. The fetcher walks back from
// asOf within the same month, then steps to the previous month if
// the lookback hasn't found data yet.
type VIXProvider interface {
	GetVIX(ctx context.Context, asOf time.Time) (VIX, error)
}

// SetTAIFEXMarketEndpoints overrides the URLs used by GetOptionsPCR /
// GetVIX so tests can route to a fixture server. Production callers
// should rely on New() which wires the live defaults; pass empty
// strings to disable either path. vixURLTemplate must contain exactly
// one %s placeholder for the YYYYMM month stem.
func (p *HTTPProvider) SetTAIFEXMarketEndpoints(pcrURL, vixURLTemplate string) {
	p.taifexPCR = pcrURL
	p.taifexVIX = vixURLTemplate
}

// GetOptionsPCR fetches the daily TAIEX-options Put/Call ratio at or
// before asOf. Same lookback / no-data-page semantics as the TAIFEX
// futures fetcher. Returns ErrUnavailable when the endpoint is unset
// or when the lookback exhausts.
func (p *HTTPProvider) GetOptionsPCR(ctx context.Context, asOf time.Time) (OptionsPCR, error) {
	if p.taifexPCR == "" {
		return OptionsPCR{}, ErrUnavailable
	}
	if asOf.IsZero() {
		asOf = time.Now()
	}
	day := asOf
	for i := 0; i < maxLookbackDays; i++ {
		pcr, ok, err := p.fetchPCROnce(ctx, day)
		if err != nil {
			return OptionsPCR{}, err
		}
		if ok {
			return pcr, nil
		}
		day = day.AddDate(0, 0, -1)
	}
	return OptionsPCR{}, ErrUnavailable
}

// fetchPCROnce issues one POST against the PCR endpoint for a single
// date. Returns (pcr, true, nil) on a usable CSV, (zero, false, nil)
// on the no-data alert HTML, or (zero, false, err) on transport /
// parse failures.
func (p *HTTPProvider) fetchPCROnce(ctx context.Context, day time.Time) (OptionsPCR, bool, error) {
	dateStr := day.Format("2006/01/02")
	form := url.Values{
		"queryStartDate": []string{dateStr},
		"queryEndDate":   []string{dateStr},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.taifexPCR, strings.NewReader(form.Encode()))
	if err != nil {
		return OptionsPCR{}, false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; imagelet/0.3)")

	resp, err := p.client.Do(req)
	if err != nil {
		return OptionsPCR{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return OptionsPCR{}, false, fmt.Errorf("taifex pcr: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return OptionsPCR{}, false, err
	}
	if bytes.HasPrefix(bytes.TrimLeft(body, " \t\r\n"), []byte("<")) {
		return OptionsPCR{}, false, nil
	}

	pcr, err := parsePCRCSV(body)
	if err != nil {
		return OptionsPCR{}, false, err
	}
	pcr.AsOf = day
	return pcr, true, nil
}

// parsePCRCSV reads the PCR CSV (single data row) and returns the
// parsed ratios. The CSV body is plain ASCII (no Chinese in data
// cells — header line is in BIG5 which we skip past via the index
// shift, so no transcoder is needed for the values).
func parsePCRCSV(body []byte) (OptionsPCR, error) {
	r := csv.NewReader(bytes.NewReader(body))
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return OptionsPCR{}, err
	}
	if len(rows) < 2 {
		return OptionsPCR{}, fmt.Errorf("taifex pcr: empty body")
	}
	row := rows[1]
	if len(row) < 7 {
		return OptionsPCR{}, fmt.Errorf("taifex pcr: short row %d cols", len(row))
	}
	vol, _ := strconv.ParseFloat(strings.TrimSpace(row[3]), 64)
	oi, _ := strconv.ParseFloat(strings.TrimSpace(row[6]), 64)
	return OptionsPCR{VolumeRatio: vol, OIRatio: oi}, nil
}

// GetVIX fetches the daily Taiwan VIX at or before asOf. Walks back
// day-by-day within the same monthly file, stepping to the previous
// month's file when the in-month lookback exhausts. Returns
// ErrUnavailable when the endpoint template is unset or no published
// row at or before asOf exists in the last two months.
func (p *HTTPProvider) GetVIX(ctx context.Context, asOf time.Time) (VIX, error) {
	if p.taifexVIX == "" {
		return VIX{}, ErrUnavailable
	}
	if asOf.IsZero() {
		asOf = time.Now()
	}
	asOf = asOf.In(twLoc)

	// Try this month, then last month — covers the start-of-month
	// edge where asOf is the 1st and the latest published row sits
	// in the previous month's file.
	for monthsBack := 0; monthsBack < 2; monthsBack++ {
		month := time.Date(asOf.Year(), asOf.Month(), 1, 0, 0, 0, 0, twLoc).AddDate(0, -monthsBack, 0)
		v, ok, err := p.fetchVIXMonth(ctx, month, asOf)
		if err != nil {
			return VIX{}, err
		}
		if ok {
			return v, nil
		}
	}
	return VIX{}, ErrUnavailable
}

// fetchVIXMonth fetches one calendar month's VIX dump and picks the
// latest row whose date is ≤ asOf. Returns (zero, false, nil) when
// the month file 404s or has no qualifying row; (zero, false, err)
// for other transport failures.
func (p *HTTPProvider) fetchVIXMonth(ctx context.Context, month, asOf time.Time) (VIX, bool, error) {
	stem := month.Format("200601")
	endpoint := fmt.Sprintf(p.taifexVIX, stem)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return VIX{}, false, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; imagelet/0.3)")

	resp, err := p.client.Do(req)
	if err != nil {
		return VIX{}, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return VIX{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return VIX{}, false, fmt.Errorf("taifex vix: %s", resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return VIX{}, false, err
	}
	return parseVIXMonth(body, asOf)
}

// parseVIXMonth scans the monthly VIX dump and returns the latest row
// (by date) whose date is ≤ asOf. Format is tab-separated lines:
//
//	YYYYMMDD<tab>HHMMSS<tab>collapse_vix<tab>1min_avg_vix
//
// The first two lines are a header (column titles + dashed underline)
// and are skipped. We don't transcode the header from BIG5 because we
// only read the data lines, which are pure ASCII.
func parseVIXMonth(body []byte, asOf time.Time) (VIX, bool, error) {
	asOfYMD := asOf.Format("20060102")
	var best VIX
	var found bool
	for _, raw := range bytes.Split(body, []byte("\n")) {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		fields := strings.Fields(string(line))
		if len(fields) < 3 {
			continue
		}
		date := fields[0]
		if len(date) != 8 || !isAllDigit(date) {
			continue // header / dashed line
		}
		if date > asOfYMD {
			continue
		}
		val, err := strconv.ParseFloat(fields[2], 64)
		if err != nil {
			continue
		}
		// File is chronological so the last qualifying row is the
		// latest; keep updating until we exhaust the month.
		ts, perr := time.ParseInLocation("20060102", date, twLoc)
		if perr != nil {
			continue
		}
		best = VIX{Value: val, AsOf: ts.Add(12 * time.Hour)}
		found = true
	}
	return best, found, nil
}

// isAllDigit reports whether every byte of s is an ASCII digit. Used
// to discriminate VIX data lines from header / dashed-line preamble
// without committing to a regex.
func isAllDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}
