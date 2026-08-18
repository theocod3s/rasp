package headless

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/theocod3s/rasp/internal/llm"
)

// Runner answers one prompt with one model call. There is no loop and no tools
// yet, so a turn is a single Stream.
type Runner struct {
	Provider llm.Provider

	// Model is the provider's own identifier, with no `provider/` prefix — that
	// prefix chooses the adapter and is not a name any API knows.
	Model string

	// MaxTokens caps the reply. No default of its own: reaching the cap fails the
	// run, so the number has to be one the user can raise, and it arrives from
	// `max_output_tokens` — which the resolver always answers.
	MaxTokens int

	// Out takes model text and nothing else. At the other end of it is a pipe.
	Out io.Writer
}

// Run makes the call and returns once the stream has finished. Text reaches Out
// as it arrives, including whatever arrived before a failure.
//
// nil means a complete reply. A stream that broke, one that stopped without
// saying so, and a reply the token cap cut short all come back as errors, so a
// caller reading stdout is never handed a half answer with nothing to say so
// (decisions.md, which also covers why truncation counts and a refusal does not).
func (r Runner) Run(ctx context.Context, prompt string) error {
	if r.MaxTokens <= 0 {
		return fmt.Errorf("the reply cap is %d, so there is nothing to ask for; "+
			"it comes from max_output_tokens in the configuration", r.MaxTokens)
	}
	req := llm.Request{
		Model:     r.Model,
		MaxTokens: r.MaxTokens,
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: []llm.Block{{Type: llm.BlockText, Text: prompt}},
		}},
	}

	var (
		printed       written
		finished      bool
		wrote         bool
		endsInNewline bool
	)
	for ev := range r.Provider.Stream(ctx, req) {
		// Ahead of the type switch, so the answer that did arrive is printed even
		// when the event carrying it is the failure.
		if ev.Partial != nil {
			if text := printed.gained(ev.Partial); text != "" {
				if _, err := io.WriteString(r.Out, text); err != nil {
					return fmt.Errorf("writing the reply: %w", err)
				}
				wrote, endsInNewline = true, strings.HasSuffix(text, "\n")
			}
		}

		switch ev.Type {
		case llm.EventError:
			if ev.Err == nil {
				return fmt.Errorf("the stream failed and named no cause; it stopped for %q", ev.StopReason)
			}
			return ev.Err
		case llm.EventDone:
			finished = true
			switch ev.StopReason {
			case llm.StopEndTurn, llm.StopRefusal:
				// The model finished, or declined; both are whole turns.
			case llm.StopMaxTokens:
				return fmt.Errorf("the reply was cut off at the %d-token limit; "+
					"raise max_output_tokens to give it more room", r.MaxTokens)
			default:
				// Closed on purpose: an untaught stop reason is a reply that stopped
				// for one, and StopToolUse — a model breaking off to call a tool
				// nothing here can run — is the case that makes it real.
				return fmt.Errorf("the reply is incomplete: the model stopped for %q, "+
					"and there is nothing here to take the next step", ev.StopReason)
			}
		}
	}
	if !finished {
		if err := ctx.Err(); err != nil {
			// Asked first, or a caller who cancelled is told their provider misbehaves.
			return err
		}
		return errors.New("the stream ended without a terminal event, so the reply may be incomplete")
	}

	// A model rarely ends on one, and without it the shell prompt lands on top of
	// the reply's last line. Nothing downstream gains a byte: `$(…)` strips
	// trailing newlines. Only on the way out with a whole reply — a run that
	// failed has said so on stderr, and there is no line to finish.
	if wrote && !endsInNewline {
		if _, err := io.WriteString(r.Out, "\n"); err != nil {
			return fmt.Errorf("writing the reply: %w", err)
		}
	}
	return nil
}

// written is what has already been printed, counted per block rather than as one
// total: content arrives by block index, so a message can grow in a block that is
// not its last, where a single counter skips those bytes and reprints as many of
// a later block's in their place.
type written []int

// gained returns what msg has grown by since the last call, in block order, and
// records it. Reading Partial rather than adding Event.Delta up is the
// StreamResponse contract: the message is the authority, so a provider whose
// delta bookkeeping is wrong still gets its reply printed correctly. Thinking is
// not part of the reply and is skipped, and growth in a block that is not the
// last one goes out where it arrived rather than where it belongs — what has
// been written cannot be unwritten.
func (w *written) gained(msg *llm.Message) string {
	if len(*w) < len(msg.Content) {
		*w = append(*w, make([]int, len(msg.Content)-len(*w))...)
	}

	var out strings.Builder
	for i, block := range msg.Content {
		if block.Type != llm.BlockText || (*w)[i] >= len(block.Text) {
			continue
		}
		out.WriteString(block.Text[(*w)[i]:])
		(*w)[i] = len(block.Text)
	}
	return out.String()
}
