// Package qr exposes the /qr service: GET /qr returns a QR code
// encoding either DefaultText (when no ?text= is supplied) or the
// caller-supplied text, rendered in one of four wire formats per
// the project-wide format-negotiation contract:
//
//   - ?format=html or User-Agent contains Mozilla → text/html (inline SVG)
//   - ?format=svg → image/svg+xml
//   - ?format=png → image/png
//   - ?format=ascii → text/plain; charset=utf-8 (Unicode half-block art)
//   - everything else (incl. unfurl bots, unknown clients) → image/png
//
// `?format=pylon` and `Accept: text/pylon` are NOT supported on /qr —
// QR matrices have no meaningful pylon-source representation. The
// handler returns 415 instead of falling through silently.
//
// Caller-supplied `?text=` is capped at MaxQRTextBytes; oversize
// requests return 414 to match middleware/sizelimit.go's convention.
//
// The handler is reusable: external consumers can call Register on any
// gin.IRouter (mounting it under any prefix), or invoke Handler from
// their own routing layer.
package qr

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
)

// DefaultText is the URL encoded by /qr when no ?text= is supplied.
// Distinct from index.DefaultRepo (the source-code repo): this is the
// canonical deploy URL where a SCANNER lands. Two canonicals on
// purpose — /qr defaults to where the rendered service lives,
// /index advertises where the source lives.
const DefaultText = "https://imglet.sh"

// MaxQRTextBytes caps the ?text= input length. 1 KiB sits well under
// the outer middleware.MaxRequestQueryBytes (4 KiB) and implicitly
// bounds the encoded matrix to ≤ Version 40 (177×177 modules) since
// skip2's New() errors when content can't fit Version 40 at the
// chosen recovery level. Worst-case PNG ≈ 1480×1480 px ≈ 100–200 KB.
const MaxQRTextBytes = 1 << 10

// pylonUnsupportedMsg is the body returned for ?format=pylon /
// Accept: text/pylon on /qr. /now, /index, /notfound emit real pylon
// source for that path; /qr cannot — QR matrices aren't pylon syntax.
// 415 with a human-readable redirect is honest.
const pylonUnsupportedMsg = "pylon source is not supported on /qr; try ?format=png|svg|html|ascii"

// Register mounts GET /qr -> Handler on r. r is typed as gin.IRouter
// so the service can be installed on either a *gin.Engine or a route
// group (e.g. for prefix-based versioning later).
func Register(r gin.IRouter) {
	r.GET("/qr", Handler)
}

// Handler responds with a QR code encoding ?text= (or DefaultText)
// at the requested error-correction level (?ec=L|M|Q|H, default M).
//
// Headers:
//
//   - Cache-Control: public, max-age=86400 — output is deterministic
//     for (text, ec, mode) so a 1-day TTL is safe.
//   - Vary: User-Agent — flagged because /qr's UA-derived format
//     negotiation produces dramatically different bodies (PNG vs ASCII
//     vs HTML) per UA class. Without this header a downstream CDN
//     would cache one UA's response and serve it to every other UA,
//     producing wrong-content-type responses. The other rendered
//     routes (/, /now, /stock) have the same latent bug but their
//     bodies happen to be visually similar across UAs; QR makes the
//     bug visible. Fix for those routes is out of scope for this PR.
func Handler(c *gin.Context) {
	if middleware.WantsPylonSource(c) {
		c.Data(http.StatusUnsupportedMediaType, "text/plain; charset=utf-8", []byte(pylonUnsupportedMsg))
		return
	}

	text := c.Query("text")
	if text == "" {
		text = DefaultText
	}
	if len(text) > MaxQRTextBytes {
		c.AbortWithStatus(http.StatusRequestURITooLong)
		return
	}

	ec := render.ParseQRLevel(c.Query("ec"))
	mode := middleware.ResolveMode(c)

	body, err := render.QR(text, ec, mode)
	if err != nil {
		// /now's PNG-failure pattern falls back to ASCII because /now's
		// failures live in the renderer (font init / png.Encode quirks)
		// while the headline + caption are already known-good. /qr's
		// failure surface is upstream of the renderer — skip2.New
		// errors when text overflows Version 40 capacity at the chosen
		// EC level — so a fallback render call would hit the same error
		// and cache an empty 200 for a day under the public Cache-
		// Control header. Return 500 instead. Currently unreachable
		// behind the 1 KiB cap (V40 at L holds ~2.9 KiB) but the code
		// stays correct under a future cap raise.
		log.Error().Err(err).Stringer("mode", mode).Str("ec", ec.String()).Msg("render qr")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.Header("Cache-Control", "public, max-age=86400")
	c.Header("Vary", "User-Agent")

	switch mode {
	case render.ModePNG:
		c.Data(http.StatusOK, "image/png", body)
	case render.ModeSVG:
		c.Data(http.StatusOK, "image/svg+xml", body)
	case render.ModeHTML:
		og := render.OGMeta{
			Title:       "imagelet · QR",
			Description: text,
			ImageURL:    middleware.AbsoluteURL(c, "format=png"),
			ImageType:   "image/png",
			PageURL:     middleware.AbsoluteURL(c, ""),
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", render.InjectOGMeta(body, og))
	default:
		c.Data(http.StatusOK, "text/plain; charset=utf-8", body)
	}
}
