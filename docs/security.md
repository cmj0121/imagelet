# Security & limits

Imagelet runs assuming a TLS-terminating reverse proxy or CDN sits in
front of it. The defaults below cap the obvious abuse vectors; tune
them in code if your deployment topology disagrees.

## Request size caps

The router rejects oversized request lines before the handler sees them:

| Limit        | Cap    | Response                |
| ------------ | ------ | ----------------------- |
| URL path     | 1 KiB  | `414 URI Too Long`      |
| Query string | 4 KiB  | `414 URI Too Long`      |
| Request body | 64 KiB | `413 Payload Too Large` |

The body cap is defense-in-depth — no current route reads a body —
and exists so a future POST handler doesn't accidentally inherit an
unbounded reader.

## `?text=` cap on `/qr`

`/qr?text=` is capped at **1 KiB** — anything longer returns
`414 URI Too Long`, mirroring the URL-path / query-string limiters
above. The cap is sized so encoded matrices stay within QR Version 40
(177×177 modules) regardless of error-correction level, which bounds
the worst-case PNG render at ~1480×1480 px.

## `?date=` clamp

The `date=YYYY-MM-DD` override is clamped to `[now - 15y, now + 1d]`.
Values outside the window fall through to the live path, same as a
malformed date. The 1-day future tolerance absorbs caller / server
clock skew across timezones.

## `X-Forwarded-Proto` allowlist

When the request carries `X-Forwarded-Proto`, the value is honored
only when it is exactly `http` or `https`. Anything else (including
empty, `HTTPS`, or attacker-supplied junk) is ignored and the
connection's own scheme is used. Configure your proxy to set the
header to a lowercase literal.

## Snapshot decode cap

Cache snapshot files are decoded with a hard ceiling of
`MaxSnapshotBytes = 64 MiB` per file. A snapshot that exceeds the
cap returns `ErrSnapshotTooLarge`; the wrapper logs, drops the
oversize file (so a future save replaces it) and starts with an
empty in-memory cache.

## Outbound SSRF guards

The Yahoo / TWSE / TAIFEX HTTP clients go through `internal/safehttp`,
which:

- refuses redirects whose resolved IP is private, loopback,
  link-local, unspecified, or multicast;
- allows cross-host redirects only within the same eTLD+1 (so
  Yahoo's `query1` ↔ `query2` fail-over still works);
- wraps response bodies in `MaxBytesReader` — 1 MiB for Yahoo, 8
  MiB for TWSE / TAIFEX — so a hostile or misbehaving upstream
  can't exhaust memory.

## `/github/*`

A public unauthenticated `/github/*` route plus an optional
5000-req/hr `GITHUB_TOKEN` is, by construction, a GitHub-data
exfiltration tool by proxy. The mitigations below are the deployed
posture; tune in code if your topology disagrees.

### Per-IP rate limit

`/github/*` is wrapped by a token-bucket middleware
(`middleware/iplimit`) at **30 requests/min/IP**, burst 30, refill
1 token every 2s. When a bucket empties, the middleware short-circuits
to the same deterministic banner the upstream-rate-limited path emits
— status `200`, `Cache-Control: public, max-age=60`, body containing
`RATE LIMITED`. The two paths are intentionally indistinguishable to
the caller; only an internal log line distinguishes per-IP from
upstream.

The IP source is, in order:

1. `CF-Connecting-IP` request header (the trusted-proxy header
   Cloudflare sets and strips on inbound).
2. The TCP peer's `RemoteAddr` host portion.

The middleware does **not** call `c.ClientIP()`. Gin's default
`TrustedProxies` is `[0.0.0.0/0]`, which walks every value in
`X-Forwarded-For` — that makes the gin-derived client IP spoofable by
any caller setting the header. `server.New` does not call
`SetTrustedProxies`, so the permissive default is in force; the
limiter sidesteps it entirely. `RemoteAddr` is the unspoofable TCP
peer, so the bare-deploy case is also covered.

### Private-repo guard

`/github/:user/:repo` returns `404 Not Found` for any repo whose
upstream payload carries `private: true`, regardless of HTTP status
or what the configured `GITHUB_TOKEN` could see. The public route
never leaks private repo data even when the token is issued in the
configurer's own organization. Treat the configured token as
least-privileged read-only public.

### `robots.txt`

`GET /robots.txt` serves `User-agent: *` / `Disallow: /github/`.
High-frequency crawlers honour it; the Disallow dissuades drive-by
SEO enumeration of GitHub login space. The body is hardcoded in
`server/server.go`; there is no static directory.

### `GITHUB_TOKEN` handling

The token is sourced **only** from the environment, never a CLI flag
— a flag value would land in `ps -ef` and shell history. Outbound
debug logs go through a `redactedHeaders` helper that blanks
`Authorization` and `Cookie` before any header dump, so a
`-vv` run on a misbehaving upstream does not leak the token. The
startup log records the resolved mode (authed / unauthed) at info
level.
