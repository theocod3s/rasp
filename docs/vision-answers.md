# rasp vision review — recorded answers (verbatim)

Keep this file next to VISION.md; it is the calibration record the vision was rebuilt from.

## Batch 1 — 2026-08-18, text comments on the draft (before card verdicts)

- F-1, on the identity paragraph: "It's not only api key. oauth will be used later on too."
- F-2, on the identity paragraph: "it's not just for onw developer. It will be used by anyone later on as it will be open source."
- F-3, on "The forcing function is blunt: if the author still opens Claude Code for real work, rasp is not done": "That is not the forcing function."

### Edits made

- F-1 -> the identity line now reads "anyone who brings their own model access - an API key today, OAuth in a later phase (scope.md)".
- F-2 -> the opener now serves terminal-native developers generally; rasp is "built in the open" with the author as the first and most demanding user, not the sole audience.
- F-3 -> the Claude Code sentence is demoted from "the forcing function" to v1's measurement of the bar; the stated driver is daily use on a real codebase (prd §2), since a tool abandoned at the demo stage teaches only the easy half.

## Batch 2 — 2026-08-18

- H-1 (a ninth built-in tool, web_fetch), verdict in prose: "yes. is it going to be easy to implement?" — recorded as **In vision**.
- F-4, on the identity paragraph: "The goal is not for them to understand it line by line. Me saying understand line by line is just for while I'm building RASP. Eventually, the goal is for any developer to be able to use a secure simple coding agent that works with any provider. That's it. The learning part is just for me while I'm building but the long term vision is not just for that. Yeah so you can frame that how you want into the vision."

### Edits made

- H-1 yes -> the small-surface principle no longer treats eight as the identity: "The built-in tool surface stays small - eight in v1 - and grows by decision, never by drift." The mid-milestone canary stays: growth by drift is still the failure.
- F-4 -> the identity opener is now the durable goal: "any developer can use a simple coding agent that is safe by default and works with any model provider." The learning goal is reframed as the build-phase method ("the method, not the destination"), whose permanent residue is a core one reader can hold (prd S9). "Secure" is rendered as "safe by default" so the identity cannot contradict the honesty principle that rasp is not a security boundary.

## Batch 3 — 2026-08-18

- H-2 (Anthropic subscription OAuth behind a flag): **In vision** (recorded via card, no note). Preceded by two clarifying questions answered on the board: what the flag would do, and how the impersonation works / whether Anthropic permits it (it does not - it presents as Claude Code via a borrowed client ID and a spoofed system line; ToS gray area; breaks silently on enforcement change).

### Edits made

- H-2 In vision -> the honesty section gains a line permitting subscription auth ONLY as an explicit, off-by-default opt-in that names what it is and why it is fragile, never a default and never silent (prd R6). Framed this way because the verdict was "In vision" for the *flag*, and R6 already requires it arrive only as a separate explicit decision - so the vision records the guardrails the yes came with, not a blanket yes.

## Batch 4 — 2026-08-18

- H-3 (contributor adds capability early): **Conditional**, adopting the recommended rule verbatim as notes: quality does not decide a merge, the vision does; two gates - core legibility (the 30-minute-trace bar) and the current phase; a phase-3 feature arriving in phase 1 is tracked as that phase's proposal, not merged and not refused outright.

### Edits made

- H-3 Conditional -> Scope gains: "Contributions are welcome and gated by the same two tests as the author's own work - core legibility and the current phase - so capability is added by roadmap and vision, never by the quality of an unsolicited PR."

## Batch 5 — 2026-08-18

- H-3 re-recorded via card as **In vision**, notes = the two-gate rule (final verdict; supersedes Batch 4's Conditional, same reasoning, so the Scope line stands unchanged).
- H-4 (opt-in telemetry): **Off mission**, no notes.
- H-5 (SDKs after the learning goal completes): **Conditional**, no notes.

### Edits made

- H-4 Off mission -> Scope gains: "It ships no telemetry: nothing about usage ever leaves the machine, opt-in or otherwise." (Worded around usage data deliberately, so the models.dev catalog *download* stays consistent.)
- H-5 Conditional -> Understanding section gains: "Once a layer is understood, a maintained dependency may take over an edge - never the loop - and only while that thirty-minute trace still holds." The condition is rendered from the card's own framing (edges vs loop, legibility bar); refine on the board if the intended condition differs.

## Batch 6 — 2026-08-18

- H-6 (rasp serve / editor protocol): **Off mission**, notes: "No client/server split".
- H-7 (auto-yolo inside containers and CI): **In vision**, no notes.
- H-8 (prompt-injection scanning on MCP output): **Conditional**, no notes.
- H-9 (Windows first-class): **Off mission**, no notes.

### Edits made

- H-6 -> "One core, thin frontends" gains: the seam keeps frontends thin, it does not make rasp a server - no client/server split, no wire protocol, however thin.
- H-7 -> small-surface section gains the sanctioned detection: CI/containers may default to yolo, announced loudly at startup; interactive sessions still need explicit opt-in. NOTE: this amends prd §6.7 ("yolo unreachable except by explicit opt-in") and success criterion S12 for the non-interactive case - the docs need a follow-up edit so code and vision agree.
- H-8 Conditional -> honesty section gains: a heuristic guard may surface a concern; it never blocks and never claims coverage, so it cannot be mistaken for a boundary. (Condition rendered from the card's warning-card framing; refine if the intended condition differs.)
- H-9 -> Scope gains: Windows stays best-effort, stated loudly - binaries ship, but no release gates on a platform nobody here can verify.

## Batch 7 — 2026-08-18

- H-10 (a prompt variant for a model the author never uses): **In vision**, no notes.

### Edits made

- H-10 -> "Decisions carry their evidence" gains: one system prompt serves every model until measurement says otherwise; a variant earns its place with a measured failure and a measured fix, and answers to the shared corpus ever after (design §11).

All ten hypotheticals answered. Final ledger: H-1 In vision · H-2 In vision · H-3 In vision (two-gate rule) · H-4 Off mission · H-5 Conditional · H-6 Off mission · H-7 In vision · H-8 Conditional · H-9 Off mission · H-10 In vision.





