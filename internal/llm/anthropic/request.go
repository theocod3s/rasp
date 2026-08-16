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
		if err != nil {
			return sdk.MessageNewParams{}, fmt.Errorf("anthropic: message %d: %w", i, err)
		}
		params.Messages = append(params.Messages, converted)
	}

	if req.Thinking.Enabled {
		// Adaptive rather than a token budget: budget_tokens was removed from the
		// API and is a 400 on every model this runs against, so Request.Thinking's
		// BudgetTokens goes no further than here.
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
