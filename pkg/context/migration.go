package ctx

// MigrationSQL014 adds the artifact store table and wires artifact_id into content_chunks.
const MigrationSQL014 = `
CREATE TABLE IF NOT EXISTS artifacts (
    id              TEXT PRIMARY KEY,
    project_id      TEXT NOT NULL DEFAULT '',
    session_id      TEXT NOT NULL DEFAULT '',
    type            TEXT NOT NULL,
    source_tool     TEXT NOT NULL DEFAULT '',
    source_label    TEXT NOT NULL DEFAULT '',
    content_hash    TEXT NOT NULL DEFAULT '',
    size_bytes      INTEGER NOT NULL DEFAULT 0,
    ttl_expires_at  DATETIME,
    created_at      DATETIME NOT NULL,
    metadata        TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX IF NOT EXISTS idx_artifacts_project ON artifacts(project_id, type, created_at);
CREATE INDEX IF NOT EXISTS idx_artifacts_session ON artifacts(session_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_artifacts_hash ON artifacts(content_hash) WHERE content_hash != '';

ALTER TABLE content_chunks ADD COLUMN artifact_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_chunks_artifact ON content_chunks(artifact_id);
`

// Migration 009: project_id column for per-project isolation on existing databases.
const MigrationSQL009 = `
ALTER TABLE content_chunks ADD COLUMN project_id TEXT NOT NULL DEFAULT '';
ALTER TABLE session_events ADD COLUMN project_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_chunks_project ON content_chunks(project_id);
CREATE INDEX IF NOT EXISTS idx_events_project ON session_events(project_id);
`

const MigrationSQL = `
-- Ephemeral content chunks for context optimizer
CREATE TABLE IF NOT EXISTS content_chunks (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL,
    metadata TEXT NOT NULL DEFAULT '',
    content_type TEXT NOT NULL DEFAULT '',
    indexed_at DATETIME NOT NULL DEFAULT (datetime('now')),
    ttl_hours INTEGER NOT NULL DEFAULT 336
);

CREATE INDEX IF NOT EXISTS idx_chunks_session ON content_chunks(session_id);
CREATE INDEX IF NOT EXISTS idx_chunks_source ON content_chunks(source);
CREATE INDEX IF NOT EXISTS idx_chunks_indexed ON content_chunks(indexed_at);

-- FTS5 trigram index for exact/partial matching
CREATE VIRTUAL TABLE IF NOT EXISTS content_chunks_fts USING fts5(
    content,
    content='content_chunks',
    content_rowid='rowid',
    tokenize='trigram'
);

-- Keep FTS5 in sync with content_chunks
CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON content_chunks BEGIN
    INSERT INTO content_chunks_fts(rowid, content) VALUES (new.rowid, new.content);
END;

CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON content_chunks BEGIN
    INSERT INTO content_chunks_fts(content_chunks_fts, rowid, content) VALUES('delete', old.rowid, old.content);
END;

CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON content_chunks BEGIN
    INSERT INTO content_chunks_fts(content_chunks_fts, rowid, content) VALUES('delete', old.rowid, old.content);
    INSERT INTO content_chunks_fts(rowid, content) VALUES (new.rowid, new.content);
END;

-- Session events for continuity
CREATE TABLE IF NOT EXISTS session_events (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 3,
    tool_name TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    metadata TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_events_session ON session_events(session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_events_type ON session_events(event_type);

-- Vocabulary for fuzzy correction
CREATE TABLE IF NOT EXISTS content_vocabulary (
    word TEXT PRIMARY KEY
);
`
