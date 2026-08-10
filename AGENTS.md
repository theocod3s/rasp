# AGENTS.md

rasp is a terminal coding agent in Go. Implementation just started: `internal/` is 25 empty
packages, each with a `doc.go`. The design documents are the asset, and nobody re-reads 2,100
lines mid-task — so this file is the index to the rules inside them that are cheapest to break
by accident. Every claim below cites its section; when code and a document disagree, that is a
bug in one of them and gets resolved, not worked around.

`CLAUDE.md` is a symlink to this file, and `.claude/skills/` symlinks into `.agents/skills/`.
The content lives once, under the vendor-neutral name, because rasp itself discovers `AGENTS.md`
and merely *accepts* `CLAUDE.md` (prd §6.5, internals §5.1) — a repo whose own product treats
`AGENTS.md` as the primary name should be shaped that way too. Symlinks rather than pointer
files because a pointer is something a reader can decline to follow, and these rules only work
when they are already in context.

## Invariants

**Layering**

- `internal/agent` owns the loop. No terminal code, no HTTP, no filesystem syscall, and **no
  knowledge of modes** — the loop never branches on plan/manual/auto/yolo (design §1, §2 table).
- `internal/permission` owns the four modes as presets over one approval ladder; `yolo`
  short-circuits before every other check (design §7, prd §6.7).
- Services are leaf packages that meet only through the interfaces in `llm` and `tool`. `agent`
  is the only package that composes them (design §1).
- No MCP type, error code, method name or protocol concept may leave `internal/mcp`. It exposes
  `tool.Tool` values plus `Start`/`Shutdown`/`Status`, and nothing else. The test: a new MCP spec
  revision must be a dependency bump plus changes confined to `internal/mcp/` (design §8.0, §8.5).

**Streaming**

- `Provider.Stream` returns `iter.Seq[Event]` and **never** a Go error. Model, request and
  runtime failures arrive as a terminal `EventError` — one error path, not two, which is what
  lets the retry classifier be a pure function over a message (design §3.1).
- Every event carries `Partial`, the full accumulated message. Consumers render `Partial`; they
  never reassemble deltas. The neutral message is allocated **outside** the stream loop so
  `Partial` is a stable pointer, not an allocation per token (design §3.1, internals §4.2).

**Tools**

- `Result.Content` is what the model sees. `Details` is a typed payload for the UI and is never
  serialized to the model. Tools return data; the UI decides how to draw it — which is also what
  makes headless mode free (design §3.4, internals §3.2).
- A failing tool is not a Go error: `Result{IsError: true}` with `err == nil`. The `error` return
  means "the tool itself could not run", and the loop converts that into an error result too
  (design §3.4, §12).
- `internal/tool/edit` is pure string functions with no file I/O. That is precisely what makes
  the four-rung match ladder fuzzable (design §2, §13).
- The agent takes one registry snapshot per turn and holds it for every step; `Set` is always
  sorted by name. The tool list sits inside the cached prompt prefix, so an unstable order
  silently destroys the cache on every request (design §3.3).
- Results land **by index** in a pre-sized slice, never appended as they complete: `tool_result`
  order must match `tool_use` order or the provider rejects the request (design §6 rule 6).
- Same-file mutations serialize on a **realpath-keyed** mutex; different files stay parallel.
  `EvalSymlinks` is load-bearing — `./a.go`, `a.go` and a symlink to it must take the same lock,
  or the mutex silently does nothing and two writes corrupt the file (design §6 rule 6).

**Safety**

- Every file tool routes through `internal/workspace` (`os.Root`), never `os` directly. `../`
  escapes and symlinks pointing outside the workspace are rejected (design §2, prd §6.6).
- A `tool_use` never exists without a `tool_result`. Commit the assistant message and its results
  together or neither, and repair history on load — `session.Sanitize` runs on **every** `Load`
  (design §4 invariant 1, prd §6.6).
- A response truncated by the output limit (`StopMaxTokens`) fails *every* pending tool call:
  truncated JSON can parse and validate while being semantically wrong (design §4 invariant 2).
- Each tool call runs under a panic guard; a panic becomes an error result and the process
  survives. This matters more with MCP in scope: a third-party server is code we did not write
  (design §4 invariant 4). It runs as a **subprocess**, never in our address space (§8).
- rasp is not a security boundary and no document may imply otherwise. Plan mode's redirection
  guard is a strong speed bump, not a proof (design §7.3a, prd §6.6).

**Process hygiene**

- **A check that cannot run must fail, not pass.** Three bugs here have had one shape: the
  absence of a signal read as a pass. `just fmt-check` captured `$(gofmt -l .)` and dropped the
  exit status, so a file `gofmt` could not parse reported clean. `arch_test.go` classified a
  package as absent when its files were excluded for the host `GOOS`, generated *zero* subtests
  for it, and called that green. The pre-push hook's first draft wrote `if ! just ci; then
  status=$?`, where `$?` is the status of the **negation** — always zero — so a failing build
  would have pushed. None is visible from a green run. When you write a check, decide what it
  does when the checker is missing, errors, or matches nothing, and make that path loud.
- stdout belongs to the UI. Logs go to a file via `internal/logx`, and no secret ever reaches a
  log record (design §2, M0-09).
- `go.uber.org/goleak` in `TestMain`. Goroutines are spawned per turn, per tool, per bash pump
  and per MCP server; a leak is a hung process on quit (design §13).
- `CGO_ENABLED=0` is load-bearing, not incidental. It is what cross-compiles the whole matrix
  from one runner, and why no cgo-linked dependency (`mattn/go-sqlite3`, tree-sitter) may enter
  `go.mod` (design §14).
- The MCP SDK is pinned exactly and never bumped unattended: read the spec changelog, bump the
  pin, run the fake-MCP-server suite and the real-server smoke test, and confirm the diff touches
  nothing outside `internal/mcp/` (design §14).

## Which source answers which question

| Question | Source |
|---|---|
| What rasp is, what it must do, milestones, risks | `docs/prd.md` |
| Does this ship in v1, wait, or never happen | `docs/scope.md` |
| Boundaries, interfaces, concurrency, storage — **the primary reference** | `docs/design.md` |
| Why a mechanism works at all, from first principles | `docs/internals.md` |
| Evidence behind a decision ("neo does X, and here's why") | `docs/findings.md` |
| What the next piece of work is, with deps and acceptance criteria | Linear — [project Rasp](https://linear.app/theocod3s/project/rasp-be0653f32d76) |

The last row is the only one that is not a file in this repo, and that is deliberate: tickets
lived in `docs/backlog.md` as well until the two drifted, so Linear is now the single record of
what the work is and where it stands.

## Conventions

- **Report a finished task in a few lines.** What changed, and anything that genuinely needs a
  decision. Not the reasoning, the verification narrative, or every caveat noticed along the way —
  the reader asks when they want more. Detail earns its place *before* the work, or when something
  failed, is uncertain, or went differently than asked. Success is the case that should be short.
- Go 1.26 (`go.mod`). Any toolchain 1.21+ fetches it via `GOTOOLCHAIN` unless it is set to
  `local`.
- `just ci` runs fmt-check, vet, build, test and race — run it before pushing. `just` is a
  **development dependency only**: it is not needed to run rasp and never ships. Every recipe is
  a one-line shell command, readable off the `justfile` and runnable by hand.
- **Adding an `internal/` package is a design change.** `arch_test.go` parses the package tree
  out of design §2 rather than copying it, and enforces two things: an entry in the §2 tree block
  (two spaces per level, trailing slash), and a `doc.go` whose comment opens `// Package <name> `
  and carries a separate paragraph starting `Does not contain:` with real substance behind it. Go
  lives under `cmd/` and `internal/` only; the module root takes repo-level `_test.go` files and
  nothing else. The §2 *table* is a separate, unenforced convention — it covers the packages worth
  calling out (14 of the 25), not every one, so add a row when the package has a boundary someone
  could plausibly get wrong.
- Refer to work items by their milestone ID — `M0-01`, `M1-09` — in commits, PR titles and
  bodies, code comments and anything written back to Linear. Every Linear issue title carries
  its ID (`M0-02 · CI`), so the ID is the handle in both places, and it survives leaving Linear
  in a way a key like `THE-5` does not: git history and code comments outlive the tracker.
- **Working a ticket — follow `.agents/skills/work-on-ticket/SKILL.md`.** It holds the
  sequence and, more usefully, the verification discipline that caught all three real bugs so
  far. Named here rather than left to the skill's own description, because that description
  reliably fails to trigger: "implement M0-02" reads like ordinary work, so the skill never gets
  consulted. A pointer from a file that is always in context is the fix.
- Split `internal/agent/agent.go` by concern (`step.go`, `tools.go`, `invariants.go`) before it
  passes ~800 lines. That file is this project's named collapse risk (design §2).
