package main

import (
	"context"
	"sync"

	"github.com/theocod3s/rasp/internal/llm"
)

// depth holds the effort level a session asks for and puts it on every request.
//
// A wrapper around the provider rather than a field on the agent: the loop is
// meant to know nothing about a setting a user can turn (design §1), and Effort
// is a Request field, so the provider is the last place able to apply one
// session-wide. Efforts is the wrapped provider's own, promoted rather than
// restated — the picker and the adapter's refusal read one list (decisions.md).
type depth struct {
	llm.Provider

	mu     sync.Mutex
	effort llm.Effort
}

func newDepth(p llm.Provider) *depth { return &depth{Provider: p} }

// Stream sends the session's level, replacing whatever the request arrived
// with: this is where the answer is kept, so a caller carrying another would run
// the turn at a depth nobody chose. The unset zero value sends no depth field.
func (d *depth) Stream(ctx context.Context, req llm.Request) llm.StreamResponse {
	req.Effort = d.Effort()
	return d.Provider.Stream(ctx, req)
}

func (d *depth) Effort() llm.Effort {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.effort
}

// SetEffort is called from the UI's Update while a turn may be streaming, which
// is what the lock is for. Read per request rather than per turn, so a level
// picked mid-turn meets that turn's next step — the lag a mode switch has, for
// the same reason (design §7.4).
func (d *depth) SetEffort(e llm.Effort) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.effort = e
}
