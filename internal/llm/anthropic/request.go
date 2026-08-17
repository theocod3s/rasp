package anthropic

import (
	"errors"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/theocod3s/rasp/internal/llm"
)

// buildParams translates a neutral request onto the wire. It fails rather than
// dropping anything it cannot express: a tool_result quietly left out of a
// request is design §4 invariant 1 broken at the last layer that can still see it.
func buildParams(req llm.Request) (sdk.MessageNewParams, error) {
	if req.Model == "" {
		return sdk.MessageNewParams{}, errors.New("anthropic: no model; the API requires one, " +
			"and sending this costs an authenticated round trip to be told so")
	}
	// Outside the Enabled branch: a budget set on a disabled config is still a
	// caller who believes it is being honoured, and provider.go now promises it is
	// refused rather than dropped.
	if req.Thinking.BudgetTokens != 0 {
		return sdk.MessageNewParams{}, fmt.Errorf("anthropic: Thinking.BudgetTokens is %d and cannot be sent; "+
			"depth is an effort level on the models that take the shape used here, and ThinkingConfig has "+
			"nowhere to carry one", req.Thinking.BudgetTokens)
	}
	if req.MaxTokens <= 0 {
		return sdk.MessageNewParams{}, fmt.Errorf("anthropic: MaxTokens is %d; the API requires a positive cap, "+
			"and sending this costs an authenticated round trip to be told so", req.MaxTokens)
	}
	// The quietest failure this adapter could have: the loop passes its per-turn
	// registry snapshot, no tools reach the wire, the model answers in prose, and
	// the turn completes looking successful with the user's tools never offered.
	if len(req.Tools) > 0 {
		return sdk.MessageNewParams{}, fmt.Errorf("anthropic: the request carries %d tools; this adapter "+
			"streams text only and would send none of them", len(req.Tools))
	}

	params := sdk.MessageNewParams{
		Model:     sdk.Model(req.Model),
		MaxTokens: int64(req.MaxTokens),
	}

	for i, block := range req.System {
		// Refused rather than skipped, unlike an empty text block in a message: the
		// system prompt is assembled here rather than replayed from a transcript, so
		// an empty one is a bug upstream and skipping it would also lose whatever
		// cache breakpoint it was carrying.
		if block.Text == "" {
			return sdk.MessageNewParams{}, fmt.Errorf("anthropic: system block %d has no text; "+
				"providers reject an empty block", i)
		}
		text := sdk.TextBlockParam{Text: block.Text}
		if block.Cache {
			text.CacheControl = sdk.NewCacheControlEphemeralParam()
		}
		params.System = append(params.System, text)
	}

	for i, msg := range req.Messages {
		converted, err := messageParam(msg)
		switch {
		case errors.Is(err, llm.ErrSkipMessage):
			// Withheld from this request, never dropped from the transcript. Which
			// messages that covers, and why the role is the whole test, is
			// llm.CheckSendable.
			continue
		case err != nil:
			return sdk.MessageNewParams{}, fmt.Errorf("anthropic: message %d: %w", i, err)
		}
		// Skipping an assistant turn leaves the user turns either side of it adjacent,
		// and the API's two descriptions of that disagree: its error table lists
		// consecutive same-role messages as a 400, its reference says they are
		// combined. Combining them here costs nothing if the API would have, and is
		// the difference between a working session and a wedged one if it would not.
		if n := len(params.Messages); n > 0 && params.Messages[n-1].Role == converted.Role {
			params.Messages[n-1].Content = append(params.Messages[n-1].Content, converted.Content...)
			continue
		}
		params.Messages = append(params.Messages, converted)
	}
	// Reached by skipping every message there was. The API refuses an empty list,
	// and the guards above exist so that costs no round trip.
	if len(params.Messages) == 0 {
		return sdk.MessageNewParams{}, errors.New("anthropic: no messages left to send; " +
			"the API requires at least one")
	}

	if req.Thinking.Enabled {
		// Adaptive is the shape current models take, and budget_tokens is the only
		// one older models accept. Nothing here can tell which is which: rasp never
		// validates a model id against a catalog, so that `openrouter/auto` and any
		// future router keep working (scope.md).
		params.Thinking = sdk.ThinkingConfigParamUnion{OfAdaptive: &sdk.ThinkingConfigAdaptiveParam{}}
	}
	return params, nil
}

func messageParam(msg llm.Message) (sdk.MessageParam, error) {
	// Decide first, translate second. What llm can be asked about is neutral
	// blocks, and the wire list is then derived from the surviving ones one for
	// one — so "is there anything to send" is asked of the same list that goes
	// out, rather than of a parallel one that has to be kept in step.
	kept := make([]llm.Block, 0, len(msg.Content))
	for _, block := range msg.Content {
		switch block.Type {
		case llm.BlockText:
			// Anthropic rejects a text block with no text, and this one is already
			// in the transcript: sent, it would 400 every later request in the
			// session rather than only this one. Empty is llm's rule, not this
			// adapter's, and IsEmpty is where it is written.
			if block.IsEmpty() {
				continue
			}
		case llm.BlockThinking:
			// Dropped rather than replayed. Anthropic wants a thinking block back
			// with the signature it arrived with, llm.Block has no field for one,
			// and replay is only required of a turn that went on to call a tool —
			// which this adapter cannot yet produce.
			continue
		default:
			return sdk.MessageParam{}, fmt.Errorf("cannot send a %s block: this adapter streams text only", block.Type)
		}
		kept = append(kept, block)
	}
	if err := llm.CheckSendable(msg.Role, kept); err != nil {
		return sdk.MessageParam{}, err
	}

	// Appended rather than assigned by index: a length taken from the wrong list
	// would leave holes, and the block union's zero value marshals to a JSON null
	// the API has nothing to do with. Every iteration either appends or returns,
	// so the wire list is as long as the one CheckSendable just passed.
	content := make([]sdk.ContentBlockParamUnion, 0, len(kept))
	for _, block := range kept {
		switch block.Type {
		case llm.BlockText:
			content = append(content, sdk.NewTextBlock(block.Text))
		default:
			return sdk.MessageParam{}, fmt.Errorf("kept a %s block and has no wire shape for it", block.Type)
		}
	}

	switch msg.Role {
	case llm.RoleUser:
		return sdk.NewUserMessage(content...), nil
	case llm.RoleAssistant:
		return sdk.NewAssistantMessage(content...), nil
	}
	return sdk.MessageParam{}, fmt.Errorf("unknown role %q", msg.Role)
}
