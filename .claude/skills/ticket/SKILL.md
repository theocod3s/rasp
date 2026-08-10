---
name: ticket
description: Work a rasp backlog item end to end — read the ticket, check deps, branch, implement, verify every acceptance criterion, open a PR, and keep docs/backlog.md and Linear in sync. Use this whenever a backlog ID appears (M0-02, M1-09, M5-04, P2-SUBAGENT), or the user says "next ticket", "pick up the CI one", "start M3-04", "implement the workspace confinement", or otherwise asks for work that maps to a milestone item — even when they never say the word "ticket". Also use when finishing, reviewing, or closing one out.
---

# Working a rasp ticket

There are ~91 MVP items. Doing them consistently matters more than doing any one of them
cleverly. This is the sequence, and the reasoning behind the parts that are easy to skip.

## 1. Read the ticket, and its spec

`docs/backlog.md` is keyed by ID — search `### M0-02`. Every ticket names a **spec anchor**
(`design §13`, `prd §6.6`). Read that section before writing anything: the backlog is a
decomposition, not the requirement, and where the two disagree the spec wins.

Check `deps`. They're hard ordering — a blocked ticket genuinely cannot start. If a dependency
is unmerged, say so and stop rather than working around it.

**Don't invent scope.** If something looks missing it is usually deliberate — check
`docs/scope.md`'s "deliberately excluded" before adding it. If it's a real gap, raise it; don't
silently fill it.

## 2. Branch

`claude/<id-lowercased>-<slug>` — `claude/m0-02-ci`. Branch from an up-to-date `main`.

## 3. Implement

Read `CLAUDE.md` first if you haven't this session. It carries the invariants that are cheapest
to break by accident, and violating one is far more expensive than the ticket is worth.

## 4. Verify every acceptance criterion, one at a time

The criteria are the definition of done, written so someone who didn't do the work can check
them. Go through them individually rather than concluding "it works".

**A check you have only seen pass is not evidence.** If the ticket adds a test, a lint, a CI
gate — anything whose job is to fail — break the thing it guards on purpose and watch it fail,
then restore. This is the single highest-value habit in this repo, and both real bugs found in
M0-01 were of exactly this shape:

- `just fmt-check` reported success on a file `gofmt` could not parse, because `$(gofmt -l .)`
  captured stdout and discarded the exit status. It passed every test that only ever fed it
  well-formed input.
- The arch test classified a package as absent when its files were excluded for the host's
  `GOOS` — and then generated *zero* subtests for it, so the acceptance criterion silently went
  unenforced on the platform where nobody was looking.

Neither is visible from a green run. Both are obvious the moment you try to make them fail.

Then `just ci` — fmt-check, vet, build, test, race. Run it before pushing, not after.

## 5. Open the PR

Body shape that has worked:

- What changed and why, in the ticket's terms.
- The acceptance criteria as a checklist, each with **how it was verified** — not just ticked.
- Judgement calls you made, flagged as such, with the alternative. These are what the reviewer
  is actually for.
- Anything deliberately left out, and why.

Reference the work by backlog ID (`M0-02`), never the Linear key (`THE-6`) — the ID is what
`docs/backlog.md` is organised by and what survives leaving Linear.

Don't merge your own PR unmarked. Run `/code-review`, and treat its findings as claims to check
rather than facts — it has a real false-positive rate, and rejecting a wrong finding with
reasoning is as valuable as fixing a right one.

## 6. Merge and close out

Rebase-merge; `main` is linear. Then, in the same pass:

- Move the Linear issue to Done.
- Sync `docs/backlog.md` if the ticket's shape changed while you worked it.

**Both carry the ticket, so both drift.** This has already happened once — a decision recorded
in Linear was absent from `backlog.md` for a day. If you change one, change the other.

Watch the counts in `backlog.md`'s milestone map (`M5 | Ship | 9`, `91 items ... 109 in all`).
They are hand-maintained, nothing checks them, and adding or removing a ticket invalidates them
silently. Count with `grep -cE '^### M[0-9]+-'` rather than trusting the header.

## 7. Report back briefly

What changed, and anything that genuinely needs a decision. Not the reasoning or the
verification narrative — the reader asks when they want more. Detail belongs *before* the work,
or when something failed or went differently than asked.

---

## Revising this skill

This file should get better as tickets teach us things. After closing one out, ask: **did
anything here mislead me, go missing, or have to be re-derived?** If so, amend it in the
ticket's own PR, so the change is reviewed alongside the work that motivated it.

Two things keep this from rotting into a changelog:

**Only record what changes what someone does next time.** A rule that applies to one ticket is
a note in that PR, not a line here. If you can't name a future ticket it would have helped,
leave it out.

**Prune as you add.** Target ~150 lines. When you add something, look for what has stopped
earning its place — a step now enforced by a test, advice that turned out to be situational, a
caveat about a milestone long since shipped. A skill nobody reads to the end fails exactly the
way a 2,100-line design document does, which is why this file exists at all.

Say *why*, not just what. "Verify checks by breaking them" is forgettable; the `fmt-check`
example is not.
