package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeRelease serves a releases/latest payload whose asset matrix matches the
// running GOOS/GOARCH, plus a checksums.txt entry, and rewires the release API
// seam at it for the duration of the test.
func fakeRelease(t *testing.T, version string) (assetName string) {
	t.Helper()
	assetName = fmt.Sprintf("anchored_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", strings.Repeat("a", 64), assetName)
	})
	mux.HandleFunc("/release", func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]any{
			"tag_name": "v" + version,
			"assets": []map[string]string{
				{"name": assetName, "browser_download_url": srv.URL + "/" + assetName},
				{"name": "checksums.txt", "browser_download_url": srv.URL + "/checksums.txt"},
			},
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode release: %v", err)
		}
	})

	orig := releaseAPIURL
	releaseAPIURL = srv.URL + "/release?repo=%s"
	t.Cleanup(func() { releaseAPIURL = orig })
	return assetName
}

// canonicalBin returns a path inside a fake $HOME that satisfies Check's
// canonical-directory guard.
func canonicalBin(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".anchored", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "anchored")
}

func TestCheck_BlockedNoVersion(t *testing.T) {
	res, err := Check(context.Background(), Options{BinPath: canonicalBin(t)})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Blocked != BlockNoVersion {
		t.Fatalf("Blocked = %q, want %q", res.Blocked, BlockNoVersion)
	}
}

func TestCheck_BlockedEnvDisabled(t *testing.T) {
	t.Setenv("ANCHORED_NO_AUTOUPDATE", "1")
	res, err := Check(context.Background(), Options{
		CurrentVersion: "0.17.0",
		BinPath:        canonicalBin(t),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Blocked != BlockEnvDisabled {
		t.Fatalf("Blocked = %q, want %q", res.Blocked, BlockEnvDisabled)
	}
}

func TestCheck_BlockedDevBuild(t *testing.T) {
	res, err := Check(context.Background(), Options{
		CurrentVersion: "0.17.0-dev+gc1d9b3c",
		BinPath:        canonicalBin(t),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Blocked != BlockDevBuild {
		t.Fatalf("Blocked = %q, want %q", res.Blocked, BlockDevBuild)
	}
	// A local guard must short-circuit before any network call, so the
	// background path stays offline on a developer's machine.
	if res.Latest != "" {
		t.Fatalf("Latest = %q, want empty without AlwaysResolve", res.Latest)
	}
}

func TestCheck_BlockedOutsideCanonicalDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	res, err := Check(context.Background(), Options{
		CurrentVersion: "0.17.0",
		BinPath:        filepath.Join("/usr", "local", "bin", "anchored"),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Blocked != BlockOutsideCanonical {
		t.Fatalf("Blocked = %q, want %q", res.Blocked, BlockOutsideCanonical)
	}
}

func TestCheck_BlockedNotNewer(t *testing.T) {
	fakeRelease(t, "0.18.0")
	res, err := Check(context.Background(), Options{
		CurrentVersion: "0.18.0",
		BinPath:        canonicalBin(t),
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Blocked != BlockNotNewer {
		t.Fatalf("Blocked = %q, want %q", res.Blocked, BlockNotNewer)
	}
	// not-newer is decided after the release resolves, so the numbers a
	// caller would report are present even though the update is refused.
	if res.Latest != "0.18.0" {
		t.Fatalf("Latest = %q, want 0.18.0", res.Latest)
	}
}

func TestCheck_UpdateAvailable(t *testing.T) {
	assetName := fakeRelease(t, "0.18.0")
	bin := canonicalBin(t)
	res, err := Check(context.Background(), Options{
		CurrentVersion: "0.17.0",
		BinPath:        bin,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Blocked != BlockNone {
		t.Fatalf("Blocked = %q, want none", res.Blocked)
	}
	if !res.Newer {
		t.Fatal("Newer = false, want true")
	}
	if res.Latest != "0.18.0" || res.Current != "0.17.0" {
		t.Fatalf("versions = %q → %q, want 0.17.0 → 0.18.0", res.Current, res.Latest)
	}
	if res.AssetName != assetName {
		t.Fatalf("AssetName = %q, want %q", res.AssetName, assetName)
	}
	if res.AssetURL == "" || res.ChecksumsURL == "" {
		t.Fatalf("asset/checksums URL empty: %+v", res)
	}
	if res.BinPath != bin {
		t.Fatalf("BinPath = %q, want %q", res.BinPath, bin)
	}
}

// AlwaysResolve is what lets an interactive caller report "you are on a dev
// build AND 0.18.0 is out" in one line, instead of one or the other.
func TestCheck_AlwaysResolveReportsLatestWhenBlocked(t *testing.T) {
	fakeRelease(t, "0.18.0")
	res, err := Check(context.Background(), Options{
		CurrentVersion: "0.17.0-dev+gc1d9b3c",
		BinPath:        canonicalBin(t),
		AlwaysResolve:  true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Blocked != BlockDevBuild {
		t.Fatalf("Blocked = %q, want %q", res.Blocked, BlockDevBuild)
	}
	if res.Latest != "0.18.0" {
		t.Fatalf("Latest = %q, want 0.18.0", res.Latest)
	}
	if res.AssetURL == "" {
		t.Fatal("AssetURL empty: a forcing caller needs it to install anyway")
	}
}

func TestCheck_ResolveErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	orig := releaseAPIURL
	releaseAPIURL = srv.URL + "?repo=%s"
	defer func() { releaseAPIURL = orig }()

	_, err := Check(context.Background(), Options{
		CurrentVersion: "0.17.0",
		BinPath:        canonicalBin(t),
	})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("error should name the HTTP status, got %v", err)
	}
}

func TestCheck_ResolvesBinPathWhenEmpty(t *testing.T) {
	res, err := Check(context.Background(), Options{CurrentVersion: ""})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// no-version short-circuits before path resolution; the point here is
	// that an empty BinPath is not itself an error.
	if res.Blocked != BlockNoVersion {
		t.Fatalf("Blocked = %q, want %q", res.Blocked, BlockNoVersion)
	}
}

func TestApply_ChecksumLookupFailureIsDistinguishable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	err := Apply(context.Background(), Result{
		BinPath:      filepath.Join(t.TempDir(), "anchored"),
		AssetURL:     srv.URL + "/asset.tar.gz",
		AssetName:    "asset.tar.gz",
		ChecksumsURL: srv.URL + "/checksums.txt",
	})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	// Run logs a different line for "could not verify" than for "install
	// failed"; the sentinel is what keeps those two apart.
	if !errors.Is(err, ErrChecksumLookup) {
		t.Fatalf("expected a checksum-lookup error, got %v", err)
	}
}

func TestApply_InstallsVerifiedPayload(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "anchored")
	if err := os.WriteFile(dst, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}

	tarball, sum := makeTarGz(t, []byte("NEW-BINARY"))
	mux := http.NewServeMux()
	mux.HandleFunc("/asset.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarball)
	})
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  asset.tar.gz\n", sum)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	err := Apply(context.Background(), Result{
		BinPath:      dst,
		AssetURL:     srv.URL + "/asset.tar.gz",
		AssetName:    "asset.tar.gz",
		ChecksumsURL: srv.URL + "/checksums.txt",
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "NEW-BINARY" {
		t.Fatalf("dst = %q, want NEW-BINARY", got)
	}
}

func TestApply_RequiresResolvedResult(t *testing.T) {
	err := Apply(context.Background(), Result{BinPath: "/tmp/anchored"})
	if err == nil {
		t.Fatal("expected an error for an unresolved Result, got nil")
	}
}

func TestCheck_ExecutableResolutionFailurePropagates(t *testing.T) {
	orig := osExecutable
	osExecutable = func() (string, error) { return "", errors.New("boom") }
	defer func() { osExecutable = orig }()

	_, err := Check(context.Background(), Options{CurrentVersion: "0.17.0"})
	if !errors.Is(err, ErrResolveExecutable) {
		t.Fatalf("want ErrResolveExecutable, got %v", err)
	}
}

// A binary built without ldflags reports no version. The background path
// stops there, but an explicit request must still be able to reach the
// release and install over it.
func TestCheck_AlwaysResolveWorksWithoutACurrentVersion(t *testing.T) {
	fakeRelease(t, "0.18.0")
	res, err := Check(context.Background(), Options{
		BinPath:       canonicalBin(t),
		AlwaysResolve: true,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Blocked != BlockNoVersion {
		t.Fatalf("Blocked = %q, want %q", res.Blocked, BlockNoVersion)
	}
	if res.Latest != "0.18.0" || res.AssetURL == "" {
		t.Fatalf("release not resolved: %+v", res)
	}
}

// fakeTagRelease serves releases/tags/{tag} for one known tag and 404s for
// anything else, so the "tag does not exist" path is exercised for real.
func fakeTagRelease(t *testing.T, knownTag, version string) {
	t.Helper()
	assetName := fmt.Sprintf("anchored_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/tags/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/"+knownTag) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		payload := map[string]any{
			"tag_name": knownTag,
			"assets": []map[string]string{
				{"name": assetName, "browser_download_url": srv.URL + "/" + assetName},
				{"name": "checksums.txt", "browser_download_url": srv.URL + "/checksums.txt"},
			},
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode release: %v", err)
		}
	})

	orig := releaseTagAPIURL
	releaseTagAPIURL = srv.URL + "/tags/%[2]s?repo=%[1]s"
	t.Cleanup(func() { releaseTagAPIURL = orig })
}

func TestCheck_ResolvesAnExplicitTag(t *testing.T) {
	fakeTagRelease(t, "v0.17.0", "0.17.0")
	for _, given := range []string{"v0.17.0", "0.17.0"} {
		res, err := Check(context.Background(), Options{
			CurrentVersion: "0.16.0",
			BinPath:        canonicalBin(t),
			TargetVersion:  given,
		})
		if err != nil {
			t.Fatalf("%s: unexpected err: %v", given, err)
		}
		if res.Latest != "0.17.0" {
			t.Errorf("%s: Latest = %q, want 0.17.0", given, res.Latest)
		}
		if res.Blocked != BlockNone {
			t.Errorf("%s: Blocked = %q, want none", given, res.Blocked)
		}
	}
}

// "no asset for linux/amd64" is what a missing tag used to look like; the
// error has to name the tag instead.
func TestCheck_MissingTagNamesTheTag(t *testing.T) {
	fakeTagRelease(t, "v0.17.0", "0.17.0")
	_, err := Check(context.Background(), Options{
		CurrentVersion: "0.16.0",
		BinPath:        canonicalBin(t),
		TargetVersion:  "v9.9.9",
	})
	if err == nil {
		t.Fatal("expected an error for a tag that does not exist")
	}
	if !strings.Contains(err.Error(), "v9.9.9") {
		t.Fatalf("error should name the tag, got %v", err)
	}
}

// A downgrade lands on not-newer, which is exactly the refusal --force
// overrides — no separate guard needed.
func TestCheck_OlderTagIsRefusedAsNotNewer(t *testing.T) {
	fakeTagRelease(t, "v0.17.0", "0.17.0")
	res, err := Check(context.Background(), Options{
		CurrentVersion: "0.18.0",
		BinPath:        canonicalBin(t),
		TargetVersion:  "v0.17.0",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Blocked != BlockNotNewer {
		t.Fatalf("Blocked = %q, want %q", res.Blocked, BlockNotNewer)
	}
	if res.AssetURL == "" {
		t.Fatal("assets must still resolve so --force can install the downgrade")
	}
}
