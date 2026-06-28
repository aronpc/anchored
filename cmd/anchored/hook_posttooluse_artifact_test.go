package main

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestClassifyArtifact(t *testing.T) {
	cases := []struct {
		name   string
		tool   string
		input  string
		output string
		want   string
	}{
		{"go test fail", "Bash", "go test ./...", "--- FAIL: TestFoo\nFAIL\tpkg\t0.1s", "test_report"},
		{"npm test", "Bash", "npm test", "Tests: 3 passed, 1 failed", "test_report"},
		{"panic stack", "Bash", "./run", "panic: nil pointer\ngoroutine 1 [running]:", "stack_trace"},
		{"python traceback", "Bash", "python x.py", "Traceback (most recent call last):\n  File ...", "stack_trace"},
		{"go build fail", "Bash", "go build ./...", "build failed: undefined reference", "build_report"},
		{"git diff", "Bash", "git diff", "diff --git a/x b/x\n+added", "diff"},
		{"webfetch", "WebFetch", "https://example.com", "<html>...</html>", "external_doc"},
		{"plain command", "Bash", "ls -la", "total 40\ndrwxr-xr-x ...", "command_output"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyArtifact(c.tool, c.input, c.output); got != c.want {
				t.Errorf("classifyArtifact(%q,%q,...) = %q, want %q", c.tool, c.input, got, c.want)
			}
		})
	}
}

// TestPostToolUse_LargeOutputRoutedToArtifact verifies the size-based routing:
// a >8KB output is captured via the artifact sink (and acknowledged) rather
// than summarized into a session event.
func TestPostToolUse_LargeOutputRoutedToArtifact(t *testing.T) {
	db := newEventTestDB(t)

	var capturedType, capturedContent string
	sink := func(projectID, sessionID, artifactType, sourceTool, sourceLabel, content string) (string, error) {
		capturedType = artifactType
		capturedContent = content
		return "art-1", nil
	}

	big := strings.Repeat("--- FAIL: TestX\n", 1000) // >8KB, test output
	resp, _ := json.Marshal(big)
	inJSON := mustJSON(t, map[string]any{
		"session_id":    "s1",
		"tool_name":     "Bash",
		"tool_input":    map[string]any{"command": "go test ./..."},
		"tool_response": json.RawMessage(resp),
	})

	out := &strings.Builder{}
	recordPostToolUseEvent(PostToolUseDeps{
		Stdin:           strings.NewReader(inJSON),
		Stdout:          out,
		DB:              db,
		ResolveProject:  func(string) string { return "proj-A" },
		NewID:           func() string { return "evt-1" },
		Logger:          nilDebugLogger(),
		CaptureArtifact: sink,
	})

	if capturedType != "test_report" {
		t.Errorf("captured type = %q, want test_report", capturedType)
	}
	if len(capturedContent) < 8*1024 {
		t.Errorf("captured content too small: %d", len(capturedContent))
	}
	if !strings.Contains(out.String(), `"artifact_id":"art-1"`) {
		t.Errorf("response missing artifact_id: %s", out.String())
	}
	// A session event referencing the artifact was recorded.
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM session_events WHERE summary LIKE 'captured test_report%'`).Scan(&n)
	if n != 1 {
		t.Errorf("want 1 artifact-reference event, got %d", n)
	}
}

// TestPostToolUse_SmallOutputStaysEvent verifies a small output keeps the
// existing event-only path (no artifact capture).
func TestPostToolUse_SmallOutputStaysEvent(t *testing.T) {
	db := newEventTestDB(t)
	var captured bool
	sink := func(_, _, _, _, _, _ string) (string, error) { captured = true; return "x", nil }

	resp, _ := json.Marshal("ok, 3 files changed")
	inJSON := mustJSON(t, map[string]any{
		"session_id":    "s1",
		"tool_name":     "Bash",
		"tool_input":    map[string]any{"command": "ls"},
		"tool_response": json.RawMessage(resp),
	})
	out := &strings.Builder{}
	recordPostToolUseEvent(PostToolUseDeps{
		Stdin: strings.NewReader(inJSON), Stdout: out, DB: db,
		ResolveProject: func(string) string { return "" },
		NewID:          func() string { return "evt-2" },
		Logger:         nilDebugLogger(), CaptureArtifact: sink,
	})
	if captured {
		t.Error("small output must not be captured as an artifact")
	}
	if !strings.Contains(out.String(), `"recorded":true`) {
		t.Errorf("expected recorded event: %s", out.String())
	}
}

func newEventTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE session_events (
		id TEXT PRIMARY KEY, session_id TEXT, project_id TEXT, event_type TEXT,
		priority INTEGER, tool_name TEXT, summary TEXT, metadata TEXT, created_at DATETIME)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
