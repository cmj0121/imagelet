package i18n_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/internal/i18n"
	"github.com/cmj0121/imagelet/middleware"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func TestLocaleString(t *testing.T) {
	cases := []struct {
		loc  i18n.Locale
		want string
	}{
		{i18n.LocaleEN, "en"},
		{i18n.LocaleZhTW, "zh-TW"},
		{i18n.LocaleZhCN, "zh-CN"},
		{i18n.Locale(99), "en"}, // unknown collapses to en
	}
	for _, tc := range cases {
		if got := tc.loc.String(); got != tc.want {
			t.Errorf("Locale(%d).String() = %q, want %q", int(tc.loc), got, tc.want)
		}
	}
}

func TestForReturnsCatalog(t *testing.T) {
	cases := []struct {
		loc i18n.Locale
		// All three locales must populate the OHLCOpen field — H4
		// will replace zh-TW / zh-CN values with real translations,
		// but the field must never be left blank.
		nonEmpty string
	}{
		{i18n.LocaleEN, "en"},
		{i18n.LocaleZhTW, "zh-TW"},
		{i18n.LocaleZhCN, "zh-CN"},
	}
	for _, tc := range cases {
		cat := i18n.For(tc.loc)
		if cat.OHLCOpen == "" {
			t.Errorf("For(%s).OHLCOpen is empty", tc.nonEmpty)
		}
		if len(cat.Weekdays) != 7 || cat.Weekdays[0] == "" {
			t.Errorf("For(%s).Weekdays missing/empty: %v", tc.nonEmpty, cat.Weekdays)
		}
	}
}

func TestForUnknownFallsBackToEnglish(t *testing.T) {
	want := i18n.For(i18n.LocaleEN)
	got := i18n.For(i18n.Locale(99))
	if got.OHLCOpen != want.OHLCOpen || got.YearProgressLabel != want.YearProgressLabel {
		t.Errorf("For(unknown) did not fall back to LocaleEN: got %+v want %+v", got, want)
	}
}

func TestGetLocaleWithoutMiddleware(t *testing.T) {
	// Direct call without LocaleDetector installed must not panic and
	// must return LocaleEN.
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if loc := i18n.GetLocale(c); loc != i18n.LocaleEN {
		t.Errorf("GetLocale(no middleware) = %s, want en", loc)
	}
	if s := i18n.LocaleString(c); s != "en" {
		t.Errorf("LocaleString(no middleware) = %q, want \"en\"", s)
	}
}

// makeRouter assembles the production-shaped middleware chain
// (RegionDetector → LocaleDetector) plus an echo handler that exposes
// the resolved locale for assertion.
func makeRouter() *gin.Engine {
	r := gin.New()
	r.Use(middleware.RegionDetector())
	r.Use(i18n.LocaleDetector())
	r.GET("/echo", func(c *gin.Context) {
		c.Header("X-Locale", i18n.GetLocale(c).String())
		c.Status(http.StatusOK)
	})
	return r
}

func send(r *gin.Engine, url string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, url, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestLocaleDetectorPrecedence(t *testing.T) {
	// ?lang= wins over CF-IPCountry wins over the en default. Each
	// row tightens the inputs from the row above to confirm the
	// expected step picked the locale. Accept-Language is no longer
	// consulted — pin that explicitly with cases that set AL but
	// expect CF-IPCountry to decide.
	cases := []struct {
		name       string
		url        string
		headers    map[string]string
		wantLocale string
	}{
		{
			name:       "default-en (no inputs)",
			url:        "/echo",
			wantLocale: "en",
		},
		{
			name:       "cf-ipcountry-tw",
			url:        "/echo",
			headers:    map[string]string{"CF-IPCountry": "TW"},
			wantLocale: "zh-TW",
		},
		{
			name:       "cf-ipcountry-hk",
			url:        "/echo",
			headers:    map[string]string{"CF-IPCountry": "HK"},
			wantLocale: "zh-TW",
		},
		{
			name:       "cf-ipcountry-cn",
			url:        "/echo",
			headers:    map[string]string{"CF-IPCountry": "CN"},
			wantLocale: "zh-CN",
		},
		{
			name:       "cf-ipcountry-sg",
			url:        "/echo",
			headers:    map[string]string{"CF-IPCountry": "SG"},
			wantLocale: "zh-CN",
		},
		{
			name:       "cf-ipcountry-jp-falls-to-en (deferred ja)",
			url:        "/echo",
			headers:    map[string]string{"CF-IPCountry": "JP"},
			wantLocale: "en",
		},
		{
			name:       "accept-language-ignored-zh-tw",
			url:        "/echo",
			headers:    map[string]string{"Accept-Language": "zh-TW"},
			wantLocale: "en", // AL no longer routes; no CF, no ?lang= → en
		},
		{
			name:       "accept-language-ignored-en-with-cf-tw",
			url:        "/echo",
			headers:    map[string]string{"CF-IPCountry": "TW", "Accept-Language": "en-US,en;q=0.9"},
			wantLocale: "zh-TW", // CF-IPCountry decides; en AL is ignored — the bug fix
		},
		{
			name:       "accept-language-ignored-zh-cn-with-cf-tw",
			url:        "/echo",
			headers:    map[string]string{"CF-IPCountry": "TW", "Accept-Language": "zh-CN"},
			wantLocale: "zh-TW", // CF wins; AL doesn't override geo
		},
		{
			name:       "lang-query-overrides-everything",
			url:        "/echo?lang=zh-CN",
			headers:    map[string]string{"CF-IPCountry": "TW", "Accept-Language": "zh-TW"},
			wantLocale: "zh-CN",
		},
		{
			name:       "lang-query-bare-zh-maps-to-tw",
			url:        "/echo?lang=zh",
			wantLocale: "zh-TW",
		},
		{
			name:       "lang-query-zh-Hant",
			url:        "/echo?lang=zh-Hant",
			wantLocale: "zh-TW",
		},
		{
			name:       "lang-query-zh-Hans",
			url:        "/echo?lang=zh-Hans",
			wantLocale: "zh-CN",
		},
		{
			name:       "lang-query-mixed-case",
			url:        "/echo?lang=ZH-tw",
			wantLocale: "zh-TW",
		},
		{
			name:       "lang-query-en-explicit",
			url:        "/echo?lang=en",
			headers:    map[string]string{"CF-IPCountry": "TW"},
			wantLocale: "en",
		},
		{
			name:       "lang-query-ja-ignored-falls-through",
			url:        "/echo?lang=ja",
			headers:    map[string]string{"CF-IPCountry": "TW"},
			wantLocale: "zh-TW", // ?lang=ja is unrecognized; CF-IPCountry decides
		},
		{
			name:       "lang-query-jp-ignored-falls-through",
			url:        "/echo?lang=jp",
			headers:    map[string]string{"CF-IPCountry": "CN"},
			wantLocale: "zh-CN",
		},
		{
			name:       "lang-query-garbage-ignored",
			url:        "/echo?lang=zzz",
			headers:    map[string]string{"CF-IPCountry": "HK"},
			wantLocale: "zh-TW",
		},
	}

	r := makeRouter()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := send(r, tc.url, tc.headers)
			if got := w.Header().Get("X-Locale"); got != tc.wantLocale {
				t.Errorf("X-Locale = %q, want %q", got, tc.wantLocale)
			}
		})
	}
}

func TestLocaleDetector_NoVaryAcceptLanguage(t *testing.T) {
	// Accept-Language no longer influences the resolved locale, so
	// LocaleDetector must NEVER append "Accept-Language" to the
	// response Vary header. Regression guard against re-introducing
	// AL-driven fragmentation of the CDN cache.
	cases := []struct {
		name    string
		url     string
		headers map[string]string
	}{
		{name: "default-en", url: "/echo"},
		{name: "cf-ipcountry-only", url: "/echo", headers: map[string]string{"CF-IPCountry": "TW"}},
		{name: "lang-query", url: "/echo?lang=zh-TW"},
		{name: "accept-language-set", url: "/echo", headers: map[string]string{"Accept-Language": "zh-TW"}},
		{name: "accept-language-and-cf", url: "/echo", headers: map[string]string{"CF-IPCountry": "TW", "Accept-Language": "en"}},
	}

	r := makeRouter()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := send(r, tc.url, tc.headers)
			for _, v := range w.Header().Values("Vary") {
				if v == "Accept-Language" {
					t.Errorf("Vary contains Accept-Language; want absent (full Vary=%v)", w.Header().Values("Vary"))
				}
			}
		})
	}
}

func TestLocaleDetector_XImageletLocaleHeader(t *testing.T) {
	// X-Imagelet-Locale must be set unconditionally on every response
	// so operators can verify staged-rollout behavior with `curl -I`
	// and access logs can extract locale without re-parsing the
	// request. Value space is the bounded enum from Locale.String() —
	// no PII.
	cases := []struct {
		name    string
		url     string
		headers map[string]string
		want    string
	}{
		{
			name: "default-en",
			url:  "/echo",
			want: "en",
		},
		{
			name:    "cf-ipcountry-tw",
			url:     "/echo",
			headers: map[string]string{"CF-IPCountry": "TW"},
			want:    "zh-TW",
		},
		{
			name:    "cf-ipcountry-cn",
			url:     "/echo",
			headers: map[string]string{"CF-IPCountry": "CN"},
			want:    "zh-CN",
		},
		{
			name:    "accept-language-ignored",
			url:     "/echo",
			headers: map[string]string{"Accept-Language": "zh-TW"},
			want:    "en", // AL no longer routes
		},
		{
			name: "lang-query-zh-tw",
			url:  "/echo?lang=zh-TW",
			want: "zh-TW",
		},
		{
			name: "lang-query-zh-cn",
			url:  "/echo?lang=zh-CN",
			want: "zh-CN",
		},
		{
			name: "lang-query-en-explicit",
			url:  "/echo?lang=en",
			want: "en",
		},
	}

	r := makeRouter()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := send(r, tc.url, tc.headers)
			if got := w.Header().Get("X-Imagelet-Locale"); got != tc.want {
				t.Errorf("X-Imagelet-Locale = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseLocaleQueryEdgeCases(t *testing.T) {
	// Internal helper not exported; exercise via LocaleDetector
	// since that's the only call site. These cases pin behavior for
	// the values the design enumerates explicitly.
	cases := []struct {
		input string
		want  string
	}{
		{"", "en"},    // empty → fall through to default
		{"   ", "en"}, // whitespace → empty after trim
		{"en", "en"},
		{"EN", "en"},
		{"zh", "zh-TW"},
		{"zh-CN", "zh-CN"},
		{"zh-cn", "zh-CN"},
		{"zh-Hans", "zh-CN"},
		{"zh-hans", "zh-CN"},
		{"zh-TW", "zh-TW"},
		{"zh-tw", "zh-TW"},
		{"zh-Hant", "zh-TW"},
		{"zh-hant", "zh-TW"},
		{"ZH-TW", "zh-TW"},
		{"ja", "en"}, // deferred — falls through, no other steps run in this test
		{"jp", "en"},
		{"fr", "en"},
		{"garbage", "en"},
	}
	r := gin.New()
	r.Use(i18n.LocaleDetector()) // no RegionDetector → no CF-IPCountry fallback in scope
	r.GET("/echo", func(c *gin.Context) {
		c.Header("X-Locale", i18n.GetLocale(c).String())
		c.Status(http.StatusOK)
	})
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			// url.QueryEscape so whitespace and odd chars don't trip
			// httptest.NewRequest's strict URL parser.
			req := httptest.NewRequest(http.MethodGet, "/echo?lang="+url.QueryEscape(tc.input), nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if got := w.Header().Get("X-Locale"); got != tc.want {
				t.Errorf("?lang=%q resolved to %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
