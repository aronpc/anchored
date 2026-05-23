package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/jholhewres/anchored/pkg/memory"
	"github.com/jholhewres/anchored/pkg/sync"
)

func runRemote(args []string) {
	if len(args) == 0 {
		printRemoteUsage()
		return
	}
	switch args[0] {
	case "status":
		runRemoteStatus(args[1:])
	case "preview":
		runRemotePreview(args[1:])
	case "sync":
		runRemoteSync(args[1:])
	default:
		printRemoteUsage()
	}
}

func printRemoteUsage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  anchored remote status   Show remote sync config status")
	fmt.Fprintln(os.Stderr, "  anchored remote preview  Preview which memories would sync (offline)")
	fmt.Fprintln(os.Stderr, "  anchored remote sync     Sync memories to remote server (--dry-run for preview)")
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

	cfg, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	_ = cfg

	ctx := context.Background()

	opts := memory.ListOptions{
		Limit:           10000,
		IncludeDeleted:  false,
	}

	projectRoot := *project
	if projectRoot == "" {
		projectRoot, _ = os.Getwd()
	}

	memories, err := svc.List(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list error: %v\n", err)
		os.Exit(1)
	}

	syncMemories := make([]sync.Memory, len(memories))
	for i, m := range memories {
		syncMemories[i] = sync.Memory{
			ID:         m.ID,
			Category:   m.Category,
			Content:    m.Content,
			ProjectID:  m.ProjectID,
			Source:     m.Source,
			SyncOrigin: m.SyncOrigin,
			SyncDirty:  m.SyncDirty,
			Metadata:   m.Metadata,
		}
	}

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

	opts := memory.ListOptions{
		Limit:          10000,
		IncludeDeleted: false,
	}

	projectRoot := *project
	if projectRoot == "" {
		projectRoot, _ = os.Getwd()
	}

	memories, err := svc.List(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list error: %v\n", err)
		os.Exit(1)
	}

	syncMemories := make([]sync.Memory, len(memories))
	for i, m := range memories {
		syncMemories[i] = sync.Memory{
			ID:         m.ID,
			Category:   m.Category,
			Content:    m.Content,
			ProjectID:  m.ProjectID,
			Source:     m.Source,
			SyncOrigin: m.SyncOrigin,
			SyncDirty:  m.SyncDirty,
			Metadata:   m.Metadata,
		}
	}

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

	client := sync.NewClient(cfg.Remote, "cli")
	if client == nil {
		fmt.Fprintln(os.Stderr, "Failed to create sync client.")
		os.Exit(1)
	}

	pushMemories := make([]sync.SyncMemory, 0, preview.Syncable)
	for _, item := range preview.Items {
		if item.Classification != sync.ClassificationSyncable {
			continue
		}
		pushMemories = append(pushMemories, sync.SyncMemory{
			ID:       item.Memory.ID,
			Category: item.Memory.Category,
			Content:  item.Memory.Content,
			Source:   item.Memory.Source,
		})
	}

	pushReq := sync.SyncPushRequest{
		ClientID:  "cli",
		ProjectID: *projectID,
		Memories:  pushMemories,
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
