package sync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jholhewres/anchored/pkg/config"
)

// Identity invariants (wave-1 contract). The five sync-safety invariants are
// covered across the suite:
//
//	(1) explicit project with mismatched key needs --force
//	    → cmd/anchored/remote_link_test.go (remoteKeysMatch matrix)
//	(2) unresolvable key resolves to NOTHING — never a fallback project
//	    → this file
//	(3) save/auto-sync output names its destination
//	    → pkg/mcp/server_save_destination_test.go
//	(4) event / preference(scope=user) never pass the safety filter
//	    → this file (+ filter_test.go per-rule cases)
//	(5) secrets are redacted/blocked before any push
//	    → cmd/anchored/identity_invariant_chain_test.go (sanitizer+filter chain)

// TestInvariant_UnresolvableKeyResolvesToNothing locks invariant (2): a key
// no configured remote knows must resolve to nil — never to a previously
// linked or default project (the 7k wrong-project dump of 2026-06).
func TestInvariant_UnresolvableKeyResolvesToNothing(t *testing.T) {
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/projects" {
			w.Write([]byte(`[]`)) // server knows no projects
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer empty.Close()

	cfg := &config.Config{Remotes: map[string]config.RemoteEntry{
		"team": {
			Name: "team", ServerURL: empty.URL, APIKey: "k", Default: true,
			// A linked project exists — it must NOT be used as a fallback.
			Projects: []string{"someone-elses-project"},
		},
	}}

	entry, projectID, matchedKey := ResolveProjectAcrossRemotes(
		context.Background(), cfg, t.TempDir(), "cli", "feedfeedfeedfeed", "beefbeefbeefbeef")
	if entry != nil || projectID != "" || matchedKey != "" {
		t.Fatalf("unresolvable key must resolve to nothing, got entry=%v project=%q key=%q",
			entry, projectID, matchedKey)
	}
}

// TestInvariant_UnreachableRemoteResolvesToNothing: connectivity failure is
// also "no resolution", not an excuse to guess a target.
func TestInvariant_UnreachableRemoteResolvesToNothing(t *testing.T) {
	cfg := &config.Config{Remotes: map[string]config.RemoteEntry{
		"down": {Name: "down", ServerURL: "http://127.0.0.1:1", APIKey: "k", Default: true},
	}}
	entry, projectID, _ := ResolveProjectAcrossRemotes(
		context.Background(), cfg, t.TempDir(), "cli", "feedfeedfeedfeed")
	if entry != nil || projectID != "" {
		t.Fatalf("unreachable remote must resolve to nothing, got entry=%v project=%q", entry, projectID)
	}
}

// TestInvariant_LocalOnlyCategoriesNeverSync locks invariant (4) at the
// preview level: events and user-scoped preferences are local-only by design.
func TestInvariant_LocalOnlyCategoriesNeverSync(t *testing.T) {
	memories := []Memory{
		{ID: "e1", Category: "event", Content: "deployed v2 to production"},
		{ID: "p1", Category: "preference", Content: "I prefer tabs", PreferenceScope: "user"},
		{ID: "d1", Category: "decision", Content: "we standardized on Postgres"},
	}
	preview := ClassifyForPreview(memories, t.TempDir())

	status := map[string]PreviewClassification{}
	for _, item := range preview.Items {
		status[item.Memory.ID] = item.Classification
	}
	if status["e1"] == ClassificationSyncable {
		t.Error("event must never be syncable")
	}
	if status["p1"] == ClassificationSyncable {
		t.Error("user-scoped preference must never be syncable")
	}
	if status["d1"] != ClassificationSyncable {
		t.Errorf("clean decision must be syncable, got %v", status["d1"])
	}
}
