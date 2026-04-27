# ImageLet

> Show you should know in single image.

## Quickstart

A minimal Go web service that frames small bits of data as ASCII or PNG, picked per request
based on the caller's `User-Agent`.

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
| `GET`  | `/weather` | Today's weather: ASCII condition icon + temperature banner + captions.     |
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

- `Accept: text/pylon` — raw pylon source, so callers can render it themselves.
- `?format=svg` — `image/svg+xml`. RFC 7303 doesn't define a `charset`
  parameter for this media type, and iOS Safari downloads the response
  instead of rendering it when the parameter is present. The SVG body
  carries its own encoding via the XML stream, so the parameter is also
  redundant.
- `?format=png` — `image/png`.
- `User-Agent` contains `Mozilla` — `image/png`.
- Anything else — `text/plain; charset=utf-8`.

`?format=` overrides the User-Agent default; bad or unrecognized values fall
through silently to UA-based negotiation (never `4xx`). `?format=ascii` is
not supported in v1 — the UA fallback already serves plain text to non-browser
clients.

Both rendered paths use pylon's native theme — Unicode frame plus ANSI Shadow block
letters — so the visual is identical across consumers. The subtitle carries the
date, weekday abbreviation, UTC offset, and a 20-cell `█`/`░` year-progress bar
so a glance tells you how far through the year `now` is. Every response sets
`Cache-Control: no-store`.

```text
$ curl http://localhost:8080/now
   ┌─────────────────────────────────────────────┐
   │    ██╗  ██╗  ██╗  ██╗  █████╗  ██████╗      │
   │   ███║ ███║  ██║  ██║ ██╔══██╗ ██╔════╝     │
   │   ╚██║ ╚██║  ███████║ ███████║ ███████╗     │
   │    ██║  ██║  ╚════██║ ██╔══██║ ██╔═══██╗    │
   │    ██║  ██║       ██║ ██║  ██║ ╚██████╔╝    │
   │    ╚═╝  ╚═╝       ╚═╝ ╚═╝  ╚═╝  ╚═════╝     │
   └─────────────────────────────────────────────┘
       2026-04-27 MON UTC+8 · year ██████░░░░░░░░░░░░░░ 32%
```

Browsers receive the same banner as a PNG (pylon rasterizes pylon glyphs to a self-contained
`image/png` payload).

When the request carries Cloudflare's `CF-Timezone` header (e.g. `Asia/Taipei`),
`/now` renders in the caller's local zone — the subtitle's `UTC±H` offset shifts
accordingly. Missing or unparseable values fall back to the server's local zone.

`/stock` follows the same negotiation matrix as `/now` (raw `text/pylon`,
`?format=svg|png`, PNG for `Mozilla` user-agents, ASCII otherwise) but renders
the caller's regional stock index instead of the wall clock. The country comes from Cloudflare's `CF-IPCountry`
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

The render is a single bordered box (V1 layout) with multiple rows stacked
inside the price banner's outer frame:

- Index name header (`TAIEX · Taiwan`, `S&P 500 · United States`, …).
- Symbol + arrow + signed pct + price + currency + date caption. Two prefixes
  can appear: `CLOSED ·` outside trading hours, `STALE ·` when the upstream
  fetch failed and the response is being served from cache. `STALE ·` wins
  over `CLOSED ·` — data integrity beats market-state hints.
- Day-range progress bar showing where the current price falls between the
  intraday high and low (omitted gracefully if Yahoo skipped the field).
- 52-week-range progress bar — same shape, year-scale span.
- For TW visitors: a thin `─ ─ ─ ─` divider, then 三大法人 institutional
  flow (外資 / 投信 / 自營 / 合計) and 融資/融券 margin balance rows
  sourced from TWSE's legacy openapi. Region-conditional CN/EN labels split
  by surface: plain text (ASCII + `text/pylon`, both terminal-shaped) gets
  Chinese; banner-and-font surfaces (PNG and SVG) get English so the visual
  output is consistent. PNG can't help it (`basicfont.Face7x13` has zero CJK
  coverage and would render tofu); SVG joins PNG by choice. The TW block
  is best-effort enrichment — TWSE upstream errors silently omit it
  without affecting the base render.

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

`/weather` follows the `/now` and `/stock` shape with a slight twist: an ASCII
condition icon is composed to the LEFT of the temperature banner (rendered with
pylon's compact `banner:mini` font for icon-balance), with up to seven caption
lines below carrying the supporting numbers:

1. Condition + location (with `STALE ·` prefix on cache-fallback responses).
2. Feels-like temperature + wind speed.
3. Today's high / low.
4. Humidity + UV index + rain probability (segments dropped when the upstream
   skipped the field; the whole row collapses if all three are absent).
5. AQI + EPA category — sourced from Open-Meteo's free `/v1/air-quality`
   endpoint. Failures silently drop the row.
6. Recent significant earthquake within 300 km — `M4.1 quake 9 km ENE of
Yilan, Taiwan (3h ago)` from USGS's `fdsnws/event/1/query`. Failures or
   no qualifying event silently drop the row.
7. Day-cycle progress bar — `day ████░░░░ 5:42-18:24` — shows where the
   current time falls in the daylight window (`render.DayCycle`).

The PNG path composes the same layout locally with `basicfont` for the icon
and pylon's PNG for the banner — same trick `/404` uses, axis flipped. CN/EN
labels split by surface (same rule as `/stock`): plain text gets Chinese
(體感, 風速, 高/低, 濕度, 紫外線, 降雨, 空污, 地震, 日); banner-and-font
surfaces (PNG, and SVG-coerced-to-PNG) stay English so the visual output
is consistent across devices.

`/weather` honors `?format=png` like the other routes but coerces
`?format=svg` to PNG in v1 — the icon-left composition uses a `basicfont`
compositor that the pylon SVG renderer can't reproduce, and a vertical-stack
fallback would visibly regress on the iPhone surface. Revisit when an SVG
icon-compositor exists.

Location resolution priority (first non-empty wins):

1. `?lat=<f>&lon=<f>` query — explicit coords. Validated for finiteness and
   range; bad values silently fall through (never `4xx`).
2. Cloudflare's `CF-IPLatitude` / `CF-IPLongitude` headers (separate Managed
   Transform from `CF-IPCountry` and `CF-Timezone` — operators must enable
   "Add visitor location headers" to surface lat/lon).
3. Country → capital fallback keyed by `CF-IPCountry`:

| Country           | Capital   | Lat / Lon          |
| ----------------- | --------- | ------------------ |
| TW                | Taipei    | 25.04, 121.56      |
| US                | New York  | 40.71, -74.01      |
| JP                | Tokyo     | 35.68, 139.69      |
| HK                | Hong Kong | 22.32, 114.17      |
| GB                | London    | 51.51, -0.13       |
| DE                | Berlin    | 52.52, 13.40       |
| _other / missing_ | New York  | (default fallback) |

Units default by country — `US`, `LR`, `MM` (the three imperial holdouts) get
Fahrenheit + mph; everywhere else gets Celsius + km/h. `?unit=c` or
`?unit=f` overrides.

Cache: 10-minute success + 10-minute failure TTL. Past 10 minutes of upstream
failure the handler emits `503 Service Unavailable` with `Retry-After: 60`
instead of continuing to serve stale data — weather staleness is a correctness
hazard, unlike stock quotes after market close. Within the failure window, the
cached forecast is served with a `STALE ·` prefix on the condition caption.
Cache key rounds lat/lon to one decimal (~10 km grid) so visitors in the same
neighborhood share a cell.

```bash
curl http://localhost:8080/weather                                 # ASCII, country fallback
curl 'http://localhost:8080/weather?lat=25.04&lon=121.56'          # ASCII, Taipei
curl 'http://localhost:8080/weather?lat=51.51&lon=-0.13&unit=c'    # ASCII, London, °C override
curl -A 'Mozilla/5.0' -o weather.png http://localhost:8080/weather # PNG
```

Forecasts are sourced from Open-Meteo's free public API (no key, no SLA).
Behind `forecast.Provider` so swapping to MET Norway or another free upstream
is a single file. AQI is from Open-Meteo's separate `/v1/air-quality`
endpoint (key-less, global, CAMS+Geos5-derived); earthquakes from USGS's
fdsnws geojson endpoint (key-less, the de-facto canonical seismic catalog).
All three providers cache + singleflight-coalesce per their own TTL —
30 m / 15 m / 10 m for AQI / quake / weather.

Unmatched paths fall through to a `404` page: pylon-rendered `404` banner stacked
above a fake Python traceback with the requested path injected into the panic
message and the trailing field. Both ASCII and PNG paths show the same content —
the PNG is composed locally (pylon banner above, traceback drawn with
`basicfont` below) because pylon's parser would shred the trace's parens and
brackets. `Accept: text/pylon` returns the bare banner source (the traceback
isn't pylon syntax). `?format=svg` returns the same banner-only SVG — no
traceback prose, since the trace can't be pylon-parsed and an SVG-side
basicfont compositor isn't built yet (v1 limitation).

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
render/                          # pylon-backed renderers — Box, Banner*, Mode, ProgressBar/YearProgress/DayCycle, sanitizers
logger/                          # zerolog setup with TTY-aware console / JSON switching
service/index/                   # the GET / landing page (banner + tagline + repo · version)
service/now/                     # the /now plugin (Register + Handler)
service/stock/                   # the /stock plugin — regional index quote (Yahoo Finance + cache)
service/stock/twse/              # TW-only enrichment — 三大法人 + margin from TWSE legacy openapi
service/weather/                 # the /weather plugin — today's forecast + enrichment
service/weather/airquality/      # AQI provider (Open-Meteo air-quality) + cached wrapper
service/weather/earthquake/      # earthquake provider (USGS fdsnws) + cached wrapper
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
    weathersvc "github.com/cmj0121/imagelet/service/weather"
    "github.com/cmj0121/imagelet/service/weather/airquality"
    "github.com/cmj0121/imagelet/service/weather/earthquake"
    weathercache "github.com/cmj0121/imagelet/service/weather/forecast/cached"
    "github.com/cmj0121/imagelet/service/weather/forecast/openmeteo"
)

func main() {
    r := server.New()                                                                 // middleware + GET /healthz
    indexsvc.Register(r, "v0.2.0")                                                    // GET / (pass your binary's version)
    nowsvc.Register(r)                                                                // GET /now
    stocksvc.Register(r, cached.New(yahoo.New()), twse.NewCached(twse.New()))         // GET /stock + TW enrichment
    weathersvc.Register(r,                                                            // GET /weather + AQI + quake
        weathercache.New(openmeteo.New()),
        airquality.NewCached(airquality.New()),
        earthquake.NewCached(earthquake.New()),
    )
    notfoundsvc.Register(r)                                                           // 404 fallback — install last
    http.ListenAndServe(":8080", r)
}
```

## Built with

| Library                                    | Role                        |
| ------------------------------------------ | --------------------------- |
| [gin](https://github.com/gin-gonic/gin)    | HTTP router                 |
| [kong](https://github.com/alecthomas/kong) | CLI flag parsing            |
| [zerolog](https://github.com/rs/zerolog)   | Structured logging          |
| [pylon](https://github.com/cmj0121/pylon)  | ASCII / PNG / SVG rendering |

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
