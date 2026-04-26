// Package notfound exposes the 404 fallback for unmatched routes:
// gin's r.NoRoute(Handler) wires it in. The response is a pylon-
// rendered "404" banner stacked above a fake Python-style traceback
// with the requested path injected. The wire format is content-
// negotiated:
//
//   - Accept: text/pylon → raw banner source (no traceback; the
//     traceback isn't pylon syntax)
//   - User-Agent contains Mozilla → image/png (banner-only; the
//     Python traceback is a terminal-only easter egg)
//   - everything else → text/plain; charset=utf-8 with banner +
//     traceback concatenated
//
// The traceback is theatre: file paths, line numbers, hex addresses,
// and the chained KeyError/RouteNotFound exceptions don't correspond
// to anything in the binary. The requested path is the only real
// signal — it appears in the panic message and the trailing field.
package notfound

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/cmj0121/pylon/src/go/pkg/pylon"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
)

// bannerSource is the pylon source for the 404 headline. Banner mode
// renders the digits as ANSI Shadow block letters across 6 rows,
// framed in pylon's native Unicode box (or `+ - |` under theme:ascii).
const bannerSource = "[ 404 | banner ]"

// Register installs Handler as gin's NoRoute fallback. Must take
// *gin.Engine (not gin.IRouter) because NoRoute is engine-level —
// you can't scope it to a route group.
func Register(r *gin.Engine) {
	r.NoRoute(Handler)
}

// Handler renders the 404 page. Status is always 404. See the
// package doc for the negotiation rules.
func Handler(c *gin.Context) {
	path := sanitizePath(c.Request.URL.Path)
	method := c.Request.Method

	c.Header("Cache-Control", "no-store")

	if strings.Contains(c.GetHeader("Accept"), "text/pylon") {
		c.Data(http.StatusNotFound, "text/pylon", []byte(bannerSource+"\n"))
		return
	}

	mode := middleware.GetMode(c)

	if mode == render.ModePNG {
		// Browser path: just the banner. The Python-traceback joke is
		// a terminal-only gift; visitors using a browser get the clean
		// 404 signal without 20 lines of mock stack trace.
		body, err := pylon.RenderPNG(pylon.Parse(bannerSource))
		if err == nil {
			c.Data(http.StatusNotFound, "image/png", body)
			return
		}
		// PNG render failure (font init, encode error) falls through
		// to the ASCII path so the caller still gets *something*.
		log.Error().Err(err).Msg("render 404 png")
	}

	bannerASCII := pylon.RenderASCII(pylon.Parse(bannerSource))
	traceback := pythonTraceback(path, method)
	body := bannerASCII + "\n" + traceback + "\n"
	c.Data(http.StatusNotFound, "text/plain; charset=utf-8", []byte(body))
}

// pythonTraceback returns the literal Python-style traceback body
// with path injected into the panic messages and the trailing
// field. The traceback is prose, not pylon syntax — pylon would
// shred the parens and brackets, so we render the banner separately
// and concatenate this text after.
func pythonTraceback(path, method string) string {
	var b strings.Builder
	b.WriteString("Traceback (most recent call last):\n")
	b.WriteString("  File \"/imagelet/server.py\", line 88, in serve\n")
	b.WriteString("    response = router.dispatch(request)\n")
	b.WriteString("               ^^^^^^^^^^^^^^^^^^^^^^^^^\n")
	b.WriteString("  File \"/imagelet/router.py\", line 127, in dispatch\n")
	b.WriteString("    return self._routes[path](request)\n")
	b.WriteString("                       ~~~~~~~~~~~~^^^\n")
	fmt.Fprintf(&b, "KeyError: '%s'\n", path)
	b.WriteString("\n")
	b.WriteString("The above exception was the direct cause of the following exception:\n")
	b.WriteString("\n")
	b.WriteString("Traceback (most recent call last):\n")
	b.WriteString("  File \"/imagelet/__main__.py\", line 8, in <module>\n")
	b.WriteString("    app.run(host=\"0.0.0.0\", port=8080)\n")
	fmt.Fprintf(&b, "imagelet.errors.RouteNotFound: no handler for '%s'\n", path)
	b.WriteString("\n")
	fmt.Fprintf(&b, "path:   %s\n", path)
	fmt.Fprintf(&b, "method: %s\n", method)
	b.WriteString("status: 404")
	return b.String()
}

// sanitizePath returns the URL path with control characters stripped
// so a hostile or malformed request can't smuggle ANSI escapes /
// newlines / NULs into the literal-text traceback. URL-encoded
// values are passed through as-is — gin has already decoded them
// to runes by the time we read URL.Path.
func sanitizePath(p string) string {
	if p == "" {
		return "/"
	}
	var b strings.Builder
	b.Grow(len(p))
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if out == "" {
		return "/"
	}
	return out
}
