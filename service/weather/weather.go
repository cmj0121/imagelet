// Package weather mounts GET /weather: today's weather rendered as a hero
// image — ASCII condition icon to the left of a pylon-rendered temperature
// banner, with up to five caption lines below (condition + location,
// feels-like + wind, today's high/low, optional humidity/UV/precip extras,
// sunrise/sunset).
//
// Wire format mirrors /now and /stock:
//
//   - Accept: text/pylon → bare temperature-banner pylon source (no captions)
//   - User-Agent contains Mozilla → image/png (icon + banner composed locally;
//     captions drawn with basicfont, axis-flipped from /404's composition)
//   - everything else → text/plain; charset=utf-8 (icon rows prepended to
//     pylon banner ASCII rows, captions appended below)
//
// Location resolution priority: validated ?lat=&lon= query → CF-IPLatitude/
// CF-IPLongitude headers → country-capital fallback keyed by CF-IPCountry.
// Bad coords silently fall through (never 4xx).
package weather

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cmj0121/pylon/src/go/pkg/pylon"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
	"github.com/cmj0121/imagelet/service/weather/airquality"
	"github.com/cmj0121/imagelet/service/weather/earthquake"
	"github.com/cmj0121/imagelet/service/weather/forecast"
)

// countryCapitals maps ISO 3166-1 alpha-2 country codes to a representative
// (lat, lon, city) triple for the country fallback path. Service-local copy
// of /stock's key set; lift to middleware only when a third consumer wants it.
var countryCapitals = map[string]struct {
	name string
	lat  float64
	lon  float64
}{
	"TW": {"Taipei", 25.04, 121.56},
	"US": {"New York", 40.71, -74.01},
	"JP": {"Tokyo", 35.68, 139.69},
	"HK": {"Hong Kong", 22.32, 114.17},
	"GB": {"London", 51.51, -0.13},
	"DE": {"Berlin", 52.52, 13.40},
}

// defaultCountry is the universal fallback when CF-IPCountry is absent or
// unknown. Matches the middleware's own default and /stock's behavior.
const defaultCountry = "US"

// imperialCountries are the three holdouts on imperial units. Source of
// truth for the country-driven unit default; ?unit=c|f overrides.
var imperialCountries = map[string]bool{
	"US": true, "LR": true, "MM": true,
}

// Layout constants. iconCells is the fixed-width column the icon occupies
// in ASCII output; iconGap separates the icon from the banner. captionPad
// (composePadding+composeGap) lines captions up against the banner's left
// edge — change one, change the other.
const (
	iconCells = 12
	iconGap   = 2

	// PNG composition.
	composePadding = 16
	composeGap     = 16
)

var (
	composeBG  = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	composeInk = color.RGBA{R: 0x0f, G: 0x1c, B: 0x2d, A: 0xff}
)

// Register mounts GET /weather. fp is the forecast.Provider — typically
// cached(openmeteo) in production. aq is the airquality.Provider; eq is
// the earthquake.Provider. Pass airquality.NoopProvider() / earthquake.
// NoopProvider() to disable enrichment surfaces (tests, dev). All
// enrichment failures are best-effort: they drop their respective
// caption row but never 5xx the base render.
func Register(r gin.IRouter, fp forecast.Provider, aq airquality.Provider, eq earthquake.Provider) {
	h := &handler{forecast: fp, airquality: aq, earthquake: eq}
	r.GET("/weather", h.serve)
}

// handler holds the dependencies for /weather.
type handler struct {
	forecast   forecast.Provider
	airquality airquality.Provider
	earthquake earthquake.Provider
}

// serve resolves location + unit, fetches a forecast, and renders the
// negotiated response. See package doc for negotiation rules.
func (h *handler) serve(c *gin.Context) {
	lat, lon, location := h.resolveLocation(c)
	unit := resolveUnit(c)

	// Fan out the three independent upstreams concurrently. Forecast gates
	// the response; AQI + quake are best-effort enrichment whose errors
	// silently drop the respective caption row. Cold-cache p95 is now
	// max(t_forecast, t_aqi, t_quake) instead of the serial sum.
	ctx := c.Request.Context()
	var (
		f     forecast.Forecast
		err   error
		aqi   airquality.Reading
		aqErr error
		quake earthquake.Event
		qkErr error
		wg    sync.WaitGroup
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		f, err = h.forecast.Get(ctx, lat, lon, unit)
	}()
	go func() {
		defer wg.Done()
		aqi, aqErr = h.airquality.Get(ctx, lat, lon)
	}()
	go func() {
		defer wg.Done()
		quake, qkErr = h.earthquake.Get(ctx, lat, lon)
	}()
	wg.Wait()

	// cached.Provider returns (cachedForecast, err) on stale-serve and
	// (zero Forecast, err) past failureTTL. TempUnit is the "have data"
	// sentinel: it's set by openmeteo on every real Forecast and stays
	// "" on the zero value. Don't key on Temperature — 0°C is a valid
	// reading on snowy days.
	stale := err != nil && f.TempUnit != ""
	fresh := err == nil

	if !stale && !fresh {
		log.Warn().Err(err).Float64("lat", lat).Float64("lon", lon).
			Msg("weather upstream failed; no cached value")
		c.Header("Retry-After", "60")
		c.String(http.StatusServiceUnavailable, "weather unavailable\n")
		return
	}
	if stale {
		log.Warn().Err(err).Float64("lat", lat).Float64("lon", lon).
			Msg("weather upstream failed; serving stale cache")
	}

	bucket := BucketOf(f.WeatherCode, f.IsDay)
	word := Word(f.WeatherCode)

	if aqErr != nil {
		log.Debug().Err(aqErr).Float64("lat", lat).Float64("lon", lon).
			Msg("airquality upstream failed; omitting AQI row")
		aqi = airquality.Reading{}
	}
	if qkErr != nil {
		log.Debug().Err(qkErr).Float64("lat", lat).Float64("lon", lon).
			Msg("earthquake upstream failed; omitting quake row")
		quake = earthquake.Event{}
	}

	c.Header("Cache-Control", "public, max-age=600")

	headline := strconv.FormatFloat(f.Temperature, 'f', 1, 64)

	// text/pylon short-circuits to the bare banner source — captions and
	// STALE prefix are rendered metadata, not part of the parseable source.
	if strings.Contains(c.GetHeader("Accept"), "text/pylon") {
		c.Data(http.StatusOK, "text/pylon", []byte(headlineSource(headline)+"\n"))
		return
	}

	mode := middleware.GetMode(c)
	// Path 1 region-conditional CN/EN: TW visitors on the ASCII surface
	// get Chinese labels (terminal CJK fonts cover them); PNG path stays
	// English because basicfont.Face7x13 has zero CJK coverage and would
	// emit U+FFFD tofu. Same predicate /stock uses, kept local since it
	// composes mode + region in a service-specific way.
	lang := resolveCaptionLang(mode, middleware.GetCountry(c))
	captions := buildCaptions(f, word, location, stale, aqi, quake, lang)
	if mode == render.ModePNG {
		body, perr := composePNG(headline, Icon(bucket), captions)
		if perr == nil {
			c.Data(http.StatusOK, "image/png", body)
			return
		}
		log.Error().Err(perr).Msg("compose weather png")
	}

	c.Data(http.StatusOK, "text/plain; charset=utf-8",
		composeASCII(headline, Icon(bucket), captions))
}

// headlineSource is the pylon source rendered as the temperature banner.
// Single-line on purpose — captions are rendered separately so pylon's
// box stacking doesn't get confused by the longer caption rows. Uses
// pylon's `banner:mini` variant — the 3-row mini font balances better
// against the 5-row ASCII condition icon than the 6-row default ANSI
// shadow font.
func headlineSource(headline string) string {
	return fmt.Sprintf("[ %s | banner:mini ]", headline)
}

// resolveLocation returns the (lat, lon, locationLabel) the handler renders
// for. Priority order (first non-empty wins): validated ?lat=&lon= query,
// validated CF-IPLatitude/CF-IPLongitude headers, country-capital fallback.
// Validation failures silently fall through — never 4xx for malformed coords.
func (h *handler) resolveLocation(c *gin.Context) (float64, float64, string) {
	// 1. Query-string lat/lon. The label is the literal coords as a human
	// hint — without a city name, the operator/user supplied the coords
	// and can recognize them.
	if lat, lon, ok := parseLatLon(c.Query("lat"), c.Query("lon")); ok {
		return lat, lon, fmt.Sprintf("%.2f,%.2f", lat, lon)
	}

	// 2. Cloudflare visitor-location headers. Country code, when present,
	// names the location since we have no city name from CF here.
	if lat, lon, ok := parseLatLon(c.GetHeader("CF-IPLatitude"), c.GetHeader("CF-IPLongitude")); ok {
		return lat, lon, middleware.GetCountry(c)
	}

	// 3. Country → capital fallback.
	country := middleware.GetCountry(c)
	cap, ok := countryCapitals[country]
	if !ok {
		country = defaultCountry
		cap = countryCapitals[country]
	}
	return cap.lat, cap.lon, fmt.Sprintf("%s (%s)", cap.name, country)
}

// parseLatLon parses (latS, lonS) into (lat, lon) and returns ok=true only
// when both parse, both are finite, and both are in range. Any failure
// returns ok=false so the caller can fall through.
func parseLatLon(latS, lonS string) (float64, float64, bool) {
	if latS == "" || lonS == "" {
		return 0, 0, false
	}
	lat, err := strconv.ParseFloat(latS, 64)
	if err != nil {
		return 0, 0, false
	}
	lon, err := strconv.ParseFloat(lonS, 64)
	if err != nil {
		return 0, 0, false
	}
	if math.IsNaN(lat) || math.IsInf(lat, 0) || math.IsNaN(lon) || math.IsInf(lon, 0) {
		return 0, 0, false
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return 0, 0, false
	}
	return lat, lon, true
}

// resolveUnit picks the unit system. Explicit ?unit=c|f beats country
// defaults. Anything else falls through to imperial-or-metric by country.
func resolveUnit(c *gin.Context) forecast.Unit {
	if q := strings.ToLower(strings.TrimSpace(c.Query("unit"))); q != "" {
		switch q {
		case "f", "fahrenheit":
			return forecast.UnitImperial
		case "c", "celsius":
			return forecast.UnitMetric
		}
	}
	if imperialCountries[middleware.GetCountry(c)] {
		return forecast.UnitImperial
	}
	return forecast.UnitMetric
}

// captionLang switches the caption label set: English (PNG-safe ASCII)
// vs Chinese (TW-visitor ASCII surface only). Picked once per request
// from `resolveCaptionLang`.
type captionLang int

const (
	langEN captionLang = iota
	langCN
)

// resolveCaptionLang picks CN labels only when the visitor is on the
// ASCII surface from a TW region — PNG always stays English because
// basicfont.Face7x13 has zero CJK coverage. Pylon-source (Accept
// header) is handled before captions are built, so it isn't on this
// path.
func resolveCaptionLang(mode render.Mode, country string) captionLang {
	if mode != render.ModePNG && country == "TW" {
		return langCN
	}
	return langEN
}

// buildCaptions assembles the caption lines. STALE prefix lives only on
// the first line so the visual loudness sits next to the condition name.
// Numeric formatting rounds to int for legibility; the banner carries the
// precise temperature. The humidity/UV/precip line is omitted entirely
// when the upstream provided none of the three. AQI and quake rows are
// omitted when their respective upstreams failed. lang switches between
// English and TW Chinese label sets — see resolveCaptionLang.
func buildCaptions(f forecast.Forecast, word, location string, stale bool,
	aqi airquality.Reading, quake earthquake.Event, lang captionLang,
) []string {
	prefix := ""
	if stale {
		prefix = "STALE · "
	}
	titledWord := word
	if titledWord != "" {
		titledWord = strings.ToUpper(titledWord[:1]) + titledWord[1:]
	}
	feels := strconv.FormatFloat(math.Round(f.FeelsLike), 'f', 0, 64)
	wind := strconv.FormatFloat(math.Round(f.WindSpeed), 'f', 0, 64)
	high := strconv.FormatFloat(math.Round(f.HighToday), 'f', 0, 64)
	low := strconv.FormatFloat(math.Round(f.LowToday), 'f', 0, 64)

	out := []string{
		fmt.Sprintf("%s%s in %s", prefix, titledWord, location),
	}
	if lang == langCN {
		out = append(out,
			fmt.Sprintf("體感 %s%s  風速 %s %s", feels, f.TempUnit, wind, f.WindUnit),
			fmt.Sprintf("高 %s%s / 低 %s%s", high, f.TempUnit, low, f.TempUnit),
		)
	} else {
		out = append(out,
			fmt.Sprintf("feels %s%s  wind %s %s", feels, f.TempUnit, wind, f.WindUnit),
			fmt.Sprintf("high %s%s / low %s%s", high, f.TempUnit, low, f.TempUnit),
		)
	}
	if extras := buildExtrasLine(f, lang); extras != "" {
		out = append(out, extras)
	}
	// AQI row sits between extras and the day-cycle bar so the cycle
	// stays as the visual close of the block. Zero Category means the
	// AQI provider errored or returned no data — drop the row.
	if aqi.Category != "" {
		if lang == langCN {
			out = append(out, fmt.Sprintf("空污 %d (%s)", aqi.USAQI, aqiCategoryCN(aqi.Category)))
		} else {
			out = append(out, fmt.Sprintf("AQI %d (%s)", aqi.USAQI, aqi.Category))
		}
	}
	// Quake row sits below AQI — both are best-effort enrichment, so
	// keep them adjacent to make absence read as "enrichment was
	// unavailable" rather than "no quake near you specifically." Zero
	// Place means the upstream errored or had no qualifying event.
	if quake.Place != "" {
		out = append(out, formatQuakeRow(quake, lang))
	}
	// Day-cycle bar replaces the standalone "sunrise X  sunset Y" row —
	// the bar's HH:MM endpoints carry the same data with a glanceable
	// position marker for "where in the day are we right now."
	if cycle := dayCycleLocalized(f.AsOf, f.Sunrise, f.Sunset, lang); cycle != "" {
		out = append(out, cycle)
	} else {
		// Polar-night / pre-sunrise-data fallback: the bar collapses to ""
		// when the daylight window is empty/inverted; keep the literal
		// sunrise+sunset times so the row isn't lost entirely.
		if lang == langCN {
			out = append(out, fmt.Sprintf("日出 %s  日落 %s",
				render.TrimHour(f.Sunrise.Format("15:04")), render.TrimHour(f.Sunset.Format("15:04"))))
		} else {
			out = append(out, fmt.Sprintf("sunrise %s  sunset %s",
				render.TrimHour(f.Sunrise.Format("15:04")), render.TrimHour(f.Sunset.Format("15:04"))))
		}
	}
	return out
}

// dayCycleLocalized wraps render.DayCycle with the CN label "日" when
// the visitor's surface is the TW-ASCII Path 1. The bar glyphs and HH:MM
// endpoints are language-agnostic, so we only swap the leading word.
func dayCycleLocalized(asOf, sunrise, sunset time.Time, lang captionLang) string {
	bar := render.DayCycle(asOf, sunrise, sunset)
	if bar == "" || lang != langCN {
		return bar
	}
	return strings.Replace(bar, "day ", "日 ", 1)
}

// aqiCategoryCN maps EPA labels onto Taiwan EPA's standard 中文 categories.
// The CN side intentionally tracks the EN buckets one-to-one — visitors
// switching surfaces (curl from TW vs PNG via browser) should see the
// same severity reading.
func aqiCategoryCN(en string) string {
	switch en {
	case "Good":
		return "良好"
	case "Moderate":
		return "中等"
	case "USG":
		return "敏感族群"
	case "Unhealthy":
		return "不健康"
	case "Very Unhealthy":
		return "非常不健康"
	case "Hazardous":
		return "危害"
	default:
		return en
	}
}

// formatQuakeRow renders "M4.1 quake 9km ENE of Yilan (3h ago)" or its CN
// counterpart "M4.1 地震 ... (3h前)". USGS's place label is kept verbatim
// in both — re-translating it would lose the directional cue and risk
// mismatching visitor expectations. Magnitude rounds to 1 decimal; age
// is computed at format-time so the row stays honest across the cache
// TTL window without re-fetching.
func formatQuakeRow(q earthquake.Event, lang captionLang) string {
	age := humanizeAge(time.Since(q.Time))
	if lang == langCN {
		return fmt.Sprintf("M%.1f 地震 %s (%s前)", q.Magnitude, q.Place, age)
	}
	return fmt.Sprintf("M%.1f quake %s (%s ago)", q.Magnitude, q.Place, age)
}

// humanizeAge formats a duration as "5s" / "3m" / "2h" / "4d", picking
// the largest unit. Sub-second / negative durations collapse to "0s" so
// the caption never reads as a sentinel value.
func humanizeAge(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// buildExtrasLine renders "humidity 47%  UV 9  rain 20%" (or CN
// "濕度 47%  紫外線 9  降雨 20%") — each segment is included only when
// the upstream supplied the field. Returns "" when none of the three
// are present so the caller skips the row entirely.
func buildExtrasLine(f forecast.Forecast, lang captionLang) string {
	var parts []string
	if f.Humidity > 0 {
		if lang == langCN {
			parts = append(parts, fmt.Sprintf("濕度 %d%%", f.Humidity))
		} else {
			parts = append(parts, fmt.Sprintf("humidity %d%%", f.Humidity))
		}
	}
	if f.UVIndexMax > 0 {
		if lang == langCN {
			parts = append(parts, fmt.Sprintf("紫外線 %.0f", f.UVIndexMax))
		} else {
			parts = append(parts, fmt.Sprintf("UV %.0f", f.UVIndexMax))
		}
	}
	if f.PrecipProbability > 0 {
		if lang == langCN {
			parts = append(parts, fmt.Sprintf("降雨 %d%%", f.PrecipProbability))
		} else {
			parts = append(parts, fmt.Sprintf("rain %d%%", f.PrecipProbability))
		}
	}
	return strings.Join(parts, "  ")
}

// composeASCII lays the icon left of the banner, one row per banner row,
// with captions left-aligned to the banner's left edge below. The banner
// is 8 rows (top frame + 6 banner glyphs + bottom frame); the icon is 5
// rows, vertically centered with blank padding.
func composeASCII(headline string, icon, captions []string) []byte {
	bannerOut := strings.TrimRight(pylon.RenderASCII(pylon.Parse(headlineSource(headline))), "\n")
	bannerLines := strings.Split(bannerOut, "\n")

	// Center the 5-row icon against the 8-row banner — top blanks before,
	// bottom blanks after, with iconCells-wide padding so the columns
	// stay aligned.
	leadBlanks := (len(bannerLines) - len(icon)) / 2
	pad := strings.Repeat(" ", iconCells)
	gap := strings.Repeat(" ", iconGap)

	var b strings.Builder
	for i, br := range bannerLines {
		ii := i - leadBlanks
		if ii >= 0 && ii < len(icon) {
			b.WriteString(icon[ii])
		} else {
			b.WriteString(pad)
		}
		b.WriteString(gap)
		b.WriteString(br)
		b.WriteByte('\n')
	}
	captionIndent := strings.Repeat(" ", iconCells+iconGap)
	for _, cap := range captions {
		b.WriteString(captionIndent)
		b.WriteString(cap)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// pngCaptionSanitizer rewrites caption text for the PNG path. basicfont.Face7x13
// only covers ASCII (U+0020..U+007E) plus U+FFFD, so any other rune renders as
// the replacement glyph. The two non-ASCII chars `buildCaptions` produces are
// `°` (degree sign in temp units) and `·` (middle dot in the STALE prefix); both
// are substituted to ASCII-safe equivalents so PNG output stays glyph-clean.
// ASCII / pylon-source paths are unaffected — they keep the pretty Unicode form.
var pngCaptionSanitizer = strings.NewReplacer(
	"°", "",
	"·", "-",
)

// composePNG composes a horizontal hero: pylon-rendered banner image right
// of the basicfont icon, with caption lines drawn below at the banner's
// left edge. Mirrors notfound.composePNG but axis-flipped — captions sit
// under the row instead of under a single stacked image.
func composePNG(headline string, icon, captions []string) ([]byte, error) {
	bannerBytes, err := pylon.RenderPNG(pylon.Parse(headlineSource(headline)))
	if err != nil {
		return nil, fmt.Errorf("render banner png: %w", err)
	}
	bannerImg, err := png.Decode(bytes.NewReader(bannerBytes))
	if err != nil {
		return nil, fmt.Errorf("decode banner png: %w", err)
	}

	face := basicfont.Face7x13
	cellW := face.Advance
	lineH := face.Height
	ascent := face.Ascent

	iconW := iconCells * cellW
	iconH := len(icon) * lineH

	bannerW := bannerImg.Bounds().Dx()
	bannerH := bannerImg.Bounds().Dy()

	rowH := bannerH
	if iconH > rowH {
		rowH = iconH
	}

	captionsX := composePadding + iconW + composeGap
	captionsY := rowH + composePadding
	captionsH := len(captions)*lineH + composePadding

	canvasW := composePadding + iconW + composeGap + bannerW + composePadding
	canvasH := rowH + captionsH

	canvas := image.NewRGBA(image.Rect(0, 0, canvasW, canvasH))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{composeBG}, image.Point{}, draw.Src)

	drawer := &font.Drawer{
		Dst:  canvas,
		Src:  &image.Uniform{composeInk},
		Face: face,
	}

	// Icon left, vertically centered against rowH.
	iconY := (rowH - iconH) / 2
	for i, row := range icon {
		drawer.Dot = fixed.Point26_6{
			X: fixed.I(composePadding),
			Y: fixed.I(iconY + ascent + i*lineH),
		}
		drawer.DrawString(row)
	}

	// Banner pasted right of the icon, vertically centered (defensive —
	// won't trigger at current sizes since the banner is the taller of
	// the two).
	bannerY := (rowH - bannerH) / 2
	bannerX := composePadding + iconW + composeGap
	draw.Draw(canvas,
		image.Rect(bannerX, bannerY, bannerX+bannerW, bannerY+bannerH),
		bannerImg, image.Point{}, draw.Over)

	// Captions below, at the banner's left edge.
	for i, cap := range captions {
		drawer.Dot = fixed.Point26_6{
			X: fixed.I(captionsX),
			Y: fixed.I(captionsY + ascent + i*lineH),
		}
		drawer.DrawString(pngCaptionSanitizer.Replace(cap))
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}
	return buf.Bytes(), nil
}
