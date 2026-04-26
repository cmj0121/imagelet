// Package weather mounts the GET /weather service - rendering today's
// weather as a hero image (icon + temperature banner + captions).
//
// This file owns the condition-code translation: Open-Meteo's WMO
// weather codes (~30 values) collapse to 9 visual buckets for the
// ASCII icon, with a finer per-code word map preserving nuance in
// the caption text.
package weather

// Bucket is the visual category an Open-Meteo weather code falls into.
// One Bucket per ASCII icon; the icon is decorative, the word carries
// the precise condition.
type Bucket int

// Bucket values; one per ASCII icon glyph.
const (
	BucketClearDay Bucket = iota
	BucketClearNight
	BucketPartlyCloudy
	BucketCloudy
	BucketFog
	BucketDrizzle
	BucketRain
	BucketSnow
	BucketThunderstorm
)

// BucketOf returns the visual bucket for an Open-Meteo weather code
// and is_day flag. Codes 0 and 1 split day/night by isDay; everything
// else collapses to a single bucket regardless of time of day.
func BucketOf(code int, isDay bool) Bucket {
	switch code {
	case 0, 1:
		if isDay {
			return BucketClearDay
		}
		return BucketClearNight
	case 2:
		return BucketPartlyCloudy
	case 3:
		return BucketCloudy
	case 45, 48:
		return BucketFog
	case 51, 53, 55, 56, 57:
		return BucketDrizzle
	case 61, 63, 65, 66, 67, 80, 81, 82:
		return BucketRain
	case 71, 73, 75, 77, 85, 86:
		return BucketSnow
	case 95, 96, 99:
		return BucketThunderstorm
	default:
		return BucketCloudy
	}
}

// wordTable maps Open-Meteo WMO codes to short human strings used in
// the caption row. Kept narrower than ~12 chars so it fits a small
// banner without truncation.
var wordTable = map[int]string{
	0:  "clear",
	1:  "mainly clear",
	2:  "partly cloudy",
	3:  "overcast",
	45: "fog",
	48: "rime fog",
	51: "light drizzle",
	53: "drizzle",
	55: "heavy drizzle",
	56: "frz drizzle",
	57: "heavy frz drizzle",
	61: "light rain",
	63: "rain",
	65: "heavy rain",
	66: "frz rain",
	67: "heavy frz rain",
	71: "light snow",
	73: "snow",
	75: "heavy snow",
	77: "snow grains",
	80: "light showers",
	81: "showers",
	82: "heavy showers",
	85: "snow showers",
	86: "heavy snow showers",
	95: "thunderstorm",
	96: "thunderstorm w/ hail",
	99: "heavy thunderstorm",
}

// Word returns the human-readable condition for an Open-Meteo weather
// code. More nuanced than Bucket: drizzle / rain / heavy rain are all
// the rain BUCKET but distinct WORDS. Returns "unknown" if the code is
// not in the WMO table this package recognises.
func Word(code int) string {
	if w, ok := wordTable[code]; ok {
		return w
	}
	return "unknown"
}

// iconTable holds the 5-row by 12-column ASCII glyph for each bucket.
//
// Constraints (enforced by tests):
//   - exactly 5 rows
//   - every row exactly 12 bytes wide
//   - printable ASCII only (0x20..0x7E)
//   - no '(', ')', '[', ']' so the icon stays safe to pipe through
//     pylon banner rendering later
var iconTable = map[Bucket][]string{
	// Sun with rays.
	BucketClearDay: {
		`    \  |  / `,
		`     \ | /  `,
		`  ----O---- `,
		`     / | \  `,
		`    /  |  \ `,
	},
	// Crescent moon, leaning right.
	BucketClearNight: {
		`            `,
		`     ,--.   `,
		`    /   |   `,
		`    \   |   `,
		`     '--'   `,
	},
	// Small sun behind a cloud.
	BucketPartlyCloudy: {
		`   \  /     `,
		`  - O -     `,
		`   /  ,--.  `,
		`    .'    \ `,
		`     '----' `,
	},
	// Plain cloud.
	BucketCloudy: {
		`            `,
		`     ,--.   `,
		`   .'    '. `,
		`  |        |`,
		`   '------' `,
	},
	// Horizontal fog stripes.
	BucketFog: {
		`  _ _ _ _ _ `,
		`            `,
		`   _ _ _ _  `,
		`            `,
		`  _ _ _ _ _ `,
	},
	// Cloud with light dot drizzle.
	BucketDrizzle: {
		`     ,--.   `,
		`   .'    '. `,
		`  |________|`,
		`   ' ' ' '  `,
		`  ' ' ' ' ' `,
	},
	// Cloud with slashy rain.
	BucketRain: {
		`     ,--.   `,
		`   .'    '. `,
		`  |________|`,
		`   / / / /  `,
		`  / / / / / `,
	},
	// Cloud with snowflakes.
	BucketSnow: {
		`     ,--.   `,
		`   .'    '. `,
		`  |________|`,
		`   * * * *  `,
		`  * * * * * `,
	},
	// Cloud with zigzag bolts.
	BucketThunderstorm: {
		`     ,--.   `,
		`   .'    '. `,
		`  |________|`,
		`    /\  /\  `,
		`   /  \/  \ `,
	},
}

// Icon returns the 5-row by 12-column ASCII art glyph for the bucket.
// Each row is exactly 12 cells wide (space-padded). Suitable for
// placing to the left of a pylon banner output.
//
// The returned slice is the package-level glyph; callers must not
// mutate it.
func Icon(b Bucket) []string {
	if rows, ok := iconTable[b]; ok {
		return rows
	}
	return iconTable[BucketCloudy]
}
