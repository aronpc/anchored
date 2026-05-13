package util

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewID returns a cryptographically random 32-char hex string (16 bytes).
// It panics if the system PRNG fails.
func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}
