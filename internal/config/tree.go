package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/tidwall/jsonc"
)

// A tree is one layer's contribution, decoded but not yet typed. Every layer
// becomes one, so the precedence chain is a single rule rather than five special
// cases. A tree never holds a null.
type tree = map[string]any

// jsonNumber matches how decoding reads numbers — json.Number rather than
// float64, so a large integer comes out the way it went in.
func jsonNumber(n int) json.Number {
	return json.Number(fmt.Sprint(n))
}

// decodeJSONC parses JSONC — JSON with `//` and `/* */` comments — into a tree.
// Comments are replaced by spaces rather than removed, so every byte offset still
// points at the same place in the file the user wrote. Stripping is string-aware:
// `//` in a URL is not a comment.
func decodeJSONC(src []byte, path string) (tree, error) {
	stripped := jsonc.ToJSON(src)

	// A file holding only whitespace and comments overrides nothing, the same as
	// an absent one.
	if len(bytes.TrimSpace(stripped)) == 0 {
		return tree{}, nil
	}

	dec := json.NewDecoder(bytes.NewReader(stripped))
	dec.UseNumber()

	var t tree
	if err := dec.Decode(&t); err != nil {
		return nil, &ParseError{Path: path, Offset: syntaxOffset(err), Err: err, source: src}
	}
	// Only io.EOF means "nothing follows". Testing for a nil error instead would
	// catch a well-formed second document and wave through the far likelier
	// hand-edit — a stray brace, a comma splicing two objects — because a
	// *malformed* tail makes Token fail, and failing to read the rest of the file
	// is not evidence that there is no rest of the file.
	switch _, err := dec.Token(); {
	case errors.Is(err, io.EOF):
	case err == nil:
		return nil, &ParseError{
			Path:   path,
			Offset: dec.InputOffset(),
			Err:    errors.New("unexpected content after the top-level object"),
			source: src,
		}
	default:
		return nil, &ParseError{Path: path, Offset: syntaxOffset(err), Err: err, source: src}
	}
	if t == nil {
		return nil, &ParseError{
			Path:   path,
			Offset: -1,
			Err:    errors.New("want a JSON object, got null"),
			source: src,
		}
	}
	dropNulls(t)
	return t, nil
}

// dropNulls removes every null-valued key: a null is "not set", the same as
// leaving the key out. The alternative is `"mode": null` quietly erasing a
// built-in default while the neighbouring typo `"manaul"` refuses to start.
//
// Here rather than during the merge, which does not visit every value: where one
// side has no object to descend into, a whole subtree is assigned at once.
func dropNulls(t tree) {
	for key, val := range t {
		switch v := val.(type) {
		case nil:
			delete(t, key)
		case tree:
			// An empty object stays: `{"api_key": null}` means what `{}` means.
			dropNulls(v)
		}
	}
}

func syntaxOffset(err error) int64 {
	if syn, ok := errors.AsType[*json.SyntaxError](err); ok {
		return syn.Offset
	}
	if typ, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return typ.Offset
	}
	return -1
}

// ParseError is a config file that could not be read as JSONC, with the line and
// column the offset-preserving comment strip makes possible.
type ParseError struct {
	Path   string
	Offset int64
	Err    error

	source []byte
}

func (e *ParseError) Error() string {
	if e.Offset < 0 || e.Offset > int64(len(e.source)) {
		return fmt.Sprintf("%s: %v", e.Path, e.Err)
	}
	line, col := lineCol(e.source, int(e.Offset))
	return fmt.Sprintf("%s:%d:%d: %v", e.Path, line, col, e.Err)
}

func (e *ParseError) Unwrap() error { return e.Err }

func lineCol(src []byte, offset int) (line, col int) {
	offset = min(offset, len(src))
	line = 1 + bytes.Count(src[:offset], []byte("\n"))
	lineStart := bytes.LastIndexByte(src[:offset], '\n') + 1
	return line, offset - lineStart + 1
}

// merge deep-merges src onto dst and records where each resulting value came
// from, under prefix. Objects merge per key; scalars and arrays replace, so a
// user narrowing `models` to one entry means one entry.
func merge(dst, src tree, origin Origin, prefix []string, origins Origins) {
	for _, key := range sortedKeys(src) {
		path := childPath(prefix, key)
		srcVal := src[key]

		dstSub, dstIsObj := dst[key].(tree)
		srcSub, srcIsObj := srcVal.(tree)
		if dstIsObj && srcIsObj {
			merge(dstSub, srcSub, origin, path, origins)

			// An object carries an origin of its own only while it is empty,
			// having no leaf to hang one on. Once something lands inside, the
			// entry here would describe a value nothing prints — the row
			// `rasp config check` would draw as `providers  null`.
			if len(dstSub) > 0 {
				delete(origins, joinPath(path))
			} else {
				origins[joinPath(path)] = origin
			}
			continue
		}

		// Anything under this key is gone, so its origins are too: a scalar
		// replacing an object would otherwise leave entries pointing at nothing.
		forgetSubtree(origins, joinPath(path))

		dst[key] = srcVal
		recordOrigins(srcVal, origin, path, origins)
	}
}

func recordOrigins(val any, origin Origin, path []string, origins Origins) {
	sub, isObj := val.(tree)
	if !isObj || len(sub) == 0 {
		// An empty object is a value in its own right, with no leaf to hang the
		// origin on.
		origins[joinPath(path)] = origin
		return
	}
	for _, key := range sortedKeys(sub) {
		recordOrigins(sub[key], origin, childPath(path, key), origins)
	}
}

// childPath returns prefix followed by key, in a slice of its own: appending to
// prefix would let one branch of the walk write into another's through the
// shared backing array.
func childPath(prefix []string, key string) []string {
	return append(slices.Clip(prefix), key)
}

func forgetSubtree(origins Origins, path string) {
	delete(origins, path)
	for p := range origins {
		if strings.HasPrefix(p, path+".") {
			delete(origins, p)
		}
	}
}

// setPath writes val at a key path, creating objects along the way: how the
// environment and flag layers become trees shaped like the file layers.
func setPath(t tree, val any, segments ...string) {
	for _, seg := range segments[:len(segments)-1] {
		sub, ok := t[seg].(tree)
		if !ok {
			sub = tree{}
			t[seg] = sub
		}
		t = sub
	}
	t[segments[len(segments)-1]] = val
}

// discard removes a key path from the merged tree and its origin table together:
// an origin outliving its value points at a setting nobody can read.
func discard(t tree, origins Origins, segments ...string) {
	deletePath(t, segments...)
	forgetSubtree(origins, joinPath(segments))
}

// deletePath removes a key path along with any object it leaves empty.
func deletePath(t tree, segments ...string) {
	switch len(segments) {
	case 0:
		return
	case 1:
		delete(t, segments[0])
		return
	}
	sub, ok := t[segments[0]].(tree)
	if !ok {
		return
	}
	deletePath(sub, segments[1:]...)
	if len(sub) == 0 {
		delete(t, segments[0])
	}
}

func lookupPath(t tree, segments ...string) (any, bool) {
	var cur any = t
	for _, seg := range segments {
		sub, ok := cur.(tree)
		if !ok {
			return nil, false
		}
		if cur, ok = sub[seg]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// sortedKeys keeps every walk over a tree deterministic: Go randomizes map
// iteration, and a warning list that reorders between runs is one nobody can
// diff.
func sortedKeys(t tree) []string {
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func decodeInto(t tree, cfg *Config) error {
	raw, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("re-encoding the merged configuration: %w", err)
	}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return fmt.Errorf("reading the merged configuration: %w", err)
	}
	return nil
}
