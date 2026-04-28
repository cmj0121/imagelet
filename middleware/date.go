package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"
)

// dateOverrideKey is the gin-context key under which DateOverrideDetector
// stashes the parsed override time. The "imagelet." prefix namespaces it so
// it cannot collide with keys set by other middlewares running on the same
// engine.
const dateOverrideKey = "imagelet.date.override"

// dateOverrideQueryParam is the query string key callers use to pin a
// time-aware endpoint to a historical date — e.g. /now?date=2012-02-02.
const dateOverrideQueryParam = "date"

// dateOverrideLayout is the strict format DateOverrideDetector accepts.
// Anything else (timestamps, RFC 3339, US-style) falls through silently
// so an unrelated query parameter can't accidentally be mis-parsed.
const dateOverrideLayout = "2006-01-02"

// DateOverrideDetector returns a gin middleware that parses an optional
// ?date=YYYY-MM-DD query parameter and stashes the resolved *time.Time*
// on the gin context for handlers to consume. Handlers retrieve the
// override with GetDate (or compute the effective wall-clock with NowFor).
//
// The override is interpreted in the caller's resolved location (from
// TimezoneDetector) at noon — so /now's date math, /stock's lookback walk,
// and any other downstream consumer agree on which calendar day was
// requested. Behavior:
//
//   - missing or empty ?date=         → nothing stashed; GetDate returns false
//   - valid YYYY-MM-DD                → parsed time stashed at noon in loc
//   - unparseable / wrong format      → nothing stashed (logged at debug);
//     GetDate returns false. Never 4xx — matches the project's existing
//     fall-through pattern for ?format= and ?region=.
//
// MUST run AFTER TimezoneDetector so the date string is anchored to the
// caller's location instead of time.Local. Order is enforced by where
// server.New() registers the middleware in the chain.
func DateOverrideDetector() gin.HandlerFunc {
	return func(c *gin.Context) {
		if v := c.Query(dateOverrideQueryParam); v != "" {
			loc := GetLocation(c)
			t, err := time.ParseInLocation(dateOverrideLayout, v, loc)
			if err == nil {
				c.Set(dateOverrideKey, t.Add(12*time.Hour))
			} else if e := log.Debug(); e.Enabled() {
				e.Str("date", v).Msg("invalid date override")
			}
		}
		c.Next()
	}
}

// GetDate returns the date override stashed by DateOverrideDetector for
// the current request. The bool reports whether an override was set;
// callers MUST gate on it before reading the time, since the zero value
// otherwise looks like January 1, year 1.
func GetDate(c *gin.Context) (time.Time, bool) {
	v, ok := c.Get(dateOverrideKey)
	if !ok {
		return time.Time{}, false
	}
	t, ok := v.(time.Time)
	if !ok || t.IsZero() {
		return time.Time{}, false
	}
	return t, true
}

// NowFor returns the effective "now" timestamp for the current request.
// When no ?date= override is set, it returns time.Now() in the caller's
// resolved location — equivalent to time.Now().In(GetLocation(c)).
//
// When an override is present, NowFor reprojects the wall-clock H/M/S/ns
// of the real current moment onto the override's Y/M/D in the resolved
// location. Using time.Date directly (rather than AddDate) avoids
// accumulating DST drift across long offsets — a request with date set
// to twenty years ago should still report the same hour-of-day as the
// real clock.
//
// Handlers that build their entire timestamp from a single Now() call
// can swap to NowFor without further changes; weekday strips, year-
// progress bars, and date captions all derive from the returned time.
func NowFor(c *gin.Context) time.Time {
	loc := GetLocation(c)
	now := time.Now().In(loc)
	override, ok := GetDate(c)
	if !ok {
		return now
	}
	return time.Date(
		override.Year(), override.Month(), override.Day(),
		now.Hour(), now.Minute(), now.Second(), now.Nanosecond(),
		loc,
	)
}
