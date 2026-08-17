package llm

import (
	"errors"
	"fmt"
)

// ErrSkipMessage means: leave this message out of the request. Nothing is
// deleted — the message stays in the transcript and on screen, because a refusal
// is something the user should be able to see happened.
var ErrSkipMessage = errors.New("nothing left to send")

// CheckSendable answers whether a message still holds anything to send, once an
// adapter has dropped the blocks it alone cannot express:
//
//	err := llm.CheckSendable(m)
//	if errors.Is(err, llm.ErrSkipMessage) { continue }
//	if err != nil { return err }
//
// The role decides, and how the message came to be empty does not. Four routes
// leave an assistant message with nothing in it — a turn truncated mid-flight, a
// refusal (which arrives as a 200 with no blocks), a cancelled turn, and a block
// cut on its boundary — so the model put it there and it is a state, not a bug.
// Refusing one fails every later request built from that transcript rather than
// only the next, and the transcript is on disk: the session is dead until
// someone edits the file by hand.
//
// rasp writes a user message, so an unsendable one is a bug in this process, and
// skipping it would leave the model answering the previous turn twice.
//
// An error rather than a bool, so that the role rule can live here: a bool puts
// `if m.Role == RoleAssistant { continue }` back in every adapter, where the
// next author has to already know the rule in order to write it.
func CheckSendable(m Message) error {
	for _, block := range m.Content {
		if !block.IsEmpty() {
			return nil
		}
	}

	reason := "every block it holds is empty"
	if len(m.Content) == 0 {
		reason = "it holds no blocks"
	}
	if m.Role == RoleAssistant {
		return fmt.Errorf("%w: %s", ErrSkipMessage, reason)
	}
	return fmt.Errorf("a %s message has nothing left to send: %s", m.Role, reason)
}

// IsEmpty reports whether a block carries nothing to put on the wire. A provider
// rejects a text block with no text exactly as it rejects a message with no
// blocks, which is why both halves of the rule live here.
//
// A tool_use or tool_result is never empty, whatever its fields hold: dropping
// one breaks the pairing design §4 invariant 1 rests on, and an adapter that
// cannot send one refuses the request rather than leaving it out. An
// unrecognised type goes the same way — withholding is the destructive answer.
func (b Block) IsEmpty() bool {
	switch b.Type {
	case BlockText, BlockThinking:
		return b.Text == ""
	}
	return false
}
