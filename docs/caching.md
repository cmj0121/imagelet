# Caching

Upstream calls to Yahoo Finance, TWSE, and TAIFEX are cached in
process so a steady stream of `/stock` requests doesn't fan out
duplicate fetches. The cache layer is layered:

| Provider                            | Success TTL | Notes                                          |
| ----------------------------------- | ----------- | ---------------------------------------------- |
| Yahoo quote (per symbol)            | 60s         | Singleflight; serves stale on upstream error.  |
| TWSE market-wide aggregate          | 4h          | BFI82U + MI_INDEX + MI_MARGN merged.           |
| TWSE per-stock T86 / TWT93U / MARGN | 24h / 30min | Keyed on (stockID, resolved date).             |
| TAIFEX retail futures / PCR / VIX   | 24h / 30min | Keyed on resolved date.                        |
| Live breadth (universe + MIS)       | 4h / 30s    | Universe 4h; MIS batch 30s.                    |
| TWSE name lookup                    | forever     | Stable per stock ID; in `sync.Map`.            |
| HTML responses                      | per header  | `internal/htmlcache`, LRU 256, text/html only. |

Per-stock and TAIFEX caches use a publish-window-aware TTL: requests
covering today before ~17:00 Asia/Taipei (the TWSE / TAIFEX afterTrading
publish hour) cache for at most 30 minutes — long enough to absorb burst
traffic, short enough that the cache flips fresh once afterTrading data
lands. Past dates and post-publish entries hold for 24 hours since they
don't change upstream.

The cache wrapper owns the walk-back loop: providers expose
`Fetch*Exact(date)` helpers that fetch a single date and report
`(value, found, err)`, and the cache walks back across cached entries
calling Exact for misses. Found=false negative-cache sentinels remember
"no data published for this date" so a holiday week doesn't re-probe
the same dead dates on every request.

## Disk persistence (`--cache-dir`)

Pass `--cache-dir <path>` to persist per-stock and TAIFEX caches
across restarts. The cache restores on startup and saves on graceful
shutdown (SIGINT / SIGTERM); per-provider JSON files live under the
directory:

```text
<cache-dir>/
├── twse-t86.json
├── twse-securities-lending.json
├── twse-stock-margin.json
├── taifex-retail-futures.json
├── taifex-options-pcr.json
└── taifex-vix.json
```

Each file carries a schema-version header — a future cached-value
field addition invalidates stale snapshots gracefully (the wrapper
logs and deletes the file so the next save writes a current-schema
replacement). Atomic writes (tempfile + rename) prevent partial
flushes on crash; the temp file is opened with `O_NOFOLLOW` /
`O_EXCL` and the rename target is rejected if it points at a
symlink, so a hostile cache directory can't be tricked into
overwriting an unrelated file. Disk persistence is best-effort:
SIGKILL / OOM / panic skip the save, so the restored cache may lag
the in-memory state by a few requests.

The cache directory is created `0o700` and snapshot files are
written `0o600` — owned and readable only by the imagelet UID.
**Operators sharing the cache directory between UIDs (backup
sidecar, log shipper, etc.) must run those companions as the same
user** or copy the snapshots out-of-band; the previous world-
readable layout is gone. The `--cache-dir` argument is resolved via
`filepath.Abs` at startup so a relative path keeps pointing at the
same place after a `chdir`.

Yahoo quote, live breadth, name lookup, and HTML response caches stay
in-memory only — their TTLs are short enough that snapshotting them
adds little value.
