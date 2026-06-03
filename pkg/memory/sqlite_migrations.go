package memory

import (
	"database/sql"
	"fmt"

	ctxpkg "github.com/jholhewres/anchored/pkg/context"
)

type migration struct {
	Name string
	Up   string
}

func Migrate(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS migrations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	migrations := []migration{
		{Name: "001_initial_schema", Up: initSchema()},
		{Name: "002_indexed_files", Up: `CREATE TABLE IF NOT EXISTS indexed_files (
			path TEXT PRIMARY KEY,
			sha256 TEXT NOT NULL,
			indexed_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`},
		{Name: "003_content_hash_soft_delete", Up: `
			ALTER TABLE memories ADD COLUMN content_hash TEXT;
			ALTER TABLE memories ADD COLUMN deleted_at DATETIME;
			UPDATE memories SET content_hash = '' WHERE content_hash IS NULL;
			CREATE INDEX IF NOT EXISTS idx_memories_content_hash ON memories(content_hash, project_id);
			CREATE INDEX IF NOT EXISTS idx_memories_deleted_at ON memories(deleted_at);
		`},
		{Name: "004_cross_project_search", Up: `
			CREATE INDEX IF NOT EXISTS idx_memories_global_search ON memories(category, deleted_at);
			CREATE INDEX IF NOT EXISTS idx_memories_source ON memories(source);
		`},
		{Name: "005_live_sessions", Up: `
			ALTER TABLE sessions ADD COLUMN last_activity_at DATETIME;
			ALTER TABLE sessions ADD COLUMN ended_at DATETIME;
			ALTER TABLE sessions ADD COLUMN source_tool TEXT;
			ALTER TABLE sessions ADD COLUMN metadata TEXT;
			CREATE INDEX IF NOT EXISTS idx_sessions_source_session_id ON sessions(source_session_id);
			CREATE INDEX IF NOT EXISTS idx_sessions_last_activity ON sessions(last_activity_at);
		`},
		{Name: "006_auto_capture", Up: `
			CREATE INDEX IF NOT EXISTS idx_memories_source_type ON memories(source);
		`},
		{Name: "007_dream_system", Up: `
			CREATE TABLE IF NOT EXISTS dream_runs (
				id TEXT PRIMARY KEY,
				started_at DATETIME,
				finished_at DATETIME,
				config TEXT,
				memories_analyzed INTEGER DEFAULT 0,
				actions_proposed INTEGER DEFAULT 0,
				actions_applied INTEGER DEFAULT 0,
				status TEXT DEFAULT 'pending'
			);
			CREATE TABLE IF NOT EXISTS dream_actions (
				id TEXT PRIMARY KEY,
				run_id TEXT REFERENCES dream_runs(id),
				memory_id TEXT,
				related_memory_id TEXT,
				action_type TEXT,
				confidence REAL,
				reason TEXT,
				proposed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
				applied_at DATETIME,
				status TEXT DEFAULT 'proposed'
			);
		`},
		{Name: "008_content_optimizer", Up: ctxpkg.MigrationSQL},
		{Name: "009_content_project_id", Up: ctxpkg.MigrationSQL009},
		{Name: "010_project_remote_key", Up: `
			ALTER TABLE projects ADD COLUMN remote_key TEXT;
			CREATE INDEX IF NOT EXISTS idx_projects_remote_key ON projects(remote_key);
		`},
		{Name: "011_sync_metadata", Up: `
			ALTER TABLE memories ADD COLUMN sync_dirty BOOLEAN DEFAULT FALSE;
			ALTER TABLE memories ADD COLUMN sync_origin TEXT DEFAULT 'local';
			ALTER TABLE memories ADD COLUMN author TEXT;
			ALTER TABLE memories ADD COLUMN remote_project_key TEXT;
			CREATE TABLE IF NOT EXISTS sync_state (
				project_id TEXT PRIMARY KEY,
				remote_project_key TEXT,
				watermark TEXT,
				last_sync DATETIME,
				client_id TEXT NOT NULL
			);
		`},
		{Name: "012_curation_state", Up: `
			CREATE TABLE IF NOT EXISTS curation_state (
				key TEXT PRIMARY KEY,
				value TEXT NOT NULL,
				updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
			);
		`},
		// Rebuild the FTS index with a multilingual tokenizer. The old
		// 'porter unicode61' applied English-only stemming to a PT/ES/EN corpus,
		// degrading lexical recall for non-English memories. Drop + recreate +
		// repopulate; the existing INSERT/UPDATE/DELETE triggers reference the
		// table by name and keep working after the rebuild.
		{Name: "013_fts_multilingual_tokenizer", Up: `
			DROP TABLE IF EXISTS memories_fts;
			CREATE VIRTUAL TABLE memories_fts USING fts5(
				content,
				keywords,
				content='memories',
				content_rowid='rowid',
				tokenize='unicode61 remove_diacritics 2'
			);
			INSERT INTO memories_fts(memories_fts) VALUES('rebuild');
		`},
	}

	for _, m := range migrations {
		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM migrations WHERE name = ?", m.Name).Scan(&count)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", m.Name, err)
		}
		if count > 0 {
			continue
		}

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin tx for migration %s: %w", m.Name, err)
		}

		if _, err := tx.Exec(m.Up); err != nil {
			tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", m.Name, err)
		}

		if _, err := tx.Exec("INSERT INTO migrations (name) VALUES (?)", m.Name); err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", m.Name, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", m.Name, err)
		}
	}

	// One-time Go-side backfill: contentHash() now normalizes (lowercase +
	// whitespace) before hashing, so every row stored before this upgrade has a
	// stale hash and would dodge exact-dedup on the next save. Recompute them
	// all once. Kept out of the SQL migration list because SQLite has no
	// SHA-256; guarded by a marker row so it runs exactly once.
	if err := backfillNormalizedContentHashes(db); err != nil {
		return fmt.Errorf("backfill normalized content hashes: %w", err)
	}

	return nil
}

const normalizedHashMarker = "go_014_normalize_content_hash"

func backfillNormalizedContentHashes(db *sql.DB) error {
	var done int
	if err := db.QueryRow("SELECT COUNT(*) FROM migrations WHERE name = ?", normalizedHashMarker).Scan(&done); err != nil {
		return fmt.Errorf("check marker: %w", err)
	}
	if done > 0 {
		return nil
	}

	rows, err := db.Query("SELECT id, content FROM memories")
	if err != nil {
		return fmt.Errorf("scan memories: %w", err)
	}
	type row struct{ id, content string }
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.content); err != nil {
			rows.Close()
			return fmt.Errorf("scan row: %w", err)
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, r := range all {
		if _, err := tx.Exec("UPDATE memories SET content_hash = ? WHERE id = ?", contentHash(r.content), r.id); err != nil {
			tx.Rollback()
			return fmt.Errorf("rehash %s: %w", r.id, err)
		}
	}
	if _, err := tx.Exec("INSERT INTO migrations (name) VALUES (?)", normalizedHashMarker); err != nil {
		tx.Rollback()
		return fmt.Errorf("record marker: %w", err)
	}
	return tx.Commit()
}
