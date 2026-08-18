# Vision

`rasp` exists so that any developer can use a simple coding agent that is safe by default and works with any model provider.
It lives in the terminal, ships as one static binary, and is built in the open; its author is the first and most demanding user, and it serves anyone who brings their own model access - an API key today, OAuth in a later phase (scope.md).
It turns plain-language requests into read, edit, and test cycles shown honestly as streaming text and readable diffs.
It owns exactly one thing: the loop between a request typed in a terminal and a verified, visible change to a working tree.

Daily use on a real codebase is the bar that keeps it honest: a tool abandoned at the demo stage teaches only the easy half (prd §2).
v1 measures that bar bluntly: the author stops opening Claude Code for real work (prd §8).

## Understanding wins on the core while it is built, pragmatism at the edges

The learning goal is the method, not the destination: the agent loop, tool dispatch, and context management are hand-written so their builder understands them line by line, and what that phase leaves behind for everyone else is a core one reader can hold (prd §2).
Rendering, diff computation, and syntax highlighting come from libraries, because reimplementing Glamour teaches nothing about agents.
No agent framework enters the tree; wrapping the loop in someone else's abstraction defeats the build (scope.md).
A reader unfamiliar with the codebase can trace one full turn in under thirty minutes, and that is a release criterion, not a hope (prd S9).
Once a layer is understood, a maintained dependency may take over an edge - never the loop - and only while that thirty-minute trace still holds.

## Fail loudly, never silently mis-apply

An edit that lands in the wrong place is worse than an edit that fails (prd P2).
"Fuzzy" means whitespace only; there is no approximate matching, because a near-miss match corrupts a file quietly (findings).
An adapter refuses what it cannot express - a tool list, an effort rung, an unknown stop reason - and never substitutes, clamps, or silently drops (decisions.md).
A reply the output limit cut short is a failure in a pipeline, not a completion with a caveat (decisions.md).
A check that cannot run must fail, not pass; every check gets broken on purpose and watched failing before it counts as evidence (AGENTS.md).

## Honest about what it is

The safety net is a blast-radius limiter, and every document says so plainly (prd P4).
Plan mode is a strong speed bump, not a proof, and an overstated guarantee is worse than the gap (design §7.3a).
A heuristic guard - an injection warning on untrusted tool output, say - may surface a concern; it never blocks and never claims coverage, so it cannot be mistaken for a boundary.
Estimates are shown as estimates; what the catalog cannot answer degrades visibly, never invisibly (design §10.2).
rasp never guesses: no model routing, no auto-selection, and nothing user-facing named "auto" that is not the permission mode (scope.md).
A convenience that only works by presenting rasp as another product - subscription auth through a borrowed client identity - may ship only as an explicit, off-by-default opt-in that names what it is and why it is fragile, never as a default and never silently (prd R6).
Implying more safety, more certainty, or more capability than rasp delivers is the actual harm.

## One core, thin frontends

The agent core emits typed events and knows nothing about terminals, enforced by a lint rather than by discipline (design §1).
Tools return data; the UI decides how to draw it, which is why headless mode is a second consumer rather than a second implementation (prd P3, P6).
Every planned extension has a named seam, and a feature that needs restructuring means the seam is wrong and gets fixed first (design §15).
The seam exists to keep frontends thin, not to make rasp a server: no client/server split, no wire protocol, however thin.

## A small surface, held deliberately

The built-in tool surface stays small - eight in v1 - and grows by decision, never by drift: every addition argues for itself, and every knob is a compatibility commitment (prd P7).
MCP is the pressure valve: users add tools without rasp adding surface, and no MCP concept escapes `internal/mcp` (design §8.0).
Modes are permission presets; the loop never branches on them, and a mode that cannot be a preset is a feature wearing a mode's clothes (prd P8).
One sanctioned detection: an environment with nobody to ask - CI, a container - may default to yolo, announced loudly at startup; an interactive session still reaches yolo only by explicit opt-in.
Nothing rasp does not control gets load-bearing trust: third-party servers run as subprocesses, their spec stays sealed behind one package, and their failures are ordinary tool errors (design §8).
The built-in tool count is the canary; if it grows mid-milestone, scope is slipping (prd R4).

## Decisions carry their evidence

Every rule traces to something read at source, measured, or shipped and reverted - never to taste (findings.md).
One system prompt serves every model until measurement says otherwise; a variant earns its place with a measured failure and a measured fix, and answers to the shared corpus ever after (design §11).
A settled decision records what reversing it looks like from the outside, because the rules worth writing down are the ones that fail silently (decisions.md).
When code and a document disagree, that is a bug in one of them and gets resolved, not worked around (AGENTS.md).

## Scope

rasp is not a security boundary, and no document may imply otherwise.
It is not an agent framework, not a client/server platform, not a model router, and not a multi-pane workspace (scope.md).
It ships no telemetry: nothing about usage ever leaves the machine, opt-in or otherwise.
Windows stays best-effort, stated loudly: binaries ship, but no release gates on a platform nobody here can verify (prd §3).
Sub-agents, LSP, hooks, and OAuth wait until the single-agent path is solid; they are phases, where the excluded list is decisions (scope.md).
Contributions are welcome and gated by the same two tests as the author's own work - core legibility and the current phase - so capability is added by roadmap and vision, never by the quality of an unsolicited PR.
The MVP is the smallest thing genuinely useful daily, not a demo.

A change aligns when it makes daily use better on a real codebase, keeps the core legible to one reader, fails loudly when it fails, and states its own limits plainly.
A change should be resisted when it adds a tool or knob without an argument, guesses where it could ask or refuse, claims safety or certainty it cannot demonstrate, moves agent logic into a dependency, or lets a protocol concept leak past its package.
