<p align="center">
  <img src="./assets/logo.svg" alt="imagelet" width="128" height="128">
</p>

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
      --cache-dir=STRING  Persist per-stock + TAIFEX cache snapshots
                          to this directory; restored on startup, saved
                          on graceful shutdown. Empty (default) disables
                          disk persistence.
```

### Environment variables

- `GITHUB_TOKEN` (default: unset) — optional. When set, raises the
  per-process `/github/*` upstream cap from 60 to 5000 requests per hour.

The `GITHUB_TOKEN` value is read only from the environment — never
from a CLI flag — so it stays out of `ps -ef` and shell history. See
[`docs/github.md`](./docs/github.md) for the full posture.

## Routes

- `GET /` — `IMAGELET` banner with tagline and `<repo> · <version>` caption.
- `GET /healthz` — returns `200 No Content`. Liveness probe — never renders, never allocates.
- `GET /favicon.ico` — multi-resolution (16/32/48) ICO of the brand mark, baked into the binary.
- `GET /favicon.svg` — SVG brand mark; referenced by `<link rel="icon">` in HTML responses.
- `GET /now` — banner-rendered current time with date / weekday / zone caption.
- `GET /stock` — banner-rendered regional stock-index quote.
- `GET /stock/:symbol` — banner-rendered quote for a caller-specified Yahoo symbol.
- `GET /qr` — QR code encoding `?text=` (default `https://imglet.sh`); `?ec=L|M|Q|H`.
- `GET /github/:user` — banner card for a GitHub user / org login.
- `GET /github/:user/:repo` — banner card for a GitHub `owner/name` public repository.
- `*` (other) — `404` banner above a fake Python traceback with the requested path inside.

Detailed docs live under [`docs/`](./docs):

- [`docs/routes.md`](./docs/routes.md) — `?date=` overrides, `/stock/:symbol` selection.
- [`docs/stock-render.md`](./docs/stock-render.md) — OHLC + MA bars, glyph vocabulary, market-open semantics.
- [`docs/caching.md`](./docs/caching.md) — caching layers and `--cache-dir` disk persistence.
- [`docs/security.md`](./docs/security.md) — request-size caps, `?date=` clamp, SSRF guards, snapshot decode cap.
- [`docs/html-view.md`](./docs/html-view.md) — Open Graph meta and `/stock` keyboard shortcuts.
- [`docs/localization.md`](./docs/localization.md) — locale negotiation, `?lang=` override, Vary policy.
- [`docs/qr.md`](./docs/qr.md) — `/qr` parameters, error-correction levels, ASCII scannability caveat.
- [`docs/github.md`](./docs/github.md) — `/github/:user` and
  `/github/:user/:repo` cards, glyph substitutions, `GITHUB_TOKEN` posture.

## Result

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

## Localization

Imagelet renders financial-card output in three locales: English (`en`),
Traditional Chinese (`zh-TW`), and Simplified Chinese (`zh-CN`). Japanese
is deferred — the embedded Sarasa Mono SC font has incomplete kana
coverage, so `?lang=ja` falls back to `en` (NOT `zh-TW`; Japanese readers
prefer English over the wrong CJK script).

The locale is resolved per request, in this order:

1. `?lang=` query parameter — `en`, `zh`, `zh-TW`, `zh-Hant`, `zh-CN`,
   `zh-Hans` (case-insensitive). Bare `zh` resolves to `zh-CN` per CLDR
   convention.
2. `Accept-Language` request header — parsed via
   `golang.org/x/text/language.NewMatcher` over the supported set.
3. `CF-IPCountry` request header — TW / HK / MO → `zh-TW`; CN / SG →
   `zh-CN`; everything else falls through.
4. Fallback: `en`.

| Locale              | BCP-47 tag(s) accepted   | Country defaults              |
| ------------------- | ------------------------ | ----------------------------- |
| English             | `en`                     | (anything not below; default) |
| Traditional Chinese | `zh-TW`, `zh-Hant`       | TW, HK, MO                    |
| Simplified Chinese  | `zh-CN`, `zh-Hans`, `zh` | CN, SG                        |

Bare `zh` collapses onto `zh-CN` because the Unicode CLDR project treats
simplified as the script-default for ambiguous `zh` inputs. Visitors who
want traditional must say `zh-TW` or `zh-Hant`.

```bash
# explicit override
curl https://imagelet.example.com/stock/2330.TW?lang=zh-TW

# browser-style negotiation
curl -H 'Accept-Language: zh-TW' https://imagelet.example.com/stock/2330.TW

# CDN geo default
curl -H 'CF-IPCountry: TW' https://imagelet.example.com/stock/2330.TW
```

Behavioral notes:

- `en` visitors see only the generic OHLC + MA card. The TWSE-specific
  enrichment rows (`漲跌家數`, `融資`, `融券`, `外資籌碼`, …) are
  stripped — there's no clean English vocabulary for several of those
  business terms, and emitting them transliterated would be worse than
  omitting them.
- `zh-TW` and `zh-CN` visitors see the full TWSE enrichment with
  localized labels (Traditional / Simplified script respectively).
- `Vary: Accept-Language` is set **only** when `Accept-Language`
  actually picked the locale. `?lang=` overrides skip the Vary header
  (the URL itself differentiates), and so do CF-IPCountry-only matches.
  This bounds CDN cache fragmentation.
- The HTML response cache key includes the resolved locale, so three
  locales × URL = at most 3× the working set. The LRU is sized
  accordingly.
- `/qr` and `/github/*` are locale-agnostic — the rendered bytes are
  the same for every locale at the same path. Locale resolution still
  runs (so `htmlcache` keys stay project-wide locale-aware), but the
  output does not vary on it.

See [`docs/localization.md`](./docs/localization.md) for the full
behavior spec and the Cloudflare cache-key normalization guidance.

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
