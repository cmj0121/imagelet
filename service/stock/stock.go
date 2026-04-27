// Package stock exposes the /stock service: GET /stock returns the current
// price of the caller's regional stock index (e.g. TAIEX for TW, S&P 500
// for US) as a pylon banner stacked above a multi-row data box. The wire
// format mirrors /now -- content-negotiated:
//
//   - Accept: text/pylon → raw pylon source (callers render it themselves)
//   - User-Agent contains Mozilla → image/png
//   - everything else → text/plain; charset=utf-8 (ASCII)
//
// Region resolution: ?region= query takes precedence over the country code
// stashed by middleware.RegionDetector (CF-IPCountry), which itself falls
// back to "US" when the header is missing. Unknown countries fall back to
// the S&P 500 (^GSPC). Cache-Control: public, max-age=60 is set on every
// rendered response so a CDN can absorb traffic spikes.
//
// V1 layout (post pylon-v0.2): a single outer box stacks the index-name
// header, the symbol/percent/price/date caption, the day-range and 52w-
// range progress bars, and -- only on the TW path -- a `─ ─ ─ ─` divider
// followed by 三大法人 + 融資/融券 aggregates fetched from TWSE. The TW
// block uses Chinese labels in the ASCII surface (terminal users have CJK
// fonts) and English labels in the PNG surface (pylon's PNG font has zero
// CJK coverage and would emit tofu glyphs).
package stock

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
	"github.com/cmj0121/imagelet/service/stock/quote"
	"github.com/cmj0121/imagelet/service/stock/twse"
)

// symbolByCountry maps an ISO 3166-1 alpha-2 country code to the index
// symbol the /stock service renders for visitors from that country.
// Hardcoded for v0.2 -- no per-tenant override, no DB lookup.
var symbolByCountry = map[string]string{
	"TW": "^TWII",
	"US": "^GSPC",
	"JP": "^N225",
	"HK": "^HSI",
	"GB": "^FTSE",
	"DE": "^GDAXI",
}

// indexNameBySymbol maps a Yahoo symbol to a human-readable
// `Index Name · Region` header line shown above the data caption in V1
// layout.
var indexNameBySymbol = map[string]string{
	"^TWII":  "TAIEX · Taiwan",
	"^GSPC":  "S&P 500 · United States",
	"^N225":  "Nikkei 225 · Japan",
	"^HSI":   "Hang Seng · Hong Kong",
	"^FTSE":  "FTSE 100 · United Kingdom",
	"^GDAXI": "DAX · Germany",
}

// defaultSymbol is returned by symbolFor when the country isn't in the
// map. S&P 500 is the de-facto global benchmark and matches the
// middleware's "US" default country.
const defaultSymbol = "^GSPC"

// progressBarWidth is the cell count for the day/52w bars in the V1
// caption block. 20 is wide enough to read fractions but narrow enough
// to stay under typical terminal widths next to the surrounding
// caption text.
const progressBarWidth = 20

// vDivider is the section break between the common data block and the
// TW-only enrichment block in V1 layout. Alternating `─` + space reads
// as a thin divider in both ASCII and PNG surfaces -- a true blank
// line via ZWSP causes a PNG width-measurement artifact, so we use a
// visible separator instead.
const vDivider = "─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─"

// pylonBracketRe matches a complete pair of round or square brackets and
// their contents. Pylon parses `(...)` as a borderless box and `[...]` as
// a framed box; an unsanitized symbol or currency name containing either
// pair would smuggle a nested element into the caption and break the
// rendered output. Unmatched brackets are deliberately left alone (we
// only strip *complete* pairs).
var pylonBracketRe = regexp.MustCompile(`\(([^()]*)\)|\[([^\[\]]*)\]`)

// pylonAmpRefRe matches `&` immediately followed by a Ref-trigger char
// (`[A-Za-z_]`, exactly pylon's parser.go refRe). When this pattern fires,
// pylon parses the run as an inline Ref node and force-breaks the row
// around it -- visible symptom is captions like "S&P 500" fragmenting
// into 3 stacked rows. We insert literal spaces around the `&` so pylon's
// regex no longer matches (`& ` no longer matches `&[A-Za-z_]`), the
// `&` glyph itself remains, and every renderer (terminal + JetBrains
// Mono PNG) shows it cleanly. ZWNJ / ZWSP / fullwidth `＆` were tried
// and all failed in JetBrains Mono — invisible-format chars draw as
// `?` tofu, fullwidth has no glyph. A regular space is the only
// reliably-rendered separator.
var pylonAmpRefRe = regexp.MustCompile(`&([A-Za-z_])`)

// indexNameFor returns the human-readable header line for symbol, or
// empty string if symbol isn't in indexNameBySymbol. Callers MUST handle
// the empty case (omit the header row) so unknown symbols still render.
func indexNameFor(symbol string) string {
	return indexNameBySymbol[symbol]
}

// Register mounts GET /stock on r. r is typed as gin.IRouter so the
// service can be installed on either a *gin.Engine or a route group.
// p is the quote provider -- typically a cached.Provider wrapping
// yahoo.Provider in production. tw is the TW-specific market-data
// provider; pass nil to disable the TW enrichment block (the renderer
// gracefully omits it).
func Register(r gin.IRouter, p quote.Provider, tw twse.Provider) {
	h := &handler{provider: p, twse: tw}
	r.GET("/stock", h.serve)
}

// handler holds the dependencies for the /stock endpoint.
type handler struct {
	provider quote.Provider
	twse     twse.Provider // optional; nil disables TW enrichment block
}

// serve resolves the country, looks up the symbol, fetches a Quote (and
// TW market data when applicable), and renders the result. See package
// doc for the negotiation rules.
func (h *handler) serve(c *gin.Context) {
	country := resolveCountry(c)
	symbol := symbolFor(country)

	q, err := h.provider.Get(c.Request.Context(), symbol)
	stale := err != nil && q.Symbol != ""
	fresh := err == nil

	switch {
	case stale:
		log.Warn().Err(err).Str("symbol", symbol).Msg("quote upstream failed; serving stale cache")
	case !fresh:
		log.Warn().Err(err).Str("symbol", symbol).Msg("quote upstream failed; no cached value")
		c.Header("Retry-After", "60")
		c.String(http.StatusServiceUnavailable, "quote unavailable\n")
		return
	}

	// TW-specific enrichment is best-effort: failure here MUST NOT block
	// the rendered response. We log and proceed with empty MarketData,
	// which causes the TW block to be omitted gracefully.
	var tw twse.MarketData
	if country == "TW" && h.twse != nil {
		tw, err = h.twse.Get(c.Request.Context())
		if err != nil {
			log.Warn().Err(err).Msg("twse upstream failed; omitting TW enrichment block")
			tw = twse.MarketData{}
		}
	}

	headline := strconv.FormatFloat(q.Last, 'f', 2, 64)

	mode := middleware.GetMode(c)

	// Build the bare pylon source first; it's identical for the
	// text/pylon short-circuit and serves as input to BannerStack for
	// the rendered surfaces. Path 1 region-conditional formatting picks
	// CN labels for ASCII / EN for PNG (and text/pylon mirrors ASCII --
	// terminal-shaped clients get CJK).
	useEnglish := mode == render.ModePNG
	lines := buildLines(symbol, q, tw, stale, useEnglish)

	c.Header("Cache-Control", "public, max-age=60")

	// Mirrors /now: Accept: text/pylon short-circuits to the raw source.
	if strings.Contains(c.GetHeader("Accept"), "text/pylon") {
		// text/pylon clients are typically renderer-shaped (terminal-y),
		// so use the ASCII variant labels. PNG-bound clients don't
		// generally request text/pylon; if they do, the pylon source
		// they get back will need their own renderer to handle CJK.
		asciiLines := buildLines(symbol, q, tw, stale, false)
		c.Data(http.StatusOK, "text/pylon", []byte(render.BannerStackSource(headline, asciiLines)))
		return
	}

	body, rerr := render.BannerStack(headline, lines, mode)
	if rerr != nil {
		log.Error().Err(rerr).Stringer("mode", mode).Msg("render banner stack")
		body, _ = render.BannerStack(headline, lines, render.ModeASCII)
		mode = render.ModeASCII
	}
	if mode == render.ModePNG {
		c.Data(http.StatusOK, "image/png", body)
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", body)
}

// buildLines assembles the V1 outer-box content lines for the given
// quote and (optional) TW market data. Line order:
//
//  1. Index header (e.g. "TAIEX · Taiwan") -- omitted for unknown symbol
//  2. Symbol caption: STALE/CLOSED prefix + symbol + arrow + change% + price + currency + date
//  3. Day-range bar (omitted when DayHigh/DayLow missing)
//  4. 52-week-range bar (omitted when Week52High/Week52Low missing)
//  5. Divider + TW enrichment block (omitted unless tw.HasInstitutional() || tw.HasMargin())
//
// useEnglish=true picks the English labels for the TW block (PNG path
// since pylon's PNG font has no CJK glyphs); useEnglish=false picks
// Chinese labels (ASCII path).
func buildLines(symbol string, q quote.Quote, tw twse.MarketData, stale, useEnglish bool) []string {
	lines := make([]string, 0, 10)

	if name := indexNameFor(symbol); name != "" {
		lines = append(lines, stripPylonSyntax(name))
	}

	prefix := ""
	switch {
	case stale:
		prefix = "STALE · "
	case q.IsClosed:
		prefix = "CLOSED · "
	}
	arrow := "▲"
	if q.ChangePercent() < 0 {
		arrow = "▼"
	}
	caption := stripPylonSyntax(fmt.Sprintf(
		"%s%s  %s %+.2f%%  %s %s  %s",
		prefix, symbol, arrow, q.ChangePercent(),
		formatPrice(q.Last), q.Currency,
		q.AsOf.Format("2006-01-02"),
	))
	lines = append(lines, caption)

	if q.HasDayRange() {
		lines = append(lines, fmt.Sprintf("day %s %d%%",
			render.ProgressBar(q.DayPosition(), progressBarWidth),
			pctOf(q.DayPosition()),
		))
	}
	if q.Has52WeekRange() {
		lines = append(lines, fmt.Sprintf("52w %s %d%%",
			render.ProgressBar(q.Week52Position(), progressBarWidth),
			pctOf(q.Week52Position()),
		))
	}

	if tw.HasInstitutional() || tw.HasMargin() {
		lines = append(lines, vDivider)
		if tw.HasInstitutional() {
			lines = append(lines, formatInstitutional(tw, useEnglish))
		}
		if tw.HasMargin() {
			lines = append(lines, formatMargin(tw, useEnglish))
		}
	}
	return lines
}

// formatInstitutional formats the BFI82U row. CN: `三大法人  外資 +43.9B  投信 +2.2B  自營 +8.9B  合計 ▲ +55.0B`.
// EN: `institutional  foreign +43.9B  trust +2.2B  dealer +8.9B  net ▲ +55.0B`.
//
// Numbers are NTD billions with one decimal. Net carries an arrow
// matching its sign for symmetry with the symbol caption above.
func formatInstitutional(tw twse.MarketData, useEnglish bool) string {
	arrow := "▲"
	if tw.Net < 0 {
		arrow = "▼"
	}
	if useEnglish {
		return stripPylonSyntax(fmt.Sprintf(
			"institutional  foreign %s  trust %s  dealer %s  net %s %s",
			formatNTDBillions(tw.ForeignNet),
			formatNTDBillions(tw.TrustNet),
			formatNTDBillions(tw.DealerNet),
			arrow, formatNTDBillions(tw.Net),
		))
	}
	return stripPylonSyntax(fmt.Sprintf(
		"三大法人  外資 %s  投信 %s  自營 %s  合計 %s %s",
		formatNTDBillions(tw.ForeignNet),
		formatNTDBillions(tw.TrustNet),
		formatNTDBillions(tw.DealerNet),
		arrow, formatNTDBillions(tw.Net),
	))
}

// formatMargin formats the MI_MARGN row. CN: `融資餘額 4,409億 TWD  融券餘額 19萬張`.
// EN: `margin long 4.4T TWD  margin short 190.8K lots`.
//
// CN uses native Taiwanese unit conventions (億 = 100M, 萬 = 10K, 張 =
// lot of typically 1000 shares). EN converts to international units.
func formatMargin(tw twse.MarketData, useEnglish bool) string {
	if useEnglish {
		return stripPylonSyntax(fmt.Sprintf(
			"margin long %s TWD  margin short %s lots",
			formatLargeNumber(tw.MarginLongTWD),
			formatLargeNumber(tw.MarginShortLots),
		))
	}
	return stripPylonSyntax(fmt.Sprintf(
		"融資餘額 %s  融券餘額 %s",
		formatTWDInYi(tw.MarginLongTWD),
		formatLotsInWan(tw.MarginShortLots),
	))
}

// formatNTDBillions converts raw NTD into a `±XX.XB` string. e.g.
// 43,907,871,828 → `+43.9B`, -1,200,000,000 → `-1.2B`. One decimal
// reads naturally for the typical 0.5B–500B daily-flow range.
func formatNTDBillions(v int64) string {
	billions := float64(v) / 1e9
	return fmt.Sprintf("%+.1fB", billions)
}

// formatTWDInYi formats raw NTD as `N,NNN億` (1 億 = 1e8). Used by the
// CN margin caption -- this is the unit Taiwanese readers expect.
func formatTWDInYi(v int64) string {
	yi := v / 100_000_000
	return fmt.Sprintf("%s億", formatThousands(yi))
}

// formatLotsInWan formats lot count as `NNN萬張` (1 萬 = 10000) so a
// typical six-digit lot count reads compactly.
func formatLotsInWan(v int64) string {
	wan := float64(v) / 1e4
	return fmt.Sprintf("%.1f萬張", wan)
}

// formatLargeNumber compacts large integers to `XX.XK / XX.XM / XX.XB / XX.XT`
// so the EN margin caption reads at a glance regardless of magnitude.
func formatLargeNumber(v int64) string {
	abs := v
	if abs < 0 {
		abs = -abs
	}
	switch {
	case abs >= 1_000_000_000_000:
		return fmt.Sprintf("%.1fT", float64(v)/1_000_000_000_000)
	case abs >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(v)/1_000_000_000)
	case abs >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(v)/1_000_000)
	case abs >= 1_000:
		return fmt.Sprintf("%.1fK", float64(v)/1_000)
	}
	return strconv.FormatInt(v, 10)
}

// formatThousands inserts thousands separators into an int64 -- used by
// the CN unit converters where readers expect the comma format.
func formatThousands(v int64) string {
	if v < 1000 && v > -1000 {
		return strconv.FormatInt(v, 10)
	}
	raw := strconv.FormatInt(v, 10)
	negative := strings.HasPrefix(raw, "-")
	if negative {
		raw = raw[1:]
	}
	n := len(raw)
	var b strings.Builder
	first := n % 3
	if first > 0 {
		b.WriteString(raw[:first])
	}
	for i := first; i < n; i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(raw[i : i+3])
	}
	if negative {
		return "-" + b.String()
	}
	return b.String()
}

// pctOf converts a [0,1] fraction to a 0-100 integer percentage.
func pctOf(f float64) int {
	if f <= 0 {
		return 0
	}
	if f >= 1 {
		return 100
	}
	// round-half-to-even via int(f*100 + 0.5) is fine for display.
	return int(f*100 + 0.5)
}

// resolveCountry returns the country code the request should be served
// for. ?region=XX takes precedence over the middleware-stashed country.
func resolveCountry(c *gin.Context) string {
	if v := strings.ToUpper(strings.TrimSpace(c.Query("region"))); v != "" {
		return v
	}
	return middleware.GetCountry(c)
}

// symbolFor returns the index symbol for country, falling back to
// defaultSymbol when country isn't in symbolByCountry.
func symbolFor(country string) string {
	if s, ok := symbolByCountry[country]; ok {
		return s
	}
	return defaultSymbol
}

// formatPrice formats v as a non-negative decimal with two decimal places
// and a thousands separator -- e.g. 21834.5 → "21,834.50". Negative
// inputs are not expected (stock prices are non-negative) and are
// normalized to "0.00".
func formatPrice(v float64) string {
	if v <= 0 {
		return "0.00"
	}
	raw := strconv.FormatFloat(v, 'f', 2, 64)
	dot := strings.IndexByte(raw, '.')
	intPart, fracPart := raw[:dot], raw[dot:]
	n := len(intPart)
	if n <= 3 {
		return intPart + fracPart
	}
	var b strings.Builder
	b.Grow(n + n/3 + len(fracPart))
	first := n % 3
	if first > 0 {
		b.WriteString(intPart[:first])
	}
	for i := first; i < n; i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(intPart[i : i+3])
	}
	b.WriteString(fracPart)
	return b.String()
}

// stripPylonSyntax removes complete `(...)` and `[...]` pairs (and their
// contents) from s, neutralizes any `&[A-Za-z_]` that would otherwise
// parse as a pylon Ref node by spacing the `&` away from the following
// letter, and collapses the resulting whitespace. The literal `&` glyph
// survives, so "S&P 500" renders as "S & P 500" — brand-recognizable on
// every surface (terminal + JetBrains Mono PNG + text/pylon).
//
// Standalone `&` (e.g. "A & B") and `&` before non-letters never matched
// the Ref regex, so they pass through unchanged. strings.Fields below
// collapses any doubled spaces this produces ("S &P" -> "S  & P" -> "S & P").
//
// The `^` in `^GSPC` etc. is pylon-safe and left alone; the `·` prefix
// separator is also preserved.
func stripPylonSyntax(s string) string {
	cleaned := pylonBracketRe.ReplaceAllString(s, "")
	cleaned = pylonAmpRefRe.ReplaceAllString(cleaned, " & $1")
	return strings.Join(strings.Fields(cleaned), " ")
}

// noopTWSE is a `twse.Provider` that returns ErrUnavailable so callers
// can wire `Register(r, quote, stock.NoopTWSE())` when they don't have
// (or don't want) the TW enrichment block. Production wires the real
// twse.Cached. NoopTWSE is exported for cmd/main.go and tests.
type noopTWSE struct{}

func (noopTWSE) Get(_ context.Context) (twse.MarketData, error) {
	return twse.MarketData{}, twse.ErrUnavailable
}

// NoopTWSE returns a twse.Provider that always errors with
// twse.ErrUnavailable. Use when wiring /stock in environments where
// the TWSE enrichment block isn't wanted (cron, local dev, regions
// other than TW).
func NoopTWSE() twse.Provider { return noopTWSE{} }
