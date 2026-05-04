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
bar's band tracks MA5 / MA10. Lower / upper sides are each floored
at ±0.5% (so a degenerate all-equal-to-price input still draws a
usable bar) and capped at ±5% (anything past that clips to the edge
with the `▶` / `◀` saturation sentinels). On a gap-down day where
low and open sit near `-5%` while high is only modestly above
price, the left side stretches to fit the open / low pair and the
right side stretches independently to fit the high — both halves
use the bar's full width.

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
