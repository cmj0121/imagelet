package middleware

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// newCtx builds a gin.Context with a request shaped like a typical inbound
// hit. Keeping the helper local so tests can flip headers without spinning
// up a full server.
func newCtx(t *testing.T, target string, headers map[string]string, tlsState *tls.ConnectionState) *gin.Context {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.TLS = tlsState
	c.Request = req
	return c
}

func TestAbsoluteURLDefaultsToHTTP(t *testing.T) {
	c := newCtx(t, "http://example.com/now", nil, nil)
	got := AbsoluteURL(c, "")
	want := "http://example.com/now"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestAbsoluteURLPreservesQuery(t *testing.T) {
	c := newCtx(t, "http://example.com/stock?region=tw&format=html", nil, nil)
	got := AbsoluteURL(c, "")
	want := "http://example.com/stock?region=tw&format=html"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestAbsoluteURLQueryOverrideMergesAndReplacesKey(t *testing.T) {
	// Override key (format) replaces current value; non-overlapping
	// keys (region) survive.
	c := newCtx(t, "http://example.com/stock?region=tw&format=html", nil, nil)
	got := AbsoluteURL(c, "format=svg")
	want := "http://example.com/stock?format=svg&region=tw"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestAbsoluteURLQueryOverrideMergesNewKey(t *testing.T) {
	// Override adds format=svg without disturbing the existing region key.
	// Mirrors the og:image case where the user shares /stock?region=tw and
	// the preview image must be /stock?region=tw&format=svg.
	c := newCtx(t, "http://example.com/stock?region=tw", nil, nil)
	got := AbsoluteURL(c, "format=svg")
	want := "http://example.com/stock?format=svg&region=tw"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestAbsoluteURLQueryOverridePreservesDateOverride(t *testing.T) {
	// /now?date=2012-02-02 → og:image must keep date= and add format=svg.
	c := newCtx(t, "http://example.com/now?date=2012-02-02", nil, nil)
	got := AbsoluteURL(c, "format=svg")
	want := "http://example.com/now?date=2012-02-02&format=svg"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestAbsoluteURLQueryOverrideOnEmptyQuery(t *testing.T) {
	c := newCtx(t, "http://example.com/now", nil, nil)
	got := AbsoluteURL(c, "format=svg")
	want := "http://example.com/now?format=svg"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestAbsoluteURLPicksHTTPSFromForwardedProto(t *testing.T) {
	c := newCtx(t, "http://example.com/now", map[string]string{
		"X-Forwarded-Proto": "https",
	}, nil)
	got := AbsoluteURL(c, "")
	want := "https://example.com/now"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestAbsoluteURLPicksHTTPSFromTLS(t *testing.T) {
	c := newCtx(t, "http://example.com/now", nil, &tls.ConnectionState{})
	got := AbsoluteURL(c, "")
	want := "https://example.com/now"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestAbsoluteURLForwardedProtoCaseInsensitive(t *testing.T) {
	c := newCtx(t, "http://example.com/now", map[string]string{
		"X-Forwarded-Proto": "HTTPS",
	}, nil)
	got := AbsoluteURL(c, "")
	if got != "https://example.com/now" {
		t.Errorf("got %q want lowercased https scheme", got)
	}
}

func TestAbsoluteURLRejectsJavascriptScheme(t *testing.T) {
	c := newCtx(t, "http://example.com/now", map[string]string{
		"X-Forwarded-Proto": "javascript",
	}, nil)
	got := AbsoluteURL(c, "")
	if !strings.HasPrefix(got, "http://") {
		t.Errorf("got %q, want http:// prefix (javascript scheme must be ignored)", got)
	}
}

func TestAbsoluteURLRejectsDataScheme(t *testing.T) {
	c := newCtx(t, "http://example.com/now", map[string]string{
		"X-Forwarded-Proto": "data:",
	}, nil)
	got := AbsoluteURL(c, "")
	if !strings.HasPrefix(got, "http://") {
		t.Errorf("got %q, want http:// prefix (data: scheme must be ignored)", got)
	}
}

func TestAbsoluteURLAcceptsHTTPSCaseInsensitive(t *testing.T) {
	c := newCtx(t, "http://example.com/now", map[string]string{
		"X-Forwarded-Proto": "HTTPS",
	}, nil)
	got := AbsoluteURL(c, "")
	if !strings.HasPrefix(got, "https://") {
		t.Errorf("got %q, want https:// prefix", got)
	}
}

func TestAbsoluteURLPreservesHostFromRequest(t *testing.T) {
	c := newCtx(t, "http://imagelet.example.com:8443/stock/2330.TW", nil, nil)
	got := AbsoluteURL(c, "")
	want := "http://imagelet.example.com:8443/stock/2330.TW"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
