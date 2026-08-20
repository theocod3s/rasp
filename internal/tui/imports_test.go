package tui_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	// neutral is the one package the UI meets a provider through: Message, Event,
	// the Provider interface and the effort ladder.
	neutral = "github.com/theocod3s/rasp/internal/llm"

	// llmDir is that package on disk, from this one. The adapters are read out of
	// it rather than listed here, so one added later is barred the day it appears
	// rather than the day someone remembers this file.
	llmDir = "../llm"
)

// notAdapters are the packages under internal/llm that are not a wire format:
// the retry policy both adapters share, and the scripted provider the tests here
// drive the loop with. Everything else under it is an adapter, so a new package
// is barred until somebody decides otherwise.
var notAdapters = []string{"fake", "retry"}

// minInspected is a floor under the walk. It sits well below the file count so
// an ordinary addition never trips it, and well above zero because a walk that
// found nothing is the quietest pass a check like this has: a renamed directory,
// a glob that stopped matching, and the tree reads as clean.
const minInspected = 20

// TestTheUIReachesNoAdapter holds design §1's line between the frontends and the
// wire formats. The picker's list of effort levels comes from Provider.Efforts,
// and a UI that could import an adapter could as easily read that adapter's own
// table instead — the copy that goes stale in silence, offering a level the
// request path refuses or hiding one it would accept (decisions.md).
//
// Test files are walked too: a test reaching for a real adapter to find out what
// a provider can send is the same drift, arriving through the door nobody
// watches.
func TestTheUIReachesNoAdapter(t *testing.T) {
	barred := barrier(t)

	fset := token.NewFileSet()
	inspected, sawNeutral := 0, false

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			if path != "." && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return fs.SkipDir
			}
			return nil
		case !strings.HasSuffix(d.Name(), ".go"):
			return nil
		}

		f, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}
		inspected++

		for _, spec := range f.Imports {
			imported, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			if imported == neutral {
				sawNeutral = true
			}
			if barred(imported) {
				t.Errorf("%s imports %q; the UI meets a provider through %s, and a wire format is "+
					"never one of the things it needs to know", path, imported, neutral)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the UI's sources: %v", err)
	}

	if inspected < minInspected {
		t.Fatalf("inspected %d Go file(s) under the UI, fewer than the %d this package has had for "+
			"several milestones; the walk is finding almost nothing", inspected, minInspected)
	}
	// The UI does import the neutral package — llm.Message, and the effort levels
	// the picker draws. Seeing it proves the import blocks above were read rather
	// than skipped past.
	if !sawNeutral {
		t.Errorf("no file under the UI imports %s, so nothing here reads an import block the way "+
			"this test assumes", neutral)
	}
}

// barrier reads the adapters out of the tree and returns the rule the walk
// applies. It fails rather than returning a predicate that would let everything
// through, which is what a bare "no adapter was imported" result would look like
// if internal/llm moved.
func barrier(t *testing.T) func(string) bool {
	t.Helper()

	entries, err := os.ReadDir(llmDir)
	if err != nil {
		t.Fatalf("reading %s, which is where the adapters are: %v", llmDir, err)
	}

	var adapters, present []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "testdata" {
			continue
		}
		present = append(present, e.Name())
		if !slices.Contains(notAdapters, e.Name()) {
			adapters = append(adapters, neutral+"/"+e.Name())
		}
	}

	// Both halves can go quiet on their own. With no adapters the walk guards
	// nothing; with an exception naming a package that is gone, the list reads as
	// covering something it no longer does.
	if len(adapters) == 0 {
		t.Fatalf("found no adapter packages under %s, so this test would pass over any import at all "+
			"— the directories there are %v", llmDir, present)
	}
	for _, name := range notAdapters {
		if !slices.Contains(present, name) {
			t.Fatalf("%s is excused from this rule but is not under %s any more; the exception is "+
				"stale, and the packages there are %v", name, llmDir, present)
		}
	}

	return func(path string) bool {
		for _, adapter := range adapters {
			if path == adapter || strings.HasPrefix(path, adapter+"/") {
				return true
			}
		}
		return false
	}
}
