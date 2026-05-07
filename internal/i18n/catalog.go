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
	TWSETotalNet      string // 合計籌碼 — 三大法人 net summary
	TWSESecLending    string
	TWSETSE           string // 上市 — TWSE listed (open-market breadth row)
	TWSEOTC           string // 上櫃 — TPEx listed (open-market breadth row)
	TWSEMarginShort   string // 融資 — short form used inline in row composition
	TWSEShortShort    string // 融券 — short form used inline in row composition
	TWSEUnitYi        string // 億
	TWSEUnitWanZhang  string // 萬張
	TWSEUnitZhang     string // 張
	TWSEUnitKou       string // 口 — futures-lot suffix
	TWSEAdvLabel      string // 漲 / 涨 — breadth count prefix (advance)
	TWSEDecLabel      string // 跌 — breadth count prefix (decline)
	TWSEUnchLabel     string // 平 — breadth count prefix (unchanged)
	TWSERetailMXF     string // 小台散戶 / 小台散户 — retail futures (mini-TWII)
	TWSERetailTMF     string // 微台散戶 / 微台散户 — retail futures (micro-TWII)
	TWSEOptionsPCR    string // 台指選擇 / 台指期权 — TAIFEX options PCR row
	TWSEVIX           string // 波動指數 / 波动指数 — Taiwan VIX row
	// TDCC 集保戶股權分散表 — per-stock weekly shareholder dispersion.
	// Holders rows render only when the resolved per-stock dump exists
	// AND the staleness gate against ?date= passes (see twse.HoldersFreshFor).
	TWSEHoldersBig  string // 大戶 / 大户 — concentration tier label (≥800k shares = TDCC tiers 14+15)
	TWSEHoldersAll  string // 總戶數 / 总户数 — headline 合計 (TDCC tier 17) holder count
	TWSEHoldersHold string // 持股 / 持股 — share-of-shares prefix on the concentration line
	TWSEHoldersUnit string // 戶 / 户 — account count unit suffix
}
