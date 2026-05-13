package util

import "log/slog"

// DefaultLogger returns the provided logger, or slog.Default() if nil.
func DefaultLogger(l *slog.Logger) *slog.Logger {
	if l == nil {
		return slog.Default()
	}
	return l
}
