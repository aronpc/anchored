package sync

import (
	"encoding/json"
	"testing"
)

func TestSyncPushRequest_JSONRoundTrip(t *testing.T) {
	original := SyncPushRequest{
		ClientID:  "client-1",
		ProjectID: "proj-1",
		Memories: []SyncMemory{
			{ID: "m1", Category: "fact", Content: "test content", Source: "user", PreferenceScope: "project"},
		},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SyncPushRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.ClientID != "client-1" {
		t.Errorf("expected ClientID=client-1, got %s", decoded.ClientID)
	}
	if decoded.ProjectID != "proj-1" {
		t.Errorf("expected ProjectID=proj-1, got %s", decoded.ProjectID)
	}
	if len(decoded.Memories) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(decoded.Memories))
	}
	if decoded.Memories[0].ID != "m1" {
		t.Errorf("expected memory ID m1, got %s", decoded.Memories[0].ID)
	}
	if decoded.Memories[0].PreferenceScope != "project" {
		t.Errorf("expected PreferenceScope=project, got %s", decoded.Memories[0].PreferenceScope)
	}
}

func TestSyncPushResponse_JSONRoundTrip(t *testing.T) {
	original := SyncPushResponse{
		Accepted: 5,
		Rejected: 2,
		Errors:   []string{"bad memory m3", "bad memory m7"},
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SyncPushResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Accepted != 5 {
		t.Errorf("expected Accepted=5, got %d", decoded.Accepted)
	}
	if decoded.Rejected != 2 {
		t.Errorf("expected Rejected=2, got %d", decoded.Rejected)
	}
	if len(decoded.Errors) != 2 {
		t.Errorf("expected 2 errors, got %d", len(decoded.Errors))
	}
}

func TestSyncPullRequest_JSONRoundTrip(t *testing.T) {
	original := SyncPullRequest{
		ClientID:  "client-1",
		ProjectID: "proj-1",
		Watermark: "w-2024-01-01",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SyncPullRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Watermark != "w-2024-01-01" {
		t.Errorf("expected Watermark=w-2024-01-01, got %s", decoded.Watermark)
	}
}

func TestSyncPullResponse_JSONRoundTrip(t *testing.T) {
	original := SyncPullResponse{
		Memories: []SyncMemory{
			{ID: "r1", Category: "decision", Content: "use Go 1.22", Source: "sync"},
			{ID: "r2", Category: "fact", Content: "database is PostgreSQL", Source: "sync", RemoteProjectKey: "backend"},
		},
		Watermark: "w-2024-06-01",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded SyncPullResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded.Memories) != 2 {
		t.Fatalf("expected 2 memories, got %d", len(decoded.Memories))
	}
	if decoded.Memories[0].ID != "r1" {
		t.Errorf("expected memory r1, got %s", decoded.Memories[0].ID)
	}
	if decoded.Memories[1].RemoteProjectKey != "backend" {
		t.Errorf("expected RemoteProjectKey=backend, got %s", decoded.Memories[1].RemoteProjectKey)
	}
	if decoded.Watermark != "w-2024-06-01" {
		t.Errorf("expected Watermark=w-2024-06-01, got %s", decoded.Watermark)
	}
}

func TestSyncPullRequest_EmptyWatermark(t *testing.T) {
	original := SyncPullRequest{
		ClientID:  "client-1",
		ProjectID: "proj-1",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	if _, exists := raw["watermark"]; exists {
		t.Error("expected watermark to be omitted when empty, but it was present")
	}
}

func TestSyncMemory_OmitsEmptyFields(t *testing.T) {
	original := SyncMemory{
		ID:       "m1",
		Category: "fact",
		Content:  "test",
		Source:   "user",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}

	if _, exists := raw["preference_scope"]; exists {
		t.Error("expected preference_scope to be omitted when empty")
	}
	if _, exists := raw["remote_project_key"]; exists {
		t.Error("expected remote_project_key to be omitted when empty")
	}
}
