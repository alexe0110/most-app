// Package logging builds the application logger.
package logging

import (
	"log/slog"
	"os"
)

// New returns a JSON logger
func New(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}

	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
