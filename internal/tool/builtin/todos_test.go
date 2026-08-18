package builtin_test

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tool/builtin"
)

func TestTodosCreatesUpdatesAndCompletes(t *testing.T) {
	todos := builtin.NewTodos()

	res := call(t, todos, `{"items":[
		{"content":"wire the parser","status":"in_progress"},
		{"content":"add the tests","status":"pending"}
	]}`)
	wantContent(t, res, "1. [in_progress] wire the parser\n2. [pending] add the tests")
	wantItems(t, res, []builtin.TodoItem{
		{Content: "wire the parser", Status: builtin.TodoInProgress},
		{Content: "add the tests", Status: builtin.TodoPending},
	})

	res = call(t, todos, `{"items":[
		{"content":"wire the parser","status":"completed"},
		{"content":"add the tests for the parser","status":"in_progress"},
		{"content":"update the docs","status":"pending"}
	]}`)
	wantContent(t, res, "1. [completed] wire the parser\n"+
		"2. [in_progress] add the tests for the parser\n"+
		"3. [pending] update the docs")
	if got, want := res.Title, "1 of 3 done"; got != want {
		t.Errorf("Title = %q, want %q", got, want)
	}

	// The list is the session's, not the call's: it is still there on the next
	// call, which is what makes a plan survive a user message.
	wantContent(t, call(t, todos, `{}`), "1. [completed] wire the parser\n"+
		"2. [in_progress] add the tests for the parser\n"+
		"3. [pending] update the docs")
}

func TestTodosReadsBackWhenItemsIsAbsent(t *testing.T) {
	todos := builtin.NewTodos()

	call(t, todos, `{"items":[{"content":"wire the parser","status":"pending"}]}`)

	// Every shape that carries no list: no arguments at all, an empty object, and
	// a null items key. None may be read as "clear it".
	for _, args := range []string{``, `{}`, `{"items":null}`} {
		res := call(t, todos, args)
		wantContent(t, res, "1. [pending] wire the parser")
		if res.IsError {
			t.Errorf("todos(%q) came back as an error result: %q", args, res.Content)
		}
	}
}

func TestTodosClearsOnAnEmptyArray(t *testing.T) {
	todos := builtin.NewTodos()
	call(t, todos, `{"items":[{"content":"wire the parser","status":"pending"}]}`)

	res := call(t, todos, `{"items":[]}`)
	wantContent(t, res, "The todo list is empty.")
	if got, want := res.Title, "no items"; got != want {
		t.Errorf("Title = %q, want %q", got, want)
	}
	wantItems(t, res, nil)

	wantContent(t, call(t, todos, `{}`), "The todo list is empty.")
}

func TestTodosRejectsABadItemAndStoresNothing(t *testing.T) {
	good := `{"content":"wire the parser","status":"pending"}`
	cases := map[string]struct{ args, wantIn string }{
		"unknown status":  {`{"items":[` + good + `,{"content":"add the tests","status":"doing"}]}`, `Item 2 has status "doing"`},
		"blank status":    {`{"items":[{"content":"add the tests","status":""}]}`, `Item 1 has status ""`},
		"empty content":   {`{"items":[{"content":"","status":"pending"}]}`, "Item 1 has no content"},
		"blank content":   {`{"items":[{"content":"   ","status":"pending"}]}`, "Item 1 has no content"},
		"malformed input": {`{"items":42}`, "do not fit todos"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			todos := builtin.NewTodos()
			call(t, todos, `{"items":[{"content":"the plan so far","status":"in_progress"}]}`)

			res := call(t, todos, tc.args)
			if !res.IsError {
				t.Fatalf("todos(%s) succeeded; want an error result", tc.args)
			}
			if !strings.Contains(res.Content, tc.wantIn) {
				t.Errorf("error result is %q, want it to name the offending item: %q", res.Content, tc.wantIn)
			}

			// A rejected call is a whole rejected call: the plan the user last saw
			// is still the plan, rather than a prefix of the one that failed.
			wantContent(t, call(t, todos, `{}`), "1. [in_progress] the plan so far")
		})
	}
}

func TestTodosListsDoNotLeakBetweenTools(t *testing.T) {
	first, second := builtin.NewTodos(), builtin.NewTodos()

	call(t, first, `{"items":[{"content":"the first session's plan","status":"pending"}]}`)

	// Package-level state would show the first tool's list here, and every session
	// in one process would be editing one checklist.
	wantContent(t, call(t, second, `{}`), "The todo list is empty.")
	wantContent(t, call(t, first, `{}`), "1. [pending] the first session's plan")
}

func TestTodosDetailsIsASnapshotTheUICanKeep(t *testing.T) {
	todos := builtin.NewTodos()
	res := call(t, todos, `{"items":[{"content":"wire the parser","status":"pending"}]}`)

	details, ok := res.Details.(*builtin.TodosDetails)
	if !ok {
		t.Fatalf("Details is %T, want *builtin.TodosDetails", res.Details)
	}
	details.Items[0] = builtin.TodoItem{Content: "the UI got at the list", Status: builtin.TodoCompleted}

	wantContent(t, call(t, todos, `{}`), "1. [pending] wire the parser")
}

func TestTodosSurvivesConcurrentCalls(t *testing.T) {
	todos := builtin.NewTodos()

	const writers = 8
	var wg sync.WaitGroup

	// Not the call helper: its Fatalf would be a FailNow on a goroutine that is not
	// the test's, which Go does not promise to notice.
	concurrently := func(args string) {
		defer wg.Done()
		if _, err := todos.Run(t.Context(), json.RawMessage(args)); err != nil {
			t.Errorf("todos(%s) returned a Go error: %v", args, err)
		}
	}

	for i := range writers {
		wg.Add(2)
		go concurrently(fmt.Sprintf(
			`{"items":[{"content":"plan %d, step one","status":"completed"},{"content":"plan %d, step two","status":"pending"}]}`, i, i))
		go concurrently(`{}`)
	}
	wg.Wait()

	// One writer wins, and the list is that writer's: a list holding step one from
	// one plan and step two from another would mean the swap was not atomic.
	final := call(t, todos, `{}`)
	won := -1
	for i := range writers {
		if strings.Contains(final.Content, fmt.Sprintf("plan %d, step one", i)) {
			won = i
		}
	}
	if won < 0 {
		t.Fatalf("no writer's list survived; the final list reads:\n%s", final.Content)
	}
	wantContent(t, final, fmt.Sprintf("1. [completed] plan %d, step one\n2. [pending] plan %d, step two", won, won))
}

// TestTodosIsNotSequential pins the decision NewTodos documents: implementing
// tool.Sequential here would drag every batch containing a todos call into serial
// execution.
func TestTodosIsNotSequential(t *testing.T) {
	if s, ok := builtin.NewTodos().(tool.Sequential); ok && s.Sequential() {
		t.Error("todos declares itself sequential")
	}
}

// TestTodosSchemaOffersEveryStatus holds the enum tag to the constants, the
// duplication TodoStatus names.
func TestTodosSchemaOffersEveryStatus(t *testing.T) {
	schema := builtin.NewTodos().Schema()
	item := child(t, child(t, child(t, schema, "properties"), "items"), "items")
	status := child(t, child(t, item, "properties"), "status")

	var got []string
	values, ok := status["enum"].([]any)
	if !ok {
		t.Fatalf("the status property carries no enum: %v", status)
	}
	for _, v := range values {
		got = append(got, fmt.Sprint(v))
	}

	want := []string{string(builtin.TodoPending), string(builtin.TodoInProgress), string(builtin.TodoCompleted)}
	if !slices.Equal(got, want) {
		t.Errorf("the schema offers %v, but the tool accepts %v", got, want)
	}
}

// allowedTodosImports is every package todos.go may import. An allowlist, because
// the property is "touches no files and executes nothing" and a denylist of os,
// os/exec and the rest only ever names the packages someone already thought of.
//
// Scoped to the one file rather than the package: builtin also holds bash, which
// spawns processes because that is its job.
var allowedTodosImports = []string{
	"context",
	"fmt",
	"slices",
	"strings",
	"sync",
	"github.com/theocod3s/rasp/internal/tool",
}

func TestTodosTouchesNoFilesAndExecutesNothing(t *testing.T) {
	const file = "todos.go"

	// A file that has been renamed or split lands here rather than passing by
	// having nothing to inspect.
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	if len(parsed.Imports) == 0 {
		t.Fatalf("%s imports nothing at all, so it cannot be the todos tool, which is built on internal/tool", file)
	}

	for _, spec := range parsed.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("%s: cannot read the import path %s: %v", file, spec.Path.Value, err)
		}
		if !slices.Contains(allowedTodosImports, path) {
			t.Errorf("%s imports %q, which is not on this test's allowlist. If it cannot touch the "+
				"filesystem, spawn a process or reach the network, add it; otherwise todos has stopped "+
				"being a checklist and nothing else", file, path)
		}
	}
}

func call(t *testing.T, tl tool.Tool, args string) tool.Result {
	t.Helper()
	res, err := tl.Run(t.Context(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("todos(%s) returned a Go error (%v); a tool that ran and failed says so in its Result", args, err)
	}
	return res
}

func wantContent(t *testing.T, res tool.Result, want string) {
	t.Helper()
	if res.Content != want {
		t.Errorf("the model reads:\n%s\n\nwant:\n%s", res.Content, want)
	}
}

func wantItems(t *testing.T, res tool.Result, want []builtin.TodoItem) {
	t.Helper()
	details, ok := res.Details.(*builtin.TodosDetails)
	if !ok {
		t.Fatalf("Details is %T, want *builtin.TodosDetails", res.Details)
	}
	if !slices.Equal(details.Items, want) {
		t.Errorf("the UI reads %v, want %v", details.Items, want)
	}
}

func child(t *testing.T, node map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := node[key].(map[string]any)
	if !ok {
		t.Fatalf("the schema has no %q object under %v", key, slices.Sorted(maps.Keys(node)))
	}
	return value
}
