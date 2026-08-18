package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/theocod3s/rasp/internal/tool"
)

// reflectedSchema is the shape reflection over a tagged Go struct produces.
const reflectedSchema = `{
  "type": "object",
  "properties": {
    "path":        {"type": "string",  "description": "Path to the file, relative to the workspace root"},
    "old_string":  {"type": "string",  "description": "Exact text to find"},
    "replace_all": {"type": "boolean", "description": "Replace every occurrence"}
  },
  "required": ["path", "old_string"],
  "additionalProperties": false
}`

// serverSchema is what a conforming MCP server may send: a 2020-12 schema whose
// root is a $ref, with the object shape hidden behind $defs. It is the case that
// constrains the interface — a Schema() returning a Go type, or normalizing on
// the way through, would have to either lose this or resolve it.
const serverSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$ref": "#/$defs/Query",
  "$defs": {
    "Query": {
      "type": "object",
      "properties": {
        "repo":  {"$ref": "#/$defs/Repo"},
        "limit": {"type": "integer", "minimum": 1, "maximum": 100}
      },
      "allOf": [{"required": ["repo"]}],
      "unevaluatedProperties": false
    },
    "Repo": {"type": "string", "pattern": "^[^/]+/[^/]+$"}
  }
}`

func TestSchemaCarriesEitherProducersSchemaUnchanged(t *testing.T) {
	for name, raw := range map[string]string{
		"reflected over a Go struct":   reflectedSchema,
		"passed through from a server": serverSchema,
	} {
		t.Run(name, func(t *testing.T) {
			want := canonical(t, raw)
			if want == "null" || want == "{}" {
				t.Fatalf("this schema decoded to %s, so the round trip below compares nothing", want)
			}

			var decoded map[string]any
			if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
				t.Fatalf("test schema is not JSON: %v", err)
			}
			var subject tool.Tool = stub{name: "search", desc: "…", schema: decoded}

			if got := marshal(t, subject.Schema()); got != want {
				t.Errorf("the schema changed on its way through Schema()\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

func TestBothProducersSatisfyTool(t *testing.T) {
	var flat, composed map[string]any
	if err := json.Unmarshal([]byte(reflectedSchema), &flat); err != nil {
		t.Fatalf("reflected schema: %v", err)
	}
	if err := json.Unmarshal([]byte(serverSchema), &composed); err != nil {
		t.Fatalf("server schema: %v", err)
	}

	tools := []tool.Tool{
		stub{name: "edit", desc: "Replace exact text in a file", schema: flat},
		serial{stub{name: "mcp__github__search", desc: "Search a repository", schema: composed}},
	}
	for _, subject := range tools {
		if subject.Name() == "" {
			t.Error("a tool with no name cannot be called by the model or found in the registry")
		}
		if subject.Description() == "" {
			t.Errorf("%s has no description; the description is the prompt text the model chooses on", subject.Name())
		}
		if subject.Schema() == nil {
			t.Errorf("%s has no schema", subject.Name())
		}
	}
}

func TestSequentialIsOptIn(t *testing.T) {
	var parallel tool.Tool = stub{name: "read"}
	if _, ok := parallel.(tool.Sequential); ok {
		t.Error("a tool implementing only Tool satisfied tool.Sequential; it has to stay optional, because a tool that never mentions concurrency is the parallel default")
	}

	var mcp tool.Tool = serial{stub{name: "mcp__github__search"}}
	declared, ok := mcp.(tool.Sequential)
	if !ok {
		t.Fatal("a tool implementing Sequential was not recognised through Tool")
	}
	if !declared.Sequential() {
		t.Error("Sequential() returned false for a tool that declares itself sequential")
	}
}

// exitDetails stands in for the typed payload a bash-like tool hands the UI.
type exitDetails struct{ Code int }

func TestRanAndFailedCarriesNoGoError(t *testing.T) {
	failing := stub{name: "bash", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		return tool.Result{
			Content: "FAIL\tinternal/tool\nexit status 1",
			IsError: true,
			Title:   "go test ./internal/tool",
			Details: &exitDetails{Code: 1},
		}, nil
	}}

	res, err := failing.Run(context.Background(), nil)
	if err != nil {
		t.Fatalf("a tool that ran and failed returned the Go error %v; a failure the model can act on belongs in Result.IsError, and a Go error ends the turn instead", err)
	}
	if !res.IsError {
		t.Error("a failed call came back with IsError false, so the model is told it succeeded")
	}
	if res.Content == "" {
		t.Error("an error result with no content leaves the model nothing to adapt to")
	}

	details, ok := res.Details.(*exitDetails)
	if !ok {
		t.Fatalf("Details is %T, not the payload the tool put there", res.Details)
	}
	if details.Code != 1 {
		t.Errorf("Details.Code is %d, want 1", details.Code)
	}
}

var errNoShell = errors.New("no shell on PATH")

func TestCouldNotRunIsAGoError(t *testing.T) {
	broken := stub{name: "bash", run: func(context.Context, json.RawMessage) (tool.Result, error) {
		return tool.Result{}, fmt.Errorf("starting bash: %w", errNoShell)
	}}

	res, err := broken.Run(context.Background(), nil)
	if !errors.Is(err, errNoShell) {
		t.Fatalf("got error %v, want one wrapping %v", err, errNoShell)
	}
	if res.IsError || res.Content != "" || res.Details != nil {
		t.Errorf("a tool that could not run returned the populated result %+v; the loop turns this error into the result the model sees, so a tool that fills one in has it counted twice", res)
	}
}
