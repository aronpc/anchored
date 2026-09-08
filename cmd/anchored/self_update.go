package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/updater"
)

// exitUpdateAvailable is a distinct exit code so `self-update --check` can be
// polled from a script: 0 means nothing to do (up to date, or refused and
// therefore inert), 1 means the check itself failed, 10 means an update would
// be installed if applied.
const exitUpdateAvailable = 10

const (
	selfUpdateCheckTimeout = 15 * time.Second
	selfUpdateApplyTimeout = 3 * time.Minute

	// Shorter than the interactive check: doctor is advisory and already runs
	// several remote probes, so a slow release lookup should not stall it.
	doctorReleaseTimeout = 5 * time.Second
)

func runSelfUpdate(args []string) {
	fs := newFlagSet("self-update")
	configPath := fs.String("config", "", "path to config file")
	check := fs.Bool("check", false, "report the available version and exit without writing")
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON ({current, latest, bin_path, update_available, blocked})")
	force := fs.Bool("force", false, "install even when a guard refuses (dev build, non-canonical path, env kill switch, same version)")
	assumeYes := fs.Bool("yes", false, "skip the confirmation prompt --force asks before overwriting a dev build")
	noPlugin := fs.Bool("no-plugin", false, "do not synchronize the Claude Code plugin after updating the binary")
	target := fs.String("version", "", "install this published version instead of the latest (e.g. v0.17.0); a downgrade needs --force")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: anchored self-update [--check] [--json]

Updates the anchored binary from the latest official release.

  --check   report only; never writes
  --json    machine-readable output
  --force   install past a refusal you have decided against
  --yes     skip the confirmation --force asks before replacing a dev build
  --version install a specific published version instead of the latest
  --no-plugin  leave the Claude Code plugin alone

Exit codes: 0 nothing to do, 10 an update is available, 1 the check failed.

Note: `+"`anchored update <id>`"+` updates a MEMORY, not the binary.
`)
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), selfUpdateCheckTimeout)
	defer cancel()

	// AlwaysResolve so a refused check still reports the release that is
	// out there — the whole point of the command is that a user who hits a
	// guard learns both the reason and the version they are missing.
	res, err := updater.Check(ctx, updater.Options{
		CurrentVersion: selfUpdateCurrentVersion(Version),
		BinPath:        anchoredBinaryPath(),
		TargetVersion:  *target,
		AlwaysResolve:  true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "anchored self-update: %v\n", err)
		os.Exit(1)
	}

	if *check {
		if *jsonOut {
			fmt.Println(renderSelfUpdateJSON(res))
		} else {
			fmt.Print(renderSelfUpdateCheck(res, *target))
		}
		os.Exit(selfUpdateExitCode(res))
	}

	// Apply mode. A refusal is generally a failure here — the user asked for
	// an install and did not get one — with one exception: being already on
	// the requested version is the outcome they wanted, so it exits 0. `anchored
	// self-update && ...` has to survive being run twice.
	if res.Blocked == updater.BlockNotNewer && !*force && *target == "" {
		fmt.Printf("Already on the latest release (%s).\n", formatV(res.Latest))
		os.Exit(0)
	}

	if res.Blocked != updater.BlockNone {
		if !*force || !forceOverridable(res.Blocked) {
			if res.Blocked == updater.BlockNotNewer && *target != "" {
				fmt.Fprint(os.Stderr, renderDowngradeRefusal(res))
			} else {
				fmt.Fprint(os.Stderr, renderSelfUpdateCheck(res, *target))
			}
			os.Exit(1)
		}
		// Replacing a dev build is the one override that destroys work which
		// exists nowhere else, so it is the one that asks first.
		if res.Blocked == updater.BlockDevBuild {
			ok, err := confirmDevBuildOverwrite(res, os.Stdin, os.Stdout, *assumeYes, stdinIsTTY())
			if err != nil {
				fmt.Fprintf(os.Stderr, "anchored self-update: %v\n", err)
				os.Exit(1)
			}
			if !ok {
				fmt.Fprintln(os.Stderr, "Aborted. Nothing was written.")
				os.Exit(1)
			}
		}
	}

	// Checked before downloading: finding out about a read-only target after
	// pulling several MB is pure waste, and the message the user needs is the
	// same either way.
	if err := ensureWritable(res.BinPath); err != nil {
		hint := sudoHint(res.BinPath, selfUpdateFlags(*force, *assumeYes, *noPlugin, *target))
		fmt.Fprintf(os.Stderr, "anchored self-update: %v\n\nTry: %s\n", err, hint)
		os.Exit(1)
	}

	applyCtx, applyCancel := context.WithTimeout(context.Background(), selfUpdateApplyTimeout)
	defer applyCancel()

	if err := updater.Apply(applyCtx, res); err != nil {
		fmt.Fprintf(os.Stderr, "anchored self-update: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(renderSelfUpdateInstalled(res))
	fmt.Print(renderPluginSyncOutcome(syncPluginAfterUpdate(*configPath, res.Latest, *noPlugin, *force)))
}

// pluginSyncOutcome is the plugin half of an update, reported separately on
// purpose: by the time it runs the binary has already been replaced, so a
// failure here must not read as "the update failed".
type pluginSyncOutcome struct {
	MarketplaceDir     string
	CacheDir           string
	MarketplaceMissing bool
	Skipped            bool
	ConfigError        string
	Drift              PluginDrift
}

// syncPluginAfterUpdate takes installedVersion rather than reading the
// package-level Version: by the time this runs the binary has been replaced,
// so Version still reports the release this process was compiled as — the OLD
// one. Drift is measured against what is now on disk.
func syncPluginAfterUpdate(configPath, installedVersion string, noPlugin, force bool) pluginSyncOutcome {
	if noPlugin {
		return pluginSyncOutcome{Skipped: true}
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return pluginSyncOutcome{ConfigError: err.Error()}
	}

	out := pluginSyncOutcome{
		MarketplaceDir: cfg.Plugin.MarketplaceDir,
		CacheDir:       cfg.Plugin.CacheDir,
	}
	if _, err := os.Stat(cfg.Plugin.MarketplaceDir); err != nil {
		out.MarketplaceMissing = true
		return out
	}

	drift := detectPluginDriftWithForce(cfg, installedVersion, force)
	out.Drift = applyPluginAutoUpdate(drift)
	return out
}

func renderPluginSyncOutcome(o pluginSyncOutcome) string {
	if o.Skipped {
		return ""
	}

	var b strings.Builder
	b.WriteString("\nPlugin (Claude Code)\n")

	if o.ConfigError != "" {
		fmt.Fprintf(&b, "  config could not be read: %s\n  The binary update stands; the plugin was not touched.\n", o.ConfigError)
		return b.String()
	}

	fmt.Fprintf(&b, "  marketplace  %s\n  cache        %s\n", o.MarketplaceDir, o.CacheDir)

	// Saying "nothing to do" out loud matters here: the configured default
	// and a machine's real marketplace root have diverged before, and exiting
	// zero having silently skipped the plugin is the failure worth naming.
	if o.MarketplaceMissing {
		fmt.Fprintf(&b, "  → that marketplace directory does not exist, so nothing was synchronized.\n    Point plugin.marketplace_dir at the mirror you actually use.\n")
		return b.String()
	}

	d := o.Drift
	switch {
	case d.SyncError != "":
		fmt.Fprintf(&b, "  → could not refresh the mirror: %s\n    The binary update stands; the plugin is unchanged.\n", d.SyncError)
	case d.CacheInstallError != "":
		fmt.Fprintf(&b, "  → could not install the plugin: %s\n    The binary update stands; the plugin is unchanged.\n", d.CacheInstallError)
	case d.CacheInstalled:
		fmt.Fprintf(&b, "  → installed plugin %s. Restart Claude Code to load it.\n", formatV(d.CacheVersion))
	case d.SyncPerformed && d.CacheVersion == "":
		b.WriteString("  → mirror refreshed; no plugin is installed from it.\n")
	case d.SyncPerformed:
		fmt.Fprintf(&b, "  → mirror refreshed; the installed plugin %s is already current.\n", formatV(d.CacheVersion))
	default:
		b.WriteString("  → already current.\n")
	}
	return b.String()
}

// checkReleaseAvailable is the doctor's probe: it closes the loop between
// noticing you are behind and knowing the command that fixes it. Best-effort
// by construction — a doctor that goes red without network is a doctor people
// stop running.
func checkReleaseAvailable() {
	ctx, cancel := context.WithTimeout(context.Background(), doctorReleaseTimeout)
	defer cancel()

	res, err := updater.Check(ctx, updater.Options{
		CurrentVersion: selfUpdateCurrentVersion(Version),
		BinPath:        anchoredBinaryPath(),
		AlwaysResolve:  true,
	})
	status, detail, fix := releaseCheckResult(res, err)
	recordCheck(status, "release", detail, fix, false)
}

func releaseCheckResult(res updater.Result, err error) (status, detail, fix string) {
	if err != nil || res.Latest == "" {
		reason := "release not checked (offline?)"
		if err != nil {
			reason = "release not checked: " + err.Error()
		}
		return "skipped", reason, ""
	}

	switch {
	case res.Blocked == updater.BlockNone && res.Newer:
		return "warn", fmt.Sprintf("%s is available (installed %s)", formatV(res.Latest), formatV(res.Current)),
			"anchored self-update"

	case res.Blocked != updater.BlockNone && res.Newer:
		// Refused, so nothing is wrong — but the version is worth naming, and
		// the command that would install it anyway is worth handing over.
		return "skipped", fmt.Sprintf("%s is available but refused (%s); installed %s",
				formatV(res.Latest), res.Blocked, formatV(res.Current)),
			"anchored self-update --force"
	}
	return "ok", fmt.Sprintf("up to date (%s)", formatV(res.Latest)), ""
}

// forceOverridable reports whether --force may install past a refusal. Only
// the refusals a user can legitimately decide against are listed: an
// unrecognized reason keeps refusing, so a guard added later is not silently
// bypassed by a flag that predates it.
func forceOverridable(r updater.BlockReason) bool {
	switch r {
	case updater.BlockDevBuild,
		updater.BlockOutsideCanonical,
		updater.BlockEnvDisabled,
		updater.BlockNotNewer,
		updater.BlockNoVersion:
		return true
	default:
		return false
	}
}

// confirmDevBuildOverwrite asks before replacing a local build with a release.
// With assumeYes it proceeds silently; with no terminal to ask it refuses
// rather than inferring consent from silence.
func confirmDevBuildOverwrite(res updater.Result, in io.Reader, out io.Writer, assumeYes, interactive bool) (bool, error) {
	if assumeYes {
		return true, nil
	}
	if !interactive {
		return false, fmt.Errorf("replacing the dev build %s needs confirmation, but there is no terminal to ask; re-run with --yes to confirm up front", formatV(res.Current))
	}

	if _, err := fmt.Fprintf(out, `This replaces a local dev build with a release binary:

  %s  →  %s
  %s

The build you have now is kept at %s, so one rename undoes this.
Anything you have not committed is not in the release.

Continue? [y/N] `, formatV(res.Current), formatV(res.Latest), res.BinPath, res.BinPath+".prev"); err != nil {
		// If the question never reached the user, treating silence as an
		// answer would be worse than refusing outright.
		return false, fmt.Errorf("could not print the confirmation prompt: %w", err)
	}

	answer, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && answer == "" {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// stdinIsTTY reports whether stdin is a terminal. Stat beats a dependency for
// a single check: a character device is a terminal, a pipe or file is not.
func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// ensureWritable reports whether the binary at path can be replaced. The swap
// renames within the parent directory, so directory write permission is what
// decides it — a writable file inside a read-only directory cannot be
// replaced. Probing with a real temp file is the only check that agrees with
// what rename will do.
func ensureWritable(path string) error {
	dir := filepath.Dir(path)
	probe, err := os.CreateTemp(dir, ".anchored-write-probe-")
	if err != nil {
		return fmt.Errorf("cannot write %s: %w", dir, err)
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		if rmErr := os.Remove(name); rmErr != nil {
			return fmt.Errorf("cannot write %s: %w (and the probe %s could not be removed: %v)", dir, err, name, rmErr)
		}
		return fmt.Errorf("cannot write %s: %w", dir, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("cannot clean up the write probe in %s: %w", dir, err)
	}
	return nil
}

// sudoHint builds the command to retry with privileges.
//
// SECURITY INVARIANT: this string is printed for the user to paste into a
// root shell, at the moment a permission error has them least inclined to
// read it. It is therefore rebuilt from the parsed flags rather than echoed
// from os.Args — an unquoted argv would let `--version "0.1.0; curl x | sh"`
// render as two commands, the second running as root. Only the binary path
// and the flags this command defines can reach it.
func sudoHint(binPath string, flags []string) string {
	parts := append([]string{"sudo", binPath, "self-update"}, flags...)
	return strings.Join(parts, " ")
}

// selfUpdateFlags reconstructs the flag list for the sudo hint from parsed
// values, so nothing the user typed is echoed verbatim.
func selfUpdateFlags(force, assumeYes, noPlugin bool, target string) []string {
	var out []string
	if force {
		out = append(out, "--force")
	}
	if assumeYes {
		out = append(out, "--yes")
	}
	if noPlugin {
		out = append(out, "--no-plugin")
	}
	// The value is validated as a version before it can reach the network,
	// so it is safe to echo; rendering it keeps the hint runnable.
	if target != "" {
		out = append(out, "--version", target)
	}
	return out
}

// renderDowngradeRefusal exists because the generic not-newer verdict reads
// as "Up to date (v0.16.0)" when the user explicitly asked for v0.16.0 —
// which sounds like the tool is confused about what is installed.
func renderDowngradeRefusal(res updater.Result) string {
	return fmt.Sprintf(`Refused: %s is not newer than the installed %s.
Installing it would revert any fix released in between.
Run `+"`anchored self-update --force --version %s`"+` to do it anyway.
`, formatV(res.Latest), formatV(res.Current), res.Latest)
}

// renderSelfUpdateInstalled reports the swap. A downgrade gets its own
// wording: "Installed v0.16.0 (was v0.18.0)" reads like an update unless the
// direction is named.
func renderSelfUpdateInstalled(res updater.Result) string {
	if !res.Newer && res.Latest != res.Current {
		return fmt.Sprintf(`DOWNGRADED %s → %s
  binary    %s
  previous  %s

Fixes released after %s are no longer present. Restart your MCP clients.
`, formatV(res.Current), formatV(res.Latest), res.BinPath, res.BinPath+".prev", formatV(res.Latest))
	}
	return renderSelfUpdateInstalledForward(res)
}

func renderSelfUpdateInstalledForward(res updater.Result) string {
	return fmt.Sprintf(`Installed %s (was %s)
  binary    %s
  previous  %s

Restart your MCP clients (Claude Code, Cursor, ...) to pick up the new
binary — a running server keeps the old one until it exits.
`, formatV(res.Latest), formatV(res.Current), res.BinPath, res.BinPath+".prev")
}

// selfUpdateCurrentVersion strips the leading "v" that ldflags bakes into
// Version. The updater compares versions numerically, and Atoi("v0") is 0, so
// a "v"-prefixed major would silently compare as zero.
func selfUpdateCurrentVersion(v string) string {
	return strings.TrimPrefix(v, "v")
}

// selfUpdateExitCode maps a check onto a shell-usable code. A refused result
// exits 0 on purpose: without an override nothing will happen, so a poller
// must not treat it as pending work.
func selfUpdateExitCode(res updater.Result) int {
	if res.Blocked == updater.BlockNone && res.Newer {
		return exitUpdateAvailable
	}
	return 0
}

func renderSelfUpdateJSON(res updater.Result) string {
	payload := struct {
		Current         string `json:"current"`
		Latest          string `json:"latest"`
		BinPath         string `json:"bin_path"`
		UpdateAvailable bool   `json:"update_available"`
		Blocked         string `json:"blocked"`
	}{
		Current:         res.Current,
		Latest:          res.Latest,
		BinPath:         res.BinPath,
		UpdateAvailable: res.Blocked == updater.BlockNone && res.Newer,
		Blocked:         string(res.Blocked),
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("{\"error\":%q}", err.Error())
	}
	return string(out)
}

// renderSelfUpdateCheck renders the human report: versions, the file that
// would be replaced, and a verdict that always names its own cause.
func renderSelfUpdateCheck(res updater.Result, target string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "installed  %s\n", formatV(res.Current))
	switch {
	case res.Latest == "":
		fmt.Fprintf(&b, "latest     unknown (release not resolved)\n")
	case target != "":
		// Calling a pinned version "latest" is simply false.
		fmt.Fprintf(&b, "target     %s\n", formatV(res.Latest))
	default:
		fmt.Fprintf(&b, "latest     %s\n", formatV(res.Latest))
	}
	fmt.Fprintf(&b, "binary     %s\n\n", res.BinPath)
	b.WriteString(selfUpdateVerdict(res))
	return b.String()
}

func selfUpdateVerdict(res updater.Result) string {
	switch res.Blocked {
	case updater.BlockNone:
		if res.Newer {
			return fmt.Sprintf("Update available: %s → %s\nRun `anchored self-update` to install it.\n",
				formatV(res.Current), formatV(res.Latest))
		}
		return fmt.Sprintf("Up to date (%s).\n", formatV(res.Current))

	case updater.BlockNotNewer:
		return fmt.Sprintf("Up to date (%s).\n", formatV(res.Latest))

	case updater.BlockDevBuild:
		return fmt.Sprintf(`Refused: %s is a local dev build.
A binary built from a checkout is never overwritten automatically — that
would revert your own work to the release tag.
Run `+"`anchored self-update --force`"+` to install %s over it.
`, formatV(res.Current), formatV(res.Latest))

	case updater.BlockOutsideCanonical:
		return fmt.Sprintf(`Refused: the binary lives outside ~/.anchored/bin.
  %s
Only the canonical install is updated automatically.
Run `+"`anchored self-update --force`"+` to update this path anyway.
`, res.BinPath)

	case updater.BlockEnvDisabled:
		return `Refused: ANCHORED_NO_AUTOUPDATE=1 disables automatic updates.
Unset it, or run ` + "`anchored self-update --force`" + ` to override it once.
`

	case updater.BlockNoVersion:
		return `Refused: this binary reports no version, so there is nothing to
compare against. It was built without ldflags — use ` + "`make build`" + `.
`
	}
	return fmt.Sprintf("Refused: %s\n", res.Blocked)
}
