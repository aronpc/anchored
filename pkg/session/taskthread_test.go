package session

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func newTaskTestDB(t *testing.T) *Manager {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE task_threads (
			task_key TEXT PRIMARY KEY, external_ref TEXT NOT NULL DEFAULT '',
			project_ids TEXT NOT NULL DEFAULT '[]', journal TEXT NOT NULL DEFAULT '[]',
			session_ids TEXT NOT NULL DEFAULT '[]', status TEXT NOT NULL DEFAULT 'active',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	return NewManager(db, nil)
}

func TestInferTaskKey(t *testing.T) {
	cases := map[string]string{
		"feature/PROJ-123-fix-login":  "PROJ-123",
		"feature/proj-123-fix":        "PROJ-123",
		"ASCP-3460":                   "ASCP-3460",
		"bugfix/led-52968-middleware": "LED-52968",
		"main":                        "",
		"develop":                     "",
		"":                            "",
		"release/v1.2.3":              "",
		"hotfix/v1-2":                 "", // version fragment, not a ticket
		"bugfix/go1-21-patch":         "", // Go version, not a ticket
		"feature/i18n-42-strings":     "", // digit inside prefix — not a ticket
	}
	for in, want := range cases {
		if got := InferTaskKey(in); got != want {
			t.Errorf("InferTaskKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTaskThread_UpsertMergesAcrossProjects(t *testing.T) {
	mgr := newTaskTestDB(t)
	ctx := context.Background()

	th, err := mgr.UpsertTaskThread(ctx, "proj-9", TaskThreadDelta{ProjectID: "repoA", SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if th.TaskKey != "PROJ-9" || th.Status != TaskStatusActive {
		t.Fatalf("unexpected thread: %+v", th)
	}
	// Same task touched from another repo: merge, no duplicate.
	th, err = mgr.UpsertTaskThread(ctx, "PROJ-9", TaskThreadDelta{ProjectID: "repoB", SessionID: "s2", JournalNote: "decided X"})
	if err != nil {
		t.Fatal(err)
	}
	if len(th.ProjectIDs) != 2 || len(th.SessionIDs) != 2 || len(th.Journal) != 1 {
		t.Fatalf("merge failed: %+v", th)
	}
	th, _ = mgr.UpsertTaskThread(ctx, "PROJ-9", TaskThreadDelta{ProjectID: "repoA"})
	if len(th.ProjectIDs) != 2 {
		t.Fatalf("dedupe failed: %+v", th.ProjectIDs)
	}
}

func TestTaskThread_LifecycleAndTerminalGuard(t *testing.T) {
	mgr := newTaskTestDB(t)
	ctx := context.Background()
	if _, err := mgr.UpsertTaskThread(ctx, "ABC-1", TaskThreadDelta{ProjectID: "p1"}); err != nil {
		t.Fatal(err)
	}

	if err := mgr.SetTaskStatus(ctx, "abc-1", TaskStatusPaused); err != nil {
		t.Fatal(err)
	}
	// Automation touching a paused thread reactivates it.
	th, _ := mgr.UpsertTaskThread(ctx, "ABC-1", TaskThreadDelta{SessionID: "s9"})
	if th.Status != TaskStatusActive {
		t.Fatalf("paused thread should reactivate on touch, got %s", th.Status)
	}

	if err := mgr.SetTaskStatus(ctx, "ABC-1", TaskStatusDone); err != nil {
		t.Fatal(err)
	}
	// Terminal threads are never silently reopened by automation.
	th, _ = mgr.UpsertTaskThread(ctx, "ABC-1", TaskThreadDelta{ProjectID: "p2", SessionID: "s10"})
	if th.Status != TaskStatusDone {
		t.Fatalf("done thread must stay done on automation touch, got %s", th.Status)
	}
	if len(th.ProjectIDs) != 1 {
		t.Fatalf("done thread must not absorb new projects, got %+v", th.ProjectIDs)
	}

	if err := mgr.SetTaskStatus(ctx, "ABC-1", "weird"); err == nil {
		t.Fatal("invalid status must error")
	}
	if err := mgr.SetTaskStatus(ctx, "NOPE-1", TaskStatusPaused); err == nil {
		t.Fatal("unknown key must error")
	}
}
