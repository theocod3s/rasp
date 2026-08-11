package config

import (
	"cmp"
	"slices"
	"strings"
)

// Layer is one rung of design §10's precedence chain. Higher wins.
type Layer int

const (
	LayerDefault Layer = iota
	LayerGlobal
	LayerProject
	LayerEnv
	LayerFlag
)

// layers is the chain in application order, lowest first. Load walks it in
// this order, so the slice is the precedence rule rather than a description of
// one written somewhere else.
var layers = []Layer{LayerDefault, LayerGlobal, LayerProject, LayerEnv, LayerFlag}

// String names the layer as `rasp config check` prints it.
func (l Layer) String() string {
	switch l {
	case LayerDefault:
		return "default"
	case LayerGlobal:
		return "global"
	case LayerProject:
		return "project"
	case LayerEnv:
		return "env"
	case LayerFlag:
		return "flag"
	}
	return "unknown"
}

// Origin says where one resolved value came from: which layer, and which file,
// environment variable or flag within it.
type Origin struct {
	Layer Layer

	// Detail identifies the source inside the layer — a file path, a variable
	// name, a flag name. Empty for the built-in defaults, which have no
	// location to name.
	Detail string
}

// String renders an origin for a human: "project .rasp/config.json",
// "env RASP_MODEL", "flag --mode", "built-in default".
func (o Origin) String() string {
	switch {
	case o.Layer == LayerDefault:
		return "built-in default"
	case o.Layer == LayerFlag:
		return "flag --" + o.Detail
	case o.Detail == "":
		return o.Layer.String()
	}
	return o.Layer.String() + " " + o.Detail
}

// Origins records the winning origin of every resolved value, keyed by the
// value's key path.
//
// A key path is the JSON keys from the root joined with ".", so `model` and
// `providers.anthropic.api_key`. A literal "." inside a key — legal in a bash
// pattern like `rm *.go` — is escaped as `\.`, so two different locations can
// never collapse onto one entry.
type Origins map[string]Origin

// At returns the origin of the value at a key path.
//
// When the key holds an object, the origins sit on its leaves rather than on
// the key — which is exactly the case a caller asking about a *malformed*
// value runs into, since "an object where a string belongs" has no leaf of its
// own. So a miss falls back to the first origin recorded beneath the key. The
// leaves under one wrongly-shaped value normally share an origin; where a
// later layer has overridden part of it they do not, and taking the first in
// sorted order at least makes the answer the same every run.
func (o Origins) At(key string) (Origin, bool) {
	if origin, ok := o[key]; ok {
		return origin, true
	}
	for _, path := range o.Paths() {
		if strings.HasPrefix(path, key+".") {
			return o[path], true
		}
	}
	return Origin{}, false
}

// Paths returns every recorded key path in sorted order.
func (o Origins) Paths() []string {
	paths := make([]string, 0, len(o))
	for p := range o {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	return paths
}

// pathEscaper escapes a separator that appears inside a key. A bash pattern
// like `rm *.go` is a legal key, and joining it unescaped would put it at a
// path some other setting could also occupy.
var pathEscaper = strings.NewReplacer(`\`, `\\`, `.`, `\.`)

// joinPath renders a key path from its segments.
func joinPath(segments []string) string {
	escaped := make([]string, len(segments))
	for i, s := range segments {
		escaped[i] = pathEscaper.Replace(s)
	}
	return strings.Join(escaped, ".")
}

// splitPath is joinPath's inverse: it recovers the segments of a key path,
// honouring the escapes joinPath wrote.
func splitPath(key string) []string {
	var (
		segments []string
		cur      strings.Builder
		escaped  bool
	)
	for _, r := range key {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		case r == '.':
			segments = append(segments, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(segments, cur.String())
}

// Warning is a configuration problem that is worth saying out loud but is not
// worth refusing to start over.
type Warning struct {
	Origin Origin

	// Key is the key path the warning is about, or "" when it concerns the
	// source as a whole.
	Key string

	Message string
}

// String renders a warning as one line.
func (w Warning) String() string {
	if w.Key == "" {
		return w.Message + " (" + w.Origin.String() + ")"
	}
	return w.Key + ": " + w.Message + " (" + w.Origin.String() + ")"
}

// Source reports one place Load looked, whether or not it found anything. The
// misses matter as much as the hits: "which file is it not reading" is the
// question `rasp config check` exists to answer.
type Source struct {
	Origin Origin

	// Loaded is whether this source contributed any value.
	Loaded bool

	// Note explains a source that contributed nothing — "not found",
	// "no variables set". Empty when Loaded.
	Note string
}

// sortWarnings orders warnings by layer, then key, then message, so repeated
// runs print the same thing.
func sortWarnings(ws []Warning) {
	slices.SortStableFunc(ws, func(a, b Warning) int {
		return cmp.Or(
			cmp.Compare(a.Origin.Layer, b.Origin.Layer),
			cmp.Compare(a.Origin.Detail, b.Origin.Detail),
			cmp.Compare(a.Key, b.Key),
			cmp.Compare(a.Message, b.Message),
		)
	})
}
