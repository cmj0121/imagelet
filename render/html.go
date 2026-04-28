package render

// HTML wrapper constants. The doc is hand-rolled (no html/template) because
// the only dynamic part is a pre-rendered, already-XML-safe SVG body and a
// templating layer would only obscure the structure. Keep the prefix and
// suffix as separate byte slices so WrapHTML can stream-concatenate without
// going through a format string.
//
// Responsive sizing: the page centers the SVG with `width: min(96vw, 600px)`
// so the figure scales UP from pylon's native ~360px on desktop (vector
// zoom — text and frame all scale proportionally) and scales DOWN on
// narrow phones. The 600px cap keeps the figure comfortable when the
// rendered content is long (e.g. /stock TW path with three sections);
// without the cap, an ultra-wide monitor blows the SVG to an
// uncomfortable size where the centered captions float far apart.
// Body background matches SVGBackground so the inline figure reads as
// a seamless full-page render rather than a card floating on
// contrasting paper.
var (
	htmlPrefix = []byte(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>imagelet</title>
<style>html,body{margin:0;height:100%}body{display:flex;align-items:center;justify-content:center;background:` + SVGBackground + `;color:` + SVGForeground + `;padding:1rem;box-sizing:border-box}svg{width:100%;max-width:600px;height:auto}</style>
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
