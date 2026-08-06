# Routes

- `GET /` — `IMAGELET` banner with tagline and `<repo> · <version>` caption.
- `GET /healthz` — returns `200 No Content`. Liveness probe — never renders, never allocates.
- `GET /favicon.ico` — multi-resolution (16/32/48) ICO of the brand mark, baked into the binary.
- `GET /favicon.svg` — SVG brand mark; referenced by `<link rel="icon">` in HTML responses.
- `GET /now` — banner-rendered current time with date / weekday / zone caption.
- `GET /stock` — banner-rendered regional stock-index quote.
- `GET /stock/:symbol` — banner-rendered quote for a caller-specified Yahoo symbol.
- `GET /qr` — QR code encoding `?text=` (default `https://imglet.sh`); `?ec=L|M|Q|H`.
- `GET /sysinfo` — banner card with hostname, OS, kernel, CPU, RAM, uptime, and load
  average. **Disabled by default** — requires `--sysinfo` flag; see
  [`docs/security.md`](./security.md#sysinfo--opt-in-infrastructure-disclosure).
- `GET /metrics` — banner card with per-route request counts since startup.
  **Disabled by default** — requires `--metrics` flag. `/healthz` and
  `/robots.txt` are registered before the counter middleware and are not
  counted; see
  [`docs/security.md`](./security.md#metrics--opt-in-route-analytics).
- `GET /github/:user` — banner card for a GitHub user / org login.
- `GET /github/:user/:repo` — banner card for a GitHub `owner/name` public repository.
- `GET /dns/:hostname` — banner card for a public hostname's DNS records
  (A / AAAA / CNAME / MX / NS / TXT / SOA / CAA / SRV).
- `*` (other) — `404` banner above a fake Python traceback with the requested path inside.

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
block (institutional spot flow / breadth / margin balance, plus
三大法人 and 散戶 futures positioning on the market-wide view)
activates by
symbol suffix (`.TW`, `.TWO`) or the `^TWII` index, independent of
the visitor's region. For `<id>.TW` symbols, the 三大法人 rows show
PER-STOCK flow (TWSE T86) instead of the market-wide aggregate.

See [stock-render.md](./stock-render.md) for the OHLC + MA bar
rendering details.

## `/github/:user` and `/github/:user/:repo`

Banner cards rendered from public GitHub data: profile (login, name,
bio, followers / public repos, location, joined month) and repo
(description, stars / forks / open issues, language · license ·
default branch, last-pushed relative time, latest release tag). Logins
must match GitHub's documented charset; private repos always render
as 404. The banner is text-only — avatars are deferred indefinitely.

See [github.md](./github.md) for the field-by-field card layout, the
glyph substitution table applied to caption text, the per-status
`Cache-Control` matrix, and the `GITHUB_TOKEN` posture.

## `/dns/:hostname`

`/dns/:hostname` resolves a public hostname against a DNS-over-TLS
recursive resolver (Cloudflare 1.1.1.1 by default) and renders the
populated record types as a banner card. Records covered: A, AAAA,
CNAME, MX, NS, TXT (prefix-classified summary), SOA, CAA, SRV, plus a
`DNSSEC ✓` row when the upstream's AD bit is set.

Hostnames are normalized (lowercased, trailing-dot trimmed,
non-ASCII inputs run through `idna.Lookup.ToASCII`). Leading
underscore per label is allowed so `_dmarc.example.com`,
`_acme-challenge.example.com`, and `_sip._tcp.example.com` work.
Reserved suffixes (`.local`, `.localhost`, `.internal`, `.lan`,
`.example`, `.test`, `.invalid`, `.arpa`) and IP literals are
refused with `400 Bad Request`.

See [dns.md](./dns.md) for the full record-type list, the per-status
`Cache-Control` matrix, the private-IP filter caveat, and the
`DNS_RESOLVER` / `DNS_RESOLVER_SNI` posture.
