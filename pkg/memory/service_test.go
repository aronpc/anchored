package memory

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/project"
)

// --- Mock Store for Service tests ---

type svcMockStore struct {
	mu       sync.Mutex
	memories map[string]*Memory
	hashIdx  map[string]string // contentHash -> id
	seq      int
}

func newSvcMockStore() *svcMockStore {
	return &svcMockStore{
		memories: make(map[string]*Memory),
		hashIdx:  make(map[string]string),
	}
}

func (s *svcMockStore) Save(_ context.Context, m Memory) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID == "" {
		s.seq++
		m.ID = fmt.Sprintf("mock-%d", s.seq)
	}
	cl := m
	s.memories[m.ID] = &cl
	if m.ContentHash != "" {
		s.hashIdx[m.ContentHash] = m.ID
	}
	return nil
}

func (s *svcMockStore) Get(_ context.Context, id string) (*Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.memories[id]
	if !ok {
		return nil, nil
	}
	cl := *m
	return &cl, nil
}

func (s *svcMockStore) Update(_ context.Context, id, content, category string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.memories[id]
	if !ok {
		return nil
	}
	m.Content = content
	m.Category = category
	return nil
}

func (s *svcMockStore) UpdateMetadata(_ context.Context, id string, metadata any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.memories[id]; ok {
		m.Metadata = metadata
	}
	return nil
}

func (s *svcMockStore) Search(_ context.Context, query string, _ SearchOptions) ([]SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var results []SearchResult
	for _, m := range s.memories {
		results = append(results, SearchResult{Memory: *m, Score: 0.5})
	}
	return results, nil
}

func (s *svcMockStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.memories, id)
	return nil
}

func (s *svcMockStore) SoftDelete(_ context.Context, id string) error {
	return s.Delete(context.Background(), id)
}

func (s *svcMockStore) DeleteByScope(_ context.Context, _ DeleteScopeOptions) (int, error) {
	return 0, nil
}

func (s *svcMockStore) List(_ context.Context, opts ListOptions) ([]Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []Memory
	for _, m := range s.memories {
		if opts.Category != "" && m.Category != opts.Category {
			continue
		}
		result = append(result, *m)
	}
	if opts.Limit > 0 && len(result) > opts.Limit {
		result = result[:opts.Limit]
	}
	return result, nil
}

func (s *svcMockStore) Stats(_ context.Context) (*StoreStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := &StoreStats{ByCategory: make(map[string]int), ByProject: make(map[string]int)}
	for _, m := range s.memories {
		stats.TotalMemories++
		stats.ByCategory[m.Category]++
	}
	return stats, nil
}

func (s *svcMockStore) UpdateEmbedding(_ context.Context, id string, emb []float32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m, ok := s.memories[id]; ok {
		m.Embedding = emb
	}
	return nil
}

func (s *svcMockStore) ListWithoutEmbedding(_ context.Context, limit int) ([]Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []Memory
	for _, m := range s.memories {
		if len(m.Embedding) == 0 {
			result = append(result, *m)
		}
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (s *svcMockStore) FindByContentHash(_ context.Context, hash string, _ *string) (*Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.hashIdx[hash]
	if !ok {
		return nil, nil
	}
	cl := *s.memories[id]
	return &cl, nil
}

func (s *svcMockStore) BackfillContentHash(_ context.Context) (int, error) { return 0, nil }
func (s *svcMockStore) DB() *sql.DB                                        { return nil }
func (s *svcMockStore) VectorCache() *VectorCache                          { return nil }
func (s *svcMockStore) Close() error                                       { return nil }

// --- Mock Embedder ---

type svcMockEmbedder struct {
	dims   int
	vecs   map[string][]float32
	mu     sync.Mutex
	closed bool
}

func newSvcMockEmbedder() *svcMockEmbedder {
	return &svcMockEmbedder{
		dims: 4,
		vecs: make(map[string][]float32),
	}
}

func (e *svcMockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([][]float32, len(texts))
	for i, t := range texts {
		if v, ok := e.vecs[t]; ok {
			result[i] = v
		} else {
			vec := []float32{0.1, 0.2, 0.3, 0.4}
			e.vecs[t] = vec
			result[i] = vec
		}
	}
	return result, nil
}

func (e *svcMockEmbedder) Dimensions() int { return e.dims }
func (e *svcMockEmbedder) Name() string    { return "mock" }
func (e *svcMockEmbedder) Model() string   { return "mock-model" }
func (e *svcMockEmbedder) Close() error    { e.closed = true; return nil }

// --- Helpers ---

func newTestService(t *testing.T, store Store, embedder EmbeddingProvider) *Service {
	t.Helper()
	return &Service{
		store:     store,
		sanitizer: NewSanitizer(config.SanitizerConfig{Enabled: false}),
		projDet:   project.NewDetector(nil),
		embedder:  embedder,
		logger:    slog.Default(),
		embedSem:  make(chan struct{}, 10),
		shutdown:  make(chan struct{}),
	}
}

// saveAndGetID stores via Service.Save and retrieves the ID from the store.
// Service.Save returns &m where m.ID may be empty (Save receives Memory by value).
func saveAndGetSvcID(t *testing.T, svc *Service, store *svcMockStore, ctx context.Context, content, category, source string) string {
	t.Helper()
	_, err := svc.Save(ctx, content, category, source, "")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, m := range store.memories {
		if m.Content == content {
			return m.ID
		}
	}
	t.Fatal("saved memory not found in mock store")
	return ""
}

// --- Tests ---

func TestService_Save_Basic(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	m, err := svc.Save(ctx, "user prefers Vim over Emacs", "preference", "test", "")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if m == nil {
		t.Fatal("expected memory, got nil")
	}
	if m.Content != "user prefers Vim over Emacs" {
		t.Errorf("content: got %q", m.Content)
	}
	if m.Category != "preference" {
		t.Errorf("category: got %q", m.Category)
	}
	if m.ContentHash == "" {
		t.Error("expected content hash")
	}

	// Verify it was actually stored
	id := saveAndGetSvcID(t, svc, store, ctx, "user prefers Vim over Emacs", "preference", "test")
	if id == "" {
		t.Error("expected ID in store")
	}
}

func TestService_Save_AutoCategorize(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	m, err := svc.Save(ctx, "deployed v2 today", "", "test", "")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if m.Category == "" {
		t.Error("expected auto-categorization")
	}
}

func TestService_Save_EmptyContent(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	_, err := svc.Save(ctx, "", "fact", "test", "")
	if err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestService_Save_WhitespaceOnly(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	_, err := svc.Save(ctx, "   \t\n  ", "fact", "test", "")
	if err == nil {
		t.Fatal("expected error for whitespace-only content")
	}
}

func TestService_Save_DedupByHash(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	_, err := svc.Save(ctx, "same content here", "fact", "test", "")
	if err != nil {
		t.Fatalf("save 1: %v", err)
	}

	_, err = svc.Save(ctx, "same content here", "decision", "test", "")
	if err != nil {
		t.Fatalf("save 2: %v", err)
	}

	// Both saves should result in only 1 memory (dedup by hash)
	store.mu.Lock()
	count := len(store.memories)
	store.mu.Unlock()
	if count != 1 {
		t.Errorf("dedup: expected 1 memory in store, got %d", count)
	}
}

func TestService_SaveWithOptions_SkipEmbed(t *testing.T) {
	store := newSvcMockStore()
	embedder := newSvcMockEmbedder()
	svc := newTestService(t, store, embedder)
	ctx := context.Background()

	m, err := svc.SaveWithOptions(ctx, SaveOptions{
		Content:   "skip embed test",
		Category:  "fact",
		Source:    "test",
		SkipEmbed: true,
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if m == nil {
		t.Fatal("expected memory")
	}
}

func TestService_Get(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	id := saveAndGetSvcID(t, svc, store, ctx, "test content", "fact", "test")

	got, err := svc.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("expected memory")
	}
	if got.Content != "test content" {
		t.Errorf("content: got %q", got.Content)
	}

	missing, _ := svc.Get(ctx, "nonexistent")
	if missing != nil {
		t.Error("expected nil for nonexistent")
	}
}

func TestService_Delete(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	id := saveAndGetSvcID(t, svc, store, ctx, "to delete", "fact", "test")

	if err := svc.Forget(ctx, id); err != nil {
		t.Fatalf("forget: %v", err)
	}

	got, _ := svc.Get(ctx, id)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestService_Update(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	id := saveAndGetSvcID(t, svc, store, ctx, "original", "fact", "test")

	updated, err := svc.Update(ctx, id, "updated content", "decision")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Content != "updated content" {
		t.Errorf("content: got %q", updated.Content)
	}
	if updated.Category != "decision" {
		t.Errorf("category: got %q", updated.Category)
	}
}

func TestService_Update_NotFound(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	_, err := svc.Update(ctx, "nonexistent", "content", "fact")
	if err == nil {
		t.Fatal("expected error for nonexistent memory")
	}
}

func TestService_Update_NoChanges(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	id := saveAndGetSvcID(t, svc, store, ctx, "original", "fact", "test")

	_, err := svc.Update(ctx, id, "", "")
	if err == nil {
		t.Fatal("expected error when nothing to update")
	}
}

func TestService_Update_CategoryOnly(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	id := saveAndGetSvcID(t, svc, store, ctx, "original content", "fact", "test")

	updated, err := svc.Update(ctx, id, "", "decision")
	if err != nil {
		t.Fatalf("update category: %v", err)
	}
	if updated.Content != "original content" {
		t.Errorf("content should be preserved: got %q", updated.Content)
	}
	if updated.Category != "decision" {
		t.Errorf("category: got %q", updated.Category)
	}
}

func TestService_Search(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	svc.Save(ctx, "Go is a programming language", "fact", "test", "")

	results, err := svc.Search(ctx, "programming", SearchOptions{MaxResults: 10})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
}

func TestService_Search_EmptyQuery(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	_, err := svc.Search(ctx, "", SearchOptions{})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestService_List(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	svc.Save(ctx, "fact 1", "fact", "test", "")
	svc.Save(ctx, "fact 2", "fact", "test", "")
	svc.Save(ctx, "decision 1", "decision", "test", "")

	all, err := svc.List(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3, got %d", len(all))
	}

	facts, err := svc.List(ctx, ListOptions{Category: "fact"})
	if err != nil {
		t.Fatalf("list facts: %v", err)
	}
	if len(facts) != 2 {
		t.Errorf("expected 2 facts, got %d", len(facts))
	}
}

func TestService_Stats(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	svc.Save(ctx, "a", "fact", "test", "")
	svc.Save(ctx, "b", "decision", "test", "")

	stats, err := svc.Stats(ctx)
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TotalMemories != 2 {
		t.Errorf("total: got %d, want 2", stats.TotalMemories)
	}
}

func TestService_SoftForget(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	id := saveAndGetSvcID(t, svc, store, ctx, "soft delete me", "fact", "test")

	if err := svc.SoftForget(ctx, id); err != nil {
		t.Fatalf("soft forget: %v", err)
	}
}

func TestService_BackfillEmbeddings_NoEmbedder(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	_, err := svc.BackfillEmbeddings(ctx, 100)
	if err == nil {
		t.Fatal("expected error when no embedder")
	}
}

func TestService_BackfillEmbeddings_WithEmbedder(t *testing.T) {
	// Use real SQLiteStore since BackfillEmbeddings needs EmbeddingCache with real DB
	db := openTestDB(t)
	cache := NewVectorCache(slog.Default())
	cache.Load(db)
	store := &SQLiteStore{db: db, cache: cache, logger: slog.Default()}
	embedder := newSvcMockEmbedder()
	embCache := NewEmbeddingCache(db, slog.Default())

	svc := &Service{
		store:     store,
		sanitizer: NewSanitizer(config.SanitizerConfig{Enabled: false}),
		projDet:   project.NewDetector(nil),
		embedder:  embedder,
		cache:     embCache,
		logger:    slog.Default(),
		embedSem:  make(chan struct{}, 10),
		shutdown:  make(chan struct{}),
	}
	ctx := context.Background()

	// Insert directly into the store (bypassing svc.Save) so the memories land
	// without embeddings. Going through svc.Save would race its async embed
	// goroutine against the backfill loop, which intermittently hits SQLITE_BUSY
	// under modernc's deferred transaction locking.
	if err := store.Save(ctx, Memory{Content: "embed me 1", Category: "fact", Source: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, Memory{Content: "embed me 2", Category: "fact", Source: "test"}); err != nil {
		t.Fatal(err)
	}

	total, err := svc.BackfillEmbeddings(ctx, 10)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if total < 1 {
		t.Errorf("expected at least 1 backfilled, got %d", total)
	}
}

func TestService_BackfillEmbeddings_DefaultBatchSize(t *testing.T) {
	db := openTestDB(t)
	cache := NewVectorCache(slog.Default())
	cache.Load(db)
	store := &SQLiteStore{db: db, cache: cache, logger: slog.Default()}
	embedder := newSvcMockEmbedder()
	embCache := NewEmbeddingCache(db, slog.Default())

	svc := &Service{
		store:     store,
		sanitizer: NewSanitizer(config.SanitizerConfig{Enabled: false}),
		projDet:   project.NewDetector(nil),
		embedder:  embedder,
		cache:     embCache,
		logger:    slog.Default(),
		embedSem:  make(chan struct{}, 10),
		shutdown:  make(chan struct{}),
	}
	ctx := context.Background()

	// Insert directly into the store (bypassing svc.Save) to avoid racing the
	// async embed goroutine against the backfill loop (SQLITE_BUSY under modernc).
	if err := store.Save(ctx, Memory{Content: "content for backfill", Category: "fact", Source: "test"}); err != nil {
		t.Fatal(err)
	}

	total, err := svc.BackfillEmbeddings(ctx, 0)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	// At least 1 should succeed
	if total < 1 {
		t.Errorf("expected at least 1, got %d", total)
	}
}

func TestService_SaveRaw(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	err := svc.SaveRaw(ctx, "raw content", "fact", "test", "")
	if err != nil {
		t.Fatalf("save raw: %v", err)
	}
}

func TestService_SaveRawNoEmbed(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	ctx := context.Background()

	err := svc.SaveRawNoEmbed(ctx, "no embed content", "fact", "test", "")
	if err != nil {
		t.Fatalf("save raw no embed: %v", err)
	}
}

func TestService_ResolveProject(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)

	result := svc.ResolveProject("")
	if result != "" {
		t.Errorf("expected empty for empty cwd, got %q", result)
	}
}

func TestService_SetKGExtractor(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	svc.SetKGExtractor(nil)
}

func TestService_Close(t *testing.T) {
	store := newSvcMockStore()
	embedder := newSvcMockEmbedder()
	svc := newTestService(t, store, embedder)

	svc.Close()

	if !embedder.closed {
		t.Error("expected embedder to be closed")
	}
}

func TestService_Close_NilEmbedder(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	svc.Close()
}

func TestService_Save_Sanitizer(t *testing.T) {
	store := newSvcMockStore()
	svc := newTestService(t, store, nil)
	svc.sanitizer = NewSanitizer(config.SanitizerConfig{Enabled: true})
	ctx := context.Background()

	m, err := svc.Save(ctx, "the api_key = skabc123def456ghi789jkl012mno345", "fact", "test", "")
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if m.Content == "the api_key = skabc123def456ghi789jkl012mno345" {
		t.Error("expected content to be sanitized")
	}
}
