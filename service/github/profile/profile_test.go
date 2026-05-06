package profile

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// fixtureBytes returns testdata/<name>; the fixture path is relative to
// this test file's parent (service/github/testdata/).
func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("../testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

// route is one path → handler binding for newServer.
type route struct {
	path    string
	handler http.HandlerFunc
}

// newServer returns an httptest.Server that dispatches by exact path
// match. Any unmatched path returns 500 — tests should fail loudly when
// a provider issues an unexpected request.
func newServer(t *testing.T, routes ...route) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for _, r := range routes {
		mux.HandleFunc(r.path, r.handler)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		t.Errorf("unexpected upstream request: %s %s", req.Method, req.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	})
	return httptest.NewServer(mux)
}

// newClient returns a Client pointed at srv with the given token.
func newClient(srv *httptest.Server, token string) *Client {
	return NewWithEndpoint(srv.URL, srv.Client(), token)
}

// ---------------------------------------------------------------------
// UserProvider
// ---------------------------------------------------------------------

func TestUserProvider_OK(t *testing.T) {
	body := fixtureBytes(t, "user.json")
	srv := newServer(t, route{
		path: "/users/octocat",
		handler: func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
				t.Errorf("Accept = %q, want application/vnd.github+json", got)
			}
			if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
				t.Errorf("X-GitHub-Api-Version = %q, want 2022-11-28", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		},
	})
	defer srv.Close()

	p, err := NewUserProvider(newClient(srv, "")).User(context.Background(), "octocat")
	if err != nil {
		t.Fatalf("User: %v", err)
	}
	if p.Login != "octocat" {
		t.Errorf("Login = %q, want octocat", p.Login)
	}
	if p.Name != "The Octocat" {
		t.Errorf("Name = %q, want The Octocat", p.Name)
	}
	// Bio is null in the fixture — must dereference to "" without panic.
	if p.Bio != "" {
		t.Errorf("Bio = %q, want empty (fixture has bio=null)", p.Bio)
	}
	if p.Location != "San Francisco" {
		t.Errorf("Location = %q, want San Francisco", p.Location)
	}
	if p.PublicRepos != 8 {
		t.Errorf("PublicRepos = %d, want 8", p.PublicRepos)
	}
	if p.Followers == 0 {
		t.Errorf("Followers = 0, want non-zero")
	}
	if p.JoinedAt.IsZero() {
		t.Errorf("JoinedAt is zero, want parsed from created_at")
	}
	if y := p.JoinedAt.Year(); y != 2011 {
		t.Errorf("JoinedAt.Year() = %d, want 2011", y)
	}
}

func TestUserProvider_NotFound(t *testing.T) {
	srv := newServer(t, route{
		path: "/users/ghost",
		handler: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		},
	})
	defer srv.Close()

	_, err := NewUserProvider(newClient(srv, "")).User(context.Background(), "ghost")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestUserProvider_RateLimited(t *testing.T) {
	srv := newServer(t, route{
		path: "/users/octocat",
		handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "1700000000")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"API rate limit exceeded"}`))
		},
	})
	defer srv.Close()

	_, err := NewUserProvider(newClient(srv, "")).User(context.Background(), "octocat")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
}

func TestUserProvider_429(t *testing.T) {
	srv := newServer(t, route{
		path: "/users/octocat",
		handler: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		},
	})
	defer srv.Close()

	_, err := NewUserProvider(newClient(srv, "")).User(context.Background(), "octocat")
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited (from 429)", err)
	}
}

func TestUserProvider_Unavailable(t *testing.T) {
	srv := newServer(t, route{
		path: "/users/octocat",
		handler: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		},
	})
	defer srv.Close()

	_, err := NewUserProvider(newClient(srv, "")).User(context.Background(), "octocat")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}

// ---------------------------------------------------------------------
// RepoProvider
// ---------------------------------------------------------------------

func TestRepoProvider_OK(t *testing.T) {
	repoBody := fixtureBytes(t, "repo.json")
	releaseBody := []byte(`{"tag_name":"v1.2.3"}`)
	srv := newServer(t,
		route{
			path: "/repos/octocat/Hello-World",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(repoBody)
			},
		},
		route{
			path: "/repos/octocat/Hello-World/releases/latest",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(releaseBody)
			},
		},
	)
	defer srv.Close()

	r, err := NewRepoProvider(newClient(srv, "")).Repo(context.Background(), "octocat", "Hello-World")
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	if r.FullName != "octocat/Hello-World" {
		t.Errorf("FullName = %q, want octocat/Hello-World", r.FullName)
	}
	if r.Description == "" {
		t.Errorf("Description = empty, want non-empty")
	}
	if r.Stars == 0 {
		t.Errorf("Stars = 0, want non-zero")
	}
	if r.Forks == 0 {
		t.Errorf("Forks = 0, want non-zero")
	}
	if r.DefaultBranch != "master" {
		t.Errorf("DefaultBranch = %q, want master", r.DefaultBranch)
	}
	if r.PushedAt.IsZero() {
		t.Errorf("PushedAt is zero, want parsed from pushed_at")
	}
	if r.LatestRelease != "v1.2.3" {
		t.Errorf("LatestRelease = %q, want v1.2.3", r.LatestRelease)
	}
}

func TestRepoProvider_NoRelease(t *testing.T) {
	repoBody := fixtureBytes(t, "repo.json")
	srv := newServer(t,
		route{
			path: "/repos/octocat/Hello-World",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(repoBody)
			},
		},
		route{
			path: "/repos/octocat/Hello-World/releases/latest",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
		},
	)
	defer srv.Close()

	r, err := NewRepoProvider(newClient(srv, "")).Repo(context.Background(), "octocat", "Hello-World")
	if err != nil {
		t.Fatalf("Repo: %v", err)
	}
	if r.LatestRelease != "" {
		t.Errorf("LatestRelease = %q, want empty (releases/latest 404)", r.LatestRelease)
	}
}

func TestRepoProvider_NotFound(t *testing.T) {
	srv := newServer(t, route{
		path: "/repos/octocat/no-such",
		handler: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		},
	})
	defer srv.Close()

	_, err := NewRepoProvider(newClient(srv, "")).Repo(context.Background(), "octocat", "no-such")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestRepoProvider_PrivateIsNotFound(t *testing.T) {
	// 200 with private:true must collapse to ErrNotFound regardless of
	// HTTP status — the public route MUST NOT expose private repos that
	// a configured token can see.
	srv := newServer(t, route{
		path: "/repos/owner/secret",
		handler: func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{
				"full_name": "owner/secret",
				"private": true,
				"description": "should never render",
				"stargazers_count": 1,
				"default_branch": "main",
				"pushed_at": "2024-01-01T00:00:00Z"
			}`))
		},
	})
	defer srv.Close()

	_, err := NewRepoProvider(newClient(srv, "")).Repo(context.Background(), "owner", "secret")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound (private:true must hide)", err)
	}
}

// ---------------------------------------------------------------------
// Client (auth + accept + redaction)
// ---------------------------------------------------------------------

func TestClient_AuthHeader(t *testing.T) {
	cases := []struct {
		name     string
		token    string
		wantAuth string
	}{
		{name: "authed", token: "ghp_abc", wantAuth: "Bearer ghp_abc"},
		{name: "unauthed", token: "", wantAuth: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, route{
				path: "/users/x",
				handler: func(w http.ResponseWriter, r *http.Request) {
					got := r.Header.Get("Authorization")
					if got != tc.wantAuth {
						t.Errorf("Authorization = %q, want %q", got, tc.wantAuth)
					}
					_, _ = w.Write([]byte(`{"login":"x","public_repos":0,"followers":0,"created_at":"2020-01-01T00:00:00Z"}`))
				},
			})
			defer srv.Close()

			if _, err := NewUserProvider(newClient(srv, tc.token)).User(context.Background(), "x"); err != nil {
				t.Fatalf("User: %v", err)
			}
		})
	}
}

func TestClient_AcceptHeader(t *testing.T) {
	called := false
	srv := newServer(t, route{
		path: "/users/x",
		handler: func(w http.ResponseWriter, r *http.Request) {
			called = true
			if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
				t.Errorf("Accept = %q, want application/vnd.github+json", got)
			}
			if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
				t.Errorf("X-GitHub-Api-Version = %q, want 2022-11-28", got)
			}
			if got := r.Header.Get("User-Agent"); got == "" {
				t.Errorf("User-Agent missing")
			}
			_, _ = w.Write([]byte(`{"login":"x","public_repos":0,"followers":0,"created_at":"2020-01-01T00:00:00Z"}`))
		},
	})
	defer srv.Close()

	if _, err := NewUserProvider(newClient(srv, "")).User(context.Background(), "x"); err != nil {
		t.Fatalf("User: %v", err)
	}
	if !called {
		t.Fatalf("upstream handler was never called")
	}
}

func TestRedactedHeaders_StripsAuth(t *testing.T) {
	in := http.Header{}
	in.Set("Authorization", "Bearer secret-token")
	in.Set("Cookie", "session=abc")
	in.Set("X-Trace-Id", "keep-me")

	out := redactedHeaders(in)
	if got := out.Get("Authorization"); got != "REDACTED" {
		t.Errorf("Authorization = %q, want REDACTED", got)
	}
	if got := out.Get("Cookie"); got != "REDACTED" {
		t.Errorf("Cookie = %q, want REDACTED", got)
	}
	if got := out.Get("X-Trace-Id"); got != "keep-me" {
		t.Errorf("X-Trace-Id = %q, want keep-me", got)
	}
	// The original header MUST NOT be mutated.
	if got := in.Get("Authorization"); got != "Bearer secret-token" {
		t.Errorf("input mutated: Authorization = %q, want Bearer secret-token", got)
	}
}

// TestUserProvider_TimeoutMapsToUnavailable pins that a transport-level
// failure (here: server closes without responding) collapses to
// ErrUnavailable rather than leaking an opaque transport error.
func TestUserProvider_TimeoutMapsToUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatalf("ResponseWriter is not a Hijacker")
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close() // hang up before writing a response
	}))
	defer srv.Close()

	c := NewWithEndpoint(srv.URL, &http.Client{Timeout: 200 * time.Millisecond}, "")
	_, err := NewUserProvider(c).User(context.Background(), "octocat")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
}
