package mcp

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/kg"
	"github.com/jholhewres/anchored/pkg/memory"
	"github.com/jholhewres/anchored/pkg/session"
)

// newTestServer builds a fully-wired Server against a throwaway on-disk SQLite
// DB with embeddings disabled (provider "none"), so tests stay offline and fast.
// KG and session.Manager are attached so kg/session tool handlers are exercised
// instead of short-circuiting on nil.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Memory:    config.MemoryConfig{DatabasePath: filepath.Join(dir, "t.db"), StorageDir: dir},
		Embedding: config.EmbeddingConfig{Provider: "none"},
	}
	if err := config.EnsureDirs(cfg); err != nil {
		t.Fatalf("ensure dirs: %v", err)
	}
	log := slog.New(slog.NewTextHandler(&discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
	memSvc, err := memory.NewService(cfg, log)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	t.Cleanup(memSvc.Close)

	kgSvc := kg.New(memSvc.StoreDB(), log)
	memSvc.SetKGExtractor(kg.NewPatternExtractor(kgSvc, log))
	sessMgr := session.NewManager(memSvc.StoreDB(), log)

	// Best-effort context optimizer (same pattern as serve.go): if it fails to
	// initialize in the test environment, the ctx_* tools stay unavailable but
	// the rest of the server is exercised. On Linux the sandbox compiles, so
	// this wires the optimizer and covers server_ctx.go wrapper methods.
	var optimizer OptimizerFacade
	cfg.ContextOptimizer = config.ContextOptimizerConfig{Enabled: true}
	if opt, err := NewCtxOptimizer(memSvc.StoreDB(), cfg.ContextOptimizer, log); err == nil {
		optimizer = opt
		t.Cleanup(opt.Close)
	}

	return NewServer(memSvc, kgSvc, sessMgr, optimizer, cfg, "test", log)
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func callToolJSON(t *testing.T, s *Server, tool string, args any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return s.callTool(context.Background(), tool, raw)
}

func TestServer_ToolSaveListForgetUpdate(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	_ = ctx

	// Save a fact — the response is "Saved [<category>] memory <ID>".
	res, err := callToolJSON(t, s, "anchored_save", map[string]any{
		"content":  "Anchored stores memories in SQLite with FTS5.",
		"category": "fact",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	id := extractSaveID(res)
	if id == "" {
		t.Fatalf("could not extract memory id from save response: %s", res)
	}

	// List should now include the saved memory.
	if _, err := callToolJSON(t, s, "anchored_list", map[string]any{
		"category": "fact",
		"limit":    5,
	}); err != nil {
		t.Fatalf("list: %v", err)
	}

	// Update its content.
	if _, err := callToolJSON(t, s, "anchored_update", map[string]any{
		"id":      id,
		"content": "Anchored stores memories in SQLite with FTS5 and ONNX vectors.",
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Forget (soft-delete) it.
	if _, err := callToolJSON(t, s, "anchored_forget", map[string]any{"id": id}); err != nil {
		t.Fatalf("forget: %v", err)
	}
}

func TestServer_ToolStatsAndKG(t *testing.T) {
	s := newTestServer(t)

	// Stats should return a numeric/structured payload, not error on nil mem.
	if _, err := callToolJSON(t, s, "anchored_stats", map[string]any{}); err != nil {
		t.Fatalf("stats: %v", err)
	}

	// KG add then query a triple.
	if _, err := callToolJSON(t, s, "anchored_kg_add", map[string]any{
		"subject":   "anchored",
		"predicate": "uses",
		"object":    "sqlite",
	}); err != nil {
		t.Fatalf("kg_add: %v", err)
	}
	if _, err := callToolJSON(t, s, "anchored_kg_query", map[string]any{
		"entity": "anchored",
	}); err != nil {
		t.Fatalf("kg_query: %v", err)
	}
}

func TestServer_ToolSessionEndAndSearch(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	// Start a live session so EndSession has something to close.
	sid, err := s.sessions.StartSession(ctx, "test", "sess-1", "", ".")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	// session_end with a real session id and a summary exercises the summary-save path.
	res, err := callToolJSON(t, s, "anchored_session_end", map[string]any{
		"session_id": sid,
		"summary":    "Tested the MCP handler integration suite.",
	})
	if err != nil {
		t.Fatalf("session_end: %v", err)
	}
	if !contains(res, "ended") {
		t.Fatalf("expected 'ended' in response, got: %s", res)
	}

	// session_end without a session_id should error before touching the manager.
	if _, err := callToolJSON(t, s, "anchored_session_end", map[string]any{}); err == nil {
		t.Fatal("expected error when session_id is missing")
	}

	// Save a memory then search for it — exercises the search render path.
	if _, err := callToolJSON(t, s, "anchored_save", map[string]any{
		"content":  "FTS5 powers the hybrid search in anchored.",
		"category": "fact",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := callToolJSON(t, s, "anchored_search", map[string]any{
		"query":       "hybrid search",
		"max_results": 3,
	}); err != nil {
		t.Fatalf("search: %v", err)
	}
}

func TestServer_ToolContext(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	// Start a live session so RecordActivity runs inside toolContext.
	sid, err := s.sessions.StartSession(ctx, "test", "ctx-sess", "", ".")
	if err != nil {
		t.Fatalf("start session: %v", err)
	}

	// Populate the working set so renderWorkingSet has focus signals to emit.
	if _, err := s.sessions.UpdateWorkingSet(ctx, sid, session.WorkingSetDelta{
		Files:    []string{"main.go", "server.go"},
		Symbols:  []string{"NewServer"},
		Commands: []string{"make test"},
		Errors:   []string{"undefined: foo"},
	}); err != nil {
		t.Fatalf("update working set: %v", err)
	}

	// Save a memory so the context bundle has recent memories + stats to render.
	if _, err := callToolJSON(t, s, "anchored_save", map[string]any{
		"content":  "The context bundle fuses identity, project meta, stats, and recent memories.",
		"category": "fact",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	// anchored_context fans out four parallel reads (identity, project meta,
	// project stats, recent memories + session events + KG edges) then renders
	// the working set. Exercising it covers toolContext, renderWorkingSet,
	// readIdentityFile, lookupProjectMeta, projectScopedStats, recentSessionEvents.
	res, err := callToolJSON(t, s, "anchored_context", map[string]any{
		"cwd":        ".",
		"session_id": sid,
	})
	if err != nil {
		t.Fatalf("context: %v", err)
	}
	if res == "" {
		t.Fatal("expected non-empty context bundle")
	}

	// Invalid JSON args should fall back to CWD="." rather than erroring.
	if _, err := callToolJSON(t, s, "anchored_context", map[string]any{}); err != nil {
		t.Fatalf("context with empty args: %v", err)
	}
}

func TestServer_Resources(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	// resources/list returns the static resource definitions.
	out := s.HandleMessage(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`))
	if !contains(string(out), "resources") {
		t.Fatalf("resources/list response missing 'resources': %s", out)
	}

	// resources/read with bad params -> error response.
	out = s.HandleMessage(ctx, []byte(`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":123}}`))
	if !contains(string(out), "error") {
		t.Fatalf("expected error for invalid params: %s", out)
	}

	// resources/read anchored://memory/stats -> stats payload.
	out = s.HandleMessage(ctx, []byte(`{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"anchored://memory/stats"}}`))
	if !contains(string(out), "Total") {
		t.Fatalf("stats resource missing 'Total': %s", out)
	}

	// resources/read anchored://memory/recent -> recent payload (empty DB -> "No memories yet.").
	out = s.HandleMessage(ctx, []byte(`{"jsonrpc":"2.0","id":4,"method":"resources/read","params":{"uri":"anchored://memory/recent"}}`))
	if !contains(string(out), "memories") && !contains(string(out), "No memories") {
		t.Fatalf("recent resource unexpected: %s", out)
	}

	// resources/read anchored://identity -> identity payload (may be empty file, no error path).
	_ = s.HandleMessage(ctx, []byte(`{"jsonrpc":"2.0","id":5,"method":"resources/read","params":{"uri":"anchored://identity"}}`))
}

func TestServer_CtxTools(t *testing.T) {
	s := newTestServer(t)
	if s.optimizer == nil {
		t.Skip("context optimizer not available in this environment")
	}

	// anchored_index via content exercises IndexContent + resetSearchThrottle.
	if _, err := callToolJSON(t, s, "anchored_index", map[string]any{
		"content": "func hello() { print('hi') }",
		"source":  "test",
	}); err != nil {
		t.Fatalf("index: %v", err)
	}

	// anchored_execute runs shell code, exercises Execute + IndexRaw + Search + renderExecOutput.
	res, err := callToolJSON(t, s, "anchored_execute", map[string]any{
		"language": "shell",
		"code":     "echo hello-anchored",
		"intent":   "smoke test",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !contains(res, "hello-anchored") {
		t.Fatalf("execute did not echo output: %s", res)
	}

	// anchored_ctx_search exercises Search against the indexed content.
	if _, err := callToolJSON(t, s, "anchored_ctx_search", map[string]any{
		"queries": []string{"hello"},
		"limit":   2,
	}); err != nil {
		t.Fatalf("ctx_search: %v", err)
	}

	// anchored_batch_execute exercises ExecuteBatch.
	if _, err := callToolJSON(t, s, "anchored_batch_execute", map[string]any{
		"commands": []map[string]any{
			{"language": "shell", "code": "echo one"},
			{"language": "shell", "code": "echo two"},
		},
		"queries": []string{"one"},
	}); err != nil {
		t.Fatalf("batch_execute: %v", err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (indexOf(s, substr) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// extractSaveID parses the ID out of a "Saved [<category>] memory <ID>" response.
func extractSaveID(s string) string {
	const marker = "memory "
	i := indexOf(s, marker)
	if i < 0 {
		return ""
	}
	start := i + len(marker)
	end := start
	for end < len(s) && s[end] != ' ' && s[end] != '\n' {
		end++
	}
	return s[start:end]
}
