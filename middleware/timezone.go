package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
)

// locationKey is the gin-context key under which TimezoneDetector stores the
// resolved *time.Location. The "imagelet." prefix namespaces it so it cannot
// collide with keys set by other middlewares running on the same engine.
const locationKey = "imagelet.location"

// cfTimezoneHeader is the request header Cloudflare sets when the
// "IP Geolocation" feature is enabled on a zone. Value is an IANA zone
// name like "America/Los_Angeles" or "Asia/Taipei".
//
// Other CDNs that expose timezone data (e.g. Fastly) use different header
// names — wire each one explicitly here when adding support.
const cfTimezoneHeader = "CF-Timezone"

// TimezoneDetector returns a gin middleware that resolves a *time.Location
// from the request's CF-Timezone header and stashes it on the gin context.
// Handlers retrieve it with GetLocation.
//
// Behavior:
//   - missing or empty CF-Timezone → nothing stashed; GetLocation falls back
//     to time.Local
//   - valid IANA zone               → parsed *time.Location stashed
//   - unparseable value             → nothing stashed (and logged at debug);
//     GetLocation falls back to time.Local
//
// The middleware is stateless and safe to install on multiple engines.
func TimezoneDetector() gin.HandlerFunc {
	return func(c *gin.Context) {
		if name := c.GetHeader(cfTimezoneHeader); name != "" {
			if loc, err := time.LoadLocation(name); err == nil {
				c.Set(locationKey, loc)
			} else if e := log.Debug(); e.Enabled() {
				e.Str("cf_timezone", name).Err(err).Msg("invalid timezone")
			}
		}
		c.Next()
	}
}

// GetLocation returns the *time.Location chosen by TimezoneDetector for the
// current request. If TimezoneDetector was not installed or the header was
// missing / unparseable, GetLocation returns time.Local — handlers can
// always call time.Now().In(GetLocation(c)) safely without a nil check.
func GetLocation(c *gin.Context) *time.Location {
	v, ok := c.Get(locationKey)
	if !ok {
		return time.Local
	}
	loc, ok := v.(*time.Location)
	if !ok || loc == nil {
		return time.Local
	}
	return loc
}
