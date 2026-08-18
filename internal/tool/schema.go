package tool

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

const (
	descTag = "desc"
	enumTag = "enum"
)

var (
	jsonMarshaler   = reflect.TypeFor[json.Marshaler]()
	jsonUnmarshaler = reflect.TypeFor[json.Unmarshaler]()
)

func schemaOf(t reflect.Type) map[string]any {
	root := t
	for root.Kind() == reflect.Pointer {
		root = root.Elem()
	}
	if root.Kind() != reflect.Struct {
		refuseType(t, "a tool's input has to be a struct, because a provider requires an object schema and only fields can name its properties")
	}
	return objectSchema(root, map[reflect.Type]bool{})
}

func objectSchema(t reflect.Type, open map[reflect.Type]bool) map[string]any {
	open[t] = true
	defer delete(open, t)

	properties := map[string]any{}
	var required []string
	collectFields(t, properties, &required, open)

	object := map[string]any{
		"type":       "object",
		"properties": properties,
		// additionalProperties tells the model not to invent keys, and it is what
		// OpenAI's strict function calling requires. Run deliberately does not
		// enforce it: an extra key costs nothing to ignore, and refusing the call
		// over one turns a request we could have served into a failed turn.
		"additionalProperties": false,
	}
	if len(required) > 0 {
		object["required"] = required
	}
	return object
}

func collectFields(t reflect.Type, properties map[string]any, required *[]string, open map[reflect.Type]bool) {
	for i := range t.NumField() {
		f := t.Field(i)
		name, opts, skip := parseJSONTag(f)
		if skip {
			continue
		}

		// An untagged embedded struct is flattened by encoding/json, so its fields
		// are properties of the outer object rather than one nested under the type
		// name. Tagged or not a struct, it falls through and is treated as ordinary.
		if embedded := embeddedStruct(f); embedded != nil && name == "" {
			if open[embedded] {
				refuseType(embedded, "the type embeds itself, so its properties would never finish")
			}
			open[embedded] = true
			collectFields(embedded, properties, required, open)
			delete(open, embedded)
			continue
		}

		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		if opts.has("string") {
			refuseType(f.Type, `the ",string" json option puts a number or bool on the wire quoted, which no derived schema would describe`)
		}
		if _, taken := properties[name]; taken {
			refuseType(t, fmt.Sprintf("two fields both claim the property %q; encoding/json resolves that by depth and a schema cannot", name))
		}

		properties[name] = fieldSchema(f, open)

		// omitempty and omitzero differ in what counts as absent, and neither
		// difference reaches the model: both say the field may be left out.
		if !opts.has("omitempty") && !opts.has("omitzero") {
			*required = append(*required, name)
		}
	}
}

func fieldSchema(f reflect.StructField, open map[reflect.Type]bool) map[string]any {
	node := typeSchema(f.Type, open)
	if desc := f.Tag.Get(descTag); desc != "" {
		node["description"] = desc
	}
	if raw, ok := f.Tag.Lookup(enumTag); ok {
		applyEnum(node, raw, f)
	}
	return node
}

// applyEnum constrains a field to a fixed set of strings. Go has no enum, so the
// values are listed on the field rather than discovered from the constants of its
// type — there is nothing to reflect over.
func applyEnum(node map[string]any, raw string, f reflect.StructField) {
	target := node
	if items, ok := node["items"].(map[string]any); ok {
		target = items
	}
	if target["type"] != "string" {
		refuseType(f.Type, "an enum tag lists string values, so it needs a string field or a slice of one")
	}

	values := []any{}
	for _, v := range strings.Split(raw, ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			refuseType(f.Type, "an enum tag with a blank value constrains nothing; list the values or drop the tag")
		}
		values = append(values, v)
	}
	target["enum"] = values
}

func typeSchema(t reflect.Type, open map[reflect.Type]bool) map[string]any {
	// A type that encodes itself has a wire shape its fields do not describe, and
	// time.Time is the one everybody reaches for. Reflecting over it anyway would
	// hand the model an object where a string belongs.
	if encodesItself(t) {
		refuseType(t, "the type marshals or unmarshals itself, so its fields are not its wire shape; take a string and convert")
	}

	switch t.Kind() {
	case reflect.Pointer:
		return typeSchema(t.Elem(), open)

	case reflect.Bool:
		return map[string]any{"type": "boolean"}

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}

	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}

	case reflect.String:
		return map[string]any{"type": "string"}

	case reflect.Interface:
		if t.NumMethod() != 0 {
			refuseType(t, "an interface with methods has no single JSON shape; name the concrete type")
		}
		return map[string]any{}

	case reflect.Slice:
		// encoding/json writes a byte slice as base64, not as an array of numbers.
		if t.Elem().Kind() == reflect.Uint8 && !encodesItself(t.Elem()) {
			return map[string]any{"type": "string", "contentEncoding": "base64"}
		}
		return map[string]any{"type": "array", "items": typeSchema(t.Elem(), open)}

	case reflect.Array:
		return map[string]any{"type": "array", "items": typeSchema(t.Elem(), open)}

	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			refuseType(t, "a JSON object's keys are strings; key the map by a string type")
		}
		return map[string]any{"type": "object", "additionalProperties": typeSchema(t.Elem(), open)}

	case reflect.Struct:
		if open[t] {
			refuseType(t, "the type contains itself, so its schema would never finish")
		}
		return objectSchema(t, open)
	}

	return refuseType(t, "no JSON type describes it")
}

func encodesItself(t reflect.Type) bool {
	p := reflect.PointerTo(t)
	return t.Implements(jsonMarshaler) || t.Implements(jsonUnmarshaler) ||
		p.Implements(jsonMarshaler) || p.Implements(jsonUnmarshaler)
}

func embeddedStruct(f reflect.StructField) reflect.Type {
	if !f.Anonymous {
		return nil
	}
	t := f.Type
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct || encodesItself(t) {
		return nil
	}
	return t
}

type tagOptions []string

func (o tagOptions) has(want string) bool {
	for _, opt := range o {
		if opt == want {
			return true
		}
	}
	return false
}

func parseJSONTag(f reflect.StructField) (name string, opts tagOptions, skip bool) {
	raw, ok := f.Tag.Lookup("json")
	if !ok {
		return "", nil, false
	}
	if raw == "-" {
		return "", nil, true
	}
	name, rest, _ := strings.Cut(raw, ",")
	if rest == "" {
		return name, nil, false
	}
	return name, strings.Split(rest, ","), false
}

// refuseType never returns; the map result exists so callers can `return` it.
func refuseType(t reflect.Type, why string) map[string]any {
	panic(fmt.Sprintf("tool: cannot derive a JSON Schema for %s: %s", t, why))
}
