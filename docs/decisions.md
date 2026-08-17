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

*Settled in M0-07. Lifting the rule into one shared predicate is M0-07b; the rule itself is not
open.*

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
it settled. What the drop was for is unresolved and tracked as M0-07c.*

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
