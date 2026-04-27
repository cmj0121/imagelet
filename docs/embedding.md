# Embedding

Mount imagelet's pieces inside any gin-based application. Top-level packages
are importable; `internal/` is not used.

## Project layout

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

## Example

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
