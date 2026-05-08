package twse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// defaultRevenueListedEndpoint is TWSE OpenAPI t187ap05_L — monthly
// operating revenue per listed company. Chinese-keyed JSON; one row
// per listed equity per month, ~1,073 rows on a normal publish.
//
// **YoY and MoM are pre-computed by the upstream** — the renderer
// just emits them. Field map (verified 2026-05-08, 2330 sample):
//   - 資料年月: ROC year + month (e.g. "11503" = 2026-03)
//   - 公司代號: stock id
//   - 產業別: human name (NOT numeric code; differs from t187ap03_L!)
//   - 營業收入-當月營收: current month revenue (NTD 千元)
//   - 營業收入-去年同月增減(%): YoY pct (e.g. 45.19)
//   - 營業收入-上月比較增減(%): MoM pct
//
// Verified 2026-05-08 against 2330: 當月營收 415,191,699 (NTD千元)
// = 4,151億 NTD. YoY = 45.19%, MoM = 30.70%.
const defaultRevenueListedEndpoint = "https://openapi.twse.com.tw/v1/opendata/t187ap05_L"

// defaultRevenueOTCEndpoint is the TPEx parallel: mopsfin_t187ap05_O.
// **Identical Chinese-keyed schema** to TWSE; 881 OTC companies as of
// 2026-05-08. Closes the OTC asymmetry gap that earlier revenue/yield
// rows had — both listed types now render the monthly-revenue row.
const defaultRevenueOTCEndpoint = "https://www.tpex.org.tw/openapi/v1/mopsfin_t187ap05_O"

// Revenue captures one company's monthly revenue snapshot. Empty
// (zero StockID) when the upstream had no row — rare on either
// endpoint since both publish the full universe each month.
//
// CurrentTWD is in raw NTD (the upstream gives 千元 = thousands;
// this struct multiplies × 1000 for consumer convenience).
// PrevYearMoMPct and PrevMonthMoMPct are upstream-computed
// percentages (e.g. 45.19 means +45.19% YoY).
type Revenue struct {
	StockID         string    // 公司代號
	Name            string    // 公司名稱
	Industry        string    // 產業別 — already a human name on this endpoint
	YearMonth       string    // 資料年月 — ROC YYYMM e.g. "11503" = 2026-03
	CurrentTWD      int64     // 營業收入-當月營收 (raw NTD)
	YoYPct          float64   // 營業收入-去年同月增減(%) — already a pct
	MoMPct          float64   // 營業收入-上月比較增減(%) — already a pct
	AsOf            time.Time // 出表日期 — publication date
}

// Has reports whether the upstream produced a row for this stock.
func (r Revenue) Has() bool { return r.StockID != "" && r.CurrentTWD > 0 }

// RevenueProvider is implemented by anything that can answer per-stock
// monthly-revenue queries. The handler routes to either the TWSE-listed
// or OTC variant based on the symbol's listing type.
type RevenueProvider interface {
	GetRevenue(ctx context.Context, stockID string, asOf time.Time) (Revenue, error)
}

// RevenueExactProvider is the cache-friendly twin.
type RevenueExactProvider interface {
	FetchRevenueExact(ctx context.Context, asOf time.Time) (RevenueDump, bool, error)
}

// OTCRevenueProvider mirrors RevenueProvider but routes to TPEx —
// kept distinct so the type-assertion-based wiring in NewCached
// recognises both.
type OTCRevenueProvider interface {
	GetOTCRevenue(ctx context.Context, stockID string, asOf time.Time) (Revenue, error)
}

// OTCRevenueExactProvider is the cache-friendly twin for OTC.
type OTCRevenueExactProvider interface {
	FetchOTCRevenueExact(ctx context.Context, asOf time.Time) (RevenueDump, bool, error)
}

// RevenueDump is the parsed per-stock monthly-revenue map.
type RevenueDump struct {
	AsOf time.Time
	Rows map[string]Revenue
}

// SetRevenueListedEndpoint / SetRevenueOTCEndpoint override upstream
// URLs for tests.
func (p *HTTPProvider) SetRevenueListedEndpoint(endpoint string) {
	p.revenueListed = endpoint
}

func (p *HTTPProvider) SetRevenueOTCEndpoint(endpoint string) {
	p.revenueOTC = endpoint
}

// FetchRevenueExact pulls the TWSE t187ap05_L monthly revenue snapshot.
func (p *HTTPProvider) FetchRevenueExact(ctx context.Context, _ time.Time) (RevenueDump, bool, error) {
	if p.revenueListed == "" {
		return RevenueDump{}, false, ErrUnavailable
	}
	return parseRevenue(p, p.revenueListed, ctx)
}

// FetchOTCRevenueExact pulls the TPEx mopsfin_t187ap05_O equivalent.
// Schema is identical to TWSE — same parser handles both.
func (p *HTTPProvider) FetchOTCRevenueExact(ctx context.Context, _ time.Time) (RevenueDump, bool, error) {
	if p.revenueOTC == "" {
		return RevenueDump{}, false, ErrUnavailable
	}
	return parseRevenue(p, p.revenueOTC, ctx)
}

// parseRevenue is the schema-shared parser for both TWSE and TPEx
// monthly-revenue endpoints. Both expose Chinese-keyed JSON arrays
// with the same column layout (verified 2026-05-08).
func parseRevenue(p *HTTPProvider, endpoint string, ctx context.Context) (RevenueDump, bool, error) {
	body, err := p.fetch(ctx, endpoint)
	if err != nil {
		return RevenueDump{}, false, err
	}

	var raw []map[string]string
	if err := json.Unmarshal(body, &raw); err != nil {
		return RevenueDump{}, false, fmt.Errorf("twse: revenue unmarshal: %w", err)
	}
	if len(raw) == 0 {
		return RevenueDump{}, false, nil
	}

	asOf := parseTWSEDate(strings.TrimSpace(raw[0]["出表日期"]))
	out := RevenueDump{AsOf: asOf, Rows: make(map[string]Revenue, len(raw))}
	for _, row := range raw {
		stockID := strings.TrimSpace(row["公司代號"])
		if stockID == "" {
			continue
		}
		// Upstream gives 千元; multiply × 1000 for raw NTD.
		currentThousands := parseTWSENumber(row["營業收入-當月營收"])
		out.Rows[stockID] = Revenue{
			StockID:    stockID,
			Name:       strings.TrimSpace(row["公司名稱"]),
			Industry:   strings.TrimSpace(row["產業別"]),
			YearMonth:  strings.TrimSpace(row["資料年月"]),
			CurrentTWD: currentThousands * 1000,
			YoYPct:     parseTWSEFloat(row["營業收入-去年同月增減(%)"]),
			MoMPct:     parseTWSEFloat(row["營業收入-上月比較增減(%)"]),
			AsOf:       asOf,
		}
	}
	return out, true, nil
}

// GetRevenue returns the per-stock TWSE-listed revenue from an
// uncached HTTPProvider.
func (p *HTTPProvider) GetRevenue(ctx context.Context, stockID string, asOf time.Time) (Revenue, error) {
	dump, found, err := p.FetchRevenueExact(ctx, asOf)
	if err != nil {
		return Revenue{}, err
	}
	if !found {
		return Revenue{}, ErrUnavailable
	}
	r, ok := dump.Rows[strings.TrimSpace(stockID)]
	if !ok {
		return Revenue{}, ErrUnavailable
	}
	return r, nil
}

// GetOTCRevenue returns the per-stock OTC revenue.
func (p *HTTPProvider) GetOTCRevenue(ctx context.Context, stockID string, asOf time.Time) (Revenue, error) {
	dump, found, err := p.FetchOTCRevenueExact(ctx, asOf)
	if err != nil {
		return Revenue{}, err
	}
	if !found {
		return Revenue{}, ErrUnavailable
	}
	r, ok := dump.Rows[strings.TrimSpace(stockID)]
	if !ok {
		return Revenue{}, ErrUnavailable
	}
	return r, nil
}
