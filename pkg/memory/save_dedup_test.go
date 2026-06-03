package memory

import (
	"context"
	"log/slog"
	"testing"
)

func TestContentHash_NormalizesCaseAndWhitespace(t *testing.T) {
	if contentHash("We use Postgres.") != contentHash("we  use   postgres.\n") {
		t.Error("case/whitespace variants should hash equal")
	}
	if contentHash("we use postgres") == contentHash("we use mysql") {
		t.Error("distinct content must hash differently")
	}
}

type nearDupMockStore struct {
	Store
	candidates []SearchResult
}

func (m *nearDupMockStore) Search(_ context.Context, _ string, _ SearchOptions) ([]SearchResult, error) {
	return m.candidates, nil
}

func TestFindNearDuplicate(t *testing.T) {
	svc := &Service{logger: slog.Default()}
	content := "the sync engine uses watermark and tombstones for delta updates"

	// A near-identical restatement should be detected as a duplicate.
	svc.store = &nearDupMockStore{candidates: []SearchResult{
		{Memory: Memory{ID: "x", Content: "the sync engine uses watermark and tombstones for delta updates always"}},
	}}
	if dup := svc.findNearDuplicate(context.Background(), content, nil); dup == nil || dup.ID != "x" {
		t.Fatalf("expected near-dup match 'x', got %v", dup)
	}

	// A different fact that happens to surface as a candidate must NOT merge.
	svc.store = &nearDupMockStore{candidates: []SearchResult{
		{Memory: Memory{ID: "y", Content: "the billing service charges invoices monthly via stripe"}},
	}}
	if dup := svc.findNearDuplicate(context.Background(), content, nil); dup != nil {
		t.Fatalf("expected no near-dup, got %v", dup)
	}
}
