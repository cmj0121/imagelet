package now_test

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestNowHTMLIncludesOGTags(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/now", nil)
	req.Host = "imagelet.example.com"
	req.Header.Set("User-Agent", "Mozilla/5.0")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<meta property="og:type" content="website">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<meta property="og:image" content="http://imagelet.example.com/now?format=png">`,
		`<meta property="og:image:type" content="image/png">`,
		`<meta property="og:url" content="http://imagelet.example.com/now">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}

	// Title must mention HH:MM. Match the og:title attribute value.
	titleRe := regexp.MustCompile(`property="og:title" content="imagelet · \d{2}:\d{2}"`)
	if !titleRe.MatchString(body) {
		t.Errorf("og:title not matching imagelet · HH:MM; body:\n%s", body)
	}

	// Description should carry the date + UTC offset + weekday.
	descRe := regexp.MustCompile(`property="og:description" content="\d{4}-\d{2}-\d{2} UTC[+-]\d+ · [A-Z][a-z]+"`)
	if !descRe.MatchString(body) {
		t.Errorf("og:description not in expected format; body:\n%s", body)
	}
}

func TestNowHTMLOGHonorsDateOverride(t *testing.T) {
	// /now?date=2012-02-02 should produce og:url that preserves the
	// query (since that's what makes the URL canonical), and og:image
	// must merge it with format=png so social cards render the raster.
	gin.SetMode(gin.TestMode)
	r := newRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/now?date=2012-02-02", nil)
	req.Host = "example.com"
	req.Header.Set("User-Agent", "Mozilla/5.0")
	r.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `content="http://example.com/now?date=2012-02-02"`) {
		t.Errorf("og:url should preserve the date= query; body:\n%s", body)
	}
	if !strings.Contains(body, `content="http://example.com/now?date=2012-02-02&amp;format=png"`) {
		t.Errorf("og:image should merge date= with format=png; body:\n%s", body)
	}
	if !strings.Contains(body, `2012-02-02`) {
		t.Errorf("description should reflect overridden date; body:\n%s", body)
	}
}

func TestNowSVGOmitsOGTags(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/now?format=svg", nil)
	r.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "og:title") {
		t.Errorf("raw SVG should not carry og:title")
	}
}
