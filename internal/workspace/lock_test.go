package workspace_test

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/workspace"
)

// A wait no lock should ever make anyone serve, and one every held lock always
// does: the first is long enough that a loaded scheduler still gets there, the
// second short enough to be paid once per blocked caller.
const (
	neverWaited = 2 * time.Second
	alwaysWaits = 100 * time.Millisecond
)

func TestLockFileSerializesEverySpellingOfOnePath(t *testing.T) {
	f := newFixture(t)

	for _, c := range []struct {
		what string
		a, b string
	}{
		{"a bare name and a dot-prefixed one", "inside.txt", "./inside.txt"},
		{"a bare name and dot-dot back to it", "inside.txt", "sub/../inside.txt"},
		{"a relative name and its absolute spelling", "inside.txt", filepath.Join(f.dir, "inside.txt")},
	} {
		t.Run(c.what, func(t *testing.T) {
			peak, alone := hold(t, f.ws, c.a, c.b)
			if peak != 1 {
				t.Errorf("%d holders were inside at once; %q and %q are one file", peak, c.a, c.b)
			}
			if alone != 2 {
				t.Errorf("%d of the 2 holders waited out the barrier alone; meeting it at all means "+
					"%q and %q took different locks", alone, c.a, c.b)
			}
		})
	}
}

// TestLockFileSerializesASymlinkWithItsTarget is the spelling EvalSymlinks is in
// the key for, and the one a lexical key passes every other case without.
func TestLockFileSerializesASymlinkWithItsTarget(t *testing.T) {
	f := newFixture(t)
	symlink(t, "inside.txt", filepath.Join(f.dir, "link.txt"))

	peak, alone := hold(t, f.ws, "inside.txt", "link.txt")
	if peak != 1 {
		t.Errorf("%d holders were inside at once; link.txt is inside.txt", peak)
	}
	if alone != 2 {
		t.Errorf("%d of the 2 holders waited out the barrier alone; a lock keyed on the name as "+
			"supplied lets a symlink and its target be written at the same time", alone)
	}
}

func TestLockFileLeavesDifferentFilesParallel(t *testing.T) {
	f := newFixture(t)

	peak, alone := hold(t, f.ws, "inside.txt", "sub/other.txt")
	if peak != 2 {
		t.Errorf("only %d holder was ever inside; two different files serialized on one lock", peak)
	}
	if alone != 0 {
		t.Errorf("%d of the 2 holders waited out the barrier alone, so they never overlapped", alone)
	}
}

// TestLockFileSerializesUnderLoad is the count-based half of the same claim, and
// the one the race detector reads: the counter is guarded by nothing but the
// lock, so a lock that serializes nothing loses increments and trips -race for
// the same reason.
func TestLockFileSerializesUnderLoad(t *testing.T) {
	f := newFixture(t)

	spellings := []string{"inside.txt", "./inside.txt", "sub/../inside.txt", filepath.Join(f.dir, "inside.txt")}
	const rounds = 200

	counter := 0
	var wg sync.WaitGroup
	for _, name := range spellings {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range rounds {
				unlock, err := f.ws.LockFile(name)
				if err != nil {
					t.Errorf("LockFile(%q): %v", name, err)
					return
				}
				counter++
				unlock()
			}
		}()
	}
	wg.Wait()

	if want := len(spellings) * rounds; counter != want {
		t.Errorf("%d of %d increments survived; the spellings did not share one lock", counter, want)
	}
}

// TestLockFileKeysAFileThatDoesNotExistYetWhereItWillBe covers the write that
// creates a file: it has no realpath to key on, and the key it gets instead has
// to be the one every later mutation of that file resolves to.
func TestLockFileKeysAFileThatDoesNotExistYetWhereItWillBe(t *testing.T) {
	f := newFixture(t)
	const name = "created.txt"

	unlock, err := f.ws.LockFile(name)
	if err != nil {
		t.Fatalf("LockFile(%q) before the file exists: %v", name, err)
	}
	write(t, filepath.Join(f.dir, name), "created")

	second := waiting(t, f.ws, name)
	select {
	case <-second:
		t.Fatal("the file was locked again while the first hold was open; a path locked before it " +
			"exists is keyed somewhere its own realpath will not land")
	case <-time.After(alwaysWaits):
	}

	unlock()
	release(t, second)
}

func TestLockFileRefusesAPathOutsideTheWorkspace(t *testing.T) {
	f := newFixture(t)

	for _, given := range []string{"../outside/secret.txt", filepath.Join(f.outside, "secret.txt")} {
		unlock, err := f.ws.LockFile(given)
		if unlock != nil {
			unlock()
			t.Errorf("LockFile(%q) handed back a release, so a refused path still took a lock", given)
		}
		assertOutside(t, err, given, f.ws.Root())
	}
}

// TestUnlockingTwiceDoesNotReleaseTheNextHolder is why the release is not the
// mutex's own Unlock. A second call to a stale release would hand the file to a
// third caller while the second is still writing it — the corruption this lock
// exists to prevent, reached through the lock itself.
func TestUnlockingTwiceDoesNotReleaseTheNextHolder(t *testing.T) {
	f := newFixture(t)

	first, err := f.ws.LockFile("inside.txt")
	if err != nil {
		t.Fatalf("LockFile: %v", err)
	}
	first()

	second, err := f.ws.LockFile("inside.txt")
	if err != nil {
		t.Fatalf("LockFile: %v", err)
	}
	first()

	third := waiting(t, f.ws, "inside.txt")
	select {
	case <-third:
		t.Fatal("a stale release opened the lock its next holder was inside")
	case <-time.After(alwaysWaits):
	}

	second()
	release(t, third)
}

// hold takes the lock on each name from its own goroutine and meets a barrier of
// that many inside the critical section, reporting the most holders ever inside
// together and how many stood there alone.
//
// The barrier is what makes both answers facts rather than guesses, and neither
// rests on how long anything took: names the lock treats as one file can never
// meet it however long they wait, and names it treats as different files meet it
// at once and wait out nothing.
func hold(t *testing.T, ws *workspace.Workspace, names ...string) (peak, alone int) {
	t.Helper()

	r := &rendezvous{width: len(names), wait: alwaysWaits, open: make(chan struct{})}

	var wg sync.WaitGroup
	for _, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := ws.LockFile(name)
			if err != nil {
				t.Errorf("LockFile(%q): %v", name, err)
				return
			}
			defer unlock()
			r.attend()
		}()
	}
	wg.Wait()

	return r.peaked()
}

type rendezvous struct {
	width int
	wait  time.Duration

	mu       sync.Mutex
	inFlight int
	peak     int
	alone    int
	met      bool
	open     chan struct{}
}

func (r *rendezvous) attend() {
	r.mu.Lock()
	r.inFlight++
	r.peak = max(r.peak, r.inFlight)
	if r.inFlight == r.width && !r.met {
		r.met = true
		close(r.open)
	}
	r.mu.Unlock()

	timer := time.NewTimer(r.wait)
	defer timer.Stop()
	select {
	case <-r.open:
	case <-timer.C:
		r.mu.Lock()
		r.alone++
		r.mu.Unlock()
	}

	r.mu.Lock()
	r.inFlight--
	r.mu.Unlock()
}

func (r *rendezvous) peaked() (peak, alone int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak, r.alone
}

// waiting starts a goroutine that takes the lock on name and sends its release
// down the channel once it has it. The channel is buffered, so a goroutine that
// acquires where the test expected it to block still finishes.
func waiting(t *testing.T, ws *workspace.Workspace, name string) <-chan func() {
	t.Helper()

	got := make(chan func(), 1)
	go func() {
		unlock, err := ws.LockFile(name)
		if err != nil {
			t.Errorf("LockFile(%q): %v", name, err)
			close(got)
			return
		}
		got <- unlock
	}()
	return got
}

func release(t *testing.T, got <-chan func()) {
	t.Helper()

	select {
	case unlock := <-got:
		if unlock == nil {
			t.Fatal("the waiting caller failed rather than taking the lock")
		}
		unlock()
	case <-time.After(neverWaited):
		t.Fatal("the lock was never handed to the caller waiting on it")
	}
}
