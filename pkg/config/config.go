package config

import (
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Memory           MemoryConfig           `yaml:"memory"`
	Embedding        EmbeddingConfig        `yaml:"embedding"`
	Search           SearchConfig           `yaml:"search"`
	Sanitizer        SanitizerConfig        `yaml:"sanitizer"`
	Indexer          IndexerConfig          `yaml:"indexer"`
	Curation         CurationConfig         `yaml:"curation"`
	Dream            DreamConfig            `yaml:"dream"`
	ContextOptimizer ContextOptimizerConfig `yaml:"context_optimizer"`
	Debug            DebugConfig            `yaml:"debug"`
	Plugin           PluginConfig           `yaml:"plugin"`
	Remote           RemoteConfig           `yaml:"remote"`
	// Remotes maps named remotes. When non-empty, takes precedence over Remote.
	Remotes map[string]RemoteEntry `yaml:"remotes,omitempty"`
}

// RemoteConfig controls the remote sync endpoint for team-shared memories.
// All fields default to zero/off — no network calls happen unless explicitly
// configured. The preview command is offline-only and uses the filter pipeline
// without contacting any server.
type RemoteConfig struct {
	Enabled   bool     `yaml:"enabled"`
	ServerURL string   `yaml:"server_url"`
	APIKey    string   `yaml:"api_key"`
	Projects  []string `yaml:"projects"`
	// AutoSync controls whether anchored_save pushes each newly-saved memory
	// to the remote automatically (local-first: local save always succeeds,
	// the remote push is best-effort and async). The sync safety filter still
	// applies, so user-scoped/personal/secret memories never leave the machine.
	// Defaults to true when a remote is configured; set explicitly to false
	// to disable.
	// omitempty keeps an unset value out of the file entirely — marshaling a
	// nil *bool without it writes a confusing "auto_sync: null" (which still
	// means enabled, but reads like something is broken).
	AutoSync *bool `yaml:"auto_sync,omitempty"`
}

// RemoteEntry is a single named remote server in the remotes map.
type RemoteEntry struct {
	Name      string `yaml:"-"`
	ServerURL string `yaml:"server_url"`
	APIKey    string `yaml:"api_key"`
	Default   bool   `yaml:"default,omitempty"`
	// Paths are glob patterns matched against a project's local path to
	// route it to this remote (e.g. "/home/me/work/*"). path.Match
	// semantics: `*` does not cross `/`.
	Paths []string `yaml:"paths,omitempty"`
	// Projects are remote project IDs linked to THIS server via
	// `anchored remote link --remote <name> <id>`. IDs are server-scoped,
	// so each entry carries its own links.
	Projects []string `yaml:"projects,omitempty"`
	AutoSync *bool    `yaml:"auto_sync,omitempty"`
}

// PluginConfig controls how anchored handles drift between the binary version
// and the Claude Code plugin cache. Detection (notification in SessionStart)
// is always on; AutoUpdate flips on the opt-in side effect of fast-forwarding
// the marketplace git mirror and removing the stale cache entry, which lets
// Claude Code re-fetch the plugin on its next launch.
type PluginConfig struct {
	// AutoUpdate=true makes the SessionStart hook fast-forward the
	// marketplace git clone and delete the old cache dir when a newer
	// release is detected. User still has to restart Claude Code, but
	// no /plugin commands are needed.
	AutoUpdate bool `yaml:"auto_update"`
	// MarketplaceDir overrides where the Claude Code marketplace mirror
	// lives. Defaults to ~/.claude/plugins/marketplaces/anchored.
	MarketplaceDir string `yaml:"marketplace_dir"`
	// CacheDir overrides where Claude Code keeps the installed-plugin cache.
	// Defaults to ~/.claude/plugins/cache/anchored/anchored.
	CacheDir string `yaml:"cache_dir"`
	// AutoRecall controls the UserPromptSubmit auto-recall injection:
	//   "off"  — inject only the routing reminder (no retrieval)
	//   "hits" — inject the routing reminder + top relevant memories (default)
	//   "full" — "hits" plus recent artifacts (test reports, stack traces)
	// Empty resolves to "hits".
	AutoRecall string `yaml:"auto_recall"`
	// HookBudgetBytes caps the size of the auto-recall context block. Lowest-
	// relevance hits are dropped to fit rather than truncating mid-content.
	// Empty/0 resolves to 4800 (~1200 tokens).
	HookBudgetBytes int `yaml:"hook_budget_bytes"`
}

// AutoRecallMode resolves the configured mode to one of off|hits|full,
// defaulting to "hits" for any empty/unknown value.
func (p PluginConfig) AutoRecallMode() string {
	switch p.AutoRecall {
	case "off", "hits", "full":
		return p.AutoRecall
	default:
		return "hits"
	}
}

// HookBudget resolves the configured byte budget, defaulting to 4800.
func (p PluginConfig) HookBudget() int {
	if p.HookBudgetBytes > 0 {
		return p.HookBudgetBytes
	}
	return 4800
}

// DebugConfig controls anchored's optional NDJSON event log.
//
// When Enabled is false (the default), no log file is created and no events
// are recorded — the cost of the feature is one struct field. When Enabled is
// true, every hook invocation and every MCP tool call is appended to Path as
// a single JSON line, suitable for `jq` post-mortem analysis.
//
// Env overrides (handled in pkg/debuglog): ANCHORED_DEBUG and
// ANCHORED_DEBUG_PATH always win, so users can flip logging on without
// editing config.yaml.
type DebugConfig struct {
	Enabled bool   `yaml:"enabled"`
	Path    string `yaml:"path"`
}

type ContextOptimizerConfig struct {
	Enabled        bool `yaml:"enabled"`
	DefaultTTL     int  `yaml:"default_ttl_hours"`
	LRUCapMB       int  `yaml:"lru_cap_mb"`
	SandboxTimeout int  `yaml:"sandbox_timeout_seconds"`
	MaxOutputKB    int  `yaml:"max_output_kb"`
	FetchCacheTTL  int  `yaml:"fetch_cache_ttl_hours"`
}

type DreamConfig struct {
	Aggressiveness      string  `yaml:"aggressiveness"`
	DedupThreshold      float64 `yaml:"dedup_threshold"`
	MaxDeletionsPerRun  int     `yaml:"max_deletions_per_run"`
	ContradictionAction string  `yaml:"contradiction_action"`
}

type IndexerConfig struct {
	Enabled  bool     `yaml:"enabled"`
	Paths    []string `yaml:"paths"`
	Interval string   `yaml:"interval"`
}

// CurationConfig controls the lightweight serve-time maintenance pass.
// It is intentionally safe by default: the worker only refreshes lifecycle
// metadata such as quality_score/importance/curation_status. Destructive
// cleanup still requires explicit CLI commands.
type CurationConfig struct {
	Enabled       bool `yaml:"enabled"`
	IntervalHours int  `yaml:"interval_hours"`
	// IntervalMinutes overrides IntervalHours when > 0. Useful for tests and
	// users who want more aggressive local-only maintenance.
	IntervalMinutes int     `yaml:"interval_minutes,omitempty"`
	Threshold       float64 `yaml:"threshold"`
	MaxUpdates      int     `yaml:"max_updates_per_run"`
}

type MemoryConfig struct {
	StorageDir   string `yaml:"storage_dir"`
	DatabasePath string `yaml:"database_path"`
}

type EmbeddingConfig struct {
	Provider   string `yaml:"provider"`
	Model      string `yaml:"model"`
	ModelDir   string `yaml:"model_dir"`
	Quantize   bool   `yaml:"quantize"`
	Dimensions int    `yaml:"dimensions"`
}

type SearchConfig struct {
	VectorWeight              float64 `yaml:"vector_weight"`
	BM25Weight                float64 `yaml:"bm25_weight"`
	MaxResults                int     `yaml:"max_results"`
	MMREnabled                bool    `yaml:"mmr_enabled"`
	MMRLambda                 float64 `yaml:"mmr_lambda"`
	TemporalDecayEnabled      bool    `yaml:"temporal_decay_enabled"`
	TemporalDecayHalfLifeDays int     `yaml:"temporal_decay_half_life_days"`
}

type SanitizerConfig struct {
	Enabled  bool     `yaml:"enabled"`
	Patterns []string `yaml:"patterns"`
}

func Defaults() *Config {
	return &Config{
		Memory: MemoryConfig{
			StorageDir:   "~/.anchored/data",
			DatabasePath: "~/.anchored/data/anchored.db",
		},
		Embedding: EmbeddingConfig{
			Provider:   "onnx",
			Model:      "paraphrase-multilingual-MiniLM-L12-v2",
			ModelDir:   "~/.anchored/data/onnx",
			Quantize:   true,
			Dimensions: 384,
		},
		Search: SearchConfig{
			VectorWeight: 0.7,
			BM25Weight:   0.3,
			MaxResults:   20,
		},
		Sanitizer: SanitizerConfig{
			Enabled: false,
		},
		Curation: CurationConfig{
			Enabled:         true,
			IntervalHours:   24,
			IntervalMinutes: 15,
			Threshold:       0.55,
			MaxUpdates:      50,
		},
		Dream: DreamConfig{
			Aggressiveness:      "moderate",
			DedupThreshold:      0.75,
			MaxDeletionsPerRun:  50,
			ContradictionAction: "flag",
		},
		ContextOptimizer: ContextOptimizerConfig{
			Enabled:        false,
			DefaultTTL:     336,
			LRUCapMB:       50,
			SandboxTimeout: 30,
			MaxOutputKB:    1024,
			FetchCacheTTL:  24,
		},
		Debug: DebugConfig{
			Enabled: false,
			Path:    "~/.anchored/debug.log",
		},
		Plugin: PluginConfig{
			// Default true — same policy as the binary auto-updater.
			// Drift between binary version and installed plugin cache
			// would otherwise silently leave the user without hooks.
			AutoUpdate:     true,
			MarketplaceDir: "~/.claude/plugins/marketplaces/anchored",
			CacheDir:       "~/.claude/plugins/cache/anchored/anchored",
		},
	}
}

func Load(path string) (*Config, error) {
	cfg := Defaults()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Debug("config file not found, using defaults", "path", path)
			return expandPaths(cfg), nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg.migrateRemotes()

	return expandPaths(cfg), nil
}

func expandPaths(cfg *Config) *Config {
	home, err := os.UserHomeDir()
	if err != nil {
		return cfg
	}

	cfg.Memory.StorageDir = expandHome(cfg.Memory.StorageDir, home)
	cfg.Memory.DatabasePath = expandHome(cfg.Memory.DatabasePath, home)
	cfg.Embedding.ModelDir = expandHome(cfg.Embedding.ModelDir, home)
	cfg.Plugin.MarketplaceDir = expandHome(cfg.Plugin.MarketplaceDir, home)
	cfg.Plugin.CacheDir = expandHome(cfg.Plugin.CacheDir, home)

	return cfg
}

func expandHome(path, home string) string {
	if len(path) >= 2 && path[:2] == "~/" {
		return filepath.Join(home, path[2:])
	}
	return path
}

func (c *Config) migrateRemotes() {
	for name, entry := range c.Remotes {
		entry.Name = name
		c.Remotes[name] = entry
	}
	// Merge the legacy singular `remote:` block into the named map as
	// "default" so it keeps resolving alongside named entries. Without
	// this, adding a second (named) server would silently drop the first
	// from routing. An explicit "default" entry in the map wins.
	if c.Remote.ServerURL == "" {
		return
	}
	if _, exists := c.Remotes["default"]; exists {
		return
	}
	hasDefault := false
	for _, entry := range c.Remotes {
		if entry.Default {
			hasDefault = true
			break
		}
	}
	if c.Remotes == nil {
		c.Remotes = map[string]RemoteEntry{}
	}
	c.Remotes["default"] = RemoteEntry{
		Name:      "default",
		ServerURL: c.Remote.ServerURL,
		APIKey:    c.Remote.APIKey,
		Default:   !hasDefault,
		Projects:  c.Remote.Projects,
		AutoSync:  c.Remote.AutoSync,
	}
}

func (c *Config) ResolveRemote(projectPath string) *RemoteEntry {
	for name, entry := range c.Remotes {
		for _, pattern := range entry.Paths {
			if globMatch(pattern, projectPath) {
				entry.Name = name
				return &entry
			}
		}
	}
	for name, entry := range c.Remotes {
		if entry.Default {
			entry.Name = name
			return &entry
		}
	}
	return nil
}

func (e *RemoteEntry) AutoSyncEnabled() bool {
	if e.AutoSync == nil {
		return true
	}
	return *e.AutoSync
}

func globMatch(pattern, s string) bool {
	matched, _ := path.Match(pattern, s)
	return matched
}

func EnsureDirs(cfg *Config) error {
	dirs := []string{
		cfg.Memory.StorageDir,
		cfg.Embedding.ModelDir,
	}

	// Also ensure the parent dir of the database exists.
	if cfg.Memory.DatabasePath != "" {
		dirs = append(dirs, filepath.Dir(cfg.Memory.DatabasePath))
	}

	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	return nil
}
