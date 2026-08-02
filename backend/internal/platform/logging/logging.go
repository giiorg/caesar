// Package logging builds caesar's only logger: slog with a JSON handler.
// No other logging library may be imported anywhere (arch-test deny-list).
package logging

import (
	"fmt"
	"io"
	"log/slog"
)

func New(w io.Writer, level string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("logging: unknown level %q: %w", level, err)
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})), nil
}
