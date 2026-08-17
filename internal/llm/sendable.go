package llm

import (
	"errors"
	"fmt"
)

// ErrSkipMessage means: leave this message out of the request. Nothing is
// deleted — it stays in the transcript and on screen, because a refusal is
// something the user should be able to see happened.
var ErrSkipMessage = errors.New("withheld from the request: nothing left to send")

// CheckSendable answers whether a message still holds anything to send. Ask it
// about what survived the adapter's own dropping, never about the transcript:
//
//	kept := m // m without the blocks only this adapter cannot express
//	err := llm.CheckSendable(kept)
//	if errors.Is(err, llm.ErrSkipMessage) { continue }
//	if err != nil { return err }
//
// The role decides, and how the message came to be empty does not: four routes
// leave an assistant turn with nothing in it — truncation mid-flight, a refusal
// (a 200 with no blocks), cancellation, and a block cut on its boundary. The
// model put it there, and refusing it fails every later request built from that
// transcript, not only the next. A user message is rasp's own, so an unsendable
// one is a bug here, and skipping it would leave the model answering the previous
// turn twice.
//
// An error rather than a bool, so the role rule can live here rather than in
// every adapter, where the next author would have to know it exists to write it.
func CheckSendable(m Message) error {
	for _, block := range m.Content {
		if !block.IsEmpty() {
			return nil
		}
	}
	if m.Role == RoleAssistant {
		return ErrSkipMessage
	}
	// Quoted because the unset role a zero-value Message carries is the likeliest
	// one to reach this.
	return fmt.Errorf("a %q message has nothing left to send", m.Role)
}

// IsEmpty reports whether a block carries nothing to put on the wire — a
// send-side question only. A block a stream has opened is empty until its
// contents arrive, and dropping it there shifts every block after it, which is
// the one bug this layer has shipped (decisions.md).
//
// A tool_use or tool_result is never empty, whatever its fields hold: dropping
// one breaks the pairing design §4 invariant 1 rests on, and an adapter that
// cannot send one refuses the request rather than leaving it out. An unrecognised
// type goes the same way — withholding is the destructive answer.
func (b Block) IsEmpty() bool {
	switch b.Type {
	case BlockText, BlockThinking:
		return b.Text == ""
	}
	return false
}
