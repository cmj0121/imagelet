# ImageLet

> Show you should know in single image.

## Quickstart

A minimal Go web service that frames small bits of data as HTML / SVG / PNG / ASCII,
picked per request from the caller's `User-Agent` and an optional `?format=` override.

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

## Docker

```bash
make docker-build   # docker build -t imagelet:dev .
make docker-run     # docker run --rm -p 8080:8080 imagelet:dev
```

The image is multi-stage: `golang:1.25-bookworm` builder cross-compiles a static
binary, runtime is `gcr.io/distroless/static-debian12:nonroot`. Highlights:

- Distroless static base — no shell, no package manager, no glibc.
- Runs as `nonroot` (UID 65532).
- ~20 MB total. `tzdata` is embedded in the binary, so `time.LoadLocation` works
  without `/usr/share/zoneinfo` on the host.
- Multi-arch source — one Dockerfile, both `linux/amd64` and `linux/arm64`.

For hardened deployments, run with read-only rootfs — distroless static needs
no writable filesystem at runtime:

```bash
docker run --rm --read-only --tmpfs /tmp -p 8080:8080 imagelet:dev
```

Override the image tag:

```bash
DOCKER_IMAGE=myorg/imagelet:1.2.3 make docker-build
```

Build for both architectures (buildx; needs a registry to actually push):

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t imagelet:dev .
```

The image has no `HEALTHCHECK` directive — distroless lacks the shell and HTTP
client a probe would need, and orchestrators (Kubernetes, Compose, Fly machines,
...) supply their own. `GET /healthz` returns `200 No Content` and is the
intended liveness probe — it never renders, never reaches an upstream, never
allocates. (`GET /` is now a rendered landing page and unsuitable for high-rate
probes.)

### Image releases

Pre-built multi-arch images are published to `ghcr.io/cmj0121/imagelet` by
`.github/workflows/release.yml`:

```bash
docker pull ghcr.io/cmj0121/imagelet:latest
```

Available tags: `latest` (most recent semver release), `vX.Y.Z` (exact release),
`X.Y` (latest patch in that minor), `main-<sha>` (every push to `main`). Pin
something other than `latest` for production.

ghcr packages default to **private**. After the first publish, flip the package
visibility to public via the GitHub UI if you want unauthenticated `docker pull`
to work — this is a one-time per-package action, not workflow-controllable.

## Routes

| Method | Path       | Description                                                                |
| ------ | ---------- | -------------------------------------------------------------------------- |
| `GET`  | `/`        | `IMAGELET` banner with tagline and `<repo> · <version>` caption.           |
| `GET`  | `/healthz` | Returns `200 No Content`. Liveness probe — never renders, never allocates. |
| `GET`  | `/now`     | Banner-rendered current time with date / weekday / zone caption.           |
| `GET`  | `/stock`   | Banner-rendered regional stock-index quote.                                |
| `*`    | _other_    | `404` banner above a fake Python traceback with the requested path inside. |

`/` is the landing page. The version segment is stamped at link time via
`-ldflags="-X main.version=…"`; `make build VERSION=v0.1.0` produces a binary
that reports `v0.1.0`, the Dockerfile defaults to `docker`, and CI passes the
git tag (or `main-<sha>`). Body is constant for the binary's lifetime so the
response sets `Cache-Control: public, max-age=3600`.

```bash
curl http://localhost:8080/
```

```text
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

`/now` content-negotiates per request. Precedence (highest first):

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

SVG output ships with a GitHub-dark palette by default — `#0d1117`
background, `#c9d1d9` ink — for a tech-leaning rendered surface. The HTML
page wrapper uses the same colors so `?format=html` reads as a seamless
full-page render rather than a card floating on contrasting paper. PNG
and ASCII paths are unaffected; pylon's PNG keeps its own theme-locked
colors, terminals draw their own background.

Both rendered paths use pylon's native theme — Unicode frame plus ANSI Shadow block
letters — so the visual is identical across consumers. Two borderless caption
rows sit under the banner: a combined date+UTC and Sunday-first weekday strip
joined by a `·` middle-dot, and a 20-cell `█`/`░` year-progress bar. The
weekday strip uses angle brackets (`<M>`) instead of square brackets because
pylon's parser would treat literal `[M]` as a nested bordered-box and shred
the layout. Every response sets `Cache-Control: no-store`.

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

Browsers receive the same banner as an HTML page that inlines the SVG — a
single self-contained response, viewport-clean on mobile. Append
`?format=png` for raw `image/png` (pylon rasterizes pylon glyphs to a
self-contained payload) or `?format=svg` for a bare SVG document.

When the request carries Cloudflare's `CF-Timezone` header (e.g. `Asia/Taipei`),
`/now` renders in the caller's local zone — the subtitle's `UTC±H` offset shifts
accordingly. Missing or unparseable values fall back to the server's local zone.

`/stock` follows the same negotiation matrix as `/now` (raw pylon via
`Accept: text/pylon` or `?format=pylon`, `?format=html|svg|png|ascii`, HTML
for `Mozilla` user-agents, ASCII otherwise) but renders the caller's
regional stock index instead of the wall clock. The country comes from Cloudflare's `CF-IPCountry`
header and falls back to `US` when the header is missing or unrecognized; the
`?region=XX` query parameter overrides the header (handy for local dev and CI).
Region codes are two-letter ISO 3166-1 alpha-2, case-insensitive.

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
absorb traffic spikes (contrast with `/now`'s `no-store`). Content-Type
negotiation matches `/now`; `Accept: text/pylon` returns the raw banner source.

Failure modes:

- Upstream fetch fails AND no cached value: `503 Service Unavailable` with
  `Retry-After: 60` and a plain-text body `quote unavailable\n`.
- Upstream fetch fails AND cache hit: `200 OK` with the cached quote and a
  `STALE ·` prefix on the caption.

```bash
curl http://localhost:8080/stock                       # ASCII, default region (US -> ^GSPC)
curl 'http://localhost:8080/stock?region=TW'           # ASCII, override to TAIEX
curl -A 'Mozilla/5.0' -o stock.png http://localhost:8080/stock
```

The PNG variant uses `-o` because the body is binary; the `-A 'Mozilla/5.0'`
flag is what flips the negotiator into the PNG path.

Quotes are sourced from Yahoo Finance's unofficial v8 chart API. The provider is
hidden behind `quote.Provider`, so swapping in a different upstream is one file.
Yahoo's cache TTL is 60 s success / 10 m failure — short enough to track
intraday moves, long enough to avoid rate-limit pressure on the free endpoint.
The TW enrichment block (`service/stock/twse`) is on a much slower cycle:
TWSE publishes daily aggregates ~16:00 Asia/Taipei, so its cache uses a 4 h
success TTL with 30 m back-off on transient errors. The cache absorbs short
outages — see the failure modes above.

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

```bash
curl -s http://localhost:8080/no-such-route
```

```text
┌───────────────────────────────┐
│   ██╗  ██╗ ██████╗ ██╗  ██╗   │
│   ██║  ██║██╔═████╗██║  ██║   │
│   ███████║██║██╔██║███████║   │
│   ╚════██║████╔╝██║╚════██║   │
│        ██║╚██████╔╝     ██║   │
│        ╚═╝ ╚═════╝      ╚═╝   │
└───────────────────────────────┘
Traceback (most recent call last):
  File "/imagelet/server.py", line 88, in serve
    response = router.dispatch(request)
               ^^^^^^^^^^^^^^^^^^^^^^^^^
  File "/imagelet/router.py", line 127, in dispatch
    return self._routes[path](request)
                       ~~~~~~~~~~~~^^^
KeyError: '/no-such-route'

The above exception was the direct cause of the following exception:

Traceback (most recent call last):
  File "/imagelet/__main__.py", line 8, in <module>
    app.run(host="0.0.0.0", port=8080)
imagelet.errors.RouteNotFound: no handler for '/no-such-route'

path:   /no-such-route
method: GET
status: 404
```

The traceback is theatre — the file paths, line numbers, and chained exceptions
don't correspond to anything in the binary (imagelet is Go, not Python). The
requested path is the only real signal.

## Project layout

Top-level packages are importable; `internal/` is not used.

```text
cmd/imagelet/                    # binary entry point
server/                          # core router — middleware chain + GET /healthz
middleware/                      # reusable gin middlewares (ClientDetector + GetMode, RegionDetector + GetCountry)
render/                          # pylon-backed renderers — Box, Banner*, Mode, ProgressBar/YearProgress/WeekStrip, sanitizers
logger/                          # zerolog setup with TTY-aware console / JSON switching
service/index/                   # the GET / landing page (banner + tagline + repo · version)
service/now/                     # the /now plugin (Register + Handler)
service/stock/                   # the /stock plugin — regional index quote (Yahoo Finance + cache)
service/stock/twse/              # TW-only enrichment — 三大法人 + margin from TWSE legacy openapi
service/notfound/                # the 404 fallback — banner + fake Python traceback
```

Mount imagelet's pieces inside any gin-based application:

```go
import (
    "net/http"

    "github.com/cmj0121/imagelet/server"
    indexsvc "github.com/cmj0121/imagelet/service/index"
    notfoundsvc "github.com/cmj0121/imagelet/service/notfound"
    nowsvc "github.com/cmj0121/imagelet/service/now"
    stocksvc "github.com/cmj0121/imagelet/service/stock"
    "github.com/cmj0121/imagelet/service/stock/quote/cached"
    "github.com/cmj0121/imagelet/service/stock/quote/yahoo"
    "github.com/cmj0121/imagelet/service/stock/twse"
)

func main() {
    r := server.New()                                                                 // middleware + GET /healthz
    indexsvc.Register(r, "v0.2.0")                                                    // GET / (pass your binary's version)
    nowsvc.Register(r)                                                                // GET /now
    stocksvc.Register(r, cached.New(yahoo.New()), twse.NewCached(twse.New()))         // GET /stock + TW enrichment
    notfoundsvc.Register(r)                                                           // 404 fallback — install last
    http.ListenAndServe(":8080", r)
}
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
