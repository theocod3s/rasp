package tool

import (
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/theocod3s/rasp/internal/llm"
)

// Registry holds the tools the model may call: built-ins, fixed for the process,
// plus a group per dynamic source ("mcp:github") that comes and goes as servers
// connect, crash or are reconfigured. It is written by the MCP manager and read
// only through Snapshot (design §3.3). The zero value is an empty registry.
type Registry struct {
	mu      sync.RWMutex
	builtin []Tool
	dynamic map[string][]Tool
	version uint64
	current *Set
}

// NewRegistry returns a registry holding the given built-in tools.
func NewRegistry(builtin []Tool) *Registry {
	r := &Registry{builtin: slices.Clone(builtin)}
	r.rebuild()
	return r
}

// Replace installs source's tools, discarding whatever that source had before.
// Registering no tools is not the same as Remove: the source stays known and
// empty, which is what a server that connected and offers nothing looks like.
func (r *Registry) Replace(source string, tools []Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.dynamic == nil {
		r.dynamic = make(map[string][]Tool)
	}
	r.dynamic[source] = slices.Clone(tools)
	r.version++
	r.rebuild()
}

// Remove drops a source and everything it registered; removing one that was
// never registered does nothing.
func (r *Registry) Remove(source string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, known := r.dynamic[source]; !known {
		return
	}
	delete(r.dynamic, source)
	r.version++
	r.rebuild()
}

// Snapshot returns an immutable view. The agent takes exactly one per turn and
// holds it for every step, so the loop reads with no lock and no risk of the list
// shifting under it, and a server that crashes mid-turn keeps its tools callable
// to the end of that turn — a call there fails as an ordinary tool error the
// model can work around. A server connecting mid-session appears in the next
// turn's snapshot, costing one cache miss rather than one per request.
func (r *Registry) Snapshot() *Set {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.current == nil {
		return &emptySet
	}
	return r.current
}

// rebuild composes a new Set from the current contents. Callers hold r.mu for
// writing, the constructor excepted. The Set is built fresh and never patched in
// place: snapshots already handed out point at the previous one, and a turn
// holds its snapshot to the end.
func (r *Registry) rebuild() {
	all := make([]Tool, 0, len(r.builtin))
	all = append(all, r.builtin...)
	for _, source := range slices.Sorted(maps.Keys(r.dynamic)) {
		all = append(all, r.dynamic[source]...)
	}
	slices.SortStableFunc(all, func(a, b Tool) int { return strings.Compare(a.Name(), b.Name()) })

	set := &Set{tools: make([]Tool, 0, len(all)), byName: make(map[string]Tool, len(all)), version: r.version}
	for _, t := range all {
		// A name resolves to exactly one tool, so two sources offering the same one
		// cannot both be kept: the list and byName would disagree, and the model
		// would be handed a name it cannot address. The sort is stable over
		// built-ins first and then sources in name order, so which duplicate
		// survives is deterministic rather than map-iteration luck.
		if _, taken := set.byName[t.Name()]; taken {
			continue
		}
		set.byName[t.Name()] = t
		set.tools = append(set.tools, t)
	}
	r.current = set
}

// Set is one snapshot of the registry: read-only once Snapshot has returned it,
// and therefore safe to read from any number of goroutines.
type Set struct {
	tools   []Tool
	byName  map[string]Tool
	version uint64
}

var emptySet Set

// Specs is the tool list as a request carries it, sorted by name. The order is
// load-bearing: the list sits inside the cached prompt prefix, so an unstable one
// destroys the cache on every request — invisible until cache_read_input_tokens
// is found pinned at zero (design §3.3, §11). MCP revision 2026-07-28 recommends
// servers list deterministically for that reason; sorting here does not rely on it.
func (s *Set) Specs() []llm.ToolSpec {
	specs := make([]llm.ToolSpec, len(s.tools))
	for i, t := range s.tools {
		specs[i] = llm.ToolSpec{Name: t.Name(), Description: t.Description(), Schema: t.Schema()}
	}
	return specs
}

// Get finds the tool a call names.
func (s *Set) Get(name string) (Tool, bool) {
	t, ok := s.byName[name]
	return t, ok
}

// Version identifies the registry generation this Set came from. It advances on
// every mutation, not only on those that change the resulting list.
func (s *Set) Version() uint64 { return s.version }
