package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
)

// anthropicish is a provider subset with two rungs of the ladder missing, which
// is the shape that makes an offered level worth checking: none and minimal are
// on llm's ladder and off this list, so a picker with a table of its own would
// draw them and a picker reading Efforts cannot.
var anthropicish = []llm.Effort{
	llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax,
}

// TestThePickerOffersTheProvidersOwnLevelsAndNoOthers.
func TestThePickerOffersTheProvidersOwnLevelsAndNoOthers(t *testing.T) {
	d := &depths{current: llm.EffortHigh, lists: [][]llm.Effort{anthropicish}}

	m, _ := picker(t, d).dispatch("effort", "")

	if got := listed(t, m); !slices.Equal(got, anthropicish) {
		t.Errorf("the picker offers %v, and the provider publishes %v", got, anthropicish)
	}
	// The marker, not only the membership: a list with nothing marked leaves the
	// user guessing which depth their turns are running at.
	if text := drawn(m); !strings.Contains(text, "  > high") {
		t.Errorf("the level this session asks for is not marked:\n%s", text)
	}
	for _, absent := range []llm.Effort{llm.EffortNone, llm.EffortMinimal} {
		if strings.Contains(drawn(m), string(absent)) {
			t.Errorf("the picker mentions %q, which this provider cannot send:\n%s", absent, drawn(m))
		}
	}
}

// TestThePickerFollowsTheProviderRatherThanItsOwnCopy is the criterion a second
// table would fail. Nothing switches provider yet, so the switch is played by a
// Depth whose answer changes between opens — a picker that read Efforts once at
// startup would draw the first list twice and never say a word about it.
func TestThePickerFollowsTheProviderRatherThanItsOwnCopy(t *testing.T) {
	shortened := []llm.Effort{llm.EffortLow, llm.EffortHigh}
	d := &depths{lists: [][]llm.Effort{anthropicish, shortened}}
	m := picker(t, d)

	first, _ := m.dispatch("effort", "")
	second, _ := first.dispatch("effort", "")

	if got := listed(t, first); !slices.Equal(got, anthropicish) {
		t.Errorf("the first open offers %v, want %v", got, anthropicish)
	}
	// Only the second open's own lines: the first is still in the conversation
	// above it, and comparing the whole would pass on a picker that drew the
	// stale list a second time.
	both := listed(t, second)
	if len(both) < len(anthropicish) {
		t.Fatalf("the conversation offers %v, which is short of the first open alone — there is no "+
			"second open in it to read", both)
	}
	got := both[len(anthropicish):]
	if !slices.Equal(got, shortened) {
		t.Errorf("the second open offers %v after the provider's list became %v", got, shortened)
	}
	if d.opens != 2 {
		t.Errorf("Efforts was called %d time(s) for two opens, so one of them drew a kept list", d.opens)
	}
}

// TestPickingALevelSetsItForTheSession.
func TestPickingALevelSetsItForTheSession(t *testing.T) {
	d := &depths{lists: [][]llm.Effort{anthropicish}}

	// Upper case on purpose: a level is typed, and the ladder is lower case.
	m, _ := picker(t, d).dispatch("effort", "MAX")

	if !slices.Equal(d.set, []llm.Effort{llm.EffortMax}) {
		t.Errorf("the provider was set to %v, want the one level that was picked", d.set)
	}
	if text := words(drawn(m)); !strings.Contains(text, "asks for max") {
		t.Errorf("picking a level drew no confirmation of it:\n%s", text)
	}
}

// TestALevelThisProviderCannotSendIsRefusedAndNamed. The picker is a second
// layer over the adapter's own refusal, so pointing at an unsendable rung is not
// a way to reach one — and a refusal that drew nothing would read as a level
// silently accepted.
func TestALevelThisProviderCannotSendIsRefusedAndNamed(t *testing.T) {
	d := &depths{lists: [][]llm.Effort{anthropicish}}

	m, _ := picker(t, d).dispatch("effort", "minimal")

	if len(d.set) != 0 {
		t.Errorf("the provider was set to %v by a level it never published", d.set)
	}
	text := words(drawn(m))
	if !strings.Contains(text, "minimal") || !strings.Contains(text, "no such level") {
		t.Errorf("the refusal does not name the level it could not take:\n%s", text)
	}
	// And the list comes with it, so the answer says what can be asked for
	// instead of only what cannot.
	if got := listed(t, m); !slices.Equal(got, anthropicish) {
		t.Errorf("the refusal offers %v, want the provider's own %v", got, anthropicish)
	}
}

// TestALevelLeftInvalidIsShownRatherThanClamped is the state a provider switch
// leaves behind: a level chosen against the previous provider, still set, and
// unsendable now. Clamping it to the nearest rung is the tempting fix and the
// one decisions.md forbids — the turn would run at a depth nobody asked for,
// with nothing on the screen to say so.
func TestALevelLeftInvalidIsShownRatherThanClamped(t *testing.T) {
	d := &depths{current: llm.EffortMinimal, lists: [][]llm.Effort{anthropicish}}

	m, _ := picker(t, d).dispatch("effort", "")

	text := words(drawn(m))
	switch {
	case !strings.Contains(text, "asking for minimal"):
		t.Errorf("the level this session is set to is not named:\n%s", text)
	case !strings.Contains(text, "every turn fails"):
		t.Errorf("nothing says what a level the provider cannot send does to a turn:\n%s", text)
	}
	if d.current != llm.EffortMinimal {
		t.Errorf("the level was quietly moved to %q; drawing the list is not a place to change it", d.current)
	}
	if got := listed(t, m); !slices.Equal(got, anthropicish) {
		t.Errorf("the picker offers %v, want the provider's own %v with none of them marked", got, anthropicish)
	}
}

// TestAProviderRefusingALevelIsDrawnRatherThanSwallowed is the other half of the
// same criterion, on the path that has no picker in it: the adapter fails the
// request naming the rung (decisions.md), and both routes that failure takes out
// of a turn have to put it on the screen. A refusal absorbed here would leave a
// session whose every turn ends in nothing at all.
func TestAProviderRefusingALevelIsDrawnRatherThanSwallowed(t *testing.T) {
	refusal := errors.New(`anthropic: cannot send effort "minimal"; this API takes ` +
		`[low medium high xhigh max], and a turn never runs at a depth other than the one asked for`)

	for _, tc := range []struct {
		name string
		msg  tea.Msg
	}{
		{name: "a turn that failed", msg: turnDone{err: refusal}},
		{name: "an error event from the loop", msg: agentMsg{event: agent.Event{Kind: agent.EventError, Err: refusal}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := update(newModel(t.Context(), &promptTurner{}, goldenConfig()), tc.msg)

			frame := words(m.View().Content)
			if !strings.Contains(frame, "cannot send effort") || !strings.Contains(frame, "minimal") {
				t.Errorf("the refusal never reached the screen:\n%s", frame)
			}
		})
	}
}

// TestTheCommandSaysSoWhenThereIsNothingToPickFrom. Both states are reachable by
// composition rather than by anything the user did, and both would otherwise
// draw a heading over an empty list — which reads as a picker that failed to
// load rather than as a session with no provider behind it.
func TestTheCommandSaysSoWhenThereIsNothingToPickFrom(t *testing.T) {
	for _, tc := range []struct {
		name  string
		depth Depth
		want  string
	}{
		{name: "no provider", want: "no provider behind it"},
		{name: "a provider publishing nothing", depth: &depths{}, want: "publishes no effort levels"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel(t.Context(), &promptTurner{}, Config{Depth: tc.depth})

			m, _ = m.dispatch("effort", "")

			if text := words(drawn(m)); !strings.Contains(text, tc.want) {
				t.Errorf("the answer does not mention %q:\n%s", tc.want, text)
			}
			if got := listed(t, m); len(got) != 0 {
				t.Errorf("the answer drew levels %v, and there were none to draw", got)
			}
		})
	}
}

// picker is a model with a Depth behind it and nothing else set up.
func picker(t *testing.T, d Depth) Model {
	t.Helper()
	return newModel(t.Context(), &promptTurner{}, Config{Depth: d})
}

// drawn is the conversation as the terminal would show it, ANSI stripped and
// every line kept: the picker's answer is a list, and words() would run it into
// one line.
func drawn(m Model) string { return ansi.Strip(m.chat.Render(goldenWidth)) }

// listed reads back the levels the conversation is offering. The picker draws
// one per line, indented, and nothing else in the answer is — so a level that
// stopped being drawn as a level disappears from here rather than hiding inside
// a sentence.
func listed(t *testing.T, m Model) []llm.Effort {
	t.Helper()

	var got []llm.Effort
	for _, line := range strings.Split(drawn(m), "\n") {
		if !strings.HasPrefix(line, "  ") {
			continue
		}
		got = append(got, llm.Effort(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "> "))))
	}
	return got
}

// depths is a Depth a test drives by hand. lists is what Efforts answers, one
// entry per open with the last standing for every open after — which is how a
// provider switched mid-session is played while nothing can switch one.
type depths struct {
	lists   [][]llm.Effort
	opens   int
	current llm.Effort
	set     []llm.Effort
}

func (d *depths) Efforts() []llm.Effort {
	if len(d.lists) == 0 {
		return nil
	}
	list := d.lists[min(d.opens, len(d.lists)-1)]
	d.opens++
	// A fresh slice, as every real provider returns: a picker sorting one in
	// place would otherwise be reordering the list the refusal reads.
	return slices.Clone(list)
}

func (d *depths) Effort() llm.Effort { return d.current }

func (d *depths) SetEffort(e llm.Effort) {
	d.current = e
	d.set = append(d.set, e)
}
