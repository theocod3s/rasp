package builtin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tool/builtin"
	"github.com/theocod3s/rasp/internal/workspace"
)

// A wait no tool should ever make anyone serve, and one a tool blocked on the
// file lock always does.
const (
	neverWaited = 2 * time.Second
	alwaysWaits = 100 * time.Millisecond
)

// TestEditHoldsTheFileLockAcrossItsReadAndWrite is the span the lock has to
// cover rather than merely the write. The file changes underneath the waiting
// edit, so an edit holding the lock only around its write would apply to text it
// read before waiting and silently undo the change it waited for.
func TestEditHoldsTheFileLockAcrossItsReadAndWrite(t *testing.T) {
	ws, reads, dir := editWorkspace(t)
	editWrite(t, reads, dir, "main.go", "one\ntwo\n")

	unlock := lock(t, ws, "main.go")
	defer unlock()

	second := run(t, builtin.Edit(ws, reads), `{"path":"main.go","old_string":"two","new_string":"TWO"}`)
	select {
	case <-second:
		t.Fatal("the edit finished while the file was locked, so it takes no lock at all")
	case <-time.After(alwaysWaits):
	}

	// The edit that got there first, landing while the second is still waiting.
	editWrite(t, reads, dir, "main.go", "ONE\ntwo\n")
	unlock()

	if result := arrive(t, second); result.IsError {
		t.Fatalf("the edit failed once the lock was released: %s", result.Content)
	}
	if got := editRead(t, dir, "main.go"); got != "ONE\nTWO\n" {
		t.Errorf("main.go reads %q, want %q — the waiting edit rewrote the file from the text it "+
			"had read before it waited", got, "ONE\nTWO\n")
	}
}

// TestConcurrentEditsOfOneFileBothApply is the same claim without a test holding
// anything: two edits of one file, started together, each replacing a different
// line. Serialized, either order leaves both replacements; unserialized, both
// read the original and whichever writes last drops the other.
func TestConcurrentEditsOfOneFileBothApply(t *testing.T) {
	ws, reads, dir := editWorkspace(t)
	edit := builtin.Edit(ws, reads)

	const rounds = 32
	for i := range rounds {
		name := fmt.Sprintf("round%d.go", i)
		editWrite(t, reads, dir, name, "one\ntwo\n")

		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, replacement := range [][2]string{{"one", "ONE"}, {"two", "TWO"}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				args := fmt.Sprintf(`{"path":%q,"old_string":%q,"new_string":%q}`,
					name, replacement[0], replacement[1])
				<-start
				result, err := edit.Run(context.Background(), json.RawMessage(args))
				switch {
				case err != nil:
					t.Errorf("edit of %s could not run: %v", name, err)
				case result.IsError:
					t.Errorf("edit of %s failed: %s", name, result.Content)
				}
			}()
		}
		close(start)
		wg.Wait()

		if got := editRead(t, dir, name); got != "ONE\nTWO\n" {
			t.Fatalf("after %d rounds %s reads %q, want %q; one edit read the file before the "+
				"other's write and then overwrote it", i+1, name, got, "ONE\nTWO\n")
		}
	}
}

// TestEditOfAnotherFileDoesNotWaitOnALockedOne is the other half: the whole of
// one edit runs inside the window another file's lock is held open, so a lock
// keyed on anything coarser than the file fails here.
func TestEditOfAnotherFileDoesNotWaitOnALockedOne(t *testing.T) {
	ws, reads, dir := editWorkspace(t)
	editWrite(t, reads, dir, "held.go", "one\n")
	editWrite(t, reads, dir, "free.go", "one\n")

	unlock := lock(t, ws, "held.go")
	defer unlock()

	if result := arrive(t, run(t, builtin.Edit(ws, reads), `{"path":"free.go","old_string":"one","new_string":"ONE"}`)); result.IsError {
		t.Fatalf("the edit of free.go failed: %s", result.Content)
	}
	if got := editRead(t, dir, "free.go"); got != "ONE\n" {
		t.Errorf("free.go reads %q, want %q", got, "ONE\n")
	}
	if got := editRead(t, dir, "held.go"); got != "one\n" {
		t.Errorf("held.go reads %q; nothing edited it", got)
	}
}

// TestWriteHoldsTheFileLockAcrossItsStatAndRename covers write's own span. The
// file appears while the write waits, and the write has to notice: the stat that
// decides created — and the mode the replacement lands with — is read inside the
// lock or it is read of a file somebody else is about to replace.
func TestWriteHoldsTheFileLockAcrossItsStatAndRename(t *testing.T) {
	ws, reads, dir := editWorkspace(t)

	unlock := lock(t, ws, "notes.txt")
	defer unlock()

	second := run(t, builtin.NewWrite(ws, reads), `{"path":"notes.txt","content":"second"}`)
	select {
	case <-second:
		t.Fatal("the write finished while the file was locked, so it takes no lock at all")
	case <-time.After(alwaysWaits):
	}

	editWrite(t, reads, dir, "notes.txt", "first")
	unlock()

	result := arrive(t, second)
	if result.IsError {
		t.Fatalf("the write failed once the lock was released: %s", result.Content)
	}
	details, ok := result.Details.(*tool.DiffDetails)
	if !ok {
		t.Fatalf("Details is %T, want *tool.DiffDetails", result.Details)
	}
	// The diff is of what the write replaced, so it names the contents that
	// appeared while the write waited. A write that read the path before taking
	// the lock found nothing there and would report creating the file: every
	// line added and none taken away.
	if details.Deletions == 0 {
		t.Errorf("the write's diff takes nothing away:\n%s\nso it read the path before taking the "+
			"lock, and diffed against a file that was not there yet", details.Unified)
	}
	if got := editRead(t, dir, "notes.txt"); got != "second" {
		t.Errorf("notes.txt reads %q, want %q", got, "second")
	}
}

func lock(t *testing.T, ws *workspace.Workspace, name string) func() {
	t.Helper()

	unlock, err := ws.LockFile(name)
	if err != nil {
		t.Fatalf("locking %s: %v", name, err)
	}
	return unlock
}

// run calls the tool on its own goroutine, so a test can watch whether it
// finishes rather than waiting for it. The channel is buffered, so a call that
// returns where the test expected it to block still finishes.
func run(t *testing.T, impl tool.Tool, args string) <-chan tool.Result {
	t.Helper()

	done := make(chan tool.Result, 1)
	go func() {
		result, err := impl.Run(context.Background(), json.RawMessage(args))
		if err != nil {
			t.Errorf("%s could not run: %v", impl.Name(), err)
		}
		done <- result
	}()
	return done
}

func arrive(t *testing.T, done <-chan tool.Result) tool.Result {
	t.Helper()

	select {
	case result := <-done:
		return result
	case <-time.After(neverWaited):
		t.Fatal("the call never returned; it is still waiting on a lock nothing holds")
		return tool.Result{}
	}
}
