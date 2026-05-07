package twse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// defaultOTCListingInfoEndpoint is the TPEx OpenAPI v1 OTC company-info
// dump (上櫃公司基本資料), the parallel to TWSE's t187ap03_L. JSON array
// of flat objects with ENGLISH keys (TPEx's OpenAPI convention; TWSE
// uses Chinese keys for the same dataset). 885 OTC companies as of
// 2026-05-08.
//
// Verified live: 6488 (環球晶) carries SecuritiesIndustryCode="24"
// (matches TWSE's 半導體業 in the existing static map). The numeric
// industry-code system is shared across TWSE and TPEx — this provider
// reuses the existing twseIndustryNames map rather than maintaining
// a parallel TPEx mapping.
const defaultOTCListingInfoEndpoint = "https://www.tpex.org.tw/openapi/v1/mopsfin_t187ap03_O"

// OTCListingInfoProvider mirrors ListingInfoProvider for OTC stocks.
// Returns the same ListingInfo value type; only the upstream schema
// differs (English vs Chinese keys), and that's hidden inside the
// fetcher.
type OTCListingInfoProvider interface {
	GetOTCListingInfo(ctx context.Context, stockID string, asOf time.Time) (ListingInfo, error)
}

// OTCListingInfoExactProvider is the cache-friendly twin: pulls the
// live TPEx snapshot once per call.
type OTCListingInfoExactProvider interface {
	FetchOTCListingInfoExact(ctx context.Context, asOf time.Time) (ListingInfoDump, bool, error)
}

// SetOTCListingInfoEndpoint overrides the upstream URL for tests.
func (p *HTTPProvider) SetOTCListingInfoEndpoint(endpoint string) {
	p.otcListingInfo = endpoint
}

// FetchOTCListingInfoExact pulls the TPEx mopsfin_t187ap03_O snapshot
// and returns the parsed per-stock map. The English-keyed schema:
//   - SecuritiesCompanyCode  (== TWSE 公司代號)
//   - CompanyAbbreviation    (== TWSE 公司簡稱)
//   - SecuritiesIndustryCode (== TWSE 產業別; same numeric system)
//   - DateOfListing          (== TWSE 上市日期; YYYYMMDD)
func (p *HTTPProvider) FetchOTCListingInfoExact(ctx context.Context, _ time.Time) (ListingInfoDump, bool, error) {
	if p.otcListingInfo == "" {
		return ListingInfoDump{}, false, ErrUnavailable
	}
	body, err := p.fetch(ctx, p.otcListingInfo)
	if err != nil {
		return ListingInfoDump{}, false, err
	}

	var raw []map[string]string
	if err := json.Unmarshal(body, &raw); err != nil {
		return ListingInfoDump{}, false, fmt.Errorf("twse: otc listinginfo unmarshal: %w", err)
	}
	if len(raw) == 0 {
		return ListingInfoDump{}, false, nil
	}

	asOf := parseTWSEDate(strings.TrimSpace(raw[0]["Date"]))
	out := ListingInfoDump{AsOf: asOf, Rows: make(map[string]ListingInfo, len(raw))}
	for _, row := range raw {
		stockID := strings.TrimSpace(row["SecuritiesCompanyCode"])
		if stockID == "" {
			continue
		}
		code := strings.TrimSpace(row["SecuritiesIndustryCode"])
		out.Rows[stockID] = ListingInfo{
			StockID:      stockID,
			Name:         strings.TrimSpace(row["CompanyAbbreviation"]),
			IndustryCode: code,
			IndustryName: industryNameOf(code),
			ListingDate:  parseTWSEDate(strings.TrimSpace(row["DateOfListing"])),
		}
	}
	return out, true, nil
}

// GetOTCListingInfo returns the OTC per-stock listing info from an
// uncached HTTPProvider. The cache wrapper (CachedOTCListingInfo) is
// the production path.
func (p *HTTPProvider) GetOTCListingInfo(ctx context.Context, stockID string, asOf time.Time) (ListingInfo, error) {
	dump, found, err := p.FetchOTCListingInfoExact(ctx, asOf)
	if err != nil {
		return ListingInfo{}, err
	}
	if !found {
		return ListingInfo{}, ErrUnavailable
	}
	info, ok := dump.Rows[strings.TrimSpace(stockID)]
	if !ok {
		return ListingInfo{}, ErrUnavailable
	}
	return info, nil
}
