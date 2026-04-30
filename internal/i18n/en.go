package i18n

// catalogEN is the English (en) catalog. The source-of-truth for label
// keys: every other locale file must populate the same set of fields.
// TWSE-specific fields are intentionally empty — en mode strips those
// rows entirely (service/stock decides emission via locale check, not
// empty-string detection).
var catalogEN = Catalog{
	OHLCOpen:  "O",
	OHLCHigh:  "H",
	OHLCLow:   "L",
	OHLCClose: "C",
	OHLCPrev:  "P",

	MA5Marker:     "M5",
	MA10Marker:    "M10",
	MAGoldenCross: "5↗10",
	MADeathCross:  "5↘10",
	MAFlat:        "≈",

	YearProgressLabel: "year",
	StaleTag:          "STALE",
	ClosedTag:         "CLOSED",
	Separator:         "·",

	IndexTagline:               "show you should know in single image",
	NowOGTitle:                 "imagelet",
	NowOGDescription:           "Current time and year progress",
	StockOGDescriptionPriceFmt: "%s @ %s (%s)",
	NotFoundTitle:              "404 Not Found",

	Weekdays: [7]string{
		"Sunday", "Monday", "Tuesday", "Wednesday",
		"Thursday", "Friday", "Saturday",
	},

	HelpHeading: "Shortcuts",
	HelpEsc:     "Close help",
	HelpEscHint: "Press Esc or click outside to close",
	HelpPrev:    "Previous day",
	HelpNext:    "Next day",
	HelpToday:   "Today",
	HelpToggle:  "Toggle help",

	// TWSE-specific labels left empty. service/stock checks the
	// locale and skips these rows for LocaleEN, so these never reach
	// the renderer — the catalog satisfies the typed-struct invariant
	// without populating values that have no clean English equivalent.
}
