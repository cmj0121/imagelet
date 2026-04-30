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
	if got := i18n.AcceptLanguageInfluenced(c); got {
		t.Errorf("AcceptLanguageInfluenced(no middleware) = true, want false")
	}
	if s := i18n.LocaleString(c); s != "en" {
		t.Errorf("LocaleString(no middleware) = %q, want \"en\"", s)
	}
}

// makeRouter assembles the production-shaped middleware chain
// (RegionDetector → LocaleDetector) plus an echo handler that exposes
// the resolved locale and AL-influenced flag for assertion.
func makeRouter() *gin.Engine {
	r := gin.New()
	r.Use(middleware.RegionDetector())
	r.Use(i18n.LocaleDetector())
	r.GET("/echo", func(c *gin.Context) {
		c.Header("X-Locale", i18n.GetLocale(c).String())
		if i18n.AcceptLanguageInfluenced(c) {
			c.Header("X-AL-Influenced", "yes")
		}
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
	// ?lang= wins over Accept-Language wins over CF-IPCountry wins
	// over the en default. Each row tightens the inputs from the row
	// above to confirm the expected step picked the locale.
	cases := []struct {
		name             string
		url              string
		headers          map[string]string
		wantLocale       string
		wantALInfluenced bool
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
			name:             "accept-language-zh-tw",
			url:              "/echo",
			headers:          map[string]string{"Accept-Language": "zh-TW"},
			wantLocale:       "zh-TW",
			wantALInfluenced: true,
		},
		{
			name:             "accept-language-zh-cn",
			url:              "/echo",
			headers:          map[string]string{"Accept-Language": "zh-CN"},
			wantLocale:       "zh-CN",
			wantALInfluenced: true,
		},
		{
			name:             "accept-language-en",
			url:              "/echo",
			headers:          map[string]string{"Accept-Language": "en"},
			wantLocale:       "en",
			wantALInfluenced: true,
		},
		{
			name:             "accept-language-overrides-cf-ipcountry",
			url:              "/echo",
			headers:          map[string]string{"CF-IPCountry": "TW", "Accept-Language": "en"},
			wantLocale:       "en",
			wantALInfluenced: true,
		},
		{
			name:       "accept-language-ja-falls-through-to-cf",
			url:        "/echo",
			headers:    map[string]string{"CF-IPCountry": "TW", "Accept-Language": "ja"},
			wantLocale: "zh-TW", // matcher returns no-confidence on ja, falls to CF=TW
		},
		{
			name:       "lang-query-overrides-everything",
			url:        "/echo?lang=zh-CN",
			headers:    map[string]string{"CF-IPCountry": "TW", "Accept-Language": "zh-TW"},
			wantLocale: "zh-CN",
		},
		{
			name:       "lang-query-bare-zh-maps-to-cn",
			url:        "/echo?lang=zh",
			wantLocale: "zh-CN",
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
		{
			name:       "accept-language-empty-falls-through",
			url:        "/echo",
			headers:    map[string]string{"CF-IPCountry": "TW", "Accept-Language": ""},
			wantLocale: "zh-TW",
		},
		{
			name:       "accept-language-malformed-falls-through",
			url:        "/echo",
			headers:    map[string]string{"CF-IPCountry": "TW", "Accept-Language": "zh-TW;q=invalid;;"},
			wantLocale: "zh-TW",
		},
		{
			name:             "accept-language-multi-tag",
			url:              "/echo",
			headers:          map[string]string{"Accept-Language": "fr-FR, ja;q=0.7, zh-TW;q=0.5"},
			wantLocale:       "zh-TW",
			wantALInfluenced: true,
		},
	}

	r := makeRouter()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := send(r, tc.url, tc.headers)
			if got := w.Header().Get("X-Locale"); got != tc.wantLocale {
				t.Errorf("X-Locale = %q, want %q", got, tc.wantLocale)
			}
			gotInfluenced := w.Header().Get("X-AL-Influenced") == "yes"
			if gotInfluenced != tc.wantALInfluenced {
				t.Errorf("AL-influenced = %v, want %v", gotInfluenced, tc.wantALInfluenced)
			}
		})
	}
}

func TestLocaleDetectorVaryHeader(t *testing.T) {
	// Vary: Accept-Language must appear iff the response was
	// influenced by Accept-Language. Skipped for ?lang= (URL itself
	// differentiates) and for CF-IPCountry-only paths (geo-derived
	// default isn't AL-driven).
	cases := []struct {
		name     string
		url      string
		headers  map[string]string
		wantVary bool
	}{
		{
			name:     "default-en — no Vary",
			url:      "/echo",
			wantVary: false,
		},
		{
			name:     "cf-ipcountry-only — no Vary",
			url:      "/echo",
			headers:  map[string]string{"CF-IPCountry": "TW"},
			wantVary: false,
		},
		{
			name:     "lang-query — no Vary",
			url:      "/echo?lang=zh-TW",
			wantVary: false,
		},
		{
			name:     "lang-query-overrides-AL — no Vary",
			url:      "/echo?lang=en",
			headers:  map[string]string{"Accept-Language": "zh-TW"},
			wantVary: false,
		},
		{
			name:     "accept-language-only — Vary set",
			url:      "/echo",
			headers:  map[string]string{"Accept-Language": "zh-TW"},
			wantVary: true,
		},
		{
			name:     "accept-language-overrides-cf — Vary set",
			url:      "/echo",
			headers:  map[string]string{"CF-IPCountry": "TW", "Accept-Language": "en"},
			wantVary: true,
		},
	}

	r := makeRouter()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := send(r, tc.url, tc.headers)
			vary := w.Header().Values("Vary")
			has := false
			for _, v := range vary {
				if v == "Accept-Language" {
					has = true
					break
				}
			}
			if has != tc.wantVary {
				t.Errorf("Vary contains Accept-Language = %v, want %v (full Vary=%v)", has, tc.wantVary, vary)
			}
		})
	}
}

func TestLocaleDetector_VaryHeaderSurvivesPanic(t *testing.T) {
	// If a downstream handler panics, gin.Recovery catches it and
	// ships a 500. The Vary: Accept-Language hook must still fire on
	// the recovered response — wrapping it in a defer (placed BEFORE
	// c.Next()) is what makes that work. Regression guard for the
	// post-c.Next() form, where panic skipped the Vary write entirely.
	r := gin.New()
	r.Use(gin.Recovery()) // installed first so it can catch downstream panics
	r.Use(middleware.RegionDetector())
	r.Use(i18n.LocaleDetector())
	r.GET("/boom", func(c *gin.Context) {
		panic("simulated handler failure")
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	req.Header.Set("Accept-Language", "zh-TW")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 from gin.Recovery, got %d", w.Code)
	}
	vary := w.Header().Values("Vary")
	has := false
	for _, v := range vary {
		if v == "Accept-Language" {
			has = true
			break
		}
	}
	if !has {
		t.Errorf("Vary: Accept-Language missing on recovered 500 response (full Vary=%v)", vary)
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
			name:    "accept-language-zh-tw",
			url:     "/echo",
			headers: map[string]string{"Accept-Language": "zh-TW"},
			want:    "zh-TW",
		},
		{
			name:    "accept-language-zh-cn",
			url:     "/echo",
			headers: map[string]string{"Accept-Language": "zh-CN"},
			want:    "zh-CN",
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
		{"zh", "zh-CN"},
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
