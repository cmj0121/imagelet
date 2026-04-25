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

Flags:

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

### Routes

| Method | Path   | Description                                                    |
| ------ | ------ | -------------------------------------------------------------- |
| `GET`  | `/`    | Returns `200 No Content` with an empty body. Liveness probe.   |
| `GET`  | `/now` | Returns the current server-local time `HH:MM` framed in a box. |

#### `GET /now`

The wire format is chosen by the `ClientDetector` middleware, which inspects the
request's `User-Agent`:

| User-Agent                                                               | Response                                |
| ------------------------------------------------------------------------ | --------------------------------------- |
| empty                                                                    | `text/plain; charset=utf-8` (ASCII box) |
| contains `Mozilla` (case-insensitive)                                    | `image/svg+xml` (SVG document)          |
| anything else (`curl`, `wget`, `Go-http-client`, `python-requests`, ...) | `text/plain; charset=utf-8` (ASCII box) |

Every response sets `Cache-Control: no-store` because the body changes every minute.

CLI client — gets pure-ASCII glyphs (`+ - |`) so terminals that can't render Unicode
still display correctly:

```bash
$ curl -i http://localhost:8080/now
HTTP/1.1 200 OK
Cache-Control: no-store
Content-Type: text/plain; charset=utf-8
Content-Length: 42

+-----------+
|   13:45   |
+-----------+
```

Browser (or any Mozilla-flavored UA) — gets a self-contained SVG document the page can
embed directly:

```bash
$ curl -i -A 'Mozilla/5.0' http://localhost:8080/now
HTTP/1.1 200 OK
Cache-Control: no-store
Content-Type: image/svg+xml
Content-Length: 648

<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 65 39" width="65" height="39">
  ...
  <text>│   13:45   │</text>
  ...
</svg>
```

## Project layout

Packages are organized so external Go projects can import any of them — `internal/` is
not used. Services are plug-in style: each one self-registers on a `gin.IRouter`.

```text
cmd/imagelet/      # binary entry point
server/            # core router framework — gin.Recovery + ClientDetector preinstalled
middleware/        # reusable gin middlewares (ClientDetector, GetMode)
render/            # pylon-backed renderers (Box, Mode)
logger/            # zerolog setup with TTY-aware console / JSON switching
service/now/       # the /now plugin: now.Register(r), now.Handler
```

Public API at a glance:

| Package       | Key exports                                                                |
| ------------- | -------------------------------------------------------------------------- |
| `server`      | `New() *gin.Engine` — engine with Recovery + ClientDetector + `GET /`      |
| `middleware`  | `ClientDetector() gin.HandlerFunc`, `GetMode(c) render.Mode`               |
| `render`      | `Box(text, mode) string`, `Mode` (with `String()`), `ModeASCII`, `ModeSVG` |
| `service/now` | `Register(r gin.IRouter)`, `Handler(c)`                                    |
| `logger`      | `Setup(level string) error`                                                |

### Reuse imagelet from your own service

Mount imagelet's pieces inside any gin-based application — services pick which plugins
they want, and the framework parts (server, middleware, render) come along for free.

```go
import (
    "net/http"

    "github.com/cmj0121/imagelet/server"
    nowsvc "github.com/cmj0121/imagelet/service/now"
)

func main() {
    r := server.New()        // gin.Recovery + ClientDetector + GET / preinstalled
    nowsvc.Register(r)       // mounts GET /now with content negotiation

    http.ListenAndServe(":8080", r)
}
```

### Built with

| Library                                    | Role                      |
| ------------------------------------------ | ------------------------- |
| [gin](https://github.com/gin-gonic/gin)    | HTTP router               |
| [kong](https://github.com/alecthomas/kong) | CLI flag parsing          |
| [zerolog](https://github.com/rs/zerolog)   | Structured logging        |
| [pylon](https://github.com/cmj0121/pylon)  | ASCII / SVG box rendering |

Pylon is pinned to commit `93b11e6bbcff` (the `v0.5.0` tag's commit) via a Go pseudo-version.
The pylon Go module lives at `github.com/cmj0121/pylon/src/go`; subdirectory tags
(`src/go/v0.5.0`) are not published, so `go get` resolves the latest commit on the
default branch rather than a named tag.

## License

Source-available under the [PolyForm Noncommercial License 1.0.0](./LICENSE).
Free for personal, research, and noncommercial use. Commercial use is not permitted —
please contact the author if you need a commercial license.
