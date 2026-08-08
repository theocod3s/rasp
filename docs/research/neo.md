# neo — architecture research

Source: full clone of `https://github.com/owainlewis/neo`, HEAD `51cbf4b`
("fix(llm): filter incompatible raw history (#287)"). Read at source level.

**Why this one matters most to us:** it is the closest existing thing to what we're
building — a Go coding agent, Bubble Tea v2 TUI, single binary, hand-rolled provider
clients, ~25k LOC. Small enough to read end to end.

---

## 1. Vitals

| | |
|---|---|
| Language | Go (module `github.com/owainlewis/neo`, `go 1.25.12`) |
| LOC | 110 `.go` files, **25,298 lines**. 51 test files totalling **12,973 lines** — roughly half the codebase is tests |
| Dependencies | Almost entirely Charm: `bubbletea/v2`, `bubbles/v2`, `lipgloss/v2`, `glamour/v2`, plus `chroma`, `goldmark`, `yaml.v3`. **No LLM SDKs** — every provider client is hand-rolled `net/http`. No agent framework |
| License | MIT |
| Activity | 274 commits, effectively single-author (272 by Owain Lewis). First commit 2026-03-31, latest 2026-08-05 — under five months old. Latest tag `v0.6.2` |

**Maturity:** young and single-maintainer, but unusually disciplined for its age — heavy
test coverage, an internal `docs/developer/guides/*` architecture doc set, deprecated
config keys tracked with migration errors rather than silently ignored. Reads like
production code, not a prototype.

## 2. Package layout

```
cmd/neo/            CLI entrypoint: arg parsing, provider wiring, chat/headless/session commands
internal/
  agent/            The core agent loop (agent.go, 852 lines) — turns, tool dispatch, steering
  llm/              Provider-neutral types (provider.go) + one subpackage per backend:
    anthropic/        Messages API client
    openai/           Chat/Responses API + codex.go (ChatGPT subscription, SSE)
    chatcompletions/  Shared OpenAI-compatible wire format (used by openai, openrouter)
    google/           Gemini GenerateContent client
    openrouter/       Built on chatcompletions
    retry/            Shared HTTP retry/backoff with Retry-After handling
    llmtest/          Fake provider for tests
  tools/            bash.go, fs.go (read/write/edit), search.go (grep/glob), tool.go (interface/registry)
  factory/          Subagent orchestration: supervisor.go (budgets, event bus), runner.go, the "agent" tool
  workflow/         The "workflow" (todo-list) tool + its event stream
  compact/          Context-window compaction (summarize.go)
  session/          Session persistence (JSON files + index) and resume
  config/           neo.yaml loading, defaults, feature flags
  approval/         Tool-approval rule matching
  auth/             OpenAI device-code OAuth + credential store
  projectctx/       AGENTS.md discovery/composition into the system prompt
  skills/           SKILL.md discovery, $name / /name expansion
  phase/            Built-in named prompts (/design, /plan, /build, /review)
  workspace/        Repo-root detection, symlink-safe path resolution
  tui/              Bubble Tea v2 chat UI: model.go (1567 lines), blocks.go, input.go
  atomicfile/       Atomic, mode-preserving file writes
  logx/             Structured debug logging gated by env var
```

## 3. The agent loop

`internal/agent/agent.go`, function `(*Agent).run` (lines 324–453). One call to
`Send`/`SendWith` pushes a user message onto `a.messages` and calls `run`, which
iterates until termination:

```go
// internal/agent/agent.go:324
func (a *Agent) run(ctx context.Context) (string, error) {
    var finalText strings.Builder
    for turn := 0; turn < a.cfg.MaxTurns; turn++ {
        provider, model, compactor := a.backend()
        compaction, err := compactor.Compact(ctx, a.messages)   // maybe summarize old turns
        a.messages = compaction.Messages
        resp, err := provider.Complete(ctx, llm.Request{        // 1 blocking HTTP call, not streamed
            Model: model, System: a.cfg.System, SystemBlocks: a.cfg.SystemBlocks,
            Messages: a.messages, Tools: a.cfg.Tools.Specs(),
        })
        ...
        toolResults, steering := a.processResponseContent(ctx, resp.Content, &finalText)
        a.messages = append(a.messages, assistantMsg)
        if len(toolResults) > 0 {
            toolResults = a.appendSteering(toolResults, steering)
            a.messages = append(a.messages, llm.Message{Role: llm.RoleUser, Content: toolResults})
            continue   // loop again with tool_results fed back as a user message
        }
        if resp.StopReason == "end_turn" || ... { return ... }   // terminate
    }
    return ..., ErrMaxTurns  // safety fuse, default 500
}
```

**Termination** is driven by the provider's `StopReason` — `end_turn`, `stop_sequence`,
`refusal`, `max_tokens`, `pause_turn`, `model_context_window_exceeded` — plus a hard
`MaxTurns=500` fuse. Every branch is handled explicitly, and an *unknown* stop reason
(`knownStopReason`, line 455) is treated as an error rather than silently looping.

**Tool dispatch** — `processResponseContent` (line 511) walks the response's content
blocks. Text becomes events; consecutive `tool_use` blocks that are all parallel-safe
are batched and run concurrently via a semaphore-bounded pool (`executePreparedGroup`,
line 623, `MaxParallelTools` default 8). A call requiring approval acts as a serial
barrier splitting the batch.

**Transcript invariant** — the assistant message and its matching tool_results are built
but *not committed* until both are ready, "so the transcript never contains a tool_use
without its tool_result, even if a tool panics" (comment at line 363). This matters
because the Anthropic and OpenAI APIs both reject orphaned `tool_use` blocks. It is
enforced structurally, not by convention.

**Mid-turn steering** — while a turn is in flight the TUI can inject additional user text
(`Steer`, line 278), appended as extra content blocks after the current tool batch
completes rather than waiting for a fresh turn. Implemented with `steerMu`/`steerPending`
drained at defined barrier points (`executionStop`, line 714).

Every tool call carries `CallMetadata` (tool_use ID, parallel-group id/size/position)
through `context.Context`, so downstream consumers can attribute events without polluting
the tool interface.

## 4. Tool system

```go
// internal/tools/tool.go:11
type Tool interface {
    Name() string
    Spec() llm.ToolSpec
    Run(ctx context.Context, input map[string]any) (string, error)
}

// Optional. Tools that do NOT implement this are always serial (fail-closed).
type ParallelTool interface {
    ParallelSafe(input map[string]any) bool
}
```

`Registry` is a `map[string]Tool` with `Get`, `Specs()` (sorted, sent to the provider) and
`Filter(names)` (used to restrict a subagent's tool set). `ToolSpec.InputSchema` is a
hand-written `map[string]any` JSON-schema object per tool — no reflection, no codegen.

| Tool | File | Behavior |
|---|---|---|
| `bash` | `tools/bash.go` | `/bin/bash -c`, 2 min default timeout, own process group killed on cancel, output bounded to 256KB keeping head+tail |
| `read_file` | `tools/fs.go` | Whole file (256KB cap) or an offset/limit line window |
| `write_file` | `tools/fs.go` | Atomic overwrite via `atomicfile.WritePreserveMode` |
| `edit_file` | `tools/fs.go` | Exact-match `old_string`→`new_string`. **Fails** if absent or non-unique |
| `grep` | `tools/search.go` | Regex over workspace files using `os.OpenRoot` (Go 1.24+ rooted FS, symlink-escape-safe), returns structured JSON |
| `glob` | `tools/search.go` | Hand-rolled `**`-aware matcher, workspace-scoped |
| `agent` | `factory/supervisor.go` | Spawns a fresh memoryless subagent. `mode: work` (writable, serial) or `mode: inspect` (read-only, parallel-safe) |
| `workflow` | `workflow/workflow.go` | Visible todo-list/checklist tool — the TodoWrite equivalent |

**Error surfacing** — `runPreparedTool` (agent.go:764) turns a Go `error` into
`toolOutcome{text: fmt.Sprintf("error: %v\n%s", err, out), isError: true}`, which becomes
a `tool_result` block with `IsError: true`. The model sees the error inline and can retry.

**Details worth noting:**
- `edit_file`'s "exactly one occurrence" constraint (fs.go:262) is a simple, robust
  anti-ambiguity mechanism — no fuzzy patching, just `strings.Count`.
- `grep`/`glob` are the *only* path-scoped tools. `read_file`/`write_file`/`edit_file`
  are not restricted at all — see §8.
- `boundedOutput` (bash.go:95) keeps *both ends* of long output and never exceeds the
  byte cap even after inserting the truncation marker.

## 5. Provider abstraction

```go
// internal/llm/provider.go:93
type Provider interface {
    Name() string
    Complete(ctx context.Context, req Request) (*Response, error)
}
```

`Request`/`Response`/`Message`/`ContentBlock` are Anthropic-shaped — Anthropic is clearly
the reference model, and every other provider translates into that shape.

- **Anthropic** — nearly pass-through. Prompt-cache breakpoints via `SystemBlock.Cache` →
  `cache_control: {type: ephemeral}`.
- **OpenAI-compatible** (`chatcompletions`, shared by openai + openrouter) — assistant
  tool calls become `tool_calls[].function.{name,arguments}`; `tool_result` blocks become
  separate `{role: "tool", tool_call_id, content}` messages; `finish_reason == "tool_calls"`
  → `"tool_use"`, `"length"` → `"max_tokens"`.
- **OpenAI Responses / Codex** — different shape again, reassembled from SSE.
- **Google Gemini** — `generativelanguage.googleapis.com` `GenerateContent`.

**Streaming: none.** No provider streams tokens to the UI. Even the Codex client, which
uses `Accept: text/event-stream` and parses SSE, fully buffers before extracting the final
response. The comment is explicit: *"the SSE body is small enough to buffer fully — neo
presents blocking results, so there is no need to consume deltas incrementally"*
(codex.go:104). **This is the biggest UX gap in the project.**

**Retry** — a shared `internal/llm/retry` package with exponential backoff + jitter and
`Retry-After` parsing, used identically by all four clients.

## 6. Context management

- **Messages** — `[]llm.Message{Role, Content []ContentBlock, DisplayText}`. `DisplayText`
  lets the TUI show a short string (e.g. a slash command) while the LLM sees the expanded
  prompt.
- **Persistence** — one JSON file per session at `~/.neo/sessions/<id>.json`, plus an
  `index.json` summary for listing. Atomic rename, `0600` (transcripts hold file contents).
  `neo resume <id>` restores the session's original CWD.
- **Compaction** (`internal/compact/summarize.go`) — triggers when a rough token estimate
  (`chars/4`) exceeds 70% of the context window and more than `KeepRecent` (20) messages
  exist. Finds a `SafeSplitPoint` that never separates a `tool_use` from its `tool_result`,
  asks the same provider to summarize everything before it, and replaces the head with one
  synthetic user message. No sliding-window truncation anywhere — compaction is the only
  mechanism.
- **Prompt caching** — `llm.SystemBlock{Text, Cache bool}` splits the system prompt into a
  stable cacheable base (core instructions + phase/skill catalog) and a dynamic uncached
  tail (AGENTS.md), so the cache breakpoint lands after the stable part and the dynamic
  part never evicts it.
- **Project context** — `AGENTS.md`, not `CLAUDE.md`. Global `~/.neo/AGENTS.md` plus every
  `AGENTS.md` from repo root down to cwd, outermost first, symlink-escape-checked,
  concatenated under labeled `## <path>` headings.
- **Skills** — `SKILL.md` under `~/.neo/skills/<name>/` or `<repo>/.neo/skills/<name>/`,
  YAML frontmatter + Markdown body, invoked as `$name` inline or `/name args`.

## 7. TUI

Bubble Tea v2 + Bubbles v2 (`viewport`, `textarea`, `spinner`) + Lip Gloss v2 + Glamour v2.
`internal/tui/model.go` is 1567 lines and holds the whole Elm model.

**Structure** — `blocks []block` is an append-only scrollback of typed render units:
`userBlock`, `textBlock`, `toolCallBlock`, `parallelBlock`, `workflowBlock`, `treeBlock`
(subagents), `approvalBlock`, `errorBlock`. `Update` switches on message type; `View`
composes viewport + input + status line.

**No flicker from partial tokens — because there are none.** Each turn produces whole
`agent.Event`s, so rendering is block-append rather than per-character redraw. Live
activity (elapsed timers) refreshes on the `spinner.TickMsg` cadence instead.

**Async wiring is direct `p.Send()`, not a channel pump:**

```go
// internal/tui/model.go:109
p := tea.NewProgram(m, programOptions(opts)...)
ag.SetEventHandler(func(e agent.Event) { p.Send(agentEventMsg{ev: e}) })
if opts.StepEvents != nil {
    go func() { for ev := range opts.StepEvents { p.Send(stepEventMsg{ev: ev}) } }()
}
ag.SetApprover(func(ctx context.Context, req agent.ApprovalRequest) (bool, error) {
    reply := make(chan bool, 1)
    p.Send(approvalRequestMsg{req: req, reply: reply})
    select {
    case ok := <-reply: return ok, nil
    case <-ctx.Done(): return false, ctx.Err()
    }
})
```

The comment at model.go:110 notes this was deliberate — it "avoids a hand-rolled channel
pump and the back-pressure that came with it." Total agent↔TUI wiring is ~30 lines.

**Turn execution is a `tea.Cmd`:**

```go
// internal/tui/model.go:1501
func (m *model) startSend(displayText, text string, images []string) tea.Cmd {
    ctx, cancel := context.WithCancel(ctx)
    m.sendCancel = cancel
    return func() tea.Msg {
        _, err := m.ag.SendWithDisplay(ctx, text, displayText, images)
        return sendResultMsg{err: err}
    }
}
```

**Key bindings** — `ctrl+c`/`ctrl+d` quit (cancels in-flight turn first, saves session,
second press force-quits); `esc` soft-interrupts; **typing while busy + `enter` steers the
active turn** rather than queuing; `ctrl+enter` explicitly queues a follow-up;
`shift+enter`/`alt+enter`/`ctrl+j` newline; `ctrl+l` clear; `ctrl+o` expand last truncated
tool result; `/` and `!` prefixes for slash and shell commands.

**Tool display** — one-line receipts by default ("Ran go test ./...", "Edited internal/foo.go"),
expanding to a verbose card only when `output.verbose` is set. **There is no diff view** —
`edit_file` shows only `edit <path>`, never a rendered before/after.

**Parallel visualization** — `parallelBlock` is pre-allocated at final height before work
starts, so out-of-order completion of concurrent calls never reflows the display.
`treeBlock` reconstructs a subagent execution tree from the supervisor's flat event stream.

## 8. Permissions & safety

**No filesystem sandbox for file writes.** `read_file`/`write_file`/`edit_file` operate on
whatever path the model supplies, with zero root-scoping — plain `os.Open`/`os.ReadFile`.
Only `grep`/`glob` are confined to the workspace.

**Approval is a confirmation list, not a security boundary.** `approval.Matcher.Requires`
checks a user-configured `tool_approvals: []string` (exact tool names, or bash command
prefixes). The source documents it as *"literal user preferences, not a security policy"*
(matcher.go:9).

**Process-level sandboxing was deliberately punted.** A former `--permission` flag was
removed; using it now errors with *"run Neo inside a sandbox and use tool_approvals for
optional interactive confirmations"* — i.e. isolation is expected from the OS or container,
not from neo.

**Subagents do get least privilege**: `dynamicAgentTools = {bash, read_file, write_file,
edit_file, grep, glob}` for `mode:work`, `inspectAgentTools = {read_file, grep, glob}` for
`mode:inspect`. Admission is budget-capped (`MaxAgents`) and each runs under
`context.WithTimeout(ctx, MaxWall)`.

## 9. Concurrency

```go
// internal/agent/agent.go:649
outcomes := make([]toolOutcome, len(calls))
sem := make(chan struct{}, a.cfg.MaxParallelTools)
var wg sync.WaitGroup
for i, call := range calls {
    wg.Add(1)
    go func(i int, call preparedToolCall) {
        defer wg.Done()
        select {
        case sem <- struct{}{}:
            defer func() { <-sem }()
            outcomes[i] = a.runPreparedTool(ctx, call)
        case <-ctx.Done():
            outcomes[i] = toolOutcome{text: "skipped because the active turn was canceled", isError: true}
        }
    }(i, call)
}
wg.Wait()
```

Results are written by index into a pre-sized slice — no ordering races.

**Cancellation** — one `context.CancelFunc` per in-flight turn, stored on the TUI model.
The loop checks `ctx.Err()` at every barrier and, on mid-batch cancellation, emits synthetic
`tool_result`s marked *"skipped because the active turn was canceled"* for calls that never
ran — preserving the pairing invariant even on interrupt.

**Bash** runs in its own process group; on cancel or timeout `killProcessGroup` kills the
whole group, not just the parent.

**Supervisor fan-in** — the subagent event channel is buffered (256) with a non-blocking
send (`select { case s.Events <- ev: default: }`) so agents never block on a slow UI.

## 10. Config & auth

- **Config** — one YAML file, first-hit-wins: `./neo.yaml` → `~/.neo/config.yaml` →
  embedded default. No merging across levels. Keys include `provider`, `openai_auth`,
  `model`, `subagents.*`, `features.*` (tri-state `*bool`), `compaction.context_window_tokens`,
  `tool_approvals`, `output.verbose`, `phases`.
- **API keys** — plain environment variables, read directly by each provider's `New()`.
  No keychain integration.
- **OAuth** — one exception: `neo login` runs a **device-code flow** against ChatGPT and
  persists tokens to `~/.neo/auth.json` (0600). `auth.TokenSource` refreshes transparently.
- **Model switching** — `/model` at runtime, with a live-fetched list for OpenRouter
  (5s timeout, falls back to a default) and hardcoded lists elsewhere.
- Sessions record which provider/model/auth-mode produced them, so `resume` can validate
  the saved backend is still configured.

## 11. Worth copying

1. **Provider-neutral core types with per-backend adapters, no shared SDK.** Adding a fifth
   provider is a self-contained ~250-line file.
2. **Transcript-integrity discipline** — `tool_use` never exists without `tool_result`,
   enforced structurally (agent.go:464–494). The single most common source of "invalid
   request" bugs in hand-rolled loops.
3. **Runtime-owned parallelism** — `ParallelSafe` is decided by the tool, never by a flag
   the model sets, and fails closed to serial.
4. **Structured system prompt with explicit cache breakpoints** (`SystemBlock{Text, Cache}`).
5. **Mid-turn steering** as a first-class concept, distinct from cancel and from queuing.
6. **Restricted subagent tool sets by mode**, plus a session-wide cap and per-agent wall clock.
7. **Head+tail-preserving output truncation** — keeping both ends is exactly what you need
   to debug a failed command.
8. **Direct `p.Send()` instead of a hand-rolled event pump** — ~30 lines of wiring.
9. **Config rejects deprecated keys with an actionable message** rather than ignoring them.
10. **Internal developer docs mirror the code** (`docs/developer/guides/*`).

## 12. Gaps and weaknesses

1. **No filesystem sandbox for file writes** — the model can overwrite any path the OS user
   can. Documented as intentional, but it means neo alone provides no blast-radius
   containment.
2. **No token streaming anywhere**, even where the wire protocol supports it. The user sees
   nothing until the entire turn's HTTP response completes.
3. **No diff rendering.** Edits show only a path. A significant gap for reviewing what the
   model actually changed.
4. **Compaction token accounting is `chars/4`** with no real tokenizer — trigger timing can
   be meaningfully off for code-heavy transcripts, which tokenize denser than prose.
5. **Session store has no cross-process locking** — the source admits "concurrent neo
   processes can lose index updates."
6. **`grep`/`glob` reimplement solved problems** — hand-rolled `**` matcher, crude
   first-NUL-byte binary detection, no `.gitignore` awareness beyond a fixed skip list.
7. **Bus factor of one** — 272 of 274 commits from a single author, with API churn already
   (`permissions` → `tool_approvals`).
8. **`MaxTurns=500` is the only runaway guard** — no token or dollar circuit breaker.
