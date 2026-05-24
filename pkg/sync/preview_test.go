package sync

import (
	"testing"
)

func TestClassifyForPreview_CleanContent(t *testing.T) {
	memories := []Memory{
		{
			ID:       "m1",
			Category: "fact",
			Content:  "the application uses React for the frontend",
			Source:   "user",
		},
	}
	result := ClassifyForPreview(memories, "")
	if result.Total != 1 {
		t.Errorf("expected Total=1, got %d", result.Total)
	}
	if result.Syncable != 1 {
		t.Errorf("expected Syncable=1, got %d", result.Syncable)
	}
	if len(result.Items) != 1 || result.Items[0].Classification != ClassificationSyncable {
		t.Errorf("expected syncable, got %s", result.Items[0].Classification)
	}
}

func TestClassifyForPreview_LocalPath(t *testing.T) {
	memories := []Memory{
		{
			ID:       "m2",
			Category: "fact",
			Content:  "project lives at /home/user/projects/myapp",
			Source:   "user",
		},
	}
	result := ClassifyForPreview(memories, "")
	if result.Blocked != 1 {
		t.Errorf("expected Blocked=1, got %d", result.Blocked)
	}
	if len(result.Items) != 1 || result.Items[0].Classification != ClassificationBlocked {
		t.Errorf("expected blocked, got %s", result.Items[0].Classification)
	}
	if result.Items[0].Reason != "local_path" {
		t.Errorf("expected reason=local_path, got %s", result.Items[0].Reason)
	}
}

func TestClassifyForPreview_Secret(t *testing.T) {
	memories := []Memory{
		{
			ID:       "m3",
			Category: "fact",
			Content:  "api_key=sk-abcdefghijklmnopqrstuvwxyz12345 for the service",
			Source:   "user",
		},
	}
	result := ClassifyForPreview(memories, "")
	if result.Blocked != 1 {
		t.Errorf("expected Blocked=1, got %d", result.Blocked)
	}
	if result.Items[0].Reason != "secret_pattern" {
		t.Errorf("expected reason=secret_pattern, got %s", result.Items[0].Reason)
	}
}

func TestClassifyForPreview_PersonalPreference(t *testing.T) {
	memories := []Memory{
		{
			ID:       "m4",
			Category: "preference",
			Content:  "prefer dark theme",
			Source:   "user",
			Metadata: map[string]any{"scope": "user"},
		},
	}
	result := ClassifyForPreview(memories, "")
	if result.Blocked != 1 {
		t.Errorf("expected Blocked=1 for personal preference, got %d", result.Blocked)
	}
	if result.Items[0].Reason != "personal_preference" {
		t.Errorf("expected reason=personal_preference, got %s", result.Items[0].Reason)
	}
}

func TestClassifyForPreview_AlreadySynced(t *testing.T) {
	memories := []Memory{
		{
			ID:         "m5",
			Category:   "fact",
			Content:    "team uses Go 1.22",
			Source:     "sync",
			SyncOrigin: "remote",
		},
	}
	result := ClassifyForPreview(memories, "")
	if result.NeedsReview != 1 {
		t.Errorf("expected NeedsReview=1 for already synced, got %d", result.NeedsReview)
	}
	if result.Items[0].Reason != "already_synced" {
		t.Errorf("expected reason=already_synced, got %s", result.Items[0].Reason)
	}
}

func TestClassifyForPreview_PendingChanges(t *testing.T) {
	memories := []Memory{
		{
			ID:        "m6",
			Category:  "fact",
			Content:   "database is PostgreSQL",
			Source:    "user",
			SyncDirty: true,
		},
	}
	result := ClassifyForPreview(memories, "")
	if result.NeedsReview != 1 {
		t.Errorf("expected NeedsReview=1 for pending changes, got %d", result.NeedsReview)
	}
	if result.Items[0].Reason != "pending_changes" {
		t.Errorf("expected reason=pending_changes, got %s", result.Items[0].Reason)
	}
}

func TestClassifyForPreview_ProjectPreference(t *testing.T) {
	memories := []Memory{
		{
			ID:       "m7",
			Category: "preference",
			Content:  "always use TypeScript strict mode",
			Source:   "user",
			Metadata: map[string]any{"scope": "project"},
		},
	}
	result := ClassifyForPreview(memories, "")
	if result.Syncable != 1 {
		t.Errorf("expected Syncable=1 for project preference, got %d", result.Syncable)
	}
}

func TestClassifyForPreview_TeamPreference(t *testing.T) {
	memories := []Memory{
		{
			ID:       "m8",
			Category: "preference",
			Content:  "prefer small PRs",
			Source:    "user",
			Metadata: map[string]any{"scope": "team"},
		},
	}
	result := ClassifyForPreview(memories, "")
	if result.Syncable != 1 {
		t.Errorf("expected Syncable=1 for team preference, got %d", result.Syncable)
	}
}

func TestClassifyForPreview_Mixed(t *testing.T) {
	pid := "proj-1"
	memories := []Memory{
		{ID: "a", Category: "fact", Content: "clean content", Source: "user"},
		{ID: "b", Category: "fact", Content: "path /home/alice/x", Source: "user"},
		{ID: "c", Category: "fact", Content: "remote fact", Source: "sync", SyncOrigin: "remote"},
		{ID: "d", Category: "fact", Content: "dirty fact", Source: "user", SyncDirty: true, ProjectID: &pid},
	}
	result := ClassifyForPreview(memories, "")
	if result.Total != 4 {
		t.Errorf("expected Total=4, got %d", result.Total)
	}
	if result.Syncable != 1 {
		t.Errorf("expected Syncable=1, got %d", result.Syncable)
	}
	if result.Blocked != 1 {
		t.Errorf("expected Blocked=1, got %d", result.Blocked)
	}
	if result.NeedsReview != 2 {
		t.Errorf("expected NeedsReview=2, got %d", result.NeedsReview)
	}
}

func TestClassifyForPreview_Empty(t *testing.T) {
	result := ClassifyForPreview(nil, "")
	if result.Total != 0 {
		t.Errorf("expected Total=0, got %d", result.Total)
	}
	if len(result.Items) != 0 {
		t.Errorf("expected no items, got %d", len(result.Items))
	}
}

func TestClassifyForPreview_V2EpisodicBlocked(t *testing.T) {
	memories := []Memory{
		{
			ID:       "e1",
			Category: "event",
			Content:  "deployed v2 today",
			Source:   "user",
			Metadata: map[string]any{"memory_type": "episodic"},
		},
	}
	result := ClassifyForPreview(memories, "")
	if result.Blocked != 1 {
		t.Errorf("expected Blocked=1 for episodic, got %d", result.Blocked)
	}
	if len(result.Items) == 0 || result.Items[0].Reason != "episodic_local_only" {
		t.Errorf("expected reason=episodic_local_only, got %q", result.Items[0].Reason)
	}
}

func TestClassifyForPreview_V2PrecompactBlocked(t *testing.T) {
	memories := []Memory{
		{
			ID:       "pc1",
			Category: "summary",
			Content:  "precompact snapshot",
			Source:   "precompact_hook",
			Metadata: map[string]any{"memory_type": "operational", "kind": "precompact", "origin": "precompact"},
		},
	}
	result := ClassifyForPreview(memories, "")
	if result.Blocked != 1 {
		t.Errorf("expected Blocked=1 for precompact, got %d", result.Blocked)
	}
	if len(result.Items) == 0 || result.Items[0].Reason != "precompact_local_only" {
		t.Errorf("expected reason=precompact_local_only, got %q", result.Items[0].Reason)
	}
}

func TestClassifyForPreview_V2UserScopeBlocked(t *testing.T) {
	memories := []Memory{
		{
			ID:       "u1",
			Category: "fact",
			Content:  "personal fact",
			Source:   "user",
			Metadata: map[string]any{"memory_type": "semantic", "scope": "user"},
		},
	}
	result := ClassifyForPreview(memories, "")
	if result.Blocked != 1 {
		t.Errorf("expected Blocked=1 for user-scope, got %d (syncable=%d, needs_review=%d)", result.Blocked, result.Syncable, result.NeedsReview)
	}
	if len(result.Items) == 0 {
		t.Fatal("expected at least one item")
	}
	reason := result.Items[0].Reason
	if reason != "user_scope_blocked" && reason != "personal_preference" {
		t.Errorf("expected user_scope_blocked or personal_preference, got %q", reason)
	}
}

func TestClassifyForPreview_V2SemanticProjectSyncable(t *testing.T) {
	memories := []Memory{
		{
			ID:       "s1",
			Category: "decision",
			Content:  "use Go 1.24 for the project",
			Source:   "user",
			Metadata: map[string]any{"memory_type": "semantic", "scope": "project", "origin": "bootstrap"},
		},
	}
	result := ClassifyForPreview(memories, "")
	if result.Syncable != 1 {
		t.Errorf("expected Syncable=1 for bootstrap semantic project memory, got %d", result.Syncable)
	}
}

func TestClassifyForPreview_V2OperationalBlocked(t *testing.T) {
	memories := []Memory{
		{
			ID:       "o1",
			Category: "summary",
			Content:  "operational snapshot",
			Source:   "user",
			Metadata: map[string]any{"memory_type": "operational", "kind": "snapshot"},
		},
	}
	result := ClassifyForPreview(memories, "")
	if result.Blocked != 1 {
		t.Errorf("expected Blocked=1 for operational non-handoff, got %d", result.Blocked)
	}
}

func TestClassifyForPreview_V2DreamSemanticProjectSyncable(t *testing.T) {
	memories := []Memory{
		{
			ID:       "d1",
			Category: "learning",
			Content:  "extracted learning from dream",
			Source:   "dream",
			Metadata: map[string]any{"memory_type": "semantic", "scope": "project", "origin": "dream"},
		},
	}
	result := ClassifyForPreview(memories, "")
	if result.Syncable != 1 {
		t.Errorf("expected Syncable=1 for dream semantic project memory, got %d", result.Syncable)
	}
}
