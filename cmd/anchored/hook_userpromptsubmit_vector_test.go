package main

import (
	"testing"

	"github.com/jholhewres/anchored/pkg/memory"
)

// expandFromCache uses the top hit's precomputed vector as a seed and returns
// the semantically-near memories that BM25 did NOT already surface, tagged
// "vector". The far memory (below the cosine floor) and the seed itself are
// excluded.
func TestExpandFromCache_PullsSemanticNeighbours(t *testing.T) {
	db := newFTSTestDB(t)
	insertMem(t, db, "m1", "proj-A", "decision", "seed memory about hybrid ranking")
	insertMem(t, db, "m2", "proj-A", "learning", "near memory sharing no literal tokens")
	insertMem(t, db, "m3", "proj-A", "fact", "totally unrelated far memory")

	vc := memory.NewVectorCache(nil)
	vc.Put("m1", []float32{1, 0, 0})     // seed
	vc.Put("m2", []float32{0.9, 0.1, 0}) // cosine ~0.99 vs seed -> near
	vc.Put("m3", []float32{0, 0, 1})     // cosine 0 vs seed -> far, filtered

	hits := []preSearchHit{{ID: "m1", Category: "decision", Content: "seed memory about hybrid ranking"}}
	out := expandFromCache(vc, db, hits, "proj-A")

	if len(out) != 1 || out[0].ID != "m2" {
		t.Fatalf("expandFromCache = %+v, want exactly [m2]", out)
	}
	if !hasSignal(out[0].Signals, "vector") {
		t.Errorf("merged memory must be tagged with the 'vector' signal, got %v", out[0].Signals)
	}
}

// A seed whose ID is not in the cache yields no expansion (fail-soft).
func TestExpandFromCache_NoSeedVector(t *testing.T) {
	db := newFTSTestDB(t)
	vc := memory.NewVectorCache(nil)
	hits := []preSearchHit{{ID: "missing", Category: "fact", Content: "x"}}
	if out := expandFromCache(vc, db, hits, ""); out != nil {
		t.Errorf("expected nil expansion when the seed has no cached vector, got %+v", out)
	}
}

// fetchMemoriesByIDs honors project scope, skips deleted rows, and preserves
// the input (relevance) order.
func TestFetchMemoriesByIDs_ScopeAndOrder(t *testing.T) {
	db := newFTSTestDB(t)
	insertMem(t, db, "a", "proj-A", "decision", "alpha")
	insertMem(t, db, "b", "proj-B", "fact", "bravo")
	insertMem(t, db, "c", "proj-A", "event", "charlie")

	// Project-scoped: proj-B row is excluded.
	got := fetchMemoriesByIDs(db, []string{"c", "a", "b"}, "proj-A")
	if len(got) != 2 || got[0].ID != "c" || got[1].ID != "a" {
		t.Fatalf("scoped fetch = %+v, want [c, a] in order", got)
	}

	// Global: empty projectID returns all requested, in input order.
	all := fetchMemoriesByIDs(db, []string{"b", "a"}, "")
	if len(all) != 2 || all[0].ID != "b" || all[1].ID != "a" {
		t.Fatalf("global fetch = %+v, want [b, a] in order", all)
	}
}

func hasSignal(sigs []string, want string) bool {
	for _, s := range sigs {
		if s == want {
			return true
		}
	}
	return false
}
