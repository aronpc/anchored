package project

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Consolidation repairs the project rows written while identity was the path.
// Every checkout of a repository — a linked worktree, a local clone, the same
// directory before it was renamed — got a row of its own and started with no
// memory, while the repository's history stayed under whichever path was seen
// first.
//
// Detection now keys on the git origin, which only helps from now on. Folding
// the rows that already fragmented needs git and the filesystem: to prove
// which path is a worktree of which repository, and to read the origin of rows
// that never stored a remote_key. So it cannot be a SQL migration — it lives
// here, behind an explicit command.

// MergeKind distinguishes the two repairs.
type MergeKind string

const (
	// KindMerge folds a worktree's project into the repository's existing one.
	KindMerge MergeKind = "merge"
	// KindRepath moves a project onto its repository's path when no row exists
	// there yet — the worktree was seen but the main checkout never was.
	KindRepath MergeKind = "repath"
	// KindOrigin folds a checkout into another row of the same git origin: the
	// one holding the most memory.
	KindOrigin MergeKind = "origin"
)

// Merge is one repair: everything under From is reattributed to Into.
type Merge struct {
	Kind MergeKind
	From Project
	// Into is the destination project. For KindRepath it is zero and IntoPath
	// carries the path From will be moved to.
	Into     Project
	IntoPath string
	// Key is the origin's remote_key when the repair was decided by origin. The
	// destination receives it if it has none, so detection finds it by key.
	Key string
	// Rows counts what moves, per table, so a dry run can be read before
	// anything is written.
	Rows map[string]int
}

// Total is how many rows this repair reattributes.
func (m Merge) Total() int {
	n := 0
	for _, c := range m.Rows {
		n += c
	}
	return n
}

// Report is the plan. Nothing in it has been written.
type Report struct {
	Scanned int
	Merges  []Merge
	// Missing are projects whose path is gone from disk and whose stored
	// remote_key matches no other row. Nothing identifies them, so they are
	// reported and left alone rather than guessed at. A vanished checkout whose
	// key does match is folded like any other checkout of that origin.
	Missing []Stranded
}

// Stranded is a project whose directory is gone, with how much memory is
// attributed to it — so the report can say what is sitting there rather than
// just naming a path.
type Stranded struct {
	Project  Project
	Memories int
}

// Affected is how many rows the whole plan would move.
func (r *Report) Affected() int {
	n := 0
	for _, m := range r.Merges {
		n += m.Total()
	}
	return n
}

// projectIDTables are every table that attributes a row to a project. Derived
// at runtime so a new table cannot be silently left behind by this command.
func projectIDTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		tables = append(tables, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var withProject []string
	for _, t := range tables {
		has, _, err := projectIDColumn(db, t)
		if err != nil {
			return nil, err
		}
		if has {
			withProject = append(withProject, t)
		}
	}
	sort.Strings(withProject)
	return withProject, nil
}

// projectIDColumn reports whether the table has a project_id column and whether
// that column is part of the primary key. A table keyed by project_id holds at
// most one row per project, so merging two of them is a collision rather than a
// move: the destination's row is the one that survives.
func projectIDColumn(db *sql.DB, table string) (has bool, isPK bool, err error) {
	rows, err := db.Query(`SELECT name, pk FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var pk int
		if err := rows.Scan(&name, &pk); err != nil {
			return false, false, err
		}
		if name == "project_id" {
			return true, pk > 0, nil
		}
	}
	return false, false, rows.Err()
}

// PlanConsolidate works out which project rows are worktrees of another and
// what folding them would move. It only reads.
func (d *Detector) PlanConsolidate() (*Report, error) {
	rows, err := d.db.Query(`SELECT id, name, path, COALESCE(remote_key, '') FROM projects`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var all []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Path, &p.RemoteKey); err != nil {
			return nil, err
		}
		all = append(all, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	byPath := make(map[string]Project, len(all))
	for _, p := range all {
		byPath[filepath.Clean(p.Path)] = p
	}

	tables, err := projectIDTables(d.db)
	if err != nil {
		return nil, fmt.Errorf("inspect schema: %w", err)
	}

	report := &Report{Scanned: len(all)}
	memories := make(map[string]int, len(all))
	gone := make(map[string]bool)
	for _, p := range all {
		path := filepath.Clean(p.Path)

		var n int
		if err := d.db.QueryRow(
			`SELECT COUNT(*) FROM memories WHERE project_id = ?`, p.ID,
		).Scan(&n); err != nil {
			return nil, fmt.Errorf("count memories: %w", err)
		}
		memories[p.ID] = n

		if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
			gone[p.ID] = true
			continue
		}

		// linkedWorktreeRoot returns "" for anything that is not a linked
		// worktree, so a main checkout, a submodule and a bare repository are
		// all left alone.
		root := linkedWorktreeRoot(path)
		if root == "" || filepath.Clean(root) == path {
			continue
		}
		root = filepath.Clean(root)

		m := Merge{From: p}
		if dest, ok := byPath[root]; ok {
			if dest.ID == p.ID {
				continue
			}
			m.Kind = KindMerge
			m.Into = dest
			m.IntoPath = dest.Path
		} else {
			m.Kind = KindRepath
			m.IntoPath = root
		}

		if m.Kind == KindMerge {
			if m.Rows, err = d.countProjectRows(tables, p.ID); err != nil {
				return nil, err
			}
		}
		report.Merges = append(report.Merges, m)
	}

	folded, err := d.planOriginMerges(report, all, tables, memories, gone)
	if err != nil {
		return nil, err
	}
	for _, p := range all {
		if gone[p.ID] && !folded[p.ID] {
			report.Missing = append(report.Missing, Stranded{Project: p, Memories: memories[p.ID]})
		}
	}

	sort.Slice(report.Merges, func(i, j int) bool {
		return report.Merges[i].From.Path < report.Merges[j].From.Path
	})
	return report, nil
}

func (d *Detector) countProjectRows(tables []string, projectID string) (map[string]int, error) {
	rows := make(map[string]int, len(tables))
	for _, t := range tables {
		var n int
		q := fmt.Sprintf(`SELECT COUNT(*) FROM %q WHERE project_id = ?`, t)
		if err := d.db.QueryRow(q, projectID).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		if n > 0 {
			rows[t] = n
		}
	}
	return rows, nil
}

// planOriginMerges groups the rows by git origin and folds each group into the
// row holding the most memory. It returns the IDs it scheduled for folding.
//
// A row on disk is keyed by the origin git reports for it now, whatever it
// stored; a row whose directory is gone can only offer its stored remote_key.
// A repository with no origin has no identity beyond its path and is left out.
func (d *Detector) planOriginMerges(report *Report, all []Project, tables []string, memories map[string]int, gone map[string]bool) (map[string]bool, error) {
	// canonicalOf maps every key spelling seen on a live repository (the legacy
	// normalization, a stale stored key) to its canonical key, so a vanished
	// row stored under an older spelling still joins its repository.
	canonicalOf := make(map[string]string)
	keyOf := make(map[string]string, len(all))
	for _, p := range all {
		if gone[p.ID] {
			continue
		}
		canonical, legacy := RemoteKeysFromDir(filepath.Clean(p.Path))
		if canonical == "" {
			continue
		}
		keyOf[p.ID] = canonical
		for _, k := range []string{canonical, legacy, p.RemoteKey} {
			if k != "" {
				canonicalOf[k] = canonical
			}
		}
	}
	for _, p := range all {
		if !gone[p.ID] || p.RemoteKey == "" {
			continue
		}
		if c, ok := canonicalOf[p.RemoteKey]; ok {
			keyOf[p.ID] = c
		} else {
			keyOf[p.ID] = p.RemoteKey
		}
	}

	// A worktree already being folded cannot be a destination; its merge is
	// retargeted below so nothing ever folds into a row that is going away.
	worktreeMerge := make(map[string]int)
	for i, m := range report.Merges {
		if m.Kind == KindMerge {
			worktreeMerge[m.From.ID] = i
		}
	}

	groups := make(map[string][]Project)
	for _, p := range all {
		if k := keyOf[p.ID]; k != "" {
			groups[k] = append(groups[k], p)
		}
	}

	folded := make(map[string]bool)
	for key, group := range groups {
		if len(group) < 2 {
			continue
		}
		var candidates []Project
		for _, p := range group {
			if _, isWorktree := worktreeMerge[p.ID]; !isWorktree {
				candidates = append(candidates, p)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		sort.Slice(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			if memories[a.ID] != memories[b.ID] {
				return memories[a.ID] > memories[b.ID]
			}
			if gone[a.ID] != gone[b.ID] {
				return !gone[a.ID]
			}
			return a.ID < b.ID
		})
		dest := candidates[0]

		for _, p := range group {
			if p.ID == dest.ID {
				continue
			}
			if i, isWorktree := worktreeMerge[p.ID]; isWorktree {
				report.Merges[i].Into = dest
				report.Merges[i].IntoPath = dest.Path
				report.Merges[i].Key = key
				continue
			}
			rows, err := d.countProjectRows(tables, p.ID)
			if err != nil {
				return nil, err
			}
			report.Merges = append(report.Merges, Merge{
				Kind: KindOrigin, From: p, Into: dest, IntoPath: dest.Path, Key: key, Rows: rows,
			})
			folded[p.ID] = true
		}
	}
	return folded, nil
}

// ApplyConsolidate writes the plan. Everything happens in one transaction: the
// database either ends up fully consolidated or untouched.
func (d *Detector) ApplyConsolidate(r *Report) error {
	if r == nil || len(r.Merges) == 0 {
		return nil
	}

	tables, err := projectIDTables(d.db)
	if err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}
	pkTable := make(map[string]bool, len(tables))
	for _, t := range tables {
		_, isPK, err := projectIDColumn(d.db, t)
		if err != nil {
			return err
		}
		pkTable[t] = isPK
	}

	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Rows go away first: a repath may move a project onto a path that a row
	// being folded still holds.
	for _, m := range r.Merges {
		if m.Kind == KindRepath {
			continue
		}

		for _, t := range tables {
			if pkTable[t] {
				// One row per project by construction. The destination already
				// has its own, and it is the repository's — the worktree's copy
				// is derived state that would only collide.
				if _, err := tx.Exec(
					fmt.Sprintf(`UPDATE OR IGNORE %q SET project_id = ? WHERE project_id = ?`, t),
					m.Into.ID, m.From.ID,
				); err != nil {
					return fmt.Errorf("move %s: %w", t, err)
				}
				if _, err := tx.Exec(
					fmt.Sprintf(`DELETE FROM %q WHERE project_id = ?`, t), m.From.ID,
				); err != nil {
					return fmt.Errorf("prune %s: %w", t, err)
				}
				continue
			}
			if _, err := tx.Exec(
				fmt.Sprintf(`UPDATE %q SET project_id = ? WHERE project_id = ?`, t),
				m.Into.ID, m.From.ID,
			); err != nil {
				return fmt.Errorf("move %s: %w", t, err)
			}
		}

		if _, err := tx.Exec(`DELETE FROM projects WHERE id = ?`, m.From.ID); err != nil {
			return fmt.Errorf("drop project %s: %w", m.From.Path, err)
		}
		if m.Key != "" {
			if _, err := tx.Exec(
				`UPDATE projects SET remote_key = ? WHERE id = ? AND COALESCE(remote_key, '') = ''`,
				m.Key, m.Into.ID,
			); err != nil {
				return fmt.Errorf("key project %s: %w", m.Into.Path, err)
			}
		}
	}

	for _, m := range r.Merges {
		if m.Kind != KindRepath {
			continue
		}
		if _, err := tx.Exec(`UPDATE projects SET path = ? WHERE id = ?`, m.IntoPath, m.From.ID); err != nil {
			return fmt.Errorf("repath %s: %w", m.From.Path, err)
		}
	}

	return tx.Commit()
}
