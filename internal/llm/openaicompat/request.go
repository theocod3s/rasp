package openaicompat

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"

	"github.com/theocod3s/rasp/internal/llm"
)

// blockSeparator joins two neutral text blocks into the one `content` string this
// wire has room for. Only a transcript recorded against a block-shaped API carries
// more than one, and there the blocks are paragraphs of a single reply.
const blockSeparator = "\n\n"

// buildParams translates a neutral request onto the wire. It fails rather than
// dropping anything it cannot express: a tool_result quietly left out of a request
// is design §4 invariant 1 broken at the last layer that can still see it.
func buildParams(req llm.Request) (sdk.ChatCompletionNewParams, error) {
	if req.Model == "" {
		return sdk.ChatCompletionNewParams{}, errors.New("no model; the API requires one, " +
			"and sending this costs an authenticated round trip to be told so")
	}
	if req.MaxTokens <= 0 {
		return sdk.ChatCompletionNewParams{}, fmt.Errorf("MaxTokens is %d; a reply with no cap is "+
			"one nothing stops, and the endpoints this adapter serves are billed by the token", req.MaxTokens)
	}
	params := sdk.ChatCompletionNewParams{
		Model: shared.ChatModel(req.Model),
		// `max_tokens` rather than the `max_completion_tokens` OpenAI now wants:
		// Ollama accepts that one and ignores it, so the current field caps nothing
		// there and says nothing about it. This one fails loudly, naming the
		// parameter, on the reasoning models that refuse it.
		MaxTokens: sdk.Int(int64(req.MaxTokens)),
		// An OpenAI-compatible endpoint reports no usage at all unless this is set,
		// and Message.Usage is what context estimation trusts (design §11).
		StreamOptions: sdk.ChatCompletionStreamOptionsParam{IncludeUsage: sdk.Bool(true)},
	}

	for i, spec := range req.Tools {
		tool, err := toolParam(spec)
		if err != nil {
			return sdk.ChatCompletionNewParams{}, fmt.Errorf("tool %d: %w", i, err)
		}
		params.Tools = append(params.Tools, tool)
	}

	// Up here with the other guards rather than beside the translation below:
	// reporting an unsendable rung only after some unrelated message failed costs
	// the caller a second round of the same refusal.
	if req.Effort != "" {
		level, ok := wireEffort[req.Effort]
		if !ok {
			return sdk.ChatCompletionNewParams{}, fmt.Errorf("cannot send effort %q; this API takes %v, "+
				"and a turn never runs at a depth other than the one asked for", req.Effort, supported)
		}
		params.ReasoningEffort = level
	}

	// One message, in order. There are no cache breakpoints to place — this wire
	// caches its prefix without being asked — so the Cache flag has nothing to apply
	// and the order is the half of design §11 that survives.
	var system []string
	for i, block := range req.System {
		// Refused rather than skipped, unlike an empty text block in a message: the
		// system prompt is assembled here rather than replayed, so an empty one is a
		// bug upstream.
		if block.Text == "" {
			return sdk.ChatCompletionNewParams{}, fmt.Errorf("system block %d has no text; "+
				"providers reject an empty block", i)
		}
		system = append(system, block.Text)
	}
	if len(system) > 0 {
		params.Messages = append(params.Messages, sdk.SystemMessage(strings.Join(system, blockSeparator)))
	}

	for i, msg := range req.Messages {
		converted, err := messageParams(msg)
		switch {
		case errors.Is(err, llm.ErrSkipMessage):
			// Withheld from this request, never dropped from the transcript — which
			// messages, and why the role decides, is llm.CheckSendable.
			continue
		case err != nil:
			return sdk.ChatCompletionNewParams{}, fmt.Errorf("message %d: %w", i, err)
		}
		params.Messages = append(params.Messages, converted...)
	}
	// Reached by skipping every message there was, or by a request carrying none.
	// The API refuses an empty list, and the guards here exist so that costs no
	// round trip.
	if len(params.Messages) == 0 {
		return sdk.ChatCompletionNewParams{}, errors.New("no messages left to send; " +
			"the API requires at least one")
	}
	return params, nil
}

// toolParam puts one tool on the wire. The schema goes through as the tool or the
// MCP server wrote it — this wire takes a whole JSON Schema object in `parameters`,
// so unlike Anthropic's there is no field to strip or re-derive.
func toolParam(spec llm.ToolSpec) (sdk.ChatCompletionToolUnionParam, error) {
	if spec.Name == "" {
		return sdk.ChatCompletionToolUnionParam{}, errors.New("no name; the model calls a tool by the " +
			"name it was given, and nothing could be resolved from an answer naming none")
	}
	def := shared.FunctionDefinitionParam{Name: spec.Name, Parameters: shared.FunctionParameters(spec.Schema)}
	if spec.Description != "" {
		def.Description = sdk.String(spec.Description)
	}
	return sdk.ChatCompletionFunctionTool(def), nil
}

// wireEffort is the single source for what this adapter can express: supported is
// derived from its keys and buildParams refuses everything else, so a picker and a
// refusal cannot come apart. A rung added to llm's ladder is refused until someone
// maps it here, which is the safe direction.
var wireEffort = map[llm.Effort]shared.ReasoningEffort{
	llm.EffortNone:    shared.ReasoningEffortNone,
	llm.EffortMinimal: shared.ReasoningEffortMinimal,
	llm.EffortLow:     shared.ReasoningEffortLow,
	llm.EffortMedium:  shared.ReasoningEffortMedium,
	llm.EffortHigh:    shared.ReasoningEffortHigh,
	llm.EffortXHigh:   shared.ReasoningEffortXhigh,
	llm.EffortMax:     shared.ReasoningEffortMax,
}

// supported is the ladder minus the rungs wireEffort has no entry for, derived
// once at init: the filter is destructive, so running it per call would rest on
// EffortLadder returning a fresh slice — another package's promise, withdrawn
// without anything here noticing.
var supported = slices.DeleteFunc(llm.EffortLadder(), func(e llm.Effort) bool {
	_, ok := wireEffort[e]
	return !ok
})

// Efforts is the whole ladder: `reasoning_effort` has a member for every rung,
// which is what made the ladder the union of the two protocols. Per protocol rather
// than per model, so a model with no reasoning is offered `max` and answers with an
// API error (design §3.1). Cloned: supported is what buildParams refuses against.
func (c *Client) Efforts() []llm.Effort { return slices.Clone(supported) }

// messageParams puts one neutral message on the wire, as one message or several: a
// user turn answering two tool calls is two `tool` messages here, where the block
// model carries both in one.
func messageParams(msg llm.Message) ([]sdk.ChatCompletionMessageParamUnion, error) {
	// Decide first, translate second, so "is there anything to send" is asked of the
	// same list that goes out rather than of a parallel one kept in step by hand.
	kept := make([]llm.Block, 0, len(msg.Content))
	for _, block := range msg.Content {
		switch block.Type {
		case llm.BlockText:
			// Rejected with no text, and this one is already in the transcript: sent, it
			// fails every later request in the session rather than only this one. Empty
			// is llm's rule, not this adapter's — IsEmpty is where it is written.
			if block.IsEmpty() {
				continue
			}
		case llm.BlockThinking:
			// Dropped rather than replayed: this wire has no field for reasoning on the
			// way back in, and the endpoints that emit it want an opaque token of their
			// own rather than the text.
			continue
		case llm.BlockToolUse:
			if block.ID == "" || block.Name == "" {
				return nil, fmt.Errorf("a tool_use block names %q with id %q; both are required, "+
					"and the result answering it points at the id", block.Name, block.ID)
			}
		case llm.BlockToolResult:
			if block.ToolUseID == "" {
				return nil, errors.New("a tool_result block names no tool_use; " +
					"the API rejects one answering nothing")
			}
		default:
			return nil, fmt.Errorf("cannot send a %s block: this adapter has no wire shape for it", block.Type)
		}
		kept = append(kept, block)
	}
	if err := llm.CheckSendable(msg.Role, kept); err != nil {
		return nil, err
	}

	var (
		text  []string
		uses  []sdk.ChatCompletionMessageToolCallUnionParam
		tools []sdk.ChatCompletionMessageParamUnion
	)
	for _, block := range kept {
		switch block.Type {
		case llm.BlockText:
			text = append(text, block.Text)
		case llm.BlockToolUse:
			uses = append(uses, sdk.ChatCompletionMessageToolCallUnionParam{
				OfFunction: &sdk.ChatCompletionMessageFunctionToolCallParam{
					ID: block.ID,
					// Arguments rather than Input: a turn the output limit cut commits a
					// fragment, and this field is a string — so the fragment would go out
					// as arguments the model never finished writing.
					Function: sdk.ChatCompletionMessageFunctionToolCallFunctionParam{
						Name:      block.Name,
						Arguments: string(block.Arguments()),
					},
				},
			})
		case llm.BlockToolResult:
			// This wire has no is_error flag, and nothing the model can act on is lost:
			// Result.Content is what it sees either way, and a failing tool's content
			// says what went wrong in words (design §3.4).
			tools = append(tools, sdk.ToolMessage(block.Content, block.ToolUseID))
		default:
			return nil, fmt.Errorf("kept a %s block and has no wire shape for it", block.Type)
		}
	}

	// Results first: this API requires the `tool` messages answering a call to
	// follow the assistant message that made it with nothing in between.
	out := tools
	joined := strings.Join(text, blockSeparator)
	switch msg.Role {
	case llm.RoleUser:
		if joined != "" {
			out = append(out, sdk.UserMessage(joined))
		}
		return out, nil
	case llm.RoleAssistant:
		assistant := sdk.ChatCompletionAssistantMessageParam{ToolCalls: uses}
		if joined != "" {
			assistant.Content.OfString = sdk.String(joined)
		}
		return append(out, sdk.ChatCompletionMessageParamUnion{OfAssistant: &assistant}), nil
	}
	return nil, fmt.Errorf("unknown role %q", msg.Role)
}
