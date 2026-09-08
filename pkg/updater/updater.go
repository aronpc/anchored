// Package updater performs background self-update from GitHub releases.
//
// On startup the MCP server invokes Run, which checks the latest release,
// downloads the matching tarball, and atomically replaces the running
// binary. The currently executing process keeps the old in-memory image
// (Linux holds the inode open via /proc/self/exe), so the swap is safe;
// the next invocation picks up the new binary.
package updater

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRepo  = "jholhewres/anchored"
	checkTimeout = 8 * time.Second
	dlTimeout    = 90 * time.Second

	// maxReleaseJSON and maxChecksumsBytes bound the two response bodies,
	// which are attacker-controlled if the API or a proxy in front of it is.
	maxReleaseJSON    = 4 << 20
	maxChecksumsBytes = 1 << 20
)

// These are vars rather than consts so tests can point the release lookup at
// a local server instead of GitHub.
var (
	releaseAPIURL    = "https://api.github.com/repos/%s/releases/latest"
	releaseTagAPIURL = "https://api.github.com/repos/%s/releases/tags/%s"

	// maxBinaryBytes bounds the extraction. hdr.Size is attacker-supplied and
	// the digest is not verified until after the payload is on disk, so an
	// adversary who can tamper with the asset stream but not with
	// checksums.txt cannot install code — but could fill the disk. A real
	// release is tens of MB. A var so a test can lower it instead of
	// generating half a gigabyte.
	maxBinaryBytes int64 = 512 << 20
)

// Options controls a single update attempt.
type Options struct {
	Repo           string // GitHub owner/repo. Empty falls back to defaultRepo.
	CurrentVersion string // Semver without leading "v".
	BinPath        string // Path to the binary to replace. Empty resolves via os.Executable.
	Logger         *slog.Logger

	// TargetVersion pins the update to one published release instead of the
	// latest. Accepts "v0.17.0" or "0.17.0". Empty means latest.
	TargetVersion string

	// AlwaysResolve makes Check contact the release API even when a local
	// guard already refused the update, so an interactive caller can report
	// the available version next to the reason it was refused. The
	// background path leaves it false to keep dev-build startups offline.
	AlwaysResolve bool
}

// Run performs a non-blocking self-update. It is safe to invoke from a
// goroutine: any failure is logged and swallowed. Policy lives in Check; Run
// decides only what to log and when to stop.
func Run(ctx context.Context, opts Options) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	res, err := Check(checkCtx, opts)
	if err != nil {
		if errors.Is(err, ErrResolveExecutable) {
			log.Debug("autoupdate: cannot resolve executable", "error", err)
			return
		}
		// Warn, not Debug: silent failures here meant users had no signal
		// when their auto-update was broken (network down, repo renamed,
		// asset matrix changed, GitHub rate-limit). Background goroutine, so
		// noise stays minimal — one line per startup at most.
		log.Warn("autoupdate: check failed", "error", err)
		return
	}

	// Allowlist, not a set of early returns: only BlockNone proceeds. An
	// unrecognized reason still stops here, so a guard added later is not
	// silently bypassed on the unattended path — the same property
	// forceOverridable gives the interactive one.
	if res.Blocked != BlockNone {
		logBlockedUpdate(log, res)
		return
	}

	log.Info("autoupdate: new version available", "current", res.Current, "latest", res.Latest)

	dlCtx, dlCancel := context.WithTimeout(ctx, dlTimeout)
	defer dlCancel()

	if err := Apply(dlCtx, res); err != nil {
		if errors.Is(err, ErrChecksumLookup) {
			// Refuse to install without a verified digest. GoReleaser
			// publishes checksums.txt for every release; if it isn't
			// reachable, treat the download as untrusted and skip the swap.
			log.Warn("autoupdate: checksum lookup failed, skipping install", "error", err)
			return
		}
		log.Warn("autoupdate: install failed", "error", err)
		return
	}

	log.Info("autoupdate: installed, restart MCP server to activate", "version", res.Latest, "path", res.BinPath, "backup", res.BinPath+".prev")
}

func logBlockedUpdate(log *slog.Logger, res Result) {
	switch res.Blocked {
	case BlockEnvDisabled, BlockNoVersion:
		// Deliberately silent: the user asked for no updates.
	case BlockDevBuild:
		// A dev build installed into the canonical dir is an intentional
		// local checkout (make sync-bin); self-updating it would silently
		// revert the developer's working binary back to the release tag.
		log.Debug("autoupdate: dev build, self-update disabled", "current", res.Current)
	case BlockOutsideCanonical:
		log.Debug("autoupdate: skip, binary outside canonical dir", "path", res.BinPath)
	case BlockNotNewer:
		log.Debug("autoupdate: already on latest", "current", res.Current, "latest", res.Latest)
	default:
		log.Warn("autoupdate: refused", "reason", res.Blocked)
	}
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

// fetchRelease returns version, asset URL, asset filename, and the
// checksums.txt URL from a release: the latest one when tag is empty, or that
// exact tag otherwise. The asset filename is needed
// later to look up the right line in checksums.txt; the checksum URL is
// resolved here so we can fail fast if GoReleaser stopped publishing it.
func fetchRelease(ctx context.Context, repo, tag string) (version string, assetURL string, assetName string, checksumsURL string, err error) {
	url := fmt.Sprintf(releaseAPIURL, repo)
	if tag != "" {
		url = fmt.Sprintf(releaseTagAPIURL, repo, tag)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "anchored-updater")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// A missing tag used to surface downstream as "no asset for
		// linux/amd64", which blames the platform for a typo in the tag.
		if tag != "" && resp.StatusCode == http.StatusNotFound {
			return "", "", "", "", fmt.Errorf("release %s not found in %s", tag, repo)
		}
		return "", "", "", "", fmt.Errorf("github releases: HTTP %d", resp.StatusCode)
	}

	var rel ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseJSON)).Decode(&rel); err != nil {
		return "", "", "", "", fmt.Errorf("decode release: %w", err)
	}

	version = strings.TrimPrefix(rel.TagName, "v")
	if version == "" {
		return "", "", "", "", errors.New("empty tag_name in release")
	}
	// tag_name comes from the release document, which is attacker-controlled
	// if the API or a proxy is, and it reaches a copy-pasteable command via
	// the CLI's refusal messages. GoReleaser only ever publishes versions.
	if !semverTag.MatchString(version) {
		return "", "", "", "", fmt.Errorf("release tag_name is not a version: %q", rel.TagName)
	}

	wantSuffix := fmt.Sprintf("_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	for _, a := range rel.Assets {
		if strings.HasSuffix(a.Name, wantSuffix) {
			assetURL = a.BrowserDownloadURL
			assetName = a.Name
			break
		}
	}
	if assetURL == "" {
		return "", "", "", "", fmt.Errorf("no asset for %s/%s with version %s", runtime.GOOS, runtime.GOARCH, version)
	}

	for _, a := range rel.Assets {
		if a.Name == "checksums.txt" {
			checksumsURL = a.BrowserDownloadURL
			break
		}
	}
	if checksumsURL == "" {
		return "", "", "", "", errors.New("checksums.txt not in release assets")
	}
	return version, assetURL, assetName, checksumsURL, nil
}

// fetchChecksum downloads checksums.txt and returns the lowercase hex sha256
// for assetName. The file is small (one line per asset, ~80 bytes each), so
// reading it whole is fine. Format follows GoReleaser/sha256sum convention:
//
//	<hex digest>  <filename>
func fetchChecksum(ctx context.Context, url, assetName string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "anchored-updater")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("checksums.txt: HTTP %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(io.LimitReader(resp.Body, maxChecksumsBytes))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		// Two-field split: digest and filename. GoReleaser uses two spaces;
		// allow any whitespace to stay compatible with sha256sum's output.
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[len(fields)-1] == assetName {
			return strings.ToLower(fields[0]), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read checksums: %w", err)
	}
	return "", fmt.Errorf("checksum not found for %s", assetName)
}

// downloadAndReplace streams the tarball, validates its SHA-256 against
// wantSum, extracts the embedded `anchored` binary, and atomically swaps
// it into dst while keeping the previous binary at dst+".prev" so a bad
// update can be rolled back manually with one rename.
func downloadAndReplace(ctx context.Context, url, dst, wantSum string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "anchored-updater")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	// Tee the raw response body into the hasher so we digest exactly what
	// the upstream signed. gzip.NewReader is allowed to stop reading at the
	// gzip EOF before consuming the whole HTTP body, so we drain the rest
	// after extraction completes — otherwise the digest would be partial.
	hasher := sha256.New()
	tee := io.TeeReader(resp.Body, hasher)

	gz, err := gzip.NewReader(tee)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	tmpPath := dst + ".new"
	tmp, err := createStagingFile(tmpPath)
	if err != nil {
		return err
	}

	written := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			tmp.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "anchored" {
			continue
		}
		if hdr.Size > maxBinaryBytes {
			return abortStaging(tmp, tmpPath,
				fmt.Errorf("tar entry %q declares %d bytes, over the %d limit", hdr.Name, hdr.Size, maxBinaryBytes))
		}
		// Rejected rather than truncated: a clipped binary that happened to
		// match its digest would install and then fail at runtime.
		n, err := io.CopyN(tmp, tr, maxBinaryBytes+1)
		if err != nil && !errors.Is(err, io.EOF) {
			return abortStaging(tmp, tmpPath, fmt.Errorf("write tmp: %w", err))
		}
		if n > maxBinaryBytes {
			return abortStaging(tmp, tmpPath, fmt.Errorf("payload exceeds the %d byte limit", maxBinaryBytes))
		}
		written = true
		break
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close tmp: %w", err)
	}
	if !written {
		os.Remove(tmpPath)
		return errors.New("anchored binary not found in tarball")
	}

	// Drain any trailing bytes the gzip reader didn't consume so the hash
	// covers the full tar.gz payload, then compare.
	// Bounded like the extraction: this writes nowhere, but an endless body
	// would otherwise burn the whole download timeout for nothing.
	if _, err := io.CopyN(io.Discard, tee, maxBinaryBytes+1); err != nil && !errors.Is(err, io.EOF) {
		os.Remove(tmpPath)
		return fmt.Errorf("drain body: %w", err)
	}
	gotSum := hex.EncodeToString(hasher.Sum(nil))
	if gotSum != strings.ToLower(wantSum) {
		os.Remove(tmpPath)
		return fmt.Errorf("checksum mismatch: want %s got %s", wantSum, gotSum)
	}

	// The backup is a hardlink, not a rename: dst keeps existing for the
	// whole operation, so a client spawning `anchored serve` mid-update never
	// finds the path missing. That also makes the single rename below a true
	// atomic swap — dst goes straight from the old inode to the new one.
	prevPath := dst + ".prev"
	backedUp := false
	if _, statErr := os.Stat(dst); statErr == nil {
		// A symlink here would be followed by Remove's caller expectations
		// and could point the "backup" anywhere; refuse instead.
		if fi, err := os.Lstat(prevPath); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if rmErr := os.Remove(tmpPath); rmErr != nil {
				return fmt.Errorf("refusing to install: %s is a symlink (and %s could not be cleaned up: %v)", prevPath, tmpPath, rmErr)
			}
			return fmt.Errorf("refusing to install: %s is a symlink", prevPath)
		}
		if err := os.Remove(prevPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			if rmErr := os.Remove(tmpPath); rmErr != nil {
				return fmt.Errorf("clear stale backup %s: %w (and %s could not be cleaned up: %v)", prevPath, err, tmpPath, rmErr)
			}
			return fmt.Errorf("clear stale backup %s: %w", prevPath, err)
		}
		if err := os.Link(dst, prevPath); err != nil {
			if rmErr := os.Remove(tmpPath); rmErr != nil {
				return fmt.Errorf("backup current: %w (and %s could not be cleaned up: %v)", err, tmpPath, rmErr)
			}
			return fmt.Errorf("backup current: %w", err)
		}
		backedUp = true
	}

	if err := os.Rename(tmpPath, dst); err != nil {
		// Only restore a backup this call actually made. A .prev left by an
		// earlier cycle holds an older binary, and promoting it here would
		// turn a failed install into a silent downgrade.
		if backedUp {
			if rbErr := os.Rename(prevPath, dst); rbErr != nil {
				return fmt.Errorf("rename: %w (and the rollback from %s failed: %v — restore it by hand)", err, prevPath, rbErr)
			}
		}
		if rmErr := os.Remove(tmpPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			return fmt.Errorf("rename: %w (and %s could not be cleaned up: %v)", err, tmpPath, rmErr)
		}
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// abortStaging closes and removes the staging file, folding any cleanup
// failure into the error being reported: a leaked <bin>.new holds an
// unverified payload.
func abortStaging(tmp *os.File, tmpPath string, cause error) error {
	var problems []string
	if err := tmp.Close(); err != nil {
		problems = append(problems, fmt.Sprintf("close %s: %v", tmpPath, err))
	}
	if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		problems = append(problems, fmt.Sprintf("remove %s: %v", tmpPath, err))
	}
	if len(problems) == 0 {
		return cause
	}
	return fmt.Errorf("%w (cleanup also failed: %s)", cause, strings.Join(problems, "; "))
}

// createStagingFile opens the file the new binary is written to, refusing to
// reuse anything already at that path.
//
// SECURITY INVARIANT: this file becomes the installed binary via rename, so
// whoever owns it owns what the machine executes afterwards. O_CREATE alone
// preserves the owner and mode of an existing file, so a regular file planted
// here by another user would receive the genuine release bytes and then be
// renamed into place — still writable by them. O_EXCL refuses that, oNoFollow
// refuses a symlink, and the mode is set on the descriptor rather than left to
// umask. A stale file from a crashed run is removed first, but only after
// Lstat confirms it is not a symlink.
func createStagingFile(tmpPath string) (*os.File, error) {
	if fi, err := os.Lstat(tmpPath); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("refusing to install: %s is a symlink", tmpPath)
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("refusing to install: %s is not a regular file", tmpPath)
		}
		if err := os.Remove(tmpPath); err != nil {
			return nil, fmt.Errorf("remove stale %s: %w", tmpPath, err)
		}
	}

	tmp, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY|oNoFollow, 0o700)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", tmpPath, err)
	}
	if err := tmp.Chmod(0o755); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return nil, abortStaging(tmp, tmpPath, fmt.Errorf("chmod %s: %w", tmpPath, err))
	}
	return tmp, nil
}

// IsDevBuild reports whether v is a local development build: the bare "dev"
// placeholder (go build without ldflags) or a `make build` stamp carrying
// "-dev+g<hash>". Dev builds are excluded from self-update and from plugin
// drift comparison — the git suffix makes both comparisons meaningless.
func IsDevBuild(v string) bool {
	return v == "dev" || strings.Contains(v, "-dev+")
}

// isNewer reports whether latest > current using lexical semver split.
// Pre-release suffixes (-rc1) compare lexicographically; "1.2.3" beats "1.2.3-rc1".
func isNewer(latest, current string) bool {
	a := splitSemver(latest)
	b := splitSemver(current)
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	// Equal numeric parts: stable (no suffix) > pre-release.
	la := preRelease(latest)
	lc := preRelease(current)
	if la == "" && lc != "" {
		return true
	}
	if la != "" && lc == "" {
		return false
	}
	return la > lc
}

func splitSemver(v string) [3]int {
	v = strings.SplitN(v, "-", 2)[0]
	parts := strings.SplitN(v, ".", 3)
	var out [3]int
	for i := 0; i < 3 && i < len(parts); i++ {
		out[i], _ = strconv.Atoi(parts[i])
	}
	return out
}

func preRelease(v string) string {
	if i := strings.Index(v, "-"); i >= 0 {
		return v[i+1:]
	}
	return ""
}
