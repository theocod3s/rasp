package prompt

import (
	_ "embed"
	"strings"
	"time"

	"github.com/theocod3s/rasp/internal/llm"
)

// identity is block 1 of design §11. One text serves every model — see Input.Model.
//
//go:embed system.md
var identity string

// Input is everything one system prompt is assembled from. Nothing here reads
// the clock, the filesystem or git: the caller supplies every value, so the same
// Input always produces the same blocks.
type Input struct {
	// Model is the seam for a per-model variant and is deliberately unread.
	// design §11 keeps the door open; VISION.md sets the price of walking through
	// it at a measured failure and a measured fix.
	Model string

	// Instructions are the AGENTS.md compositions, outermost first. They are
	// stable within a session and so sit inside the cacheable prefix — which is
	// also the warning: a caller that rebuilds them each turn and lands on so
	// much as a different trailing newline re-bills the whole prefix.
	Instructions []string

	// ModeInstructions is what the active permission mode tells the model, as
	// text. The words are permission's; this package only decides where they go,
	// which is after the last breakpoint — Shift+Tab is a casual, frequent
	// action, and re-billing thousands of cached tokens for ~60 tokens of mode
	// text is not a trade worth making (design §7.6).
	ModeInstructions string

	Env Env
}

// Env is the volatile environment, rendered as the final block. An empty field
// is omitted rather than rendered blank, so a caller that cannot answer "which
// git branch" tells the model nothing instead of something false.
type Env struct {
	Cwd       string
	Platform  string
	GitBranch string

	// Now renders as a date. The zero time omits the line.
	Now time.Time
}

// Build assembles the system prompt as ordered blocks: the stable text first,
// carrying the cache breakpoint, then everything volatile after it. The
// Anthropic cache is a prefix match, so one byte changed ahead of the breakpoint
// invalidates every token behind it (design §11).
//
// Tool descriptions belong to that prefix too and are not blocks here: they
// reach the wire through Request.Tools, from the per-turn registry snapshot that
// keeps them in a stable order (design §3.3). Restating them as system text
// would send every schema twice.
//
// No block is emitted with empty text, which providers reject.
func Build(in Input) []llm.SystemBlock {
	blocks := []llm.SystemBlock{{Text: strings.TrimSpace(identity)}}
	for _, text := range in.Instructions {
		if text = strings.TrimSpace(text); text != "" {
			blocks = append(blocks, llm.SystemBlock{Text: text})
		}
	}

	// On the last stable block rather than at a fixed index: with no AGENTS.md
	// there is only the identity block, and a flag hard-coded to the second one
	// would leave the request with no breakpoint at all — cacheable content that
	// is never cached, visible only as cache_read_input_tokens pinned at zero.
	blocks[len(blocks)-1].Cache = true

	for _, text := range []string{in.ModeInstructions, in.Env.text()} {
		if text = strings.TrimSpace(text); text != "" {
			blocks = append(blocks, llm.SystemBlock{Text: text})
		}
	}
	return blocks
}

func (e Env) text() string {
	date := ""
	if !e.Now.IsZero() {
		date = e.Now.Format(time.DateOnly)
	}

	var lines []string
	for _, field := range []struct{ label, value string }{
		{"Working directory", e.Cwd},
		{"Platform", e.Platform},
		{"Git branch", e.GitBranch},
		{"Date", date},
	} {
		if field.value != "" {
			lines = append(lines, field.label+": "+field.value)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "Environment:\n" + strings.Join(lines, "\n")
}
