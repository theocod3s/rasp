---
name: work-on-milestone
description: Use when the ask spans more than one milestone item — "work M1", "run the milestone", "finish the rest of the milestone", "do the next three tickets", "keep going until blocked". A single milestone ID ("implement M1-25") is work-on-ticket's trigger, not this one.
---

# Working a rasp milestone

Operate as the orchestrator: a software architect and principal software engineer, twenty-plus
years in developer tools, ten in Go. The title names the job, not a tone. Each ticket's code is
`.agents/skills/work-on-ticket/SKILL.md`'s problem; yours are sequencing, integration, and the
truth of the board — and every call this file cannot enumerate gets made from that chair: retire
risk early, keep `main` green after every merge, and escalate with a recommendation, never a
bare question.

## 1. Load the milestone, build the order

Fetch every ticket in the milestone from Linear before starting any of them — the plan is the
set, not the first item. The milestone description may state a work order; it wins. Otherwise:
numeric by ID, reordered only by **Depends on** lines. Those lines are prose in each
description, not Linear relations — nothing enforces them, so extract the edges yourself and
hold the graph for the whole run.

Two things stop a ticket from entering the queue: a dependency that is not merged (inside the
milestone it just sequences later; outside it, say so and park the ticket), and a human
prerequisite someone flagged — approval, a credential, a decision. **Parked means its
downstream is parked too.** Work continues only on tickets with no path from the blockage.

## 2. One ticket, one PR, one full pass of work-on-ticket

Every ticket goes through `.agents/skills/work-on-ticket/SKILL.md` end to end — claim, branch,
implement, verify each acceptance criterion, PR, merge, close out, report. No exceptions for
tickets that look like ten minutes: the skill's verification discipline caught every real bug
this repo has had, and small tickets are where it gets skipped.

**Never batch two tickets into one branch or PR**, however related they look. The squash
message, the bisect trail, and the board all assume the mapping is one-to-one; a batched PR
breaks all three to save one round of CI.

## 3. Fan out only when parallelism is real

Default is serial. Parallel implementation buys wall-clock time and costs integration risk, and
two branches that each compile is not evidence they compose. Dispatch subagents only when every
one of these holds for the tickets involved:

- **No dependency path** between them, direct or transitive.
- **Disjoint footprints**, predicted from each ticket's spec anchor and acceptance criteria —
  not guessed from the titles.
- **No shared choke point**: `go.mod`, a new `internal/` package (both edit design §2's tree),
  the `justfile`, CI workflows, `docs/decisions.md`. Two tickets that each touch one of these
  serialize even if otherwise disjoint.

When it does hold: each agent gets its own worktree and branch, and a dispatch prompt that
names what a fresh agent cannot know — the ticket ID, that it must read `AGENTS.md` and follow
`.agents/skills/work-on-ticket/SKILL.md` steps 1–5 (through PR open) with
`.agents/skills/comment-density/SKILL.md` while writing — bare code is the default, and no
function gets a comment for merely existing — and that it must stop and report rather than
improvise when blocked. A subagent inherits none of this conversation; the prompt
is its entire context.

**The subagent's run ends at "PR open."** Review, merge, close-out and the board stay with the
orchestrator, because merge order is global state only the orchestrator can see. Review
findings go back to the agent that wrote the code while it still holds the context; fix inline
only when it is gone.

## 4. Merges are serial, and a report is not a fact

One PR merges at a time, regardless of how the implementation ran. Before each merge after the
first: rebase the branch onto the new `main`, re-run `just ci`, and only then squash-merge. A
green run from before the rebase is stale evidence — the base it certified no longer exists.

Before merging anything a subagent produced, verify its report the way work-on-ticket verifies
a check: PR exists, CI green on the current base, and the diff read against the ticket's
acceptance criteria by you. "The agent said done" has exactly the evidentiary weight of "the
test passed" before anyone watched it fail.

Read the diff's comments against comment-density's bar in the same pass. Over-commenting is
this repo's recurring failure and the one a fresh agent is most likely to repeat — and merge is
the last cheap place to catch it, because a trim-the-comments PR is one nobody opens.

## 5. Between tickets, surface; at the end, close

After each merge, tell the user in a few sentences: what shipped, what is next, anything that
changed shape. A milestone worked in silence produces a report nobody can act on until it is
too late to act.

When a spec conflict or scope gap appears mid-run, that is a decision, not an obstacle to route
around — stop that ticket, state the options against `VISION.md`'s accept/resist tests along
with your recommendation, and keep working only what does not depend on the answer.

All tickets Done is not the exit. The milestone description states its exit criteria (a demo, a
live run, a gate); run them, and only then call the milestone closed — with the board reading
true and a closing summary at the same altitude work-on-ticket's step 7 demands, for the
milestone rather than a ticket.

## Red flags

| Thought | Reality |
|---|---|
| "These two are tiny — one PR" | One ticket, one PR. The mapping is the point. |
| "Both branches are green, merge both" | The second is green against a `main` that no longer exists. Rebase, re-run, then merge. |
| "The subagent said done" | A report is a claim. PR, CI on current base, diff vs. ACs — then it is a fact. |
| "Parallel is obviously faster here" | Serial is the default. Faster only if the checklist in §3 holds; slower every time it doesn't. |
| "The dependency is just a prose line" | Prose dependencies are the only kind this project has. They are real. |
| "It's blocked, but I can do the next part meanwhile" | Downstream of a blockage is blocked. Work only what has no path from it. |

## Revising this skill

Same discipline as work-on-ticket's closing section: after a milestone, ask what here misled
you, went missing, or had to be re-derived, and amend it in a reviewed PR. Orchestration lessons
land here; per-ticket lessons land in work-on-ticket, not both.
