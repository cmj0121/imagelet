package i18n

// catalogZhTW is the Traditional Chinese (zh-TW) catalog.
//
// TODO(H4): replace English placeholder values with real zh-TW
// translations. Skeleton committed in H1 so the typed-struct compile-
// time check covers all three locales from day one — H4 edits values,
// it does not add fields.
var catalogZhTW = Catalog{
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

	// TWSE-specific labels — H4 will fill these with real zh-TW
	// translations (漲跌家數 / 信用餘額 / 融資餘額 / etc.).
	TWSEAdvDecRow:     "AdvDec",
	TWSEMarginCredit:  "Credit",
	TWSEMarginBalance: "Margin balance",
	TWSEShortBalance:  "Short balance",
	TWSEForeignNet:    "Foreign net",
	TWSETrustNet:      "Trust net",
	TWSEDealerNet:     "Dealer net",
	TWSESecLending:    "Sec lending",
	TWSETSE:           "TSE",
	TWSEOTC:           "OTC",
	TWSEMarginShort:   "M",
	TWSEShortShort:    "S",
	TWSEUnitYi:        "100M",
	TWSEUnitWanZhang:  "10Klots",
	TWSEUnitZhang:     "lots",
}
