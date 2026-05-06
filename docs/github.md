# `/github`

Renders public GitHub user / organization profiles and public repos as
banner cards through the standard four wire formats (ASCII / SVG / PNG
/ HTML).

```bash
curl https://imglet.sh/github/octocat
curl https://imglet.sh/github/octocat/Hello-World?format=png
```

## Endpoints

- `GET /github/:user` — profile card for a user or organization login.
- `GET /github/:user/:repo` — repository card for `owner/name`.

`:user` is the GitHub login: alphanumeric segments joined by single
hyphens, max 39 characters, no leading / trailing hyphen, no consecutive
hyphens, no dots, no underscores. `:repo` accepts alphanumerics plus
`.`, `_`, and `-`, max 100 characters. Inputs outside those charsets
return `400 Bad Request` with `Cache-Control: no-store` and a
plain-text body — the request never reaches GitHub, which defends the
upstream rate-limit budget against typo-floods.

The path segments are passed through verbatim; case is preserved into
the upstream query and uppercased only for the rendered headline.

## Card content

**Profile card** (rows in order, each conditional on a non-empty value):

- Headline — login, uppercased.
- Display name.
- Bio.
- `Organization` badge — only when upstream `type == "Organization"`.
- Stats line —
  `★ <followers> followers · ⌥ <public_repos> repos · <following> following · <gists> gists`,
  always rendered, even at zero.
- Company — leading `@` is stripped (pylon parses leading `@` as a directive).
- Blog homepage — `http(s)://` scheme stripped so the row reads as bare host + path.
- Twitter handle — rendered as `x.com/<handle>` (URL form distinguishes it
  from a free-text row and avoids the leading-`@` directive parse).
- Location and `joined <Mon YYYY>` — collapsed onto a single row when
  both are populated (`<location> · joined <Mon YYYY>`); each renders
  alone if the other is missing.

The `Profile.Stars` aggregate (sum of stars across owned repos) is
deliberately omitted: computing it would require a paginated walk of
`/users/:login/repos`, which alone exhausts the unauthenticated 60-req
/hr quota for any moderately active account. Followers + public-repo
count already convey reach.

**Repo card** (rows in order, each conditional on a non-empty value):

- Headline — repo NAME only, uppercased. Owner moves to the first
  caption row so the rendered banner stays the same visual width as
  the user-card banner; viewers that fit-to-width otherwise shrink a
  longer `OWNER/NAME` headline and the body rows read smaller.
- Owner login.
- Description.
- `★ <stars>  ⎇ <forks>  ⚠ <open_issues>` — always rendered.
- `<language> · <license> · <default_branch>` — joined with U+00B7;
  individual missing fields drop out of the join.
- `pushed <relative time ago>` — from `pushed_at`. Thresholds: just
  now / Nm ago / Nh ago / Nd ago (under 30d) / Nmo ago (under 365d) /
  Ny ago.
- `release <tag>` — `tag_name` from `/releases/latest`. Omitted when
  the repo has no releases or the release fetch failed.

License prefers `license.spdx_id` and falls back to `license.name`.

## Format negotiation

Same UA-derived defaults as the rest of imagelet: curl → ASCII,
Mozilla → HTML, unfurl bots and unknown clients → PNG. `?format=`
overrides UA detection. See [routes.md](./routes.md).

`?format=pylon` and `Accept: text/pylon` are NOT supported on
`/github/*` — banner cards have no honest pylon-source representation,
so the handler returns `415 Unsupported Media Type` with a plain-text
redirect message. Mirrors `/qr`'s posture.

## Cache-Control by status

- 200 profile — `public, max-age=600`
- 200 repo — `public, max-age=120`
- 200 stale value (caption `( stale data )`) — `public, max-age=60`
- 200 rate-limited banner — `public, max-age=60`
- 404 login or repo not found — `public, max-age=3600`
- 415 `?format=pylon` — no Cache-Control
- 400 invalid login or path — `no-store`
- 502 upstream failed and no stale value — `no-store`

`Vary: User-Agent` is emitted on every response, including error paths,
so a downstream CDN that already cached one UA class doesn't serve the
wrong body to another. There is no `Vary: Accept-Language` — the
output is locale-agnostic.

The rate-limited banner returns status **200**, not 429. Unfurl bots
and CDN caches honour `max-age=60` on a 200 and stop hammering us
within one TTL window; RFC 7234 doesn't list 429 as cacheable by
default, so a 429 would re-hit our origin on every request.

## Caveats

### Avatar deferred indefinitely

The card is text-only — no avatar image. Compositing the upstream
avatar into PNG output is an HTTP fetch + decode + scale + composite
chain that roughly doubles the per-request work for a banner-only
service. Out of scope for v1; revisit only on an explicit user
request.

### Glyph substitution in captions

The banner font lacks a small set of characters that the upstream API
returns freely in bios, descriptions, and license names. The handler
substitutes them in caption rows (headlines are not affected because
`loginRe` / `repoRe` already constrain the headline charset):

| Upstream | Rendered |
| -------- | -------- |
| `:`      | `;`      |
| `%`      | `pct`    |
| `,`      | (space)  |
| `▲`      | (space)  |
| `▼`      | (space)  |

The `▲` / `▼` substitution is intentional — those arrows belong to
`/stock`'s change-direction vocabulary, and a bio carrying one would
read as a misleading market signal. `:` and `%` are mangled by pylon's
narrow-row themes, so they are normalized at the github layer.

Captions are also truncated to 60 runes (multi-byte safe) with a
trailing `…` suffix.

### 404 cached for 1 hour

A login / repo that returns `ErrNotFound` is cached as 404 for one
hour. If a user just renamed (`oldname` → `newname`), unfurl bots
that cached `/github/oldname` as 404 will keep serving the stale
404 for up to that window. The 1-hour negative TTL is what makes the
unauthenticated 60-req/hr path survivable under typo-floods, so the
trade-off is by design.

### Private repos always 404

`/github/:user/:repo` returns `404 Not Found` for any private repo,
even when the configured `GITHUB_TOKEN` has access (e.g. a token
issued in the configurer's own organization). The public route never
exposes private repo data.

## Authentication

`GITHUB_TOKEN` is an optional environment variable read once at
startup. Effects:

- Unset — requests run unauthenticated; GitHub caps the process at
  60 requests/hour across `/github/*`.
- Set — the token is sent as `Authorization: Bearer <token>`; the
  cap rises to 5000 requests/hour.

The token is **per-process**, not per-user — every visitor's render
shares the configured budget. Treat it as least-privileged read-only
public; the route never exposes private repo data even when a token
with broader scopes is configured.

The token is sourced **only** from the environment, never a CLI flag.
A flag value would land in process listings (`ps -ef`) and shell
history; the env-only contract avoids that. Outbound debug logs go
through `redactedHeaders`, which blanks `Authorization` and `Cookie`
before any header dump.

The startup log records the resolved mode at info level:

```text
github: token mode authed (5000 req/hr)
github: token mode unauthed (60 req/hr)
```

## Localization

`/github/*` emits no localized strings. The locale machinery still
runs per request (so `htmlcache` keys stay locale-aware project-wide),
but the rendered card is the same bytes for every locale at the same
path.

## Examples

```bash
# profile card, ASCII (curl UA)
curl https://imglet.sh/github/octocat

# repo card, force PNG
curl -o card.png https://imglet.sh/github/octocat/Hello-World?format=png

# HTML wrapper with Open Graph meta (unfurl-friendly)
curl https://imglet.sh/github/octocat?format=html
```
