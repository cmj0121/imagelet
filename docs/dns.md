# `/dns`

Renders public DNS records for a hostname as a banner card through the
standard four wire formats (ASCII / SVG / PNG / HTML). Pylon source is
not supported.

```bash
curl https://imglet.sh/dns/example.com
curl https://imglet.sh/dns/_dmarc.example.com?format=png
```

## Endpoint

- `GET /dns/:hostname` — banner card for a public hostname.

`:hostname` is normalized before validation:

- Lowercased and trimmed of any trailing dot, so `Example.com.` and
  `EXAMPLE.COM` collapse to one cache key.
- Non-ASCII inputs run through `idna.Lookup.ToASCII`, so `exämple.com`
  reaches the resolver as the canonical punycode form. ASCII inputs
  bypass the IDN step (which would reject RFC 8552 underscored labels
  like `_dmarc`).
- Length-capped at 253 bytes (RFC 1035) after canonicalization.

The validation regex allows a leading underscore per label so the
headline use cases work end-to-end:

- `_dmarc.example.com`
- `_acme-challenge.example.com`
- `_spf.google.com`
- `_sip._tcp.example.com`

The following inputs return `400 Bad Request` with `Cache-Control:
no-store` and a plain-text body, before any wire query is issued:

- IP literals — `1.1.1.1`, `1.1.1.1.` (trailing-dot form), `2606:4700::1111`.
- All-numeric rightmost label — no all-numeric TLDs exist per ICANN, so
  `1.2.3` is treated as an IPv4 fragment typo.
- Empty / oversized hostnames.
- Reserved suffixes: `.local`, `.localhost`, `.internal`, `.lan`,
  `.example`, `.test`, `.invalid`, `.arpa` (RFC 6762 + RFC 2606 +
  RFC 6761).

## Card content

Rows render in a fixed order; empty record types drop. The first
caption row is always the canonical FQDN, so the
first-alphanumeric-label headline rule below does not lose the full
hostname:

- Headline — first dot-separated label that is purely
  alphanumeric, uppercased. `_dmarc.example.com` renders as `EXAMPLE`
  (NOT `_DMARC` — figlet fonts at banner scale don't carry an
  underscore glyph). Bare `com` renders as `COM`. Pathological
  all-underscored names render as `?`.
- FQDN row — the canonical hostname (always present).
- `A` row — public IPv4 addresses joined with `·`. Private,
  loopback, link-local, unspecified, and multicast addresses are
  filtered before this row is built.
- `AAAA` row — public IPv6 addresses, same filter.
- `CNAME` row — single value, trailing dot trimmed.
- `MX` row — `<priority> <host>` pairs joined with `·`.
- `NS` row — authoritative nameservers joined with `·`.
- `TXT` row — prefix-classified summary, NOT raw text. Detects
  `v=spf1` (`spf`), `v=DMARC1` (`dmarc`), `v=DKIM1` (`dkim` /
  `N dkim`), and verification tokens by prefix
  (`google-site-verification=`, `apple-domain-verification=`,
  `facebook-domain-verification=`, `stripe-verification=`, `MS=`,
  `_atproto`). Rendered as a `·`-joined list with counts (e.g.
  `spf · dmarc · 3 verifications`). Anything unmatched falls into
  `N other`. Banner cards summarize, not truncate.
- `SOA` row — `<mname> <rname> <serial>`. `refresh` / `retry` /
  `expire` are dropped from the rendered card.
- `CAA` row — `<flag> <tag> <value>` records joined with `·`.
- `SRV` row — `<priority> <weight> <port> <target>` for the first
  record, plus `· +N more` when more SRV records exist.
- `DNSSEC ✓` row — appears when at least one per-type response
  carried the AD bit. Cloudflare 1.1.1.1 is a validating resolver;
  one set bit is the honest signal that the zone is signed.

Captions are truncated to 60 runes (multi-byte safe) with a trailing
`…` suffix, except numeric rows which are emitted verbatim.

## Format negotiation

Same UA-derived defaults as the rest of imagelet: curl → ASCII,
Mozilla → HTML, unfurl bots and unknown clients → PNG. `?format=`
overrides UA detection. See [routes.md](./routes.md).

`?format=pylon` and `Accept: text/pylon` are NOT supported on
`/dns/*` — banner cards have no honest pylon-source representation,
so the handler returns `415 Unsupported Media Type` with a plain-text
redirect message. Mirrors `/github`'s posture.

## Cache-Control by status

The handler emits one `Cache-Control` value per response state. The
resolver-level positive cache is independent and honors the upstream
record TTL (clamped to `[60s, 600s]`); these values are the
banner-cache TTLs the CDN sees:

- 200 (fresh) — `public, max-age=300`. Resolver layer caches each
  RRset for `min(record TTL, 600s)` with a 60s floor.
- 200 (stale, with `( stale data )` caption appended) —
  `public, max-age=60`. Emitted when the upstream is unavailable but
  a prior good value is still in cache.
- 404 (NXDOMAIN) — `public, max-age=3600`. NS existence-probe
  returned NXDOMAIN, or NS came back empty on success.
- 415 (`?format=pylon`) — no `Cache-Control` header (short-circuit
  before banner emission).
- 400 (invalid hostname) — `no-store`. The request never reaches
  the resolver.
- 503 (self-throttled) — `public, max-age=60`. The process-global
  upstream rate gate denied the query before it left the box. This
  is distinct from the per-IP rate limit, which returns
  `200 + max-age=60` with the `RATE LIMITED` banner.
- 502 (upstream unavailable, no stale value) — `no-store`.

`Vary: User-Agent` is emitted on every response, including error
paths (200 / 400 / 404 / 415 / 502 / 503), so a downstream CDN that
already cached one UA class doesn't serve the wrong body to another.
There is no `Vary: Accept-Language` — the output is locale-agnostic.

The per-IP rate-limited banner returns status **200** (not 429) so
CDN caches and unfurl bots honour `max-age=60` and stop hammering us
within one TTL window. The self-throttled path returns **503** so
the caller learns the route is overloaded server-side, not that the
queried hostname misbehaved; the `max-age=60` keeps the response
CDN-cacheable so a botnet flood doesn't re-hit the origin every
request.

## Caveats

### Private-IP filter is A/AAAA only

The private / loopback / link-local / unspecified / multicast filter
applies to A and AAAA address literals only. CNAME targets, NS hosts,
MX hosts, SRV targets, and TXT free-text values are passed through
verbatim — chasing every record-type leak vector is whack-a-mole.
Operators that don't want CNAME / NS / MX targets enumerated should
not host those records in publicly resolvable zones; the route's
filter is necessary, not sufficient.

### Refused suffixes

The route refuses hostnames that end in `.local`, `.localhost`,
`.internal`, `.lan`, `.example`, `.test`, `.invalid`, or `.arpa`.
These are the RFC 6762 (mDNS), RFC 2606 (reserved), RFC 6761 (special
use), and reverse-DNS namespaces — they should never resolve over
public recursion, and the route declines to participate.

### DoT by default

The upstream resolver speaks DNS-over-TLS (DoT) on port 853, so every
viewer's queried hostname rides the wire encrypted. Operators on
hostile networks (untrusted resolver path, captive-portal middleboxes)
get this for free. Plaintext UDP port 53 is opt-in via the
`DNS_RESOLVER` env var below.

### Process-global upstream rate gate

A `golang.org/x/time/rate.Limiter` caps egress at 100 q/s sustain /
200 burst across the whole process. Under attack the per-IP limit
(30 req/min) × N IPs × 9 record types per request can fan out into a
flood that trips Cloudflare 1.1.1.1's per-source rate limit and
breaks the route for legitimate users. The egress gate caps that
footprint before it leaves the box; calls denied by it return
`ErrSelfThrottled` → `503 + max-age=60`.

### `_dmarc` and friends headline as the parent

Underscored labels are figlet-unsafe at banner scale, so the headline
collapses to the first purely-alphanumeric label of the hostname. The
canonical FQDN appears in the first caption row regardless, so the
full hostname is never lost — only the headline rendering picks the
visually cleaner sibling.

## Resolver configuration

`DNS_RESOLVER` is an optional environment variable read once at
startup. Effects:

- Unset — resolver fallback list defaults to
  `1.1.1.1:853,1.0.0.1:853` (Cloudflare DoT).
- Set — comma-separated `host:port` list, tried in order with a
  200 ms preflight dial per Lookup. The first live resolver wins
  and is reused for that Lookup's nine parallel queries.

The port is required on each entry — DoT-vs-plaintext is encoded as
`:853` vs `:53`, and a silent default would mask a typo. To opt out
of DoT, point at port 53 explicitly:

```bash
DNS_RESOLVER=1.1.1.1:53 imagelet
```

`DNS_RESOLVER_SNI` overrides the TLS hostname presented during the
DoT handshake. Defaults to `cloudflare-dns.com`. Operators using a
self-signed internal resolver set this to match their certificate's
CN / SAN. There is intentionally no `DNS_RESOLVER_INSECURE` knob —
silently skipping TLS verification is a footgun that gets set in dev
and forgotten in prod.

A resolver swap requires a process restart: the in-process cache
holds entries resolved against the previous upstream's recursive
view (and DNSSEC chain), and continuing to serve them would mix
views.

The startup log records the resolved fallback list at info level:

```text
dns: resolver configured (default DoT, port 853) resolvers=["1.1.1.1:853","1.0.0.1:853"]
```

## Examples

```bash
# A / AAAA / NS / SOA card for a public domain (curl UA → ASCII)
curl https://imglet.sh/dns/example.com

# DMARC discovery card — _dmarc renders as the parent's headline
curl https://imglet.sh/dns/_dmarc.example.com

# SRV record card, force PNG output
curl -o sip.png https://imglet.sh/dns/_sip._tcp.example.com?format=png
```
