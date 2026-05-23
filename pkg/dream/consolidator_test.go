package dream

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/jholhewres/anchored/pkg/memory"
	_ "github.com/mattn/go-sqlite3"
)

func setupConsolidatorTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := memory.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestConsolidate_ExactDuplicate_SoftDeletes(t *testing.T) {
	db := setupConsolidatorTestDB(t)
	c := NewConsolidator(db, nil)

	report := &DreamReport{
		Actions: []DreamAction{
			{ID: "a1", MemoryID: "mem-1", ActionType: "dedup", Confidence: 1.0, Reason: "exact hash match"},
		},
	}

	result, err := c.Consolidate(context.Background(), report, DreamConfigForAggressiveness("moderate"))
	if err != nil {
		t.Fatal(err)
	}
	if result.SoftDeleted != 1 {
		t.Errorf("expected 1 soft delete, got %d", result.SoftDeleted)
	}
}

func TestConsolidate_Contradiction_FlagsOnly(t *testing.T) {
	db := setupConsolidatorTestDB(t)
	c := NewConsolidator(db, nil)

	report := &DreamReport{
		Actions: []DreamAction{
			{ID: "a1", MemoryID: "mem-1", ActionType: "contradiction", Confidence: 0.6, Reason: "negation"},
		},
	}

	result, err := c.Consolidate(context.Background(), report, DreamConfigForAggressiveness("aggressive"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Flagged != 1 {
		t.Errorf("expected 1 flagged, got %d", result.Flagged)
	}
	if result.SoftDeleted != 0 {
		t.Errorf("expected 0 deleted, got %d", result.SoftDeleted)
	}
}

func TestConsolidate_RespectsMaxDeletions(t *testing.T) {
	db := setupConsolidatorTestDB(t)
	c := NewConsolidator(db, nil)

	actions := make([]DreamAction, 10)
	for i := range actions {
		actions[i] = DreamAction{
			ID:         fmt.Sprintf("a%d", i),
			MemoryID:   fmt.Sprintf("mem-%d", i),
			ActionType: "dedup",
			Confidence: 1.0,
		}
	}

	report := &DreamReport{Actions: actions}
	cfg := DreamConfigForAggressiveness("moderate")
	cfg.MaxDeletionsPerRun = 5

	result, err := c.Consolidate(context.Background(), report, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if result.SoftDeleted != 5 {
		t.Errorf("expected 5 deletions, got %d", result.SoftDeleted)
	}
	if result.Skipped != 5 {
		t.Errorf("expected 5 skipped, got %d", result.Skipped)
	}
}

func TestConsolidate_ConservativeSkipsAll(t *testing.T) {
	db := setupConsolidatorTestDB(t)
	c := NewConsolidator(db, nil)

	report := &DreamReport{
		Actions: []DreamAction{
			{ID: "a1", MemoryID: "mem-1", ActionType: "dedup", Confidence: 0.95, Reason: "near dup"},
		},
	}

	result, err := c.Consolidate(context.Background(), report, DreamConfigForAggressiveness("conservative"))
	if err != nil {
		t.Fatal(err)
	}
	if result.SoftDeleted != 0 {
		t.Errorf("conservative should not delete, got %d", result.SoftDeleted)
	}
}

func TestDreamConfig_Levels(t *testing.T) {
	conservative := DreamConfigForAggressiveness("conservative")
	moderate := DreamConfigForAggressiveness("moderate")
	aggressive := DreamConfigForAggressiveness("aggressive")

	if conservative.DedupThreshold <= moderate.DedupThreshold {
		t.Errorf("conservative threshold (%.2f) should be > moderate (%.2f)", conservative.DedupThreshold, moderate.DedupThreshold)
	}
	if moderate.DedupThreshold <= aggressive.DedupThreshold {
		t.Errorf("moderate threshold (%.2f) should be > aggressive (%.2f)", moderate.DedupThreshold, aggressive.DedupThreshold)
	}
}

func insertTestMemory(t *testing.T, ctx context.Context, db *sql.DB, id, content string) {
	t.Helper()
	_, err := db.ExecContext(ctx,
		"INSERT INTO memories (id, category, content, content_hash) VALUES (?, 'fact', ?, 'hash1')",
		id, content)
	if err != nil {
		t.Fatal(err)
	}
}

func insertTestDreamAction(t *testing.T, ctx context.Context, db *sql.DB, id, memoryID, relatedID, actionType string, confidence float64, status string) {
	t.Helper()
	_, err := db.ExecContext(ctx,
		"INSERT INTO dream_actions (id, run_id, memory_id, related_memory_id, action_type, confidence, reason, status) VALUES (?, 'test-run', ?, ?, ?, ?, 'test', ?)",
		id, memoryID, relatedID, actionType, confidence, status)
	if err != nil {
		t.Fatal(err)
	}
}

func TestApplyAction_Dedup_SoftDeletes(t *testing.T) {
	ctx := context.Background()
	db := setupConsolidatorTestDB(t)
	c := NewConsolidator(db, nil)

	insertTestMemory(t, ctx, db, "mem-old", "duplicate content")
	insertTestMemory(t, ctx, db, "mem-new", "duplicate content")
	insertTestDreamAction(t, ctx, db, "act-1", "mem-old", "mem-new", "dedup", 1.0, "proposed")

	result, err := c.ApplyAction(ctx, "act-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "applied" {
		t.Errorf("expected status applied, got %q", result.Status)
	}
	if result.ActionType != "dedup" {
		t.Errorf("expected action_type dedup, got %q", result.ActionType)
	}
	if result.MemoryID != "mem-old" {
		t.Errorf("expected memory_id mem-old, got %q", result.MemoryID)
	}

	var deletedAt *string
	err = db.QueryRowContext(ctx, "SELECT deleted_at FROM memories WHERE id = 'mem-old'").Scan(&deletedAt)
	if err != nil {
		t.Fatal(err)
	}
	if deletedAt == nil {
		t.Error("expected mem-old to be soft-deleted, but deleted_at is nil")
	}

	var actionStatus string
	var appliedAt *string
	err = db.QueryRowContext(ctx, "SELECT status, applied_at FROM dream_actions WHERE id = 'act-1'").Scan(&actionStatus, &appliedAt)
	if err != nil {
		t.Fatal(err)
	}
	if actionStatus != "applied" {
		t.Errorf("expected action status applied, got %q", actionStatus)
	}
	if appliedAt == nil {
		t.Error("expected applied_at to be set, got nil")
	}
}

func TestApplyAction_Contradiction_ReturnsError(t *testing.T) {
	ctx := context.Background()
	db := setupConsolidatorTestDB(t)
	c := NewConsolidator(db, nil)

	insertTestMemory(t, ctx, db, "mem-a", "go is great")
	insertTestMemory(t, ctx, db, "mem-b", "go is terrible")
	insertTestDreamAction(t, ctx, db, "act-cont", "mem-a", "mem-b", "contradiction", 0.6, "proposed")

	_, err := c.ApplyAction(ctx, "act-cont")
	if err == nil {
		t.Fatal("expected error for contradiction action, got nil")
	}
}

func TestApplyAction_NonexistentID_ReturnsError(t *testing.T) {
	ctx := context.Background()
	db := setupConsolidatorTestDB(t)
	c := NewConsolidator(db, nil)

	_, err := c.ApplyAction(ctx, "does-not-exist")
	if err == nil {
		t.Fatal("expected error for nonexistent action, got nil")
	}
}

func TestApplyAction_AlreadyApplied_ReturnsError(t *testing.T) {
	ctx := context.Background()
	db := setupConsolidatorTestDB(t)
	c := NewConsolidator(db, nil)

	insertTestMemory(t, ctx, db, "mem-x", "some content")
	insertTestDreamAction(t, ctx, db, "act-done", "mem-x", "", "dedup", 1.0, "applied")

	_, err := c.ApplyAction(ctx, "act-done")
	if err == nil {
		t.Fatal("expected error for already-applied action, got nil")
	}
}
