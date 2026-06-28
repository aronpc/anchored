package main

import (
	"context"
	"testing"
	"time"

	"github.com/jholhewres/anchored/pkg/memory"
)

// markStale rewrites a memory's metadata to look like it was scored by an older
// scorer formula, so the curation worker treats it as a re-curation candidate.
func markStale(t *testing.T, svc *memory.Service, id string) {
	t.Helper()
	meta := memory.MemoryMetadata{ScorerVersion: memory.QualityScorerVersion - 1, CurationStatus: memory.CurationStatusLowSignal}
	if err := svc.UpdateMetadata(context.Background(), id, meta.ToAny()); err != nil {
		t.Fatalf("mark stale: %v", err)
	}
}

// seedMemories saves n high-signal memories and returns their IDs read back
// from the store. The IDs are fetched via SQL because Service.Save assigns the
// UUID to a by-value copy, so the returned struct's ID is empty.
func seedMemories(t *testing.T, svc *memory.Service, n int) []string {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		content := "A sufficiently long and meaningful project decision number " +
			string(rune('A'+i)) + " describing why we curate memories with a versioned scorer."
		if _, err := svc.Save(ctx, content, "decision", "test", ""); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	rows, err := svc.StoreDB().QueryContext(ctx,
		`SELECT id FROM memories WHERE deleted_at IS NULL ORDER BY created_at ASC`)
	if err != nil {
		t.Fatalf("list ids: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan id: %v", err)
		}
		ids = append(ids, id)
	}
	if len(ids) < n {
		t.Fatalf("seeded %d memories, found %d", n, len(ids))
	}
	return ids
}

func TestCurationDue(t *testing.T) {
	cfgPath := newTestEnv(t)
	_, _, svc, err := initService(cfgPath)
	if err != nil {
		t.Fatalf("initService: %v", err)
	}
	defer svc.Close()

	ctx := context.Background()
	db := svc.StoreDB()
	now := time.Now().UTC()

	if !curationDue(ctx, db, time.Hour, now, nil) {
		t.Fatal("expected due=true when never run")
	}
	if err := setCurationLastRun(ctx, db, now); err != nil {
		t.Fatalf("setCurationLastRun: %v", err)
	}
	if curationDue(ctx, db, time.Hour, now.Add(30*time.Minute), nil) {
		t.Fatal("expected due=false within interval")
	}
	if !curationDue(ctx, db, time.Hour, now.Add(2*time.Hour), nil) {
		t.Fatal("expected due=true after interval")
	}
}

func TestCurationWorkerPass_StampsAndDrains(t *testing.T) {
	cfgPath := newTestEnv(t)
	_, _, svc, err := initService(cfgPath)
	if err != nil {
		t.Fatalf("initService: %v", err)
	}
	defer svc.Close()

	ctx := context.Background()
	ids := seedMemories(t, svc, 3)
	for _, id := range ids {
		markStale(t, svc, id)
	}

	updated, scanned, err := runCurationMetadataPass(ctx, svc, memory.RemoteQualityThreshold, 500)
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if updated != 3 {
		t.Fatalf("updated = %d, want 3", updated)
	}
	if scanned < 3 {
		t.Fatalf("scanned = %d, want >= 3", scanned)
	}

	// All should now carry the current scorer version and have stale low_signal
	// flags cleared (the seeded content is high-signal).
	for _, id := range ids {
		m, err := svc.Get(ctx, id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		meta := memory.ParseMetadata(m.Metadata)
		if meta.ScorerVersion != memory.QualityScorerVersion {
			t.Fatalf("id %s scorer_version = %d, want %d", id, meta.ScorerVersion, memory.QualityScorerVersion)
		}
		if meta.CurationStatus == memory.CurationStatusLowSignal {
			t.Fatalf("id %s still low_signal; stale flag not repaired", id)
		}
	}

	// Second pass must be a no-op: the corpus is fully drained.
	updated2, _, err := runCurationMetadataPass(ctx, svc, memory.RemoteQualityThreshold, 500)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if updated2 != 0 {
		t.Fatalf("second pass updated = %d, want 0 (drained)", updated2)
	}
}

func TestCurationWorkerPass_RespectsMaxUpdates(t *testing.T) {
	cfgPath := newTestEnv(t)
	_, _, svc, err := initService(cfgPath)
	if err != nil {
		t.Fatalf("initService: %v", err)
	}
	defer svc.Close()

	ctx := context.Background()
	ids := seedMemories(t, svc, 4)
	for _, id := range ids {
		markStale(t, svc, id)
	}

	updated, _, err := runCurationMetadataPass(ctx, svc, memory.RemoteQualityThreshold, 2)
	if err != nil {
		t.Fatalf("pass: %v", err)
	}
	if updated != 2 {
		t.Fatalf("updated = %d, want 2 (max_updates cap)", updated)
	}
}

func TestListRecentCurationCandidates_SkipsCurrentVersion(t *testing.T) {
	cfgPath := newTestEnv(t)
	_, _, svc, err := initService(cfgPath)
	if err != nil {
		t.Fatalf("initService: %v", err)
	}
	defer svc.Close()

	ctx := context.Background()
	ids := seedMemories(t, svc, 2) // saved at current scorer version
	markStale(t, svc, ids[0])      // only this one becomes stale

	cands, err := listRecentCurationCandidates(ctx, svc.StoreDB(), 100)
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(cands) != 1 || cands[0].ID != ids[0] {
		t.Fatalf("candidates = %d (%v), want exactly the stale one %s", len(cands), idsOf(cands), ids[0])
	}
}

func TestCurationBootstrap_DrainsBacklogOnce(t *testing.T) {
	cfgPath := newTestEnv(t)
	_, _, svc, err := initService(cfgPath)
	if err != nil {
		t.Fatalf("initService: %v", err)
	}
	defer svc.Close()

	ctx := context.Background()
	ids := seedMemories(t, svc, 5)
	for _, id := range ids {
		markStale(t, svc, id)
	}

	// First bootstrap: drains the whole backlog and records the version.
	curationBootstrap(ctx, svc, memory.RemoteQualityThreshold, nil)

	v, err := getCurationReconciledVersion(ctx, svc.StoreDB())
	if err != nil {
		t.Fatalf("read reconciled version: %v", err)
	}
	if v != memory.QualityScorerVersion {
		t.Fatalf("reconciled_version = %d, want %d", v, memory.QualityScorerVersion)
	}
	cands, _ := listRecentCurationCandidates(ctx, svc.StoreDB(), 100)
	if len(cands) != 0 {
		t.Fatalf("after bootstrap, candidates = %d, want 0", len(cands))
	}

	// Second bootstrap is a no-op (version already current): re-staling a memory
	// must NOT be picked up by bootstrap (only by the incremental ticker).
	markStale(t, svc, ids[0])
	curationBootstrap(ctx, svc, memory.RemoteQualityThreshold, nil)
	m, _ := svc.Get(ctx, ids[0])
	if memory.ParseMetadata(m.Metadata).ScorerVersion == memory.QualityScorerVersion {
		t.Fatal("bootstrap should not re-run once reconciled_version is current")
	}
}

func idsOf(ms []memory.Memory) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}
