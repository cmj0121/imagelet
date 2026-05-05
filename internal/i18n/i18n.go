// Package i18n owns imagelet's locale type, per-locale message catalog,
// and the LocaleDetector gin middleware.
//
// The package has two consumers:
//
//   - Service handlers (service/...) read the resolved Locale from the
//     gin context via GetLocale and look up label strings via For(loc).
//   - internal/htmlcache reads the canonical Locale tag (LocaleString)
//     to mix it into the cache key, so two requests differing only in
//     locale don't collide.
//
// Render (render/...) does NOT depend on this package — it accepts
// pre-translated strings from the service layer. Keeping render
// locale-agnostic preserves its testability and avoids a Locale type
// leaking into pylon-source construction.
package i18n

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/imagelet/middleware"
)

// localeKey is the gin-context key under which LocaleDetector stores
// the resolved Locale. The "imagelet." prefix namespaces it so it
// cannot collide with keys set by other middlewares running on the
// same engine.
const localeKey = "imagelet.i18n.locale"

// headerXImageletLocale is the response header LocaleDetector writes
// so operators can verify staged-rollout behavior with `curl -I` and
// access logs can extract the resolved locale without re-parsing the
// request.
const headerXImageletLocale = "X-Imagelet-Locale"

// Locale is the canonical identifier for a supported imagelet locale.
// LocaleEN is the zero value and the safe fallback for unknown inputs.
type Locale int

const (
	// LocaleEN is the English (en) catalog. Used when no negotiation
	// step picks a CJK locale; also the fallback for ?lang=ja (deferred).
	LocaleEN Locale = iota
	// LocaleZhTW is the Traditional Chinese (zh-TW) catalog. Default
	// for CF-IPCountry ∈ {TW, HK, MO}; also the target of bare
	// ?lang=zh / ?lang=zh-Hant.
	LocaleZhTW
	// LocaleZhCN is the Simplified Chinese (zh-CN) catalog. Default
	// for CF-IPCountry ∈ {CN, SG}; also the target of ?lang=zh-CN /
	// ?lang=zh-Hans.
	LocaleZhCN
)

// String returns the canonical BCP-47 tag for the locale ("en", "zh-TW",
// "zh-CN"). Used by internal/htmlcache as the locale fragment of the
// cache key — keeping the value space bounded by locale-count rather
// than by raw-Accept-Language-format-count. Unknown locales render as
// "en" so a corrupt value never silently splits the cache.
func (l Locale) String() string {
	switch l {
	case LocaleZhTW:
		return "zh-TW"
	case LocaleZhCN:
		return "zh-CN"
	}
	return "en"
}

// LocaleString is GetLocale(c).String() in one call. Exported so it
// can be passed by-name to internal/htmlcache.WithLocale, keeping
// htmlcache itself locale-agnostic at the type level.
func LocaleString(c *gin.Context) string {
	return GetLocale(c).String()
}

// GetLocale returns the Locale chosen by LocaleDetector for the current
// request. If LocaleDetector was not installed (or panicked), GetLocale
// returns LocaleEN — handlers can always call it without a nil check.
func GetLocale(c *gin.Context) Locale {
	v, ok := c.Get(localeKey)
	if !ok {
		return LocaleEN
	}
	loc, ok := v.(Locale)
	if !ok {
		return LocaleEN
	}
	return loc
}

// countryToLocale maps the visitor's ISO 3166-1 alpha-2 country code
// (resolved by middleware.RegionDetector from CF-IPCountry) to a
// default Locale. Only countries with a non-en preferred locale are
// listed; everything else (including JP and US) falls through to
// LocaleEN.
//
// JP is intentionally absent — the embedded Sarasa Mono SC font has
// incomplete kana coverage, so ja is deferred. ?lang=ja resolves to
// LocaleEN (NOT LocaleZhTW, because Japanese readers prefer English
// over the wrong CJK script).
//
// HK / MO map to zh-TW (traditional script). HK Cantonese-Mandarin
// terminology drift is real but defensible at this granularity;
// ?lang=zh-CN is the explicit override.
var countryToLocale = map[string]Locale{
	"TW": LocaleZhTW, "HK": LocaleZhTW, "MO": LocaleZhTW,
	"CN": LocaleZhCN, "SG": LocaleZhCN,
}

// LocaleDetector returns a gin middleware that resolves the request's
// Locale and stashes it on the gin context. Must be installed AFTER
// middleware.RegionDetector — the CF-IPCountry-based fallback step
// reads what RegionDetector wrote.
//
// Resolution order (first match wins):
//
//  1. ?lang= query parameter — explicit user override. Recognized
//     forms: en, zh, zh-TW, zh-Hant, zh-CN, zh-Hans (case-insensitive).
//     Bare "zh" maps to zh-TW (TW market focus). Unrecognized values
//     (including ja, jp) are ignored and the chain proceeds.
//  2. CF-IPCountry → countryToLocale lookup. TW/HK/MO → zh-TW;
//     CN/SG → zh-CN; everything else falls through.
//  3. LocaleEN.
//
// Accept-Language is intentionally NOT consulted. A TW visitor on an
// English-UI browser sends Accept-Language: en-US,en;q=0.9 — the CLDR
// matcher would lock that to LocaleEN with high confidence and never
// reach CF-IPCountry, defeating the geo-default this service is built
// around. Trusting Cloudflare's geo signal over the browser's UI
// language matches the deployment's TW-market focus; users who want a
// different locale say so explicitly via ?lang=.
//
// X-Imagelet-Locale is set unconditionally so handlers can observe
// (and, in a hypothetical future, override) it. The header value is
// bounded to the {"en","zh-TW","zh-CN"} enum from Locale.String() —
// no PII, safe to log and to expose to operators running `curl -I`
// against staged-rollout deployments.
func LocaleDetector() gin.HandlerFunc {
	return func(c *gin.Context) {
		loc := resolveLocale(c)
		c.Set(localeKey, loc)
		c.Writer.Header().Set(headerXImageletLocale, loc.String())
		if e := log.Debug(); e.Enabled() {
			e.Stringer("locale", loc).Msg("locale resolved")
		}
		c.Next()
	}
}

// resolveLocale runs the three-step negotiation. Split out from
// LocaleDetector so unit tests can exercise the resolution logic
// without spinning up a router.
func resolveLocale(c *gin.Context) Locale {
	if loc, ok := parseLocaleQuery(c.Query("lang")); ok {
		return loc
	}
	if loc, ok := countryToLocale[middleware.GetCountry(c)]; ok {
		return loc
	}
	return LocaleEN
}

// parseLocaleQuery interprets a ?lang= value. Recognized forms below;
// anything else (including "ja", "jp", "fr", garbage) returns
// (LocaleEN, false) so the caller can fall through to the next
// negotiation step rather than silently defaulting.
//
// Bare "zh" maps to LocaleZhTW. imagelet's primary CJK audience reads
// traditional script (TW market focus); a deliberate `?lang=zh`
// override lands on the deployment's native script as the
// least-surprising outcome. Callers who want simplified must say
// "zh-CN" or "zh-Hans".
func parseLocaleQuery(s string) (Locale, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return LocaleEN, false
	case "en":
		return LocaleEN, true
	case "zh-cn", "zh-hans":
		return LocaleZhCN, true
	case "zh", "zh-tw", "zh-hant":
		return LocaleZhTW, true
	}
	return LocaleEN, false
}

// For returns the Catalog for the requested locale. Unknown locales
// fall back to the English catalog so a corrupt value can't render
// blank labels. Returned by pointer to keep request-path copies cheap
// — the catalog is ~800 bytes and gets threaded through several render
// helpers per stock render.
func For(loc Locale) *Catalog {
	switch loc {
	case LocaleZhTW:
		return &catalogZhTW
	case LocaleZhCN:
		return &catalogZhCN
	}
	return &catalogEN
}

// CatalogFor is a one-call helper that resolves the request's Locale
// and returns its Catalog. Equivalent to For(GetLocale(c)).
func CatalogFor(c *gin.Context) *Catalog {
	return For(GetLocale(c))
}
