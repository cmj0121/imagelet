// Command imagelet runs the imagelet HTTP service.
package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
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

	"github.com/cmj0121/imagelet/logger"
	"github.com/cmj0121/imagelet/server"
	"github.com/cmj0121/imagelet/service/notfound"
	"github.com/cmj0121/imagelet/service/now"
	"github.com/cmj0121/imagelet/service/stock"
	"github.com/cmj0121/imagelet/service/stock/quote/cached"
	"github.com/cmj0121/imagelet/service/stock/quote/yahoo"
)

// cli defines the top-level kong CLI flags.
type cli struct {
	Host    string `short:"H" help:"Host address to bind." default:"0.0.0.0"`
	Port    int    `short:"p" help:"TCP port to listen on." default:"8080"`
	Verbose int    `short:"v" type:"counter" help:"Increase log verbosity (-v for debug, -vv for trace)."`
}

func main() {
	var c cli
	kong.Parse(&c,
		kong.Name("imagelet"),
		kong.Description("imagelet HTTP service."),
	)

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
	now.Register(r)

	// Build the cached Yahoo provider once so the in-memory cache (and its
	// singleflight stampede control) is shared across all /stock requests;
	// constructing per-request would defeat the cache.
	quoteProvider := cached.New(yahoo.New())
	stock.Register(r, quoteProvider)

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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	log.Info().Msg("server stopped")
}
