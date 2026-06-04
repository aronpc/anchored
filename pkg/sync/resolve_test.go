package sync

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jholhewres/anchored/pkg/config"
)

// fakeProjectsServer answers GET /v1/projects with a single project carrying
// the given remote_key ("" → empty list).
func fakeProjectsServer(t *testing.T, remoteKey string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		if remoteKey == "" {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprintf(w, `[{"id":"proj-on-this-server","name":"x","slug":"x","remote_key":%q}]`, remoteKey)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

// TestResolveProjectAcrossRemotes locks the "works right after configure"
// contract: a second server added with no routing paths must still be found
// when it is the one that knows the repository's git-origin key.
func TestResolveProjectAcrossRemotes(t *testing.T) {
	const key = "abc123key"
	personal := fakeProjectsServer(t, "") // default remote: does NOT know the repo
	company := fakeProjectsServer(t, key) // freshly-added remote: knows it

	cfg := config.Defaults()
	cfg.Remotes = map[string]config.RemoteEntry{
		"default": {Name: "default", ServerURL: personal.URL, APIKey: "k1", Default: true},
		"company": {Name: "company", ServerURL: company.URL, APIKey: "k2"}, // no paths, no default
	}

	entry, pid, matched := ResolveProjectAcrossRemotes(context.Background(), cfg, "/some/repo", "test", key)
	if entry == nil || entry.Name != "company" || pid != "proj-on-this-server" || matched != key {
		t.Fatalf("probe should find the company remote, got entry=%+v pid=%q matched=%q", entry, pid, matched)
	}

	// When the resolved (default) remote knows the key, it wins — the probe
	// must not jump to another server.
	both := fakeProjectsServer(t, key)
	cfg.Remotes["default"] = config.RemoteEntry{Name: "default", ServerURL: both.URL, APIKey: "k1", Default: true}
	entry, _, _ = ResolveProjectAcrossRemotes(context.Background(), cfg, "/some/repo", "test", key)
	if entry == nil || entry.Name != "default" {
		t.Fatalf("resolved remote should win when it knows the key, got %+v", entry)
	}

	// Unknown key everywhere → nothing.
	if e, p, m := ResolveProjectAcrossRemotes(context.Background(), cfg, "/some/repo", "test", "nope"); e != nil || p != "" || m != "" {
		t.Fatalf("unknown key should resolve to nothing, got %+v %q %q", e, p, m)
	}
}
