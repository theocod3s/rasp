package headless

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/theocod3s/rasp/internal/llm"
)

// DefaultMaxTokens caps the reply. Configuration has no key for it: design §10's
// context.reserve_tokens is the compaction margin, not an output budget, so the
// number lives here until a turn has reason to vary it.
const DefaultMaxTokens = 8192

// Runner answers one prompt with one model call. There is no loop and no tools
// yet, so a turn is a single Stream.
type Runner struct {
	Provider llm.Provider

	// Model is the provider's own identifier, with no `provider/` prefix — that
	// prefix chooses the adapter and is not a name any API knows.
	Model string

	// Zero takes DefaultMaxTokens.
	MaxTokens int

	// Out takes model text and nothing else. At the other end of it is a pipe.
	Out io.Writer

	// Warn takes what the user should see and a script must not parse. Nil
	// discards it.
	Warn io.Writer
}

// Run makes the call and returns once the stream has finished. Text reaches Out
// as it arrives, including whatever arrived before a failure; the failure comes
// back as an error, for the caller to put wherever errors belong.
func (r Runner) Run(ctx context.Context, prompt string) error {
	maxTokens := r.MaxTokens
	if maxTokens == 0 {
		maxTokens = DefaultMaxTokens
	}
	req := llm.Request{
		Model:     r.Model,
		MaxTokens: maxTokens,
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: []llm.Block{{Type: llm.BlockText, Text: prompt}},
		}},
	}

	written, finished := 0, false
	for ev := range r.Provider.Stream(ctx, req) {
		// Ahead of the type switch, so the answer that did arrive is printed even
		// when the event carrying it is the failure.
		if ev.Partial != nil {
			if text := tail(ev.Partial, written); text != "" {
				n, err := io.WriteString(r.Out, text)
				written += n
				if err != nil {
					return fmt.Errorf("writing the reply: %w", err)
				}
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
			if ev.StopReason == llm.StopMaxTokens {
				r.warn("the reply was cut off at the %d-token limit", maxTokens)
			}
		}
	}
	if !finished {
		// Exiting 0 here would report half an answer as a whole one, which is the
		// one thing a script piping this cannot detect for itself.
		return errors.New("the stream ended without a terminal event, so the reply may be incomplete")
	}
	return nil
}

func (r Runner) warn(format string, args ...any) {
	if r.Warn == nil {
		return
	}
	fmt.Fprintf(r.Warn, "rasp: "+format+"\n", args...)
}

// tail is the text of msg beyond its first written bytes. Deriving output this
// way rather than appending Event.Delta is the StreamResponse contract: Partial
// is the authority, and a consumer that adds deltas up itself is one that breaks
// when a provider's delta bookkeeping is wrong rather than when its message is.
//
// Thinking is not part of the reply, so it is skipped rather than printed.
func tail(msg *llm.Message, written int) string {
	var (
		out  strings.Builder
		seen int
	)
	for _, block := range msg.Content {
		if block.Type != llm.BlockText {
			continue
		}
		text := block.Text
		seen += len(text)
		if seen <= written {
			continue
		}
		if start := len(text) - (seen - written); start > 0 {
			text = text[start:]
		}
		out.WriteString(text)
	}
	return out.String()
}
