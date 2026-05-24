package sync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jholhewres/anchored/pkg/config"
)

func TestNewClient_Disabled(t *testing.T) {
	cfg := config.RemoteConfig{Enabled: false}
	client := NewClient(cfg, "test-client")
	if client != nil {
		t.Error("expected nil client when config is disabled")
	}
}

func TestNewClient_Enabled(t *testing.T) {
	cfg := config.RemoteConfig{
		Enabled:   true,
		ServerURL: "https://example.com",
		APIKey:    "test-key",
	}
	client := NewClient(cfg, "test-client")
	if client == nil {
		t.Fatal("expected non-nil client when config is enabled")
	}
	if client.serverURL != "https://example.com" {
		t.Errorf("expected serverURL=https://example.com, got %s", client.serverURL)
	}
	if client.apiKey != "test-key" {
		t.Error("apiKey mismatch")
	}
	if client.clientID != "test-client" {
		t.Error("clientID mismatch")
	}
}

func TestPush_ValidRequest(t *testing.T) {
	var receivedPath string
	var receivedAuth string
	var receivedBody SyncPushRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Errorf("decode body: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SyncPushResponse{
			Accepted: len(receivedBody.Memories),
			Rejected: 0,
		})
	}))
	defer srv.Close()

	client := &Client{
		httpClient: srv.Client(),
		serverURL:  srv.URL,
		apiKey:     "secret-key",
		clientID:   "test-client",
	}

	resp, err := client.Push(context.Background(), SyncPushRequest{
		ClientID:  "test-client",
		ProjectID: "proj-1",
		Memories: []SyncMemory{
			{ID: "m1", Category: "fact", Content: "clean content", Source: "user"},
			{ID: "m2", Category: "fact", Content: "another clean fact", Source: "user"},
		},
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if receivedPath != "/api/v1/sync/push" {
		t.Errorf("expected path /api/v1/sync/push, got %s", receivedPath)
	}
	if receivedAuth != "Bearer secret-key" {
		t.Errorf("expected Bearer auth, got %s", receivedAuth)
	}
	if resp.Accepted != 2 {
		t.Errorf("expected 2 accepted, got %d", resp.Accepted)
	}
	if len(receivedBody.Memories) != 2 {
		t.Errorf("expected 2 memories in request body, got %d", len(receivedBody.Memories))
	}
}

func TestPush_BlockedContentRejected(t *testing.T) {
	var receivedMemories []SyncMemory
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body SyncPushRequest
		json.NewDecoder(r.Body).Decode(&body)
		receivedMemories = body.Memories
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SyncPushResponse{Accepted: 0, Rejected: 0})
	}))
	defer srv.Close()

	client := &Client{
		httpClient: srv.Client(),
		serverURL:  srv.URL,
		apiKey:     "key",
		clientID:   "test-client",
	}

	resp, err := client.Push(context.Background(), SyncPushRequest{
		ClientID:  "test-client",
		ProjectID: "proj-1",
		Memories: []SyncMemory{
			{ID: "m1", Category: "fact", Content: "project lives at /home/alice/myapp", Source: "user"},
		},
	})
	if err != nil {
		t.Fatalf("push should not error on blocked content, got: %v", err)
	}
	if len(receivedMemories) != 0 {
		t.Errorf("blocked content was sent to server — expected 0 memories in body, got %d", len(receivedMemories))
	}
	if resp.Rejected != 1 {
		t.Errorf("expected 1 rejected, got %d", resp.Rejected)
	}
	if len(resp.Errors) == 0 {
		t.Error("expected rejection error message")
	}
}

func TestPush_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := &Client{
		httpClient: srv.Client(),
		serverURL:  srv.URL,
		apiKey:     "key",
		clientID:   "test-client",
	}

	_, err := client.Push(context.Background(), SyncPushRequest{
		ClientID:  "test-client",
		ProjectID: "proj-1",
		Memories: []SyncMemory{
			{ID: "m1", Category: "fact", Content: "clean", Source: "user"},
		},
	})
	if err == nil {
		t.Fatal("expected error on server 500")
	}
}

func TestPull_WithWatermark(t *testing.T) {
	var receivedBody SyncPullRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/sync/pull" {
			t.Errorf("expected /api/v1/sync/pull, got %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&receivedBody); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SyncPullResponse{
			Memories: []SyncMemory{
				{ID: "r1", Category: "fact", Content: "remote fact", Source: "sync"},
			},
			Watermark: "w2",
		})
	}))
	defer srv.Close()

	client := &Client{
		httpClient: srv.Client(),
		serverURL:  srv.URL,
		apiKey:     "key",
		clientID:   "test-client",
	}

	resp, err := client.Pull(context.Background(), SyncPullRequest{
		ClientID:  "test-client",
		ProjectID: "proj-1",
		Watermark: "w1",
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if receivedBody.Watermark != "w1" {
		t.Errorf("expected watermark w1, got %s", receivedBody.Watermark)
	}
	if len(resp.Memories) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(resp.Memories))
	}
	if resp.Memories[0].ID != "r1" {
		t.Errorf("expected memory r1, got %s", resp.Memories[0].ID)
	}
	if resp.Watermark != "w2" {
		t.Errorf("expected watermark w2, got %s", resp.Watermark)
	}
}

func TestPull_AuthorizationHeader(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SyncPullResponse{})
	}))
	defer srv.Close()

	client := &Client{
		httpClient: srv.Client(),
		serverURL:  srv.URL,
		apiKey:     "my-api-key",
		clientID:   "test-client",
	}

	_, err := client.Pull(context.Background(), SyncPullRequest{
		ClientID:  "test-client",
		ProjectID: "proj-1",
	})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if receivedAuth != "Bearer my-api-key" {
		t.Errorf("expected 'Bearer my-api-key', got '%s'", receivedAuth)
	}
}

// Regression: a memory whose only "local path" sits under projectRoot must
// be classified syncable by the preview AND must not be re-blocked by Push.
// Before the fix, Push re-applied RemoteSafetyFilter with empty projectRoot
// and silently dropped these.
func TestPush_PathUnderProjectRoot_NotReBlocked(t *testing.T) {
	var receivedMemories []SyncMemory
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body SyncPushRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		receivedMemories = body.Memories
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SyncPushResponse{Accepted: len(body.Memories)})
	}))
	defer srv.Close()

	projectRoot := "/home/alice/myproj"
	memories := []Memory{
		{ID: "m1", Category: "fact", Content: "see " + projectRoot + "/foo.go for details", Source: "user"},
	}
	preview := ClassifyForPreview(memories, projectRoot)
	if preview.Syncable != 1 {
		t.Fatalf("preview should classify as syncable, got %+v", preview)
	}

	client := &Client{
		httpClient: srv.Client(),
		serverURL:  srv.URL,
		apiKey:     "key",
		clientID:   "test-client",
	}

	resp, err := client.Push(context.Background(), SyncPushRequest{
		ClientID:    "test-client",
		ProjectID:   "proj-1",
		ProjectRoot: projectRoot,
		Memories: []SyncMemory{
			{ID: "m1", Category: "fact", Content: preview.Items[0].Memory.Content, Source: "user"},
		},
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if resp.Accepted != 1 {
		t.Fatalf("expected 1 accepted, got %d (rejected=%d errors=%v)", resp.Accepted, resp.Rejected, resp.Errors)
	}
	if len(receivedMemories) != 1 {
		t.Fatalf("expected 1 memory in body, got %d", len(receivedMemories))
	}
	if got := receivedMemories[0].Content; got == memories[0].Content {
		t.Errorf("expected rewritten content, got raw: %q", got)
	}
}

// Defense in depth: even if the caller forgets to filter scope=user,
// Push must not forward it to the server.
func TestPush_PersonalPreferenceRejectedAtPush(t *testing.T) {
	var receivedMemories []SyncMemory
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body SyncPushRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		receivedMemories = body.Memories
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SyncPushResponse{Accepted: 0})
	}))
	defer srv.Close()

	client := &Client{
		httpClient: srv.Client(),
		serverURL:  srv.URL,
		apiKey:     "key",
		clientID:   "test",
	}

	resp, err := client.Push(context.Background(), SyncPushRequest{
		ClientID:  "test",
		ProjectID: "p",
		Memories: []SyncMemory{
			{ID: "m1", Category: "preference", Content: "I like dark theme", Source: "user", PreferenceScope: "user"},
		},
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if resp.Rejected != 1 {
		t.Fatalf("expected 1 rejected, got %d", resp.Rejected)
	}
	if len(receivedMemories) != 0 {
		t.Errorf("personal preference leaked to server: %d memories", len(receivedMemories))
	}
}

// When everything is locally rejected, no HTTP call should happen.
func TestPush_ShortCircuitsWhenAllBlocked(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SyncPushResponse{})
	}))
	defer srv.Close()

	client := &Client{
		httpClient: srv.Client(),
		serverURL:  srv.URL,
		apiKey:     "k",
		clientID:   "test",
	}

	resp, err := client.Push(context.Background(), SyncPushRequest{
		ClientID:  "test",
		ProjectID: "p",
		Memories: []SyncMemory{
			{ID: "m1", Category: "fact", Content: "lives at /home/alice/x", Source: "user"},
		},
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if hits != 0 {
		t.Errorf("expected 0 HTTP hits when all rejected, got %d", hits)
	}
	if resp.Rejected != 1 || resp.Accepted != 0 {
		t.Errorf("expected accepted=0 rejected=1, got %+v", resp)
	}
}

// When APIKey is empty, no Authorization header should be sent.
func TestPush_NoAuthHeaderWhenAPIKeyEmpty(t *testing.T) {
	var receivedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SyncPushResponse{Accepted: 1})
	}))
	defer srv.Close()

	client := &Client{
		httpClient: srv.Client(),
		serverURL:  srv.URL,
		apiKey:     "",
		clientID:   "test",
	}

	_, err := client.Push(context.Background(), SyncPushRequest{
		ClientID:  "test",
		ProjectID: "p",
		Memories: []SyncMemory{
			{ID: "m1", Category: "fact", Content: "clean", Source: "user"},
		},
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if receivedAuth != "" {
		t.Errorf("expected no Authorization header, got %q", receivedAuth)
	}
}

func TestPull_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := &Client{
		httpClient: srv.Client(),
		serverURL:  srv.URL,
		apiKey:     "k",
		clientID:   "test",
	}

	_, err := client.Pull(context.Background(), SyncPullRequest{ClientID: "test", ProjectID: "p"})
	if err == nil {
		t.Fatal("expected error on 5xx")
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Errorf("expected HTTPError, got %T: %v", err, err)
	}
	if httpErr != nil && httpErr.Status != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", httpErr.Status)
	}
}
