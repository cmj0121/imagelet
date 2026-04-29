# Stock rendering

Each rendered quote shows: symbol + Yahoo short/long name as the
title, session volume + dollar volume + day high/low in the caption
rows, an OHLC range bar with a `▼` marker for the previous close,
an MA position bar showing where the current price sits relative to
the 5- and 10-day moving averages, and the four institutional rows
(when applicable).

Both bars are centered on the current price (`C` glyph at the
center column = the value the headline caption is reporting), with
each bar's half-width pct band fitted to its own markers — so quiet
days fill the bar instead of clustering near center. The OHLC bar's
band tracks open / high / low / prev-close; the MA bar's band
tracks MA5 / MA10 / prev-close. Bands are floored at ±0.5% (so a
degenerate all-equal-to-price input still draws a usable bar) and
capped at ±5% (anything past that clips to the edge with the
`▶` / `◀` saturation sentinels):

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
