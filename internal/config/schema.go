package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// kind is the sort of JSON value a key holds, named the way an error says it.
type kind int

const (
	kindAny kind = iota // anything goes; nothing in Config asks for this today
	kindObject
	kindString
	kindNumber
	kindInteger
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
	case kindInteger:
		return "a whole number"
	case kindBool:
		return "a boolean"
	case kindArray:
		return "an array"
	}
	return "a value"
}

// kindOf names the sort of a decoded JSON value. The second result is false for
// null, which JSON allows anywhere and the decoder reads as a zero value.
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

// keySpec is the shape Config accepts. A child named "*" stands for a map, whose
// keys are the user's to choose.
type keySpec struct {
	kind     kind
	children map[string]*keySpec
}

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
	spec := specOf(reflect.TypeFor[Config]())

	// `modes` is a Go map, so reflection reads its keys as the user's to invent.
	// They are the four mode names, and left as a wildcard `modes.manaul` would
	// load clean and never be consulted — a permission override that reads as
	// applied and is not. Narrowing routes the typo through the unknown-key
	// warning that already exists.
	if modes := spec.children["modes"]; modes != nil {
		perMode := modes.children[wildcardKey]
		modes.children = make(map[string]*keySpec, len(modeNames))
		for _, name := range modeNames {
			modes.children[name] = perMode
		}
	}
	return spec
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
		// Not walked: arrays replace rather than merge.
		return &keySpec{kind: kindArray}

	case reflect.String:
		return &keySpec{kind: kindString}

	case reflect.Bool:
		return &keySpec{kind: kindBool}

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &keySpec{kind: kindInteger}

	case reflect.Float32, reflect.Float64:
		return &keySpec{kind: kindNumber}

	default:
		return &keySpec{kind: kindAny}
	}
}

type mismatch struct {
	key  string
	want kind

	// got names what arrived: a kind ("a string"), or the value itself where the
	// kind is not the problem — `16.5` where a whole number belongs is a number,
	// and saying so would explain nothing.
	got string
}

// fitsInteger reports whether a decoded JSON number fits an int. A stray decimal
// point and a value past the 64-bit range both fail here, and both would reach
// encoding/json otherwise, whose error names a Go field and never a file.
func fitsInteger(val any) bool {
	num, ok := val.(json.Number)
	if !ok {
		return false
	}
	_, err := num.Int64()
	return err == nil
}

// inspect reports what will not survive the decode, keyed by key path so the
// caller can name the file each came from. An unknown key is a warning, because
// refusing to start would mean a config written for a newer rasp could not run
// against an older one; a value of the wrong sort is an error.
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
				// A key with no opinion about its contents. Nulls are gone by
				// now, so `known` is defensive.
			case sub.kind == kindInteger:
				if got != kindNumber {
					mismatched = append(mismatched, mismatch{joinPath(here), sub.kind, got.String()})
					continue
				}
				if !fitsInteger(node[key]) {
					mismatched = append(mismatched, mismatch{
						joinPath(here), sub.kind, fmt.Sprint(node[key]),
					})
					continue
				}
			case got != sub.kind:
				mismatched = append(mismatched, mismatch{joinPath(here), sub.kind, got.String()})
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
