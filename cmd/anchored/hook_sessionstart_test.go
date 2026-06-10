package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/jholhewres/anchored/pkg/memory"

	_ "github.com/mattn/go-sqlite3"
)

// newSessionStartTestDB runs the REAL client migrations so the queries in the
// sessionstart hook are validated against the production schema (pinned and
// curation_status live in the metadata JSON column, not as real columns — a
// hand-rolled schema here would hide that class of bug).
func newSessionStartTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := memory.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func insertSessionStartMem(t *testing.T, db *sql.DB, id, projectID, category, content, metadata string, deleted bool) {
	t.Helper()
	deletedAt := any(nil)
	if deleted {
		deletedAt = "2026-01-01T00:00:00Z"
	}
	_, err := db.Exec(
		`INSERT INTO memories (id, project_id, category, content, content_hash, created_at, updated_at, metadata, deleted_at)
		 VALUES (?, ?, ?, ?, '', datetime('now'), datetime('now'), ?, ?)`,
		id, projectID, category, content, metadata, deletedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
}

// TestQueryDecisions_MetadataPinnedAndLifecycle is the regression test for the
// schema mismatch: pinned/curation_status are metadata JSON fields in the
// client. It also guards the soft-delete and low-signal exclusions.
func TestQueryDecisions_MetadataPinnedAndLifecycle(t *testing.T) {
	db := newSessionStartTestDB(t)
	hc := &HookContext{db: db}

	insertSessionStartMem(t, db, "m1", "proj1", "fact",
		"pinned architectural ground rule", `{"pinned": true}`, false)
	insertSessionStartMem(t, db, "m2", "proj1", "decision",
		"we settled on postgres for the server", `{}`, false)
	insertSessionStartMem(t, db, "m3", "proj1", "decision",
		"deleted decision must not appear", `{}`, true)
	insertSessionStartMem(t, db, "m4", "proj1", "learning",
		"low signal noise should be excluded", `{"curation_status": "low_signal"}`, false)
	insertSessionStartMem(t, db, "m5", "proj1", "fact",
		"plain fact without pin is not a decision", `{}`, false)

	items := queryDecisions(context.Background(), hc, "proj1")
	if len(items) != 2 {
		t.Fatalf("want 2 items (pinned + decision), got %d: %+v", len(items), items)
	}
	// Pinned memory must come first (priority 0).
	if !strings.Contains(items[0].Text, "pinned architectural ground rule") {
		t.Errorf("pinned memory should rank first, got: %s", items[0].Text)
	}
	if items[0].Priority != 0 {
		t.Errorf("pinned item priority = %d, want 0", items[0].Priority)
	}
	if !strings.Contains(items[1].Text, "settled on postgres") {
		t.Errorf("decision memory missing, got: %s", items[1].Text)
	}
	for _, it := range items {
		if strings.Contains(it.Text, "deleted decision") {
			t.Error("soft-deleted memory leaked into the sessionstart block")
		}
		if strings.Contains(it.Text, "low signal noise") {
			t.Error("low_signal memory leaked into the sessionstart block")
		}
	}
}

// TestBuildSessionStartTiers_FailSafeOnEmptyDB ensures an empty (but migrated)
// DB produces tiers without errors and Assemble-able output.
func TestBuildSessionStartTiers_FailSafeOnEmptyDB(t *testing.T) {
	db := newSessionStartTestDB(t)
	hc := &HookContext{db: db}

	tiers := buildSessionStartTiers(context.Background(), hc, "", "proj-none")
	if len(tiers) != 4 {
		t.Fatalf("want 4 tiers, got %d", len(tiers))
	}
	// decisions/task/events must be empty on a fresh DB (identity may pick up
	// the developer's real ~/.anchored/identity.md, which is fine).
	for _, tier := range tiers[1:] {
		if tier.Name == "task" || tier.Name == "events" || tier.Name == "decisions" {
			if len(tier.Items) != 0 {
				t.Errorf("tier %s should be empty on fresh DB, got %d items", tier.Name, len(tier.Items))
			}
		}
	}
}
