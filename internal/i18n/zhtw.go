package i18n

// catalogZhTW is the Traditional Chinese (zh-TW) catalog. Values use
// Taiwan financial-news conventions (漲跌家數, 融資餘額, 億, 萬張)
// rather than mainland equivalents — zh-CN diverges on script (融資 →
// 融资) and on a handful of terms (證券 → 证券, 戶 → 户).
//
// Latin-locked symbols (M5 / M10 markers and the 5↗10 / 5↘10 / ≈ trend
// glyphs) keep the same value across all locales by design; the marker
// comment in catalog.go locks them.
var catalogZhTW = Catalog{
	OHLCOpen:  "開",
	OHLCHigh:  "高",
	OHLCLow:   "低",
	OHLCClose: "收",
	OHLCPrev:  "昨",

	MA5Marker:     "M5",
	MA10Marker:    "M10",
	MAGoldenCross: "5↗10",
	MADeathCross:  "5↘10",
	MAFlat:        "≈",

	YearProgressLabel: "年度",
	StaleTag:          "舊資料",
	ClosedTag:         "已收盤",
	Separator:         "·",

	IndexTagline:               "一張圖看懂該知道的事",
	NowOGTitle:                 "imagelet",
	NowOGDescription:           "現在時間與年度進度",
	StockOGDescriptionPriceFmt: "%s @ %s (%s)",
	NotFoundTitle:              "找不到頁面",

	Weekdays: [7]string{
		"星期日", "星期一", "星期二", "星期三",
		"星期四", "星期五", "星期六",
	},

	HelpHeading: "快捷鍵",
	HelpEsc:     "關閉說明",
	HelpEscHint: "按 Esc 或點擊外部關閉",
	HelpPrev:    "前一天",
	HelpNext:    "後一天",
	HelpToday:   "今天",
	HelpToggle:  "顯示／隱藏說明",

	TWSEAdvDecRow:     "漲跌家數",
	TWSEMarginCredit:  "信用餘額",
	TWSEMarginBalance: "融資餘額",
	TWSEShortBalance:  "融券餘額",
	TWSEForeignNet:    "外資籌碼",
	TWSETrustNet:      "投信籌碼",
	TWSEDealerNet:     "自營籌碼",
	TWSETotalNet:      "合計籌碼",
	TWSESecLending:    "借券賣出",
	TWSETSE:           "上市",
	TWSEOTC:           "上櫃",
	TWSEMarginShort:   "融資",
	TWSEShortShort:    "融券",
	TWSEUnitYi:        "億",
	TWSEUnitWanZhang:  "萬張",
	TWSEUnitZhang:     "張",
	TWSEUnitKou:       "口",
	TWSEAdvLabel:      "漲",
	TWSEDecLabel:      "跌",
	TWSEUnchLabel:     "平",
	TWSERetailMXF:     "小台散戶",
	TWSERetailTMF:     "微台散戶",
	TWSEOptionsPCR:    "台指選擇",
	TWSEVIX:           "波動指數",
	TWSEHoldersBig:      "大戶",
	TWSEHoldersAll:      "總戶數",
	TWSEHoldersHold:     "持股",
	TWSEHoldersUnit:     "戶",
	TWSEBlockTrades:     "大宗交易",
	TWSEBlockTradesUnit: "筆",
	TWSEDividendYield:   "殖利率",
	TWSEListingPrefix:   "上市",
	TWSEForeignHolding:  "外資持股",
}
