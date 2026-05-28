package config

import "testing"

func TestRemoteConfig_Defaults(t *testing.T) {
	cfg := Defaults()
	if cfg.Remote.Enabled {
		t.Error("expected Remote.Enabled=false by default")
	}
	if cfg.Remote.ServerURL != "" {
		t.Errorf("expected Remote.ServerURL empty, got %q", cfg.Remote.ServerURL)
	}
	if cfg.Remote.APIKey != "" {
		t.Errorf("expected Remote.APIKey empty, got %q", cfg.Remote.APIKey)
	}
	if len(cfg.Remote.Projects) != 0 {
		t.Errorf("expected Remote.Projects nil, got %v", cfg.Remote.Projects)
	}
}

func TestCurationConfig_DefaultsEnabled(t *testing.T) {
	cfg := Defaults()
	if !cfg.Curation.Enabled {
		t.Fatal("expected curation worker to be enabled by default")
	}
	if cfg.Curation.IntervalHours != 24 {
		t.Errorf("IntervalHours = %d, want 24", cfg.Curation.IntervalHours)
	}
	if cfg.Curation.IntervalMinutes != 15 {
		t.Errorf("IntervalMinutes = %d, want 15", cfg.Curation.IntervalMinutes)
	}
	if cfg.Curation.Threshold != 0.55 {
		t.Errorf("Threshold = %.2f, want 0.55", cfg.Curation.Threshold)
	}
	if cfg.Curation.MaxUpdates != 50 {
		t.Errorf("MaxUpdates = %d, want 50", cfg.Curation.MaxUpdates)
	}
}
