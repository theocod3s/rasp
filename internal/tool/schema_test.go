package tool_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/tool"
)

func noop[TIn any](context.Context, TIn) (tool.Result, error) { return tool.Result{}, nil }

type editIn struct {
	Path       string `json:"path"                  desc:"Path to the file, relative to the workspace root"`
	OldString  string `json:"old_string"            desc:"Exact text to find"`
	ReplaceAll bool   `json:"replace_all,omitempty" desc:"Replace every occurrence"`
	scratch    string // invisible to encoding/json, so it must stay invisible in the schema
	Internal   string `json:"-"`
}

func TestFlatSchemaShape(t *testing.T) {
	want := canonical(t, `{
	  "type": "object",
	  "properties": {
	    "path":        {"type": "string",  "description": "Path to the file, relative to the workspace root"},
	    "old_string":  {"type": "string",  "description": "Exact text to find"},
	    "replace_all": {"type": "boolean", "description": "Replace every occurrence"}
	  },
	  "required": ["path", "old_string"],
	  "additionalProperties": false
	}`)

	got := marshal(t, tool.New("edit", "Replace exact text in a file", noop[editIn]).Schema())
	if got != want {
		t.Errorf("reflection over the input struct produced\n got: %s\nwant: %s", got, want)
	}
}

type severity string

type span struct {
	Start int `json:"start" desc:"First line, 1-based"`
	Count int `json:"count" desc:"How many lines"`
}

type nestedIn struct {
	Where  span           `json:"where"            desc:"The span to read"`
	Levels []severity     `json:"levels,omitempty" desc:"Which findings to report" enum:"info, warning ,error"`
	Caps   map[string]int `json:"caps,omitempty"   desc:"Per-rule limits"`
	Extra  []span         `json:"extra,omitempty"  desc:"Further spans"`
}

func TestNestedSliceMapAndEnumShape(t *testing.T) {
	want := canonical(t, `{
	  "type": "object",
	  "properties": {
	    "where": {
	      "type": "object",
	      "description": "The span to read",
	      "properties": {
	        "start": {"type": "integer", "description": "First line, 1-based"},
	        "count": {"type": "integer", "description": "How many lines"}
	      },
	      "required": ["start", "count"],
	      "additionalProperties": false
	    },
	    "levels": {
	      "type": "array",
	      "description": "Which findings to report",
	      "items": {"type": "string", "enum": ["info", "warning", "error"]}
	    },
	    "caps": {
	      "type": "object",
	      "description": "Per-rule limits",
	      "additionalProperties": {"type": "integer"}
	    },
	    "extra": {
	      "type": "array",
	      "description": "Further spans",
	      "items": {
	        "type": "object",
	        "properties": {
	          "start": {"type": "integer", "description": "First line, 1-based"},
	          "count": {"type": "integer", "description": "How many lines"}
	        },
	        "required": ["start", "count"],
	        "additionalProperties": false
	      }
	    }
	  },
	  "required": ["where"],
	  "additionalProperties": false
	}`)

	got := marshal(t, tool.New("read", "Read a span of a file", noop[nestedIn]).Schema())
	if got != want {
		t.Errorf("reflection over a nested input struct produced\n got: %s\nwant: %s", got, want)
	}
}

type promoted struct {
	Reason string `json:"reason" desc:"Why the call is being made"`
}

type embeddingIn struct {
	promoted
	Path string `json:"path" desc:"Path to the file"`
}

func TestEmbeddedFieldsArePromotedTheWayEncodingJSONPromotesThem(t *testing.T) {
	want := canonical(t, `{
	  "type": "object",
	  "properties": {
	    "reason": {"type": "string", "description": "Why the call is being made"},
	    "path":   {"type": "string", "description": "Path to the file"}
	  },
	  "required": ["reason", "path"],
	  "additionalProperties": false
	}`)

	got := marshal(t, tool.New("audit", "Audit a file", noop[embeddingIn]).Schema())
	if got != want {
		t.Errorf("an embedded struct did not flatten\n got: %s\nwant: %s", got, want)
	}
}

type selfRef struct {
	Next *selfRef `json:"next"`
}

type quoted struct {
	Count int `json:"count,string"`
}

type piped struct {
	Ch chan int `json:"ch"`
}

type intKeyed struct {
	By map[int]string `json:"by"`
}

type stamped struct {
	At time.Time `json:"at"`
}

type numericEnum struct {
	N int `json:"n" enum:"1,2"`
}

type blankEnum struct {
	S string `json:"s" enum:"low,,high"`
}

type emptyEnum struct {
	S string `json:"s" enum:""`
}

type collide struct {
	promoted
	Reason string `json:"reason"`
}

// TestNewRefusesWhatNoSchemaCanDescribe asserts each case against the reason it
// is refused for, not merely that something went wrong. Recovering any panic at
// all was the first version, and it stayed green when the non-struct guard was
// deleted: reflect panicked on NumField instead, an unrelated crash the test read
// as the refusal working.
func TestNewRefusesWhatNoSchemaCanDescribe(t *testing.T) {
	cases := []struct {
		name      string
		construct func()
		because   string
	}{
		{"an input that is not a struct", func() { tool.New("x", "d", noop[string]) }, "has to be a struct"},
		{"a self-referential type", func() { tool.New("x", "d", noop[selfRef]) }, "contains itself"},
		{`the ",string" json option`, func() { tool.New("x", "d", noop[quoted]) }, `",string" json option`},
		{"a channel field", func() { tool.New("x", "d", noop[piped]) }, "no JSON type describes it"},
		{"a map keyed by something but string", func() { tool.New("x", "d", noop[intKeyed]) }, "keys are strings"},
		{"a field that marshals itself", func() { tool.New("x", "d", noop[stamped]) }, "marshals or unmarshals itself"},
		{"an enum on a non-string field", func() { tool.New("x", "d", noop[numericEnum]) }, "needs a string field"},
		{"an enum with a blank value", func() { tool.New("x", "d", noop[blankEnum]) }, "blank value"},
		{"an empty enum", func() { tool.New("x", "d", noop[emptyEnum]) }, "blank value"},
		{"two fields claiming one property", func() { tool.New("x", "d", noop[collide]) }, `both claim the property "reason"`},
		{"a tool with no name", func() { tool.New("", "d", noop[editIn]) }, "no name"},
		{"a tool with no description", func() { tool.New("x", "", noop[editIn]) }, "no description"},
		{"a tool with no handler", func() { tool.New[editIn]("x", "d", nil) }, "no handler"},
	}

	if len(cases) == 0 {
		t.Fatal("no cases: this test would pass without refusing anything")
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			defer func() {
				switch refusal := recover().(type) {
				case nil:
					t.Error("construction returned a Tool; a schema nobody can honour has to fail at startup, not on the first call")
				case string:
					if !strings.HasPrefix(refusal, "tool: ") {
						t.Errorf("panicked with %q, which did not come from this package", refusal)
					}
					if !strings.Contains(refusal, c.because) {
						t.Errorf("refused with %q, which does not mention %q", refusal, c.because)
					}
				default:
					t.Errorf("panicked with %T(%v); a deliberate refusal panics with a string", refusal, refusal)
				}
			}()
			c.construct()
		})
	}
}
