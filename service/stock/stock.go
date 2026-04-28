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
	"time"

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

// titleFor builds the title row shown above the price box —
// `<symbol> · <name>` when Yahoo supplies a name, bare `<symbol>`
// otherwise. ShortName is preferred on CJK-capable surfaces (often
// localized e.g. "台積電"); LongName is used on the PNG path where
// pylon's basicfont has zero CJK coverage and would render Chinese as
// tofu — Yahoo's longName is conventionally Latin (e.g. "Taiwan
// Semiconductor Manufacturing Company Limited"). Falls back to the
// other field when the preferred one is empty.
func titleFor(symbol string, q quote.Quote, useEnglish bool) string {
	name := q.Name
	if useEnglish {
		name = q.LongName
		if name == "" {
			name = q.Name
		}
	} else if name == "" {
		name = q.LongName
	}
	if name == "" {
		return symbol
	}
	return symbol + " · " + name
}

// Register mounts GET /stock and GET /stock/:symbol on r. r is typed
// as gin.IRouter so the service can be installed on either a
// *gin.Engine or a route group. p is the quote provider -- typically
// a cached.Provider wrapping yahoo.Provider in production. tw is the
// TW-specific market-data provider; pass nil to disable the TW
// enrichment block (the renderer gracefully omits it).
//
// Route shape:
//
//   - GET /stock              → region-routed (CF-IPCountry) → symbol map.
//   - GET /stock/:symbol      → caller-specified Yahoo symbol. The
//     `^` index prefix must be percent-encoded as %5E by clients that
//     don't auto-encode it (curl encodes; some shells don't). TW
//     enrichment activates by symbol suffix (.TW / .TWO) or the
//     ^TWII index, independent of the visitor's country.
func Register(r gin.IRouter, p quote.Provider, tw twse.Provider) {
	h := &handler{provider: p, twse: tw}
	r.GET("/stock", h.serve)
	r.GET("/stock/:symbol", h.serveSymbol)
}

// handler holds the dependencies for the /stock endpoint.
type handler struct {
	provider quote.Provider
	twse     twse.Provider // optional; nil disables TW enrichment block
}

// fetchQuote picks the right provider entry point for the request: live
// Get when no ?date= override is set, GetAt (via HistoricalProvider)
// when one is. Falls back to ErrUnavailable if the wired provider does
// not implement HistoricalProvider — the caller then surfaces a clean
// 503 rather than silently returning today's quote on a historical URL.
func (h *handler) fetchQuote(ctx context.Context, symbol string, asOf time.Time, override bool) (quote.Quote, error) {
	if !override {
		return h.provider.Get(ctx, symbol)
	}
	hp, ok := h.provider.(quote.HistoricalProvider)
	if !ok {
		return quote.Quote{}, quote.ErrUnavailable
	}
	return hp.GetAt(ctx, symbol, asOf)
}

// serve handles GET /stock — the region-routed entry point. It maps
// the caller's country (from CF-IPCountry, or ?region= override) to
// a symbol via the symbolByCountry table and dispatches to renderSymbol.
func (h *handler) serve(c *gin.Context) {
	country := resolveCountry(c)
	symbol := symbolFor(country)
	h.renderSymbol(c, symbol, isTWSymbol(symbol))
}

// serveSymbol handles GET /stock/:symbol — the caller-specified entry
// point. The path segment is normalized (uppercased + trimmed) and
// validated against an allowlist before reaching upstream. TW
// enrichment activates by symbol suffix (.TW / .TWO) or the ^TWII
// index, independent of the visitor's region.
func (h *handler) serveSymbol(c *gin.Context) {
	symbol := strings.ToUpper(strings.TrimSpace(c.Param("symbol")))
	if !validSymbol(symbol) {
		c.Header("Cache-Control", "no-store")
		c.String(http.StatusNotFound, "invalid symbol\n")
		return
	}
	h.renderSymbol(c, symbol, isTWSymbol(symbol))
}

// renderSymbol fetches a Quote (and TW market data when enrichTW is
// true), then content-negotiates and writes the response. Shared body
// for both /stock and /stock/:symbol — the only difference between the
// two routes is how the symbol and the enrichTW gate are derived.
func (h *handler) renderSymbol(c *gin.Context, symbol string, enrichTW bool) {
	asOf, dateOverride := middleware.GetDate(c)

	q, err := h.fetchQuote(c.Request.Context(), symbol, asOf, dateOverride)
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
	// Three paths into the same MarketData carrier:
	//  - Date override: hist GetAt fetches the daily afterTrading triple
	//    pinned to the requested historical date. Live breadth is skipped
	//    (intra-day metric, meaningless for a past date).
	//  - Closed market (no override): Provider.Get fetches the daily
	//    afterTrading triple (BFI82U positioning, MI_MARGN credit,
	//    MI_INDEX breadth) and the renderer surfaces all three sections.
	//  - Open market (no override): positioning + credit are stale (TWSE
	//    freezes them until ~16:00) so we skip Provider.Get; instead, if
	//    the wired provider also implements twse.LiveBreadthProvider, we
	//    fetch the live intra-day breadth snapshot (computed by polling
	//    MIS across the listed-equity universe) and stamp just the
	//    breadth fields onto MarketData. The renderer then shows the
	//    breadth row alone, gated as before.
	var tw twse.MarketData
	var perStock twse.StockData
	var live twse.LiveBreadth
	var retail twse.RetailFutures
	var lending twse.SecuritiesLending
	var pcr twse.OptionsPCR
	var vix twse.VIX
	if enrichTW && h.twse != nil {
		ctx := c.Request.Context()
		switch {
		case dateOverride:
			if hp, ok := h.twse.(twse.HistoricalProvider); ok {
				tw, err = hp.GetAt(ctx, asOf)
				if err != nil {
					log.Warn().Err(err).Time("asOf", asOf).Msg("twse historical fetch failed; omitting TW enrichment block")
					tw = twse.MarketData{}
				}
			}
		case q.IsClosed:
			tw, err = h.twse.Get(ctx)
			if err != nil {
				log.Warn().Err(err).Msg("twse upstream failed; omitting TW enrichment block")
				tw = twse.MarketData{}
			}
		default:
			if lbp, ok := h.twse.(twse.LiveBreadthProvider); ok {
				b, lerr := lbp.FetchLiveBreadth(ctx)
				if lerr != nil {
					log.Warn().Err(lerr).Msg("twse live breadth failed; omitting breadth row")
				} else {
					live = b
				}
			}
		}

		// Per-stock T86 lookup overlays the market-wide BFI82U positioning
		// rows when the symbol carries a TWSE numeric stock id (e.g.
		// 2330.TW → "2330"). T86 only covers TWSE 上市; .TWO (TPEx 上櫃)
		// returns no row and falls back to market-wide positioning. Open
		// market: T86 is afterTrading, so the per-stock fetch is gated on
		// q.IsClosed or dateOverride to avoid showing stale positioning
		// labelled as today's price.
		stockID := twStockID(symbol)
		if stockID != "" && (q.IsClosed || dateOverride) {
			if psp, ok := h.twse.(twse.PerStockProvider); ok {
				asOfQuery := asOf
				if !dateOverride {
					asOfQuery = time.Now()
				}
				ps, perr := psp.GetForStock(ctx, stockID, asOfQuery)
				if perr != nil {
					log.Warn().Err(perr).Str("stock", stockID).Msg("twse per-stock fetch failed; falling back to market-wide positioning")
				} else {
					perStock = ps
				}
			}
		}

		// CN name override: Yahoo's chart endpoint returns Latin
		// shortName for TW listings (e.g. "TAIWAN SEMICONDUCTOR
		// MANUFACTUR" for 2330.TW). MIS getStockInfo carries the
		// localized 證券簡稱 in `n`. Prefer perStock.Name when T86
		// already gave us one; otherwise hit MIS via the optional
		// NameProvider. The MIS lookup is memoized forever in the
		// HTTPProvider's process-local map, so repeat /stock/<id>
		// requests don't fan out an extra HTTP call. Failures are
		// best-effort: log and keep Yahoo's Latin name as fallback.
		if stockID != "" {
			switch {
			case perStock.Name != "":
				q.Name = perStock.Name
			default:
				if np, ok := h.twse.(twse.NameProvider); ok {
					isOTC := strings.HasSuffix(symbol, ".TWO")
					if name, lerr := np.LookupName(c.Request.Context(), stockID, isOTC); lerr == nil {
						q.Name = name
					} else if lerr != twse.ErrUnavailable {
						log.Debug().Err(lerr).Str("stock", stockID).Msg("twse name lookup failed; keeping upstream name")
					}
				}
			}
		}

		// Retail futures positioning (小台 / 微台) is a market-wide
		// signal, not per-symbol — sourced from TAIFEX 三大法人區分
		// 各期貨契約 OI with retail derived as -(institutional net OI).
		// Gated on q.IsClosed || dateOverride for the same reason as
		// the BFI82U positioning rows: the TAIFEX upstream is daily
		// afterTrading and would otherwise show yesterday's numbers
		// alongside today's price during the live session. Failures
		// are best-effort: log and zero out so the renderer skips the
		// 散戶 group gracefully.
		if (q.IsClosed || dateOverride) && (h.twse != nil) {
			ctx := c.Request.Context()
			asOfQuery := asOf
			if !dateOverride {
				asOfQuery = time.Now()
			}

			if stockID != "" {
				// Per-stock view: only the per-symbol signal applies.
				// Retail futures, PCR, and VIX are market-wide and
				// would just be noise alongside a single ticker.
				if slp, ok := h.twse.(twse.SecuritiesLendingProvider); ok {
					if l, lerr := slp.GetSecuritiesLending(ctx, stockID, asOfQuery); lerr == nil {
						lending = l
					} else if lerr != twse.ErrUnavailable {
						log.Warn().Err(lerr).Str("stock", stockID).Msg("twse securities-lending fetch failed; omitting 借券 row")
					}
				}
			} else {
				if rfp, ok := h.twse.(twse.RetailFuturesProvider); ok {
					if r, rerr := rfp.GetRetailFutures(ctx, asOfQuery); rerr == nil {
						retail = r
					} else if rerr != twse.ErrUnavailable {
						log.Warn().Err(rerr).Msg("taifex retail futures fetch failed; omitting 散戶 group")
					}
				}
				if op, ok := h.twse.(twse.OptionsPCRProvider); ok {
					if v, perr := op.GetOptionsPCR(ctx, asOfQuery); perr == nil {
						pcr = v
					} else if perr != twse.ErrUnavailable {
						log.Warn().Err(perr).Msg("taifex pcr fetch failed; omitting PCR row")
					}
				}
				if vp, ok := h.twse.(twse.VIXProvider); ok {
					if v, verr := vp.GetVIX(ctx, asOfQuery); verr == nil {
						vix = v
					} else if verr != twse.ErrUnavailable {
						log.Warn().Err(verr).Msg("taifex vix fetch failed; omitting VIX row")
					}
				}
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
		bs := buildBlocks(symbol, q, tw, perStock, live, retail, lending, pcr, vix, stale, false)
		c.Data(http.StatusOK, "text/pylon",
			[]byte(render.BannerSourceBoxes(headline, "", bs.captions, bs.boxes())))
		return
	}

	mode := middleware.ResolveMode(c)

	// CN labels appear on every surface that can render CJK glyphs:
	// ASCII (terminal-side fonts), text/pylon (consumer-side render),
	// SVG and HTML (Cascadia/Menlo with browser-provided CJK fallback).
	// PNG is the only EN holdout — pylon's PNG uses basicfont.Face7x13
	// which has zero CJK coverage; CN there would render as tofu.
	useEnglish := mode == render.ModePNG
	bs := buildBlocks(symbol, q, tw, perStock, live, retail, lending, pcr, vix, stale, useEnglish)

	renderAt := func(m render.Mode) ([]byte, error) {
		return render.BannerBoxes(headline, "", bs.captions, bs.boxes(), m)
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
		og := render.OGMeta{
			Title:       titleFor(symbol, q, false),
			Description: ogDescription(q),
			ImageURL:    middleware.AbsoluteURL(c, "format=svg"),
			PageURL:     middleware.AbsoluteURL(c, ""),
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", render.InjectOGMeta(body, og))
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", body)
}

// ogDescription builds the og:description / twitter:description for the
// /stock page. Mirrors the on-figure primary caption row: change-percent
// arrow, signed change percent, formatted price, currency. Reads as a
// glanceable summary in a chat preview unfurl.
func ogDescription(q quote.Quote) string {
	arrow := "▲"
	if q.ChangePercent() < 0 {
		arrow = "▼"
	}
	return fmt.Sprintf("%s %+.2f%%  %s %s",
		arrow, q.ChangePercent(), formatPrice(q.Last), q.Currency)
}

// stockBlocks is the per-surface render content for render.BannerSourceBoxes.
// captions are the borderless header rows (title + price/percent/date +
// optional volume) that sit directly under the banner; body is the flat
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
// uses the Chinese label set. perStock takes precedence over tw for
// the 三大法人 group: when the symbol carries a TWSE stock id and
// T86 returned a row, we render PER-STOCK flow with shares; otherwise
// fall back to the market-wide BFI82U numbers in NTD.
func buildBlocks(symbol string, q quote.Quote, tw twse.MarketData, perStock twse.StockData, live twse.LiveBreadth, retail twse.RetailFutures, lending twse.SecuritiesLending, pcr twse.OptionsPCR, vix twse.VIX, stale, useEnglish bool) stockBlocks {
	bs := stockBlocks{captions: make([]string, 0, 3)}

	// Title row: `<symbol> · <name>`. The symbol/name pair contains
	// "S&P 500" in some cases — `&P` would otherwise parse as a pylon
	// Ref node and shred the line, so the strip is load-bearing here.
	bs.captions = append(bs.captions, render.StripPylonSyntax(titleFor(symbol, q, useEnglish)))

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

	// Primary caption: prefix + change% + price + currency + date.
	// Currency comes from upstream Yahoo and could in theory carry junk;
	// keep the strip on the symbol caption.
	bs.captions = append(bs.captions, render.StripPylonSyntax(fmt.Sprintf(
		"%s%s %+.2f%%  %s %s  %s",
		prefix, arrow, q.ChangePercent(),
		formatPrice(q.Last), q.Currency,
		q.AsOf.Format("2006-01-02"),
	)))

	// Volume caption (optional): `Vol 50.3M · 12.6B USD` when the
	// upstream supplied a volume. Dollar-volume is an approximation —
	// shares × last-price — since Yahoo's chart endpoint doesn't carry
	// the true session-VWAP. Close enough at the glance-resolution this
	// surface targets.
	if q.Volume > 0 {
		bs.captions = append(bs.captions, render.StripPylonSyntax(formatVolumeRow(q)))
	}

	if q.HasOHLC() {
		top, bar, bottom := render.OHLCBar(q.Open, q.DayHigh, q.DayLow, q.Last, q.PrevClose, ohlcWidth, formatPrice)
		if bar != "" {
			// Leading + trailing ZWSP rows give the OHLC block breathing
			// room from the price/caption banner above and from whatever
			// follows below (TW enrichment, or bottom of the figure for
			// non-TW visitors). A literal empty row would be trimmed by
			// pylon; ZWSP holds the line.
			bs.ohlc = []string{blankRow, top, bar, bottom, blankRow}
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
	// 漲跌家數 is a market-wide count; hide it on per-stock views so
	// the panel only carries rows about the queried ticker.
	isPerStock := twStockID(symbol) != ""
	var groups [][]string
	switch {
	case q.IsClosed && perStock.HasFlow():
		groups = append(groups, perStockPositioningRows(perStock, q, useEnglish))
	case q.IsClosed && tw.HasInstitutional():
		groups = append(groups, positioningRows(tw, useEnglish))
	}
	if !isPerStock {
		if rows := breadthRows(tw, live, q.IsClosed, useEnglish); len(rows) > 0 {
			groups = append(groups, rows)
		}
	}
	if q.IsClosed && tw.HasMargin() {
		groups = append(groups, creditRows(tw, useEnglish))
	}
	if q.IsClosed && lending.Has() {
		groups = append(groups, []string{lendingRow(lending, useEnglish)})
	}
	if q.IsClosed && (pcr.Has() || vix.Has()) {
		groups = append(groups, []string{marketSentimentRow(pcr, vix, useEnglish)})
	}
	if q.IsClosed && retail.HasAny() {
		groups = append(groups, retailFuturesRows(retail, useEnglish))
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

// perStockPositioningRows formats the 三大法人 section for a single
// stock: 外資籌碼 / 投信籌碼 / 自營籌碼 / 合計籌碼, each with a
// center-split SignedBar on a shared scale. The upstream T86 carries
// SHARES (not NTD), so amounts are converted to NTD via shares ×
// q.Last for visual consistency with the market-wide block. Same
// row shape as positioningRows so a TW symbol seamlessly switches
// between per-stock and market-wide presentations.
func perStockPositioningRows(d twse.StockData, q quote.Quote, useEnglish bool) []string {
	const halfWidth = 10
	maxF := float64(absMaxInt64(d.ForeignNet, d.TrustNet, d.DealerNet, d.Net))
	price := q.Last

	type entry struct {
		labelEN, labelCN string
		shares           int64
	}
	rows := []entry{
		{"foreign", "外資籌碼", d.ForeignNet},
		{"trust  ", "投信籌碼", d.TrustNet},
		{"dealer ", "自營籌碼", d.DealerNet},
		{"total  ", "合計籌碼", d.Net},
	}

	out := make([]string, 0, len(rows))
	for i, r := range rows {
		bar := render.SignedBar(float64(r.shares), maxF, halfWidth)
		amount := formatNTDFromShares(r.shares, price)
		var line string
		if useEnglish {
			line = fmt.Sprintf("%s  %s  %s", r.labelEN, bar, amount)
		} else {
			line = fmt.Sprintf("%s  %s  %s", r.labelCN, bar, amount)
		}
		if i == len(rows)-1 {
			arrowChar := "▲"
			if r.shares < 0 {
				arrowChar = "▼"
			}
			line += "  " + arrowChar
		}
		out = append(out, line)
	}
	return out
}

// formatNTDFromShares converts a share count + per-share price into a
// signed `±XX.XB` NTD string. Shares can be negative (net sell), so
// the sign is preserved through the multiplication. When price is 0
// (rare — would require q.Last unavailable), falls back to the share
// count alone with a "shares" suffix to keep the row informative.
func formatNTDFromShares(shares int64, price float64) string {
	if price <= 0 {
		return fmt.Sprintf("%+d shares", shares)
	}
	ntd := float64(shares) * price
	return fmt.Sprintf("%+.1fB", ntd/1e9)
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

// lendingRow formats the 借券賣出 row: standing securities-lending
// short balance in lots (1 張 = 1000 shares). The headline metric is
// the standing balance, not per-day flow — high values flag
// significant outstanding short pressure on the listing. Single row,
// no SignedBar (the value is unsigned and a bar without a comparator
// adds noise rather than information).
func lendingRow(d twse.SecuritiesLending, useEnglish bool) string {
	lots := d.Balance / 1000 // shares → 張
	if useEnglish {
		return fmt.Sprintf("sbl-bal     %s lots", formatThousands(lots))
	}
	return fmt.Sprintf("借券賣出  %s 張", formatThousands(lots))
}

// marketSentimentRow combines the 台指選擇權 Put/Call ratio and the
// Taiwan VIX onto a single line: PCR carries a ▲ / ▼ glyph (PCR > 100
// = puts dominate, drawn ▼; otherwise ▲), VIX is the close-of-session
// value to two decimals. Either half may be empty when its upstream
// fetcher is unavailable; in that case the row collapses to whichever
// signal is present. Callers gate on (pcr.Has() || vix.Has()).
func marketSentimentRow(p twse.OptionsPCR, v twse.VIX, useEnglish bool) string {
	var parts []string
	if p.Has() {
		arrow := "▲"
		if p.OIRatio > 100 {
			arrow = "▼"
		}
		if useEnglish {
			parts = append(parts, fmt.Sprintf("opt-pcr  %.1f%%%s", p.OIRatio, arrow))
		} else {
			parts = append(parts, fmt.Sprintf("台指選擇  PCR %.1f%%%s", p.OIRatio, arrow))
		}
	}
	if v.Has() {
		if useEnglish {
			parts = append(parts, fmt.Sprintf("vix  %.2f", v.Value))
		} else {
			parts = append(parts, fmt.Sprintf("波動指數  %.2f", v.Value))
		}
	}
	return strings.Join(parts, "  ")
}

// retailFuturesRows formats the 散戶 section: 小台散戶 / 微台散戶
// each with a center-split SignedBar. Values are retail net OI in
// lots (positive = retail net long, negative = retail net short),
// derived upstream from -(institutional net OI). The shared bar
// scale is the larger absolute value across the two contracts so
// the more concentrated row uses full bar width and the less
// concentrated row reads as a proportion of it.
//
// Rows where the upstream had no data (HasMXF / HasTMF false) are
// silently skipped; the handler caller has already gated on
// retail.HasAny() so this never returns an empty slice.
func retailFuturesRows(r twse.RetailFutures, useEnglish bool) []string {
	const halfWidth = 10
	scale := absMaxInt64(r.MXFNet, r.TMFNet)
	if scale == 0 {
		return nil
	}
	out := make([]string, 0, 2)
	if r.HasMXF() {
		out = append(out, retailFuturesRow(mxfLabel(useEnglish), r.MXFNet, scale, halfWidth))
	}
	if r.HasTMF() {
		out = append(out, retailFuturesRow(tmfLabel(useEnglish), r.TMFNet, scale, halfWidth))
	}
	return out
}

// retailFuturesRow formats one row: label + SignedBar + signed lot count.
// The lot count carries a thousands separator so a typical 4–5 digit
// reading scans cleanly.
func retailFuturesRow(label string, net, scale int64, halfWidth int) string {
	bar := render.SignedBar(float64(net), float64(scale), halfWidth)
	return fmt.Sprintf("%s  %s  %s 口", label, bar, formatSignedLots(net))
}

func mxfLabel(en bool) string {
	if en {
		return "mxf-retail"
	}
	return "小台散戶"
}

func tmfLabel(en bool) string {
	if en {
		return "tmf-retail"
	}
	return "微台散戶"
}

// formatSignedLots returns the lot count with a leading sign and
// thousands separator: 1234 → "+1,234"; -56789 → "-56,789".
func formatSignedLots(v int64) string {
	if v >= 0 {
		return "+" + formatThousands(v)
	}
	return formatThousands(v) // negative numbers carry their own minus
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

// formatVolumeRow returns the volume caption row: `Vol <shares> ·
// <dollar-volume> <currency>`. Both halves are compacted via
// formatLargeNumber so a typical 1M-1B shares input reads at a glance
// without overflowing the figure width.
func formatVolumeRow(q quote.Quote) string {
	shares := formatLargeNumber(q.Volume)
	dollar := formatLargeNumber(int64(q.Last * float64(q.Volume)))
	if q.Currency == "" {
		return fmt.Sprintf("Vol %s · %s", shares, dollar)
	}
	return fmt.Sprintf("Vol %s · %s %s", shares, dollar, q.Currency)
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

// isTWSymbol reports whether symbol describes a Taiwan listing — TWSE
// 上市 (`.TW` suffix), TPEx 上櫃 (`.TWO` suffix), or the TAIEX index
// itself (`^TWII`). The /stock handler uses this to decide whether to
// request the TW market-wide enrichment block (institutional flow,
// breadth, margin balance), which is relevant for ANY TW listing
// because those aggregates describe market context, not the
// individual stock.
//
// Symbol is expected uppercased (the caller-symbol path normalizes
// via strings.ToUpper); region-routed callers feed the symbolByCountry
// table which is uppercase by construction.
func isTWSymbol(symbol string) bool {
	if symbol == "^TWII" {
		return true
	}
	return strings.HasSuffix(symbol, ".TW") || strings.HasSuffix(symbol, ".TWO")
}

// twStockID extracts the numeric TWSE stock id from `<digits>.TW` /
// `<digits>.TWO` symbols (e.g. "2330.TW" → "2330"). Returns empty for
// the ^TWII index, non-TW symbols, or non-numeric prefixes — those
// have no T86 row to fetch. T86 only publishes for TWSE 上市
// listings, so .TWO callers will get an upstream miss; the handler
// surfaces that as a graceful fallback to market-wide positioning.
func twStockID(symbol string) string {
	dot := strings.IndexByte(symbol, '.')
	if dot <= 0 {
		return ""
	}
	suffix := symbol[dot:]
	if suffix != ".TW" && suffix != ".TWO" {
		return ""
	}
	prefix := symbol[:dot]
	for _, r := range prefix {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return prefix
}

// symbolMaxLen caps the path-symbol length. Real Yahoo symbols are
// well under this — `BRK-B`, `2330.TW`, `EURUSD=X`, `^GSPC` — so 16
// is comfortably above the longest reasonable input while bounding
// upstream URL length.
const symbolMaxLen = 16

// validSymbol applies a strict allowlist to the path-supplied symbol
// before it reaches upstream. Permitted: A-Z, 0-9, `.` (exchange
// suffix), `-` (e.g. BRK-B), `^` (index prefix), `=` (FX/futures).
// Anything else is rejected with 404 — the handler doesn't 4xx on
// unknown-but-shaped symbols (Yahoo's no-data response surfaces as
// 503 already), so 404 here is reserved for "couldn't even ATTEMPT
// the upstream call" and keeps the contract clean.
func validSymbol(symbol string) bool {
	if symbol == "" || len(symbol) > symbolMaxLen {
		return false
	}
	for _, r := range symbol {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.', r == '-', r == '^', r == '=':
		default:
			return false
		}
	}
	return true
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
