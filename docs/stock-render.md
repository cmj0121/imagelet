# Stock rendering

Each rendered quote shows: symbol + Yahoo short/long name as the
title, session volume + dollar volume + day high/low in the caption
rows, an OHLC range bar with a `▼` marker for the previous close,
an MA position bar showing where the current price sits relative to
the 5- and 10-day moving averages, and the four institutional rows
(when applicable).

Both bars are centered on the current price (`C` glyph at the
center column = the value the headline caption is reporting), with
each bar's per-side band fitted independently to its own markers —
so each half of the bar fills the width regardless of how lopsided
the data is. The OHLC bar's band tracks open / high / low; the MA
bar's band tracks MA5 / MA10. Lower / upper sides are each capped
at ±5% (anything past that clips to the edge with the `▶` / `◀`
saturation sentinels); there is no floor — the band shrinks to fit
even tight days, so a 0.1% high lands near the right edge instead
of clustering near center. On a gap-down day where low and open
sit near `-5%` while high is only modestly above price, the left
side stretches to fit the open / low pair and the right side
stretches independently to fit the high — both halves use the
bar's full width.

```text
                                ▼
L───────────────────O████C─────────────────────────────────────H─
O: 6,820.00 · C: 6,827.41 · P: 6,860.19
H: 6,895.00 · L: 6,810.50

                                ▼
─────────────────M10──M5────────C────────────────────────────────
M5: ▲6,825.00 · M10: ▲6,815.00 · 5↗10
```

## Glyph vocabulary

Consistent across both bars:

| Glyph      | Meaning                                        |
| ---------- | ---------------------------------------------- |
| `C`        | current close (always at the bar's center)     |
| `O`        | today's open (offset from `C` by % difference) |
| `▼`        | previous trading day's close (top row)         |
| `L` `H`    | session low / high (OHLC bar only)             |
| `M5` `M10` | MA5 / MA10 markers (MA bar only)               |
| `█`        | OHLC body fill, bullish (last >= open, 紅K)    |
| `░`        | OHLC body fill, bearish (last < open, 黑K)     |

Markers past the fitted band edge clip to the bar edge with `▶`
(right-clip) / `◀` (left-clip) saturation sentinels, with the marker
glyph itself bumped one column inward so both stay visible.

When today's open rounds to the same column as the current price
(near-doji: a quiet day where `open ≈ last`), the `O` glyph wins on
the shared column. The close value is still readable from the OCP
data row, so showing today's open on the bar is the more useful
read.

## Captions

Both bars carry left-anchored data rows beneath them: the OHLC bar
emits `O: <p> · C: <p> · P: <p>` and (when a session range is
known) `H: <p> · L: <p>`. The MA bar emits a single combined caption
`M5: ▲<v> · M10: ▲<v> · <trend>` — each MA carries a directional
arrow (`▲` price above the MA, `▼` below), and the trailing token is
the canonical golden-cross / death-cross hint: `5↗10` when MA5 sits
above MA10, `5↘10` when below, `≈` when the two MAs are within 0.1%
of each other. The whole MA block is omitted when the upstream
returns fewer than 5 closed sessions (newly listed stocks).

## Market-open semantics

When the market is open, today's intraday close is excluded from the
MA so the price-vs-MA arrow stays meaningful (otherwise the price
would be partly comparing against itself).

The OHLC bar suppresses the day's close while the session is still
open — the trading day hasn't finalized so there is no real close
yet. The `C` glyph drops from the OHLC bar (the center column stays
as wick `─`), the bullish/bearish body fill drops with it, and the
OCP data row renders `C: -` in place of the price. `O`, `H`, `L`,
and the `▼` previous-close marker all keep their values — they
remain valid intraday. The bar still mathematically centers on the
live price, so every surviving marker stays positioned correctly
relative to it.

The MA bar keeps its `C` glyph at the center column regardless of
market state. The MA bar's `C` is purely positional — the OHLC bar
above already carries `C: -` to signal that the close hasn't
finalized — and without it the bar collapses to floating MA labels
when both M5 / M10 cluster on the same side of price.

With the body fill gone, the OHLC bar gains a `⟦` / `⟧` frame just
outside the L and H markers so the day's range still reads as a
single bracketed span. The frame uses U+27E6 / U+27E7 (mathematical
white square brackets) — visually equivalent to literal `[` / `]`
but pylon-safe; literal brackets would re-frame the bar row as a
nested element. Saturated sides (where ◀ / ▶ already signal
"value off-screen") skip the bracket since there's no column for it
to occupy without erasing the sentinel.

```text
                                ▼
────────────────⟦L───O────────────H⟧────────────────────────
O: 4,460.00 · C: - · P: 4,450.00
H: 4,520.00 · L: 4,440.00
```

## Holders rows (TWSE only)

For per-stock TWSE views (`/stock/2330.TW`, `/stock/6488.TWO`, etc.)
the renderer appends two summary rows from the TDCC weekly
集保戶股權分散表 (shareholder dispersion table). Sourced once a week
from TDCC and reused as a parsed map across requests; one row per
stock survives into the rendered card:

```text
大戶    1,721 戶  0.07%  ·  持股 86.36%
總戶數  2,519,187
```

- **大戶 line**: count of accounts holding ≥800 k shares (the bucket
  combines TDCC tiers 14 and 15), the bucket's share of the total
  account population (`0.07%`), and its share of all custodied shares
  (`持股 86.36%`). The high concentration on TSMC is the canonical
  "institutional float" signal — answers "how concentrated is the
  ownership?" at a glance.
- **總戶數 line**: the absolute account count from TDCC's tier 17
  (合計). Useful for the "newly listed vs. established" comparison
  between symbols and for tracking issuance over time.

The 大戶 bucket combines tiers 14 (800 k–1 M shares) and 15 (>1 M
shares) — tier 15 alone underestimates concentration on
thinly-traded mid-caps where 800 k–1 M holders are still meaningful.
Bucketing 14+15 matches the convention used by Goodinfo and other
public 籌碼分析 surfaces. TDCC's tier 16 (差異數調整) is a settlement-
adjustment row, not a holder bucket — dropped at parse time.

### Update cadence

TDCC publishes once per week (typically the Thursday after the
target Friday's close), so the cached entry refreshes daily but the
underlying values move on a weekly cadence. Concentration drift is
slow enough that the rendered card stays meaningful intra-day; the
holders fetch is therefore the one TWSE row that does **not** gate on
`q.IsClosed` — the lending / margin / 三大法人 rows hide intra-day
to avoid showing yesterday's afterTrading numbers next to today's
live price, but holders is structurally weekly and stays visible.

### Locale gating

The 大戶 / 總戶數 rows render on `zh-TW` and `zh-CN`; `en` strips
them along with the rest of the TWSE block (see
[`docs/localization.md`](./localization.md) — there is no clean
English vocabulary for 集保戶 / 持股分級 / 大戶, so the rows are
suppressed rather than emitted as transliteration).

### `?date=` staleness gate

When `?date=` pins a historical OHLC bar more than 14 days off the
TDCC dump's published date, the holders rows are silently suppressed.
Stitching January's price action against April's dispersion is
misleading — especially on stocks where the holder base shifted
materially between the two dates (IPOs, secondary offerings,
buybacks). Within the 14-day window the rows render as usual; past
it, the renderer treats the data as if it were unavailable.

### Where the rows sit

The new rows sit beneath the existing per-stock TW enrichment groups
and above the 散戶 group on market-wide views:

```text
外資籌碼  ░░░░░░░░░░│████████░░  +9.0B
投信籌碼  ░░░░░░░░░░│███░░░░░░░  +3.3B
自營籌碼  ░░░░░░░░░░│░░░░░░░░░░  +0.3B
合計籌碼  ░░░░░░░░░░│██████████  +12.6B  ▲

融資餘額  26,840 張
融券餘額  119 張
借券賣出  4,492 張

大戶    1,721 戶  0.07%  ·  持股 86.36%  ·  ▲0.05pp
總戶數  2,519,187
大宗交易  10 筆  ·  4,309 張  ·  99.81 億
殖利率 0.95%  ·  PER 34.87  ·  PBR 11.05
半導體業  ·  上市 1994  ·  外資持股 70.66%  ·  業均 43.13%
2026/03 月營收 4,151.92 億  ·  YoY ▲45.19%
```

Groups are separated by zero-width-space rows so pylon's row parser
keeps them distinct without trimming the gap. The bottom block
(holders + 大宗交易 + 殖利率 + sector + 月營收) is rendered as a
SINGLE group — they're all per-stock slow-moving signals, and a
blank between every line was structural noise.

### Per-stock vs market scope

The per-stock card panel only carries rows about the queried
ticker. Market-wide rows (`漲跌家數` breadth, market 信用餘額,
市場 三大法人 fallback, 市場情緒 PCR/VIX, 散戶 retail futures)
render only on `/stock` (regional index); they're hidden on
`/stock/:symbol` views.

This means a per-stock view with no per-stock institutional flow
(delisted, OTC where T86 has no row) **omits the 三大法人 group
entirely** rather than substituting market-wide totals labelled
identically — a 6488.TWO card no longer shows `+46.4B 外資籌碼`
that's actually the TSE-wide aggregate. Same logic for credit
balance: per-stock view shows `融資餘額 / 融券餘額 / 借券賣出`
(in 張) only; market view shows `信用餘額 融資 4,409億 融券
19.1萬張` (in 億 / 萬張) only. No more dual blocks with similar
prefixes.

### Holders weekly Δ pill

The trailing token on the 大戶 line shows the week-over-week change
in the 持股% (concentration) since the prior TDCC publication:

- `▲X.XXpp` — concentration tightening (more institutional float)
- `▼X.XXpp` — concentration loosening (institutional distribution)
- `≈` — no material drift (|Δ| < 0.05pp)

The pill is omitted on cold start (no prior dump rotated through the
cache yet) and on a long pod restart where the previous-week dump
expired before save. Warm-up is one weekly publish cycle (≤7d).
Snapshot persistence (`--cache-dir`) extends Δ continuity across
short restarts; the previous-dump tracker mirrors the restored cache
entry so the next fresh fetch with a new AsOf rotates correctly.

### 大宗交易 row (TWSE only)

The per-stock 大宗交易 row aggregates all matched-trade events from
the latest BFIAUU snapshot for the resolved stock id. Three numbers:

- **Count** (`筆 / 笔`) — number of distinct block-trade events.
- **Volume** (`張 / 张`) — total share volume converted to 張
  (1 張 = 1,000 shares, TWSE convention).
- **Value** (`億 / 亿`) — total TWD value with 2dp.

The row renders intra-day too — block trades are reported as the
session runs, so the same-day file refreshes during open hours. Most
stocks have no block trades on most days; the row is silently
omitted in that branch (the most-common path).

Source: TWSE OpenAPI v1 `/exchangeReport/BFIAUU` (auth-free JSON,
single-day snapshot). Cache: publish-window-aware TTL via
`ttlForAsOf` — 30 minutes before 17:00 Asia/Taipei, 24h post-publish.

### 殖利率 / PER / PBR row (TWSE only)

The fundamentals row carries the per-stock daily snapshot of
dividend yield, P/E ratio, and P/B ratio:

```text
殖利率 0.98%  ·  PER 33.97  ·  PBR 10.77
```

Per-segment skip — each metric renders only when non-zero:

- A non-dividend-paying stock omits the `殖利率` prefix entirely
  (DividendYield = 0 from upstream "-" → 0).
- A loss-making stock omits PER (PERatio = 0).
- A row with no published metrics at all is skipped entirely.

`PER` and `PBR` labels stay Latin across both zh locales — both
acronyms are universal in TW + CN financial media, and keeping them
Latin keeps the row tight. Only the dividend-yield label localises
(殖利率 / 股息率).

Source: TWSE OpenAPI v1 `/exchangeReport/BWIBBU_d` (auth-free JSON).
The metrics are derived from published close + cumulative-12mo
dividend / EPS, so they're stable during the session and render
alongside the live price without intra-day flicker. Cache: same
publish-window TTL as 大宗交易.

### Sector + listing-year + 外資持股 row (TWSE only)

The per-stock context row combines three slow-moving signals:

```text
半導體業  ·  上市 1994  ·  外資持股 70.65%
```

Per-segment skip on missing data:

- **Sector tag** comes from TWSE's 上市公司基本資料 (`t187ap03_L`).
  The upstream returns 產業別 as a 2-character numeric code (e.g.
  `24`); a static 33-row map in the twse package resolves it to TW
  Traditional names (`半導體業`, `金融保險業`, etc.). Codes outside
  the map fall through to "" and that segment is skipped. zh-CN
  viewers see Traditional sector names — same compromise as
  upstream-provided 證券名稱 (we don't translate `鴻海` to `鸿海`
  either).
- **Listing year** is the 4-digit year from the same dataset's
  上市日期. Surfacing the year alone is enough context for the
  "established vs. recent IPO" read; the full date felt clunky.
- **外資持股** is the 全體外資及陸資持股比率% from the daily
  rwd `MI_QFIIS` endpoint. The renderer surfaces the headline
  ratio only — `AvailablePct` and `UpperlimitPct` stay on the
  struct for future use.

A stock with **none** of the four signals omits the row. ETFs
(0050, 006208) typically render only the foreign-holdings segment
because t187ap03_L is a companies-only file and ETFs are absent.

OTC stocks (上櫃) render the row via TPEx's parallel endpoint
(`mopsfin_t187ap03_O`). Industry-code numbering is shared between
TWSE and TPEx so the same `twseIndustryNames` static map resolves
6488 = 24 = 半導體業. Per-stock 外資持股 stays TWSE-only — TPEx
OpenAPI doesn't expose it — so the OTC row stops at sector +
上市 + 業均.

The trailing **業均 X%** segment is the industry-aggregate foreign
holdings from `MI_QFIIS_cat` (35-row daily aggregate), keyed by
the resolved sector NAME from listing-info. Hidden when per-stock
外資持股 is absent — without a per-stock comparand, the industry
mean reads as floating trivia. The lookup tolerates a `業`-suffix
mismatch between the per-stock sector name (e.g. `金融保險業`) and
the `MI_QFIIS_cat` name (`金融保險`); a few smaller industries
(電子商務, 文化創意業) aren't in the aggregate file at all and the
overlay segment silently omits.

Sources:

- `t187ap03_L` (TWSE-listed): single-key 24h cache.
- `mopsfin_t187ap03_O` (OTC): single-key 24h cache, parallel.
- `MI_QFIIS_cat` (industry aggregate): single-key 24h cache.
- `MI_QFIIS` (per-stock foreign): date-pinned legacy `rwd/zh/fund`
  with walkback like TWT93U / lending. TWSE-listed only.

### Monthly revenue row (TWSE + OTC)

Per-stock monthly operating-revenue row from TWSE's t187ap05_L
(上市) and TPEx's mopsfin_t187ap05_O (上櫃). Both endpoints
publish identical Chinese-keyed schema — one parser handles both.

```text
2026/03 月營收 4,151.92 億  ·  YoY ▲45.19%
```

Three signals from one upstream row:

- **Year-month**: ROC `資料年月` (`"11503"`) decoded to Gregorian
  `2026/03` via `rocYearMonthLabel`. ROC year + 1911 = Gregorian.
- **Revenue**: `當月營收` in 千元, multiplied × 1000 at parse time
  for raw NTD, then rendered in 億 (NTD 100M) units with 2dp.
- **YoY**: pre-computed by upstream as `去年同月增減(%)`. Direction
  rendered as `▲` for non-negative, `▼` for negative; magnitude
  is always the absolute value.

The row covers BOTH listed types — handler routes by symbol suffix
(.TW → TWSE, .TWO → TPEx). Revenue publishes monthly (~10th of the
following month for most listings); 24h cache TTL is plenty for
the monthly cadence.
