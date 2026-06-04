package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/memory"
	"github.com/jholhewres/anchored/pkg/sync"
)

func runSearch(args []string) {
	fs := newFlagSet("search")
	category := fs.String("category", "", "filter by category")
	project := fs.String("project", "", "filter by project ID")
	cwd := fs.String("cwd", "", "current working directory for project detection")
	global := fs.Bool("global", false, "search across all projects")
	limit := fs.Int("limit", 10, "max results")
	configPath := fs.String("config", "", "path to config file")
	remote := fs.String("remote", "", "search on remote server (name or empty for default)")

	remoteSet := hasExplicitFlag(args, "remote")
	fs.Parse(reorderArgsForFlag(fs, args))

	query := strings.Join(fs.Args(), " ")
	if query == "" {
		fmt.Fprintln(os.Stderr, "Usage: anchored search <query> [--category] [--project] [--cwd] [--global] [--limit] [--remote]")
		os.Exit(1)
	}

	_, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	ctx := context.Background()

	if remoteSet {
		if results, ok := searchRemote(ctx, svc, *configPath, *remote, *project, *cwd, query, *limit); ok {
			if len(results) == 0 {
				fmt.Println("No results found on remote.")
				return
			}
			for i, r := range results {
				fmt.Printf("%d. [%s] %s (id=%s project=%s updated=%s)\n",
					i+1, r.Category, truncate(r.Content, 120), r.ID, r.ProjectID, r.UpdatedAt)
			}
			return
		}
		// Remote failed — fall through to local search
	}

	projectID := *project
	if projectID == "" && *cwd != "" {
		projectID = svc.ResolveProject(*cwd)
	}
	if projectID != "" {
		resolved, err := resolveProjectFilter(ctx, svc, projectID)
		if err != nil {
			projectID = ""
		} else {
			projectID = resolved
		}
	}

	if *global {
		projectID = ""
	}

	opts := memory.SearchOptions{
		MaxResults: *limit,
		Category:   *category,
		ProjectID:  projectID,
	}

	results, err := svc.Search(ctx, query, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "search error: %v\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return
	}

	for i, r := range results {
		projectLabel := ""
		if *global && r.Memory.ProjectID != nil && *r.Memory.ProjectID != "" {
			projectLabel = fmt.Sprintf(" project=%s", *r.Memory.ProjectID)
		}
		fmt.Printf("%d. [%s]%s %s (score=%.3f id=%s)\n", i+1, r.Memory.Category, projectLabel, truncate(r.Memory.Content, 120), r.Score, r.Memory.ID)
	}
}

func searchRemote(ctx context.Context, svc *memory.Service, configPath, remoteName, projectID, cwd, query string, limit int) ([]sync.RemoteSearchResult, bool) {
	cfg, err := loadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "remote search unavailable: %v — falling back to local\n", err)
		return nil, false
	}

	var entry *config.RemoteEntry
	if remoteName != "" && cfg.Remotes != nil {
		if e, ok := cfg.Remotes[remoteName]; ok {
			e.Name = remoteName
			entry = &e
		}
	}
	if entry == nil {
		rCwd := cwd
		if rCwd == "" {
			rCwd, _ = os.Getwd()
		}
		entry = cfg.ResolveRemote(rCwd)
	}

	if entry == nil {
		fmt.Fprintln(os.Stderr, "no remote configured, falling back to local search")
		return nil, false
	}

	if projectID == "" {
		// Resolve the REMOTE project: the server only knows its own IDs. With
		// an explicit --remote name, match the repo's git-origin key against
		// that server only; otherwise probe every configured remote so a
		// freshly-configured second server works with zero routing setup.
		rCwd := cwd
		if rCwd == "" {
			rCwd, _ = os.Getwd()
		}
		originRouted := false
		if proj, pErr := svc.ResolveProjectInfo(rCwd); pErr == nil && proj != nil && proj.RemoteKey != "" {
			originRouted = true
			if remoteName != "" {
				client := sync.NewClientFromEntry(*entry, "cli")
				projectID = client.ResolveProjectIDByRemoteKey(ctx, proj.RemoteKey)
			} else if target, pid := sync.ResolveProjectAcrossRemotes(ctx, cfg, rCwd, proj.RemoteKey, "cli"); target != nil && pid != "" {
				entry = target
				projectID = pid
			}
		}
		if projectID == "" && !originRouted && len(entry.Projects) > 0 {
			projectID = entry.Projects[0]
		}
		if projectID == "" {
			fmt.Fprintln(os.Stderr, "remote search unavailable: no matching remote project for this repo — falling back to local")
			return nil, false
		}
	}

	client := sync.NewClientFromEntry(*entry, "cli")
	results, err := client.SearchRemote(ctx, projectID, query, limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "remote search unavailable: %v — falling back to local\n", err)
		return nil, false
	}
	return results, true
}

func resolveProjectFilter(ctx context.Context, svc *memory.Service, spec string) (string, error) {
	var id string
	err := svc.StoreDB().QueryRowContext(ctx,
		`SELECT id FROM projects
		 WHERE id = ? OR name = ? OR path = ? OR path LIKE ?
		 ORDER BY CASE WHEN id = ? THEN 0 WHEN name = ? THEN 1 WHEN path = ? THEN 2 ELSE 3 END
		 LIMIT 1`,
		spec, spec, spec, "%/"+spec, spec, spec, spec,
	).Scan(&id)
	if err == nil {
		return id, nil
	}

	rows, err := svc.StoreDB().QueryContext(ctx, "SELECT id, name, path FROM projects")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	normalizedSpec := normalizeProjectSpec(spec)
	for rows.Next() {
		var projectID, name, path string
		if err := rows.Scan(&projectID, &name, &path); err != nil {
			return "", err
		}
		if normalizeProjectSpec(name) == normalizedSpec || normalizeProjectSpec(filepath.Base(path)) == normalizedSpec {
			return projectID, nil
		}
	}
	return "", fmt.Errorf("project %q not found", spec)
}

func normalizeProjectSpec(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	replacer := strings.NewReplacer(".", "", "-", "", "_", "", " ", "")
	return replacer.Replace(s)
}

func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
