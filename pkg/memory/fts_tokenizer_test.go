package memory

import (
	"context"
	"path/filepath"
	"testing"
)

// TestFTSDiacriticFolding verifies the multilingual tokenizer (migration 013):
// an accented Portuguese memory is found by an unaccented query.
func TestFTSDiacriticFolding(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "fts.db"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	if err := store.Save(ctx, Memory{ID: "m1", Category: "fact", Content: "a sincronização usa watermark e tombstones"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Query without the diacritic; remove_diacritics=2 should still match.
	res, err := store.Search(ctx, "sincronizacao", SearchOptions{MaxResults: 5})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(res) == 0 {
		t.Fatal("unaccented query should match the accented memory (diacritic folding)")
	}
}
