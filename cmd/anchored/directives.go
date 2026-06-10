package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/jholhewres/anchored/pkg/memory"
)

// runDirectives manages the user's standing rules: short do/don't directives
// that the SessionStart hook injects at the top of every session, unranked.
// They are first-party instructions (written by the user, not recalled data),
// stored as pinned preference memories marked with metadata.directive=true.
func runDirectives(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: anchored directives <add|list|rm> ...")
		fmt.Fprintln(os.Stderr, "  add \"<text>\" [--project]   add a standing rule (global by default)")
		fmt.Fprintln(os.Stderr, "  list                         list standing rules")
		fmt.Fprintln(os.Stderr, "  rm <id>                      remove a standing rule (soft delete)")
		os.Exit(1)
	}
	switch args[0] {
	case "add":
		runDirectivesAdd(args[1:])
	case "list":
		runDirectivesList(args[1:])
	case "rm":
		runDirectivesRm(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown directives subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func runDirectivesAdd(args []string) {
	fs := newFlagSet("directives add")
	projectScoped := fs.Bool("project", false, "scope the rule to the current project (default: global)")
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(reorderArgsForFlag(fs, args))

	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		fmt.Fprintln(os.Stderr, "Usage: anchored directives add \"<text>\" [--project]")
		os.Exit(1)
	}

	_, _, svc, err := initService(*configPath)
	if err != nil {
		slog.Error("failed to initialize", "error", err)
		os.Exit(1)
	}
	defer svc.Close()

	// Global directives carry no CWD so they stay user-scoped (project_id '')
	// and load in every project; --project binds the rule to the current repo.
	cwd := ""
	scope := memory.ScopeUser
	if *projectScoped {
		cwd, _ = os.Getwd()
		scope = memory.ScopeProject
	}

	meta := memory.MemoryMetadata{
		MemoryType: memory.MemoryTypeSemantic,
		Kind:       "directive",
		Scope:      scope,
		Pinned:     true,
		Source:     "directive",
		Extra:      map[string]any{"directive": true},
	}

	m, err := svc.SaveWithOptions(context.Background(), memory.SaveOptions{
		Content:  text,
		Category: "preference",
		Source:   "directive",
		CWD:      cwd,
		Metadata: meta.ToAny(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "directives add error: %v\n", err)
		os.Exit(1)
	}

	scopeLabel := "global"
	if *projectScoped {
		scopeLabel = "project"
	}
	fmt.Printf("Added %s directive %s\n", scopeLabel, m.ID)
}

func runDirectivesList(args []string) {
	fs := newFlagSet("directives list")
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(args)

	hc, err := openHookContext(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "directives list error: %v\n", err)
		os.Exit(1)
	}
	defer hc.Close()

	rows, err := hc.db.QueryContext(context.Background(), `
		SELECT id, COALESCE(project_id, ''), content
		FROM memories
		WHERE json_extract(metadata, '$.directive') = 1
		  AND deleted_at IS NULL
		ORDER BY created_at ASC`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "directives list error: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var id, projectID, content string
		if err := rows.Scan(&id, &projectID, &content); err != nil {
			continue
		}
		scope := "global"
		if projectID != "" {
			scope = "project:" + shortID(projectID)
		}
		fmt.Printf("%s  [%s]  %s\n", shortID(id), scope, content)
		count++
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "directives list error: %v\n", err)
		os.Exit(1)
	}
	if count == 0 {
		fmt.Println("No directives. Add one with: anchored directives add \"never do X\"")
	}
}

func runDirectivesRm(args []string) {
	fs := newFlagSet("directives rm")
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(reorderArgsForFlag(fs, args))

	idArg := ""
	if fs.NArg() > 0 {
		idArg = strings.TrimSpace(fs.Arg(0))
	}
	if idArg == "" {
		fmt.Fprintln(os.Stderr, "Usage: anchored directives rm <id>")
		os.Exit(1)
	}

	hc, err := openHookContext(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "directives rm error: %v\n", err)
		os.Exit(1)
	}
	defer hc.Close()

	id, err := resolveDirectiveID(context.Background(), hc, idArg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "directives rm error: %v\n", err)
		os.Exit(1)
	}

	// Soft delete keeps the row recoverable and lets sync tombstones propagate
	// through the normal path. sync_dirty=1 is set explicitly (unlike the
	// store's SoftDelete) so the deletion is picked up by the next sync push.
	if _, err := hc.db.ExecContext(context.Background(),
		`UPDATE memories SET deleted_at = CURRENT_TIMESTAMP, sync_dirty = 1
		 WHERE id = ? AND deleted_at IS NULL`, id); err != nil {
		fmt.Fprintf(os.Stderr, "directives rm error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Removed directive %s\n", shortID(id))
}

// resolveDirectiveID accepts a full memory ID or a unique prefix, restricted
// to directive-marked rows so a short prefix can't soft-delete an arbitrary
// memory.
func resolveDirectiveID(ctx context.Context, hc *HookContext, idArg string) (string, error) {
	// Escape LIKE wildcards so a stray % or _ in the argument can't widen the
	// prefix match (the LIMIT 2 ambiguity check would still refuse to delete,
	// but the error message would be misleading).
	escaped := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(idArg)
	rows, err := hc.db.QueryContext(ctx, `
		SELECT id FROM memories
		WHERE json_extract(metadata, '$.directive') = 1
		  AND deleted_at IS NULL
		  AND id LIKE ? ESCAPE '\'
		LIMIT 2`, escaped+"%")
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var matches []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		matches = append(matches, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no directive matches %q", idArg)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("prefix %q is ambiguous, use a longer prefix", idArg)
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
