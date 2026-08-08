# opencode — architecture research

Source: `github.com/anomalyco/opencode` (`sst/opencode` now 301-redirects here — same repo, SST
renamed the org). Two snapshots read:

- **`v1.0.13`** (tag, 2025-11-02) — **the last release with the Go TUI**, and the primary
  source for this report.
- **HEAD** (`284214c7`, 2026-08-08) — for current vitals and to check which patterns survived.

---

## The headline: opencode deleted its Go TUI

**At HEAD, `packages/tui` contains zero `.go` files.** Bisecting across 1,087 tags puts the
cutover precisely: `v1.0.13` (2025-11-02) is the last Go-TUI release; `v1.0.14` (2025-11-03)
ships a complete rewrite in TypeScript/SolidJS on **OpenTUI**, a custom renderer with a Zig
core and TS bindings. The commit is not subtle:

```
f68374a  "DELETE GO BUBBLETEA CRAP HOORAY"  — Dax Raad, 2025-11-02
```

Project docs attribute the rewrite to "performance and capability issues" with Go+Bubbletea.
The cutover wasn't abrupt — at `v1.0.13` a full parallel TS TUI already existed in-tree
(`src/cli/cmd/tui/`, SolidJS) alongside the still-default Go binary. They built the
replacement first, then flipped the switch and deleted the Go package in one release.

**Read this carefully before drawing the wrong conclusion.** See §13 for the full argument,
but briefly: opencode's pain came overwhelmingly from running **Go and TypeScript as separate
client and server**, not from Bubble Tea itself. They kept the client/server split and
removed the language split. Meanwhile Charm's own Crush is thriving on Bubble Tea v2 at 145k
LOC with weekly releases. The lesson is *"don't split languages,"* not *"don't use Bubble
Tea"* — and a single-language, single-process Go agent has none of the constraints that made
opencode's setup expensive.

---

## 1. Vitals

| | |
|---|---|
| License | MIT |
| Created | 2025-04-30 |
| Scale | ~194,800 ★ / 24,920 forks / ~990 contributors / 1,087 tags |
| Activity | Several releases per day (`v1.0.0` → `v1.18.15` at time of research) |
| Downloads | >10.19M combined (7.8M GitHub, 2.37M npm) by 2026-01-29 |
| Distribution | curl installer, npm, Homebrew, scoop/choco, AUR, mise |

**Size across the cutover:**

| snapshot | Go | TypeScript | TSX |
|---|---|---|---|
| v1.0.13 | 44,087 LOC / 156 files (`packages/tui`: 28,119 / 101) | 39,963 / 279 | 25,390 / 132 |
| HEAD | **0** | 533,057 / 2,684 | 141,426 / 607 |

At the moment the Go TUI existed, client and server were almost exactly the same size —
~28.1k Go vs ~27.7–30.8k TS. A genuinely balanced split, not a thin client on a fat server.

**The Go module list (`packages/tui/go.mod` @ v1.0.13)** — note it ran the same beta
generation of Bubble Tea v2 we're targeting:

```
github.com/charmbracelet/bubbletea/v2  v2.0.0-beta.4
github.com/charmbracelet/bubbles/v2    v2.0.0-beta.1
github.com/charmbracelet/lipgloss/v2   v2.0.0-beta.3
github.com/charmbracelet/glamour       v0.10.0
github.com/charmbracelet/x/ansi        v0.9.3
github.com/alecthomas/chroma/v2        v2.18.0
github.com/sergi/go-diff               v1.3.2
github.com/lithammer/fuzzysearch       v1.1.8
github.com/muesli/{ansi,reflow,termenv}
github.com/BurntSushi/toml, fsnotify, google/uuid, golang.org/x/image, rsc.io/qr
github.com/sst/opencode-sdk-go         v0.1.0-alpha.8   // Stainless-generated
replace github.com/charmbracelet/x/input => ./input      // vendored/patched input driver
```

`oapi-codegen` and `go-jsonschema` are declared as tool deps but have **no active call site** —
vestigial. The real generator is **Stainless**, confirmed from generated file headers.

---

## 2. The client/server split

**Unchanged by the TUI rewrite** — both the Go TUI and the new TS one are pure HTTP+SSE
clients of the identical server. They threw away the entire client in a different language and
the server package barely moved. That's the strongest possible evidence for the boundary.

**What runs where.** The TS process owns *everything stateful*: LLM calls, session
persistence, tool execution (all real filesystem and process access happens only here),
auth, permissions, LSP/MCP/plugins. The Go binary is a **stateless rendering client** — no
file I/O, no LLM calls, no tool execution. It holds only local UI state.

**Protocol: REST + one global SSE stream.** Not JSON-RPC, not WebSocket. A Hono app under
Bun, self-documenting via `hono-openapi`:

```ts
// server.ts:1699-1721
export function listen(opts: { port: number; hostname: string }) {
  const server = Bun.serve({
    port: opts.port, hostname: opts.hostname,
    idleTimeout: 0,        // no idle timeout — required for a long-lived SSE connection
    fetch: App().fetch,
  })
  return server
}
```

Commands are plain REST. Every domain event funnels through an internal `Bus` and fans out
over **one shared SSE connection** (`server.ts:1653-1696`: `streamSSE(c, ...)` +
`Bus.subscribeAll(...)`). There's no per-session channel — clients filter by session ID
themselves.

A **second, reversed channel** lets the server push commands *into* an attached TUI (open a
dialog, show a toast) by long-poll: `GET /tui/control/next` + `POST /tui/control/response`.
So it's bidirectional, but as two independent one-way streams rather than a symmetric protocol.

### Startup and discovery: an env var, not a discovery protocol

```ts
// cli/cmd/tui.ts:104-155
const server = Server.listen({ port: args.port, hostname: args.hostname })  // port 0 = OS-assigned

const tui = Bun.embeddedFiles.find((item) => (item as File).name.includes("tui")) as File
if (tui) {
  // release: the Go binary is EMBEDDED inside the compiled Bun executable
  const binary = path.join(Global.Path.cache, "tui", tui.name)
  if (!(await Bun.file(binary).exists())) await Bun.write(binary, tui, { mode: 0o755 })
  cmd = [binary]
} else {
  // dev: build the Go TUI from source on the fly
  await $`go build -o ./dist/tui ./main.go`.cwd(dir)
}

const proc = Bun.spawn({
  cmd: [...cmd, ...flags],
  stdout: "inherit", stderr: "inherit", stdin: "inherit",
  env: { ...process.env, CGO_ENABLED: "0", OPENCODE_SERVER: server.url.toString() },
  onExit: () => { server.stop() },
})
```

No port file, no stdout parsing, no probing. The TS CLI is the **parent**: picks the port,
starts the server in-process, spawns the Go binary as a child with the URL in an env var and
stdio fully inherited. `onExit` ties server lifetime to the child. One logical process, two
OS processes.

It's also the **distribution mechanism**: `Bun.build({ compile })` produces one native
executable per platform, and the goreleaser-built Go binary is embedded via
`Bun.embeddedFiles`, self-extracting to `~/.cache/opencode/tui/` on first run. Users always
got one binary on disk that contained two binaries.

### The costs, observed

1. **A second toolchain and type system.** Zod schemas → OpenAPI 3.1 → GCS → Stainless →
   generated Go package, with its own changelog and stats file — an entire pipeline existing
   only to keep two languages' types in sync. Very plausibly the real driver of the deletion.
2. **Every UI action is an HTTP round-trip**, even on localhost.
3. **Startup latency**: spawn Bun → bind socket → spawn Go → round-trips, before first frame.
4. **Non-scoped fan-out**: every client receives every event for every session and filters
   locally.
5. **Distribution plumbing**: cross-compile 7 Go targets, cross-compile N Bun executables,
   glue each Go binary into the matching Bun executable.
6. **Cross-language debugging.** "The UI didn't update" could be a Go render bug, a Go SSE
   bug, a TS emission bug, or drift between the generated Go struct and the Zod shape.

**Their own resolution:** keep the split, drop the language boundary. The new TUI package spec
states the discipline explicitly — *"The SDK is the TUI's OpenCode boundary. Missing backend
data or operations must be added to the server API and generated SDK rather than imported from
backend implementation modules."* Same architecture, one language.

---

## 4. The agent loop

`packages/opencode/src/session/prompt.ts`. **opencode drives the outer loop itself** rather
than using the AI SDK's multi-step machinery:

```ts
// prompt.ts:226-419, composite
let step = 0
while (true) {
  const msgs = await getMessages({ sessionID, model, providerID, signal: abort.signal })
  step++
  const doStream = () => streamText({
    maxRetries: 0,               // "set to 0, we handle loop"
    stopWhen: stepCountIs(1),     // ONE model turn per streamText call
    abortSignal: abort.signal,
    tools, messages: [...system, ...MessageV2.toModelMessage(msgs)],
    model: wrapLanguageModel({ model: model.language, middleware: [...] }),
  })
  let result = await processor.process(doStream(), { count: 0, max: maxRetries })
  if (result.shouldRetry) {
    for (let retry = 1; retry < maxRetries; retry++) {
      await SessionRetry.sleep(getRetryDelayInMs(err, retry), abort.signal)  // cancellable
      result = await processor.process(doStream(), { count: retry, max: maxRetries })
      if (!result.shouldRetry) break
    }
  }
  await processor.end()
  if (!result.blocked && !result.info.error) {
    if ((await stream.finishReason) === "tool-calls") continue   // ← the whole agent loop
  }
  SessionCompaction.prune(input)
  return result
}
```

If a user sends a message while a turn is in flight, it's **queued and returns a Promise**
that resolves when the turn finishes — serializing input rather than dropping it.

### tool_use/tool_result pairing — a forced-error sweep

Keyed on the AI SDK's `toolCallId` in a per-stream table
(`toolcalls: Record<string, ToolPart>`), transitioning `pending → running → completed | error`.
The invariant is guaranteed by an unconditional sweep after the stream ends, success or not:

```ts
// prompt.ts:1300-1319
const p = await Session.getParts(assistantMsg.id)
for (const part of p) {
  if (part.type === "tool" && part.state.status !== "completed" && part.state.status !== "error") {
    await Session.updatePart({ ...part, state: { ...part.state, status: "error",
      error: "Tool execution aborted", time: { start: Date.now(), end: Date.now() } } })
  }
}
```

Any tool part left `pending`/`running` when the stream is interrupted gets force-flipped to
`error`. On replay, every tool part emits a valid `output-error`/`output-available` block —
no orphan ever reaches the next call. **This is a third distinct solution** to the same
problem: neo prevents-on-write, Crush repairs-on-read, opencode sweeps-on-turn-end.

### Doom-loop detection

```
prompt.ts:1088-1111 — if the last 3 tool parts are the same tool with byte-identical JSON
input, fire Permission.ask({ type: "doom-loop" }) and interrupt the model.
```

Simpler than Crush's hash-over-a-window, and arguably enough.

### Cancellation

One lock per session using explicit resource management — `using abort = lock(sessionID)`,
deterministically released on scope exit even on throw. The signal threads into `streamText`,
every tool's `ctx.abort`, the retry sleep, and LSP calls.

### Sub-agents: literal recursion

`tool/task.ts` creates a child `Session` with `parentID` and calls **the same
`SessionPrompt.prompt()`** against it. Full separate context window. It disables
`task`/`todowrite`/`todoread` in the child to cap fan-out (no nested sub-agents), subscribes
to the child's bus events to live-update the parent tool call's metadata, and propagates
abort. No separate orchestrator needed.

---

## 5. The edit tool — a nine-strategy cascade

The most elaborate of the four projects, and it credits its sources in a header comment
(Cline's `diff-apply` evals, Gemini CLI's `editCorrector.ts`). Not exact-only, not fuzzy-first
— a fixed-order cascade of generators, each yielding candidate substrings, tried in order
until exactly one unambiguous match is found:

1. **`SimpleReplacer`** — plain exact substring match.
2. **`LineTrimmedReplacer`** — line-by-line after `.trim()` on each line.
3. **`BlockAnchorReplacer`** — the sophisticated one (`edit.ts:213-346`). Requires ≥3 lines;
   anchors on first and last line matching exactly (trimmed), scores middle lines with a
   hand-rolled Levenshtein (`:151-167`). A single candidate needs only the anchors; multiple
   candidates require average middle similarity ≥ 0.3, best score wins.
4. **`WhitespaceNormalizedReplacer`** — collapse whitespace runs to single spaces.
5. **`IndentationFlexibleReplacer`** — strip common leading indentation from both sides.
6. **`EscapeNormalizedReplacer`** — unescape literal `\n`, `\t`, `\"`, `\\` (over-escaped model output).
7. **`TrimmedBoundaryReplacer`** — only the outer whitespace of the whole block differs.
8. **`ContextAwareReplacer`** — anchor on first/last line, accept if ≥50% of middle lines match.
9. **`MultiOccurrenceReplacer`** — yield every occurrence; powers `replaceAll`, last resort.

```ts
// edit.ts:603-640
for (const replacer of [SimpleReplacer, LineTrimmedReplacer, BlockAnchorReplacer,
     WhitespaceNormalizedReplacer, IndentationFlexibleReplacer, EscapeNormalizedReplacer,
     TrimmedBoundaryReplacer, ContextAwareReplacer, MultiOccurrenceReplacer]) {
  for (const search of replacer(content, oldString)) {
    const index = content.indexOf(search)
    if (index === -1) continue
    notFound = false
    if (replaceAll) return content.replaceAll(search, newString)
    if (index !== content.lastIndexOf(search)) continue    // ambiguous here — try next strategy
    return content.substring(0, index) + newString + content.substring(index + search.length)
  }
}
```

It never silently picks among ambiguous candidates within a strategy — it rejects and defers.
**No LSP or AST-based matching anywhere**; all string/line heuristics. LSP diagnostics are
pulled *after* a successful edit and reported back in the same tool result.

> **The most useful signal in this whole report.** opencode's own in-progress V2 engine
> (`packages/core/src/tool/edit.ts:84`) is **exact-match only**, with an explicit TODO:
>
> ```
> // TODO: Port V1 fuzzy correction strategies only after exact-edit behavior is established:
> // line-trimmed matching, block-anchor fallback, indentation correction, similarity review.
> ```
>
> The team that wrote the nine-strategy cascade treats it as separable, deferrable complexity
> when starting fresh. That directly validates starting simple and adding rungs from evidence.

---

## 6. Providers

Built on the **Vercel AI SDK** (`ai` + 19 `@ai-sdk/*` packages) plus the **models.dev**
catalog, refreshed hourly:

```ts
// provider/models.ts:72-91
const result = await fetch("https://models.dev/api.json", { signal: AbortSignal.timeout(10_000) })
if (result?.ok) await Bun.write(file, await result.text())
setInterval(() => ModelsDev.refresh(), 60 * 1000 * 60).unref()
```

Only 6 providers have bespoke loaders (`anthropic`, `opencode`, `openai`, `azure`,
`openrouter`, `vercel`); everything else works generically off models.dev metadata.

**Dynamic package installation** — provider SDKs are `bun install`-ed on demand the first
time a provider is used, not bundled. Clever for a hosted CLI supporting hundreds of models;
a supply-chain and offline liability for anything else.

**Tool-call normalization and caching in one file** (`provider/transform.ts:6-70`):

```ts
function normalizeToolCallIds(msgs: ModelMessage[]): ModelMessage[] {
  // Claude rejects tool-call IDs with characters outside [a-zA-Z0-9_-]
  ... toolCallId: part.toolCallId.replace(/[^a-zA-Z0-9_-]/g, "_")
}

function applyCaching(msgs: ModelMessage[], providerID: string) {
  const providerOptions = {
    anthropic:        { cacheControl: { type: "ephemeral" } },
    openrouter:       { cache_control: { type: "ephemeral" } },
    bedrock:          { cachePoint:   { type: "ephemeral" } },
    openaiCompatible: { cache_control: { type: "ephemeral" } },
  }
  // marks first 2 system messages and last 2 non-system messages as breakpoints
}
```

**Retry** is entirely custom (`session/retry.ts`), because they need to persist a visible
"retry" part between attempts and sleep against the cancellation signal:

```ts
export const RETRY_INITIAL_DELAY = 2000
export const RETRY_BACKOFF_FACTOR = 2
// honors Retry-After-Ms / Retry-After (seconds or HTTP-date) when present and sane (<60s)
```

---

## 7. Auth — 63 lines, total

```ts
// auth/index.ts — the entire file
export namespace Auth {
  export const Oauth = z.object({ type: z.literal("oauth"), refresh: z.string(),
                                  access: z.string(), expires: z.number() })
  export const Api = z.object({ type: z.literal("api"), key: z.string() })
  export const WellKnown = z.object({ type: z.literal("wellknown"), key: z.string(), token: z.string() })
  export const Info = z.discriminatedUnion("type", [Oauth, Api, WellKnown])

  const filepath = path.join(Global.Path.data, "auth.json")   // XDG_DATA_HOME

  export async function set(key: string, info: Info) {
    const data = await all()
    await Bun.write(filepath, JSON.stringify({ ...data, [key]: info }, null, 2))
    await fs.chmod(filepath, 0o600)
  }
  ...
}
```

Plaintext JSON, `0600`, keyed by provider ID, **no OS keychain** — the fourth project in a row
to make that choice. `WellKnown` is a third type for self-hosted providers exposing a
`/.well-known/opencode` document with a login command.

**No OAuth flow is hardcoded in core.** Two plugins auto-load unless disabled:

```ts
// plugin/index.ts:29-33
plugins.push("opencode-copilot-auth@0.0.3")
plugins.push("opencode-anthropic-auth@0.0.2")
```

The auth-method contract supports `method: "auto"` (silent polling / local redirect) and
`method: "code"` (paste it back) — keeping vendor endpoint knowledge out of core entirely.

### The Anthropic subscription OAuth mechanics

The plugin is fetched from npm at runtime, so it isn't visible in the git checkout. Its
internals show a real PKCE flow **using Claude Code's own OAuth client ID**:

```js
const CLIENT_ID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e";
async function authorize(mode) {
  const pkce = await generatePKCE();
  const url = new URL(`https://${mode === "console" ? "console.anthropic.com" : "claude.ai"}/oauth/authorize`);
  url.searchParams.set("code_challenge", pkce.challenge);
  url.searchParams.set("code_challenge_method", "S256");
  url.searchParams.set("state", pkce.verifier);
  return { url: url.toString(), verifier: pkce.verifier };
}
```

When a session authenticates this way, opencode injects *"You are Claude Code, Anthropic's
official CLI for Claude."* as the first system-prompt line and **zeroes the reported cost
client-side**. That's the mechanism behind the checked-in `PROMPT_ANTHROPIC_SPOOF`
(`session/system.ts:20-22`) — presenting the session as Claude Code so subscription
entitlement applies instead of metered API billing.

The OAuth itself is ordinary PKCE. The entitlement comes from **presenting as a different
client**. That's the precise shape of the thing, and it matches pi exactly.

---

## 8. Context management

**Parts, not strings.** A message has `role` plus a flat `parts: Part[]` — a discriminated
union of `TextPart`, `ReasoningPart`, `FilePart` (with a `SymbolSource` variant carrying an
LSP `Range`), `ToolPart` (`pending→running→completed|error`), `StepStartPart`/`StepFinishPart`
(token usage + filesystem snapshot per step), `SnapshotPart`, `PatchPart`, `AgentPart`,
`RetryPart`. This is what lets the UI render tool calls, diffs and reasoning as distinct
inline blocks instead of parsing markdown.

**Storage: plain JSON files, no DB.** One file per entity under
`~/.local/share/opencode/storage/{project,session,message,part}/`, file-locked, with a
versioned `MIGRATIONS` array that rewrites on-disk layout.

> **A nice trick:** sessions are namespaced per-project **by the repo's first commit hash**
> (`git rev-list --max-parents=0 --all`, `storage.ts:47-60`), not by working-directory path.
> The same repo maps to the same storage bucket no matter where or how many times it's
> checked out.

**Compaction is two-tier — cheap pruning before expensive summarization:**

```ts
export const PRUNE_MINIMUM = 20_000
export const PRUNE_PROTECT = 40_000
```

1. **Prune**, run unconditionally after every turn, no LLM call, essentially free: walk
   backward through tool calls and **blank the output of old ones** once more than 40k tokens
   of tool content has accumulated, only touching calls older than the most recent 20k-token
   window. Huge file reads and long grep results get erased in place; recent turns stay intact.
2. **Summarize**, only on real overflow:

```ts
// compaction.ts:33-40
export function isOverflow(input: { tokens, model }) {
  const context = input.model.limit.context
  if (context === 0) return false
  const count = input.tokens.input + input.tokens.cache.read + input.tokens.output
  const output = Math.min(input.model.limit.output, OUTPUT_TOKEN_MAX) || OUTPUT_TOKEN_MAX
  return count > context - output
}
```

For that turn only, working history becomes `[summary, "resume from where you left off"]`.
Full history stays on disk untouched.

**The tiering is the insight.** Most context bloat is stale tool output, and deleting it costs
nothing. Only summarize when that isn't enough.

**System prompt** — an Anthropic-only spoof preamble → a **model-family-specific base prompt**
chosen by substring match on model ID (`PROMPT_ANTHROPIC` for claude, `PROMPT_BEAST` for
gpt/o1/o3, `PROMPT_GEMINI`, `PROMPT_CODEX` for gpt-5) → environment (cwd, git status,
platform, date, a ripgrep-generated file tree capped at 200 entries) → AGENTS.md.

**AGENTS.md** (`system.ts:58-99`): walk up from cwd to the git worktree root collecting every
match; `LOCAL_RULE_FILES = ["AGENTS.md", "CLAUDE.md", "CONTEXT.md"]` with **first filename that
hits anywhere wins** (one family only); plus one global from `~/.config/opencode/AGENTS.md` or
`~/.claude/CLAUDE.md`; plus glob-matched extras from config. Each prefixed
`"Instructions from: <path>"` for provenance.

---

## 9. The Go TUI (v1.0.13)

101 files, ~25.3k non-test LOC. Bubble Tea v2 beta — the same generation we're targeting.

```
cmd/opencode/main.go       entrypoint: SDK client, SSE→program.Send bridge, tea.Program setup
internal/app/              App struct — client handle, session/message state (963 LOC)
internal/tui/tui.go        root model + Update/View, one 1,636-line file
internal/api/              reverse long-poll: server pushes commands into the TUI (41 LOC)
internal/components/
  chat/                    message list, editor, content-hash render cache (3,321 LOC)
  diff/                    unified + side-by-side, syntax highlighting (~1,000 LOC)
  dialog/                  theme/models/timeline/agents/session/search/help modals
internal/theme/            Theme interface + manager + JSON-loadable theme assets
internal/viewport/         custom viewport, not stock bubbles.viewport (803 LOC)
input/                     vendored/forked terminal input driver
```

### Root model and Update

They don't avoid a large `Update` — it's a ~830-line switch in a 1,636-line file. What keeps
it tractable: real state and logic live in `*app.App` (963 LOC), so `Update` is mostly
dispatch.

`Update` is a **strict priority chain**, each branch returning early:

1. An active **permission request** intercepts `enter`/`esc`/`a` directly (`:107-143`) — a
   blocking state, not routed through the modal system at all.
2. **Bash mode** (a `!`-prefixed inline shell command) intercepts input.
3. If `modal != nil`, keys forward to it.
4. Leader-key sequences.
5. Completion-dialog triggers.
6. The reverse control channel (`api.Request`), always answered via `api.Reply(...)`.
7. Generic type-switch, including SSE event cases.

Then **every message, handled or not, is unconditionally forwarded to every sub-component**
(`:898-921`), each doing its own type-switch — so spinners and cursor blinks keep ticking
regardless of which branch fired.

### The SSE → tea.Msg bridge — essentially zero glue

Because the Stainless-generated SDK already decodes SSE into typed Go values, the entire
bridge is:

```go
// cmd/opencode/main.go:144-157 — this is all of it
go func() {
  stream := httpClient.Event.ListStreaming(ctx, opencode.EventListParams{})
  for stream.Next() {
    evt := stream.Current().AsUnion()    // typed union
    program.Send(evt)                    // straight onto the Bubble Tea queue
  }
  if err := stream.Err(); err != nil { program.Send(err) }
}()
```

The trick: `tea.Msg` is `interface{}`, so the **raw SDK union type goes in directly** — no
wrapper, no adapter. `tui.go` then switches on those SDK types:
`case opencode.EventListResponseEventMessagePartUpdated:` with a nested switch on the part
union.

**Optimistic local updates** (`app.go:771-796`): sending a prompt appends the user's message
to local state *immediately*, then fires the HTTP call wrapped as a `tea.Cmd` whose only job
on success is silence — the assistant response arrives later via SSE, not as that call's
return value.

### Rendering — content-hash cache

```go
// components/chat/cache.go — the whole file
type PartCache struct { mu sync.RWMutex; cache map[string]string }
func (c *PartCache) GenerateKey(params ...any) string {
  h := fnv.New64a()
  for _, p := range params { h.Write(fmt.Appendf(nil, ":%v", p)) }
  return hex.EncodeToString(h.Sum(nil))
}
```

Used as `key := cache.GenerateKey(id, part.Text, width, files, author, isQueued)`.

**Because the key includes the full accumulated text, streaming markdown is *not* debounced or
incrementally patched** — every SSE delta produces a new key and a full Glamour re-render of
that one message. The payoff is on all the *other* frames: cursor blinks, spinner ticks,
scrolling and resizes hit the cache for every completed message and skip Glamour entirely.
Only the actively-streaming message pays.

Simpler than Crush's stable-prefix approach, and a reasonable v1 — but it is O(n) per delta on
the streaming message, which is exactly what Crush's boundary detection exists to avoid.

### Diff rendering

`internal/components/diff/diff.go` (957 LOC) hand-rolls its own unified-diff parser rather
than using `go-diff`'s object model for rendering. It computes **intraline highlighting**
between removed/added line pairs. Both layouts exist, and the choice is **width-driven, not
user-configured**:

```go
// components/chat/message.go:558-571
if width < 120 {
    formattedDiff, _ = diff.FormatUnifiedDiff(filename, patch, diff.WithWidth(width-2))
} else {
    formattedDiff, _ = diff.FormatDiff(filename, patch, diff.WithWidth(width-2))  // side-by-side
}
```

That's a nice touch worth copying — narrow terminal gets unified, wide gets side-by-side, no
setting to discover.

### Theming, keys, misc

- **`Theme` is a Go interface**, not a struct — ~35 required color methods, including **11
  diff-specific** ones (`DiffAdded`, `DiffAddedBg`, `DiffHighlightAdded`, `DiffLineNumber`,
  `DiffRemovedLineNumberBg`, …). Each returns a `compat.AdaptiveColor`. 24–30 built-in themes
  load from JSON assets. A background-color probe (`tea.BackgroundColorMsg`, skipped on WSL)
  auto-selects light/dark, with an RGB→ANSI-16 downgrade path.
- **Permission approval is not a modal** — rendered inline in the transcript and intercepted
  at the very top of `Update`. `enter`=once, `a`=always, `esc`=reject.
- **Focus is implicit** — the priority chain decides who consumes a key. No focus-manager
  abstraction, no separate focus state.
- **Keybindings come from the server's config**, string-parsed (`<leader>n`, `ctrl+left`) into
  a `CommandRegistry`, dispatched via `ExecuteCommandMsg`.
- `tea.WindowSizeMsg` rebuilds one global `layout.Current` every component reads — a single
  recomputed source of truth. Combined with width being part of the cache key, a resize forces
  exactly one full re-render pass.

---

## 10. Permissions

```ts
export const Response = z.enum(["once", "always", "reject"])
```

A blocking modal driven from the *server* — the tool call literally `await`s a Promise that
resolves when the user answers. A `Plugin.trigger("permission.ask", ...)` hook runs first so a
plugin can auto-decide headlessly before any UI appears.

**The `"always"` behavior is worth copying**: it back-fills an `approved[sessionID]` map
(wildcard-matched) and **retroactively auto-resolves every other currently-pending request the
new pattern now covers**. Approving `git *` once clears a whole backlog of queued prompts from
parallel tool calls in the same turn.

**Permissions live on the agent, not globally**, and bash is matched by **glob against the
literal command string**:

```ts
permission = { edit: "allow"|"ask"|"deny",
               bash: Record<globPattern, "allow"|"ask"|"deny">,
               webfetch?: ... }
```

### Plan/build modes are not a feature — they're two agents with different defaults

```ts
// agent/agent.ts:42-65
const defaultPermission = { edit: "allow", bash: { "*": "allow" }, webfetch: "allow" }

const planPermission = mergeAgentPermissions({
  edit: "deny",
  bash: {
    "cut*": "allow", "diff*": "allow", "du*": "allow", "file *": "allow",
    "find * -delete*": "ask", "find * -exec*": "ask", "find *": "allow",  // specific overrides broad
    "git diff*": "allow", "git log*": "allow", "git show*": "allow", "git status*": "allow",
  },
}, cfg.permission ?? {})
```

Both register identically otherwise. Switching plan→build mid-session injects a synthetic
reminder part so the model is told its constraints changed. **This is the most elegant design
in the report** — "modes" cost zero special-casing in the loop.

**Sandboxing: none.** Plain child-process spawn, no container, no seccomp, no restricted PATH.
Grepping for `sandbox|isolat|docker|container` finds nothing. `plan` is safe only because its
config denies risky patterns, not because it's incapable of running them.

---

## 11. Extensibility

- **MCP** — all three transports (stdio, streamable-HTTP with SSE fallback), via the official
  SDK. MCP tools merge into the same tool map and pass through the same plugin hooks.
- **LSP** — auto-detects and **auto-installs** language servers (`go install gopls@latest` if
  Go is present, pyright via its package manager, bundled `tsserver.js`). Every `edit`/`write`
  triggers `LSP.touchFile()` + `LSP.diagnostics()` and injects resulting errors **into that
  same tool call's output** — the model sees "you just broke the build" in the same turn.
- **Formatters** — auto-run on `File.Event.Edited`, with a project-aware availability probe:
  `prettier` only claims a file if the project actually depends on prettier.
- **Plugins** — `(input: PluginInput) => Promise<Hooks>`, `bun install`-ed on the fly. Hooks
  cover raw bus events, config mutation, registering new tools, auth, `chat.message`,
  `chat.params`, `tool.execute.before/after`, and `permission.ask`.
- **ACP** — a *third* front-end (`src/acp/`) implementing Zed's Agent Client Protocol against
  the same `Session` module. Adding a whole new client kind required no change to the core.

---

## 12. Worth copying

1. **Two-tier compaction** — free pruning of stale tool output (thresholds 20k/40k) before any
   LLM summarization. Most bloat is old tool results, and deleting them costs nothing.
2. **Plan/build as agents with different default permissions**, not a special-cased mode flag.
3. **Forced-error sweep** on any interrupted stream, guaranteeing no orphaned `tool_use`.
4. **`"always"` retroactively resolves other pending requests** it now covers.
5. **Width-based unified/side-by-side diff switch** — one `if`, no setting to discover.
6. **Content-hash render cache** — ~10 lines, and it makes every non-streaming frame free.
7. **Doom-loop detection** — 3 identical consecutive tool calls → interrupt.
8. **Post-edit LSP diagnostics injected into the same tool result.**
9. **Storage namespaced by first-commit hash**, so a repo maps consistently regardless of
   checkout path.
10. **Retry honoring `Retry-After`/`Retry-After-Ms`, sleeping against the cancel signal** —
    ~50 lines, correctly composed with cancellation.
11. **The nine-strategy edit cascade** — but see §5: their own V2 defers it. Add rungs from
    evidence, not upfront.

---

## 13. What not to copy — and the Bubble Tea question

**The client/server HTTP split is the single biggest thing not to copy, and opencode's own
history agrees.** They needed it because Go and TypeScript were genuinely separate
client/server languages, plus web/desktop/console/ACP clients. A single-binary Go agent has
none of those constraints:

- You don't need OpenAPI codegen, a Hono app, SSE fan-out or a discovery env var if the TUI
  and the agent core are both Go. They can be one process. **You still get the exact `tea.Msg`
  ergonomics of §9** by publishing from an in-process pub/sub straight into `program.Send()`.
- The reverse long-poll control channel exists purely because the processes are separate.
- The whole Stainless/oapi-codegen toolchain exists only to keep a Go client honest against TS
  Zod schemas — and by the end, even opencode had left `oapi-codegen` as a vestigial dep.
- Dynamic runtime `bun install` of provider SDKs is a supply-chain and offline liability.
- Six model-family-specific system prompts grew from supporting everything models.dev lists.
- `packages/core`'s parallel Effect-TS rewrite means two storage layers and two edit tools
  coexisting — the cost of rewriting a live OSS project, not a pattern to imitate.

### So should we still use Bubble Tea?

Yes — but the finding deserves a straight answer rather than a dismissal.

**What opencode's rewrite is evidence for:** running a Go TUI against a TypeScript server is
expensive. Type drift, a codegen pipeline, four places to look for any bug, and distribution
plumbing to glue two binaries together. That cost is real and it's what they removed.

**What it is not evidence for:** that Bubble Tea can't carry a serious coding agent. The
counter-example is decisive — **Crush is Charm's own coding agent, 145k LOC, 27k stars, weekly
releases, entirely Bubble Tea v2**, and Bubble Tea v2 was hardened *as Crush's engine* before
public release. opencode also ran Bubble Tea v2 only in **beta** (`v2.0.0-beta.4`), before the
Cursed Renderer landed.

**What we should take from it:**

- Single language, single process. That's the configuration that avoids opencode's actual pain,
  and it's what we already chose.
- Their performance complaints map to real problems that *do* have answers: their content-hash
  cache re-renders the whole streaming message on every delta (Crush's stable-prefix boundary
  fixes exactly this), and they hand-rolled a viewport rather than fixing the stock one.
- If we ever hit a genuine Bubble Tea wall, the escape hatch is the same seam that let opencode
  swap clients: a core that emits typed events and knows nothing about terminals.
