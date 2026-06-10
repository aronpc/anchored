package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/jholhewres/anchored/pkg/session"
)

// runTask manages cross-project task threads (Feature B): a ticket-keyed unit
// of work that spans repositories. The active thread is normally inferred
// from the branch name (feature/PROJ-123-...); these commands are the
// explicit override and the lifecycle controls. No interactive prompts —
// every transition is a command.
func runTask(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: anchored task <start|pause|resume|done|cancel|note|status> ...")
		fmt.Fprintln(os.Stderr, "  start <KEY> [--ref URL]   start (or reactivate) a task thread")
		fmt.Fprintln(os.Stderr, "  pause <KEY>               pause the thread (kept, not injected)")
		fmt.Fprintln(os.Stderr, "  resume <KEY>              reactivate a paused thread")
		fmt.Fprintln(os.Stderr, "  done <KEY>                close the thread and consolidate it into a summary memory")
		fmt.Fprintln(os.Stderr, "  cancel <KEY>              close the thread WITHOUT consolidating")
		fmt.Fprintln(os.Stderr, "  note <KEY> \"<text>\"       append a short journal note to the thread")
		fmt.Fprintln(os.Stderr, "  status                    show active threads (and the branch-inferred key here)")
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]

	switch sub {
	case "start":
		runTaskStart(rest)
	case "pause":
		runTaskSetStatus(rest, session.TaskStatusPaused, "Paused")
	case "resume":
		runTaskSetStatus(rest, session.TaskStatusActive, "Resumed")
	case "cancel":
		runTaskSetStatus(rest, session.TaskStatusCancelled, "Cancelled (not consolidated)")
	case "done":
		runTaskDone(rest)
	case "note":
		runTaskNote(rest)
	case "status":
		runTaskStatus(rest)
	default:
		fmt.Fprintf(os.Stderr, "Unknown task subcommand: %s\n", sub)
		os.Exit(1)
	}
}

func taskManager(configPath string) (*HookContext, *session.Manager, error) {
	hc, err := openHookContextWrite(configPath)
	if err != nil {
		return nil, nil, err
	}
	return hc, session.NewManager(hc.db, nil), nil
}

func runTaskStart(args []string) {
	fs := newFlagSet("task start")
	ref := fs.String("ref", "", "external reference (Jira/Trello URL or ID)")
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(reorderArgsForFlag(fs, args))
	key := firstArg(fs.Args())
	if key == "" {
		fmt.Fprintln(os.Stderr, "Usage: anchored task start <KEY> [--ref URL]")
		os.Exit(1)
	}

	hc, mgr, err := taskManager(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "task start error: %v\n", err)
		os.Exit(1)
	}
	defer hc.Close()

	cwd, _ := os.Getwd()
	projectID := hc.ResolveProject(cwd)

	// start on a terminal thread reopens it explicitly.
	if t, _ := mgr.GetTaskThread(context.Background(), strings.ToUpper(key)); t != nil &&
		(t.Status == session.TaskStatusDone || t.Status == session.TaskStatusCancelled) {
		if err := mgr.SetTaskStatus(context.Background(), key, session.TaskStatusActive); err != nil {
			fmt.Fprintf(os.Stderr, "task start error: %v\n", err)
			os.Exit(1)
		}
	}

	t, err := mgr.UpsertTaskThread(context.Background(), key, session.TaskThreadDelta{
		ProjectID:   projectID,
		ExternalRef: *ref,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "task start error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Task %s active (%d project(s) touched)\n", t.TaskKey, len(t.ProjectIDs))
}

func runTaskSetStatus(args []string, status, label string) {
	fs := newFlagSet("task " + status)
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(reorderArgsForFlag(fs, args))
	key := firstArg(fs.Args())
	if key == "" {
		fmt.Fprintln(os.Stderr, "Usage: anchored task <pause|resume|cancel> <KEY>")
		os.Exit(1)
	}
	hc, mgr, err := taskManager(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "task error: %v\n", err)
		os.Exit(1)
	}
	defer hc.Close()
	if err := mgr.SetTaskStatus(context.Background(), key, status); err != nil {
		fmt.Fprintf(os.Stderr, "task error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s task %s\n", label, strings.ToUpper(key))
}

// runTaskDone closes the thread and consolidates it into a durable summary
// memory — the moment the ephemeral working state becomes lasting knowledge.
func runTaskDone(args []string) {
	fs := newFlagSet("task done")
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(reorderArgsForFlag(fs, args))
	key := firstArg(fs.Args())
	if key == "" {
		fmt.Fprintln(os.Stderr, "Usage: anchored task done <KEY>")
		os.Exit(1)
	}

	// The consolidation save goes through the full service (sanitization,
	// keywords, sync eligibility), so initService is appropriate here — this
	// is a CLI command, not a latency-bound hook.
	_, _, svc, err := initService(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "task done error: %v\n", err)
		os.Exit(1)
	}
	defer svc.Close()

	mgr := session.NewManager(svc.StoreDB(), nil)
	ctx := context.Background()
	t, err := mgr.GetTaskThread(ctx, strings.ToUpper(strings.TrimSpace(key)))
	if err != nil || t == nil {
		fmt.Fprintf(os.Stderr, "task done error: task %q not found\n", key)
		os.Exit(1)
	}

	// Resolve project names for the summary — IDs are meaningless to a reader.
	names := make([]string, 0, len(t.ProjectIDs))
	for _, pid := range t.ProjectIDs {
		var name string
		if err := svc.StoreDB().QueryRowContext(ctx,
			`SELECT name FROM projects WHERE id = ?`, pid).Scan(&name); err != nil || name == "" {
			name = pid
		}
		names = append(names, name)
	}
	summary := renderTaskSummary(t, names)
	cwd, _ := os.Getwd()
	if _, err := svc.Save(ctx, summary, "summary", "task_done", cwd); err != nil {
		fmt.Fprintf(os.Stderr, "task done: consolidation save failed: %v\n", err)
		os.Exit(1)
	}
	if err := mgr.SetTaskStatus(ctx, t.TaskKey, session.TaskStatusDone); err != nil {
		fmt.Fprintf(os.Stderr, "task done error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Task %s done — consolidated into a summary memory (%d project(s), %d journal note(s))\n",
		t.TaskKey, len(t.ProjectIDs), len(t.Journal))
}

func runTaskNote(args []string) {
	fs := newFlagSet("task note")
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(reorderArgsForFlag(fs, args))
	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "Usage: anchored task note <KEY> \"<text>\"")
		os.Exit(1)
	}
	key := fs.Arg(0)
	note := strings.TrimSpace(strings.Join(fs.Args()[1:], " "))

	hc, mgr, err := taskManager(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "task note error: %v\n", err)
		os.Exit(1)
	}
	defer hc.Close()
	if _, err := mgr.UpsertTaskThread(context.Background(), key, session.TaskThreadDelta{JournalNote: note}); err != nil {
		fmt.Fprintf(os.Stderr, "task note error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Noted on %s\n", strings.ToUpper(key))
}

func runTaskStatus(args []string) {
	fs := newFlagSet("task status")
	configPath := fs.String("config", "", "path to config file")
	fs.Parse(args)

	hc, mgr, err := taskManager(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "task status error: %v\n", err)
		os.Exit(1)
	}
	defer hc.Close()

	cwd, _ := os.Getwd()
	if key := session.InferTaskKey(currentGitBranch(cwd)); key != "" {
		fmt.Printf("Branch here infers task: %s\n", key)
	}

	threads, err := mgr.ActiveTaskThreads(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "task status error: %v\n", err)
		os.Exit(1)
	}
	if len(threads) == 0 {
		fmt.Println("No active task threads. Start one with: anchored task start <KEY>")
		return
	}
	for _, t := range threads {
		line := fmt.Sprintf("%s  [%s]  projects=%d", t.TaskKey, t.Status, len(t.ProjectIDs))
		if t.ExternalRef != "" {
			line += "  ref=" + t.ExternalRef
		}
		if len(t.Journal) > 0 {
			line += "  last_note=" + truncateRunes(t.Journal[0], 60)
		}
		fmt.Println(line)
	}
}

// currentGitBranch returns the checked-out branch of cwd, or "" outside a
// repo, on detached HEAD, or on any error. symbolic-ref (not rev-parse) so a
// branch with no commits yet still resolves. Hard 300ms timeout — this runs
// inside the SessionStart hook, which must never hang on a wedged git or a
// slow network filesystem. Always best-effort.
func currentGitBranch(cwd string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func firstArg(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return strings.TrimSpace(args[0])
}

// renderTaskSummary turns a finished thread into the content of its
// consolidation memory. projectNames is positional with t.ProjectIDs.
func renderTaskSummary(t *session.TaskThread, projectNames []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task %s concluded.", t.TaskKey)
	if t.ExternalRef != "" {
		fmt.Fprintf(&b, " Ref: %s.", t.ExternalRef)
	}
	if len(projectNames) > 0 {
		fmt.Fprintf(&b, " Projects touched: %s.", strings.Join(projectNames, ", "))
	}
	if len(t.Journal) > 0 {
		b.WriteString(" Journal: ")
		// Journal is newest-first; replay oldest-first for a readable recap.
		for i := len(t.Journal) - 1; i >= 0; i-- {
			b.WriteString(t.Journal[i])
			if i > 0 {
				b.WriteString("; ")
			}
		}
		b.WriteString(".")
	}
	return b.String()
}
