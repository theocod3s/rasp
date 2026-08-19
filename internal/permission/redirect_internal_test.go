package permission

import "testing"

// TestWhichOperatorsSendOutputIntoAFile is the scanner on its own: what counts
// as a redirect, what only looks like one, and the quoting that separates them.
// The `want` column is the operator a denial quotes back, so an empty one means
// the command was left alone.
func TestWhichOperatorsSendOutputIntoAFile(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{
			name:    "the write nothing in the command looks like",
			command: `echo "package main" > auth.go`,
			want:    ">",
		},
		{
			name:    "an append is its own operator",
			command: "cat template.go >> handler.go",
			want:    ">>",
		},
		{
			name:    "a pipe into tee",
			command: "go vet ./... | tee report.txt",
			want:    "| tee",
		},
		{
			name:    "a pipe into tee with no space between them",
			command: "go vet ./... |tee report.txt",
			want:    "| tee",
		},
		{
			name:    "a pipe into tee behind a logical or",
			command: "go vet ./... || tee report.txt",
			want:    "| tee",
		},
		{
			name:    "a redirect at the very end of the line",
			command: "echo hi >",
			want:    ">",
		},
		{
			name:    "a file descriptor before it is still a write",
			command: "go vet ./... 2>errors.txt",
			want:    ">",
		},
		{
			name:    "output and errors together",
			command: "go build ./... &>build.log",
			want:    ">",
		},
		{
			name:    "a redirect inside a double-quoted argument, which is how -c is written",
			command: `bash -c "echo x > f"`,
			want:    ">",
		},
		{
			name:    "a redirect after an apostrophe inside double quotes",
			command: `echo "don't" > notes.md`,
			want:    ">",
		},
		{
			name:    "input redirection reads and is left alone",
			command: "wc -l < main.go",
		},
		{
			name:    "a pipe into something that is not tee",
			command: "git log --oneline | head -20",
		},
		{
			name:    "a pipe into a command that merely starts with tee",
			command: "cat x | teehee",
		},
		{
			name:    "an arrow inside single quotes is a search, not a redirect",
			command: `rg '->' internal/`,
		},
		{
			name:    "an escaped operator is an argument",
			command: `echo a \> b`,
		},
		{
			name:    "a command with nothing in it",
			command: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, found := redirection(tc.command)
			if found != (tc.want != "") {
				t.Fatalf("redirection(%q) found = %v, want %v", tc.command, found, tc.want != "")
			}
			if got != tc.want {
				t.Errorf("redirection(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

// TestTheGuardReachesOnlyCommandsUnderASetThatAskedForIt covers the three ways
// redirectionDenied declines to look: the set never turned the guard on, the
// request is not an execute, or it is an execute with no command line — an MCP
// call, whose tool name is not a shell command and must not be read as one.
func TestTheGuardReachesOnlyCommandsUnderASetThatAskedForIt(t *testing.T) {
	command := "echo x > f"

	on := &compiledSet{denyRedirection: true}
	if _, denied := on.redirectionDenied(Request{
		Tool: "bash", Action: ActionExecute, Command: command,
	}); !denied {
		t.Fatal("the guard passed a redirect under a set that turned it on")
	}

	tests := []struct {
		name string
		set  *compiledSet
		req  Request
	}{
		{
			name: "a set that left the guard off",
			set:  &compiledSet{},
			req:  Request{Tool: "bash", Action: ActionExecute, Command: command},
		},
		{
			name: "a write, whose path is not a command line",
			set:  on,
			req:  Request{Tool: "write", Action: ActionWrite, Path: command},
		},
		{
			name: "an MCP call, matched by tool name",
			set:  on,
			req:  Request{Tool: "mcp__shell__run > x", Action: ActionExecute},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if operator, denied := tc.set.redirectionDenied(tc.req); denied {
				t.Errorf("the guard refused %v over %q", tc.req, operator)
			}
		})
	}
}
