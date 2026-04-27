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

| Method | Path       | Description                                                                |
| ------ | ---------- | -------------------------------------------------------------------------- |
| `GET`  | `/`        | `IMAGELET` banner with tagline and `<repo> · <version>` caption.           |
| `GET`  | `/healthz` | Returns `200 No Content`. Liveness probe — never renders, never allocates. |
| `GET`  | `/now`     | Banner-rendered current time with date / weekday / zone caption.           |
| `GET`  | `/stock`   | Banner-rendered regional stock-index quote.                                |
| `*`    | _other_    | `404` banner above a fake Python traceback with the requested path inside. |

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
