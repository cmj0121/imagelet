// Package github exposes the /github/:user and /github/:user/:repo
// banner-card routes. Handler shape mirrors service/qr (WantsPylonSource
// short-circuit + ResolveMode + OG meta on HTML) and service/notfound
// (banner-only error renders). Wire format per the project-wide
// negotiation contract: ASCII / SVG / PNG / HTML; pylon source is NOT
// supported on this route — banner cards have no honest pylon
// representation, so the handler returns 415 instead of falling through.
//
// Headlines bypass the caption sanitizer because loginRe / repoRe
// already constrain them to a render-safe charset. Captions flow
// through sanitize() at the point of access for every Profile / Repo
// field — one source of truth, trivially testable via TestSanitizer.
//
// Error mapping (see DESIGN.md §5.3):
//
//	nil err (user)               → 200, max-age=600
//	nil err (repo)               → 200, max-age=120
//	stale value + ErrUnavailable → 200, max-age=60, "( stale data )" caption appended
//	ErrNotFound                  → 404, max-age=3600, "NOT FOUND" banner
//	ErrRateLimited               → 200, max-age=60, "RATE LIMITED" banner
//	ErrUnavailable (no value)    → 502, no body
//	WantsPylonSource             → 415, plain-text redirect message
//	invalid login / path         → 400, plain-text "invalid login\n"
//
// Rate-limited returns 200 (not 429) so unfurl bots and CDN caches
// honour the max-age=60 header — RFC 7234 doesn't list 429 as
// cacheable by default, so a 429 would re-hit our origin on every
// upstream-rate-limited request.
package github

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/imagelet/middleware"
	"github.com/cmj0121/imagelet/render"
	"github.com/cmj0121/imagelet/service/github/profile"
)

// pylonUnsupportedMsg is the body returned for ?format=pylon /
// Accept: text/pylon on /github. Mirrors the qr handler's redirect.
const pylonUnsupportedMsg = "pylon source is not supported on /github; try ?format=png|svg|html|ascii"

// loginRe matches GitHub's user-login charset: alphanumeric segments
// joined by single hyphens, max 39 chars, no leading/trailing hyphen,
// no consecutive hyphens, no dots, no underscores. Stricter than the
// repo-name charset; matches GitHub's documented rules so a request
// guaranteed to 404 upstream is rejected at the edge — that defends
// the rate-limit budget under typo-floods.
var loginRe = regexp.MustCompile(`^[A-Za-z0-9](?:-?[A-Za-z0-9])*$`)

// loginMaxLen is GitHub's documented login length limit.
const loginMaxLen = 39

// repoRe matches a repo name segment. GitHub allows `.`, `_`, `-`, plus
// alphanumerics; max 100 chars in practice.
var repoRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

// Per-route Cache-Control max-age values (DESIGN.md §5.3 / R8).
const (
	userMaxAge      = 600  // 10 min — profile fields rarely change
	repoMaxAge      = 120  // 2 min — last-commit drives volatility
	notFoundMaxAge  = 3600 // 1 h — negative cache for typo-floods
	rateLimitMaxAge = 60   // 1 min — rate-limited and stale-served paths
)

// captionBudget caps a caption row's rendered rune count. Matches /stock.
const captionBudget = 60

// truncateSuffix is appended when a caption is shortened. U+2026
// HORIZONTAL ELLIPSIS — the Unit-1 banner-glyph spike confirmed this
// renders correctly in the banner font. If a future font swap blanks it,
// change to "..." here and every truncate caller picks it up.
const truncateSuffix = "…"

// sanitizerReplacer is the caption-text forbidden-char substitution
// table (DESIGN.md §7). Apply ONLY to caption text — headlines skip the
// table because loginRe / repoRe already constrain the headline charset
// to render-safe characters.
//
// `:` → `;` because pylon's default theme treats some colon contexts as
// directive separators in narrow rows. `%` → `pct` because pylon's
// percent-encoded glyph fallback misrenders trailing `%` in SVG. `,` →
// space, and `▲` / `▼` drop because /stock reserves those arrows for
// change signs and we don't want a bio carrying a misleading arrow.
var sanitizerReplacer = strings.NewReplacer(
	":", ";",
	"%", "pct",
	",", " ",
	"▲", " ",
	"▼", " ",
)

// sanitize applies the forbidden-char table, runs render.StripPylonSyntax
// as a belt-and-braces guard, and truncates to captionBudget runes
// (multi-byte safe — the rune count, not byte count, is the budget).
func sanitize(s string) string {
	s = sanitizerReplacer.Replace(s)
	s = render.StripPylonSyntax(s)
	if utf8.RuneCountInString(s) > captionBudget {
		s = truncateRunes(s, captionBudget-1) + truncateSuffix
	}
	return s
}

// truncateRunes returns s shortened to at most n runes. Safe for
// multi-byte UTF-8 — slices on a rune boundary.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for idx := range s {
		if i == n {
			return s[:idx]
		}
		i++
	}
	return s
}

// humanizePushed formats a "pushed" timestamp as a coarse relative
// duration. Locked thresholds (DESIGN.md §5):
//
//	< 1 minute   → "just now"
//	< 1 hour     → "Nm ago"
//	< 24 hours   → "Nh ago"
//	< 30 days    → "Nd ago"
//	< 365 days   → "Nmo ago"
//	else         → "Ny ago"
//
// "Months" and "years" use 30d / 365d divisors (no calendar arithmetic)
// because the rendered string is a vibe, not a legal document, and the
// constants keep the function pure and testable without timezone
// parameters. Zero time → "" so the caller can drop the row.
func humanizePushed(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d/(30*24*time.Hour)))
	default:
		return fmt.Sprintf("%dy ago", int(d/(365*24*time.Hour)))
	}
}

// handler holds the per-route Provider dependencies. The fields are
// the narrow profile interfaces — UserCached / RepoCached implement
// these, but tests can substitute fakes without dragging in the cache
// machinery.
type handler struct {
	user profile.UserProvider
	repo profile.RepoProvider
}

// Register mounts both routes on r. r is typed as gin.IRouter so the
// per-IP rate limit middleware in main.go can wrap it as a route group.
//
// Order: register the 1-segment route BEFORE the 2-segment route. gin's
// radix tree resolves both correctly with this order. TestPathRouting
// asserts the happy order; TestPathRouting_ReverseOrder pins what gin
// does on a swapped registration so a future regression is visible.
func Register(r gin.IRouter, up profile.UserProvider, rp profile.RepoProvider) {
	h := &handler{user: up, repo: rp}
	r.GET("/github/:user", h.serveUser)
	r.GET("/github/:user/:repo", h.serveRepo)
}

// RateLimitedHandler is the exported shim around the unexported
// renderRateLimited so middleware/iplimit can short-circuit to the same
// banner the upstream-rate-limited path emits — body, status, and
// headers identical (DESIGN.md §6 wiring).
func RateLimitedHandler(c *gin.Context) {
	renderRateLimited(c)
}

func (h *handler) serveUser(c *gin.Context) {
	c.Header("Vary", "User-Agent")
	if middleware.WantsPylonSource(c) {
		c.Data(http.StatusUnsupportedMediaType, "text/plain; charset=utf-8", []byte(pylonUnsupportedMsg))
		return
	}
	login := strings.TrimSpace(c.Param("user"))
	if len(login) > loginMaxLen || !loginRe.MatchString(login) {
		c.Header("Cache-Control", "no-store")
		c.String(http.StatusBadRequest, "invalid login\n")
		return
	}
	p, err := h.user.User(c.Request.Context(), login)
	h.renderUser(c, login, p, err)
}

func (h *handler) serveRepo(c *gin.Context) {
	c.Header("Vary", "User-Agent")
	if middleware.WantsPylonSource(c) {
		c.Data(http.StatusUnsupportedMediaType, "text/plain; charset=utf-8", []byte(pylonUnsupportedMsg))
		return
	}
	owner := strings.TrimSpace(c.Param("user"))
	name := strings.TrimSpace(c.Param("repo"))
	if len(owner) > loginMaxLen || !loginRe.MatchString(owner) || !repoRe.MatchString(name) {
		c.Header("Cache-Control", "no-store")
		c.String(http.StatusBadRequest, "invalid path\n")
		return
	}
	r, err := h.repo.Repo(c.Request.Context(), owner, name)
	h.renderRepo(c, owner+"/"+name, r, err)
}

// renderUser dispatches on err and builds the user-card banner.
func (h *handler) renderUser(c *gin.Context, login string, p profile.Profile, err error) {
	headline := strings.ToUpper(login)
	switch {
	case err == nil:
		writeBanner(c, headline, userCaptions(p), http.StatusOK, userMaxAge)
	case errors.Is(err, profile.ErrNotFound):
		renderNotFound(c, login)
	case errors.Is(err, profile.ErrRateLimited):
		renderRateLimited(c)
	default:
		// Stale-serve: cached.User can return a populated Profile + a
		// transient err (ErrUnavailable). p.Login distinguishes
		// "stale value present" from "no value at all" — the only
		// honest signal short of a third return value (DESIGN.md §5.3).
		if p.Login != "" {
			captions := append(userCaptions(p), "( stale data )")
			writeBanner(c, headline, captions, http.StatusOK, rateLimitMaxAge)
			return
		}
		log.Warn().Err(err).Str("path", c.Request.URL.Path).Msg("github upstream failed")
		c.Header("Cache-Control", "no-store")
		c.AbortWithStatus(http.StatusBadGateway)
	}
}

// renderRepo dispatches on err and builds the repo-card banner. The
// headline is the repo NAME only (without owner) so the rendered banner
// stays the same visual width as the user-card banner — viewers that
// fit-to-width otherwise shrink the wider OWNER/NAME headline and the
// glyphs read smaller. The owner is emitted as the first caption row
// so the affiliation is still visible.
func (h *handler) renderRepo(c *gin.Context, fullName string, r profile.Repo, err error) {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok {
		// Fallback for an unexpected fullName shape — keep the original
		// headline behavior so the path stays renderable.
		owner, name = "", fullName
	}
	headline := strings.ToUpper(name)
	captions := func(base []string) []string {
		if owner == "" {
			return base
		}
		return append([]string{owner}, base...)
	}
	switch {
	case err == nil:
		writeBanner(c, headline, captions(repoCaptions(r)), http.StatusOK, repoMaxAge)
	case errors.Is(err, profile.ErrNotFound):
		renderNotFound(c, fullName)
	case errors.Is(err, profile.ErrRateLimited):
		renderRateLimited(c)
	default:
		if r.FullName != "" {
			rows := append(captions(repoCaptions(r)), "( stale data )")
			writeBanner(c, headline, rows, http.StatusOK, rateLimitMaxAge)
			return
		}
		log.Warn().Err(err).Str("path", c.Request.URL.Path).Msg("github upstream failed")
		c.Header("Cache-Control", "no-store")
		c.AbortWithStatus(http.StatusBadGateway)
	}
}

// writeBanner is the per-mode dispatch shared by every successful
// response path (success, stale, 404, rate-limited). Always emits
// Cache-Control + Vary BEFORE the body so even error paths get
// cacheable headers.
func writeBanner(c *gin.Context, headline string, captions []string, status, maxAge int) {
	c.Header("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
	c.Header("Vary", "User-Agent")
	mode := middleware.ResolveMode(c)
	body, err := render.BannerMulti(headline, captions, mode)
	if err != nil {
		log.Error().Err(err).Str("headline", headline).Stringer("mode", mode).Msg("github banner render")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}
	switch mode {
	case render.ModePNG:
		c.Data(status, "image/png", body)
	case render.ModeSVG:
		c.Data(status, "image/svg+xml", body)
	case render.ModeHTML:
		og := render.OGMeta{
			Title:     "imagelet · " + headline,
			ImageURL:  middleware.AbsoluteURL(c, "format=png"),
			ImageType: "image/png",
			PageURL:   middleware.AbsoluteURL(c, ""),
		}
		c.Data(status, "text/html; charset=utf-8", render.InjectOGMeta(body, og))
	default:
		c.Data(status, "text/plain; charset=utf-8", body)
	}
}

// renderNotFound emits a 404 banner with target as the single caption.
// target is either the login (`octocat`) or the full-name
// (`octocat/Hello-World`), already validated by loginRe / repoRe so
// it's safe to pass through without sanitisation.
func renderNotFound(c *gin.Context, target string) {
	writeBanner(c, "NOT FOUND", []string{target}, http.StatusNotFound, notFoundMaxAge)
}

// renderRateLimited emits the deterministic rate-limited banner.
// Status is 200 (not 429) so CDNs and unfurl-bot caches honour the
// max-age=60 header (DESIGN.md §5.3).
func renderRateLimited(c *gin.Context) {
	writeBanner(c, "RATE LIMITED", []string{"try again later"}, http.StatusOK, rateLimitMaxAge)
}

// userCaptions assembles the per-row caption slice for a Profile.
// Empty fields drop their row (instead of rendering as blank). Every
// free-text field passes through sanitize() at the point of access —
// one source of truth and trivially testable via TestSanitizer.
func userCaptions(p profile.Profile) []string {
	var out []string
	if s := sanitize(p.Name); s != "" {
		out = append(out, s)
	}
	if s := sanitize(p.Bio); s != "" {
		out = append(out, s)
	}
	if p.IsOrganization {
		out = append(out, "Organization")
	}
	// Stats row uses safe glyphs (★, ⌥ confirmed by the banner-font
	// pre-flight) for the existing fields and `·` for separators (already
	// in the font — used by /index's "repo · version" caption).
	out = append(out, fmt.Sprintf("★ %d followers · ⌥ %d repos · %d following · %d gists",
		p.Followers, p.PublicRepos, p.Following, p.PublicGists))
	// Company values from GitHub frequently start with `@` (the form
	// people type when linking to an org account). pylon parses a
	// caption that starts with `@` as a directive, so the literal value
	// "@github" renders as a pylon error. Strip the leading `@` before
	// rendering — the surrounding rows (org badge, blog URL) carry the
	// affiliation context anyway.
	if c := strings.TrimPrefix(strings.TrimSpace(p.Company), "@"); c != "" {
		if s := sanitize(c); s != "" {
			out = append(out, s)
		}
	}
	if s := sanitizeBlog(p.Blog); s != "" {
		out = append(out, s)
	}
	// Twitter rebranded to X; render the handle as a bare x.com URL so
	// it visually distinguishes from a free-text company row and avoids
	// pylon's leading-`@` directive parse.
	if t := strings.TrimPrefix(strings.TrimSpace(p.TwitterUsername), "@"); t != "" {
		if s := sanitize(t); s != "" {
			out = append(out, "x.com/"+s)
		}
	}
	// Location and joined-since collapse into one row with `·` between
	// them so a card with both populated reads as a single line of
	// metadata instead of two near-empty rows.
	loc := sanitize(p.Location)
	var joined string
	if !p.JoinedAt.IsZero() {
		joined = "joined " + p.JoinedAt.Format("Jan 2006")
	}
	switch {
	case loc != "" && joined != "":
		out = append(out, loc+" · "+joined)
	case loc != "":
		out = append(out, loc)
	case joined != "":
		out = append(out, joined)
	}
	return out
}

// sanitizeBlog strips http(s):// scheme from a homepage URL so the
// banner-font `:`→`;` substitution (which would render as "https;//...")
// doesn't disfigure the row. The bare host+path passes through sanitize
// for the remaining substitutions and the rune-budget truncation.
func sanitizeBlog(blog string) string {
	s := strings.TrimSpace(blog)
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	return sanitize(s)
}

// repoCaptions assembles the per-row caption slice for a Repo. Same
// per-field sanitize discipline as userCaptions; the language·license·
// branch row is composed from individually-sanitized parts before
// joining (the "·" separator is U+00B7 and is not in the sanitizer
// table, so the join after sanitisation is safe).
func repoCaptions(r profile.Repo) []string {
	var out []string
	if s := sanitize(r.Description); s != "" {
		out = append(out, s)
	}
	out = append(out, fmt.Sprintf("★ %d  ⎇ %d  ⚠ %d", r.Stars, r.Forks, r.OpenIssues))
	parts := []string{}
	if s := sanitize(r.Language); s != "" {
		parts = append(parts, s)
	}
	if s := sanitize(r.License); s != "" {
		parts = append(parts, s)
	}
	if s := sanitize(r.DefaultBranch); s != "" {
		parts = append(parts, s)
	}
	if len(parts) > 0 {
		out = append(out, strings.Join(parts, " · "))
	}
	if s := humanizePushed(r.PushedAt); s != "" {
		out = append(out, "pushed "+s)
	}
	if s := sanitize(r.LatestRelease); s != "" {
		out = append(out, "release "+s)
	}
	return out
}
