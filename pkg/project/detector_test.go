package project

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
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
	db, err := sql.Open("sqlite3", ":memory:")
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

// TestDetect_WorktreeSharesMainProject covers the case a linked worktree used
// to break: --show-toplevel returns the worktree's own path, so the worktree
// got a project row of its own and started with no memory of the repository.
func TestDetect_WorktreeSharesMainProject(t *testing.T) {
	db := openTestDB(t)
	d := NewDetector(db)

	main := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, main, "init", "-q")
	runGit(t, main, "config", "user.email", "t@example.com")
	runGit(t, main, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(main, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, main, "add", "f.txt")
	runGit(t, main, "commit", "-qm", "init")

	mainProject, err := d.Detect(main)
	if err != nil || mainProject == nil {
		t.Fatalf("detect main: %v (project=%v)", err, mainProject)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, main, "worktree", "add", "-q", "--detach", wt)

	wtProject, err := d.Detect(wt)
	if err != nil || wtProject == nil {
		t.Fatalf("detect worktree: %v (project=%v)", err, wtProject)
	}

	if wtProject.ID != mainProject.ID {
		t.Errorf("worktree resolved to project %s, want the main repository's %s",
			wtProject.ID, mainProject.ID)
	}
	if wtProject.Path != mainProject.Path {
		t.Errorf("worktree path = %q, want %q", wtProject.Path, mainProject.Path)
	}

	// A subdirectory of the worktree must resolve the same way.
	sub := filepath.Join(wt, "nested")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	subProject, err := d.Detect(sub)
	if err != nil || subProject == nil {
		t.Fatalf("detect worktree subdir: %v", err)
	}
	if subProject.ID != mainProject.ID {
		t.Errorf("worktree subdir resolved to %s, want %s", subProject.ID, mainProject.ID)
	}

	// Exactly one project row for the repository.
	var rows int
	if err := db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("projects rows = %d, want 1", rows)
	}
}

// TestDetect_SeparateClonesStaySeparate guards the other side of the fix: two
// independent clones are not worktrees and must keep their own projects, so a
// repository checked out for different operational contexts does not get its
// memory merged.
// Repositórios SEM origin ficam separados — é o fallback por caminho, e o
// único caso em que dois checkouts do mesmo trabalho não podem ser reconhecidos
// como um. O nome anterior deste teste dizia "clones", mas ele constrói os
// repositórios com `git init` e nenhum remote: nunca exercitou clone de um
// mesmo remote, e por isso não prendia a garantia que o CHANGELOG anunciava.
func TestDetect_ReposSemOriginFicamSeparados(t *testing.T) {
	db := openTestDB(t)
	d := NewDetector(db)

	var ids []string
	for _, name := range []string{"clone-a", "clone-b"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		runGit(t, dir, "init", "-q")
		p, err := d.Detect(dir)
		if err != nil || p == nil {
			t.Fatalf("detect %s: %v", name, err)
		}
		ids = append(ids, p.ID)
	}
	if ids[0] == ids[1] {
		t.Error("independent clones collapsed into one project")
	}
}

// repoComOrigin cria um repo git com um commit e o origin pedido.
func repoComOrigin(t *testing.T, dir, origin string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "t@example.com")
	runGit(t, dir, "config", "user.name", "t")
	if origin != "" {
		runGit(t, dir, "remote", "add", "origin", origin)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "commit", "-qm", "init")
	return dir
}

// Dois clones do mesmo repositório são o MESMO projeto. É o caso que motivou a
// mudança: o bakeoff faz `git clone --local` por braço de experimento, e cada
// clone tem .git próprio — logo --git-common-dir aponta para si mesmo e não há
// nada no git, além do origin, que ligue o clone ao repositório de onde veio.
// Antes disso cada braço começava cego para as memórias do repo que clonou.
func TestDetect_ClonesDoMesmoRemoteSaoUmProjeto(t *testing.T) {
	db := openTestDB(t)
	d := NewDetector(db)
	const origin = "git@bitbucket.org:newfold/gatorllm.git"

	raiz := t.TempDir()
	principal := repoComOrigin(t, filepath.Join(raiz, "gatorllm"), origin)
	// Cópia da árvore INTEIRA, .git incluído: é o que o bakeoff faz quando o
	// clone falha (ele pede --branch <sha>, que o git recusa). O clone
	// resultante herda o .git/config do original, logo o MESMO origin — que é
	// exatamente a informação que liga um ao outro sem envolver caminho.
	clone := filepath.Join(raiz, "work", "arm-a", "task")
	if err := os.MkdirAll(filepath.Dir(clone), 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", "-a", principal+"/.", clone).CombinedOutput(); err != nil {
		t.Fatalf("copiando arvore: %v\n%s", err, out)
	}
	if got := gitOutputTeste(t, clone, "remote", "get-url", "origin"); got != origin {
		t.Fatalf("a copia devia herdar o origin: %q", got)
	}

	p1, err := d.Detect(principal)
	if err != nil || p1 == nil {
		t.Fatalf("detect principal: %v", err)
	}
	p2, err := d.Detect(clone)
	if err != nil || p2 == nil {
		t.Fatalf("detect clone: %v", err)
	}

	if p1.ID != p2.ID {
		t.Errorf("clone virou projeto separado: %q vs %q (name %q vs %q)",
			p1.ID, p2.ID, p1.Name, p2.Name)
	}
	// O rótulo fica no original: o clone é efêmero e o path guardado tem de
	// continuar sendo o que uma pessoa reconhece.
	if p2.Path != principal {
		t.Errorf("path migrou para o clone: %q, esperava %q", p2.Path, principal)
	}
	if n := contarProjetos(t, db); n != 1 {
		t.Errorf("%d linhas em projects, esperava 1", n)
	}
}

// Repositório MOVIDO continua o mesmo projeto, e a linha acompanha o novo
// lugar. Com identidade por path, renomear um diretório abandonava o histórico
// inteiro e criava um projeto vazio no caminho novo.
func TestDetect_RepositorioMovidoMantemOProjeto(t *testing.T) {
	db := openTestDB(t)
	d := NewDetector(db)

	raiz := t.TempDir()
	antes := repoComOrigin(t, filepath.Join(raiz, "antes"), "git@github.com:user/repo.git")
	p1, err := d.Detect(antes)
	if err != nil || p1 == nil {
		t.Fatalf("detect antes: %v", err)
	}

	depois := filepath.Join(raiz, "depois")
	if err := os.Rename(antes, depois); err != nil {
		t.Fatal(err)
	}

	p2, err := d.Detect(depois)
	if err != nil || p2 == nil {
		t.Fatalf("detect depois: %v", err)
	}
	if p1.ID != p2.ID {
		t.Errorf("mover o repo criou projeto novo: %q vs %q", p1.ID, p2.ID)
	}
	if p2.Path != depois {
		t.Errorf("path ficou no lugar antigo: %q, esperava %q", p2.Path, depois)
	}
	if n := contarProjetos(t, db); n != 1 {
		t.Errorf("%d linhas em projects, esperava 1", n)
	}
}

// Sem origin não há identidade além do lugar, e dois diretórios seguem
// separados. É o único caso em que dois checkouts do mesmo trabalho não podem
// ser reconhecidos como um — e não há nada disponível para dizer o contrário.
func TestDetect_SemOriginCaiNoPath(t *testing.T) {
	db := openTestDB(t)
	d := NewDetector(db)

	raiz := t.TempDir()
	a := repoComOrigin(t, filepath.Join(raiz, "a"), "")
	b := repoComOrigin(t, filepath.Join(raiz, "b"), "")

	pa, err := d.Detect(a)
	if err != nil || pa == nil {
		t.Fatalf("detect a: %v", err)
	}
	pb, err := d.Detect(b)
	if err != nil || pb == nil {
		t.Fatalf("detect b: %v", err)
	}
	if pa.ID == pb.ID {
		t.Error("repos sem origin colapsaram num projeto")
	}
	if pa.RemoteKey != "" || pb.RemoteKey != "" {
		t.Errorf("remote_key deveria ser vazio: %q e %q", pa.RemoteKey, pb.RemoteKey)
	}
}

// Worktree linkado casa pelo ORIGIN, sem depender de --git-common-dir. O caso
// já era coberto pela resolução de gitRoot; este teste prende o fato de que a
// identidade por origin o resolve sozinha — se o gitRoot voltar a devolver o
// path do worktree, o projeto continua um só.
func TestDetect_WorktreeCasaPeloOrigin(t *testing.T) {
	db := openTestDB(t)
	d := NewDetector(db)

	raiz := t.TempDir()
	principal := repoComOrigin(t, filepath.Join(raiz, "repo"), "git@github.com:user/repo.git")
	p1, err := d.Detect(principal)
	if err != nil || p1 == nil {
		t.Fatalf("detect principal: %v", err)
	}

	wt := filepath.Join(raiz, "wt")
	runGit(t, principal, "worktree", "add", "-q", "--detach", wt)

	p2, err := d.Detect(wt)
	if err != nil || p2 == nil {
		t.Fatalf("detect worktree: %v", err)
	}
	if p1.ID != p2.ID {
		t.Errorf("worktree virou projeto separado: %q vs %q", p1.ID, p2.ID)
	}
	if n := contarProjetos(t, db); n != 1 {
		t.Errorf("%d linhas em projects, esperava 1", n)
	}
}

func contarProjetos(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func gitOutputTeste(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// A decisão declarada no CHANGELOG: dois checkouts do mesmo repositório são o
// mesmo projeto, mesmo em diretórios sem relação nenhuma entre si. Este é o
// teste que faltava — a versão anterior afirmava o contrário em prosa e não
// tinha nada que o verificasse.
func TestDetect_CheckoutsDoMesmoRemoteSaoUmProjeto(t *testing.T) {
	db := openTestDB(t)
	d := NewDetector(db)
	const origin = "git@github.com:user/mesmo-repo.git"

	raiz := t.TempDir()
	a := repoComOrigin(t, filepath.Join(raiz, "checkout-a"), origin)
	b := repoComOrigin(t, filepath.Join(raiz, "outro", "lugar", "checkout-b"), origin)

	pa, err := d.Detect(a)
	if err != nil || pa == nil {
		t.Fatalf("detect a: %v", err)
	}
	pb, err := d.Detect(b)
	if err != nil || pb == nil {
		t.Fatalf("detect b: %v", err)
	}
	if pa.ID != pb.ID {
		t.Errorf("checkouts do mesmo remote viraram projetos distintos: %q vs %q", pa.ID, pb.ID)
	}
	if n := contarProjetos(t, db); n != 1 {
		t.Errorf("%d linhas em projects, esperava 1", n)
	}
}

// Remotes DIFERENTES continuam projetos diferentes: a identidade é o remote, e
// dois repositórios que não são o mesmo não podem colapsar por acidente de
// chave vazia ou de normalização.
func TestDetect_RemotesDiferentesNaoColapsam(t *testing.T) {
	db := openTestDB(t)
	d := NewDetector(db)

	raiz := t.TempDir()
	a := repoComOrigin(t, filepath.Join(raiz, "a"), "git@github.com:user/um.git")
	b := repoComOrigin(t, filepath.Join(raiz, "b"), "git@github.com:user/outro.git")

	pa, _ := d.Detect(a)
	pb, _ := d.Detect(b)
	if pa == nil || pb == nil {
		t.Fatal("detect devolveu nil")
	}
	if pa.ID == pb.ID {
		t.Error("repositorios distintos colapsaram num projeto")
	}
	if n := contarProjetos(t, db); n != 2 {
		t.Errorf("%d linhas em projects, esperava 2", n)
	}
}
