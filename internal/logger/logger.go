// Package logger configures the process-wide zerolog logger.
package logger

import (
	"fmt"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Setup parses the log level and configures the global zerolog logger.
// It writes a human-friendly console format when stdout is a TTY, JSON otherwise.
func Setup(level string) error {
	lvl, err := zerolog.ParseLevel(level)
	if err != nil {
		return fmt.Errorf("parse log level %q: %w", level, err)
	}
	zerolog.SetGlobalLevel(lvl)

	if isatty.IsTerminal(os.Stdout.Fd()) {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout})
	}
	return nil
}
