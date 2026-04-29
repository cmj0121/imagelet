package safehttp

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// mustReq builds a *http.Request for a test URL, panicking on error.
// Tests synth via=[] / via=[req] slices to drive RedirectPolicy
// without spinning up a real server.
func mustReq(t *testing.T, rawURL string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("NewRequest %q: %v", rawURL, err)
	}
	return req
}

func TestRedirectPolicyAllowsSameETLDPlus1(t *testing.T) {
	prev := mustReq(t, "https://query1.finance.yahoo.com/path")
	next := mustReq(t, "https://query2.finance.yahoo.com/path")
	if err := RedirectPolicy(next, []*http.Request{prev}); err != nil {
		t.Fatalf("RedirectPolicy refused same eTLD+1 hop: %v", err)
	}
}

func TestRedirectPolicyRejectsLoopback(t *testing.T) {
	prev := mustReq(t, "https://www.example.com/")
	next := mustReq(t, "http://127.0.0.1/")
	err := RedirectPolicy(next, []*http.Request{prev})
	if err == nil {
		t.Fatalf("RedirectPolicy: want error for loopback, got nil")
	}
	if !strings.Contains(err.Error(), "unsafe host") {
		t.Errorf("err = %v, want substring %q", err, "unsafe host")
	}
}

func TestRedirectPolicyRejectsPrivateIP(t *testing.T) {
	prev := mustReq(t, "https://www.example.com/")
	next := mustReq(t, "http://192.168.1.1/")
	err := RedirectPolicy(next, []*http.Request{prev})
	if err == nil {
		t.Fatalf("RedirectPolicy: want error for private IP, got nil")
	}
	if !strings.Contains(err.Error(), "unsafe host") {
		t.Errorf("err = %v, want substring %q", err, "unsafe host")
	}
}

func TestRedirectPolicyRejectsLinkLocal(t *testing.T) {
	prev := mustReq(t, "https://www.example.com/")
	next := mustReq(t, "http://169.254.169.254/")
	err := RedirectPolicy(next, []*http.Request{prev})
	if err == nil {
		t.Fatalf("RedirectPolicy: want error for link-local, got nil")
	}
	if !strings.Contains(err.Error(), "unsafe host") {
		t.Errorf("err = %v, want substring %q", err, "unsafe host")
	}
}

func TestRedirectPolicyRejectsTooManyHops(t *testing.T) {
	via := make([]*http.Request, DefaultRedirectCap)
	for i := range via {
		via[i] = mustReq(t, "https://example.com/")
	}
	next := mustReq(t, "https://example.com/again")
	err := RedirectPolicy(next, via)
	if err == nil {
		t.Fatalf("RedirectPolicy: want error for too-many redirects, got nil")
	}
	if !strings.Contains(err.Error(), "too many redirects") {
		t.Errorf("err = %v, want substring %q", err, "too many redirects")
	}
}

func TestBoundBodyCapsRead(t *testing.T) {
	const capBytes = 1024
	resp := &http.Response{
		Body: io.NopCloser(strings.NewReader(strings.Repeat("a", 1<<20))),
	}
	BoundBody(resp, capBytes)

	_, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatalf("ReadAll: want error from MaxBytesReader cap, got nil")
	}
}
