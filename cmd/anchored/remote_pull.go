package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/memory"
	"github.com/jholhewres/anchored/pkg/sync"

	_ "modernc.org/sqlite"
)

// runRemotePull materializes memories from the remote server into the local
// store — the inverse of `remote sync`. It is the half of cross-machine sync
// that was missing: until now memories from other machines only surfaced in
// search (lazy); pull writes them into the local SQLite so they are available
// offline and indexed like any local memory.
//
// Each pulled memory is saved with the local project's id (matched by git-origin
// remote_key) and stamped sync_origin='remote', so the push path classifies it
// as already_synced and never re-sends it (closing the A↔B loop). A per-project
// watermark (sync_state table) makes the pull incremental.
//
// Usage:
//
//	anchored remote pull                 pull the CURRENT repo by its git origin
//	anchored remote pull --all           pull every local project that has a remote_key
//	anchored remote pull --project-id ID force a specific remote project
//	anchored remote pull --dry-run       show what would arrive, write nothing
func runRemotePull(args []string) {
	fs := newFlagSet("remote pull")
	configPath := fs.String("config", "", "path to config file")
	all := fs.Bool("all", false, "pull every local project that has a git-origin remote_key")
	projectID := fs.String("project-id", "", "force a specific remote project id to pull from (overrides git-origin routing)")
	remoteName := fs.String("remote", "", "use a specific named remote (default: the default remote)")
	dryRun := fs.Bool("dry-run", false, "show what would be pulled without writing to the local store")
	fs.Parse(args)

	cfg, logger, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	client := buildPullClient(cfg, *remoteName)
	if client == nil {
		fmt.Fprintln(os.Stderr, "No remote configured. Run `anchored remote configure --server <url> --key <key>` first.")
		os.Exit(1)
	}

	ctx := context.Background()
	verb := pullVerb(*dryRun) // "pulled" or "would pull"

	// --project-id: a single forced remote project, no origin routing.
	if *projectID != "" {
		proj, _ := svc.ResolveProjectInfo(mustCwd())
		lp := localProj{name: "(forced)"}
		if proj != nil {
			lp.id, lp.name, lp.remoteKey = proj.ID, proj.Name, proj.RemoteKey
		}
		n, err := pullFromRemoteProject(ctx, svc, client, lp, *projectID, *dryRun, logger)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pull failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Pull complete: %d memories (remote project %s).\n", n, *projectID)
		return
	}

	// Build the list of local projects to pull.
	var projects []localProj
	switch {
	case *all:
		projects = loadLocalProjectsWithRemoteKey(cfg.Memory.DatabasePath)
		if len(projects) == 0 {
			fmt.Println("No local projects with a git-origin remote_key — nothing to pull.")
			fmt.Println("Run `anchored remote sync` first to register projects on the server.")
			return
		}
	default:
		proj, err := svc.ResolveProjectInfo(mustCwd())
		if err != nil || proj == nil || proj.RemoteKey == "" {
			fmt.Fprintln(os.Stderr, "Not inside a repository with a git remote 'origin' — `remote pull` is repo-scoped.")
			fmt.Fprintln(os.Stderr, "Run it from a repo, use --all, or pass --project-id <id>.")
			os.Exit(1)
		}
		projects = []localProj{{id: proj.ID, name: proj.Name, remoteKey: proj.RemoteKey}}
	}

	total, failed := 0, 0
	for _, p := range projects {
		// Resolve the server-side project id from the local repo's origin key.
		remoteProjID, _ := client.ResolveProjectIDByRemoteKeys(ctx, p.remoteKey)
		if remoteProjID == "" {
			fmt.Fprintf(os.Stderr, "  [%s] no remote project for key %s — run `anchored remote sync` to create it first.\n", p.name, p.remoteKey)
			continue
		}
		n, err := pullFromRemoteProject(ctx, svc, client, p, remoteProjID, *dryRun, logger)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [%s] pull failed: %v\n", p.name, err)
			failed++
			continue
		}
		fmt.Printf("  [%s] %s %d memories\n", p.name, verb, n)
		total += n
	}

	fmt.Printf("\n%s %d memories across %d projects (%d failed).\n", capitalize(verb), total, len(projects), failed)
	if failed > 0 {
		os.Exit(1)
	}
}

// pullFromRemoteProject fetches incremental memories from one remote project
// and materializes them in the local store (unless dryRun). It is the core of
// the pull path, kept separate so the --all loop and the single-project path
// share it.
func pullFromRemoteProject(ctx context.Context, svc *memory.Service, client *sync.Client, p localProj, remoteProjID string, dryRun bool, logger *slog.Logger) (int, error) {
	wm, err := svc.GetPullWatermark(ctx, p.id)
	if err != nil {
		return 0, fmt.Errorf("get watermark: %w", err)
	}
	resp, err := client.Pull(ctx, sync.SyncPullRequest{ClientID: "cli", ProjectID: remoteProjID, Watermark: wm})
	if err != nil {
		return 0, fmt.Errorf("pull: %w", err)
	}
	if dryRun {
		return len(resp.Memories), nil
	}

	var projectID *string
	if p.id != "" {
		pid := p.id
		projectID = &pid
	}
	for _, m := range resp.Memories {
		source := m.Source
		if source == "" {
			source = "remote-sync"
		}
		if _, err := svc.SaveRemoteMemory(ctx, memory.SaveOptions{
			Content:   m.Content,
			Category:  m.Category,
			Source:    source,
			ProjectID: projectID,
		}); err != nil {
			logger.Warn("pull: save remote memory failed", "error", err)
		}
	}
	// Advance the watermark even when some saves warned, so a re-run doesn't
	// re-fetch the same batch forever; the dedup (content_hash) absorbs any
	// that did land.
	if err := svc.SetPullState(ctx, p.id, p.remoteKey, resp.Watermark, "cli"); err != nil {
		return len(resp.Memories), fmt.Errorf("set pull state: %w", err)
	}
	return len(resp.Memories), nil
}

// buildPullClient resolves the remote to pull from: a named entry when --remote
// is set, else the default (named "default", falling back to the legacy singular
// block, as the per-project sync path uses). Returns nil when nothing usable
// is configured.
func buildPullClient(cfg *config.Config, name string) *sync.Client {
	if name != "" {
		entry, ok := cfg.Remotes[name]
		if !ok {
			fmt.Fprintf(os.Stderr, "remote %q not found in config (available: %s)\n", name, remoteNames(cfg))
			os.Exit(1)
		}
		entry.Name = name
		return sync.NewClientFromEntry(entry, "cli")
	}
	if cfg.Remotes != nil {
		if def, ok := cfg.Remotes["default"]; ok {
			def.Name = "default"
			return sync.NewClientFromEntry(def, "cli")
		}
	}
	if cfg.Remote.ServerURL == "" {
		return nil
	}
	return sync.NewClient(cfg.Remote, "cli")
}

// loadLocalProjectsWithRemoteKey lists local projects that carry a git-origin
// remote_key — i.e. repos that can be matched to a server-side project. Reads
// the sqlite store directly because memory.Service does not expose a project
// lookup.
func loadLocalProjectsWithRemoteKey(dbPath string) []localProj {
	expanded := dbPath
	if strings.HasPrefix(expanded, "~/") {
		if home, _ := os.UserHomeDir(); home != "" {
			expanded = home + expanded[1:]
		}
	}
	db, err := sql.Open("sqlite", expanded)
	if err != nil {
		slog.Warn("open local db for project lookup failed", "error", err)
		return nil
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, name, remote_key FROM projects WHERE remote_key IS NOT NULL AND remote_key != ''`)
	if err != nil {
		slog.Warn("query local projects failed", "error", err)
		return nil
	}
	defer rows.Close()
	var out []localProj
	for rows.Next() {
		var p localProj
		if err := rows.Scan(&p.id, &p.name, &p.remoteKey); err == nil {
			out = append(out, p)
		}
	}
	return out
}

type localProj struct {
	id        string
	name      string
	remoteKey string
}

func mustCwd() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func pullVerb(dryRun bool) string {
	if dryRun {
		return "would pull"
	}
	return "pulled"
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
