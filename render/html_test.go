package render

import (
	"bytes"
	"strings"
	"testing"
)

// TestWrapHTML_StillBaseline pins the baseline wrapper: it must contain the
// SVG body verbatim, retain the splice target </head>, end with the
// </body></html> close, and emit no <script> block. If this regresses the
// date-nav variant probably leaked into htmlPrefix / htmlSuffix.
func TestWrapHTML_StillBaseline(t *testing.T) {
	out := WrapHTML([]byte("<svg/>"))
	s := string(out)
	if !strings.Contains(s, "<svg/>") {
		t.Errorf("baseline wrapper dropped svg body:\n%s", s)
	}
	if !strings.Contains(s, "</head>") {
		t.Errorf("baseline wrapper missing </head> splice target:\n%s", s)
	}
	if !strings.Contains(s, "</body>\n</html>\n") {
		t.Errorf("baseline wrapper missing </body></html> close:\n%s", s)
	}
	if strings.Contains(s, "<script>") {
		t.Errorf("baseline wrapper unexpectedly embeds <script>:\n%s", s)
	}
}

// TestWrapHTMLWithDateNav_HasScript checks the inline behavior is wired in.
// The keydown listener is the spec's central hook so we pin the literal
// substring rather than a looser match.
func TestWrapHTMLWithDateNav_HasScript(t *testing.T) {
	out := WrapHTMLWithDateNav([]byte("<svg/>"))
	s := string(out)
	if !strings.Contains(s, "<script>") {
		t.Errorf("date-nav wrapper missing <script> block")
	}
	if !strings.Contains(s, "addEventListener('keydown'") {
		t.Errorf("date-nav wrapper missing keydown handler")
	}
}

// TestWrapHTMLWithDateNav_HasNavButtons asserts both chevron buttons are
// emitted with the data-imagelet-nav delta the script reads.
func TestWrapHTMLWithDateNav_HasNavButtons(t *testing.T) {
	out := WrapHTMLWithDateNav([]byte("<svg/>"))
	s := string(out)
	for _, want := range []string{
		`data-imagelet-nav="-1"`,
		`data-imagelet-nav="1"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing nav attribute %q", want)
		}
	}
}

// TestWrapHTMLWithDateNav_HasHelpDialog checks the help modal markup
// (id, role) and a couple of shortcut entries that act as a canary that
// the <dl> survived editing.
func TestWrapHTMLWithDateNav_HasHelpDialog(t *testing.T) {
	out := WrapHTMLWithDateNav([]byte("<svg/>"))
	s := string(out)
	for _, want := range []string{
		`id="imagelet-help"`,
		`role="dialog"`,
		`<dt>?</dt>`,
		`<dt>t</dt>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing help-dialog fragment %q", want)
		}
	}
}

// TestWrapHTMLWithDateNav_OGInjectionStillWorks proves the date-nav variant
// keeps its </head> splice target intact and that OG insertion does not
// truncate the trailing nav/script block.
func TestWrapHTMLWithDateNav_OGInjectionStillWorks(t *testing.T) {
	body := WrapHTMLWithDateNav([]byte("<svg/>"))
	out := InjectOGMeta(body, OGMeta{Title: "x", Description: "y"})
	s := string(out)
	for _, want := range []string{
		`property="og:title"`,
		`id="imagelet-help"`,
		`<script>`,
		`addEventListener('keydown'`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("OG-injected wrapper missing %q\nfull:\n%s", want, s)
		}
	}
}

// TestInjectDateNav_AddsScriptAndStyle checks the post-processor wires the
// same fragments as the wrapper variant when applied to a plain WrapHTML
// body. Pins script presence, the prev-button data attribute, the help
// dialog id, and the literal nav CSS class so a regression in either
// fragment is caught.
func TestInjectDateNav_AddsScriptAndStyle(t *testing.T) {
	out := InjectDateNav(WrapHTML([]byte("<svg/>")))
	s := string(out)
	for _, want := range []string{
		"<script>",
		`data-imagelet-nav="-1"`,
		`id="imagelet-help"`,
		".imagelet-nav-prev",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("InjectDateNav output missing %q\nfull:\n%s", want, s)
		}
	}
}

// TestInjectDateNav_NoOpWithoutTags asserts the defensive fallback: when
// neither </head> nor </body> is present the input is returned byte-for-
// byte unchanged so callers can splice blindly without an HTML check.
func TestInjectDateNav_NoOpWithoutTags(t *testing.T) {
	in := []byte("plain text")
	out := InjectDateNav(in)
	if !bytes.Equal(in, out) {
		t.Errorf("InjectDateNav mutated tag-less input:\nin:  %q\nout: %q", in, out)
	}
}

// TestInjectDateNav_ComposesWithOGMeta proves the two post-processors
// stack: nav DOM via InjectDateNav then OG meta via InjectOGMeta both
// land in the final document with all splice targets intact.
func TestInjectDateNav_ComposesWithOGMeta(t *testing.T) {
	body := InjectDateNav(WrapHTML([]byte("<svg/>")))
	out := InjectOGMeta(body, OGMeta{Title: "x", Description: "y"})
	s := string(out)
	for _, want := range []string{
		`property="og:title"`,
		`id="imagelet-help"`,
		"<script>",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("composed wrapper missing %q\nfull:\n%s", want, s)
		}
	}
}

// TestWrapHTMLWithDateNav_PreservesSVG checks the SVG body lands between
// </head> and the first nav button — i.e. it sits inside <body> ahead of
// the injected DOM, where InjectOGMeta and any future enrichment expect it.
func TestWrapHTMLWithDateNav_PreservesSVG(t *testing.T) {
	marker := []byte(`<svg id="probe"/>`)
	out := WrapHTMLWithDateNav(marker)

	headEnd := bytes.Index(out, []byte("</head>"))
	if headEnd < 0 {
		t.Fatalf("output missing </head>: %s", out)
	}
	probeIdx := bytes.Index(out, marker)
	if probeIdx < 0 {
		t.Fatalf("output missing svg marker: %s", out)
	}
	navIdx := bytes.Index(out, []byte(`class="imagelet-nav-prev"`))
	if navIdx < 0 {
		t.Fatalf("output missing prev nav button: %s", out)
	}
	if !(headEnd < probeIdx && probeIdx < navIdx) {
		t.Errorf("svg marker out of order: head=%d probe=%d nav=%d", headEnd, probeIdx, navIdx)
	}
}
