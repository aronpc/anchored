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
	"testing"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/memory"
)

// TestToolSave_NamesAutoSyncDestination locks invariant (3) of the sync
// identity contract: a save that auto-syncs must name the exact server and
// project it goes to — "(auto-sync → <remote> · <slug>)" — never a bare
// "(auto-sync)" that leaves the user guessing where the memory landed.
func TestToolSave_NamesAutoSyncDestination(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	for _, args := range [][]string{
		{"init", "-q", repo},
		{"-C", repo, "remote", "add", "origin", "https://github.com/test/dest-fixture.git"},
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

	proj, err := svc.ResolveProjectInfo(repo)
	if err != nil || proj == nil || proj.RemoteKey == "" {
		t.Fatalf("ResolveProjectInfo: proj=%v err=%v", proj, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"id":"rp-7","name":"dest-fixture","slug":"dest-fixture","remote_key":%q}]`, proj.RemoteKey)
	})
	mux.HandleFunc("/v1/memories", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":"m-1","category":"decision","project_id":"rp-7","created":true}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	cfg.Remotes = map[string]config.RemoteEntry{
		"teamsrv": {Name: "teamsrv", ServerURL: ts.URL, APIKey: "test-key", Default: true},
	}

	srv := NewServer(svc, nil, nil, nil, cfg, "test", slog.Default())

	args, _ := json.Marshal(map[string]any{
		"content":  "decision: destination labeling is part of the sync contract",
		"category": "decision",
		"cwd":      repo,
	})
	out, err := srv.toolSave(context.Background(), args)
	if err != nil {
		t.Fatalf("toolSave: %v", err)
	}

	want := "(auto-sync → teamsrv · dest-fixture)"
	if !strings.Contains(out, want) {
		t.Fatalf("save result must name its destination %q\n--- output ---\n%s", want, out)
	}
	if strings.Contains(out, "(auto-sync)") && !strings.Contains(out, want) {
		t.Fatalf("bare (auto-sync) without destination: %s", out)
	}
}
