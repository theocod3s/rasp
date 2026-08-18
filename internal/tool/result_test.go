package tool_test

import (
	"reflect"
	"testing"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tool"
)

// audience says, per field of Result, whether the model sees it. Nothing turns a
// Result into a tool_result block yet — the step loop will — so until then this
// map and the two tests over it are what hold the rule that Details never
// reaches the model: a field cannot join Result without someone classifying it,
// and a classified field cannot land on the wire unnoticed.
var audience = map[string]bool{
	"Content": true,
	"IsError": true,
	"Title":   false,
	"Details": false,
}

func TestEveryResultFieldHasAnAudience(t *testing.T) {
	result := reflect.TypeFor[tool.Result]()

	present := make(map[string]bool, result.NumField())
	for i := range result.NumField() {
		name := result.Field(i).Name
		present[name] = true
		if _, ok := audience[name]; !ok {
			t.Errorf("Result grew field %s: decide whether the model sees it, and say so here. Content and IsError become the tool_result block; everything else is the UI's", name)
		}
	}

	for name := range audience {
		if !present[name] {
			t.Errorf("this test classifies %s, which Result no longer has: classify whatever replaced it rather than dropping the entry", name)
		}
	}
}

// TestOnlyModelFieldsFitTheWireBlock checks the reason Details cannot leak: the
// only thing built out of a Result is an llm.Block of type tool_result, and that
// block has nowhere for a UI payload to sit. It reads llm from tool's tests
// because that is where the rule becomes false if it ever does.
func TestOnlyModelFieldsFitTheWireBlock(t *testing.T) {
	block := reflect.TypeFor[llm.Block]()

	for name, modelSees := range audience {
		_, onWire := block.FieldByName(name)
		switch {
		case modelSees && !onWire:
			t.Errorf("Result.%s is meant for the model but llm.Block has no such field, so the loop has nowhere to copy it", name)
		case !modelSees && onWire:
			t.Errorf("llm.Block now has a field named %s: a tool_result block with room for a UI payload is exactly how Details reaches the model", name)
		}
	}
}

func TestDetailsTakesAnyPayload(t *testing.T) {
	field, ok := reflect.TypeFor[tool.Result]().FieldByName("Details")
	if !ok {
		t.Fatal("Result has no Details field")
	}
	if field.Type.Kind() != reflect.Interface || field.Type.NumMethod() != 0 {
		t.Errorf("Details is %s: an MCP tool's structured output is arbitrary decoded JSON, and any narrower type forces the MCP adapter to wrap what it is required to pass through untouched", field.Type)
	}
}
