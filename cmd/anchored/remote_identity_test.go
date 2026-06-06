package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jholhewres/anchored/pkg/config"
)

// fakeProjectsServer serves GET /v1/projects with the given JSON body.
func fakeProjectsServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/projects" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBuildNoProjectError_Actionable(t *testing.T) {
	msg := buildNoProjectError(
		"git@github.example.com:org/repo.git",
		"abcd1234abcd1234", "ffff0000ffff0000",
		[]string{"default", "team"},
	)
	for _, want := range []string{
		"git@github.example.com:org/repo.git",
		"abcd1234abcd1234",
		"ffff0000ffff0000 (legacy)",
		"default, team",
		"anchored remote link <slug> --remote <name>",
		"anchored doctor",
		"Repository URL",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q:\n%s", want, msg)
		}
	}
}

func TestRepoIdentityLines_Match(t *testing.T) {
	srv := fakeProjectsServer(t,
		`[{"id":"p-1","name":"Repo","slug":"repo","remote_key":"abcd1234abcd1234","remote_key_v1":""}]`)

	cfg := &config.Config{Remotes: map[string]config.RemoteEntry{
		"team": {Name: "team", ServerURL: srv.URL, APIKey: "k", Default: true},
	}}
	lines := repoIdentityLines(context.Background(), cfg, t.TempDir(),
		"git@github.example.com:org/repo.git", "abcd1234abcd1234", "")
	if len(lines) == 0 {
		t.Fatal("no lines")
	}
	if !strings.Contains(lines[0], "match — project p-1") || !strings.Contains(lines[0], `"team"`) {
		t.Fatalf("match line: %q", lines[0])
	}
}

func TestRepoIdentityLines_NoMatch(t *testing.T) {
	srv := fakeProjectsServer(t, `[]`)
	cfg := &config.Config{Remotes: map[string]config.RemoteEntry{
		"team": {Name: "team", ServerURL: srv.URL, APIKey: "k", Default: true},
	}}
	lines := repoIdentityLines(context.Background(), cfg, t.TempDir(),
		"git@github.example.com:org/repo.git", "abcd1234abcd1234", "")
	if len(lines) < 2 {
		t.Fatalf("lines: %v", lines)
	}
	if !strings.Contains(lines[0], "no configured remote has a project") {
		t.Fatalf("no-match line: %q", lines[0])
	}
	if !strings.Contains(lines[1], "Repository URL git@github.example.com:org/repo.git") {
		t.Fatalf("fix line: %q", lines[1])
	}
}

func TestRepoIdentityLines_LinkedMismatch(t *testing.T) {
	// The remote knows one project, linked in config, registered to a
	// DIFFERENT repository key — the classic wrong-project push setup.
	srv := fakeProjectsServer(t,
		`[{"id":"p-9","name":"Other","slug":"other","remote_key":"9999aaaa9999aaaa","remote_key_v1":""}]`)
	cfg := &config.Config{Remotes: map[string]config.RemoteEntry{
		"team": {Name: "team", ServerURL: srv.URL, APIKey: "k", Default: true, Projects: []string{"p-9"}},
	}}
	lines := repoIdentityLines(context.Background(), cfg, t.TempDir(),
		"git@github.example.com:org/repo.git", "abcd1234abcd1234", "")

	var mismatch bool
	for _, l := range lines {
		if strings.Contains(l, "MISMATCH") && strings.Contains(l, `"other"`) {
			mismatch = true
		}
	}
	if !mismatch {
		t.Fatalf("expected linked-project mismatch line, got: %v", lines)
	}
}
