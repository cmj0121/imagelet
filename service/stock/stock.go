// Package stock exposes the /stock service: GET /stock returns the current
// price of the caller's regional stock index (e.g. TAIEX for TW, S&P 500
// for US) as a pylon banner stacked above a status caption. The wire
// format mirrors /now — content-negotiated:
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
package stock

import (
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
)

// symbolByCountry maps an ISO 3166-1 alpha-2 country code to the index
// symbol the /stock service renders for visitors from that country.
// Hardcoded for v0.2 — no per-tenant override, no DB lookup.
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
// layout. Reader who sees `^TWII` in the caption can place it instantly
// when the header reads `TAIEX · Taiwan`. Empty header for unknown symbols
// (the bare `^GSPC` etc. in the caption is then the only label).
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

// indexNameFor returns the human-readable header line for symbol, or
// empty string if symbol isn't in indexNameBySymbol. Callers MUST handle
// the empty case (omit the header row) so unknown symbols still render.
func indexNameFor(symbol string) string {
	return indexNameBySymbol[symbol]
}

// pylonBracketRe matches a complete pair of round or square brackets and
// their contents. Pylon parses `(...)` as a borderless box and `[...]` as
// a framed box; an unsanitized symbol or currency name containing either
// pair would smuggle a nested element into the caption and break the
// rendered output. Unmatched brackets are deliberately left alone (we
// only strip *complete* pairs).
var pylonBracketRe = regexp.MustCompile(`\(([^()]*)\)|\[([^\[\]]*)\]`)

// Register mounts GET /stock on r. r is typed as gin.IRouter so the
// service can be installed on either a *gin.Engine or a route group.
// p is the quote provider — typically a cached.Provider wrapping
// yahoo.Provider in production, a fake in tests.
func Register(r gin.IRouter, p quote.Provider) {
	h := &handler{provider: p}
	r.GET("/stock", h.serve)
}

// handler holds the dependencies for the /stock endpoint.
type handler struct {
	provider quote.Provider
}

// serve resolves the country, looks up the symbol, fetches a Quote, and
// renders the result. See package doc for the negotiation rules.
func (h *handler) serve(c *gin.Context) {
	country := resolveCountry(c)
	symbol := symbolFor(country)

	q, err := h.provider.Get(c.Request.Context(), symbol)

	// stale mirrors cached.Provider's stale-failure return shape: when an
	// upstream refresh fails but a previously-good Quote is still in the
	// cache, the provider returns (cachedQuote, err). Detect that by
	// checking err != nil AND q.Symbol != "" (a zero Quote has no symbol).
	stale := err != nil && q.Symbol != ""
	fresh := err == nil

	switch {
	case stale:
		log.Warn().Err(err).Str("symbol", symbol).Msg("quote upstream failed; serving stale cache")
	case !fresh:
		// No cached value either — the only signal an operator gets that
		// Yahoo broke. Retry-After: 60 is the client revalidate hint;
		// server-side won't actually retry upstream until failureTTL (10m)
		// elapses, so subsequent 503s come from cached error.
		log.Warn().Err(err).Str("symbol", symbol).Msg("quote upstream failed; no cached value")
		c.Header("Retry-After", "60")
		c.String(http.StatusServiceUnavailable, "quote unavailable\n")
		return
	}

	headline := strconv.FormatFloat(q.Last, 'f', 2, 64)

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

	c.Header("Cache-Control", "public, max-age=60")

	// Mirrors /now: Accept: text/pylon short-circuits to the raw source
	// so external renderers (or curious humans) can see what the renderer
	// is actually parsing.
	if strings.Contains(c.GetHeader("Accept"), "text/pylon") {
		c.Data(http.StatusOK, "text/pylon", []byte(render.BannerSource(headline, caption)))
		return
	}

	mode := middleware.GetMode(c)
	body, rerr := render.Banner(headline, caption, mode)
	if rerr != nil {
		log.Error().Err(rerr).Stringer("mode", mode).Msg("render banner")
		body, _ = render.Banner(headline, caption, render.ModeASCII)
		mode = render.ModeASCII
	}
	if mode == render.ModePNG {
		c.Data(http.StatusOK, "image/png", body)
		return
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", body)
}

// resolveCountry returns the country code the request should be served
// for. ?region=XX takes precedence over the middleware-stashed country so
// local dev / CI / debugging works without a real CF-IPCountry header.
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
// and a thousands separator — e.g. 21834.5 → "21,834.50". Negative inputs
// are not expected (stock prices are non-negative) and are normalized to
// "0.00".
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
// contents) from s and collapses the resulting whitespace. Defensive
// sanitization: pylon parses both bracket pairs as nested boxes, so a
// stray pair in the caption breaks the rendered output. The `^` in
// `^GSPC` etc. is pylon-safe and left alone; the `·` prefix separator
// is also preserved.
func stripPylonSyntax(s string) string {
	cleaned := pylonBracketRe.ReplaceAllString(s, "")
	return strings.Join(strings.Fields(cleaned), " ")
}
