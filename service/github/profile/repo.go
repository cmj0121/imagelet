package profile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// repoResponse mirrors the subset of /repos/:owner/:name fields used by
// Repo. Nullable upstream fields use pointers to distinguish "absent"
// from "empty string" / "0" — Language and License both go null on
// fresh repos with no commits / no LICENSE file.
type repoResponse struct {
	FullName      string         `json:"full_name"`
	Private       bool           `json:"private"`
	Description   *string        `json:"description"`
	Stars         int            `json:"stargazers_count"`
	Forks         int            `json:"forks_count"`
	OpenIssues    int            `json:"open_issues_count"`
	Language      *string        `json:"language"`
	License       *licenseObject `json:"license"`
	DefaultBranch string         `json:"default_branch"`
	PushedAt      string         `json:"pushed_at"`
}

// licenseObject mirrors the upstream `license` sub-object. spdx_id is
// preferred ("MIT", "Apache-2.0"); name is the human-readable fallback
// ("MIT License"). Both are nullable on the upstream; only spdx_id ==
// "NOASSERTION" carries no useful signal so we treat it as absent.
type licenseObject struct {
	SpdxID *string `json:"spdx_id"`
	Name   *string `json:"name"`
}

// releaseResponse decodes /repos/:owner/:name/releases/latest. Tag is
// the only field consumed.
type releaseResponse struct {
	TagName string `json:"tag_name"`
}

// repoPath returns the /repos/:owner/:name URL path.
func repoPath(owner, name string) string {
	return "/repos/" + owner + "/" + name
}

// releasePath returns the /repos/:owner/:name/releases/latest URL path.
func releasePath(owner, name string) string {
	return "/repos/" + owner + "/" + name + "/releases/latest"
}

// RepoProviderImpl satisfies RepoProvider against a shared Client.
type RepoProviderImpl struct {
	client *Client
}

// NewRepoProvider returns a RepoProviderImpl backed by the given Client.
func NewRepoProvider(client *Client) *RepoProviderImpl {
	return &RepoProviderImpl{client: client}
}

// Repo fetches the public repo for (owner, name). The latest-release
// fetch is best-effort — a 404 (no releases yet) returns LatestRelease
// == "" without failing the parent call. Any other error on the release
// fetch is logged in the Client's debug line and reduced to "" here so
// the repo card still renders.
//
// Private-repo guard (R6 / Fix 8): if the upstream payload has
// private == true, we return ErrNotFound regardless of HTTP status.
// A configured token may legitimately have access to private repos in
// the configurer's own org; the public /github/:user/:repo route MUST
// NOT expose that.
func (p *RepoProviderImpl) Repo(ctx context.Context, owner, name string) (Repo, error) {
	resp, err := p.client.do(ctx, repoPath(owner, name))
	if err != nil {
		return Repo{}, err
	}
	defer drainAndClose(resp)

	var raw repoResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Repo{}, ErrUnavailable
	}
	_, _ = io.Copy(io.Discard, resp.Body)

	if raw.Private {
		return Repo{}, ErrNotFound
	}
	if raw.FullName == "" {
		return Repo{}, ErrUnavailable
	}

	out := Repo{
		FullName:      raw.FullName,
		Description:   derefString(raw.Description),
		Stars:         raw.Stars,
		Forks:         raw.Forks,
		OpenIssues:    raw.OpenIssues,
		Language:      derefString(raw.Language),
		License:       pickLicense(raw.License),
		DefaultBranch: raw.DefaultBranch,
	}
	if raw.PushedAt != "" {
		if t, err := time.Parse(time.RFC3339, raw.PushedAt); err == nil {
			out.PushedAt = t
		}
	}

	// Best-effort latest-release lookup. Errors and ErrNotFound both
	// collapse to LatestRelease == "" — the repo card still renders.
	// We deliberately do NOT propagate ErrRateLimited here either: the
	// repo data is already paid for (cached upstream), so failing the
	// whole card on a release-only rate-limit would be a regression.
	if tag, err := p.fetchLatestRelease(ctx, owner, name); err == nil {
		out.LatestRelease = tag
	}

	return out, nil
}

// fetchLatestRelease returns the tag_name of the latest release, or ""
// + nil when none exists. Returns the underlying error only on transport
// / parse failure; ErrNotFound is swallowed (no releases is the common
// case). Caller should treat any returned error as "release row absent".
func (p *RepoProviderImpl) fetchLatestRelease(ctx context.Context, owner, name string) (string, error) {
	resp, err := p.client.do(ctx, releasePath(owner, name))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	defer drainAndClose(resp)

	var raw releaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return "", ErrUnavailable
	}
	_, _ = io.Copy(io.Discard, resp.Body)

	return raw.TagName, nil
}

// pickLicense returns spdx_id (preferred) or name from the upstream
// license object. "NOASSERTION" is treated as absent because it carries
// no useful render signal.
func pickLicense(l *licenseObject) string {
	if l == nil {
		return ""
	}
	if l.SpdxID != nil {
		v := *l.SpdxID
		if v != "" && v != "NOASSERTION" {
			return v
		}
	}
	if l.Name != nil {
		return *l.Name
	}
	return ""
}
