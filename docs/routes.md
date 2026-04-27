# Routes

Detailed per-route behavior, content-negotiation rules, region resolution, cache
policies, and failure modes. The README's per-route examples show the shape of
each response; this document covers the contract.

## Content negotiation

All rendered routes negotiate per request. Precedence (highest first):

- `Accept: text/pylon` OR `?format=pylon` — raw pylon source, so callers can
  render it themselves. Both spellings exist for header / query parity.
- `?format=html` — `text/html; charset=utf-8`. A self-contained HTML5 page
  with the SVG inlined in the body and a viewport meta so a top-level
  navigation reads cleanly on mobile.
- `?format=svg` — `image/svg+xml`. RFC 7303 doesn't define a `charset`
  parameter for this media type, and iOS Safari downloads the response
  instead of rendering it when the parameter is present. The SVG body
  carries its own encoding via the XML stream, so the parameter is also
  redundant.
- `?format=png` — `image/png`.
- `?format=ascii` — `text/plain; charset=utf-8`.
- `User-Agent` contains `Mozilla` — `text/html; charset=utf-8` (HTML
  wrapping inline SVG, same body as `?format=html`).
- Anything else — `text/plain; charset=utf-8`.

`?format=` overrides the User-Agent default; bad or unrecognized values fall
through silently to UA-based negotiation (never `4xx`). Third-party sites
that previously embedded imagelet via `<img src="…">` should append
`?format=png` (or `?format=svg`) to keep the raw-image contract — the UA
classifier alone can't distinguish a top-level navigation from an image
sub-request, and now defaults to HTML for `Mozilla`.

## Theme

SVG output ships with a GitHub-dark palette by default — `#0d1117`
background, `#c9d1d9` ink — for a tech-leaning rendered surface. The HTML
page wrapper uses the same colors so `?format=html` reads as a seamless
full-page render rather than a card floating on contrasting paper. PNG
and ASCII paths are unaffected; pylon's PNG keeps its own theme-locked
colors, terminals draw their own background.

Both rendered paths use pylon's native theme — Unicode frame plus ANSI Shadow
block letters — so the visual is identical across consumers.

## `/`

Landing page. The version segment is stamped at link time via
`-ldflags="-X main.version=…"`; `make build VERSION=v0.1.0` produces a binary
that reports `v0.1.0`, the Dockerfile defaults to `docker`, and CI passes the
git tag (or `main-<sha>`). Body is constant for the binary's lifetime so the
response sets `Cache-Control: public, max-age=3600`.

## `/now`

Two borderless caption rows sit under the time banner: a combined date+UTC and
Sunday-first weekday strip joined by a `·` middle-dot, and a 20-cell `█`/`░`
year-progress bar. The weekday strip uses angle brackets (`<M>`) instead of
square brackets because pylon's parser would treat literal `[M]` as a nested
bordered-box and shred the layout. Every response sets `Cache-Control: no-store`.

When the request carries Cloudflare's `CF-Timezone` header (e.g. `Asia/Taipei`),
`/now` renders in the caller's local zone — the subtitle's `UTC±H` offset shifts
accordingly. Missing or unparseable values fall back to the server's local zone.

## `/stock`

Renders the caller's regional stock index. The country comes from Cloudflare's
`CF-IPCountry` header and falls back to `US` when the header is missing or
unrecognized; the `?region=XX` query parameter overrides the header (handy for
local dev and CI). Region codes are two-letter ISO 3166-1 alpha-2,
case-insensitive.

| Country           | Symbol   | Index              |
| ----------------- | -------- | ------------------ |
| TW                | `^TWII`  | TAIEX              |
| US                | `^GSPC`  | S&P 500            |
| JP                | `^N225`  | Nikkei 225         |
| HK                | `^HSI`   | Hang Seng          |
| GB                | `^FTSE`  | FTSE 100           |
| DE                | `^GDAXI` | DAX                |
| _other / missing_ | `^GSPC`  | (default fallback) |

The render is a price banner with borderless caption rows stacked underneath
(matching `/now`'s layout):

- Index name header (`TAIEX · Taiwan`, `S&P 500 · United States`, …).
- Symbol + arrow + signed pct + price + currency + date caption. Two prefixes
  can appear: `CLOSED ·` outside trading hours, `STALE ·` when the upstream
  fetch failed and the response is being served from cache. `STALE ·` wins
  over `CLOSED ·` — data integrity beats market-state hints.
- For TW visitors: a thin `─ ─ ─ ─` divider, then 三大法人 institutional
  flow (外資 / 投信 / 自營 / 合計) and 融資/融券 margin balance rows
  sourced from TWSE's legacy openapi. Region-conditional CN/EN labels split
  by surface: plain text (ASCII + `text/pylon`, both terminal-shaped) gets
  Chinese; banner-and-font surfaces (HTML, SVG, PNG) get English so the
  visual output is consistent. PNG can't help it (`basicfont.Face7x13` has
  zero CJK coverage and would render tofu); SVG and HTML join PNG by
  choice. The TW block is best-effort enrichment — TWSE upstream errors
  silently omit it without affecting the base render.

Every rendered response sets `Cache-Control: public, max-age=60`, so a CDN can
absorb traffic spikes (contrast with `/now`'s `no-store`).

Failure modes:

- Upstream fetch fails AND no cached value: `503 Service Unavailable` with
  `Retry-After: 60` and a plain-text body `quote unavailable\n`.
- Upstream fetch fails AND cache hit: `200 OK` with the cached quote and a
  `STALE ·` prefix on the caption.

Quotes are sourced from Yahoo Finance's unofficial v8 chart API. The provider is
hidden behind `quote.Provider`, so swapping in a different upstream is one file.
Yahoo's cache TTL is 60 s success / 10 m failure — short enough to track
intraday moves, long enough to avoid rate-limit pressure on the free endpoint.
The TW enrichment block (`service/stock/twse`) is on a much slower cycle:
TWSE publishes daily aggregates ~16:00 Asia/Taipei, so its cache uses a 4 h
success TTL with 30 m back-off on transient errors.

## `*` (404 fallback)

Unmatched paths fall through to a `404` page: pylon-rendered `404` banner stacked
above a fake Python traceback with the requested path injected into the panic
message and the trailing field. ASCII and PNG paths show the full content —
the PNG is composed locally (pylon banner above, traceback drawn with
`basicfont` below) because pylon's parser would shred the trace's parens and
brackets. `Accept: text/pylon` (or `?format=pylon`) returns the bare banner
source. `?format=svg` and `?format=html` return the banner-only SVG / HTML
respectively — no traceback prose, since the trace can't be pylon-parsed and
an SVG-side basicfont compositor isn't built (v1 limitation, lower priority
than per-route format support).

The traceback is theatre — the file paths, line numbers, and chained exceptions
don't correspond to anything in the binary (imagelet is Go, not Python). The
requested path is the only real signal.
