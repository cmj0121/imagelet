# `/qr`

Encodes a string as a QR code, rendered through the same four wire
formats every other route supports (ASCII / SVG / PNG / HTML).

```bash
curl https://imglet.sh/qr?text=hello&format=ascii
```

## Parameters

- `text` — string, default `https://imglet.sh`. URL-decoded by gin.
  Cap: 1 KiB; oversize requests return `414 URI Too Long`.
- `ec` — string, default `M`. Error-correction level: `L`, `M`, `Q`,
  or `H` (case-insensitive). Bad values silently fall back to `M`.
- `format` — string, default UA-derived. One of `html`, `svg`, `png`,
  `ascii`. Follows the project-wide negotiation rules in
  [routes.md](./routes.md): curl → ASCII, Mozilla → HTML, unfurl
  bots and unknown clients → PNG. `?format=` overrides UA detection.

`?format=pylon` and `Accept: text/pylon` are NOT supported on `/qr`
(QR matrices have no pylon-source representation). The handler returns
`415 Unsupported Media Type` with a redirect message — distinct from
the silent fall-through other rendered routes use for unknown
`?format=` values.

## Error-correction levels

| Level | Recovery | Pick when                                                               |
| ----- | -------- | ----------------------------------------------------------------------- |
| `L`   | ~7 %     | Clean print conditions, want the smallest matrix possible.              |
| `M`   | ~15 %    | Default; balance of size vs damage tolerance.                           |
| `Q`   | ~25 %    | Outdoor signage, wear/dirt expected.                                    |
| `H`   | ~30 %    | Logo overlays in the QR centre, partial occlusion, harshest conditions. |

Higher levels grow the encoded matrix (more redundancy needs more
modules), so an `H` QR is ~2× the byte count of an `L` QR for the
same payload.

## Output

- `?format=svg` — single-file SVG; one `<path>` carrying every dark
  module, one background `<rect>`. Module size 8 px; quiet zone
  4 modules per spec.
- `?format=png` — PNG raster at the same scale. Worst case (1 KiB
  text at level `H`) ≈ 1480×1480 px.
- `?format=html` — the SVG inlined in the project's standard HTML
  wrapper (centred figure, GitHub-dark page background). Open Graph
  meta is injected so unfurl bots see the PNG directly.
- `?format=ascii` — Unicode half-block art (`▀▄█` plus space). One
  character row encodes two module rows; quiet zone is 1 module.

### ASCII scannability

The ASCII surface is **decorative-leaning**: terminal anti-aliasing,
theme inversion, and font ligatures all interfere with phone scanner
autofocus. For guaranteed scannability use `?format=png` or
`?format=svg`. The `ASCII` mode name is kept for project-wide
consistency with `/now`, `/stock`, and `/`; the bytes themselves are
Unicode (U+2580 / U+2584 / U+2588).

## Colours

QR figures render canonical **black-on-white** to maximize scanner
reliability across the long tail of phone cameras — a deliberate
departure from the GitHub-dark palette `PaintSVG` applies to every
other route. The HTML wrapper still paints the page background dark,
so the QR figure reads as a white card on a dark page.

## Caching

```http
Cache-Control: public, max-age=86400
Vary: User-Agent
```

Output is deterministic for `(text, ec, format)`, so a 1-day TTL is
safe. `Vary: User-Agent` is emitted because the default `format` is
UA-derived and a downstream CDN that ignored UA would otherwise serve
the cached body for the wrong client class. (`/now`, `/stock`, `/`
have the same UA-derived negotiation but currently emit no `Vary` —
their bodies happen to read similarly across UAs so the bug stays
invisible. Tracked as a follow-up.)

The HTML response also passes through the in-process `htmlcache` LRU
that wraps every route in `cmd/imagelet/main.go`.

## Two canonical URLs

`/qr` defaults to `https://imglet.sh` — the URL a SCANNER lands on,
i.e. the deploy. The repo caption on `/` still reads
`github.com/cmj0121/imagelet` — where a HUMAN goes to read the source.
The split is intentional: a QR pasted on a poster shouldn't redirect
people through GitHub, and the repo caption shouldn't follow whichever
deploy currently happens to be live.

## Localization

`/qr` emits no localized strings. The locale machinery still resolves
per request (so `htmlcache` keys stay locale-aware project-wide), but
the QR figure itself is the same bytes for every locale at the same
`(text, ec)`.
