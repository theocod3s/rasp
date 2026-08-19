package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/theocod3s/rasp/internal/tool"
)

// echo answers with its own name, so a result read back out of the slice says
// which call produced it.
type echo struct{ name string }

func (e echo) Name() string           { return e.name }
func (e echo) Description() string    { return e.name + " is a tool for a test" }
func (e echo) Schema() map[string]any { return map[string]any{"type": "object"} }

func (e echo) Run(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: e.name + " ran"}, nil
}

type serialEcho struct{ echo }

func (serialEcho) Sequential() bool { return true }

// TestDispatchLeavesTheSlotOfACallItDidNotRunEmpty is the half of design §6 rule 6
// that no completion order can show: a slot is written, never claimed. A call the
// batch skips leaves its own index empty and moves nothing after it — where an
// implementation taking the next free slot per finished call keeps every result,
// shifts the ones behind the gap onto their neighbours' calls, and leaves the
// empty slot at the end. In the serial path that mistake involves no race at all:
// completion order there already is request order, so writing results out as they
// come is right up until a batch has a gap in it.
//
// Reaching into the package because a gap mid-batch is not reachable through Send
// today — a call whose arguments never finished arriving comes with the stop
// reason that refuses the whole batch (design §4 invariant 2), and a cancelled
// serial batch only ever leaves gaps at the end. Approval partitioning is what
// will hand dispatch a batch with a gap in the middle (design §6 rule 5).
func TestDispatchLeavesTheSlotOfACallItDidNotRunEmpty(t *testing.T) {
	calls := []pendingCall{
		{id: "call_1", name: "alpha", ready: true},
		{id: "call_2", name: "beta", ready: true},
		// No announcement completed this one's arguments, so nothing runs it.
		{id: "call_3", name: "gamma"},
		{id: "call_4", name: "delta", ready: true},
	}
	want := []string{"alpha ran", "beta ran", "", "delta ran"}

	cases := []struct {
		name   string
		serial bool
		tools  []tool.Tool
	}{
		{"a batch running all at once", false, []tool.Tool{
			echo{"alpha"}, echo{"beta"}, echo{"gamma"}, echo{"delta"},
		}},
		{"a batch one call made serial", true, []tool.Tool{
			echo{"alpha"}, serialEcho{echo{"beta"}}, echo{"gamma"}, echo{"delta"},
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tools := tool.NewRegistry(c.tools).Snapshot()
			// Which path the case takes is what it is here to cover, so a case that
			// quietly took the other one says so rather than running its sibling twice.
			if got := serial(tools, calls); got != c.serial {
				t.Fatalf("the batch dispatches serially: %t, and this case is %s", got, c.name)
			}

			results := make([]*tool.Result, len(calls))
			(&turn{agent: &Agent{}, tools: tools}).dispatch(context.Background(), calls, results)

			for i, w := range want {
				got := results[i]
				switch {
				case w == "":
					if got != nil {
						t.Errorf("the slot of call %d (%s), which nothing ran, holds %q; a result that "+
							"moves into it is one the model reads as the answer to a call it never made",
							i, calls[i].name, got.Content)
					}
				case got == nil:
					t.Errorf("call %d (%s) ran and its slot is empty", i, calls[i].name)
				case got.Content != w:
					t.Errorf("the slot of call %d (%s) holds %q; a result belongs at the index of the "+
						"call that produced it", i, calls[i].name, got.Content)
				}
			}
		})
	}
}
