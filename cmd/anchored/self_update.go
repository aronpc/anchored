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
			fmt.Print(renderSelfUpdateCheck(res))
		}
		os.Exit(selfUpdateExitCode(res))
	}

	// Apply mode. The user asked for an install, so a refusal is a failure
	// here — unlike --check, where it is just the state of the world.
	if res.Blocked != updater.BlockNone {
		if !*force || !forceOverridable(res.Blocked) {
			fmt.Fprint(os.Stderr, renderSelfUpdateCheck(res))
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
		fmt.Fprintf(os.Stderr, "anchored self-update: %v\n\nTry: %s\n", err, sudoHint(os.Args))
		os.Exit(1)
	}

	applyCtx, applyCancel := context.WithTimeout(context.Background(), selfUpdateApplyTimeout)
	defer applyCancel()

	if err := updater.Apply(applyCtx, res); err != nil {
		fmt.Fprintf(os.Stderr, "anchored self-update: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(renderSelfUpdateInstalled(res))
	fmt.Print(renderPluginSyncOutcome(syncPluginAfterUpdate(*configPath, *noPlugin, *force)))
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

func syncPluginAfterUpdate(configPath string, noPlugin, force bool) pluginSyncOutcome {
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

	drift := detectPluginDrift(cfg, Version)
	if force {
		drift = detectPluginDriftForced(cfg)
	}
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
	case d.SyncPerformed:
		fmt.Fprintf(&b, "  → mirror refreshed; the installed plugin %s is already current.\n", formatV(d.CacheVersion))
	default:
		b.WriteString("  → already current.\n")
	}
	return b.String()
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

	fmt.Fprintf(out, `This replaces a local dev build with a release binary:

  %s  →  %s
  %s

The build you have now is kept at %s, so one rename undoes this.
Anything you have not committed is not in the release.

Continue? [y/N] `, formatV(res.Current), formatV(res.Latest), res.BinPath, res.BinPath+".prev")

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
		os.Remove(name)
		return fmt.Errorf("cannot write %s: %w", dir, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("cannot clean up the write probe in %s: %w", dir, err)
	}
	return nil
}

// sudoHint echoes back the invocation the user typed, prefixed with sudo, so
// the suggestion never drifts out of date as flags are added.
func sudoHint(argv []string) string {
	return "sudo " + strings.Join(argv, " ")
}

func renderSelfUpdateInstalled(res updater.Result) string {
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
func renderSelfUpdateCheck(res updater.Result) string {
	var b strings.Builder

	fmt.Fprintf(&b, "installed  %s\n", formatV(res.Current))
	if res.Latest != "" {
		fmt.Fprintf(&b, "latest     %s\n", formatV(res.Latest))
	} else {
		fmt.Fprintf(&b, "latest     unknown (release not resolved)\n")
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
