package workspace_test

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/workspace"
)

func TestTrackerReturnsTheMtimeARecordedReadSaw(t *testing.T) {
	tr := workspace.NewTracker()
	when := time.Date(2026, 8, 18, 9, 30, 0, 0, time.UTC)

	if _, ok := tr.LastRead("main.go"); ok {
		t.Fatal("a tracker that has recorded nothing reports a read of main.go, so every later " +
			"assertion here is about a lookup that answers regardless of what was recorded")
	}

	tr.Record("main.go", when)
	got, ok := tr.LastRead("main.go")
	if !ok {
		t.Fatal("LastRead(main.go) found nothing after Record(main.go)")
	}
	if !got.Equal(when) {
		t.Errorf("LastRead(main.go) = %v, want the mtime Record was given, %v", got, when)
	}
	if _, ok := tr.LastRead("other.go"); ok {
		t.Error("recording a read of main.go also reported one for other.go")
	}
}

// TestTrackerKeepsTheLatestReadOfAFile is the case an edit depends on: a file
// read, changed and read again is no longer stale, and it stops being stale only
// because the second read replaced the first mtime rather than being dropped
// beside it.
func TestTrackerKeepsTheLatestReadOfAFile(t *testing.T) {
	tr := workspace.NewTracker()
	first := time.Date(2026, 8, 18, 9, 30, 0, 0, time.UTC)
	second := first.Add(time.Minute)

	tr.Record("main.go", first)
	tr.Record("main.go", second)

	got, ok := tr.LastRead("main.go")
	if !ok {
		t.Fatal("LastRead(main.go) found nothing after two Records")
	}
	if !got.Equal(second) {
		t.Errorf("LastRead(main.go) = %v, want the second read's mtime %v", got, second)
	}
}

// TestTrackerSurvivesConcurrentReads exists for the race detector: tools run in
// parallel by default (design §6), so several reads record at once while an edit
// may be asking about a different file.
func TestTrackerSurvivesConcurrentReads(t *testing.T) {
	tr := workspace.NewTracker()
	const writers = 8

	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path := "file" + strconv.Itoa(i) + ".go"
			tr.Record(path, time.Unix(int64(i), 0))
			tr.LastRead("file0.go")
		}()
	}
	wg.Wait()

	for i := range writers {
		path := "file" + strconv.Itoa(i) + ".go"
		got, ok := tr.LastRead(path)
		if !ok {
			t.Fatalf("LastRead(%s) found nothing, so the concurrent Records did not all land", path)
		}
		if want := time.Unix(int64(i), 0); !got.Equal(want) {
			t.Errorf("LastRead(%s) = %v, want %v", path, got, want)
		}
	}
}
