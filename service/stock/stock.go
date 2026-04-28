// Package stock exposes the /stock service: GET /stock returns the current
// price of the caller's regional stock index (e.g. TAIEX for TW, S&P 500
// for US) as a pylon banner stacked above a multi-row data box. The wire
// format mirrors /now -- content-negotiated:
//
//   - Accept: text/pylon OR ?format=pylon → raw pylon source
//   - ?format=html or User-Agent contains Mozilla → text/html (inline SVG)
//   - ?format=svg → image/svg+xml
//   - ?format=png → image/png
//   - ?format=ascii → text/plain; charset=utf-8 (ASCII)
//   - everything else → text/plain; charset=utf-8 (ASCII)
//
// Region resolution: ?region= query takes precedence over the country code
// stashed by middleware.RegionDetector (CF-IPCountry), which itself falls
// back to "US" when the header is missing. Unknown countries fall back to
// the S&P 500 (^GSPC). Cache-Control: public, max-age=60 is set on every
// rendered response so a CDN can absorb traffic spikes.
//
// Layout: the price banner sits above two borderless caption rows
// (index-name header + symbol/percent/price/date caption). On the TW
// path a single borderless multi-row block follows, AlignLeft, with
// every data row carrying a compound label so no separate section
// title is needed:
//
//	外資籌碼  bar  +43.9B
//	投信籌碼  bar  +2.2B
//	自營籌碼  bar  +8.9B
//	合計籌碼  bar  +55.0B  ▲
//	(blank)
//	漲跌家數  bar  漲 312 跌 691 平 63
//	(blank)
//	信用餘額  融資 N億   融券 N萬張
//	散戶多空  bar  +N%
//
// Blank rows between groups are zero-width space (U+200B) — a literal
// blank string would be trimmed away by pylon's row parser. Bordered
// boxes and `[A] <-> [B]` side-by-side rows were earlier designs but
// pylon v0.5 mis-renders both around CJK content in SVG mode. The TW block uses Chinese labels on every
// surface that can render CJK glyphs (ASCII / text/pylon / SVG / HTML);
// PNG is the lone English holdout because pylon's PNG uses
// basicfont.Face7x13 which has zero CJK coverage and would render
// Chinese as tofu.
package stock

import (
	"context"
	"fmt"
	"net/http"
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

// ohlcWidth is the column count of the OHLC range bar. 65 spreads the
// wicks and body wide enough to read marker positions at a glance and
// reduces the chance of Open/Close label collision on narrow bodies,
// while still fitting comfortably inside the 600px HTML wrapper cap
// with pylon's monospace SVG glyphs (~5px/char × 65 = ~325px before
// box parens and padding).
const ohlcWidth = 65

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
	//
	// Two paths into the same MarketData carrier:
	//  - Closed market: Provider.Get fetches the daily afterTrading
	//    triple (BFI82U positioning, MI_MARGN credit, MI_INDEX breadth)
	//    and the renderer surfaces all three sections.
	//  - Open market: positioning + credit are stale (TWSE freezes them
	//    until ~16:00) so we skip Provider.Get; instead, if the wired
	//    provider also implements twse.LiveBreadthProvider, we fetch
	//    the live intra-day breadth snapshot (computed by polling MIS
	//    across the listed-equity universe) and stamp just the breadth
	//    fields onto MarketData. The renderer then shows the breadth
	//    row alone, gated as before.
	var tw twse.MarketData
	var live twse.LiveBreadth
	if country == "TW" && h.twse != nil {
		ctx := c.Request.Context()
		if q.IsClosed {
			tw, err = h.twse.Get(ctx)
			if err != nil {
				log.Warn().Err(err).Msg("twse upstream failed; omitting TW enrichment block")
				tw = twse.MarketData{}
			}
		} else if lbp, ok := h.twse.(twse.LiveBreadthProvider); ok {
			b, lerr := lbp.FetchLiveBreadth(ctx)
			if lerr != nil {
				log.Warn().Err(lerr).Msg("twse live breadth failed; omitting breadth row")
			} else {
				live = b
			}
		}
	}

	headline := strconv.FormatFloat(q.Last, 'f', 2, 64)

	c.Header("Cache-Control", "public, max-age=60")

	// Pylon-source short-circuit: bare source, ASCII label set. text/pylon
	// clients are typically renderer-shaped (terminal-y), so they get CN
	// labels via the ASCII path; honors both Accept: text/pylon and
	// ?format=pylon for header/query parity.
	if middleware.WantsPylonSource(c) {
		bs := buildBlocks(symbol, q, tw, live, stale, false)
		c.Data(http.StatusOK, "text/pylon",
			[]byte(render.BannerSourceBoxes(headline, bs.captions, bs.boxes())))
		return
	}

	mode := middleware.ResolveMode(c)

	// CN labels appear on every surface that can render CJK glyphs:
	// ASCII (terminal-side fonts), text/pylon (consumer-side render),
	// SVG and HTML (Cascadia/Menlo with browser-provided CJK fallback).
	// PNG is the only EN holdout — pylon's PNG uses basicfont.Face7x13
	// which has zero CJK coverage; CN there would render as tofu.
	useEnglish := mode == render.ModePNG
	bs := buildBlocks(symbol, q, tw, live, stale, useEnglish)

	renderAt := func(m render.Mode) ([]byte, error) {
		return render.BannerBoxes(headline, bs.captions, bs.boxes(), m)
	}
	body, rerr := renderAt(mode)
	if rerr != nil {
		log.Error().Err(rerr).Stringer("mode", mode).Msg("render banner box")
		body, _ = renderAt(render.ModeASCII)
		mode = render.ModeASCII
	}
	if mode == render.ModePNG {
		c.Data(http.StatusOK, "image/png", body)
		return
	}
	if mode == render.ModeSVG {
		c.Data(http.StatusOK, "image/svg+xml", body)
		return
	}
	if mode == render.ModeHTML {
		c.Data(http.StatusOK, "text/html; charset=utf-8", body)
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", body)
}

// stockBlocks is the per-surface render content for render.BannerSourceBoxes.
// captions are the borderless header rows (index name + symbol/percent/
// price/date) that sit directly under the banner; body is the flat
// data-row list that fills the single AlignLeft borderless block
// underneath, with U+200B (zero-width space) separator rows marking
// group boundaries.
type stockBlocks struct {
	captions []string
	ohlc     []string
	body     []string
}

// boxes assembles the data section under the banner + captions as a
// single AlignLeft borderless box: the OHLC range bar (3 rows + ZWSP
// breath) stacks above the TW enrichment rows (positioning / breadth
// / credit). Folding both into one box (rather than two) shares the
// alignment context — pylon's AlignLeft only flush-lefts rows WITHIN
// a box, and adjacent boxes are independently centered in the figure;
// a narrower row (e.g. breadth alone during open) would otherwise
// sit centered while the wider OHLC bar stays left-aligned in its
// own box. Returns nil when both halves are empty so non-TW visitors
// without OHLC render as banner+captions only.
func (bs stockBlocks) boxes() []render.BoxBlock {
	rows := make([]string, 0, len(bs.ohlc)+len(bs.body))
	rows = append(rows, bs.ohlc...)
	rows = append(rows, bs.body...)
	if len(rows) == 0 {
		return nil
	}
	return []render.BoxBlock{{Rows: rows, Align: render.AlignLeft}}
}

// blankRow is the U+200B (zero-width space) separator row that creates
// a visible gap between groups inside the AlignLeft body block. A
// literal empty / whitespace-only row would be trimmed by pylon and
// disappear from the rendered output; ZWSP is invisible but takes a
// row, so the gap survives.
const blankRow = "​"

// buildBlocks assembles the per-surface content. useEnglish=true picks
// English labels for the TW block on the PNG path only — pylon's PNG
// font (basicfont.Face7x13) has zero CJK coverage so Chinese would
// render as tofu. Every other surface (ASCII, text/pylon, SVG, HTML)
// uses the Chinese label set.
func buildBlocks(symbol string, q quote.Quote, tw twse.MarketData, live twse.LiveBreadth, stale, useEnglish bool) stockBlocks {
	bs := stockBlocks{captions: make([]string, 0, 2)}

	// Index name comes from a static map but contains "S&P 500" — `&P`
	// would otherwise parse as a pylon Ref node and shred the line, so
	// the strip is load-bearing here.
	if name := indexNameFor(symbol); name != "" {
		bs.captions = append(bs.captions, render.StripPylonSyntax(name))
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
	// Currency comes from upstream Yahoo and could in theory carry junk;
	// keep the strip on the symbol caption.
	bs.captions = append(bs.captions, render.StripPylonSyntax(fmt.Sprintf(
		"%s%s  %s %+.2f%%  %s %s  %s",
		prefix, symbol, arrow, q.ChangePercent(),
		formatPrice(q.Last), q.Currency,
		q.AsOf.Format("2006-01-02"),
	)))

	if q.HasOHLC() {
		top, bar, bottom := render.OHLCBar(q.Open, q.DayHigh, q.DayLow, q.Last, ohlcWidth, formatPrice)
		if bar != "" {
			// Trailing ZWSP row gives the OHLC block breathing room
			// before whatever comes next (TW enrichment block, or the
			// bottom of the figure for non-TW visitors). A literal
			// empty row would be trimmed by pylon; ZWSP holds the line.
			bs.ohlc = []string{top, bar, bottom, blankRow}
		}
	}

	// Market-state gating: 籌碼面 (positioning) and 信用餘額 (credit)
	// source from TWSE `afterTrading/` endpoints that publish once-per-
	// day after close — during open hours the provider's lookback walks
	// back to yesterday's file, so those rows would carry the previous
	// session's numbers labelled the same as today's price. Gate them
	// on q.IsClosed so they only render when the data is actually
	// today's.
	//
	// Breadth has two sources: when closed, MI_INDEX afterTrading carries
	// the day's TSE-only totals on MarketData and we render a single
	// 漲跌家數 row; when open, live carries per-exchange counts (上市 +
	// 上櫃) computed by polling MIS across both universes, and we render
	// one row per exchange.
	var groups [][]string
	if q.IsClosed && tw.HasInstitutional() {
		groups = append(groups, positioningRows(tw, useEnglish))
	}
	if rows := breadthRows(tw, live, q.IsClosed, useEnglish); len(rows) > 0 {
		groups = append(groups, rows)
	}
	if q.IsClosed && tw.HasMargin() {
		groups = append(groups, creditRows(tw, useEnglish))
	}
	for i, g := range groups {
		if i > 0 {
			bs.body = append(bs.body, blankRow)
		}
		bs.body = append(bs.body, g...)
	}
	return bs
}

// breadthRows picks the right breadth presentation for the market
// state. Closed market reads from tw (MI_INDEX afterTrading TSE
// totals) and emits one row labelled 漲跌家數. Open market reads
// from live (per-exchange MIS aggregate) and emits one row per
// populated exchange labelled 上市 / 上櫃 — TPEx outages can leave
// only 上市 populated, in which case the 上櫃 row is silently
// skipped. Returns nil when no source is available.
func breadthRows(tw twse.MarketData, live twse.LiveBreadth, isClosed, useEnglish bool) []string {
	const halfWidth = 10
	if !isClosed && live.HasBreadth() {
		var rows []string
		if live.HasTSEBreadth() {
			rows = append(rows, breadthRow(
				tseLabel(useEnglish),
				live.TSEAdvance, live.TSEDecline, live.TSEUnchanged,
				halfWidth, useEnglish,
			))
		}
		if live.HasOTCBreadth() {
			rows = append(rows, breadthRow(
				otcLabel(useEnglish),
				live.OTCAdvance, live.OTCDecline, live.OTCUnchanged,
				halfWidth, useEnglish,
			))
		}
		return rows
	}
	if tw.HasBreadth() {
		return []string{breadthRow(
			closedLabel(useEnglish),
			tw.AdvanceCount, tw.DeclineCount, tw.UnchangedCount,
			halfWidth, useEnglish,
		)}
	}
	return nil
}

// breadthRow formats one breadth line: label + SignedBar + counts.
// Shared shape across the closed-market 漲跌家數 row and the open-
// market 上市 / 上櫃 split rows so column widths line up under
// AlignLeft regardless of which path produced them.
func breadthRow(label string, adv, dec, unc int64, halfWidth int, useEnglish bool) string {
	moving := adv + dec
	score := 0.0
	if moving > 0 {
		score = float64(adv-dec) / float64(moving)
	}
	bar := render.SignedBar(score, 1.0, halfWidth)
	if useEnglish {
		return fmt.Sprintf("%s  %s  up %d  down %d  even %d", label, bar, adv, dec, unc)
	}
	return fmt.Sprintf("%s  %s  漲 %d 跌 %d 平 %d", label, bar, adv, dec, unc)
}

func tseLabel(en bool) string {
	if en {
		return "tse  "
	}
	return "上市"
}

func otcLabel(en bool) string {
	if en {
		return "otc  "
	}
	return "上櫃"
}

func closedLabel(en bool) string {
	if en {
		return "breadth"
	}
	return "漲跌家數"
}

// positioningRows formats the 三大法人 section: 外資籌碼 / 投信籌碼 /
// 自營籌碼 / 合計籌碼, each with a center-split SignedBar on a shared
// scale (the largest absolute value across the four), so a glance
// reveals which participant dominated the day. The 合計 row carries a
// ▲/▼ arrow matching its sign as the summary cue.
func positioningRows(tw twse.MarketData, useEnglish bool) []string {
	const halfWidth = 10
	maxF := float64(absMaxInt64(tw.ForeignNet, tw.TrustNet, tw.DealerNet, tw.Net))

	type entry struct{ labelEN, labelCN string; value int64 }
	rows := []entry{
		{"foreign", "外資籌碼", tw.ForeignNet},
		{"trust  ", "投信籌碼", tw.TrustNet},
		{"dealer ", "自營籌碼", tw.DealerNet},
		{"total  ", "合計籌碼", tw.Net},
	}

	out := make([]string, 0, len(rows))
	for i, r := range rows {
		bar := render.SignedBar(float64(r.value), maxF, halfWidth)
		amount := formatNTDBillions(r.value)
		var line string
		if useEnglish {
			line = fmt.Sprintf("%s  %s  %s", r.labelEN, bar, amount)
		} else {
			line = fmt.Sprintf("%s  %s  %s", r.labelCN, bar, amount)
		}
		if i == len(rows)-1 {
			arrowChar := "▲"
			if r.value < 0 {
				arrowChar = "▼"
			}
			line += "  " + arrowChar
		}
		out = append(out, line)
	}
	return out
}

// absMaxInt64 returns the largest absolute value among the inputs.
// Returns 0 when all inputs are 0 so callers can divide-guard upstream.
func absMaxInt64(vs ...int64) int64 {
	var m int64
	for _, v := range vs {
		a := v
		if a < 0 {
			a = -a
		}
		if a > m {
			m = a
		}
	}
	return m
}

// creditRows formats the credit section: a single 信用餘額 row carrying
// the 融資 / 融券 totals. CN units (億 / 萬張) on CJK-capable surfaces,
// K/M/B/T in EN. The retail bull/bear row was dropped — its score sits
// near +1 most days because TW retail margin is structurally long-biased,
// so the signal reads as drift-from-baseline rather than absolute
// sentiment and was not adding glance-value.
func creditRows(tw twse.MarketData, useEnglish bool) []string {
	if useEnglish {
		return []string{fmt.Sprintf("balance   long %s   short %s",
			formatLargeNumber(tw.MarginLongTWD), formatLargeNumber(tw.MarginShortLots))}
	}
	return []string{fmt.Sprintf("信用餘額  融資 %s   融券 %s",
		formatTWDInYi(tw.MarginLongTWD), formatLotsInWan(tw.MarginShortLots))}
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
