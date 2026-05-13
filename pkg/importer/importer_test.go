package importer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// --- Mocks ---

// mockSource implements Source for orchestration tests.
type mockSource struct {
	name      string
	path      string
	detected  bool
	importFn  func(ctx context.Context, store ImportStore) ImportResult
}

func (m *mockSource) Name() string  { return m.name }
func (m *mockSource) Path() string  { return m.path }
func (m *mockSource) Detect() bool  { return m.detected }

func (m *mockSource) Import(ctx context.Context, store ImportStore) ImportResult {
	if m.importFn != nil {
		return m.importFn(ctx, store)
	}
	return ImportResult{Source: m.name}
}

// mockStore implements ImportStore + ImportTracker for RunAll tests.
type runAllMockStore struct {
	mu          sync.Mutex
	saved       []runAllSaveCall
	saveErr     error
	imports     []mockImportRecord
	lastImport  *ImportRecordInfo
}

type runAllSaveCall struct {
	content  string
	category string
	source   string
	cwd      string
}

type mockImportRecord struct {
	id      string
	source  string
	path    string
	status  string
	count   int
	errMsg  string
}

func (m *runAllMockStore) SaveRaw(_ context.Context, content, category, source, cwd string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return m.saveErr
	}
	m.saved = append(m.saved, runAllSaveCall{content, category, source, cwd})
	return nil
}

func (m *runAllMockStore) SaveRawWithSource(_ context.Context, content, category, source string, _ *string, cwd string) error {
	return m.SaveRaw(nil, content, category, source, cwd)
}

func (m *runAllMockStore) CreateImport(id, source, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.imports = append(m.imports, mockImportRecord{id: id, source: source, path: path})
	return nil
}

func (m *runAllMockStore) UpdateImport(id, status string, memoriesImported int, errMsg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.imports {
		if m.imports[i].id == id {
			m.imports[i].status = status
			m.imports[i].count = memoriesImported
			m.imports[i].errMsg = errMsg
		}
	}
	return nil
}

func (m *runAllMockStore) GetLastImport(source string) (*ImportRecordInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastImport, nil
}

// noTrackerStore implements ImportStore but NOT ImportTracker.
type noTrackerStore struct {
	mu    sync.Mutex
	saved []runAllSaveCall
}

func (n *noTrackerStore) SaveRaw(_ context.Context, content, category, source, cwd string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.saved = append(n.saved, runAllSaveCall{content, category, source, cwd})
	return nil
}

func (n *noTrackerStore) SaveRawWithSource(_ context.Context, content, category, source string, _ *string, cwd string) error {
	return n.SaveRaw(nil, content, category, source, cwd)
}

// --- Tests ---

func TestRunAll_NoSources_ReturnsEmpty(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	results := RunAll(context.Background(), nil, &runAllMockStore{}, logger)
	if len(results) != 0 {
		t.Fatalf("expected 0 results with no sources, got %d", len(results))
	}
}

func TestRunAll_EmptySourceSlice_ReturnsEmpty(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	results := RunAll(context.Background(), []Source{}, &runAllMockStore{}, logger)
	if len(results) != 0 {
		t.Fatalf("expected 0 results with empty source slice, got %d", len(results))
	}
}

func TestRunAll_UndetectedSource_Skipped(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := &runAllMockStore{}

	src := &mockSource{
		name:     "test-undetected",
		path:     "/nonexistent",
		detected: false,
	}

	results := RunAll(context.Background(), []Source{src}, store, logger)
	if len(results) != 0 {
		t.Fatalf("expected 0 results when source not detected, got %d", len(results))
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.saved) != 0 {
		t.Fatalf("expected no saves when source not detected, got %d", len(store.saved))
	}
}

func TestRunAll_DetectedSource_NoData_ZeroCounts(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := &noTrackerStore{}

	src := &mockSource{
		name:     "empty-source",
		path:     t.TempDir(),
		detected: true,
		importFn: func(_ context.Context, _ ImportStore) ImportResult {
			return ImportResult{Source: "empty-source", Found: 0, Imported: 0}
		},
	}

	results := RunAll(context.Background(), []Source{src}, store, logger)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Source != "empty-source" {
		t.Errorf("expected source 'empty-source', got %q", results[0].Source)
	}
	if results[0].Imported != 0 {
		t.Errorf("expected 0 imported, got %d", results[0].Imported)
	}
}

func TestRunAll_ValidSource_ImportsCorrectly(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := &noTrackerStore{}

	imported := 0
	src := &mockSource{
		name:     "test-valid",
		path:     t.TempDir(),
		detected: true,
		importFn: func(_ context.Context, s ImportStore) ImportResult {
			_ = s.SaveRaw(context.Background(), "test fact content", "fact", "test-valid", "/test")
			imported++
			return ImportResult{Source: "test-valid", Found: 1, Imported: 1}
		},
	}

	results := RunAll(context.Background(), []Source{src}, store, logger)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Imported != 1 {
		t.Errorf("expected 1 imported, got %d", results[0].Imported)
	}
	if results[0].Source != "test-valid" {
		t.Errorf("expected source 'test-valid', got %q", results[0].Source)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.saved) != 1 {
		t.Fatalf("expected 1 saved, got %d", len(store.saved))
	}
	if store.saved[0].content != "test fact content" {
		t.Errorf("unexpected content: %q", store.saved[0].content)
	}
}

func TestRunAll_SourceImportError_RecordsError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := &noTrackerStore{}

	src := &mockSource{
		name:     "failing-source",
		path:     t.TempDir(),
		detected: true,
		importFn: func(_ context.Context, _ ImportStore) ImportResult {
			return ImportResult{
				Source:   "failing-source",
				Found:    5,
				Imported: 2,
				Errors:   3,
			}
		},
	}

	results := RunAll(context.Background(), []Source{src}, store, logger)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Errors != 3 {
		t.Errorf("expected 3 errors, got %d", results[0].Errors)
	}
	if results[0].Imported != 2 {
		t.Errorf("expected 2 imported, got %d", results[0].Imported)
	}
}

func TestRunAll_MultipleSources_ProcessesAll(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := &noTrackerStore{}

	sources := []Source{
		&mockSource{
			name:     "source-a",
			path:     t.TempDir(),
			detected: true,
			importFn: func(_ context.Context, s ImportStore) ImportResult {
				_ = s.SaveRaw(context.Background(), "from A", "fact", "source-a", "/a")
				return ImportResult{Source: "source-a", Found: 1, Imported: 1}
			},
		},
		&mockSource{
			name:     "source-b",
			path:     t.TempDir(),
			detected: true,
			importFn: func(_ context.Context, s ImportStore) ImportResult {
				_ = s.SaveRaw(context.Background(), "from B", "preference", "source-b", "/b")
				_ = s.SaveRaw(context.Background(), "from B too", "fact", "source-b", "/b")
				return ImportResult{Source: "source-b", Found: 2, Imported: 2}
			},
		},
		&mockSource{
			name:     "source-c-skipped",
			path:     "/nonexistent",
			detected: false,
		},
	}

	results := RunAll(context.Background(), sources, store, logger)
	if len(results) != 2 {
		t.Fatalf("expected 2 results (undetected skipped), got %d", len(results))
	}

	totalImported := 0
	for _, r := range results {
		totalImported += r.Imported
	}
	if totalImported != 3 {
		t.Errorf("expected 3 total imported, got %d", totalImported)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.saved) != 3 {
		t.Fatalf("expected 3 saved, got %d", len(store.saved))
	}
}

func TestRunAll_TrackerCreateAndUpdate(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := &runAllMockStore{}

	src := &mockSource{
		name:     "tracked-source",
		path:     t.TempDir(),
		detected: true,
		importFn: func(_ context.Context, _ ImportStore) ImportResult {
			return ImportResult{Source: "tracked-source", Found: 10, Imported: 7}
		},
	}

	results := RunAll(context.Background(), []Source{src}, store, logger)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.imports) != 1 {
		t.Fatalf("expected 1 import record, got %d", len(store.imports))
	}
	rec := store.imports[0]
	if rec.source != "tracked-source" {
		t.Errorf("expected source 'tracked-source', got %q", rec.source)
	}
	if rec.status != "done" {
		t.Errorf("expected status 'done', got %q", rec.status)
	}
	if rec.count != 7 {
		t.Errorf("expected count 7, got %d", rec.count)
	}
}

func TestRunAll_TrackerSkipsUnchanged(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	tmpDir := t.TempDir()
	// Create a file so Stat succeeds and ModTime is known.
	testFile := filepath.Join(tmpDir, "data.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Simulate that last import was done after the file was modified.
	past := time.Now().Add(1 * time.Hour)
	store := &runAllMockStore{
		lastImport: &ImportRecordInfo{
			Source:     "unchanged-source",
			Path:       tmpDir,
			Status:     "done",
			FinishedAt: &past,
		},
	}

	importCalled := false
	src := &mockSource{
		name:     "unchanged-source",
		path:     tmpDir,
		detected: true,
		importFn: func(_ context.Context, _ ImportStore) ImportResult {
			importCalled = true
			return ImportResult{Source: "unchanged-source", Imported: 99}
		},
	}

	results := RunAll(context.Background(), []Source{src}, store, logger)
	if len(results) != 1 {
		t.Fatalf("expected 1 result (skipped), got %d", len(results))
	}
	if results[0].Imported != 0 {
		t.Errorf("expected 0 imported for skipped source, got %d", results[0].Imported)
	}
	if importCalled {
		t.Error("Import should not have been called for unchanged source")
	}
}

func TestRunAll_ForceFlag_IgnoresTracker(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "data.txt")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Simulate last import in the future (would normally skip).
	future := time.Now().Add(1 * time.Hour)
	store := &runAllMockStore{
		lastImport: &ImportRecordInfo{
			Source:     "force-source",
			Path:       tmpDir,
			Status:     "done",
			FinishedAt: &future,
		},
	}

	importCalled := false
	src := &mockSource{
		name:     "force-source",
		path:     tmpDir,
		detected: true,
		importFn: func(_ context.Context, _ ImportStore) ImportResult {
			importCalled = true
			return ImportResult{Source: "force-source", Found: 1, Imported: 1}
		},
	}

	results := RunAll(context.Background(), []Source{src}, store, logger, RunAllOptions{Force: true})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !importCalled {
		t.Error("Import should have been called when Force=true")
	}
	if results[0].Imported != 1 {
		t.Errorf("expected 1 imported with Force=true, got %d", results[0].Imported)
	}
}

func TestRunAll_StoreWithoutTracker_NoPanic(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := &noTrackerStore{}

	src := &mockSource{
		name:     "simple-source",
		path:     t.TempDir(),
		detected: true,
		importFn: func(_ context.Context, s ImportStore) ImportResult {
			_ = s.SaveRaw(context.Background(), "content", "fact", "simple-source", "/test")
			return ImportResult{Source: "simple-source", Found: 1, Imported: 1}
		},
	}

	// Should not panic even though store does not implement ImportTracker.
	results := RunAll(context.Background(), []Source{src}, store, logger)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Imported != 1 {
		t.Errorf("expected 1 imported, got %d", results[0].Imported)
	}
}

func TestRunAll_CancelledContext(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := &noTrackerStore{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	src := &mockSource{
		name:     "cancel-source",
		path:     t.TempDir(),
		detected: true,
		importFn: func(_ context.Context, _ ImportStore) ImportResult {
			return ImportResult{Source: "cancel-source", Imported: 0}
		},
	}

	// RunAll should still function (sources handle ctx cancellation internally).
	results := RunAll(ctx, []Source{src}, store, logger)
	if len(results) != 1 {
		t.Fatalf("expected 1 result even with cancelled context, got %d", len(results))
	}
}

func TestRunAll_StoreSaveError_SourcesHandleErrors(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	store := &runAllMockStore{saveErr: fmt.Errorf("disk full")}

	src := &mockSource{
		name:     "error-source",
		path:     t.TempDir(),
		detected: true,
		importFn: func(ctx context.Context, s ImportStore) ImportResult {
			err := s.SaveRaw(ctx, "content", "fact", "error-source", "/test")
			if err != nil {
				return ImportResult{Source: "error-source", Found: 1, Errors: 1}
			}
			return ImportResult{Source: "error-source", Found: 1, Imported: 1}
		},
	}

	results := RunAll(context.Background(), []Source{src}, store, logger)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Errors != 1 {
		t.Errorf("expected 1 error, got %d", results[0].Errors)
	}
}
