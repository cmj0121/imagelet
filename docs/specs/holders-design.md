<!-- markdownlint-disable MD013 -->

# Design — TDCC per-stock holders distribution

**Status**: ward-approved, ready for hale dispatch.
**Empirical verification**: completed 2026-05-07 against live TDCC OpenAPI.

## TL;DR

| Decision         | Value                                                              |
| ---------------- | ------------------------------------------------------------------ |
| Endpoint         | `https://openapi.tdcc.com.tw/v1/opendata/1-5`                      |
| Body cap         | **16 MiB** (per-fetch override; live size 9.5 MiB)                 |
| Walkback         | **NONE** — endpoint serves only the latest weekly snapshot         |
| Cache key        | `holders\|<asOfYYYYMMDD>` — date parsed from response, not request |
| TTL              | 24h success / 1h failure (publish-window-aware)                    |
| Render rows      | **2 lines** appended to TW enrichment block                        |
| Locale gate      | `zh-TW` / `zh-CN` only (matches existing pattern)                  |
| Coverage         | 上市 + 上櫃 + ETF + 興櫃 + 特別股 (3,967 distinct ids verified)    |
| `?date=` support | latest snapshot returned for any asOf (not date-pinned)            |
| Fixture          | `service/stock/twse/testdata/holders_2330_6488_0050_20260430.json` |

## 1. Endpoint shape (verified 2026-05-07)

`GET https://openapi.tdcc.com.tw/v1/opendata/1-5`

- **Format**: JSON array of flat objects.
- **Size**: 9.5 MiB raw (uncompressed). Exceeds `safehttp.DefaultBodyCap` (8 MiB) — must raise per-fetch cap to **16 MiB** for this path only. Do NOT loosen the global default.
- **Row count**: ~67k rows (≈3,967 stocks × 17 tiers).
- **Latency**: ~6s on a residential link; production 5s timeout is borderline. Bump to **15s** for THIS path only.

### Object schema (verified verbatim)

| JSON key            | Type   | Notes                                                                                   |
| ------------------- | ------ | --------------------------------------------------------------------------------------- |
| `證券代號`          | string | **Right-padded to 6 chars with U+0020 spaces.** e.g. `"2330  "`, `"00400A"`. MUST trim. |
| `持股分級`          | string | Tier index `"1"`–`"17"`. MUST parse to int.                                             |
| `人數`              | string | Holder count.                                                                           |
| `股數`              | string | Share count.                                                                            |
| `占集保庫存數比例%` | string | Percent, 2dp, e.g. `"85.58"`. (No `%` in the value, just in the key.)                   |
| `資料日期`          | string | Date `YYYYMMDD`. **Key has a U+FEFF BOM prefix** — see schema-trap below.               |

### ⚠️ Schema traps (record in code comments)

1. **BOM in `資料日期` key**: the upstream emits this key with a leading
   U+FEFF (byte-order mark). Markdown auto-formatters strip the BOM from
   this doc on save, so don't trust visual side-by-side comparisons —
   verify against `xxd`/`hexdump` of the live JSON instead. A struct tag
   of `json:"資料日期"` (no BOM) silently unmarshals to `""`. Two options:
   - Unmarshal into `map[string]string` and look up the key by suffix.
   - Use the BOM-prefixed tag explicitly (Go forbids raw BOM mid-source —
     declare via `string([]byte{0xef, 0xbb, 0xbf}) + "資料日期"` instead).
   - Recommended: **use `map[string]string` + look up the date once per
     fetch** (only one date per response), avoid leaking the BOM into the
     per-row hot path.
2. **Right-padded stock id**: `"2330  "` not `"2330"`. `strings.TrimSpace` every id before comparing.
3. **All numeric values are strings**, including the tier index. Use `parseTWSENumber` (already in `twse.go`).

### Tier mapping (TDCC canonical, validated against 2330 & 6488)

| Tier | Range (shares) | Tier | Range (shares)      |
| ---- | -------------- | ---- | ------------------- |
| 1    | 1–999          | 10   | 100,001–200,000     |
| 2    | 1,000–5,000    | 11   | 200,001–400,000     |
| 3    | 5,001–10,000   | 12   | 400,001–600,000     |
| 4    | 10,001–15,000  | 13   | 600,001–800,000     |
| 5    | 15,001–20,000  | 14   | 800,001–1,000,000   |
| 6    | 20,001–30,000  | 15   | >1,000,000          |
| 7    | 30,001–40,000  | 16   | 差異數調整 (drop)   |
| 8    | 40,001–50,000  | 17   | 合計 (TOTAL — 100%) |
| 9    | 50,001–100,000 |      |                     |

Tier 16 (差異數調整) is a settlement adjustment — exclude from rendering. Tier 17 is the grand total — surface as `總戶數`.

### Sample row (2330, tier 15)

```json
{
  "證券代號": "2330  ",
  "占集保庫存數比例%": "85.58",
  "人數": "1497",
  "﻿資料日期": "20260430",
  "股數": "22193101818",
  "持股分級": "15"
}
```

Reading: as of 2026-04-30, **1,497** accounts each hold >1M shares of TSMC, totalling **85.58%** of all custodied shares. This is the "concentrated institutional float" signal that the renderer surfaces.

### Publish cadence (verified)

- **Snapshot date**: dataset published 2026-05-07 carries `資料日期 = 20260430` — a **Thursday**, 7 days back.
- **Cadence**: weekly. The `qryStock` UI exposes 51 historical dates (~1 year archive).
- **OpenAPI 1-5 carries only the latest snapshot.** No historical lookup via this endpoint. Historical lookup requires `qryStockAjax.do` HTML scraping — out of scope.

## 2. Coverage

Verified 3,967 distinct stock ids in the live dump. Includes:

- **上市 4-digit numeric** (e.g. `2330` TSMC, `2317` Hon Hai)
- **上櫃** (e.g. `6488` Global Wafers — confirmed)
- **ETF** (e.g. `0050`, `0056`, `006208`)
- **特別股 with letter suffix** (e.g. `2348A`)
- **權證 / leveraged ETFs** (`00631L`, `00632R`)

For `/stock/:symbol`, hale must extract the 4-digit (or 4-char with letter suffix) TWSE id from the Yahoo symbol (`2330.TW` → `2330`, `2348A.TW` → `2348A`). Helpers like `extractTWStockID` already exist in the handler — reuse them. If extraction fails (non-TW symbol), skip holders entirely.

## 3. Struct shape

```go
// HoldersDistribution captures the TDCC weekly shareholder dispersion
// for one TW listing. Empty (zero-valued) when upstream had no row for
// the stock id; renderer gates on Has() to omit gracefully.
type HoldersDistribution struct {
    StockID    string                 // trimmed, no padding
    AsOf       time.Time              // 資料日期 from upstream, not from request
    Tiers      [15]HoldersTier        // tier 1..15 verbatim; 16/17 excluded
    TotalCount int64                  // tier 17 人數 (合計 — total accounts)
    TotalShare int64                  // tier 17 股數
}

type HoldersTier struct {
    Count int64   // 人數 — holder count in this band
    Share int64   // 股數 — share count in this band
    Pct   float64 // 占集保庫存數比例% — already in [0,100], 2dp from upstream
}

func (h HoldersDistribution) Has() bool { return h.TotalCount > 0 }
```

Decision: **keep all 15 buckets in-struct, render only summary**. Storing all tiers gives the renderer freedom to evolve the surface without re-fetching.

Tier 16 dropped at parse time (settlement adjustment, not actionable).
Tier 17 split out as `Total*` fields (the headline 合計 row), so callers don't accidentally include it in band sums.

## 4. Render row budget — 2 lines

The existing `/stock/:symbol` TW enrichment block is dense (4 lines). Adding 4+ holder tiers risks overflow on narrow surfaces (40-col PNG) and dilutes the existing institutional / margin signal.

Pick the **two most actionable signals**:

```text
大戶 1,721 戶 (0.07%) · 持股 86.35%
總戶數 2,519,187
```

- **Line A — concentration**: bucket tiers 14+15 as 大戶 (≥800k shares). Show count + % of total accounts + % of total shares held by that bucket. This is the "is the float concentrated?" signal — the single most-asked retail question about TWSE listings.
- **Line B — headline 總戶數**: the absolute account count from tier 17 (合計). Useful for "newly listed" vs "established" comparison and for change-over-time once historical caching arrives.

**Why not 3 lines (small/mid/large breakdown)?** Tested layout against existing rows — 7 enrichment lines pushes the OHLC + MA bars off-screen on common 80-col narrow PNG renders. 2 is the right ceiling.

**Why 大戶 = tiers 14+15 (≥800k shares)?** Tier 15 alone (>1M) is the canonical signal but may underestimate concentration for thinly-traded mid-caps where 800k–1M holders are still meaningful. Bucketing 14+15 is the same convention used by Goodinfo / public 籌碼分析 sites.

**Localization**:

- `zh-TW`: `大戶 / 戶 / 持股 / 總戶數` (Traditional)
- `zh-CN`: `大户 / 户 / 持股 / 总户数` (Simplified — verify against `internal/i18n/zhcn.go`)
- `en`: stripped — matches existing TW enrichment policy. No EN render path in scope.

Hale: place these two lines **after** existing margin/lending rows in the TW block, gated on `HoldersDistribution.Has()`.

## 5. Cache shape

### No walkback

OpenAPI 1-5 always returns "this week's snapshot". There's no per-date variation to walk back through. The Cached wrapper here is a degenerate walkback (single-key cache, not date-keyed walkback like T86/Lending).

### Architecture

```text
/stock/:symbol handler
       │
       ▼
twse.Cached.GetHoldersDistribution(stockID, asOf)
       │   ① ignore asOf (informational)
       ▼
CachedHolders                           ┌─ ttlcache.Cache[string, holdersDump] ─┐
       │   ② cache key = "latest"        │  Entry holds parsed                  │
       │      OR resolved-date once we   │  map[stockID]HoldersDistribution +   │
       │      have the response          │  AsOf time                            │
       │                                  └───────────────────────────────────────┘
       ▼
HoldersExactProvider.FetchHoldersExact
       │   ③ raise body cap to 16 MiB, timeout 15s
       │   ④ parse JSON once, build map[stockID]→HoldersDistribution
       ▼
HTTP GET https://openapi.tdcc.com.tw/v1/opendata/1-5
```

Key insight: cache the **parsed map**, not raw JSON. 100 concurrent /stock requests on different stocks during the same week = 1 fetch + 100 map lookups. Singleflight in `ttlcache.GetOrFetch` handles the concurrent-fetch case.

### Concrete types

```go
// holdersDump is the cached parsed payload — entire universe map plus
// the resolved AsOf. Not exported; only the per-stock HoldersDistribution
// crosses the cache boundary.
type holdersDump struct {
    AsOf time.Time
    Rows map[string]HoldersDistribution // key: trimmed stock id
}

type HoldersExactProvider interface {
    // FetchHoldersExact pulls the live OpenAPI dump and returns the
    // parsed-once snapshot. found=false reserved for "upstream returned
    // empty array" (publish gap); transport / parse errors return err.
    FetchHoldersExact(ctx context.Context) (holdersDump, bool, error)
}

type CachedHolders struct {
    upstream HoldersExactProvider
    cache    *ttlcache.Cache[string, holdersDump]  // single key "latest"
    now      func() time.Time
}

func (c *CachedHolders) GetHoldersDistribution(
    ctx context.Context, stockID string, asOf time.Time,
) (HoldersDistribution, error)
```

### TTL policy

| Condition                                      | TTL              |
| ---------------------------------------------- | ---------------- |
| Upstream success                               | 24h              |
| Upstream empty / `found=false`                 | 1h               |
| Past Thursday 04:00 Asia/Taipei (publish gate) | until next 04:00 |

The publish-window logic in `cached_walkback.go::ttlForAsOf` is daily; for weekly data we compute TTL inline:

```go
func holdersTTLAt(now time.Time) time.Duration {
    // TDCC publishes ~Thu morning Asia/Taipei. Hold past the next
    // Thursday 04:00 to refresh once-per-week.
    // ... (hale to implement)
}
```

Don't over-engineer — even a flat 24h TTL is fine. The publish-gate refinement is a polish-pass item; baseline is 24h success, 1h failure.

### Snapshot persistence

Wire into `Cached.LoadSnapshots / SaveSnapshots` with new filename:

```go
const snapshotHolders = "tdcc-holders.json"
```

Add `loadOne(c.cachedHolders, dir, snapshotHolders)` calls and the `loadFrom`/`saveTo` methods on `CachedHolders`. Mirror the exact pattern of `CachedT86`.

**Snapshot size**: a serialized holdersDump = ~67k rows × ~80 bytes = ~5 MiB on disk. Verified safe: `ttlcache.MaxSnapshotBytes = 64 << 20` (64 MiB) accommodates this comfortably with ~12× headroom. No cap raise required. Document the disk impact in `docs/caching.md` so operators know `--cache-dir` grows by ~5 MiB once holders is wired.

**Cold-start storm note**: K8s rollout = N pods × 9.5 MiB simultaneous fetch. Singleflight is per-process, not cross-pod. Snapshot persistence (working — see above) is the mitigation: the SaveSnapshots-on-shutdown / LoadSnapshots-on-startup cycle means rolled pods come up warm. Document in ops review.

## 6. Walkback override decision

**Not applicable** — no walkback for this provider.

If we later add historical lookup via `qryStockAjax.do` scraping (out of scope), THEN bump `maxLookbackDays` to 14 to cover holiday gaps in the weekly cadence. Document but don't implement.

## 7. Body cap exception

```go
// fetchHolders raises the per-call body cap to 16 MiB because the TDCC
// OpenAPI dump is ~9.5 MiB live (full universe × 17 tiers). This is the
// ONLY fetch path in the project that exceeds safehttp.DefaultBodyCap;
// the global default stays at 8 MiB for everything else.
const holdersBodyCap = 16 << 20 // 16 MiB

// holdersFetchTimeout overrides the per-request 5s timeout because the
// 9.5 MiB body takes ~6s on a residential link and pushes 8s on
// constrained CDN paths.
const holdersFetchTimeout = 15 * time.Second
```

Apply via a per-fetcher `*http.Client` wrapper or by manipulating the response with `safehttp.BoundBody(resp, holdersBodyCap)` after the request. The existing `HTTPProvider.fetch` calls `safehttp.BoundBody(resp, safehttp.DefaultBodyCap)` — for holders, hale must NOT use that helper. Add a `fetchLarge` sibling method that takes an explicit cap.

Tenth-man flagged silent truncation as the failure mode — make sure the parser bails LOUDLY if it hits EOF mid-array (it will: `json.Unmarshal` returns "unexpected EOF" / "invalid character"). Add a regression test that wires a fixture larger than the cap and asserts `err != nil`.

## 8. Locale gating

Match existing `service/stock/stock.go` TW enrichment policy verbatim:

- Resolve locale per request (already done by middleware).
- If `locale ∈ {zh-TW, zh-CN}` AND `HoldersDistribution.Has()` → render the 2 lines.
- Else → strip silently. No "(English not available)" indicator; matches how 三大法人 etc. are stripped on `en`.

The 大戶 / 戶 / 持股 / 總戶數 strings live in `internal/i18n` catalogs. Hale: add new keys to `zhtw.go`, `zhcn.go`, leave `en.go` alone (won't be referenced).

## 9. Failure modes the renderer must handle

| Mode                          | Cause                                 | Behaviour                                                |
| ----------------------------- | ------------------------------------- | -------------------------------------------------------- |
| Network timeout               | TDCC slow / unreachable               | Skip rows; log Warn once per cache TTL                   |
| HTTP 5xx                      | TDCC outage                           | Skip rows; serve stale cache if present                  |
| HTTP 200 + empty array        | Publish gap (rare; weekly bug)        | Skip rows; cache empty for 1h                            |
| Body truncated                | Cap hit (config drift)                | `json.Unmarshal` errors LOUDLY; do not partial-render    |
| Stock id absent               | Pre-listing / delisted / non-TW       | Skip rows silently                                       |
| Tier 17 missing               | Schema drift                          | Use sum of tiers 1-15 as fallback total; log Warn        |
| **Schema-version probe fail** | TDCC renamed/added/dropped a JSON key | Return `ErrUnavailable`; log Warn; skip rows; no partial |
| **Stale `?date=` (>14d gap)** | User pinned a historical OHLC         | Skip rows silently (see §10)                             |

### Schema-version probe (added after tenth-man)

TDCC has historically renamed datasets (1792 → 1-5 itself is evidence). After unmarshaling the array, check that the first row's key set matches the expected six keys before iterating. Mismatch = log + `ErrUnavailable`, never silently parse:

```go
// Required key set for the holders dump. The U+FEFF BOM-prefixed
// 資料日期 is part of the contract — drift in this key set means
// TDCC changed the schema and we should fail closed rather than
// rendering whatever we managed to extract.
var holdersExpectedKeys = []string{
    "證券代號",
    "持股分級",
    "人數",
    "股數",
    "占集保庫存數比例%",
    "﻿資料日期",
}

func validateHoldersSchema(first map[string]string) error {
    if len(first) != len(holdersExpectedKeys) {
        return ErrUnavailable
    }
    for _, k := range holdersExpectedKeys {
        if _, ok := first[k]; !ok {
            return ErrUnavailable
        }
    }
    return nil
}
```

Run on the FIRST row of the response only — schema drift uniformly affects all rows, so per-row validation is wasted work.

## 10. `?date=` staleness gate

**Decision (revised after tenth-man challenge)**: do NOT fall through silently. Skip holders rows when the user's `?date=` is more than 14 days before the dump's resolved `AsOf`. Stitching January's OHLC against April's holders is misleading, not just suboptimal — especially for IPOs where the dispersion changed materially after the requested date.

```go
// holdersStaleWindow caps how far the request's asOf may diverge from
// the dump's published AsOf before holders is suppressed. 14 days
// covers the 7-day-to-2-week publish lag typical for TDCC, plus a
// week of buffer; anything older means the request is targeting a
// historical OHLC bar that no longer aligns with current holders.
const holdersStaleWindow = 14 * 24 * time.Hour

func holdersFreshFor(reqAsOf, dumpAsOf time.Time) bool {
    if reqAsOf.IsZero() {
        return true // no ?date= override; render
    }
    delta := dumpAsOf.Sub(reqAsOf)
    if delta < 0 {
        delta = -delta
    }
    return delta <= holdersStaleWindow
}
```

The renderer calls `holdersFreshFor` before emitting rows; on `false`, treat as `Has() == false` and skip. No "stale" indicator on screen — silent suppression matches how /stock already strips other rows that don't apply.

## 11. Tenth-man verdicts on this design

### Round 1 (self-challenge during empirical verification)

1. **CRITICAL**: Initial plan named dataset 1792, which returns "No Data!" — would have wasted a unit's work. Verified live and pivoted to `1-5`.
2. **HIGH**: Initial plan said "filter by exact stock id `"2330"`"; would have silently returned empty due to space-padding. Documented + flagged as schema-trap #1.
3. **MEDIUM**: Initial plan inherited per-(stockID, date) walkback from T86. The OpenAPI dump has no per-date variation — collapsed to single-key cache + parsed-map. Removes ~40 LoC of pointless walkback.

### Round 2 (formal challenge after design draft)

1. **HIGH**: Snapshot persistence might exceed `ttlcache.MaxSnapshotBytes`. **Resolved by inspection**: `MaxSnapshotBytes = 64 << 20` (64 MiB), well above the ~5 MiB holders snapshot. No cap raise required (§5).
2. **HIGH**: Schema drift not in the failure-mode table; TDCC renamed datasets before. **Resolved**: added schema-version probe in §9 — fails closed with `ErrUnavailable` on key-set mismatch, never partial-parses.
3. **MEDIUM**: `?date=` silent fall-through stitches misaligned OHLC + holders dates. **Resolved**: added `holdersStaleWindow = 14d` gate in §10 — skip rows when `|asOf - dumpAsOf| > 14d`.
4. **LOW**: Cold-start storm on K8s rollout. **Resolved by note**: snapshot persistence (working) means rolled pods come up warm; per-process singleflight covers the rest.

**Verdict: GO.** All round-2 findings folded in.

## 12. Hale's checklist

- [ ] Add `defaultHoldersEndpoint` constant + `HoldersDistribution`, `HoldersTier`, `holdersDump` types in `service/stock/twse/holders.go`.
- [ ] Add `HoldersExactProvider` interface + `HTTPProvider.FetchHoldersExact` method.
- [ ] Add `holdersBodyCap = 16 << 20`, `holdersFetchTimeout = 15s`, `fetchLarge` helper. Do NOT touch `safehttp.DefaultBodyCap`.
- [ ] Add `CachedHolders` wrapper in `cached_walkback.go` (single-key, no date walk).
- [ ] Wire `CachedHolders` into `Cached.NewCached()` via `if e, ok := inner.(HoldersExactProvider); ok { c.cachedHolders = ... }`.
- [ ] Wire snapshot persistence: `snapshotHolders = "tdcc-holders.json"` + Load/Save plumbing.
- [ ] Add 2-line rendering on `/stock/:symbol` after the existing margin rows; locale-gate to `zh-TW`/`zh-CN`.
- [ ] Add i18n keys: `holders.large`, `holders.account`, `holders.holdSharePct`, `holders.totalAccounts` (or similar — match existing key style).
- [ ] Add **schema-version probe** (§9): `validateHoldersSchema(first)` checks the 6-key set on the first row; mismatch → `ErrUnavailable`. No partial parse.
- [ ] Add **`?date=` staleness gate** (§10): `holdersFreshFor(reqAsOf, dumpAsOf)` skips rows when |delta| > 14d.
- [ ] Tests:
  - parse the pinned fixture and assert 2330's tier 15 has 1,497 holders / 85.58%.
  - assert OTC `6488` parses identically (coverage check).
  - assert padded id `"2330  "` matches a `"2330"` request (trimming).
  - assert BOM key `資料日期` is read correctly.
  - assert body-cap regression: response > cap → fetch errors loudly (do not partial-parse).
  - assert schema-version probe: drop one expected key from the fixture → `ErrUnavailable`.
  - assert `holdersFreshFor`: zero asOf → true; 0d delta → true; 13d → true; 15d → false.
  - cached-second-call returns same data without re-fetch (singleflight).
- [ ] No new fixture file beyond the pinned one — request larger fixtures from ward if needed.
