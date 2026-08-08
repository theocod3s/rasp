# rasp internals — how a coding agent actually works

A learning document. [prd.md](prd.md) says *what* rasp is; [design.md](design.md) says *how*
it's structured. This one explains *why it works at all* — the mechanics underneath, at the
level of "what's on the wire, which goroutine, what breaks if you get it wrong."

Read it top to bottom. Each section assumes the one before it.

**Vocabulary**, fixed across all rasp docs:

| Term | Meaning |
|---|---|
| **turn** | One `Send` — a user message through to the model's final answer. May contain many model calls |
| **step** | One model call plus the execution of any tools it requested. A turn is 1..N steps |
| **block** | One piece of message content: text, thinking, `tool_use`, or `tool_result` |

---

## 1. The idea that makes it work

The single most surprising thing about coding agents is how small the core is. Strip away the
TUI, the config, the providers, and the loop is about forty lines:

```go
for {
    response := callModel(conversation)      // 1. ask
    conversation = append(conversation, response)

    if response.StopReason != "tool_use" {   // 2. did it want a tool?
        return                               //    no → done
    }

    var results []Block
    for _, call := range response.ToolCalls() {
        results = append(results, execute(call))   // 3. run them
    }
    conversation = append(conversation, userMessage(results))   // 4. feed back
}                                                               // 5. loop
```

That's it. Everything else in rasp — all 1,800 lines of design doc — exists to make those
five lines *reliable*, *observable*, and *safe*.

Three consequences worth internalizing, because they explain most design decisions later:

**The model has no memory and no hands.** It cannot read a file. It cannot run a command. It
receives text, emits text, and forgets everything between calls. The entire conversation is
re-sent on every single model call. When you "continue a conversation," you are re-uploading
the whole history each time. This is why context management is not a nice-to-have — it is the
constraint the whole system is organized around.

**The model doesn't execute anything; it *requests*.** A `tool_use` block is a structured
wish. Your code decides whether to grant it. This is the *only* reason a permission system is
possible at all — there's a natural interception point between "the model asked" and "the
thing happened."

**The loop is driven by one field.** `stop_reason == "tool_use"` means keep going; anything
else means stop. The model decides when it's finished by simply not asking for another tool.
Nothing supervises it. That's also why runaway loops are possible, and why we need loop
detection (§4.6).

---

## 2. A turn, end to end

Let's trace one real turn: you type *"how many Go files are in internal?"*

### 2.1 What goes out

Every model call sends the **entire** state. Not a delta — the whole thing:

```jsonc
POST https://api.anthropic.com/v1/messages
{
  "model": "claude-opus-5",
  "max_tokens": 8192,
  "system": [
    { "type": "text",
      "text": "You are rasp, a coding agent...",
      "cache_control": {"type": "ephemeral"} },     // ← cache breakpoint
    { "type": "text",
      "text": "<project_instructions path=\"AGENTS.md\">..." }
  ],
  "tools": [
    { "name": "bash",
      "description": "Execute a shell command in the workspace root.",
      "input_schema": {
        "type": "object",
        "properties": {
          "command": {"type": "string", "description": "The command to run"},
          "timeout": {"type": "integer", "description": "Seconds; default 120"}
        },
        "required": ["command"]
      }},
    /* read, write, edit, grep, find, ls, todos ... */
  ],
  "messages": [
    {"role": "user", "content": "how many Go files are in internal?"}
  ]
}
```

Note what tools *are*: entries in a JSON array. There is no registration, no server-side
state, no handshake. You describe your tools on every request, and the model picks from that
list. Change the list between calls and the model's options change immediately.

**The descriptions and schemas are prompt text.** This is the most under-appreciated fact in
the whole system. `"description": "Execute a shell command"` is the *entire* basis on which
the model decides whether to use `bash` versus `grep`. Tool descriptions deserve the same care
as the system prompt, because they *are* the system prompt.

### 2.2 What comes back

```jsonc
{
  "stop_reason": "tool_use",
  "content": [
    {"type": "text", "text": "I'll count them."},
    {"type": "tool_use",
     "id": "toolu_01A9F3kQ...",              // ← remember this ID
     "name": "bash",
     "input": {"command": "find internal -name '*.go' | wc -l"}}
  ],
  "usage": {"input_tokens": 2841, "output_tokens": 68}
}
```

### 2.3 What goes back

You execute, then send the result as a **user** message — which feels wrong the first time,
but is correct. From the model's perspective, tool output is information arriving from the
outside world, and that's the user's role in the protocol:

```jsonc
{
  "role": "user",
  "content": [
    {"type": "tool_result",
     "tool_use_id": "toolu_01A9F3kQ...",     // ← must match exactly
     "content": "     127\n",
     "is_error": false}
  ]
}
```

Then you call again with all four messages, and the model answers: *"There are 127 Go files in
`internal`."* `stop_reason: "end_turn"`. Loop exits. One turn, two steps.

### 2.4 The invariant everything depends on

**Every `tool_use` block in the history must have a matching `tool_result` with the same ID.**

Violate it and the API returns 400. Not "the request failed" — *that message is now in your
history*, so every subsequent request in that session also fails. The session is bricked
permanently.

Now consider how easy this is to hit. The user presses Esc mid-turn. You've already persisted
the assistant message containing `tool_use`. The tool never ran. That session is dead.

The four reference projects solve it three different ways, and rasp does two of them:

| Project | Strategy |
|---|---|
| neo | **Prevent on write** — build the assistant message and its results together, commit both or neither |
| Crush | **Repair on read** — on load, inject synthetic error results for orphans, drop orphaned results |
| opencode | **Sweep on turn end** — force any tool part still `pending`/`running` to `error` |

Prevention handles cancellation. It does *not* handle `kill -9`, a panic, or a partial write.
So rasp does both — prevent on write, and repair on read:

```go
// On load: for every tool_use with no matching tool_result, synthesize one.
syntheticParts = append(syntheticParts, ToolResultPart{
    ToolCallID: tc.ID,
    Output: ToolResultError{
        Error: errors.New("tool call was interrupted and did not produce a result, " +
                          "you may retry this call if the result is still needed"),
    },
})
```

Note the error text is written *for the model to read*. It says "you may retry" because that's
the useful next action. Every string that reaches the model is prompt engineering.

---

## 3. Tool calls in depth

### 3.1 Where the schema comes from

Hand-writing JSON Schema per tool is what neo does and it rots — the schema and the struct you
unmarshal into drift apart silently. Crush's approach is better and it's what rasp uses:
define one Go struct, derive the schema from it by reflection.

```go
type EditParams struct {
    FilePath   string `json:"file_path"   description:"Absolute path to the file to edit"`
    OldString  string `json:"old_string"  description:"Exact text to replace; must be unique"`
    NewString  string `json:"new_string"  description:"Replacement text"`
    ReplaceAll bool   `json:"replace_all,omitempty" description:"Replace every occurrence"`
}

var EditTool = NewTool[EditParams]("edit", editDescription, func(
    ctx context.Context, p EditParams, tc ToolContext) (Result, error) {
    // p is already unmarshalled and validated
})
```

One struct is the schema the model sees, the unmarshal target, *and* the compile-time type.
They cannot drift. `omitempty` marks a field optional; the `description` tag is prompt text.

**MCP tools can't work this way** — their schemas arrive at runtime as arbitrary JSON Schema,
now including `$ref` and any 2020-12 keyword. So rasp's `Tool` interface returns
`map[string]any`, and the generic constructor is just *one way* to produce that:

```go
type Tool interface {
    Name() string
    Description() string
    Schema() map[string]any                              // reflection OR opaque passthrough
    Run(ctx context.Context, raw json.RawMessage, tc ToolContext) (Result, error)
}
```

One interface, two producers. That's what lets MCP tools sit in the same registry, hit the
same permission gate, and appear to the model as ordinary tools.

### 3.2 What a tool returns

```go
type Result struct {
    Content  string      // what the model sees. Always text.
    IsError  bool        // becomes is_error in the tool_result block
    Details  any         // typed payload for the UI. The model never sees this.
}
```

The split matters. `Content` is prompt text. `Details` might hold a computed diff, a file
list, an exit code — things the *TUI* renders richly but which would waste tokens if sent to
the model.

This is where pi went wrong: their tools return terminal UI components directly. Their own
analysis flags it — the tools become unusable by any other frontend. **Tools return data. The
UI decides how to draw it.** That single rule is also what makes `rasp run -p "..."` headless
mode free.

### 3.3 Errors are information, not failures

```go
// A failing command is NOT a Go error.
if errors.As(err, &exitErr) {
    return Result{
        Content: fmt.Sprintf("%s\n\nexit code %d", out, exitErr.ExitCode()),
        IsError: true,
    }, nil                        // ← nil error
}
```

A failing test, a missing file, a compile error — these are *observations the model needs*.
Return them as `Result{IsError: true}` and the model reads the message and adapts. Return a Go
`error` and you've turned a recoverable situation into a dead turn.

Reserve the Go `error` return for "this tool could not run at all."

### 3.4 The edit tool: why it's the hard one

Editing is where agents fail most, because the model must reproduce existing text *exactly*
to say where the change goes. Whitespace drift, smart quotes, a tab-vs-spaces mismatch — any
of these breaks a naive exact match.

The industry converged on **exact-string replace**, not diffs. Models are strong at
reproducing text they just read and weak at computing line numbers or `@@` hunk metadata,
which requires counting. Exact-match also fails *detectably*: zero matches means nothing
happened, more than one means ambiguous. Both are recoverable. A fuzzy patch that applies to
the wrong location is not.

rasp uses Crush's four-rung ladder:

```
1. Exact match.  >1 occurrence without replace_all → hard error asking for more context.
                 Never "first match wins."
2. Zero matches → retry with whitespace normalized, accepting WHOLE-LINE-ALIGNED matches only.
3. On a normalized match → re-indent the replacement to the file's detected indent unit,
                 and TELL THE MODEL the match wasn't byte-exact so it can verify.
4. Still nothing → line-similarity scan, print the file's actual content at the closest
                 location with whitespace visualized (→ for tab, · for space).
```

Rung 4 is the one people skip, and it's the highest-value one. "Not found" makes the model
guess again. Showing it what's *actually* there lets it fix its own input.

Note the boundary: **no Levenshtein, no approximate character matching.** "Fuzzy" means
whitespace only. Approximate matching is how you silently corrupt a file.

> Worth knowing: opencode has a **nine**-strategy cascade — and their own V2 rewrite ships
> exact-match-only with a TODO to port the rest "only after exact-edit behavior is
> established." The team that wrote the elaborate version defers it when starting fresh. Add
> rungs from evidence, not upfront.

### 3.5 Shell commands: five ways to get it wrong

The naive version:

```go
out, err := exec.Command("bash", "-c", cmd).CombinedOutput()   // wrong
```

**(a) `CommandContext` doesn't kill what you think.** A documented, unresolved Go limitation
([#21135](https://github.com/golang/go/issues/21135)): it kills only the direct child. Run
`bash -c "npm run dev &"` and cancel — bash dies, the dev server holds port 3000 forever.

```go
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}   // child leads its own group
cmd.Cancel = func() error {
    return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)   // negative PID = whole group
}
cmd.WaitDelay = 2 * time.Second   // don't hang if a grandchild holds the pipe open
```

`Cmd.Cancel` and `Cmd.WaitDelay` exist (since Go 1.20) precisely for this.

**(b) Split pipes lose ordering.** Point stdout and stderr at the *same* writer and the OS
preserves chronological order for free. Two pipes read by two goroutines gives you all the
stdout then all the stderr — and a compiler error no longer sits next to the file it came
from.

**(c) Unbounded output.** `cat` a 2MB log and you've blown the context window. Cap it — and
keep **both ends**. The head has the command echo; the tail has the actual error. Truncating
either destroys the diagnosis.

**(d) No live feedback.** A 90-second test run showing nothing looks like a hang. But one UI
event per line floods the renderer. Throttle at the source (pi polls a shared buffer every
100ms; Crush debounces at the persistence layer at 33ms).

**(e) No gate.** The permission check happens *before* execution, and rejection ends the turn
rather than just the tool — otherwise the model retries the thing you just refused.

---

## 4. Streaming

### 4.1 Why it isn't optional

Non-streaming and streaming take the same wall-clock time. They feel completely different:

```
No streaming (neo)            Streaming
> fix the bug                 > fix the bug
[ 8 seconds of nothing ]      Looking at the auth middleware,
                              the issue is that Check() doesn't
"Looking at the auth          propagate the context…
middleware, ..."                ↑ starts at 400ms
  ↑ all at once at 8s
```

The second one feels instant because you start reading immediately. The first feels broken —
you can't tell thinking from hung. neo's own report calls this its biggest gap.

It's also brutal to retrofit, because it changes three layers at once: the provider interface
(one call → an event sequence), the event model, and the render strategy (append a finished
block → mutate the last block 30×/second).

### 4.2 The two contracts

rasp takes both from pi verbatim.

**Every event carries the full accumulated message, not just the delta.**

To see why this matters, look at what a stream actually delivers. Here's Anthropic sending
*"I'll read it."* followed by a tool call:

```
content_block_start   index=0  type=text
content_block_delta   index=0  text_delta="I'll"
content_block_delta   index=0  text_delta=" read"
content_block_delta   index=0  text_delta=" it."
content_block_stop    index=0
content_block_start   index=1  type=tool_use  name=read  id=toolu_01A9
content_block_delta   index=1  input_json_delta="{\"pa"
content_block_delta   index=1  input_json_delta="th\": \"au"
content_block_delta   index=1  input_json_delta="th.go\"}"
content_block_stop    index=1
message_delta         stop_reason=tool_use
```

**Fragments**, tagged with a block index, where the tool arguments are one JSON object split at
arbitrary byte boundaries. `{"pa` is not valid JSON. Neither is `th": "au`.

Somebody has to glue that back together. **The contract is about who** — and the answer is the
provider layer, never the consumer:

```go
type Event struct {
    Type    EventType
    Delta   string     // just the new text — convenient, never authoritative
    Partial *Message   // the FULL message so far. ALWAYS populated.
    // ...
}
```

If events carried only `Delta`, every consumer would need a block table, a partial-JSON buffer
per block, and knowledge of when a fragment stream becomes parseable. Worse, **OpenAI's wire
shape is different** — tool calls arrive as a `tool_calls[]` array where the function name
appears only on the first fragment, and there's no per-block stop event at all. So each
consumer would grow a branch per provider, and the provider abstraction would have leaked into
the view layer, which is the one thing it exists to prevent.

Instead the UI is one line:

```go
case agent.Event:
    m.current = ev.Partial      // that's the entire UI logic
```

No accumulation state anywhere above the provider. The UI is a pure function of the last event
it saw, so resize, re-render and replay-from-log are all trivially correct.

> **"Isn't sending the whole message every delta wasteful?"** No — `Partial` is a *pointer*.
> The provider mutates one message in place and hands back the same address each time. Eight
> bytes per event, no copying. The contract costs nothing; it only decides where complexity is
> allowed to live.

**In practice both SDKs do the gluing for us**, which means what we write per provider is the
*projection*, not the reassembly:

```go
acc := anthropic.Message{}      // or openai.ChatCompletionAccumulator{}
msg := &Message{}               // OUR neutral message — allocated once, mutated in place

for stream.Next() {
    acc.Accumulate(stream.Current())   // openai: acc.AddChunk(...)
    project(msg, &acc)                 // ← the only part we write: their shape → ours
    yield(Event{Type: …, Delta: …, Partial: msg})
}
```

Roughly 60 lines per provider family. Note `msg` is allocated *outside* the loop — that's what
makes `Partial` a stable pointer rather than a fresh allocation per token.

**The stream never returns a Go error for model failures.**

```go
type StreamResponse = iter.Seq[Event]   // no (Event, error)

// Failures arrive as a terminal Event{Type: EventError, StopReason: ...}
```

One error path instead of two. Which is exactly why the retry classifier can be a pure
function over a message rather than a tangle of `catch` blocks at every call site.

### 4.3 Getting deltas into a Bubble Tea UI

Bubble Tea's rule: **only `Update` mutates the model.** A goroutine writing model fields
directly is a data race, because `View` reads concurrently.

The sanctioned bridge is `Program.Send`, which is safe from any goroutine:

```go
// One goroutine drains agent events into the UI's mailbox.
go func() {
    for ev := range agentEvents {
        program.Send(ev)          // tea.Msg is interface{} — send the event directly
    }
}()

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch ev := msg.(type) {
    case agent.Event:
        m.current = ev.Partial    // just re-render state
        return m, nil
    }
}
```

neo's comment notes they chose this over a hand-rolled channel pump specifically to escape
backpressure problems. Their entire agent↔TUI wiring is ~30 lines.

The turn itself runs as a `tea.Cmd` — Bubble Tea's own goroutine-per-command mechanism — so
`Update` never blocks:

```go
func (m *Model) startTurn(text string) tea.Cmd {
    ctx, cancel := context.WithCancel(context.Background())
    m.cancel = cancel                        // Esc calls this
    return func() tea.Msg {
        err := m.agent.Send(ctx, text)
        return turnDoneMsg{err: err}
    }
}
```

### 4.4 The markdown problem

Glamour renders *complete* documents. Mid-stream you constantly have half-written syntax — an
unclosed ` ``` ` fence, a table with one row. Feed that to Glamour and it flickers or renders
garbage.

Two obvious fixes, both bad: re-render everything per token is O(n²); render plain then
pretty-print at the end loses live formatting.

Crush's answer — find the longest prefix you can **prove** has no markdown construct open
(after a blank line, no open fence, list, table, quote or setext header), render it once, cache
it, and re-render only the small unstable tail:

```
│ Here's the fix:                 │
│                                 │  ← stable prefix: rendered ONCE, cached
│ ```go                           │
│ func Check(ctx context.Context) │
│ ```                             │
├─────────────────────────────────┤  ← provably-safe boundary
│ Now let me also upda            │  ← unstable tail: re-rendered each frame
```

Their comment names the subtlety: *"Two renders concatenated are NOT generally equal to a
single render of the whole document — glamour's wrap state is reset between calls."* So the
check is conservative and falls back to a full render whenever unsure.

Two gotchas that cost real time otherwise: Glamour's `TermRenderer` is expensive to construct
(memoize per width), and **it is not reentrant** (guard it with a mutex).

### 4.5 Not re-rendering everything, every frame

A 200-message conversation re-rendered on every cursor blink is unusable. Two caches:

- **Per-item render cache keyed by width.** Cache hit returns the string; miss re-renders.
- **A freeze flag.** Once a message is `Finished()`, it can never change — return it verbatim
  forever without calling `Render()` at all.

opencode keys its cache on a hash of `(id, full text, width, …)`, which is simpler but means
the streaming message re-renders fully on every delta. That's precisely what Crush's
stable-prefix boundary avoids — and plausibly part of why opencode found Go+Bubble Tea slow.

**What this means for one arriving token**, in a 200-message conversation. `View()` is called
and does rebuild the whole frame string — but almost none of that is real work:

| Work | Cost |
|---|---|
| 199 finished messages | **Cache hit.** Return a stored string. No markdown parsing at all |
| The streaming message's stable prefix | **Cache hit.** Reused verbatim (§4.4) |
| The streaming message's unstable tail | **Real work** — Glamour renders ~200 characters |
| Assembling the frame | String concatenation of pre-rendered pieces. Microseconds |
| Writing to the terminal | Bubble Tea diffs against the last frame; only changed lines are written |

Three layers, each removing a different cost: the frame diff means unchanged messages produce
zero terminal output, the item cache means finished messages never re-render, and the
stable-prefix boundary means even the *streaming* message only re-renders its tail. The
expensive operation — parsing and styling markdown — runs on a couple hundred characters
rather than the whole conversation.

Remove the item cache and you re-render 200 messages through Glamour and Chroma thirty times a
second, which is visibly unusable. Remove the stable-prefix boundary and the streaming message
alone becomes O(n²) as it grows.

### 4.6 Guarding the loop

Nothing supervises the model. Three cheap guards:

| Guard | Mechanism |
|---|---|
| **Truncated tool calls** | If `stop_reason == "length"`, fail *every* tool call in that message. Truncated JSON can parse *and* validate while being semantically wrong — silent file corruption |
| **Doom loops** | Hash `(tool, input, output)` per step; halt if the same signature repeats >5 times in the last 10. opencode uses a simpler rule: 3 identical consecutive calls |
| **Panic recovery** | Recover in each tool's `Run` and convert to a failed result. One bad tool returns an error instead of killing the process |

---

## 5. Configuration

Users configure a coding agent in three distinct layers that are easy to conflate.

| Layer | Answers | rasp | Claude Code | pi | opencode |
|---|---|---|---|---|---|
| **Settings** | Which model? What permissions? | `~/.config/rasp/config.json`, `.rasp/config.json` | `settings.json` | `~/.pi/agent/settings.json` | `opencode.json` |
| **Instructions** | How should it behave *in this repo*? | `AGENTS.md` | `CLAUDE.md` | `AGENTS.md` (+`CLAUDE.md`) | `AGENTS.md`, `CLAUDE.md` |
| **Tools** | What else can it do? | `.mcp.json` | `.mcp.json` | extensions (no MCP) | MCP + plugins |

### 5.1 Instructions — the `AGENTS.md` convention

A markdown file the agent reads into its system prompt. That's the whole idea. It carries the
things a model can't infer: "run `task test`, not `go test`", "this package is deprecated",
"we use table-driven tests."

Discovery walks **up** from the working directory to the repo root, collecting every file
found, outermost first — so a monorepo's root conventions and a package's local rules compose:

```
~/.config/rasp/AGENTS.md      global, lowest priority
/repo/AGENTS.md               repo-wide
/repo/services/api/AGENTS.md  most specific, wins
```

Each is wrapped with its path for provenance, so the model can tell you *which* file told it
something:

```xml
<project_instructions path="/repo/AGENTS.md">
...
</project_instructions>
```

Two details worth stealing. **Read other tools' files too** — Crush reads `CLAUDE.md`,
`GEMINI.md`, `.cursorrules` and `.github/copilot-instructions.md`, so an existing project works
with zero migration. And **handle git worktrees**: a linked worktree can otherwise load the
main repo's `AGENTS.md` twice. pi has a specific fix for this; it's a real bug.

### 5.2 Settings — precedence and secrets

Precedence, lowest to highest: **built-in defaults → global config → project config → env vars
→ CLI flags.**

The format is **JSONC** — JSON with `//` comments stripped before parsing, the same choice
Crush and opencode made. Plain JSON has nowhere to record *why* a setting is what it is, which
matters more than it sounds for a file you edit by hand months apart.

```jsonc
// .rasp/config.json
{
  "model": "claude-opus-5",
  "provider": {
    "anthropic": { "api_key": "$(op read op://vault/anthropic/key)" }
  },
  "mode": "manual",
  "permissions": { "bash": { "git diff*": "allow", "git log*": "allow" } }
}
```

That `$(...)` is deliberate. Config values support shell expansion — `$VAR`, `${VAR:-default}`,
and `$(command)` — so a user can point at 1Password, `pass`, or any secret manager, and we
build no keyring integration. All four reference projects store credentials as plaintext `0600`
files; shell expansion is strictly better than that and cheaper than a keyring.

One security rule the design doc added and I think is right: **a project config may not set
`"mode": "yolo"` or override the yolo preset.** A cloned repo that silently disables every
guardrail is an attack, not a feature.

#### Importing from other tools

If you already use one of these agents, its config is on your disk somewhere. On **first run
only**, rasp looks for those files, shows what it found, and asks once:

```
Found existing configuration:

  Claude Desktop   3 MCP servers (github, postgres, playwright)
  Claude Code      1 MCP server (sentry) · ANTHROPIC_API_KEY
  Codex            OPENAI_API_KEY

Import into rasp?  [Y/n]
```

Two design choices in there worth naming.

**It imports everything in one prompt, including API keys.** The instinct is to ask separately
about credentials — I had that instinct and it was wrong. The key is already plaintext on this
machine, in a file owned by this user, at the same permissions. Copying it into a second such
file doesn't change the threat model at all; anyone who can read rasp's config can already read
Claude's. A second prompt would be friction that buys nothing. *Showing* what will be copied is
transparency; gating it would be theatre.

**After importing, rasp reads only its own config.** This is the real advantage over reading
other products' files on every startup: no ongoing dependency on a schema someone else can
change without telling us. One-time copy, then independence.

#### Where model metadata comes from

To show a model picker, display cost, and know when to compact, rasp needs each model's context
window, pricing, and whether it supports tool calling. That comes from **models.dev** — a
community-maintained JSON catalog covering every provider — fetched on startup with ETag
revalidation and cached to disk.

The failure behaviour matters more than the happy path. On timeout, network failure or a
malformed response, it falls back to the last cached copy, and failing that to a small embedded
snapshot. It is never a startup error and never blocks the first prompt. Custom models defined
in config override the catalog.

The honest cost: correctness now depends on a third-party file. pi fetches the same catalog and
their generator carries dozens of hand-written corrections to its data.

### 5.3 MCP — tools you didn't write

rasp ships eight built-in tools — `read`, `write`, `edit`, `bash`, `grep`, `find`, `ls`, and
`todos` (a checklist the model maintains for itself; it touches no files and runs nothing).
MCP is the escape valve that makes eight acceptable: a user who needs GitHub, Postgres or
Playwright gets it without us writing anything.

```jsonc
// .mcp.json — same format Claude Code uses
{
  "mcpServers": {
    "github": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"] }
  }
}
```

MCP is JSON-RPC 2.0 over a transport. rasp implements **stdio only**: spawn the server as a
subprocess, write JSON-RPC to stdin, read from stdout. The OS gives you the connection and
lifecycle is process lifecycle.

Its tools merge into the same registry, namespaced (`mcp__github__create_issue`), and pass
through the **same permission gate**. A third-party server gets no more freedom than our own
`bash`.

Three guardrails, because MCP's failure modes are real: a **tool-count budget** (some servers
expose 40+ tools, each costing context on every request and degrading tool selection), a **hard
connect timeout** (a dead server must not hang startup), and **failures as ordinary tool
errors**.

> **Why containment matters here.** MCP revision `2026-07-28` *removed the
> `initialize` handshake entirely* — the protocol is now stateless — added a mandatory
> `server/discover` RPC, and requires a new `resultType` on every result. Two revisions in
> eight months, both breaking. So every MCP concept stays sealed in `internal/mcp/` behind
> rasp's own `Tool` interface. A spec revision becomes a dependency bump plus one package,
> never a change to the loop.

### 5.4 Modes

Three of the four modes are **permission presets** — no branch anywhere in the agent loop:

| Mode | edit/write | bash | Gate |
|---|---|---|---|
| plan | deny | read-only patterns | active |
| manual *(default)* | ask | ask | active |
| auto | allow | allow-listed, else ask | active |
| **yolo** | allow | allow | **bypassed at rung 0** |

opencode's insight: `plan` and `build` register identically and differ *only* in their default
permission map. Modes cost zero special-casing.

`yolo` is the deliberate exception — a short-circuit checked before the ladder, not a preset,
because "a matcher configuration that silently approves everything" is exactly the thing that
gets reached by accident. It's unreachable from the Shift+Tab cycle, requires explicit opt-in,
and never survives a restart.

#### Why plan mode is harder than it looks

Denying `edit` and `write` is trivial. Bash is the problem, because `git log` is exactly what
planning *needs* and `rm -rf` obviously isn't — and bash is one tool, so the decision has to
be made on the command *text*:

```
ALLOW (silent)
  search      rg*  grep*  ag*  fd*  find *
  vcs read    git status*  git log*  git diff*  git show*  git blame*
  inspect     ls*  cat*  head*  tail*  wc*  file *  stat*  tree*
  language    go list*  go doc*  go env*  go vet*  npm ls*  cargo tree*
  env         pwd  which*  echo*  env  date

ASK (more specific patterns override the broader allow above)
  find * -delete*   find * -exec*
  git checkout*  git reset*  git clean*  git push*
  go test*  go build*          ← run arbitrary code / write build artifacts
  sed -i*  perl -i*

DENY
  anything containing  >   >>   | tee

everything else → ask
```

Now the part worth actually understanding. **Glob-matching command strings is fundamentally
leaky**, because a shell writes files without any write-looking command:

```bash
echo "package main" > auth.go     # matches `echo*` → allowed → file written
cat template.go > handler.go      # matches `cat*`  → allowed → file written
```

Hence the outright ban on redirection. But even that isn't airtight — `bash -c "..."` hides
the whole command from the matcher, and so does any script you invoke.

So the honest framing, which the docs state and rasp's UI should never contradict: **plan mode
is a strong speed bump, not a proof.** It reliably stops accidents. It does not stop a
determined model, and pretending otherwise is worse than not having it, because a guarantee
people believe is more dangerous than a precaution they understand.

Note pi is *not* a reference here — it ships no plan mode and no permission gating at all
(*"No plan mode. Write plans to files, or build it with extensions"*). The references are
opencode and Claude Code.

---

## 6. Agentic workflow patterns — which ones rasp actually uses

You'll have met these as separate architectures: prompt chaining, routing, parallelization,
orchestrator-workers, evaluator-optimizer. Here's the honest mapping, because **a coding agent
is not built from these patterns** — it's built from the one they're usually contrasted with.

### 6.1 The distinction that matters

**Workflows** are systems where *you* wrote the control flow. Step 1 then step 2 then step 3.
Predictable, testable, cheap — and they require you to know the steps in advance.

**Agents** are systems where the *model* directs its own process in a loop with tools, and you
don't know ahead of time how many steps it will take.

Coding is not decomposable in advance. You don't know whether answering a question needs one
file read or forty, or whether the test will pass. So a coding agent is fundamentally the
**autonomous agent** pattern: an augmented LLM in a loop, which is exactly §1.

That's not a limitation — it's the correct choice for an unpredictable task domain. The
workflow patterns still show up, but as **small internal mechanisms**, not as the architecture.

### 6.2 Where each pattern actually appears

**Routing** — *"classify input, send it to a specialized handler."*

Real, and cheap: **model selection by job**. The main loop needs a strong model. Generating a
session title, summarizing for compaction, or classifying an error does not.

```
main turn        → claude-opus-5     (expensive, capable)
session title    → claude-haiku-4.5  (cheap, fast)
compaction       → claude-haiku-4.5
```

Phase 2 extends it: routing a task to a sub-agent with a restricted toolset.

**Prompt chaining** — *"output of step 1 becomes input to step 2."*

Appears in three places:

1. **Compaction** — summarize the old conversation with one call, then continue with
   `[summary, "resume from where you left off"]`. A genuine chain.
2. **plan → build** — plan mode produces a plan; you switch modes; the plan becomes the input
   to execution. Chained by the *user*, which is the point.
3. **Sub-agent delegation** (phase 2) — parent asks, child runs a full loop, child's final text
   becomes one `tool_result` in the parent.

**Parallelization** — *"run independent subtasks concurrently, aggregate."*

Real, and shipping in v1. The model can emit several `tool_use` blocks in one step — "read
these six files" — and rasp runs them **concurrently by default**, reads and writes alike.
This is pi's model rather than Crush's, which marks only its sub-agent tool parallel.

Parallelism is also where every correctness trap in this document lives, so it only works
because four mechanisms exist together. Remove any one and you get silent data loss, not
slowness:

1. **Per-file mutex keyed by resolved realpath.** Two concurrent edits to `auth.go` must
   serialize; an edit to `auth.go` and one to `main.go` need not. `filepath.EvalSymlinks`
   matters — `./auth.go`, `auth.go` and a symlink to it must take the *same* lock, or the
   mutex quietly protects nothing. pi's version is about 30 lines.
2. **Result reordering.** Tools finish in whatever order they finish, but `tool_result` blocks
   must be emitted in the order the model asked for them, because providers reject a mismatch
   against the `tool_use` sequence. Writing results by index into a pre-sized slice gives this
   for free; appending as they complete is the bug.
3. **A concurrency cap** — 8 here. Unbounded goroutines against a slow filesystem is its own
   failure mode.
4. **Approval as a serial barrier.** If two concurrent calls both need your permission, you
   get two prompts racing for one terminal. neo's fix is to split the batch at any call
   requiring approval: run what precedes it concurrently, wait, prompt, then continue.

Parallel *sub-agents* is a separate thing, and that's phase 2.

**Orchestrator-workers** — *"a central model breaks down a task and delegates."*

This is exactly the sub-agent / Task pattern, and it's phase 2. opencode's implementation is
literal recursion: create a child session with `parentID`, call the *same* prompt function
against it, return the child's final text as the parent's tool result. Its key property is a
**separate context window** — the parent doesn't pay for the child's exploration.

**Evaluator-optimizer** — *"one model generates, another critiques, loop until good."*

The most interesting case, because rasp gets a **degenerate but real version for free**. The
agent writes code, runs the tests, reads the failures, fixes them. That's generate → evaluate →
refine. But the evaluator isn't a critic model — it's `go test`, and the loop isn't coded, it
*emerges* because test output comes back as a `tool_result` and the model reacts.

That's usually better than an LLM critic: a compiler is a ground-truth evaluator with no
hallucination and no token cost. A real evaluator-optimizer (a second model reviewing diffs)
would be a phase-3 addition, and honestly of unproven value next to just running the tests.

### 6.3 Summary

| Pattern | In rasp? | As what |
|---|---|---|
| **Augmented LLM in a loop** | **Yes — this is the architecture** | The agent loop, §1 |
| Routing | Yes, MVP | Model selection by job (main vs title vs compaction) |
| Prompt chaining | Yes, MVP | Compaction; plan→build |
| Parallelization | Yes, MVP | Tools run concurrently by default, behind four safety mechanisms |
| Orchestrator-workers | Phase 2 | Sub-agents with restricted toolsets |
| Evaluator-optimizer | Emergent, not coded | Run tests → read failures → fix |

**The takeaway.** If you came to this expecting to assemble a coding agent out of workflow
patterns, the useful correction is: don't. Build one excellent loop, give it good tools,
manage its context carefully, and make it safe to interrupt. The patterns are lenses for
understanding pieces of what you built — not a blueprint for building it.

---

## 7. Context management

Every model call re-sends everything, so the context window is the binding constraint.

**Prompt caching** makes this affordable. Mark a breakpoint and the provider caches everything
*before* it; a cache read costs ~10% of a normal input token. But it's a **prefix match** —
one byte changed before the breakpoint invalidates everything after it.

That dictates the system prompt's structure:

```
┌─ stable, cacheable ────────────────┐
│ base instructions                  │
│ tool definitions      ← in stable order! │
├─ cache_control breakpoint ─────────┤
│ AGENTS.md content                  │
│ cwd, date, git branch, mode        │  ← volatile: AFTER the breakpoint
└────────────────────────────────────┘
```

Tool ordering matters more than it looks: an unstable order silently destroys the cache on
every request — expensive and invisible. The MCP spec now asks servers to return `tools/list`
deterministically for exactly this reason.

**Compaction** handles overflow. Never truncate — summarize. The rules:

- Trigger before you hit the wall: `used > window - reserve`.
- **Never cut between a `tool_use` and its `tool_result`.** Same invariant as §2.4.
- Carry forward *which files were read and modified*, so the agent doesn't forget what it
  already touched.
- Estimate tokens hybrid: use the *real* usage from the last assistant message, then `chars/4`
  only for messages after it. A flat `chars/4` drifts badly on code.

opencode adds a cheap tier worth copying: before summarizing, **blank the output of old tool
calls**. Most bloat is a stale 4,000-line file read from twenty turns ago. Deleting it costs
nothing and needs no model call. Only summarize when that isn't enough.

---

## 8. How sessions are stored

A session is **one JSONL file** — one JSON object per line, appended to as the turn progresses.
That's what Claude Code, pi and opencode all do, and the reasons are unglamorous but decisive:

- **Appending never rewrites the file.** A JSON blob per session means an O(n) write every
  turn; by turn 200 you're rewriting megabytes to add one message.
- **A crash leaves a valid file.** Read until the last complete line and skip a torn final one.
  There is no corrupt-file state to recover from, because a partial line is simply not a line.
- **`tail -f` and `jq` work on it.** This matters more than it sounds while you're debugging a
  loop that misbehaves once every twenty turns.
- **Resume is "read the file, replay it."** No migration, no schema, no query layer.

```jsonl
{"id":"01J8XZ4Q7K","parent_id":"","kind":"meta","model":"claude-opus-5","mode":"manual",…}
{"id":"01J8XZ4R2A","parent_id":"01J8XZ4Q7K","kind":"message","message":{"role":"user",…}}
{"id":"01J8XZ4X9C","parent_id":"01J8XZ4R2A","kind":"message","message":{"role":"assistant",…}}
```

Note `parent_id` on every entry. rasp doesn't ship conversation branching in v1 — but the field
costs one string now, and without it, adding `/fork` later means migrating everyone's history.
pi's sessions are a genuine tree for exactly this reason. Note also that a model change is its
own entry kind, not metadata: replaying a session then reproduces *which model produced which
turn*, which you'll want the first time you switch models mid-session and something changes.

### Why there's no database

The obvious objection to files is listing them: a session picker seems to need opening every
file to read its title. With 2,000 sessions that's slow.

Except that never happens, because sessions **shard by project**:

```
~/.local/share/rasp/sessions/<project-key>/20260808T024153_01J8XZ4Q7K.jsonl
```

`<project-key>` is the repo's **first commit hash** (`git rev-list --max-parents=0 --all`).
Listing reads one directory — this project's sessions, typically dozens. There is no view that
enumerates all sessions ever, so the scaling problem has no way to appear.

The first-commit hash is opencode's trick and it's better than the obvious alternative. pi keys
on a hash of the working directory, which means moving or re-cloning the repo orphans your
history. A commit hash follows the project.

Two cautionary data points for anyone tempted to add an index anyway:

- **neo built one and it has a known bug.** One JSON file per session plus a shared
  `index.json` — and neo's own source comments admit *"concurrent neo processes can lose index
  updates."* That's the real cost: an index needs coordination the append-only files don't.
- **Crush went SQLite from the start** and had to force `SetMaxOpenConns(1)` after concurrent
  sub-agent sessions caused WAL desync (`SQLITE_NOTADB`). It works, but it has its own edges.

If the picker ever crosses ~50ms, an index is purely additive later, with JSONL still
authoritative. Build it when a measurement asks for it, not before.

### The repair pass on load

Loading isn't just parsing. It's also where the §2.4 invariant gets enforced: walk the
reconstructed history, and for any `tool_use` without a matching `tool_result`, synthesize an
error result; drop any `tool_result` whose `tool_use` is missing.

This is what makes "kill the process at any point and resume" actually true rather than
aspirational — and it's why the repair belongs at the storage boundary rather than in the agent
loop. The loop should never have to think about it.

---

## 9. Where to start reading the code

When the code exists, this is the order that will make sense:

1. `internal/llm/provider.go` — the interface and the stream contract (§4.2)
2. `internal/agent/loop.go` — the loop from §1, with the invariants from §2.4 and §4.6
3. `internal/tools/tool.go` then `bash.go` — the interface, then the hardest tool (§3.5)
4. `internal/tui/model.go` — `Update`, and the `Program.Send` bridge (§4.3)
5. `internal/session/store.go` — JSONL append and the repair-on-read pass (§8)

If you can trace a keystroke through all five in under thirty minutes, the code is doing its
job. That's success criterion S9 in the PRD, and it's the one most likely to quietly fail.
