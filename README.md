# ImageLet

> Show you should know in single image.

A minimal Go web service that frames small bits of data as HTML / SVG / PNG / ASCII,
picked per request from the caller's `User-Agent` and an optional `?format=` override.

## Quickstart

```bash
make build   # compile binary to bin/imagelet
make run     # run on 0.0.0.0:8080 with info log level
make test    # run unit tests
```

```text
$ imagelet --help
Usage: imagelet [flags]

imagelet HTTP service.

Flags:
  -h, --help              Show context-sensitive help.
  -H, --host="0.0.0.0"    Host address to bind.
  -p, --port=8080         TCP port to listen on.
  -v, --verbose           Increase log verbosity (-v for debug, -vv for trace).
```

## Routes

| Method | Path             | Description                                                                |
| ------ | ---------------- | -------------------------------------------------------------------------- |
| `GET`  | `/`              | `IMAGELET` banner with tagline and `<repo> · <version>` caption.           |
| `GET`  | `/healthz`       | Returns `200 No Content`. Liveness probe — never renders, never allocates. |
| `GET`  | `/now`           | Banner-rendered current time with date / weekday / zone caption.           |
| `GET`  | `/stock`         | Banner-rendered regional stock-index quote.                                |
| `GET`  | `/stock/:symbol` | Banner-rendered quote for a caller-specified Yahoo symbol.                 |
| `*`    | _other_          | `404` banner above a fake Python traceback with the requested path inside. |

Time-aware routes accept an optional `?date=YYYY-MM-DD` query parameter
to pin the response to a historical date. `/now?date=2012-02-02` keeps
the wall-clock HH:MM but shifts the calendar / weekday / year-progress
caption onto the requested day. `/stock?date=2012-02-02` loads the
latest stock data on or before that date (closest completed trading
session). Invalid dates fall through silently to the live path.

`/stock/:symbol` accepts any Yahoo symbol (e.g. `2330.TW`, `AAPL`,
`BRK-B`, `EURUSD=X`). Index symbols starting with `^` must be
percent-encoded — `/stock/%5EGSPC` for `^GSPC`. The TW enrichment
block (institutional flow / breadth / margin balance) activates by
symbol suffix (`.TW`, `.TWO`) or the `^TWII` index, independent of
the visitor's region. For `<id>.TW` symbols, the 三大法人 rows show
PER-STOCK flow (TWSE T86) instead of the market-wide aggregate.

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
O: 6,860.19 · C: 6,827.41 · P: 6,820.00
H: 6,895.00 · L: 6,810.50

                                ▼
─────────────────M10──M5────────C────────────────────────────────
M5: ▲6,830.00 · M10: ▼6,815.00 · 5↗10
```

Glyph vocabulary (consistent across both bars):

| Glyph      | Meaning                                        |
| ---------- | ---------------------------------------------- |
| `C`        | current close (always at the bar's center)     |
| `O`        | today's open (offset from `C` by % difference) |
| `▼`        | previous trading day's close (top row)         |
| `L` `H`    | session low / high (OHLC bar only)             |
| `M5` `M10` | MA5 / MA10 markers (MA bar only)               |
| `█`        | OHLC body fill, bullish (last >= open, 紅K)    |
| `░`        | OHLC body fill, bearish (last < open, 黑K)     |

Markers outside the ±3% band clip to the bar edge with `▶` (right-
clip) / `◀` (left-clip) saturation sentinels, with the marker glyph
itself bumped one column inward so both stay visible.

Both bars now carry left-anchored data rows beneath them: the OHLC
bar emits `O: <p> · C: <p> · P: <p>` and (when a session range is
known) `H: <p> · L: <p>`. The MA bar emits a single combined caption
`M5: ▲<v> · M10: ▲<v> · <trend>` — each MA carries a directional
arrow (`▲` price above the MA, `▼` below), and the trailing token is
the canonical golden-cross / death-cross hint: `5↗10` when MA5 sits
above MA10, `5↘10` when below, `≈` when the two MAs are within 0.1%
of each other. The whole MA block is omitted when the upstream
returns fewer than 5 closed sessions (newly listed stocks).

When the market is open, today's intraday close is excluded from the
MA so the price-vs-MA arrow stays meaningful (otherwise the price
would be partly comparing against itself).

HTML responses carry Open Graph + Twitter Card meta tags so links
shared on Slack / Discord / Twitter / Facebook unfurl with a title,
description, and preview image. The `og:image` URL points at the
same page's SVG render (`?format=svg`); Discord, Slack, and Telegram
display SVG previews, while Twitter and Facebook fall back to the
plain link if their crawler can't decode SVG. Switching the preview
to PNG (universal compatibility) is a one-line query change.

### Keyboard shortcuts (HTML view)

The /stock HTML view ships an inline JS handler that rewrites the
`?date=YYYY-MM-DD` parameter on these keys:

| Key       | Action                             |
| --------- | ---------------------------------- |
| `←` / `h` | Previous day                       |
| `→` / `l` | Next day                           |
| `t`       | Today (clear the `date=` override) |
| `?`       | Toggle the shortcut help overlay   |
| `Esc`     | Close the help overlay             |

On-screen prev / next chevron buttons sit at the left/right edges of
the viewport for mouse and touch users — clicking them is equivalent
to pressing the matching arrow key. Held arrow keys are short-
circuited via `e.repeat` so a long press doesn't slam the server.

Date navigation steps by calendar day; weekend / holiday quotes
fall back to the previous trading session server-side. /now, /, and
the 404 view do not register the handlers — the bindings only attach
on /stock.

### `/`

```text
$ curl http://localhost:8080/
┌──────────────────────────────────────────────────────────────────────┐
│   ██╗███╗   ███╗ █████╗  ██████╗ ███████╗██╗     ███████╗████████╗   │
│   ██║████╗ ████║██╔══██╗██╔════╝ ██╔════╝██║     ██╔════╝╚══██╔══╝   │
│   ██║██╔████╔██║███████║██║  ███╗█████╗  ██║     █████╗     ██║      │
│   ██║██║╚██╔╝██║██╔══██║██║   ██║██╔══╝  ██║     ██╔══╝     ██║      │
│   ██║██║ ╚═╝ ██║██║  ██║╚██████╔╝███████╗███████╗███████╗   ██║      │
│   ╚═╝╚═╝     ╚═╝╚═╝  ╚═╝ ╚═════╝ ╚══════╝╚══════╝╚══════╝   ╚═╝      │
└──────────────────────────────────────────────────────────────────────┘
        show you should know in single image
        github.com/cmj0121/imagelet · v0.1.0
```

### `/now`

```text
$ curl http://localhost:8080/now
┌───────────────────────────────────────────┐
│    ██╗ █████╗          ██████╗  █████╗    │
│   ███║██╔══██╗   ██   ██╔═████╗██╔══██╗   │
│   ╚██║╚██████║   ██   ██║██╔██║╚██████║   │
│    ██║ ╚═══██║        ████╔╝██║ ╚═══██║   │
│    ██║ █████╔╝   ██   ╚██████╔╝ █████╔╝   │
│    ╚═╝ ╚════╝    ██    ╚═════╝  ╚════╝    │
└───────────────────────────────────────────┘
     2026-04-27 UTC+8 · S <M> T W T F S
        year ██████░░░░░░░░░░░░░░ 32%
```

### `/stock`

```text
$ curl http://localhost:8080/stock
   ┌───────────────────────────────────────────────────────────┐
   │   ███████╗ █████╗  ██████╗  ██████╗    ██╗  ██╗██████╗    │
   │   ██╔════╝██╔══██╗██╔═████╗██╔═████╗   ██║  ██║╚════██╗   │
   │   ███████╗╚█████╔╝██║██╔██║██║██╔██║   ███████║ █████╔╝   │
   │   ╚════██║██╔══██╗████╔╝██║████╔╝██║   ╚════██║██╔═══╝    │
   │   ███████║╚█████╔╝╚██████╔╝╚██████╔╝██╗     ██║███████╗   │
   │   ╚══════╝ ╚════╝  ╚═════╝  ╚═════╝ ╚═╝     ╚═╝╚══════╝   │
   └───────────────────────────────────────────────────────────┘
                  S & P 500 · United States
           ^GSPC ▲ +0.35% 5,800.42 USD 2026-04-25
```

### `*` (404 fallback)

```text
$ curl http://localhost:8080/no-such-route
┌───────────────────────────────┐
│   ██╗  ██╗ ██████╗ ██╗  ██╗   │
│   ██║  ██║██╔═████╗██║  ██║   │
│   ███████║██║██╔██║███████║   │
│   ╚════██║████╔╝██║╚════██║   │
│        ██║╚██████╔╝     ██║   │
│        ╚═╝ ╚═════╝      ╚═╝   │
└───────────────────────────────┘
Traceback (most recent call last):
  File "/imagelet/router.py", line 127, in dispatch
    return self._routes[path](request)
KeyError: '/no-such-route'

path:   /no-such-route
method: GET
status: 404
```

## Built with

| Library                                    | Role                                                    |
| ------------------------------------------ | ------------------------------------------------------- |
| [gin](https://github.com/gin-gonic/gin)    | HTTP router                                             |
| [kong](https://github.com/alecthomas/kong) | CLI flag parsing                                        |
| [zerolog](https://github.com/rs/zerolog)   | Structured logging                                      |
| [pylon](https://github.com/cmj0121/pylon)  | ASCII / PNG / SVG rendering (HTML wraps the SVG output) |

Pylon is pinned via a Go pseudo-version of the `v0.5.0` tag's commit `93b11e6bbcff`;
the module path `github.com/cmj0121/pylon/src/go` is a nested go.mod, so `@latest`
resolves the canonical pinned commit.

## License

Source-available under the [PolyForm Noncommercial License 1.0.0](./LICENSE).
Free for personal, research, and noncommercial use. Commercial use is not permitted —
please contact the author if you need a commercial license.

## DDD (Dream-Driven Development)

This project follows the DDD (Dream-Driven Development) methodology, which means the project
is driven by what I envision.

All features are based on my needs and my dreams.
