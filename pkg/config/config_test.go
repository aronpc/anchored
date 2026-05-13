package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaults_ReturnsValidConfig(t *testing.T) {
	cfg := Defaults()
	if cfg == nil {
		t.Fatal("Defaults() returned nil")
	}

	// Memory defaults.
	if cfg.Memory.StorageDir == "" {
		t.Error("Memory.StorageDir is empty")
	}
	if cfg.Memory.DatabasePath == "" {
		t.Error("Memory.DatabasePath is empty")
	}

	// Embedding defaults.
	if cfg.Embedding.Provider != "onnx" {
		t.Errorf("Embedding.Provider = %q, want %q", cfg.Embedding.Provider, "onnx")
	}
	if cfg.Embedding.Dimensions != 384 {
		t.Errorf("Embedding.Dimensions = %d, want 384", cfg.Embedding.Dimensions)
	}

	// Search defaults.
	if cfg.Search.VectorWeight != 0.7 {
		t.Errorf("Search.VectorWeight = %f, want 0.7", cfg.Search.VectorWeight)
	}
	if cfg.Search.BM25Weight != 0.3 {
		t.Errorf("Search.BM25Weight = %f, want 0.3", cfg.Search.BM25Weight)
	}
	if cfg.Search.MaxResults != 20 {
		t.Errorf("Search.MaxResults = %d, want 20", cfg.Search.MaxResults)
	}

	// Dream defaults.
	if cfg.Dream.Aggressiveness != "moderate" {
		t.Errorf("Dream.Aggressiveness = %q, want %q", cfg.Dream.Aggressiveness, "moderate")
	}

	// Debug defaults.
	if cfg.Debug.Enabled {
		t.Error("Debug.Enabled should be false by default")
	}

	// Plugin defaults.
	if !cfg.Plugin.AutoUpdate {
		t.Error("Plugin.AutoUpdate should be true by default")
	}
}

func TestLoad_ParsesYAMLFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	content := `
memory:
  storage_dir: /tmp/anchored-test
  database_path: /tmp/anchored-test/anchored.db
embedding:
  provider: onnx
  dimensions: 256
search:
  max_results: 10
debug:
  enabled: true
  path: /tmp/anchored-debug.log
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Memory.StorageDir != "/tmp/anchored-test" {
		t.Errorf("StorageDir = %q, want %q", cfg.Memory.StorageDir, "/tmp/anchored-test")
	}
	if cfg.Embedding.Dimensions != 256 {
		t.Errorf("Dimensions = %d, want 256", cfg.Embedding.Dimensions)
	}
	if cfg.Search.MaxResults != 10 {
		t.Errorf("MaxResults = %d, want 10", cfg.Search.MaxResults)
	}
	if !cfg.Debug.Enabled {
		t.Error("Debug.Enabled should be true")
	}
}

func TestLoad_ExpandsTildePaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.yaml")

	content := `
memory:
  storage_dir: ~/anchored-test-data
  database_path: ~/anchored-test-data/anchored.db
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	wantDir := filepath.Join(home, "anchored-test-data")
	if cfg.Memory.StorageDir != wantDir {
		t.Errorf("StorageDir = %q, want %q", cfg.Memory.StorageDir, wantDir)
	}

	wantDB := filepath.Join(home, "anchored-test-data", "anchored.db")
	if cfg.Memory.DatabasePath != wantDB {
		t.Errorf("DatabasePath = %q, want %q", cfg.Memory.DatabasePath, wantDB)
	}
}

func TestLoad_MissingFile_ReturnsDefaults(t *testing.T) {
	cfg, err := Load("/tmp/anchored_missing_config_12345.yaml")
	if err != nil {
		t.Fatalf("Load(missing) returned error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load(missing) returned nil config")
	}

	// Should return defaults with expanded paths.
	if cfg.Memory.StorageDir == "" {
		t.Error("expected default StorageDir from missing file")
	}
}
