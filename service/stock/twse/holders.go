package twse

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cmj0121/imagelet/internal/safehttp"
)

// defaultHoldersEndpoint returns the TDCC OpenAPI 集保戶股權分散表 — a
// single bulk dump containing every TWSE-listed equity's per-tier
// shareholder distribution for the most recent weekly snapshot. JSON
// array of flat objects; ~9.5 MiB live, ~67k rows (≈3,967 stocks ×
// 17 tiers). No filtering parameters; we filter client-side after a
// once-per-cache-window fetch.
//
// Verified 2026-05-07 against live TDCC: dataset id pivoted from the
// historical 1792 (returns "No Data!") to 1-5. See
// docs/specs/holders-design.md for the empirical verification trail.
const defaultHoldersEndpoint = "https://openapi.tdcc.com.tw/v1/opendata/1-5"

// holdersBodyCap is the per-fetch response cap for the holders dump.
// 16 MiB sits well above the live 9.5 MiB body but caps a runaway /
// schema-drifted response from OOMing the process. This is the ONLY
// fetch path in the project that exceeds safehttp.DefaultBodyCap (8
// MiB); everything else uses the default. Declared as var (not const)
// so tests can rebind it temporarily to exercise the cap-trip path
// without serving a real 16 MiB body.
var holdersBodyCap int64 = 16 << 20

// holdersFetchTimeout overrides the per-request 5s timeout used by
// HTTPProvider.client. The 9.5 MiB body takes ~6s on a residential
// link and pushes 8s on constrained CDN paths, so we give the holders
// fetcher its own dedicated client with a 15s budget.
const holdersFetchTimeout = 15 * time.Second

// holdersStaleWindow caps how far the request's asOf may diverge from
// the dump's published AsOf before holders is suppressed. 14 days
// covers TDCC's 7-day-to-2-week publish lag plus a week of buffer;
// anything older means the request is targeting a historical OHLC bar
// that no longer aligns with current shareholder dispersion.
const holdersStaleWindow = 14 * 24 * time.Hour

// bomDateKey is TDCC's `資料日期` JSON key with the U+FEFF byte-order-mark
// prefix the upstream emits verbatim. A struct tag of `json:"資料日期"`
// (no BOM) silently unmarshals to "" — every consumer must look up the
// date by this exact literal. Initialised via a byte-slice rather
// than an inline U+FEFF source character because Go forbids BOMs
// mid-file (`go vet`: "invalid BOM in the middle of the file").
var bomDateKey = string([]byte{0xef, 0xbb, 0xbf}) + "資料日期"

// holdersExpectedKeys is the canonical TDCC 1-5 row schema, captured
// 2026-05-07 against the live endpoint. The schema-version probe
// (validateHoldersSchema) compares this set against the first row of
// every response so an upstream key rename / addition / removal fails
// closed (ErrUnavailable) instead of silently rendering whatever we
// managed to extract. TDCC has historically renumbered datasets
// (1792 → 1-5 itself is evidence), so a strict probe is load-bearing.
var holdersExpectedKeys = []string{
	"證券代號",
	"持股分級",
	"人數",
	"股數",
	"占集保庫存數比例%",
	bomDateKey,
}

// HoldersDistribution captures the TDCC weekly shareholder dispersion
// for one TW listing. The 15-element Tiers array indexes by (tier-1)
// where tier 1..15 follows TDCC's canonical mapping (1: 1–999 shares,
// 2: 1k–5k, …, 14: 800k–1M, 15: >1M); tier 16 (差異數調整) is dropped
// at parse time as a settlement-adjustment row not actionable for
// rendering, and tier 17 (合計) is split out as Total* fields so
// callers can't accidentally double-count it as a band.
//
// Empty (zero-valued) when the upstream had no row for the stock id —
// pre-listing, delisted, non-TW, or pre-publish on a fresh-listing
// week. Renderer gates on Has() to omit the rows gracefully.
type HoldersDistribution struct {
	StockID    string          // 證券代號, trimmed (no upstream space-padding)
	AsOf       time.Time       // 資料日期 from upstream, not from request
	Tiers      [15]HoldersTier // tier 1..15 verbatim; 16/17 excluded
	TotalCount int64           // tier 17 人數 (合計 — total accounts holding the stock)
	TotalShare int64           // tier 17 股數 (合計 — total shares custodied)
}

// HoldersTier captures one band of the TDCC 持股分級 breakdown for a
// single stock. Pct is in [0,100] with 2dp precision verbatim from
// upstream — converting to a [0,1] fraction would lose the published
// rounding without buying anything for the renderer.
type HoldersTier struct {
	Count int64   // 人數 — distinct accounts holding shares in this band
	Share int64   // 股數 — total shares held within this band
	Pct   float64 // 占集保庫存數比例% — already in [0,100]
}

// Has reports whether the upstream produced a non-empty distribution
// for this stock. Treats TotalCount == 0 as "not fetched" — every
// listed equity has at least a few hundred holders, so a true zero
// only happens when upstream had no row at all.
func (h HoldersDistribution) Has() bool { return h.TotalCount > 0 }

// holdersDump is the cached parsed payload — the entire TWSE universe
// keyed by trimmed stock id, plus the 資料日期 the upstream stamped
// onto every row. Unexported because only the per-stock
// HoldersDistribution crosses the cache boundary; callers never see
// the full dump directly. The cache wrapper holds a single dump per
// snapshot so 100 concurrent /stock requests during the same week
// converge on one fetch + 100 map lookups.
type holdersDump struct {
	AsOf time.Time                      `json:"as_of"`
	Rows map[string]HoldersDistribution `json:"rows"`
}

// HoldersProvider is implemented by anything that can answer
// per-stock TDCC dispersion queries. Get takes asOf so the caller can
// pin a historical date via /stock/:symbol?date=... — implementations
// may surface ErrUnavailable when asOf is too far from the published
// snapshot (see HoldersFreshFor / holdersStaleWindow).
type HoldersProvider interface {
	GetHoldersDistribution(ctx context.Context, stockID string, asOf time.Time) (HoldersDistribution, error)
}

// HoldersExactProvider is the cache-friendly twin: pulls the live
// OpenAPI dump exactly once per call with no internal walk-back, so a
// caching wrapper above can hold the parsed map and skip the network
// for the rest of the publish window. found=false reserved for
// "upstream answered with an empty array" (publish gap on a holiday-
// shortened week); transport / parse / schema-drift failures return
// err so the caller can decide between retry vs. surface.
type HoldersExactProvider interface {
	FetchHoldersExact(ctx context.Context) (holdersDump, bool, error)
}

// SetHoldersEndpoint overrides the upstream URL used by
// FetchHoldersExact, so tests can route to a fixture httptest.Server.
// Production callers should rely on New() which wires
// defaultHoldersEndpoint automatically. Empty disables
// FetchHoldersExact (returns ErrUnavailable).
func (p *HTTPProvider) SetHoldersEndpoint(endpoint string) {
	p.holders = endpoint
}

// FetchHoldersExact pulls the TDCC OpenAPI 1-5 weekly dump and returns
// the parsed-once snapshot keyed by trimmed stock id. The dump itself
// has no per-date variation (the endpoint always serves the latest
// snapshot), so the caching wrapper above this method keys on a
// single sentinel rather than the resolved date. found=false means
// the upstream replied with an empty array — a rare publish gap, not
// an error. err is reserved for transport / parse / schema-drift
// failures the caller should see.
func (p *HTTPProvider) FetchHoldersExact(ctx context.Context) (holdersDump, bool, error) {
	if p.holders == "" {
		return holdersDump{}, false, ErrUnavailable
	}
	return p.fetchHolders(ctx, holdersBodyCap)
}

// fetchHolders is the inner of FetchHoldersExact, parameterised by a
// body cap so tests can shrink the cap to a few KiB and exercise the
// cap-trip / loud-failure path without serving a real 16 MiB body.
// Production callers always pass holdersBodyCap.
func (p *HTTPProvider) fetchHolders(ctx context.Context, capBytes int64) (holdersDump, bool, error) {
	body, err := fetchLarge(ctx, p.holders, capBytes)
	if err != nil {
		return holdersDump{}, false, err
	}

	var raw []map[string]string
	if err := json.Unmarshal(body, &raw); err != nil {
		return holdersDump{}, false, fmt.Errorf("twse: holders unmarshal: %w", err)
	}
	if len(raw) == 0 {
		return holdersDump{}, false, nil
	}
	if err := validateHoldersSchema(raw[0]); err != nil {
		return holdersDump{}, false, err
	}

	// Date stamp is uniform across rows; read once from the first row.
	asOf := parseTWSEDate(raw[0][bomDateKey])

	// Two-pass build keeps Tiers[..] zero-initialised across stocks
	// even when upstream omits a tier row for a given stock.
	rows := make(map[string]*HoldersDistribution, len(raw)/17)
	for _, row := range raw {
		stockID := strings.TrimSpace(row["證券代號"])
		if stockID == "" {
			continue
		}
		tier, err := strconv.Atoi(strings.TrimSpace(row["持股分級"]))
		if err != nil || tier < 1 || tier > 17 {
			continue
		}
		// Tier 16 (差異數調整) is a settlement reconciliation row, not
		// a holder bucket — drop entirely.
		if tier == 16 {
			continue
		}
		dist := rows[stockID]
		if dist == nil {
			dist = &HoldersDistribution{StockID: stockID, AsOf: asOf}
			rows[stockID] = dist
		}
		count := parseTWSENumber(row["人數"])
		share := parseTWSENumber(row["股數"])
		pct, _ := strconv.ParseFloat(strings.TrimSpace(row["占集保庫存數比例%"]), 64)
		if tier == 17 {
			dist.TotalCount = count
			dist.TotalShare = share
			continue
		}
		dist.Tiers[tier-1] = HoldersTier{Count: count, Share: share, Pct: pct}
	}
	if len(rows) == 0 {
		return holdersDump{}, false, nil
	}
	out := holdersDump{AsOf: asOf, Rows: make(map[string]HoldersDistribution, len(rows))}
	for k, v := range rows {
		out.Rows[k] = *v
	}
	return out, true, nil
}

// GetHoldersDistribution implements HoldersProvider via an uncached
// pass-through to FetchHoldersExact. The production path goes through
// CachedHolders, which reuses the parsed dump across requests; this
// direct method exists for symmetry with the T86 / TWT93U pattern
// (where HTTPProvider satisfies the public *Provider interface) so a
// Cached wrapper around a non-HTTPProvider Provider still has a
// fallback to call. Single-flight stampede protection lives in the
// cache layer, not here — direct callers pay one fetch per request.
func (p *HTTPProvider) GetHoldersDistribution(ctx context.Context, stockID string, _ time.Time) (HoldersDistribution, error) {
	dump, found, err := p.FetchHoldersExact(ctx)
	if err != nil {
		return HoldersDistribution{}, err
	}
	if !found {
		return HoldersDistribution{}, ErrUnavailable
	}
	return holdersLookup(dump, stockID)
}

// holdersLookup picks the requested stock's row out of a parsed dump,
// surfacing ErrUnavailable when the stock id is absent (delisted /
// pre-listing / non-TWSE). Centralised so CachedHolders and the
// uncached HTTPProvider path share one code path for the lookup.
func holdersLookup(dump holdersDump, stockID string) (HoldersDistribution, error) {
	d, ok := dump.Rows[strings.TrimSpace(stockID)]
	if !ok {
		return HoldersDistribution{}, ErrUnavailable
	}
	return d, nil
}

// fetchLarge is the body-cap-aware sibling of HTTPProvider.fetch. It
// constructs a one-shot http.Client with the holders-specific 15s
// timeout, applies safehttp.BoundBody with the supplied cap (NOT the
// 8 MiB default — the holders dump is legitimately ~9.5 MiB), and
// returns the body bytes. Per-call client construction is cheap; the
// alternative of caching a second long-timeout client on HTTPProvider
// would muddy the lifecycle without saving anything meaningful at
// the once-per-day cadence holders runs at.
func fetchLarge(ctx context.Context, url string, capBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", safehttp.DefaultUserAgent)
	client := safehttp.NewClient(holdersFetchTimeout)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	safehttp.BoundBody(resp, capBytes)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("twse: holders %s: %s", resp.Status, body)
	}
	return io.ReadAll(resp.Body)
}

// validateHoldersSchema fails closed when the first row's key set
// drifts from holdersExpectedKeys — TDCC has renamed datasets before,
// and we'd rather surface ErrUnavailable than render whatever fields
// we managed to recognise. Run on the FIRST row only; schema drift is
// uniform across the response so per-row validation buys nothing.
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

// HoldersFreshFor reports whether holders rows should render given a
// request's asOf and the dump's published AsOf. Returns true (render)
// when the request has no asOf override (zero time) or when the gap
// is within holdersStaleWindow either way; false (skip) when the
// request is pinning a historical OHLC bar that's drifted out of
// alignment with current dispersion. Exported so the /stock handler
// can apply the staleness gate when assembling enrichment rows.
func HoldersFreshFor(reqAsOf, dumpAsOf time.Time) bool {
	if reqAsOf.IsZero() {
		return true
	}
	delta := dumpAsOf.Sub(reqAsOf)
	if delta < 0 {
		delta = -delta
	}
	return delta <= holdersStaleWindow
}
