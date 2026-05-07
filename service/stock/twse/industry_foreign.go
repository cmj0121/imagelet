package twse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// defaultIndustryForeignEndpoint is the TWSE OpenAPI MI_QFIIS_cat
// dataset — daily 外資及陸資投資持股by-industry aggregate. JSON array of
// 36 rows (35 TWSE industries + ETF as a separate aggregate row),
// keyed by industry NAME (not numeric code) which matches the names
// in twseIndustryNames.
//
// Verified 2026-05-08: returns rows like
//   {"IndustryCat":"半導體業","Numbers":"43","Percentage":"43.10",...}
// Used to overlay a `業均 X%` segment onto the existing context row
// — readers can compare a stock's per-stock 外資持股 to the industry
// mean at a glance.
const defaultIndustryForeignEndpoint = "https://openapi.twse.com.tw/v1/exchangeReport/MI_QFIIS_cat"

// IndustryForeign captures one industry's aggregate foreign + PRC
// holdings ratio. Industry is the human-readable name (e.g.
// "半導體業") matching twseIndustryNames values, NOT the numeric code.
//
// Empty (zero Industry) when the upstream had no row for the queried
// industry name. Renderer gates on Has() to omit the segment.
type IndustryForeign struct {
	Industry        string  // 產業別 — e.g. "半導體業"; "ETF" for the ETF aggregate row
	Companies       int64   // count of companies in the category
	HoldingPct      float64 // foreign + PRC holdings ratio across the category
	IssuedShares    int64   // total issued shares across the category
	ForeignHeld     int64   // foreign + PRC shares held across the category
}

// Has reports whether the upstream produced a row for this industry.
func (i IndustryForeign) Has() bool { return i.Industry != "" }

// IndustryForeignProvider is implemented by anything that can answer
// industry-aggregate foreign-holdings queries. industryName matches
// the names in twseIndustryNames (e.g. "半導體業"), not the numeric
// code from t187ap03_L's 產業別 field.
type IndustryForeignProvider interface {
	GetIndustryForeign(ctx context.Context, industryName string, asOf time.Time) (IndustryForeign, error)
}

// IndustryForeignExactProvider is the cache-friendly twin: pulls the
// live snapshot once per call.
type IndustryForeignExactProvider interface {
	FetchIndustryForeignExact(ctx context.Context, asOf time.Time) (IndustryForeignDump, bool, error)
}

// IndustryForeignDump is the parsed per-industry map from one
// MI_QFIIS_cat snapshot — typically 36 rows (35 industries + ETF).
type IndustryForeignDump struct {
	AsOf time.Time
	Rows map[string]IndustryForeign
}

// SetIndustryForeignEndpoint overrides the upstream URL for tests.
func (p *HTTPProvider) SetIndustryForeignEndpoint(endpoint string) {
	p.industryForeign = endpoint
}

// FetchIndustryForeignExact pulls the MI_QFIIS_cat snapshot and
// returns the parsed per-industry map. The endpoint serves only the
// latest publication (no per-date variation), so the caching wrapper
// keys on a single sentinel.
func (p *HTTPProvider) FetchIndustryForeignExact(ctx context.Context, _ time.Time) (IndustryForeignDump, bool, error) {
	if p.industryForeign == "" {
		return IndustryForeignDump{}, false, ErrUnavailable
	}
	body, err := p.fetch(ctx, p.industryForeign)
	if err != nil {
		return IndustryForeignDump{}, false, err
	}

	var raw []map[string]string
	if err := json.Unmarshal(body, &raw); err != nil {
		return IndustryForeignDump{}, false, fmt.Errorf("twse: industry-foreign unmarshal: %w", err)
	}
	if len(raw) == 0 {
		return IndustryForeignDump{}, false, nil
	}

	out := IndustryForeignDump{Rows: make(map[string]IndustryForeign, len(raw))}
	for _, row := range raw {
		industry := strings.TrimSpace(row["IndustryCat"])
		if industry == "" {
			continue
		}
		out.Rows[industry] = IndustryForeign{
			Industry:     industry,
			Companies:    parseTWSENumber(row["Numbers"]),
			HoldingPct:   parseTWSEFloat(row["Percentage"]),
			IssuedShares: parseTWSENumber(row["ShareNumber"]),
			ForeignHeld:  parseTWSENumber(row["ForeignMainlandAreaShare"]),
		}
	}
	return out, true, nil
}

// GetIndustryForeign returns the industry-aggregate foreign-holdings
// row for the given industry name. Industry name should be one of
// the TW Trad names from twseIndustryNames (e.g. "半導體業") —
// passing the numeric code instead returns ErrUnavailable.
func (p *HTTPProvider) GetIndustryForeign(ctx context.Context, industryName string, asOf time.Time) (IndustryForeign, error) {
	dump, found, err := p.FetchIndustryForeignExact(ctx, asOf)
	if err != nil {
		return IndustryForeign{}, err
	}
	if !found {
		return IndustryForeign{}, ErrUnavailable
	}
	row, ok := dump.Rows[strings.TrimSpace(industryName)]
	if !ok {
		return IndustryForeign{}, ErrUnavailable
	}
	return row, nil
}
