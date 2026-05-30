package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
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
	fmt.Fprintln(os.Stderr, "  anchored remote status                              Show remote sync config status")
	fmt.Fprintln(os.Stderr, "  anchored remote preview                             Preview which memories would sync (offline)")
	fmt.Fprintln(os.Stderr, "  anchored remote sync                                Sync memories to remote server (--dry-run for preview)")
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

func runRemoteSync(args []string) {
	fs := newFlagSet("remote sync")
	configPath := fs.String("config", "", "path to config file")
	project := fs.String("project", "", "project path filter (default: cwd)")
	dryRun := fs.Bool("dry-run", false, "preview what would be pushed without making network calls")
	projectID := fs.String("project-id", "", "remote project ID for sync")
	fs.Parse(args)

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

	memories, err := listAllMemories(ctx, svc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list error: %v\n", err)
		os.Exit(1)
	}

	syncMemories := toSyncMemories(memories)

	preview := sync.ClassifyForPreview(syncMemories, projectRoot)

	if *dryRun {
		fmt.Println("=== DRY RUN: no network calls ===")
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

	// Resolve the default remote project when --project-id is not given. Rather
	// than blindly using the first linked id (which goes stale when the server's
	// projects change — e.g. after a re-provision), validate the linked ids
	// against the server and pick the first that still exists, reporting stale
	// links with an unlink hint. This prevents every memory failing with
	// "project not found" because the default pointed at a deleted project.
	if *projectID == "" {
		resolved, err := resolveDefaultProject(ctx, client, cfg.Remote.Projects, cfg.Remote.ServerURL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		if resolved != "" {
			*projectID = resolved
			fmt.Printf("Using linked project %s as default (override with --project-id)\n", resolved)
		}
	}

	pushMemories := make([]sync.SyncMemory, 0, preview.Syncable)
	for _, item := range preview.Items {
		if item.Classification != sync.ClassificationSyncable {
			continue
		}
		// item.Memory.Content is already path-rewritten by the preview;
		// PreferenceScope and RemoteProjectKey are carried through so the
		// server can route per-project and skip personal preferences.
		pushMemories = append(pushMemories, sync.SyncMemory{
			ID:               item.Memory.ID,
			Category:         item.Memory.Category,
			Content:          item.Memory.Content,
			Source:           item.Memory.Source,
			PreferenceScope:  item.Memory.PreferenceScope,
			RemoteProjectKey: derefString(item.Memory.RemoteProjectKey),
			Metadata:         item.Memory.Metadata,
		})
	}

	pushReq := sync.SyncPushRequest{
		ClientID:    "cli",
		ProjectID:   *projectID,
		Memories:    pushMemories,
		ProjectRoot: projectRoot,
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

// resolveDefaultProject picks the first linked project that still exists on the
// server. Stale links (projects deleted/changed server-side) are reported with
// an unlink hint rather than silently failing every push. Returns "" with no
// error when there are no links (each memory then routes via its own
// remote_project_key, and the server validates per memory).
func resolveDefaultProject(ctx context.Context, client *sync.Client, linked []string, serverURL string) (string, error) {
	if len(linked) == 0 {
		return "", nil
	}

	serverProjects, err := client.ListProjects(ctx)
	if err != nil {
		// Could not verify (transient/endpoint issue). Fall back to the first
		// link rather than blocking sync — the server still validates per memory.
		fmt.Fprintf(os.Stderr, "warning: could not verify linked projects against the server (%v); using first link\n", err)
		return linked[0], nil
	}

	live := make(map[string]struct{}, len(serverProjects))
	for _, p := range serverProjects {
		live[p.ID] = struct{}{}
	}

	var firstValid string
	var stale []string
	for _, id := range linked {
		if _, ok := live[id]; ok {
			if firstValid == "" {
				firstValid = id
			}
		} else {
			stale = append(stale, id)
		}
	}

	if len(stale) > 0 {
		fmt.Fprintf(os.Stderr, "note: %d linked project(s) no longer exist on the server and were ignored:\n", len(stale))
		for _, id := range stale {
			fmt.Fprintf(os.Stderr, "  %s   (remove with: anchored remote unlink %s)\n", id, id)
		}
	}

	if firstValid == "" {
		return "", fmt.Errorf("none of your linked projects exist on %s — link a current project id (from the dashboard) with: anchored remote link <project_id>", serverURL)
	}
	return firstValid, nil
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
