// Package intent classifies a user prompt into a coarse development intent so
// the auto-recall hook can pick a relevant retrieval plan (debugging pulls
// error context, planning pulls decisions, etc). It is deliberately cheap:
// lowercased keyword/substring matching over a bilingual (PT+EN) signal table,
// no LLM and no network, so it can run inline in a pre-prompt hook.
package intent

import "strings"

// Kind is the detected intent category.
type Kind string

const (
	KindPlanning      Kind = "planning"
	KindArchitecture  Kind = "architecture_decision"
	KindCodeChange    Kind = "code_change"
	KindDebugging     Kind = "debugging"
	KindTestExecution Kind = "test_execution"
	KindRelease       Kind = "release"
	KindSecurity      Kind = "security_sensitive"
	KindMemoryOp      Kind = "memory_operation"
	KindUnknown       Kind = "unknown"
)

// Intent is the classification result. Confidence is a rough 0..1 score based
// on how many distinct signals matched (saturating), useful for gating noisy
// injection on weak matches. Signals lists the matched cues for debuggability.
type Intent struct {
	Kind       Kind
	Confidence float64
	Signals    []string
}

// signal is one matchable cue mapped to a kind. match is a lowercase substring;
// matching is word-ish (we surround the haystack with spaces and require the
// cue to appear, which is enough for keyword cues without a full tokenizer).
type signal struct {
	kind Kind
	cue  string
}

// signals is the bilingual cue table. Order does not matter; scoring counts
// distinct matched kinds. Keep cues lowercase. Prefer specific multi-word cues
// over single ambiguous words to reduce false positives.
var signals = []signal{
	// debugging
	{KindDebugging, "error"}, {KindDebugging, "erro"}, {KindDebugging, "bug"},
	{KindDebugging, "panic"}, {KindDebugging, "stack trace"}, {KindDebugging, "stacktrace"},
	{KindDebugging, "traceback"}, {KindDebugging, "exception"}, {KindDebugging, "crash"},
	{KindDebugging, "falha"}, {KindDebugging, "quebrou"}, {KindDebugging, "not working"},
	{KindDebugging, "não funciona"}, {KindDebugging, "nao funciona"}, {KindDebugging, "debug"},
	{KindDebugging, "why does"}, {KindDebugging, "por que"}, {KindDebugging, "porque"},
	{KindDebugging, "fix"}, {KindDebugging, "corrig"}, {KindDebugging, "investigate"},
	// test execution
	{KindTestExecution, "run the test"}, {KindTestExecution, "run tests"},
	{KindTestExecution, "go test"}, {KindTestExecution, "npm test"}, {KindTestExecution, "pytest"},
	{KindTestExecution, "unit test"}, {KindTestExecution, "rodar os testes"},
	{KindTestExecution, "rodar teste"}, {KindTestExecution, "testar"}, {KindTestExecution, "test suite"},
	{KindTestExecution, "make test"}, {KindTestExecution, "test coverage"},
	// release
	{KindRelease, "release"}, {KindRelease, "deploy"}, {KindRelease, "publish"},
	{KindRelease, "tag a version"}, {KindRelease, "cut a release"}, {KindRelease, "ship it"},
	{KindRelease, "lançar"}, {KindRelease, "lancar"}, {KindRelease, "publicar"},
	{KindRelease, "versão"}, {KindRelease, "bump version"}, {KindRelease, "changelog"},
	// security
	{KindSecurity, "security"}, {KindSecurity, "vulnerab"}, {KindSecurity, "exploit"},
	{KindSecurity, "auth"}, {KindSecurity, "credential"}, {KindSecurity, "secret"},
	{KindSecurity, "segurança"}, {KindSecurity, "seguranca"}, {KindSecurity, "senha"},
	{KindSecurity, "token"}, {KindSecurity, "permission"}, {KindSecurity, "csrf"},
	{KindSecurity, "injection"}, {KindSecurity, "sql injection"}, {KindSecurity, "xss"},
	// planning
	{KindPlanning, "plan"}, {KindPlanning, "planej"}, {KindPlanning, "roadmap"},
	{KindPlanning, "break down"}, {KindPlanning, "break this down"}, {KindPlanning, "break this into"}, {KindPlanning, "milestone"},
	{KindPlanning, "next steps"}, {KindPlanning, "próximos passos"}, {KindPlanning, "proximos passos"},
	{KindPlanning, "strategy"}, {KindPlanning, "estratégia"}, {KindPlanning, "estrategia"},
	{KindPlanning, "how should we"}, {KindPlanning, "como devemos"},
	// architecture decision
	{KindArchitecture, "architecture"}, {KindArchitecture, "arquitetura"},
	{KindArchitecture, "design the"}, {KindArchitecture, "should we use"},
	{KindArchitecture, "trade-off"}, {KindArchitecture, "tradeoff"}, {KindArchitecture, "trade off"},
	{KindArchitecture, "which database"}, {KindArchitecture, "qual banco"},
	{KindArchitecture, "decision"}, {KindArchitecture, "decisão"}, {KindArchitecture, "decisao"},
	{KindArchitecture, "pattern"}, {KindArchitecture, "padrão"}, {KindArchitecture, "abordagem"},
	{KindArchitecture, "approach"},
	// code change
	{KindCodeChange, "implement"}, {KindCodeChange, "implementa"}, {KindCodeChange, "add a"},
	{KindCodeChange, "adicionar"}, {KindCodeChange, "refactor"}, {KindCodeChange, "refatora"},
	{KindCodeChange, "rename"}, {KindCodeChange, "renomear"}, {KindCodeChange, "extract"},
	{KindCodeChange, "write a function"}, {KindCodeChange, "create a"}, {KindCodeChange, "criar"},
	{KindCodeChange, "edit"}, {KindCodeChange, "modify"}, {KindCodeChange, "change the"},
	{KindCodeChange, "altera"}, {KindCodeChange, "escreva"},
	// memory operation
	{KindMemoryOp, "remember"}, {KindMemoryOp, "lembra"}, {KindMemoryOp, "lembre"},
	{KindMemoryOp, "save this"}, {KindMemoryOp, "salva"}, {KindMemoryOp, "guarda"},
	{KindMemoryOp, "anota"}, {KindMemoryOp, "memoriza"}, {KindMemoryOp, "what did we"},
	{KindMemoryOp, "o que decidimos"}, {KindMemoryOp, "recall"}, {KindMemoryOp, "memory"},
	{KindMemoryOp, "memória"}, {KindMemoryOp, "memoria"},
}

// priority breaks ties when multiple kinds match the same number of signals.
// Higher wins. Security and memory ops are intent-defining even with one cue;
// debugging/test beat the broad code_change/planning buckets.
var priority = map[Kind]int{
	KindMemoryOp:      9,
	KindSecurity:      8,
	KindDebugging:     7,
	KindRelease:       6,
	KindTestExecution: 5,
	KindArchitecture:  4,
	KindPlanning:      3,
	KindCodeChange:    2,
	KindUnknown:       0,
}

// Detect classifies a prompt. Empty/whitespace or no-cue prompts return
// KindUnknown with zero confidence so callers can suppress noisy injection.
func Detect(prompt string) Intent {
	p := " " + strings.ToLower(strings.TrimSpace(prompt)) + " "
	if strings.TrimSpace(prompt) == "" {
		return Intent{Kind: KindUnknown}
	}

	counts := map[Kind]int{}
	matched := map[string]bool{}
	var signalsHit []string
	for _, s := range signals {
		if strings.Contains(p, s.cue) && !matched[s.cue] {
			matched[s.cue] = true
			counts[s.kind]++
			signalsHit = append(signalsHit, string(s.kind)+":"+s.cue)
		}
	}
	if len(counts) == 0 {
		return Intent{Kind: KindUnknown}
	}

	best := KindUnknown
	bestCount := 0
	for k, c := range counts {
		if c > bestCount || (c == bestCount && priority[k] > priority[best]) {
			best, bestCount = k, c
		}
	}

	// Confidence saturates: one cue ~0.5, two ~0.75, three+ ~0.9+. A single
	// high-priority cue (security/memory/debugging) gets a small bump so the
	// gate treats it as a confident match.
	conf := 1.0 - 1.0/float64(bestCount+1)
	if bestCount == 1 && priority[best] >= 7 {
		conf = 0.6
	}

	return Intent{Kind: best, Confidence: conf, Signals: signalsHit}
}
