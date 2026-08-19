package builtin_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tool/builtin"
	"github.com/theocod3s/rasp/internal/workspace"
)

func TestEditRefusesAFileThisSessionHasNotRead(t *testing.T) {
	ws, reads, dir := editWorkspace(t)
	writeUnread(t, dir, "main.go", "package main\n")

	result := editRun(t, ws, reads, `{"path":"main.go","old_string":"main","new_string":"MAIN"}`)
	if !result.IsError {
		t.Fatalf("edit succeeded on a file this session never read: %s", result.Content)
	}
	for _, want := range []string{"main.go", "Read it before"} {
		if !strings.Contains(result.Content, want) {
			t.Errorf("message %q does not carry %q", result.Content, want)
		}
	}
	if got := editRead(t, dir, "main.go"); got != "package main\n" {
		t.Errorf("a refused edit rewrote the file to %q", got)
	}
}

func TestEditRefusesAFileModifiedSinceItWasRead(t *testing.T) {
	ws, reads, dir := editWorkspace(t)
	read := builtin.NewRead(ws, reads)
	writeUnread(t, dir, "main.go", "package main\n")

	if res, err := read.Run(context.Background(), json.RawMessage(`{"path":"main.go"}`)); err != nil || res.IsError {
		t.Fatalf("read failed: result %+v, err %v", res, err)
	}

	// A change to the content and, deterministically rather than by however much
	// real time elapses between two writes, to the mtime: the read this session
	// took never saw either.
	writeUnread(t, dir, "main.go", "package other\n")
	touch(t, dir, "main.go", time.Now().Add(time.Hour))

	result := editRun(t, ws, reads, `{"path":"main.go","old_string":"other","new_string":"OTHER"}`)
	if !result.IsError {
		t.Fatalf("edit succeeded on a file modified since it was read: %s", result.Content)
	}
	for _, want := range []string{"main.go", "Read it again"} {
		if !strings.Contains(result.Content, want) {
			t.Errorf("message %q does not carry %q", result.Content, want)
		}
	}
	if got := editRead(t, dir, "main.go"); got != "package other\n" {
		t.Errorf("a refused edit rewrote the file to %q", got)
	}
}

func TestEditSucceedsAfterAnActualRead(t *testing.T) {
	ws, reads, dir := editWorkspace(t)
	read := builtin.NewRead(ws, reads)
	writeUnread(t, dir, "main.go", "package main\n")

	if res, err := read.Run(context.Background(), json.RawMessage(`{"path":"main.go"}`)); err != nil || res.IsError {
		t.Fatalf("read failed: result %+v, err %v", res, err)
	}

	result := editRun(t, ws, reads, `{"path":"main.go","old_string":"main","new_string":"MAIN"}`)
	if result.IsError {
		t.Fatalf("edit failed after an actual read: %s", result.Content)
	}
	if got := editRead(t, dir, "main.go"); got != "package MAIN\n" {
		t.Errorf("file = %q, want %q", got, "package MAIN\n")
	}
}

// TestASuccessfulEditNeedsNoRereadForTheNextOne is the case the guard cannot be
// allowed to break: two edits of the same file, back to back, with nothing
// between them but the first edit's own write. If a successful edit did not
// re-record itself, every second edit in a normal editing session would refuse.
func TestASuccessfulEditNeedsNoRereadForTheNextOne(t *testing.T) {
	ws, reads, dir := editWorkspace(t)
	editWrite(t, reads, dir, "main.go", "one\ntwo\n")

	if result := editRun(t, ws, reads, `{"path":"main.go","old_string":"one","new_string":"ONE"}`); result.IsError {
		t.Fatalf("first edit failed: %s", result.Content)
	}

	result := editRun(t, ws, reads, `{"path":"main.go","old_string":"two","new_string":"TWO"}`)
	if result.IsError {
		t.Fatalf("second edit was refused, so the first edit's own write did not record a read: %s",
			result.Content)
	}
	if got := editRead(t, dir, "main.go"); got != "ONE\nTWO\n" {
		t.Errorf("file = %q, want %q", got, "ONE\nTWO\n")
	}
}

func TestWriteRefusesToOverwriteAFileThisSessionHasNotRead(t *testing.T) {
	ws, reads, dir := editWorkspace(t)
	writeUnread(t, dir, "notes.txt", "old")

	result := writeRun(t, ws, reads, `{"path":"notes.txt","content":"new"}`)
	if !result.IsError {
		t.Fatalf("write succeeded overwriting a file this session never read: %s", result.Content)
	}
	for _, want := range []string{"notes.txt", "Read it before"} {
		if !strings.Contains(result.Content, want) {
			t.Errorf("message %q does not carry %q", result.Content, want)
		}
	}
	if got := editRead(t, dir, "notes.txt"); got != "old" {
		t.Errorf("a refused write rewrote the file to %q", got)
	}
}

func TestWriteRefusesToOverwriteAFileModifiedSinceItWasRead(t *testing.T) {
	ws, reads, dir := editWorkspace(t)
	read := builtin.NewRead(ws, reads)
	writeUnread(t, dir, "notes.txt", "old")

	if res, err := read.Run(context.Background(), json.RawMessage(`{"path":"notes.txt"}`)); err != nil || res.IsError {
		t.Fatalf("read failed: result %+v, err %v", res, err)
	}

	writeUnread(t, dir, "notes.txt", "changed underneath")
	touch(t, dir, "notes.txt", time.Now().Add(time.Hour))

	result := writeRun(t, ws, reads, `{"path":"notes.txt","content":"new"}`)
	if !result.IsError {
		t.Fatalf("write succeeded overwriting a file modified since it was read: %s", result.Content)
	}
	for _, want := range []string{"notes.txt", "Read it again"} {
		if !strings.Contains(result.Content, want) {
			t.Errorf("message %q does not carry %q", result.Content, want)
		}
	}
	if got := editRead(t, dir, "notes.txt"); got != "changed underneath" {
		t.Errorf("a refused write rewrote the file to %q", got)
	}
}

// TestWriteOfANewFileNeedsNoPriorRead is the other half of the same rule: a
// path nothing has written yet has no existing content to clobber, so write
// creating one is not an overwrite and the guard does not apply to it.
func TestWriteOfANewFileNeedsNoPriorRead(t *testing.T) {
	ws, reads, dir := editWorkspace(t)

	result := writeRun(t, ws, reads, `{"path":"fresh.txt","content":"hello"}`)
	if result.IsError {
		t.Fatalf("write of a brand-new file was refused: %s", result.Content)
	}
	if got := editRead(t, dir, "fresh.txt"); got != "hello" {
		t.Errorf("file = %q, want %q", got, "hello")
	}
}

// TestASuccessfulWriteNeedsNoRereadForALaterEdit is TestASuccessfulEditNeedsNoRereadForTheNextOne's
// counterpart across tools: a write and a follow-up edit of the file it just
// produced share one tracker, and the write's own record has to satisfy the
// edit exactly as a read would.
func TestASuccessfulWriteNeedsNoRereadForALaterEdit(t *testing.T) {
	ws, reads, dir := editWorkspace(t)

	if result := writeRun(t, ws, reads, `{"path":"fresh.go","content":"one\ntwo\n"}`); result.IsError {
		t.Fatalf("write failed: %s", result.Content)
	}

	result := editRun(t, ws, reads, `{"path":"fresh.go","old_string":"one","new_string":"ONE"}`)
	if result.IsError {
		t.Fatalf("edit was refused after a write of the same file, so the write did not record "+
			"a read: %s", result.Content)
	}
	if got := editRead(t, dir, "fresh.go"); got != "ONE\ntwo\n" {
		t.Errorf("file = %q, want %q", got, "ONE\ntwo\n")
	}
}

// writeUnread puts content on disk without going through any tool, so the file
// exists but this session has not read it — the state every refusal test in
// this file starts from.
func writeUnread(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// touch sets name's mtime explicitly, rather than relying on however much real
// time separates two writes: on a filesystem with coarse mtime resolution, two
// writes in the same test can otherwise land on the same timestamp and the
// staleness this test is about never actually happens.
func touch(t *testing.T, dir, name string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(filepath.Join(dir, name), when, when); err != nil {
		t.Fatalf("setting the mtime of %s: %v", name, err)
	}
}

func writeRun(t *testing.T, ws *workspace.Workspace, reads *workspace.Tracker, args string) tool.Result {
	t.Helper()

	result, err := builtin.NewWrite(ws, reads).Run(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("write could not run: %v", err)
	}
	return result
}
