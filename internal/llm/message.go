package llm

import "encoding/json"

// Role is who produced a message. There is no system role: a prefix-matched
// cache needs to know where the stable text ends, which a message in the
// transcript cannot express, so the system prompt travels in Request.System as
// ordered blocks with cache breakpoints (design §11).
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// BlockType names what a Block carries. It is the JSON discriminant as well as
// the Go one, so the on-disk transcript reads the way the wire does.
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockThinking   BlockType = "thinking"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
)

// Block is one piece of a message's content: a flat union, so the field comments
// rather than the type system say which type owns which field.
//
// The json tags are the session file's format (design §9), not only the
// provider's: session storage persists llm.Message verbatim, so renaming a tag
// here breaks every transcript already on disk.
type Block struct {
	Type BlockType `json:"type"`

	// Text carries BlockText and BlockThinking content.
	//
	// Anthropic requires a thinking block to be replayed verbatim, signature and
	// all, when the turn that thought went on to call a tool. There is no field
	// for that signature yet; adding one is additive, so the first adapter to
	// meet the wire format settles its shape.
	Text string `json:"text,omitempty"`

	ID    string          `json:"id,omitempty"`    // BlockToolUse — the provider's call id
	Name  string          `json:"name,omitempty"`  // BlockToolUse
	Input json.RawMessage `json:"input,omitempty"` // BlockToolUse — arguments, as they arrived

	// ToolUseID must match the ID of a BlockToolUse. Providers reject a
	// tool_result naming no tool_use, and a tool_use with no result — the
	// invariant the agent loop and session.Sanitize exist to hold (design §4
	// invariant 1).
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`  // BlockToolResult — what the model sees
	IsError   bool   `json:"is_error,omitempty"` // BlockToolResult
}

// Arguments is a tool call's arguments as anything sending them should read
// them: the bytes that arrived, or `{}` when those bytes are not an object.
//
// The state it exists for is a turn truncated at the output limit, committed
// with its tool_use block and a fragment cut mid-object (design §4 invariant 2
// fails the call; the block stays). Three shapes are rejected on replay — a
// fragment, which json.Marshal refuses so the whole message encodes to nothing;
// no input at all; and `null`, which is how an OpenAI-compatible endpoint
// normalises an empty arguments string. Substituting `{}` loses only the
// arguments the guard exists to refuse, and keeps the block for the tool_result
// that fails it to point at.
//
// Exported because the loop keeps running after a truncated turn, so the next
// request is built from that same in-memory message. An adapter reading Input
// directly would put `{"pa` on the wire.
func (b Block) Arguments() json.RawMessage {
	if b.Type != BlockToolUse || isJSONObject(b.Input) {
		return b.Input
	}
	return json.RawMessage(emptyArguments)
}

func (b Block) MarshalJSON() ([]byte, error) {
	b.Input = b.Arguments()

	// The natural mistake with a flat union — copy a block, switch the type —
	// leaves the old type's fields behind, and `content` on a tool_use is a 400
	// exactly like `input` on a tool_result.
	switch b.Type {
	case BlockText, BlockThinking:
		b.ID, b.Name, b.ToolUseID, b.Content, b.IsError = "", "", "", "", false
	case BlockToolUse:
		b.Text, b.ToolUseID, b.Content, b.IsError = "", "", "", false
	case BlockToolResult:
		b.Text, b.ID, b.Name = "", "", ""
	default:
		// An untaught type keeps nothing but its type: emitting whichever
		// variant's fields happen to be set is the 400 the scrubbing prevents.
		// Content coming out empty tells whoever added it to add a case here.
		b.Text, b.ID, b.Name, b.Input, b.ToolUseID, b.Content, b.IsError = "", "", "", nil, "", "", false
	}

	switch {
	case b.Type != BlockToolUse && len(b.Input) > 0:
		// A *valid* stray is the dangerous half: these tags are the provider's
		// format as well as the session file's, so an extra input key on a
		// tool_result is a 400 rather than something anyone cleans up later.
		b.Input = nil
	}
	type block Block // no methods, so no recursion
	return json.Marshal(block(b))
}

// Message is the provider-neutral message, Anthropic-shaped because their block
// model is the more expressive of the two and translating down to OpenAI is
// easier than the reverse (design §3.1). A streaming provider allocates one per
// call and mutates it in place — see the StreamResponse contract.
type Message struct {
	Role Role `json:"role"`

	// Content is required, and no value of it rescues an empty message: nil
	// encodes as null, `[]` is refused by the same providers, and a text block
	// whose text never arrived encodes as {"type":"text"} and is refused too.
	// Both are reachable when a turn breaks off before anything streams, and
	// refusing to commit them belongs to whoever commits — Block.MarshalJSON can
	// correct a field's value, not a message's absence.
	Content    []Block    `json:"content"`
	StopReason StopReason `json:"stop_reason,omitempty"`

	// Usage is what the provider reported, and it is authoritative: context
	// estimation trusts the last reported usage and only guesses at the tail
	// after it (design §11). omitzero keeps it out of user messages.
	Usage Usage `json:"usage,omitzero"`

	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// Usage is the token count for one model call. Input excludes anything served
// from or written to the cache, so the context actually sent is the sum of all
// three counts; a cache write is counted once, on the turn that creates it.
type Usage struct {
	Input      int `json:"input,omitempty"`
	Output     int `json:"output,omitempty"`
	CacheRead  int `json:"cache_read,omitempty"`
	CacheWrite int `json:"cache_write,omitempty"`
}

// StopReason is why the model stopped generating.
type StopReason string

const (
	StopEndTurn StopReason = "end_turn"
	StopToolUse StopReason = "tool_use"

	// StopMaxTokens fails every pending tool call in that step, because truncated
	// arguments can parse and validate while being semantically wrong (design §4
	// invariant 2).
	StopMaxTokens StopReason = "max_tokens"

	StopRefusal StopReason = "refusal"
	StopAborted StopReason = "aborted"
	StopError   StopReason = "error"
)
