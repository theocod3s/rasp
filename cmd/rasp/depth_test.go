package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/config"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tool"
)

// TestTheSessionsLevelIsOnEveryRequest. The picker writes here and the loop
// never learns depth exists, so a level that stopped reaching the request would
// leave a session whose menu works and whose turns all run at the API's default.
func TestTheSessionsLevelIsOnEveryRequest(t *testing.T) {
	p := &recorder{}
	d := newDepth(p)

	drain(d.Stream(t.Context(), llm.Request{Model: "m"}))
	d.SetEffort(llm.EffortMax)
	drain(d.Stream(t.Context(), llm.Request{Model: "m"}))
	// A request that arrived carrying a level of its own. This is where the
	// session's answer is kept, so the one on the request loses — a turn running
	// at a depth nobody chose is what refusing to clamp exists to prevent
	// (decisions.md).
	drain(d.Stream(t.Context(), llm.Request{Model: "m", Effort: llm.EffortLow}))

	want := []llm.Effort{"", llm.EffortMax, llm.EffortMax}
	var got []llm.Effort
	for _, req := range p.sent {
		got = append(got, req.Effort)
	}
	if !slices.Equal(got, want) {
		t.Errorf("the requests carried %v, want %v — the unset default sends no depth field at all", got, want)
	}
}

// TestThePickerAndTheRefusalReadOneList. The levels the picker draws are the
// provider's own, promoted rather than restated: a copy here would be the one
// that goes stale, offering a rung the request path refuses (decisions.md).
func TestThePickerAndTheRefusalReadOneList(t *testing.T) {
	provider, _, err := buildProvider(t.Context(), &config.Result{
		Config: config.Config{Model: "anthropic/claude-opus-5"},
	})
	if err != nil {
		t.Fatalf("buildProvider: %v", err)
	}

	offered := newDepth(provider).Efforts()
	if want := provider.Efforts(); !slices.Equal(offered, want) {
		t.Errorf("the picker is offered %v and the adapter takes %v", offered, want)
	}
	// And it is the adapter's subset rather than the whole ladder, so a wrapper
	// that answered with everything would fail here rather than pass by matching
	// a list nobody narrowed.
	if ladder := llm.EffortLadder(); slices.Equal(offered, ladder) {
		t.Errorf("the picker is offered the whole ladder %v; this adapter cannot send all of it", ladder)
	}
}

// TestAnUnsendableLevelIsRefusedWithNoPickerInvolved is the ticket's own point:
// the picker is a second layer, never the guard. The level here is set the way
// configuration or a provider switched underneath one would leave it — no menu,
// no filtering — and the adapter still fails the request and names the rung.
func TestAnUnsendableLevelIsRefusedWithNoPickerInvolved(t *testing.T) {
	provider, model, err := buildProvider(t.Context(), &config.Result{
		Config: config.Config{Model: "anthropic/claude-opus-5"},
	})
	if err != nil {
		t.Fatalf("buildProvider: %v", err)
	}

	d := newDepth(provider)
	// A rung on llm's ladder that Anthropic's enum has no member for, so the
	// refusal is the adapter's and not a validation this file invented.
	d.SetEffort(llm.EffortNone)
	if slices.Contains(d.Efforts(), llm.EffortNone) {
		t.Fatal("this adapter publishes none, so the level below would be sent rather than refused")
	}

	t.Run("the request path", func(t *testing.T) {
		var failed error
		for ev := range d.Stream(t.Context(), llm.Request{Model: model, MaxTokens: 1024}) {
			if ev.Type == llm.EventError {
				failed = ev.Err
			}
		}
		if failed == nil {
			t.Fatal("the request was accepted, so the turn would run at whatever depth the API " +
				"defaults to with nothing saying so")
		}
		if !strings.Contains(failed.Error(), string(llm.EffortNone)) {
			t.Errorf("the stream failed with %v, and the level it could not send is not named", failed)
		}
	})

	// The same refusal where a user meets it: a turn that ends carrying the
	// adapter's message, which the UI draws rather than swallows (internal/tui).
	t.Run("the turn a user would have started", func(t *testing.T) {
		a, err := agent.New(agent.Config{
			Provider:  d,
			Tools:     tool.NewRegistry(nil),
			Model:     model,
			MaxTokens: 1024,
		})
		if err != nil {
			t.Fatalf("agent.New: %v", err)
		}

		err = a.Send(t.Context(), "say something")
		if err == nil {
			t.Fatal("the turn finished, though every request in it carried a level this adapter refuses")
		}
		if !strings.Contains(err.Error(), string(llm.EffortNone)) {
			t.Errorf("the turn failed with %v, and the level behind it is not named", err)
		}
	})
}

// recorder is a provider that keeps the requests it was handed and answers each
// with an empty finished reply.
type recorder struct{ sent []llm.Request }

func (r *recorder) ID() string { return "recorder" }

func (r *recorder) Efforts() []llm.Effort { return llm.EffortLadder() }

func (r *recorder) Stream(_ context.Context, req llm.Request) llm.StreamResponse {
	r.sent = append(r.sent, req)
	return func(yield func(llm.Event) bool) {
		yield(llm.Event{
			Type:       llm.EventDone,
			StopReason: llm.StopEndTurn,
			Partial:    &llm.Message{Role: llm.RoleAssistant},
		})
	}
}

func drain(stream llm.StreamResponse) {
	for range stream {
	}
}
