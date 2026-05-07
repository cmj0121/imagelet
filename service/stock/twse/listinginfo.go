package twse

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// defaultListingInfoEndpoint is the TWSE OpenAPI v1 t187ap03_L dataset
// — the 上市公司基本資料 (listed-company basic info) bulk file. JSON
// array of flat objects with Chinese keys; one row per listed
// company, ~1,084 rows on a normal day. Includes 產業別 (industry
// classification, NUMERIC code 01..91) and 上市日期 (listing date,
// YYYYMMDD).
//
// Verified 2026-05-07 against live: 1,084 entries, 33 distinct
// industry codes. ETFs are NOT in this list — t187ap03_L is for
// COMPANIES, so an ETF lookup falls through to ErrUnavailable
// gracefully. OTC stocks are at TPEx's separate endpoint and out
// of scope for this provider.
const defaultListingInfoEndpoint = "https://openapi.twse.com.tw/v1/opendata/t187ap03_L"

// twseIndustryNames maps the 2-character TWSE industry classification
// code to its Chinese name. Codes 01-38 + 91 are the universe TWSE
// has assigned over the years — older codes 07, 13, 19 are vestigial
// (folded into successor categories) and absent from current data;
// 32-34 are primarily OTC and may not appear in the TWSE dump.
//
// Names are TW Traditional only — zh-CN viewers see Trad sector
// names (same compromise as upstream-provided 證券名稱 like 鴻海).
// A future Trad→Simp dictionary lives in i18n if/when needed.
//
// Codes not in the map fall through to TWSEIndustryUnknown so the
// renderer can emit a neutral placeholder rather than raw "27" digits.
var twseIndustryNames = map[string]string{
	"01": "水泥工業",
	"02": "食品工業",
	"03": "塑膠工業",
	"04": "紡織纖維",
	"05": "電機機械",
	"06": "電器電纜",
	"08": "玻璃陶瓷",
	"09": "造紙工業",
	"10": "鋼鐵工業",
	"11": "橡膠工業",
	"12": "汽車工業",
	"14": "建材營造",
	"15": "航運業",
	"16": "觀光餐旅",
	"17": "金融保險業",
	"18": "貿易百貨",
	"20": "其他",
	"21": "化學工業",
	"22": "生技醫療業",
	"23": "油電燃氣業",
	"24": "半導體業",
	"25": "電腦及週邊設備業",
	"26": "光電業",
	"27": "通信網路業",
	"28": "電子零組件業",
	"29": "電子通路業",
	"30": "資訊服務業",
	"31": "其他電子業",
	"32": "文化創意業",
	"33": "農業科技業",
	"34": "電子商務",
	"35": "綠能環保",
	"36": "數位雲端",
	"37": "運動休閒",
	"38": "居家生活",
	"91": "存託憑證",
}

// industryNameOf returns the TW Traditional name for a TWSE industry
// code. Falls through to "" for unmapped codes — the renderer treats
// "" as "no sector tag known" and skips that segment.
func industryNameOf(code string) string {
	return twseIndustryNames[strings.TrimSpace(code)]
}

// ListingInfo captures the per-company basic info from t187ap03_L.
// Empty (zero StockID) when the upstream had no row for the stock id
// (delisted / pre-listing / OTC / ETF / non-TW); the renderer gates
// on Has() to omit the row gracefully.
type ListingInfo struct {
	StockID      string    // 公司代號
	Name         string    // 公司簡稱
	IndustryCode string    // 產業別 — 2-char numeric code (e.g. "24")
	IndustryName string    // resolved via industryNameOf — empty for unmapped codes
	ListingDate  time.Time // 上市日期 — YYYYMMDD parsed
}

// Has reports whether the upstream produced a row for this stock.
// StockID == "" means "not found in dump" — every entry in the bulk
// has a non-empty 公司代號, so a zero StockID is the renderer's
// signal to skip the row.
func (l ListingInfo) Has() bool { return l.StockID != "" }

// ListingInfoProvider is implemented by anything that can answer
// per-stock company basic info queries. asOf is informational —
// t187ap03_L has no per-date variation, the upstream stamps the
// publication day on every row.
type ListingInfoProvider interface {
	GetListingInfo(ctx context.Context, stockID string, asOf time.Time) (ListingInfo, error)
}

// ListingInfoExactProvider is the cache-friendly twin: pulls the live
// snapshot once per call with no walk-back, so a caching wrapper
// holds the parsed-once map and skips the network for the rest of
// the cache TTL. found=false reserved for empty upstream replies.
type ListingInfoExactProvider interface {
	FetchListingInfoExact(ctx context.Context, asOf time.Time) (ListingInfoDump, bool, error)
}

// ListingInfoDump is the parsed per-company map from one t187ap03_L
// snapshot. Cached as a single per-process map; the renderer pulls
// the per-stock row on demand via GetListingInfo.
type ListingInfoDump struct {
	AsOf time.Time
	Rows map[string]ListingInfo
}

// SetListingInfoEndpoint overrides the upstream URL used by
// FetchListingInfoExact. Tests use this to point at httptest.Server.
// Empty disables the provider (returns ErrUnavailable).
func (p *HTTPProvider) SetListingInfoEndpoint(endpoint string) {
	p.listingInfo = endpoint
}

// FetchListingInfoExact pulls the t187ap03_L snapshot and returns the
// parsed per-stock map. Like the other OpenAPI providers, the dump
// has no per-date variation in the URL — the caching wrapper keys
// on a single sentinel.
func (p *HTTPProvider) FetchListingInfoExact(ctx context.Context, _ time.Time) (ListingInfoDump, bool, error) {
	if p.listingInfo == "" {
		return ListingInfoDump{}, false, ErrUnavailable
	}
	body, err := p.fetch(ctx, p.listingInfo)
	if err != nil {
		return ListingInfoDump{}, false, err
	}

	var raw []map[string]string
	if err := json.Unmarshal(body, &raw); err != nil {
		return ListingInfoDump{}, false, fmt.Errorf("twse: listinginfo unmarshal: %w", err)
	}
	if len(raw) == 0 {
		return ListingInfoDump{}, false, nil
	}

	asOf := parseTWSEDate(strings.TrimSpace(raw[0]["出表日期"]))
	out := ListingInfoDump{AsOf: asOf, Rows: make(map[string]ListingInfo, len(raw))}
	for _, row := range raw {
		stockID := strings.TrimSpace(row["公司代號"])
		if stockID == "" {
			continue
		}
		code := strings.TrimSpace(row["產業別"])
		out.Rows[stockID] = ListingInfo{
			StockID:      stockID,
			Name:         strings.TrimSpace(row["公司簡稱"]),
			IndustryCode: code,
			IndustryName: industryNameOf(code),
			ListingDate:  parseTWSEDate(strings.TrimSpace(row["上市日期"])),
		}
	}
	return out, true, nil
}

// GetListingInfo returns per-stock listing info from an uncached
// HTTPProvider. The cache wrapper in cached_walkback.go is the
// production path; this bare implementation exists for tests and
// for symmetry with the other providers.
func (p *HTTPProvider) GetListingInfo(ctx context.Context, stockID string, asOf time.Time) (ListingInfo, error) {
	dump, found, err := p.FetchListingInfoExact(ctx, asOf)
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
