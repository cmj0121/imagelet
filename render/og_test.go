package render

import (
	"bytes"
	"strings"
	"testing"
)

func TestInjectOGMetaInsertsBeforeHeadClose(t *testing.T) {
	body := []byte("<html><head><title>x</title></head><body></body></html>")
	og := OGMeta{
		Title:       "imagelet",
		Description: "show you should know",
		ImageURL:    "https://example.com/now?format=png",
		ImageType:   "image/png",
		PageURL:     "https://example.com/now",
	}

	out := InjectOGMeta(body, og)
	headCloseIdx := bytes.Index(out, []byte("</head>"))
	if headCloseIdx < 0 {
		t.Fatalf("output missing </head>: %s", out)
	}
	prefix := string(out[:headCloseIdx])
	for _, want := range []string{
		`<meta property="og:type" content="website">`,
		`<meta property="og:site_name" content="imagelet">`,
		`<meta property="og:title" content="imagelet">`,
		`<meta property="og:description" content="show you should know">`,
		`<meta property="og:image" content="https://example.com/now?format=png">`,
		`<meta property="og:image:type" content="image/png">`,
		`<meta property="og:url" content="https://example.com/now">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<meta name="twitter:title" content="imagelet">`,
		`<meta name="twitter:description" content="show you should know">`,
		`<meta name="twitter:image" content="https://example.com/now?format=png">`,
	} {
		if !strings.Contains(prefix, want) {
			t.Errorf("missing tag %q\nprefix: %s", want, prefix)
		}
	}
}

func TestInjectOGMetaEscapesAttributeValues(t *testing.T) {
	body := []byte("<head></head>")
	og := OGMeta{
		Title:       `2330 · "台積電" & friends`,
		Description: "<script>alert(1)</script>",
		ImageURL:    "https://example.com/x?a=1&b=2",
	}
	out := string(InjectOGMeta(body, og))

	for _, banned := range []string{
		`<script>`,
		`"台積電"`, // raw double-quote inside attr value would break parsing
	} {
		if strings.Contains(out, banned) {
			t.Errorf("output contains unescaped %q\nout: %s", banned, out)
		}
	}
	for _, want := range []string{
		`&#34;台積電&#34;`,
		`&amp; friends`,
		`&lt;script&gt;alert(1)&lt;/script&gt;`,
		`a=1&amp;b=2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected escaped value %q\nout: %s", want, out)
		}
	}
}

func TestInjectOGMetaEmptyOGReturnsUnchanged(t *testing.T) {
	body := []byte("<html><head></head><body></body></html>")
	out := InjectOGMeta(body, OGMeta{})
	if !bytes.Equal(out, body) {
		t.Errorf("expected body unchanged when og is empty\nbody: %s\nout:  %s", body, out)
	}
}

func TestInjectOGMetaMissingHeadCloseReturnsUnchanged(t *testing.T) {
	body := []byte("<svg><text>hi</text></svg>")
	out := InjectOGMeta(body, OGMeta{Title: "x"})
	if !bytes.Equal(out, body) {
		t.Errorf("expected body unchanged when </head> missing\nbody: %s\nout:  %s", body, out)
	}
}

func TestInjectOGMetaOmitsEmptyFields(t *testing.T) {
	body := []byte("<head></head>")
	og := OGMeta{Title: "imagelet"} // Description/ImageURL/PageURL empty
	out := string(InjectOGMeta(body, og))

	for _, want := range []string{
		`property="og:title"`,
		`property="og:type"`,  // always emitted when any field set
		`name="twitter:card"`, // always emitted when any field set
		`name="twitter:title"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q present\nout: %s", want, out)
		}
	}
	for _, banned := range []string{
		`property="og:description"`,
		`property="og:image"`,
		`property="og:image:type"`,
		`property="og:url"`,
		`name="twitter:description"`,
		`name="twitter:image"`,
	} {
		if strings.Contains(out, banned) {
			t.Errorf("expected %q absent\nout: %s", banned, out)
		}
	}
}

func TestInjectOGMetaWrapHTMLIntegration(t *testing.T) {
	wrapped := WrapHTML([]byte("<svg/>"))
	og := OGMeta{Title: "imagelet", PageURL: "https://example.com/"}
	out := InjectOGMeta(wrapped, og)
	if !bytes.Contains(out, []byte(`property="og:title"`)) {
		t.Errorf("expected og:title in WrapHTML output: %s", out)
	}
	if !bytes.Contains(out, []byte("<svg/>")) {
		t.Errorf("expected svg body preserved: %s", out)
	}
	headCloseIdx := bytes.Index(out, []byte("</head>"))
	titleIdx := bytes.Index(out, []byte(`property="og:title"`))
	if titleIdx < 0 || titleIdx > headCloseIdx {
		t.Errorf("og:title should appear before </head>; titleIdx=%d headCloseIdx=%d", titleIdx, headCloseIdx)
	}
}

func TestOGMetaIsZero(t *testing.T) {
	if !(OGMeta{}).IsZero() {
		t.Errorf("zero OGMeta should report IsZero")
	}
	if (OGMeta{Title: "x"}).IsZero() {
		t.Errorf("OGMeta with Title should not report IsZero")
	}
	if (OGMeta{ImageURL: "x"}).IsZero() {
		t.Errorf("OGMeta with ImageURL should not report IsZero")
	}
}
