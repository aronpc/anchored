// Package eval provides small, deterministic local evaluations that gate
// regressions in the three properties that matter most for anchored: retrieval
// recall, sync privacy safety, and project-identity resolution. The evals run
// offline (BM25-only, no embeddings, no network) so they fit in CI and on a
// dev laptop.
package eval

import (
	"embed"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

//go:embed fixtures/*.yaml
var fixturesFS embed.FS

// DefaultFixture returns the embedded fixture bytes for a base name like
// "recall_basic.yaml", so the installed binary runs evals without the repo
// checked out. Callers may override with a file via --fixture.
func DefaultFixture(name string) ([]byte, error) {
	return fixturesFS.ReadFile("fixtures/" + name)
}

// FixtureBytes resolves fixture data: the file at path when non-empty,
// otherwise the embedded default named by defaultName.
func FixtureBytes(path, defaultName string) ([]byte, error) {
	if path != "" {
		return os.ReadFile(path)
	}
	return DefaultFixture(defaultName)
}

// RecallFixture seeds a corpus and asserts Recall@K for a set of queries.
type RecallFixture struct {
	K           int            `yaml:"k"`
	MinRecall   float64        `yaml:"min_recall"`
	Memories    []RecallMemory `yaml:"memories"`
	Queries     []RecallQuery  `yaml:"queries"`
	Description string         `yaml:"description"`
}

type RecallMemory struct {
	Key      string   `yaml:"key"`
	Category string   `yaml:"category"`
	Content  string   `yaml:"content"`
	Keywords []string `yaml:"keywords"`
}

type RecallQuery struct {
	Query     string   `yaml:"query"`
	Expect    []string `yaml:"expect"`
	MinRecall float64  `yaml:"min_recall"` // overrides fixture MinRecall when > 0
}

// PrivacyFixture lists content that must never reach a remote, each with the
// expectation of being blocked or rewritten by the safety filter + sanitizer.
type PrivacyFixture struct {
	Description string        `yaml:"description"`
	Items       []PrivacyItem `yaml:"items"`
}

type PrivacyItem struct {
	Name     string         `yaml:"name"`
	Content  string         `yaml:"content"`
	Metadata map[string]any `yaml:"metadata"`
	// MustBlock requires the safety filter to block the item outright.
	// MustRedact requires the sanitizer/filter to rewrite the content (secret
	// or local path removed) even if not blocked.
	MustBlock  bool `yaml:"must_block"`
	MustRedact bool `yaml:"must_redact"`
}

// IdentityFixture asserts remote-key derivation invariants from git origins.
type IdentityFixture struct {
	Description string         `yaml:"description"`
	Cases       []IdentityCase `yaml:"cases"`
}

type IdentityCase struct {
	Name   string `yaml:"name"`
	Origin string `yaml:"origin"`
	// ExpectCanonical / ExpectLegacy are the keys the derivation must produce.
	// Empty means "don't assert this field".
	ExpectCanonical string `yaml:"expect_canonical"`
	ExpectLegacy    string `yaml:"expect_legacy"`
	// SameAs names another case whose canonical key this one must equal (e.g.
	// scp-syntax and https forms of the same repo resolve identically).
	SameAs string `yaml:"same_as"`
	// MustDiffer names another case whose canonical key this one must NOT equal.
	MustDiffer string `yaml:"must_differ"`
}

func parseYAML(data []byte, out any) error {
	if err := yaml.Unmarshal(data, out); err != nil {
		return fmt.Errorf("parse fixture: %w", err)
	}
	return nil
}

// Report is the common result of an eval run.
type Report struct {
	Name    string       `json:"name"`
	Passed  bool         `json:"passed"`
	Score   float64      `json:"score"`
	Summary string       `json:"summary"`
	Cases   []CaseResult `json:"cases"`
}

type CaseResult struct {
	Name   string  `json:"name"`
	Passed bool    `json:"passed"`
	Score  float64 `json:"score,omitempty"`
	Detail string  `json:"detail,omitempty"`
}
