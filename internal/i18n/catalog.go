package i18n

// Catalog is the per-locale string table. Adding a field forces every
// locale file (en.go / zhtw.go / zhcn.go) to compile-fix — that's the
// whole reason this is a typed struct rather than a map.
//
// Pure string table by design: rendering policies (e.g. "en strips
// TWSE-specific rows") live at the use site in service/stock, NOT as
// boolean flags here.
type Catalog struct {
	// Generic visualization labels.
	OHLCOpen, OHLCHigh, OHLCLow, OHLCClose, OHLCPrev string
	// MA5Marker / MA10Marker stay Latin across all locales — visual
	// stability beats localization here, and the trend token below
	// carries enough localization signal. Comment-locked so future
	// contributors don't "fix" them.
	MA5Marker, MA10Marker               string
	MAGoldenCross, MADeathCross, MAFlat string
	YearProgressLabel                   string
	StaleTag, ClosedTag                 string
	// Separator is exposed for symmetry — services that compose
	// "<label>: <value> · <label>: <value>" can pull it from the
	// catalog rather than hardcoding "·". Same value across locales
	// today; cheap to vary later if a locale prefers a different
	// separator (e.g. full-width "·" vs ASCII "·").
	Separator string

	// Service taglines and OG meta. StockOGDescriptionPriceFmt is a
	// fmt.Sprintf format string with %s placeholders for symbol,
	// formatted price, and formatted change.
	IndexTagline               string
	NowOGTitle                 string
	NowOGDescription           string
	StockOGDescriptionPriceFmt string
	NotFoundTitle              string

	// Weekdays holds the full weekday names indexed by time.Weekday()
	// where Sunday = 0 ... Saturday = 6. Used by /now's caption row.
	Weekdays [7]string

	// HTML keyboard help dialog.
	HelpHeading string
	HelpEsc     string
	HelpEscHint string
	HelpPrev    string
	HelpNext    string
	HelpToday   string
	HelpToggle  string

	// TWSE-specific labels. Empty in the en catalog — service/stock
	// gates emission on locale (showTWSEEnrichment), NOT on
	// empty-string detection of these fields.
	TWSEAdvDecRow     string
	TWSEMarginCredit  string
	TWSEMarginBalance string
	TWSEShortBalance  string
	TWSEForeignNet    string
	TWSETrustNet      string
	TWSEDealerNet     string
	TWSESecLending    string
	TWSETSE           string
	TWSEOTC           string
	TWSEMarginShort   string // 融資 — short form used inline in row composition
	TWSEShortShort    string // 融券 — short form used inline in row composition
	TWSEUnitYi        string // 億
	TWSEUnitWanZhang  string // 萬張
	TWSEUnitZhang     string // 張
}
