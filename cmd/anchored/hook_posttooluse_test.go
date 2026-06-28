package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	ctxpkg "github.com/jholhewres/anchored/pkg/context"
	"github.com/jholhewres/anchored/pkg/debuglog"
	_ "modernc.org/sqlite"
)

// TestPostToolUseInsertSQL_AgainstSchema executes the exact INSERT statement
// the hook uses against an in-memory DB seeded with MigrationSQL +
// MigrationSQL009. Catches column-count / column-order regressions
// (the bug 0.4.5 fixed).
func TestPostToolUseInsertSQL_AgainstSchema(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(ctxpkg.MigrationSQL); err != nil {
		t.Fatalf("migration: %v", err)
	}
	if _, err := db.Exec(ctxpkg.MigrationSQL009); err != nil {
		t.Fatalf("migration 009: %v", err)
	}

	_, err = db.ExecContext(context.Background(), postToolUseInsertSQL,
		"event-1", "sess-A", "proj-X", "Bash", `{"stdout":"hi"}`, `{"cwd":"/tmp"}`,
	)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	var (
		eid, sid, pid, etype, tname, summary, metadata string
		priority                                       int
	)
	err = db.QueryRow(`SELECT id, session_id, project_id, event_type, priority, tool_name, summary, metadata
		FROM session_events WHERE id = ?`, "event-1").Scan(
		&eid, &sid, &pid, &etype, &priority, &tname, &summary, &metadata,
	)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if eid != "event-1" || sid != "sess-A" || pid != "proj-X" {
		t.Errorf("ids: got %q/%q/%q", eid, sid, pid)
	}
	if etype != "tool_call" || priority != 3 {
		t.Errorf("event_type/priority: %q/%d (want tool_call/3)", etype, priority)
	}
	if tname != "Bash" {
		t.Errorf("tool_name: %q", tname)
	}
	if summary != `{"stdout":"hi"}` {
		t.Errorf("summary: %q", summary)
	}
	if metadata != `{"cwd":"/tmp"}` {
		t.Errorf("metadata: %q", metadata)
	}
}

func TestSummarizeToolEvent_PrefersResponse(t *testing.T) {
	resp := json.RawMessage(`{"stdout":"hello\nworld","exit":0}`)
	in := json.RawMessage(`{"command":"echo hi"}`)
	got := summarizeToolEvent(resp, in, 500)
	if !strings.Contains(got, `"stdout":"hello\nworld"`) {
		t.Fatalf("response not preserved: %q", got)
	}
	if strings.Contains(got, "echo hi") {
		t.Fatalf("input should be ignored when response present: %q", got)
	}
}

func TestSummarizeToolEvent_FallsBackToInput(t *testing.T) {
	cases := []json.RawMessage{nil, json.RawMessage("null"), json.RawMessage("")}
	for _, resp := range cases {
		got := summarizeToolEvent(resp, json.RawMessage(`{"x":1}`), 500)
		if got != `{"x":1}` {
			t.Errorf("fallback failed for resp=%q, got %q", string(resp), got)
		}
	}
}

func TestSummarizeToolEvent_TruncatesAtMaxRunes(t *testing.T) {
	long := strings.Repeat("a", 1000)
	resp := json.RawMessage(`"` + long + `"`)
	got := summarizeToolEvent(resp, nil, 100)
	if utf8.RuneCountInString(got) != 100 {
		t.Fatalf("expected exact 100 runes, got %d", utf8.RuneCountInString(got))
	}
}

// TestSummarizeToolEvent_TruncateRespectsUTF8 guards against byte-level
// slicing in the middle of a multibyte sequence.
func TestSummarizeToolEvent_TruncateRespectsUTF8(t *testing.T) {
	// Each "ção é " is 8 bytes / 6 runes; 200 reps = 1600 bytes / 1200 runes.
	body := strings.Repeat("ção é ", 200)
	resp, _ := json.Marshal(body)
	got := summarizeToolEvent(json.RawMessage(resp), nil, 50)
	if !utf8.ValidString(got) {
		t.Fatalf("output is not valid UTF-8: %q", got)
	}
	if utf8.RuneCountInString(got) > 50 {
		t.Fatalf("rune count %d > 50", utf8.RuneCountInString(got))
	}
}

func TestSummarizeToolEvent_NormalizesWhitespace(t *testing.T) {
	pretty := json.RawMessage("{\n  \"k\": \"v\"\n}")
	got := summarizeToolEvent(pretty, nil, 500)
	if got != `{"k":"v"}` {
		t.Fatalf("compact should drop whitespace, got %q", got)
	}
}

// TestSummarizeToolEvent_PreservesKeyOrderAndNumbers is the regression test
// for the v0.4.5 review: Marshal(Unmarshal()) reordered keys and lossily
// converted big integers to float64. json.Compact does neither.
func TestSummarizeToolEvent_PreservesKeyOrderAndNumbers(t *testing.T) {
	in := json.RawMessage(`{"stdout":"x","stderr":"y","exit":0}`)
	if got := summarizeToolEvent(in, nil, 500); got != `{"stdout":"x","stderr":"y","exit":0}` {
		t.Fatalf("key order changed or compaction broken: %q", got)
	}

	bigInt := json.RawMessage(`{"id":12345678901234567890}`)
	got := summarizeToolEvent(bigInt, nil, 500)
	if !strings.Contains(got, "12345678901234567890") {
		t.Fatalf("large integer lost precision: %q", got)
	}
}

func TestBuildPostToolUseMetadata(t *testing.T) {
	got := buildPostToolUseMetadata("/tmp/x", "PostToolUse", 42)
	var parsed map[string]any
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("metadata not valid JSON: %v — %q", err, got)
	}
	if parsed["cwd"] != "/tmp/x" {
		t.Errorf("cwd: %v", parsed["cwd"])
	}
	if parsed["hook_event_name"] != "PostToolUse" {
		t.Errorf("hook_event_name: %v", parsed["hook_event_name"])
	}
	if parsed["raw_length"].(float64) != 42 {
		t.Errorf("raw_length: %v", parsed["raw_length"])
	}
}

func TestBuildPostToolUseMetadata_TruncatesAt1KB(t *testing.T) {
	huge := strings.Repeat("x", 5000)
	got := buildPostToolUseMetadata(huge, "PostToolUse", 0)
	if len(got) > 1024 {
		t.Fatalf("metadata > 1024 bytes: %d", len(got))
	}
}

// TestRecordPostToolUseEvent_EndToEnd feeds a realistic Claude Code stdin
// payload through the full record pipeline against an in-memory sqlite DB
// and verifies the resulting row plus the JSON response line.
func TestRecordPostToolUseEvent_EndToEnd(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(ctxpkg.MigrationSQL); err != nil {
		t.Fatalf("migration: %v", err)
	}
	if _, err := db.Exec(ctxpkg.MigrationSQL009); err != nil {
		t.Fatalf("migration 009: %v", err)
	}

	stdin := strings.NewReader(`{
		"session_id":      "sess-A",
		"hook_event_name": "PostToolUse",
		"cwd":             "/repo",
		"tool_name":       "Bash",
		"tool_input":      {"command": "echo hi"},
		"tool_response":   {"stdout": "hi\n", "exit": 0}
	}`)
	var stdout strings.Builder

	deps := PostToolUseDeps{
		Stdin:          stdin,
		Stdout:         &stdout,
		DB:             db,
		ResolveProject: func(cwd string) string { return "proj-X" },
		NewID:          func() string { return "evt-1" },
		Logger:         nilDebugLogger(),
	}
	recordPostToolUseEvent(deps)

	if !strings.Contains(stdout.String(), `"recorded":true`) {
		t.Fatalf("expected recorded=true, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), `"event_id":"evt-1"`) {
		t.Fatalf("expected event_id=evt-1, got %q", stdout.String())
	}

	var (
		sid, pid, etype, tname, summary string
		priority                        int
	)
	err = db.QueryRow(`SELECT session_id, project_id, event_type, priority, tool_name, summary
		FROM session_events WHERE id = ?`, "evt-1").Scan(
		&sid, &pid, &etype, &priority, &tname, &summary,
	)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if sid != "sess-A" || pid != "proj-X" || etype != "tool_call" || priority != 3 || tname != "Bash" {
		t.Errorf("row mismatch: sid=%q pid=%q etype=%q pri=%d tool=%q", sid, pid, etype, priority, tname)
	}
	if !strings.Contains(summary, `"stdout":"hi\n"`) {
		t.Errorf("summary missing stdout: %q", summary)
	}
}

// TestRecordPostToolUseEvent_FeedsWorkingSet asserts a Write tool call feeds
// the touched file into the working set via the UpdateWorkingSet dep.
func TestRecordPostToolUseEvent_FeedsWorkingSet(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(ctxpkg.MigrationSQL); err != nil {
		t.Fatalf("migration: %v", err)
	}
	if _, err := db.Exec(ctxpkg.MigrationSQL009); err != nil {
		t.Fatalf("migration 009: %v", err)
	}

	stdin := strings.NewReader(`{
		"session_id":      "sess-WS",
		"hook_event_name": "PostToolUse",
		"cwd":             "/repo",
		"tool_name":       "Write",
		"tool_input":      {"file_path": "pkg/sync/client.go", "content": "package sync"},
		"tool_response":   {"ok": true}
	}`)
	var stdout strings.Builder

	var gotSession, gotProject string
	var gotFiles, gotCommands, gotTests []string
	deps := PostToolUseDeps{
		Stdin:          stdin,
		Stdout:         &stdout,
		DB:             db,
		ResolveProject: func(cwd string) string { return "proj-WS" },
		NewID:          func() string { return "evt-ws" },
		Logger:         nilDebugLogger(),
		UpdateWorkingSet: func(sessionID, projectID string, files, commands, tests []string) error {
			gotSession, gotProject = sessionID, projectID
			gotFiles, gotCommands, gotTests = files, commands, tests
			return nil
		},
	}
	recordPostToolUseEvent(deps)

	if gotSession != "sess-WS" || gotProject != "proj-WS" {
		t.Fatalf("working set wiring: session=%q project=%q", gotSession, gotProject)
	}
	if len(gotFiles) != 1 || gotFiles[0] != "pkg/sync/client.go" {
		t.Fatalf("expected the written file in working set, got %v", gotFiles)
	}
	if len(gotCommands) != 0 || len(gotTests) != 0 {
		t.Fatalf("Write should not produce commands/tests: cmds=%v tests=%v", gotCommands, gotTests)
	}
}

func TestWorkingSetDelta(t *testing.T) {
	cases := []struct {
		name, tool, input        string
		wantFiles, wantCmd, wantTest int
	}{
		{"write file", "Write", `{"file_path":"a/b.go"}`, 1, 0, 0},
		{"edit file", "Edit", `{"file_path":"a/b.go"}`, 1, 0, 0},
		{"bash test", "Bash", `{"command":"go test ./pkg/sync/"}`, 0, 0, 1},
		{"bash command", "Bash", `{"command":"git status"}`, 0, 1, 0},
		{"empty input", "Bash", ``, 0, 0, 0},
		{"unknown tool", "WebSearch", `{"query":"x"}`, 0, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, cmds, tests := workingSetDelta(tc.tool, tc.input)
			if len(files) != tc.wantFiles || len(cmds) != tc.wantCmd || len(tests) != tc.wantTest {
				t.Fatalf("delta(%s,%s) = files%v cmds%v tests%v", tc.tool, tc.input, files, cmds, tests)
			}
		})
	}
}

// TestRecordPostToolUseEvent_MissingSessionID asserts the graceful "no row"
// path when neither stdin nor flag carry a session_id.
func TestRecordPostToolUseEvent_MissingSessionID(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(ctxpkg.MigrationSQL); err != nil {
		t.Fatalf("migration: %v", err)
	}
	if _, err := db.Exec(ctxpkg.MigrationSQL009); err != nil {
		t.Fatalf("migration 009: %v", err)
	}

	stdin := strings.NewReader(`{"tool_name":"Read"}`) // no session_id
	var stdout strings.Builder

	recordPostToolUseEvent(PostToolUseDeps{
		Stdin:          stdin,
		Stdout:         &stdout,
		DB:             db,
		ResolveProject: func(string) string { return "" },
		NewID:          func() string { return "evt-1" },
		Logger:         nilDebugLogger(),
	})

	if !strings.Contains(stdout.String(), `"recorded":false`) {
		t.Fatalf("expected recorded=false, got %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "missing session_id") {
		t.Fatalf("expected reason missing_session_id, got %q", stdout.String())
	}

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM session_events").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("no row should be inserted when session_id is missing, got %d", n)
	}
}

// nilDebugLogger returns a *debuglog.Logger whose Event/Close are safe to
// call without writing anywhere — debug logging is opt-in and tests don't
// need to assert on emitted events.
func nilDebugLogger() *debuglog.Logger {
	return &debuglog.Logger{}
}

func TestNewHookID_HexLength(t *testing.T) {
	id := newHookID()
	if len(id) != 32 {
		t.Fatalf("expected 32-char hex id, got %d (%q)", len(id), id)
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("non-hex char in id: %q", id)
		}
	}
}
