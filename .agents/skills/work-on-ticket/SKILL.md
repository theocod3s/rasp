---
name: work-on-ticket
description: Work a rasp ticket end to end — read the Linear issue, check deps, move it to In Progress, branch, implement, verify every acceptance criterion, open a PR, and close the issue out. Use this whenever a milestone ID appears (M0-02, M1-09, M5-04, P2-SUBAGENT), or the user says "next ticket", "pick up the CI one", "start M3-04", "implement the workspace confinement", or otherwise asks for work that maps to a milestone item — even when they never say the word "ticket". Also use when finishing, reviewing, or closing one out.
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

## 2. Claim it, then branch

**Move the issue to In Progress before writing anything** — always, even for a ticket that will
take ten minutes. Linear is the only record of where the work stands, so a ticket sitting in
Todo while a branch exists is the same failure as one left In Progress after merge (§6): the
next session reads the board and gets the wrong answer. Do it as soon as deps are clear and
before the first commit, so the record is right for the whole window someone might look.

Branch `rasp/<id-lowercased>-<slug>` — `rasp/m0-02-ci` — from an up-to-date `main`.

## 3. Implement

Read `AGENTS.md` first if you haven't this session. It carries the invariants that are cheapest
to break by accident, and violating one is far more expensive than the ticket is worth.

## 4. Verify every acceptance criterion, one at a time

The criteria are the definition of done, written so someone who didn't do the work can check
them. Go through them individually rather than concluding "it works".

**A check you have only seen pass is not evidence.** If the ticket adds a test, a lint, a CI
gate — anything whose job is to fail — break the thing it guards on purpose and watch it fail,
then restore. This is the single highest-value habit in this repo: it is how all three of the
bugs behind AGENTS.md's *"a check that cannot run must fail, not pass"* were caught, and none
of them was visible from a green run.

The trap inside that habit: **a break that stops the package compiling proves nothing.** M0-04
replaced the string-aware JSONC comment stripper with a naive one and the suite went red — but
red from the build, so no test ran and none was shown to catch anything. `if false {` does it
too, whenever it leaves a variable or an import unused. If your break produces a build error, it
is the compiler that caught you, not the check.

And **assert the right failure, not just a failure.** M0-05's timeout test checked that *some*
error came back. A later fix broke working credential helpers by returning a different error a
second sooner, and the test stayed green — passing precisely when the thing it guarded was being
destroyed. `err != nil` is the assertion most likely to survive its own subject.

Once there is more than a handful, script it: apply one mutation, run the tests, restore, and
print caught/not-caught per case. M0-04 ran twenty-four that way in under a minute, which is the
difference between doing this for the two obvious guards and doing it for all of them. **Restore
by writing back the bytes you replaced** — never `git checkout -- .`, which resets the whole tree
to HEAD and takes every other uncommitted change in the working directory with it. It did.

**A not-caught result has three causes and only one is the check's fault.** The check may be
weak; the test may never reach the line you mutated; or the `-run` pattern may match no test at
all, which exits 0 and reads exactly like a pass. M0-06 hit the second (a guard reached only
after the stream had already been abandoned) and the third (a subtest name with an apostrophe in
it). Make the harness shout when nothing ran, and confirm the line runs, before touching the
check.

Then `just ci` — fmt-check, vet, build, test, race. Run it before pushing, not after.

## 5. Open the PR

**Write the description for an engineering director.** Someone two levels out from the code
should be able to read it and know what shipped and why it matters — so lead with what the PR
*does*, in plain language, and describe the capability or the fix rather than the mechanism.
Then anything the reviewer genuinely needs: a judgement call and its alternative, a risk, a
piece deliberately left out.

**Don't list the acceptance criteria.** They live in the ticket and the reviewer can open it;
copying them across turns the description into a form nobody reads. Verification already
happened in §4 and does not get re-staged here.

**Keep it short enough that the reader reaches the diff.** M0-04's body ran well past a thousand
words, re-arguing reasoning that was already in the commit messages and the code comments — and
a description nobody finishes is one that fails at the only job it has, which is to get someone
into the diff with the right questions. If a decision needs a paragraph to defend, that paragraph
belongs in the code, where the next reader is actually standing.

Reference the work by milestone ID (`M0-02`), never the Linear key (`THE-6`) — the issue title
carries the ID, and git history outlives the tracker.

Don't merge your own PR unmarked. Run `/code-review`, and treat its findings as claims to check
rather than facts — it has a real false-positive rate, and rejecting a wrong finding with
reasoning is as valuable as fixing a right one.

**Then remember the fixes are code nobody has reviewed** — and keep going until it converges.
M0-04's first pass produced 345 lines of them, and a second over that delta found four more.
M0-05 took five passes for 17 findings, and the clincher is that pass 3's fix *caused* pass 4's
worst bug: `cmd.WaitDelay` starts its timer when the child exits, so the fix for a helper that
hung turned into a failure for every helper that worked. Stop when a pass reports its own
findings already fixed, not when you are tired of the loop.

## 6. Merge and close out

Squash-merge, with a message you wrote rather than the one GitHub concatenates — see the rule
in `AGENTS.md`. Then move the issue to Done in the same pass — the other half of §2's rule, and
an issue left In Progress after merge misleads exactly as much as one never started.

If the ticket's shape changed while you worked it — a criterion that turned out wrong, a
decision taken mid-flight — edit the issue description to match what was actually built. A
ticket that no longer describes its own outcome is worse than no ticket, because it reads
as authoritative.

## 7. Report back

Once the PR is merged and the issue is Done, **write the closing summary for a CTO**: someone who
knows the project but has not seen this work, will not open the diff, and cannot be assumed to
know what any identifier or section number refers to. So expand a term the first time it appears,
and describe every change by what it now *does* before naming what it touched.

- One or two sentences on what the work delivers, in plain language.
- Then **file by file, what changed in each** — a sentence or two per file, its new behaviour and
  the reason for it, not a restatement of the diff.
- Anything that genuinely needs a decision.

Short, not thin: no reasoning trail, no verification narrative, no caveats gathered along the
way. The reader asks when they want more.

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
