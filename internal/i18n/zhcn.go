package i18n

// catalogZhCN is the Simplified Chinese (zh-CN) catalog. Values follow
// mainland-China financial-news conventions (融资余额, 涨跌家数 — though
// "家数" reads naturally in both scripts) and use simplified script for
// every CJK character. Where terminology diverges from Taiwan usage
// (e.g. 台指選擇 → 台指期权 for the options PCR row label), the zh-CN
// value uses the mainland term.
//
// Latin-locked symbols (M5 / M10 markers and the 5↗10 / 5↘10 / ≈ trend
// glyphs) stay identical to the en / zh-TW catalogs by design.
var catalogZhCN = Catalog{
	OHLCOpen:  "开",
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
	StaleTag:          "旧数据",
	ClosedTag:         "已收盘",
	Separator:         "·",

	IndexTagline:               "一张图看懂该知道的事",
	NowOGTitle:                 "imagelet",
	NowOGDescription:           "当前时间与年度进度",
	StockOGDescriptionPriceFmt: "%s @ %s (%s)",
	NotFoundTitle:              "找不到页面",

	Weekdays: [7]string{
		"星期日", "星期一", "星期二", "星期三",
		"星期四", "星期五", "星期六",
	},

	HelpHeading: "快捷键",
	HelpEsc:     "关闭说明",
	HelpEscHint: "按 Esc 或点击外部关闭",
	HelpPrev:    "前一天",
	HelpNext:    "后一天",
	HelpToday:   "今天",
	HelpToggle:  "显示／隐藏说明",

	TWSEAdvDecRow:     "涨跌家数",
	TWSEMarginCredit:  "信用余额",
	TWSEMarginBalance: "融资余额",
	TWSEShortBalance:  "融券余额",
	TWSEForeignNet:    "外资筹码",
	TWSETrustNet:      "投信筹码",
	TWSEDealerNet:     "自营筹码",
	TWSETotalNet:      "合计筹码",
	TWSESecLending:    "借券卖出",
	TWSETSE:           "上市",
	TWSEOTC:           "上柜",
	TWSEMarginShort:   "融资",
	TWSEShortShort:    "融券",
	TWSEUnitYi:        "亿",
	TWSEUnitWanZhang:  "万张",
	TWSEUnitZhang:     "张",
	TWSEUnitKou:       "口",
	TWSEAdvLabel:      "涨",
	TWSEDecLabel:      "跌",
	TWSEUnchLabel:     "平",
	TWSERetailMXF:     "小台散户",
	TWSERetailTMF:     "微台散户",
	TWSEOptionsPCR:    "台指期权",
	TWSEVIX:           "波动指数",
	TWSEHoldersBig:    "大户",
	TWSEHoldersAll:    "总户数",
	TWSEHoldersHold:   "持股",
	TWSEHoldersUnit:   "户",
}
