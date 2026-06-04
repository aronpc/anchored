package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jholhewres/anchored/pkg/config"
)

func slugProjectsServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/projects", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[
			{"id":"11111111-2222-3333-4444-555555555555","name":"API","slug":"api","remote_key":"k1"},
			{"id":"66666666-7777-8888-9999-aaaaaaaaaaaa","name":"Web","slug":"web","remote_key":"k2"}
		]`)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestResolveLinkTargetUUIDPassthrough(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	got, err := resolveLinkTarget(id, config.RemoteEntry{ServerURL: "http://unused.example.com"}, "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != id {
		t.Fatalf("UUID should pass through unchanged, got %q", got)
	}
}

func TestResolveLinkTargetSlugMatch(t *testing.T) {
	ts := slugProjectsServer(t)
	got, err := resolveLinkTarget("web", config.RemoteEntry{ServerURL: ts.URL, APIKey: "k"}, "default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "66666666-7777-8888-9999-aaaaaaaaaaaa" {
		t.Fatalf("slug 'web' should resolve to its project id, got %q", got)
	}
}

func TestResolveLinkTargetSlugNotFound(t *testing.T) {
	ts := slugProjectsServer(t)
	_, err := resolveLinkTarget("nope", config.RemoteEntry{ServerURL: ts.URL, APIKey: "k"}, "company")
	if err == nil {
		t.Fatal("expected an error for an unknown slug")
	}
	msg := err.Error()
	if !strings.Contains(msg, `no project with slug "nope" on remote "company"`) {
		t.Errorf("error message missing slug/remote context: %q", msg)
	}
	// Available slugs should be listed to guide the user.
	if !strings.Contains(msg, "api") || !strings.Contains(msg, "web") {
		t.Errorf("error message should list available slugs, got %q", msg)
	}
}
