# Go ecosystem for a terminal coding agent

Compiled 2026-08-08. Versions verified against pkg.go.dev, GitHub releases and live docs —
not recalled from memory. Items that could not be pinned confidently are flagged at the end.

Constraint driving every choice below: **one static binary**, `CGO_ENABLED=0`, cross-compiled
from a single CI runner. That single constraint eliminates several otherwise-obvious
libraries (notably `mattn/go-sqlite3`) and is the reason for some non-obvious picks.

---

## Recommended stack

| Library | Version | For | Why |
|---|---|---|---|
| Go | 1.23+ | — | minimum required by `anthropic-sdk-go` (we have 1.26.5) |
| `anthropics/anthropic-sdk-go` | **v1.62.0** | Claude API | official, streaming, tool use, prompt caching, very active. `Message.Accumulate` glues stream fragments |
| `openai/openai-go` | v3 | every OpenAI-compatible endpoint | **official**, base-URL swappable, and ships `ChatCompletionAccumulator` — the community `sashabaranov/go-openai` does not, and hand-rolling that reassembly is the leak we're avoiding |
| `charmbracelet/bubbletea/v2` | **v2.0.8** | TUI framework | Elm architecture maps onto an agent loop; `Program.Send` bridges goroutines into the UI |
| `charmbracelet/bubbles/v2` | **v2.1.1** | components | `viewport`, `textarea`, `spinner`, `list`, `help` |
| `charmbracelet/lipgloss/v2` | **v2.0.5** | styling/layout | composable styles, `JoinHorizontal/Vertical`, ANSI-aware measurement |
| `charmbracelet/glamour/v2` | **v2.0.1** | markdown | renders model output; wraps Chroma |
| `alecthomas/chroma/v2` | **v2.27.0** | syntax highlighting | powers Glamour; usable standalone for diffs/file views |
| `aymanbagabas/go-udiff` | maintained | diff generation | returns real unified-diff strings; successor to the stalled `gotextdiff` |
| `bluekeyes/go-gitdiff` | **v0.9.0** | diff application | strict, fail-loud patch application (only if we ever need it) |
| `bmatcuk/doublestar/v4` | **v4.10.0** | globbing | `**` patterns and brace expansion stdlib lacks |
| ripgrep (`rg`) | external | code search | shell out when present, pure-Go fallback when not |
| JSONL append files | — | transcript (source of truth) | what Claude Code itself uses; crash-safe, inspectable, trivial resume |
| `modernc.org/sqlite` | see flags | session index | **pure Go, CGO-free** — the whole reason to prefer it over `mattn` |
| `creack/pty` | **v1.1.24** | pty | only for subprocesses that need a real tty |
| `dnaeon/go-vcr/v4` | **v4.0.7** | HTTP fixtures | record/replay real provider traffic in tests |
| `charmbracelet/x/exp/teatest` | experimental | TUI testing | only option today; don't over-invest |
| `goreleaser` | **v2.17.1** | releases | cross-compiled binaries + Homebrew tap from one YAML |

Everything is pure Go except optional `rg` and `creack/pty` (platform syscalls, no cgo), so
`CGO_ENABLED=0` and trivial cross-compilation hold throughout.

---

## 1. TUI framework

### Bubble Tea v2 is stable — build against it

**v2.0.0 shipped 2026-02-24** (latest patch **v2.0.8**) — the project's first breaking
release in six years, alongside Lip Gloss v2, Bubbles v2 and Glamour v2. It was battle-tested
for months as the engine behind Charm's own coding agent, **Crush**, before public release.
v1 (v1.3.10) is frozen; pkg.go.dev already flags it as not-latest.

**Practical warning:** most tutorials online still target v1 and won't compile. Differences
that will bite:

- `View()` returns a `tea.View` struct, not a `string`. Terminal features (alt-screen, mouse
  mode, cursor, window title) are declarative fields on it rather than one-off commands.
- A new **"Cursed Renderer"** (ncurses-algorithm-based) specifically targets flicker/tearing
  and cuts redraw bandwidth over SSH.
- Key events split into `KeyPressMsg`/`KeyReleaseMsg`; mouse into
  `MouseClickMsg`/`MouseReleaseMsg`/`MouseWheelMsg`/`MouseMotionMsg`.

An official upgrade guide lives in the repo.

### The architecture

```go
func (m model) Init() tea.Cmd                            // once at startup
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd)  // pure state transition
func (m model) View() tea.View                           // render current state
```

`Update` is the only place state changes. Every external event — keypress, tick, streamed
token, tool result — arrives as a `tea.Msg`. That maps directly onto an agent loop.

### `tea.Cmd` vs goroutines — and when each is right

A `tea.Cmd` is just `func() tea.Msg`. Bubble Tea runs it on its own goroutine and delivers
the result back into `Update`, so all mutation stays in one function and rendering can never
race it.

Pitfalls:
- A `Cmd` that blocks forever leaks a goroutine — Bubble Tea does not cancel them.
- Mutating model fields from your own goroutine instead of returning a `Msg` **is a data
  race**; `View()` reads the model concurrently.
- A `Cmd` returning a type `Update` doesn't type-switch on is silently dropped. `nil` is
  valid and means "nothing further".

**For a long-lived token stream, a single blocking `Cmd` is the wrong shape.** Use a
goroutine reading the stream and bridge into the UI with `Program.Send`:

```go
type tokenMsg string
type streamDoneMsg struct{ err error }

func pumpStream(p *tea.Program, tokens <-chan string, done <-chan error) {
    go func() {
        for {
            select {
            case tok, ok := <-tokens:
                if !ok { return }
                p.Send(tokenMsg(tok))
            case err := <-done:
                p.Send(streamDoneMsg{err: err})
                return
            }
        }
    }()
}
```

`p.Send` is safe from any goroutine and is the sanctioned mechanism for injecting external
events. **Both neo and (in its own idiom) pi converge on this pattern** — neo's comment
explicitly says it replaced a hand-rolled channel pump to escape backpressure problems.

### Components (bubbles v2.1.1)

`viewport` (scrollback — v2 added horizontal scroll and a custom gutter), `textarea`
(v2 added a real cursor and dynamic height), `textinput`, `spinner`, `list`, `table`,
`progress`, `paginator`, `help`, `filepicker`, `timer`.

**There is no diff-rendering component** — confirmed against the full v2 list. We build that
from Lip Gloss + a diff library.

### Glamour and the streaming-markdown problem

`glamour.Render(md, "dark")` renders a **complete** document. There is no streaming or
partial-render API, and feeding it incomplete markdown mid-stream — an unclosed code fence,
a half-written table — flickers or misrenders.

The workaround (widely used, not officially documented): **render plain text token-by-token
while streaming, then do one Glamour pass when the stream completes.** Alternatively
re-render on a debounce or at paragraph boundaries rather than per token. This is a specific
problem we have to solve deliberately; the Crush report should tell us what Charm does.

### Known pain points

- **Windows flicker** was a tracked issue (≥0.26.0); the v2 renderer targets it with atomic
  frame writes.
- **Viewport correctness** — `GotoBottom()` and visible-area calculation have had reported
  bugs with wrapped multiline content. Test with large tool-output scrollback.
- **Redraw bandwidth over SSH** — an older issue, mitigated by the v2 rewrite.
- **Mouse mode vs native copy-paste** — enabling mouse capture changes how the emulator
  handles selection. This afflicts *all* full-screen TUIs, not Bubble Tea specifically. Most
  emulators honor Shift to force native selection; many coding-agent TUIs leave mouse mode
  off by default for exactly this reason.

### Alternatives, briefly

`rivo/tview` is widget-based on `tcell` — retained-mode `Flex`/`Table`/`Form`/`TreeView`.
Good for form- and grid-heavy admin TUIs; a weak fit for a continuously-appending, richly
styled chat view. No compositional styling equivalent to Lip Gloss, no Glamour integration,
and inline (non-alt-screen) operation is less natural. `termui`, `tcell`, `gocui` are lesser
options. **Recommendation: Bubble Tea v2**, with Crush as the reference implementation.

---

## 2. Rendering and diffs

**Chroma v2.27.0** is what Glamour uses internally, so we get it free for fenced code. For
standalone use (diff content, file preview panes):

```go
import "github.com/alecthomas/chroma/v2/quick"
quick.Highlight(os.Stdout, sourceCode, "go", "terminal256", "monokai")
```

(A Chroma v3 alpha exists with an `iter.Seq[Token]` API — not the build target yet.)

**Diff libraries — the landscape has shifted:**

| Library | Verdict |
|---|---|
| `sergi/go-diff` | diff-match-patch port; does **not** emit standard unified diffs, widely reported stale. Avoid |
| `hexops/gotextdiff` | extraction of `gopls`'s internal diff, good pedigree, real unified output — but **abandoned** |
| `aymanbagabas/go-udiff` | zero-dependency, actively maintained continuation of that lineage. `udiff.Unified(a, b, oldText, newText)` returns a unified-diff string. **Use this** |
| `bluekeyes/go-gitdiff` v0.9.0 | *applies* diffs — parses `git diff`/`format-patch`/plain unified and applies **strictly** (no fuzz), failing loudly on mismatch. Good property for an agent |

**Rendering a colored diff** (no library does this for us):

1. Compute with `go-udiff` → unified-diff string or structured hunks.
2. Split into lines, classify by leading `+` / `-` / ` ` / `@@`.
3. Apply a `lipgloss.Style` per class — green additions, red deletions, faint hunk headers.
4. For GitHub-style intra-line highlighting, run stripped content through Chroma and overlay
   the +/- background tint. (pi does word-level intra-line diffing; solid-color lines are a
   perfectly good v1.)
5. Feed the styled block into a `viewport` for scrolling.

---

## 3. Anthropic API from Go

`anthropic-sdk-go` **v1.62.0** (2026-08-07), requires **Go 1.23+**, actively developed —
recent releases added session budgets, the advisor tool, pinned inference geography and
skills auto-loading. Stainless-generated from the same OpenAPI spec as the Python/TS SDKs,
so field names map 1:1 with Go casing.

```go
client := anthropic.NewClient()                       // reads ANTHROPIC_API_KEY
// or anthropic.NewClient(option.WithAPIKey("..."))
```

### Streaming

```go
stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
    Model: "claude-opus-5", MaxTokens: 4096, Messages: messages, Tools: tools,
})
for stream.Next() {
    event := stream.Current()
    switch e := event.AsAny().(type) {
    case anthropic.ContentBlockDeltaEvent:
        if d, ok := e.Delta.AsAny().(anthropic.TextDelta); ok {
            program.Send(tokenMsg(d.Text))
        }
    }
}
if err := stream.Err(); err != nil { /* handle */ }
```

**There is no `GetFinalMessage()`** on the Go stream (unlike Python/TS). Accumulate manually:

```go
message := anthropic.Message{}
for stream.Next() { message.Accumulate(stream.Current()) }
```

### Tools

```go
addTool := anthropic.ToolParam{
    Name:        "run_bash",
    Description: anthropic.String("Execute a shell command in the project root"),
    InputSchema: anthropic.ToolInputSchemaParam{
        Properties: map[string]any{"command": map[string]any{"type": "string"}},
    },
}
tools := []anthropic.ToolUnionParam{{OfTool: &addTool}}
```

Response `Content` is a flattened `ContentBlockUnion` you type-switch via `.AsAny()`; a
`tool_use` block's raw JSON input is at `block.JSON.Input.Raw()`. Results go back with
`NewToolResultBlock(toolUseID, content, isError)` inside `NewUserMessage(...)`.

### Prompt caching

```go
System: []anthropic.TextBlockParam{{
    Text:         systemPrompt,
    CacheControl: anthropic.NewCacheControlEphemeralParam(), // 5m TTL default
}},
```

Verify hits via `resp.Usage.CacheReadInputTokens` / `CacheCreationInputTokens`. Cache is a
**prefix match** — any byte change before a breakpoint invalidates everything after it. Keep
the system prompt and tool list byte-stable, and put volatile content (timestamps, per-turn
state) *after* the last breakpoint. This is exactly why neo splits its system prompt into a
cacheable base block and an uncached AGENTS.md tail.

### A full streaming tool-use turn

```go
func runTurn(ctx context.Context, client anthropic.Client, messages []anthropic.MessageParam,
    tools []anthropic.ToolUnionParam) ([]anthropic.MessageParam, error) {

    for {
        stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
            Model: "claude-opus-5", MaxTokens: 4096, Messages: messages, Tools: tools,
            System: []anthropic.TextBlockParam{{
                Text: systemPrompt, CacheControl: anthropic.NewCacheControlEphemeralParam(),
            }},
        })

        msg := anthropic.Message{}
        for stream.Next() {
            event := stream.Current()
            msg.Accumulate(event)
            if delta, ok := event.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
                if td, ok := delta.Delta.AsAny().(anthropic.TextDelta); ok {
                    program.Send(tokenMsg(td.Text))
                }
            }
        }
        if err := stream.Err(); err != nil { return messages, err }

        messages = append(messages, msg.ToParam())

        if msg.StopReason != anthropic.StopReasonToolUse {
            return messages, nil
        }

        var results []anthropic.ContentBlockParamUnion
        for _, block := range msg.Content {
            if tu, ok := block.AsAny().(anthropic.ToolUseBlock); ok {
                out, isErr := executeTool(tu.Name, tu.JSON.Input.Raw())
                results = append(results, anthropic.NewToolResultBlock(tu.ID, out, isErr))
            }
        }
        messages = append(messages, anthropic.NewUserMessage(results...))
    }
}
```

The SDK also ships a beta `toolrunner` (`client.Beta.Messages.NewToolRunner(...)`) that
automates this loop. **We should not use it** — hand-writing the loop is the point, and it
gives us the integration points we need for streaming into Bubble Tea.

### Multi-provider

Anthropic's API is not OpenAI-wire-compatible, so there's no drop-in swap. For everything
else, use the **official [`openai/openai-go`](https://pkg.go.dev/github.com/openai/openai-go)**
against any OpenAI-compatible endpoint by overriding the base URL — Groq, OpenRouter, DeepSeek,
Together, xAI, Mistral, Ollama, LM Studio, vLLM. That plus the native Anthropic SDK is the
two-adapter shape. Crush uses `openai-go/v3`.

> **Corrected.** An earlier draft recommended `sashabaranov/go-openai` (10.7k ★, active). It
> works, but it has **no streaming accumulator** — you hand-roll fragment reassembly, including
> tracking `tool_calls[]` by index, remembering the function name that appears only on the
> first fragment, buffering argument strings, and deciding when they're parseable. The official
> SDK ships `ChatCompletionAccumulator` for exactly this:
>
> ```go
> acc := openai.ChatCompletionAccumulator{}
> for stream.Next() {
>     acc.AddChunk(stream.Current())
>     if tc, ok := acc.JustFinishedToolCall(); ok { /* name + arguments complete */ }
>     if c, ok := acc.JustFinishedContent(); ok  { /* text block complete */ }
> }
> ```
>
> `JustFinishedToolCall` matters more than it looks: unlike Anthropic, the OpenAI wire format
> has **no per-block completion signal** — there's no `content_block_stop`, only a
> `finish_reason` at the end of the whole response. Without the accumulator you have to infer
> completion yourself, and that inference is exactly the leaky state that must not end up in
> the UI (see internals §4.2).

**What we actually write is the projection, not the gluing.** Both SDKs accumulate; each
adapter maps its SDK's accumulated state onto rasp's neutral `*Message`. Roughly 60 lines per
provider family, and the only place the wire-shape asymmetry is visible:

| | Anthropic | OpenAI |
|---|---|---|
| Structure | `content_block_start/delta/stop`, explicit index and type | `choices[0].delta` with `.content` and `.tool_calls[]` |
| Tool args | `input_json_delta` on a typed block | `.tool_calls[i].function.arguments` string fragments |
| Tool identity | `id`/`name` on `content_block_start` | `id`/`name` usually only on the **first** fragment for that index |
| Block completion | explicit `content_block_stop` | none — inferred from `finish_reason` |
| Stop reason | `tool_use`, `end_turn`, `max_tokens` | `tool_calls`, `stop`, `length` |

Unified-abstraction libraries surfaced (`any-llm-go` from Mozilla, GoAI SDK, `gollm`) but
production maturity was not verified — and a thin interface of our own teaches more.

---

## 4. Safe command execution

**`exec.CommandContext` kills only the direct child, not its descendants.** This is a real,
documented, unresolved limitation ([golang/go#21135](https://github.com/golang/go/issues/21135)):
`bash -c "foo &"` leaves orphans when the context is cancelled.

```go
func runCommand(ctx context.Context, dir string, timeout time.Duration, name string, args ...string) (string, error) {
    ctx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()

    cmd := exec.CommandContext(ctx, name, args...)
    cmd.Dir = dir
    cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}  // child leads a new process group

    var buf syncBuffer        // mutex-guarded bytes.Buffer
    cmd.Stdout = &buf
    cmd.Stderr = &buf         // same writer -> true chronological interleaving, free

    // Default cancel kills only the child. Override to kill the whole group.
    cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
    cmd.WaitDelay = 2 * time.Second

    err := cmd.Run()
    if ctx.Err() == context.DeadlineExceeded {
        return buf.String(), fmt.Errorf("command timed out: %w", ctx.Err())
    }
    return buf.String(), err
}
```

`Cmd.Cancel` and `Cmd.WaitDelay` were added in **Go 1.20** precisely to make this override
clean. neo does exactly this. **Windows** has no process-group equivalent — use
`CREATE_NEW_PROCESS_GROUP` plus `taskkill /T /F /PID`, gated on `runtime.GOOS`.

**Interleaving:** pointing `Stdout` and `Stderr` at the *same* writer preserves chronological
order for free, because the OS write calls land in one stream in the order they happen. Two
separate pipes read by two goroutines loses that guarantee.

**pty (`creack/pty` v1.1.24):** needed when a command behaves differently on a non-tty — many
CLIs disable color or change buffering when `isatty()` is false. For the bulk of agent use
(builds, tests, git, linters) plain pipes are simpler and dodge pty gotchas (echo, line
discipline, `\r\n`). Start with pipes; set `NO_COLOR=1` where it helps.

**macOS `sandbox-exec`:** deprecated for years with no documented replacement, no removal
timeline. Still the only documented way to apply Seatbelt policies to arbitrary processes.

```scheme
(version 1)
(deny default)
(allow process-fork process-exec)
(allow file-read*)
(allow file-write* (subpath "/path/to/allowed/workdir"))
(allow file-write* (subpath "/private/tmp"))
```

SBPL is undocumented and unstable; Apple's profiles under `/System/Library/Sandbox/Profiles`
are the only reference. Linux equivalents are `bubblewrap` or a throwaway container. Go's
stdlib has **no sandboxing primitive**.

**The simpler, more common defense is path restriction**, not OS sandboxing: resolve every
model-supplied path (`filepath.Clean` → `Abs` → `EvalSymlinks` to catch symlink escapes) and
verify it stays under the project root (`filepath.Rel`, reject anything starting with `..`).
Go 1.24's `os.Root` does this natively — neo already uses it for `grep`/`glob`.

---

## 5. File editing strategies

| Strategy | Mechanism | Failure mode |
|---|---|---|
| **Exact string replace** | model supplies `old_string`/`new_string`, must match exactly once | fails **loudly and safely** — 0 or >1 matches is an explicit error |
| Line-range replacement | start/end line + new content | stale line numbers → **silent wrong-location edits** |
| Unified diff / patch | model generates a diff, patcher applies | LLMs are inconsistent at `@@` hunk metadata; strict appliers reject, fuzzy ones **silently misapply** |
| Whole-file rewrite | model outputs the entire file | token-expensive; drops unrelated content near context limits |

**Consensus, informed by how Claude Code, Aider and Cursor actually work: exact-string
match-and-replace with sufficient surrounding context is the most reliable strategy.** Models
are strong at reproducing text they just read via a file-read tool, and weak at computing
line numbers or hunk metadata, which requires counting. Exact-match also fails *detectably*:
zero matches means nothing happened; multiple means ambiguous — both recoverable by asking
the model to include more context. Claude Code's `Edit` works this way, and Aider migrated
from a diff format to SEARCH/REPLACE blocks for exactly this reason.

**Recommendation:** exact-string-replace as the primary edit tool, whole-file write as a
fallback for new files. Add pi's normalized fuzzy retry (trailing whitespace, NFKC, curly
quotes → ASCII) as a second attempt before erroring — it recovers a meaningful share of
near-misses without introducing silent misapplication.

---

## 6. Code search

**Shelling out to ripgrep is the pragmatic default**, and effectively what Claude Code does:
`rg` is dramatically faster (parallel, memory-mapped), already respects `.gitignore`, and
handles binary detection and Unicode correctly. The cost is an external dependency, in
tension with the single-binary goal.

**The compromise:** use `rg` when it's on `$PATH`, fall back to pure Go otherwise. (pi goes
further and *auto-downloads* `rg`/`fd` from GitHub releases into its config dir, with a
`PI_OFFLINE` opt-out — clever, but more machinery than we need.)

**Pure-Go fallback:** `filepath.WalkDir` (faster than `Walk`) + `regexp`;
`bmatcuk/doublestar/v4` **v4.10.0** for `**` globs and brace expansion. For `.gitignore`:
`sabhiram/go-gitignore` works but is unmaintained (last published 2021, 500+ dependents, low
risk); `go-git/go-git/v5/plumbing/format/gitignore` is the maintained alternative.

**Tree-sitter: skip it.** Both Go bindings (`smacker/go-tree-sitter`, community-migrating-away;
`tree-sitter/go-tree-sitter`, the official one) typically require cgo-linked C grammars,
which conflicts with `CGO_ENABLED=0`. Claude Code itself relies on ripgrep-style text search
plus the model's own code understanding. Tree-sitter earns its keep in editors doing
real-time highlighting, not in an agent whose smart layer is the LLM.

---

## 7. Session persistence

**Claude Code uses JSONL** — `~/.claude/projects/<url-encoded-project-path>/<session-id>.jsonl`,
one project folder (path munged: `/`, spaces, `~` → `-`), one file per session, appended per
turn. Rationale: append-only avoids rewriting the file every turn, and a crash leaves a valid
replayable file up to the last complete line (a truncated final line is simply skipped).
pi does the same, adding `id`/`parentId` to make it a tree, and writes via temp+rename.

**`modernc.org/sqlite` is confirmed pure Go, CGO-free** — a transpiled port of C SQLite via
`ccgo`, not a wrapper. That is what makes it compatible with `CGO_ENABLED=0` and trivial
cross-compilation, unlike `mattn/go-sqlite3` which needs a working C toolchain on every
target and breaks simple multi-platform CI. 3,500+ importing packages; Gogs migrated to it
specifically to drop cgo. Roughly 2× slower than `mattn` on inserts — immaterial at our
volume.

> **Footgun:** the driver name is `"sqlite"`, not `"sqlite3"`. `import _ "modernc.org/sqlite"`
> then `sql.Open("sqlite", path)`.

**`go.etcd.io/bbolt` v1.5.0** — pure-Go embedded B+tree KV store, ACID, single file. Simpler
than SQL, but no query flexibility beyond key/prefix iteration.

**Recommendation: JSONL as the source of truth, `modernc.org/sqlite` as a thin index.**
Mirrors Claude Code's proven, crash-safe, debuggable approach — resume is "read the file,
replay it." Add a SQLite table with one row per session (id, project path, timestamps, title,
message count) only when we need listing/search UI. Keep the append-heavy path on JSONL. Skip
bbolt.

---

## 8. Testing an agent

**HTTP fixtures — `dnaeon/go-vcr/v4` v4.0.7** (2026-06-25). `recorder.New(cassette)` returns
a `RoundTripper`; the first run hits the real API and records to a YAML cassette, later runs
replay with no network. Supports custom request matchers (needed — Anthropic bodies contain
non-deterministic IDs) and redaction hooks (**scrub `x-api-key` before committing**). Wire in
via `option.WithHTTPClient(&http.Client{Transport: recorder})`.

**Fake provider** — an `httptest.Server` hand-writing minimal SSE frames
(`content_block_delta`, `message_stop`) with the SDK base URL pointed at it. Fully
deterministic, more work to keep in sync. pi ships exactly this as a `faux` provider and
requires it in their test harness, so the suite runs with zero API cost.

**Recommended split:** fake provider for fast unit tests of loop logic (tool-call parsing,
message accumulation, error handling); go-vcr cassettes for a handful of end-to-end
integration tests over a real recorded streaming tool-use turn.

**Golden files** — standard Go idiom with a `-update` flag. Applies well to snapshotting a
`View()`'s exact rendered bytes (catches styling regressions) and the sequence of tool calls
a loop produces for a scripted conversation.

**`teatest`** — `github.com/charmbracelet/x/exp/teatest`, still explicitly experimental.
`NewTestModel(t, model)` runs a model headlessly; drive with `Send`/`Type`, assert via
`FinalModel(t)` or `RequireEqualOutput` against a golden file; `WaitFor` waits for a string
to appear. Note: there's an open unmerged proposal
([bubbletea#1654](https://github.com/charmbracelet/bubbletea/issues/1654)) for a successor
framework, because maintainers describe teatest's async design as lacking a non-brittle
testing story. Use it, but don't build abstractions on top you can't swap out.

**Fuzzing** — `go test -fuzz` suits exact-string-replace edit logic well. Seed with real
edits, fuzz for panics and incorrect matches.

---

## 9. Distribution

**goreleaser v2.17.1** (2026-07-26) — binaries, checksums, archives, GitHub releases with
changelogs, and package-manager publishing from one YAML, triggered on tag push.

```yaml
builds:
  - env: [CGO_ENABLED=0]
    goos: [darwin, linux, windows]
    goarch: [amd64, arm64]
archives:
  - formats: [tar.gz]
    format_overrides:
      - goos: windows
        formats: [zip]
checksum:
  name_template: "checksums.txt"
```

**`CGO_ENABLED=0` caveat worth knowing:** Go's `net` package has two DNS resolvers, and
disabling cgo forces the pure-Go one, which reads `/etc/resolv.conf` directly and doesn't
support mDNS/`.local` or some corporate/VPN resolution. For an agent calling
`api.anthropic.com` over public DNS this never matters in practice.

**Homebrew:** goreleaser generates and pushes a formula into a tap repo (`homebrew-<name>`);
users get `brew install <you>/tap/<tool>`. **Timely:** the `brews:` config section is
documented as **deprecated** in favor of `homebrew_casks:` (goreleaser ≥2.10), which is now
recommended even for a plain CLI binary and adds shell-completion generation and macOS
notarization. Don't copy an older example's `brews:` block.

`.deb`/`.rpm`/`.apk` via `nfpms:` and Windows Scoop via `scoops:` are one extra YAML block
each on the same build matrix.

---

## Flagged / unverified

- **`modernc.org/sqlite`** — exact semver patch not pinned confidently (conflicting sources).
  Confirmed pure-Go/CGO-free, last published 2026-08-03, embeds SQLite 3.53.3. Run
  `go list -m -versions modernc.org/sqlite` at implementation time.
- **`bbolt` v1.5.0** — version well corroborated; release date varied between sources.
- **`creack/pty`** — pkg.go.dev shows a "highest tagged major version is v2" banner but
  GitHub releases show v1.1.24 as latest with no v2. Treating GitHub as authoritative.
- **`sandbox-exec` deprecation** — confirmed deprecated, no dated removal timeline found.
- **Crush's diff-rendering implementation** — the composition pattern in §2 is inferred from
  how the libraries are designed to fit together, **not** verified against Crush's source.
  The dedicated Crush research report supersedes this.
- **goreleaser `brews:`** — confirmed deprecated; not confirmed whether still functional.
- **Unified multi-provider libraries** (`any-llm-go`, GoAI SDK, `omnillm-core`) — surfaced in
  search, production maturity not independently verified. Exploratory, not recommended.
