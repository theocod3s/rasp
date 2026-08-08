# pi — architecture research

Source: full clone of `https://github.com/earendil-works/pi` (history unshallowed),
HEAD `e47b8e3`, 2026-08-07. Read at source level across `packages/*/src`.

**Why this one matters:** it is the most architecturally ambitious of the three — 30+
providers, a hand-rolled TUI framework with differential rendering, tree-structured
sessions, and a deliberately radical stance on permissions. It is TypeScript, so we copy
ideas rather than code.

---

## 1. Vitals

| | |
|---|---|
| Language | TypeScript throughout (npm workspaces monorepo). ~10KB of C for a native terminal input addon on macOS/Windows |
| LOC | ~253,000 across 1,119 `.ts`/`.tsx` files. By package: `coding-agent` 58.7k, `ai` 22.4k, `tui` 16.2k, `agent` 12.4k |
| License | MIT (Mario Zechner, 2025) |
| Activity | **5,569 commits**, first commit 2025-08-09, latest 2026-08-07 — roughly 15 commits/day for a year. 301 commit authors; top two are Mario Zechner ("badlogic", LibGDX creator, 3,514) and Armin Ronacher ("mitsuhiko", Flask/Jinja2, 514) |
| Popularity | 85,296 ★ / 10,581 forks. **~1.6M weekly npm downloads** |
| Ships as | npm package `@earendil-works/pi-coding-agent` (`bin: pi`) **and** a self-contained Bun-compiled binary, with self-update |
| Version | root monorepo `0.0.3`; CLI package `0.84.1`, **254 releases** |
| Runtime | Node ≥22.19.0 |

Pre-1.0 but not a toy: durability model, protocol versioning, exact-pinned dependencies
enforced by CI script, generated shrinkwrap, extensive vitest suites.

**Dependencies are deliberately lean.** `.npmrc` sets `save-exact=true` and
`min-release-age=2` — the latter refuses same-day releases, which dodges the window in
which a compromised package is usually caught and unpublished. `packages/tui` has **two**
runtime dependencies (`marked`, `get-east-asian-width`) for 16k lines of terminal UI;
`telemetry` has zero. Also: lifecycle-script allowlist, `--ignore-scripts` everywhere,
published shrinkwrap, scheduled `npm audit`. Rare rigor for an agent project.

## 2. Package layout

```
packages/
├── ai/             @pi-ai            30+ providers, streaming, tool-calling, retries, OAuth, model catalog
├── agent/          @pi-agent-core    transport-agnostic agent loop + Agent class + experimental "harness v2"
├── coding-agent/   @pi-coding-agent  the actual CLI: tools, TUI wiring, sessions, extensions, modes
├── tui/            @pi-tui           from-scratch terminal UI framework, differential rendering
├── protocol/       @pi-protocol      CBOR wire protocol for remote sessions
├── client/         @pi-client        transport-neutral client
├── server/         @pi-server        experimental session server (Unix socket / WS)
├── session-backends/sqlite-node      SQLite session storage (alternative to JSONL)
├── telemetry/      @pi-telemetry     vendor-neutral tracing contracts, no exporter
└── evals/          @pi-evals         behavioral evals with a fake "faux" LLM provider
```

`coding-agent/src`:

```
core/     tools/, extensions/, compaction/, system-prompt.ts, session-manager.ts,
          trust-manager.ts, skills.ts, slash-commands.ts, keybindings.ts, model-registry.ts
modes/    interactive/ (TUI), rpc/ (JSONL stdio), print-mode.ts (headless), json-event.ts
cli/      arg parsing, auth flows, session picker, startup UI
server/   create-harness.ts — bridges the experimental AgentHarness into coding-agent's tools
client/   remote-session.ts
```

> **Navigation warning.** Two parallel agent-runtime stacks exist in-repo. The *shipping*
> path is `coding-agent` → `Agent` (`packages/agent/src/agent.ts`) → hand-written
> `session-manager.ts`. The *experimental* path is `AgentHarness` (`agent/src/harness/`)
> with multi-lane concurrency and durable operations — its `create()` throws
> `HarnessNotImplemented` for anything beyond construction, and its only consumer is the
> experimental server package. Don't mistake the WIP for the real implementation.

## 3. The agent loop

`packages/agent/src/agent-loop.ts` (797 lines) — the highest-signal file in the repo.

### The central architectural idea: a pure loop with callback seams

The loop is **a plain async function that owns no state**. `runLoop(context, config, emit)`
takes a context snapshot and a config full of callbacks, and emits events. A separate
stateful wrapper — the `Agent` class (`agent.ts:173`) — owns the transcript, the message
queues, and the public `subscribe()` event bus.

Every policy decision is a config callback, not loop logic:

| Callback | Decides |
|---|---|
| `transformContext` | compaction — rewrite the transcript before the call |
| `convertToLlm` | which messages the model actually sees |
| `shouldStopAfterTurn` | graceful early exit (e.g. stop before overflowing) |
| `prepareNextTurn` | hot-swap model or thinking level between turns |
| `beforeToolCall` | block or rewrite a call — **this is the permission seam** |
| `afterToolCall` | rewrite a result field-by-field |
| `getSteeringMessages` / `getFollowUpMessages` | mid-run injection |

The payoff: you can test the loop with no provider, no filesystem, and no terminal. This
is the single most transferable idea in the codebase, and it's the shape we should copy.

**`runLoop` control flow** (agent-loop.ts:155–275):

1. Outer loop keeps the session alive across "would stop, but a follow-up arrived".
2. Inner loop, one iteration per turn:
   - Drain **steering messages** queued mid-run, splice into context.
   - `streamAssistantResponse()` — one LLM call, forwarding `text_delta`/`toolcall_delta`
     events as `message_update` while mutating a partial `AssistantMessage` in place.
     This is what lets the TUI render tokens incrementally.
   - Extract `toolCall` blocks. **If `stopReason === "length"`, fail every tool call in
     the message rather than execute it** — see below.
   - Otherwise execute (parallel or sequential), append tool-result messages.
   - `prepareNextTurn` / `shouldStopAfterTurn` hooks can hot-swap model or thinking level,
     or force early exit.
3. Terminates on `stopReason` `error`/`aborted`, or when a turn produces no tool calls and
   both the steering and follow-up queues are empty.

### The truncated-tool-call guard

```ts
// packages/agent/src/agent-loop.ts:206
// A "length" stop means the output was cut off by the token limit, so
// every tool call in the message may carry truncated arguments. Fail
// them all instead of executing potentially borked calls.
const executedToolBatch =
  message.stopReason === "length"
    ? await failToolCallsFromTruncatedMessage(toolCalls, emit)
    : await executeToolCalls(currentContext, message, config, signal, emit);
```

Truncated JSON arguments can still parse *and* validate against a schema while being
semantically wrong — an `edit` call with a truncated `newText` silently corrupts a file.
Cheap to implement, easy to miss.

### Tool dispatch

`prepareToolCall` → `executePreparedToolCall` → `finalizeExecutedToolCall`.

- `prepareToolCall` validates args against the TypeBox schema, applies `prepareArguments`
  compat shims, and runs the `beforeToolCall` hook — which can **block** the call.
- Execution is **parallel by default**; a tool can declare `executionMode: "sequential"`,
  and one sequential call forces the whole batch sequential.
- Completion order and result-message order are decoupled: `tool_execution_end` fires in
  completion order, but result messages are re-emitted in original content order
  afterward — providers require tool-result ordering to match tool-call ordering.
- Tools **throw** on failure rather than encoding `isError` themselves; the loop catches
  and converts to `{content: [...], isError: true}`.
- A batch early-terminates only if **every** finalized result sets `terminate: true` —
  deliberately conservative, so one blocked call can't cut off unrelated parallel work.

### Sub-agents

**Not built in** — an explicit design choice. The reference pattern
(`examples/extensions/subagent/index.ts`) spawns a **separate `pi` child process** per task
with `--mode json -p --no-session` and parses its JSONL stdout. Supports single / parallel
(≤8 tasks, concurrency 4) / chain modes with a `{previous}` output-passing placeholder.

The experimental `AgentHarness` introduces **lanes** — named parallel execution tracks
within one session — and its docs say "a subagent tool runs on a second lane of its
parent's session". But the same docs say coding-agent migration is out of scope.

## 4. Tool system

**Exactly 7 built-in tools**: `read`, `bash`, `edit`, `write`, `grep`, `find`, `ls`.
Deliberately minimal. `createCodingToolDefinitions()` bundles `read/bash/edit/write`;
`createReadOnlyToolDefinitions()` bundles `read/grep/find/ls`.

```ts
// packages/agent/src/types.ts:386
export interface AgentTool<TParameters extends TSchema = TSchema, TDetails = any>
  extends Tool<TParameters> {
  label: string;
  prepareArguments?: (args: unknown) => Static<TParameters>;
  execute: (
    toolCallId: string,
    params: Static<TParameters>,
    signal?: AbortSignal,
    onUpdate?: AgentToolUpdateCallback<TDetails>,
  ) => Promise<AgentToolResult<TDetails>>;
  executionMode?: ToolExecutionMode; // "sequential" | "parallel"
}
```

Parameters are **TypeBox** `Type.Object(...)`, which is already JSON-Schema-shaped at
runtime — no conversion pass. The same object is the runtime validator
(`validateToolArguments`), the wire schema sent to the provider, *and* the compile-time
type via `Static<typeof schema>`. That deletes an entire layer most agents need. (Go has
no equivalent — we either hand-write schema maps like neo, or reflect over struct tags.)

### `ExecutionEnv` — every tool routes filesystem and process access through an interface

No tool ever calls `node:fs` directly. Reads, writes and process spawning go through an
injected `env` (`ExecutionEnv`). The whole `write` tool, in full:

```ts
// packages/agent/src/harness/tools/write.ts:8
const writeSchema = Type.Object({
  path: Type.String({ description: "Path to the file to write (relative or absolute)" }),
  content: Type.String({ description: "Content to write to the file" }),
});

export function createWriteTool<TContext extends ExecutionToolContext = ExecutionToolContext>() {
  return {
    name: "write",
    label: "write",
    description: "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. Automatically creates parent directories.",
    parameters: writeSchema,
    async execute(_toolCallId, { path, content }, signal, _onUpdate, { env }) {
      const absolutePath = await resolveToolPath(env, path, signal);
      return withFileMutationQueue(env, absolutePath, async () => {
        if (signal?.aborted) throw new Error("Operation aborted");
        getOrThrow(await env.writeFile(absolutePath, content, signal));
        return { content: [{ type: "text", text: `Successfully wrote ${content.length} bytes to ${path}` }] };
      });
    },
  };
}
```

This indirection is what makes their **Gondolin** extension possible: it swaps the `env`
so every built-in tool executes inside a local Linux micro-VM, while pi and the provider
credentials stay on the host — **without touching a single line of tool code**. If we want
sandboxing to ever be addable, this is the seam to build in from day one. It costs almost
nothing up front and is very expensive to retrofit.

The richer coding-agent `ToolDefinition` adds `renderCall()`/`renderResult()` returning
`pi-tui` `Component` objects — **tools own their own terminal rendering**.
`tool-definition-wrapper.ts` adapts this down to the plain `AgentTool` the loop consumes.

### The edit tool

`core/tools/edit.ts` + `edit-diff.ts` — the most carefully built tool in the codebase:

- Exact-string match first. On failure, **normalized fuzzy fallback**: strip trailing
  whitespace, NFKC-normalize, fold Unicode quotes/dashes to ASCII, retry (edit-diff.ts:206–244).
- Rejects non-unique matches with "Found N occurrences… must be unique"; rejects no-match
  with "must match exactly including all whitespace and newlines".
- **Multiple disjoint edits per call** (`edits: [{oldText, newText}]`), all matched against
  the *original* content and applied in reverse offset order so they can't interfere.
- Preserves BOM and original line-ending style (CRLF/LF).
- Streams a **live diff preview** in the TUI as soon as arguments finish streaming, before
  execution.
- Produces both a display diff and a real unified patch.

### The bash tool

No default timeout; caller-suppliable `timeout` in seconds. Streams stdout+stderr through
an `OutputAccumulator` with **100ms-throttled** partial updates so the TUI gets live output
without event flooding. Caps at 2000 lines / 50KB and spills full output to a temp file
when truncated, appending the path. Kills the whole process tree on abort/timeout. Injects
`PI_SESSION_ID`/`PI_PROVIDER`/`PI_MODEL`/`PI_REASONING_LEVEL` into the child so agent-run
scripts can introspect their own context.

### Per-file mutation queue

```ts
// packages/coding-agent/src/core/tools/file-mutation-queue.ts:32 (condensed)
export async function withFileMutationQueue<T>(filePath: string, fn: () => Promise<T>): Promise<T> {
  const key = await getMutationQueueKey(filePath);           // realpath-resolved
  const currentQueue = fileMutationQueues.get(key) ?? Promise.resolve();
  let releaseNext!: () => void;
  const nextQueue = new Promise<void>((res) => { releaseNext = res; });
  fileMutationQueues.set(key, currentQueue.then(() => nextQueue));
  await currentQueue;
  try { return await fn(); }
  finally { releaseNext(); }
}
```

A realpath-keyed async mutex. Parallel calls touching the *same* file serialize; different
files stay parallel. Solves the "two parallel edits race on one file" bug without a global
lock, in about 30 lines.

### grep / find

Shell out to **ripgrep** and **fd**, which pi **auto-downloads** from GitHub releases into
`~/.pi/agent/bin` if not on `$PATH`, with a `PI_OFFLINE` escape hatch.

## 5. Provider abstraction

The largest single piece of engineering in the repo. Two distinct concepts:

- **`Api`** — 10 wire protocols: `openai-completions`, `openai-responses`,
  `azure-openai-responses`, `openai-codex-responses`, `anthropic-messages`,
  `bedrock-converse-stream`, `google-generative-ai`, `google-vertex`,
  `mistral-conversations`, `pi-messages`.
- **`Provider`** — 30+ commercial front-ends each mapping to one `Api`: anthropic, openai,
  google, bedrock, github-copilot, xai, groq, cerebras, openrouter, vercel-ai-gateway, zai,
  mistral, minimax, moonshot, deepseek, fireworks, together, baseten, nvidia, huggingface,
  cloudflare, several regional variants, opencode…

So ~30 providers reduce to ~8 wire implementations differing by base URL, auth and catalog.

**Model discovery** — `scripts/generate-models.ts` (~2,300 lines) pulls from
`models.dev/api.json` plus live `/models` endpoints, with hand-written override comments
correcting known catalog inaccuracies, emitting a checked-in `models.generated.ts` that is
never hand-edited (AGENTS.md-enforced).

**Streaming normalization** — every provider emits the same event union:

```ts
// packages/ai/src/types.ts:523
export type AssistantMessageEvent =
  | { type: "start"; partial: AssistantMessage }
  | { type: "text_delta"; contentIndex: number; delta: string; partial: AssistantMessage }
  | { type: "toolcall_start"; contentIndex: number; partial: AssistantMessage }
  | { type: "toolcall_delta"; contentIndex: number; delta: string; partial: AssistantMessage }
  | { type: "toolcall_end"; contentIndex: number; toolCall: ToolCall; partial: AssistantMessage }
  | { type: "done"; reason: ...; message: AssistantMessage }
  | { type: "error"; reason: ...; error: AssistantMessage };
```

Two contracts make this work, and both are worth adopting verbatim:

**Every event carries `partial`** — the full accumulated message so far — so consumers never
reassemble deltas themselves. The UI just re-renders `partial`. That single decision removes
a whole category of streaming bugs.

**`StreamFn` must never throw.** Request, model and runtime failures are all encoded as a
final `AssistantMessage` with `stopReason: "error"` delivered through the normal stream:

```ts
// packages/agent/src/types.ts:24
/**
 * Contract:
 * - Must not throw or return a rejected promise for request/model/runtime failures.
 * - Failures must be encoded in the returned stream via protocol events and a
 *   final AssistantMessage with stopReason "error" or "aborted" and errorMessage.
 */
export type StreamFn = (model, context, options?) => AssistantMessageEventStream | Promise<...>;
```

One error path instead of two — which is exactly why their retry classifier can be a pure
function over a message rather than a tangle of `catch` blocks.

Tool-call argument JSON is assembled from streamed deltas and salvage-parsed if truncated
(`parseStreamingJson`), so partial arguments are inspectable mid-stream — which is what
lets the edit tool render a live diff preview before execution.

**The `OpenAICompletionsCompat` quirk matrix** (types.ts:545) is a dense flag interface
capturing per-provider deviations: whether `store` / `developer` role / `reasoning_effort` /
`finish_reason` / `stream_options.include_usage` are supported, which `max_tokens` field
name to use, whether tool results need a `name`, whether an assistant message must separate
a user message from tool results, whether thinking blocks must be faked as `<thinking>`
text, and **11 different `thinkingFormat` variants**. This is months of "OpenAI-compatible
but not really" pain encoded as data rather than scattered conditionals.

**Retry — two honest layers:**

1. **Transport** (`provider-retry.ts`). They call the vendor SDKs with `maxRetries: 0` and
   reimplement retry themselves — *because the SDKs' own retry timers ignore `AbortSignal`*,
   so a user pressing Ctrl-C during a backoff sleep would be stuck. Honors `x-should-retry`,
   retries 408/409/429/5xx, prefers server `retry-after-ms` / `retry-after` over its own
   backoff, and applies 25% downward jitter to a capped exponential:

   ```ts
   // packages/ai/src/utils/provider-retry.ts:51
   const exponentialDelay = Math.min(0.5 * 2 ** retryIndex, 8) * 1000;  // cap 8s
   return exponentialDelay * (1 - Math.random() * 0.25);                // jitter
   ```

   Sharp detail: a server-requested delay above `maxRetryDelayMs` (default 60s) **throws
   rather than sleeps**. A provider asking for a 10-minute wait surfaces as an error instead
   of a silent hang.

2. **Semantic** (`retry.ts` `retryAssistantCall`). Operates on the resulting
   `AssistantMessage`, classifying `errorMessage` by regex over ~40 patterns:
   `RETRYABLE_PROVIDER_ERROR_PATTERN` (overloaded, rate limit, 5xx, `fetch failed`,
   `ENOTFOUND`, `socket hang up`, `stream ended before message_stop`, `ResourceExhausted`)
   versus a **higher-priority** `NON_RETRYABLE_PROVIDER_LIMIT_ERROR_PATTERN`
   (`insufficient_quota`, `quota exceeded`, `billing`). So a 429 meaning "you're out of
   money" fails fast instead of burning the whole retry budget. Aborts landing mid-backoff
   are normalized into a clean `aborted` message so callers never special-case timing.

Nearly every regex carries a GitHub issue number in a comment — this list was earned in
production, not designed.

**Prompt caching** — Anthropic `cache_control` breakpoints on the system prompt, the last
tool definition, and the last content block of the last user message, with `"none" | "short"
| "long"` retention (`long` = 1h TTL where supported).

### "Stealth mode" — the Anthropic OAuth finding

When authenticated via Anthropic OAuth (a Claude Pro/Max subscription token rather than a
metered API key), pi **renames every tool to Claude Code's canonical names** (`Read`,
`Bash`, `Edit`, …) and force-injects the system message *"You are Claude Code, Anthropic's
official CLI for Claude."* ahead of its own prompt (anthropic-messages.ts:75–112, 940–990).

The source comment reads `// Stealth mode: Mimic Claude Code's tool naming exactly`, sourced
from the author's `cchistory` project which scrapes Claude Code's prompts across versions,
and pins `claudeCodeVersion = "2.1.75"`.

This is presumably necessary because Anthropic's subscription OAuth scope gates on client
identity. Two things follow: subscription login is achievable, and it requires ongoing
impersonation that breaks silently whenever enforcement changes — plus it sits in ToS gray
territory. Worth an explicit decision rather than an accident.

OAuth flows also exist for xAI, OpenAI Codex, Kimi, GitHub Copilot and OpenRouter.

## 6. Context management

**Messages** — `AgentMessage = Message | CustomAgentMessages[...]`, a discriminated union
extended by apps via TypeScript declaration merging, so UI-only message kinds (notifications,
artifacts) can live in the transcript and be filtered out by `convertToLlm` before reaching
the model.

**Sessions are a tree, not a log.** `session-manager.ts` (1,714 lines) is an append-only
**JSONL** store, one file per session under `~/.pi/agent/sessions/<cwd-hash>/`. Every entry
carries `uuid`/`parentUuid` (uuidv7), forming a real DAG. Entry kinds go beyond messages:
`model_change`, `thinking_level_change`, `active_tools_change`, `compaction`,
`branch_summary`, `custom`.

Because it's a tree inside one file, **branching costs nothing** — `/fork`, `/clone` and
`/tree` navigate it without copying anything. `branchWithSummary()` rewinds the leaf and
leaves a summary of the abandoned path behind.

Two details worth copying:

- **Model and thinking-level changes are first-class transcript entries**, not metadata. So
  replaying a session reproduces exactly which model produced which turn — which matters
  the moment you switch models mid-session, and is invisible if you only store messages.
- **Writes are atomic**: `publishFileAtomically` (`jsonl/storage.ts:32`) writes a sibling
  `.tmp` then `rename`s over the destination, so a crash mid-write leaves an ignorable temp
  file rather than a corrupted session. Format is at v3 with auto-migration from v1 (linear)
  and v2.

**Compaction — three tiers** (`core/compaction/compaction.ts`):

1. **Threshold**: `contextTokens > contextWindow - reserveTokens` (default reserve 16,384;
   `keepRecentTokens` 20,000 kept verbatim).
2. **Overflow recovery**: if a request *still* fails with a context-overflow error despite
   the proactive check, run one compact-and-retry cycle, guarded by
   `_overflowRecoveryAttempted` so it can never loop.
3. **Manual** `/compact`.

**Token estimation is hybrid** (`estimateContextTokens`, compaction.ts:216) — take the
*real* usage numbers reported by the last assistant message, then add a `chars/4` heuristic
only for messages after it. Far more accurate than estimating the whole transcript, and
free. (neo estimates everything at `chars/4`, which drifts badly on code-heavy sessions.)

**Cut-point selection** (`findCutPoint`, :374) walks backwards accumulating tokens until
`keepRecentTokens` is hit, then snaps to the nearest *valid* cut point — and valid points
exclude `toolResult` messages, so a tool call is never separated from its result.

**Split-turn handling** is the sophisticated part. If the cut lands mid-turn, pi runs *two*
summarization calls — one for prior history, one for the turn prefix with a dedicated
`TURN_PREFIX_SUMMARIZATION_PROMPT` — and concatenates them under a
`**Turn Context (split turn):**` header.

**Summaries are iterative, not regenerated.** A previous summary is fed back in with an
`UPDATE_SUMMARIZATION_PROMPT` whose rules are PRESERVE / ADD / UPDATE / move items from
"In Progress" to "Done". The output has a fixed structure — Goal, Constraints, Progress
(Done/In Progress/Blocked), Key Decisions, Next Steps, Critical Context — with "preserve
exact file paths, function names, and error messages" repeated in every prompt variant.

**File-operation tracking survives compaction**: read and modified file lists are extracted
from the summarized messages and merged with the *previous* compaction's lists, then
appended to the summary. The agent doesn't forget which files it already touched.

Summarization calls set `cacheRetention: "none"` and a fresh `sessionId` — they're
standalone and shouldn't pollute the prompt cache. Compaction is itself a session entry, so
reopening a session reconstructs context deterministically without re-running the LLM.

**System prompt** (`core/system-prompt.ts`, 162 lines) — comparatively minimal: role framing
→ available tools (one-line snippets, only for tools that registered one) → deduplicated
guideline bullets contributed by active tools → `<project_context>` blocks → skills → cwd.
Fully overridable via `customPrompt` / `SYSTEM.md` / `APPEND_SYSTEM.md`.

**Project context** — candidates per directory, in order: `AGENTS.override.md`, `AGENTS.md`,
`AGENTS.MD`, `CLAUDE.md`, `CLAUDE.MD`. Loads a global one from `~/.pi/agent/`, then walks
cwd up to filesystem root collecting one per ancestor, outermost first, each wrapped in
`<project_instructions path="...">`. Handles the git-worktree case where a linked worktree
would otherwise double-load the main repo's file (`findShadowedContextFile`) — a very
specific real-world bug fix.

**Skills** — implements the [Agent Skills standard](https://agentskills.io/specification):
`SKILL.md` with frontmatter `name`/`description` (max 64/1024 chars), discovered from
`~/.pi/agent/skills/`, `~/.agents/skills/`, `.pi/skills/`, `.agents/skills/` (cwd +
ancestors), npm packages, or `--skill`. Explicitly **interoperable with Claude Code and
Codex skill directories**.

The loading strategy is worth noting: skills are advertised in the system prompt as an
`<available_skills>` block carrying only name, description and location — **the model reads
the file itself, on demand**. Progressive disclosure rather than preloading, so a hundred
skills cost a hundred lines of context instead of a hundred documents.

## 7. TUI

A **fully custom, hand-rolled terminal UI framework** (`packages/tui`) — no Ink, no blessed,
no React.

**Differential rendering** — diff the previous frame's lines against the new frame, find the
changed range, rewrite only those lines, wrapped in CSI 2026 synchronized-output escapes to
avoid tearing:

```ts
// packages/tui/src/tui-main-screen.ts:294
let firstChanged = -1, lastChanged = -1;
const maxLines = Math.max(newLines.length, this.previousLines.length);
for (let i = 0; i < maxLines; i++) {
  const oldLine = i < this.previousLines.length ? this.previousLines[i] : "";
  const newLine = i < newLines.length ? newLines[i] : "";
  if (oldLine !== newLine) {
    if (firstChanged === -1) firstChanged = i;
    lastChanged = i;
  }
}
```

Render scheduling is throttled by a coalescing timer — **except keyboard input, which takes
an immediate-render fast path** because "even `setTimeout(0)` can take a full 16ms tick on
Windows" (tui.ts:891). Real cross-platform latency tuning.

**Component model** — minimal `Component` interface (`render(width): string[]`, optional
`handleInput`) with `Box`, `Container`, `Text`, `Markdown`, `SelectList`, `Editor`,
`ScrollView`, `Stack`/`HStack`/`VStack`, `Image`, `Spacer`. Inline images via Kitty/iTerm2
graphics protocols, with special line tracking so image rows survive the diff.

A small **native addon** (`packages/tui/native/{darwin,win32}`) handles keyboard-modifier
detection Node can't get cheaply through escape-sequence queries.

**Async agent → UI** — `Agent.subscribe(listener)` delivers every loop event
(`message_start/update/end`, `tool_execution_*`, `turn_*`, `agent_start/end`);
`interactive-mode.ts` turns these into component updates and calls `invalidate()`.
`core/event-bus.ts` provides a second, broader pub/sub that also drives extensions — **the
UI and extensions consume the identical event stream**, which is why extensions can
implement plan mode and permission gates entirely outside core.

**Diffs** — word-level intra-line highlighting via the `diff` npm package's `diffWords`,
inverse-video on changed spans, GitHub-style — not just red/green whole lines.

**Modes** — interactive TUI, `--mode print`/`-p` (one-shot headless), `--mode json`
(structured JSONL event stream, what the subagent extension consumes), `--mode rpc`
(LF-delimited JSONL request/response for editor integration; the README explicitly warns
that generic line readers like Node's `readline` break because they split on Unicode line
separators inside JSON payloads).

**Slash commands** — `/settings /model /scoped-models /export /import /share /copy /name
/session /changelog /hotkeys /fork /clone /tree /trust /login /logout /new /compact /resume
/reload /quit`.

**Key bindings** — `escape` cancel, `ctrl+d` exit, `shift+tab` cycle thinking level,
`ctrl+p`/`shift+ctrl+p` cycle model, `ctrl+l` model selector, `ctrl+o` expand tool output,
`ctrl+t` toggle thinking blocks, `alt+enter` queue follow-up, `alt+up` dequeue. Fully
overridable via `~/.pi/agent/keybindings.json`, with an AGENTS.md rule forbidding hardcoded
key checks elsewhere.

## 8. Permissions & safety

pi's most distinctive stance, and deliberate rather than an oversight. From the README:

> **No permission popups.** Run in a container, or build your own confirmation flow with
> extensions… **No plan mode.** Write plans to files, or build it with extensions…
> **No sub-agents.** … **No MCP.** Build CLI tools with READMEs, or build an extension.

From `SECURITY.md`, listed as explicitly out of scope for vulnerability reports:

> *"Local code execution or sandboxing behavior (the Pi coding agent intentionally does not
> have a sandbox)"* … *"Pi treats the local user account and files writable by that account
> as inside the same trust boundary as the Pi process itself."*

Concretely:

- **No approval gate in core.** `beforeToolCall`/`afterToolCall` exist as hooks but are
  wired only to the extension runner. With no extension registered, every tool call —
  including `bash` and `write` — executes unconditionally.
- **No path jailing.** `resolveToolPath` absolutizes; nothing checks the result stays inside
  the project.
- **"Trust" ≠ tool approval.** `trust-manager.ts` gates only whether project-local
  *configuration* (`settings.json`, extensions, skills, prompts, themes, `SYSTEM.md`) loads
  from an untrusted directory. Nothing to do with approving tool calls.
- **Sandboxing is opt-in**: `examples/extensions/sandbox/` wraps `@anthropic-ai/sandbox-runtime`
  (`sandbox-exec` on macOS, `bubblewrap` on Linux) with `allowedDomains`/`denyRead`/
  `allowWrite`/`denyWrite` — by **replacing the built-in bash tool entirely**.

The entire shipped confirmation flow is a 35-line example:

```ts
// packages/coding-agent/examples/extensions/permission-gate.ts
pi.on("tool_call", async (event, ctx) => {
  if (event.toolName !== "bash") return undefined;
  const command = event.input.command as string;
  const isDangerous = dangerousPatterns.some((p) => p.test(command));
  if (isDangerous) {
    if (!ctx.hasUI) return { block: true, reason: "Dangerous command blocked (no UI for confirmation)" };
    const choice = await ctx.ui.select(`⚠️ Dangerous command:\n\n  ${command}\n\nAllow?`, ["Yes", "No"]);
    if (choice !== "Yes") return { block: true, reason: "Blocked by user" };
  }
  return undefined;
});
```

Auth secrets are protected at file level: `~/.pi/agent/auth.json` at `0600`, parent `0700`,
guarded with `proper-lockfile`.

## 9. Extensibility

Extensibility is where pi puts almost everything other agents bake into core. The
`ExtensionAPI` (`core/extensions/types.ts`, 1,727 lines) exposes:

- `on(event, handler)` for **~30 typed lifecycle events**: session start/shutdown/
  before-compact/before-fork, agent start/end/settled, turn start/end, message start/update/
  end, tool_execution start/update/end, `tool_call`/`tool_result`,
  `before_provider_request`/`before_provider_headers`/`after_provider_response`,
  `model_select`, `input`, `project_trust`, `resources_discover`, `user_bash`.
- `registerTool()` — register or **replace** tools wholesale.
- `registerProvider()` — a full custom LLM provider at runtime, including custom
  `streamSimple` transport, headers, and OAuth login/refresh. Live, no restart.
- `registerCommand()`, `registerShortcut()`, `registerFlag()`.
- `registerMessageRenderer`/`registerMarkdownTransformer`/`registerEntryRenderer`.
- `sendMessage`, `setActiveTools`, `setModel`, `exec()`.

**MCP is explicitly not supported** — confirmed by absence in source, and stated directly
with a link to the author's blog post arguing against MCP as a protocol.

`examples/extensions/` holds ~70 examples standing in for built-in features:
`permission-gate.ts`, `plan-mode/`, `sandbox/`, `subagent/`, `protected-paths.ts`,
`confirm-destructive.ts`, `custom-provider-anthropic/`, `git-checkpoint.ts`, `notify.ts` —
and a playable Doom port rendered in the terminal.

## 10. Config & auth

- **Config root** `~/.pi/agent/` (name configurable via `package.json`'s `piConfig.configDir`).
  Project overrides in `<project>/.pi/`.
- **Settings** `~/.pi/agent/settings.json`.
- **Auth** `~/.pi/agent/auth.json`, mode `0600`, `proper-lockfile`-guarded with revision
  checks for concurrent modification. **No OS keychain** — plaintext on disk, protected only
  by file permissions. The escape hatch is command indirection: a credential can be
  `{"command": "..."}` that shells out to a secret manager instead of storing a literal.
- **API key resolution** — literal string, `$ENV_VAR`/`${ENV_VAR}` interpolation, or a
  leading `!command`. Plus full OAuth flows (Anthropic, GitHub Copilot, OpenAI Codex, Qwen,
  Xiaomi, opencode), refreshed with a **5-minute validity margin**.
- **Resolution order**: a stored credential *owns* the provider. Env vars are consulted only
  when nothing is stored, and there is deliberately **no silent env fallback after a failed
  refresh** — a broken OAuth token surfaces as an error rather than quietly downgrading to
  some other identity.
- `getApiKey` is re-resolved **on every LLM call** (agent-loop.ts:305), specifically so a
  short-lived token can't expire during a long tool phase and kill the turn.
- **Model selection** — `model-registry.ts` / `model-resolver.ts` / `models-store.ts`
  resolve against the generated catalog; `--model` flag, `/model` command,
  `PI_PROVIDER`/`PI_MODEL` env vars.

## 11. Worth copying

1. **A pure loop with callback seams, wrapped by a stateful `Agent`.** Policy lives in
   callbacks; the loop owns no state. Testable with no provider, no filesystem, no terminal.
   The most transferable idea in the codebase.
2. **`ExecutionEnv` indirection** — no tool touches the filesystem directly. Costs nothing
   up front; the only reason sandboxing can be added later without rewriting every tool.
3. **Truncated-output tool-call safety** (agent-loop.ts:206) — refuse to execute tool calls
   from a `length`-stopped message. Silent file corruption if skipped.
4. **Per-file mutation queue** — a realpath-keyed async mutex, ~30 lines, makes parallel
   tool execution safe against same-file races without a global lock.
5. **`StreamFn` must never throw** — errors arrive as a message with `stopReason: "error"`.
   One error path instead of two, which is why the retry classifier can be a pure function.
6. **Steering and follow-up as two separate queues** — "interrupt now" and "queue until
   done" are genuinely different operations; separating them avoids racing the tool executor.
7. **Every stream event carries the full accumulated `partial`** — the UI re-renders rather
   than reassembling deltas. Removes a whole category of streaming bugs.
8. **Edit tool exact→fuzzy fallback** with loud errors on genuine ambiguity, preserving BOM
   and line endings, multiple disjoint edits matched against original content.
9. **`OpenAICompletionsCompat` quirk matrix** — encode provider deviations as data, not
   scattered conditionals. 11 reasoning-effort encodings alone.
10. **Two-tier retry with error classification** — never retry quota/billing exhaustion;
    throw rather than sleep on absurd server-requested delays; annotate each regex with the
    issue it came from.
11. **Hybrid token estimation** — real usage from the last assistant message, `chars/4` only
    for the tail after it. Accurate where it matters, free everywhere else.
12. **Sessions as a branchable tree**, written atomically, with model changes as first-class
    entries — branching, resuming and faithful replay all fall out of the data structure.
13. **Compaction that tracks read/modified files across the boundary**, uses split-turn dual
    summaries, and updates a structured summary iteratively rather than regenerating it.
14. **Differential terminal rendering with synchronized output** and platform-specific input
    latency tuning.
15. **Extensions consume the same event stream as the UI** — not a weaker parallel API,
    which is why the example library can implement plan mode and permission gates externally.
16. **Explicit security-model documentation** — stating "no sandbox, same trust boundary as
    the local user" up front beats implying more safety than you deliver.
17. **Supply-chain discipline** — exact pins, `min-release-age=2`, `--ignore-scripts`,
    published shrinkwrap.

## 12. Gaps and weaknesses

1. **No default safety net.** Out of the box `bash`/`write`/`edit` run unconfirmed and
   unsandboxed. Disclosed and deliberate — but the risky configuration is the *default*,
   not opt-in risk-taking.
2. **Split architecture in-repo.** Two overlapping session/orchestration implementations.
   Nearly every method in `agent-harness.ts` returns `HarnessNotImplemented` (:355–442) and
   `create()` throws on any existing session record — the polished lane/suspend/resume API
   is aspirational. No stated plan to reconcile them.
3. **`agent-session.ts` is 3,342 lines** — the monolith the rest of the codebase carefully
   avoids being, absorbing compaction orchestration, session switching, bash, extensions,
   model management and events. A useful warning: the clean layering held everywhere
   *except* the one place where the product actually gets assembled. That's the file we'll
   be most tempted to write, and the one to watch.
3. **No MCP** cuts pi off from the existing MCP server ecosystem without a user-built bridge.
4. **No built-in sub-agent isolation** — the reference implementation spawns a whole new
   `pi` process per task, heavier than an in-process context fork.
5. **Very fast churn** (~15 commits/day, 0.0.x monorepo version) implies API instability;
   several extension-facing types already carry `@deprecated`.
6. **Tools are coupled to TUI rendering** — `bash.ts` returns `pi-tui` components directly.
   Convenient for one first-party frontend; makes tools unusable elsewhere without carrying
   the TUI as a dependency.
7. **"Stealth mode" is fragile** — a manually-maintained tool-name list and pinned Claude
   Code version that will silently degrade when Anthropic changes enforcement.
8. **30+ providers is a maintenance liability** — correctness depends on `models.dev`, an
   external catalog their own generator already carries dozens of manual corrections for.
