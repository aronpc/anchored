package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jholhewres/anchored/pkg/sync"

	_ "modernc.org/sqlite"
)

// runRemoteSyncPerProject pushes the local store one local-project at a time,
// creating one remote project per local-project so that the original
// segmentation is preserved. Memories without a local project are pushed to
// a fallback "misc" project.
//
// Usage: anchored remote sync-per-project [--config path] [--min-memories N]
func runRemoteSyncPerProject(args []string) {
	fs := newFlagSet("remote sync-per-project")
	configPath := fs.String("config", "", "path to config file")
	minMemories := fs.Int("min-memories", 1, "skip local projects with fewer syncable memories than this")
	fallback := fs.String("fallback-name", "misc", "remote project name for memories without a local project")
	fs.Parse(args)

	cfg, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	if !cfg.Remote.Enabled {
		fmt.Fprintln(os.Stderr, "Remote sync is disabled. Enable in config first.")
		os.Exit(1)
	}

	ctx := context.Background()

	memories, err := listAllMemories(ctx, svc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list error: %v\n", err)
		os.Exit(1)
	}

	// Resolve local project_id → name by querying the local sqlite store
	// directly. The memory.Service does not expose a project lookup.
	projectNames := loadLocalProjectNames(cfg.Memory.DatabasePath)

	syncMemories := toSyncMemories(memories)
	cwd, _ := os.Getwd()
	preview := sync.ClassifyForPreview(syncMemories, cwd)

	// Group syncable memories by local project_id.
	type bucket struct {
		name     string
		memories []sync.SyncMemory
	}
	groups := map[string]*bucket{}
	noProject := &bucket{name: *fallback}

	for _, item := range preview.Items {
		if item.Classification != sync.ClassificationSyncable {
			continue
		}
		sm := sync.SyncMemory{
			ID:               item.Memory.ID,
			Category:         item.Memory.Category,
			Content:          item.Memory.Content,
			Source:           item.Memory.Source,
			PreferenceScope:  item.Memory.PreferenceScope,
			RemoteProjectKey: derefString(item.Memory.RemoteProjectKey),
			Metadata:         item.Memory.Metadata,
		}
		if item.Memory.ProjectID == nil || *item.Memory.ProjectID == "" {
			noProject.memories = append(noProject.memories, sm)
			continue
		}
		pid := *item.Memory.ProjectID
		b, ok := groups[pid]
		if !ok {
			name := projectNames[pid]
			if name == "" {
				name = "project-" + pid[:8]
			}
			b = &bucket{name: name}
			groups[pid] = b
		}
		b.memories = append(b.memories, sm)
	}

	// Stable ordering: largest groups first.
	type pair struct {
		key string
		b   *bucket
	}
	all := make([]pair, 0, len(groups)+1)
	for k, v := range groups {
		all = append(all, pair{k, v})
	}
	if len(noProject.memories) > 0 {
		all = append(all, pair{"__no_project__", noProject})
	}

	// Print plan.
	fmt.Printf("Total syncable: %d across %d local projects\n", preview.Syncable, len(all))
	fmt.Printf("Threshold: skipping projects with <%d memories\n\n", *minMemories)
	fmt.Println("Plan:")
	skipped := 0
	pushable := 0
	for _, p := range all {
		marker := ""
		if len(p.b.memories) < *minMemories {
			marker = " (skip)"
			skipped++
		} else {
			pushable += len(p.b.memories)
		}
		fmt.Printf("  %-40s %5d%s\n", p.b.name, len(p.b.memories), marker)
	}
	fmt.Printf("\nWill push %d memories across %d projects (skipping %d small)\n\n",
		pushable, len(all)-skipped, skipped)

	client := sync.NewClient(cfg.Remote, "cli-per-project")
	httpAPI := newAdminAPI(cfg.Remote.ServerURL, cfg.Remote.APIKey)

	totalAccepted := 0
	totalRejected := 0
	for _, p := range all {
		if len(p.b.memories) < *minMemories {
			continue
		}
		slug := toRemoteSlug(p.b.name)
		remoteKey := slug + "-" + fmt.Sprintf("%d", time.Now().Unix())
		projID, err := httpAPI.ensureProject(p.b.name, slug, remoteKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [%s] create project failed: %v\n", p.b.name, err)
			continue
		}

		req := sync.SyncPushRequest{
			ClientID:    "cli-per-project",
			ProjectID:   projID,
			Memories:    p.b.memories,
			ProjectRoot: cwd,
		}
		resp, err := client.Push(ctx, req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [%s] push failed: %v\n", p.b.name, err)
			continue
		}
		fmt.Printf("  [%s] %d accepted, %d rejected\n", p.b.name, resp.Accepted, resp.Rejected)
		totalAccepted += resp.Accepted
		totalRejected += resp.Rejected
	}

	fmt.Printf("\nDone. %d accepted, %d rejected total.\n", totalAccepted, totalRejected)
}

func loadLocalProjectNames(dbPath string) map[string]string {
	expanded := dbPath
	if strings.HasPrefix(dbPath, "~/") {
		if home, _ := os.UserHomeDir(); home != "" {
			expanded = home + dbPath[1:]
		}
	}
	db, err := sql.Open("sqlite", expanded)
	if err != nil {
		slog.Warn("open local db for project lookup failed", "error", err)
		return map[string]string{}
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, name FROM projects`)
	if err != nil {
		slog.Warn("query local projects failed", "error", err)
		return map[string]string{}
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err == nil {
			out[id] = name
		}
	}
	return out
}

var slugInvalidRe = regexp.MustCompile(`[^a-z0-9-]+`)

func toRemoteSlug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ReplaceAll(s, "_", "-")
	s = slugInvalidRe.ReplaceAllString(s, "")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "project"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

type adminAPI struct {
	baseURL string
	apiKey  string
	client  *http.Client
}

func newAdminAPI(baseURL, apiKey string) *adminAPI {
	return &adminAPI{baseURL: baseURL, apiKey: apiKey, client: &http.Client{Timeout: 60 * time.Second}}
}

func (a *adminAPI) ensureProject(name, slug, remoteKey string) (string, error) {
	// Try existing first — server returns 500 on unique-violation instead of
	// 409, so probing by slug avoids the noisy create path entirely.
	if id, err := a.findProjectBySlug(slug); err == nil && id != "" {
		return id, nil
	}

	body := map[string]string{"name": name, "slug": slug, "remote_key": remoteKey}
	buf, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, a.baseURL+"/v1/projects", bytes.NewReader(buf))
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		// Any error: try fallback lookup before giving up — server may have
		// silently created the project on a prior retry.
		if id, err := a.findProjectBySlug(slug); err == nil && id != "" {
			return id, nil
		}
		return "", fmt.Errorf("create project %d: %s", resp.StatusCode, string(respBody))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

func (a *adminAPI) findProjectBySlug(slug string) (string, error) {
	req, _ := http.NewRequest(http.MethodGet, a.baseURL+"/v1/projects", nil)
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("list projects %d", resp.StatusCode)
	}
	var projects []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &projects); err != nil {
		return "", err
	}
	for _, p := range projects {
		if p.Slug == slug {
			return p.ID, nil
		}
	}
	return "", nil // not found is not an error here
}
