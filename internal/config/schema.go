package config

import (
	"reflect"
	"strings"
	"sync"
)

// keySpec is the shape Config accepts, as a trie of key names. A child named
// "*" stands for a map, where any key is the user's to choose — a provider id,
// an MCP server name, a bash glob. A node with no children is a value, and
// nothing may sit below it.
type keySpec struct {
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

// leaf reports whether this node holds a value rather than an object.
func (k *keySpec) leaf() bool { return k == nil || len(k.children) == 0 }

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
		spec := &keySpec{children: map[string]*keySpec{}}
		for i := range t.NumField() {
			f := t.Field(i)
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
		return &keySpec{children: map[string]*keySpec{wildcardKey: specOf(t.Elem())}}

	default:
		// Scalars and slices are values. A slice is deliberately not walked:
		// arrays replace rather than merge, so nothing addresses inside one.
		return &keySpec{}
	}
}

// unknownKeys returns the key paths in t that Config has no home for, sorted.
//
// The decoder ignores an unrecognised key in silence, which turns one typo
// into a setting that reads as applied and is not. Reporting them is a warning
// rather than an error so a config written for a newer rasp still starts.
func unknownKeys(t tree) []string {
	var found []string
	var walk func(node tree, spec *keySpec, path []string)
	walk = func(node tree, spec *keySpec, path []string) {
		for _, key := range sortedKeys(node) {
			sub := spec.child(key)
			if sub == nil {
				found = append(found, joinPath(childPath(path, key)))
				continue
			}
			// A value where an object belongs is a type error, and the decoder
			// reports it with more detail than this walk could.
			if child, ok := node[key].(tree); ok && !sub.leaf() {
				walk(child, sub, childPath(path, key))
			}
		}
	}
	walk(t, configSpec(), nil)
	return found
}
