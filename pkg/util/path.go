package util

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandHome replaces a leading ~/ with the user's home directory.
// If the path does not start with ~/ it is returned unchanged.
func ExpandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}
