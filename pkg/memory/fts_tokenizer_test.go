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

// TestBackfillNormalizedContentHashes verifies the one-time Go backfill that
// re-hashes rows stored before contentHash() started normalizing.
func TestBackfillNormalizedContentHashes(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "rehash.db"), nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	content := "We Use Postgres"
	// Simulate a pre-upgrade row: stored with a stale (non-normalized) hash.
	if err := store.Save(ctx, Memory{ID: "m1", Category: "fact", Content: content, ContentHash: "STALE_HASH"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Re-arm the backfill (the marker was set when the store first opened) and
	// re-run migrations to trigger it.
	db := store.DB()
	if _, err := db.Exec("DELETE FROM migrations WHERE name = ?", normalizedHashMarker); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}

	var got string
	if err := db.QueryRow("SELECT content_hash FROM memories WHERE id = 'm1'").Scan(&got); err != nil {
		t.Fatalf("read hash: %v", err)
	}
	if got != contentHash(content) {
		t.Fatalf("hash not normalized: got %q want %q", got, contentHash(content))
	}
}
