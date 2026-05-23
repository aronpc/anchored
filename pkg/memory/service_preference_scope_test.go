package memory

import (
	"context"
	"testing"
)

type capturePreferenceScopeStore struct {
	Store
	saved    Memory
	existing *Memory
}

func (s *capturePreferenceScopeStore) Save(_ context.Context, m Memory) error {
	s.saved = m
	return nil
}

func (s *capturePreferenceScopeStore) FindByContentHash(_ context.Context, _ string, _ *string) (*Memory, error) {
	return s.existing, nil
}

func TestServiceSaveWithOptions_DefaultsPreferenceScopeToUser(t *testing.T) {
	store := &capturePreferenceScopeStore{}
	svc := &Service{store: store}

	m, err := svc.SaveWithOptions(context.Background(), SaveOptions{
		Content:  "Prefer small PRs",
		Category: "preference",
		Source:   "test",
	})
	if err != nil {
		t.Fatalf("SaveWithOptions error: %v", err)
	}

	metadata := ParseMetadata(m.Metadata)
	if metadata.PreferenceScope != PreferenceScopeUser {
		t.Fatalf("returned memory preference scope: got %q want %q", metadata.PreferenceScope, PreferenceScopeUser)
	}

	savedMetadata := ParseMetadata(store.saved.Metadata)
	if savedMetadata.PreferenceScope != PreferenceScopeUser {
		t.Fatalf("saved memory preference scope: got %q want %q", savedMetadata.PreferenceScope, PreferenceScopeUser)
	}
}

func TestServiceSaveWithOptions_UsesExplicitPreferenceScope(t *testing.T) {
	store := &capturePreferenceScopeStore{}
	svc := &Service{store: store}

	m, err := svc.SaveWithOptions(context.Background(), SaveOptions{
		Content:         "We prefer short-lived branches on this project",
		Category:        "preference",
		Source:          "test",
		PreferenceScope: PreferenceScopeProject,
	})
	if err != nil {
		t.Fatalf("SaveWithOptions error: %v", err)
	}

	metadata := ParseMetadata(m.Metadata)
	if metadata.PreferenceScope != PreferenceScopeProject {
		t.Fatalf("returned memory preference scope: got %q want %q", metadata.PreferenceScope, PreferenceScopeProject)
	}
}

func TestServiceSaveWithOptions_DoesNotSetScopeForNonPreference(t *testing.T) {
	store := &capturePreferenceScopeStore{}
	svc := &Service{store: store}

	m, err := svc.SaveWithOptions(context.Background(), SaveOptions{
		Content:         "We chose Postgres",
		Category:        "decision",
		Source:          "test",
		PreferenceScope: PreferenceScopeTeam,
	})
	if err != nil {
		t.Fatalf("SaveWithOptions error: %v", err)
	}

	if m.Metadata != nil {
		t.Fatalf("expected non-preference metadata to remain nil, got %#v", m.Metadata)
	}
}

func TestServiceSaveWithOptions_PreservesExistingMetadataWhenUpdatingDuplicatePreference(t *testing.T) {
	store := &capturePreferenceScopeStore{
		existing: &Memory{
			ID:       "mem_existing",
			Metadata: MemoryMetadata{Source: "import"}.ToAny(),
		},
	}
	svc := &Service{store: store}

	m, err := svc.SaveWithOptions(context.Background(), SaveOptions{
		Content:         "Prefer small PRs",
		Category:        "preference",
		Source:          "test",
		PreferenceScope: PreferenceScopeTeam,
	})
	if err != nil {
		t.Fatalf("SaveWithOptions error: %v", err)
	}

	metadata := ParseMetadata(m.Metadata)
	if metadata.Source != "import" {
		t.Fatalf("expected existing metadata source to be preserved, got %q", metadata.Source)
	}
	if metadata.PreferenceScope != PreferenceScopeTeam {
		t.Fatalf("expected updated preference scope %q, got %q", PreferenceScopeTeam, metadata.PreferenceScope)
	}
}
