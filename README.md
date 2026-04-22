# ImageLet

> Show you should know in single image.

## DDD (Dream-Driven Development)

This project follows the DDD (Dream-Driven Development) methodology, which means the project
is driven by what I envision.

All features are based on my needs and my dreams.

## Quickstart

A minimal Go web service exposing a single route.

```bash
make build   # compile binary to bin/imagelet
make run     # run on 0.0.0.0:8080 with info log level
make test    # run unit tests
```

Flags:

```
$ imagelet --help
Usage: imagelet [flags]

imagelet HTTP service.

Flags:
  -h, --help                Show context-sensitive help.
  -H, --host="0.0.0.0"      Host address to bind.
  -p, --port=8080           TCP port to listen on.
  -l, --log-level="info"    Log level (trace|debug|info|warn|error|fatal|panic).
```

### Routes

- `GET /` — returns `200 No Content` with an empty body.

## License

Source-available under the [PolyForm Noncommercial License 1.0.0](./LICENSE).
Free for personal, research, and noncommercial use. Commercial use is not permitted —
please contact the author if you need a commercial license.
