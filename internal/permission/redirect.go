package permission

import (
	"fmt"
	"strings"
)

// redirectionDenied names the operator the guard refused this request over.
// Resolve and Explain both go through it, so a denial cannot explain something
// other than what it refused.
func (c *compiledSet) redirectionDenied(req Request) (string, bool) {
	if !c.denyRedirection || req.Action != ActionExecute || req.Command == "" {
		return "", false
	}
	return redirection(req.Command)
}

// redirection names the operator in cmd that would send a command's output into
// a file — `>`, `>>`, or a pipe into tee — and reports whether one is there.
//
// Only single quotes are inert, since they are the one construct that makes
// every character between them literal: `rg '->' .` writes nothing and has to
// run, and a scan of the whole string turns it away every time. A `>` inside
// double quotes is read as a redirect even though the shell would not, because
// `bash -c "... > f"` is the spelling that reaches a file; the price is refusing
// `grep "=>" x`, which costs a prompt rather than a tree.
//
// A digit before `>` is read as one too, where design §7.3a's sketch excludes
// it: `2>` is a file descriptor, and `go vet ./... 2>errors.txt` writes a file
// exactly as `>` does. What stays out of reach is anything that hides the
// operator from a reader of the command text — `sh -c '... > f'`, `python -c`,
// xargs, a script — and those fall to the pattern table, which singles none of
// them out and so asks.
func redirection(cmd string) (string, bool) {
	const (
		bare = iota
		single
		double
	)

	state := bare
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch state {
		case single:
			if c == '\'' {
				state = bare
			}
			continue
		case double:
			// This state earns its place on one character: an apostrophe in here
			// is literal, and without it `echo "don't" > f` reads as single-quoted
			// from the apostrophe on and its redirect goes unseen.
			if c == '"' {
				state = bare
			}
			if c != '>' && c != '|' {
				continue
			}
		}

		switch {
		case c == '\'':
			state = single
		case c == '"':
			state = double
		case c == '\\':
			i++ // the next character is literal, so `echo a \> b` redirects nothing
		case c == '>':
			if i+1 < len(cmd) && cmd[i+1] == '>' {
				return ">>", true
			}
			return ">", true
		case c == '|' && teeFollows(cmd[i+1:]):
			return "| tee", true
		}
	}
	return "", false
}

func teeFollows(afterPipe string) bool {
	rest, ok := strings.CutPrefix(strings.TrimLeft(afterPipe, " \t"), "tee")
	if !ok || rest == "" {
		return ok
	}
	// `tee-hee` and `tee.sh` are other commands; `tee -a f` is not.
	switch c := rest[0]; {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
		c == '_', c == '-', c == '.':
		return false
	}
	return true
}

// redirectionDenial teaches rather than refuses, and its closing sentence is the
// load-bearing one: tighten this wording into a promise and rasp starts claiming
// a guarantee that a read of the command text cannot make.
func redirectionDenial(req Request, operator string) string {
	return fmt.Sprintf("`%s` in `%s` sends the output into a file, and the active mode refuses "+
		"that: it is a mode for reading and proposing, and a redirect changes your tree while "+
		"no part of the command looks like a write. Switch to manual or auto to run this as it "+
		"stands, or propose the change and let a write or edit tool make it. This is a read of "+
		"the command text — it stops the accident, not every route a shell has to a file.",
		operator, req)
}
