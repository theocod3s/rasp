package tool_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/theocod3s/rasp/internal/tool"
)

// registered is a Tool that is nothing but a name and the source that supplied
// it, which is all the registry sorts, keys and de-duplicates on. The source
// travels in the description so a test can tell two tools of the same name apart.
type registered struct {
	name   string
	source string
}

func (t registered) Name() string           { return t.name }
func (t registered) Description() string    { return "from " + t.source }
func (t registered) Schema() map[string]any { return map[string]any{"type": "object", "title": t.name} }

func (t registered) Run(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: t.source + "/" + t.name}, nil
}

func from(source string, names ...string) []tool.Tool {
	tools := make([]tool.Tool, len(names))
	for i, name := range names {
		tools[i] = registered{name: name, source: source}
	}
	return tools
}

func names(t *testing.T, s *tool.Set) []string {
	t.Helper()
	specs := s.Specs()
	if len(specs) == 0 {
		t.Fatal("this snapshot holds no tools, so every ordering and membership check below compares nothing")
	}
	out := make([]string, len(specs))
	for i, spec := range specs {
		out[i] = spec.Name
	}
	return out
}

func TestSpecsAreSortedWhateverOrderToolsArrivedIn(t *testing.T) {
	builtins := []string{"write", "read", "bash"}
	zed := []string{"zed_open", "apply_patch"}
	github := []string{"search_repositories", "create_issue"}

	reg := tool.NewRegistry(from("builtin", builtins...))
	reg.Replace("mcp:zed", from("mcp:zed", zed...))
	reg.Replace("mcp:github", from("mcp:github", github...))

	inserted := slices.Concat(builtins, zed, github)
	if slices.IsSorted(inserted) {
		t.Fatal("this fixture arrives in sorted order, so a registry that merely kept insertion order would pass it")
	}

	want := slices.Sorted(slices.Values(inserted))
	if got := names(t, reg.Snapshot()); !slices.Equal(got, want) {
		t.Errorf("Specs() is not sorted by name\n got: %v\nwant: %v", got, want)
	}
}

func TestSpecsCarryWhatTheRequestSends(t *testing.T) {
	reg := tool.NewRegistry(from("builtin", "read"))
	specs := reg.Snapshot().Specs()
	if len(specs) != 1 {
		t.Fatalf("got %d specs, want 1", len(specs))
	}

	spec, source := specs[0], registered{name: "read", source: "builtin"}
	if spec.Name != source.Name() {
		t.Errorf("spec name is %q, want %q", spec.Name, source.Name())
	}
	if spec.Description != source.Description() {
		t.Errorf("spec description is %q, want %q; the description is the prompt text the model chooses on", spec.Description, source.Description())
	}
	if title, _ := spec.Schema["title"].(string); title != "read" {
		t.Errorf("spec schema is %v, want the tool's own schema", spec.Schema)
	}
}

// A rebuild walks a map of sources, and Go randomizes map iteration, so a
// registry that leaked that order would produce a different list per rebuild —
// with no error, and no symptom beyond a cache hit rate of zero.
func TestOrderIsIdenticalAcrossRebuilds(t *testing.T) {
	reg := tool.NewRegistry(from("builtin", "read", "write"))
	for _, source := range []string{"mcp:a", "mcp:b", "mcp:c", "mcp:d", "mcp:e"} {
		reg.Replace(source, from(source, source+"_alpha", source+"_beta"))
	}

	first := names(t, reg.Snapshot())
	for i := range 50 {
		reg.Replace("mcp:c", from("mcp:c", "mcp:c_alpha", "mcp:c_beta"))
		if got := names(t, reg.Snapshot()); !slices.Equal(got, first) {
			t.Fatalf("rebuild %d produced a different order\n got: %v\nwant: %v", i, got, first)
		}
	}
}

func TestASnapshotDoesNotMoveWhenTheRegistryDoes(t *testing.T) {
	reg := tool.NewRegistry(from("builtin", "read", "write"))
	reg.Replace("mcp:github", from("mcp:github", "create_issue"))

	held := reg.Snapshot()
	before := names(t, held)

	reg.Replace("mcp:github", from("restarted", "create_issue", "list_issues"))
	reg.Replace("mcp:linear", from("mcp:linear", "list_issues_linear"))
	reg.Remove("mcp:github")

	if after := names(t, held); !slices.Equal(before, after) {
		t.Errorf("the held snapshot changed while the registry was mutated\nbefore: %v\n after: %v", before, after)
	}
	// A crashed server's tools stay callable for the rest of the turn, and they
	// resolve to the instance the turn started with, not the replacement.
	crashed, ok := held.Get("create_issue")
	if !ok {
		t.Fatal("a tool whose source was removed mid-turn is no longer in the snapshot the turn is holding; the call routes nowhere instead of failing as an ordinary tool error")
	}
	if crashed.Description() != "from mcp:github" {
		t.Errorf("the held snapshot resolved create_issue to %q, want the instance it was taken with", crashed.Description())
	}

	// Without this the test would pass just as well on a registry that ignored
	// every mutation.
	fresh := names(t, reg.Snapshot())
	if slices.Equal(before, fresh) {
		t.Fatalf("a fresh snapshot is identical to one taken before three mutations (%v), so the mutations are not landing and stability here proves nothing", fresh)
	}
	if slices.Contains(fresh, "create_issue") {
		t.Errorf("create_issue survived Remove in a fresh snapshot: %v", fresh)
	}
	if held.Version() == reg.Snapshot().Version() {
		t.Errorf("both snapshots report version %d, so nothing distinguishes the generation a turn was taken from", held.Version())
	}
}

func TestDuplicateNamesResolveToOneToolDeterministically(t *testing.T) {
	reg := tool.NewRegistry(from("builtin", "search"))
	reg.Replace("mcp:b", from("mcp:b", "search"))
	reg.Replace("mcp:a", from("mcp:a", "search"))

	for i := range 20 {
		reg.Replace("mcp:b", from("mcp:b", "search"))

		snap := reg.Snapshot()
		if got := names(t, snap); !slices.Equal(got, []string{"search"}) {
			t.Fatalf("rebuild %d put %v on the wire; a name the model cannot address unambiguously has to be resolved before it leaves here", i, got)
		}
		winner, _ := snap.Get("search")
		if winner.Description() != "from builtin" {
			t.Fatalf("rebuild %d resolved search to %q, want the built-in; which duplicate wins has to be the same every time or the prompt prefix moves", i, winner.Description())
		}
	}
}

// The MCP manager rebuilds a []Tool per refresh and is free to reuse the
// backing array; nothing about Replace tells it otherwise.
func TestTheCallersSliceIsNotTheRegistrysCopy(t *testing.T) {
	builtins := from("builtin", "read")
	dynamic := from("mcp:github", "create_issue")

	reg := tool.NewRegistry(builtins)
	reg.Replace("mcp:github", dynamic)
	before := names(t, reg.Snapshot())

	builtins[0] = registered{name: "rm_rf", source: "elsewhere"}
	dynamic[0] = registered{name: "rm_rf", source: "elsewhere"}

	// The stored slices are read again only when a Set is built, so without a
	// rebuild here the aliasing cannot show up and the test proves nothing.
	stale := reg.Snapshot().Version()
	reg.Replace("mcp:probe", nil)
	fresh := reg.Snapshot()
	if fresh.Version() == stale {
		t.Fatalf("still on version %d after a Replace, so nothing re-read the slices this test wrote to", stale)
	}

	if after := names(t, fresh); !slices.Equal(before, after) {
		t.Errorf("writing to the caller's slice changed the registry\nbefore: %v\n after: %v", before, after)
	}
}

func TestZeroRegistryIsUsable(t *testing.T) {
	var reg tool.Registry

	empty := reg.Snapshot()
	if empty == nil {
		t.Fatal("Snapshot on an untouched registry returned nil; every caller dereferences it")
	}
	if got := empty.Specs(); len(got) != 0 {
		t.Errorf("an untouched registry produced %v", got)
	}
	if _, ok := empty.Get("read"); ok {
		t.Error("an untouched registry resolved a tool name")
	}

	reg.Replace("mcp:github", from("mcp:github", "create_issue"))
	if _, ok := reg.Snapshot().Get("create_issue"); !ok {
		t.Error("a source registered on an untouched registry is not in the next snapshot")
	}
}

// The MCP manager writes while the agent goroutine reads: servers connect,
// crash and refresh on their own schedule. Run under -race, which is where this
// test earns its place.
func TestSnapshotsAndMutationsRunInParallel(t *testing.T) {
	builtins := from("builtin", "read", "write", "bash")
	reg := tool.NewRegistry(builtins)

	const (
		writers = 4
		readers = 4
		rounds  = 200
	)

	start := make(chan struct{})
	var wg sync.WaitGroup

	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			source := fmt.Sprintf("mcp:%d", w)
			<-start
			for i := range rounds {
				reg.Replace(source, from(source, fmt.Sprintf("%s_tool_%d", source, i%3)))
				if i%4 == 0 {
					reg.Remove(source)
				}
			}
		}()
	}

	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range rounds {
				snap := reg.Snapshot()
				specs := snap.Specs()

				seen := make([]string, len(specs))
				for i, spec := range specs {
					seen[i] = spec.Name
				}
				if !slices.IsSorted(seen) {
					t.Errorf("a snapshot taken during mutation is not sorted: %v", seen)
					return
				}
				if len(slices.Compact(slices.Clone(seen))) != len(seen) {
					t.Errorf("a snapshot taken during mutation holds a duplicate name: %v", seen)
					return
				}
				for _, builtin := range builtins {
					if _, ok := snap.Get(builtin.Name()); !ok {
						t.Errorf("built-in %s vanished from a snapshot taken during mutation: %v", builtin.Name(), seen)
						return
					}
				}
			}
		}()
	}

	close(start)
	wg.Wait()

	// Readers hold their invariants just as well against a registry nothing wrote
	// to, so the run has to show that the writes landed while they were reading.
	if v := reg.Snapshot().Version(); v < writers*rounds/2 {
		t.Errorf("the registry advanced %d generations under %d writers × %d rounds, too few for the readers to have overlapped a mutation", v, writers, rounds)
	}
}
