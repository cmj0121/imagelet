package weather

import "testing"

func TestBucketOf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		code  int
		isDay bool
		want  Bucket
	}{
		// Day/night split for clear codes.
		{"clear-day-0", 0, true, BucketClearDay},
		{"clear-night-0", 0, false, BucketClearNight},
		{"clear-day-1", 1, true, BucketClearDay},
		{"clear-night-1", 1, false, BucketClearNight},

		// Cloud progression.
		{"partly-cloudy", 2, true, BucketPartlyCloudy},
		{"partly-cloudy-night", 2, false, BucketPartlyCloudy},
		{"overcast", 3, true, BucketCloudy},

		// Fog.
		{"fog", 45, true, BucketFog},
		{"rime-fog", 48, false, BucketFog},

		// Drizzle.
		{"drizzle-light", 51, true, BucketDrizzle},
		{"drizzle-moderate", 53, true, BucketDrizzle},
		{"drizzle-dense", 55, true, BucketDrizzle},
		{"drizzle-frz-light", 56, true, BucketDrizzle},
		{"drizzle-frz-dense", 57, false, BucketDrizzle},

		// Rain (incl. showers and freezing).
		{"rain-light", 61, true, BucketRain},
		{"rain", 63, true, BucketRain},
		{"rain-heavy", 65, false, BucketRain},
		{"rain-frz-light", 66, true, BucketRain},
		{"rain-frz-heavy", 67, false, BucketRain},
		{"showers-light", 80, true, BucketRain},
		{"showers-moderate", 81, true, BucketRain},
		{"showers-violent", 82, true, BucketRain},

		// Snow.
		{"snow-light", 71, false, BucketSnow},
		{"snow", 73, false, BucketSnow},
		{"snow-heavy", 75, false, BucketSnow},
		{"snow-grains", 77, false, BucketSnow},
		{"snow-showers-slight", 85, false, BucketSnow},
		{"snow-showers-heavy", 86, false, BucketSnow},

		// Thunderstorm.
		{"thunder", 95, true, BucketThunderstorm},
		{"thunder-hail-slight", 96, false, BucketThunderstorm},
		{"thunder-hail-heavy", 99, true, BucketThunderstorm},

		// Unknown -> safe fallback.
		{"unknown-negative", -1, true, BucketCloudy},
		{"unknown-large", 999, false, BucketCloudy},
		{"unknown-gap", 50, true, BucketCloudy},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := BucketOf(tc.code, tc.isDay)
			if got != tc.want {
				t.Fatalf("BucketOf(%d, %v) = %d, want %d", tc.code, tc.isDay, got, tc.want)
			}
		})
	}
}

func TestWord(t *testing.T) {
	t.Parallel()

	cases := []struct {
		code int
		want string
	}{
		{0, "clear"},
		{1, "mainly clear"},
		{2, "partly cloudy"},
		{3, "overcast"},
		{45, "fog"},
		{51, "light drizzle"},
		{63, "rain"},
		{65, "heavy rain"},
		{73, "snow"},
		{82, "heavy showers"},
		{95, "thunderstorm"},
		{96, "thunderstorm w/ hail"},

		// Unknown codes fall back to "unknown".
		{-1, "unknown"},
		{4, "unknown"},
		{100, "unknown"},
	}

	for _, tc := range cases {
		got := Word(tc.code)
		if got != tc.want {
			t.Errorf("Word(%d) = %q, want %q", tc.code, got, tc.want)
		}
	}
}

// allBuckets enumerates every defined Bucket so the icon-shape tests
// fail loudly if a new bucket is added without an icon.
var allBuckets = []Bucket{
	BucketClearDay,
	BucketClearNight,
	BucketPartlyCloudy,
	BucketCloudy,
	BucketFog,
	BucketDrizzle,
	BucketRain,
	BucketSnow,
	BucketThunderstorm,
}

func TestIconShape(t *testing.T) {
	t.Parallel()

	for _, b := range allBuckets {
		rows := Icon(b)
		if len(rows) != 5 {
			t.Errorf("Icon(%d): got %d rows, want 5", b, len(rows))
			continue
		}
		for i, r := range rows {
			if len(r) != 12 {
				t.Errorf("Icon(%d) row %d: len=%d, want 12 (%q)", b, i, len(r), r)
			}
		}
	}
}

func TestIconASCIIOnly(t *testing.T) {
	t.Parallel()

	for _, b := range allBuckets {
		for i, r := range Icon(b) {
			for j := 0; j < len(r); j++ {
				c := r[j]
				if c < 0x20 || c > 0x7E {
					t.Errorf("Icon(%d) row %d col %d: byte 0x%02X not printable ASCII", b, i, j, c)
				}
			}
		}
	}
}

func TestIconNoPylonBrackets(t *testing.T) {
	t.Parallel()

	forbidden := "()[]"
	for _, b := range allBuckets {
		for i, r := range Icon(b) {
			for j := 0; j < len(r); j++ {
				c := r[j]
				for k := 0; k < len(forbidden); k++ {
					if c == forbidden[k] {
						t.Errorf("Icon(%d) row %d col %d: forbidden char %q in %q", b, i, j, c, r)
					}
				}
			}
		}
	}
}
