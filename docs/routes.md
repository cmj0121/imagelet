# Routes

| Method | Path             | Description                                                                |
| ------ | ---------------- | -------------------------------------------------------------------------- |
| `GET`  | `/`              | `IMAGELET` banner with tagline and `<repo> · <version>` caption.           |
| `GET`  | `/healthz`       | Returns `200 No Content`. Liveness probe — never renders, never allocates. |
| `GET`  | `/now`           | Banner-rendered current time with date / weekday / zone caption.           |
| `GET`  | `/stock`         | Banner-rendered regional stock-index quote.                                |
| `GET`  | `/stock/:symbol` | Banner-rendered quote for a caller-specified Yahoo symbol.                 |
| `*`    | _other_          | `404` banner above a fake Python traceback with the requested path inside. |

## Date override

Time-aware routes accept an optional `?date=YYYY-MM-DD` query parameter
to pin the response to a historical date. `/now?date=2012-02-02` keeps
the wall-clock HH:MM but shifts the calendar / weekday / year-progress
caption onto the requested day. `/stock?date=2012-02-02` loads the
latest stock data on or before that date (closest completed trading
session). Invalid dates — including values outside the
[now − 15y, now + 1d] clamp window — fall through silently to the
live path.

## `/stock/:symbol`

`/stock/:symbol` accepts any Yahoo symbol (e.g. `2330.TW`, `AAPL`,
`BRK-B`, `EURUSD=X`). Index symbols starting with `^` must be
percent-encoded — `/stock/%5EGSPC` for `^GSPC`. The TW enrichment
block (institutional flow / breadth / margin balance) activates by
symbol suffix (`.TW`, `.TWO`) or the `^TWII` index, independent of
the visitor's region. For `<id>.TW` symbols, the 三大法人 rows show
PER-STOCK flow (TWSE T86) instead of the market-wide aggregate.

See [stock-render.md](./stock-render.md) for the OHLC + MA bar
rendering details.
