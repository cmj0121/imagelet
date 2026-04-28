package render

import (
	"bytes"
	"html"
	"strings"
)

// OGMeta carries the Open Graph + Twitter Card meta values that handlers
// want injected into the HTML <head>. Empty values are omitted from the
// emitted tag set so callers can populate only the fields they have.
//
// All fields are user-visible plain text (Title / Description) or
// already-built absolute URLs (ImageURL / PageURL). InjectOGMeta
// HTML-attribute-escapes every value before splicing — callers do NOT
// need to escape themselves.
type OGMeta struct {
	Title       string // og:title + twitter:title
	Description string // og:description + twitter:description
	ImageURL    string // og:image + twitter:image (absolute URL)
	ImageType   string // og:image:type (MIME, e.g. image/png) — emitted only when set
	PageURL     string // og:url (absolute URL, canonical)
}

// IsZero reports whether og has no fields populated. InjectOGMeta uses
// this to short-circuit when the caller hasn't supplied any metadata —
// the wrapped HTML is returned unchanged so callers can blindly call
// InjectOGMeta without checking themselves.
func (og OGMeta) IsZero() bool {
	return og.Title == "" && og.Description == "" && og.ImageURL == "" && og.PageURL == ""
}

// headCloseTag is the byte sequence InjectOGMeta splices BEFORE. WrapHTML
// emits this exact token (literal `</head>` with no attributes — `<head>`
// has none either). If WrapHTML ever changes to emit attributes on the
// head element the splice would silently miss; the defensive
// return-unchanged keeps that scenario safe rather than corrupting the
// document.
var headCloseTag = []byte("</head>")

// InjectOGMeta returns body with Open Graph + Twitter Card <meta> tags
// spliced before the first `</head>`. When body has no `</head>`
// (non-HTML payload, raw SVG, etc.) or og is empty, body is returned
// unchanged — the call is safe to make blindly.
//
// Tags emitted (per non-empty field):
//
//	og:title, og:description, og:image, og:url
//	og:type=website, og:site_name=imagelet (always when any field set)
//	twitter:card=summary_large_image (always when any field set)
//	twitter:title, twitter:description, twitter:image
//
// Every value is HTML-attribute-escaped via html.EscapeString. The
// function builds a single byte block and inserts it once, so repeated
// calls on the same body would accumulate duplicates — callers wire
// it once per response.
func InjectOGMeta(body []byte, og OGMeta) []byte {
	if og.IsZero() {
		return body
	}
	idx := bytes.Index(body, headCloseTag)
	if idx < 0 {
		return body
	}
	block := buildOGBlock(og)
	out := make([]byte, 0, len(body)+len(block))
	out = append(out, body[:idx]...)
	out = append(out, block...)
	out = append(out, body[idx:]...)
	return out
}

// buildOGBlock formats the meta-tag block. Each tag sits on its own
// line ending with a newline so the spliced HTML reads naturally when
// dumped to a terminal. The block is bounded by leading + trailing
// newlines so it sits between the previous </style> (or whatever)
// and the </head> close.
func buildOGBlock(og OGMeta) []byte {
	var b strings.Builder
	b.WriteByte('\n')
	writeMetaProperty(&b, "og:type", "website")
	writeMetaProperty(&b, "og:site_name", "imagelet")
	if og.Title != "" {
		writeMetaProperty(&b, "og:title", og.Title)
	}
	if og.Description != "" {
		writeMetaProperty(&b, "og:description", og.Description)
	}
	if og.ImageURL != "" {
		writeMetaProperty(&b, "og:image", og.ImageURL)
	}
	if og.ImageType != "" {
		writeMetaProperty(&b, "og:image:type", og.ImageType)
	}
	if og.PageURL != "" {
		writeMetaProperty(&b, "og:url", og.PageURL)
	}
	writeMetaName(&b, "twitter:card", "summary_large_image")
	if og.Title != "" {
		writeMetaName(&b, "twitter:title", og.Title)
	}
	if og.Description != "" {
		writeMetaName(&b, "twitter:description", og.Description)
	}
	if og.ImageURL != "" {
		writeMetaName(&b, "twitter:image", og.ImageURL)
	}
	return []byte(b.String())
}

func writeMetaProperty(b *strings.Builder, property, content string) {
	b.WriteString(`<meta property="`)
	b.WriteString(property)
	b.WriteString(`" content="`)
	b.WriteString(html.EscapeString(content))
	b.WriteString("\">\n")
}

func writeMetaName(b *strings.Builder, name, content string) {
	b.WriteString(`<meta name="`)
	b.WriteString(name)
	b.WriteString(`" content="`)
	b.WriteString(html.EscapeString(content))
	b.WriteString("\">\n")
}
