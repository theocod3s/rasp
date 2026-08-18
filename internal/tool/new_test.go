package tool_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/tool"
)

type label struct {
	Key   string `json:"key"   desc:"Label name"`
	Value string `json:"value" desc:"Label value"`
}

type reason struct {
	Why string `json:"why" desc:"Why the call is being made"`
}

// driftIn exercises every shape the reflector claims to support, in one struct,
// so the round trip below covers them together rather than one test each.
type driftIn struct {
	reason
	Path    string         `json:"path"             desc:"Path to the file"`
	Level   severity       `json:"level"            desc:"How loudly to report" enum:"low,high"`
	Where   span           `json:"where"            desc:"The span to audit"`
	Ratio   float64        `json:"ratio"            desc:"Failure threshold"`
	Blob    []byte         `json:"blob"             desc:"Raw contents"`
	Skip    []severity     `json:"skip,omitempty"   desc:"Findings to drop" enum:"low,high"`
	Labels  []label        `json:"labels,omitempty" desc:"Extra labels"`
	Caps    map[string]int `json:"caps,omitempty"   desc:"Per-rule limits"`
	Fix     bool           `json:"fix,omitempty"    desc:"Rewrite the file in place"`
	Notes   *string        `json:"notes,omitempty"  desc:"Anything else"`
	Nested  *span          `json:"nested,omitempty" desc:"A pointer to a struct"`
	Trailer span           `json:"trailer,omitzero" desc:"An optional struct"`
}

// TestSchemaAndUnmarshalTargetCannotDrift is the claim the reflection constructor
// exists for. It never mentions a property name: the arguments are built out of
// the schema the tool published, sent through Run, and the struct that comes out
// the other side is compared back against that schema using encoding/json as the
// independent witness. Rename a property in the emitted schema and the field it
// fed stays zero; drop one and the key sets diverge; derive the schema from a
// different type than Run unmarshals into and both happen at once.
func TestSchemaAndUnmarshalTargetCannotDrift(t *testing.T) {
	var got driftIn
	var ran bool
	subject := tool.New("audit", "Audit a file", func(_ context.Context, in driftIn) (tool.Result, error) {
		got, ran = in, true
		return tool.Result{Content: "audited"}, nil
	})

	schema := subject.Schema()
	arguments := populate(t, schema, "")

	raw, err := json.Marshal(arguments)
	if err != nil {
		t.Fatalf("encoding the arguments built from the schema: %v", err)
	}
	res, err := subject.Run(context.Background(), raw)
	if err != nil {
		t.Fatalf("Run returned %v for arguments built from its own schema", err)
	}
	if res.IsError {
		t.Fatalf("arguments built from the tool's own schema were rejected: %s", res.Content)
	}
	if !ran {
		t.Fatal("the handler never ran, so nothing below examines a decoded value")
	}

	// Every property the model was told about reached a field. A property whose
	// name no longer matches its field unmarshals into nothing and shows up here.
	if leaves := assertPopulated(t, reflect.ValueOf(got), "driftIn"); leaves < reflect.TypeFor[driftIn]().NumField() {
		t.Fatalf("the walk examined %d leaves for a %d-field input, so most of it went unchecked", leaves, reflect.TypeFor[driftIn]().NumField())
	}

	// And nothing else did. encoding/json's own view of the decoded value has to
	// have exactly the keys the schema published, at every level.
	if compared := assertSameKeys(t, schema, reencode(t, got), ""); compared <= 1 {
		t.Fatalf("the key comparison visited %d nodes, so it stopped at the root", compared)
	}
}

// TestRequiredIsWhatEncodingJSONAlwaysSends pins optionality against the same
// witness: marshalling the zero input emits exactly the fields that cannot be
// left out, which is what `required` has to name.
func TestRequiredIsWhatEncodingJSONAlwaysSends(t *testing.T) {
	schema := tool.New("audit", "Audit a file", noop[driftIn]).Schema()

	want := slices.Sorted(maps.Keys(reencode(t, driftIn{})))
	if len(want) == 0 {
		t.Fatal("the zero input marshalled to no fields at all, so this compares nothing")
	}

	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("the schema carries no required list (%T), so every field reads as optional", schema["required"])
	}
	got := slices.Sorted(slices.Values(required))

	if !slices.Equal(got, want) {
		t.Errorf("required is %v; encoding/json always sends %v", got, want)
	}
	for _, name := range want {
		if strings.HasSuffix(name, "omitempty") {
			t.Fatalf("%q reads like an unparsed tag, so the witness is not what it claims", name)
		}
	}
}

func TestArgumentsThatDoNotFitComeBackAsAnErrorResult(t *testing.T) {
	subject := tool.New("audit", "Audit a file", func(context.Context, driftIn) (tool.Result, error) {
		t.Error("the handler ran on arguments that do not parse")
		return tool.Result{}, nil
	})

	res, err := subject.Run(context.Background(), json.RawMessage(`{"path": 7}`))
	if err != nil {
		t.Fatalf("bad arguments returned the Go error %v; a call the model can retry is an error result, and a Go error ends the turn", err)
	}
	if !res.IsError {
		t.Fatal("arguments that do not parse came back as a success")
	}
	if !strings.Contains(res.Content, "audit") {
		t.Errorf("the error result does not name the tool: %q", res.Content)
	}
}

func TestNameAndDescriptionAreCarriedThrough(t *testing.T) {
	subject := tool.New("audit", "Audit a file", noop[driftIn])
	if subject.Name() != "audit" {
		t.Errorf("Name is %q", subject.Name())
	}
	if subject.Description() != "Audit a file" {
		t.Errorf("Description is %q", subject.Description())
	}
	if _, ok := subject.(tool.Sequential); ok {
		t.Error("a reflected tool satisfied tool.Sequential; parallel is the default and a built-in has to opt in for itself")
	}
}

// populate builds a JSON value that conforms to one schema node. Every leaf it
// produces is non-zero, which is what lets assertPopulated tell "the field was
// filled" from "the name did not match".
func populate(t *testing.T, node map[string]any, path string) any {
	t.Helper()

	if enum, ok := node["enum"].([]any); ok {
		if len(enum) == 0 {
			t.Fatalf("%s: an empty enum leaves no value to send", path)
		}
		return enum[0]
	}

	switch node["type"] {
	case "string":
		if node["contentEncoding"] == "base64" {
			return base64.StdEncoding.EncodeToString([]byte("contents"))
		}
		return "value of " + path
	case "boolean":
		return true
	case "integer":
		return 7
	case "number":
		return 1.5
	case "array":
		items, ok := node["items"].(map[string]any)
		if !ok {
			t.Fatalf("%s: an array with no items schema describes nothing", path)
		}
		return []any{populate(t, items, path+"[0]")}
	case "object":
		if properties, ok := node["properties"].(map[string]any); ok {
			if len(properties) == 0 {
				t.Fatalf("%s: an object with no properties would send an empty value that every check below accepts", path)
			}
			out := map[string]any{}
			for name, sub := range properties {
				out[name] = populate(t, node1(t, sub, path+"."+name), path+"."+name)
			}
			return out
		}
		if values, ok := node["additionalProperties"].(map[string]any); ok {
			return map[string]any{"a-key": populate(t, values, path+"[a-key]")}
		}
		t.Fatalf("%s: an object with neither properties nor a value schema: %v", path, node)
	}

	t.Fatalf("%s: no type in schema node %v", path, node)
	return nil
}

func node1(t *testing.T, v any, path string) map[string]any {
	t.Helper()
	node, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s: schema node is %T, not an object", path, v)
	}
	return node
}

// assertPopulated fails on any leaf of a decoded input still holding its zero
// value, which is what a property name that no longer matches its field leaves
// behind. It returns the number of leaves it examined, because a walk that
// reaches none of them is the quietest pass there is.
func assertPopulated(t *testing.T, v reflect.Value, path string) int {
	t.Helper()

	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			t.Errorf("%s is nil", path)
			return 1
		}
		return assertPopulated(t, v.Elem(), path)
	case reflect.Struct:
		examined := 0
		for i := range v.NumField() {
			f := v.Type().Field(i)
			if !f.IsExported() && !f.Anonymous {
				continue
			}
			examined += assertPopulated(t, v.Field(i), path+"."+f.Name)
		}
		return examined
	case reflect.Slice, reflect.Map:
		if v.Len() == 0 {
			t.Errorf("%s is empty", path)
			return 1
		}
		examined := 0
		if v.Kind() == reflect.Slice {
			for i := range v.Len() {
				examined += assertPopulated(t, v.Index(i), fmt.Sprintf("%s[%d]", path, i))
			}
			return examined
		}
		for _, key := range v.MapKeys() {
			examined += assertPopulated(t, v.MapIndex(key), fmt.Sprintf("%s[%v]", path, key))
		}
		return examined
	default:
		if v.IsZero() {
			t.Errorf("%s is the zero value, so nothing was decoded into it", path)
		}
		return 1
	}
}

// assertSameKeys walks a schema node beside a JSON value re-encoded from the
// decoded input and fails where the two disagree about which keys exist. Like
// assertPopulated it returns how many nodes it compared, so a walk that stops at
// the root cannot report success.
func assertSameKeys(t *testing.T, node map[string]any, value any, path string) int {
	t.Helper()

	if properties, ok := node["properties"].(map[string]any); ok {
		object, ok := value.(map[string]any)
		if !ok {
			t.Errorf("%s: the schema says object, encoding/json wrote %T", path, value)
			return 1
		}
		want, got := slices.Sorted(maps.Keys(properties)), slices.Sorted(maps.Keys(object))
		if !slices.Equal(want, got) {
			t.Errorf("%s: schema properties %v, encoding/json wrote %v", path, want, got)
			return 1
		}
		compared := 1
		for name, sub := range properties {
			compared += assertSameKeys(t, node1(t, sub, path+"."+name), object[name], path+"."+name)
		}
		return compared
	}

	if values, ok := node["additionalProperties"].(map[string]any); ok {
		object, ok := value.(map[string]any)
		if !ok {
			t.Errorf("%s: the schema says object, encoding/json wrote %T", path, value)
			return 1
		}
		compared := 1
		for name, v := range object {
			compared += assertSameKeys(t, values, v, path+"."+name)
		}
		return compared
	}

	if items, ok := node["items"].(map[string]any); ok {
		array, ok := value.([]any)
		if !ok {
			t.Errorf("%s: the schema says array, encoding/json wrote %T", path, value)
			return 1
		}
		compared := 1
		for i, v := range array {
			compared += assertSameKeys(t, items, v, fmt.Sprintf("%s[%d]", path, i))
		}
		return compared
	}

	return 1
}

// reencode asks encoding/json what fields a value has. It is the witness the two
// tests above rest on: it reads the struct tags independently of the reflector.
func reencode(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("re-encoding a decoded input: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding a re-encoded input: %v", err)
	}
	return out
}
