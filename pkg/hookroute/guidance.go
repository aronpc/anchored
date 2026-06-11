package hookroute

// Guidance strings injected as additionalContext. They are intentionally
// short and intent-based (not phrase dictionaries): a rule the model can apply
// beats a list it has to match. Each foregrounds anchored's value — memory
// first, sandbox second — because the user's pain is that the model explores
// files before it ever consults memory.

const memoryRecallGuidance = `<context_guidance>
  <tip>
    You're searching the codebase. If this is about a prior decision, a convention, where something was discussed, or "how we do X" — call anchored_search FIRST: the answer may already be in memory and save the exploration. Pair anchored_kg_query when you name a specific project/service/library. Then continue the code search if memory didn't cover it.
  </tip>
</context_guidance>`

const grepGuidance = `<context_guidance>
  <tip>
    Before grepping: if you're looking for a prior decision, convention, or "where we discussed X", anchored_search may already hold it — search memory first. When you intend to COUNT, FILTER, or AGGREGATE matches (not spot-check one), run the search through anchored_execute(language, code) so the raw match list stays in the sandbox and only your derived answer enters context.
  </tip>
</context_guidance>`

const globGuidance = `<context_guidance>
  <tip>
    Looking for where something lives? If a prior decision or convention would answer it, anchored_search is faster than walking the tree. Otherwise carry on — Glob is correct for locating files you'll then open.
  </tip>
</context_guidance>`

const readGuidance = `<context_guidance>
  <tip>
    Reading to EDIT the file? Read is correct — Edit needs the exact bytes in your context to match against. Reading to ANALYZE, summarize, or extract from a large file (logs, JSON, CSVs, build artifacts)? Use anchored_execute_file(path, language, code) — the bytes stay in the sandbox and only what your code prints enters context.
  </tip>
</context_guidance>`

const bashGuidance = `<context_guidance>
  <tip>
    When you intend to PROCESS output (filter, count, parse, aggregate), use anchored_execute(language, code) — the raw output stays in the sandbox and only what you print enters context. Bash stays right for short fixed output or state mutation (git, mkdir, rm, mv, navigation). And if the question is about prior work or a decision, anchored_search first.
  </tip>
</context_guidance>`

const externalMCPGuidance = `<context_guidance>
  <tip>
    External MCP tools often return large payloads (channel history, file content, search results) that enter context in full. When you'll filter, count, or aggregate that data, pipe it through anchored_execute(language, code) so only the derived answer enters context.
  </tip>
</context_guidance>`

// webFetchGuidanceNoOpt is the soft fallback when the optimizer is disabled, so
// we don't deny WebFetch into a sandbox tool that would itself error.
const webFetchGuidanceNoOpt = `<context_guidance>
  <tip>
    Fetching a URL you'll want to query later? Enable context_optimizer to use anchored_fetch_and_index + anchored_ctx_search (raw page bytes stay out of context). With it off, WebFetch is your option — keep an eye on payload size.
  </tip>
</context_guidance>`
