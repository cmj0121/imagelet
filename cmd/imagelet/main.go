// Command imagelet runs the imagelet HTTP service.
package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
	// Embeds Go's zoneinfo database into the binary so time.LoadLocation
	// works on minimal runtimes (distroless, scratch, alpine variants
	// without tzdata). Adds ~450 KB; required by middleware.TimezoneDetector.
	_ "time/tzdata"

	"github.com/alecthomas/kong"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/imagelet/internal/htmlcache"
	"github.com/cmj0121/imagelet/internal/i18n"
	"github.com/cmj0121/imagelet/internal/safehttp"
	"github.com/cmj0121/imagelet/logger"
	"github.com/cmj0121/imagelet/middleware/iplimit"
	"github.com/cmj0121/imagelet/server"
	"github.com/cmj0121/imagelet/service/dns"
	dnsresolver "github.com/cmj0121/imagelet/service/dns/resolver"
	dnscached "github.com/cmj0121/imagelet/service/dns/resolver/cached"
	"github.com/cmj0121/imagelet/service/favicon"
	"github.com/cmj0121/imagelet/service/github"
	githubprofile "github.com/cmj0121/imagelet/service/github/profile"
	githubcached "github.com/cmj0121/imagelet/service/github/profile/cached"
	"github.com/cmj0121/imagelet/service/index"
	"github.com/cmj0121/imagelet/service/notfound"
	"github.com/cmj0121/imagelet/service/now"
	"github.com/cmj0121/imagelet/service/qr"
	"github.com/cmj0121/imagelet/service/stock"
	"github.com/cmj0121/imagelet/service/sysinfo"
	"github.com/cmj0121/imagelet/service/stock/quote/cached"
	"github.com/cmj0121/imagelet/service/stock/quote/yahoo"
	"github.com/cmj0121/imagelet/service/stock/twse"
)

// version is stamped at link time via -ldflags="-X main.version=…".
// The Makefile defaults it to "dev"; the Dockerfile defaults to
// "docker"; the release.yml workflow passes the docker/metadata-action
// resolved tag (e.g. "0.1.0" or "main-1234abc"). Reported in GET /'s
// rendered body.
var version = "dev"

// cli defines the top-level kong CLI flags.
type cli struct {
	Host     string `short:"H" help:"Host address to bind." default:"0.0.0.0"`
	Port     int    `short:"p" help:"TCP port to listen on." default:"8080"`
	Verbose  int    `short:"v" type:"counter" help:"Increase log verbosity (-v for debug, -vv for trace)."`
	CacheDir string `name:"cache-dir" help:"Persist per-stock + TAIFEX cache snapshots to this directory; restored on startup, saved on graceful shutdown. Empty (default) disables disk persistence."`
	Sysinfo  bool   `name:"sysinfo" help:"Enable GET /sysinfo (hostname, OS, kernel, CPU, RAM, uptime, load). Disabled by default — /sysinfo exposes local infrastructure details."`
}

func main() {
	var c cli
	kong.Parse(&c,
		kong.Name("imagelet"),
		kong.Description("imagelet HTTP service."),
	)

	if c.CacheDir != "" {
		abs, err := filepath.Abs(c.CacheDir)
		if err != nil {
			log.Fatal().Err(err).Str("cache-dir", c.CacheDir).Msg("resolve cache-dir failed")
		}
		c.CacheDir = abs
	}

	level := "info"
	switch {
	case c.Verbose >= 2:
		level = "trace"
	case c.Verbose == 1:
		level = "debug"
	}
	if err := logger.Setup(level); err != nil {
		log.Fatal().Err(err).Msg("configure logger")
	}

	// Resolved zone is logged so /now's wall-clock format is unambiguous.
	// In containers without /etc/localtime this prints "UTC".
	log.Info().Str("tz", time.Local.String()).Msg("server timezone")

	gin.SetMode(gin.ReleaseMode)
	gin.DefaultWriter = log.Logger
	gin.DefaultErrorWriter = log.Logger

	r := server.New()
	// In-process HTML response cache: a middleware that respects the
	// handlers' own Cache-Control: max-age headers. Routes that emit
	// no-store (/now, /404) opt out automatically; / and /stock
	// participate via their existing Cache-Control values. Mounted
	// before the route handlers so it sees every request.
	//
	// Capacity 1024 (vs the package-default 256) absorbs the ~4×
	// fan-out that locale-aware caching introduces — the cache key
	// now varies by resolved Locale (via WithLocale), and three
	// supported locales × the existing per-symbol working set leaves
	// headroom for hot routes without LRU thrash.
	r.Use(htmlcache.NewWithCapacity(1024).WithLocale(i18n.LocaleString).Middleware())
	favicon.Register(r)
	index.Register(r, version)
	now.Register(r)
	qr.Register(r)
	var sysinfoHandler *sysinfo.Handler
	if c.Sysinfo {
		sysinfoHandler = sysinfo.Register(r, sysinfo.RealCollector{})
	}

	// Build the cached Yahoo provider once so the in-memory cache (and its
	// singleflight stampede control) is shared across all /stock requests;
	// constructing per-request would defeat the cache.
	quoteProvider := cached.New(yahoo.New())
	// TW-only enrichment provider: 三大法人 + market margin balance from
	// TWSE legacy openapi. Cached with 4h success / 30m failure TTL since
	// TWSE publishes daily (~16:00 Asia/Taipei). NewCached auto-attaches
	// per-(stockID, date) walk-back caches over T86 / TWT93U / MI_MARGN
	// and date-keyed walk-back caches over the TAIFEX endpoints.
	twseProvider := twse.NewCached(twse.New())
	if c.CacheDir != "" {
		twseProvider.LoadSnapshots(c.CacheDir)
		log.Info().Str("dir", c.CacheDir).Msg("ttlcache: snapshots loaded")
	}
	stock.Register(r, quoteProvider, twseProvider)

	// Lifecycle ctx — created BEFORE the github wiring so the cached
	// providers' LogHourly goroutines bind to a context that drains on
	// SIGINT/SIGTERM (no goroutine leak). Nothing between stock.Register
	// above and the cancellation block below reads ctx, so the move-up
	// is benign.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Bind the sysinfo background refresher to the shutdown context so the
	// goroutine drains on SIGINT/SIGTERM (mirrors userCache.LogHourly(ctx)).
	if sysinfoHandler != nil {
		sysinfoHandler.Start(ctx)
	}

	// /github routes — shared upstream Client + per-route Providers,
	// wrapped by per-IP rate limit middleware (R10). The rate-limited
	// branch shares the same banner shape as the upstream-rate-limited
	// path so /github callers see one consistent throttle UX.
	githubClient := githubprofile.New(safehttp.NewClient(5*time.Second), os.Getenv("GITHUB_TOKEN"))
	if githubClient.IsAuthed() {
		log.Info().Msg("github: token mode authed (5000 req/hr)")
	} else {
		log.Info().Msg("github: token mode unauthed (60 req/hr)")
	}
	userCache := githubcached.NewUser(githubprofile.NewUserProvider(githubClient))
	repoCache := githubcached.NewRepo(githubprofile.NewRepoProvider(githubClient))
	go userCache.LogHourly(ctx)
	go repoCache.LogHourly(ctx)
	// Empty group prefix: github.Register adds the /github/... segment itself,
	// so prefixing the group would produce /github/github/.... The middleware
	// only attaches to the routes registered on this group, so per-IP rate
	// limiting still applies exclusively to /github/* — no other routes are
	// throttled.
	githubGroup := r.Group("", iplimit.New30PerMin().Middleware(github.RateLimitedHandler))
	github.Register(githubGroup, userCache, repoCache)

	// /dns routes — process-shared resolver with DoT default + comma-list
	// fallback (R2), wrapped in a TTL+singleflight cache (R6 clamped),
	// per-IP rate-limited (R10) on its OWN iplimit instance (NOT shared
	// with /github — bucket budgets are per-route; sharing would let a
	// caller's /github traffic exhaust their /dns budget and vice versa).
	// Empty group prefix mirrors /github: dns.Register adds /dns/... itself.
	dnsClient, err := dnsresolver.New(os.Getenv("DNS_RESOLVER"))
	if err != nil {
		log.Fatal().Err(err).Msg("dns: parse DNS_RESOLVER")
	}
	log.Info().Strs("resolvers", dnsClient.Resolvers()).Msg("dns: resolver configured (default DoT, port 853)")
	dnsCache := dnscached.New(dnsClient)
	go dnsCache.LogHourly(ctx)
	dnsGroup := r.Group("", iplimit.New30PerMin().Middleware(dns.RateLimitedHandler))
	dns.Register(dnsGroup, dnsCache)

	// NoRoute fallback — must be installed last so every other route had
	// a chance to claim its path first.
	notfound.Register(r)

	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 14, // 16 KiB — caps client header floods.
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info().Str("addr", addr).Msg("server listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		if err != nil {
			log.Fatal().Err(err).Msg("server terminated")
		}
		return
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatal().Err(err).Msg("graceful shutdown failed")
	}
	if err := <-errCh; err != nil {
		log.Fatal().Err(err).Msg("server terminated")
	}

	if c.CacheDir != "" {
		twseProvider.SaveSnapshots(c.CacheDir)
		log.Info().Str("dir", c.CacheDir).Msg("ttlcache: snapshots saved")
	}

	log.Info().Msg("server stopped")
}
