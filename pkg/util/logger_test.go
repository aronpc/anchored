package util

import (
	"log/slog"
	"testing"
)

func TestDefaultLogger_ReturnsDefaultOnNil(t *testing.T) {
	got := DefaultLogger(nil)
	if got == nil {
		t.Fatal("DefaultLogger(nil) returned nil, expected slog.Default()")
	}
	// Verify it is actually the default logger.
	if got != slog.Default() {
		t.Fatal("DefaultLogger(nil) did not return slog.Default()")
	}
}

func TestDefaultLogger_ReturnsSameOnNonNil(t *testing.T) {
	logger := slog.Default()
	got := DefaultLogger(logger)
	if got != logger {
		t.Fatal("DefaultLogger(non-nil) returned a different logger than the one passed in")
	}
}
