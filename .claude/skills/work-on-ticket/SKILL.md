---
name: work-on-ticket
description: Work a rasp ticket end to end — read the Linear issue, check deps, branch, implement, verify every acceptance criterion, open a PR, and close the issue out. Use this whenever a milestone ID appears (M0-02, M1-09, M5-04, P2-SUBAGENT), or the user says "next ticket", "pick up the CI one", "start M3-04", "implement the workspace confinement", or otherwise asks for work that maps to a milestone item — even when they never say the word "ticket". Also use when finishing, reviewing, or closing one out.
---

# Working a rasp ticket

There are ~91 MVP items. Doing them consistently matters more than doing any one of them
cleverly. This is the sequence, and the reasoning behind the parts that are easy to skip.

## 1. Read the ticket, and its spec

Tickets live in Linear and nowhere else — [project Rasp](https://linear.app/theocod3s/project/rasp-be0653f32d76),
one milestone per M0–M5 plus one per future phase. Find one by its ID: search the project for
`M0-02` and take the exact title match (`M0-02 · CI`). Fuzzy matches rank below it. The issue
carries size, deps, spec anchor and acceptance criteria.

Every ticket names a **spec anchor** (`design §13`, `prd §6.6`). Read that section before
writing anything: the ticket is a decomposition, not the requirement, and where the two
disagree the spec wins.

Check **Depends on**. Those are hard ordering — a blocked ticket genuinely cannot start. They
live in the description as prose, not as Linear blocking relations, so nothing enforces them
and nothing will stop you starting a ticket whose dependency is unmerged. Read them. If one is
unmerged, say so and stop rather than working around it.

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
then restore. This is the single highest-value habit in this repo: it is how all three of the
bugs behind CLAUDE.md's *"a check that cannot run must fail, not pass"* were caught, and none
of them was visible from a green run.

Then `just ci` — fmt-check, vet, build, test, race. Run it before pushing, not after.

## 5. Open the PR

Body shape that has worked:

- What changed and why, in the ticket's terms.
- The acceptance criteria as a checklist, each with **how it was verified** — not just ticked.
- Judgement calls you made, flagged as such, with the alternative. These are what the reviewer
  is actually for.
- Anything deliberately left out, and why.

Reference the work by milestone ID (`M0-02`), never the Linear key (`THE-6`) — the issue title
carries the ID, and git history outlives the tracker.

Don't merge your own PR unmarked. Run `/code-review`, and treat its findings as claims to check
rather than facts — it has a real false-positive rate, and rejecting a wrong finding with
reasoning is as valuable as fixing a right one.

## 6. Merge and close out

Rebase-merge; `main` is linear. Then move the issue to Done in the same pass. Linear is the
only record of where the work stands, so an issue left In Progress is what will mislead the
next session.

If the ticket's shape changed while you worked it — a criterion that turned out wrong, a
decision taken mid-flight — edit the issue description to match what was actually built. This
is the step that used to be "sync the two copies", and it is worth keeping now there is one:
a ticket that no longer describes its own outcome is worse than no ticket, because it reads
as authoritative.

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
