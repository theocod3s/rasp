package builtin_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tool/builtin"
	"github.com/theocod3s/rasp/internal/workspace"
)

// editTwice holds the same statement in two places, so an edit naming one of
// them names both.
const (
	editTwice          = "func serve() {\n\tport := 8080\n}\n\nfunc probe() {\n\tport := 8080\n}\n"
	editFirstMatchWins = "func serve() {\n\tport := 9090\n}\n\nfunc probe() {\n\tport := 8080\n}\n"
	editBothReplaced   = "func serve() {\n\tport := 9090\n}\n\nfunc probe() {\n\tport := 9090\n}\n"
)

func TestEditReplacesAndReportsADiff(t *testing.T) {
	ws, dir := editWorkspace(t)
	editWrite(t, dir, "main.go", "a\nb\nc\n")

	// One line out, two in, so a diff that counted additions as deletions reads
	// differently from one that did not.
	result := editRun(t, ws, `{"path":"main.go","old_string":"b","new_string":"B1\nB2"}`)
	if result.IsError {
		t.Fatalf("edit failed: %s", result.Content)
	}

	if got := editRead(t, dir, "main.go"); got != "a\nB1\nB2\nc\n" {
		t.Errorf("file = %q, want %q", got, "a\nB1\nB2\nc\n")
	}

	details, ok := result.Details.(*tool.DiffDetails)
	if !ok {
		t.Fatalf("Details = %T, want *tool.DiffDetails — the diff is the UI's payload and "+
			"never reaches the model", result.Details)
	}
	want := tool.DiffDetails{
		Path:      "main.go",
		Unified:   "--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,4 @@\n a\n-b\n+B1\n+B2\n c\n",
		Additions: 2,
		Deletions: 1,
	}
	if *details != want {
		t.Errorf("details = %#v, want %#v", *details, want)
	}
	if strings.Contains(result.Content, details.Unified) {
		t.Error("the diff reached the model in Content, where it costs tokens for something " +
			"only the UI draws")
	}
}

// TestEditRefusesAnAmbiguousMatch is the rung the tool exists to get right: the
// file has to come back untouched, and the message has to carry the count, or
// the model has no way to know how much context to add.
func TestEditRefusesAnAmbiguousMatch(t *testing.T) {
	ws, dir := editWorkspace(t)
	editWrite(t, dir, "server.go", editTwice)

	result := editRun(t, ws, `{"path":"server.go","old_string":"8080","new_string":"9090"}`)
	if !result.IsError {
		t.Fatalf("edit succeeded on an ambiguous match: %s", result.Content)
	}
	if !strings.Contains(result.Content, "2 times") {
		t.Errorf("message does not count the occurrences: %q", result.Content)
	}
	if !strings.Contains(result.Content, "replace_all") {
		t.Errorf("message does not offer replace_all, which is the other way out: %q", result.Content)
	}
	if result.Details != nil {
		t.Errorf("a refused edit produced Details %#v", result.Details)
	}

	switch got := editRead(t, dir, "server.go"); got {
	case editFirstMatchWins:
		t.Fatal("first match wins: the port in serve() was replaced and probe() left holding " +
			"the old one, on an edit that named neither")
	case editBothReplaced:
		t.Fatal("every occurrence was replaced without replace_all being set")
	case editTwice:
	default:
		t.Fatalf("a refused edit rewrote the file to %q", got)
	}
}

func TestEditReplaceAllTakesEveryOccurrence(t *testing.T) {
	ws, dir := editWorkspace(t)
	editWrite(t, dir, "server.go", editTwice)

	result := editRun(t, ws, `{"path":"server.go","old_string":"8080","new_string":"9090","replace_all":true}`)
	if result.IsError {
		t.Fatalf("edit failed: %s", result.Content)
	}
	if got := editRead(t, dir, "server.go"); got != editBothReplaced {
		t.Errorf("file = %q, want %q", got, editBothReplaced)
	}
	if !strings.Contains(result.Content, "2 replacements") {
		t.Errorf("message does not report how many occurrences changed: %q", result.Content)
	}
}

func TestEditRefusalsLeaveTheFileAlone(t *testing.T) {
	const original = "if x {\n\treturn 1\n}\n"

	tests := map[string]struct {
		args string
		want string // a phrase the model needs in order to act
	}{
		"text is not there": {
			args: `{"path":"main.go","old_string":"return 2","new_string":"return 3"}`,
			want: "not in main.go",
		},
		"whitespace differs": {
			args: `{"path":"main.go","old_string":"    return 1","new_string":"    return 2"}`,
			want: "not in main.go",
		},
		"nothing to match": {
			args: `{"path":"main.go","old_string":"","new_string":"x"}`,
			want: "empty",
		},
		"replacement is the original": {
			args: `{"path":"main.go","old_string":"return 1","new_string":"return 1"}`,
			want: "identical",
		},
		"file is not there": {
			args: `{"path":"absent.go","old_string":"a","new_string":"b"}`,
			want: "absent.go",
		},
		"path leaves the workspace": {
			args: `{"path":"../escape.go","old_string":"a","new_string":"b"}`,
			want: "outside the workspace",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ws, dir := editWorkspace(t)
			editWrite(t, dir, "main.go", original)

			result := editRun(t, ws, tc.args)
			if !result.IsError {
				t.Fatalf("edit succeeded: %s", result.Content)
			}
			if !strings.Contains(result.Content, tc.want) {
				t.Errorf("message %q does not mention %q", result.Content, tc.want)
			}
			if got := editRead(t, dir, "main.go"); got != original {
				t.Errorf("a refused edit rewrote the file to %q", got)
			}
		})
	}
}

// TestEditKeepsTheFileMode holds a private file private. Editing in place gets
// that from the operating system, since the mode passed to a write applies only
// when it creates the file — so the plausible simplification this guards against
// is a switch to writing a temporary file and renaming it over the original,
// which would publish every 0600 file it touched.
func TestEditKeepsTheFileMode(t *testing.T) {
	ws, dir := editWorkspace(t)
	editWrite(t, dir, "secret.txt", "a\n")
	if err := os.Chmod(filepath.Join(dir, "secret.txt"), 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if result := editRun(t, ws, `{"path":"secret.txt","old_string":"a","new_string":"b"}`); result.IsError {
		t.Fatalf("edit failed: %s", result.Content)
	}

	info, err := os.Stat(filepath.Join(dir, "secret.txt"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %v, want %v", got, os.FileMode(0o600))
	}
}

func TestEditSchema(t *testing.T) {
	ws, _ := editWorkspace(t)
	schema := builtin.Edit(ws).Schema()

	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties object: %#v", schema)
	}
	for _, name := range []string{"path", "old_string", "new_string", "replace_all"} {
		if _, ok := properties[name]; !ok {
			t.Errorf("schema has no %q property", name)
		}
	}

	required, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("schema has no required list: %#v", schema)
	}
	want := []string{"path", "old_string", "new_string"}
	if strings.Join(required, ",") != strings.Join(want, ",") {
		t.Errorf("required = %v, want %v; replace_all defaults to false and asking the model "+
			"for it every call spends tokens on a no-op", required, want)
	}
}

func editWorkspace(t *testing.T) (*workspace.Workspace, string) {
	t.Helper()

	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatalf("opening a workspace at %s: %v", dir, err)
	}
	t.Cleanup(func() { ws.Close() })

	// t.TempDir hands back /var/... on macOS, which is a symlink to /private/var;
	// workspace.Open resolves it, so reading a file back has to use the resolved
	// root or the paths compare unequal for no reason the test is about.
	return ws, ws.Root()
}

func editRun(t *testing.T, ws *workspace.Workspace, args string) tool.Result {
	t.Helper()

	result, err := builtin.Edit(ws).Run(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("edit could not run: %v", err)
	}
	return result
}

func editWrite(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func editRead(t *testing.T, dir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}
