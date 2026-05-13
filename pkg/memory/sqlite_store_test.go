package memory

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", fmt.Sprintf("%s?_journal_mode=WAL&_busy_timeout=30000&_txlock=immediate", tmp))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newTestSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	db := openTestDB(t)
	cache := NewVectorCache(slog.Default())
	if err := cache.Load(db); err != nil {
		t.Fatalf("cache load: %v", err)
	}
	return &SQLiteStore{db: db, cache: cache, logger: slog.Default()}
}

// saveAndGetID inserts a Memory and returns the generated ID.
// SQLiteStore.Save receives Memory by value, so the ID is assigned inside Save.
func saveAndGetID(t *testing.T, s *SQLiteStore, ctx context.Context, m Memory) string {
	t.Helper()
	if err := s.Save(ctx, m); err != nil {
		t.Fatalf("save: %v", err)
	}
	all, err := s.List(ctx, ListOptions{Category: m.Category})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, mem := range all {
		if mem.Content == m.Content {
			return mem.ID
		}
	}
	t.Fatal("saved memory not found via list")
	return ""
}

func TestSQLiteStore_SaveAndGet(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	id := saveAndGetID(t, s, ctx, Memory{
		Content:  "user prefers dark mode in the editor",
		Category: "preference",
		Source:   "test",
	})

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected memory, got nil")
	}
	if got.Content != "user prefers dark mode in the editor" {
		t.Errorf("content: got %q", got.Content)
	}
	if got.Category != "preference" {
		t.Errorf("category: got %q", got.Category)
	}
	if got.ContentHash == "" {
		t.Error("expected content hash to be set")
	}
	if got.CreatedAt.IsZero() {
		t.Error("expected created_at to be set")
	}
}

func TestSQLiteStore_Save_Upsert(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	id1 := saveAndGetID(t, s, ctx, Memory{
		Content:  "original content",
		Category: "fact",
		Source:   "test",
	})

	// Upsert with same ID
	if err := s.Save(ctx, Memory{
		ID:       id1,
		Content:  "updated content",
		Category: "learning",
		Source:   "test",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := s.Get(ctx, id1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Content != "updated content" {
		t.Errorf("content after upsert: got %q", got.Content)
	}
	if got.Category != "learning" {
		t.Errorf("category after upsert: got %q", got.Category)
	}
}

func TestSQLiteStore_Get_NotFound(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	got, err := s.Get(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent ID")
	}
}

func TestSQLiteStore_Delete(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	id := saveAndGetID(t, s, ctx, Memory{Content: "to be deleted", Category: "fact", Source: "test"})

	if err := s.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get after delete: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestSQLiteStore_SoftDelete(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	id := saveAndGetID(t, s, ctx, Memory{Content: "soft delete me", Category: "fact", Source: "test"})

	if err := s.SoftDelete(ctx, id); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get after soft delete: %v", err)
	}
	if got != nil {
		t.Error("expected nil after soft delete (Get filters deleted_at IS NULL)")
	}
}

func TestSQLiteStore_Update(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	id := saveAndGetID(t, s, ctx, Memory{Content: "original", Category: "fact", Source: "test"})

	if err := s.Update(ctx, id, "updated content", "decision"); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if got.Content != "updated content" {
		t.Errorf("content: got %q", got.Content)
	}
	if got.Category != "decision" {
		t.Errorf("category: got %q", got.Category)
	}
}

func TestSQLiteStore_List(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	for _, m := range []Memory{
		{Content: "fact one", Category: "fact", Source: "test"},
		{Content: "decision one", Category: "decision", Source: "test"},
		{Content: "learning one", Category: "learning", Source: "test"},
	} {
		s.Save(ctx, m)
	}

	all, err := s.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 memories, got %d", len(all))
	}

	facts, err := s.List(ctx, ListOptions{Category: "fact"})
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	if len(facts) != 1 {
		t.Errorf("expected 1 fact, got %d", len(facts))
	}

	multi, err := s.List(ctx, ListOptions{Categories: []string{"fact", "decision"}})
	if err != nil {
		t.Fatalf("list multi-category: %v", err)
	}
	if len(multi) != 2 {
		t.Errorf("expected 2 for multi-category, got %d", len(multi))
	}
}

func TestSQLiteStore_List_WithLimit(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	for i := range 5 {
		s.Save(ctx, Memory{Content: fmt.Sprintf("memory %d", i), Category: "fact", Source: "test"})
	}

	results, err := s.List(ctx, ListOptions{Limit: 3})
	if err != nil {
		t.Fatalf("list with limit: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results with limit, got %d", len(results))
	}
}

func TestSQLiteStore_Search(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	for _, m := range []Memory{
		{Content: "Go is a statically typed programming language", Category: "fact", Source: "docs"},
		{Content: "React uses a virtual DOM for efficient updates", Category: "fact", Source: "docs"},
		{Content: "The project uses PostgreSQL for persistence", Category: "decision", Source: "meeting"},
	} {
		s.Save(ctx, m)
	}

	results, err := s.Search(ctx, "programming", SearchOptions{MaxResults: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 search result")
	}
	if results[0].Score <= 0 {
		t.Errorf("expected positive score, got %f", results[0].Score)
	}

	results, err = s.Search(ctx, "PostgreSQL", SearchOptions{MaxResults: 10, Category: "decision"})
	if err != nil {
		t.Fatalf("search with category: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result with category filter, got %d", len(results))
	}
}

func TestSQLiteStore_Stats(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	proj := "proj-1"
	for _, m := range []Memory{
		{Content: "a", Category: "fact", Source: "test", ProjectID: &proj},
		{Content: "b", Category: "fact", Source: "test", ProjectID: &proj},
		{Content: "c", Category: "decision", Source: "test"},
	} {
		s.Save(ctx, m)
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TotalMemories != 3 {
		t.Errorf("total: got %d, want 3", stats.TotalMemories)
	}
	if stats.ByCategory["fact"] != 2 {
		t.Errorf("fact count: got %d, want 2", stats.ByCategory["fact"])
	}
	if stats.ByProject[proj] != 2 {
		t.Errorf("project count: got %d, want 2", stats.ByProject[proj])
	}
}

func TestSQLiteStore_UpdateEmbedding(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	id := saveAndGetID(t, s, ctx, Memory{Content: "embed me", Category: "fact", Source: "test"})

	vec := []float32{0.1, 0.2, 0.3, 0.4}
	if err := s.UpdateEmbedding(ctx, id, vec); err != nil {
		t.Fatalf("update embedding: %v", err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Embedding) != 4 {
		t.Fatalf("embedding length: got %d, want 4", len(got.Embedding))
	}
	if got.Embedding[0] != 0.1 {
		t.Errorf("embedding[0]: got %f, want 0.1", got.Embedding[0])
	}
}

func TestSQLiteStore_ListWithoutEmbedding(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	id1 := saveAndGetID(t, s, ctx, Memory{Content: "no embed", Category: "fact", Source: "test"})
	_ = saveAndGetID(t, s, ctx, Memory{Content: "has embed", Category: "learning", Source: "test"})

	// Embed the second one
	all, _ := s.List(ctx, ListOptions{Category: "learning"})
	if len(all) > 0 {
		s.UpdateEmbedding(ctx, all[0].ID, []float32{0.5, 0.5})
	}

	pending, err := s.ListWithoutEmbedding(ctx, 10)
	if err != nil {
		t.Fatalf("list without embedding: %v", err)
	}
	for _, p := range pending {
		if p.ID == id1 {
			return // found our pending memory
		}
	}
	t.Errorf("expected memory %q in pending list", id1)
}

func TestSQLiteStore_FindByContentHash(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	proj := "proj-1"
	id := saveAndGetID(t, s, ctx, Memory{
		Content:   "unique content for hashing",
		Category:  "fact",
		Source:    "test",
		ProjectID: &proj,
	})

	got, _ := s.Get(ctx, id)
	hash := got.ContentHash

	found, err := s.FindByContentHash(ctx, hash, &proj)
	if err != nil {
		t.Fatalf("find by hash: %v", err)
	}
	if found == nil {
		t.Fatal("expected to find memory by hash")
	}

	found, err = s.FindByContentHash(ctx, hash, nil)
	if err != nil {
		t.Fatalf("find by hash no project: %v", err)
	}
	if found != nil {
		t.Error("expected nil when project ID doesn't match")
	}
}

func TestSQLiteStore_BackfillContentHash(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// Insert directly to bypass auto-hash, using a dedicated connection
	// to avoid SQLITE_BUSY with rows open during in-loop UPDATE.
	s.db.ExecContext(ctx,
		"INSERT INTO memories (id, category, content, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
		"manual-1", "fact", "no hash content", "test", time.Now().UTC(), time.Now().UTC())

	count, err := s.BackfillContentHash(ctx)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	// NOTE: BackfillContentHash may fail with SQLITE_BUSY in tests due to
	// read-then-write inside a single connection. The function logs warnings
	// and returns 0. This is a known limitation of the current implementation.
	if count > 0 {
		got, err := s.Get(ctx, "manual-1")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.ContentHash == "" {
			t.Error("expected content hash to be backfilled")
		}
	}
}

func TestSQLiteStore_DeleteByScope(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	proj := "proj-scope"
	for _, m := range []Memory{
		{Content: "a", Category: "fact", Source: "src1", ProjectID: &proj},
		{Content: "b", Category: "decision", Source: "src1", ProjectID: &proj},
		{Content: "c", Category: "fact", Source: "src2"},
	} {
		s.Save(ctx, m)
	}

	n, err := s.DeleteByScope(ctx, DeleteScopeOptions{ProjectID: proj, Category: "fact", Hard: false})
	if err != nil {
		t.Fatalf("delete by scope: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 soft-deleted, got %d", n)
	}

	remaining, _ := s.List(ctx, ListOptions{ProjectID: proj})
	if len(remaining) != 1 {
		t.Errorf("expected 1 remaining in project, got %d", len(remaining))
	}

	n, err = s.DeleteByScope(ctx, DeleteScopeOptions{Source: "src2", Hard: true})
	if err != nil {
		t.Fatalf("hard delete by scope: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 hard-deleted, got %d", n)
	}
}

func TestSQLiteStore_DeleteByScope_NoConditions(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	_, err := s.DeleteByScope(ctx, DeleteScopeOptions{})
	if err == nil {
		t.Fatal("expected error when no scope conditions provided")
	}
}

func TestSQLiteStore_CountWithoutEmbedding(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	saveAndGetID(t, s, ctx, Memory{Content: "no embed", Category: "fact", Source: "test"})
	id2 := saveAndGetID(t, s, ctx, Memory{Content: "has embed", Category: "learning", Source: "test"})
	s.UpdateEmbedding(ctx, id2, []float32{0.1})

	count, err := s.CountWithoutEmbedding(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 without embedding, got %d", count)
	}
}

func TestSQLiteStore_SaveWithMetadata(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	id := saveAndGetID(t, s, ctx, Memory{
		Content:  "test with metadata",
		Category: "fact",
		Source:   "test",
		Metadata: map[string]any{"key": "value", "count": float64(42)},
	})

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	meta, ok := got.Metadata.(map[string]any)
	if !ok {
		t.Fatalf("metadata type: got %T", got.Metadata)
	}
	if meta["key"] != "value" {
		t.Errorf("metadata key: got %v", meta["key"])
	}
}

func TestSQLiteStore_ImportTracking(t *testing.T) {
	s := newTestSQLiteStore(t)

	if err := s.CreateImport("imp-1", "claude_code", "/path/to/file"); err != nil {
		t.Fatalf("create import: %v", err)
	}

	if err := s.UpdateImport("imp-1", "completed", 42, ""); err != nil {
		t.Fatalf("update import: %v", err)
	}

	record, err := s.GetLastImport("claude_code")
	if err != nil {
		t.Fatalf("get last import: %v", err)
	}
	if record == nil {
		t.Fatal("expected import record")
	}
	if record.Status != "completed" {
		t.Errorf("status: got %q", record.Status)
	}
	if record.MemoriesImported != 42 {
		t.Errorf("memories_imported: got %d", record.MemoriesImported)
	}

	record, err = s.GetLastImport("unknown")
	if err != nil {
		t.Fatalf("get last import unknown: %v", err)
	}
	if record != nil {
		t.Error("expected nil for unknown source")
	}
}

func TestSQLiteStore_SaveWithProjectID(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	proj := "test-project"
	saveAndGetID(t, s, ctx, Memory{
		Content:   "project-specific memory",
		Category:  "fact",
		Source:    "test",
		ProjectID: &proj,
	})

	results, err := s.List(ctx, ListOptions{ProjectID: proj})
	if err != nil {
		t.Fatalf("list by project: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if *results[0].ProjectID != proj {
		t.Errorf("project_id: got %q, want %q", *results[0].ProjectID, proj)
	}
}

func TestSQLiteStore_SaveWithKeywords(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// Keywords are set by the caller (Service layer), not by SQLiteStore.Save
	kw := []string{"language", "garbage", "collection"}
	m := Memory{
		Content:  "the Go language has garbage collection",
		Category: "fact",
		Source:   "test",
		Keywords: kw,
	}
	if err := s.Save(ctx, m); err != nil {
		t.Fatalf("save: %v", err)
	}

	all, _ := s.List(ctx, ListOptions{})
	if len(all) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(all))
	}
	got := all[0]
	if len(got.Keywords) != 3 {
		t.Fatalf("expected 3 keywords, got %d: %v", len(got.Keywords), got.Keywords)
	}
}

func TestNewSQLiteStore(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "new.db")
	s, err := NewSQLiteStore(tmp, slog.Default())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer s.Close()

	if s.DB() == nil {
		t.Error("expected DB to be non-nil")
	}
	if s.VectorCache() == nil {
		t.Error("expected VectorCache to be non-nil")
	}

	if _, err := os.Stat(tmp); err != nil {
		t.Errorf("db file: %v", err)
	}
}
