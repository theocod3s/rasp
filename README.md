# rasp

A coding agent for the terminal.

Go, single static binary, chat-first TUI. Model-agnostic — native Anthropic plus any
OpenAI-compatible endpoint (OpenRouter, Groq, DeepSeek, Ollama, LM Studio, and the rest).

> **Status: early implementation.** The skeleton is in place — module, package tree, task
> runner — but nothing runs yet: the binary prints its version and exits. The documents
> below are the plan, and they're written to be read — the research findings in particular
> may be useful to anyone else building in this space.

## Why

Two reasons, and the project only succeeds if both hold:

1. **To understand how coding agents actually work.** Not by reading about them — by building
   the loop, the tool dispatch, the context management and the TUI from scratch, with no agent
   framework in the dependency list.
2. **To be good enough to use every day.** A learning exercise that nobody runs teaches you
   less than one you depend on.

## Documents

| | |
|---|---|
| [docs/prd.md](docs/prd.md) | What rasp is and why — goals, requirements, UX, milestones, risks |
| [docs/design.md](docs/design.md) | How it's built — architecture, interfaces, concurrency, storage |
| [docs/internals.md](docs/internals.md) | How it *works* — the mechanics, taught from first principles |
| [docs/scope.md](docs/scope.md) | MVP versus later versus never |
| [docs/findings.md](docs/findings.md) | What five existing agents settled — the evidence behind the design |

New to this? Read [docs/internals.md](docs/internals.md). It explains what a coding agent is
at the wire level — what a tool call actually looks like, why streaming changes the
architecture, and which "agentic workflow patterns" a coding agent really uses (fewer than
you'd think).

## Research

Every design decision here rests on reading real code. Five agents were taken apart at the
source level:

| Project | Why it mattered |
|---|---|
| [Crush](https://github.com/charmbracelet/crush) | Charm's own agent — Go, Bubble Tea v2, our exact stack |
| [neo](https://github.com/owainlewis/neo) | Small enough to read end to end (~25k lines of Go) |
| [pi](https://github.com/earendil-works/pi) | The most architecturally ambitious; a hand-rolled TUI framework |
| [opencode](https://github.com/sst/opencode) | A client/server split — and it *deleted* its Go TUI, which is worth understanding |
| Go ecosystem | Libraries, verified versions, sandboxing, distribution |

What that reading concluded is cross-cut in [docs/findings.md](docs/findings.md) — where the
projects converge, where they diverge, and which divergence we followed. The per-project
reports behind it are not kept.

## Planned for v1

- **Streaming from the first commit.** A blocking agent feels broken, and it's painful to
  retrofit.
- **Eight tools** — `read`, `write`, `edit`, `bash`, `grep`, `find`, `ls`, `todos`. Plus MCP
  (stdio) for everything else — you extend rasp with MCP servers, not by us adding tools.
- **Four modes** — `plan`, `manual`, `auto`, `yolo` — implemented as permission presets, with
  no mode-specific branch in the agent loop.
- **Real diffs.** Every edit renders as a colored unified diff. You should never have to run
  `git diff` in another terminal to see what the agent did.
- **A blast-radius limiter.** File tools confined to the workspace via `os.Root`; an approval
  gate on writes and shell commands. This is *not* a security boundary against a hostile
  model, and the docs say so plainly.
- **Sessions that can't be bricked.** An interrupted turn is repaired on load, not left to
  poison every subsequent request.

## Development

Go 1.26. `go.mod` names it, so any Go 1.21 or newer toolchain fetches it on demand — unless
you've set `GOTOOLCHAIN=local`, in which case install 1.26 yourself.

Tasks run through [`just`](https://github.com/casey/just). It is a **development dependency
only**: it is not needed to run rasp, it is not in the release archives, and a `go install`
never sees it.

```sh
brew install just     # or: cargo install just, or a binary from the releases page

just                  # list every recipe
just build            # compile every package
just test             # run the tests
just binary           # build ./rasp with the version stamped in
just ci               # fmt-check, vet, build, test, race
```

`just ci` is the full check sequence, and it is the one CI will run, so a green local run
should mean a green pipeline. If you would rather not install anything, every recipe is a
plain `go` command — read it off the [justfile](justfile) and run it directly.

## Name

`rasp` — a small tool that shapes things by taking material away. Unrelated to Runtime
Application Self-Protection, which unfortunately got the acronym first.

## License

[MIT](LICENSE)
