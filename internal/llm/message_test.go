package llm_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/theocod3s/rasp/internal/llm"
)

// example pairs a message with the line design §9 shows it as on disk. Copied
// verbatim: the session file is append-only, so those lines *are* the format.
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

// TestMessageEncodesToTheSessionFormat pins the wire names by exact bytes, so a
// renamed tag is called a migration at the moment of the rename.
//
// One deliberate difference from design §9's example: its tool_result spells out
// "is_error":false, while omitempty leaves it out here. It decodes identically,
// and the alternative is a marshaller per block type.
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

	// A transcript is written by a version of rasp that is not necessarily this
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
// broken.
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

// TestTruncatedToolCallStillEncodes is the state design §4 invariant 2 commits.
// Without Block.MarshalJSON's substitution the whole message encodes to nothing
// and the turn cannot be written — invariant 1, broken by an encoder.
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

	// The block has to survive, for the failing tool_result to point at.
	var loaded llm.Message
	if err := json.Unmarshal(encoded, &loaded); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}
	if got := loaded.Content[0].ID; got != "toolu_01A9" {
		t.Errorf("tool_use id = %q, want %q", got, "toolu_01A9")
	}
}

// TestToolCallWithNoArgumentsStillEncodesAnObject is the other half: a turn can
// break off before any arguments chunk arrives at all.
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

// TestNullArgumentsEncodeAsAnObject covers the shapes that are valid JSON and
// still rejected on replay.
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

// TestStrayArgumentsDoNotSinkTheMessage is the same protection for a field set
// on a block that should not carry one.
func TestStrayArgumentsDoNotSinkTheMessage(t *testing.T) {
	msg := llm.Message{
		Role: llm.RoleUser,
		Content: []llm.Block{{
			Type: llm.BlockToolResult, ToolUseID: "toolu_01A9", Content: "ok", Input: []byte(`{"stray":true}`),
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

// TestStrayFieldsFromAnotherBlockTypeAreDropped: copy a block, switch the type,
// leave the old type's fields behind.
func TestStrayFieldsFromAnotherBlockTypeAreDropped(t *testing.T) {
	msg := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.Block{{
			Type:      llm.BlockToolUse,
			ID:        "toolu_01A9",
			Name:      "read",
			Input:     []byte(`{"path":"auth.go"}`),
			Text:      "stray text",
			ToolUseID: "toolu_09",
			Content:   "stray result",
			IsError:   true,
		}},
	}

	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	const want = `{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01A9","name":"read","input":{"path":"auth.go"}}]}`
	if string(encoded) != want {
		t.Errorf("encoding:\n got %s\nwant %s", encoded, want)
	}
}

// TestAnUnknownBlockTypeKeepsNothing is for the next person to add a BlockType.
func TestAnUnknownBlockTypeKeepsNothing(t *testing.T) {
	msg := llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.Block{{
			Type:      "redacted_thinking",
			Text:      "kept secret",
			ID:        "toolu_01",
			Name:      "read",
			Input:     []byte(`{"path":"a.go"}`),
			ToolUseID: "toolu_09",
			Content:   "stray",
			IsError:   true,
		}},
	}

	encoded, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshalling: %v", err)
	}
	const want = `{"role":"assistant","content":[{"type":"redacted_thinking"}]}`
	if string(encoded) != want {
		t.Errorf("encoding:\n got %s\nwant %s", encoded, want)
	}
}

// TestArgumentsCorrectsWhatTheWireWouldReject is the encoder's substitution,
// reachable by everything else that has to apply it.
func TestArgumentsCorrectsWhatTheWireWouldReject(t *testing.T) {
	cases := map[string]struct {
		block llm.Block
		want  string
	}{
		"a fragment": {
			block: llm.Block{Type: llm.BlockToolUse, ID: "t1", Name: "write", Input: []byte(`{"pa`)},
			want:  `{}`,
		},
		"no arguments at all": {
			block: llm.Block{Type: llm.BlockToolUse, ID: "t1", Name: "write"},
			want:  `{}`,
		},
		"null": {
			block: llm.Block{Type: llm.BlockToolUse, ID: "t1", Name: "write", Input: []byte(`null`)},
			want:  `{}`,
		},
		"arguments that arrived whole": {
			block: llm.Block{Type: llm.BlockToolUse, ID: "t1", Name: "write", Input: []byte(`{"path":"a.go"}`)},
			want:  `{"path":"a.go"}`,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := string(tc.block.Arguments()); got != tc.want {
				t.Errorf("Arguments() = %s, want %s", got, tc.want)
			}
		})
	}
}
