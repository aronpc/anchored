package memory

import (
	"context"
	"testing"
)

// TestService_SaveRemoteMemory_MarksOrigin verifies the remote-pull
// materialization path: a memory pulled from the server lands in the local
// store with the explicit project override AND is stamped sync_origin='remote',
// so the push path classifies it as already_synced and never re-sends it
// (anti-loop). A second pull of the same content dedups to the same row.
func TestService_SaveRemoteMemory_MarksOrigin(t *testing.T) {
	store := newTestSQLiteStore(t)
	svc := newTestService(t, store, nil) // nil embedder + SkipEmbed below
	ctx := context.Background()

	pid := "proj-local-1"
	m, err := svc.SaveRemoteMemory(ctx, SaveOptions{
		Content:   "team standardized on Postgres for new services",
		Category:  "decision",
		Source:    "remote-sync",
		ProjectID: &pid,
		SkipEmbed: true,
	})
	if err != nil {
		t.Fatalf("SaveRemoteMemory: %v", err)
	}

	got, err := store.Get(ctx, m.ID)
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if got.SyncOrigin != "remote" {
		t.Errorf("expected sync_origin=remote (anti-loop marker), got %q", got.SyncOrigin)
	}
	if got.ProjectID == nil || *got.ProjectID != pid {
		t.Errorf("expected project override %q, got %v", pid, got.ProjectID)
	}

	// Idempotent re-pull: identical content dedups (content_hash) to the same
	// row — no duplicate, still marked remote.
	m2, err := svc.SaveRemoteMemory(ctx, SaveOptions{
		Content:   "team standardized on Postgres for new services",
		Category:  "decision",
		Source:    "remote-sync",
		ProjectID: &pid,
		SkipEmbed: true,
	})
	if err != nil {
		t.Fatalf("SaveRemoteMemory (dup): %v", err)
	}
	if m2.ID != m.ID {
		t.Errorf("expected dedup to same id %s, got %s", m.ID, m2.ID)
	}
}
