package anthropic

import (
	"errors"
	"fmt"
	"maps"
	"slices"

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
	if req.MaxTokens <= 0 {
		return sdk.MessageNewParams{}, fmt.Errorf("anthropic: MaxTokens is %d; the API requires a positive cap, "+
			"and sending this costs an authenticated round trip to be told so", req.MaxTokens)
	}
	params := sdk.MessageNewParams{
		Model:     sdk.Model(req.Model),
		MaxTokens: int64(req.MaxTokens),
	}

	for i, spec := range req.Tools {
		tool, err := toolParam(spec)
		if err != nil {
			return sdk.MessageNewParams{}, fmt.Errorf("anthropic: tool %d: %w", i, err)
		}
		params.Tools = append(params.Tools, tool)
	}

	if req.Thinking.Enabled {
		// Adaptive unconditionally: a model id is never checked against a catalog
		// (scope.md), so nothing here can tell whether the model takes this shape or
		// only the older enabled/budget_tokens one. Adaptive is what current models
		// take, and the shape the SDK does not warn about on stderr.
		params.Thinking = sdk.ThinkingConfigParamUnion{OfAdaptive: &sdk.ThinkingConfigAdaptiveParam{}}
	}
	// Up here with the other guards rather than beside the translation below: an
	// unsendable rung is the caller's to fix, and reporting it only after some
	// unrelated message failed costs them a second round of the same refusal.
	if req.Effort != "" {
		level, ok := wireEffort[req.Effort]
		if !ok {
			return sdk.MessageNewParams{}, fmt.Errorf("anthropic: cannot send effort %q; this API takes %v, "+
				"and a turn never runs at a depth other than the one asked for", req.Effort, supported)
		}
		params.OutputConfig.Effort = level
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

	return params, nil
}

// toolParam puts one tool on the wire. The schema travels through ExtraFields
// rather than InputSchema's own Properties and Required, so it goes out as the
// tool or the MCP server wrote it: naming those two keys drops every other
// keyword — additionalProperties, $defs, enum — and the model would be told the
// arguments are looser than they are.
func toolParam(spec llm.ToolSpec) (sdk.ToolUnionParam, error) {
	if spec.Name == "" {
		return sdk.ToolUnionParam{}, errors.New("no name; the model calls a tool by the name it was " +
			"given, and nothing could be resolved from an answer naming none")
	}

	schema := maps.Clone(spec.Schema)
	if schema == nil {
		schema = map[string]any{}
	}
	// The SDK writes `type` itself and would emit it twice. Anthropic takes an
	// object schema and nothing else, so another type is refused rather than
	// relabelled into one — the tool would then be called with arguments its own
	// unmarshal target cannot read.
	if kind, ok := schema["type"]; ok {
		if kind != "object" {
			return sdk.ToolUnionParam{}, fmt.Errorf("input schema is a %v; the API takes an object schema", kind)
		}
		delete(schema, "type")
	}

	def := sdk.ToolParam{Name: spec.Name, InputSchema: sdk.ToolInputSchemaParam{ExtraFields: schema}}
	if spec.Description != "" {
		def.Description = sdk.String(spec.Description)
	}
	return sdk.ToolUnionParam{OfTool: &def}, nil
}

// wireEffort is the single source for what this adapter can express: supported is
// derived from its keys and buildParams refuses everything else, so a picker and a
// refusal cannot come apart. A rung added to llm's ladder is refused until someone
// maps it here — the safe direction, since the API rejects a value its own enum
// has no member for.
var wireEffort = map[llm.Effort]sdk.OutputConfigEffort{
	llm.EffortLow:    sdk.OutputConfigEffortLow,
	llm.EffortMedium: sdk.OutputConfigEffortMedium,
	llm.EffortHigh:   sdk.OutputConfigEffortHigh,
	llm.EffortXHigh:  sdk.OutputConfigEffortXhigh,
	llm.EffortMax:    sdk.OutputConfigEffortMax,
}

// supported is the ladder minus the rungs wireEffort has no entry for, derived
// once at init: the filter is destructive, so running it per call would depend on
// EffortLadder returning a fresh slice every time — a promise made in another
// package that nothing here would notice being withdrawn.
var supported = slices.DeleteFunc(llm.EffortLadder(), func(e llm.Effort) bool {
	_, ok := wireEffort[e]
	return !ok
})

// Efforts is every rung but none and minimal, which Anthropic's enum has no
// member for. Cloned: supported is also what buildParams refuses against.
func (c *Client) Efforts() []llm.Effort { return slices.Clone(supported) }

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
			// with the signature it arrived with, and llm.Block has no field for one.
			// Replay is required only of a thinking turn that went on to call a tool,
			// so this is a gap that opens the first request that sets Thinking.Enabled
			// with tools in it — which nothing builds yet.
			continue
		case llm.BlockToolUse:
			if block.ID == "" || block.Name == "" {
				return sdk.MessageParam{}, fmt.Errorf("a tool_use block names %q with id %q; "+
					"both are required, and the result answering it points at the id", block.Name, block.ID)
			}
		case llm.BlockToolResult:
			if block.ToolUseID == "" {
				return sdk.MessageParam{}, errors.New("a tool_result block names no tool_use; " +
					"the API rejects one answering nothing")
			}
		default:
			return sdk.MessageParam{}, fmt.Errorf("cannot send a %s block: this adapter has no wire shape for it", block.Type)
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
		case llm.BlockToolUse:
			// Arguments rather than Input: json.Marshal refuses the fragment a
			// truncated turn commits, and refuses the whole request with it.
			content = append(content, sdk.NewToolUseBlock(block.ID, block.Arguments(), block.Name))
		case llm.BlockToolResult:
			content = append(content, toolResultBlock(block))
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

// toolResultBlock carries what a tool produced back to the model. A tool that
// succeeds and prints nothing is ordinary, and the two ways of sending one are
// not equivalent: the content list is optional, an empty text block inside it is
// rejected like any other.
func toolResultBlock(block llm.Block) sdk.ContentBlockParamUnion {
	result := sdk.ToolResultBlockParam{ToolUseID: block.ToolUseID, IsError: sdk.Bool(block.IsError)}
	if block.Content != "" {
		result.Content = []sdk.ToolResultBlockParamContentUnion{{OfText: &sdk.TextBlockParam{Text: block.Content}}}
	}
	return sdk.ContentBlockParamUnion{OfToolResult: &result}
}
