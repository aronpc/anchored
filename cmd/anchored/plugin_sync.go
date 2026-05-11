package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jholhewres/anchored/pkg/config"
)

// gitFastForwardTimeout caps how long the SessionStart hook can spend pulling
// the marketplace mirror. Unreachable remote, hung TCP, prompt for
// credentials — any of those would otherwise block Claude Code's launch
// indefinitely. Hooks are best-effort, so missing a sync is fine.
const gitFastForwardTimeout = 10 * time.Second

// syncLockPath is the advisory lock acquired before mutating the marketplace
// mirror or plugin cache. Concurrent SessionStart firings (two Claude Code
// windows opening at once) compete for this lock; whichever loses skips the
// sync silently and falls back to the manual-fix notice.
func syncLockPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".anchored", "plugin_sync.lock")
}

// PluginDrift describes two independent staleness signals between the
// running binary and the user's Claude Code plugin state:
//
//   - MirrorBehind: the marketplace git clone has not pulled the commits
//     that ship the current binary version. Anchored can fix this by itself
//     with a fast-forward `git pull`.
//   - CacheBehind:  the installed plugin cache directory lags behind the
//     mirror (or is missing entirely). Anchored CANNOT safely fix this on
//     its own — installed_plugins.json belongs to Claude Code and rewriting
//     it without a documented API would be a footgun. Instead we tell the
//     user to run `/plugin install anchored@anchored`, which is idempotent
//     and the canonical entry point.
//
// HasDrift = MirrorBehind || CacheBehind. Either implies the user is
// running with older hooks/skills than the binary expects.
type PluginDrift struct {
	BinaryVersion  string // binary's compile-time Version (set via ldflags)
	MirrorVersion  string // version field of mirror's plugin.json; "" when unreadable
	CacheVersion   string // newest semver dir under CacheDir; "" when absent
	HasDrift       bool
	MirrorBehind   bool // mirror lags binary (anchored can fix via git pull)
	CacheBehind    bool // cache lags mirror/binary (user must run /plugin install)
	MarketplaceDir string
	CacheDir       string
	SyncPerformed  bool   // git pull was actually run and succeeded
	SyncError      string // non-empty when AutoUpdate tried and failed
}

// detectPluginDrift compares the binary version with (a) the marketplace
// mirror's plugin.json and (b) the cache directory contents. Either being
// behind the binary counts as drift. The hook is best-effort: any IO or
// parse failure is silently treated as "no signal" rather than propagated.
func detectPluginDrift(cfg *config.Config, binaryVersion string) PluginDrift {
	d := PluginDrift{
		BinaryVersion:  binaryVersion,
		MarketplaceDir: cfg.Plugin.MarketplaceDir,
		CacheDir:       cfg.Plugin.CacheDir,
	}
	if binaryVersion == "" || binaryVersion == "dev" {
		// "dev" placeholder = local `go build` without ldflags. Drift
		// comparison is meaningless then.
		return d
	}

	d.MirrorVersion = readMirrorPluginVersion(cfg.Plugin.MarketplaceDir)
	d.CacheVersion = newestInstalledVersion(cfg.Plugin.CacheDir)

	if d.MirrorVersion != "" && compareSemver(d.MirrorVersion, binaryVersion) < 0 {
		d.MirrorBehind = true
	}
	// Cache behind = cache absent OR cache older than binary. We compare to
	// the binary (not the mirror) because the mirror may itself be stale and
	// about to be fast-forwarded; the binary is the authoritative target.
	if d.CacheVersion == "" || compareSemver(d.CacheVersion, binaryVersion) < 0 {
		d.CacheBehind = true
	}
	d.HasDrift = d.MirrorBehind || d.CacheBehind
	return d
}

// readMirrorPluginVersion extracts the version from the marketplace mirror's
// `.claude-plugin/plugin.json`. Returns "" if the mirror dir or file is
// missing, or if the version field is empty.
func readMirrorPluginVersion(mirrorDir string) string {
	if mirrorDir == "" {
		return ""
	}
	v, err := pluginManifestVersion(filepath.Join(mirrorDir, ".claude-plugin", "plugin.json"))
	if err != nil {
		return ""
	}
	return v
}

// newestInstalledVersion walks CacheDir for sub-directories whose name parses
// as a semver and returns the highest. Returns "" when the directory does
// not exist, no version dirs are present, or none parse cleanly.
func newestInstalledVersion(cacheDir string) string {
	if cacheDir == "" {
		return ""
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return ""
	}
	var best string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		v := strings.TrimPrefix(e.Name(), "v")
		if !looksLikeSemver(v) {
			continue
		}
		// Confirm a plugin.json lives inside — protects against half-installed
		// or unrelated directories sharing the cache root.
		if _, err := os.Stat(filepath.Join(cacheDir, e.Name(), ".claude-plugin", "plugin.json")); err != nil {
			continue
		}
		if best == "" || compareSemver(best, v) < 0 {
			best = v
		}
	}
	return best
}

// looksLikeSemver requires EXACTLY three numeric segments before any optional
// "-prerelease" suffix. Strings like "0.4.6.extra", "1.2", "abc.def.ghi", and
// "0.4.6foo" are all rejected so compareSemver never sees garbage.
func looksLikeSemver(v string) bool {
	parts := strings.SplitN(v, "-", 2)
	dots := strings.Split(parts[0], ".")
	if len(dots) != 3 {
		return false
	}
	for _, d := range dots {
		if d == "" {
			return false
		}
		if _, err := strconv.Atoi(d); err != nil {
			return false
		}
	}
	return true
}

// compareSemver returns -1/0/1 for a < b / a == b / a > b. Only the X.Y.Z
// numeric part is compared; pre-release suffixes are ignored on purpose to
// avoid confusing rc1 < rc2 lexical edge cases when a release goes out.
func compareSemver(a, b string) int {
	pa := semverTriple(a)
	pb := semverTriple(b)
	for i := 0; i < 3; i++ {
		if pa[i] < pb[i] {
			return -1
		}
		if pa[i] > pb[i] {
			return 1
		}
	}
	return 0
}

// semverTriple parses X.Y.Z into three ints via strconv.Atoi. Pre-release
// suffixes and stray characters yield 0 for that position (still total order
// preserving relative to clean versions). Overflow is handled by Atoi.
func semverTriple(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	v = strings.SplitN(v, "-", 2)[0]
	parts := strings.SplitN(v, ".", 3)
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			// Best-effort: trim trailing non-digits so "6.extra" still
			// extracts 6 (matches the previous behavior).
			trimmed := parts[i]
			for j, r := range trimmed {
				if r < '0' || r > '9' {
					trimmed = trimmed[:j]
					break
				}
			}
			if trimmed == "" {
				out[i] = 0
				continue
			}
			n, _ = strconv.Atoi(trimmed)
		}
		out[i] = n
	}
	return out
}

// applyPluginAutoUpdate runs when config.Plugin.AutoUpdate is true and the
// marketplace mirror is behind the binary. It performs a fast-forward git
// pull and nothing else — the cache directory and Claude Code's
// installed_plugins.json registry are intentionally left alone, because
// they're Claude Code's state and rewriting them without a documented API
// caused the v0.4.7/v0.4.8 "ghost install at non-existent path" regression.
//
// After a successful sync the user still has to run
// `/plugin install anchored@anchored` (idempotent) to pick up the new files,
// which is exactly what the rendered notice instructs.
//
// Safety:
//   - `git pull --ff-only` runs under a 10s context timeout with
//     GIT_TERMINAL_PROMPT=0, GIT_ASKPASS=/bin/true, SSH_ASKPASS=/bin/true,
//     GIT_OPTIONAL_LOCKS=0 — unreachable remote, missing credential, or
//     askpass GUI cannot hang the SessionStart hook.
//   - Advisory flock at ~/.anchored/plugin_sync.lock prevents two SessionStart
//     firings (two Claude Code windows) from racing on the mirror.
//   - Failures become SyncError on the returned struct; the hook never aborts.
func applyPluginAutoUpdate(d PluginDrift) PluginDrift {
	if !d.MirrorBehind {
		// Nothing for anchored to do. CacheBehind without MirrorBehind is
		// purely a user-side action (/plugin install); we still want the
		// notice to render so the caller keeps the struct as-is.
		return d
	}

	unlock, locked := tryAcquireSyncLock()
	if !locked {
		d.SyncError = "another anchored sync is in progress; skipping"
		return d
	}
	defer unlock()

	if err := gitFastForward(d.MarketplaceDir); err != nil {
		d.SyncError = "git pull failed: " + err.Error()
		return d
	}
	d.SyncPerformed = true
	// Re-read the mirror version so the notice reflects what the user will
	// actually get when they run /plugin install.
	if v := readMirrorPluginVersion(d.MarketplaceDir); v != "" {
		d.MirrorVersion = v
	}
	return d
}

// tryAcquireSyncLock is OS-specific:
//   - plugin_sync_unix.go uses syscall.Flock (Linux + macOS + BSD)
//   - plugin_sync_windows.go is a permissive noop
//
// Both honor the same contract: return (releaseFn, true) when the caller
// holds an exclusive lock on ~/.anchored/plugin_sync.lock; (nop, false)
// when someone else is mutating the plugin cache.

func gitFastForward(dir string) error {
	if dir == "" {
		return fmt.Errorf("marketplace dir empty")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return fmt.Errorf("not a git repo: %s", dir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitFastForwardTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "pull", "--ff-only", "--quiet")
	cmd.Dir = dir
	// Strip anything that could open an interactive prompt: no terminal
	// prompts, no SSH askpass GUI, no credential helper UI. If auth is
	// required and not pre-cached, we'd rather fail fast and tell the user
	// than hang the hook.
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/true",
		"SSH_ASKPASS=/bin/true",
		"GIT_OPTIONAL_LOCKS=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("timeout after %s", gitFastForwardTimeout)
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// renderPluginUpdateNotice builds an XML snippet for the SessionStart
// additionalContext when drift is detected. Shape mirrors the rest of the
// anchored bundle (XML-tagged sections) so the agent can route on a stable
// element name. The notice tells the user what anchored already did (git
// pull, when MirrorBehind+AutoUpdate) and what the user still needs to do
// (/plugin install + restart).
func renderPluginUpdateNotice(d PluginDrift) string {
	if !d.HasDrift {
		return ""
	}

	cacheAttr := d.CacheVersion
	if cacheAttr == "" {
		cacheAttr = "absent"
	}

	var sb strings.Builder
	sb.WriteString("<anchored_plugin_update")
	fmt.Fprintf(&sb, " binary=%q mirror=%q cache=%q", d.BinaryVersion, d.MirrorVersion, cacheAttr)
	if d.SyncPerformed {
		sb.WriteString(" mirror_synced=\"true\"")
	}
	sb.WriteString(">\n")

	switch {
	case d.SyncError != "":
		// Auto-sync was attempted and failed; fall back to manual.
		errMsg := truncateUTF8(d.SyncError, 200)
		fmt.Fprintf(&sb, "  Auto-sync failed: %s\n", escapeText(errMsg))
		sb.WriteString("  Manual fix: /plugin marketplace update anchored && /plugin install anchored@anchored — then restart Claude Code.\n")
	case d.SyncPerformed:
		// Mirror was just fast-forwarded; cache still needs Claude Code action.
		fmt.Fprintf(&sb, "  Marketplace mirror auto-synced to v%s.\n", escapeText(d.MirrorVersion))
		sb.WriteString("  Next: run `/plugin install anchored@anchored` then restart Claude Code to load the new hooks.\n")
	case d.MirrorBehind:
		// AutoUpdate is off and the mirror is stale; user has to drive both steps.
		sb.WriteString("  Plugin is out of date. Run: /plugin marketplace update anchored && /plugin install anchored@anchored — then restart Claude Code.\n")
	default:
		// Only CacheBehind: mirror is current, cache lags. /plugin install is enough.
		sb.WriteString("  Plugin cache is stale. Run `/plugin install anchored@anchored` then restart Claude Code.\n")
	}
	sb.WriteString("</anchored_plugin_update>")
	return sb.String()
}

// truncateUTF8 caps a string at maxRunes runes, appending an ellipsis when
// truncated. Used on user-visible error messages to keep notices bounded.
func truncateUTF8(s string, maxRunes int) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	r := []rune(s)
	return string(r[:maxRunes]) + "…"
}

// pluginManifestVersion reads the version field from a plugin.json. Used by
// tests; production code goes through newestInstalledVersion which is more
// resilient to half-installed states.
func pluginManifestVersion(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var doc struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", err
	}
	return doc.Version, nil
}
