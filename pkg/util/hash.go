package util

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
)

// ContentHash returns the SHA-256 hex digest of s.
func ContentHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// FileHash returns the SHA-256 hex digest of the file contents at path,
// reading in 32 KB chunks.
func FileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	reader := bufio.NewReader(f)
	buf := make([]byte, 32*1024)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err != nil {
			break
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}
