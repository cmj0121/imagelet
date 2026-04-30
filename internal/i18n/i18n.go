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
	"golang.org/x/text/language"

	"github.com/cmj0121/imagelet/middleware"
)

// localeKey and alInfluencedKey are the gin-context keys under which
// LocaleDetector stores the resolved Locale and the
// "Accept-Language influenced the choice" boolean. The "imagelet."
// prefix namespaces them so they cannot collide with keys set by other
// middlewares running on the same engine.
const (
	localeKey       = "imagelet.i18n.locale"
	alInfluencedKey = "imagelet.i18n.al-influenced"
)

// Locale is the canonical identifier for a supported imagelet locale.
// LocaleEN is the zero value and the safe fallback for unknown inputs.
type Locale int

const (
	// LocaleEN is the English (en) catalog. Used when no negotiation
	// step picks a CJK locale; also the fallback for ?lang=ja (deferred).
	LocaleEN Locale = iota
	// LocaleZhTW is the Traditional Chinese (zh-TW) catalog. Default
	// for CF-IPCountry ∈ {TW, HK, MO}; matches Accept-Language zh-Hant.
	LocaleZhTW
	// LocaleZhCN is the Simplified Chinese (zh-CN) catalog. Default
	// for CF-IPCountry ∈ {CN, SG}; matches Accept-Language zh-Hans and
	// bare zh.
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

// AcceptLanguageInfluenced reports whether the resolved Locale was
// chosen via the Accept-Language header (rather than ?lang= or
// CF-IPCountry). LocaleDetector reads this flag in its post-c.Next()
// hook to decide whether to append "Accept-Language" to the Vary
// header. Exported primarily for tests.
func AcceptLanguageInfluenced(c *gin.Context) bool {
	v, ok := c.Get(alInfluencedKey)
	if !ok {
		return false
	}
	flag, ok := v.(bool)
	return ok && flag
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

// matcher resolves an Accept-Language header value to one of the
// supported locales. Built once at init from the canonical script
// tags (zh-Hant / zh-Hans) so Accept-Language: zh-CN routes to
// LocaleZhCN and Accept-Language: zh-TW routes to LocaleZhTW without
// either accidentally collapsing onto the wrong script.
//
// matcherTags and matcherIndexToLocale are kept in lock-step: tag at
// index i maps to locale at the same index in matcherIndexToLocale.
var (
	matcherTags = []language.Tag{
		language.English,            // 0 → LocaleEN
		language.TraditionalChinese, // 1 → LocaleZhTW (zh-Hant)
		language.SimplifiedChinese,  // 2 → LocaleZhCN (zh-Hans)
	}
	matcherIndexToLocale = [...]Locale{LocaleEN, LocaleZhTW, LocaleZhCN}
	matcher              = language.NewMatcher(matcherTags)
)

// LocaleDetector returns a gin middleware that resolves the request's
// Locale and stashes it on the gin context. Must be installed AFTER
// middleware.RegionDetector — the CF-IPCountry-based fallback step
// reads what RegionDetector wrote.
//
// Resolution order (first match wins):
//
//  1. ?lang= query parameter — explicit user override. Recognized
//     forms: en, zh, zh-TW, zh-Hant, zh-CN, zh-Hans (case-insensitive).
//     Bare "zh" maps to zh-CN per CLDR convention. Unrecognized values
//     (including ja, jp) are ignored and the chain proceeds.
//  2. Accept-Language header via golang.org/x/text/language.NewMatcher.
//     Records al-influenced=true on success; this is the ONLY step
//     that flips the flag.
//  3. CF-IPCountry → countryToLocale lookup. TW/HK/MO → zh-TW;
//     CN/SG → zh-CN; everything else falls through.
//  4. LocaleEN.
//
// After c.Next() returns, the middleware appends "Accept-Language" to
// the response's Vary header IFF al-influenced was set. The Vary write
// is wrapped in a defer so that even if a downstream handler panics
// (and gin.Recovery converts it to a 500), the cache-correctness
// contract still holds — the recovered response carries Vary just like
// a normal one. Owning Vary here (rather than per-handler) keeps the
// contract in one place — new routes and existing routes alike Just Work.
//
// X-Imagelet-Locale is set unconditionally BEFORE c.Next() so handlers
// can observe (and, in a hypothetical future, override) it. The header
// value is bounded to the {"en","zh-TW","zh-CN"} enum from
// Locale.String() — no PII, safe to log and to expose to operators
// running `curl -I` against staged-rollout deployments.
func LocaleDetector() gin.HandlerFunc {
	return func(c *gin.Context) {
		loc, alInfluenced := resolveLocale(c)
		c.Set(localeKey, loc)
		c.Set(alInfluencedKey, alInfluenced)
		c.Writer.Header().Set("X-Imagelet-Locale", loc.String())
		if e := log.Debug(); e.Enabled() {
			e.Stringer("locale", loc).Bool("al_influenced", alInfluenced).Msg("locale resolved")
		}

		defer func() {
			if alInfluenced {
				c.Writer.Header().Add("Vary", "Accept-Language")
			}
		}()

		c.Next()
	}
}

// resolveLocale runs the four-step negotiation. Split out from
// LocaleDetector so unit tests can exercise the resolution logic
// without spinning up a router.
func resolveLocale(c *gin.Context) (Locale, bool) {
	if loc, ok := parseLocaleQuery(c.Query("lang")); ok {
		return loc, false
	}
	if al := c.GetHeader("Accept-Language"); al != "" {
		if loc, matched := matchAcceptLanguage(al); matched {
			return loc, true
		}
	}
	if loc, ok := countryToLocale[middleware.GetCountry(c)]; ok {
		return loc, false
	}
	return LocaleEN, false
}

// parseLocaleQuery interprets a ?lang= value. Recognized forms below;
// anything else (including "ja", "jp", "fr", garbage) returns
// (LocaleEN, false) so the caller can fall through to the next
// negotiation step rather than silently defaulting.
//
// Bare "zh" maps to LocaleZhCN per CLDR convention — the Unicode
// project treats simplified as the script-default for ambiguous "zh"
// inputs. Users wanting traditional must say "zh-TW" or "zh-Hant".
func parseLocaleQuery(s string) (Locale, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return LocaleEN, false
	case "en":
		return LocaleEN, true
	case "zh", "zh-cn", "zh-hans":
		return LocaleZhCN, true
	case "zh-tw", "zh-hant":
		return LocaleZhTW, true
	}
	return LocaleEN, false
}

// matchAcceptLanguage runs the Accept-Language header through the
// package matcher. Returns matched=false on parse error, empty input,
// or no-confidence match (ja, fr, *, etc. with no zh/en signal) — so
// the caller can fall through to CF-IPCountry rather than silently
// resolving to LocaleEN.
func matchAcceptLanguage(al string) (Locale, bool) {
	tags, _, err := language.ParseAcceptLanguage(al)
	if err != nil || len(tags) == 0 {
		return LocaleEN, false
	}
	_, idx, conf := matcher.Match(tags...)
	if conf == language.No {
		return LocaleEN, false
	}
	if idx < 0 || idx >= len(matcherIndexToLocale) {
		return LocaleEN, false
	}
	return matcherIndexToLocale[idx], true
}

// For returns the Catalog for the requested locale. Unknown locales
// fall back to the English catalog so a corrupt value can't render
// blank labels.
func For(loc Locale) Catalog {
	switch loc {
	case LocaleZhTW:
		return catalogZhTW
	case LocaleZhCN:
		return catalogZhCN
	}
	return catalogEN
}
