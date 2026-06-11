// Package hookroute is the single source of truth for anchored's PreToolUse
// routing: how the agent's native tool calls (Read/Grep/Glob/Bash/WebFetch/
// Agent and external MCP tools) are steered toward anchored's memory and
// sandbox tools.
//
// It is a Go port of context-mode's hooks/core/routing.mjs, adapted to two
// things anchored cares about that context-mode does not:
//
//  1. Memory recall. anchored is a memory layer first. When the model reaches
//     for Read/Grep/Glob/codebase-search, that is exactly the moment a prior
//     decision or convention might already be in memory — so those branches
//     nudge toward anchored_search/anchored_context, not only toward the
//     sandbox. This is the lever that fixes "the model greps files before it
//     ever consults memory".
//
//  2. Optimizer gating. Every sandbox tool (anchored_execute*, _index,
//     _fetch_and_index, _ctx_search) hard-errors when context_optimizer is
//     disabled. So a deny-redirect into one of those tools while the optimizer
//     is off would be a dead-end. Redirects that target a sandbox tool are
//     gated on OptimizerEnabled and degrade to a soft nudge when it is off.
//
// Why deny (not allow+updatedInput) for Bash redirects: Claude Code ignores
// `updatedInput.command` under permissionDecision "allow" — the original
// command runs unchanged. Only "deny" with the redirect text in the reason is
// honored. (Verified by context-mode; see hooks/core/formatters.mjs.) So Bash
// redirects are built as Deny decisions directly; Modify is reserved for the
// Agent prompt injection, which CC does honor via updatedInput.
package hookroute

import (
	"regexp"
	"strings"
)

// ActionKind is the normalized decision type. FormatDecision maps it to the
// Claude Code hook wire shape.
type ActionKind string

const (
	ActionPass    ActionKind = ""        // passthrough — let the tool run
	ActionDeny    ActionKind = "deny"    // block the tool, feed Reason to the model
	ActionAsk     ActionKind = "ask"     // prompt the user
	ActionModify  ActionKind = "modify"  // rewrite tool input (Agent prompt only)
	ActionContext ActionKind = "context" // allow + inject AdditionalContext
)

// Decision is the normalized routing outcome.
type Decision struct {
	Action            ActionKind
	Reason            string         // deny reason
	UpdatedInput      map[string]any // modify payload
	AdditionalContext string         // context payload
}

// Options carries the per-call context the router needs.
type Options struct {
	OptimizerEnabled bool   // context_optimizer.enabled — gates sandbox redirects
	SubagentBlock    string // routing block prepended to Agent prompts
	SessionID        string // stable id for the guidance throttle
	ProjectDir       string // reserved for future per-project policy
}

// externalMCPNudgeEvery — re-fire the external-MCP advisory every N matching
// calls so it survives compaction in long MCP-heavy sessions.
const externalMCPNudgeEvery = 10

// memoryRecallNudgeEvery — re-fire the "search memory first" advisory on
// codebase-exploration tools periodically; once-per-session is too weak once
// the session compacts.
const memoryRecallNudgeEvery = 8

// RoutePreToolUse returns a normalized decision for a PreToolUse event, or nil
// for passthrough. It does NOT handle anchored's own security blocks
// (dangerous-pattern in sandbox tools, secret-in-memory-file) — those run in
// the hook before this is called.
func RoutePreToolUse(tool string, toolInput map[string]any, opt Options) *Decision {
	canonical := canonicalToolName(tool)

	switch canonical {
	case "WebFetch":
		return routeWebFetch(toolInput, opt)
	case "Read":
		return routeRead(toolInput, opt)
	case "Grep":
		return guidanceOnce("grep", grepGuidance, opt.SessionID)
	case "Glob":
		return guidanceOnce("glob", globGuidance, opt.SessionID)
	case "Bash":
		return routeBash(toolInput, opt)
	case "Agent":
		return routeAgent(toolInput, opt)
	}

	// External MCP tools (not anchored's own): periodic nudge to pipe large
	// payloads through anchored_execute. Gated on optimizer (the redirect
	// target). Skip anchored's own tools — they have dedicated handling.
	if opt.OptimizerEnabled && isExternalMCPTool(tool) {
		return guidancePeriodic("external-mcp", externalMCPGuidance, opt.SessionID, externalMCPNudgeEvery)
	}

	return nil
}

// ─── WebFetch ───────────────────────────────────────────────────────────────

func routeWebFetch(toolInput map[string]any, opt Options) *Decision {
	url, _ := toolInput["url"].(string)
	if !opt.OptimizerEnabled {
		// Can't redirect into a disabled sandbox — nudge once instead of
		// denying into a dead-end.
		return guidanceOnce("webfetch", webFetchGuidanceNoOpt, opt.SessionID)
	}
	return &Decision{
		Action: ActionDeny,
		Reason: "anchored: WebFetch redirected. Call anchored_fetch_and_index(url: \"" + url +
			"\", source: \"...\") to fetch + index the page, then anchored_ctx_search(queries: [...]) to query it — the raw page bytes stay out of your context. Full network access; retry the same call on a transient DNS error (EAI_AGAIN, ETIMEDOUT, ENETUNREACH).",
	}
}

// ─── Read ─────────────────────────────────────────────────────────────────--

// readRedirectThreshold — files larger than this flood context; redirect to
// anchored_execute_file for analysis. Smaller reads get the once-per-session
// nudge. Matches context-mode's 50_000-byte threshold.
const readRedirectThreshold = 50_000

func routeRead(toolInput map[string]any, opt Options) *Decision {
	// Never block a Read the model needs in order to Edit — Edit must match the
	// exact bytes. We can't know intent here, so Read is never DENIED; the
	// large-file case still only nudges (context), never denies.
	return guidanceOnce("read", readGuidance, opt.SessionID)
}

// ─── Bash ─────────────────────────────────────────────────────────────────--

func routeBash(toolInput map[string]any, opt Options) *Decision {
	command := shellCommand(toolInput)
	if command == "" {
		return nil
	}
	stripped := stripQuotedContent(command)

	// curl/wget that floods stdout → redirect to sandbox (gated on optimizer).
	if curlWgetFloods(stripped) {
		if opt.OptimizerEnabled {
			return &Decision{
				Action: ActionDeny,
				Reason: "anchored: curl/wget redirected. Call anchored_execute(language, code) to fetch the URL, derive your answer in code, and print only the result — the raw HTTP body stays in the sandbox instead of entering your context. Or anchored_fetch_and_index(url, source) when you want to query the response later. Full network access; retry on transient DNS errors (EAI_AGAIN, ETIMEDOUT, ENETUNREACH).",
			}
		}
		return guidanceOnce("bash", bashGuidance, opt.SessionID)
	}

	// Inline HTTP in a -e/-c snippet (fetch()/requests.get/http.get) → sandbox.
	// Match against `noHeredoc` (quotes INTACT, only heredoc bodies removed) on
	// purpose: the HTTP call we want to redirect lives inside the quoted script
	// literal of `node -e "fetch(url)"` / `python3 -c "requests.get(url)"`, so
	// stripping quotes would hide the very thing we mean to catch. These
	// patterns are specific enough that innocent quoted text rarely trips them
	// (unlike the bare word "curl"). Heredoc bodies are still removed so a
	// `cat <<EOF … requests.get … EOF` data blob doesn't false-positive.
	noHeredoc := stripHeredocs(command)
	if inlineHTTPRe.MatchString(noHeredoc) {
		if opt.OptimizerEnabled {
			return &Decision{
				Action: ActionDeny,
				Reason: "anchored: inline HTTP redirected. Call anchored_execute(language, code) to fetch, derive your answer in code, and print only the result — the raw response body stays in the sandbox. Full network access; retry on transient DNS errors.",
			}
		}
		return guidanceOnce("bash", bashGuidance, opt.SessionID)
	}

	// Verbose build tools (gradle/maven/sbt) → sandbox tail.
	if buildToolRe.MatchString(stripped) {
		if opt.OptimizerEnabled {
			return &Decision{
				Action: ActionDeny,
				Reason: "anchored: build tool redirected. Call anchored_execute(language: \"shell\", code: \"<your build cmd> 2>&1 | tail -30\") so the verbose build log stays in the sandbox and only the tail enters your context. Swap tail for grep -E '(error|warning|FAIL)' to surface only what matters.",
			}
		}
		return guidanceOnce("bash", bashGuidance, opt.SessionID)
	}

	// Structurally-bounded probes (pwd, git status, --version, …) → no nudge.
	if isStructurallyBounded(command) {
		return nil
	}

	// Codebase search via shell (grep -r, rg, ag, find …) is exactly when a
	// prior decision/convention might already be in memory → memory nudge,
	// re-fired periodically so it survives compaction.
	if codebaseSearchRe.MatchString(stripped) {
		if d := guidancePeriodic("memory-recall", memoryRecallGuidance, opt.SessionID, memoryRecallNudgeEvery); d != nil {
			return d
		}
		// Already nudged this cycle — fall through to the generic bash nudge.
	}

	// Everything else: generic once-per-session bash nudge.
	return guidanceOnce("bash", bashGuidance, opt.SessionID)
}

// ─── Agent (subagent prompt injection) ───────────────────────────────────--

// agentPromptFields are the field names different clients use for the subagent
// instruction. We inject the routing block into whichever is present.
var agentPromptFields = []string{"prompt", "request", "objective", "question", "query", "task"}

func routeAgent(toolInput map[string]any, opt Options) *Decision {
	if opt.SubagentBlock == "" {
		return nil
	}
	field := "prompt"
	for _, f := range agentPromptFields {
		if _, ok := toolInput[f]; ok {
			field = f
			break
		}
	}
	prompt, _ := toolInput[field].(string)

	updated := make(map[string]any, len(toolInput)+1)
	for k, v := range toolInput {
		updated[k] = v
	}
	updated[field] = prompt + "\n\n" + opt.SubagentBlock

	return &Decision{Action: ActionModify, UpdatedInput: updated}
}

// ─── Detection helpers ───────────────────────────────────────────────────--

// canonicalToolName normalizes a platform-specific tool name (or an
// mcp__/MCP:/@server form) to the canonical Claude Code name. Only the aliases
// anchored's hooks actually route on are mapped; unknowns pass through.
func canonicalToolName(tool string) string {
	if c, ok := toolAliases[tool]; ok {
		return c
	}
	return tool
}

var toolAliases = map[string]string{
	// Gemini CLI / Qwen
	"run_shell_command": "Bash",
	"read_file":         "Read",
	"read_many_files":   "Read",
	"grep_search":       "Grep",
	"search_file_content": "Grep",
	"web_fetch":         "WebFetch",
	"glob":              "Glob",
	// Claude Code legacy name for the subagent tool
	"Task": "Agent",
	// OpenCode
	"bash":  "Bash",
	"view":  "Read",
	"grep":  "Grep",
	"fetch": "WebFetch",
	"agent": "Agent",
	// Codex
	"shell":         "Bash",
	"local_shell":   "Bash",
	"container.exec": "Bash",
	"grep_files":    "Grep",
	// VS Code Copilot / Cursor
	"run_in_terminal": "Bash",
	"Shell":           "Bash",
	// Kiro
	"fs_read":      "Read",
	"execute_bash": "Bash",
}

// isExternalMCPTool reports whether tool is an MCP-namespaced tool that does
// NOT belong to anchored itself. Mirrors context-mode's isExternalMcpTool for
// the Claude Code wire shape (mcp__<server>__<tool>).
func isExternalMCPTool(tool string) bool {
	if strings.HasPrefix(tool, "mcp__") {
		rest := strings.TrimPrefix(tool, "mcp__")
		server := rest
		if i := strings.Index(rest, "__"); i >= 0 {
			server = rest[:i]
		}
		if server == "" {
			return false
		}
		return !strings.Contains(server, "anchored")
	}
	return false
}

func shellCommand(toolInput map[string]any) string {
	if s, ok := toolInput["command"].(string); ok && s != "" {
		return s
	}
	if s, ok := toolInput["cmd"].(string); ok && s != "" {
		return s
	}
	return ""
}

// hasShellControlOperator reports whether a command contains any shell control
// operator that could compose a "safe" command with an unbounded sink. RE2 has
// no lookahead (unlike context-mode's JS regex), so this is a strict char/seq
// scan: ANY of | & ; < > backtick newline, or `$(`, disqualifies. Stricter
// than context-mode (it also rejects bare single < > &) — fail-safe: more
// commands get the nudge, none bypass the gate.
func hasShellControlOperator(cmd string) bool {
	if strings.ContainsAny(cmd, "|&;<>`\n\r") {
		return true
	}
	return strings.Contains(cmd, "$(")
}

var safeCommandPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^pwd$`),
	regexp.MustCompile(`^whoami$`),
	regexp.MustCompile(`^hostname(\s+-[a-zA-Z]+)?$`),
	regexp.MustCompile(`^uname(\s+-[a-zA-Z]+)?$`),
	regexp.MustCompile(`^id(\s+\S+)?$`),
	regexp.MustCompile(`^date(\s+[^\r\n]+)?$`),
	regexp.MustCompile(`^echo\s`),
	regexp.MustCompile(`^printf\s`),
	regexp.MustCompile(`^which\s+\S+(\s+\S+)*$`),
	regexp.MustCompile(`^type\s+\S+(\s+\S+)*$`),
	regexp.MustCompile(`^command\s+-v\s+\S+(\s+\S+)*$`),
	regexp.MustCompile(`^readlink(\s+[^\r\n]+)?$`),
	regexp.MustCompile(`^basename(\s+[^\r\n]+)?$`),
	regexp.MustCompile(`^dirname(\s+[^\r\n]+)?$`),
	regexp.MustCompile(`^realpath(\s+[^\r\n]+)?$`),
	regexp.MustCompile(`^cd(\s+[^\r\n]+)?$`),
	regexp.MustCompile(`^mkdir(\s+[^\r\n]+)?$`),
	regexp.MustCompile(`^touch\s+[^\r\n]+$`),
	regexp.MustCompile(`^mv\s+[^\r\n]+$`),
	regexp.MustCompile(`^cp\s+[^\r\n]+$`),
	regexp.MustCompile(`^rm\s+[^\r\n]+$`),
	regexp.MustCompile(`^ln\s+[^\r\n]+$`),
	regexp.MustCompile(`^ls(\s+[^\r\n]+)?$`),
	regexp.MustCompile(`^git\s+status(\s+[^\r\n]+)?$`),
	regexp.MustCompile(`^git\s+rev-parse(\s+[^\r\n]+)?$`),
	regexp.MustCompile(`^git\s+remote(\s+-v|\s+show\s+\S+)?$`),
	regexp.MustCompile(`^git\s+branch(\s+[^\r\n]+)?$`),
	regexp.MustCompile(`^git\s+config\s+--get(\s+[^\r\n]+)?$`),
	regexp.MustCompile(`^git\s+diff\s+--stat(\s+[^\r\n]+)?$`),
	regexp.MustCompile(`^git\s+diff\s+--name-only(\s+[^\r\n]+)?$`),
	regexp.MustCompile(`^git\s+stash\s+list$`),
	regexp.MustCompile(`^git\s+tag(\s+-l(\s+[^\r\n]+)?)?$`),
	regexp.MustCompile(`^git\s+log\s+-\d{1,2}(\s+[^\r\n]+)?$`),
	regexp.MustCompile(`(^|\s)--version(\s|$)`),
	regexp.MustCompile(`^\S+\s+-V(\s|$)`),
}

// verboseFlagRe matches -v / --verbose anywhere in a flag bundle. RE2 can't do
// the negative lookahead context-mode uses, so cp/mv/rm/ln verbose exclusion is
// a post-filter on top of the base pattern match.
var verboseFlagRe = regexp.MustCompile(`(^|\s)(--verbose|-[a-zA-Z]*v[a-zA-Z]*)(\s|$)`)
var recursiveLsRe = regexp.MustCompile(`(^|\s)(--recursive|-[a-zA-Z]*R[a-zA-Z]*)(\s|$)`)

var verboseProneCmd = regexp.MustCompile(`^(cp|mv|rm|ln)\s`)

// isStructurallyBounded reports whether a command's output is small enough that
// the routing nudge would be noise. Conservative: any shell control operator or
// an unknown command returns false.
func isStructurallyBounded(command string) bool {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return false
	}
	if hasShellControlOperator(trimmed) {
		return false
	}
	matched := false
	for _, rx := range safeCommandPatterns {
		if rx.MatchString(trimmed) {
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	// Post-filter the verbose/recursive carve-outs RE2 lookahead can't express.
	if verboseProneCmd.MatchString(trimmed) && verboseFlagRe.MatchString(trimmed) {
		return false
	}
	if strings.HasPrefix(trimmed, "ls") && recursiveLsRe.MatchString(trimmed) {
		return false
	}
	return true
}

// stripHeredocs removes heredoc bodies so inline-HTTP regexes only see the
// command tokens, not e.g. `cat <<EOF ... requests.get ... EOF`.
var heredocRe = regexp.MustCompile(`<<-?\s*["']?(\w+)["']?[\s\S]*?\n\s*`)

func stripHeredocs(cmd string) string {
	// RE2 has no backreference for the closing label; approximate by removing
	// from the heredoc opener to end-of-string when present. This is a best-
	// effort scrub for false-positive suppression, not a security boundary.
	loc := regexp.MustCompile(`<<-?\s*["']?\w+["']?`).FindStringIndex(cmd)
	if loc == nil {
		return cmd
	}
	return cmd[:loc[0]]
}

// stripQuotedContent removes heredocs and quoted strings so token regexes don't
// match inside e.g. `gh issue edit --body "text with curl in it"`.
var singleQuoteRe = regexp.MustCompile(`'[^']*'`)
var doubleQuoteRe = regexp.MustCompile(`"[^"]*"`)

func stripQuotedContent(cmd string) string {
	s := stripHeredocs(cmd)
	s = singleQuoteRe.ReplaceAllString(s, "''")
	s = doubleQuoteRe.ReplaceAllString(s, `""`)
	return s
}

var (
	curlWgetRe   = regexp.MustCompile(`(?i)(^|\s|&&|\||;)(curl|wget)\s`)
	inlineHTTPRe = regexp.MustCompile(`(?i)fetch\s*\(\s*['"](https?://|http)|requests\.(get|post|put)\s*\(|http\.(get|request)\s*\(`)
	buildToolRe  = regexp.MustCompile(`(?i)(^|\s|&&|\||;)(\./gradlew|gradlew|gradle|\./mvnw|mvnw|mvn|\./sbt|sbt)(\s|$)`)
	// codebaseSearchRe matches shell-driven code search that a memory lookup
	// might short-circuit: recursive grep, ripgrep, the silver searcher, find.
	codebaseSearchRe = regexp.MustCompile(`(?i)(^|\s|&&|\||;)(grep\s+(-[a-zA-Z]*[rR][a-zA-Z]*\s|--recursive)|rg\s|ag\s|find\s+\S)`)
)

// curlWgetFloods reports whether a curl/wget invocation would flood stdout
// (no file output, or stdout alias, or verbose). Silent file-output downloads
// are allowed. Mirrors context-mode's per-segment evaluation.
func curlWgetFloods(stripped string) bool {
	if !curlWgetRe.MatchString(stripped) {
		return false
	}
	segments := regexp.MustCompile(`\s*(?:&&|\|\||;)\s*`).Split(stripped, -1)
	for _, seg := range segments {
		s := strings.TrimSpace(seg)
		if !regexp.MustCompile(`(?i)(^|\s)(curl|wget)\s`).MatchString(s) {
			continue
		}
		isCurl := regexp.MustCompile(`(?i)\bcurl\b`).MatchString(s)
		isWget := regexp.MustCompile(`(?i)\bwget\b`).MatchString(s)

		hasFileOutput := false
		if isCurl {
			hasFileOutput = regexp.MustCompile(`\s(-o|--output)\s`).MatchString(s) || strings.Contains(s, ">")
		} else if isWget {
			hasFileOutput = regexp.MustCompile(`\s(-O|--output-document)\s`).MatchString(s) || strings.Contains(s, ">")
		}
		if !hasFileOutput {
			return true // stdout flood
		}
		// stdout aliases: -o -, -o /dev/stdout, -O - → still floods stdout.
		if isCurl && regexp.MustCompile(`\s(-o|--output)\s+(-|/dev/stdout)(\s|$)`).MatchString(s) {
			return true
		}
		if isWget && regexp.MustCompile(`\s(-O|--output-document)\s+(-|/dev/stdout)(\s|$)`).MatchString(s) {
			return true
		}
		// verbose/trace floods stderr → context.
		if regexp.MustCompile(`\s(-v|--verbose|--trace)\b`).MatchString(s) {
			return true
		}
		// Genuine file destination, no stdout alias, not verbose: nothing of
		// substance reaches the model's context. Allow it. (We deliberately
		// diverge from context-mode here, which also requires -s/--silent to
		// suppress curl's progress meter — for a memory-first tool, denying a
		// plain file download is too intrusive and the redirect message, which
		// talks about the "HTTP body", would be misleading for a download.)
	}
	return false
}
