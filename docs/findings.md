# Findings: what three codebases agree on, and where they diverge

Synthesis of source-level readings of Crush, neo, pi and the Go ecosystem around them. The
per-project reports those readings produced are no longer kept; this document is what survived
them.

> **Status:** Crush landed and resolved both questions that were marked ⚠ — how to stream
> markdown without flicker, and how to avoid re-rendering the conversation every frame. It
> also **corrected one recommendation** (tool schema generation). opencode is still
> outstanding; it should only affect the client/server discussion, which we've already
> decided against.

---

## Locked decisions

From the brief so far, these are settled and not up for re-litigation:

| Decision | Choice |
|---|---|
| Language / distribution | Go, single static binary, `CGO_ENABLED=0` |
| Goal | A tool used daily **and** understood deeply — phased, not either/or |
| Libraries | Curated libraries (Bubble Tea et al.) — **no agent framework** |
| Provider layer | Native Anthropic adapter + one OpenAI-compatible adapter with swappable base URL |
| Auth | **API keys only** in the MVP (with `$(command)` shell expansion). OAuth is phase 2 |
| TUI shape | Chat-first, like neo and pi — no multi-pane workspace |
| Topology | Single process. Agent core as a UI-agnostic package |

The topology follows from "single binary like neo and pi" — both are single-process, and
both keep the agent core free of terminal knowledge. neo's entire agent↔TUI wiring is
about 30 lines.

---

## Where all three converge

These are the strongest signals in the research. When independent projects arrive at the
same answer, it is usually load-bearing.

### 1. Tool schemas — *corrected by Crush*

I previously recommended hand-writing schema maps, based on neo (which writes `map[string]any`
literals) and pi (TypeBox, which *is* JSON Schema at runtime). **Crush shows a better option
for Go**, and it's the one to take:

```go
// fantasy@v0.40.0/tool.go:102 — generic constructor, schema derived from the input type
func NewAgentTool[TInput any](name, description string, fn ...) AgentTool
// internally: schema.Generate(reflect.TypeOf(input))
```

A tool author defines one Go struct, tagged for JSON name and description:

```go
type EditParams struct {
    FilePath  string `json:"file_path" description:"Absolute path to the file to edit"`
    OldString string `json:"old_string" description:"Exact text to replace"`
    NewString string `json:"new_string" description:"Replacement text"`
}
```

That struct is simultaneously the schema source *and* the unmarshal target, with no
duplication and no drift between "what the model was told" and "what we parse." Charm rolled
their own small reflector rather than using `invopop/jsonschema` (which they do use, but only
for the *config* schema).

**For us:** reflection with a `description` struct tag. The descriptions still get hand-tuned
— they're prompt text — but they live next to the field they describe instead of in a
parallel map that silently rots.

### 2. The edit tool is exact-string replace, and it fails loudly

All three converge, and the ecosystem scan confirms Claude Code and Aider both arrived here
from other designs. But they differ in how much recovery they attempt:

| | Strategy |
|---|---|
| neo | Exact match, exactly one occurrence, else error. Nothing more |
| pi | Exact → normalized retry (trailing whitespace, NFKC, curly quotes → ASCII) → error |
| **Crush** | Exact → ambiguity check → **whole-line whitespace-normalized match with automatic re-indentation to the file's detected indent style** → **diagnostic hint showing the closest near-miss** |

Crush's ladder is the best of the three and worth copying wholesale. Two details make it:

- The normalized tier only accepts matches aligned on **whole-line boundaries** — never a
  partial line — and re-indents the replacement to the file's actual indent unit (tabs vs
  N-space) before splicing. It then **tells the model in the tool result** that the match
  wasn't byte-exact, so it can verify.
- On total failure it doesn't just say "not found." It runs a line-similarity scan, finds the
  best-guess location, and prints the file's actual content there with tabs and leading
  spaces visualized (`→`, `·`) so the model can self-correct rather than blindly retry.

Note what Crush deliberately does *not* do: there is **no Levenshtein or approximate character
matching** anywhere. "Fuzzy" means whitespace normalization only. That's the right line —
approximate matching is how you silently corrupt a file.

**For us:** Crush's four-rung ladder. Whole-file write as the fallback for new files.

### 3. Compaction, never truncation

Both summarize with the LLM rather than dropping a sliding window, and both refuse to cut
between a `tool_use` and its `tool_result`.

**For us:** same. Steal pi's refinements — hybrid token estimation (real usage from the last
assistant message, `chars/4` only for the tail), and carrying read/modified file lists across
the boundary so the agent doesn't forget what it touched.

### 4. `AGENTS.md`, discovered by walking up from cwd

Both use `AGENTS.md` (pi also accepts `CLAUDE.md`), both walk ancestors, both order
outermost-first, both wrap each file in a labeled block.

**For us:** `AGENTS.md`, accept `CLAUDE.md`. Copy pi's git-worktree shadowing fix — it's a
real bug we'd otherwise hit.

### 5. Prompt caching needs a deliberately stable prefix

Both split the system prompt into a cacheable stable block and an uncached dynamic tail. The
Anthropic cache is a prefix match: one byte changed before a breakpoint invalidates
everything after it.

**For us:** design the system prompt as ordered blocks with an explicit `Cache bool`, and
put anything volatile (timestamps, cwd, per-turn state) after the last breakpoint. Cheap to
build in, expensive to retrofit.

### 6. Kill the process group, not the process

Both kill the whole group on bash timeout/cancel. Go's `exec.CommandContext` only kills the
direct child ([golang/go#21135](https://github.com/golang/go/issues/21135)), so
`bash -c "foo &"` orphans grandchildren.

**For us:** `SysProcAttr{Setpgid: true}` plus a `cmd.Cancel` override calling
`syscall.Kill(-pid, SIGKILL)`, and `cmd.WaitDelay`. Point `Stdout` and `Stderr` at the same
writer for free chronological interleaving.

### 7. Bounded output that keeps both ends

neo caps at 256KB keeping head **and** tail. pi caps at 2000 lines / 50KB and spills the
full output to a temp file, appending the path.

**For us:** both. The tail of a failing build is where the error is; the head is where the
command echo is. Truncating either loses the diagnosis.

---

## Where they diverge — and what we should do

### Streaming: neo has none, pi streams everything

| | neo | pi | Crush |
|---|---|---|---|
| Token streaming | **None.** Every provider call blocks. Even its SSE client fully buffers before parsing | Full — deltas flow to the UI, each event carrying the accumulated `partial` | Full — normalized into one event enum, delivered as a Go 1.23 `iter.Seq[StreamPart]` |

neo's own code comments defend this ("neo presents blocking results"), but its research
report flags it as the project's biggest UX gap: nothing renders until the entire turn's
HTTP response completes.

**Decision: stream from day one.** This is the difference between a demo and a tool you use
daily, and it is genuinely painful to retrofit — it changes the event model, the UI's render
strategy, and the provider interface all at once.

Adopt pi's two contracts verbatim:
- **Every stream event carries the full accumulated message so far.** The UI re-renders
  `partial`; it never reassembles deltas. Removes a whole class of bug.
- **The provider stream function must never return an error for model/request failures.**
  Encode them as a final message with `stop_reason: error`. One error path, not two — which
  is exactly why pi's retry classifier can be a pure function over a message.

### Diff rendering: Crush is the model, neo is the warning

neo has **no diff view at all** — `edit_file` renders as the literal string `edit <path>`.
You cannot see what the agent changed without running `git diff` in another terminal. Given
that edits *are* the product, that's the single worst gap in the reference set.

pi does word-level intra-line diffing. **Crush** has a self-contained `internal/ui/diffview`
package (1,536 LOC, its own build file, clearly meant to be extractable):

- **`aymanbagabas/go-udiff`** computes — confirming the ecosystem scan's pick, and note the
  traps: `hexops/gotextdiff` is abandoned and `sergi/go-diff` doesn't emit unified format.
- **Both unified and side-by-side** layouts.
- **Line-level styling only** — a `Style` struct with one `LineStyle{LineNumber, Symbol, Code}`
  per class (`InsertLine`, `DeleteLine`, `EqualLine`, `MissingLine`, `DividerLine`,
  `Filename`). No intra-line word highlighting, unlike pi.
- **Chroma syntax-highlights inside each diff line**, memoized by content hash (`xxh3`).
- **Wide lines scroll horizontally rather than wrap**, with grapheme-aware cutting via
  `charmbracelet/x/ansi` — wrapped diffs are unreadable.

**Decision:** build it, following Crush's shape. Unified layout and line-level color for v1;
syntax highlighting and side-by-side are additive later.

### Permissions: two ship nothing, Crush ships a real one

neo's approval matcher is documented as *"literal user preferences, not a security policy"*.
pi's `SECURITY.md` states outright: *"the Pi coding agent intentionally does not have a
sandbox"*, and its entire confirmation flow is a 35-line example extension. opencode also
ships no sandboxing.

**Crush is the counter-example, and it settles the argument.** It has a real permission
service — a six-rung ladder that only asks the user when nothing else has already answered:

```
1. YOLO mode on?                                 → allow
2. tool in the config allow-list?                → allow
3. a PreToolUse hook pre-approved it?            → allow
4. "always allow" set for this session?          → allow
5. already granted for this (tool, action, path)? → allow
6. otherwise → publish a request, block on a channel, ask
```

Two details worth copying: the grant is keyed by **path**, so "always allow writes in `/foo`"
does not silently cover `/bar`; and grants are session-scoped and in-memory, so they don't
quietly persist across restarts. Crush also doesn't hard-jail paths — reading outside the
working directory triggers an *extra* permission request rather than a silent allow or block.

So the position isn't "everyone omits this, we should be different." It's "the most mature
project in the set built one, and the pattern is about 200 lines."

**Decision: ship a real default.** Not because they're careless, but because we're building
this fresh and the cost is low:
- Workspace-root every file tool via **Go 1.24's `os.Root`**. neo already does this for
  `grep`/`glob` and simply never extended it to `read`/`write`/`edit`. Extending it is nearly
  free at design time and awkward later.
- An approval gate on writes and bash, with an allow-list and an explicit auto-accept mode.
- Be honest in the docs about what it is: a blast-radius limiter, not a security boundary
  against a hostile model.

### Tools owning their UI rendering: pi's mistake, worth avoiding

pi's `bash.ts` returns terminal components directly from the tool. pi's own analysis flags
this as a weakness — the tools can't be used by any other frontend without dragging the TUI
along.

**Crush does the opposite and it's clearly better.** Tools return a plain
`ToolResponse{Content string, IsError bool, ...}`, and the TUI has a *separate* renderer per
tool family under `internal/ui/chat/` (`bash.go`, `file.go`, `search.go`, `fetch.go`,
`agent.go`). Tool logic and tool presentation are different files owned by different packages.

**Decision: tools return structured data.** A `ToolResult` carries text for the model plus
optional typed details (a diff, a file list, an exit code). The TUI decides how to draw it.
This also keeps a headless mode honest — and it's what makes `rasp run -p "..."` free.

### Client/server: three of four have one, and we still shouldn't

Crush has `server`/`client`/`proto`/`backend` plus Swagger codegen — roughly 15,000 LOC so
multiple clients can attach to one long-running agent over a Unix socket. opencode goes
further still, with a Go TUI, an HTTP/SSE API, *and* an ACP server (Zed's editor protocol) as
three front-ends on one core.

That's a real payoff — but it's a payoff for *their* problem (multi-client, editor
integration, remote sessions), not ours. Both projects show the same thing though, and it's
the part worth taking: **one core engine, N thin front-ends.** Get the seam right — a core
package that emits typed events and knows nothing about terminals — and a server becomes
additive later rather than a rewrite. Crush's own `Workspace` interface is exactly that seam.

**Decision: single process, clean seam, no protocol.** Same as before, now with evidence for
both halves.

### Sub-agents: neo has them, pi deliberately doesn't

neo ships an `agent` tool with `work`/`inspect` modes, restricted tool sets, a session-wide
cap and per-agent wall-clock timeouts. pi ships nothing and says so loudly, offering a
session *tree* (`/fork`, `/tree`) for exploration instead.

**Decision: not in v1.** It's a genuine capability, but it multiplies the concurrency and
context-management surface before the single-agent path is solid. neo's design — restricted
tool sets by mode, hard caps — is the one to copy when we do.

### Session storage: JSON blob vs JSONL tree

neo writes one JSON file per session plus an index, and admits concurrent processes can lose
index updates. pi writes append-only JSONL where every entry has `id`/`parentId`, forming a
tree, written atomically via temp+rename. Claude Code also uses JSONL.

**Decision: JSONL, append-only, atomic writes.** Include `parent_id` on every entry from the
start even if we don't ship branching in v1 — it costs one field now and makes `/fork`
possible later without a migration. Model changes go in as first-class entries, not
metadata, so replay reproduces which model produced which turn. Add a SQLite index
(`modernc.org/sqlite`, pure Go) only when listing/search actually needs one.

---

## Ideas worth stealing outright

Ranked by value-to-effort. Every one of these is small.

| # | Idea | Source | Why |
|---|---|---|---|
| 1 | **`tool_use` never exists without `tool_result`** — *two* mechanisms. Build the assistant message and its results together, committing both or neither (neo); **and** repair on history reload by injecting synthetic error results for orphaned calls and dropping orphaned results (Crush, `agent.go:1626/1662`) | neo + Crush | The most common source of "invalid request" errors. Prevent-on-write handles cancellation; repair-on-read *also* survives crashes, kills and partial writes. An orphan that reaches disk **permanently bricks the session** — every later request fails |
| 1b | **Buffer tool calls until the step's stream fully drains, then dispatch** | Crush | Guarantees ordering; makes opt-in parallelism a single flag |
| 1c | **Loop detection** — SHA-256 of `(tool, input, output)` per step; halt if the same signature appears >5 times in the last 10 | Crush | ~30 lines. neo's only runaway guard is `MaxTurns=500`; pi has none |
| 1d | **Recover panics inside every tool**, convert to a failed result | Crush | One bad tool returns an error instead of killing the process |
| 1e | **Read-before-edit + stale-mtime rejection** (`filetracker`) | Crush | Refuse to edit a file this session hasn't read, or one modified since that read. Stops the model clobbering changes it never saw |
| 2 | **Truncated-tool-call guard** — if `stop_reason == "length"`, fail *every* tool call in that message | pi | Truncated JSON args can parse *and* validate while being semantically wrong. Silent file corruption |
| 3 | **Pure loop + stateful wrapper** — the loop owns no state; policy lives in callbacks | pi | Lets you test the loop with no provider, no filesystem, no terminal |
| 4 | **Runtime-owned parallelism** — the tool decides if it's parallel-safe, never the model; unknown tools fail closed to serial | neo | Removes an entire trust question |
| 5 | **Per-file mutation mutex**, keyed by resolved realpath | pi | ~30 lines. Makes parallel edits safe without a global lock |
| 6 | **Filesystem access behind an interface**, never direct syscalls in tools | pi | The only reason pi can run tools inside a micro-VM without touching tool code. Free now, expensive later |
| 7 | **Two-tier retry** — transport (honors `retry-after`, jitter, *throws* past a cap rather than sleeping) and semantic (never retry quota/billing) | pi | A 429 meaning "out of credit" shouldn't burn the retry budget |
| 8 | **Steering vs follow-up as separate queues** — "interrupt now" vs "queue until done" | both | Genuinely different operations; separating them avoids racing the tool executor |
| 9 | **Hybrid token estimation** — real usage from the last assistant message, `chars/4` only for the tail | pi | neo's flat `chars/4` drifts badly on code-heavy sessions |
| 10 | **Re-resolve credentials on every LLM call** | pi | An OAuth token expiring during a long tool phase shouldn't kill the turn — and we're shipping OAuth |

---

## The OAuth question needs a decision

We now have four data points, and they're unambiguous.

| | Anthropic subscription OAuth |
|---|---|
| **pi** | Yes — renames every tool to Claude Code's names (`Read`, `Bash`, `Edit`…) and injects *"You are Claude Code, Anthropic's official CLI for Claude."* Source comment: `// Stealth mode`. Pins `claudeCodeVersion = "2.1.75"` |
| **opencode** | Yes — PKCE against `console.anthropic.com/oauth/authorize` / `claude.ai/oauth/authorize` using **Claude Code's own client ID** (`9d1c250a-…`), injects the same "You are Claude Code" system line (from a file literally named `anthropic_spoof.txt`), and **zeroes the reported cost client-side** |
| **neo** | No — OAuth only for ChatGPT/Codex, via device code |
| **Crush** | No — Copilot and their own Hyper product only. The generic `OAuthToken` field exists for Anthropic but no flow ships |

So: it is achievable, two projects do it, and **both do it by presenting as a different
client** — not by a token exchange Anthropic offers third parties. It breaks silently when
enforcement changes, and it's ToS gray area.

The uncontroversial flows carry none of that: **GitHub Copilot** (device code — Crush and pi
both ship it), Google, OpenRouter, Qwen. Crush's Copilot implementation adds a nice touch —
it reads the existing Copilot CLI/VS Code token off disk so a user already logged in
elsewhere never sees a login screen.

**Recommendation unchanged, now better evidenced:** build a `Credential` interface that
refreshes itself, ship API keys plus the uncontroversial flows, and make Anthropic
subscription login a separate, explicitly-flagged decision rather than an accident.

Two mechanics worth copying from Crush regardless:

- **Proactive refresh** at `max(expires_in/10, 30s)` before expiry, so a token never dies
  mid-request; plus explicit revoked-refresh-token detection (`"revoked"` / `"invalid_grant"`)
  so a dead token says "log in again" instead of retrying forever.
- **Embed the OAuth client config inside the stored token**, so a process restart can rebuild
  the refresh path without re-running discovery.

And from pi: **re-resolve credentials on every LLM call**, so a token expiring during a long
tool phase doesn't kill the turn.

**Storage — reconsider `go-keyring`.** All four projects use plaintext `0600` files and none
use an OS keyring. Crush's answer is better than plain files though: API keys support full
shell expansion including `$(command)`, so `"api_key": "$(op read op://vault/anthropic/key)"`
works with any secret manager and we build nothing. That's probably the right default, with a
keyring as an optional backend later.

---

## Two warnings from their weaknesses

**pi's `agent-session.ts` is 3,342 lines.** The rest of that codebase is carefully layered —
and then everything collapses into one file at the point where the product actually gets
assembled: compaction orchestration, session switching, bash, extensions, model management,
events. That is the file we will be most tempted to write. Watch for it.

**neo's bus factor is one, and its API has already churned** (`permissions` → `tool_approvals`,
`--permission` removed). Not a criticism of a young project — a reminder that config surface
is a commitment, and worth keeping small at first.

---

## Resolved by Crush

**1. Streaming markdown — solved, and better than either option I proposed.** Not
"re-render everything per token" (O(n²)) and not "plain text then final pass" (loses live
formatting). Crush finds the longest prefix it can *prove* has no markdown construct left
open — after a blank line, no open fence, list, table, quote or setext header — renders that
once, caches it, and re-renders only the small unstable tail each frame.

```
│ Here's the fix:                    │
│                                    │  ← stable prefix: rendered ONCE, cached
│ ```go                              │
│ func Check(ctx context.Context)    │
│ ```                                │
├────────────────────────────────────┤  ← provably-safe boundary
│ Now let me also upda               │  ← unstable tail: re-rendered each frame
```

The subtlety their comment names: *"Two renders concatenated are NOT generally equal to a
single render of the whole document — glamour's wrap state is reset between calls."* So the
boundary check is deliberately conservative and falls back to a full render whenever unsure.

Two gotchas we'd otherwise hit: Glamour's `TermRenderer` is expensive to construct (memoize
per width), and **it is not reentrant** (guard with a mutex).

**2. Block caching — solved.** Two independent layers. Each item caches its own rendered
string keyed by width; the list is virtualized so only visible items render at all; and the
list additionally memoizes by `(item, width, version)` with a `Finished()` freeze flag that
returns finished items verbatim forever without calling `Render()`.

> The two independent Crush readings disagreed slightly on whether a list-*level* cache
> exists (their own `AGENTS.md` says items should cache internally). The per-item cache is the
> one to build first — it's simple and gets most of the win.

**3. The real answer to "don't redraw per token" is upstream of the UI entirely.** Crush
debounces at the *persistence* layer: streaming deltas within a 33ms window coalesce into one
DB write and one pubsub event. The UI never sees a per-token message. Throttling lives where
the data is, not scattered through the view.

## Still open for the PRD

1. **Tool surface for v1.** pi ships 7 (`read`, `bash`, `edit`, `write`, `grep`, `find`,
   `ls`), neo 8, Crush ~30. Leaning to pi's set plus a todo tool.
2. **Model catalog.** pi generates one from `models.dev`; **Crush fetches `catwalk`**, Charm's
   own hosted equivalent, with ETag caching; neo hardcodes defaults and live-fetches only for
   OpenRouter. Both mature projects fetch rather than hardcode. Probably: hardcoded defaults
   plus user-defined models in config, with a fetched catalog as a later enhancement.
3. **Parallel tools — how aggressive?** neo runs 8 concurrently; pi is parallel by default;
   **Crush marks only the sub-agent tool parallel** and runs everything else sequentially.
   Crush's conservatism is attractive for v1: sequential is easy to reason about, and parallel
   file reads are rarely the bottleneck.
4. **Credential storage.** All three use plaintext `0600` files, none use an OS keyring. Crush
   adds shell expansion including `$(command)`, so a user can point at 1Password without us
   building keyring support. That may be the better middle path than `go-keyring`.

---

## Model routing: everyone shipped it first, and the cache makes the naive version backwards

Added after the rest of this document, when *"auto mode — pick the model from task
difficulty"* came up as a candidate differentiator. It isn't one. The reasons are worth
keeping, because the idea is attractive enough to come back.

**It already ships, twice, in the shape we would have built.**
[OpenRouter's auto router](https://openrouter.ai/docs/guides/routing/routers/auto-router)
picks a model per request from prompt complexity and accepts `allowed_models` wildcards like
`anthropic/*` — which is "auto within one provider", already built, no markup, with session
stickiness and cost tiers thrown in. [Cursor's Auto mode](https://cursor.com/changelog/router)
is a router trained on 600,000+ live requests. Claude Code ships the deterministic version:
`opusplan` runs Opus in plan mode and Sonnet for execution — routing derived from a state the
user already set, not guessed.

The trained routers are the ones to learn from, because **the training data is the moat**.
[RouteLLM](https://www.lmsys.org/blog/2024-07-01-routellm/) is explicit that classifier
accuracy is capped by labelled examples from your own workload. We have none and will have
none. A router without that data is a coin flip wearing a feature's clothes.

**Prompt caching makes routing *down* cost more.** A model switch invalidates the cache
completely — tools, system and messages — because cache entries are model-scoped. A rasp turn
is not one request but one per step (design §4), each resending the transcript, so steps after
the first are cache reads at a fraction of base price. Switching mid-turn throws that away and
pays full price on a cold prefix, plus a write premium to re-cache. For a mid-sized turn the
"cheaper" model can cost several times more for the step it lands on, and the cold prefill is
a direct attack on the sub-second first token in prd §8. Check current multipliers against
provider pricing before relying on the arithmetic; the direction of the result is stable, the
magnitudes are not.

This also cuts against work already done: design §3.3 keeps the tool list byte-stable
precisely to protect that prefix. An auto-router that changes model mid-session discards it
anyway. The two features fight.

**models.dev has no capability field.** Entries carry cost, limits, modalities and flags —
nothing ranking models by how good they are. So "pick the best model for a hard task" is
unanswerable from the catalog, and rasp would ship its own hand-maintained tier table. That
reintroduces exactly what design §10.2 rejects in its first sentence: *"every new model release
needs a rasp release, which is the wrong coupling for a tool whose entire pitch is being
model-agnostic."*

**`auto` is already a permission mode.** design §7.2 defines it as allow-edits with
allow-listed bash, Shift+Tab cycles into it, and it sits at the far left of the status line
because knowing your permission state is safety-relevant. The status line also shows the active
model (prd §6.3), so a model selector named `auto` puts the word twice on one line meaning two
unrelated things — and invites a user to read "auto" as routing when it actually means rasp may
edit files without asking.

**pi settles it.** pi ships a provider called `radius` — a remote gateway, OAuth or
`RADIUS_API_KEY`, described in its own source as *"purely dynamic … no static catalog entry"* —
whose default model id is `"auto"` (`model-resolver.ts:27`, in the same table as
`anthropic: "claude-opus-4-8"`). So pi's `auto` is a string it forwards to somebody else's
router. Grepping its source for difficulty, complexity or classification turns up only
`tools/read.ts` classifying *files* as skill or docs. **pi implements no routing whatsoever**,
and has no cheap-model role either. The agent closest to rasp in intent looked at this problem
and forwarded it upstream.

**The decision.** `/model` is the entire user-facing story — the user picks, from whatever
provider they authenticated with. No auto mode, now or later. Four supporting pieces:

1. **Never validate a model id against the catalog.** Forward whatever string is configured.
   This is what makes `openrouter/auto`, pi's `radius/auto`, and every router not yet invented
   work for free — rasp does not need to know routers exist. A catalog miss degrades the
   context and cost display to conservative estimates; it must never block a request. Same
   anti-coupling argument as §10.2, one level up.
2. **A default model per provider**, so "I don't want to choose" needs no mechanism. pi does
   this in one line per provider.
3. **One internal config key for the cheap model** used by compaction and session titles.
   internals §6.2 already commits to this, and design §11 gives the compaction call its own
   session with no cache breakpoints — so it cannot fight the cache. It is not routing and not
   user-facing: it is refusing to run a flagship model on a summarization job. A `plan` role was
   considered and dropped, because it is the one that would switch the *main* conversation's
   model, and `/model` covers it.
4. **Surface `cache_read_input_tokens` in the status line.** design §8.4 already calls it worth
   watching. A user who can see the cache go cold when they switch will route better than any
   classifier we could ship, and it costs one integer.

If real routing ever earns its place, sub-agents are the home: the child has its own context
window, so a cheap model there costs nothing in cache terms.

The failure mode that decides it: a cheap model on a hard task returns a confident wrong
answer, and the user has no way to see that the routing caused it. That is the same shape as
the fuzzy edit matching this project already refuses — silent, plausible, wrong.
