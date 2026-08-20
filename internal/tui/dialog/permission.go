package dialog

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// The question's first two columns, and the margin everything under it is set
// in by — the same two a tool card spends on its marker, so a prompt and the
// cards above it line up.
const (
	promptMarker = "? "
	promptIndent = "  "

	// choiceGap separates the answers. Wide, because each is a letter and a
	// phrase, and a single space between them reads as one sentence.
	choiceGap = "   "
)

// Permission is one approval request as the transcript draws it: what is being
// asked for, and the three answers it takes.
//
// Inline rather than floating over the conversation. The call it is about is the
// last thing on the screen, and an overlay drawn on top of the transcript hides
// exactly the context the answer depends on.
type Permission struct {
	Request permission.Request

	// Answered closes the question: the item keeps its place in the conversation
	// and draws nothing from then on. What happened next is the tool's own card,
	// and a line repeating the decision would sit there for the whole session.
	Answered bool

	// Background is the terminal's, and picks the palette.
	Background styles.Background
}

func (p Permission) Finished() bool { return p.Answered }

func (p Permission) Render(width int) string {
	if p.Answered || p.Request.Tool == "" {
		return ""
	}

	// Set in like everything under it and then giving its first two columns back
	// to the marker, so a headline too long for the terminal continues under
	// itself rather than at column zero.
	head := inset(p.Request.Tool+" needs your approval", width)

	var b strings.Builder
	b.WriteString(promptMarker + strings.TrimPrefix(head, promptIndent))
	for _, line := range subject(p.Request) {
		b.WriteString("\n" + inset(line, width))
	}

	// Styled a line at a time: Lip Gloss pads a multi-line string out to its
	// longest with spaces a reader would find again on anything they copy.
	muted := styles.For(p.Background).Muted
	for _, line := range strings.Split(wrap(choiceLine(), width-len(promptIndent)), "\n") {
		b.WriteString("\n" + promptIndent + muted.Render(line))
	}
	return b.String()
}

// subject is what the call would touch: the path, the command, or both when a
// request carries both — dropping either would leave the reader approving less
// than they were asked about.
func subject(req permission.Request) []string {
	var out []string
	if req.Path != "" {
		out = append(out, req.Path)
	}
	if req.Command != "" {
		out = append(out, req.Command)
	}
	return out
}

// answers are the three a prompt takes, in the order it lists them and under
// the keys it lists them by (design §5, step 15).
//
// The `always` label names the call the grant covers rather than saying
// "always", because a grant is keyed on the tool, the action, the path and the
// command together (design §7.7). A reader who takes it to mean every later
// edit has approved far more than they were asked about.
var answers = [...]struct {
	key      rune
	label    string
	decision permission.Decision
}{
	{'y', "once", permission.DecisionOnce},
	{'a', "always for this exact call", permission.DecisionAlways},
	{'n', "reject", permission.DecisionReject},
}

// Decide is the answer a key means, and whether it means one at all. Reported
// rather than defaulted: a key that answers nothing has to leave the question
// standing, or every stray press decides it.
func Decide(key rune) (permission.Decision, bool) {
	for _, a := range answers {
		if a.key == key {
			return a.decision, true
		}
	}
	return "", false
}

func choiceLine() string {
	parts := make([]string, 0, len(answers))
	for _, a := range answers {
		parts = append(parts, string(a.key)+" "+a.label)
	}
	return strings.Join(parts, choiceGap)
}

func inset(text string, width int) string {
	lines := strings.Split(wrap(text, width-len(promptIndent)), "\n")
	for i, line := range lines {
		lines[i] = promptIndent + line
	}
	return strings.Join(lines, "\n")
}

// wrap breaks text at width, measured in terminal cells rather than runes so a
// path of double-width characters still fits; a width nobody has reported yet
// breaks nothing. The space a break leaves behind goes with it, because a
// wrapped command is one a reader is likely to copy back out.
func wrap(text string, width int) string {
	if width <= 0 {
		return text
	}
	lines := strings.Split(ansi.Wrap(text, width, ""), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}
