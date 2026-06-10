package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPluginConfig_SessionStartBudget_Defaults(t *testing.T) {
	var p PluginConfig
	if got := p.SessionStartBudget(); got != 7000 {
		t.Errorf("SessionStartBudget() = %d, want 7000 (nil default)", got)
	}
}

func TestPluginConfig_AutoSaveStopEnabled_Defaults(t *testing.T) {
	var p PluginConfig
	if !p.AutoSaveStopEnabled() {
		t.Error("AutoSaveStopEnabled() = false, want true (nil default)")
	}
}

func TestPluginConfig_AdaptiveReminderEnabled_Defaults(t *testing.T) {
	var p PluginConfig
	if !p.AdaptiveReminderEnabled() {
		t.Error("AdaptiveReminderEnabled() = false, want true (nil default)")
	}
}

func TestPluginConfig_SessionStartBudget_ExplicitZero(t *testing.T) {
	v := 0
	p := PluginConfig{SessionStartBudgetBytes: &v}
	if got := p.SessionStartBudget(); got != 0 {
		t.Errorf("SessionStartBudget() = %d, want 0 (explicit zero)", got)
	}
}

func TestPluginConfig_AutoSaveStop_ExplicitFalse(t *testing.T) {
	f := false
	p := PluginConfig{AutoSaveStop: &f}
	if p.AutoSaveStopEnabled() {
		t.Error("AutoSaveStopEnabled() = true, want false (explicit false)")
	}
}

func TestPluginConfig_AdaptiveReminder_ExplicitFalse(t *testing.T) {
	f := false
	p := PluginConfig{AdaptiveReminder: &f}
	if p.AdaptiveReminderEnabled() {
		t.Error("AdaptiveReminderEnabled() = true, want false (explicit false)")
	}
}

func TestPluginConfig_YAMLRoundTrip_Absent(t *testing.T) {
	// When keys are absent, defaults apply.
	const src = `plugin: {}`
	var cfg Config
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	p := cfg.Plugin
	if p.SessionStartBudgetBytes != nil {
		t.Errorf("SessionStartBudgetBytes should be nil when absent, got %v", p.SessionStartBudgetBytes)
	}
	if p.AutoSaveStop != nil {
		t.Errorf("AutoSaveStop should be nil when absent, got %v", p.AutoSaveStop)
	}
	if p.AdaptiveReminder != nil {
		t.Errorf("AdaptiveReminder should be nil when absent, got %v", p.AdaptiveReminder)
	}
	if got := p.SessionStartBudget(); got != 7000 {
		t.Errorf("SessionStartBudget() = %d, want 7000", got)
	}
	if !p.AutoSaveStopEnabled() {
		t.Error("AutoSaveStopEnabled() = false, want true")
	}
	if !p.AdaptiveReminderEnabled() {
		t.Error("AdaptiveReminderEnabled() = false, want true")
	}
}

func TestPluginConfig_YAMLRoundTrip_ExplicitZeroAndFalse(t *testing.T) {
	const src = `
plugin:
  sessionstart_budget_bytes: 0
  auto_save_stop: false
  adaptive_reminder: false
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	p := cfg.Plugin
	if p.SessionStartBudgetBytes == nil {
		t.Fatal("SessionStartBudgetBytes should be non-nil when set to 0")
	}
	if got := p.SessionStartBudget(); got != 0 {
		t.Errorf("SessionStartBudget() = %d, want 0", got)
	}
	if p.AutoSaveStop == nil {
		t.Fatal("AutoSaveStop should be non-nil when set to false")
	}
	if p.AutoSaveStopEnabled() {
		t.Error("AutoSaveStopEnabled() = true, want false")
	}
	if p.AdaptiveReminder == nil {
		t.Fatal("AdaptiveReminder should be non-nil when set to false")
	}
	if p.AdaptiveReminderEnabled() {
		t.Error("AdaptiveReminderEnabled() = true, want false")
	}
}

func TestPluginConfig_YAMLRoundTrip_ExplicitValues(t *testing.T) {
	const src = `
plugin:
  sessionstart_budget_bytes: 5000
  auto_save_stop: true
  adaptive_reminder: true
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	p := cfg.Plugin
	if got := p.SessionStartBudget(); got != 5000 {
		t.Errorf("SessionStartBudget() = %d, want 5000", got)
	}
	if !p.AutoSaveStopEnabled() {
		t.Error("AutoSaveStopEnabled() = false, want true")
	}
	if !p.AdaptiveReminderEnabled() {
		t.Error("AdaptiveReminderEnabled() = false, want true")
	}
}

func TestPluginConfig_HookBudget_Unchanged(t *testing.T) {
	// Verify HookBudget() is still correct after the struct change.
	var p PluginConfig
	if got := p.HookBudget(); got != 4800 {
		t.Errorf("HookBudget() = %d, want 4800 (default)", got)
	}
	p.HookBudgetBytes = 9600
	if got := p.HookBudget(); got != 9600 {
		t.Errorf("HookBudget() = %d, want 9600", got)
	}
}
