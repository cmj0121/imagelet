package stock_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/service/stock/quote"
)

// TestLocaleGoldenMatrix is H4's regression net: one representative
// page per format (ASCII / SVG / HTML) per locale (en / zh-TW / zh-CN).
// PNG is exercised separately as a 200-OK + non-empty + correct
// content-type probe per locale (binary; substrings don't apply).
//
// The risk H4 mitigates is "translation drift between catalog and
// renderer" — a row label that flows through the catalog in one
// locale but accidentally hardcodes another. These cells pin the
// wire output per locale so a regression on any of catalog / service
// / render layers surfaces here.
//
// 2330.TW under closed market with full TWSE enrichment exercises
// every catalog field that has a real (non-empty) value across all
// three locales — OHLC labels, MA-bar labels, TWSE-block labels,
// and unit suffixes.
func TestLocaleGoldenMatrix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// ^TWII (the index view) exercises the market-wide BFI82U
	// positioning rows + the 漲跌家數 breadth row + the 信用餘額
	// margin row, which together cover every TWSE-block label that
	// has a non-empty zh-TW / zh-CN value. A per-stock view (e.g.
	// 2330.TW) hides the breadth row, leaving 漲跌家數 unexercised.
	q := freshQuote()
	q.Symbol = "^TWII"
	q.Currency = "TWD"
	q.IsClosed = true
	q.MA5 = 575.20
	q.MA10 = 568.40
	q.Last = 583.00

	cases := []struct {
		name string
		// langQuery sets ?lang= explicitly so each test self-documents
		// its locale. CF-IPCountry stays unset for en, so the
		// negotiation short-circuits at step 1 (?lang=) and the country
		// fallback never influences anything.
		langQuery string
		// wantOHLCOpen is the locale-specific Open-row prefix. en uses
		// "O:", zh-TW "開:", zh-CN "开:".
		wantOHLCOpen string
		// wantTWSE lists locale-specific TWSE labels that MUST appear.
		// Empty for en (TWSE rows are stripped under en).
		wantTWSE []string
		// bannedTWSE lists locale-specific labels that must NOT appear.
		// en bans CJK labels entirely; zh-TW bans simplified variants;
		// zh-CN bans traditional variants.
		bannedTWSE []string
	}{
		{
			name:         "en",
			langQuery:    "en",
			wantOHLCOpen: "O: 4,460.00",
			// Catalog values are empty for en; the showTWSEEnrichment
			// gate strips the rows entirely. Pin both 漲跌家數 and
			// 涨跌家数 absent so neither script leaks into en output.
			bannedTWSE: []string{"漲跌家數", "涨跌家数", "外資籌碼", "外资筹码", "信用餘額", "信用余额"},
		},
		{
			name:         "zh-TW",
			langQuery:    "zh-TW",
			wantOHLCOpen: "開: 4,460.00",
			wantTWSE: []string{
				"漲跌家數", "外資籌碼", "投信籌碼", "自營籌碼", "合計籌碼",
				"信用餘額", "融資", "融券",
			},
			// Simplified variants must not leak into the zh-TW surface.
			bannedTWSE: []string{"涨跌家数", "外资筹码", "信用余额", "开:"},
		},
		{
			name:         "zh-CN",
			langQuery:    "zh-CN",
			wantOHLCOpen: "开: 4,460.00",
			wantTWSE: []string{
				"涨跌家数", "外资筹码", "投信筹码", "自营筹码", "合计筹码",
				"信用余额", "融资", "融券",
			},
			// Traditional variants must not leak into the zh-CN surface.
			bannedTWSE: []string{"漲跌家數", "外資籌碼", "信用餘額", "開:"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("ascii", func(t *testing.T) {
				body := fetchLocale(t, q, tc.langQuery, "ascii")
				assertLocaleSurface(t, body, tc.wantOHLCOpen, tc.wantTWSE, tc.bannedTWSE)
			})
			t.Run("svg", func(t *testing.T) {
				body := fetchLocale(t, q, tc.langQuery, "svg")
				assertLocaleSurface(t, body, tc.wantOHLCOpen, tc.wantTWSE, tc.bannedTWSE)
			})
			t.Run("html", func(t *testing.T) {
				body := fetchLocale(t, q, tc.langQuery, "html")
				assertLocaleSurface(t, body, tc.wantOHLCOpen, tc.wantTWSE, tc.bannedTWSE)
			})
			t.Run("png", func(t *testing.T) {
				rec := fetchLocaleRaw(t, q, tc.langQuery, "png")
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200", rec.Code)
				}
				if got := rec.Header().Get("Content-Type"); got != "image/png" {
					t.Errorf("Content-Type = %q, want image/png", got)
				}
				if !bytes.HasPrefix(rec.Body.Bytes(), pngMagic) {
					t.Errorf("body missing PNG magic bytes")
				}
				if rec.Body.Len() < 1000 {
					t.Errorf("PNG body suspiciously small: %d bytes", rec.Body.Len())
				}
			})
		})
	}
}

// fetchLocale issues GET /stock/2330.TW?lang=<lang>&format=<format>
// against a TW-enriched router with the canned closed-market freshTW
// fixture, and returns the response body as a string. The format query
// overrides the User-Agent default so the same call works for ascii /
// svg / html alike.
func fetchLocale(t *testing.T, q quote.Quote, lang, format string) string {
	t.Helper()
	rec := fetchLocaleRaw(t, q, lang, format)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (lang=%s format=%s body=%q)", rec.Code, lang, format, rec.Body.String())
	}
	return rec.Body.String()
}

func fetchLocaleRaw(t *testing.T, q quote.Quote, lang, format string) *httptest.ResponseRecorder {
	t.Helper()
	r := newRouterWithTWSE(fakeProvider{q: q}, fakeTWSE{d: freshTW()})
	// Use the region-routed entry (?region=TW) rather than /stock/^TWII
	// to avoid percent-encoding the caret in the URL. The market-wide
	// fixture exercises 漲跌家數 + the BFI82U positioning rows, which
	// the per-stock route hides.
	url := "/stock?region=TW&format=" + format
	if lang != "" {
		url += "&lang=" + lang
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	// curl UA so the format= override (not the UA-driven HTML default)
	// determines the wire format, even when format=html.
	req.Header.Set("User-Agent", "curl/8.4.0")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestBareZhResolvesToZhTW asserts that ?lang=zh maps to zh-TW (Traditional
// Chinese), not zh-CN. This is intentional: the deployment targets a
// TW-market audience, so bare zh should default to Traditional. A regression
// here would cause the zh-CN Simplified labels to appear instead.
func TestBareZhResolvesToZhTW(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "^TWII"
	q.Currency = "TWD"
	q.IsClosed = true

	body := fetchLocale(t, q, "zh", "ascii")
	// zh-TW uses Traditional script; zh-CN uses Simplified.
	// "開:" is Traditional; "开:" is Simplified.
	if !strings.Contains(body, "開:") {
		t.Errorf("?lang=zh: body missing Traditional OHLC label '開:'; got Simplified or en\n--- body ---\n%s", body)
	}
	if strings.Contains(body, "开:") {
		t.Errorf("?lang=zh: body contains Simplified OHLC label '开:'; bare zh should resolve to zh-TW\n--- body ---\n%s", body)
	}
}

// TestCFIPCountryLocaleResolution checks that the CF-IPCountry header drives
// locale when no ?lang= is given: TW → zh-TW, CN → zh-CN, US → en.
func TestCFIPCountryLocaleResolution(t *testing.T) {
	gin.SetMode(gin.TestMode)
	q := freshQuote()
	q.Symbol = "^TWII"
	q.Currency = "TWD"
	q.IsClosed = true

	cases := []struct {
		country    string
		wantLabel  string
		bannedLabel string
	}{
		{"TW", "開:", "开:"},
		{"HK", "開:", "开:"},
		{"CN", "开:", "開:"},
		{"SG", "开:", "開:"},
	}
	for _, tc := range cases {
		t.Run(tc.country, func(t *testing.T) {
			r := newRouterWithTWSE(fakeProvider{q: q}, fakeTWSE{d: freshTW()})
			req := httptest.NewRequest(http.MethodGet, "/stock?region=TW&format=ascii", nil)
			req.Header.Set("User-Agent", "curl/8.4.0")
			req.Header.Set("CF-IPCountry", tc.country)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("CF-IPCountry=%s: status = %d, want 200", tc.country, rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, tc.wantLabel) {
				t.Errorf("CF-IPCountry=%s: body missing %q\n--- body ---\n%s", tc.country, tc.wantLabel, body)
			}
			if strings.Contains(body, tc.bannedLabel) {
				t.Errorf("CF-IPCountry=%s: body contains banned %q (wrong locale)\n--- body ---\n%s", tc.country, tc.bannedLabel, body)
			}
		})
	}
}

func assertLocaleSurface(t *testing.T, body, wantOHLCOpen string, wantTWSE, bannedTWSE []string) {
	t.Helper()
	if !strings.Contains(body, wantOHLCOpen) {
		t.Errorf("body missing OHLC open row %q\n--- body ---\n%s", wantOHLCOpen, body)
	}
	for _, want := range wantTWSE {
		if !strings.Contains(body, want) {
			t.Errorf("body missing TWSE label %q\n--- body ---\n%s", want, body)
		}
	}
	for _, banned := range bannedTWSE {
		if strings.Contains(body, banned) {
			t.Errorf("body contains banned label %q (cross-locale leak)\n--- body ---\n%s", banned, body)
		}
	}
}
