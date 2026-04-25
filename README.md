# ImageLet

> Show you should know in single image.

## Quickstart

A minimal Go web service that frames small bits of data as ASCII or SVG, picked per request
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
...) supply their own. `GET /` returns `200 No Content` and is the intended
liveness probe.

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

| Method | Path   | Description                                                      |
| ------ | ------ | ---------------------------------------------------------------- |
| `GET`  | `/`    | Returns `200 No Content`. Liveness probe.                        |
| `GET`  | `/now` | Banner-rendered current time with date / weekday / zone caption. |

`/now` content-negotiates per request:

- `Accept: text/pylon` — raw pylon source, so callers can render it themselves.
- `User-Agent` contains `Mozilla` — `image/svg+xml`.
- Anything else — `text/plain; charset=utf-8`.

Both rendered paths use pylon's native theme — Unicode frame plus ANSI Shadow block
letters — so the visual is identical across consumers. The `:` in `HH:MM` is
substituted with a space (pylon's banner font has no `:` glyph); the gap reads as a
clock separator. Every response sets `Cache-Control: no-store`.

```text
$ curl http://localhost:8080/now
   ┌───────────────────────────────────────┐
   │    ██╗ █████╗     ██╗  ██╗ ██████╗    │
   │   ███║██╔══██╗    ██║  ██║██╔════╝    │
   │   ╚██║╚██████║    ███████║███████╗    │
   │    ██║ ╚═══██║    ╚════██║██╔═══██╗   │
   │    ██║ █████╔╝         ██║╚██████╔╝   │
   │    ╚═╝ ╚════╝          ╚═╝ ╚═════╝    │
   └───────────────────────────────────────┘
             2026-04-25 SAT UTC+8
```

Browsers see the same banner wrapped in a self-contained `<svg>` document — pylon paints
the solid `█` blocks as `<rect>` elements so it scales crisply.

When the request carries Cloudflare's `CF-Timezone` header (e.g. `Asia/Taipei`),
`/now` renders in the caller's local zone — the subtitle's `UTC±H` offset shifts
accordingly. Missing or unparseable values fall back to the server's local zone.

## Project layout

Top-level packages are importable; `internal/` is not used.

```text
cmd/imagelet/      # binary entry point
server/            # core router — Recovery + Logger + ClientDetector preinstalled
middleware/        # reusable gin middlewares (ClientDetector + GetMode)
render/            # pylon-backed renderers (Box, Banner, Mode)
logger/            # zerolog setup with TTY-aware console / JSON switching
service/now/       # the /now plugin (Register + Handler)
```

Mount imagelet's pieces inside any gin-based application:

```go
import (
    "net/http"

    "github.com/cmj0121/imagelet/server"
    nowsvc "github.com/cmj0121/imagelet/service/now"
)

func main() {
    r := server.New()      // engine with all middleware + GET /
    nowsvc.Register(r)     // mounts GET /now with content negotiation
    http.ListenAndServe(":8080", r)
}
```

## Built with

| Library                                    | Role                  |
| ------------------------------------------ | --------------------- |
| [gin](https://github.com/gin-gonic/gin)    | HTTP router           |
| [kong](https://github.com/alecthomas/kong) | CLI flag parsing      |
| [zerolog](https://github.com/rs/zerolog)   | Structured logging    |
| [pylon](https://github.com/cmj0121/pylon)  | ASCII / SVG rendering |

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
