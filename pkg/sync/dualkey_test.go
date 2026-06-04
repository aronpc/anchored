package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/project"
)

// bitbucket-style origin where the canonical (v2) and legacy (v1) keys differ:
// the legacy key keeps the port, the canonical key strips it.
const dualKeyOrigin = "ssh://git@bitbucket.example.com:7999/proj/repo.git"

// TestResolveProjectIDByRemoteKeysLegacyFallback proves the dual-key probe finds
// a project still registered under the legacy key, reporting which key matched,
// after the canonical key misses.
func TestResolveProjectIDByRemoteKeysLegacyFallback(t *testing.T) {
	canonical := project.DeriveRemoteKeyFromURL(dualKeyOrigin)
	legacy := project.DeriveLegacyRemoteKeyFromURL(dualKeyOrigin)
	if canonical == legacy || canonical == "" || legacy == "" {
		t.Fatalf("test vector must have distinct non-empty keys, canonical=%q legacy=%q", canonical, legacy)
	}

	// Server only knows the project under the legacy key.
	ts := fakeProjectsServer(t, legacy)
	client := NewClientFromEntry(config.RemoteEntry{Name: "x", ServerURL: ts.URL, APIKey: "k"}, "test")

	pid, matched := client.ResolveProjectIDByRemoteKeys(context.Background(), canonical, legacy)
	if pid != "proj-on-this-server" {
		t.Fatalf("expected to resolve project under legacy key, got pid=%q", pid)
	}
	if matched != legacy {
		t.Fatalf("expected matchedKey == legacy %q, got %q", legacy, matched)
	}

	// No keys / all empty → nothing.
	if pid, matched := client.ResolveProjectIDByRemoteKeys(context.Background(), "", ""); pid != "" || matched != "" {
		t.Fatalf("empty keys should resolve to nothing, got pid=%q matched=%q", pid, matched)
	}
}

// TestPushStampsMatchedLegacyKey asserts that when resolution finds the project
// under the legacy key, the outgoing push payload carries that legacy key in
// both project_claim.remote_key and every memory's remote_project_key — so the
// push lands in the existing project instead of forking a canonical duplicate.
func TestPushStampsMatchedLegacyKey(t *testing.T) {
	canonical := project.DeriveRemoteKeyFromURL(dualKeyOrigin)
	legacy := project.DeriveLegacyRemoteKeyFromURL(dualKeyOrigin)

	var pushBody SyncPushRequest
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		// Only knows the project under the legacy key.
		fmt.Fprintf(w, `[{"id":"proj-legacy","name":"repo","slug":"repo","remote_key":%q}]`, legacy)
	})
	mux.HandleFunc("/api/v1/sync/push", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &pushBody)
		fmt.Fprint(w, `{"accepted":1,"rejected":0,"project_id":"proj-legacy"}`)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	client := NewClientFromEntry(config.RemoteEntry{Name: "x", ServerURL: ts.URL, APIKey: "k"}, "test")

	// Simulate the caller's stamping logic: resolve which key the server knows,
	// then stamp the payload with it.
	_, matched := client.ResolveProjectIDByRemoteKeys(context.Background(), canonical, legacy)
	if matched != legacy {
		t.Fatalf("setup: expected to match legacy key, got %q", matched)
	}

	req := SyncPushRequest{
		ClientID:     "test",
		ProjectClaim: &ProjectClaim{Name: "repo", RemoteKey: matched},
		Memories: []SyncMemory{{
			ID:               "m1",
			Category:         "decision",
			Content:          "a syncable decision about the architecture",
			Source:           "cli",
			RemoteProjectKey: matched,
		}},
	}
	if _, err := client.Push(context.Background(), req); err != nil {
		t.Fatalf("push: %v", err)
	}

	if pushBody.ProjectClaim == nil || pushBody.ProjectClaim.RemoteKey != legacy {
		t.Fatalf("project_claim.remote_key = %v, want legacy %q", pushBody.ProjectClaim, legacy)
	}
	if len(pushBody.Memories) != 1 || pushBody.Memories[0].RemoteProjectKey != legacy {
		t.Fatalf("memories[0].remote_project_key = %+v, want legacy %q", pushBody.Memories, legacy)
	}
	if pushBody.ProjectClaim.RemoteKey == canonical {
		t.Fatalf("payload must not carry the canonical key when server knows the legacy one")
	}
}
