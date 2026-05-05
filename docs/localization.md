# Localization

Imagelet's user-facing output (OHLC labels, MA captions, year-progress
caption, keyboard-help dialog, Open Graph meta, weekday names, TWSE
enrichment rows) localizes per request. Three locales ship today:
English (`en`), Traditional Chinese (`zh-TW`), and Simplified Chinese
(`zh-CN`).

## Supported locales

| Locale              | BCP-47 tag(s) accepted (via `?lang=`) | Country defaults              | Script      |
| ------------------- | ------------------------------------- | ----------------------------- | ----------- |
| English             | `en`                                  | (anything not below; default) | Latin       |
| Traditional Chinese | `zh-TW`, `zh-Hant`, `zh`              | TW, HK, MO                    | Traditional |
| Simplified Chinese  | `zh-CN`, `zh-Hans`                    | CN, SG                        | Simplified  |

Bare `?lang=zh` resolves to `zh-TW`. imagelet's primary CJK audience
reads traditional script (TW market focus), so a deliberate
`?lang=zh` override lands on the deployment's native script as the
least-surprising outcome. Callers wanting simplified must say
`?lang=zh-CN` or `?lang=zh-Hans` explicitly.

Note: the `Accept-Language` matcher (step 2 below) still follows
CLDR — `Accept-Language: zh` resolves to `zh-CN`. The divergence is
intentional: `?lang=` is explicit user intent, where the deployment's
audience overrides script-default convention; `Accept-Language` is
browser-driven and where CLDR conventions are widely understood.

HK and MO default to `zh-TW` because traditional script is the
prevailing written form there. Cantonese-Mandarin terminology drift
(financial-news vocabulary) is real but defensible at this granularity;
`?lang=zh-CN` is the explicit override.

## Negotiation order

The `LocaleDetector` middleware resolves the request locale via:

1. **`?lang=` query parameter** — explicit user override. Recognized
   forms: `en`, `zh`, `zh-TW`, `zh-Hant`, `zh-CN`, `zh-Hans`,
   case-insensitive. Unrecognized values (including `ja`, `jp`, `fr`,
   garbage) are ignored and the chain proceeds.
2. **`Accept-Language` header** — parsed through
   `golang.org/x/text/language.NewMatcher` over
   `[English, TraditionalChinese, SimplifiedChinese]`. The matcher's
   confidence-aware routing keeps `zh-CN` → `zh-Hans` and `zh-TW` →
   `zh-Hant` from collapsing onto the wrong script. No-confidence
   matches (e.g. `Accept-Language: ja`, `*`, malformed) fall through.
3. **`CF-IPCountry` header** — set by Cloudflare or whichever upstream
   fronts the deployment. Two-letter ISO 3166-1 alpha-2 country codes:
   TW / HK / MO → `zh-TW`; CN / SG → `zh-CN`; everything else,
   including JP and US, falls through.
4. **Fallback** — `en`.

`?lang=ja` is treated like an unrecognized value: the chain falls
through to step 2, then step 3, and likely lands on `en`. Falling
through to `zh-TW` would be wrong — Japanese readers prefer English
over the wrong CJK script.

## Examples

```bash
# Explicit override
curl https://imagelet.example.com/stock/2330.TW?lang=zh-TW
curl https://imagelet.example.com/stock/2330.TW?lang=zh-CN
curl https://imagelet.example.com/stock/2330.TW?lang=en

# Browser-style negotiation
curl -H 'Accept-Language: zh-TW,zh;q=0.9,en;q=0.5' \
     https://imagelet.example.com/stock/2330.TW
curl -H 'Accept-Language: zh-CN' \
     https://imagelet.example.com/stock/2330.TW

# CDN geo default
curl -H 'CF-IPCountry: TW' https://imagelet.example.com/stock/2330.TW
curl -H 'CF-IPCountry: CN' https://imagelet.example.com/stock/2330.TW
curl -H 'CF-IPCountry: JP' https://imagelet.example.com/stock/2330.TW   # → en
```

## What localizes

Generic visualization labels (OHLC bar prefixes, MA captions,
year-progress caption, weekday names, keyboard-help dialog) translate
across all three locales. A few tokens stay Latin or symbolic by
design:

- `M5` / `M10` MA markers — pylon's ASCII bar uses one-cell rune
  positioning, and a 2-cell CJK glyph at a marker column would
  misalign the bar.
- `↗` / `↘` / `≈` MA-trend tokens — symbolic; carries enough
  localization signal in the surrounding label.
- `▲` / `▼` direction arrows, `█` / `░` body fills, `▶` / `◀`
  saturation sentinels — pylon glyph vocabulary, locale-independent.
- `IMAGELET` / `404` / `HH:MM` banner letterforms — pylon banners are
  Latin-only ASCII art. Subtitles below the banner localize.

## TWSE enrichment policy

The TWSE-specific block (institutional flow, breadth, margin balance,
retail futures, options PCR, VIX) is gated by locale:

- `en` visitors see only the generic OHLC + MA card. The TWSE rows
  are stripped at the service layer (`service/stock.showTWSEEnrichment`)
  — several of those terms (`借券賣出當日餘額`, `融資`, `融券`,
  `外資籌碼`) lack clean English equivalents, so emitting them
  transliterated would be worse than omitting them. The TWSE upstream
  fetch is also skipped, so `en` stock requests are slightly cheaper.
- `zh-TW` visitors see the full TWSE block with traditional script
  (`漲跌家數`, `融資`, `融券`, `外資籌碼`, `億`, `萬張`, `張`).
- `zh-CN` visitors see the full TWSE block with simplified script
  (`涨跌家数`, `融资`, `融券`, `外资筹码`, `亿`, `万张`, `张`).

Row-level column padding is computed in display cells via
`mattn/go-runewidth` so CJK rows align under monospace rendering.

## Caching and Vary

`internal/htmlcache` keys HTML responses by URL plus the resolved
locale fragment (`loc=en`, `loc=zh-TW`, `loc=zh-CN`). Three locales ×
URL = at most 3× the per-URL working set; the LRU is sized 1024 to
absorb the fan-out with headroom.

`Vary: Accept-Language` is appended **only** when `Accept-Language`
actually picked the locale (step 2 of negotiation). `?lang=` overrides
skip Vary entirely — the URL itself differentiates the cache entry —
and CF-IPCountry-only matches skip it too. This keeps the Vary
contract minimal and bounds CDN cache fragmentation.

The middleware owns the Vary header in a single post-`c.Next()` hook.
Handlers do not need to remember to call it; new routes Just Work.

### Upstream concurrency under multi-locale load

The htmlcache singleflight key includes the resolved locale, so two
concurrent cold-cache requests for the same URL from visitors in
different locales spawn two parallel upstream flights instead of one.
For a hot symbol during a cold-cache window the upstream RPS multiplier
is **2–3×** baseline (en + zh-TW dominate; zh-CN is rare). The 4h
provider-level cache TTL inside `service/stock/quote/cached` bounds
the blast radius — only the first concurrent burst per locale per TTL
window pays the cost.

### Cloudflare normalization

`Vary: Accept-Language` on Cloudflare fragments the edge cache by raw
header value unless the zone is configured to normalize the header.
Without normalization, every distinct browser `Accept-Language` string
(including weight-list permutations) becomes its own cache entry, even
though our matcher would resolve dozens of variants to the same locale.

Configure the Cloudflare zone to:

- Strip `Accept-Language` from the cache key for routes that don't
  need locale-specific responses (e.g. `/healthz`, static assets), or
- Apply a Cache Rule that normalizes `Accept-Language` to the matched
  base locale (`zh-Hant`, `zh-Hans`, or `en`) before keying.

The simpler operational stance is to honor `Vary: Accept-Language` only
on `/stock`, `/stock/:symbol`, `/now`, and `/` — the routes that emit
locale-aware HTML — and to bypass the header on `/healthz` entirely.

## Implementation map

| Concern                        | File                              |
| ------------------------------ | --------------------------------- |
| Locale type, BCP-47 tag        | `internal/i18n/i18n.go`           |
| Negotiation logic              | `internal/i18n/i18n.go`           |
| English catalog                | `internal/i18n/en.go`             |
| Traditional Chinese catalog    | `internal/i18n/zhtw.go`           |
| Simplified Chinese catalog     | `internal/i18n/zhcn.go`           |
| `LocaleDetector` middleware    | `internal/i18n/i18n.go`           |
| HTML cache key locale fragment | `internal/htmlcache/htmlcache.go` |
| TWSE enrichment locale gate    | `service/stock/stock.go`          |
| CJK row-padding (runewidth)    | `service/stock/stock.go`          |
