package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/project"
	"github.com/jholhewres/anchored/pkg/sync"
)

// probeTimeout bounds every per-remote network probe so a dead server cannot
// stall the whole doctor run. Var (not const) so tests can shrink it.
var probeTimeout = 3 * time.Second

// checkResult is one doctor finding. The JSON shape ({name, status, detail,
// fix_command}) is a stable contract consumed by scripts and the e2e suite.
type checkResult struct {
	Name       string `json:"name"`
	Status     string `json:"status"` // "ok" | "failed" | "skipped"
	Detail     string `json:"detail,omitempty"`
	FixCommand string `json:"fix_command,omitempty"`
	critical   bool
}

var (
	doctorChecks   []checkResult
	doctorJSONMode bool
)

// recordCheck appends a finding and, outside JSON mode, prints it in the
// human format doctor has always used.
func recordCheck(status, name, detail, fix string, critical bool) {
	doctorChecks = append(doctorChecks, checkResult{
		Name: name, Status: status, Detail: detail, FixCommand: fix, critical: critical,
	})
	if doctorJSONMode {
		return
	}
	mark := "[ ]"
	switch status {
	case "ok":
		mark = "[x]"
	case "skipped":
		mark = "[-]"
	}
	fmt.Printf("%s %s", mark, name)
	if detail != "" {
		fmt.Printf(" — %s", detail)
	}
	fmt.Println()
	if status == "failed" && fix != "" {
		fmt.Printf("    → %s\n", fix)
	}
}

// finishDoctor emits the JSON document in --json mode and exits non-zero when
// any critical check failed. Critical = config load, database open, remote
// connectivity/auth for configured remotes. Informational checks (MCP host
// registrations, identity probe, plugin drift) never fail the run.
func finishDoctor() {
	exitCode := 0
	for _, c := range doctorChecks {
		if c.Status == "failed" && c.critical {
			exitCode = 1
			break
		}
	}
	if doctorJSONMode {
		out := struct {
			Version string        `json:"version"`
			Checks  []checkResult `json:"checks"`
		}{Version: Version, Checks: doctorChecks}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	} else {
		fmt.Println()
	}
	os.Exit(exitCode)
}

// keyPrefix returns at most the first 8 characters of an API key, masking
// anything shorter outright. Doctor must never print a full key — 8 chars is
// enough to tell keys apart without donating entropy to an attacker.
func keyPrefix(key string) string {
	const show = 8
	if len(key) <= show {
		return strings.Repeat("*", len(key))
	}
	return key[:show] + "…"
}

// sanitizeURL strips any userinfo (user:pass@) from a URL before it reaches
// terminal or JSON output — credentials embedded in server URLs must never
// be echoed back.
func sanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid-url>"
	}
	u.User = nil
	return u.String()
}

// remoteProbe is the outcome of probing one remote's health + auth.
type remoteProbe struct {
	Class   string // "ok" | "dns" | "tls" | "timeout" | "auth" | "unreachable" | "http_<code>"
	Latency time.Duration
	Version string
}

// classifyProbeErr maps a transport error to an actionable class. Order
// matters: timeouts mention contexts, TLS errors mention x509 — check the
// specific shapes before falling back to string sniffing.
func classifyProbeErr(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	msg := err.Error()
	if strings.Contains(msg, "x509") || strings.Contains(msg, "tls:") || strings.Contains(msg, "certificate") {
		return "tls"
	}
	return "unreachable"
}

// probeRemote checks one remote in two steps: GET /v1/health (connectivity,
// latency, server version — unauthenticated) then GET /v1/me with the API key
// (auth validity). Any failure short-circuits with its class. Redirects are
// never followed: the probe endpoints don't redirect, and following one could
// hand the Bearer token to a different host.
func probeRemote(ctx context.Context, entry config.RemoteEntry) remoteProbe {
	client := &http.Client{
		Timeout: probeTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	base := strings.TrimRight(entry.ServerURL, "/")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/health", nil)
	if err != nil {
		return remoteProbe{Class: "unreachable"}
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return remoteProbe{Class: classifyProbeErr(err)}
	}
	defer resp.Body.Close()
	latency := time.Since(start)
	var health struct {
		Version string `json:"version"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&health)
	if resp.StatusCode != http.StatusOK {
		return remoteProbe{Class: fmt.Sprintf("http_%d", resp.StatusCode), Latency: latency}
	}

	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/me", nil)
	if err != nil {
		return remoteProbe{Class: "unreachable", Latency: latency, Version: health.Version}
	}
	req2.Header.Set("Authorization", "Bearer "+entry.APIKey)
	resp2, err := client.Do(req2)
	if err != nil {
		return remoteProbe{Class: classifyProbeErr(err), Latency: latency, Version: health.Version}
	}
	defer resp2.Body.Close()
	if resp2.StatusCode == http.StatusUnauthorized || resp2.StatusCode == http.StatusForbidden {
		return remoteProbe{Class: "auth", Latency: latency, Version: health.Version}
	}
	if resp2.StatusCode != http.StatusOK {
		return remoteProbe{Class: fmt.Sprintf("http_%d", resp2.StatusCode), Latency: latency, Version: health.Version}
	}
	return remoteProbe{Class: "ok", Latency: latency, Version: health.Version}
}

// remoteFix maps a probe class to the most likely fix command/action.
func remoteFix(class string, entry config.RemoteEntry) string {
	switch class {
	case "dns":
		return "hostname does not resolve — check the server URL in ~/.anchored/config.yaml and your network/VPN/DNS (on WSL check /etc/resolv.conf)"
	case "tls":
		return "TLS verification failed — update CA certificates (e.g. 'sudo apt install --reinstall ca-certificates') or check for a corporate proxy"
	case "timeout":
		return fmt.Sprintf("no response within %s — check firewall rules and that the server is running (curl -v %s/v1/health)", probeTimeout, strings.TrimRight(sanitizeURL(entry.ServerURL), "/"))
	case "auth":
		return fmt.Sprintf("API key %s rejected — generate a new key in the panel (API Keys) and update ~/.anchored/config.yaml", keyPrefix(entry.APIKey))
	default:
		return fmt.Sprintf("connection failed — verify the URL and try: curl -v %s/v1/health", strings.TrimRight(sanitizeURL(entry.ServerURL), "/"))
	}
}

// sortedRemoteNames returns the configured remote names, default first, then
// alphabetical — the same priority order sync resolution uses.
func sortedRemoteNames(cfg *config.Config) []string {
	names := make([]string, 0, len(cfg.Remotes))
	for name := range cfg.Remotes {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		di, dj := cfg.Remotes[names[i]].Default, cfg.Remotes[names[j]].Default
		if di != dj {
			return di
		}
		return names[i] < names[j]
	})
	return names
}

// checkRemoteConnectivity probes every configured remote and reports
// connectivity, latency, server version and auth validity. Failures here are
// critical: a configured-but-unreachable remote means sync is silently dead.
// Returns whether at least one remote answered (gates the identity probe).
func checkRemoteConnectivity(cfg *config.Config) bool {
	if len(cfg.Remotes) == 0 {
		recordCheck("ok", "remotes: none configured (local-only mode)", "", "", false)
		return false
	}
	anyReachable := false
	for _, name := range sortedRemoteNames(cfg) {
		entry := cfg.Remotes[name]
		ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
		probe := probeRemote(ctx, entry)
		cancel()

		label := fmt.Sprintf("remote %q (%s)", name, sanitizeURL(entry.ServerURL))
		switch probe.Class {
		case "ok":
			anyReachable = true
			detail := fmt.Sprintf("ok · %dms · server %s · key %s",
				probe.Latency.Milliseconds(), probe.Version, keyPrefix(entry.APIKey))
			recordCheck("ok", label, detail, "", false)
		case "auth":
			anyReachable = true
			recordCheck("failed", label,
				fmt.Sprintf("reachable (server %s) but the API key was rejected", probe.Version),
				remoteFix("auth", entry), true)
		default:
			recordCheck("failed", label, "connectivity failed: "+probe.Class,
				remoteFix(probe.Class, entry), true)
		}
	}
	return anyReachable
}

// checkRemoteConfigSanity validates the routing config: exactly one resolvable
// default, valid path globs, and the effective auto-sync per remote.
func checkRemoteConfigSanity(cfg *config.Config) {
	if len(cfg.Remotes) == 0 {
		return
	}

	var defaults []string
	for _, name := range sortedRemoteNames(cfg) {
		entry := cfg.Remotes[name]
		if entry.Default {
			defaults = append(defaults, name)
		}
		for _, pattern := range entry.Paths {
			if _, err := path.Match(pattern, "probe"); err != nil {
				recordCheck("failed", fmt.Sprintf("remote %q path pattern %q", name, pattern),
					"invalid glob: "+err.Error(),
					"fix the pattern in ~/.anchored/config.yaml (path.Match syntax: * does not cross /)", false)
			}
		}
		mode := "auto-sync on"
		if !entry.AutoSyncEnabled() {
			mode = "auto-sync off"
		}
		recordCheck("ok", fmt.Sprintf("remote %q routing", name),
			fmt.Sprintf("%s · %d path pattern(s) · default=%v", mode, len(entry.Paths), entry.Default), "", false)
	}

	switch len(defaults) {
	case 0:
		recordCheck("failed", "default remote", "no remote has default: true — saves outside configured paths will stay local",
			"set 'default: true' on one remote in ~/.anchored/config.yaml (or re-run 'anchored remote configure')", false)
	case 1:
		recordCheck("ok", "default remote: "+defaults[0], "", "", false)
	default:
		recordCheck("failed", "default remote", "multiple remotes marked default: "+strings.Join(defaults, ", "),
			"keep 'default: true' on exactly one remote in ~/.anchored/config.yaml", false)
	}
}

// checkProjectIdentity derives the cwd repo's remote keys and probes which
// configured remote knows them. Never critical, and skipped (not failed) when
// the network is down — identity is a routing question, not a health one.
func checkProjectIdentity(cfg *config.Config, cwd string, anyRemoteReachable bool) {
	if len(cfg.Remotes) == 0 {
		return
	}
	origin := gitOriginURL(cwd)
	if origin == "" {
		recordCheck("skipped", "project identity", "cwd is not a git repo with an 'origin' remote", "", false)
		return
	}
	if !anyRemoteReachable {
		recordCheck("skipped", "project identity", "no remote reachable — cannot probe (fix connectivity first)", "", false)
		return
	}

	canonicalKey := project.DeriveRemoteKeyFromURL(origin)
	legacyKey := project.DeriveLegacyRemoteKeyFromURL(origin)

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	target, projectID, matchedKey := sync.ResolveProjectAcrossRemotes(ctx, cfg, cwd, "cli", canonicalKey, legacyKey)
	if target != nil && projectID != "" {
		recordCheck("ok", "project identity",
			fmt.Sprintf("%s → remote %q project %s (key %s)", origin, target.Name, projectID, matchedKey), "", false)
		return
	}
	recordCheck("failed", "project identity",
		fmt.Sprintf("no configured remote has a project for %s (keys tried: %s, %s)", origin, canonicalKey, legacyKey),
		"create the project in the panel with Repository URL "+origin+", or link an existing one: anchored remote link <slug> --remote <name>", false)
}

// checkPluginDrift reports when the Claude Code plugin mirror/cache lag the
// binary — stale hooks are a common "it works in CLI but not in the IDE".
func checkPluginDrift(cfg *config.Config) {
	drift := detectPluginDrift(cfg, Version)
	if drift.BinaryVersion == "" || drift.BinaryVersion == "dev" {
		return
	}
	if drift.MirrorVersion == "" && drift.CacheVersion == "" {
		recordCheck("skipped", "Claude Code plugin", "plugin not installed (mirror/cache absent)", "", false)
		return
	}
	if !drift.HasDrift {
		recordCheck("ok", fmt.Sprintf("Claude Code plugin up to date (mirror %s, cache %s)",
			drift.MirrorVersion, drift.CacheVersion), "", "", false)
		return
	}
	recordCheck("failed", "Claude Code plugin",
		fmt.Sprintf("plugin lags the binary (binary %s, mirror %s, cache %s) — hooks may be stale",
			drift.BinaryVersion, drift.MirrorVersion, drift.CacheVersion),
		"restart your IDE (the session-start hook auto-updates the plugin), or reinstall it", false)
}
