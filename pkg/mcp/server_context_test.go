package mcp

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jholhewres/anchored/pkg/kg"
	"github.com/jholhewres/anchored/pkg/memory"
)

func TestRenderContextBundle_Empty(t *testing.T) {
	out := renderContextBundle("", "", "", "", 0, nil, nil, nil, nil, anchoredContextBudget)
	if !strings.Contains(out, "<anchored_context ") || !strings.Contains(out, "</anchored_context>") {
		t.Fatalf("missing wrapper tags: %q", out)
	}
	if !strings.Contains(out, "not instructions") {
		t.Fatalf("missing data-not-instructions fencing note: %q", out)
	}
	if strings.Contains(out, "<identity>") || strings.Contains(out, "<project ") || strings.Contains(out, "<recent>") || strings.Contains(out, "<events>") {
		t.Fatalf("expected no inner sections, got: %q", out)
	}
}

func TestRenderContextBundle_AllSections(t *testing.T) {
	mems := []memory.Memory{
		{Category: "decision", Content: "Settled on RRF for hybrid search", CreatedAt: time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)},
		{Category: "learning", Content: "got bit by tokenizer post_processor", CreatedAt: time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC)},
	}
	events := []ctxRecentEvent{
		{EventType: "precompact_snapshot", Summary: "compaction at 50%"},
	}
	byCat := map[string]int{"decision": 8, "learning": 4, "fact": 12}

	out := renderContextBundle(
		"# Identity\nGo dev",
		"anchored", "/home/x/anchored", "proj-1", 24,
		byCat, mems, events, nil, anchoredContextBudget,
	)

	for _, want := range []string{
		"<identity>",
		"Go dev",
		`<project id="proj-1" name="anchored" path="/home/x/anchored" memories="24">`,
		`<by_category scope="project">decision=8 fact=12 learning=4</by_category>`,
		"<recent>",
		"[decision] 2026-05-08 — Settled on RRF",
		"[learning] 2026-05-06 — got bit by",
		"<events>",
		"[precompact_snapshot] compaction at 50%",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n--- output ---\n%s", want, out)
		}
	}
}

func TestRenderContextBundle_TruncatesOversizeIdentity(t *testing.T) {
	// Tight budget keeps test fast and exercises truncation logic. Identity
	// is intentionally smaller than identityCap (600) so that path is the
	// budget enforcer, not the per-section cap.
	huge := strings.Repeat("x", 500)
	out := renderContextBundle(huge, "", "", "", 0, nil, nil, nil, nil, 200)
	if len(out) > 200 {
		t.Fatalf("budget breach: %d > 200", len(out))
	}
	if !strings.HasSuffix(out, "</anchored_context>") {
		t.Fatalf("missing closing tag after truncation:\n%s", out)
	}
}

// TestRenderContextBundle_HardCapsIdentityWithoutNewlines exercises the
// "single giant line" path: a multi-KB identity that has no \n so line-based
// truncation alone can't bring it inside budget. The byte-trim fallback must
// kick in and the result must be ≤ budget.
func TestRenderContextBundle_HardCapsIdentityWithoutNewlines(t *testing.T) {
	one := strings.Repeat("x", 5000)
	out := renderContextBundle(one, "", "", "", 0, nil, nil, nil, nil, 300)
	if len(out) > 300 {
		t.Fatalf("budget breach: %d > 300\n%s", len(out), out)
	}
	if !strings.HasSuffix(out, "</anchored_context>") {
		t.Fatalf("missing closing tag: %q", out)
	}
}

// TestRenderContextBundle_BudgetIsHard guarantees the budget contract for
// realistic inputs: large identity + project + many recent items.
func TestRenderContextBundle_BudgetIsHard(t *testing.T) {
	mems := make([]memory.Memory, 10)
	for i := range mems {
		mems[i] = memory.Memory{
			Category:  "decision",
			Content:   strings.Repeat("loooong text ", 30),
			CreatedAt: time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC),
		}
	}
	out := renderContextBundle(
		strings.Repeat("ipsum ", 200), "anchored", "/p", "id-1", 99,
		map[string]int{"decision": 5, "fact": 3}, mems, nil, nil, 1024,
	)
	if len(out) > 1024 {
		t.Fatalf("budget breach: %d > 1024", len(out))
	}
}

// TestRenderContextBundle_PreservesUTF8AfterTruncate ensures we never split a
// multibyte rune. Uses combining characters and accented PT-BR.
func TestRenderContextBundle_PreservesUTF8AfterTruncate(t *testing.T) {
	identity := strings.Repeat("ção é ñ ", 500) // multi-byte runes
	out := renderContextBundle(identity, "", "", "", 0, nil, nil, nil, nil, 256)
	if !utf8.ValidString(out) {
		t.Fatalf("output is not valid UTF-8 after truncation:\n%q", out)
	}
	if len(out) > 256 {
		t.Fatalf("budget breach: %d > 256", len(out))
	}
}

// TestRenderContextBundle_EscapesXML guards against unescaped &, <, > leaking
// into attribute values or text nodes — a project path with `&` or memory
// content with `<script>` would otherwise corrupt the bundle.
func TestRenderContextBundle_EscapesXML(t *testing.T) {
	mems := []memory.Memory{
		{Category: "fact", Content: "<script>alert(1)</script>", CreatedAt: time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)},
	}
	out := renderContextBundle(
		"identity & co", "weird & name", `/path/with"quote`, "p1", 1,
		nil, mems, nil, nil, anchoredContextBudget,
	)
	for _, banned := range []string{
		`name="weird & name"`,
		`path="/path/with"quote"`,
		"<script>",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("unescaped fragment leaked into output: %q", banned)
		}
	}
	for _, want := range []string{
		`name="weird &amp; name"`,
		`path="/path/with&quot;quote"`,
		"&lt;script&gt;alert(1)&lt;/script&gt;",
		"identity &amp; co",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected escaped fragment %q in output\n--- output ---\n%s", want, out)
		}
	}
}

func TestTruncateContextBundle_KeepsClosingTagAndDropsWholeLines(t *testing.T) {
	body := "<anchored_context>\n  <identity>\n    line one\n    line two\n    line three\n  </identity>\n</anchored_context>"
	got := truncateContextBundle(body, 60)
	if !strings.HasSuffix(got, "</anchored_context>") {
		t.Fatalf("must end with closing tag: %q", got)
	}
	if !strings.Contains(got, "<truncated/>") {
		t.Fatalf("must mark truncation: %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		// No partial line should leak (we only drop whole lines).
		if strings.Contains(line, "line tw") && line != "    line two" {
			t.Fatalf("partial line leaked: %q", line)
		}
	}
}

func TestRecentBundleCategories_Coverage(t *testing.T) {
	// Sanity: the bundle's "durable knowledge" set is exactly these five.
	// summary/event are excluded by design — see comment on the constant.
	want := map[string]bool{
		"decision": true, "learning": true, "plan": true, "preference": true, "fact": true,
	}
	if len(recentBundleCategories) != len(want) {
		t.Fatalf("len(recentBundleCategories) = %d, want %d", len(recentBundleCategories), len(want))
	}
	for _, c := range recentBundleCategories {
		if !want[c] {
			t.Errorf("unexpected category in bundle set: %q", c)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 4, "hell…"},
		{"ção", 2, "çã…"},
		{"ção é", 5, "ção é"},
		{"", 5, ""},
		{"x", 0, ""},
	}
	for _, tc := range cases {
		got := truncateRunes(tc.in, tc.max)
		if got != tc.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

func TestEscapeAttrAndText(t *testing.T) {
	if got := escapeAttr(`a&b<c>d"e`); got != "a&amp;b&lt;c&gt;d&quot;e" {
		t.Errorf("escapeAttr: %q", got)
	}
	if got := escapeAttr("line\nbreak"); got != "line&#xA;break" {
		t.Errorf("escapeAttr newline: %q", got)
	}
	if got := escapeText(`a&b<c>d"e`); got != `a&amp;b&lt;c&gt;d"e` {
		t.Errorf("escapeText (quote should pass through): %q", got)
	}
}

func TestRankBundleMemories_PrefersFresher(t *testing.T) {
	old := memory.Memory{Content: "old", CreatedAt: time.Now().AddDate(0, 0, -120)}
	fresh := memory.Memory{Content: "fresh", CreatedAt: time.Now()}
	ranked := rankBundleMemories([]memory.Memory{old, fresh}, 5)
	if len(ranked) != 2 || ranked[0].Content != "fresh" {
		t.Fatalf("fresh memory should rank first, got %+v", ranked)
	}
}

func TestRenderContextBundle_Relationships(t *testing.T) {
	rels := []kg.Triple{{Subject: "payments", Predicate: "depends_on", Object: "postgres"}}
	out := renderContextBundle("", "", "", "p1", 0, nil, nil, nil, rels, anchoredContextBudget)
	if !strings.Contains(out, "<relationships>") || !strings.Contains(out, "payments depends_on postgres") {
		t.Fatalf("missing relationships section: %q", out)
	}
}
