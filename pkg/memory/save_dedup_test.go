package memory

import (
	"context"
	"log/slog"
	"testing"
)

// contentHash is verbatim (no normalization) to stay byte-compatible with the
// sync protocol and older clients. Case/whitespace variants therefore hash
// DIFFERENTLY here and are instead folded by the near-duplicate merge
// (TestFindNearDuplicate). This test pins that contract.
func TestContentHash_Verbatim(t *testing.T) {
	if contentHash("We use Postgres.") == contentHash("we use postgres.") {
		t.Error("contentHash must be verbatim (case-sensitive) for sync compatibility")
	}
	if contentHash("we use postgres") != contentHash("we use postgres") {
		t.Error("identical content must hash equal")
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
	content := "The sync engine uses watermark and tombstones for delta updates"

	// Same text modulo case/whitespace IS a duplicate.
	svc.store = &nearDupMockStore{candidates: []SearchResult{
		{Memory: Memory{ID: "x", Content: "the   sync engine uses watermark and tombstones for delta updates\n"}},
	}}
	if dup := svc.findNearDuplicate(context.Background(), content, nil); dup == nil || dup.ID != "x" {
		t.Fatalf("expected case/whitespace variant match 'x', got %v", dup)
	}

	// A near-identical-but-DIFFERENT restatement must NOT merge (only one extra
	// word). This is the over-merge the old Jaccard approach caused.
	svc.store = &nearDupMockStore{candidates: []SearchResult{
		{Memory: Memory{ID: "y", Content: "the sync engine uses watermark and tombstones for delta updates always"}},
	}}
	if dup := svc.findNearDuplicate(context.Background(), content, nil); dup != nil {
		t.Fatalf("near-identical but distinct content must NOT merge, got %v", dup)
	}

	// A genuinely different fact must NOT merge.
	svc.store = &nearDupMockStore{candidates: []SearchResult{
		{Memory: Memory{ID: "z", Content: "the billing service charges invoices monthly via stripe"}},
	}}
	if dup := svc.findNearDuplicate(context.Background(), content, nil); dup != nil {
		t.Fatalf("expected no near-dup, got %v", dup)
	}
}
