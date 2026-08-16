package anthropic

import (
	"errors"
	"fmt"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/theocod3s/rasp/internal/llm"
)

// errNothingLeftToSend marks a message whose every block this adapter drops, as
// against one that arrived with no blocks at all.
var errNothingLeftToSend = errors.New("every block was dropped")

// buildParams translates a neutral request onto the wire. It fails rather than
// dropping anything it cannot express: a tool_result quietly left out of a
// request is design §4 invariant 1 broken at the last layer that can still see it.
func buildParams(req llm.Request) (sdk.MessageNewParams, error) {
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

	for _, block := range req.System {
		text := sdk.TextBlockParam{Text: block.Text}
		if block.Cache {
			text.CacheControl = sdk.NewCacheControlEphemeralParam()
		}
		params.System = append(params.System, text)
	}

	for i, msg := range req.Messages {
		converted, err := messageParam(msg)
		switch {
		case errors.Is(err, errNothingLeftToSend):
			// A turn truncated while the model was still thinking commits an
			// assistant message holding nothing but a thinking block. Dropping the
			// thinking leaves nothing to send, and failing here would fail every
			// later request built from that transcript too — one truncated turn
			// would wedge the session with no way out but editing the file.
			continue
		case err != nil:
			return sdk.MessageNewParams{}, fmt.Errorf("anthropic: message %d: %w", i, err)
		}
		params.Messages = append(params.Messages, converted)
	}

	if req.Thinking.Enabled {
		if req.Thinking.BudgetTokens != 0 {
			return sdk.MessageNewParams{}, fmt.Errorf("anthropic: Thinking.BudgetTokens is %d and cannot be sent; "+
				"depth is an effort level on the models that take the shape below, and ThinkingConfig has "+
				"nowhere to carry one", req.Thinking.BudgetTokens)
		}
		// Adaptive is the shape current models take, and budget_tokens is the only
		// one older models accept. Nothing here can tell which is which: rasp never
		// validates a model id against a catalog, so that `openrouter/auto` and any
		// future router keep working (scope.md).
		params.Thinking = sdk.ThinkingConfigParamUnion{OfAdaptive: &sdk.ThinkingConfigAdaptiveParam{}}
	}
	return params, nil
}

func messageParam(msg llm.Message) (sdk.MessageParam, error) {
	var content []sdk.ContentBlockParamUnion
	for _, block := range msg.Content {
		switch block.Type {
		case llm.BlockText:
			content = append(content, sdk.NewTextBlock(block.Text))
		case llm.BlockThinking:
			// Dropped rather than replayed. Anthropic wants a thinking block back
			// with the signature it arrived with, llm.Block has no field for one,
			// and replay is only required of a turn that went on to call a tool —
			// which this adapter cannot yet produce.
			continue
		default:
			return sdk.MessageParam{}, fmt.Errorf("cannot send a %s block: this adapter streams text only", block.Type)
		}
	}
	if len(content) == 0 {
		// Two different failures. A message that arrived empty is a caller's bug and
		// says so; one emptied by the drop above is a state the transcript can
		// legitimately hold, and buildParams skips it rather than refusing forever.
		if len(msg.Content) > 0 {
			return sdk.MessageParam{}, errNothingLeftToSend
		}
		return sdk.MessageParam{}, errors.New("no content to send; providers reject an empty message")
	}

	switch msg.Role {
	case llm.RoleUser:
		return sdk.NewUserMessage(content...), nil
	case llm.RoleAssistant:
		return sdk.NewAssistantMessage(content...), nil
	}
	return sdk.MessageParam{}, fmt.Errorf("unknown role %q", msg.Role)
}
