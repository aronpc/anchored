package session

import (
	"context"
	"testing"
)

func TestWorkingSet_GetEmpty(t *testing.T) {
	db := setupTestDB(t)
	m := NewManager(db, nil)
	ws, err := m.GetWorkingSet(context.Background(), "no-such-session")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ws != nil {
		t.Fatalf("want nil for missing session, got %+v", ws)
	}
	if !ws.Empty() {
		t.Fatal("nil working set must report Empty")
	}
}

func TestWorkingSet_MergeDedupeRecencyCap(t *testing.T) {
	db := setupTestDB(t)
	m := NewManager(db, nil)
	ctx := context.Background()
	sid := "s1"

	if _, err := m.UpdateWorkingSet(ctx, sid, WorkingSetDelta{
		ProjectID: "p1",
		Files:     []string{"pkg/sync/client.go", "pkg/sync/engine.go"},
		Tests:     []string{"go test ./pkg/sync/"},
	}); err != nil {
		t.Fatalf("update 1: %v", err)
	}
	// Second update: one repeat (different case) + one new; newest goes first.
	ws, err := m.UpdateWorkingSet(ctx, sid, WorkingSetDelta{
		Files: []string{"pkg/sync/CLIENT.go", "cmd/anchored/hook.go"},
	})
	if err != nil {
		t.Fatalf("update 2: %v", err)
	}

	if len(ws.Files) != 3 {
		t.Fatalf("want 3 deduped files, got %v", ws.Files)
	}
	if ws.Files[0] != "pkg/sync/CLIENT.go" {
		t.Fatalf("newest file must be first, got %v", ws.Files)
	}
	// case-insensitive dedupe keeps the first-seen form for the older entry.
	for _, f := range ws.Files {
		if f == "pkg/sync/client.go" {
			t.Fatalf("case-duplicate should have been collapsed: %v", ws.Files)
		}
	}
	if len(ws.Tests) != 1 {
		t.Fatalf("tests should persist across updates, got %v", ws.Tests)
	}

	// Round-trips through the DB.
	got, err := m.GetWorkingSet(ctx, sid)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Files) != 3 || got.ProjectID != "p1" {
		t.Fatalf("reload mismatch: %+v", got)
	}
}

func TestWorkingSet_Cap(t *testing.T) {
	db := setupTestDB(t)
	m := NewManager(db, nil)
	ctx := context.Background()
	many := make([]string, workingSetMaxItems+20)
	for i := range many {
		many[i] = "file" + itoaWS(i) + ".go"
	}
	ws, err := m.UpdateWorkingSet(ctx, "s", WorkingSetDelta{Files: many})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(ws.Files) != workingSetMaxItems {
		t.Fatalf("files not capped: got %d want %d", len(ws.Files), workingSetMaxItems)
	}
}

func TestWorkingSet_TopicShiftResetOnProjectChange(t *testing.T) {
	db := setupTestDB(t)
	m := NewManager(db, nil)
	ctx := context.Background()
	sid := "s1"

	if _, err := m.UpdateWorkingSet(ctx, sid, WorkingSetDelta{
		ProjectID: "proj-a",
		Files:     []string{"a/one.go", "a/two.go"},
	}); err != nil {
		t.Fatalf("update a: %v", err)
	}
	// Switching to a different project resets the prior focus.
	ws, err := m.UpdateWorkingSet(ctx, sid, WorkingSetDelta{
		ProjectID: "proj-b",
		Files:     []string{"b/main.go"},
	})
	if err != nil {
		t.Fatalf("update b: %v", err)
	}
	if ws.ProjectID != "proj-b" {
		t.Fatalf("project not updated: %+v", ws)
	}
	if len(ws.Files) != 1 || ws.Files[0] != "b/main.go" {
		t.Fatalf("topic shift must drop project-a files, got %v", ws.Files)
	}
}

func itoaWS(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
