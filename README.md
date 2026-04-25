# ImageLet

> Show you should know in single image.

## DDD (Dream-Driven Development)

This project follows the DDD (Dream-Driven Development) methodology, which means the project
is driven by what I envision.

All features are based on my needs and my dreams.

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
