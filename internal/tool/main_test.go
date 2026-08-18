package tool_test

import (
	"context"
	"encoding/json"
	"testing"

	"go.uber.org/goleak"

	"github.com/theocod3s/rasp/internal/tool"
)

// TestMain runs the leak detector over the package. Nothing here spawns a
// goroutine yet, and the check has to be in place before the first tool that
// pumps output from a child process arrives.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// stub is a Tool assembled per test. Both of the producers the interface exists
// for — reflection over a Go struct, and an MCP server's pass-through — look
// like this from the outside, which is the whole claim of the interface.
type stub struct {
	name   string
	desc   string
	schema map[string]any
	run    func(context.Context, json.RawMessage) (tool.Result, error)
}

func (s stub) Name() string           { return s.name }
func (s stub) Description() string    { return s.desc }
func (s stub) Schema() map[string]any { return s.schema }

func (s stub) Run(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	if s.run == nil {
		return tool.Result{Content: "ok"}, nil
	}
	return s.run(ctx, raw)
}

// serial opts into serial execution, the way every MCP tool does.
type serial struct{ stub }

func (serial) Sequential() bool { return true }

// canonical decodes a schema and re-encodes it with map keys sorted, so two
// schemas compare as readable strings. It decodes from the source text rather
// than reusing a decoded map: a pass-through that stripped keywords in place
// would otherwise corrupt the expectation and the comparison too.
func canonical(t *testing.T, raw string) string {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("test schema is not JSON: %v", err)
	}
	return marshal(t, decoded)
}

func marshal(t *testing.T, schema map[string]any) string {
	t.Helper()
	out, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("re-encoding a schema: %v", err)
	}
	return string(out)
}
