package util

import (
	"regexp"
	"testing"
)

func TestNewID_NonEmpty(t *testing.T) {
	id := NewID()
	if id == "" {
		t.Fatal("NewID() returned empty string")
	}
}

func TestNewID_32HexChars(t *testing.T) {
	id := NewID()
	if len(id) != 32 {
		t.Fatalf("NewID() returned %d chars, want 32", len(id))
	}
}

func TestNewID_ValidHex(t *testing.T) {
	id := NewID()
	matched, err := regexp.MatchString(`^[0-9a-f]{32}$`, id)
	if err != nil {
		t.Fatalf("regex match error: %v", err)
	}
	if !matched {
		t.Fatalf("NewID() = %q, want 32 lowercase hex characters", id)
	}
}

func TestNewID_Unique(t *testing.T) {
	ids := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		id := NewID()
		if _, exists := ids[id]; exists {
			t.Fatalf("duplicate ID generated: %q", id)
		}
		ids[id] = struct{}{}
	}
}
