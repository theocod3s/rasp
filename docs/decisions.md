# Decisions

Rules that are already settled and that new code must not quietly reverse.

The bar is deliberately high. A decision earns a place here when someone who never saw the work
would otherwise re-derive it, or undo it while believing they were fixing something. Three things
that do **not** belong:

- **What is still open.** That lives in the Linear issue that will settle it.
- **What only shapes one file.** That lives in the code, where the reader is standing.
- **What a document already says.** `design.md` is the primary reference; this file does not
  restate it.

Each entry says what the rule is, why, and — the part worth the most — what reversing it looks
like from the outside, because none of these fail loudly.

---

## Retries never live in a provider SDK

Every vendor SDK is constructed with its retry count set to zero. Retrying is `internal/llm/retry`'s
job, for every provider, and design §12 puts it there because a vendor's timer ignores the turn's
context and sleeps for whatever `Retry-After` names — where ours is interruptible and refuses a
delay above its own cap. Left on, the two also multiply: three SDK attempts inside three of ours
is nine requests for one turn.

**Reversing it looks like:** nothing, until a user cancels a turn and the UI hangs for a minute, or
a rate-limited provider names a five-minute delay and rasp takes it. An SDK bump that changes the
default reintroduces this silently, so the count is set explicitly rather than left at zero by
luck.

*Settled in M0-07.*

## Configuring a credential clears the ones it replaces

When a credential is configured, the adapter also deletes the header any other credential would
have used. SDKs resolve credentials from the environment and *add* headers rather than replace
them, so a configured key and an ambient token both reach the wire and the server rejects the pair.

**Reversing it looks like:** every single turn failing for the subset of users who front the API
with a gateway — and the error names neither credential, so it reads as a bad key.

*Settled in M0-07, where `ANTHROPIC_AUTH_TOKEN` and a configured key both went out.*

## A base-URL adapter never falls back to its SDK's vendor, for the endpoint or the credential

The OpenAI-compatible adapter refuses a request with no `base_url`, and when no API key is
configured it *deletes* the Authorization header the SDK would otherwise fill from
`OPENAI_API_KEY`. Both defaults exist and both are wrong here: this adapter's endpoint is by
definition not OpenAI's, so falling back means sending a conversation, or a credential for one
vendor, to another.

This narrows the rule two entries down — *configuring a credential clears the ones it replaces* —
rather than contradicting it. Anthropic's adapter leaves its SDK's credential chain alone because
there the endpoint really is Anthropic's. The test is whether the vendor's default endpoint is the
one being talked to.

**Reversing it looks like:** a provider named `ollama` whose `base_url` never made it through
configuration, answering fluently from `api.openai.com` and billing an OpenAI account — or an
`OPENAI_API_KEY` exported for something else arriving at whatever a LAN address is running.
Neither errors. Tempting because both look like the missing half of the credential-chain rule.

*Settled in M1-23.*

## An adapter refuses what it cannot express; it never silently drops it

If a request carries something an adapter cannot put on the wire — tools it does not support, a
field with no equivalent, a block type it does not know — the adapter fails the request and says
what it could not send. It does not send a request with the piece missing.

**Reversing it looks like:** a turn that completes successfully and wrongly. The user's tools were
never offered, so the model answered in prose; or a `tool_result` went missing and the pairing
design §4 invariant 1 rests on broke at the last layer that could still see it. Both surface far
from the cause, and a dropped tool list surfaces as nothing at all.

*Settled in M0-07.*

## An assistant message with nothing left to send is skipped, never refused, never deleted

A transcript can hold an assistant message with nothing sendable in it: a turn truncated
mid-flight, a refusal (a 200 with no blocks), a cancelled turn, a block cut on its boundary. The
adapter skips it. The **role** decides and how the message got that way does not — the model wrote
it, so it is a state, not a bug. A *user* message with nothing sendable is still an error, because
rasp writes those.

The message stays in the transcript and on screen either way. A refusal is something the user
should be able to see happened; it is withheld from the request, not erased.

**This is not repair-on-read.** `session.Sanitize` runs on `Load`, and most routes to this state
happen in memory during a live turn — nothing is ever loaded. Repairing there would fix reopened
sessions, miss live ones, and read as though it had fixed both.

**Reversing it looks like:** one bad turn killing a conversation permanently. The message is on
disk, so every later request in that session fails the same way, and the only way out is editing
the file by hand.

*Settled in M0-07, and lifted in M0-07b into `llm.CheckSendable`, which takes the blocks an adapter
kept rather than the message it started from — so asking it about a raw transcript message, the one
way to get a wrong answer, does not typecheck.*

## An adapter drops a block on its type, never on its contents

Projection is positional: wire block *i* becomes neutral block *i*. An adapter may drop a block
whose **type** it will never surface — `redacted_thinking` — because a type is fixed the moment
the block opens, so every event shifts the blocks after it by the same amount and no index moves.
It may **not** drop a block because the block is currently empty. Contents arrive later.

Providers stream blocks concurrently and address them by index, not by whichever started last —
Anthropic's SDK says so in as many words. So a later block can receive its text first.

**Reversing it looks like:** the later block quietly taking the earlier one's index, and losing it
again when the earlier block fills. The UI redraws a paragraph as a different paragraph. This is
the only bug the adapter has shipped, and the comment justifying it asserted the opposite of what
the dependency documents — so the tempting version of this reads as obviously safe.

*Settled in M0-07, by shipping it wrong. `TestStreamSurvivesInterleavedDeltas` and a mutation keep
it settled. What the drop was for is settled in M0-07c: the empty block is legal, and emptiness
belongs to the send side.*

## An unrecognised stop reason is an error, never a finished turn

A stop reason the adapter does not have a mapping for produces a terminal error, not `end_turn`.
Mapping the unknown onto "finished" is the tempting default because it keeps the happy path
working.

**Reversing it looks like:** the loop handed a stopped turn dressed as a complete answer. Nothing
downstream retries it, because nothing downstream can tell it was not finished — and providers add
stop reasons between releases, so this is a *when*, not an *if*.

*Settled in M0-07, where `pause_turn` and `model_context_window_exceeded` were both live cases.*

## A session stays in the neutral format, whoever it is cloned or switched onto

Cloning a session onto a different model copies the neutral format to the neutral format, minus
whatever cannot cross. It does **not** write the target provider's shape to disk. Wire translation
stays in the adapter, where it happens per request.

Two reasons. Once one session file on disk is provider-shaped, everything that reads sessions has
to know which shape it is holding. And a session written in one provider's shape cannot be cloned
back.

**Reversing it looks like:** working perfectly for the first clone, then a one-way door — plus
`/model`, which must switch provider mid-session with no clone step to hang a translation on, so
the per-request path has to exist regardless.

*Settled while scoping M3-18.*

## Depth is one effort ladder, and an adapter refuses a rung rather than clamping it

`llm` carries a seven-rung ladder — `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max` — the
union of what Anthropic and OpenAI accept, so neither provider loses a level it supports. An adapter
handed a rung it cannot put on the wire fails the request and names it. It never substitutes the
nearest rung it *can* send.

The union is what makes refusal affordable: only two rungs are unsendable anywhere (`none` and
`minimal`, on Anthropic), so refusing is a rare path rather than a routine one. `Provider.Efforts()`
publishes each adapter's list, and the picker and the refusal read that same list — two copies drift,
and the drift is invisible, because a rung that stops being offered simply never appears in the menu.

Clamping is the tempting version, and it is what pi does: `clampThinkingLevel` walks to the nearest
supported rung, and `clampReasoning` folds `xhigh` and `max` down to `high`.

**Reversing it looks like:** a turn that ran at a depth nobody asked for — costing what `high` costs
while the status line says `max`. Nothing errors. The only symptom is answers slightly worse than the
setting promises, which reads as the model having a bad day.

*Settled while scoping M0-07a, after reading how Crush, opencode and pi each solve it. All three need
a per-model catalog to decide what a model accepts; rasp has none and will not have one (scope.md,
M3-12), so the list is per-protocol — which is also why `xhigh` can be offered and still rejected.*

## The stream contract describes what a provider sends, not what would be tidy

A content block that opens and closes without ever receiving a delta is legal. `CheckStream` does not
reject it, and a finished turn may carry one. Emptiness is handled where a message is built for the
wire — `messageParam` skips empty blocks, and `CheckSendable` makes that rule shared — not where a
stream is validated.

The contract shipped with the opposite rule and it could not hold. Every adapter-side way to satisfy
it required removing a block from a position, which is exactly what the index rule forbids, and the
index rule is the load-bearing one: a consumer has already drawn the block.

The **count** goes the same way: a finished turn may carry no blocks at all. An adapter drops
`redacted_thinking` on its type (above), so a turn whose only content was redacted thinking arrives
with none — and at that point it cannot be told from an adapter that mapped nothing. Nothing needs
the rule anyway, since an assistant message with nothing left to send is withheld from the next
request rather than refused (above).

**Reversing it looks like:** a re-added emptiness or count check that no adapter can pass, so the
first provider to send an empty block, or to end a turn on a block type the adapter drops, fails a
turn the API considered successful. Tempting because "a turn that finished had time to fill it" reads
as obviously true. The tightening is tempting for the same reason and fails a third real shape: "at
least one block *with content in it*" rejects an Anthropic turn that hit a stop sequence, which
returns exactly one empty text block.

*Settled while scoping M0-07c, and the count half in M1-29 — after that half was argued in both
directions a second time.*

## The standard loggers belong to rasp, not to whatever wrote to them

`logx.Init` takes Go's standard `log` and the `slog` default and points both at the log file. Not
because one SDK misbehaves, but because any dependency may write to either and rasp has no other
defence. Silencing a specific SDK fixes the instance someone found and none of the ones nobody has
hit yet.

**Reversing it looks like:** a line of dependency output painted across a full-screen TUI mid-turn,
with nothing on screen to say what wrote it. "It only writes to stderr" is not a mitigation — stderr
is no safer than stdout once the alternate screen buffer is in use.

*Settled while scoping M0-07d, where `anthropic-sdk-go` wrote two lines from inside the first
request, one of them naming an environment variable that was not set.*

## A reply the output limit cut short is a failure, not a completion

A non-interactive run exits non-zero when the output limit truncates a reply, printing the
truncated text to stdout all the same. Design §4's termination table calls `StopMaxTokens` with no
tool calls "Complete; warn the reply was cut off" — that is the loop's decision about whether to
take another step, which is a different question from what a process reports to a shell. A caller
reading stdout has no way of its own to tell a half answer from a whole one, so producing one and
reporting success is the only failure it cannot detect.

A refusal is not this case: the model finished, having declined, so the turn is complete and exits
0, as §4 says. An interrupt is its own case and went the same way — see below.

**Reversing it looks like:** nothing at all interactively, because a person reads the reply and can
see where it stops. In a pipeline it looks like a commit message cut mid-sentence or a generated
file missing its last function, discovered wherever that output is finally read, with a green exit
status behind it. Tempting because design §4 appears to say the opposite in as many words.

*Settled in M0-08, the first non-interactive consumer.*

## An interrupted turn is an error to whoever called it

`Send` returns an error wrapping `agent.ErrInterrupted` when a turn stopped because it was
cancelled rather than because the model finished, and the context's own error travels with it. The
turn still commits what arrived — the partial assistant reply, and a failed result standing in for
every call that did not run — so the transcript it leaves is one the next request can be built
from. Design §4's termination table says to commit and emit `EventTurnEnd`; what the *function*
reports is the question this settles.

The tempting version returns nil. A person pressed Esc, nothing malfunctioned, and the UI has
already drawn everything the turn produced. It is wrong for the reason above: the caller may not be
a person. A script, a test, or the headless runner cannot tell a turn that finished from one that
stopped part way, and success is the one failure it has no way to detect. A frontend that genuinely
does not care asks `errors.Is(err, agent.ErrInterrupted)` and stays quiet, in the one place that has
a user to explain it to.

**A frontend has to stay quiet in two places, not one.** The error `Send` returns is also emitted as
an `EventError`, because design §4's termination path emits one before the `EventTurnEnd` of every
turn that failed. A UI that filters the return value and draws every event still draws
`error: the turn was interrupted before the model finished` under a conversation the user
deliberately stopped. This paragraph was added after exactly that shipped: the model's cancellation
was correct, its tests were green, and the screen said otherwise.

**Reversing it looks like:** an automated run reporting a finished task with half of it done, and a
transcript that reads as though the model chose to stop there. Interactively it looks like nothing
at all, which is why this gets reversed by someone testing only in the TUI.

*Settled in M1-04, the work that added the cancelling; the second rule in M2-02, the first frontend
that had to obey it.*

---

## The agent serialises its event callback, so no consumer needs a lock

A batch runs its tools concurrently, so `EventToolStart` and `EventToolEnd` come from as many
goroutines as there are calls in flight. The agent holds a lock across the call into
`Config.Events`, which means a consumer is never entered twice at once and can keep unguarded
state. The cost is on us and it is bounded: an event handler that blocks now stalls every other
tool's events behind it, which is the same rule as before — be quick in there — with a wider blast
radius.

The tempting version is to drop the lock and document the callback as concurrent. It reads as
cheaper: the lock sits on the assistant-delta path too, which fires hundreds of times a second,
and a Bubble Tea frontend forwards straight into `program.Send`, which is already goroutine-safe.
Both are true and neither is the point. Pushing the requirement outward means every frontend, every
test recorder, and the headless runner each grow their own mutex, and the one consumer where
forgetting it is invisible is the TUI — precisely the one anybody actually looks at.

**Reversing it looks like:** nothing, until a turn dispatches more than one tool at once. Then a
consumer that appends to a slice corrupts it, and the report is a frontend crashing or dropping
events on batches only, in a build nobody ran under `-race`.

*Settled in M1-20, the work that made dispatch parallel.*

---

## The mutation lock is taken by the tool, not by the workspace method it wraps

`workspace.LockFile` hands out a per-file mutex and the mutating tools acquire it themselves:
`edit` from before its read to after its write, `write` from before its stat to after its rename.
The workspace's own `WriteFile`, `Rename` and `Stat` take nothing.

Pushing the lock down into those methods is the version that looks better. It is one place instead
of three, no tool can forget it, and it passes every test that two concurrent writes to one file do
not interleave. It is also wrong, because the unit being protected is not the write — it is the
read the write was derived from. Two edits that each lock their own write still both read the
original, and the second one's write is the first one's change deleted. Only the caller knows where
its read-modify-write began, which is why the lock is a handle it holds rather than a service it
calls.

**Reversing it looks like:** an edit reported as applied, with `Result.Content` naming the
replacements it made and a diff in `Details` to match, whose change is simply absent from the file.
Nothing errors and nothing is corrupt — one of two edits the model asked for in the same batch is
missing, so it reads as a model that changed its mind.

*Settled in M1-21, the work that added the lock.*

---

## A version is an annotated tag on main, minor per milestone

Versioning starts at `v0.1.0`, which marks M0 and M1 together — tagging began after both had
shipped, so they share the tag. From M2 on, each milestone exit gets `v0.<milestone>.0` on the
squash commit that closed it, and a fix that ships between milestones takes the next patch
number. `v1.0.0` is not the milestone counter reaching ten: it is M5's dogfood-week verdict,
declared deliberately or not at all.

The tag is the only place a version lives. `git describe` feeds the justfile's `-X main.version`
stamp, and M5's release pipeline triggers on the same tags — a `VERSION` file or a constant in
`main.go` beyond the `dev` fallback would be a second source, and two sources drift until the
binary lies about itself.

**Reversing it looks like:** nothing — a skipped tag just means `rasp --version` reports a bare
commit hash again, a bug report names a build nobody can map to a milestone, and the release
pipeline arrives in M5 with no convention to trigger on, so one gets invented under ship
pressure.

*Settled at the M2 kickoff, before any M2 ticket.*

---

## The agent → UI bridge copies the accumulated message, and drops deltas rather than the turn

Two rules at one seam, both about the same handful of bytes crossing between the turn's goroutine
and Bubble Tea's.

**The bridge copies.** `Event.Message` on an assistant delta is the provider's own message,
mutated in place for the rest of the step (design §3.1). That is exactly right for a consumer
rendering inside the callback, and exactly wrong for one that hands the pointer to another
goroutine: `Update` stores it and `View` reads it while the turn is still writing. The copy is
per block, not per byte — a `Block` holds a string header and the provider replaces the string
rather than writing through it — so it costs about what keeping the pointer would.

**And it drops deltas, never anything else.** The mailbox between the callback and `Program.Send`
is bounded. A full one discards a delta, because every event carries the whole accumulation and
the next delta says everything the dropped one would have; anything else waits, however slow the
UI is. A dropped turn end is a UI busy for the rest of the session with nothing later to correct
it, which is a worse trade than a turn stalled behind a frame.

The tempting version of the first rule is written out in internals §4.3 as `m.current =
ev.Partial`, which is that race — so someone deleting the copy is not being careless, they are
following a document. §4.3 has been corrected; this entry is the part that survives a reader who
never opens it. The tempting version of the second is one policy for the whole channel, in either
direction: drop everything, or block on everything.

**Reversing it looks like:** for the copy, nothing outside `-race` — the UI draws plausible text
either way, because a message caught mid-write is still a valid one. For the drop policy, a turn
that finished with the spinner still running, on long replies only, on slow terminals only.

*Settled in M2-01, the work that built the bridge.*

---

## The loop's approval seam speaks in tool calls, not in permission requests

`agent.Approver` takes an `llm.ToolCall` — an id, a name and the arguments the model sent. It does
not take a `permission.Request`, and `internal/agent` does not import `internal/permission`.

The obvious simplification is to pass the request the service actually wants. It fails on what a
request holds: an `Action`, a resolved `Path`, a `Command`. None of those is in a tool call. Filling
them means knowing that `read` is a read and `write` is a write, and which JSON field of each tool's
arguments is the path — per-tool semantics, in the one package design §2 gives no knowledge of tools
beyond the interface. A grant is keyed on exactly those fields (§7.7), so a loop that half-filled
them would hand the user grants that cover nothing or cover everything. The mapping belongs to the
adapter the composition root writes, which knows both sides because composing them is its job
(design §1).

**Reversing it looks like:** an import of `internal/permission` into `internal/agent` that reads as
tidying, followed by a `switch` on tool name to work out the action — the mode branch design §7
exists to keep out of the loop, arriving by way of a type.

*Settled in M2-15, the work that made approval a serial barrier.*

---

## A config override merges into the preset as data, and the result compiles once

`permission.Merge` lays a user's `modes.<name>` onto the built-in `PermissionSet`, and
`permission.Compile` turns the single merged set into one `Rules`. Nothing wraps a compiled
`Rules` in another that answers first.

The wrapper is the obvious shape — two layers, ask the override, fall back to the preset — and it
breaks two things that no test of its own would notice. `Rules` has an optional companion,
`Explainer` (design §7.3a): a set that refused for a reason it can name says so, and a wrapper
that forwards `Resolve` without `Explain` still denies, so the only thing lost is the sentence
telling the model what to do instead. And resolving in two passes abandons design §7.3's
specificity ordering *across* the layers: a broad `"go *": "allow"` in a config would answer
before the preset's narrower `"go test*": "ask"` ever ran, which is precisely the carve-back plan
mode is built on.

**Reversing it looks like:** a small `type overlay struct{ over, base Rules }` added to "layer the
user's config on top of the preset". It reads as composition, it compiles, and both losses are
invisible from a green run.

*Settled in M2-16, the work that made the modes data.*
