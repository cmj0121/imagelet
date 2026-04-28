package stock_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestStockHTMLIncludesOGTags(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{q: freshQuote()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/stock", nil)
	req.Host = "imagelet.example.com"
	req.Header.Set("User-Agent", "Mozilla/5.0")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<meta property="og:type" content="website">`,
		`<meta property="og:site_name" content="imagelet">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<meta property="og:image" content="http://imagelet.example.com/stock?format=svg">`,
		`<meta property="og:url" content="http://imagelet.example.com/stock">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}

	// og:title should reflect the symbol — non-TW UA defaults to ^GSPC,
	// possibly with a name suffix. Just assert the symbol is present.
	titleRe := regexp.MustCompile(`property="og:title" content="\^GSPC[^"]*"`)
	if !titleRe.MatchString(body) {
		t.Errorf("og:title should mention ^GSPC; body:\n%s", body)
	}

	// og:description should look like the on-figure caption: arrow + percent + price.
	descRe := regexp.MustCompile(`property="og:description" content="[▲▼] [+-]\d+\.\d{2}%  [\d,]+\.\d{2} [A-Z]+"`)
	if !descRe.MatchString(body) {
		t.Errorf("og:description not matching expected format; body:\n%s", body)
	}
}

func TestStockSymbolRouteOGTagsCarrySymbol(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{q: freshQuote()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/stock/2330.TW", nil)
	req.Host = "imagelet.example.com"
	req.Header.Set("User-Agent", "Mozilla/5.0")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `content="http://imagelet.example.com/stock/2330.TW?format=svg"`) {
		t.Errorf("og:image should target the per-symbol path; body:\n%s", body)
	}
	if !strings.Contains(body, `content="http://imagelet.example.com/stock/2330.TW"`) {
		t.Errorf("og:url should be the per-symbol path; body:\n%s", body)
	}
	if !strings.Contains(body, `property="og:title" content="2330.TW`) {
		t.Errorf("og:title should mention 2330.TW; body:\n%s", body)
	}
}

func TestStockSVGOmitsOGTags(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(fakeProvider{q: freshQuote()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/stock?format=svg", nil)
	r.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "og:title") {
		t.Errorf("raw SVG should not carry og:title")
	}
}
