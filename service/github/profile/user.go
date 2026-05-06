package profile

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

// userResponse mirrors the subset of /users/:login fields used by Profile.
// Pointer-typed scalars accept JSON null cleanly (e.g. "name": null,
// "bio": null) without forcing the parser into a string-or-null union.
type userResponse struct {
	Login       string  `json:"login"`
	Name        *string `json:"name"`
	Bio         *string `json:"bio"`
	Location    *string `json:"location"`
	PublicRepos int     `json:"public_repos"`
	Followers   int     `json:"followers"`
	CreatedAt   string  `json:"created_at"`
}

// userPath returns the /users/:login URL path.
func userPath(login string) string {
	return "/users/" + login
}

// UserProviderImpl satisfies UserProvider against a shared Client.
type UserProviderImpl struct {
	client *Client
}

// NewUserProvider returns a UserProviderImpl backed by the given Client.
func NewUserProvider(client *Client) *UserProviderImpl {
	return &UserProviderImpl{client: client}
}

// User fetches the public user/org profile for login. Errors are reduced
// to ErrNotFound / ErrRateLimited / ErrUnavailable; transport and parse
// failures collapse to ErrUnavailable.
func (p *UserProviderImpl) User(ctx context.Context, login string) (Profile, error) {
	resp, err := p.client.do(ctx, userPath(login))
	if err != nil {
		return Profile{}, err
	}
	defer drainAndClose(resp)

	var raw userResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return Profile{}, ErrUnavailable
	}
	// Drain trailing bytes so net/http can return the connection to the
	// keep-alive pool — json.Decoder stops at the closing `}`.
	_, _ = io.Copy(io.Discard, resp.Body)

	if raw.Login == "" {
		// 200 with a body that doesn't even carry a login is honest
		// upstream weirdness; surface as ErrUnavailable so the cached
		// layer applies the short failureTTL rather than the long
		// notFoundTTL.
		return Profile{}, ErrUnavailable
	}

	out := Profile{
		Login:       raw.Login,
		Name:        derefString(raw.Name),
		Bio:         derefString(raw.Bio),
		Location:    derefString(raw.Location),
		PublicRepos: raw.PublicRepos,
		Followers:   raw.Followers,
	}
	if raw.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, raw.CreatedAt); err == nil {
			out.JoinedAt = t
		}
	}
	return out, nil
}

// derefString returns the dereferenced pointer or "" when nil.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
