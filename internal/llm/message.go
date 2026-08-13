package llm

import "encoding/json"

// Role is who produced a message.
//
// There is no system role. The system prompt travels in Request.System as
// ordered blocks with cache breakpoints (design §11), because a prefix-matched
// cache needs to know where the stable text ends — something a message in the
// transcript cannot express.
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

// Block is one piece of a message's content: a union with a discriminant rather
// than an interface, because every consumer already switches on the type and an
// interface would buy nothing but a MarshalJSON per variant. The field comments
// say which type owns which field; a field set on the wrong type is a bug that
// the adapters, not the type system, have to avoid.
//
// The json tags are the session file's format (design §9), not only the
// provider's: session storage persists llm.Message verbatim, so renaming a tag
// here breaks every transcript already on disk.
type Block struct {
	Type BlockType `json:"type"`

	// Text carries BlockText and BlockThinking content.
	//
	// Thinking is not only for the reader: Anthropic requires a thinking block
	// to be replayed verbatim, signature and all, when a turn that thought went
	// on to call a tool. There is no field for that signature yet, and adding
	// one is additive — an older transcript simply has none — so the adapter
	// that first meets the wire format settles its shape, with a test.
	Text string `json:"text,omitempty"`

	ID    string          `json:"id,omitempty"`    // BlockToolUse — the provider's call id
	Name  string          `json:"name,omitempty"`  // BlockToolUse
	Input json.RawMessage `json:"input,omitempty"` // BlockToolUse — arguments, as they arrived

	// ToolUseID must match the ID of a BlockToolUse. Providers reject a
	// tool_result that names no tool_use and a tool_use with no result, which
	// is the invariant the agent loop and session.Sanitize exist to hold
	// (design §4 invariant 1).
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`  // BlockToolResult — what the model sees
	IsError   bool   `json:"is_error,omitempty"` // BlockToolResult
}

// MarshalJSON writes a block, substituting an empty object for tool-call
// arguments that are not valid JSON.
//
// The substitution exists because of one state the rest of the system requires:
// a response truncated at the output limit is committed, tool_use block and all
// (design §4 invariant 2 fails every call in it), and that block's arguments are
// a fragment cut mid-object. json.Marshal validates a json.RawMessage, so
// without this the whole message fails to encode and returns zero bytes — and a
// message that cannot be written cannot be committed together with its results,
// which is invariant 1.
//
// An object rather than merely valid JSON, because `null` is both valid and how
// an OpenAI-compatible endpoint normalises an empty arguments string — and a
// tool_use whose input is null is rejected on replay exactly like one with no
// input at all.
//
// Absent arguments go the same way, and for the same reason: a turn can be cut
// off before any arguments chunk arrives, and every provider rejects a tool_use
// block with no input at all — so omitting the field would brick the replay this
// method exists to keep working.
//
// Dropping the fragment loses nothing that means anything: those arguments are
// the ones the guard exists to refuse, and what has to survive is the block, so
// the tool_result that fails it has something to point at. What is written stays
// an object, so the turn replays.
func (b Block) MarshalJSON() ([]byte, error) {
	switch {
	case b.Type == BlockToolUse && !isJSONObject(b.Input):
		b.Input = json.RawMessage("{}")
	case b.Type != BlockToolUse && len(b.Input) > 0:
		// Arguments on a block that has no business holding them are a bug
		// somewhere upstream, and dropping them keeps the message writable rather
		// than making the whole turn unencodable over a stray field. Valid ones
		// go too, and are the more dangerous half: these tags are the provider's
		// format as well as the session file's, and an extra input key on a
		// tool_result is a 400 rather than something we quietly clean up.
		b.Input = nil
	}
	type block Block // no methods, so no recursion
	return json.Marshal(block(b))
}

// Message is the provider-neutral message. The model is Anthropic-shaped
// because Anthropic's block model is the more expressive of the two and
// translating down to OpenAI is easier than the reverse (design §3.1).
//
// A streaming provider allocates exactly one of these per call and mutates it
// in place, handing the same pointer back as Event.Partial on every event. See
// the StreamResponse contract.
type Message struct {
	Role       Role       `json:"role"`
	Content    []Block    `json:"content"`
	StopReason StopReason `json:"stop_reason,omitempty"`

	// Usage is what the provider reported, and it is authoritative: context
	// estimation trusts the last reported usage and only guesses at the tail
	// after it (design §11). omitzero keeps it out of user messages, which
	// never have any.
	Usage Usage `json:"usage,omitzero"`

	Model    string `json:"model,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// Usage is the token count for one model call.
//
// The two cache fields are separate because Anthropic reports them separately
// and they price differently, but the reason both are here rather than one
// merged number is context estimation: input excludes anything served from or
// written to the cache, so the size of the context actually sent is the sum of
// all three. A cache write is counted once, on the turn that creates it.
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

	// StopMaxTokens means the response was cut off at the output limit. It is
	// not merely informational: it fails every pending tool call in that step,
	// because truncated arguments can parse and validate while being
	// semantically wrong (design §4 invariant 2).
	StopMaxTokens StopReason = "max_tokens"

	StopRefusal StopReason = "refusal"
	StopAborted StopReason = "aborted"
	StopError   StopReason = "error"
)
