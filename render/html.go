package render

import (
	"bytes"
	"fmt"
	"html"
)

// HelpLabels carries the user-visible strings rendered into the
// keyboard-shortcut help dialog and the prev/next button aria-labels.
// Pre-translated by the caller; render keeps no locale of its own.
type HelpLabels struct {
	Heading string // h2 inside the modal
	Esc     string // dd for the Esc shortcut row
	EscHint string // hint paragraph below the dl
	Prev    string // dd for ←/h row + prev-button aria-label
	Next    string // dd for →/l row + next-button aria-label
	Today   string // dd for t row
	Toggle  string // dd for ? row
}

// DefaultHelpLabels returns the English label set used by tests and
// callers that haven't wired through a locale. Values match
// internal/i18n/en.go's catalog byte-for-byte.
func DefaultHelpLabels() HelpLabels {
	return HelpLabels{
		Heading: "Shortcuts",
		Esc:     "Close help",
		EscHint: "Press Esc or click outside to close",
		Prev:    "Previous day",
		Next:    "Next day",
		Today:   "Today",
		Toggle:  "Toggle help",
	}
}

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
//
// Two wrapper variants are exported:
//
//	WrapHTML              — minimal wrapper, just the centered SVG.
//	WrapHTMLWithDateNav   — same wrapper plus prev/next chevrons, a
//	                        keyboard-shortcut help modal, and a vanilla
//	                        JS handler that rewrites the `date=YYYY-MM-DD`
//	                        query parameter for arrow / vim / `t` keys.
//
// The date-nav fragments are also exposed via InjectDateNav, a post-
// processor that splices into already-wrapped HTML — analogous to
// InjectOGMeta. This lets callers compose date-nav onto a body that
// went through render.BannerBoxes (which calls WrapHTML internally)
// without forking the wrapper.
//
// Both variants emit a single literal `</head>` token so render.InjectOGMeta
// can splice OG / Twitter meta tags before it without further parsing.
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

// navStyleFragment is the extra <style> block injected into <head> for the
// date-nav variant. It sits AFTER the baseline style so its rules layer on
// top, and BEFORE </head> so InjectOGMeta's splice target stays intact.
var navStyleFragment = []byte(`<style>.imagelet-nav-prev,.imagelet-nav-next{position:fixed;top:50%;transform:translateY(-50%);width:48px;height:48px;display:flex;align-items:center;justify-content:center;background:rgba(255,255,255,0.05);color:` + SVGForeground + `;border:1px solid rgba(255,255,255,0.1);border-radius:8px;font-size:32px;line-height:1;cursor:pointer;user-select:none;-webkit-tap-highlight-color:transparent;font-family:serif;padding:0}.imagelet-nav-prev:hover,.imagelet-nav-next:hover{background:rgba(255,255,255,0.12)}.imagelet-nav-prev{left:12px}.imagelet-nav-next{right:12px}.imagelet-help{position:fixed;inset:0;background:rgba(0,0,0,0.6);display:none;align-items:center;justify-content:center;z-index:10}.imagelet-help.visible{display:flex}.imagelet-help-content{max-width:360px;width:90%;background:#1f1f1f;color:` + SVGForeground + `;border-radius:10px;padding:1.25rem 1.5rem;box-sizing:border-box}.imagelet-help-content h2{margin:0 0 .75rem;font-size:1.1rem}.imagelet-help-content dl{display:grid;grid-template-columns:auto 1fr;gap:.4rem .75rem;margin:0}.imagelet-help-content dt{display:inline-block;background:#333;color:` + SVGForeground + `;font-family:ui-monospace,Menlo,Consolas,monospace;font-size:.85rem;padding:.1rem .45rem;border-radius:4px;justify-self:start}.imagelet-help-content dd{margin:0;align-self:center;font-size:.9rem}.imagelet-help-hint{margin:.9rem 0 0;font-size:.8rem;opacity:.65}.imagelet-pastdate{position:fixed;top:12px;left:50%;transform:translateX(-50%);background:#1f1f1f;color:#e9e9e9;border:1px solid rgba(255,255,255,0.15);border-radius:9999px;padding:.35rem .9rem;font-family:ui-monospace,Menlo,Consolas,monospace;font-size:.8rem;z-index:11;box-shadow:0 2px 8px rgba(0,0,0,0.4);display:none}.imagelet-pastdate.visible{display:inline-block}.imagelet-pastdate code{background:rgba(255,255,255,0.1);padding:0 .3rem;border-radius:3px;margin:0 .2rem}@media (max-width: 480px){.imagelet-nav-prev,.imagelet-nav-next{width:36px;height:36px;font-size:24px;top:auto;bottom:12px;transform:none;box-shadow:0 2px 8px rgba(0,0,0,0.4)}.imagelet-nav-prev{left:12px}.imagelet-nav-next{right:12px}.imagelet-pastdate{top:8px;font-size:.75rem;padding:.25rem .7rem}}</style>
`)

// navBodyDialogFmt is the dialog DOM template. Verbs in placement
// order: prev aria-label, next aria-label, dialog heading, prev dd,
// next dd, today dd, toggle dd, esc dd, hint paragraph. All labels
// flow through html.EscapeString before splicing — defense in depth
// against a future translator inserting HTML-special chars.
//
// Vim and arrow bindings are merged into single rows ("← / h") so the
// dialog matches the catalog's five functional shortcuts (prev, next,
// today, toggle, esc) rather than seven keys with English-only "(vim)"
// parentheticals.
const navBodyDialogFmt = `<div class="imagelet-pastdate" id="imagelet-pastdate" role="status" aria-live="polite"></div>
<button type="button" class="imagelet-nav-prev" data-imagelet-nav="-1" aria-label="%s">‹</button>
<button type="button" class="imagelet-nav-next" data-imagelet-nav="1" aria-label="%s">›</button>
<div class="imagelet-help" id="imagelet-help" role="dialog" aria-modal="true" aria-hidden="true" aria-labelledby="imagelet-help-title">
  <div class="imagelet-help-content">
    <h2 id="imagelet-help-title">%s</h2>
    <dl>
      <dt>← / h</dt><dd>%s</dd>
      <dt>→ / l</dt><dd>%s</dd>
      <dt>t</dt><dd>%s</dd>
      <dt>?</dt><dd>%s</dd>
      <dt>Esc</dt><dd>%s</dd>
    </dl>
    <p class="imagelet-help-hint">%s</p>
  </div>
</div>
`

// navBodyScriptFragment is the inline behavior <script>. Split from
// the dialog DOM so the labels-aware splice stays readable; the
// script itself has no localizable content (DOM-driven).
var navBodyScriptFragment = []byte(`<script>
(function () {
  function getCurrentDate() {
    var p = new URLSearchParams(location.search);
    var v = p.get('date');
    if (v && /^\d{4}-\d{2}-\d{2}$/.test(v)) {
      return new Date(v + 'T00:00:00');
    }
    var n = new Date();
    return new Date(n.getFullYear(), n.getMonth(), n.getDate());
  }
  function fmt(d) {
    var y = d.getFullYear();
    var m = String(d.getMonth() + 1).padStart(2, '0');
    var dd = String(d.getDate()).padStart(2, '0');
    return y + '-' + m + '-' + dd;
  }
  function navBy(delta) {
    var d = getCurrentDate();
    d.setDate(d.getDate() + delta);
    var p = new URLSearchParams(location.search);
    p.set('date', fmt(d));
    location.search = p.toString();
  }
  function goToday() {
    var p = new URLSearchParams(location.search);
    p.delete('date');
    var q = p.toString();
    location.search = q ? '?' + q : '';
  }
  function toggleHelp(force) {
    var el = document.getElementById('imagelet-help');
    if (!el) return;
    var show = (force === undefined) ? !el.classList.contains('visible') : force;
    el.classList.toggle('visible', show);
    el.setAttribute('aria-hidden', show ? 'false' : 'true');
  }
  document.addEventListener('keydown', function (e) {
    if (e.repeat) return;
    if (e.target && (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA' || e.target.isContentEditable)) return;
    switch (e.key) {
      case 'ArrowLeft':
      case 'h':
        e.preventDefault();
        navBy(-1);
        break;
      case 'ArrowRight':
      case 'l':
        e.preventDefault();
        navBy(1);
        break;
      case 't':
        e.preventDefault();
        goToday();
        break;
      case '?':
        e.preventDefault();
        toggleHelp();
        break;
      case 'Escape':
        toggleHelp(false);
        break;
    }
  });
  document.querySelectorAll('[data-imagelet-nav]').forEach(function (el) {
    el.addEventListener('click', function (ev) {
      ev.preventDefault();
      navBy(parseInt(el.dataset.imageletNav, 10));
    });
  });
  var help = document.getElementById('imagelet-help');
  if (help) {
    help.addEventListener('click', function (ev) {
      if (ev.target === help) toggleHelp(false);
    });
  }
  function refreshPastDatePill() {
    var pill = document.getElementById('imagelet-pastdate');
    if (!pill) return;
    var p = new URLSearchParams(location.search);
    var v = p.get('date');
    if (v && /^\d{4}-\d{2}-\d{2}$/.test(v)) {
      pill.textContent = '';
      pill.appendChild(document.createTextNode('Viewing '));
      var c = document.createElement('code');
      c.textContent = v;
      pill.appendChild(c);
      pill.appendChild(document.createTextNode(' · press '));
      var k = document.createElement('code');
      k.textContent = 't';
      pill.appendChild(k);
      pill.appendChild(document.createTextNode(' for today'));
      pill.classList.add('visible');
    } else {
      pill.classList.remove('visible');
      pill.textContent = '';
    }
  }
  refreshPastDatePill();
})();
</script>
`)

// bodyCloseTag is the byte sequence InjectDateNav splices the nav DOM
// BEFORE. WrapHTML emits this exact token (literal `</body>`) so the
// splice always lands; if the wrapper is ever changed to emit attributes
// the defensive fallback returns the body unchanged rather than
// corrupting the document.
var bodyCloseTag = []byte("</body>")

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

// WrapHTMLWithDateNav returns the same self-contained HTML5 document as
// WrapHTML, plus fixed-position prev/next chevron buttons, a keyboard-
// shortcut help modal, and an inline vanilla-JS handler that rewrites the
// `date=YYYY-MM-DD` query parameter on arrow / vim / `t` / `?` / `Esc`
// key presses (and matching button clicks).
//
// Implemented as WrapHTML composed with InjectDateNav so the splice
// machinery has a single source of truth shared with callers (e.g.
// /stock) that wrap via render.BannerBoxes and inject after the fact.
func WrapHTMLWithDateNav(svgBody []byte, labels HelpLabels) []byte {
	return InjectDateNav(WrapHTML(svgBody), labels)
}

// InjectDateNav splices the date-nav <style> before </head> and the nav
// DOM + behavior <script> before </body>. When body lacks either tag
// (non-HTML payload, raw SVG, etc.) it is returned unchanged — the call
// is safe to make blindly.
//
// Composes with InjectOGMeta in either order: this function targets
// </head> and </body>, while InjectOGMeta only touches </head>; both
// keep their splice landmarks intact for the other.
func InjectDateNav(body []byte, labels HelpLabels) []byte {
	headIdx := bytes.Index(body, headCloseTag)
	bodyIdx := bytes.Index(body, bodyCloseTag)
	if headIdx < 0 || bodyIdx < 0 {
		return body
	}
	dialog := buildNavBodyDialog(labels)
	out := make([]byte, 0, len(body)+len(navStyleFragment)+len(dialog)+len(navBodyScriptFragment))
	out = append(out, body[:headIdx]...)
	out = append(out, navStyleFragment...)
	out = append(out, body[headIdx:bodyIdx]...)
	out = append(out, dialog...)
	out = append(out, navBodyScriptFragment...)
	out = append(out, body[bodyIdx:]...)
	return out
}

// buildNavBodyDialog formats the dialog DOM with the supplied labels.
// All label values flow through html.EscapeString so a stray HTML-
// special character in a translation can't break the page.
func buildNavBodyDialog(labels HelpLabels) []byte {
	return []byte(fmt.Sprintf(navBodyDialogFmt,
		html.EscapeString(labels.Prev),    // prev-button aria-label
		html.EscapeString(labels.Next),    // next-button aria-label
		html.EscapeString(labels.Heading), // h2
		html.EscapeString(labels.Prev),    // ← / h dd
		html.EscapeString(labels.Next),    // → / l dd
		html.EscapeString(labels.Today),   // t dd
		html.EscapeString(labels.Toggle),  // ? dd
		html.EscapeString(labels.Esc),     // Esc dd
		html.EscapeString(labels.EscHint), // hint paragraph
	))
}
