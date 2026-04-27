package render

// HTML wrapper constants. The doc is hand-rolled (no html/template) because
// the only dynamic part is a pre-rendered, already-XML-safe SVG body and a
// templating layer would only obscure the structure. Keep the prefix and
// suffix as separate byte slices so WrapHTML can stream-concatenate without
// going through a format string.
//
// Responsive sizing: the page centers the SVG with `width: min(96vw, 1200px)`
// so the figure scales UP from pylon's native ~360px on desktop (vector
// zoom — text and frame all scale proportionally, making the rendered
// content readable on a 1080p+ screen) and scales DOWN on narrow phones.
// The 1200px cap keeps ultra-wide monitors from blowing the figure to an
// uncomfortable size. Body background matches SVGBackground so the inline
// figure reads as a seamless full-page render rather than a card floating
// on contrasting paper.
var (
	htmlPrefix = []byte(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>imagelet</title>
<style>html,body{margin:0;height:100%}body{display:flex;align-items:center;justify-content:center;background:` + SVGBackground + `;color:` + SVGForeground + `;padding:1rem;box-sizing:border-box}svg{width:100%;max-width:1200px;height:auto}</style>
</head>
<body>
`)
	htmlSuffix = []byte(`
</body>
</html>
`)
)

// WrapHTML returns a self-contained HTML5 document inlining svgBody in the
// document body. svgBody is written verbatim — pylon's RenderSVG already
// XML-escapes text content, so the caller is responsible for ensuring any
// custom-built SVG is also XML-safe before passing it here.
//
// The returned bytes start with the HTML5 doctype and end with a trailing
// newline so file writes / shell redirection produce a clean file. The
// page is fully self-contained: no external CSS, fonts, or scripts.
func WrapHTML(svgBody []byte) []byte {
	out := make([]byte, 0, len(htmlPrefix)+len(svgBody)+len(htmlSuffix))
	out = append(out, htmlPrefix...)
	out = append(out, svgBody...)
	out = append(out, htmlSuffix...)
	return out
}
