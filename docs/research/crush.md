# Crush — architecture research

Source: clone of `github.com/charmbracelet/crush` at `649c3b1f` (tag `v0.88.1`, 2026-08-07).
Two dependencies were pulled from the module cache and read directly:
`charm.land/fantasy@v0.40.0` (Charm's AI-SDK-style agent/provider engine, ~43k LOC) and
`charm.land/catwalk@v0.51.6` (Charm's model catalog).

**Why this is the most important reference:** it is Go, Bubble Tea v2, single static binary,
multi-provider — our exact stack, built by the people who wrote the TUI framework. Bubble
Tea v2 was hardened as Crush's engine before public release.

> **Framing correction.** Crush is *not* the "simple single-binary TUI" its reputation
> suggests. At 144k LOC it has grown a client/server split, a hooks engine, an Agent Skills
> implementation, three OAuth flows, and a Bash-scripted config format layered over JSON.
> Much of it is scope we should deliberately not copy — see §12.

---

## 1. Vitals

| | |
|---|---|
| LOC | **144,804 lines of Go across 592 files** (excluding `fantasy`/`catwalk`) |
| License | **FSL-1.1-MIT** (Functional Source License). Source-available, *not* OSI-approved; converts to plain MIT two years after each version's release |
| Activity | Created 2025-05-21. 27,157 ★, 2,130 forks, 614 open issues, 142 contributors. `v0.88.1` (2026-08-07) plus a nightly channel — weekly-or-faster cadence |
| Build | `CGO_ENABLED=0`, `GOEXPERIMENT=greenteagc`, `-trimpath` |
| Targets | linux/darwin/windows/freebsd/openbsd/netbsd/android × amd64/arm64/386/arm(v7) |
| Channels | Homebrew tap, Scoop, npm (`@charmland/crush`), Nix (NUR), Winget, plus generated shell completions and man pages in every archive |

**Largest packages:** `ui` 45,117 · `agent` 26,013 · `config` 11,731 · `cmd` 5,630 ·
`server` 5,476 · `backend` 4,945 · `shell` 4,791 · `workspace` 3,538 · `oauth` 2,675.

### The dependency list — a vetted shortlist for us

```
charm.land/{bubbles/v2, bubbletea/v2, lipgloss/v2, glamour/v2, log/v2, fang/v2,
            catwalk, fantasy, x/vcr}
github.com/alecthomas/chroma/v2            syntax highlighting
github.com/aymanbagabas/go-udiff           diff computation
github.com/bmatcuk/doublestar/v4           globbing
github.com/charlievieth/fastwalk           fast directory walking
github.com/charmbracelet/ultraviolet       cell-buffer screen rendering
github.com/charmbracelet/colorprofile      terminal color detection
github.com/charmbracelet/x/{ansi,editor,etag,exp/charmtone,exp/golden,powernap,term,...}
github.com/invopop/jsonschema              config schema (NOT tool schemas)
github.com/spf13/cobra                     CLI
github.com/openai/openai-go/v3             OpenAI SDK
github.com/modelcontextprotocol/go-sdk     MCP
github.com/ncruces/go-sqlite3              SQLite (WASM, CGO-free)
modernc.org/sqlite                         SQLite (pure-Go transpile)
github.com/pressly/goose/v3                migrations
github.com/sourcegraph/jsonrpc2            LSP transport
mvdan.cc/sh/v3                             cross-platform shell execution
github.com/sahilm/fuzzy                    fuzzy matching
github.com/go-git/go-git/v5, github.com/google/uuid, github.com/dustin/go-humanize
github.com/lucasb-eyer/go-colorful, github.com/rivo/uniseg, github.com/mattn/go-isatty
github.com/tidwall/{gjson,sjson}, github.com/itchyny/gojq, gopkg.in/yaml.v3
golang.org/x/{net,oauth2,sync,sys,text}, gopkg.in/natefinch/lumberjack.v2
```

Notable indirect: `github.com/charmbracelet/anthropic-sdk-go` — **Charm maintains their own
fork of the Anthropic SDK**. Also `google.golang.org/genai` and the full `aws-sdk-go-v2`.

> **Two SQLite drivers, deliberately.** `ncruces/go-sqlite3` (WASM-based) and
> `modernc.org/sqlite` (pure-Go transpile), selected per platform, both CGO-free — that's
> what keeps `CGO_ENABLED=0` true on every target. For us, one (`modernc`) is enough.

## 2. Package layout

| Package | LOC | Purpose |
|---|---|---|
| `agent` | 26,013 | `SessionAgent`, `Coordinator`, hooks decorator, prompts, built-in tools, MCP client |
| `ui` | 45,117 | The entire Bubble Tea v2 TUI — see §8 |
| `config` | 11,731 | `crush.json` / `crushrc` loading, provider+model resolution, context-file discovery |
| `backend` | 4,945 | Transport-agnostic ops consumed by the HTTP server and (planned) ACP |
| `server` / `client` / `proto` | 5,476 / 2,141 / 2,314 | HTTP API over Unix socket, matching SDK, shared wire types |
| `shell` / `shellconfig` | 4,791 / 2,412 | Cross-platform shell exec (`mvdan.cc/sh`); the Bash-powered `crushrc` format |
| `oauth` | 2,675 | Generic OAuth2 token + `copilot`, `hyper`, `mcp`, `callback` |
| `db` | 2,480 | SQLite via sqlc + goose migrations |
| `lsp` | 2,314 | LSP client manager, auto-discovery, on-demand startup |
| `message` | 2,189 | `Message`/`ContentPart` domain model, debounced update service |
| `skills` | 1,929 | Agent Skills standard + `skills/builtin` |
| `csync` | 1,568 | Generic concurrency-safe `Map`/`Slice`/`Value` used instead of raw mutexes |
| `hooks` | 1,426 | Shell-command hook engine, Claude-Code-compatible `PreToolUse` |
| `permission` | 925 | Tool-permission approval service and allow-lists |
| `pubsub` | 303 | Lightweight in-process fan-out broker, generic over payload |
| `discover` | 1,604 | Auto-discovery of local model servers (Ollama, llama.cpp, LM Studio) |
| `filetracker` / `history` / `lock` / `projects` / `question` | small | supporting services |

## 3. The agent loop

**Crush does not hand-roll an agent loop.** It wraps `charm.land/fantasy`, a general-purpose
Go "AI SDK" Charm built and open-sourced alongside it. Crush's own layer handles session
bookkeeping, persistence and cancellation; `fantasy` owns the step loop, tool dispatch and
retry.

```go
// fantasy@v0.40.0/agent.go:922 — the step loop, condensed
for stepNumber := 0; ; stepNumber++ {
    // PrepareStep hook mutates messages / model / tools for this step
    retry := RetryWithExponentialBackoffRespectingRetryHeaders[stepExecutionResult](retryOptions)
    result, err := retry(ctx, func() (stepExecutionResult, error) {
        stream, err := retryModel.Stream(ctx, streamCall)
        if err != nil { return stepExecutionResult{}, err }
        return a.processStepStream(ctx, stream, opts, steps, stepTools, stepExecProviderTools)
    })
    steps = append(steps, result.StepResult)
    if opts.OnStepFinish != nil { _ = opts.OnStepFinish(result.StepResult) }
    if isStopConditionMet(call.StopWhen, steps) || !result.ShouldContinue { break }
}
```

**Crush's `PrepareStep`** (`internal/agent/agent.go:808`) is where session work happens each
step: fold in queued follow-up prompts (`drainQueueForStep`), stamp Anthropic `cache_control`
on the system message and last two messages, and create the assistant DB row for this step.

### How tool_use/tool_result pairing is guaranteed — two layers ⭐

**Layer 1, within a turn.** Tool calls are collected into `pendingDispatches` while the
stream is consumed, and are **only dispatched after the full step's stream has drained**,
including every `OnToolCall` callback (`fantasy@v0.40.0/agent.go:1598`):

> *"Buffer dispatch until stream is fully consumed so that all OnToolCall callbacks complete
> before any tool result is written"*

**Layer 2, across turns — and this is the part neo and pi don't have.** A run can be
cancelled *mid-stream*: Escape cancels the context, `Stream` returns early, and a `tool_use`
may already be persisted with no `tool_result` ever arriving. Every LLM API rejects an
orphaned `tool_use`, which would **permanently lock the session** — every subsequent request
fails. Crush repairs this on history reload, in both directions:

```go
// internal/agent/agent.go:1662 — inject a synthetic result for every orphaned call
func syntheticToolResultsForOrphanedCalls(m message.Message, knownToolResultIDs map[string]struct{}) (fantasy.Message, bool) {
    var syntheticParts []fantasy.MessagePart
    for _, tc := range m.ToolCalls() {
        if _, hasResult := knownToolResultIDs[tc.ID]; hasResult {
            continue
        }
        syntheticParts = append(syntheticParts, fantasy.ToolResultPart{
            ToolCallID: tc.ID,
            Output: fantasy.ToolResultOutputContentError{
                Error: errors.New("tool call was interrupted and did not produce a result, you may retry this call if the result is still needed"),
            },
        })
    }
    if len(syntheticParts) == 0 { return fantasy.Message{}, false }
    return fantasy.Message{Role: fantasy.MessageRoleTool, Content: syntheticParts}, true
}
```

plus the inverse, `filterOrphanedToolResults` (agent.go:1626), which drops any `tool_result`
whose `tool_call_id` has no matching `tool_use`.

**This is a repair-on-read strategy rather than neo's prevent-on-write strategy.** Both work;
Crush's is more robust because it also survives a crash, a killed process, or a corrupted
write — anything that leaves the transcript unpaired on disk. Worth doing *both*.

### Termination — composable stop conditions

Three independent ways to stop, and the design is worth copying: **`StopWhen []StopCondition`**
means context-budget summarization and loop detection are ordinary functions registered
alongside the model's own stop reason, not special cases welded into the loop.

1. Model returns a finish reason other than tool-calls.
2. A registered `StopCondition` fires.
3. A tool result sets `StopTurn: true` (permission denial, hook halt). Crush maps this to
   `FinishReasonEndTurn` even though the raw reason was tool-calls.

Crush registers two of its own (`internal/agent/agent.go:1037`): an auto-summarization
trigger based on remaining context budget, and repeated-tool-call loop detection.

### Loop detection — ~30 lines, worth stealing

```go
// internal/agent/loop_detection.go:16 — SHA-256 signature of (tool name, input, output)
// per step; if the same signature appears >5 times in the last 10 steps, halt.
```

Neither neo nor pi has anything equivalent. neo's only runaway guard is `MaxTurns=500`.

### Cancellation

`sessionAgent.Cancel` (`agent.go:1955`) looks up a per-session `context.CancelFunc` and calls
it, *and* records a monotonic "cancel mark" so runs already dispatched but not yet active get
cancelled on entry rather than running anyway.

On the TUI side, **Escape is a two-stage cancel** (`internal/ui/model/ui.go:4169`): the first
press arms `isCanceling`, the second actually cancels. Small, effective anti-fat-finger detail.

### Retry

`fantasy@v0.40.0/retry.go` — exponential backoff (default 5s × 2^n, max 3 retries) that
respects `retry-after-ms` / `retry-after` headers including HTTP-date parsing, plus **exactly
one credential-refresh-then-retry cycle** via an `OnAuthRefresh` hook (used for expired AWS
SSO / OAuth tokens). That last part is directly relevant to us given we're shipping OAuth.

## 4. Tool system

```go
// fantasy@v0.40.0/tool.go:93
type AgentTool interface {
    Info() ToolInfo
    Run(ctx context.Context, params ToolCall) (ToolResponse, error)
    ProviderOptions() ProviderOptions
    SetProviderOptions(opts ProviderOptions)
}
```

### Schema generation: reflection over Go structs

**This corrects an assumption.** neo hand-writes `map[string]any` schemas; Crush does not.
Tools are built with a generic constructor:

```go
// fantasy@v0.40.0/tool.go:102
func NewAgentTool[TInput any](name, description string, fn ...) AgentTool
// internally: schema.Generate(reflect.TypeOf(input)) via charm.land/fantasy/schema
```

Tool authors define a Go input struct and never write JSON Schema by hand. (Note: the
`invopop/jsonschema` dependency is for the *config* schema, not tool schemas — Charm rolled
their own reflection package for tools.)

### Tool names

`bash`, `edit`, `multiedit`, `view`, `write`, `download`, `glob`, `grep`, `rg`, `ls`,
`search`, `sourcegraph`, `fetch`, `web_fetch`, `web_search`, `agenticfetch`, `diagnostics`,
`references`, `todos`, `question`, `job_kill`, `job_output`, `lsp_call_hierarchy`,
`lsp_definition`, `lsp_rename`, `lsp_replace_symbol`, `lsp_restart`, `lsp_symbols`,
`list_mcp_resources`, `read_mcp_resource`, `crush_info`, `crush_logs`, `agent` (sub-agent
dispatch), plus dynamically registered `mcp_*`.

That's ~30 tools versus pi's 7 and neo's 8 — a lot of surface.

> **The bash tool doesn't shell out.** Crush embeds a **real POSIX shell interpreter**
> (`mvdan.cc/sh/v3`) rather than calling `exec.Command("sh", "-c", ...)`. That gives
> consistent behavior on Windows without requiring bash, and it's the same interpreter that
> powers their `crushrc` config format and config shell-expansion. Worth knowing the option
> exists — though for us, `exec` with a process group (see `go-ecosystem.md` §4) is simpler
> and matches what neo and pi do.
>
> There's also a **background-job primitive**: bash can background a command and return a
> `shell_id`, with `job_output` (optionally blocking) and `job_kill` as separate tools.

### Registration, errors, parallelism

Registration is a plain `csync.Slice[fantasy.AgentTool]` on `sessionAgent`; MCP tools are
appended and removed live as servers connect.

Errors are `ToolResponse{IsError: true, Content: ...}`. Two details worth copying:
`executeSingleTool` distinguishes *critical* errors (the tool failed to run at all) from
ordinary error results, and **a panic inside any tool `Run` is recovered and converted into a
failed `ToolResponse`** (`runToolSafely`, `agent.go:867`) so one bad tool can't take down the
process.

Parallelism is per-tool opt-in via `ToolInfo.Parallel bool`, default false. The dispatcher is
a single coordinator goroutine draining a channel: parallel-marked calls launch on their own
goroutine gated by a **5-slot semaphore**; non-parallel calls run synchronously inside the
coordinator, strictly in the order the model emitted them.

> **Worth noting:** in Crush, *only the `agent` (sub-agent) tool is marked parallel.* Ordinary
> tools — bash, edit, grep, view — always run sequentially even when the model emits several
> `tool_use` blocks in one turn. Only concurrent sub-agent delegations actually run in
> parallel. That's a much more conservative default than neo (8 concurrent tools) or pi
> (parallel by default), and it's a reasonable starting point: sequential is easy to reason
> about, and parallel file reads are rarely the bottleneck.

### The edit tool — the best of the three, by a distance

`internal/agent/tools/edit.go` + `edit_whitespace.go`. A four-rung ladder:

1. **Exact match** (`findAndReplace`, edit.go:201) via `strings.Index`. If `old_string`
   appears more than once and `replace_all` isn't set, that's a hard error asking for more
   context — never a silent "first match wins."
2. **Whitespace-normalized fallback** (`findNormalizedMatches`, edit_whitespace.go:24) —
   collapse whitespace runs to single spaces line-by-line, search, but **only accept matches
   aligned on whole-line boundaries**, returning line ranges in the *original* content.
3. **Auto re-indentation** (`adaptIndentation`, edit_whitespace.go:112) — detect the file's
   indent unit (tabs vs N-space) and re-indent the replacement to match before writing, then
   **append a note to the tool result telling the model the match wasn't byte-exact** so it
   can verify.
4. **Diagnostic hint on total failure** (`diagnoseMismatch`, edit_whitespace.go:219) — find
   the closest line-based match and return a human-readable diff hint in the error, so the
   model can self-correct instead of blindly retrying.

```go
// internal/agent/tools/edit.go:201
func findAndReplace(content, old, new string, replaceAll bool) (string, bool, error) {
    if !replaceAll {
        index := strings.Index(content, old)
        if index != -1 {
            if index != strings.LastIndex(content, old) {
                return "", false, errors.New("old_string appears multiple times...")
            }
            return content[:index] + new + content[index+len(old):], false, nil
        }
    }
    if result, ok := normalizedReplace(content, old, new, replaceAll); ok {
        return result, true, nil   // whitespace-normalized, re-indented to file style
    }
    return "", false, notFoundError(content, old)
}
```

## 5. Provider abstraction

Entirely delegated to `charm.land/fantasy` — a separate ~43k LOC module.

```go
// fantasy@v0.40.0/model.go:255
type LanguageModel interface {
    Generate(context.Context, Call) (*Response, error)
    Stream(context.Context, Call) (StreamResponse, error)
    GenerateObject(context.Context, ObjectCall) (*ObjectResponse, error)
    StreamObject(context.Context, ObjectCall) (ObjectStreamResponse, error)
    Provider() string
    Model() string
}
```

**9 wire-protocol implementations** (`fantasy/providers/`): `anthropic`, `azure`, `bedrock`,
`google`, `openai`, `openaicompat` (generic base-URL override — covers Groq/DeepSeek/xAI/
Ollama/LM Studio), `openrouter`, `vercel`, `kronk` (local models).

**38 branded providers** in the catalog but only **8 transport types** — most branded
providers are `openai-compat` with a different base URL and model list. That is exactly the
architecture we chose independently, validated by two projects now (pi's 30-providers/8-APIs
split is the same shape).

They use **official SDKs, not hand-rolled HTTP**: `charmbracelet/anthropic-sdk-go` (their
fork), `openai/openai-go/v3`, `google.golang.org/genai`, `aws-sdk-go-v2`. The Anthropic
client is reused to drive Bedrock *and* Vertex — one Claude client, three deployment targets.

### The `openaicompat` pattern — directly copyable for our design ⭐

Our plan is "native Anthropic + one OpenAI-compatible adapter." Crush shows exactly how to
build the second one, and the answer is **don't reimplement the OpenAI client** — wrap the
real one and inject hook functions where compat endpoints deviate:

```go
// fantasy@v0.40.0/providers/openaicompat/openaicompat.go — the whole file is ~60 lines
func New(opts ...Option) (fantasy.Provider, error) {
    providerOptions := options{
        openaiOptions: []openai.Option{openai.WithName(Name)},
        languageModelOptions: []openai.LanguageModelOption{
            openai.WithLanguageModelPrepareCallFunc(PrepareCallFunc),
            openai.WithLanguageModelStreamExtraFunc(StreamExtraFunc),
            openai.WithLanguageModelExtraContentFunc(ExtraContentFunc),
            openai.WithLanguageModelToPromptFunc(ToPromptFunc),
        },
        objectMode: fantasy.ObjectModeTool,
    }
    return openai.New(providerOptions.openaiOptions...)
}
```

"OpenAI-compatible" becomes *the real OpenAI client plus four seams* — request preparation,
stream extras, extra content handling, prompt conversion. A provider that deviates supplies
its own hook rather than forking the client. Compare pi, which encodes the same knowledge as
a ~30-flag `OpenAICompletionsCompat` struct. Both work; Crush's is more extensible, pi's is
easier to read at a glance.

**Model catalog: `catwalk`**, Charm's own hosted registry at `https://catwalk.charm.land`,
fetched with ETag caching. Conceptually models.dev, but self-hosted. Answers our open
question about catalogs: you do need one, and both mature projects fetch rather than hardcode.

**Streaming normalization** is a Go 1.23 iterator, which is idiomatic and elegant:

```go
// fantasy@v0.40.0/model.go:190
type StreamResponse = iter.Seq[StreamPart]

// StreamPartType (model.go:132): text_start/delta/end, reasoning_start/delta/end,
// tool_input_start/delta/end, tool_call, tool_result, source, finish, error
```

Each provider's `Stream` is literally a `func(yield func(StreamPart) bool)` translating its
native chunks into that enum:

```go
// fantasy/providers/anthropic/anthropic.go:1432, trimmed
stream := a.client.Messages.NewStreaming(ctx, *params, reqOpts...)
return func(yield func(fantasy.StreamPart) bool) {
    for stream.Next() {
        chunk := stream.Current()
        switch chunk.Type {
        case "content_block_start":
            switch chunk.ContentBlock.Type {
            case "tool_use":
                yield(fantasy.StreamPart{
                    Type: fantasy.StreamPartTypeToolInputStart,
                    ID: chunk.ContentBlock.ID, ToolCallName: chunk.ContentBlock.Name,
                })
            }
        }
    }
}
```

## 6. Auth

Crush ships **real OAuth**, which makes it our best reference for that requirement.

```go
// internal/oauth/token.go:19
type Token struct {
    AccessToken  string       `json:"access_token"`
    RefreshToken string       `json:"refresh_token,omitempty"`
    ExpiresIn    int          `json:"expires_in"`
    ExpiresAt    int64        `json:"expires_at"`
    Client       *OAuthClient `json:"client,omitempty"` // endpoints captured for later refresh
}
```

- **Proactive refresh** — `IsExpired` (token.go:64) returns true at
  `max(expires_in/10, 30s)` *before* actual expiry, so a token never dies mid-request.
- **Revoked-token detection** — `TokenExchangeError.IsRefreshTokenRevoked` checks for
  `"revoked"` / `"invalid_grant"` in the body, so a dead refresh token produces a clear
  "log in again" rather than a retry loop.
- **GitHub Copilot** (`internal/oauth/copilot/`) — a real device-code flow. Plus a nice
  interop trick: it **reads the existing Copilot CLI/VS Code token store off disk**
  (`~/.config/github-copilot/apps.json`), so a user already logged in elsewhere doesn't
  re-auth.
- **Hyper** (Charm's hosted inference) — also device-code.
- **MCP OAuth** — a `golang.org/x/oauth2` token source that persists refreshed tokens back to
  disk.
- **AWS SSO** — refreshed via the `OnAuthRefresh` hook fired by `fantasy`'s retry on a 401.
- **Local callback server** (`internal/oauth/callback/page.go`) serves the browser-redirect
  landing page.

**API keys support full shell expansion**, which is a nicer answer than a keyring for a lot
of people. `internal/config/resolve.go` resolves `$VAR`, `${VAR}`, `${VAR:-default}`,
`${VAR:?message}` **and `$(command)`** — through the same embedded POSIX shell the bash tool
uses. So this works out of the box:

```json
{ "api_key": "$(op read op://vault/anthropic/key)" }
```

The config also keeps both the resolved key and the original template string, so the key can
be re-resolved after an auth failure — which is what makes `OnAuthRefresh` work for
shell-sourced credentials.

**Credential storage is plaintext JSON. No OS keyring** — `rg -i "keyring|keychain"` across
`internal/` returns zero hits. Tokens live in `~/.local/share/crush/crush.json` (global) or
`.crush/crush.json` (workspace), read and written with `gjson`/`sjson` for targeted JSON-path
access rather than full unmarshal/marshal (so a write can't clobber unrelated fields), via a
platform-specific **atomic-rename writer** so a crash mid-write can't corrupt the file.

All three projects converge on plaintext files rather than a keychain. That's worth weighing
against our `go-keyring` plan — files are simpler and behave identically headless and over
SSH — but it *is* a genuine weakness: anyone with filesystem read access gets live OAuth
refresh tokens and API keys in the clear. Shell-expansion-to-a-secret-manager is arguably the
better middle path.

> `machineid.ProtectedID` appears in the codebase but is used **only** as an anonymous
> telemetry identifier, not to encrypt anything.

**No Anthropic subscription OAuth.** Crush implements Copilot and its own product, not Claude
Pro/Max — the flow pi implements via impersonation. That's three data points now: pi does it
with a stealth hack, neo does ChatGPT device-code only, Crush skips it entirely.

## 7. Context management

```go
// internal/message/content.go:169
type Message struct {
    ID, Role, SessionID string
    Parts                []ContentPart
    Model, Provider      string
    CreatedAt, UpdatedAt int64
    IsSummaryMessage     bool
}
```

`ContentPart` is a closed interface (an `isPart()` marker) implemented by `TextContent`,
`ReasoningContent`, `ImageURLContent`, `BinaryContent`, `ToolCall`, `ToolResult`, `Finish`,
`ShellCommand`. Tool calls, results, reasoning and even bang-mode shell transcripts are all
typed parts in one ordered list, serialized as a single JSON array.

**Storage is SQLite**, not JSONL — the one place Crush diverges from pi and Claude Code.
`sqlc` for query codegen, `goose` for migrations (7 files). Schema:

```sql
sessions(id, parent_session_id, title, message_count,
         prompt_tokens, completion_tokens, cost, timestamps)
messages(id, session_id, role, parts TEXT /* JSON */, model, timestamps, finished_at)
files(id, session_id, path, content, version, ...)   -- per-session file-version history
```

plus triggers keeping `sessions.message_count` in sync, and later migrations adding
`sessions.summary_message_id` (the compaction pointer), `messages.is_summary_message`, and
`sessions.todos`. Note `parent_session_id` — sub-agent runs are real child sessions with cost
rolling up to the parent.

`parts` is a **JSON TEXT column, not normalized** — content-part polymorphism is entirely
Go-side via a type-tagged wrapper. Simple, and it means schema migrations don't chase every
new part type.

> **Operational detail worth knowing:** both drivers open with `journal_mode=WAL` and
> `busy_timeout=30000`, and the pool is forced to **`SetMaxOpenConns(1)`**. A code comment
> explains why: concurrent sub-agent sessions caused WAL desync (`SQLITE_NOTADB`) with
> multiple pooled connections. If we use SQLite with any concurrency, this is a landmine
> someone already stepped on.

**Two separate file-tracking systems, easy to conflate:**

- **`internal/history`** — a full per-session, per-path **version history**. Every edit and
  write calls `CreateVersion` storing the *complete* content (not a diff) per version. That's
  an undo/checkpoint mechanism neither neo nor pi has.
- **`internal/filetracker`** — much smaller: records only the **last-read timestamp** per
  `(session, path)`. It's the enforcement mechanism for **"you must `view` a file before you
  may `edit` it"**, plus stale-file detection (file mtime newer than last read ⇒ reject the
  edit). That second rule is a genuinely good safety idea and costs almost nothing.

**Compaction, mechanically:** `Summarize()` spins up a *separate* agent with a dedicated
`summary.md` prompt, replays history non-tool-using, and writes the result as an assistant
message flagged `IsSummaryMessage`. `session.SummaryMessageID` points at it and
`PromptTokens` resets to 0. On the next turn, `getSessionMessages` truncates history to start
*at* the summary message and **relabels its role to User**. Everything before stays on disk
but is never replayed. Simpler than pi's split-turn dual-summary machinery, and the
"everything is still on disk" property is nice.

**Compaction** is a `StopWhen` condition (`agent.go:1037`): once
`promptTokens + completionTokens` comes within a threshold of the context window — 20K buffer
above 200K windows, else 20% of the window — the step loop halts and `Summarize()` runs, then
re-queues the original prompt with a note that the session was interrupted for length.

**Prompt caching** — `cache_control: {type: "ephemeral"}` on the system prompt, the last tool
definition, and **the last 2 messages each step** (agent.go:840), gated by
`CRUSH_DISABLE_ANTHROPIC_CACHE`. Also applied for Bedrock and Vercel since both proxy Claude.

**System prompt** — Go templates via `go:embed` (`internal/agent/templates/*.md.tpl`,
e.g. `coder.md.tpl`, `task.md.tpl`), rendered with runtime data.

**Context-file discovery is unusually broad** (`internal/config/config.go:22`) and this is a
genuinely good idea:

```
.github/copilot-instructions.md, .cursorrules, .cursor/rules/,
CLAUDE.md (+ .local), GEMINI.md/gemini.md,
crush.md/CRUSH.md (all case variants, + .local),
AGENTS.md/agents.md/Agents.md
```

plus a separate global set (`~/.config/crush/CRUSH.md`, `~/.config/AGENTS.md`). Reading
*other* tools' convention files means an existing project's instructions just work with zero
migration.

**Config is dual-format**: legacy `crush.json` (902-line JSON Schema generated from
`invopop/jsonschema` struct tags) *and* `crushrc` — a **Bash script** with registered builtins
(`provider`, `model`, `mcp`, `lsp`, `permissions`, `hook`, `options`) implemented in
`internal/shellconfig/`. Both are discovered and deep-merged. See §12.

## 8. The Bubble Tea v2 TUI

The most valuable section, and Crush ships a maintainer-written architecture doc —
**`internal/ui/AGENTS.md`** (239 lines) — that answers most of it directly.

Packages: `ui/{model, chat, dialog, list, common, completions, attachments, styles,
diffview, anim, image, logo, notification, util, xchroma}`. `model` 11,243 · `chat` 11,355 ·
`dialog` 11,270 · `list` 2,252 · `diffview` 1,536 · `styles` 1,926.

### One model, not nested Elm sub-models

Their documented, deliberate choice (`internal/ui/AGENTS.md:56`):

> *"Sub-components (`Chat`, `List`, `Attachments`, `Completions`, etc.) do not participate in
> the standard Elm architecture message loop. They are stateful structs with imperative
> methods that the main model calls directly … `Chat` and `List` have no `Update` method at
> all."*

`UI` has one `Update` that is a ~2,300-line type switch — but its 89 methods are split across
12 files in the package rather than one, and dialogs get a **two-level dispatch**:
`action := m.dialog.Update(msg)` returns a typed `Action any` from a dialog stack, which a
*second*, separate switch interprets. That keeps "what a keypress does" out of the routing
switch.

Notable: they rejected the nested-sub-model pattern that most Bubble Tea tutorials teach.

### Event → UI bridge: pub/sub, not raw channels

Domain events are published on typed `pubsub.Broker[T]`s, fanned into one shared
`pubsub.Broker[tea.Msg]`, then drained by a single goroutine calling `program.Send`:

```go
// internal/app/app.go:551 — generic fan-in adapter
func setupSubscriber[T any](ctx context.Context, wg *sync.WaitGroup, name string,
    subscriber func(context.Context) <-chan pubsub.Event[T], broker *pubsub.Broker[tea.Msg]) {
    wg.Go(func() {
        subCh := subscriber(ctx)
        for {
            select {
            case event, ok := <-subCh:
                if !ok { return }
                broker.Publish(pubsub.UpdatedEvent, tea.Msg(event))
            case <-ctx.Done():
                return
            }
        }
    })
}

// internal/app/app.go:646 — the actual bridge
func (app *App) Subscribe(program *tea.Program) {
    events := app.events.Subscribe(tuiCtx)
    for {
        select {
        case <-tuiCtx.Done(): return
        case ev, ok := <-events:
            if !ok { return }
            program.Send(ev.Payload)
        }
    }
}
```

**`PublishMustDeliver`** (app.go:583) is a bounded-blocking variant reserved for terminal
events like `RunComplete` that must never be dropped by a full channel. If you build the
naive version you eventually hit "a run finished but the UI never found out" — this is the fix.

### Streaming cadence: debounce at the data layer

**This is the answer to "how do I not redraw on every token": you never send a `tea.Msg` per
token at all.** The coalescing happens at the *persistence* layer, below the UI:

```go
// internal/message/message.go:16
// defaultUpdateDebounce is the default debounce window for [Service.Update].
// Streaming deltas that arrive within the window are coalesced into a
// single SQL write and a single pubsub event. Terminal updates
// (finish/error/cancel/tool-call structural changes) bypass the
// debounce and flush synchronously.
const defaultUpdateDebounce = 33 * time.Millisecond
```

33ms is ~30fps. So token arrival rate is decoupled from SQLite write rate *and* from pubsub
event rate, before Bubble Tea's own render loop decouples it a third time. Three layers
between "token arrives" and "frame drawn" — and critically, the throttling lives where the
data does, not scattered through the UI.

### Append-only rendering: two-tier caching ⭐

**This is the single most valuable pattern in the report.** Rendering uses two independent
cache layers:

**1. List-level memo** (`internal/ui/list/list.go`) — each item cached by
`(pointer, width, version)`. The `Item` interface requires `Render(width) string`,
`Version() uint64` (a manual dirty-bit the item bumps on mutation), and `Finished() bool`.
Once `Finished()` is true the entry is **frozen** and returned verbatim forever — `Render()`
is never called again — until `Version()` bumps or it's explicitly invalidated:

```go
// internal/ui/list/list.go:308
version := rawItem.Version()
if entry != nil && entry.width == l.width && entry.version == version {
    if !entry.frozen { return entry }
    if _, suppressed := l.freezeSuppressed[rawItem]; !suppressed { return entry }
}
rendered := item.Render(l.width)   // cache miss: actually render
```

**2. Item-level render cache** (`internal/ui/chat/messages.go:166`, `cachedMessageItem`) —
each message caches its own rendered string *and* a separately-cached "prefixed" variant (the
per-line focus/selection prefix), so toggling focus doesn't re-render content, just re-prefixes.

**3. Draw-level cache** (`internal/ui/model/chat.go:120`) — `Chat.drawCache` memoizes the
*decoded* form of the last `list.Render()` output, so two consecutive frames with identical
bytes skip the ANSI re-parse that ultraviolet's cell-buffer draw does every call.

Code comments cross-reference an internal perf design doc
(`docs/notes/2026-05-12-chat-rendering-perf.md`, not shipped) by section number — a strong
signal this was a deliberate, documented effort rather than accidental optimization.

### Streaming markdown: stable-prefix incremental rendering ⭐

**This answers our open question directly, and the answer is better than either option I
proposed.** Not "re-render everything per token" (expensive) and not "plain text then final
pass" (loses live formatting). Instead — find the longest prefix provably safe to render,
cache it, and only re-render the trailing delta:

```go
// internal/ui/chat/streaming_markdown.go:63, condensed
// stablePrefix = longest literal prefix of `content` for which we've proven no
// markdown construct is left open (fence / list / table / quote / setext header).
// Only the trailing delta past that boundary gets a fresh Glamour render;
// the cached prefix render is reused verbatim.
boundary := s.findBoundaryAfter(content)      // O(delta), not O(n)
if boundary <= len(s.stablePrefix) {
    trail := content[len(s.stablePrefix):]
    return glueRenders(s.stablePrefixRender, s.renderTrailing(trail, renderer))
}
// else: promote the boundary, render only the new safe chunk, cache it.
```

The comment notes the subtlety that makes this hard: *"Two renders concatenated are NOT
generally equal to a single render of the whole document — glamour's wrap state is reset
between calls."* So the boundary check is deliberately conservative and falls back to a full
re-render whenever unsure. There's a dedicated benchmark, `BenchmarkFindBoundaryAfter`.

The cached struct is small — it only needs the width, the stable prefix, its render, and
enough state to prove nothing is open:

```go
type streamingMarkdown struct {
    width              int
    stablePrefix       string
    stablePrefixRender string
    baseFenceCount     int
    baseHasListMarker  bool
}
```

Two practical gotchas they hit, both of which we would otherwise rediscover the hard way:

- **Glamour's `TermRenderer` is expensive to construct** (it builds a Goldmark pipeline), so
  instances are memoized per width in package-level maps (`internal/ui/common/markdown.go`).
- **Glamour is not reentrant** — access is serialized behind `common.LockMarkdownRenderer`.

### Diff rendering

Own package, `internal/ui/diffview/` (1,536 LOC). `aymanbagabas/go-udiff` computes
(`udiff.Lines` + `udiff.ToUnifiedDiff` with configurable context lines) — **confirming the
library choice from the ecosystem scan** — Chroma highlights *inside* each diff line via
`internal/ui/xchroma`, Lip Gloss v2 styles it.

```go
// internal/ui/diffview/diffview.go:255
dv.edits = udiff.Lines(dv.before.content, dv.after.content)
dv.unified, dv.err = udiff.ToUnifiedDiff(
    dv.before.path, dv.after.path, dv.before.content, dv.edits, dv.contextLines)

// Fluent API:
diffview.New().Unified().Before(path, old).After(path, new).
    ContextLines(3).Style(diffview.DefaultDarkStyle()).Width(w).String()
```

**Both unified and side-by-side** layouts (`renderUnified` / `renderSplit`). Styling is a
`Style` struct with one `LineStyle{LineNumber, Symbol, Code lipgloss.Style}` per line class —
`DividerLine`, `MissingLine` (split-view padding column), `EqualLine`, `InsertLine`,
`DeleteLine`, `Filename`.

### The screen-buffer draw pipeline

Bubble Tea v2 + `charmbracelet/ultraviolet` allow implementing
`Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor` instead of only returning a string from
`View()`. Crush uses this throughout — one cell buffer per frame, components draw into
rectangular sub-regions, flattened to a string exactly once:

```go
// internal/ui/model/ui.go:2987
func (m *UI) View() tea.View {
    var v tea.View
    v.AltScreen = true
    canvas := uv.NewScreenBuffer(m.width, m.height)
    v.Cursor = m.Draw(canvas, canvas.Bounds())     // components draw into shared buffer
    content := strings.ReplaceAll(canvas.Render(), "\r\n", "\n")
    v.Content = content
    return v
}
```

`internal/ui/AGENTS.md:22` calls this **hybrid rendering**: screen-buffer components for
top-level layout, string-based components (`list.List`, `completions`) rendering to a string
that gets stamped on via `uv.NewStyledString(str).Draw(scr, rect)`. `Chat.Draw` is literally
`uv.NewStyledString(m.list.Render()).Draw(scr, area)`.

### Dialogs, focus, theming, keys

```go
// internal/ui/dialog/dialog.go:34
type Dialog interface {
    ID() string
    HandleMsg(msg tea.Msg) Action           // Action is `any`; the caller interprets it
    Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor
}
```

Managed as a stack by `Overlay`, drawn last. **Grace period** (dialog.go:53):
`graceQuietPeriod = 425ms`, `graceMaxDelay = 1500ms` — newly-opened dialogs absorb keystrokes
left over from *before* they opened, so a stray keypress can't accidentally trigger the new
dialog's action. Cheap, high-value polish.

**Dialog sizing rules** are documented as a recurring bug source (`AGENTS.md:165`): size to
the content area (`m.width - frameSize`), inset with `Padding` never `Margin`, render styled
segments individually and concatenate rather than wrapping one giant string — *an inner
style's reset code clobbers the outer color otherwise* — and clamp for small terminals.

**Focus** is a flat enum (`uiFocusNone/Editor/Main/Sidebar`), not a focus tree.

**Theming is token-based.** `styles/quickstyle.go` builds a full `Styles` struct from a
palette of *semantic* tokens (primary/secondary/accent/fgBase/bgBase/success/error/warning/
destructive); `styles/themes.go` defines concrete themes by calling `quickStyle` and
overriding only genuine differences. `ThemeForProvider(providerID)` swaps the whole theme
when the active provider changes.

**Keys** — `bubbles/v2/key`, grouped by UI area into a nested `KeyMap` struct feeding
`ShortHelp()`/`FullHelp()` for the help footer.

### Flicker, resize, large scrollback

- `list.TotalHeight()` renders *every* item to compute an exact scrollbar thumb — explicitly
  forbidden per-frame during a resize drag. The scrollbar is **hidden mid-resize** and the
  height cache warmed incrementally afterward via `list.Prewarm`
  (`resizeSettleDuration = 120ms`, `warmBatchSize = 25`).
- Chroma style/lexer construction is memoized (`common.ChromaStyle`, `xchroma.MatchLexer`);
  the docs explicitly warn against calling `chroma.MustNewStyle` / `lexers.Match` on any
  render path.
- Real benchmarks for hot paths: `BenchmarkResizeSession`, `BenchmarkStreamingThinking`,
  `BenchmarkStreamingThinkingSteadyState`, `BenchmarkFindBoundaryAfter`.
- The house rule (`AGENTS.md:9`): *"Never do IO or expensive work in `Update`; always use a
  `tea.Cmd`. Never change model state inside a command; update state in the main `Update`
  loop."*

## 9. Permissions

A pubsub-request + blocking-channel pattern — the same shape as neo's approver, but with a
six-rung ladder before it ever asks:

```go
// internal/permission/permission.go:181, trimmed
func (s *permissionService) Request(ctx context.Context, opts CreatePermissionRequest) (bool, error) {
    if s.skip.Load() { return true, nil }                                   // 1. YOLO mode
    commandKey := opts.ToolName + ":" + opts.Action
    if slices.Contains(s.allowedTools, commandKey) ||
       slices.Contains(s.allowedTools, opts.ToolName) { return true, nil }  // 2. config allowlist
    if hookApproved(ctx, opts.ToolCallID) { return true, nil }              // 3. PreToolUse hook
    if s.autoApproveSessions[opts.SessionID] { return true, nil }           // 4. session-wide
    if _, ok := s.sessionPermissions.Get(PermissionKey{...}); ok {          // 5. per (tool,action,path)
        return true, nil
    }
    respCh := make(chan bool, 1)                                            // 6. ask the user
    s.pendingRequests.Set(permission.ID, respCh)
    s.Publish(pubsub.CreatedEvent, permission)
    select {
    case <-ctx.Done(): return false, ctx.Err()
    case granted := <-respCh: return granted, nil
    }
}
```

`Grant`/`GrantPersistent`/`Deny` all route through one `resolve()` helper so concurrent
resolution attempts race safely — first caller wins, the rest are no-ops.

**Path is part of the key.** The request carries a `Path` resolved to a directory, which
becomes part of `PermissionKey{SessionID, ToolName, Action, Path}` — so "always allow writes
in `/foo`" does not implicitly cover `/bar`.

**Grants are session-scoped and in-memory only**, not persisted across restarts — only the
static `allowedTools` config list survives. That's a defensible default.

**YOLO mode** — `atomic.Bool` set by `--yolo` or a TUI toggle, skipping every check.

Crush is the only one of the three with a real permission system. neo's is a preference list;
pi's is an example extension.

## 10. Extensibility

- **MCP** — three transports (`stdio`, `sse`, `http`). Server instructions from
  `InitializeResult().Instructions` are concatenated into the system prompt wrapped in
  `<mcp-instructions>` tags. MCP tools merge into the same `[]fantasy.AgentTool` the model
  sees, with no special-casing downstream. Also native **Docker MCP Gateway** auto-detection.
- **LSP** — client manager with auto-discovery and on-demand startup, exposed both as ambient
  diagnostics and as active tools (`lsp_definition`, `lsp_rename`, `lsp_symbols`, …).
- **Sub-agents** — `internal/agent/coordinator.go` (1,622 LOC) manages named agents
  (`AgentCoder`, `AgentTask`) and exposes an `agent` tool for dispatch.
- **Skills** — the open Agent Skills standard, with built-ins including one that lets the
  model write and configure Crush's own hooks for you.
- **Hooks** — `internal/hooks/`, shell commands on lifecycle events (currently `PreToolUse`),
  matched by regex against the tool name, **explicitly Claude-Code-compatible**. They run in
  parallel but compose deterministically in config order, receive tool input via env vars
  (`$CRUSH_TOOL_INPUT_COMMAND`), and can allow/deny/rewrite the call or inject context.
  `internal/agent/hooked_tool.go` is the decorator wrapping every tool, running *before* the
  permission check.
- **Client/server** — `internal/server` exposes a Swagger-documented HTTP API over a Unix
  socket; `internal/backend` is the transport-agnostic core both the local TUI path and the
  HTTP server call into. ACP (Agent Client Protocol) support appears planned, not implemented.

## 11. Worth copying

1. **Orphaned tool-call reconciliation on history reload** (`agent.go:1626, 1662`) — the fix
   for the bug class where a cancelled turn permanently bricks a session because the provider
   API rejects unpaired `tool_use` blocks. Repair-on-read survives crashes and kills, which
   prevent-on-write does not. **Do both.**
2. **The `openaicompat` adapter pattern** — wrap the real OpenAI client and inject four hook
   functions, rather than reimplementing it. Exactly our second adapter.
3. **The `list.Item` version + frozen render cache** (`list/list.go:308`, `list/item.go:20`) —
   cheapest, highest-leverage fix for re-rendering a whole conversation every frame. A
   `Version() uint64` dirty-bit plus a `Finished() bool` freeze flag is trivial and eliminates
   nearly all wasted work on long, mostly-static scrollback.
4. **Buffer-then-dispatch tool execution** (`fantasy/agent.go:1598, 1658`) — collect all tool
   calls from a step, let every callback fire, *then* execute. Guarantees ordering and enables
   safe opt-in parallelism via one flag.
5. **`filetracker`'s read-before-edit rule** — refuse an edit to a file the session hasn't
   read, and refuse it if the file's mtime is newer than the last read. Two cheap checks that
   prevent the model clobbering changes it never saw.
6. **API keys via shell expansion**, including `$(command)` — lets a user point at 1Password
   or any secret manager without us building keyring integration.
3. **The edit tool's fallback ladder** (`tools/edit.go:201`, `edit_whitespace.go:24`) — exact →
   ambiguity check → whole-line normalized fallback with auto re-indentation → diagnostic
   hint. Removes most "edit failed, string didn't match" frustration.
4. **Stable-prefix incremental markdown rendering** (`chat/streaming_markdown.go`) — the
   answer to streaming Glamour without flicker or full re-renders.
5. **Debounced message-update service** (33ms, sync flush on terminal state) — decouples
   token arrival from DB writes and UI events.
6. **Loop detection via step-signature hashing** (`loop_detection.go`) — ~30 lines, halts
   runaway tool loops. Neither other project has this.
7. **`StopWhen []StopCondition` composable termination** — summarization triggers and loop
   detection become ordinary registered functions rather than special cases in the loop.
8. **Panic recovery per tool** (`runToolSafely`, `fantasy/agent.go:867`) — one bad tool
   returns a failed result instead of crashing the process.
9. **Dialog interface + 425ms grace period** (`dialog/dialog.go:34, 53`) — a 3-method
   interface for an overlay stack, plus absorbing stray pre-open keystrokes.
10. **Broad context-file discovery** (`config/config.go:22`) — reading `CLAUDE.md`,
    `GEMINI.md`, `.cursorrules`, `.github/copilot-instructions.md` means an existing project's
    instructions work with zero migration.
11. **Proactive OAuth refresh** at `max(expires_in/10, 30s)` before expiry, plus explicit
    revoked-refresh-token detection.
12. **Two-stage Escape cancel** — arm, then confirm.

## 12. Overkill for us

Blunt list, because Crush's size is the main trap here:

- **The client/server split** (`server`, `client`, `backend`, `proto`, Swagger codegen,
  `internal/swagger` at 4,497 generated LOC) — a full HTTP API over a Unix socket so multiple
  clients can attach to one agent. Skip entirely.
- **Dual JSON + Bash-scripted `crushrc` config** (`shellconfig/`, 2,412 LOC) — maintaining two
  formats, one of which is a Bash interpreter with registered builtins. One JSON file is fine.
- **Three OAuth device-code flows + reading Copilot's own token store** — great for a
  commercial multi-provider product; for us, one or two flows covers the value.
- **`herdr` terminal-multiplexer integration** — reporting agent state over a socket to a
  specific third-party multiplexer. Extremely niche.
- **The Agent Skills implementation** — redundant with good tool descriptions and an
  `AGENTS.md` unless you specifically want cross-tool skill portability.
- **The defensive concurrency bookkeeping in `sessionAgent.Run`** — accept-sequence numbers,
  cancel marks, per-session dispatch mutexes, `AcceptedRun` reservations. This complexity
  exists *specifically* to make client/server + queued follow-ups + concurrent cancel
  race-free. A single-process agent needs one `context.CancelFunc` per session and a busy flag.
- **Two SQLite drivers picked per platform** — pick `modernc.org/sqlite` and move on.
- **~33 tools** — pi ships 7. Start there.
