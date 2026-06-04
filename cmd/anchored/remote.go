package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/jholhewres/anchored/pkg/memory"
	"github.com/jholhewres/anchored/pkg/sync"
)

func runRemote(args []string) {
	if len(args) == 0 {
		printRemoteUsage()
		return
	}
	switch args[0] {
	case "configure":
		runRemoteConfigure(args[1:])
	case "link":
		runRemoteLink(args[1:])
	case "unlink":
		runRemoteUnlink(args[1:])
	case "status":
		runRemoteStatus(args[1:])
	case "preview":
		runRemotePreview(args[1:])
	case "sync":
		runRemoteSync(args[1:])
	case "sync-per-project":
		runRemoteSyncPerProject(args[1:])
	default:
		printRemoteUsage()
	}
}

func printRemoteUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  anchored remote configure --server URL --key KEY   Wire a remote anchored_oss server")
	fmt.Fprintln(os.Stderr, "  anchored remote link <project_id>                   Subscribe to a remote project so its memories sync")
	fmt.Fprintln(os.Stderr, "  anchored remote unlink <project_id>                 Stop syncing memories tied to a remote project")
	fmt.Fprintln(os.Stderr, "  anchored remote status                              Show remote config + how the current repo routes (git origin)")
	fmt.Fprintln(os.Stderr, "  anchored remote preview                             Preview which memories would sync (offline)")
	fmt.Fprintln(os.Stderr, "  anchored remote sync                                Sync the CURRENT repo by its git origin (--dry-run to preview)")
	fmt.Fprintln(os.Stderr, "  anchored remote sync --all                          Sync every local project, each routed by its own git origin")
	fmt.Fprintln(os.Stderr, "  anchored remote sync --project-id <id>              Force all current-repo memories into a specific remote project")
}

// runRemoteLink adds a remote project_id to the local config's
// remote.projects list. Memories with a matching remote_project_key on their
// metadata will be routed to that project during sync. Idempotent.
func runRemoteLink(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: anchored remote link <project_id>")
		os.Exit(1)
	}
	projectID := args[0]

	configFile, cfg := loadWritableConfig()
	for _, p := range cfg.Remote.Projects {
		if p == projectID {
			fmt.Printf("Already linked to %s\n", projectID)
			return
		}
	}
	cfg.Remote.Projects = append(cfg.Remote.Projects, projectID)
	writeConfigFile(configFile, cfg)
	fmt.Printf("Linked project %s\n", projectID)
	fmt.Printf("  Total projects subscribed: %d\n", len(cfg.Remote.Projects))
}

// runRemoteUnlink removes a project_id from the local config's
// remote.projects list. No-op if the id isn't present.
func runRemoteUnlink(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: anchored remote unlink <project_id>")
		os.Exit(1)
	}
	projectID := args[0]

	configFile, cfg := loadWritableConfig()
	out := cfg.Remote.Projects[:0]
	removed := false
	for _, p := range cfg.Remote.Projects {
		if p == projectID {
			removed = true
			continue
		}
		out = append(out, p)
	}
	cfg.Remote.Projects = out
	writeConfigFile(configFile, cfg)
	if removed {
		fmt.Printf("Unlinked project %s\n", projectID)
	} else {
		fmt.Printf("Project %s was not linked\n", projectID)
	}
}

// runRemoteConfigure wires (or rewires) a remote anchored_oss server into the
// local config. It always sets remote.enabled=true unless --disable is passed.
// Existing values are overwritten — config rotation is via re-running this.
func runRemoteConfigure(args []string) {
	fs := newFlagSet("remote configure")
	server := fs.String("server", "", "remote server URL (e.g. https://anchored.acme.com)")
	key := fs.String("key", "", "admin/sync API key for the server")
	disable := fs.Bool("disable", false, "turn remote sync off (other flags are ignored)")
	fs.Parse(args)

	configFile, cfg := loadWritableConfig()

	if *disable {
		cfg.Remote.Enabled = false
		writeConfigFile(configFile, cfg)
		fmt.Printf("Remote sync disabled (config: %s)\n", configFile)
		return
	}

	if *server == "" || *key == "" {
		fmt.Fprintln(os.Stderr, "Usage: anchored remote configure --server URL --key KEY [--disable]")
		os.Exit(1)
	}

	newURL := strings.TrimRight(*server, "/")
	// Project IDs are server-scoped, so pointing at a different server makes the
	// existing links meaningless — clear them to avoid stale "project not found"
	// pushes. Re-pointing at the same server keeps the links.
	if cfg.Remote.ServerURL != "" && cfg.Remote.ServerURL != newURL && len(cfg.Remote.Projects) > 0 {
		fmt.Printf("Server changed (%s → %s); cleared %d stale project link(s).\n",
			cfg.Remote.ServerURL, newURL, len(cfg.Remote.Projects))
		cfg.Remote.Projects = nil
	}

	cfg.Remote.Enabled = true
	cfg.Remote.ServerURL = newURL
	cfg.Remote.APIKey = *key

	writeConfigFile(configFile, cfg)
	fmt.Printf("Remote sync configured.\n")
	fmt.Printf("  Server: %s\n", cfg.Remote.ServerURL)
	fmt.Printf("  Key:    %s\n", maskKey(cfg.Remote.APIKey))
	fmt.Printf("  Config: %s\n", configFile)
}

func runRemoteStatus(args []string) {
	fs := newFlagSet("remote status")
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(args)

	cfg, err := loadConfig(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	fmt.Printf("Remote sync: %s\n", boolStr(cfg.Remote.Enabled, "enabled", "disabled"))
	fmt.Printf("Server URL:  %s\n", orEmpty(cfg.Remote.ServerURL, "(not configured)"))
	fmt.Printf("API Key:     %s\n", maskKey(cfg.Remote.APIKey))
	fmt.Printf("Projects:    %d configured\n", len(cfg.Remote.Projects))

	// Show how the current repo would route, so the user can confirm the
	// git-origin match before syncing. Best-effort: needs an initialized store.
	if _, _, svc, err := initService(*configPath); err == nil {
		defer svc.Close()
		cwd, _ := os.Getwd()
		if proj, err := svc.ResolveProjectInfo(cwd); err == nil && proj != nil {
			fmt.Println()
			fmt.Printf("Current repo: %s\n", proj.Name)
			fmt.Printf("  Origin:     %s\n", orEmpty(gitOriginURL(cwd), "(no origin — sync will refuse)"))
			fmt.Printf("  Remote key: %s\n", orEmpty(proj.RemoteKey, "(none)"))
		}
	}
}

func runRemotePreview(args []string) {
	fs := newFlagSet("remote preview")
	configPath := fs.String("config", "", "path to config file")
	project := fs.String("project", "", "project path filter (default: cwd)")
	format := fs.String("format", "table", "output format: table or json")
	fs.Parse(args)

	_, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	ctx := context.Background()

	projectRoot := *project
	if projectRoot == "" {
		projectRoot, _ = os.Getwd()
	}

	memories, err := listAllMemories(ctx, svc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list error: %v\n", err)
		os.Exit(1)
	}

	syncMemories := toSyncMemories(memories)

	result := sync.ClassifyForPreview(syncMemories, projectRoot)

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			fmt.Fprintf(os.Stderr, "json encode error: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("Total: %d | Syncable: %d | Blocked: %d | Needs Review: %d\n\n",
			result.Total, result.Syncable, result.Blocked, result.NeedsReview)
		for _, item := range result.Items {
			content := item.Memory.Content
			if len(content) > 80 {
				content = content[:80] + "..."
			}
			fmt.Printf("  %-12s %-8s %s\n", item.Classification, item.Memory.Category, content)
			if item.Reason != "" {
				fmt.Printf("  %12s └─ %s\n", "", item.Reason)
			}
		}
	}
}

func boolStr(v bool, t, f string) string {
	if v {
		return t
	}
	return f
}

func orEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func maskKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}

// runRemoteSync syncs the CURRENT repository's memories to the remote server,
// identifying the remote project by the repo's git-origin remote_key (not its
// local directory name). Two clones of the same repo — in different folders, on
// different machines — therefore land in the same remote project.
//
// Routing:
//   - default (repo-scoped): resolve the cwd's local project, push only its
//     memories, and send a ProjectClaim{Name, RemoteKey} so the server
//     resolves-or-creates the matching remote project by git origin.
//   - --project-id <id>: force all (cwd-scoped) memories into a specific remote
//     project (legacy/manual override).
//   - --all: push every local project, each routed by its own git-origin
//     remote_key (multi-project; see runRemoteSyncPerProject).
func runRemoteSync(args []string) {
	fs := newFlagSet("remote sync")
	configPath := fs.String("config", "", "path to config file")
	project := fs.String("project", "", "project path filter (default: cwd)")
	dryRun := fs.Bool("dry-run", false, "preview what would be pushed without making network calls")
	projectID := fs.String("project-id", "", "force a specific remote project ID (overrides git-origin routing)")
	all := fs.Bool("all", false, "sync every local project (each routed by its own git origin), not just the current repo")
	fs.Parse(args)

	if *all {
		runRemoteSyncPerProject(args)
		return
	}

	cfg, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	ctx := context.Background()

	projectRoot := *project
	if projectRoot == "" {
		projectRoot, _ = os.Getwd()
	}

	// Resolve the current repository. Identity is the git-origin remote_key,
	// never the directory name — so a repo synced from /a/anchored and
	// /b/anchored-fork (same origin) routes to one remote project.
	proj, err := svc.ResolveProjectInfo(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve project error: %v\n", err)
		os.Exit(1)
	}

	// When forcing a remote project id, the git-origin guard is bypassed
	// intentionally (manual override). Otherwise we require a git repo with an
	// origin so we can confirm the repository identity before pushing.
	if *projectID == "" {
		// Linked project takes precedence over git-origin auto-routing.
		// If the user ran `anchored remote link <id>`, use that project
		// directly instead of sending a ProjectClaim (which would create a
		// new project when the server doesn't recognize the git origin).
		if len(cfg.Remote.Projects) > 0 {
			*projectID = cfg.Remote.Projects[0]
			fmt.Printf("Using linked project %s (use --project-id to override)\n", *projectID)
		} else {
			if proj == nil {
				fmt.Fprintln(os.Stderr, "Not inside a git repository — `remote sync` is repo-scoped.")
				fmt.Fprintln(os.Stderr, "Run it from a repo, use --all to sync every local project, or --project-id <id> to target one explicitly.")
				os.Exit(1)
			}
			if proj.RemoteKey == "" {
				fmt.Fprintf(os.Stderr, "Repository %q has no git remote 'origin', so it can't be matched across machines.\n", proj.Name)
				fmt.Fprintln(os.Stderr, "Add one (git remote add origin <url>) or use --project-id <id> to target a remote project explicitly.")
				os.Exit(1)
			}
		}
	}

	// Repo-scoped: pull only this project's memories. Without a project (the
	// --project-id override outside a repo) fall back to the full store.
	var memories []memory.Memory
	if proj != nil {
		memories, err = listProjectMemories(ctx, svc, proj.ID)
	} else {
		memories, err = listAllMemories(ctx, svc)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "list error: %v\n", err)
		os.Exit(1)
	}

	syncMemories := toSyncMemories(memories)
	preview := sync.ClassifyForPreview(syncMemories, projectRoot)

	if *dryRun {
		fmt.Println("=== DRY RUN: no network calls ===")
		if proj != nil {
			fmt.Printf("Repo:   %s\n", proj.Name)
			fmt.Printf("Origin: %s\n", orEmpty(gitOriginURL(projectRoot), "(none)"))
			fmt.Printf("Key:    %s\n", orEmpty(proj.RemoteKey, "(none)"))
		}
		fmt.Printf("Total: %d | Syncable: %d | Blocked: %d | Needs Review: %d\n\n",
			preview.Total, preview.Syncable, preview.Blocked, preview.NeedsReview)
		for _, item := range preview.Items {
			content := item.Memory.Content
			if len(content) > 80 {
				content = content[:80] + "..."
			}
			fmt.Printf("  %-12s %-8s %s\n", item.Classification, item.Memory.Category, content)
			if item.Reason != "" {
				fmt.Printf("  %12s └─ %s\n", "", item.Reason)
			}
		}
		return
	}

	if !cfg.Remote.Enabled {
		fmt.Fprintln(os.Stderr, "Remote sync is disabled. Enable in config or use --dry-run.")
		os.Exit(1)
	}

	// NewClient only returns nil when cfg.Remote.Enabled is false, which is
	// guarded above — no nil check needed here.
	client := sync.NewClient(cfg.Remote, "cli")

	pushMemories := make([]sync.SyncMemory, 0, preview.Syncable)
	for _, item := range preview.Items {
		if item.Classification != sync.ClassificationSyncable {
			continue
		}
		// item.Memory.Content is already path-rewritten by the preview.
		// In repo-scoped mode we stamp every memory with the cwd repo's
		// git-origin key so the server groups them into one project by origin,
		// regardless of any stale per-memory key.
		rpk := derefString(item.Memory.RemoteProjectKey)
		if proj != nil && proj.RemoteKey != "" {
			rpk = proj.RemoteKey
		}
		pushMemories = append(pushMemories, sync.SyncMemory{
			ID:               item.Memory.ID,
			Category:         item.Memory.Category,
			Content:          item.Memory.Content,
			Source:           item.Memory.Source,
			PreferenceScope:  item.Memory.PreferenceScope,
			RemoteProjectKey: rpk,
			Metadata:         item.Memory.Metadata,
		})
	}

	pushReq := sync.SyncPushRequest{
		ClientID:    "cli",
		ProjectID:   *projectID,
		Memories:    pushMemories,
		ProjectRoot: projectRoot,
	}
	// Send a friendly claim so a project auto-created by origin gets the repo's
	// name instead of "auto-<hash>". Only when routing by origin (no forced id).
	if *projectID == "" && proj != nil && proj.RemoteKey != "" {
		pushReq.ProjectClaim = &sync.ProjectClaim{Name: proj.Name, RemoteKey: proj.RemoteKey}
		fmt.Printf("Repo %q · origin %s · key %s → remote project %q\n",
			proj.Name, orEmpty(gitOriginURL(projectRoot), "(none)"), proj.RemoteKey, proj.Name)
	}

	resp, err := client.Push(ctx, pushReq)
	if err != nil {
		fmt.Fprintf(os.Stderr, "push error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Push complete: %d accepted, %d rejected\n", resp.Accepted, resp.Rejected)
	if len(resp.Errors) > 0 {
		for _, e := range resp.Errors {
			fmt.Fprintf(os.Stderr, "  error: %s\n", e)
		}
	}
}

// listProjectMemories paginates through every non-deleted memory belonging to a
// single local project.
func listProjectMemories(ctx context.Context, svc *memory.Service, projectID string) ([]memory.Memory, error) {
	const pageSize = 1000
	var all []memory.Memory
	offset := 0
	for {
		page, err := svc.List(ctx, memory.ListOptions{
			Limit:          pageSize,
			Offset:         offset,
			ProjectID:      projectID,
			IncludeDeleted: false,
		})
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageSize {
			return all, nil
		}
		offset += pageSize
	}
}


// listAllMemories paginates through every non-deleted memory.
// Using a fixed Limit caps the result set; without pagination, large stores
// silently drop rows past the cap.
func listAllMemories(ctx context.Context, svc *memory.Service) ([]memory.Memory, error) {
	const pageSize = 1000
	var all []memory.Memory
	offset := 0
	for {
		page, err := svc.List(ctx, memory.ListOptions{
			Limit:          pageSize,
			Offset:         offset,
			IncludeDeleted: false,
		})
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) < pageSize {
			return all, nil
		}
		offset += pageSize
	}
}

func toSyncMemories(memories []memory.Memory) []sync.Memory {
	out := make([]sync.Memory, len(memories))
	for i, m := range memories {
		out[i] = sync.Memory{
			ID:               m.ID,
			Category:         m.Category,
			Content:          m.Content,
			ProjectID:        m.ProjectID,
			Source:           m.Source,
			SyncOrigin:       m.SyncOrigin,
			SyncDirty:        m.SyncDirty,
			RemoteProjectKey: m.RemoteProjectKey,
			PreferenceScope:  preferenceScopeFromMetadata(m.Metadata),
			Metadata:         m.Metadata,
		}
	}
	return out
}

func preferenceScopeFromMetadata(v any) string {
	switch m := v.(type) {
	case memory.MemoryMetadata:
		return m.PreferenceScope
	case map[string]any:
		s, _ := m["scope"].(string)
		return s
	default:
		return memory.ParseMetadata(v).PreferenceScope
	}
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// gitOriginURL returns the raw `git remote get-url origin` for a directory, or
// "" when there is no repo/origin. Used only for human-readable confirmation
// output; routing relies on the detector's derived remote_key, not this string.
func gitOriginURL(dir string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
