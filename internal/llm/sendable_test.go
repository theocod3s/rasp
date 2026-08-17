package llm_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/llm"
)

// TestCheckSendableSkipsAnEmptyAssistantTurn covers the four routes a transcript
// takes to a message with nothing in it, in the shape each one leaves behind: a
// refusal and a cancelled turn arrive with no blocks at all, a truncated turn
// with only what the adapter dropped, and a block cut on its boundary with text
// that never came.
func TestCheckSendableSkipsAnEmptyAssistantTurn(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content []llm.Block
	}{
		{"no blocks at all", nil},
		{"an empty slice", []llm.Block{}},
		{"a block cut on its boundary", []llm.Block{{Type: llm.BlockText}}},
		{"nothing but empty blocks", []llm.Block{{Type: llm.BlockText}, {Type: llm.BlockThinking}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := llm.CheckSendable(llm.RoleAssistant, tc.content)
			if !errors.Is(err, llm.ErrSkipMessage) {
				t.Fatalf("err = %v, want one wrapping ErrSkipMessage; refusing it fails every later "+
					"request built from this transcript, not just the next one", err)
			}
		})
	}
}

// TestCheckSendableRefusesEveryOtherRole: rasp writes a user message, so an
// unsendable one is a bug here rather than a state the model left behind. The
// assertion is that the error is NOT the skip — any error at all would also be
// returned by the version of this rule that skips everything and then finds no
// messages left.
func TestCheckSendableRefusesEveryOtherRole(t *testing.T) {
	for _, role := range []llm.Role{llm.RoleUser, "system", ""} {
		t.Run(string(role), func(t *testing.T) {
			err := llm.CheckSendable(role, nil)
			switch {
			case err == nil:
				t.Fatalf("no error; a %q message with nothing in it went out as sendable", role)
			case errors.Is(err, llm.ErrSkipMessage):
				t.Fatalf("err = %v; only an assistant turn is withheld, and skipping this one leaves "+
					"the model answering the previous turn twice", err)
			// Quoted, so the check still has something to look for when the role is
			// unset — Contains against "" is satisfied by every string, and a check
			// that cannot fail reads exactly like one that passed.
			case !strings.Contains(err.Error(), fmt.Sprintf("%q", role)):
				t.Errorf("err = %v, want one naming the role that produced it", err)
			}
		})
	}
}

// TestCheckSendablePassesAnythingWithContent is the other direction, and the one
// that matters more: a message wrongly withheld is a turn the model never sees.
func TestCheckSendablePassesAnythingWithContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content []llm.Block
	}{
		{"text", []llm.Block{{Type: llm.BlockText, Text: "hello"}}},
		{"thinking", []llm.Block{{Type: llm.BlockThinking, Text: "reasoning"}}},
		{"one empty block beside a full one", []llm.Block{
			{Type: llm.BlockText, Text: "One moment."},
			{Type: llm.BlockText},
		}},
		// Never empty however bare, in either direction: a tool_use withheld from
		// the request leaves its tool_result answering nothing, and a tool_result
		// withheld leaves the tool_use unanswered (design §4 invariant 1).
		{"a bare tool_use", []llm.Block{{Type: llm.BlockToolUse}}},
		{"a tool_result with no output", []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "toolu_01"}}},
		{"a block type nothing here knows", []llm.Block{{Type: "redacted_thinking"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, role := range []llm.Role{llm.RoleAssistant, llm.RoleUser} {
				if err := llm.CheckSendable(role, tc.content); err != nil {
					t.Errorf("%s: err = %v, want nil", role, err)
				}
			}
		})
	}
}

// TestIsEmptyReadsTheFieldItsTypeOwns: Block is a flat union, so a tool_use
// carrying leftover text from a copy-and-retype must not read as content, and a
// text block must not be rescued by a field belonging to another variant.
func TestIsEmptyReadsTheFieldItsTypeOwns(t *testing.T) {
	for _, tc := range []struct {
		name  string
		block llm.Block
		want  bool
	}{
		{"text with text", llm.Block{Type: llm.BlockText, Text: "hi"}, false},
		{"text with none", llm.Block{Type: llm.BlockText}, true},
		{"thinking with none", llm.Block{Type: llm.BlockThinking}, true},
		{"text carrying only another variant's fields", llm.Block{
			Type: llm.BlockText, ID: "toolu_01", Name: "read", Content: "ok",
		}, true},
		{"tool_use, whatever else it carries", llm.Block{
			Type: llm.BlockToolUse, Text: "left over", Input: json.RawMessage(`{}`),
		}, false},
		// The case that separates this from `return b.Text == ""`, which the whole
		// table above passes: the tempting simplification drops the type switch,
		// and dropping a bare tool_use is design §4 invariant 1 broken.
		{"a tool_use with nothing in it at all", llm.Block{Type: llm.BlockToolUse}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.block.IsEmpty(); got != tc.want {
				t.Errorf("IsEmpty() = %v, want %v", got, tc.want)
			}
		})
	}
}
