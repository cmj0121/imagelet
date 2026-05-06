package github_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/service/github"
	"github.com/cmj0121/imagelet/service/github/profile"
)

// pngMagic is the 8-byte PNG file signature.
var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

// fakeUser implements profile.UserProvider with canned responses + a
// per-call counter so tests can assert "no upstream call" for the 400
// validation paths.
type fakeUser struct {
	mu    sync.Mutex
	p     profile.Profile
	err   error
	calls int
}

func (f *fakeUser) User(_ context.Context, _ string) (profile.Profile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.p, f.err
}

// fakeRepo mirrors fakeUser for profile.RepoProvider.
type fakeRepo struct {
	mu    sync.Mutex
	r     profile.Repo
	err   error
	calls int
}

func (f *fakeRepo) Repo(_ context.Context, _, _ string) (profile.Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.r, f.err
}

// freshProfile returns a deterministic Profile with every field
// populated so caption-row assertions don't depend on optional fields.
func freshProfile() profile.Profile {
	return profile.Profile{
		Login:       "octocat",
		Name:        "The Octocat",
		Bio:         "interactive demos and tutorials",
		Location:    "San Francisco",
		PublicRepos: 8,
		Followers:   1234,
		JoinedAt:    time.Date(2008, 4, 1, 0, 0, 0, 0, time.UTC),
	}
}

// freshRepo returns a deterministic Repo with every field populated.
// PushedAt is set to a fixed past instant; tests that need a specific
// humanizePushed bucket override the field locally.
func freshRepo() profile.Repo {
	return profile.Repo{
		FullName:      "octocat/Hello-World",
		Description:   "my first repository on GitHub",
		Stars:         2300,
		Forks:         1700,
		OpenIssues:    12,
		Language:      "Go",
		License:       "MIT",
		DefaultBranch: "main",
		PushedAt:      time.Now().Add(-3 * 24 * time.Hour),
		LatestRelease: "v1.2.3",
	}
}

// newRouter wires the middleware the handler depends on (ClientDetector
// for UA-derived mode resolution) plus the github routes mounted at the
// engine root. Tests construct fakes per-case for deterministic assertions.
func newRouter(up profile.UserProvider, rp profile.RepoProvider) *gin.Engine {
	r := gin.New()
	r.Use(middleware.ClientDetector())
	github.Register(r, up, rp)
	return r
}

// do is a small helper that sends a request through r and returns the
// recorder. Cuts repetition across the test cases.
func do(r *gin.Engine, method, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// --- user card --------------------------------------------------------------

func TestUserCard_PNG(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(&fakeUser{p: freshProfile()}, &fakeRepo{})
	rec := do(r, http.MethodGet, "/github/octocat", map[string]string{
		"User-Agent": "facebookexternalhit/1.1", // unfurl bot → PNG
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), pngMagic) {
		t.Errorf("body does not start with PNG magic")
	}
}

func TestUserCard_SVG(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(&fakeUser{p: freshProfile()}, &fakeRepo{})
	rec := do(r, http.MethodGet, "/github/octocat?format=svg", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/svg+xml" {
		t.Errorf("Content-Type = %q, want image/svg+xml", got)
	}
	if !strings.Contains(rec.Body.String(), "<svg") {
		t.Errorf("SVG body missing <svg root\n--- body (first 200) ---\n%.200s", rec.Body.String())
	}
}

func TestUserCard_ASCII(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(&fakeUser{p: freshProfile()}, &fakeRepo{})
	rec := do(r, http.MethodGet, "/github/octocat", map[string]string{
		"User-Agent": "curl/8.4.0",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain prefix", got)
	}
	body := rec.Body.String()
	// ANSI-Shadow banner glyphs land in the ASCII surface; a banner
	// frame line + the headline letters are enough to confirm we hit
	// the banner path.
	if !strings.Contains(body, "OCTOCAT") && !strings.Contains(body, "║") && !strings.Contains(body, "│") {
		t.Errorf("ASCII body does not look like a banner\n--- body ---\n%s", body)
	}
}

func TestUserCard_HTMLOG(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(&fakeUser{p: freshProfile()}, &fakeRepo{})
	rec := do(r, http.MethodGet, "/github/octocat?format=html", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html prefix", got)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `property="og:image"`) {
		t.Errorf("HTML body missing og:image\n--- body (first 400) ---\n%.400s", body)
	}
	if !strings.Contains(body, "format=png") {
		t.Errorf("HTML body missing og:image URL with format=png\n--- body (first 400) ---\n%.400s", body)
	}
	if !strings.Contains(body, `property="og:url"`) {
		t.Errorf("HTML body missing og:url\n--- body (first 400) ---\n%.400s", body)
	}
}

func TestUserCard_404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(&fakeUser{err: profile.ErrNotFound}, &fakeRepo{})
	rec := do(r, http.MethodGet, "/github/octocat", map[string]string{
		"User-Agent": "curl/8.4.0",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=3600" {
		t.Errorf("Cache-Control = %q, want public, max-age=3600", got)
	}
	// "NOT FOUND" renders as block-letter glyphs in the ASCII banner;
	// the literal headline string is NOT present in the body. Assert on
	// the caption row (the validated target) which IS rendered as
	// literal text.
	if !strings.Contains(rec.Body.String(), "octocat") {
		t.Errorf("404 body missing target caption\n--- body ---\n%s", rec.Body.String())
	}
}

func TestUserCard_RateLimited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(&fakeUser{err: profile.ErrRateLimited}, &fakeRepo{})
	rec := do(r, http.MethodGet, "/github/octocat", map[string]string{
		"User-Agent": "curl/8.4.0",
	})
	// Locked decision: rate-limited returns 200 (not 429) so CDNs / unfurl
	// bots honour the max-age=60 header (DESIGN.md §5.3).
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (rate-limited returns 200, not 429)", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("Cache-Control = %q, want public, max-age=60", got)
	}
	// Headline "RATE LIMITED" renders as block-letter glyphs in ASCII;
	// assert on the caption row instead.
	if !strings.Contains(rec.Body.String(), "try again later") {
		t.Errorf("rate-limited body missing 'try again later' caption\n--- body ---\n%s", rec.Body.String())
	}
}

func TestUserCard_Stale(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// cached.User can return a populated Profile + a transient err
	// (ErrUnavailable). The handler renders the cached value with a
	// "( stale data )" caption row appended.
	r := newRouter(&fakeUser{p: freshProfile(), err: profile.ErrUnavailable}, &fakeRepo{})
	rec := do(r, http.MethodGet, "/github/octocat", map[string]string{
		"User-Agent": "curl/8.4.0",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (stale-but-have-data must not 5xx)", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=60" {
		t.Errorf("Cache-Control = %q, want public, max-age=60", got)
	}
	if !strings.Contains(rec.Body.String(), "stale data") {
		t.Errorf("stale body missing 'stale data' caption\n--- body ---\n%s", rec.Body.String())
	}
}

func TestUserCard_502(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// Empty Profile + ErrUnavailable = no value at all → 502.
	r := newRouter(&fakeUser{p: profile.Profile{}, err: profile.ErrUnavailable}, &fakeRepo{})
	rec := do(r, http.MethodGet, "/github/octocat", map[string]string{
		"User-Agent": "curl/8.4.0",
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

// --- repo card --------------------------------------------------------------

func TestRepoCard_PNG(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(&fakeUser{}, &fakeRepo{r: freshRepo()})
	rec := do(r, http.MethodGet, "/github/octocat/Hello-World", map[string]string{
		"User-Agent": "facebookexternalhit/1.1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if !bytes.HasPrefix(rec.Body.Bytes(), pngMagic) {
		t.Errorf("body does not start with PNG magic")
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=120" {
		t.Errorf("Cache-Control = %q, want public, max-age=120", got)
	}
}

func TestRepoCard_PylonUnsupported(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := newRouter(&fakeUser{}, &fakeRepo{r: freshRepo()})
	rec := do(r, http.MethodGet, "/github/octocat/Hello-World?format=pylon", nil)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "pylon source is not supported") {
		t.Errorf("415 body missing redirect message\n--- body ---\n%s", rec.Body.String())
	}
}

// --- routing ----------------------------------------------------------------

func TestPathRouting(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := &fakeUser{p: freshProfile()}
	rp := &fakeRepo{r: freshRepo()}
	r := newRouter(up, rp)

	// /github/foo → user route.
	rec := do(r, http.MethodGet, "/github/octocat", map[string]string{"User-Agent": "curl/8.4.0"})
	if rec.Code != http.StatusOK {
		t.Errorf("/github/octocat status = %d, want 200", rec.Code)
	}
	if up.calls == 0 {
		t.Errorf("/github/octocat did not hit UserProvider")
	}
	if rp.calls != 0 {
		t.Errorf("/github/octocat unexpectedly hit RepoProvider")
	}

	// Reset counters before the repo call.
	up.calls = 0
	rp.calls = 0

	// /github/foo/bar → repo route.
	rec = do(r, http.MethodGet, "/github/octocat/Hello-World", map[string]string{"User-Agent": "curl/8.4.0"})
	if rec.Code != http.StatusOK {
		t.Errorf("/github/octocat/Hello-World status = %d, want 200", rec.Code)
	}
	if rp.calls == 0 {
		t.Errorf("/github/octocat/Hello-World did not hit RepoProvider")
	}
	if up.calls != 0 {
		t.Errorf("/github/octocat/Hello-World unexpectedly hit UserProvider")
	}

	// /github/foo/bar/baz → no route (gin returns 404; the engine has no
	// NoRoute handler in tests so the default 404 applies).
	rec = do(r, http.MethodGet, "/github/octocat/Hello-World/extra", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("/github/octocat/Hello-World/extra status = %d, want 404", rec.Code)
	}
}

// TestPathRouting_ReverseOrder pins the gin behaviour when the 2-segment
// route is registered BEFORE the 1-segment route. DESIGN.md §5 documents
// the recommended order; this test surfaces any future gin-version
// regression that would silently break the reverse case.
func TestPathRouting_ReverseOrder(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := &fakeUser{p: freshProfile()}
	rp := &fakeRepo{r: freshRepo()}

	r := gin.New()
	r.Use(middleware.ClientDetector())
	// Manually inline the inverse registration order — we cannot reach
	// into Register() to swap the order without exposing the internals.
	h := struct {
		user profile.UserProvider
		repo profile.RepoProvider
	}{up, rp}
	r.GET("/github/:user/:repo", func(c *gin.Context) {
		// Mirror serveRepo's body-relevant calls: the test only needs to
		// observe which provider was hit.
		_, _ = h.repo.Repo(c.Request.Context(), c.Param("user"), c.Param("repo"))
		c.Status(http.StatusOK)
	})
	r.GET("/github/:user", func(c *gin.Context) {
		_, _ = h.user.User(c.Request.Context(), c.Param("user"))
		c.Status(http.StatusOK)
	})

	// Both must still resolve.
	rec := do(r, http.MethodGet, "/github/octocat", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("reverse-order /github/octocat status = %d, want 200", rec.Code)
	}
	if up.calls == 0 {
		t.Errorf("reverse-order /github/octocat did not hit UserProvider — gin did not split radix tree as expected")
	}
	rec = do(r, http.MethodGet, "/github/octocat/Hello-World", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("reverse-order /github/octocat/Hello-World status = %d, want 200", rec.Code)
	}
	if rp.calls == 0 {
		t.Errorf("reverse-order /github/octocat/Hello-World did not hit RepoProvider")
	}
}

// --- headers ----------------------------------------------------------------

// TestVaryHeader pins that every successful AND every error response
// (200 / 400 / 404 / 415 / rate-limited / stale) carries Vary: User-Agent
// so a downstream CDN does not serve one UA-class's body to another.
func TestVaryHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		up   *fakeUser
		path string
	}{
		{"ok", &fakeUser{p: freshProfile()}, "/github/octocat"},
		{"notfound", &fakeUser{err: profile.ErrNotFound}, "/github/octocat"},
		{"ratelimited", &fakeUser{err: profile.ErrRateLimited}, "/github/octocat"},
		{"stale", &fakeUser{p: freshProfile(), err: profile.ErrUnavailable}, "/github/octocat"},
		{"invalid_login_400", &fakeUser{}, "/github/foo.bar"},
		{"pylon_415", &fakeUser{}, "/github/octocat?format=pylon"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRouter(tc.up, &fakeRepo{})
			rec := do(r, http.MethodGet, tc.path, map[string]string{"User-Agent": "curl/8.4.0"})
			if got := rec.Header().Get("Vary"); got != "User-Agent" {
				t.Errorf("Vary = %q, want User-Agent (status=%d)", got, rec.Code)
			}
		})
	}
}

// TestCacheControl pins the per-route max-age values from DESIGN.md §5.3.
func TestCacheControl(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name      string
		up        *fakeUser
		rp        *fakeRepo
		path      string
		wantCache string
	}{
		{"user_ok", &fakeUser{p: freshProfile()}, &fakeRepo{}, "/github/octocat", "public, max-age=600"},
		{"repo_ok", &fakeUser{}, &fakeRepo{r: freshRepo()}, "/github/octocat/Hello-World", "public, max-age=120"},
		{"user_404", &fakeUser{err: profile.ErrNotFound}, &fakeRepo{}, "/github/octocat", "public, max-age=3600"},
		{"user_ratelimited", &fakeUser{err: profile.ErrRateLimited}, &fakeRepo{}, "/github/octocat", "public, max-age=60"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRouter(tc.up, tc.rp)
			rec := do(r, http.MethodGet, tc.path, map[string]string{"User-Agent": "curl/8.4.0"})
			if got := rec.Header().Get("Cache-Control"); got != tc.wantCache {
				t.Errorf("Cache-Control = %q, want %q (status=%d)", got, tc.wantCache, rec.Code)
			}
		})
	}
}

// --- sanitizer + helpers ----------------------------------------------------

// TestSanitizer drives the substitution table through caption rendering.
// The handler doesn't export sanitize directly; we exercise it via a
// Profile bio carrying every forbidden-char and assert the rendered
// output reflects the substitutions (and does NOT carry the originals).
func TestSanitizer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name     string
		bio      string
		mustHave []string
		mustNot  []string
	}{
		{"colon_to_semicolon", "tag:value", []string{"tag;value"}, []string{"tag:value"}},
		{"percent_to_pct", "98%", []string{"98pct"}, []string{"98%"}},
		{"comma_to_space", "a,b,c", []string{"a b c"}, []string{"a,b,c"}},
		{"up_arrow_dropped", "up▲only", []string{"up only"}, []string{"▲"}},
		{"down_arrow_dropped", "down▼only", []string{"down only"}, []string{"▼"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := freshProfile()
			p.Bio = tc.bio
			r := newRouter(&fakeUser{p: p}, &fakeRepo{})
			rec := do(r, http.MethodGet, "/github/octocat", map[string]string{"User-Agent": "curl/8.4.0"})
			body := rec.Body.String()
			for _, want := range tc.mustHave {
				if !strings.Contains(body, want) {
					t.Errorf("body missing %q\n--- body ---\n%s", want, body)
				}
			}
			for _, bad := range tc.mustNot {
				if strings.Contains(body, bad) {
					t.Errorf("body unexpectedly contains %q\n--- body ---\n%s", bad, body)
				}
			}
		})
	}
}

// --- validation -------------------------------------------------------------

func TestInvalidLogin400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := &fakeUser{}
	r := newRouter(up, &fakeRepo{})
	rec := do(r, http.MethodGet, "/github/foo.bar", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if up.calls != 0 {
		t.Errorf("UserProvider was called %d times, want 0 (validation must short-circuit before upstream)", up.calls)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// TestInvalidLogin_DotsRejected drives loginRe through a table of
// invalid logins. Captures the dots-rejected fix plus a small set of
// other charset / shape violations the regex must reject.
func TestInvalidLogin_DotsRejected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name  string
		login string
	}{
		{"dot", "foo.bar"},
		{"underscore", "foo_bar"},
		{"leading_hyphen", "-foo"},
		{"trailing_hyphen", "foo-"},
		{"double_hyphen", "fo--o"},
		{"bang", "foo!bar"},
		{"too_long", strings.Repeat("a", 40)}, // 40 > loginMaxLen=39
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := &fakeUser{}
			r := newRouter(up, &fakeRepo{})
			rec := do(r, http.MethodGet, "/github/"+tc.login, nil)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 for %q", rec.Code, tc.login)
			}
			if up.calls != 0 {
				t.Errorf("UserProvider called %d times for %q, want 0", up.calls, tc.login)
			}
		})
	}
}

// TestInvalidRepoName400 pins that an invalid repo segment (literal
// space outside repoRe's [A-Za-z0-9._-] charset) returns 400 without
// hitting the upstream.
func TestInvalidRepoName400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rp := &fakeRepo{}
	r := newRouter(&fakeUser{}, rp)
	// gin's c.Param decodes %20 to a literal space; the validator rejects.
	rec := do(r, http.MethodGet, "/github/foo/bar%20baz", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if rp.calls != 0 {
		t.Errorf("RepoProvider called %d times, want 0", rp.calls)
	}
}

// --- exhaust the assertion that PNG body for repo includes its labels --

// TestRepoCard_NoRelease pins that an empty LatestRelease drops the
// release row instead of rendering a blank `release ` line. Counted via
// the ASCII output where each caption row corresponds to one `(...)`
// directive line in the rendered banner.
func TestRepoCard_NoRelease(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rd := freshRepo()
	rd.LatestRelease = ""
	r := newRouter(&fakeUser{}, &fakeRepo{r: rd})
	rec := do(r, http.MethodGet, "/github/octocat/Hello-World", map[string]string{"User-Agent": "curl/8.4.0"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "release ") {
		t.Errorf("body unexpectedly contains a release row when LatestRelease is empty\n--- body ---\n%s", body)
	}
}

// TestHumanizePushed pins the threshold buckets via the rendered repo
// card. The handler doesn't export humanizePushed directly; the round-
// trip through repoCaptions is the contract that matters.
func TestHumanizePushed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Now()
	cases := []struct {
		name string
		when time.Time
		want string
	}{
		{"under_minute", now.Add(-30 * time.Second), "just now"},
		{"under_hour", now.Add(-30 * time.Minute), "30m ago"},
		{"under_day", now.Add(-2 * time.Hour), "2h ago"},
		{"under_month", now.Add(-5 * 24 * time.Hour), "5d ago"},
		{"under_year", now.Add(-100 * 24 * time.Hour), "3mo ago"},
		{"years", now.Add(-2 * 365 * 24 * time.Hour), "2y ago"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rd := freshRepo()
			rd.PushedAt = tc.when
			r := newRouter(&fakeUser{}, &fakeRepo{r: rd})
			rec := do(r, http.MethodGet, "/github/octocat/Hello-World", map[string]string{"User-Agent": "curl/8.4.0"})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			want := "pushed " + tc.want
			if !strings.Contains(rec.Body.String(), want) {
				t.Errorf("body missing %q (PushedAt=%v)\n--- body ---\n%s", want, tc.when, rec.Body.String())
			}
		})
	}
}

// TestSanitizerTruncates pins that captions over captionBudget runes are
// shortened with the truncate suffix.
func TestSanitizerTruncates(t *testing.T) {
	gin.SetMode(gin.TestMode)
	long := strings.Repeat("x", 100) // > captionBudget=60
	p := freshProfile()
	p.Bio = long
	r := newRouter(&fakeUser{p: p}, &fakeRepo{})
	rec := do(r, http.MethodGet, "/github/octocat?format=svg", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	// The full 100-x bio must NOT survive verbatim.
	if strings.Contains(body, strings.Repeat("x", 100)) {
		t.Errorf("body contains untruncated 100-rune bio")
	}
	// The ellipsis suffix must be present.
	if !strings.Contains(body, "…") {
		// Some renderers escape the codepoint; check the entity too.
		if !strings.Contains(body, "&#x2026;") && !strings.Contains(body, "&#8230;") {
			t.Errorf("truncated body missing ellipsis suffix\n--- body (first 400) ---\n%.400s", body)
		}
	}
}

// --- compile-time guard: error sentinels remain reachable ------------------

// TestErrorSentinels catches a future drop of the sentinels at link
// time — the test fails to compile if any sentinel disappears.
func TestErrorSentinels(t *testing.T) {
	for _, e := range []error{profile.ErrNotFound, profile.ErrRateLimited, profile.ErrUnavailable} {
		if e == nil {
			t.Errorf("nil sentinel")
		}
		if !errors.Is(e, e) {
			t.Errorf("errors.Is(%v, %v) returned false — sentinel changed shape", e, e)
		}
	}
}
