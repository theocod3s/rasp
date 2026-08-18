# Architecture

The map, not the territory. [docs/design.md](docs/design.md) is the primary reference -
boundaries, interfaces, concurrency, storage - and this file only orients you in it, so every
claim here cites the section that argues for it. When code and a document disagree, that is a
bug in one of them and gets resolved, not worked around ([AGENTS.md](AGENTS.md)).

The bar this file serves: a reader unfamiliar with the codebase can trace one full turn, user
input to rendered output, in under thirty minutes (prd §8, S9). Start here, then read design §5.

## One process, four layers

```
cmd/rasp            wiring only
  ├─ internal/tui        Bubble Tea v2, chat-first     ┐ frontends: consume
  └─ internal/headless   rasp run -p, prints to stdout ┘ agent.Event, nothing else
        │
  internal/agent     THE CORE - the loop, the invariants, typed events.
        │            No terminal code, no HTTP, no filesystem syscalls,
        │            no knowledge of modes (design §1, §2).
        ├─ llm         neutral Message/Block/Event + Provider (anthropic/, openaicompat/, retry/, fake/)
        ├─ tool        interface, reflection schemas, registry + per-turn snapshots (builtin/, edit/)
        ├─ mcp         stdio subprocess manager; no MCP concept leaves this package (§8.0)
        ├─ permission  the approval ladder and the four modes (§7)
        ├─ session     append-only JSONL, Sanitize on every Load (§4, §9)
        ├─ compact     prune, then summarize (§11)
        ├─ prompt      ordered system blocks with cache flags (§11)
        ├─ workspace   os.Root confinement, read-before-edit (§2)
        └─ config, auth, wakelock, logx (§10, §3.6, §6.1, §2)
```

Services are leaf packages that meet only through the interfaces in `llm` and `tool`; `agent`
is the only package that composes them (design §1). The full tree, which `arch_test.go`
enforces against design §2, lives there - adding an `internal/` package is a design change.

## The seams that carry the weight

- **`agent.Event` is the entire surface between core and frontends** (design §3.5). The
  headless runner exists to prove the seam is real: a stream with one consumer is
  indistinguishable from a stream shaped around that consumer (PR #35).
- **`Provider.Stream` returns `iter.Seq[Event]` and never a Go error**; failures arrive as a
  terminal event, and every event carries the full accumulated message (design §3.1). This is
  what makes retry a pure function and rendering a one-line job.
- **`tool.Tool` is one interface with two producers** - reflected Go structs for built-ins,
  opaque server schemas for MCP - and `Result` separates what the model sees from what the UI
  draws (design §3.2, §3.4).
- **The registry hands the loop one immutable snapshot per turn**, sorted by name, because the
  tool list sits inside the cached prompt prefix (design §3.3).
- **Modes live in `permission`, not in the loop** - three presets over one ladder, and yolo as
  a short-circuit ahead of it (design §7).
- **`internal/mcp` is a containment vessel**: subprocesses, pinned SDK, and a hard rule that a
  spec revision must be a dependency bump confined to that package (design §8.0, §14).

## Where things run and where they land

One goroutine table governs concurrency - single ownership, results by index, approval as a
serial barrier, a realpath-keyed mutex for same-file writes (design §6). Sessions are
append-only JSONL sharded by project key with no index, and that is a decision rather than a
deferral (design §9). Configuration resolves through one precedence chain with an origin
recorded per value (design §10). Context is managed by pruning stale tool output first and
summarizing only on real overflow (design §11).

## What to read for what

| Question | Where |
|---|---|
| What rasp is, what it must do, and why | [docs/prd.md](docs/prd.md) |
| Ships in v1, waits, or never happens | [docs/scope.md](docs/scope.md) |
| Boundaries, interfaces, data flow - the reference | [docs/design.md](docs/design.md) |
| Why a mechanism works, from first principles | [docs/internals.md](docs/internals.md) |
| The evidence behind a decision | [docs/findings.md](docs/findings.md) |
| Settled rules new code must not reverse | [docs/decisions.md](docs/decisions.md) |
| What rasp is refusing to become | [VISION.md](VISION.md) |
