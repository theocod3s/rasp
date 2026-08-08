# Research: How coding agents are built

Source material for designing **rasp**, a Go coding agent with a TUI.

Each report was produced by reading the actual source of the target project — cloning
the repo and tracing the agent loop, tool system, provider layer and UI wiring — not by
summarizing READMEs. File:line references throughout point at the commit checked out at
research time.

## Status

| # | Target | What it is | Report | State |
|---|--------|-----------|--------|-------|
| 1 | [charmbracelet/crush](https://github.com/charmbracelet/crush) | **Go agent on Bubble Tea v2 — our exact stack.** 145k LOC | [crush.md](crush.md) | ✅ complete |
| 2 | [owainlewis/neo](https://github.com/owainlewis/neo) | Go coding agent, Bubble Tea v2 TUI, 25k LOC | [neo.md](neo.md) | ✅ complete |
| 3 | [earendil-works/pi](https://github.com/earendil-works/pi) | TypeScript agent, custom TUI framework, 253k LOC | [pi.md](pi.md) | ✅ complete |
| 4 | Go ecosystem scan | Libraries, verified versions, API shapes, distribution | [go-ecosystem.md](go-ecosystem.md) | ✅ complete |
| 5 | [opencode](https://github.com/sst/opencode) | Go TUI + TypeScript server, client/server split | [opencode.md](opencode.md) | ⏳ in progress |

Crush and pi were each read twice by independent agents; where the two passes disagreed, both
readings are noted in the report. neo and Crush are the highest-value references — neo for
being small enough to read end to end, Crush for being the mature version of the same stack.

## Synthesis

[findings.md](findings.md) cross-cuts the completed reports: where the three agree, where
they diverge, and what each divergence means for rasp's design.

## Reading order

If you only read two things: [findings.md](findings.md) for the decisions, then
[neo.md](neo.md) §3–4 for what an agent loop and tool system actually look like in Go.

After that, [crush.md](crush.md) §8 is the most directly useful section in the whole set —
it's a Bubble Tea v2 TUI at production scale, and it answers the two problems that have no
obvious solution: how to render streaming markdown without flicker, and how to avoid
re-rendering the whole conversation every frame.
