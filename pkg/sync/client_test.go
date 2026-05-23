package sync

import (
	"context"
	"encoding/json"
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

func TestClient_Preview(t *testing.T) {
	client := &Client{
		httpClient: &http.Client{},
		serverURL:  "https://example.com",
		apiKey:     "key",
		clientID:   "test",
	}

	memories := []Memory{
		{ID: "m1", Category: "fact", Content: "clean content", Source: "user"},
		{ID: "m2", Category: "fact", Content: "path /home/alice/x", Source: "user"},
	}

	result := client.Preview(memories, "")
	if result.Total != 2 {
		t.Errorf("expected Total=2, got %d", result.Total)
	}
	if result.Syncable != 1 {
		t.Errorf("expected Syncable=1, got %d", result.Syncable)
	}
	if result.Blocked != 1 {
		t.Errorf("expected Blocked=1, got %d", result.Blocked)
	}
}
