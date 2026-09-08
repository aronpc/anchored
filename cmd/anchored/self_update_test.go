package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/updater"
)

func TestSelfUpdateExitCode(t *testing.T) {
	cases := []struct {
		name string
		res  updater.Result
		want int
	}{
		{
			name: "applicable update",
			res:  updater.Result{Current: "0.17.0", Latest: "0.18.0", Newer: true},
			want: exitUpdateAvailable,
		},
		{
			name: "already latest",
			res:  updater.Result{Current: "0.18.0", Latest: "0.18.0", Blocked: updater.BlockNotNewer},
			want: 0,
		},
		// A blocked check exits 0: nothing will happen without --force, so a
		// script polling for "there is work to do" must not be woken up by a
		// refusal it cannot act on.
		{
			name: "blocked on dev build even with a newer release out",
			res:  updater.Result{Current: "0.17.0-dev+gabc", Latest: "0.18.0", Newer: true, Blocked: updater.BlockDevBuild},
			want: 0,
		},
		{
			name: "blocked outside canonical dir",
			res:  updater.Result{Current: "0.17.0", Latest: "0.18.0", Newer: true, Blocked: updater.BlockOutsideCanonical},
			want: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := selfUpdateExitCode(tc.res); got != tc.want {
				t.Fatalf("selfUpdateExitCode = %d, want %d", got, tc.want)
			}
		})
	}
}

// Every refusal must name itself and say what overrides it; a report that
// says "blocked" without the cause is the silent no-op this command exists
// to replace.
func TestRenderSelfUpdateCheck_ExplainsEveryBlockReason(t *testing.T) {
	cases := []struct {
		reason   updater.BlockReason
		mustHave []string
	}{
		{updater.BlockDevBuild, []string{"dev", "--force"}},
		{updater.BlockOutsideCanonical, []string{".anchored/bin", "--force"}},
		{updater.BlockEnvDisabled, []string{"ANCHORED_NO_AUTOUPDATE", "--force"}},
		{updater.BlockNotNewer, []string{"0.18.0"}},
		{updater.BlockNoVersion, []string{"ldflags"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.reason), func(t *testing.T) {
			out := renderSelfUpdateCheck(updater.Result{
				Current: "0.17.0-dev+gabc",
				Latest:  "0.18.0",
				BinPath: "/home/u/.anchored/bin/anchored",
				Blocked: tc.reason,
			})
			for _, want := range tc.mustHave {
				if !strings.Contains(out, want) {
					t.Errorf("report for %q missing %q\n---\n%s", tc.reason, want, out)
				}
			}
		})
	}
}

func TestRenderSelfUpdateCheck_ShowsVersionsAndPath(t *testing.T) {
	out := renderSelfUpdateCheck(updater.Result{
		Current: "0.17.0",
		Latest:  "0.18.0",
		BinPath: "/home/u/.anchored/bin/anchored",
		Newer:   true,
	})
	for _, want := range []string{"0.17.0", "0.18.0", "/home/u/.anchored/bin/anchored"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n---\n%s", want, out)
		}
	}
}

func TestRenderSelfUpdateCheck_UnknownLatestWhenUnresolved(t *testing.T) {
	out := renderSelfUpdateCheck(updater.Result{
		Current: "0.17.0",
		BinPath: "/home/u/.anchored/bin/anchored",
		Blocked: updater.BlockEnvDisabled,
	})
	if strings.Contains(out, "→ \n") {
		t.Errorf("empty latest rendered as a dangling arrow\n---\n%s", out)
	}
}

func TestRenderSelfUpdateJSON(t *testing.T) {
	raw := renderSelfUpdateJSON(updater.Result{
		Current: "0.17.0-dev+gabc",
		Latest:  "0.18.0",
		BinPath: "/home/u/.anchored/bin/anchored",
		Newer:   true,
		Blocked: updater.BlockDevBuild,
	})

	var got struct {
		Current         string `json:"current"`
		Latest          string `json:"latest"`
		BinPath         string `json:"bin_path"`
		UpdateAvailable bool   `json:"update_available"`
		Blocked         string `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, raw)
	}
	if got.Current != "0.17.0-dev+gabc" || got.Latest != "0.18.0" {
		t.Errorf("versions = %q / %q", got.Current, got.Latest)
	}
	if got.BinPath != "/home/u/.anchored/bin/anchored" {
		t.Errorf("bin_path = %q", got.BinPath)
	}
	// update_available reports what the command would actually DO, so a
	// blocked result is false even though a newer release exists — that is
	// the distinction the exit code cannot carry.
	if got.UpdateAvailable {
		t.Error("update_available should be false when blocked")
	}
	if got.Blocked != string(updater.BlockDevBuild) {
		t.Errorf("blocked = %q, want %q", got.Blocked, updater.BlockDevBuild)
	}
}

func TestSelfUpdateCurrentVersion_StripsLeadingV(t *testing.T) {
	// Version comes from ldflags as "v0.17.0"; updater compares numerically
	// and Atoi("v0") yields 0, so the "v" has to come off before comparison.
	if got := selfUpdateCurrentVersion("v0.18.0"); got != "0.18.0" {
		t.Fatalf("got %q, want 0.18.0", got)
	}
	if got := selfUpdateCurrentVersion("0.18.0"); got != "0.18.0" {
		t.Fatalf("got %q, want 0.18.0", got)
	}
}

func TestSelfUpdateIsRegisteredInUsage(t *testing.T) {
	out := captureUsage(t)
	if !strings.Contains(out, "self-update") {
		t.Errorf("printUsage does not mention self-update:\n%s", out)
	}
	// `update` is memory update; the two are one keystroke apart and the
	// usage text is the only place a user learns which is which.
	if !strings.Contains(out, "Update a memory") {
		t.Errorf("printUsage lost the memory-update line:\n%s", out)
	}
}

// captureUsage collects what printUsage writes to stderr.
func captureUsage(t *testing.T) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	printUsage()
	os.Stderr = orig
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestEnsureWritable_AcceptsWritableTarget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "anchored")
	if err := os.WriteFile(path, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureWritable(path); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}

// The swap renames within the parent dir, so write permission on the DIR is
// what actually matters — a writable file inside a read-only dir still fails.
func TestEnsureWritable_RejectsReadOnlyParentDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "anchored")
	if err := os.WriteFile(path, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	err := ensureWritable(path)
	if err == nil {
		t.Fatal("expected an error for a read-only parent dir")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("error should name the directory, got %v", err)
	}
}

func TestEnsureWritable_ChecksParentWhenFileAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := ensureWritable(filepath.Join(dir, "not-there-yet")); err != nil {
		t.Fatalf("a fresh install into a writable dir must pass, got %v", err)
	}
}

func TestEnsureWritable_LeavesNoProbeBehind(t *testing.T) {
	dir := t.TempDir()
	if err := ensureWritable(filepath.Join(dir, "anchored")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("write probe leaked: %v", entries)
	}
}

// The hint has to be the command the user actually typed, or it ages every
// time a flag is added.
func TestSudoHint_EchoesTheInvocation(t *testing.T) {
	got := sudoHint([]string{"/usr/local/bin/anchored", "self-update", "--force"})
	want := "sudo /usr/local/bin/anchored self-update --force"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderSelfUpdateInstalled(t *testing.T) {
	out := renderSelfUpdateInstalled(updater.Result{
		Current: "0.17.0",
		Latest:  "0.18.0",
		BinPath: "/home/u/.anchored/bin/anchored",
	})
	for _, want := range []string{"0.17.0", "0.18.0", "/home/u/.anchored/bin/anchored", ".prev"} {
		if !strings.Contains(out, want) {
			t.Errorf("success report missing %q\n---\n%s", want, out)
		}
	}
	// A swapped binary does not reach a running MCP server; saying so is the
	// difference between "it worked" and "it worked and you must restart".
	if !strings.Contains(strings.ToLower(out), "restart") {
		t.Errorf("success report does not tell the user to restart\n---\n%s", out)
	}
}

// Only the refusals a user can legitimately decide against are overridable.
// An unrecognized reason must keep refusing, so a future guard is not
// silently bypassed by a flag that predates it.
func TestForceOverridable(t *testing.T) {
	overridable := []updater.BlockReason{
		updater.BlockDevBuild,
		updater.BlockOutsideCanonical,
		updater.BlockEnvDisabled,
		updater.BlockNotNewer,
		updater.BlockNoVersion,
	}
	for _, r := range overridable {
		if !forceOverridable(r) {
			t.Errorf("%q should be overridable by --force", r)
		}
	}
	if forceOverridable(updater.BlockReason("some-future-guard")) {
		t.Error("an unknown reason must not be overridable")
	}
	if forceOverridable(updater.BlockNone) {
		t.Error("BlockNone is not a refusal")
	}
}

func TestConfirmDevBuildOverwrite_ProceedsOnYes(t *testing.T) {
	var out strings.Builder
	ok, err := confirmDevBuildOverwrite(devBuildResult(), strings.NewReader("y\n"), &out, false, true)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !ok {
		t.Fatal("y should proceed")
	}
}

func TestConfirmDevBuildOverwrite_DefaultsToNo(t *testing.T) {
	for _, answer := range []string{"\n", "n\n", "no\n", "whatever\n"} {
		var out strings.Builder
		ok, err := confirmDevBuildOverwrite(devBuildResult(), strings.NewReader(answer), &out, false, true)
		if err != nil {
			t.Fatalf("answer %q: unexpected err: %v", answer, err)
		}
		if ok {
			t.Errorf("answer %q should not proceed", answer)
		}
	}
}

func TestConfirmDevBuildOverwrite_AssumeYesSkipsThePrompt(t *testing.T) {
	var out strings.Builder
	ok, err := confirmDevBuildOverwrite(devBuildResult(), strings.NewReader(""), &out, true, false)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !ok {
		t.Fatal("--yes should proceed")
	}
	if out.Len() != 0 {
		t.Errorf("--yes should not print a prompt, got %q", out.String())
	}
}

// Without a TTY there is nobody to answer, so consent cannot be assumed.
func TestConfirmDevBuildOverwrite_AbortsWithoutTTY(t *testing.T) {
	var out strings.Builder
	ok, err := confirmDevBuildOverwrite(devBuildResult(), strings.NewReader(""), &out, false, false)
	if ok {
		t.Fatal("must not proceed without a TTY and without --yes")
	}
	if err == nil {
		t.Fatal("expected an error explaining how to proceed")
	}
	if !strings.Contains(err.Error(), "--yes") {
		t.Errorf("error should point at --yes, got %v", err)
	}
}

func TestConfirmDevBuildOverwrite_PromptNamesTheStakes(t *testing.T) {
	var out strings.Builder
	if _, err := confirmDevBuildOverwrite(devBuildResult(), strings.NewReader("n\n"), &out, false, true); err != nil {
		t.Fatal(err)
	}
	prompt := out.String()
	// The user is about to lose a local build; the prompt has to say what is
	// being replaced, with what, and where the old one goes.
	for _, want := range []string{"0.17.0-dev+gabc", "0.18.0", ".prev"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, prompt)
		}
	}
}

func devBuildResult() updater.Result {
	return updater.Result{
		Current: "0.17.0-dev+gabc",
		Latest:  "0.18.0",
		BinPath: "/home/u/.anchored/bin/anchored",
		Newer:   true,
		Blocked: updater.BlockDevBuild,
	}
}

// The dev-build guard is what froze the plugin at 0.17.0 while releases moved
// on. An explicit request has to be able to read the state it hides.
func TestDetectPluginDriftForced_IgnoresTheDevBuildGuard(t *testing.T) {
	cacheDir := t.TempDir()
	mirrorDir := t.TempDir()
	seedPluginCache(t, cacheDir, "0.17.0")
	seedMirrorManifest(t, mirrorDir, "0.18.0")

	cfg := &config.Config{}
	cfg.Plugin.CacheDir = cacheDir
	cfg.Plugin.MarketplaceDir = mirrorDir

	if d := detectPluginDrift(cfg, "v0.17.0-dev+gabc"); d.MirrorVersion != "" || d.HasDrift {
		t.Fatalf("guarded detection should still see nothing on a dev build, got %+v", d)
	}

	f := detectPluginDriftForced(cfg)
	if f.MirrorVersion != "0.18.0" || f.CacheVersion != "0.17.0" {
		t.Fatalf("forced detection did not read the versions: %+v", f)
	}
	if !f.CacheBehind {
		t.Error("cache 0.17.0 against mirror 0.18.0 must count as behind")
	}
	// On an explicit request the mirror is always worth refreshing: the point
	// is to fetch the newest plugin, not to infer it from a version stamp
	// that cannot be compared.
	if !f.MirrorBehind {
		t.Error("forced detection should always try to refresh the mirror")
	}
}

func TestRenderPluginSyncOutcome_NamesTheNoOpPath(t *testing.T) {
	out := renderPluginSyncOutcome(pluginSyncOutcome{
		MarketplaceDir:     "/home/u/.claude/plugins/marketplaces/anchored",
		MarketplaceMissing: true,
	})
	if !strings.Contains(out, "/home/u/.claude/plugins/marketplaces/anchored") {
		t.Errorf("outcome must name the path it looked at\n---\n%s", out)
	}
	// The config default and this machine's real marketplace root have
	// differed before; exiting 0 having silently done nothing is the failure
	// mode worth shouting about.
	if !strings.Contains(strings.ToLower(out), "not") {
		t.Errorf("outcome must say plainly that nothing was done\n---\n%s", out)
	}
}

func TestRenderPluginSyncOutcome_ReportsAnInstall(t *testing.T) {
	out := renderPluginSyncOutcome(pluginSyncOutcome{
		MarketplaceDir: "/m",
		CacheDir:       "/c",
		Drift: PluginDrift{
			MirrorVersion:  "0.18.0",
			CacheVersion:   "0.18.0",
			SyncPerformed:  true,
			CacheInstalled: true,
		},
	})
	if !strings.Contains(out, "0.18.0") {
		t.Errorf("outcome should name the installed plugin version\n---\n%s", out)
	}
}

// A plugin failure must not read as "the update failed" — the binary was
// already replaced by then.
func TestRenderPluginSyncOutcome_FailureKeepsTheBinaryUpdate(t *testing.T) {
	out := renderPluginSyncOutcome(pluginSyncOutcome{
		MarketplaceDir: "/m",
		CacheDir:       "/c",
		Drift:          PluginDrift{MirrorBehind: true, SyncError: "git pull failed: no upstream"},
	})
	if !strings.Contains(out, "no upstream") {
		t.Errorf("outcome should carry the underlying error\n---\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "binary") {
		t.Errorf("outcome should say the binary update still stands\n---\n%s", out)
	}
}

func TestRenderPluginSyncOutcome_SkippedIsSilent(t *testing.T) {
	if out := renderPluginSyncOutcome(pluginSyncOutcome{Skipped: true}); out != "" {
		t.Errorf("--no-plugin should print nothing, got %q", out)
	}
}

// Exercises the real function, config load included, for the case the plan
// flagged as the silent failure: a configured marketplace that is not there.
func TestSyncPluginAfterUpdate_MissingMarketplaceIsReportedNotSwallowed(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	absent := filepath.Join(dir, "not-a-marketplace")
	body := "plugin:\n  marketplace_dir: " + absent + "\n  cache_dir: " + filepath.Join(dir, "cache") + "\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	out := syncPluginAfterUpdate(cfgPath, false, true)
	if !out.MarketplaceMissing {
		t.Fatalf("missing marketplace not detected: %+v", out)
	}
	if out.MarketplaceDir != absent {
		t.Errorf("MarketplaceDir = %q, want %q", out.MarketplaceDir, absent)
	}
	if rendered := renderPluginSyncOutcome(out); !strings.Contains(rendered, absent) {
		t.Errorf("report does not name the missing path\n---\n%s", rendered)
	}
}

func TestSyncPluginAfterUpdate_NoPluginSkipsEverything(t *testing.T) {
	out := syncPluginAfterUpdate("", true, true)
	if !out.Skipped {
		t.Fatal("--no-plugin must skip")
	}
	if out.MarketplaceDir != "" {
		t.Errorf("skipped sync should not resolve paths, got %q", out.MarketplaceDir)
	}
}

// The doctor closes the discovery loop: you learn you are behind from the
// diagnostic you already run, and it hands you the command.
func TestReleaseCheckResult_ReportsAnAvailableUpdate(t *testing.T) {
	status, detail, fix := releaseCheckResult(updater.Result{
		Current: "0.17.0", Latest: "0.18.0", Newer: true,
	}, nil)
	if status == "ok" {
		t.Errorf("an available update should not read as ok, got %q", status)
	}
	if !strings.Contains(detail, "0.18.0") {
		t.Errorf("detail should name the release, got %q", detail)
	}
	if !strings.Contains(fix, "self-update") {
		t.Errorf("fix should hand over the command, got %q", fix)
	}
}

func TestReleaseCheckResult_OKWhenCurrent(t *testing.T) {
	status, _, _ := releaseCheckResult(updater.Result{
		Current: "0.18.0", Latest: "0.18.0", Blocked: updater.BlockNotNewer,
	}, nil)
	if status != "ok" {
		t.Errorf("status = %q, want ok", status)
	}
}

// No network must degrade, never fail: a doctor that goes red on a plane is
// a doctor people stop running.
func TestReleaseCheckResult_DegradesWhenUnreachable(t *testing.T) {
	status, detail, _ := releaseCheckResult(updater.Result{Current: "0.18.0"}, errors.New("dial tcp: no route to host"))
	if status != "skipped" {
		t.Errorf("status = %q, want skipped", status)
	}
	if !strings.Contains(strings.ToLower(detail), "not checked") {
		t.Errorf("detail should say it was not checked, got %q", detail)
	}
}

// A dev build is not "behind" in a way the doctor should nag about, but it
// should still say a release exists.
func TestReleaseCheckResult_MentionsReleaseOnDevBuild(t *testing.T) {
	status, detail, _ := releaseCheckResult(updater.Result{
		Current: "0.17.0-dev+gabc", Latest: "0.18.0", Newer: true, Blocked: updater.BlockDevBuild,
	}, nil)
	if status == "failed" {
		t.Error("a dev build is a deliberate state, not a failure")
	}
	if !strings.Contains(detail, "0.18.0") {
		t.Errorf("detail should still name the release, got %q", detail)
	}
}
