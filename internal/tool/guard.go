package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
)

// RunSafely runs one call under a panic guard. A panicking tool comes back as
// Result{IsError: true} with a nil error, the way any other failed call does, so
// the model adapts and the process survives (design §4 invariant 4). Anything a
// tool returns normally passes through untouched, its error included.
//
// The stack trace goes to the log, never into Content: it is tokens the model
// cannot act on.
func RunSafely(ctx context.Context, t Tool, raw json.RawMessage) (res Result, err error) {
	var name string

	// returned separates "did not panic" from "panicked with a nil value", which
	// recover cannot: under GODEBUG=panicnil=1 it answers nil for both, and a
	// guard reading that as success hands the model an empty successful result for
	// a tool that blew up.
	returned := false

	defer func() {
		r := recover()
		if returned {
			return
		}
		// fmt renders a panicking String or Error method as %!v(PANIC=…). Handing
		// the value to a JSON encoder instead would let that second panic run
		// inside this recovery, where nothing is left to catch it.
		value := fmt.Sprint(r)
		slog.ErrorContext(ctx, "tool panicked", "tool", name, "panic", value, "stack", string(debug.Stack()))
		res = Result{Content: "tool panicked: " + value, IsError: true}
		err = nil
	}()

	// Read under the guard and before the call, so a tool whose Name panics is
	// contained too and the recovery path never calls back into it.
	name = t.Name()

	res, err = t.Run(ctx, raw)
	returned = true
	return res, err
}
