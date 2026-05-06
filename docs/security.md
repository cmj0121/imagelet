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
