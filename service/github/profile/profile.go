// Package profile provides public-GitHub user / repo retrieval for the
// /github service.
//
// The package follows the same shape as service/stock/quote: a value type
// (Profile, Repo), narrow Provider interfaces (UserProvider, RepoProvider),
// a shared transport+auth Client (client.go), and concrete provider
// implementations (user.go, repo.go). A TTL+singleflight cache wrapper
// lives in the cached/ subpackage.
//
// Errors are reduced to three sentinel values — ErrNotFound, ErrRateLimited,
// ErrUnavailable — so the handler can switch on them deterministically and
// the cached layer can apply per-state TTLs.
package profile

import (
	"context"
	"errors"
	"time"
)

// Profile is the value type rendered by GET /github/:user. Empty fields
// (Bio == "", Location == "", JoinedAt.IsZero()) are gracefully omitted
// by the renderer; absent is not an error.
type Profile struct {
	Login       string    // canonical login from upstream (`octocat`)
	Name        string    // display name; "" when upstream returns null
	Bio         string    // 60-char-budget caption candidate; "" allowed
	Location    string    // free-form; sanitized before render
	PublicRepos int       // public_repos
	Followers   int       // followers
	JoinedAt    time.Time // created_at
}

// Repo is the value type rendered by GET /github/:user/:repo. License
// and LatestRelease are best-effort; "" when upstream omits them.
type Repo struct {
	FullName      string    // "owner/name"
	Description   string    // 60-char-budget caption candidate
	Stars         int       // stargazers_count
	Forks         int       // forks_count
	OpenIssues    int       // open_issues_count
	Language      string    // primary language; "" allowed
	License       string    // license.spdx_id (preferred) || license.name; "" allowed
	DefaultBranch string    // default_branch
	PushedAt      time.Time // pushed_at — represents "last activity"; renderer formats as relative
	LatestRelease string    // tag_name from /releases/latest, "" when 404 or fetch failed
}

// UserProvider fetches a public user/org Profile by login.
type UserProvider interface {
	User(ctx context.Context, login string) (Profile, error)
}

// RepoProvider fetches a public repo Repo by (owner, name).
type RepoProvider interface {
	Repo(ctx context.Context, owner, name string) (Repo, error)
}

// Error sentinels. Mirror service/stock/quote.ErrUnavailable's role:
// upstream answered honestly, no transport / decoding noise.
var (
	// ErrNotFound — upstream returned 404, OR the repo response had
	// private:true (we treat private repos as not-found on the public
	// route; see client.go / repo.go). Cacheable for 1h (R6).
	ErrNotFound = errors.New("github: not found")
	// ErrRateLimited — upstream returned 403 with X-RateLimit-Remaining: 0
	// (or HTTP 429). Caller renders the deterministic rate-limited
	// banner (R1).
	ErrRateLimited = errors.New("github: rate limited")
	// ErrUnavailable — any other non-2xx, transport error, or parse failure.
	// Mapped to 502 by the handler when no stale value is available.
	ErrUnavailable = errors.New("github: upstream unavailable")
)
