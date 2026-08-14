---
name: comment-density
description: Decide which comments to write, keep, compress or cut in rasp's Go code. Use when writing a new file or package, when a review says a comment is unclear or unnecessary, when a file's comment-to-code ratio is climbing past ~25%, and as the last pass before opening a PR. Also use when asked to "clean up the comments", "this is over-commented", "trim the docs in <package>", or when a review loop has been adding justification to comments as it goes.
---

# Comments in rasp

Code documents itself through naming and structure. A comment earns its lines by
recording something the code **cannot** say. Everything else is cost: it is read
on every visit, it drifts out of date silently, and a file that is a third
prose is a file nobody finishes.

The failure mode this exists for is not laziness. It is a review loop: each
round asks "why?", the answer gets appended to the comment, and nothing is ever
taken back out. `internal/llm` shipped at 45% of its non-test lines that way,
one file at 57%. Every paragraph was true. Most of them were arguing.

## The test

Ask of each comment: **would a competent Go engineer, reading this code and the
error strings in it, be worse off without this?**

### Keep

- **Why a rule exists at all**, when the code shows only that it does.
- **Which wire shape or provider behaviour forced it.** "Anthropic's
  `content_block_start` carries `"input": {}` for every tool_use" is not
  derivable from anything in the repo.
- **Why a rule is deliberately not stricter.** These are the most valuable
  comments in the codebase and the easiest to delete by accident, because a
  loose rule looks like an oversight. Deleting one invites the next person to
  tighten it and reject real traffic.
- **A hazard with a named consequence.** "an unstable order silently destroys
  the cache on every request" — not "be careful with ordering".
- **A decision tried the other way and reverted.** Say that it was tried.
- **An invariant a plausible simplification would break.**

### Cut

- Anything that restates the identifier. `// EnvBinding maps an environment
  variable onto a config key` above `type EnvBinding struct{ Var, Key string }`.
- Anything that narrates the obvious, or re-argues a decision the code makes
  plain.
- **Anything the error string below already says.** Well-written errors in this
  repo are full sentences with reasoning; a comment repeating one is a second
  copy to keep in step.
- **Anything already stated in the source file the test exercises.** The source
  is where someone changing the rule is standing.

### Compress

Most real offenders are here: one true idea in six sentences. One or two
sentences is usually enough. When unsure whether something is load-bearing,
**keep it and shorten it** rather than dropping it.

## Say it once

The single largest source of bloat found in the August 2026 audit was not bad
comments — it was good ones written four times. "A project config arrives with
`git clone` and nobody reads it" appeared in `approval_test.go`, `mode_test.go`,
an error string in `expander.go`, and another in `validate.go`.

Pick the one place a reader is standing when the rule matters, put the reasoning
there, and let the others be a clause. For a rule enforced in code, that place
is the code. For a rule about a package boundary, it is the doc.go.

## Worked example

From `internal/llm/message.go`, before — six paragraphs on a five-line method,
three of them restating each other:

```go
// Arguments is a tool call's arguments as anything sending them should read them:
// the bytes that arrived, or an empty object when those bytes are not one. For
// any other block type it is whatever Input held, which should be nothing.
//
// Exported because the substitution has to hold everywhere a message is used, not
// only where one is written. A turn cut off at the output limit is committed with
// its fragment — design §4 invariant 2 fails the call and the block stays — and
// the loop keeps going, so the NEXT request is built from that same in-memory
// message. An adapter reading Input directly would put `{"pa` on the wire and
// take a 400 for a message already in history; reading this cannot.
//
// MarshalJSON writes a block: its arguments through Arguments, and none of the
// fields belonging to another block type.
//
// The substitution exists because of one state the rest of the system requires:
// a response truncated at the output limit is committed, tool_use block and all
// (design §4 invariant 2 fails every call in it), and that block's arguments are
// a fragment cut mid-object. json.Marshal validates a json.RawMessage, so
// without this the whole message fails to encode and returns zero bytes — and a
// message that cannot be written cannot be committed together with its results,
// which is invariant 1.
//
// An object rather than merely valid JSON, because `null` is both valid and how
// an OpenAI-compatible endpoint normalises an empty arguments string — and a
// tool_use whose input is null is rejected on replay exactly like one with no
// input at all.
//
// Absent arguments go the same way, and for the same reason: [...]
```

After — the same facts, none of the argument:

```go
// Arguments is a tool call's arguments as anything sending them should read
// them: the bytes that arrived, or `{}` when those bytes are not an object. The
// substitution is BlockToolUse-only; any other block type gets Input back as it
// stands, which should be nothing.
//
// The state it exists for is a turn truncated at the output limit, committed with
// its tool_use block and a fragment cut mid-object (design §4 invariant 2 fails
// the call; the block stays). Three shapes are rejected on replay — a fragment,
// which json.Marshal refuses so the whole message encodes to nothing; no input at
// all; and `null`, which is how an OpenAI-compatible endpoint normalises an empty
// arguments string. Substituting `{}` keeps the block for the failing tool_result
// to point at, and loses only arguments the guard exists to refuse.
//
// Exported because the loop keeps running after a truncated turn, so the next
// request is built from that same in-memory message.
```

Every wire fact survived. What went was the third restatement of the truncation
story, a paragraph about a method two lines below, and the sentence explaining
why the method is exported *twice*.

**The sentence about other block types was cut first, and a review put it back.**
It reads like scope-narrowing boilerplate, and it is not: `Arguments` returns
`Input` untouched for anything that is not a tool_use, so a doc that omits the
qualifier is wrong about behaviour rather than merely terse. That is the shape to
watch for — a clause carrying a boundary, wearing the clothes of ceremony.

## Where the bar sits

Roughly: under 15% comment lines for a file that mostly does something, rising
for a file that mostly *declares* something. Type definitions and contracts are
the exception, and not a narrow one — after the audit cut 42% of the comments in
these two packages, five files still sit above 25%:

    51%  internal/config/shell_windows.go   one function, all of it Windows
                                            quoting rules nothing else records
    50%  internal/llm/provider.go           88 lines, 20 of them the stream
                                            contract every adapter satisfies
    40%  internal/llm/message.go            a union whose every field carries a
                                            wire fact
    39%  internal/llm/event.go              the same
    29%  internal/config/config.go          the settings schema

All five are right where they are. The per-field facts do not shrink when the
file does, so a small declaration-heavy file runs high by construction, and
driving these to 15% would mean deleting wire shapes. **A ratio is a smell, not a
rule.** Judge the comments, then check the number — and if a file is above the
bar for a reason you can name in a sentence, it is fine.

To measure:

```bash
for f in $(git diff --name-only main -- '*.go'); do
  # A deleted or empty file has no ratio. Say so rather than skipping in silence:
  # unguarded, `wc -l < gone.go` leaves the arithmetic an empty operand, and the
  # loop prints a syntax error, carries on, and still exits 0.
  [ -s "$f" ] || { echo "  --   $f (deleted or empty)" >&2; continue; }
  # `grep -c ''`, not `wc -l`: wc counts newlines, so a file with no trailing one
  # is undercounted and the ratio comes out too high — with no error, which is
  # the worse half. gofmt makes that unreachable for .go files today; the check
  # should not depend on that staying true.
  printf "%3d%%  %s\n" "$(( $(grep -cE '^[[:space:]]*//' "$f") * 100 / $(grep -c '' "$f") ))" "$f"
done | sort -rn
```

## Two rasp-specific rules

- **Never put a milestone ID in a code comment** — `M0-06`, `P2-SUBAGENT` — and
  that includes the `justfile`, CI workflows and `.goreleaser.yaml` (AGENTS.md).
  The tracker is private and this repo will not be. The same reasoning bars
  "the ticket's third acceptance criterion" from a test doc comment: it reads as
  authoritative and points at nothing.
- **`doc.go` is exempt.** `arch_test.go` requires every `internal/` package doc
  to open `// Package <name> ` and to carry a separate paragraph starting
  `Does not contain:` with real substance. That is a checked convention, not
  chatter. Do not trim it to make a number look better.
