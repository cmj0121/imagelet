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
外資籌碼  ░░░░░░░░░░│████████░░  +43.9B
投信籌碼  ░░░░░░░░░░│░░░░░░░░░░  +2.2B
自營籌碼  ░░░░░░░░░░│██░░░░░░░░  +8.9B
合計籌碼  ░░░░░░░░░░│██████████  +55.0B  ▲

信用餘額  融資 4,409億   融券 19.1萬張

大戶    1,721 戶  0.07%  ·  持股 86.36%  ·  ▲0.05pp
總戶數  2,519,187

大宗交易  2 筆  ·  2,000 張  ·  44.05 億

殖利率 0.98%  ·  PER 33.97  ·  PBR 10.77
```

Each group is separated by a zero-width-space row so pylon's row
parser keeps them distinct without trimming the gap.

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
