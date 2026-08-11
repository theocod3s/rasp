package config

import (
	"reflect"
	"strings"
	"sync"
)

// kind is the sort of JSON value a key holds, named the way an error message
// wants to say it.
type kind int

const (
	kindAny kind = iota // anything goes; nothing in Config asks for this today
	kindObject
	kindString
	kindNumber
	kindBool
	kindArray
)

func (k kind) String() string {
	switch k {
	case kindObject:
		return "an object"
	case kindString:
		return "a string"
	case kindNumber:
		return "a number"
	case kindBool:
		return "a boolean"
	case kindArray:
		return "an array"
	}
	return "a value"
}

// kindOf names the sort of a decoded JSON value. The second result is false
// for null, which JSON allows anywhere and the decoder reads as a zero value.
func kindOf(val any) (kind, bool) {
	switch val.(type) {
	case nil:
		return kindAny, false
	case string:
		return kindString, true
	case bool:
		return kindBool, true
	case []any:
		return kindArray, true
	case tree:
		return kindObject, true
	default:
		// json.Number, and float64 if a decoder is ever built without
		// UseNumber.
		return kindNumber, true
	}
}

// keySpec is the shape Config accepts: what a key holds, and what may sit
// below it. A child named "*" stands for a map, where the keys are the user's
// to choose — a provider id, an MCP server name, a bash glob.
type keySpec struct {
	kind     kind
	children map[string]*keySpec
}

// child resolves one key name, falling back to the wildcard.
func (k *keySpec) child(name string) *keySpec {
	if k == nil {
		return nil
	}
	if c, ok := k.children[name]; ok {
		return c
	}
	return k.children[wildcardKey]
}

const wildcardKey = "*"

// configSpec is derived from Config's own struct tags, so it cannot drift from
// what the decoder will actually accept.
var configSpec = sync.OnceValue(func() *keySpec {
	return specOf(reflect.TypeFor[Config]())
})

func specOf(t reflect.Type) *keySpec {
	switch t.Kind() {
	case reflect.Pointer:
		return specOf(t.Elem())

	case reflect.Struct:
		spec := &keySpec{kind: kindObject, children: map[string]*keySpec{}}
		for f := range t.Fields() {
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "" || name == "-" {
				name = f.Name
			}
			spec.children[name] = specOf(f.Type)
		}
		return spec

	case reflect.Map:
		return &keySpec{
			kind:     kindObject,
			children: map[string]*keySpec{wildcardKey: specOf(t.Elem())},
		}

	case reflect.Slice, reflect.Array:
		// Not walked: arrays replace rather than merge, so nothing addresses a
		// key inside one.
		return &keySpec{kind: kindArray}

	case reflect.String:
		return &keySpec{kind: kindString}

	case reflect.Bool:
		return &keySpec{kind: kindBool}

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return &keySpec{kind: kindNumber}

	default:
		return &keySpec{kind: kindAny}
	}
}

// mismatch is a value Config cannot hold.
type mismatch struct {
	key  string
	want kind
	got  kind
}

// inspect walks the merged tree against Config's shape and reports what will
// not survive the decode: keys Config has no home for, and values of the wrong
// sort. Both are keyed by key path, so the caller can name the file each came
// from — the answer a reader wants, and the one encoding/json cannot give,
// since its errors are addressed to Go field names and lose map keys entirely.
//
// The two carry different severities. An unknown key is a warning: the decoder
// ignores it in silence, which turns one typo into a setting that reads as
// applied and is not, but refusing to start would mean a config written for a
// newer rasp could not run against an older one. A value of the wrong sort is
// an error, because there is nothing to fall back to.
func inspect(t tree) (unknown []string, mismatched []mismatch) {
	var walk func(node tree, spec *keySpec, path []string)
	walk = func(node tree, spec *keySpec, path []string) {
		for _, key := range sortedKeys(node) {
			sub, here := spec.child(key), childPath(path, key)
			if sub == nil {
				unknown = append(unknown, joinPath(here))
				continue
			}

			got, known := kindOf(node[key])
			switch {
			case !known || sub.kind == kindAny:
				// null, or a key with no opinion about its contents.
			case got != sub.kind:
				mismatched = append(mismatched, mismatch{joinPath(here), sub.kind, got})
				continue
			}

			if child, ok := node[key].(tree); ok && sub.kind == kindObject {
				walk(child, sub, here)
			}
		}
	}
	walk(t, configSpec(), nil)
	return unknown, mismatched
}
