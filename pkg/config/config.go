package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"path"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Memory          MemoryConfig          `yaml:"memory"`
	Embedding       EmbeddingConfig       `yaml:"embedding"`
	Search          SearchConfig          `yaml:"search"`
	Sanitizer       SanitizerConfig       `yaml:"sanitizer"`
	Indexer         IndexerConfig         `yaml:"indexer"`
	Dream           DreamConfig           `yaml:"dream"`
	ContextOptimizer ContextOptimizerConfig `yaml:"context_optimizer"`
	Debug           DebugConfig           `yaml:"debug"`
	Plugin          PluginConfig          `yaml:"plugin"`
	Remote          RemoteConfig          `yaml:"remote"`
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
}

// RemoteEntry is a single named remote server in the remotes map.
type RemoteEntry struct {
	Name      string   `yaml:"-"`
	ServerURL string   `yaml:"server_url"`
	APIKey    string   `yaml:"api_key"`
	Default   bool     `yaml:"default,omitempty"`
	Projects  []string `yaml:"projects,omitempty"`
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
	Enabled         bool `yaml:"enabled"`
	DefaultTTL      int  `yaml:"default_ttl_hours"`
	LRUCapMB        int  `yaml:"lru_cap_mb"`
	SandboxTimeout  int  `yaml:"sandbox_timeout_seconds"`
	MaxOutputKB     int  `yaml:"max_output_kb"`
	FetchCacheTTL   int  `yaml:"fetch_cache_ttl_hours"`
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
	if len(c.Remotes) > 0 {
		for name, entry := range c.Remotes {
			entry.Name = name
			c.Remotes[name] = entry
		}
		return
	}
	if c.Remote.ServerURL != "" {
		c.Remotes = map[string]RemoteEntry{
			"default": {
				Name:      "default",
				ServerURL: c.Remote.ServerURL,
				APIKey:    c.Remote.APIKey,
				Default:   true,
				Projects:  c.Remote.Projects,
			},
		}
	}
}

func (c *Config) ResolveRemote(projectPath string) *RemoteEntry {
	for name, entry := range c.Remotes {
		for _, pattern := range entry.Projects {
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
