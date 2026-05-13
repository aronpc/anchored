package util

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContentHash_Consistent(t *testing.T) {
	input := "hello anchored"
	h1 := ContentHash(input)
	h2 := ContentHash(input)
	if h1 != h2 {
		t.Fatalf("ContentHash(%q) returned different values: %s vs %s", input, h1, h2)
	}
}

func TestContentHash_DifferentInput(t *testing.T) {
	h1 := ContentHash("hello")
	h2 := ContentHash("world")
	if h1 == h2 {
		t.Fatal("ContentHash returned same hash for different inputs")
	}
}

func TestContentHash_64HexChars(t *testing.T) {
	h := ContentHash("test input")
	if len(h) != 64 {
		t.Fatalf("ContentHash returned %d chars, want 64 (SHA-256)", len(h))
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("ContentHash returned non-hex char: %c", c)
		}
	}
}

func TestFileHash_HashesFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "testfile")
	content := []byte("file content for hashing")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := FileHash(path)
	if err != nil {
		t.Fatalf("FileHash(%q): %v", path, err)
	}

	// Verify it matches ContentHash of the same bytes.
	want := ContentHash(string(content))
	if got != want {
		t.Fatalf("FileHash = %q, want %q (matching ContentHash)", got, want)
	}

	if len(got) != 64 {
		t.Fatalf("FileHash returned %d chars, want 64", len(got))
	}
}

func TestFileHash_NonExistentFile(t *testing.T) {
	_, err := FileHash("/tmp/anchored_nonexistent_file_test_12345")
	if err == nil {
		t.Fatal("FileHash should return error for non-existent file")
	}
}
