package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jholhewres/anchored/pkg/config"
	"github.com/jholhewres/anchored/pkg/memory"
	"github.com/jholhewres/anchored/pkg/project"
)

// `anchored project consolidate` folds the project rows of one repository into
// a single project.
//
// Identity used to be the path, so every checkout of a repository — a linked
// worktree, a second clone, a directory later renamed — got a row of its own
// and started with no memory of it. Detection now keys on the git origin, but
// only from now on: reading the origin of the rows that already fragmented
// needs git and the filesystem, so folding them cannot be a SQL migration.
//
// Like `anchored migrate`, the first run writes nothing.
func runProject(args []string) {
	if len(args) == 0 {
		projectUsage()
		os.Exit(1)
	}
	switch args[0] {
	case "consolidate":
		runProjectConsolidate(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "anchored project: unknown subcommand %q\n\n", args[0])
		projectUsage()
		os.Exit(1)
	}
}

func projectUsage() {
	fmt.Fprintf(os.Stderr, `Usage: anchored project consolidate [--apply]

Folds the project rows of one repository into a single project.
`)
}

func runProjectConsolidate(args []string) {
	fs := flag.NewFlagSet("project consolidate", flag.ExitOnError)
	apply := fs.Bool("apply", false, "actually write; without it the run is a dry run")
	dbPath := fs.String("db", "", "path to the local database (default: from config)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: anchored project consolidate [--apply]

Folds the project rows of one repository into a single project, so every
checkout of it shares one memory again. Rows fragmented while identity was the
path: a linked worktree, a second clone, a directory later renamed.

The first run is a DRY RUN: it reports which projects would be folded, into
what, and how many rows move. Nothing is written until you pass --apply.

A linked worktree folds into its repository. Checkouts with the same git
origin fold into the one holding the most memory. A repository with no origin
is identified by its path alone and is left as it is, and so is a submodule
with an origin of its own. A project whose path is gone from disk is folded
when its stored remote_key matches a repository still present; otherwise it is
reported and left alone, because nothing identifies it.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}

	path := *dbPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "anchored project: resolve home: %v\n", err)
			os.Exit(1)
		}
		cfg, err := config.Load(filepath.Join(home, ".anchored", "config.yaml"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "anchored project: load config: %v\n", err)
			os.Exit(1)
		}
		path = cfg.Memory.DatabasePath
	}
	path = expandPath(path)

	store, err := memory.NewSQLiteStore(path, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "anchored project: open %s: %v\n", path, err)
		os.Exit(1)
	}
	defer store.Close()

	d := project.NewDetector(store.DB())
	report, err := d.PlanConsolidate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "anchored project: plan: %v\n", err)
		os.Exit(1)
	}

	printConsolidateReport(report, *apply)

	if len(report.Merges) == 0 {
		return
	}
	if !*apply {
		fmt.Printf("\nNothing was written. Re-run with --apply to consolidate.\n")
		return
	}
	if err := d.ApplyConsolidate(report); err != nil {
		fmt.Fprintf(os.Stderr, "anchored project: apply: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("\nConsolidated %d project(s), %d row(s) reattributed.\n",
		len(report.Merges), report.Affected())
}

func printConsolidateReport(r *project.Report, apply bool) {
	mode := "DRY RUN"
	if apply {
		mode = "APPLY"
	}
	fmt.Printf("anchored project consolidate — %s\n", mode)
	fmt.Printf("Scanned %d project(s).\n\n", r.Scanned)

	if len(r.Merges) == 0 {
		fmt.Println("No projects to fold. Nothing to do.")
	}

	for _, m := range r.Merges {
		switch m.Kind {
		case project.KindRepath:
			fmt.Printf("  repath  %s\n", m.From.Path)
			fmt.Printf("       ->  %s\n", m.IntoPath)
			fmt.Printf("          (the repository has no project row yet; this one moves onto it)\n")
		default:
			label := "merge "
			if m.Kind == project.KindOrigin {
				label = "origin"
			}
			fmt.Printf("  %s  %s  [%s]\n", label, m.From.Path, m.From.ID[:min(8, len(m.From.ID))])
			fmt.Printf("       ->  %s  [%s]\n", m.Into.Path, m.Into.ID[:min(8, len(m.Into.ID))])
			if len(m.Rows) == 0 {
				fmt.Printf("          no rows to move (this checkout never accumulated anything)\n")
			} else {
				tables := make([]string, 0, len(m.Rows))
				for t := range m.Rows {
					tables = append(tables, t)
				}
				sort.Strings(tables)
				for _, t := range tables {
					fmt.Printf("          %-20s %d\n", t, m.Rows[t])
				}
			}
		}
		fmt.Println()
	}

	if len(r.Missing) > 0 {
		stranded := 0
		for _, s := range r.Missing {
			stranded += s.Memories
		}
		fmt.Printf("Left alone — path no longer on disk (%d projects, %d memories):\n",
			len(r.Missing), stranded)
		for _, s := range r.Missing {
			fmt.Printf("  %-60s %d memories\n", s.Project.Path, s.Memories)
		}
		fmt.Printf("\n  The directory is gone and no repository on disk shares their remote_key,\n")
		fmt.Printf("  so nothing identifies them and nothing is guessed.\n")
		fmt.Printf("  Restore a path and re-run to fold it, or leave them as they are.\n\n")
	}

	if len(r.Merges) > 0 {
		fmt.Printf("Total: %d project(s) folded, %d row(s) reattributed.\n",
			len(r.Merges), r.Affected())
	}
}
