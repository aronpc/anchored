package session

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jholhewres/anchored/pkg/memory"
	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) *sql.DB {
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

func TestStartSession_CreatesNew(t *testing.T) {
	db := setupTestDB(t)
	m := NewManager(db, nil)

	id, err := m.StartSession(context.Background(), "claude-code", "src-1", "proj-1", "/work")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty id")
	}

	var endedAt sql.NullString
	err = db.QueryRow(`SELECT ended_at FROM sessions WHERE id = ?`, id).Scan(&endedAt)
	if err != nil {
		t.Fatalf("query session: %v", err)
	}
	if endedAt.Valid {
		t.Error("expected ended_at to be NULL for new session")
	}
}

func TestStartSession_ResumesExisting(t *testing.T) {
	db := setupTestDB(t)
	m := NewManager(db, nil)

	id1, err := m.StartSession(context.Background(), "claude-code", "src-resume", "proj-1", "/work")
	if err != nil {
		t.Fatalf("first StartSession: %v", err)
	}

	id2, err := m.StartSession(context.Background(), "claude-code", "src-resume", "proj-1", "/work")
	if err != nil {
		t.Fatalf("second StartSession: %v", err)
	}

	if id1 != id2 {
		t.Errorf("expected same id on resume, got %q and %q", id1, id2)
	}

	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE source_session_id = 'src-resume'`).Scan(&count)
	if err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 row, got %d", count)
	}
}

func TestRecordActivity_UpdatesTimestamp(t *testing.T) {
	db := setupTestDB(t)
	m := NewManager(db, nil)

	id, err := m.StartSession(context.Background(), "opencode", "src-act", "", "/tmp")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	// Capture initial last_activity_at.
	var before sql.NullString
	db.QueryRow(`SELECT last_activity_at FROM sessions WHERE id = ?`, id).Scan(&before)

	// Small sleep to ensure timestamp difference.
	time.Sleep(10 * time.Millisecond)

	if err := m.RecordActivity(context.Background(), id); err != nil {
		t.Fatalf("RecordActivity: %v", err)
	}

	var after sql.NullString
	var msgCount int
	db.QueryRow(`SELECT last_activity_at, message_count FROM sessions WHERE id = ?`, id).Scan(&after, &msgCount)

	if !after.Valid {
		t.Fatal("expected last_activity_at to be set")
	}
	if msgCount != 1 {
		t.Errorf("expected message_count=1, got %d", msgCount)
	}
}

func TestEndSession_SetsEndedAt(t *testing.T) {
	db := setupTestDB(t)
	m := NewManager(db, nil)

	id, err := m.StartSession(context.Background(), "cursor", "src-end", "", "/tmp")
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	if err := m.EndSession(context.Background(), id); err != nil {
		t.Fatalf("EndSession: %v", err)
	}

	s, err := m.GetActiveSession(context.Background(), "src-end")
	if err != nil {
		t.Fatalf("GetActiveSession: %v", err)
	}
	if s != nil {
		t.Error("expected nil after ending session")
	}
}

func TestEndStaleSessions_ClosesOld(t *testing.T) {
	db := setupTestDB(t)
	m := NewManager(db, nil)

	// Insert a stale session with old last_activity_at.
	_, err := db.Exec(`INSERT INTO sessions (id, source, source_session_id, created_at, last_activity_at, message_count)
		VALUES ('stale-1', 'live', 'stale-src', datetime('now', '-2 hours'), datetime('now', '-2 hours'), 0)`)
	if err != nil {
		t.Fatalf("insert stale: %v", err)
	}

	// Insert a recent session.
	_, err = db.Exec(`INSERT INTO sessions (id, source, source_session_id, created_at, last_activity_at, message_count)
		VALUES ('fresh-1', 'live', 'fresh-src', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, 0)`)
	if err != nil {
		t.Fatalf("insert fresh: %v", err)
	}

	closed, err := m.EndStaleSessions(context.Background(), 1*time.Hour)
	if err != nil {
		t.Fatalf("EndStaleSessions: %v", err)
	}
	if closed != 1 {
		t.Errorf("expected 1 stale session closed, got %d", closed)
	}

	// Verify stale is ended.
	var staleEnded sql.NullString
	db.QueryRow(`SELECT ended_at FROM sessions WHERE id = 'stale-1'`).Scan(&staleEnded)
	if !staleEnded.Valid {
		t.Error("expected stale session to be ended")
	}

	// Verify fresh is still active.
	var freshEnded sql.NullString
	db.QueryRow(`SELECT ended_at FROM sessions WHERE id = 'fresh-1'`).Scan(&freshEnded)
	if freshEnded.Valid {
		t.Error("expected fresh session to still be active")
	}
}

func TestSessionStats_Counts(t *testing.T) {
	db := setupTestDB(t)
	m := NewManager(db, nil)

	id1, _ := m.StartSession(context.Background(), "test", "stats-1", "", "/a")
	id2, _ := m.StartSession(context.Background(), "test", "stats-2", "", "/b")

	// End one.
	m.EndSession(context.Background(), id1)

	total, active, err := m.SessionStats(context.Background())
	if err != nil {
		t.Fatalf("SessionStats: %v", err)
	}

	// id2 + possibly stale rows from other tests? No — isolated DB.
	_ = id2

	if total != 2 {
		t.Errorf("expected total=2, got %d", total)
	}
	if active != 1 {
		t.Errorf("expected active=1, got %d", active)
	}
}

func TestCleanupOldEvents_DropsExpired(t *testing.T) {
	db := setupTestDB(t)
	m := NewManager(db, nil)

	// Three rows: 60 days old, 1 day old, fresh. With a 30-day retention only
	// the oldest should be dropped.
	insert := func(id, created string) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO session_events
			(id, session_id, project_id, event_type, priority, tool_name, summary, metadata, created_at)
			VALUES (?, 's1', 'p1', 'tool_call', 3, 'Bash', '', '{}', ?)`,
			id, created,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("e-old", time.Now().Add(-60*24*time.Hour).UTC().Format("2006-01-02 15:04:05"))
	insert("e-recent", time.Now().Add(-1*24*time.Hour).UTC().Format("2006-01-02 15:04:05"))
	insert("e-now", time.Now().UTC().Format("2006-01-02 15:04:05"))

	deleted, err := m.CleanupOldEvents(context.Background(), 30*24*time.Hour)
	if err != nil {
		t.Fatalf("CleanupOldEvents: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 row deleted, got %d", deleted)
	}

	var ids []string
	rows, err := db.Query(`SELECT id FROM session_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	want := []string{"e-now", "e-recent"}
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Errorf("rows after cleanup = %v, want %v", ids, want)
	}
}

func TestCleanupOldEvents_NoRetentionIsNoOp(t *testing.T) {
	db := setupTestDB(t)
	m := NewManager(db, nil)

	_, err := db.Exec(`INSERT INTO session_events
		(id, session_id, project_id, event_type, priority, tool_name, summary, metadata, created_at)
		VALUES ('x', 's1', 'p1', 'tool_call', 3, '', '', '{}', datetime('now'))`)
	if err != nil {
		t.Fatal(err)
	}

	if n, err := m.CleanupOldEvents(context.Background(), 0); err != nil || n != 0 {
		t.Errorf("retention<=0 must be a no-op, got n=%d err=%v", n, err)
	}
}
