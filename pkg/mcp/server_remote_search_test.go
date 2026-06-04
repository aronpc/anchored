package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/memory"
)

// TestToolSearch_MergesRemoteHits locks the day-to-day contract: when the
// cwd's project has a remote configured, anchored_search transparently merges
// the team server's hits into the local results — no `remote` param needed.
// Remote-origin hits carry origin="remote" so the agent can attribute them.
func TestToolSearch_MergesRemoteHits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")

	for _, args := range [][]string{
		{"init", "-q", repo},
		{"-C", repo, "remote", "add", "origin", "https://github.com/test/merge-fixture.git"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}

	cfg := config.Defaults()
	cfg.Memory.StorageDir = dir
	cfg.Memory.DatabasePath = filepath.Join(dir, "test.db")
	cfg.Embedding.Provider = "none"

	svc, err := memory.NewService(cfg, slog.Default())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	ctx := context.Background()
	if _, err := svc.Save(ctx, "local decision: keep the merge fixture deterministic", "decision", "test", repo); err != nil {
		t.Fatalf("save: %v", err)
	}
	proj, err := svc.ResolveProjectInfo(repo)
	if err != nil || proj == nil || proj.RemoteKey == "" {
		t.Fatalf("ResolveProjectInfo: proj=%v err=%v", proj, err)
	}

	var searchCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"id":"rp-1","name":"merge-fixture","slug":"merge-fixture","remote_key":%q}]`, proj.RemoteKey)
	})
	mux.HandleFunc("/v1/memories/search", func(w http.ResponseWriter, r *http.Request) {
		searchCalls.Add(1)
		if got := r.URL.Query().Get("project_id"); got != "rp-1" {
			t.Errorf("remote search project_id = %q, want rp-1 (the REMOTE id, never the local one)", got)
		}
		fmt.Fprint(w, `[{"id":"remote-1","category":"decision","content":"remote-only team memory","project_id":"rp-1"}]`)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Mirror what config.Load's migrateRemotes() produces for a singular
	// `remote:` block — ResolveRemote only consults the Remotes map.
	cfg.Remotes = map[string]config.RemoteEntry{
		"default": {Name: "default", ServerURL: ts.URL, APIKey: "test-key", Default: true},
	}

	srv := NewServer(svc, nil, nil, nil, cfg, "test", slog.Default())

	args, _ := json.Marshal(map[string]any{"query": "merge fixture", "cwd": repo})
	out, err := srv.toolSearch(ctx, args)
	if err != nil {
		t.Fatalf("toolSearch: %v", err)
	}

	if searchCalls.Load() == 0 {
		t.Fatal("remote search endpoint was never called — auto-merge did not engage")
	}
	for _, want := range []string{
		"local decision: keep the merge fixture deterministic", // local hit kept
		`origin="remote"`,                                      // remote hit tagged
		"remote-only team memory",                              // remote content present
		`id="remote-1"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, out)
		}
	}

	// Explicit remote param ("" = default) must search the remote EXCLUSIVELY.
	args, _ = json.Marshal(map[string]any{"query": "anything", "cwd": repo, "remote": ""})
	out, err = srv.toolSearch(ctx, args)
	if err != nil {
		t.Fatalf("toolSearch remote-only: %v", err)
	}
	if !strings.Contains(out, "remote-only team memory") || strings.Contains(out, "local decision:") {
		t.Errorf("remote:\"\" should be remote-exclusive\n--- output ---\n%s", out)
	}
}
