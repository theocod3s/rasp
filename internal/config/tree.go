package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/tidwall/jsonc"
)

// A tree is one layer's contribution, decoded but not yet typed: JSON objects
// as map[string]any, everything else left alone.
//
// Every layer becomes a tree — files, environment, flags and the built-in
// defaults alike — because the merge has to treat all five identically for the
// precedence chain to be a single rule rather than five special cases. Typing
// happens once, on the merged result.
type tree = map[string]any

// jsonNumber makes a number that survives the round trip through the merged
// tree. Decoding uses json.Number rather than float64 so a large integer comes
// out the way it went in; values built in Go have to match.
func jsonNumber(n int) json.Number {
	return json.Number(fmt.Sprint(n))
}

// decodeJSONC parses JSONC — JSON with `//` and `/* */` comments — into a tree.
//
// Comments are replaced by spaces rather than removed, so every byte offset in
// the stripped text still points at the same place in the file the user wrote,
// and a syntax error can be reported at the line and column they would look at.
// Stripping is string-aware: the `//` in a URL is not a comment.
func decodeJSONC(src []byte, path string) (tree, error) {
	dec := json.NewDecoder(bytes.NewReader(jsonc.ToJSON(src)))
	dec.UseNumber()

	var t tree
	if err := dec.Decode(&t); err != nil {
		return nil, &ParseError{Path: path, Offset: syntaxOffset(err), Err: err, source: src}
	}
	// A file holding a second document is a mistake worth naming; the decoder
	// would otherwise stop after the first and say nothing.
	if _, err := dec.Token(); err == nil {
		return nil, &ParseError{
			Path:   path,
			Offset: dec.InputOffset(),
			Err:    errors.New("unexpected content after the top-level object"),
			source: src,
		}
	}
	if t == nil {
		return nil, &ParseError{
			Path:   path,
			Offset: -1,
			Err:    errors.New("want a JSON object, got null"),
			source: src,
		}
	}
	return t, nil
}

// syntaxOffset digs the byte offset out of an encoding/json error, or returns
// -1 when the error carries no position.
func syntaxOffset(err error) int64 {
	if syn, ok := errors.AsType[*json.SyntaxError](err); ok {
		return syn.Offset
	}
	if typ, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		return typ.Offset
	}
	return -1
}

// ParseError is a config file that could not be read as JSONC. It names the
// file and, where the decoder gave a position, the line and column — which the
// offset-preserving comment strip is what makes possible.
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

// lineCol converts a byte offset into a 1-based line and column.
func lineCol(src []byte, offset int) (line, col int) {
	offset = min(offset, len(src))
	line = 1 + bytes.Count(src[:offset], []byte("\n"))
	lineStart := bytes.LastIndexByte(src[:offset], '\n') + 1
	return line, offset - lineStart + 1
}

// merge deep-merges src onto dst and records where each resulting value came
// from, under prefix.
//
// Objects merge per key; everything else — scalars, arrays — replaces. That is
// what lets a project config override one provider's model without restating
// the providers table, and it is why an array is a value rather than something
// to concatenate: a user narrowing `models` to one entry means one entry.
func merge(dst, src tree, origin Origin, prefix []string, origins Origins) {
	for _, key := range sortedKeys(src) {
		path := childPath(prefix, key)
		srcVal := src[key]

		dstSub, dstIsObj := dst[key].(tree)
		srcSub, srcIsObj := srcVal.(tree)
		if dstIsObj && srcIsObj {
			merge(dstSub, srcSub, origin, path, origins)
			continue
		}

		// Anything that was under this key is gone, so its origins are too —
		// otherwise a scalar replacing an object leaves entries pointing at
		// values nobody can read any more.
		forgetSubtree(origins, joinPath(path))

		dst[key] = srcVal
		recordOrigins(srcVal, origin, path, origins)
	}
}

// recordOrigins attributes val, and everything inside it, to origin.
func recordOrigins(val any, origin Origin, path []string, origins Origins) {
	sub, isObj := val.(tree)
	if !isObj || len(sub) == 0 {
		// An empty object is a value in its own right: `"providers": {}` says
		// something, and has no leaf to hang the origin on.
		origins[joinPath(path)] = origin
		return
	}
	for _, key := range sortedKeys(sub) {
		recordOrigins(sub[key], origin, childPath(path, key), origins)
	}
}

// childPath returns prefix followed by key, in a slice of its own. Appending to
// prefix directly would let one branch of the walk write into another's path
// through the shared backing array.
func childPath(prefix []string, key string) []string {
	return append(slices.Clip(prefix), key)
}

// forgetSubtree drops the origin at path and every origin below it.
func forgetSubtree(origins Origins, path string) {
	delete(origins, path)
	for p := range origins {
		if strings.HasPrefix(p, path+".") {
			delete(origins, p)
		}
	}
}

// setPath writes val at a key path, creating objects along the way. It is how
// the environment and flag layers — flat lists of key/value pairs — become
// trees shaped like the file layers.
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

// discard removes a key path from the merged tree and its origin table
// together. The two describe one thing and have to be dropped as one: an
// origin outliving its value points at a setting nobody can read.
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

// lookupPath returns the value at a key path.
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

// sortedKeys keeps every walk over a tree deterministic. Go randomizes map
// iteration, and a warning list or an origin table that reorders between runs
// is one nobody can diff.
func sortedKeys(t tree) []string {
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// decodeInto converts the merged tree into the typed Config.
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
