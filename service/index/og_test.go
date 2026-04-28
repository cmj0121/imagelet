package index_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRootHTMLIncludesOGTags(t *testing.T) {
	r := newRouter("v1.2.3")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "imagelet.example.com"
	req.Header.Set("User-Agent", "Mozilla/5.0")

	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	wants := []string{
		`<meta property="og:type" content="website">`,
		`<meta property="og:site_name" content="imagelet">`,
		`<meta property="og:title" content="imagelet">`,
		`<meta property="og:image" content="http://imagelet.example.com/?format=png">`,
		`<meta property="og:image:type" content="image/png">`,
		`<meta property="og:url" content="http://imagelet.example.com/">`,
		`<meta name="twitter:card" content="summary_large_image">`,
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
	headIdx := strings.Index(body, "</head>")
	titleIdx := strings.Index(body, `property="og:title"`)
	if titleIdx < 0 || titleIdx > headIdx {
		t.Errorf("og:title should appear before </head>: titleIdx=%d headIdx=%d", titleIdx, headIdx)
	}
}

func TestRootHTMLOGRespectsForwardedProto(t *testing.T) {
	r := newRouter("v1.2.3")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "imagelet.example.com"
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("X-Forwarded-Proto", "https")

	r.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, `content="https://imagelet.example.com/?format=png"`) {
		t.Errorf("expected https og:image; body:\n%s", body)
	}
	if !strings.Contains(body, `content="https://imagelet.example.com/"`) {
		t.Errorf("expected https og:url; body:\n%s", body)
	}
}

func TestRootSVGOmitsOGTags(t *testing.T) {
	// OG tags belong only on the HTML wrapper; raw SVG must not carry them.
	r := newRouter("v1.2.3")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?format=svg", nil)
	r.ServeHTTP(rec, req)

	if strings.Contains(rec.Body.String(), "og:title") {
		t.Errorf("raw SVG should not carry og:title")
	}
}
