package permission

import "fmt"

// Rule is one answer a permission set gives about a request — the vocabulary
// the mode presets and the user's config are written in (design §7.1).
type Rule string

const (
	RuleAllow Rule = "allow"
	RuleAsk   Rule = "ask"
	RuleDeny  Rule = "deny"
)

// Action is what a call wants to do, and it names the bucket a permission set
// resolves against (design §7.1). A bash command and an MCP call are both
// ActionExecute: what separates those two is a pattern over the command line or
// over the tool name, not the verb.
type Action string

const (
	ActionRead    Action = "read"
	ActionWrite   Action = "write"
	ActionEdit    Action = "edit"
	ActionExecute Action = "execute"
	ActionFetch   Action = "fetch"
)

// Request is one tool call waiting on an answer.
type Request struct {
	// CallID is the tool_use id, and the handle Resolve answers by. A call has
	// at most one prompt open at a time; a second Ask under an id already
	// pending is refused rather than queued.
	CallID string

	Tool   string
	Action Action

	// Path is what the call touches, empty for a call that touches no file.
	// Resolving it — absolute, and through symlinks — belongs to the caller:
	// `./a.go`, `a.go` and a link to it are three grants otherwise, and the user
	// who approved one has approved neither of the others.
	Path string

	// Command is the literal string a bash rule is matched against: the whole
	// command line, unsplit (design §7.1).
	Command string
}

// String describes a request the way a denial has to read to the model that
// asked for it.
func (r Request) String() string {
	switch {
	case r.Command != "":
		return fmt.Sprintf("%s: %s", r.Tool, r.Command)
	case r.Path != "":
		return fmt.Sprintf("%s on %s", r.Tool, r.Path)
	default:
		return r.Tool
	}
}

// grant holds everything that identified the approved call rather than the tool
// alone: a grant for `/foo` must not quietly cover `/bar` (prd §6.6), and one
// that ignored the command would turn a single approved `rm -rf dist` into every
// command bash is ever handed.
type grant struct {
	tool    string
	action  Action
	path    string
	command string
}

func (r Request) grant() grant {
	return grant{tool: r.Tool, action: r.Action, path: r.Path, command: r.Command}
}
