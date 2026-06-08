package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jholhewres/anchored/pkg/config"
)

// TestPush_AdvertisesCapabilitiesAndParsesPolicy locks the client side of
// capability negotiation: every push sends client_capabilities (the opt-in
// signal) and the optional policy hints in the response are parsed back.
func TestPush_AdvertisesCapabilitiesAndParsesPolicy(t *testing.T) {
	var gotCapabilities bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/sync/push" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, gotCapabilities = body["client_capabilities"]
		w.Write([]byte(`{"accepted":1,"rejected":0,"project_id":"rp-1",
			"policy":{"quality_threshold":0.55,"blocked_categories":["event","preference"],"max_memories_per_sync":500}}`))
	}))
	defer srv.Close()

	c := NewClientFromEntry(config.RemoteEntry{Name: "t", ServerURL: srv.URL, APIKey: "k"}, "cli")
	resp, err := c.Push(context.Background(), SyncPushRequest{
		ClientID:  "cli",
		ProjectID: "rp-1",
		Memories: []SyncMemory{{
			ID: "m1", Category: "decision",
			Content: "we chose Postgres for durable team memory storage across the fleet",
		}},
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if !gotCapabilities {
		t.Fatal("push did not advertise client_capabilities")
	}
	if resp.Policy == nil {
		t.Fatal("policy hints not parsed from response")
	}
	if resp.Policy.MaxMemoriesPerSync != 500 || resp.Policy.QualityThreshold != 0.55 {
		t.Fatalf("policy mismatch: %+v", resp.Policy)
	}
	if line := policyHintLineForTest(resp.Policy); !strings.Contains(line, "blocked categories [event preference]") || !strings.Contains(line, "max 500 per sync") {
		t.Fatalf("hint line: %q", line)
	}
}

// TestPush_NoPolicyFromOldServer: a server that omits policy yields nil, and
// the client behaves exactly as before (no hint, no error).
func TestPush_NoPolicyFromOldServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"accepted":1,"rejected":0,"project_id":"rp-1"}`))
	}))
	defer srv.Close()

	c := NewClientFromEntry(config.RemoteEntry{Name: "t", ServerURL: srv.URL, APIKey: "k"}, "cli")
	resp, err := c.Push(context.Background(), SyncPushRequest{
		ClientID: "cli", ProjectID: "rp-1",
		Memories: []SyncMemory{{ID: "m1", Category: "decision", Content: "a sufficiently substantive decision about the architecture"}},
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if resp.Policy != nil {
		t.Fatalf("old server must yield nil policy, got %+v", resp.Policy)
	}
}

// TestSharedCapabilityVectorsPresent guards that the shared contract file is
// mirrored byte-identically from the server repo (the test that asserts the
// hash match lives in CI; here we at least parse it and check the expected
// policy vector the client must understand).
func TestSharedCapabilityVectorsPresent(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "sync_capability_vectors.json"))
	if err != nil {
		t.Fatalf("read shared vectors: %v", err)
	}
	var doc struct {
		Vectors []struct {
			Name           string       `json:"name"`
			ExpectedPolicy *PolicyHints `json:"expected_policy"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	var found bool
	for _, v := range doc.Vectors {
		if v.Name == "capability_aware_policy_present" {
			found = true
			if v.ExpectedPolicy == nil || v.ExpectedPolicy.MaxMemoriesPerSync != 500 {
				t.Fatalf("vector policy mismatch: %+v", v.ExpectedPolicy)
			}
		}
	}
	if !found {
		t.Fatal("capability_aware_policy_present vector missing")
	}
}

// policyHintLineForTest re-implements the cmd-layer formatter so pkg/sync can
// assert the hint shape without importing the command package.
func policyHintLineForTest(p *PolicyHints) string {
	parts := make([]string, 0, 2)
	if len(p.BlockedCategories) > 0 {
		parts = append(parts, "blocked categories ["+strings.Join(p.BlockedCategories, " ")+"]")
	}
	if p.MaxMemoriesPerSync > 0 {
		parts = append(parts, "max "+itoa(p.MaxMemoriesPerSync)+" per sync")
	}
	return "server policy: " + strings.Join(parts, "; ")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
