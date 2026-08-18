package builtin

import (
	"context"
	"errors"
	"fmt"

	udiff "github.com/aymanbagabas/go-udiff"

	"github.com/theocod3s/rasp/internal/tool"
	"github.com/theocod3s/rasp/internal/tool/edit"
	"github.com/theocod3s/rasp/internal/workspace"
)

// EditInput is the edit tool's arguments. Every desc tag is prompt text and gets
// tuned like prompt text (design §3.2).
type EditInput struct {
	Path       string `json:"path"                  desc:"Path to the file, relative to the workspace root"`
	OldString  string `json:"old_string"            desc:"Exact text to find. Must appear exactly once unless replace_all is set. Include enough surrounding context to be unambiguous."`
	NewString  string `json:"new_string"            desc:"Replacement text"`
	ReplaceAll bool   `json:"replace_all,omitempty" desc:"Replace every occurrence instead of requiring a unique match"`
}

const editDescription = `Replace exact text in an existing file.

old_string must match the file byte for byte, indentation included, so copy it out of what you
read rather than retyping it. It must appear exactly once unless replace_all is set: more than
one occurrence is refused, and the answer is to include more of the surrounding lines, never to
guess which one was meant.`

// Edit returns the edit tool, reading and writing through ws.
func Edit(ws *workspace.Workspace) tool.Tool {
	return tool.New("edit", editDescription, func(_ context.Context, in EditInput) (tool.Result, error) {
		// The model's spelling of the path is what every error names; the resolved
		// one is for the diff, which the UI draws against the workspace root.
		path, err := ws.Resolve(in.Path)
		if err != nil {
			return editError("%v", err), nil
		}
		before, err := ws.ReadFile(in.Path)
		if err != nil {
			return editError("%v", err), nil
		}

		replacement, err := edit.Apply(string(before), in.OldString, in.NewString, in.ReplaceAll)
		if err != nil {
			return editRefusal(path, err), nil
		}

		// Diffing before writing keeps a failure here from being a file already
		// changed and a caller told the tool could not run.
		details, err := diffDetails(path, string(before), replacement.Text, replacement.Rung != edit.Exact)
		if err != nil {
			return tool.Result{}, err
		}

		// The mode is what a file created here would get, and edit does not create
		// files: old_string was found in this one, so it exists and keeps the mode
		// it has. It applies only if the file is deleted between the read and this
		// write, where a fresh file's default is the right answer anyway.
		if err := ws.WriteFile(in.Path, []byte(replacement.Text), 0o644); err != nil {
			return editError("%v", err), nil
		}

		noun := "replacements"
		if replacement.Count == 1 {
			noun = "replacement"
		}
		return tool.Result{
			// The diff itself stays in Details: it is what the UI draws, and sending
			// it to the model too would charge tokens for text it just wrote.
			Content: fmt.Sprintf("Edited %s: %d %s, +%d -%d.",
				path, replacement.Count, noun, details.Additions, details.Deletions),
			Title:   fmt.Sprintf("%s +%d -%d", path, details.Additions, details.Deletions),
			Details: details,
		}, nil
	})
}

// diffDetails renders the diff the UI draws. udiff.Unified is the obvious call
// and the wrong one: it turns the error handled here into log.Fatalf, which would
// end the process over one tool call and print to a stdout the TUI owns.
func diffDetails(path, before, after string, fuzzy bool) (*tool.DiffDetails, error) {
	u, err := udiff.ToUnifiedDiff("a/"+path, "b/"+path, before, udiff.Lines(before, after), udiff.DefaultContextLines)
	if err != nil {
		return nil, fmt.Errorf("diffing %s: %w", path, err)
	}

	details := &tool.DiffDetails{Path: path, Unified: u.String(), Fuzzy: fuzzy}
	for _, hunk := range u.Hunks {
		for _, line := range hunk.Lines {
			switch line.Kind {
			case udiff.Insert:
				details.Additions++
			case udiff.Delete:
				details.Deletions++
			}
		}
	}
	return details, nil
}

// editRefusal turns a ladder refusal into the sentence the model acts on. The
// default arm matters more than the ones above it: a rung added later reaches the
// model with its own words rather than being swallowed by a switch that has not
// heard of it.
func editRefusal(path string, err error) tool.Result {
	var ambiguous *edit.AmbiguousError
	switch {
	case errors.As(err, &ambiguous):
		return editError("old_string appears %d times in %s. Add the surrounding lines that tell "+
			"one occurrence from the others until it matches exactly one, or set replace_all to "+
			"change every one of them.", ambiguous.Count, path)
	case errors.Is(err, edit.ErrNotFound):
		return editError("old_string is not in %s. Read the file and copy old_string out of it, "+
			"whitespace and indentation included.", path)
	case errors.Is(err, edit.ErrUnchanged):
		return editError("old_string and new_string are identical, so this edit would leave %s "+
			"exactly as it is.", path)
	case errors.Is(err, edit.ErrEmpty):
		return editError("old_string is empty, so there is nothing to look for in %s. Give the "+
			"text to be replaced, or use write to create a file.", path)
	default:
		return editError("edit %s: %v", path, err)
	}
}

func editError(format string, args ...any) tool.Result {
	return tool.Result{IsError: true, Content: fmt.Sprintf(format, args...)}
}
