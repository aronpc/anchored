package kg

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// kgSchema creates the minimal KG tables needed for testing.
// We cannot import pkg/memory because memory -> kg creates an import cycle.
const kgSchema = `
CREATE TABLE IF NOT EXISTS kg_entities (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	project_id TEXT,
	embedding BLOB,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS kg_entity_aliases (
	entity_id TEXT NOT NULL REFERENCES kg_entities(id) ON DELETE CASCADE,
	alias TEXT NOT NULL,
	PRIMARY KEY (entity_id, alias)
);
CREATE TABLE IF NOT EXISTS kg_predicates (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	is_functional BOOLEAN DEFAULT FALSE,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS kg_triples (
	id TEXT PRIMARY KEY,
	subject_id TEXT NOT NULL REFERENCES kg_entities(id),
	predicate_id TEXT NOT NULL REFERENCES kg_predicates(id),
	object_id TEXT NOT NULL REFERENCES kg_entities(id),
	confidence REAL DEFAULT 1.0,
	project_id TEXT,
	valid_from DATETIME DEFAULT CURRENT_TIMESTAMP,
	valid_to DATETIME,
	txn_time DATETIME DEFAULT CURRENT_TIMESTAMP,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`

func setupKGTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if _, err := db.Exec(kgSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}

	return db
}

func TestNew_CreatesInstance(t *testing.T) {
	db := setupKGTestDB(t)
	k := New(db, nil)
	if k == nil {
		t.Fatal("New() returned nil")
	}
}

func TestAddTriple_StoresTriple(t *testing.T) {
	db := setupKGTestDB(t)
	k := New(db, nil)
	ctx := context.Background()

	tr, err := k.AddTriple(ctx, "Go", "is-a", "Programming Language", nil)
	if err != nil {
		t.Fatalf("AddTriple: %v", err)
	}
	if tr == nil {
		t.Fatal("AddTriple returned nil triple")
	}
	if tr.ID == "" {
		t.Error("triple ID is empty")
	}
	if tr.Subject != "Go" {
		t.Errorf("Subject = %q, want %q", tr.Subject, "Go")
	}
	if tr.Predicate != "is-a" {
		t.Errorf("Predicate = %q, want %q", tr.Predicate, "is-a")
	}
	if tr.Object != "Programming Language" {
		t.Errorf("Object = %q, want %q", tr.Object, "Programming Language")
	}
}

func TestQuery_ReturnsTriplesForEntity(t *testing.T) {
	db := setupKGTestDB(t)
	k := New(db, nil)
	ctx := context.Background()

	_, err := k.AddTriple(ctx, "Go", "is-a", "Programming Language", nil)
	if err != nil {
		t.Fatalf("AddTriple: %v", err)
	}

	triples, err := k.Query(ctx, "Go", nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(triples) != 1 {
		t.Fatalf("Query(Go) returned %d triples, want 1", len(triples))
	}
	if triples[0].Subject != "Go" {
		t.Errorf("Subject = %q, want %q", triples[0].Subject, "Go")
	}
}

func TestQuery_EmptyEntity_ReturnsNil(t *testing.T) {
	db := setupKGTestDB(t)
	k := New(db, nil)
	ctx := context.Background()

	triples, err := k.Query(ctx, "", nil)
	if err != nil {
		t.Fatalf("Query(''): %v", err)
	}
	if triples != nil {
		t.Fatalf("Query('') = %v, want nil", triples)
	}
}

func TestQuery_WhitespaceEntity_ReturnsNil(t *testing.T) {
	db := setupKGTestDB(t)
	k := New(db, nil)
	ctx := context.Background()

	triples, err := k.Query(ctx, "   ", nil)
	if err != nil {
		t.Fatalf("Query('   '): %v", err)
	}
	if triples != nil {
		t.Fatalf("Query('   ') = %v, want nil", triples)
	}
}

func TestQuery_AliasResolution(t *testing.T) {
	db := setupKGTestDB(t)
	k := New(db, nil)
	ctx := context.Background()

	_, err := k.AddTriple(ctx, "Go", "is-a", "Programming Language", nil)
	if err != nil {
		t.Fatalf("AddTriple: %v", err)
	}

	// The ensureEntity function automatically creates a lowercase alias.
	// Querying with the lowercase form should still match.
	triples, err := k.Query(ctx, "go", nil)
	if err != nil {
		t.Fatalf("Query(alias): %v", err)
	}
	if len(triples) != 1 {
		t.Fatalf("Query(lowercase alias 'go') returned %d triples, want 1", len(triples))
	}
}

func TestBitemporal_FunctionalPredicate(t *testing.T) {
	db := setupKGTestDB(t)
	k := New(db, nil)
	ctx := context.Background()

	// Add first triple to create the predicate, then mark it as functional.
	_, err := k.AddTriple(ctx, "Go", "version", "1.21", nil)
	if err != nil {
		t.Fatalf("first AddTriple: %v", err)
	}

	// Mark "version" as functional so old values get superseded.
	_, err = db.Exec("UPDATE kg_predicates SET is_functional = true WHERE name = 'version'")
	if err != nil {
		t.Fatalf("set functional: %v", err)
	}

	// Add a newer value for the same subject+predicate.
	_, err = k.AddTriple(ctx, "Go", "version", "1.22", nil)
	if err != nil {
		t.Fatalf("second AddTriple: %v", err)
	}

	// Query should return only the latest (1.22) as active.
	triples, err := k.Query(ctx, "Go", nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	// Filter to only the version triples.
	var activeVersions []string
	for _, tr := range triples {
		if tr.Predicate == "version" {
			activeVersions = append(activeVersions, tr.Object)
		}
	}

	if len(activeVersions) != 1 {
		t.Fatalf("expected 1 active version triple, got %d: %v", len(activeVersions), activeVersions)
	}
	if activeVersions[0] != "1.22" {
		t.Errorf("active version = %q, want %q", activeVersions[0], "1.22")
	}

	// Verify old triple has valid_to set.
	var oldValidTo sql.NullString
	err = db.QueryRow(`
		SELECT t.valid_to
		FROM kg_triples t
		JOIN kg_entities s ON t.subject_id = s.id
		JOIN kg_predicates p ON t.predicate_id = p.id
		JOIN kg_entities o ON t.object_id = o.id
		WHERE s.name = 'Go' AND p.name = 'version' AND o.name = '1.21'
	`).Scan(&oldValidTo)
	if err != nil {
		t.Fatalf("query old triple: %v", err)
	}
	if !oldValidTo.Valid {
		t.Error("old triple should have valid_to set")
	}
}

func TestQuery_ByObject(t *testing.T) {
	db := setupKGTestDB(t)
	k := New(db, nil)
	ctx := context.Background()

	_, err := k.AddTriple(ctx, "Go", "is-a", "Programming Language", nil)
	if err != nil {
		t.Fatalf("AddTriple: %v", err)
	}

	// Query by object name should also return matching triples.
	triples, err := k.Query(ctx, "Programming Language", nil)
	if err != nil {
		t.Fatalf("Query(object): %v", err)
	}
	if len(triples) != 1 {
		t.Fatalf("Query by object returned %d triples, want 1", len(triples))
	}
}

func TestAddTriple_WithProjectID(t *testing.T) {
	db := setupKGTestDB(t)
	k := New(db, nil)
	ctx := context.Background()

	projectID := "proj-test-1"
	tr, err := k.AddTriple(ctx, "React", "is-a", "Framework", &projectID)
	if err != nil {
		t.Fatalf("AddTriple with projectID: %v", err)
	}
	if tr == nil {
		t.Fatal("AddTriple returned nil triple")
	}

	// Query with matching projectID should return the triple.
	triples, err := k.Query(ctx, "React", &projectID)
	if err != nil {
		t.Fatalf("Query with projectID: %v", err)
	}
	if len(triples) != 1 {
		t.Fatalf("Query(project-filtered) returned %d triples, want 1", len(triples))
	}
	if triples[0].ProjectID == nil || *triples[0].ProjectID != projectID {
		t.Errorf("queried triple ProjectID = %v, want %q", triples[0].ProjectID, projectID)
	}

	// Query with different projectID should return no results.
	otherProject := "proj-other"
	triples, err = k.Query(ctx, "React", &otherProject)
	if err != nil {
		t.Fatalf("Query with other projectID: %v", err)
	}
	if len(triples) != 0 {
		t.Fatalf("Query(wrong project) returned %d triples, want 0", len(triples))
	}
}
