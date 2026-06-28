package project

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os/exec"
	"testing"

	_ "modernc.org/sqlite"
)

func TestNormalizeRemoteURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"https://github.com/user/repo.git", "github.com/user/repo"},
		{"https://github.com/user/repo", "github.com/user/repo"},
		{"git@github.com:user/repo.git", "github.com/user/repo"},
		{"git@github.com:user/repo", "github.com/user/repo"},
		{"ssh://git@github.com/user/repo.git", "github.com/user/repo"},
		{"ssh://git@github.com/user/repo", "github.com/user/repo"},
		{"http://github.com/user/repo.git", "github.com/user/repo"},
		{"https://www.github.com/user/repo.git", "github.com/user/repo"},
		{"https://GitHub.com/User/Repo.git", "github.com/user/repo"},
		{"git@GitHub.com:User/Repo.git", "github.com/user/repo"},
		{"https://gitlab.com/org/subgroup/repo.git", "gitlab.com/org/subgroup/repo"},
		{"git@gitlab.com:org/subgroup/repo.git", "gitlab.com/org/subgroup/repo"},
		{"https://github.com/user/repo.git/", "github.com/user/repo"},
		{"  https://github.com/user/repo.git  ", "github.com/user/repo"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeRemoteURL(tt.input)
			if got != tt.expected {
				t.Errorf("normalizeRemoteURL(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// TestRemoteKeyParityVectors locks the canonical (v2) and legacy (v1)
// normalization against the exact vectors mirrored in the sync server repo.
// Any divergence here means clients and the server would derive different
// remote_keys for the same repository.
func TestRemoteKeyParityVectors(t *testing.T) {
	canonical := []struct {
		input    string
		expected string
	}{
		{"https://github.com/user/repo.git", "github.com/user/repo"},
		{"git@github.com:user/repo.git", "github.com/user/repo"},
		{"ssh://git@github.com/user/repo", "github.com/user/repo"},
		{"ssh://git@bitbucket.example.com:7999/proj/repo.git", "bitbucket.example.com/proj/repo"},
		{"https://bitbucket.example.com/scm/proj/repo.git", "bitbucket.example.com/proj/repo"},
		{"ssh://git@gitlab.example.com:2222/group/sub/repo.git", "gitlab.example.com/group/sub/repo"},
		{"http://www.example.com/team/repo/", "example.com/team/repo"},
		{"https://example.com/x/scm/y.git", "example.com/x/scm/y"},
	}
	for _, tt := range canonical {
		t.Run("canonical/"+tt.input, func(t *testing.T) {
			if got := normalizeRemoteURL(tt.input); got != tt.expected {
				t.Errorf("normalizeRemoteURL(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}

	legacy := []struct {
		input    string
		expected string
	}{
		{"ssh://git@bitbucket.example.com:7999/proj/repo.git", "bitbucket.example.com:7999/proj/repo"},
		{"https://bitbucket.example.com/scm/proj/repo.git", "bitbucket.example.com/scm/proj/repo"},
	}
	for _, tt := range legacy {
		t.Run("legacy/"+tt.input, func(t *testing.T) {
			if got := normalizeRemoteURLLegacy(tt.input); got != tt.expected {
				t.Errorf("normalizeRemoteURLLegacy(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// TestDeriveRemoteKeyCanonicalConvergence proves the canonical key collapses an
// ssh+port URL and an https+scm URL of the same repo onto one key, while the
// legacy keys differ from it (and from each other).
func TestDeriveRemoteKeyCanonicalConvergence(t *testing.T) {
	const sshURL = "ssh://git@bitbucket.example.com:7999/proj/repo.git"
	const httpURL = "https://bitbucket.example.com/scm/proj/repo.git"

	sshCanonical := DeriveRemoteKeyFromURL(sshURL)
	httpCanonical := DeriveRemoteKeyFromURL(httpURL)
	if sshCanonical == "" || sshCanonical != httpCanonical {
		t.Fatalf("canonical keys should converge: ssh=%q http=%q", sshCanonical, httpCanonical)
	}

	sshLegacy := DeriveLegacyRemoteKeyFromURL(sshURL)
	httpLegacy := DeriveLegacyRemoteKeyFromURL(httpURL)
	if sshLegacy == sshCanonical || httpLegacy == sshCanonical {
		t.Fatalf("legacy keys must differ from canonical: canonical=%q sshLegacy=%q httpLegacy=%q",
			sshCanonical, sshLegacy, httpLegacy)
	}

	if DeriveRemoteKeyFromURL("") != "" || DeriveLegacyRemoteKeyFromURL("") != "" {
		t.Errorf("empty URL must derive empty key")
	}
}

func TestNormalizeRemoteURLSameKey(t *testing.T) {
	urls := []string{
		"https://github.com/user/repo.git",
		"git@github.com:user/repo.git",
		"ssh://git@github.com/user/repo",
		"https://www.GitHub.com/User/Repo.git",
	}

	var expected string
	for i, u := range urls {
		got := normalizeRemoteURL(u)
		if i == 0 {
			expected = got
		} else if got != expected {
			t.Errorf("normalizeRemoteURL(%q) = %q, want %q (same as first)", u, got, expected)
		}
	}
}

func TestDeriveRemoteKeyConsistent(t *testing.T) {
	normalized := "github.com/user/repo"
	hash := sha256.Sum256([]byte(normalized))
	expected := hex.EncodeToString(hash[:8])

	for i := 0; i < 10; i++ {
		hash2 := sha256.Sum256([]byte(normalized))
		got := hex.EncodeToString(hash2[:8])
		if got != expected {
			t.Errorf("inconsistent hash on iteration %d: got %q, want %q", i, got, expected)
		}
	}
}

func TestDeriveRemoteKeyEmptyForNoRemote(t *testing.T) {
	tmp := t.TempDir()
	got := deriveRemoteKey(tmp)
	if got != "" {
		t.Errorf("deriveRemoteKey on dir with no git remote = %q, want empty", got)
	}
}

func TestDetectWithGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	tmp := t.TempDir()
	runGit(t, tmp, "init")
	runGit(t, tmp, "config", "user.email", "test@test.com")
	runGit(t, tmp, "config", "user.name", "Test")

	db := openTestDB(t)
	detector := NewDetector(db)

	p1, err := detector.Detect(tmp)
	if err != nil {
		t.Fatalf("first Detect: %v", err)
	}
	if p1 == nil {
		t.Fatal("first Detect returned nil")
	}
	if p1.ID == "" {
		t.Error("project ID is empty")
	}

	p2, err := detector.Detect(tmp)
	if err != nil {
		t.Fatalf("second Detect: %v", err)
	}
	if p2.ID != p1.ID {
		t.Errorf("second Detect ID = %q, want %q", p2.ID, p1.ID)
	}

	resolved, err := detector.Resolve(p1.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.ID != p1.ID {
		t.Errorf("Resolve ID = %q, want %q", resolved.ID, p1.ID)
	}
}

func TestDetectWithRemoteKey(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	tmp := t.TempDir()
	runGit(t, tmp, "init")
	runGit(t, tmp, "config", "user.email", "test@test.com")
	runGit(t, tmp, "config", "user.name", "Test")
	runGit(t, tmp, "remote", "add", "origin", "https://github.com/user/repo.git")

	db := openTestDB(t)
	detector := NewDetector(db)

	p, err := detector.Detect(tmp)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if p == nil {
		t.Fatal("Detect returned nil")
	}
	if p.RemoteKey == "" {
		t.Error("RemoteKey is empty for repo with remote")
	}

	normalized := "github.com/user/repo"
	hash := sha256.Sum256([]byte(normalized))
	expected := hex.EncodeToString(hash[:8])
	if p.RemoteKey != expected {
		t.Errorf("RemoteKey = %q, want %q", p.RemoteKey, expected)
	}

	resolved, err := detector.Resolve(p.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.RemoteKey != p.RemoteKey {
		t.Errorf("Resolve RemoteKey = %q, want %q", resolved.RemoteKey, p.RemoteKey)
	}
}

func TestDetectNoRemoteKeyForLocalRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	tmp := t.TempDir()
	runGit(t, tmp, "init")
	runGit(t, tmp, "config", "user.email", "test@test.com")
	runGit(t, tmp, "config", "user.name", "Test")

	db := openTestDB(t)
	detector := NewDetector(db)

	p, err := detector.Detect(tmp)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if p == nil {
		t.Fatal("Detect returned nil")
	}
	if p.RemoteKey != "" {
		t.Errorf("RemoteKey = %q for repo with no remote, want empty", p.RemoteKey)
	}
}

func TestDetectBackfillsRemoteKey(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	tmp := t.TempDir()
	runGit(t, tmp, "init")
	runGit(t, tmp, "config", "user.email", "test@test.com")
	runGit(t, tmp, "config", "user.name", "Test")

	db := openTestDB(t)

	// Simulate pre-migration state: project without remote_key
	_, err := db.Exec("INSERT INTO projects (id, name, path) VALUES (?, ?, ?)",
		"test-id", "testname", tmp)
	if err != nil {
		t.Fatalf("manual insert: %v", err)
	}

	runGit(t, tmp, "remote", "add", "origin", "https://github.com/user/repo.git")

	detector := NewDetector(db)
	p, err := detector.Detect(tmp)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if p == nil {
		t.Fatal("Detect returned nil")
	}
	if p.RemoteKey == "" {
		t.Error("RemoteKey not backfilled")
	}

	var rk string
	err = db.QueryRow("SELECT COALESCE(remote_key, '') FROM projects WHERE id = ?", "test-id").Scan(&rk)
	if err != nil {
		t.Fatalf("query remote_key: %v", err)
	}
	if rk != p.RemoteKey {
		t.Errorf("DB remote_key = %q, want %q", rk, p.RemoteKey)
	}
}

// TestDetectRekeysLegacyToCanonical proves a stored project carrying the legacy
// key is updated to the canonical key once its origin now derives a different
// (v2) key — e.g. a bitbucket-style origin where the port/scm rules apply.
func TestDetectRekeysLegacyToCanonical(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	tmp := t.TempDir()
	runGit(t, tmp, "init")
	runGit(t, tmp, "config", "user.email", "test@test.com")
	runGit(t, tmp, "config", "user.name", "Test")
	const origin = "https://bitbucket.example.com/scm/proj/repo.git"
	runGit(t, tmp, "remote", "add", "origin", origin)

	canonical := DeriveRemoteKeyFromURL(origin)
	legacy := DeriveLegacyRemoteKeyFromURL(origin)
	if canonical == legacy {
		t.Fatalf("test vector must have distinct keys, got %q", canonical)
	}

	db := openTestDB(t)
	// Seed a project keyed with the legacy normalization.
	if _, err := db.Exec("INSERT INTO projects (id, name, path, remote_key) VALUES (?, ?, ?, ?)",
		"legacy-id", "repo", tmp, legacy); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	detector := NewDetector(db)
	p, err := detector.Detect(tmp)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if p.RemoteKey != canonical {
		t.Errorf("Detect RemoteKey = %q, want canonical %q", p.RemoteKey, canonical)
	}

	var rk string
	if err := db.QueryRow("SELECT remote_key FROM projects WHERE id = ?", "legacy-id").Scan(&rk); err != nil {
		t.Fatalf("query remote_key: %v", err)
	}
	if rk != canonical {
		t.Errorf("DB remote_key = %q, want canonical %q", rk, canonical)
	}
}

func TestDetectNonGitDirReturnsNil(t *testing.T) {
	tmp := t.TempDir()
	db := openTestDB(t)
	detector := NewDetector(db)

	p, err := detector.Detect(tmp)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if p != nil {
		t.Error("expected nil for non-git directory")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			path TEXT UNIQUE NOT NULL,
			source_tool TEXT,
			remote_key TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_projects_remote_key ON projects(remote_key);
	`)
	if err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}
