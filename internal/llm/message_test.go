package llm_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/theocod3s/rasp/internal/llm"
)

// example pairs a message with the line design §9 shows it as on disk. The
// lines are copied from that example rather than described, because the session
// file is append-only: they *are* the format, and a transcript written last
// month has to keep loading after any change to these types.
type example struct {
	name string
	json string
	msg  llm.Message
}

func designExamples() []example {
	return []example{{
		name: "a user message",
		json: `{"role":"user","content":[{"type":"text","text":"fix the failing auth test"}]}`,
		msg: llm.Message{
			Role:    llm.RoleUser,
			Content: []llm.Block{{Type: llm.BlockText, Text: "fix the failing auth test"}},
		},
	}, {
		name: "an assistant message that calls a tool",
		json: `{"role":"assistant","content":[` +
			`{"type":"text","text":"Let me look at the test."},` +
			`{"type":"tool_use","id":"toolu_01A9","name":"read","input":{"path":"internal/auth/check_test.go"}}` +
			`],"stop_reason":"tool_use","usage":{"input":4210,"output":88,"cache_read":3900}}`,
		msg: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.Block{
				{Type: llm.BlockText, Text: "Let me look at the test."},
				{
					Type:  llm.BlockToolUse,
					ID:    "toolu_01A9",
					Name:  "read",
					Input: json.RawMessage(`{"path":"internal/auth/check_test.go"}`),
				},
			},
			StopReason: llm.StopToolUse,
			Usage:      llm.Usage{Input: 4210, Output: 88, CacheRead: 3900},
		},
	}, {
		name: "the tool result that answers it",
		json: `{"role":"user","content":[` +
			`{"type":"tool_result","tool_use_id":"toolu_01A9","content":"package auth\n\nfunc TestCheck(t *testing.T) {\n..."}` +
			`]}`,
		msg: llm.Message{
			Role: llm.RoleUser,
			Content: []llm.Block{{
				Type:      llm.BlockToolResult,
				ToolUseID: "toolu_01A9",
				Content:   "package auth\n\nfunc TestCheck(t *testing.T) {\n...",
			}},
		},
	}}
}

// TestMessageEncodesToTheSessionFormat pins the wire names. They are not an
// implementation detail of this package: session storage persists llm.Message
// verbatim, so a renamed tag is a migration, and it is worth one exact-bytes
// comparison to be told so at the moment of the rename. It also pins the two
// absences — a user message carries no stop_reason and no usage, which is what
// omitempty and omitzero are doing there.
//
// One difference from design §9's example is deliberate. Its tool_result block
// spells out "is_error":false; here is_error is omitempty, so a result that
// succeeded says nothing rather than saying false. It decodes identically, and
// the alternative is a marshaller written per block type to emit one field
// whose absence already means what its presence would.
func TestMessageEncodesToTheSessionFormat(t *testing.T) {
	for _, ex := range designExamples() {
		t.Run(ex.name, func(t *testing.T) {
			got, err := json.Marshal(ex.msg)
			if err != nil {
				t.Fatalf("marshalling: %v", err)
			}
			if string(got) != ex.json {
				t.Errorf("encoding:\n got %s\nwant %s", got, ex.json)
			}
		})
	}
}

// TestMessageDecodesTheSessionFormat is the direction that runs on every
// resumed session.
func TestMessageDecodesTheSessionFormat(t *testing.T) {
	for _, ex := range designExamples() {
		t.Run(ex.name, func(t *testing.T) {
			var got llm.Message
			if err := json.Unmarshal([]byte(ex.json), &got); err != nil {
				t.Fatalf("unmarshalling: %v", err)
			}
			if !reflect.DeepEqual(got, ex.msg) {
				t.Errorf("decoding %s:\n got %+v\nwant %+v", ex.json, got, ex.msg)
			}
		})
	}

	// The tool_result line as design §9 actually prints it, is_error and all: a
	// transcript is written by a version of rasp that is not necessarily this
	// one, so the decoder reads the field it does not write.
	t.Run("an explicit is_error:false", func(t *testing.T) {
		const line = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01A9","content":"ok","is_error":false}]}`
		var msg llm.Message
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("unmarshalling: %v", err)
		}
		if msg.Content[0].IsError {
			t.Error(`IsError = true, want false`)
		}
	})
}

// TestFailedToolResultRoundTrips is the case omitting is_error could have
// broken: a tool result that failed must still say so after a save and a load,
// because is_error is what tells the model its command did not work.
func TestFailedToolResultRoundTrips(t *testing.T) {
	want := llm.Message{
		Role: llm.RoleUser,
		Content: []llm.Block{{
			Type:      llm.BlockToolResult,
			ToolUseID: "toolu_01A9",
			Content:   "exit status 1: no such file",
			IsError:   true,
		}},
	}

	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	var got llm.Message
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("unmarshalling %s: %v", encoded, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip through %s:\n got %+v\nwant %+v", encoded, got, want)
	}
}

// TestTruncatedToolCallStillEncodes is the state design §4 invariant 2 commits: a
// response cut off at the output limit, holding a tool_use block whose arguments
// stop mid-object. json.Marshal validates a json.RawMessage, so without the
// substitution in Block.MarshalJSON the whole message encodes to nothing and the
// turn cannot be written at all — which is invariant 1 broken by an encoder.
func TestTruncatedToolCallStillEncodes(t *testing.T) {
	msg := llm.Message{
		Role:       llm.RoleAssistant,
		StopReason: llm.StopMaxTokens,
		Content: []llm.Block{{
			Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "write", Input: []byte(`{"pa`),
		}},
	}

	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshalling a truncated turn: %v", err)
	}

	const want = `{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01A9","name":"write","input":{}}],"stop_reason":"max_tokens"}`
	if string(encoded) != want {
		t.Errorf("encoding:\n got %s\nwant %s", encoded, want)
	}

	// What has to survive is the block, so the tool_result that fails the call
	// has something to point at.
	var loaded llm.Message
	if err := json.Unmarshal(encoded, &loaded); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	if got := loaded.Content[0].ID; got != "toolu_01A9" {
		t.Errorf("tool_use id = %q, want %q", got, "toolu_01A9")
	}
}

// TestToolCallWithNoArgumentsStillEncodesAnObject is the other half of the
// truncation case: a turn can be cut off before any arguments chunk arrives at
// all, leaving the field unset rather than half-written. Every provider rejects
// a tool_use block with no input, so an omitted field bricks the replay as surely
// as an unencodable one — the block has to carry an object either way.
func TestToolCallWithNoArgumentsStillEncodesAnObject(t *testing.T) {
	msg := llm.Message{
		Role:       llm.RoleAssistant,
		StopReason: llm.StopMaxTokens,
		Content:    []llm.Block{{Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "write"}},
	}

	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}

	const want = `{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01A9","name":"write","input":{}}],"stop_reason":"max_tokens"}`
	if string(encoded) != want {
		t.Errorf("encoding:\n got %s\nwant %s", encoded, want)
	}
}

// TestNullArgumentsEncodeAsAnObject covers the shape that is valid JSON and still
// rejected on replay. `null` is how an OpenAI-compatible endpoint normalises an
// empty arguments string, so a transcript can hold one — and a tool_use whose
// input is null fails the next request exactly like one with no input.
func TestNullArgumentsEncodeAsAnObject(t *testing.T) {
	cases := map[string]string{
		"null":     `null`,
		"a number": `42`,
		"an array": `[1]`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			msg := llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.Block{{
					Type: llm.BlockToolUse, ID: "toolu_01A9", Name: "write", Input: []byte(input),
				}},
			}
			encoded, err := json.Marshal(msg)
			if err != nil {
				t.Fatalf("marshalling: %v", err)
			}
			const want = `{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01A9","name":"write","input":{}}]}`
			if string(encoded) != want {
				t.Errorf("encoding:\n got %s\nwant %s", encoded, want)
			}
		})
	}
}

// TestStrayArgumentsDoNotSinkTheMessage is the same protection for a field set on
// a block that should not carry one. It is a bug upstream either way, and the
// message still has to be writable: a turn that cannot be encoded cannot be
// committed together with its results.
func TestStrayArgumentsDoNotSinkTheMessage(t *testing.T) {
	msg := llm.Message{
		Role: llm.RoleUser,
		Content: []llm.Block{{
			Type: llm.BlockToolResult, ToolUseID: "toolu_01A9", Content: "ok", Input: []byte(`{"pa`),
		}},
	}

	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	const want = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01A9","content":"ok"}]}`
	if string(encoded) != want {
		t.Errorf("encoding:\n got %s\nwant %s", encoded, want)
	}
}
