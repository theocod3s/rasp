package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tool"
)

// turn is one Send. It lives on the goroutine that called Send and is touched by
// nothing else, which is the whole of the loop's concurrency story (design §6).
type turn struct {
	agent *Agent
	tools *tool.Set
	usage llm.Usage
	loops loopDetector
}

func (t *turn) run(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			t.emit(Event{Kind: EventError, Err: err})
		}
		t.emit(Event{Kind: EventTurnEnd, Usage: t.usage})
	}()

	for step := 0; step < t.agent.maxSteps; step++ {
		// Cancellation reaches the provider and every tool through ctx, but a
		// tool that ignores it and a stream that has already ended both leave the
		// turn here: this is the one place a turn is certain to notice.
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %w", ErrInterrupted, err)
		}
		more, err := t.step(ctx)
		if err != nil || !more {
			return err
		}
	}
	return fmt.Errorf("%w after %d model calls without the model finishing; the number is a fuse "+
		"rather than a limit, so reaching it means the turn stopped making progress",
		ErrMaxSteps, t.agent.maxSteps)
}

// step is one model call and whatever it asked for. It reports whether the turn
// takes another.
func (t *turn) step(ctx context.Context) (bool, error) {
	msg, pending, err := t.call(ctx)
	if err != nil {
		return false, err
	}
	t.usage = addUsage(t.usage, msg.Usage)
	t.emit(Event{Kind: EventAssistantEnd, Message: msg})

	calls := toolUses(msg, pending)
	dispatch, stopErr := classify(msg.StopReason, len(calls))

	results := make([]*tool.Result, len(calls))
	if dispatch {
		t.dispatch(ctx, calls, results)
	}
	t.commit(msg, calls, results, unrun(ctx, msg.StopReason))
	if stopErr != nil {
		return false, stopErr
	}

	// After the commit and never before it: the halt is a step boundary, so the
	// transcript it stops on is one the next request can still be built from.
	if err := t.checkLooping(calls, results); err != nil {
		return false, err
	}
	return len(calls) > 0, nil
}

// call runs one stream to its end, buffering the tool calls it announces rather
// than dispatching them as they arrive. Draining first means the assistant
// message is complete before any result exists, which is what makes result order
// deterministic and parallel dispatch a flag rather than a redesign (design §4).
func (t *turn) call(ctx context.Context) (*llm.Message, []llm.ToolCall, error) {
	req := llm.Request{
		Model:     t.agent.model,
		Messages:  t.agent.Messages(),
		Tools:     t.tools.Specs(),
		MaxTokens: t.agent.maxTokens,
	}

	var (
		pending  []llm.ToolCall
		terminal llm.Event
	)
	for ev := range t.agent.provider.Stream(ctx, req) {
		switch ev.Type {
		case llm.EventTextDelta, llm.EventThinkingDelta, llm.EventToolInputStart, llm.EventToolInputDelta:
			t.emit(Event{Kind: EventAssistantDelta, Message: ev.Partial})
		case llm.EventToolCall:
			pending = append(pending, *ev.ToolCall)
		case llm.EventDone, llm.EventError:
			terminal = ev
		}
	}

	switch {
	case terminal.Type == "":
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("%w: %w", ErrInterrupted, err)
		}
		return nil, nil, errors.New("the stream ended without a terminal event, so there is no telling " +
			"whether the reply finished")
	case terminal.Partial == nil:
		return nil, nil, fmt.Errorf("the stream's %s event carries no message; every event carries the "+
			"full accumulation, and the terminal one is what the turn is built from", terminal.Type)
	case terminal.Type == llm.EventError && terminal.StopReason != llm.StopAborted:
		// StopAborted is excluded on purpose: a cancelled stream is a failure to
		// the code that was streaming and a completion to the termination table,
		// which has a row of its own for it.
		if terminal.Err == nil {
			return nil, nil, fmt.Errorf("the stream failed and named no cause; it stopped for %q", terminal.StopReason)
		}
		return nil, nil, terminal.Err
	}

	// Copied: the provider owned this message and mutated it in place for the
	// length of the stream.
	msg := *terminal.Partial
	msg.Content = slices.Clone(msg.Content)
	return &msg, pending, nil
}

// classify is design §4's termination table. dispatch says whether the calls
// this message holds may run; a non-nil error ends the turn.
func classify(stop llm.StopReason, calls int) (dispatch bool, err error) {
	switch stop {
	case llm.StopToolUse:
		if calls == 0 {
			return false, fmt.Errorf("the model stopped for %q and its reply holds no tool call, "+
				"so there is nothing to run and nothing that finished", stop)
		}
		return true, nil

	case llm.StopEndTurn, llm.StopRefusal:
		// Calls alongside these are not the table's row and not a breach either:
		// llama.cpp-style servers report "stop" with tool_calls in the same
		// response, so the loop dispatches on what the message holds rather than
		// on the reason it stopped.
		return true, nil

	case llm.StopMaxTokens:
		// Nothing this step asked for runs: arguments cut off at the output limit
		// can parse and validate while meaning something else, and an edit with a
		// truncated replacement destroys the file it lands in (design §4 invariant
		// 2). With no calls there is nothing to fail, and the reason travels on the
		// committed message for a consumer to warn about.
		return false, nil

	case llm.StopAborted:
		return false, ErrInterrupted

	case llm.StopError:
		// Only an adapter reporting the failure on EventDone reaches this: the
		// usual route is a terminal EventError, which call turns into the
		// provider's own error before the switch.
		return false, errors.New("the model call failed and the stream said no more than that")

	default:
		return false, fmt.Errorf("the model stopped for %q, which this build has no rule for; "+
			"treating it as a finished answer would hand the user a turn that stopped part way", stop)
	}
}

func (t *turn) emit(ev Event) { t.agent.emit(ev) }

func addUsage(a, b llm.Usage) llm.Usage {
	return llm.Usage{
		Input:      a.Input + b.Input,
		Output:     a.Output + b.Output,
		CacheRead:  a.CacheRead + b.CacheRead,
		CacheWrite: a.CacheWrite + b.CacheWrite,
	}
}
