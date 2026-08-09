# rasp — implementation backlog

Decomposition of [prd.md](prd.md), [design.md](design.md) and [scope.md](scope.md) into
ticket-sized work. This exists so that turning the specs into tickets is *transcription*
rather than judgement — granularity, ordering and done-conditions are decided here, once.

## How to read this

**IDs are stable.** `M1-07` is a permanent handle. Never renumber; if an item is dropped,
mark it struck rather than reusing the ID. Tickets should carry the ID in the title:
`M1-07 · Per-file mutation mutex`.

**One ticket ≈ one reviewable PR** — roughly half a day to two days. If an item looks bigger
than that in practice, split it and suffix the ID (`M1-14a`, `M1-14b`) rather than renumbering
its neighbours.

**Sizes** are relative, not calendar: `XS` under an hour · `S` half a day · `M` one to two days
· `L` three or more, and probably wants splitting once it's understood better.

**Dependencies** are hard ordering only — "cannot start until". Soft preferences aren't listed.

**Spec anchors** point at the section that governs the item. If an anchor and this file
disagree, the spec wins and this file is stale.

**Acceptance criteria are the ticket's definition of done**, phrased so someone other than the
author can check them. Where a criterion is a test, it should end up as an actual test.

---

## Milestone map

| Milestone | Theme | Items | Exit demo |
|---|---|---|---|
| **M0** | Skeleton | 9 | `rasp run -p "…"` streams a response token by token |
| **M1** | The loop | 28 | Reads a file, edits it, runs the tests, reports back |
| **M2** | TUI, permissions, modes | 20 | Live transcript; Shift+Tab plan→manual; yolo unreachable by cycling |
| **M3** | Durability | 16 | Long session, compacted, interrupted, resumed next day, model switched partway |
| **M4** | MCP | 9 | A real `.mcp.json` server works with zero rasp config; killing it degrades gracefully |
| **M5** | Ship | 9 | `brew install`, then a full working day |

MVP total: **91 items**, plus **18 epics** across the four future phases — 109 in all. Future
phases are epics, not tickets; see the end.

> **These exist in Linear** as [project Rasp](https://linear.app/theocod3s/project/rasp-be0653f32d76),
> one milestone per row above plus one per future phase. Issue titles carry the ID
> (`M1-21 · Per-file mutation mutex`), so that is the handle between the two. **This file stays
> authoritative** — dependencies were deliberately not encoded as Linear blocking relations, so
> the `deps` field here is the only machine-readable ordering.

---

## M0 — Skeleton

Nothing agentic yet. The goal is one honest end-to-end path: config in, tokens out.

### M0-01 · Repo scaffolding
**XS** · deps: — · spec: design §2

`go.mod` on Go 1.26, `internal/` tree per design §2 with package doc comments stating each
package's single responsibility, task runner, `.editorconfig`.

- [ ] `go build ./...` succeeds
- [ ] every `internal/` package has a doc comment naming what does *not* belong in it

### M0-02 · CI
**XS** · deps: M0-01 · spec: design §13

- [ ] build, `go vet`, `go test ./...`, and `go test -race ./...` on push
- [ ] fails the job on any non-zero exit
- [ ] runs on linux and macOS

### M0-03 · goreleaser config
**S** · deps: M0-01 · spec: design §14

- [ ] `CGO_ENABLED=0`, `-trimpath`, version injected via ldflags
- [ ] matrix covers darwin/linux/windows × amd64/arm64
- [ ] `goreleaser build --snapshot --clean` produces runnable binaries
- [ ] uses `homebrew_casks:`, **not** the deprecated `brews:`

### M0-04 · Config loading and precedence
**M** · deps: M0-01 · spec: prd §7, design §10

JSONC parsing, the full precedence chain, and a `rasp config check` that prints the resolved
result with each value's source.

- [ ] `//` comments parse; the file extension stays `.json`
- [ ] precedence is defaults → global → project → env → flags, verified by a test per hop
- [ ] a project config setting `"mode": "yolo"` is **rejected with an explanatory error**
- [ ] `rasp config check` names the origin of every resolved value

### M0-05 · Shell expansion for config values
**S** · deps: M0-04 · spec: design §10

- [ ] `$VAR`, `${VAR}`, `${VAR:-default}`, `${VAR:?msg}` and `$(command)` all resolve
- [ ] a failing `$(command)` produces an actionable error naming the key, not a bare exit code
- [ ] expansion applies to secret-bearing fields; a test proves `$(echo hunter2)` reaches the provider

### M0-06 · Provider interface and stream contract
**M** · deps: M0-01 · spec: design §3.1

The `Provider` interface, `StreamResponse = iter.Seq[Event]`, the event union, and the neutral
message model.

- [ ] **`Stream` never returns a Go error** for model/request/runtime failure — a compile-time
      impossibility, since it returns only `iter.Seq[Event]`
- [ ] every emitted event has `Partial` populated with the full accumulated message
- [ ] a test drives a scripted failure and asserts it arrives as a terminal `EventError`

### M0-07 · Anthropic adapter — streaming text
**M** · deps: M0-06 · spec: design §3.1, internals §4.2

Text streaming only. Tool dispatch lands in M1-04.

- [ ] `messages.NewStreaming` deltas map onto the event union
- [ ] `Message.Accumulate` builds `Partial` correctly across every event type
- [ ] the neutral message is allocated **outside** the stream loop, so `Partial` is a stable
      pointer rather than a fresh allocation per token
- [ ] `stream.Err()` becomes a terminal `EventError`, never a returned error
- [ ] usage numbers are captured from the final event

### M0-08 · Headless runner
**S** · deps: M0-07 · spec: prd §7

- [ ] `rasp run -p "…"` streams tokens to stdout as they arrive, not buffered
- [ ] non-zero exit on failure, with the error on stderr
- [ ] output is clean when piped (no ANSI when stdout isn't a TTY)

### M0-09 · Structured logging
**XS** · deps: M0-01 · spec: design §2

- [ ] `log/slog` to a file under the state dir; **stdout belongs to the UI**
- [ ] level configurable by env var
- [ ] no secret ever reaches the log — a test asserts an API key is redacted

---

## M1 — The loop

The core. Everything here is what makes rasp an agent rather than a chat client.

### M1-01 · Tool interface and Result
**S** · deps: M0-06 · spec: design §3.2, internals §3.1–3.2

- [ ] `Schema()` returns `map[string]any`, so reflection and opaque MCP schemas both satisfy it
- [ ] `Result{Content, IsError, Details}` — `Details` is never serialized to the model
- [ ] a tool returning `IsError: true` with a `nil` Go error is the documented failure path

### M1-02 · Reflection-based schema generation
**M** · deps: M1-01 · spec: design §3.2, internals §3.1

`NewTool[TInput]` deriving JSON Schema from a tagged struct.

- [ ] `json` tag gives the property name; `omitempty` marks it optional
- [ ] `description` tag becomes the schema description
- [ ] nested structs, slices, maps and enums all round-trip
- [ ] the generated schema is the *same object* used to unmarshal — a test proves they cannot drift

### M1-03 · Tool registry with per-turn snapshot
**S** · deps: M1-01 · spec: design §3.3

- [ ] the registry is safe to mutate while a turn reads it
- [ ] a turn takes one snapshot and sees a stable list for its whole duration
- [ ] tool ordering within a snapshot is **deterministic**, so the prompt-cache prefix is stable

### M1-04 · The step loop
**L** · deps: M0-07, M1-03 · spec: design §4, internals §1–2

Call the model, dispatch `tool_use`, feed `tool_result` back, terminate on stop reason.

- [ ] terminates on every documented stop reason, with an unknown reason treated as an error
      rather than silently looping
- [ ] a `MaxSteps` fuse exists and is hit only by genuinely pathological input
- [ ] `context` cancellation exits promptly at a step boundary
- [ ] a scripted two-step turn (tool call, then answer) passes end to end against the fake provider

### M1-05 · tool_use/tool_result pairing — prevent on write
**M** · deps: M1-04 · spec: design §4, internals §2.4

- [ ] the assistant message and its results are committed together or not at all
- [ ] cancelling mid-batch emits synthetic results for calls that never ran
- [ ] a test cancels at every step boundary and asserts the transcript stays valid

### M1-06 · Truncated-tool-call guard
**S** · deps: M1-04 · spec: prd §6.6, internals §4.6

- [ ] `stop_reason == "length"` fails **every** tool call in that message
- [ ] the model receives an error result explaining why, not silence
- [ ] a test feeds a truncated-arguments response and asserts nothing executed

### M1-07 · Panic recovery per tool
**S** · deps: M1-01 · spec: prd §6.6

- [ ] a panicking tool returns `IsError: true` with the recovered message
- [ ] the process survives; a test panics inside a tool and asserts the turn continues
- [ ] the stack trace reaches the log, not the model

### M1-08 · Loop detection
**S** · deps: M1-04 · spec: prd §6.6, internals §4.6

- [ ] signature is a hash of `(tool name, input, output)` per step
- [ ] halts when the same signature appears more than 5 times in the last 10
- [ ] halting surfaces to the user as an explanation, not a crash

### M1-09 · Workspace confinement
**M** · deps: M0-01 · spec: design §2, prd §6.6

`os.Root`-based confinement plus path resolution.

- [ ] every file tool routes through `workspace`, never `os` directly — enforced by a lint or test
- [ ] `../` escapes are rejected
- [ ] **a symlink pointing outside the workspace is rejected**, and there's a test for it
- [ ] the error names the offending path

### M1-10 · `read` tool
**S** · deps: M1-02, M1-09 · spec: prd §6.2

- [ ] whole-file and `offset`/`limit` line-window modes
- [ ] size cap with a clear message pointing at the windowed mode
- [ ] records the read in the read-before-edit tracker (M2-19 consumes it)

### M1-11 · `write` tool
**S** · deps: M1-02, M1-09 · spec: prd §6.2

- [ ] creates parent directories
- [ ] atomic write via temp + rename; preserves mode on overwrite
- [ ] returns the byte count and whether the file was created or replaced

### M1-12 · `edit` tool — rungs 1 and 2
**M** · deps: M1-02, M1-09 · spec: prd §6.2, internals §3.4

Exact match, and the ambiguity check.

- [ ] exact match replaces and returns a diff in `Details`
- [ ] more than one occurrence without `replace_all` is a **hard error asking for more context** —
      never "first match wins"
- [ ] zero occurrences falls through to rung 3

### M1-13 · `edit` tool — rungs 3 and 4
**M** · deps: M1-12 · spec: prd §6.2, internals §3.4

Whitespace-normalized match with re-indentation, and the diagnostic on total failure.

- [ ] normalized matching accepts **whole-line-aligned matches only**
- [ ] the replacement is re-indented to the file's detected indent unit (tabs vs N-space)
- [ ] the result **tells the model** the match wasn't byte-exact
- [ ] total failure prints the closest location's real content with whitespace visualized
- [ ] no Levenshtein or approximate character matching anywhere — asserted by review

### M1-14 · `bash` tool — execution and cancellation
**M** · deps: M1-02 · spec: prd §6.2, internals §3.5

- [ ] `SysProcAttr{Setpgid: true}` plus a `cmd.Cancel` killing the **whole process group**
- [ ] `WaitDelay` set, so a grandchild holding the pipe can't hang the turn
- [ ] a test spawns `bash -c "sleep 300 &"`, cancels, and asserts **no orphan survives**
- [ ] stdout and stderr share one writer, preserving chronological order
- [ ] timeout is configurable per call with a default

### M1-15 · `bash` tool — bounded output
**S** · deps: M1-14 · spec: prd §6.2, internals §3.5

- [ ] output capped, keeping **both head and tail**
- [ ] the cap is never exceeded even after inserting the truncation marker
- [ ] full output spills to a temp file whose path is appended to the result

### M1-16 · `grep` tool
**S** · deps: M1-02, M1-09 · spec: prd §6.2

- [ ] shells out to `rg` when on `PATH`, pure-Go fallback otherwise
- [ ] identical result shape from both paths — one test table runs against each
- [ ] respects `.gitignore`; binary files skipped

### M1-17 · `find` tool
**S** · deps: M1-02, M1-09 · spec: prd §6.2

- [ ] `**` globbing via `doublestar`
- [ ] gitignore-aware; result count capped with a clear truncation notice

### M1-18 · `ls` tool
**XS** · deps: M1-02, M1-09 · spec: prd §6.2

- [ ] lists one directory with type and size
- [ ] refuses paths outside the workspace via M1-09

### M1-19 · `todos` tool
**S** · deps: M1-02 · spec: prd §6.2

- [ ] the model can create, update and complete items
- [ ] state is per-turn and visible in `Details` for the TUI to render
- [ ] touches no files and executes nothing — asserted by review

### M1-20 · Parallel dispatch with concurrency cap
**M** · deps: M1-04 · spec: design §6 rules 4–6, internals §6.2

- [ ] tools run concurrently **by default**
- [ ] a tool declaring `Sequential()` forces the whole batch sequential (pi's rule)
- [ ] a semaphore caps concurrency at 8
- [ ] `go test -race` clean under a batch of mixed tools

### M1-21 · Per-file mutation mutex
**S** · deps: M1-20 · spec: design §6 rule 6, internals §6.2

- [ ] the lock key is `filepath.EvalSymlinks`-resolved, so `./a.go`, `a.go` and a symlink to it
      take the **same** lock
- [ ] two concurrent edits to one file both apply, in order
- [ ] two edits to different files demonstrably overlap in time
- [ ] `go test -race` clean under concurrent edit load

### M1-22 · Result reordering
**S** · deps: M1-20 · spec: design §6 rule 6, internals §6.2

- [ ] results are written by index into a pre-sized slice, never appended on completion
- [ ] a test completes tools in reverse order and asserts `tool_result` order matches `tool_use` order

### M1-23 · OpenAI-compatible adapter
**M** · deps: M0-06 · spec: design §3.1, internals §4.2

Uses the **official `openai/openai-go`**, not `sashabaranov/go-openai` — the official SDK ships
`ChatCompletionAccumulator`, and the community one leaves fragment reassembly to you.

- [ ] one adapter with a swappable base URL
- [ ] fragment reassembly uses `acc.AddChunk` + `JustFinishedToolCall`/`JustFinishedContent`;
      we write only the projection onto the neutral message
- [ ] `Partial` is a stable pointer — the neutral message is allocated **outside** the stream loop
- [ ] tool calls normalize to the same event union as Anthropic despite having no per-block
      stop event and carrying the function name only on the first fragment
- [ ] verified against at least two endpoints (e.g. OpenRouter and a local Ollama)

### M1-24 · Two-tier retry
**M** · deps: M0-06 · spec: design §12

- [ ] transport tier honours `retry-after` / `retry-after-ms`, jitters, and **throws rather than
      sleeps** past a 60s cap
- [ ] semantic tier classifies over the final message; **quota/billing errors are checked first
      and fail immediately**
- [ ] backoff sleep is cancellable by the turn's context
- [ ] a test asserts a 429-for-quota does not consume the retry budget

### M1-25 · Fake provider
**M** · deps: M0-06 · spec: design §13

Scriptable provider for deterministic loop tests, with no network.

- [ ] scripts multi-step turns including tool calls, errors and truncation
- [ ] emits realistic event sequences with `Partial` populated
- [ ] every M1 loop test runs against it with zero API cost

### M1-26 · Golden edit corpus
**M** · deps: M1-13, M1-25 · spec: prd §8

The regression suite for the edit ladder — the feedback signal for system-prompt tuning.

- [ ] a corpus of real edit cases per rung, including whitespace drift and smart quotes
- [ ] each case asserts which rung matched, not merely that it succeeded
- [ ] fuzz target over the matcher, seeded from the corpus

### M1-27 · Prompt assembly with cache breakpoints
**S** · deps: M1-03 · spec: design §11, internals §7

- [ ] the system prompt is ordered blocks with an explicit cacheable flag
- [ ] volatile content (cwd, date, mode) sits **after** the last breakpoint
- [ ] a test asserts two consecutive identical turns produce a byte-identical cacheable prefix

### M1-28 · M1 integration test
**S** · deps: M1-13, M1-14, M1-25 · spec: prd §9

- [ ] the M1 demo runs headless in CI against the fake provider: read a file, edit it, run a
      command, report
- [ ] the resulting transcript passes a validity check for tool_use/tool_result pairing

---

## M2 — TUI, permissions and modes

### M2-01 · Bubble Tea skeleton and the event bridge
**M** · deps: M1-04 · spec: design §6, internals §4.3

- [ ] one goroutine drains `agent.Event` into `Program.Send`
- [ ] `Update` is the only place model state mutates — asserted by `go test -race` under streaming
- [ ] a terminal event (turn complete) can never be dropped by a full channel

### M2-02 · Turn as a `tea.Cmd`, with cancellation
**S** · deps: M2-01 · spec: design §6

- [ ] the turn runs off the `Update` goroutine; `Update` never blocks
- [ ] the cancel func is stored on the model and fires on interrupt
- [ ] a cancelled turn leaves a valid transcript (M1-05 covers the invariant)

### M2-03 · Conversation view with per-item render cache
**M** · deps: M2-01 · spec: design §6, internals §4.5

- [ ] items cache their rendered output keyed by width
- [ ] a finished item is frozen and never re-rendered
- [ ] a benchmark shows a cursor blink in a 200-message conversation re-renders nothing

### M2-04 · Streaming markdown — stable-prefix rendering
**L** · deps: M2-03 · spec: design §11, internals §4.4

The hardest UI item. Split if it runs long.

- [ ] the boundary detector proves no open fence, list, table, quote or setext header
- [ ] only the unstable tail re-renders per delta
- [ ] **falls back to a full render whenever the proof is uncertain**
- [ ] a benchmark over a long streaming message shows sub-linear render cost
- [ ] a corpus of partial-markdown snapshots renders without visual corruption

### M2-05 · Glamour renderer memoization
**XS** · deps: M2-04 · spec: internals §4.4

- [ ] `TermRenderer` memoized per width
- [ ] access serialized by a mutex — **Glamour is not reentrant**
- [ ] `go test -race` clean with concurrent renders

### M2-06 · Tool call cards
**S** · deps: M2-03 · spec: prd §6.3

- [ ] a one-line summary per call, expandable
- [ ] rendering reads `Result.Details`; the tool contributes **no UI types**
- [ ] running calls show elapsed time without re-rendering the whole conversation

### M2-07 · Diff rendering
**M** · deps: M2-06 · spec: prd §6.3, internals §3.4

- [ ] `go-udiff` computes; Lip Gloss styles per line class
- [ ] every `edit` and `write` renders a diff — never just a path
- [ ] wide lines scroll horizontally rather than wrap
- [ ] renders correctly in both light and dark terminals

### M2-08 · Status line
**S** · deps: M2-01 · spec: prd §6.3

- [ ] model, mode, context used, token counts, cost
- [ ] mode is **always visible**
- [ ] updates without re-rendering the conversation

### M2-09 · Two-stage interrupt
**S** · deps: M2-02 · spec: prd §6.3

- [ ] first Esc arms and shows a hint; second cancels
- [ ] the armed state clears on any other key
- [ ] Ctrl-C quits, cancelling any in-flight turn first and saving the session

### M2-10 · Slash commands
**S** · deps: M2-01 · spec: prd §6.3

- [ ] `/model`, `/new`, `/resume`, `/compact`, `/clear`, `/help`, `/quit`
- [ ] unknown commands produce a helpful error, never a silent no-op

### M2-11 · Permission service
**M** · deps: M1-20 · spec: design §7.7, internals §5.4

The ladder, with grants keyed by `(tool, action, path)`.

- [ ] rungs evaluate in the documented order
- [ ] a grant for `/foo` does **not** cover `/bar`
- [ ] grants are session-scoped and in-memory; a restart re-prompts
- [ ] concurrent resolvers race safely — first wins, the rest are no-ops

### M2-12 · Glob specificity resolution
**S** · deps: M2-11 · spec: design §7.3

- [ ] most-specific pattern wins, deterministically and independent of map order
- [ ] `find *: allow` with `find * -delete*: ask` composes correctly
- [ ] a test table covers overlapping patterns

### M2-13 · Redirection guard
**S** · deps: M2-12 · spec: design §7.3a, internals §5.4

- [ ] commands containing `>`, `>>` or `| tee` are denied in plan mode
- [ ] the denial message explains why rather than just refusing
- [ ] documented as a **speed bump, not a proof** — the UI never implies a guarantee

### M2-14 · Permission prompt overlay
**M** · deps: M2-11 · spec: prd §6.3

- [ ] inline in the transcript, blocking the turn
- [ ] once / always / reject
- [ ] absorbs stray keystrokes for a grace period after opening
- [ ] a pending prompt is cancellable by the turn's context

### M2-15 · Approval as a serial barrier
**S** · deps: M2-11, M1-20 · spec: design §6 rule 5

- [ ] a batch splits at any call requiring approval
- [ ] two concurrent calls never produce two racing prompts
- [ ] a test asserts prompts appear strictly one at a time

### M2-16 · Mode presets as data
**M** · deps: M2-12 · spec: design §7.2, internals §5.4

- [ ] `plan`, `manual`, `auto` are permission maps, with **no mode branch in the agent loop**
- [ ] plan mode's allow-list matches design §7.2, including search tools
- [ ] `go test`/`go build` are `ask`, not `allow`
- [ ] any preset is overridable in config

### M2-17 · Mode switching
**S** · deps: M2-16 · spec: design §7.4–7.5

- [ ] Shift+Tab cycles plan → manual → auto
- [ ] a switch injects a synthetic reminder so the model learns its constraints changed
- [ ] a mid-turn switch applies to the **next** tool call, never retroactively

### M2-18 · yolo bypass
**S** · deps: M2-16 · spec: design §7.2, prd §6.7

- [ ] implemented as an early return **before** the ladder, not a permissive preset
- [ ] **structurally unreachable from the cycle** — the cycle array cannot produce it
- [ ] `--yolo` flag and `/yolo` command only; `/yolo` requires confirmation
- [ ] a loud persistent indicator while active
- [ ] **never survives a restart** — a resumed session comes back gated

### M2-19 · Read-before-edit tracker
**S** · deps: M1-09, M1-13 · spec: prd §6.6

- [ ] editing a file this session hasn't read is refused with an actionable message
- [ ] editing a file whose mtime is newer than the last read is refused
- [ ] the refusal tells the model to re-read

### M2-20 · TUI test harness
**M** · deps: M2-03 · spec: design §13

- [ ] `teatest` drives the model headlessly
- [ ] golden files for `View()` across key states
- [ ] a `-update` flag regenerates goldens

---

## M3 — Durability

### M3-01 · JSONL session store
**M** · deps: M1-04 · spec: design §9, internals §8

- [ ] append-only; a turn never rewrites the file
- [ ] a torn final line is skipped on read, not fatal
- [ ] whole-file operations use temp + rename
- [ ] a test kills the process mid-write and asserts the file still loads

### M3-02 · Entry types and `parent_id`
**S** · deps: M3-01 · spec: design §9, internals §8

- [ ] every entry carries `id` and `parent_id`
- [ ] model and mode changes are **first-class entries**, not metadata
- [ ] replay reproduces which model and mode produced each turn

### M3-03 · Project-key sharding
**S** · deps: M3-01 · spec: design §9, internals §8

- [ ] `<project-key>` is the repo's first commit hash
- [ ] falls back to a path hash outside a repo
- [ ] moving or re-cloning the repo **preserves session history** — there's a test

### M3-04 · Repair-on-read
**M** · deps: M3-01, M1-05 · spec: design §9, internals §2.4, §8

- [ ] orphaned `tool_use` gets a synthetic error result on load
- [ ] orphaned `tool_result` is dropped
- [ ] a test kills the process at ten points during a turn and asserts every resulting session
      loads **and can take another turn**

### M3-05 · Resume and session picker
**M** · deps: M3-03, M3-04 · spec: prd §6.4

- [ ] `rasp --resume` restores the most recent session for this project
- [ ] the picker lists this project's sessions with title, time and message count
- [ ] listing stays under 50ms with 200 sessions in the project

### M3-06 · Mode restoration on resume
**XS** · deps: M3-05, M2-16 · spec: prd §6.7

- [ ] the session's mode wins over the config default
- [ ] resume prints which mode was restored
- [ ] yolo is never restored (M2-18)

### M3-07 · AGENTS.md discovery
**M** · deps: M1-27 · spec: design §8, internals §5.1

- [ ] walks cwd → repo root, outermost first; also reads `CLAUDE.md`
- [ ] each file wrapped with its path for provenance
- [ ] **the git-worktree double-load case is handled** — there's a test
- [ ] a global file participates at lowest priority

### M3-08 · Token estimation
**S** · deps: M3-01 · spec: design §11, internals §7

- [ ] uses real usage from the last assistant message, `chars/4` only for the tail after it
- [ ] a test over a code-heavy transcript shows it beats flat `chars/4`

### M3-09 · Tool-output pruning
**S** · deps: M3-08 · spec: design §11, internals §7

The cheap tier, before any LLM call.

- [ ] blanks the output of old tool calls beyond a protected recent window
- [ ] no model call involved
- [ ] a test asserts a large stale file-read shrinks while recent turns stay intact

### M3-10 · Compaction
**L** · deps: M3-09 · spec: design §11, internals §7

- [ ] triggers at `used > window - reserve`
- [ ] the cut point **never separates a `tool_use` from its `tool_result`**
- [ ] read/modified file lists carry across the boundary
- [ ] compaction is itself a session entry, so replay is deterministic without re-running the LLM
- [ ] overflow recovery runs at most once — it cannot loop

### M3-11 · Model switching mid-session
**S** · deps: M3-02 · spec: prd §6.1

- [ ] `/model` switches without ending the session
- [ ] the switch is recorded as an entry
- [ ] the prompt-cache prefix invalidation is understood and documented, not accidental

### M3-12 · models.dev catalog
**M** · deps: M0-04 · spec: design §10.2

- [ ] fetches with ETag revalidation, cached to disk
- [ ] the resolve chain is live → cached → embedded snapshot → error
- [ ] bounded by a short timeout and **off the startup path** — a test with the network blackholed
      asserts startup is unaffected
- [ ] user-defined models in config override the catalog

### M3-13 · Cost and token accounting
**S** · deps: M3-12 · spec: prd §6.1

- [ ] per-turn and per-session input, output and cache-read tokens
- [ ] cost computed from catalog pricing
- [ ] cache reads are visibly distinguished — that's how you know caching works

### M3-14 · Wakelock
**M** · deps: M1-04 · spec: prd §6.10, design §6.1

- [ ] acquired at turn start, released in a `defer`
- [ ] macOS `caffeinate -i -t 300`, **re-armed** while held
- [ ] Linux `systemd-inhibit --what=idle`; no-op where absent
- [ ] Windows `SetThreadExecutionState` **with `runtime.LockOSThread()`**
- [ ] every platform failure returns a no-op lock and logs at debug — a turn never fails
- [ ] `"keep_awake": false` disables it

### M3-15 · go-vcr cassettes
**S** · deps: M0-07 · spec: design §13

- [ ] a recorded real streaming tool-use turn replays offline
- [ ] `x-api-key` is scrubbed before the cassette is committed — asserted by a test
- [ ] a custom matcher tolerates non-deterministic request IDs

### M3-16 · M3 integration test
**S** · deps: M3-10, M3-05 · spec: prd §9

- [ ] a session long enough to compact, killed mid-turn, resumed, and continued
- [ ] the model is switched partway and replay reflects it

---

## M4 — MCP

Deliberately last, so external servers are debugged against a known-good agent.

### M4-01 · MCP stdio client
**M** · deps: M1-03 · spec: design §8

- [ ] official `modelcontextprotocol/go-sdk`, **version pinned**
- [ ] spawns the server, speaks JSON-RPC over stdin/stdout, reaps on shutdown
- [ ] **no MCP type escapes `internal/mcp/`** — enforced by an import lint

### M4-02 · Server discovery
**S** · deps: M4-01 · spec: design §8.1

- [ ] reads `.mcp.json` from the project and servers from rasp's own config
- [ ] a malformed file is skipped with a warning, never fatal
- [ ] name collisions resolve deterministically with a documented precedence

### M4-03 · Background connect and settle
**M** · deps: M4-02 · spec: design §8

- [ ] connects in the background at startup, not on the critical path
- [ ] the first turn waits a bounded settle period, then proceeds regardless
- [ ] **a dead or hanging server never blocks startup** — there's a test with a server that
      never responds

### M4-04 · Namespaced tool merge
**S** · deps: M4-03 · spec: design §8

- [ ] MCP tools enter the same registry as `mcp__<server>__<tool>`
- [ ] schemas pass through as **opaque JSON**, untouched — `$ref` and any 2020-12 keyword survive
- [ ] they appear to the model as ordinary tools

### M4-05 · MCP tools default to sequential
**XS** · deps: M4-04, M1-20 · spec: design §8

- [ ] MCP tools declare `Sequential()`; we audited ours, we can't audit theirs
- [ ] the asymmetry with built-ins is documented where a reader will find it

### M4-06 · Tool-count budget
**S** · deps: M4-04 · spec: prd §6.8, design §8

- [ ] a global ceiling and a per-server allow-list
- [ ] exceeding it produces a **startup warning naming the offending server**, never silent truncation
- [ ] the resulting tool list is deterministic, so the prompt cache survives

### M4-07 · Failure isolation
**S** · deps: M4-03 · spec: prd §6.8

- [ ] a server dying mid-turn surfaces as an ordinary tool error
- [ ] a test kills a server mid-turn and asserts the turn completes
- [ ] every MCP call passes the **same permission gate** as a built-in

### M4-08 · First-run import wizard
**M** · deps: M0-04, M4-02 · spec: prd §6.9, design §10.1

- [ ] runs on first run only
- [ ] shows exactly what was found before copying anything, then asks **once**
- [ ] imports everything behind that one prompt, **including API keys** — no separate key prompt
- [ ] afterwards rasp reads only its own config
- [ ] a malformed or missing source is skipped silently

### M4-09 · Import sources
**S** · deps: M4-08 · spec: design §10.1

- [ ] Claude Desktop, Claude Code, Codex, opencode, Crush and pi, with per-platform paths
- [ ] each source is independently testable from a fixture
- [ ] an unknown schema version degrades to skipping that source, not failing the wizard

---

## M5 — Ship

### M5-01 · Release pipeline
**S** · deps: M0-03 · spec: design §14

- [ ] tag push produces signed, checksummed binaries for the full matrix
- [ ] the version reported by `rasp --version` matches the tag

### M5-02 · Homebrew cask
**S** · deps: M5-01 · spec: design §14

- [ ] `homebrew_casks:`, not deprecated `brews:`
- [ ] `brew install theocod3s/tap/rasp` works on a clean machine
- [ ] shell completions installed

### M5-03 · README and security note
**S** · deps: — · spec: prd §3

- [ ] states plainly what the safety net **is and isn't** — a blast-radius limiter, not a
      security boundary against a hostile model
- [ ] plan mode described as a speed bump, consistent with M2-13
- [ ] install, config and first-run documented

### M5-04 · Crash-resume test in CI
**S** · deps: M3-04 · spec: prd §8

- [ ] kills the process at randomized points during a turn
- [ ] every resulting session loads and can take another turn
- [ ] runs on every push, not nightly — it guards the invariant most likely to regress silently

### M5-05 · Golden corpus in CI
**XS** · deps: M1-26 · spec: prd §8

- [ ] the edit corpus runs on every push
- [ ] a rung regression fails the build

### M5-06 · Headless mode parity
**S** · deps: M2-16 · spec: prd §6.3

- [ ] `rasp run -p` honours modes and permissions identically to the TUI
- [ ] a non-interactive session cannot prompt, so unresolvable permissions fail with a clear message

### M5-07 · Performance pass
**M** · deps: M2-04, M3-05 · spec: prd §8

- [ ] first token under 500ms on a warm cache
- [ ] a 200-message conversation scrolls without visible lag
- [ ] the session picker stays under 50ms at 200 sessions

### M5-08 · Dogfood week
**M** · deps: all · spec: prd §8

- [ ] a full working day using rasp instead of another agent, without reaching for the other tool
- [ ] every friction point filed as an issue rather than fixed inline
- [ ] the outcome decides whether M5 ships or a stabilisation milestone is inserted

### M5-09 · Open-source repo docs
**XS** · deps: — · spec: prd §3

`CONTRIBUTING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`. M5-03 covers the README; these are the
files a public repo is expected to carry and that `LICENSE` alone does not supply.

- [ ] `CONTRIBUTING.md` states `just ci` before pushing, and that the design documents are the
      source of truth — a disagreement between code and a document is a bug in one of them
- [ ] `SECURITY.md` scopes what a vulnerability report means here, saying plainly that rasp is
      **not** a security boundary, so "the agent can write files" or "a prompt can redirect it"
      are out of scope. pi does exactly this and it is why they aren't buried in such reports
      ([research/pi.md](research/pi.md))
- [ ] `CODE_OF_CONDUCT.md` present

---

## Future phases

Coarser deliberately — these are epics, not tickets. Decompose each when it's next, not now;
specifying work you won't start for months mostly produces wrong specifics.

### Phase 2 — the obvious gaps
| Epic | Notes |
|---|---|
| **P2-OAUTH** | `rasp auth login`, PKCE, callback server, proactive refresh at `max(expires_in/10, 30s)`, revoked-token detection. Start with GitHub Copilot device code. **Anthropic subscription OAuth stays a separate, explicitly-flagged decision** |
| **P2-TOOLS** | `multiedit`, `web_fetch`, `web_search`, `question`, background bash with `job_output`/`job_kill` |
| **P2-SUBAGENT** | `task` tool spawning a child session with a restricted preset and its own cost accounting rolling up |
| **P2-BRANCH** | `/fork` and `/tree` over the `parent_id` already written by M3-02 |
| **P2-MCP-HTTP** | Streamable HTTP transport. **Not** HTTP+SSE — deprecated upstream |

### Phase 3 — ecosystem
| Epic | Notes |
|---|---|
| **P3-LSP** | Diagnostics first, then `lsp_definition` / `lsp_symbols` / `lsp_rename` as tools |
| **P3-HOOKS** | `PreToolUse` shell commands, regex-matched on tool name, as a decorator around `Tool` |
| **P3-SKILLS** | Agent Skills `SKILL.md`, advertised by name and description with the model reading on demand |
| **P3-MEMORY** | Semantic memory — durable cross-session facts (the user, project conventions, settled decisions) recalled as one more ordered `prompt` block with its own cache flag. Eviction is the hard part, not storage. **Long-horizon** — listed to protect the seam, not because it is queued |

### Phase 3.5 — cross-session messaging
| Epic | Notes |
|---|---|
| **P35-TRANSPORT** | Per-session Unix socket, restricted to the OS user; discovery via files on disk with staleness detection |
| **P35-TOOLS** | `list_agents`, `send_message`; delivery via the existing steering/follow-up queues |
| **P35-TRUST** | The reason this is deferred. Inbound admission (`accept`/`hold`/`refuse`) as an axis separate from permissions; the mode-derived asymmetric default; **outbound laundering prevention**; loop throttling; the capability floor. See scope.md and design §15 |

### Phase 4 — polish
| Epic | Notes |
|---|---|
| **P4-DIFF** | Side-by-side layout, intra-line word highlighting |
| **P4-THEME** | Themes and configurable keybindings |
| **P4-HISTORY** | Per-session file-version history for undo/checkpoint |
| **P4-KEYRING** | Optional OS keyring backend alongside file storage |
| **P4-SPINNER** | Rotating status text while a turn runs, instead of a fixed spinner label. Pure `tui`; small enough to pull forward if it ever feels worth it |
| **P4-VOICE** | Speech-to-text input. Much the largest epic in this phase and the furthest from the current design, which assumes keyboard input throughout. Needs an STT engine — hosted adds a network dependency, local must not break `CGO_ENABLED=0`. **Long-horizon**, like P3-MEMORY |

---

## For whoever creates the tickets

- Use the ID in the title: `M1-21 · Per-file mutation mutex`.
- Milestones map to Linear **projects**; phases 2–4 map to projects containing one issue per epic.
- Copy the acceptance criteria into the issue description as a checklist. They're written to be
  checkable by someone who didn't do the work.
- Set blocking relationships from the `deps` field. They are hard ordering, so a blocked issue
  genuinely cannot start.
- **Don't invent scope.** If something seems missing, it's either deliberate (check
  [scope.md](scope.md)'s "deliberately excluded") or a gap worth raising rather than silently
  filling.
- Sizes are relative. Don't convert them to hours or story points without recalibrating against
  the first few completed items.
