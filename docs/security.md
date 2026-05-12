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
  MiB for TWSE / TAIFEX, 16 MiB for the TDCC holders dump (the
  only path that exceeds the 8 MiB default; the live response is
  ≈9.5 MiB, the cap leaves headroom but still bounds runaway
  responses) — so a hostile or misbehaving upstream can't exhaust
  memory. The 16 MiB cap is per-fetch on a dedicated client, so
  the global default stays at 8 MiB for everything else.

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

`GET /robots.txt` serves `User-agent: *` / `Disallow: /github/` /
`Disallow: /dns/`. High-frequency crawlers honour it; the Disallow
entries dissuade drive-by SEO enumeration of GitHub login space and
DNS hostname space. The body is hardcoded in `server/server.go`;
there is no static directory.

### `GITHUB_TOKEN` handling

The token is sourced **only** from the environment, never a CLI flag
— a flag value would land in `ps -ef` and shell history. Outbound
debug logs go through a `redactedHeaders` helper that blanks
`Authorization` and `Cookie` before any header dump, so a
`-vv` run on a misbehaving upstream does not leak the token. The
startup log records the resolved mode (authed / unauthed) at info
level.

## `/dns/*`

A public unauthenticated `/dns/*` route is, by construction, a
DNS-by-proxy enumeration tool — a caller can hide their source IP
behind imagelet and ask "what does this name resolve to" against
Cloudflare 1.1.1.1. The mitigations below are the deployed posture;
tune in code if your topology disagrees.

### Per-IP rate limit (DNS)

`/dns/*` is wrapped by the same `middleware/iplimit` token-bucket
used on `/github/*`, on its **own** limiter instance (sharing one
across routes would let a caller's `/github` traffic exhaust their
`/dns` budget). Cap is **30 requests/min/IP**, burst 30, refill
1 token every 2s. When a bucket empties, the middleware
short-circuits to a deterministic banner — status `200`,
`Cache-Control: public, max-age=60`, body containing `RATE LIMITED`.

The IP source matches `/github/*`: `CF-Connecting-IP` first, then
the unspoofable TCP peer `RemoteAddr`. Gin's spoofable
`c.ClientIP()` is intentionally not used.

### Process-global upstream rate gate

The resolver Client carries a `golang.org/x/time/rate.Limiter`
configured at **100 q/s sustain, 200 burst**, shared across the
whole process. Each `/dns/*` request fans out into nine parallel
record-type queries; under attack the per-IP cap × N IPs × 9
fan-out can flood Cloudflare 1.1.1.1's per-source rate limit and
break the route for legitimate users. The gate caps egress to the
upstream BEFORE Cloudflare's per-source budget kicks our IP, so the
worst that happens is `503 + Cache-Control: public, max-age=60` to
the caller — distinct from the per-IP `200 + max-age=60` path.

A query denied by the gate returns `ErrSelfThrottled`; a query that
left the box but came back with a connection-class error or
SERVFAIL returns `ErrUnavailable`. The two map to different status
codes (503 vs 502) so dashboards can distinguish self-imposed back-
pressure from upstream trouble.

### DoT by default

The default upstream resolver is Cloudflare 1.1.1.1:853 over
DNS-over-TLS, with a TLS handshake against `cloudflare-dns.com`.
Operators on hostile networks (untrusted resolver path, captive-
portal middleboxes) get encrypted upstream queries by default; the
viewer's queried hostname does not appear on the wire in plaintext.
Plaintext UDP port 53 is opt-in via `DNS_RESOLVER=host:53`.

There is intentionally no `DNS_RESOLVER_INSECURE` knob. Operators
with a self-signed internal resolver set `DNS_RESOLVER_SNI` to
match the certificate's CN / SAN — that's an explicit, auditable
override. A blanket "skip TLS verification" flag is the kind of
env var that gets set in dev and forgotten in prod; out of scope.

### Private-IP filter (A/AAAA only)

The route strips private, loopback, link-local, unspecified, and
multicast addresses from the rendered A and AAAA rows. CNAME, NS,
MX, SRV, and TXT values are passed through verbatim — chasing
every record-type leak vector is whack-a-mole, and an operator
who registers `internal.attacker.com` with `IN CNAME internal-srv`
in a public zone has already disclosed the name. Document the gap
honestly: the filter is necessary, not sufficient. Operators that
don't want CNAME / NS / MX targets enumerated should not host them
in publicly resolvable zones.

### Refused suffixes

The handler rejects hostnames that end in `.local`, `.localhost`,
`.internal`, `.lan`, `.example`, `.test`, `.invalid`, or `.arpa`
with `400 Bad Request` and `Cache-Control: no-store`, before any
wire query is issued. These cover RFC 6762 (mDNS), RFC 2606
(reserved), RFC 6761 (special use), and reverse-DNS namespaces —
they have no business resolving over public recursion, and the
route declines to participate. IP literals (`1.1.1.1`,
`2606:4700::1111`, trailing-dot variants) and all-numeric rightmost
labels are refused on the same path.

### `robots.txt` (DNS)

`Disallow: /dns/` is added to the hardcoded `robots.txt` body
alongside `Disallow: /github/`. High-frequency crawlers honour it;
the Disallow dissuades SEO enumeration of DNS hostname space.

## `/sysinfo` — opt-in infrastructure disclosure

`GET /sysinfo` returns the server's **hostname, OS name + version,
kernel version, CPU model + core count, total RAM, uptime, and load
average**. This data is enough for an attacker to fingerprint the
exact cloud instance type, kernel patch level, and architecture, then
cross-reference unpatched CVEs.

The route is **disabled by default**. It is only registered when the
binary is started with the `--sysinfo` flag:

```bash
imagelet --sysinfo
```

Do not enable `--sysinfo` on a publicly reachable deployment unless
the endpoint is protected by a reverse-proxy auth layer (Basic Auth,
OAuth, IP allowlist). The route carries `Cache-Control: max-age=5`
and will be cached by any CDN that caches without a cache-key on
auth headers — ensure your CDN strips or bypasses caching for
`/sysinfo` when auth is in use.
