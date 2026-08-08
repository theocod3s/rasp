# rasp — MVP and future scope

Companion to [research/findings.md](research/findings.md). This document draws the line
around v1: what ships, what waits, and what we are choosing never to build.

**Guiding rule:** the MVP is the smallest thing that is genuinely useful *daily*. Not a demo.
If a feature doesn't make the tool better to actually use on a real codebase, it waits.

---

## MVP

### Platform

| | |
|---|---|
| Language | Go 1.26 |
| Distribution | Single static binary, `CGO_ENABLED=0`, cross-compiled via goreleaser |
| Topology | Single process. Agent core is a UI-agnostic package emitting typed events |
| Frontends | TUI (primary) and `rasp run -p "..."` headless (falls out of the seam for free) |

### Auth: API keys only

- Config format is **JSONC** — JSON with `//` comments stripped before parsing, matching Crush
  and opencode. A config you hand-edit needs comments; plain JSON can't have them.
- Resolution order: config file → environment variable.
- Config values support **shell expansion**, including `$(command)` — so
  `"api_key": "$(op read op://vault/anthropic/key)"` works with any secret manager and we
  build no keyring integration. (Copied from Crush; all four reference projects store
  credentials as plaintext `0600` files and none use an OS keyring.)
- The credential layer is built behind a `Credential` interface that can refresh itself, and
  is **re-resolved on every LLM call** — so OAuth slots in later without touching the loop.

### Providers: two adapters

- **Native Anthropic** via `anthropics/anthropic-sdk-go` — prompt caching, thinking blocks,
  correct `tool_use` semantics.
- **OpenAI-compatible** with a swappable base URL — covers OpenRouter, Groq, DeepSeek, xAI,
  Mistral, Together, Ollama and LM Studio from one adapter. Built by wrapping the real OpenAI
  client with injectable hooks rather than reimplementing it (Crush's `openaicompat` pattern).

Model metadata — IDs, context windows, pricing, tool-call support — comes from the
**models.dev catalog**, fetched at runtime and cached with ETag revalidation. Hardcoding it
would mean a rasp release for every new model, which is the wrong coupling for a tool whose
pitch is being model-agnostic.

Fetching is off the startup path, bounded at 5s, and degrades through cached-fresh →
cached-stale → an embedded snapshot, so it can never delay or break a launch. User-defined
models in config sit **above** the catalog and always win, which is what makes depending on a
third-party file survivable: pi's own generator carries dozens of hand-written corrections to
models.dev data, so a wrong entry has to be locally fixable in one line.

### Streaming, from day one

Non-negotiable, and cheap only if designed in. Two contracts:

- Every stream event carries the **full accumulated message so far**, not just the delta. The
  UI re-renders state; it never reassembles fragments.
- The provider stream function **never returns an error** for model or request failures —
  they arrive as a final message with `stop_reason: error`. One error path, not two.

### The eight tools

A small, deliberate surface. pi is a genuinely capable agent with seven of these; `todos` is
the one addition, because it visibly improves long multi-step work by making the model's plan
inspectable before it burns ten minutes on a wrong approach.

| Tool | Behavior |
|---|---|
| `read` | Read a file whole, or an offset/limit line window. Size-capped |
| `write` | Create or overwrite. Creates parent dirs. Atomic write, preserves mode |
| `edit` | Exact-string replace with the four-rung fallback ladder (below) |
| `bash` | Run a command with a timeout, process-group kill, bounded output |
| `grep` | Content search. Shell out to `rg` when present, pure-Go fallback |
| `find` | Filename/glob search (`doublestar`), gitignore-aware |
| `ls` | Directory listing |
| `todos` | Model-maintained checklist. Touches no files, runs nothing — it exists so a multi-step plan is visible and correctable |

The constraint is the *surface*, not the number. Users extend the set through MCP, not by us
adding built-ins.

### Tool execution: parallel by default

pi's model. Tools run concurrently unless a tool opts out, and safety comes from four
mechanisms that must exist together:

- **Per-tool opt-out.** A tool may declare itself sequential; if any call in a batch is
  sequential, the whole batch runs sequentially.
- **Per-file mutation mutex**, keyed by `filepath.EvalSymlinks` — same-file writes serialize,
  different files stay parallel.
- **Result reordering.** Tools finish in arbitrary order; `tool_result` blocks are emitted in
  the order the model requested them, because providers require that pairing.
- **Concurrency cap of 8**, semaphore-bounded.
- **Approval is a serial barrier.** A call needing permission splits the batch, so two
  approval prompts never race.

**The edit ladder** (Crush's design, the best of the four references):

1. Exact string match. More than one occurrence without `replace_all` → hard error asking for
   more context. Never "first match wins."
2. Zero matches → retry with whitespace normalized, accepting **whole-line-aligned matches
   only**, never partial lines.
3. On a normalized match, re-indent the replacement to the file's detected indent unit (tabs
   vs N-space), and **tell the model in the result** that the match wasn't byte-exact.
4. Still nothing → run a line-similarity scan, print the file's actual content at the closest
   location with whitespace visualized, so the model can self-correct.

No Levenshtein, no approximate character matching. "Fuzzy" means whitespace only.

### TUI

Chat-first. One scrolling column, input pinned at the bottom. No panes.

- Streaming markdown via **stable-prefix incremental rendering** — prove the longest prefix
  with no open markdown construct, cache its render, re-render only the unstable tail.
  Memoize Glamour's `TermRenderer` per width; guard it with a mutex (it is not reentrant).
- **Per-item render cache keyed by width**, with a finished-item freeze flag.
- **Colored unified diffs** for every edit — `go-udiff` to compute, Lip Gloss to style,
  line-level color. Syntax highlighting and side-by-side are future.
- Collapsible tool-call cards. Tools return *data*; the TUI owns presentation.
- Status line: model, context used, token counts, cost.
- Permission prompt overlay.
- Esc interrupts the turn (two-stage: arm, then confirm). Ctrl-C quits.
- Slash commands: `/model`, `/new`, `/resume`, `/compact`, `/clear`, `/help`, `/quit`.

Event delivery: agent core → typed events → one goroutine calling `tea.Program.Send`.
Persistence-layer debounce (~33ms) so streaming never floods the UI.

### Sessions

- Append-only **JSONL**, one file per session, written atomically (temp + rename).
- Every entry carries `id` and `parent_id` from the start. We won't ship branching in v1, but
  the field costs nothing now and makes `/fork` possible later without a migration.
- Model and mode changes recorded as first-class entries, so replay reproduces which model and
  which mode produced which turn.
- Sharded by `<project-key>` — the repo's first-commit hash, so the same repo maps to the same
  bucket wherever it's checked out.
- `rasp --resume` and a session picker. Resuming restores the session's mode and announces it.
- **No session index, and that isn't a deferral.** Sharding by project means listing is bounded
  by sessions-per-repo — tens, not thousands — and no view enumerates them globally. The
  alternatives both carry documented costs: neo's sidecar `index.json` admits in its own source
  that *"concurrent neo processes can lose index updates"*, and Crush's SQLite forced
  `SetMaxOpenConns(1)` after WAL desync. JSONL stays the sole source of truth; an index is
  purely additive later if the picker ever exceeds ~50ms.

### Context management

- `AGENTS.md` discovery walking cwd → repo root, outermost first. Also read `CLAUDE.md`.
  Handle the git-worktree double-load case.
- LLM-driven **compaction**, never truncation. Cut points never separate a `tool_use` from
  its `tool_result`. Carry read/modified file lists across the boundary.
- Hybrid token estimation: real usage from the last assistant message, `chars/4` for the tail.
- Prompt caching: system prompt split into a stable cacheable block and an uncached dynamic
  tail, with volatile content after the last breakpoint.

### MCP — stdio only

The escape valve that makes eight built-in tools acceptable. A static Go binary can't load user
code, so MCP is the only way anyone extends rasp without recompiling it — which is why it's in
the MVP rather than deferred.

- **stdio transport only**, via the official `modelcontextprotocol/go-sdk`, pinned.
- Servers discovered from `./.mcp.json` and rasp's own config. Other products' files are read
  **once**, by the first-run import, then never again.
- Tools merge into the same registry, namespaced `mcp__<server>__<tool>`, and pass through the
  identical permission gate. A third-party server gets no more freedom than our own `bash`.
- Guardrails: a tool-count budget with per-server allow-list (some servers expose 40+ tools,
  each costing context on every request), a hard connect timeout, and failures surfaced as
  ordinary tool errors.
- MCP tools default to **sequential** — we audited our eight for concurrency safety and can't
  audit someone else's server.
- Every MCP concept stays sealed inside `internal/mcp/`. The spec broke twice in eight months;
  containment is what makes a revision a dependency bump rather than a rewrite.

### Four modes

`plan`, `manual` (default), `auto`, `yolo`. Three are **permission presets** over the gate
above — no mode-specific branch anywhere in the agent loop, which is what makes them about a
day's work. `yolo` is the deliberate exception: a short-circuit checked *before* the ladder,
unreachable from the Shift+Tab cycle, requiring `--yolo` or `/yolo`, and never surviving a
restart.

Plan mode's `bash` uses a curated allow-list including search and read-only VCS commands, with
unlisted commands asking rather than failing, and shell redirection denied outright. It is a
strong speed bump, not a proof — `bash -c "..."` defeats it, and the docs say so.

### First-run import

One prompt, once. Scans for existing configuration from Claude Desktop, Claude Code, Codex,
opencode, Crush and pi; shows exactly what it found; copies everything on `Y` — MCP servers,
model preferences and API keys alike. No separate opt-in for keys: they're already plaintext on
the same machine at the same permissions, so a second prompt would be friction with no security
gain. Afterwards rasp reads only its own config.

### Safety net

Framed honestly as a **blast-radius limiter, not a security boundary**.

- Every file tool confined to the workspace via Go 1.24's `os.Root` — symlink escapes
  rejected by the runtime, not by our own path arithmetic.
- Approval gate on writes and bash, with a config allow-list, session-scoped grants keyed by
  `(tool, action, path)`, and an explicit `--yolo`.
- Read-before-edit: refuse to edit a file this session hasn't read, or one whose mtime is
  newer than the last read.

### Correctness invariants

These are cheap now and very expensive to retrofit. Each one is a known bug class.

1. **`tool_use` never exists without `tool_result`** — both mechanisms. Commit the assistant
   message and its results together *and* repair on history reload (inject synthetic error
   results for orphaned calls, drop orphaned results). An orphan reaching disk permanently
   bricks a session.
2. **Truncated-tool-call guard** — if `stop_reason == "length"`, fail every tool call in that
   message rather than executing possibly-truncated arguments.
3. **Loop detection** — hash `(tool, input, output)` per step; halt if the same signature
   repeats more than 5 times in the last 10.
4. **Panic recovery per tool** — a panicking tool returns a failed result, never crashes.
5. **Per-file mutation mutex** keyed by resolved realpath (`filepath.EvalSymlinks`), so
   `./a.go`, `a.go` and a symlink to it all take the same lock.
6. **Result ordering** — parallel tools complete in arbitrary order, but `tool_result` blocks
   are emitted in the order the model requested them. Providers reject a mismatch.
7. **Process-group kill** for bash, with `cmd.Cancel` overridden and `WaitDelay` set.
8. **Two-tier retry** — transport (honors `retry-after`, jitter, throws rather than sleeps on
   absurd delays) and semantic (never retry quota/billing exhaustion).

### Testing

- Fake provider (hand-written SSE frames) for fast loop unit tests.
- `go-vcr` cassettes for a few real recorded streaming tool-use turns. Scrub `x-api-key`.
- Golden files for rendered `View()` output.
- Fuzz the edit-matching logic.

---

## Future scope

Ordered roughly by expected value.

### Phase 2 — the obvious gaps

- **OAuth.** `rasp auth login <provider>`, browser PKCE, local callback server, proactive
  refresh at `max(expires_in/10, 30s)`, revoked-token detection. Start with GitHub Copilot
  (device code, and we can read an existing Copilot CLI token off disk). *Anthropic
  subscription OAuth is a separate, explicitly-flagged decision — see findings.md.*
- **More tools, Crush-style.** `multiedit`, `web_fetch`, `web_search`, `download`,
  `question` (ask the user mid-turn), `job_output`/`job_kill` for background bash.
- **Sub-agents.** A `task` tool spawning a child session with a restricted read-only tool set
  and its own cost accounting rolling up to the parent. Copy neo's mode-based restriction and
  hard caps.
- **Session branching.** `/fork` and `/tree` over the `parent_id` field we already store.
- **MCP beyond stdio** — HTTP and Streamable-HTTP transports, OAuth-authenticated servers, and
  MCP resources and prompts. stdio ships in the MVP; these are the expensive parts.

### Phase 3 — ecosystem

- **LSP** — diagnostics, then `lsp_definition` / `lsp_symbols` / `lsp_rename` as tools.
- **Hooks** — `PreToolUse` shell commands, regex-matched on tool name.
- **Skills** — the Agent Skills `SKILL.md` standard, advertised by name/description with the
  model reading files on demand.

### Phase 3.5 — cross-session agent messaging

**Explicitly not MVP.** Independent rasp sessions — separate processes, separate repos,
separate terminals — able to discover and message each other. One agent working in the backend
asks the one that has the frontend loaded, rather than re-reading it. A long-running session
you can hand work to from another window.

This is architecturally cheaper than it sounds, because the MVP already builds most of it:

| Needed | Already exists |
|---|---|
| Somewhere to deliver a message | The **steering and follow-up queues** (design.md §6 rule 8). An inbound message is just another producer for a queue built for mid-turn user input |
| Stable addressing | Session IDs, already written to every JSONL entry |
| Adding the tools | The registry accepts dynamically-registered tools — MCP proves it |
| Not disturbing the loop | The loop never assumed it was the only consumer of those queues |

What genuinely has to be built: **discovery** (a directory of per-session socket files, with
staleness detection for sessions that died without cleaning up), **transport** (a Unix domain
socket per session; named pipes on Windows), and two tools — `list_agents` and `send_message`.

**The security question is the hard part, and it's the reason this isn't a small feature.**
A message from another agent is *untrusted input*, indistinguishable from a prompt-injection
payload. If agent A can send text that agent B acts on, A has effectively gained a lever on B's
tools.

Claude Code shipped this feature ([docs](https://code.claude.com/docs/en/cross-session-messaging))
and arrived at the same architecture — per-session Unix socket, discovery via files on disk,
`ListAgents`/`SendMessage`, delivery between tool calls mid-turn or a fresh turn when idle. That
independent convergence is worth something. More usefully, they worked out operational detail we
should adopt rather than rediscover:

- **Inbound control is a separate axis from permissions.** `accept` / `hold` / `refuse`, decided
  before the message ever reaches the model. Held messages get an approval dialog that expires
  and then drops. "Should this arrive at all" and "what may it cause" are different questions.
- **The default is computed from *both* sessions' modes, asymmetrically.** A session that
  bypasses prompts *holds* messages from an ordinary session, and accepts only from another
  bypassing one. Non-obvious and correct: a yolo-mode session is precisely the one that must not
  silently take instruction from elsewhere, because it will act without asking.
- **Block permission laundering in the sending direction too.** A sender must never ask another
  session to perform something its *own* gate refused. The obvious threat is A→B injection; this
  is A being denied and routing the work through B, and it's just as real.
- **Throttle loops.** Rate-limit per sender, drop identical repeats inside a window, cap pending
  messages. Two agents politely replying to each other forever is a genuine failure mode that
  ends only if you design for it.
- **A capability floor, stated as rules rather than left to judgement.** A message can never
  count as user consent for a permission prompt, never change configuration, and a slash command
  in its text arrives as plain text and is never executed.
- Socket restricted to the OS user, so other users on a shared machine can't reach it.

Two limitations to inherit knowingly: filesystem-scoped discovery means a session in a container
and one on the host cannot see each other, and native Windows has no Unix-socket equivalent
(named pipes would need separate work — Claude Code simply doesn't support it there).

Deferred deliberately: it multiplies the surface of both the concurrency model and the trust
model, and neither is worth destabilising before the single-session path is proven.

### Phase 4 — polish

- Side-by-side diffs, intra-line word highlighting.
- Themes, configurable keybindings.
- File-version history for undo/checkpoint.
- Optional OS keyring backend.

---

## Deliberately excluded

Not "later" — decisions not to build, with reasons.

| | Why |
|---|---|
| **Client/server split** | Crush spends ~15,000 LOC on it (`server`/`client`/`proto`/`backend` + Swagger) so multiple clients can attach to one daemon. opencode goes further with three front-ends. That solves *their* problem. We keep the clean core/UI seam — which is the part that actually pays — and skip the protocol. Additive later if ever needed |
| **Multi-pane workspace** | Panes cost horizontal width, focus management, per-pane scroll state and resize handling — a large fraction of total TUI effort. No serious coding agent is multi-pane; a status sidebar later is cheap and additive |
| **Agent framework** (langchaingo etc.) | The loop, tool dispatch and context management *are* what this project exists to understand |
| **Scripted config format** | Crush maintains JSON *and* a Bash-interpreter config DSL (2,412 LOC). One JSONC file is enough |
| **Tree-sitter** | Needs cgo-linked grammars, which breaks `CGO_ENABLED=0`. Claude Code itself uses ripgrep plus the model's own understanding |
| **Tools owning their own UI rendering** | pi does this and flags it as a weakness — the tools become unusable by any other frontend. Tools return data; the TUI renders it |
| **Multiple SQLite drivers** | Crush ships two, selected per platform, to cover OpenBSD/NetBSD/Android. One (`modernc.org/sqlite`) when we need SQLite at all |
| **Sandboxing** | Real isolation needs the OS or a VM. A partial in-process sandbox is worse than none because users mistake it for a boundary — pi's argument, and it's correct. We ship a blast-radius limiter and say so plainly |
