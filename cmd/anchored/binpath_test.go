package main

import "testing"

// fakeBinaryPath is a deterministic stand-in for the real running
// executable's path, so tests can assert on the exact command written
// instead of whatever path `go test` happens to run from.
const fakeBinaryPath = "/opt/anchored/bin/anchored"

// withFakeBinaryPath overrides osExecutable so anchoredBinaryPath() returns
// fakeBinaryPath for the duration of the test.
func withFakeBinaryPath(t *testing.T) {
	t.Helper()
	orig := osExecutable
	osExecutable = func() (string, error) { return fakeBinaryPath, nil }
	t.Cleanup(func() { osExecutable = orig })
}
