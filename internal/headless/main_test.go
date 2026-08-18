package headless_test

import (
	"context"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/theocod3s/rasp/internal/llm"
)

// TestMain runs the leak detector: the runner is driven from a second goroutine
// here, and it is what the process will spawn a turn on (design §13).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// scripted plays a fixed list of steps. Built like a real adapter in the one way
// that matters to a consumer: the message is allocated before the first yield
// and mutated in place, so Partial is the same pointer on every event.
type scripted struct {
	steps []step
	req   llm.Request // what the runner asked for; read once Run has returned
}

// step is one scripted move: it grows the message and yields the events that
// announce the growth, returning false once the consumer has stopped listening.
type step func(msg *llm.Message, yield func(llm.Event) bool) bool

func (s *scripted) ID() string { return "scripted" }

// Efforts: a double that refuses no rung, since nothing here asks for depth.
func (s *scripted) Efforts() []llm.Effort { return llm.EffortLadder() }

func (s *scripted) Stream(ctx context.Context, req llm.Request) llm.StreamResponse {
	s.req = req
	return func(yield func(llm.Event) bool) {
		msg := &llm.Message{Role: llm.RoleAssistant, Provider: s.ID(), Model: req.Model}
		if !yield(llm.Event{Type: llm.EventMessageStart, Partial: msg}) {
			return
		}
		for _, play := range s.steps {
			if !play(msg, yield) {
				return
			}
		}
	}
}

func text(chunks ...string) step {
	return deltas(llm.BlockText, llm.EventTextDelta, chunks)
}

func thinking(chunks ...string) step {
	return deltas(llm.BlockThinking, llm.EventThinkingDelta, chunks)
}

// deltas opens one block and streams it a chunk at a time, the way a provider
// splits a sentence at boundaries nobody chose.
func deltas(kind llm.BlockType, event llm.EventType, chunks []string) step {
	return func(msg *llm.Message, yield func(llm.Event) bool) bool {
		msg.Content = append(msg.Content, llm.Block{Type: kind})
		at := len(msg.Content) - 1
		for _, chunk := range chunks {
			msg.Content[at].Text += chunk
			if !yield(llm.Event{Type: event, Delta: chunk, Partial: msg}) {
				return false
			}
		}
		return true
	}
}

// miscounted and failed both move Partial in ways the StreamResponse contract
// forbids, on purpose: the runner's output rests on Partial alone, so a provider
// that misreports a delta or announces text late is still answered correctly.
// The well-behaved script is the one CheckStream is pointed at.
//
// miscounted's Delta is not what Partial gained. A consumer adding deltas up
// prints the lie; one reading Partial does not notice.
func miscounted(chunks ...string) step {
	return func(msg *llm.Message, yield func(llm.Event) bool) bool {
		msg.Content = append(msg.Content, llm.Block{Type: llm.BlockText})
		at := len(msg.Content) - 1
		for _, chunk := range chunks {
			msg.Content[at].Text += chunk
			if !yield(llm.Event{Type: llm.EventTextDelta, Delta: "<not the text>", Partial: msg}) {
				return false
			}
		}
		return true
	}
}

// grow adds to a block already open, which is how a provider streaming two text
// blocks at once delivers the earlier one's next fragment. Legal: one delta adds
// to one block, and nothing says it has to be the last.
func grow(index int, chunk string) step {
	return func(msg *llm.Message, yield func(llm.Event) bool) bool {
		msg.Content[index].Text += chunk
		return yield(llm.Event{Type: llm.EventTextDelta, Delta: chunk, Partial: msg})
	}
}

func done(reason llm.StopReason) step {
	return func(msg *llm.Message, yield func(llm.Event) bool) bool {
		msg.StopReason = reason
		return yield(llm.Event{Type: llm.EventDone, StopReason: reason, Partial: msg})
	}
}

// failed ends the stream carrying text that reached Partial without an event of
// its own, so the terminal event is the only place a consumer can see it.
func failed(err error, arrived string) step {
	return func(msg *llm.Message, yield func(llm.Event) bool) bool {
		if arrived != "" {
			msg.Content = append(msg.Content, llm.Block{Type: llm.BlockText, Text: arrived})
		}
		msg.StopReason = llm.StopError
		return yield(llm.Event{Type: llm.EventError, StopReason: llm.StopError, Err: err, Partial: msg})
	}
}

// handoff is a writer with no buffer of its own: every Write blocks until the
// test takes the bytes. A runner holding output back until the stream ended
// could not deliver a first chunk on its own.
type handoff chan string

func (h handoff) Write(p []byte) (int, error) {
	h <- string(p)
	return len(p), nil
}

func (h handoff) take(t *testing.T) string {
	t.Helper()
	select {
	case written := <-h:
		return written
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was written within 5s")
		return ""
	}
}
