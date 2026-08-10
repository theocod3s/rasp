# rasp — Product Requirements

**Status:** draft, pre-implementation
**Companions:** [scope.md](scope.md) (what ships when) · [design.md](design.md) (how it's built) · [findings.md](findings.md) (evidence)

This document is the *what* and *why*. It deliberately contains no package layout, no Go
interfaces, and no implementation strategy — those live in [design.md](design.md).

---

## 1. Summary

**rasp is a coding agent for the terminal.** You run `rasp` in a project directory, describe
what you want in plain language, and it reads your files, edits them, runs your tests, and
shows you every change as a colored diff before moving on. It ships as a single static binary
with no runtime dependencies, works with any model you can get an API key for — Anthropic
directly, or anything OpenAI-compatible including OpenRouter, Groq, DeepSeek and local Ollama
— and streams its output token by token so you can read along and interrupt the moment it goes
somewhere you didn't intend.

It runs in one of four modes — **plan**, **manual**, **auto** or **yolo** — which control how
much it can do without asking, and it picks up any **MCP servers** you already have configured,
so the tools you use elsewhere work here too.

---

## 2. Problem and motivation

Two motivations, both real, neither subordinate to the other.

**Understanding.** Coding agents went from novelty to daily tool in about eighteen months, and
the interesting parts are not the model. They are the agent loop, tool dispatch, context
compaction, and the terminal UI that makes a stream of tokens feel like a conversation. Those
are ordinary engineering problems with non-obvious solutions, and the fastest way to
understand them is to build one. Reading five agent codebases at source level (synthesized in
[findings.md](findings.md)) established *what* the solutions look like; writing one establishes
*why* each is shaped the way it is.

**Daily use.** A learning exercise that gets abandoned at the demo stage teaches you the easy
half. The parts that only surface under real use — a cancelled turn that bricks a session, a
diff you can't read at 80 columns, an edit that silently lands in the wrong place — are
exactly the parts worth learning. So rasp has to be good enough to actually reach for. The
forcing function is simple: if the author still opens Claude Code for real work, rasp isn't
done.

These pull in the same direction more often than not, but where they conflict, **understanding
wins on the core and pragmatism wins at the edges.** The agent loop, tool system, and context
management get hand-written because that's the point. Terminal rendering, diff computation and
syntax highlighting use libraries, because reimplementing Glamour teaches nothing about agents.

**Why not just use an existing one?** For daily work you could. But every mature option is
either not Go (pi, opencode, aider), or large enough that reading it is a project in itself
(Crush is 145k lines). neo is the right size to learn from at ~25k lines, and its gaps are
instructive — it has no token streaming and no diff view at all, and its own analysis names
both as the biggest problems with it.

---

## 3. Goals and non-goals

### Goals

| | |
|---|---|
| **G1** | A terminal coding agent the author uses in preference to Claude Code for a full working day |
| **G2** | An agent loop, tool system and context manager written from scratch and understood line by line |
| **G3** | Model-agnostic — Anthropic natively, plus any OpenAI-compatible endpoint, switchable mid-session |
| **G4** | One static binary, no runtime, no install ceremony. `CGO_ENABLED=0`, cross-compiled |
| **G5** | A TUI good enough that the terminal stops feeling like a constraint — streaming, readable diffs, no flicker |
| **G6** | Correct under interruption. Cancel, crash or kill at any point and the session still works afterward |
| **G7** | Extensible without recompiling — MCP servers the user already has configured work unchanged |

### Non-goals

Drawn from [scope.md](scope.md#deliberately-excluded). These are decisions, not backlog items.

| Not building | Why |
|---|---|
| **Client/server split** | Crush spends ~15k LOC on `server`/`client`/`proto`/`backend` + Swagger so multiple clients can attach to one daemon; opencode runs three front-ends on one core. That solves *their* problem. We keep the seam that pays — a core that knows nothing about terminals — and skip the protocol |
| **Multi-pane workspace** | Panes cost horizontal width, focus management, per-pane scroll state and resize handling. No serious coding agent is multi-pane. A status sidebar later is cheap and additive |
| **An agent framework** | The loop, tool dispatch and context management *are* the thing being learned. Wrapping them in someone else's abstraction defeats the project |
| **A scripted config DSL** | Crush maintains JSON *and* a Bash-interpreter config format (2,412 LOC). One JSON file is enough |
| **Tree-sitter** | Requires cgo-linked grammars, which breaks the static-binary goal. Claude Code itself relies on ripgrep plus the model's own understanding |
| **OS-level sandboxing** | Real isolation needs a VM or container. A partial in-process sandbox is *worse* than none because users mistake it for a boundary — pi's argument, and it's correct |
| **Sub-agents in v1** | A genuine capability, but it multiplies the concurrency and context surface before the single-agent path is solid. Phase 2 |
| **MCP beyond stdio** | HTTP and SSE transports, OAuth-authenticated servers, and MCP resources and prompts are all phase 2. Crush spends a 783-line handler on MCP OAuth alone — PKCE, dynamic client registration and a pool of localhost callback ports. stdio covers the servers people actually run locally, and it's the cheap 80% |
| **Windows as a first-class target** | Best-effort. Process-group kill, pty behavior and terminal quirks all differ; verifying them is a project of its own |

**A note on MCP.** pi deliberately ships none and argues against the protocol, offering a
TypeScript extension API instead. That reasoning doesn't transfer: pi can `import()` a user's
module at runtime, and a static Go binary cannot. **MCP is our plugin story** — it's the only
way a user extends rasp without recompiling it. That's why it moved into the MVP rather than
staying in phase 3.

---

## 4. Target user

**Primary: the author.** A developer who lives in the terminal, is comfortable in Go, and
currently uses Claude Code daily. Already has an Anthropic API key. Wants to understand how
the tool works, and wants the tool.

**Secondary: terminal-native developers** who are already using Claude Code, opencode, Crush
or aider and are dissatisfied with something specific about it.

What that group already has, and therefore what rasp is measured against:

| | What they'd miss switching to rasp v1 |
|---|---|
| **Claude Code** | Sub-agents, hooks, skills, web search, a very tuned system prompt |
| **opencode** | Model breadth via models.dev, LSP diagnostics, editor integration |
| **Crush** | ~30 built-in tools, LSP, sessions in SQLite, a mature TUI with themes |
| **aider** | Git-native workflow, automatic commits, repo map |

**What would make someone switch:** a single binary with no Node or Python runtime; genuinely
model-agnostic without an aggregator account; a readable, honest diff on every edit; the MCP
servers they already have configured working unchanged; and a codebase small enough to modify
yourself. **What would stop them:** no sub-agents, no LSP, and a smaller built-in tool set.
Those are known and accepted for v1 — this is not a bid to displace Claude Code for other
people, it's a bid to be good enough to displace it for one person, with an architecture that
could grow.

---

## 5. Product principles

Eight principles, each drawn from something the research settled. They exist to resolve future
arguments without relitigating the evidence.

**P1. Stream everything. A blocking agent feels broken.**
neo makes every provider call blocking — even its SSE client buffers the whole response before
parsing — and its own analysis calls this the project's biggest UX gap. Eight seconds of
nothing is indistinguishable from a hang. Streaming is also the single hardest thing to
retrofit, because it changes the provider interface, the event model and the render strategy
at once.

**P2. Fail loudly. Never silently mis-apply.**
An edit that lands in the wrong place is worse than an edit that fails. Zero matches is a
clean error the model can recover from; a fuzzy match against the wrong location corrupts a
file quietly. Crush's edit ladder normalizes whitespace but has *no* Levenshtein or
approximate character matching anywhere, and that's the right line.

**P3. Tools return data. The UI decides how to draw it.**
pi's tools return terminal components directly, and pi's own analysis flags it as a weakness —
the tools become unusable by any other frontend. Keeping tool logic and tool presentation in
different packages is also what makes headless `rasp run -p` free rather than a second
implementation.

**P4. The safety net is a blast-radius limiter, and we say so.**
Workspace confinement and an approval gate catch the ordinary failure — a confused relative
path, a bad glob, an `rm` in the wrong directory. They do not defend against a hostile model,
and the documentation will say that plainly. Implying more safety than you deliver is the
actual harm.

**P5. Correctness invariants get built in on day one, not bolted on.**
An orphaned `tool_use` block reaching disk permanently bricks a session — every subsequent
request fails. Truncated tool arguments can parse *and* validate while being semantically
wrong. These are cheap to prevent by construction and expensive to retrofit, so they're
requirements, not polish.

**P6. One core, thin frontends.**
The agent core emits typed events and knows nothing about terminals. Both Crush and opencode
demonstrate the payoff: opencode replaced its entire TUI, in a different language, without
touching the server contract. We don't want their protocol, but we want the seam.

**P7. Small surface, held deliberately.**
Eight built-in tools, one config file, a handful of slash commands. pi is a genuinely capable
agent with seven; we add `todos` because a visible plan is worth its weight on long tasks. The
constraint is the *surface*, not a magic number — but every addition needs to argue for itself.
neo's config surface has already churned once (`permissions` → `tool_approvals`) and it's a
young project. Every knob is a compatibility commitment. MCP is the pressure valve — users add
tools without us adding surface.

**P8. Modes are permission presets, not code paths.**
opencode's `plan` and `build` are registered identically and differ *only* in their default
permission map. There is no `if planMode` anywhere in their agent loop. Any future mode must
be expressible as a permission preset, or it isn't a mode — it's a feature wearing a mode's
clothes, and it needs its own justification.

The one sanctioned exception is `yolo`, which bypasses the gate rather than configuring it
(§6.7). That's a single early return, and making it *look* like a preset would be worse — a
matcher configuration that silently approves everything is precisely the kind of thing that
gets reached by accident.

---

## 6. Functional requirements

MUST = v1 blocker. SHOULD = v1 target, droppable under pressure. WON'T = explicitly out.

### 6.1 Providers and auth

- **MUST** support Anthropic natively, including streaming, tool use and prompt caching.
- **MUST** support any OpenAI-compatible endpoint via a configurable base URL — one adapter
  covering OpenRouter, Groq, DeepSeek, xAI, Mistral, Together, Ollama and LM Studio.
- **MUST** resolve API keys from the config file, then the environment.
- **MUST** support shell expansion in config values, including `$(command)`, so
  `"api_key": "$(op read op://vault/anthropic/key)"` works with any secret manager.
- **MUST** re-resolve credentials on every model call, so a future expiring token can't kill a
  turn mid-flight.
- **MUST** switch model and provider mid-session without restarting or losing history.
- **MUST** retry in two tiers: transport-level (honoring `retry-after`, with jitter) and
  semantic-level, which never retries quota or billing exhaustion.
- **SHOULD** report token counts and estimated cost per turn and per session.
- **MUST** source model metadata — IDs, context-window sizes, pricing, tool-call support — from
  the **models.dev catalog**, fetched at runtime and cached to disk with ETag revalidation,
  refreshed on an interval. Hardcoding it means every new model release needs a rasp release.
- **MUST** degrade gracefully when that fetch fails. Network error, timeout or malformed
  response falls back to the last cached copy, and failing that to a small embedded snapshot.
  Bound the request at 5s and run it **off the startup path** — a slow catalog must never delay
  the first frame, and must never be a startup error.
- **MUST** let user-defined models in config override the catalog, so an endpoint models.dev
  has never heard of still works.
- **WON'T** ship OAuth in v1. The credential layer is built behind a refreshable interface so
  it slots in later (see §10, R6).

### 6.2 Tools

- **MUST** ship eight **built-in** tools: `read`, `write`, `edit`, `bash`, `grep`, `find`,
  `ls`, `todos`. Users extend the set through MCP (§6.8), not by us adding more.
- **MUST** implement `todos` as a model-maintained checklist that touches no files and executes
  nothing. Its value is that a multi-step plan becomes visible and correctable before the model
  spends ten minutes on a wrong approach.
- **MUST** generate tool schemas from Go structs, so the schema the model sees and the type we
  unmarshal into cannot drift.
- **MUST** implement `edit` as a four-rung ladder: exact match → ambiguity rejection →
  whole-line whitespace-normalized match with re-indentation → diagnostic hint showing the
  closest near-miss. Multiple matches without `replace_all` is a hard error, never
  "first match wins."
- **MUST** tell the model when an edit matched non-exactly, so it can verify.
- **MUST** run `bash` with a timeout and kill the entire process group on cancel or timeout.
  `exec.CommandContext` alone kills only the direct child
  ([golang/go#21135](https://github.com/golang/go/issues/21135)).
- **MUST** bound all tool output, preserving both head and tail. The head has the command echo;
  the tail has the error.
- **MUST** surface tool failures to the model as tool results, not as transport errors — a
  failing test is information the model needs.
- **MUST** recover panics inside a tool and convert them to a failed result.
- **MUST** stream long-running `bash` output to the UI as it arrives, throttled.
- **MUST** run tool calls **in parallel by default**, reads and writes alike — pi's model. A
  tool may declare itself sequential, and if any call in a batch is sequential the whole batch
  runs sequentially.
- **MUST** serialize concurrent mutations to the same file behind a mutex keyed by
  `filepath.EvalSymlinks`, so `./a.go`, `a.go` and a symlink to it take the same lock while
  different files stay parallel.
- **MUST** emit `tool_result` blocks in the order the model requested them, regardless of the
  order tools actually finish. Providers reject a mismatch between `tool_use` and `tool_result`
  sequences.
- **MUST** bound concurrency at 8 simultaneous tool executions.
- **MUST** treat a call requiring permission approval as a serial barrier that splits the
  batch, so two approval prompts can never race for the user's attention.
- **SHOULD** use ripgrep for `grep` when present on `PATH`, with a pure-Go fallback.
- **WON'T** implement `multiedit`, `web_fetch`, `web_search` or a sub-agent tool in v1.

### 6.3 TUI

- **MUST** be chat-first: one scrolling conversation, input pinned at the bottom, no panes.
- **MUST** stream assistant text token by token, rendered as markdown, without flicker.
- **MUST** render a colored unified diff for every `edit` and `write`. neo shows the literal
  string `edit <path>` and nothing else; given that edits are the product, that is the single
  worst gap in the reference set.
- **MUST** show tool calls as inline cards with a one-line summary, expandable to full output.
- **MUST** show a status line with active model, context used, token counts and cost.
- **MUST** support interrupting a running turn with Esc, in two stages — arm, then confirm —
  so a stray keypress can't kill a long turn.
- **MUST** stay responsive with a long scrollback. Completed messages are never re-rendered.
- **SHOULD** support slash commands: `/model`, `/new`, `/resume`, `/compact`, `/clear`,
  `/help`, `/quit`.
- **SHOULD** degrade legibly at 80 columns.
- **WON'T** ship themes, configurable keybindings, mouse support or inline images in v1.

### 6.4 Sessions

- **MUST** persist every session as append-only JSONL, written atomically.
- **MUST** resume a previous session with full context.
- **MUST** record model changes as first-class entries, so replay reproduces which model
  produced which turn.
- **MUST** carry a parent-entry reference on every entry from v1, even though branching
  doesn't ship — it costs one field now and avoids a migration later.
- **MUST** shard sessions by `<project-key>` — the repo's first-commit hash, falling back to a
  hash of the absolute path outside a repo. The same repo maps to the same bucket wherever it's
  checked out.
- **SHOULD** provide a session picker listing recent sessions for the current project.
- **WON'T** use a database or a session index, and this isn't a deferral — the sharding removes
  the need. Listing is bounded by sessions-*per-project*, not sessions-ever; there is no view
  that enumerates them all. JSONL stays the sole source of truth. The cautionary evidence is
  concrete: neo built a separate `index.json` and its own source comments admit *"concurrent neo
  processes can lose index updates"* — an index needs coordination the append-only files don't.
  Crush went SQLite-from-the-start and had to force `SetMaxOpenConns(1)` after concurrent
  sub-agent sessions caused WAL desync (`SQLITE_NOTADB`). If the picker ever exceeds ~50ms, an
  index is purely additive with JSONL still authoritative.

### 6.5 Context management

- **MUST** discover and load `AGENTS.md`, walking from the working directory up to the repo
  root, outermost first. **MUST** also accept `CLAUDE.md`.
- **MUST** compact by LLM summarization, never by truncation, and never split a `tool_use`
  from its `tool_result`.
- **MUST** carry the list of files read and modified across a compaction boundary, so the agent
  doesn't forget what it already touched.
- **MUST** structure the system prompt so the cacheable prefix is byte-stable and all volatile
  content sits after the last cache breakpoint.
- **MUST** ship **one short system prompt for every model**, embedded and version-controlled.
  It is a tuning surface, and the golden edit corpus (S4) is its feedback signal.
- **WON'T** maintain per-model-family prompt variants in v1. opencode maintains six, selected
  by matching the model ID; that pays off when supporting every model on the market, and until
  a specific model demonstrably needs its own phrasing, N prompts is N things to keep in sync
  on instinct rather than evidence.
- **SHOULD** estimate tokens using real usage from the last assistant message plus a heuristic
  for the tail, rather than a flat character count.

### 6.6 Safety and permissions

- **MUST** confine every file tool to the workspace root, with symlink escapes rejected by the
  runtime rather than by our own path arithmetic.
- **MUST** gate `write`, `edit` and `bash` behind an approval prompt by default, with grants
  keyed by tool, action *and* path — "always allow writes in `/foo`" must not cover `/bar`.
- **MUST** apply the identical permission gate to MCP tools. No special treatment, no bypass.
- **MUST** scope session grants to memory only. They do not persist across restarts.
- **MUST** refuse to edit a file the session hasn't read, or one modified since it was read.
- **MUST** guarantee a `tool_use` never exists without a matching `tool_result` — both by
  committing them together and by repairing history on load.
- **MUST** refuse to execute tool calls from a response truncated by the output limit.
- **MUST** detect and halt runaway loops of identical tool calls.
- **WON'T** claim to be a security boundary. The README will say what it is and isn't.

### 6.7 Modes

Four modes ship in v1. **Three of them are permission presets over the gate in §6.6, not a
separate subsystem** — no mode-specific branch anywhere in the agent loop (P8). That's what
makes them roughly a day's work on top of the permission service.

| Mode | `edit` / `write` | `bash` | Permission gate | Intent |
|---|---|---|---|---|
| **plan** | deny | read-only patterns allowed, mutating denied | active | Explore and propose. Cannot modify anything |
| **manual** *(default)* | ask | ask | active | Confirm each mutating action |
| **auto** | allow | allow-listed, else ask | active | Stays out of the way, still gates unfamiliar commands |
| **yolo** | allow | allow | **skipped entirely** | No gate at all. For throwaway environments |

**`yolo` is the exception to P8, and deliberately so.** The other three change *what the gate
decides*; yolo bypasses the gate. Crush implements it as an `atomic.Bool` checked before every
other rung of the ladder, and that's the right shape — a single early return, not a preset
that happens to allow everything. Structuring it as a preset would be worse: it would mean the
matcher has a configuration that silently approves anything, which is exactly the kind of
thing that gets reached by accident.

The distinction from `auto` is real and worth preserving. `auto` still stops at an unfamiliar
`bash` command; `yolo` does not stop at anything.

- **MUST** default to `manual`.
- **MUST** implement `plan`, `manual` and `auto` purely as permission-map presets, resolved by
  the same matcher that handles user config.
- **MUST** implement `yolo` as a bypass checked *before* the permission ladder, not as a
  permissive preset within it.
- **MUST** resolve bash patterns most-specific-wins, so `find *: allow` and
  `find * -delete*: ask` compose correctly.
- **MUST** allow the user to override any preset in config. The presets are defaults, not
  policy.
- **MUST** cycle **plan → manual → auto** with Shift+Tab, and display the current mode in the
  status line at all times.
- **MUST NOT** make `yolo` reachable from the Shift+Tab cycle. It requires explicit opt-in —
  the `--yolo` flag at launch or an explicit `/yolo` command. Cycling accidentally into
  "run anything without asking" is unacceptable.
- **MUST** indicate `yolo` loudly and persistently while active — not a subtle status-line
  token. Crush restyles the editor prompt itself so the risk stays visible while you type;
  rasp does the same.
- **MUST** require an explicit confirmation when entering `yolo` via `/yolo` mid-session.
- **MUST** persist the active mode per session, restored on resume — **except `yolo`, which
  never persists.** A resumed session always comes back gated.
- **MUST** apply a mid-turn mode switch to the *next* tool call, never retroactively to calls
  already approved or in flight.
- **SHOULD** inject a short synthetic message on a mode switch so the model is told its
  constraints changed, rather than discovering it through a denial.

#### What `bash` may do in plan mode

Plan mode exists so the agent can explore and propose without changing anything. Files are
easy — `edit` and `write` are denied outright. `bash` is the hard case, because reading
`git log` is exactly what planning consists of while `rm -rf` obviously isn't, and `bash` is
one tool. So it's matched on the **command string**, with unlisted commands asking rather than
being denied — an incomplete list should degrade into a prompt, not a wall.

```
ALLOW (run silently)
  search      rg*  grep*  ag*  fd*  find *
  vcs read    git status*  git log*  git diff*  git show*
              git blame*  git branch  git remote -v
  inspect     ls*  cat*  head*  tail*  wc*  file *  stat*  tree*
  language    go list*  go doc*  go env*  go vet*
              npm ls*  cargo tree*  pip show*
  env         pwd  which*  echo*  env  date

ASK (more specific — overrides the broader allow above)
  find * -delete*   find * -exec*
  git checkout*  git reset*  git clean*  git push*  git stash*
  go test*  go build*          — they execute arbitrary code and write build artifacts
  sed -i*  perl -i*

DENY
  any command containing  >  >>  or  | tee

everything else → ask
```

- **MUST** deny shell redirection (`>`, `>>`, `| tee`) in plan mode regardless of the command,
  because redirection writes files without any write-looking command being involved.
- **MUST** treat `go test*` and `go build*` as `ask` rather than `allow` — one keypress, and
  you stay aware that plan mode just executed your test suite.

**The honest limitation, which the docs must state rather than imply away.** Glob-matching on
command strings is leaky. `echo "package main" > auth.go` matches `echo*`; `cat template.go >
handler.go` matches `cat*`. Denying redirection closes those, but `bash -c "..."` defeats the
whole scheme. **Plan mode is a strong speed bump, not a proof.** It reliably prevents accidents,
which is what it's for; it does not constrain a model that is actively trying to escape it.

pi is not a reference here — it ships no plan mode and no permission gating at all (*"No plan
mode. Write plans to files, or build it with extensions"* — their README). The references are
opencode and Claude Code.

### 6.8 MCP

Tightly scoped. stdio only. This is rasp's plugin story — a static binary can't load user code,
so MCP is the only way a user extends it without recompiling.

- **MUST** support stdio transport — spawn a subprocess, speak JSON-RPC over stdin/stdout —
  via the official `github.com/modelcontextprotocol/go-sdk`.
- **MUST** discover servers from `.mcp.json` in the project root and from rasp's own config.
- **MUST** merge MCP tools into the same registry the model sees, namespaced by server
  (`mcp__github__create_issue`), so nothing downstream special-cases them.
- **MUST** enforce a **tool-count budget with a per-server allow-list**. Some servers expose
  40+ tools; every one costs context on every request and measurably degrades tool selection.
  Exceeding the budget is a startup warning naming the offending server, not a silent
  truncation.
- **MUST** connect **lazily, on first use, with a hard timeout**. A dead or slow server MUST
  NOT block startup or hang a turn. On timeout, degrade to "server unavailable" and continue.
- **MUST** surface MCP tool failures as ordinary tool errors the model can read and react to —
  never as a crash, and never as a silent no-op.
- **MUST** offer a **first-run import** rather than reading other products' config files on
  every startup — see §6.9. After importing, rasp reads only its own config, so no other
  product's schema can break us later.
- **WON'T** support HTTP or SSE transports in v1.
- **WON'T** support OAuth-authenticated MCP servers in v1.
- **WON'T** support MCP resources or prompts in v1. Tools only.

### 6.9 First-run import

Most people arriving at rasp already have MCP servers and API keys configured somewhere else.
Making them redo that by hand is a bad first impression; reading other products' files forever
is a permanent coupling to schemas we don't control. A one-time import gets the benefit without
the liability.

- **MUST** scan, **on first run only**, for known configuration from Claude Desktop, Claude
  Code, Codex, opencode, Crush and pi.
- **MUST** show exactly what was found before copying anything, then ask once.
- **MUST** import everything found — MCP servers, model preferences **and API keys** — behind
  that single prompt. **WON'T** ask separately about keys: the key is already plaintext on the
  same machine, in a file owned by the same user at the same permissions, so copying it to a
  second such file doesn't change the threat model. A second prompt would be friction with no
  security gain. Showing what will be copied is transparency, not a gate.
- **MUST** stop consulting those files afterwards. rasp reads only its own config from then on.
- **MUST** never let a failed or malformed source file break startup — skip it silently.
- **SHOULD** mention that config supports `$(command)` expansion, so anyone who'd rather not
  duplicate a secret can replace the imported literal with `$(op read ...)`. That's their
  choice to make later, not a question to ask during setup.

### 6.10 Keeping the machine awake

A turn can run for minutes. If the laptop idle-sleeps partway through, the turn dies and the
user comes back to a broken session. Claude Code solves this with `caffeinate -i -t 300`,
re-armed — verified by observation, not documentation.

- **MUST** hold a system-idle-sleep inhibitor **only while a turn is running**, released as
  soon as it completes. Holding it for the whole session means leaving rasp open overnight
  keeps the machine awake, which is behaviour people uninstall software over.
- **MUST** use a bounded assertion that is periodically re-armed, not an indefinite one, so a
  crash or `kill -9` leaks at most one interval. 300s, matching Claude Code.
- **MUST** inhibit *idle system sleep only* — never display sleep, never sleep-on-battery. The
  screen must still dim and lock normally.
- **MUST** be best-effort. A missing `caffeinate`, absent systemd, or any platform error is
  logged at debug level and ignored. A turn **WON'T** ever fail because the machine might sleep.
- **MUST** be disableable in config, defaulting to on.
- **SHOULD** do nothing at all where the concept doesn't apply — a headless Linux server that
  never idle-sleeps needs no inhibitor and should not log warnings about it.

Platform mechanisms: `caffeinate -i -t 300` (macOS), `systemd-inhibit --what=idle` (Linux, via
logind), `SetThreadExecutionState(ES_CONTINUOUS|ES_SYSTEM_REQUIRED)` (Windows). See
[design.md](design.md) for the interface and the Go-specific thread-affinity trap on Windows.

---

## 7. User experience

### First run

With no config file, `rasp` reads `ANTHROPIC_API_KEY` from the environment and starts. The one
exception to "no wizard" is a single question, asked once, if it finds configuration you've
already written for another agent:

```
Found existing configuration:

  Claude Desktop   3 MCP servers (github, postgres, playwright)
  Claude Code      1 MCP server (sentry) · ANTHROPIC_API_KEY
  Codex            OPENAI_API_KEY

Import into rasp?  [Y/n]
```

Answer once and it's copied into rasp's own config; those files are never read again. Answer
`n` and it never asks again either.

Configuration is a single **JSONC** file — JSON with `//` comments stripped before parsing,
because a file you hand-edit needs to explain itself. Discovered per-project, then global:

```
./.rasp/config.json          project-local, checked in or not, your call
~/.config/rasp/config.json   global defaults
./.mcp.json                  MCP servers, the same file other agents read
```

```jsonc
{
  "provider": "anthropic",
  "model": "claude-opus-5",
  "mode": "manual",
  "providers": {
    // secrets resolve through the shell, so any password manager works
    "anthropic":  { "api_key": "$(op read op://vault/anthropic/key)" },
    "openrouter": { "base_url": "https://openrouter.ai/api/v1", "api_key": "$OPENROUTER_KEY" }
  },
  "permissions": { "bash": { "git status*": "allow", "git diff*": "allow" } },
  "mcp": {
    // "tools" narrows a chatty server to the calls we actually want
    "github": { "command": "gh-mcp", "tools": ["create_issue", "list_prs"] }
  }
}
```

Project context comes from `AGENTS.md` — the same file Claude Code, opencode and Crush all
read, so an existing project needs no migration. MCP servers come from `.mcp.json` or the
config above; the `tools` allow-list is optional but recommended for chatty servers, since
every exposed tool costs context on every request.

### A normal turn

```
╭─ rasp ─────────────────────────────────────────────────╮
│ › fix the nil deref in the auth middleware             │
│                                                        │
│ Let me look at the middleware first.                   │
│                                                        │
│ ▸ read   internal/auth/middleware.go        62 lines   │
│ ▸ grep   "Check("                            3 matches │
│                                                        │
│ `Check` returns a bool but the caller ignores the      │
│ second return value, so `user` is nil when the token   │
│ is expired.                                            │
│                                                        │
│ ▾ edit   internal/auth/middleware.go                   │
│   @@ -41,7 +41,10 @@                                   │
│      func middleware(next http.Handler) http.Handler { │
│   -     user := Check(r.Header.Get("Authorization"))   │
│   +     user, err := Check(r.Context(), token)         │
│   +     if err != nil {                                │
│   +         http.Error(w, "unauthorized", 401)         │
│   +         return                                     │
│   +     }                                              │
│                                                        │
│ ▸ bash   go test ./internal/auth/...          ok 0.4s  │
│                                                        │
│ Fixed. `Check` now propagates context and the caller   │
│ handles the error path.                                │
├────────────────────────────────────────────────────────┤
│ ›                                                      │
╰ manual · opus-5 · 14% ctx · ↑2.1k ↓840 · $0.06 ────────╯
```

Text appears as the model writes it. Tool cards appear collapsed with a one-line result;
`ctrl+o` expands the most recent. Diffs render inline and always expanded — you should never
have to ask what changed. The mode sits at the far left of the status line, always visible.

### Modes

Shift+Tab cycles **plan → manual → auto**. The mode is always visible in the status line, and
it persists with the session — resuming a plan-mode session comes back in plan mode.

In **plan** mode the agent can read, search and run read-only commands, but `edit`, `write`
and anything mutating are denied outright. It explores and proposes; you decide when to
execute:

```
╭─ rasp ─────────────────────────────────────────────────╮
│ › how should we fix the flaky integration test?        │
│                                                        │
│ ▸ read   internal/api/integration_test.go   240 lines  │
│ ▸ bash   git log --oneline -- integration_test.go      │
│                                                        │
│ The test shares a Postgres container across cases and  │
│ assumes serial execution. Three options:               │
│                                                        │
│ 1. `t.Parallel()` removal — smallest change, slowest   │
│ 2. Per-test schema — moderate change, keeps speed      │
│ 3. Testcontainers per suite — largest, most isolated   │
│                                                        │
│ I'd take (2). Shift+Tab to leave plan mode and I'll    │
│ implement it.                                          │
├────────────────────────────────────────────────────────┤
│ ›                                                      │
╰ plan · opus-5 · 9% ctx · ↑1.4k ↓610 · $0.03 ───────────╯
```

Switching mid-turn takes effect on the *next* tool call — a tool already approved and running
completes. Modes are permission presets (P8), so a user who wants different behavior edits the
permission map in config rather than waiting for us to add a mode.

**`yolo` is not in the cycle.** Shift+Tab goes plan → manual → auto and stops. Getting into
yolo takes `rasp --yolo` at launch or `/yolo` with a confirmation, and while it's active the
prompt itself changes so you can't forget:

```
├────────────────────────────────────────────────────────┤
│ ⚠ ›                                                    │
╰ YOLO — no approvals · opus-5 · 14% ctx · $0.06 ────────╯
```

It also never survives a restart. Resume a session that was in yolo and it comes back in
`manual`.

### A permission prompt

Inline in the transcript, not a modal — it's part of the conversation, and it blocks the turn.

```
│ ▸ bash   rm -rf ./dist && npm run build                │
│                                                        │
│   ⚠  Run this command?                                 │
│      [y] once   [a] always for this session   [n] no   │
```

`a` grants for this session only, keyed to the tool and path — it doesn't persist to disk and
doesn't widen to other directories.

### Interrupting

Esc once arms the interrupt and shows `press esc again to cancel`. Esc again cancels the turn:
in-flight tools are killed with their whole process group, any tool call without a result gets
a synthetic "interrupted" result so the session stays valid, and control returns to you with
history intact. Ctrl-C quits.

### Resuming and switching model

```
$ rasp --resume
  1  fix the nil deref in auth middleware     14 min ago   opus-5
  2  add pagination to the users endpoint      2 hrs ago   opus-5
  3  investigate the flaky integration test     yesterday  sonnet-5
```

`/model` opens a picker. Switching mid-session keeps the full conversation and records the
change in the transcript, so a replay reproduces which model said what.

### Headless

The same core, no TUI — one-shot, scriptable, streams to stdout:

```
$ rasp run -p "add a doc comment to every exported function in ./internal/auth"
$ git diff --stat
```

This is not a second implementation. It's the same agent core with a different consumer, which
is what P3 and P6 buy.

---

## 8. Success criteria

v1 is done when all of these hold.

| | Criterion | How it's checked |
|---|---|---|
| **S1** | The author completes a full working day using rasp instead of Claude Code, without switching back out of frustration | Self-reported, honestly. The single most important bar |
| **S2** | First token renders in under 1s on a warm connection | Timed against a fixed prompt |
| **S3** | No session is ever left unusable. Kill the process at any point during a turn — mid-stream, mid-tool — and `--resume` works | Automated: kill at randomized points across a scripted session, resume, assert the next turn succeeds |
| **S4** | The `edit` tool succeeds on first attempt for ≥95% of a recorded corpus of real edits | Golden corpus captured during development, run as a test |
| **S5** | Streaming stays smooth with 500+ messages of scrollback — no visible flicker, no input lag | Manual, plus a render benchmark on a synthetic session |
| **S6** | Diffs are readable at 80 columns without horizontal scrolling of the *page* | Manual, at 80/120/200 columns |
| **S7** | Binary under 40MB, cold start to first frame under 100ms | Measured in CI |
| **S8** | Works against Anthropic and at least two OpenAI-compatible providers, one of them local | Integration tests with recorded cassettes; local verified by hand |
| **S9** | A reader unfamiliar with the codebase can trace one full turn — user input to rendered output — in under 30 minutes | The author's own [design.md](design.md) walkthrough, tested on one other person |
| **S10** | An existing `.mcp.json` works with no rasp-specific configuration, and killing a server mid-session degrades gracefully rather than hanging | Integration test with a real stdio server, plus a kill-the-server test asserting the turn completes |
| **S11** | Plan mode doesn't modify the filesystem in ordinary use. Deliberately *not* claimed as "cannot" — §6.7 explains why command-string matching can't guarantee that, and overstating it would be worse than the gap itself | Automated: run a scripted session in plan mode against a git-clean tree, assert `git status` is still clean afterward. Plus a negative test: assert redirection (`echo x > f`) is denied |
| **S12** | `yolo` is unreachable by accident. No sequence of Shift+Tab presses enters it, and it never survives a restart | Automated: cycle the mode 100 times, assert yolo never activates; enter yolo, kill the process, resume, assert the mode is `manual` |

---

## 9. Milestones

Each milestone ends with something demonstrable. Effort shapes are relative, not calendar
estimates.

### M0 — Skeleton *(small)*

Repo, MIT license, CI, goreleaser config. Config loading with shell expansion. One provider
adapter that streams text to stdout. No tools, no loop.

**Demo:** `rasp run -p "write a haiku about pointers"` streams a response token by token.

### M1 — The loop *(large — this is the core)*

The agent loop: tool declaration, `tool_use` dispatch, `tool_result` feedback, termination on
stop reason. All eight tools. Parallel dispatch with the realpath mutex, result reordering and
the concurrency cap. The correctness invariants from §6.6 — pairing, truncation guard, panic
recovery, loop detection. Workspace confinement. Headless mode only.

**Demo:** `rasp run -p "add a String() method to the Config struct and run the tests"` reads
the file, makes the edit, runs `go test`, and reports the result. This is the smallest thing
that is genuinely an agent.

### M2 — The TUI, permissions and modes *(large)*

Bubble Tea v2. Streaming markdown with stable-prefix incremental rendering. Per-item render
cache. Tool cards. Colored unified diffs. Status line. Two-stage interrupt.

The permission service, and **the four modes** — roughly a day of extra work once the gate
exists, because three of them are presets over that gate and the fourth is a single early
return (P8).

**Demo:** the §7 transcript, live, including a Shift+Tab from plan into manual mid-session —
and a demonstration that no amount of cycling reaches yolo.

### M3 — Durability *(medium)*

JSONL sessions, resume, session picker. Compaction with file-tracking across the boundary.
`AGENTS.md` discovery. Prompt caching with a stable prefix. Model switching mid-session. Cost
and token accounting.

**Demo:** a session long enough to trigger compaction, interrupted, resumed the next day, with
the model switched partway through.

### M4 — MCP *(medium)*

**Deliberately last.** stdio transport via the official Go SDK. Discovery from `.mcp.json` and
rasp's own config, plus the first-run import (§6.9) that seeds both from whatever the user
already had. Namespaced merge into the tool registry. The three guardrails: tool-count budget
with per-server allow-list, lazy connect with a hard timeout, and failures surfaced as ordinary
tool errors.

Sequencing it after the core means MCP is validated against a known-good agent. Debugging a
misbehaving external server *and* an unproven loop simultaneously is how you lose a week to a
bug that was never yours.

**Demo:** a `.mcp.json` with a real server (GitHub or filesystem) works with no rasp-specific
configuration, its tools pass through the same permission prompts as built-ins, and killing
the server process mid-session degrades to "unavailable" without hanging the turn.

### M5 — Ship *(small)*

Cross-compiled release binaries, Homebrew tap, README, honest security note. Golden-corpus
edit tests and the crash-resume test from S3 running in CI.

**Demo:** `brew install`, then a full working day (S1).

---

## 10. Risks and mitigations

| | Risk | Mitigation |
|---|---|---|
| **R1** | **Bubble Tea v2 is young.** Stable since 2026-02-24, but most tutorials and blog posts still target v1 and won't compile — `View()` returns a struct now, key and mouse events are split | Treat Crush as the reference implementation, not tutorials. It's 145k lines of production Bubble Tea v2 by the framework's own authors, and v2 was hardened as its engine before public release |
| **R2** | **opencode abandoned Go + Bubble Tea**, deleting its Go TUI in `v1.0.14` with the commit message "DELETE GO BUBBLETEA CRAP HOORAY" | Their pain came from running Go and TypeScript as *separate client and server* — a Zod→OpenAPI→Stainless codegen pipeline, type drift, and four places to look for any bug. They kept the client/server split and removed the language split. We are single-language and single-process, which is precisely the configuration that avoids it. Their specific perf complaints also have known answers: their cache re-renders the whole streaming message per delta, which is what stable-prefix rendering exists to fix |
| **R3** | **Provider API drift.** Anthropic's SDK is moving fast (v1.58→v1.62 added session budgets, pinned geography, skills auto-loading); OpenAI-compatible endpoints are "compatible" to varying degrees | Pin versions. Keep the provider adapter thin and behind a narrow interface. Record HTTP cassettes so a breaking change shows up as a failing test, not a mystery at runtime |
| **R4** | **Scope creep.** Every reference project has features rasp won't, and each looks small in isolation. Crush is 145k lines and grew a client/server split, a hooks engine, three OAuth flows and a Bash config DSL | §3's non-goals are decisions, not a backlog. Anything not in [scope.md](scope.md)'s MVP section requires explicitly moving it there first. The built-in tool count is the canary — it was set at eight deliberately, and if it grows during M1, scope is slipping |
| **R5** | **The monolith trap.** pi's codebase is carefully layered everywhere *except* `agent-session.ts`, which is 3,342 lines and absorbs compaction, session switching, bash, extensions, model management and events. That is the file we will be most tempted to write | Name it in advance and watch for it. The place where the product gets assembled is where layering collapses. A hard review trigger: any file past ~600 lines gets split before the milestone closes |
| **R6** | **Subscription OAuth is a legal gray area.** Both pi and opencode authenticate against Anthropic using Claude Code's own OAuth client ID and inject "You are Claude Code, Anthropic's official CLI for Claude" — opencode's file is literally named `anthropic_spoof.txt`, and it zeroes reported cost client-side. It works by presenting as a different client, not by a mechanism Anthropic offers third parties | Ship API keys only in v1. Build the credential layer behind a refreshable interface. Phase 2 adds the uncontroversial flows first (GitHub Copilot device code). Anthropic subscription login stays an explicit, separately-decided item — never something that arrives by accident |
| **R7** | **Solo project, one contributor.** neo is 272 of 274 commits from one author, and its config surface has already churned once. Motivation is the real dependency | Milestones end in demos, not refactors — each one produces something usable. S1 (a full working day) is deliberately the top success criterion, because using the thing is what sustains building it |
| **R8** | **Learning goal quietly loses to shipping goal.** The temptation to pull in a library for the agent loop when it gets hard | The libraries list is fixed in [scope.md](scope.md) and excludes anything agent-shaped. S9 — a reader tracing a full turn in 30 minutes — is a criterion precisely to keep the core legible |
| **R9** | **MCP servers are code we don't control.** They hang, die, expose 40+ tools that flood the context and degrade tool selection, and their failures look like our bugs | The three guardrails in §6.8 are requirements, not polish: a tool-count budget with per-server allow-list, lazy connect with a hard timeout, and failures surfaced as ordinary tool errors. Sequencing MCP as the last MVP milestone (M4) means the core is known-good when external servers enter the picture |
| **R10** | **MCP's scope is a slope.** stdio is cheap; HTTP transport, OAuth-authenticated servers, resources and prompts are not. Crush's MCP OAuth handler alone is 783 lines of PKCE, dynamic client registration and a localhost callback port pool | §3 lists each of those as an explicit non-goal, not a backlog item. stdio covers the servers people actually run locally, which is the whole point of MCP as a plugin story |
| **R11** | **The MCP spec is moving fast, with breaking changes to its core.** Revision `2026-07-28` *removes* the `initialize` / `notifications/initialized` handshake entirely — MCP is now stateless, with protocol version and client capabilities carried in `_meta` on every request — adds a mandatory `server/discover` RPC that servers MUST implement, and requires a new `resultType` field on every result. That is two revisions in roughly eight months, both breaking | Depend on the official Go SDK and **pin its version**. Seal every MCP concept inside `internal/mcp/` behind rasp's own `Tool` interface, so a spec revision is a dependency bump plus one package — never a change to the agent loop or the tool registry. Carry MCP tool schemas as **opaque JSON**, since the same revision loosened `inputSchema`/`outputSchema` to arbitrary JSON Schema 2020-12 keywords including `$ref` resolution |
| **R12** | **Model metadata now depends on a third-party file.** Fetching models.dev (§6.1) means pricing, context limits and capability flags are only as correct as someone else's JSON — and it can be wrong, stale, or unreachable. pi's own catalog generator carries dozens of hand-written corrections to models.dev data, which is the clearest possible evidence that it isn't authoritative | Cache with ETag revalidation, fall back to the last good copy, then to an embedded snapshot. Bound the fetch at 5s and keep it off the startup path, so the worst case is stale metadata rather than a slow or broken launch. User-defined models in config override the catalog, so a wrong entry is always fixable locally without waiting for upstream |

**On R11, the upside — it genuinely cuts both ways.** The same revision moved *toward* our
design rather than away from it. It deprecates Roots, Sampling and Logging — three features we
had already decided to skip. It formally reclassifies HTTP+SSE as Deprecated, confirming
stdio-only was the right call. It deprecates OAuth Dynamic Client Registration in favour of
Client ID Metadata Documents, which means the 783-line handler referenced in R10 that we chose
not to copy is itself on the way out. And it now asks servers to return `tools/list` in
deterministic order, with the stated reason being *"to enable client-side caching and improve
LLM prompt cache hit rates"* — the exact concern behind our per-turn tool-set snapshot.

Source: <https://modelcontextprotocol.io/specification/2026-07-28/changelog>

---

## 11. Resolved decisions

All nine questions this document opened with are now settled. Each decision lives in the
section that owns it — this table is a map, not a second source of truth.

| Question | Decision | Where it lives |
|---|---|---|
| Parallel tool execution | Parallel by default, reads and writes alike. Per-tool sequential opt-out, realpath-keyed mutex, result reordering, cap of 8, approval as a serial barrier. MCP tools default to sequential | §6.2, [design.md](design.md) §6 |
| Model catalog | Fetch models.dev at runtime, ETag-cached, degrading to cache then to an embedded snapshot | §6.1, R12 |
| A `todos` tool | Ships. Eight built-in tools | §6.2 |
| Config format | JSONC — JSON with `//` comments | §7 |
| Session index | None, and not a deferral. Sharding by project-key removes the need | §6.4 |
| System prompt | One short prompt for every model, tuned against the golden corpus | §6.5 |
| Importing other tools' config | First-run wizard, one prompt, imports everything including keys, then never reads those files again | §6.9 |
| Plan mode's `bash` | Curated allow-list including search tools; unlisted commands ask; redirection denied. Documented as a speed bump, not a proof | §6.7 |
| Mode on resume | The session's mode wins and is announced on startup; config sets the default for new sessions only | §6.7 |

New questions will arrive during M1 — that's expected, and this section is where they go.
