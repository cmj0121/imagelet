package profile

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/cmj0121/imagelet/internal/safehttp"
)

const (
	defaultEndpoint = "https://api.github.com"
	defaultBodyCap  = 1 << 20 // 1 MiB; user/repo payloads are well under
)

// Client is the shared HTTP plumbing for both UserProvider and
// RepoProvider. One Client per process. Singleflight DOES NOT live
// here — it lives exclusively on the cached layer (cached/cached.go).
// The Client is a stateless transport+auth wrapper.
type Client struct {
	endpoint string       // override for tests
	http     *http.Client // safehttp.NewClient(timeout)
	token    string       // "" when unauthenticated; "Bearer X" emitted otherwise
}

// New returns a Client wired to api.github.com with safehttp's hardened
// transport. token is optional ("" runs unauthenticated). The caller
// logs the resolved token mode at startup.
func New(httpClient *http.Client, token string) *Client {
	return &Client{
		endpoint: defaultEndpoint,
		http:     httpClient,
		token:    token,
	}
}

// NewWithEndpoint mirrors yahoo.NewWithEndpoint — point at httptest.Server.
func NewWithEndpoint(endpoint string, httpClient *http.Client, token string) *Client {
	c := New(httpClient, token)
	c.endpoint = endpoint
	return c
}

// IsAuthed reports whether the Client carries a token. Used by the
// startup log line.
func (c *Client) IsAuthed() bool { return c.token != "" }

// do is the one method UserProvider / RepoProvider both call. Status
// mapping:
//
//	200                              → (resp, nil) — caller decodes and closes
//	404                              → ErrNotFound (body drained + closed)
//	403 + X-RateLimit-Remaining: 0   → ErrRateLimited
//	429                              → ErrRateLimited
//	anything else                    → ErrUnavailable (logged at warn)
//
// Headers set on every request:
//
//	Accept: application/vnd.github+json
//	X-GitHub-Api-Version: 2022-11-28
//	User-Agent: safehttp.DefaultUserAgent
//	Authorization: Bearer <token>     (only when token != "")
//
// On every response (success OR error), X-RateLimit-Remaining /
// X-RateLimit-Reset headers are read and emitted into a debug-level
// log line. They are NOT used to gate requests — observability only
// (R11). Per-IP traffic shaping happens in middleware/iplimit.go;
// upstream-budget enforcement is delegated to GitHub itself (which
// emits the 403 we map to ErrRateLimited).
//
// The request log MUST NOT include the Authorization or Cookie headers.
// Use redactedHeaders(r.Header) before any header dump.
func (c *Client) do(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", safehttp.DefaultUserAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		log.Warn().Err(err).Str("path", path).Dur("elapsed", time.Since(start)).Msg("github: transport error")
		return nil, ErrUnavailable
	}
	safehttp.BoundBody(resp, defaultBodyCap)

	remaining := resp.Header.Get("X-RateLimit-Remaining")
	reset := resp.Header.Get("X-RateLimit-Reset")
	logEvt := log.Debug()
	// Numeric parse — lexical compare gives wrong results for unequal
	// digit lengths ("99" < "100" is false because '9' > '1'). When the
	// field is missing or unparseable we fall through to debug.
	if n, err := strconv.Atoi(remaining); err == nil && n < 100 {
		logEvt = log.Warn()
	}
	logEvt.
		Str("path", path).
		Int("status", resp.StatusCode).
		Str("rate_remaining", remaining).
		Str("rate_reset", reset).
		Dur("elapsed", time.Since(start)).
		Msg("github: upstream call")

	switch {
	case resp.StatusCode == http.StatusOK:
		return resp, nil
	case resp.StatusCode == http.StatusNotFound:
		drainAndClose(resp)
		return nil, ErrNotFound
	case resp.StatusCode == http.StatusForbidden && remaining == "0":
		drainAndClose(resp)
		return nil, ErrRateLimited
	case resp.StatusCode == http.StatusTooManyRequests:
		drainAndClose(resp)
		return nil, ErrRateLimited
	default:
		drainAndClose(resp)
		return nil, ErrUnavailable
	}
}

// drainAndClose drains and closes resp.Body so the underlying connection
// can return to the keep-alive pool even on error paths.
func drainAndClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// redactedHeaders returns a shallow copy of h with sensitive values
// blanked. The Client and any debug-log call sites MUST go through
// this helper; logging req.Header directly is a token leak.
func redactedHeaders(h http.Header) http.Header {
	out := h.Clone()
	if out == nil {
		return out
	}
	if out.Get("Authorization") != "" {
		out.Set("Authorization", "REDACTED")
	}
	if out.Get("Cookie") != "" {
		out.Set("Cookie", "REDACTED")
	}
	return out
}
