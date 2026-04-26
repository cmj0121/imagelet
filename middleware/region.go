package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
)

// countryKey is the gin-context key under which RegionDetector stores
// the resolved 2-letter country code. The "imagelet." prefix namespaces
// it so it cannot collide with keys set by other middlewares running on
// the same engine.
const countryKey = "imagelet.region.country"

// cfIPCountryHeader is the request header Cloudflare sets when IP
// Geolocation is enabled on the zone. Value is an ISO 3166-1 alpha-2
// country code like "TW", "US", "JP". Cloudflare uses "XX" for "anonymous
// proxy / unknown" — RegionDetector stashes those verbatim and lets the
// handler decide whether to map them to a default.
const cfIPCountryHeader = "CF-IPCountry"

// defaultCountry is what GetCountry returns when no header was set or the
// middleware was not installed. US is the largest English-speaking market
// and the obvious sensible fallback for a service that mostly shows
// English-locale content.
const defaultCountry = "US"

// RegionDetector returns a gin middleware that resolves a 2-letter country
// code from the request's CF-IPCountry header and stashes it on the gin
// context. Handlers retrieve it with GetCountry.
//
// Behavior:
//   - missing or empty CF-IPCountry → nothing stashed; GetCountry falls
//     back to defaultCountry ("US")
//   - any non-empty value           → uppercased + trimmed, stashed
//
// The middleware does NOT validate the value against an ISO list or a
// known-symbols set — that's the handler's responsibility (it has the
// country→symbol map). The middleware is stateless and safe to install on
// multiple engines.
func RegionDetector() gin.HandlerFunc {
	return func(c *gin.Context) {
		if v := strings.ToUpper(strings.TrimSpace(c.GetHeader(cfIPCountryHeader))); v != "" {
			c.Set(countryKey, v)
			if e := log.Debug(); e.Enabled() {
				e.Str("cf_ipcountry", v).Msg("region classified")
			}
		}
		c.Next()
	}
}

// GetCountry returns the 2-letter country code chosen by RegionDetector for
// the current request. If RegionDetector was not installed or the header
// was missing, GetCountry returns "US" — handlers can always call it
// without a nil check or empty-string check.
func GetCountry(c *gin.Context) string {
	v, ok := c.Get(countryKey)
	if !ok {
		return defaultCountry
	}
	country, ok := v.(string)
	if !ok || country == "" {
		return defaultCountry
	}
	return country
}
