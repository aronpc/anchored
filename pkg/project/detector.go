package project

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type Project struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	RemoteKey  string  `json:"remote_key,omitempty"`
	SourceTool *string `json:"source_tool,omitempty"`
}

type Detector struct {
	db *sql.DB
}

func NewDetector(db *sql.DB) *Detector {
	return &Detector{db: db}
}

// Detect resolves cwd to the project it belongs to, creating it on first sight.
//
// IDENTITY IS THE GIT ORIGIN, NOT THE PATH. A path answers "where is this
// checkout" — a different question from "which project is this", and the two
// diverge in three ordinary situations:
//
//	linked worktree   same repository, own path
//	local clone       same repository, own path AND own .git
//	moved repository  same checkout, new path
//
// Keying on the path made each of those a fresh project row with an empty
// memory: the bakeoff framework clones a repo per experiment arm, and every arm
// started blind to the 40 memories of the repo it had just cloned. Keying on
// the origin answers all three at once, and needs no special case for any.
//
// The path is still stored — Resolve hands it back, and it is what a human
// recognizes — but it is a LABEL that follows the project, not the key that
// identifies it. When a project is found by origin at a new path, the row moves.
//
// A repository with no origin has no identity beyond where it sits, so it falls
// back to the path. That is the one case where two checkouts of the same work
// stay separate projects, and there is nothing available to tell us otherwise.
func (d *Detector) Detect(cwd string) (*Project, error) {
	gitRoot, err := gitRoot(cwd)
	if err != nil || gitRoot == "" {
		return nil, nil
	}

	gitRoot, err = filepath.Abs(gitRoot)
	if err != nil {
		return nil, err
	}

	canonical, legacy := RemoteKeysFromDir(gitRoot)

	if canonical != "" {
		// Canonical first, legacy second: a row still keyed by the old
		// normalization has to be FOUND before it can be re-keyed, and probing
		// both in one statement would leave the choice to the database.
		for _, chave := range []string{canonical, legacy} {
			if chave == "" {
				continue
			}
			existing, err := d.porRemoteKey(chave)
			if err != nil {
				return nil, err
			}
			if existing == nil {
				continue
			}
			d.reconciliar(existing, canonical, gitRoot)
			return existing, nil
		}
		// Registro anterior ao remote_key: existe, foi encontrado por nenhuma
		// chave, e tem este path. Encontrá-lo aqui é o que faz o backfill —
		// sem este passo o INSERT abaixo colide no UNIQUE de path, e um banco
		// que só precisava ganhar a chave passa a recusar o projeto.
		existing, err := d.porPath(gitRoot)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			d.reconciliar(existing, canonical, gitRoot)
			return existing, nil
		}
		return d.inserir(gitRoot, canonical)
	}

	// Sem origin: a identidade é o caminho, e é só isso que existe.
	existing, err := d.porPath(gitRoot)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}
	return d.inserir(gitRoot, "")
}

const colunasProjeto = "SELECT id, name, path, source_tool, COALESCE(remote_key, '') FROM projects"

// porRemoteKey busca o projeto por chave de origin. Devolve (nil, nil) quando
// não há linha — ausência não é erro para quem vai criar em seguida.
func (d *Detector) porRemoteKey(chave string) (*Project, error) {
	var p Project
	err := d.db.QueryRow(colunasProjeto+" WHERE remote_key = ? LIMIT 1", chave).
		Scan(&p.ID, &p.Name, &p.Path, &p.SourceTool, &p.RemoteKey)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (d *Detector) porPath(caminho string) (*Project, error) {
	var p Project
	err := d.db.QueryRow(colunasProjeto+" WHERE path = ?", caminho).
		Scan(&p.ID, &p.Name, &p.Path, &p.SourceTool, &p.RemoteKey)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// reconciliar alinha a linha encontrada com o que o disco diz agora.
//
// Duas coisas envelhecem: a chave, quando a linha vem da normalização legada, e
// o caminho, quando o projeto foi movido — ou quando quem chamou foi um clone
// efêmero. Ambas são melhor-esforço: falhar em atualizar um RÓTULO não é motivo
// para negar o projeto a quem já o encontrou.
//
// O caminho só avança para um diretório que EXISTE. Sem isso, um clone
// descartável — a árvore de runs do bakeoff é apagada ao fim — deixaria a linha
// apontando para um caminho morto, e Resolve devolveria um projeto que não está
// em lugar nenhum.
func (d *Detector) reconciliar(p *Project, canonical, gitRoot string) {
	if canonical != "" && canonical != p.RemoteKey {
		if _, err := d.db.Exec("UPDATE projects SET remote_key = ? WHERE id = ?", canonical, p.ID); err != nil {
			slog.Debug("remote_key re-key failed; will retry on next detect", "project_id", p.ID, "error", err)
		} else {
			p.RemoteKey = canonical
		}
	}

	if gitRoot == p.Path {
		return
	}
	if _, err := os.Stat(p.Path); err == nil {
		// O caminho guardado ainda existe: quem chamou é um segundo checkout do
		// mesmo repositório, não uma mudança de lugar. O rótulo fica no
		// original, que é o que a pessoa reconhece.
		return
	}
	if _, err := os.Stat(gitRoot); err != nil {
		return
	}
	if _, err := d.db.Exec("UPDATE projects SET path = ? WHERE id = ?", gitRoot, p.ID); err != nil {
		slog.Debug("project path move failed; will retry on next detect", "project_id", p.ID, "error", err)
		return
	}
	p.Path = gitRoot
}

func (d *Detector) inserir(gitRoot, remoteKey string) (*Project, error) {
	name := filepath.Base(gitRoot)
	id := newID()
	if _, err := d.db.Exec(
		"INSERT INTO projects (id, name, path, remote_key) VALUES (?, ?, ?, ?)",
		id, name, gitRoot, remoteKey,
	); err != nil {
		return nil, err
	}
	return &Project{ID: id, Name: name, Path: gitRoot, RemoteKey: remoteKey}, nil
}

func (d *Detector) Resolve(id string) (*Project, error) {
	var p Project
	err := d.db.QueryRow(colunasProjeto+" WHERE id = ?", id).
		Scan(&p.ID, &p.Name, &p.Path, &p.SourceTool, &p.RemoteKey)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func gitRoot(dir string) (string, error) {
	if root := gitCommonRoot(dir); root != "" {
		return root, nil
	}
	out, err := gitOutput(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", nil
	}
	return out, nil
}

// gitCommonRoot resolves the working tree of the shared repository, or "" when
// dir is not inside a git repository.
func gitCommonRoot(dir string) string {
	// --path-format=absolute needs git >= 2.31; without it the common dir comes
	// back relative to the process working directory in the main repository
	// (plain ".git"), so the fallback resolves it against dir.
	common, err := gitOutput(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || common == "" {
		common, err = gitOutput(dir, "rev-parse", "--git-common-dir")
		if err != nil || common == "" {
			return ""
		}
		if !filepath.IsAbs(common) {
			common = filepath.Join(dir, common)
		}
	}
	common = filepath.Clean(common)

	// A bare repository's common dir is the repository itself, with no working
	// tree above it to step up into.
	if filepath.Base(common) != ".git" {
		return common
	}
	return filepath.Dir(common)
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand should never fail on a healthy system, but ignoring the
		// error would silently return an all-zero (non-unique) id.
		panic(err)
	}
	return hex.EncodeToString(b)
}

func getGitRemoteURL(cwd string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

var (
	hostPortRe = regexp.MustCompile(`^([^/:]+):\d+(/.*)?$`)
	leadingWWW = regexp.MustCompile(`^www\.`)
	leadingGit = regexp.MustCompile(`^git@`)
)

// normalizeRemoteURL reduces various git remote URL formats to a canonical
// form (normalization v2). On top of the legacy pipeline it strips a numeric
// host port and a leading "scm/" path segment, so the same repository reached
// over different protocols/ports collapses to one key:
//
//	https://github.com/user/repo.git                   → github.com/user/repo
//	git@github.com:user/repo.git                       → github.com/user/repo
//	ssh://git@github.com/user/repo                      → github.com/user/repo
//	ssh://git@bitbucket.example.com:7999/proj/repo.git → bitbucket.example.com/proj/repo
//	https://bitbucket.example.com/scm/proj/repo.git    → bitbucket.example.com/proj/repo
func normalizeRemoteURL(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimRight(s, "/")
	s = strings.TrimSuffix(s, ".git")

	// ssh://git@host/path → host/path
	if strings.HasPrefix(s, "ssh://") {
		s = strings.TrimPrefix(s, "ssh://")
		s = leadingGit.ReplaceAllString(s, "")
	}

	// git@host:path → host/path
	if strings.Contains(s, "@") && strings.Contains(s, ":") {
		parts := strings.SplitN(s, "@", 2)
		if len(parts) == 2 {
			rest := parts[1]
			idx := strings.Index(rest, ":")
			if idx >= 0 {
				s = rest[:idx] + "/" + rest[idx+1:]
			} else {
				s = rest
			}
		}
	}

	// https:// or http:// host/path → host/path
	if strings.HasPrefix(s, "https://") {
		s = strings.TrimPrefix(s, "https://")
	} else if strings.HasPrefix(s, "http://") {
		s = strings.TrimPrefix(s, "http://")
	}

	s = leadingWWW.ReplaceAllString(s, "")
	s = strings.TrimRight(s, "/")
	s = strings.ToLower(s)

	// v2: strip a numeric port from the host (host:7999/path → host/path).
	if m := hostPortRe.FindStringSubmatch(s); m != nil {
		s = m[1] + m[2]
	}

	// v2: strip a leading "scm/" path segment (host/scm/rest → host/rest),
	// only when scm is the FIRST path segment.
	if idx := strings.Index(s, "/"); idx >= 0 {
		host, rest := s[:idx], s[idx+1:]
		if strings.HasPrefix(rest, "scm/") {
			s = host + "/" + strings.TrimPrefix(rest, "scm/")
		}
	}

	return s
}

// normalizeRemoteURLLegacy is the frozen v1 normalization pipeline, kept verbatim
// so legacy remote_keys (computed before v2) can still be derived for fallback
// resolution against servers that haven't re-keyed yet.
func normalizeRemoteURLLegacy(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimRight(s, "/")
	s = strings.TrimSuffix(s, ".git")

	// ssh://git@host/path → host/path
	if strings.HasPrefix(s, "ssh://") {
		s = strings.TrimPrefix(s, "ssh://")
		s = regexp.MustCompile(`^git@`).ReplaceAllString(s, "")
	}

	// git@host:path → host/path
	if strings.Contains(s, "@") && strings.Contains(s, ":") {
		parts := strings.SplitN(s, "@", 2)
		if len(parts) == 2 {
			rest := parts[1]
			idx := strings.Index(rest, ":")
			if idx >= 0 {
				s = rest[:idx] + "/" + rest[idx+1:]
			} else {
				s = rest
			}
		}
	}

	// https:// or http:// host/path → host/path
	if strings.HasPrefix(s, "https://") {
		s = strings.TrimPrefix(s, "https://")
	} else if strings.HasPrefix(s, "http://") {
		s = strings.TrimPrefix(s, "http://")
	}

	s = regexp.MustCompile(`^www\.`).ReplaceAllString(s, "")
	s = strings.TrimRight(s, "/")
	s = strings.ToLower(s)

	return s
}

// hashNormalized returns the 16-hex-char SHA-256 prefix of a normalized URL,
// or "" when the input is empty.
func hashNormalized(normalized string) string {
	if normalized == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(hash[:8])
}

// DeriveRemoteKeyFromURL returns the canonical (v2) remote_key for a raw git
// remote URL, or "" when empty/unnormalizable. This is the authoritative key
// derivation shared with the sync server mirror.
func DeriveRemoteKeyFromURL(rawURL string) string {
	if strings.TrimSpace(rawURL) == "" {
		return ""
	}
	return hashNormalized(normalizeRemoteURL(rawURL))
}

// DeriveLegacyRemoteKeyFromURL returns the legacy (v1) remote_key for a raw git
// remote URL, or "" when empty/unnormalizable. Used as a fallback so a project
// registered under the old key on a not-yet-rekeyed server is still found.
func DeriveLegacyRemoteKeyFromURL(rawURL string) string {
	if strings.TrimSpace(rawURL) == "" {
		return ""
	}
	return hashNormalized(normalizeRemoteURLLegacy(rawURL))
}

// deriveRemoteKey returns the canonical 16-hex-char remote_key from the git
// remote URL of cwd, or "" if no remote.
func deriveRemoteKey(cwd string) string {
	return DeriveRemoteKeyFromURL(getGitRemoteURL(cwd))
}

// RemoteKeysFromDir returns the (canonical, legacy) remote_keys for the git
// origin of dir. Either may be "" when there is no origin. Callers probe the
// canonical key first, then the legacy one, so a project still registered
// under the old key on a not-yet-rekeyed server is found.
func RemoteKeysFromDir(dir string) (canonical, legacy string) {
	rawURL := getGitRemoteURL(dir)
	if rawURL == "" {
		return "", ""
	}
	return DeriveRemoteKeyFromURL(rawURL), DeriveLegacyRemoteKeyFromURL(rawURL)
}
