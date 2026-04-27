package weather

import "testing"

// TestPNGCaptionSanitizer pins that the PNG-only caption rewriter strips the
// non-ASCII chars `buildCaptions` produces. basicfont.Face7x13 covers only
// ASCII + U+FFFD; any other rune renders as the replacement glyph in PNG. A
// regression here would silently re-introduce tofu in the rendered image.
func TestPNGCaptionSanitizer(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"feels 23°C  wind 12 km/h", "feels 23C  wind 12 km/h"},
		{"high 25°F / low 18°F", "high 25F / low 18F"},
		{"STALE · CLOUDY in Taipei (TW)", "STALE - CLOUDY in Taipei (TW)"},
		{"plain ascii passes through", "plain ascii passes through"},
	}
	for _, tc := range cases {
		got := pngCaptionSanitizer.Replace(tc.in)
		if got != tc.want {
			t.Errorf("Replace(%q) = %q, want %q", tc.in, got, tc.want)
		}
		for _, r := range got {
			if r >= 0x80 {
				t.Errorf("Replace(%q) leaked non-ASCII rune %q (U+%04X)", tc.in, r, r)
			}
		}
	}
}
