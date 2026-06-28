package eval

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/jholhewres/anchored/pkg/memory"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestEmbeddedFixturesGate runs the three evals against their embedded fixtures
// inside `make test`, so a regression in recall, privacy safety, or identity
// resolution fails the unit suite — not just the separate `make eval` target.
func TestSyncSafetyGate(t *testing.T) {
	data, err := DefaultFixture("privacy.yaml")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	rep, err := RunSyncSafety(data)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !rep.Passed {
		t.Fatalf("sync-safety eval failed: %s\n%+v", rep.Summary, rep.Cases)
	}
}

func TestIdentityGate(t *testing.T) {
	data, err := DefaultFixture("identity.yaml")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	rep, err := RunIdentity(data)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !rep.Passed {
		t.Fatalf("identity eval failed: %s\n%+v", rep.Summary, rep.Cases)
	}
}

func TestRecallGate(t *testing.T) {
	data, err := DefaultFixture("recall_basic.yaml")
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	store, err := memory.NewSQLiteStore(filepath.Join(t.TempDir(), "recall.db"), quiet())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	defer store.Close()

	rep, err := RunRecall(context.Background(), store, data)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !rep.Passed {
		t.Fatalf("recall eval failed: %s\n%+v", rep.Summary, rep.Cases)
	}
	if rep.Score < 0.8 {
		t.Fatalf("mean recall %.2f below floor 0.80", rep.Score)
	}
}
