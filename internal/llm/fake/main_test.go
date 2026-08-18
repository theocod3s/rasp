package fake_test

import (
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/theocod3s/rasp/internal/llm"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// drain reads a stream to the end through the contract checker, so every test
// here also asserts the sequence it scripted is one a real adapter could produce.
func drain(t *testing.T, seq llm.StreamResponse) []llm.Event {
	t.Helper()
	events, err := llm.CheckStream(seq)
	if err != nil {
		t.Fatalf("CheckStream: %v\nevents: %v", err, types(events))
	}
	return events
}

func types(events []llm.Event) []llm.EventType {
	out := make([]llm.EventType, len(events))
	for i, ev := range events {
		out[i] = ev.Type
	}
	return out
}

func last(t *testing.T, events []llm.Event) llm.Event {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("stream yielded no events")
	}
	return events[len(events)-1]
}

func message(t *testing.T, events []llm.Event) *llm.Message {
	t.Helper()
	return last(t, events).Partial
}

func calls(events []llm.Event) []*llm.ToolCall {
	var out []*llm.ToolCall
	for _, ev := range events {
		if ev.Type == llm.EventToolCall {
			out = append(out, ev.ToolCall)
		}
	}
	return out
}

// summary is what two runs of one script have to agree on, event by event.
// Partial is deliberately absent: it is the same pointer throughout, so every
// event shows the finished message and the final one stands for all of them.
type summary struct {
	Type       llm.EventType
	Delta      string
	StopReason llm.StopReason
	Err        string
	Call       string
}

func summarize(t *testing.T, events []llm.Event) string {
	t.Helper()

	out := make([]summary, len(events))
	for i, ev := range events {
		out[i] = summary{Type: ev.Type, Delta: ev.Delta, StopReason: ev.StopReason}
		if ev.Err != nil {
			out[i].Err = ev.Err.Error()
		}
		if ev.ToolCall != nil {
			out[i].Call = ev.ToolCall.ID + " " + ev.ToolCall.Name + " " + string(ev.ToolCall.Input)
		}
	}

	final, err := json.Marshal(message(t, events))
	if err != nil {
		t.Fatalf("marshalling the final message: %v", err)
	}
	seq, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshalling the event summary: %v", err)
	}
	return string(seq) + "\n" + string(final)
}

// mustPanic asserts that fn panics with a message mentioning want. Both halves
// matter: a panic is how this package reports a mis-scripted turn, and a test
// happy with any panic would pass on the wrong one.
func mustPanic(t *testing.T, want string, fn func()) {
	t.Helper()

	defer func() {
		t.Helper()
		got := recover()
		if got == nil {
			t.Errorf("no panic; want one mentioning %q", want)
			return
		}
		if msg, ok := got.(string); !ok || !strings.Contains(msg, want) {
			t.Errorf("panicked with %v; want a message mentioning %q", got, want)
		}
	}()
	fn()
}
