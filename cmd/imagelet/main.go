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

	"github.com/alecthomas/kong"
	"github.com/gin-gonic/gin"
	"github.com/rs/zerolog/log"

	"github.com/cmj0121/imagelet/internal/logger"
	"github.com/cmj0121/imagelet/internal/server"
)

// cli defines the top-level kong CLI flags.
type cli struct {
	Host     string `short:"H" help:"Host address to bind." default:"0.0.0.0"`
	Port     int    `short:"p" help:"TCP port to listen on." default:"8080"`
	LogLevel string `name:"log-level" short:"l" help:"Log level (trace|debug|info|warn|error|fatal|panic)." default:"info" enum:"trace,debug,info,warn,error,fatal,panic"`
}

func main() {
	var c cli
	kong.Parse(&c,
		kong.Name("imagelet"),
		kong.Description("imagelet HTTP service."),
	)

	if err := logger.Setup(c.LogLevel); err != nil {
		log.Fatal().Err(err).Msg("configure logger")
	}

	gin.SetMode(gin.ReleaseMode)
	gin.DefaultWriter = log.Logger
	gin.DefaultErrorWriter = log.Logger

	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	srv := &http.Server{
		Addr:              addr,
		Handler:           server.NewRouter(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
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
