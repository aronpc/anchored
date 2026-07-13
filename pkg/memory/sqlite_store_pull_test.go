package memory

import (
	"context"
	"testing"
)

// TestSQLiteStore_PullWatermark_RoundTrip exercises the incremental-pull
// watermark: empty on first pull, persisted after SetPullState, advanced on a
// second pull (upsert), and isolated per project.
func TestSQLiteStore_PullWatermark_RoundTrip(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	// First pull: no watermark yet.
	wm, err := s.GetPullWatermark(ctx, "proj-1")
	if err != nil {
		t.Fatalf("get (empty): %v", err)
	}
	if wm != "" {
		t.Errorf("expected empty watermark on first pull, got %q", wm)
	}

	// After a pull, the watermark is persisted.
	if err := s.SetPullState(ctx, "proj-1", "remotekey-abc", "wm-1", "cli"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if wm, _ := s.GetPullWatermark(ctx, "proj-1"); wm != "wm-1" {
		t.Errorf("expected persisted watermark %q, got %q", "wm-1", wm)
	}

	// A second pull advances the watermark (upsert on the same project).
	if err := s.SetPullState(ctx, "proj-1", "remotekey-abc", "wm-2", "cli"); err != nil {
		t.Fatalf("set (advance): %v", err)
	}
	if wm, _ := s.GetPullWatermark(ctx, "proj-1"); wm != "wm-2" {
		t.Errorf("expected advanced watermark %q, got %q", "wm-2", wm)
	}

	// A different project keeps its own (still empty) watermark.
	if wm2, _ := s.GetPullWatermark(ctx, "proj-2"); wm2 != "" {
		t.Errorf("expected empty watermark for proj-2, got %q", wm2)
	}
}

// TestSQLiteStore_MarkRemoteOrigin verifies the anti-loop marker: after pulling
// a memory from the server it is stamped sync_origin='remote' + sync_dirty=0 so
// the push path (ClassifyForPreview) classifies it as already_synced and never
// re-sends it. (The preview side is covered by
// pkg/sync/preview_test.go::TestClassifyForPreview_AlreadySynced.)
func TestSQLiteStore_MarkRemoteOrigin(t *testing.T) {
	s := newTestSQLiteStore(t)
	ctx := context.Background()

	id := saveAndGetID(t, s, ctx, Memory{
		Content:  "team standardizes on Postgres for persistence",
		Category: "decision",
		Source:   "remote-sync",
	})

	// A freshly saved memory is local, not remote.
	m, err := s.Get(ctx, id)
	if err != nil || m == nil {
		t.Fatalf("get before mark: %v", err)
	}
	if m.SyncOrigin != "" && m.SyncOrigin != "local" {
		t.Fatalf("expected local origin before marking, got %q", m.SyncOrigin)
	}

	if err := s.MarkRemoteOrigin(ctx, id); err != nil {
		t.Fatalf("mark: %v", err)
	}

	m, _ = s.Get(ctx, id)
	if m.SyncOrigin != "remote" {
		t.Errorf("expected sync_origin=remote, got %q", m.SyncOrigin)
	}
	if m.SyncDirty {
		t.Error("expected sync_dirty cleared after marking remote")
	}
}
