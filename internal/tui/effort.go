package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/llm"
)

// Depth is the session's effort setting as the picker drives it: the levels the
// provider behind it can put on the wire, and the one every turn asks for.
// Narrower than llm.Provider, where both come from — the UI has no business
// starting a stream.
//
// Efforts is called on every open rather than kept, so a session whose provider
// changed is offered the new one's levels.
type Depth interface {
	Efforts() []llm.Effort
	Effort() llm.Effort
	SetEffort(llm.Effort)
}

// effort answers /effort: the levels on their own, and one named to set it. It
// is not the guard — an adapter handed a rung it cannot express fails the
// request and names it (decisions.md), which is what still covers a level from
// configuration, a headless run, and a provider switched under one already set.
func effort(m Model, args string) (Model, tea.Cmd) {
	if m.depth == nil {
		return m.say("This session has no provider behind it, so there is no depth to list or set."), nil
	}

	levels := m.depth.Efforts()
	if len(levels) == 0 {
		// Said rather than drawn as an empty list, which reads as a picker that
		// failed to load.
		return m.say("This provider publishes no effort levels, so there is nothing to pick here: " +
			"every turn runs at whatever depth its API chooses."), nil
	}

	current := m.depth.Effort()
	if args == "" {
		return m.say(effortList(levels, current)), nil
	}

	want := llm.Effort(strings.ToLower(args))
	if !slices.Contains(levels, want) {
		return m.say("This session cannot ask for " + args + ": the provider has no such level to " +
			"send.\n\n" + effortList(levels, current)), nil
	}
	m.depth.SetEffort(want)
	return m.say("Every turn from here asks for " + string(want) + "."), nil
}

// effortList draws the levels the provider published, in that order, with the
// session's own marked. Nothing here names a level: a second copy of the list is
// the one that goes stale, and it goes stale silently — the rung simply stops
// appearing (decisions.md).
func effortList(levels []llm.Effort, current llm.Effort) string {
	var b strings.Builder
	b.WriteString("Effort is how much depth a turn is asked for, and this provider can send:")
	for _, level := range levels {
		marker := "    "
		if level == current {
			marker = "  > "
		}
		b.WriteString("\n" + marker + string(level))
	}

	switch {
	case current == "":
		b.WriteString("\n\nNothing is set, so a turn runs at whatever depth the API chooses. " +
			"/effort <level> picks one, for the rest of the session.")
	case !slices.Contains(levels, current):
		// What a provider switched under a chosen level leaves behind. Said rather
		// than corrected: the nearest level this provider can send is not the one
		// that was asked for, and substituting it is what decisions.md forbids.
		b.WriteString("\n\nThis session is asking for " + string(current) + ", which is not one of " +
			"those: the provider cannot send it, and every turn fails until /effort <level> names " +
			"one it can.")
	default:
		b.WriteString("\n\n/effort <level> picks another, for the rest of the session.")
	}
	return b.String()
}
