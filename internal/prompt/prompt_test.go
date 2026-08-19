package prompt_test

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/prompt"
)

// base is a fully populated turn: two AGENTS.md compositions, a mode, and an
// environment. Every test edits a copy of it, so what a test changes is the only
// thing that differs between two prompts.
func base() prompt.Input {
	return prompt.Input{
		Model:            "claude-sonnet-4-5",
		Instructions:     []string{"Repo rules: run the tests.", "Package rules: no cgo."},
		ModeInstructions: "Plan mode. Propose a plan; do not edit files.",
		Env: prompt.Env{
			Cwd:       "/repo",
			Platform:  "darwin",
			GitBranch: "main",
			Now:       time.Date(2026, 8, 19, 9, 30, 0, 0, time.UTC),
		},
	}
}

func TestStableBlocksComeFirstAndCarryTheOnlyBreakpoint(t *testing.T) {
	blocks := prompt.Build(base())

	if len(blocks) != 5 {
		t.Fatalf("Build produced %d blocks, want 5 (identity, two instructions, mode, environment): %v", len(blocks), blocks)
	}
	if !strings.HasPrefix(blocks[0].Text, "You are rasp") {
		t.Errorf("block 0 is %q; the identity prompt comes first", blocks[0].Text)
	}

	var flagged []int
	for i, block := range blocks {
		if block.Cache {
			flagged = append(flagged, i)
		}
	}
	if want := []int{2}; !slices.Equal(flagged, want) {
		t.Fatalf("blocks %v carry a breakpoint, want %v — it belongs on the last stable block, "+
			"which here is the innermost instructions", flagged, want)
	}

	if !strings.Contains(blocks[3].Text, "Plan mode") {
		t.Errorf("block 3 is %q, want the mode instructions", blocks[3].Text)
	}
	if !strings.Contains(blocks[4].Text, "Working directory: /repo") {
		t.Errorf("block 4 is %q, want the environment", blocks[4].Text)
	}
}

// TestIdenticalTurnsProduceAnIdenticalPrefix is the point of the whole
// arrangement: the same inputs twice have to hand the provider the same bytes,
// or the cache is a prefix match over something that keeps moving.
func TestIdenticalTurnsProduceAnIdenticalPrefix(t *testing.T) {
	first, second := prompt.Build(base()), prompt.Build(base())

	if a, b := bytesOf(prefix(t, first)), bytesOf(prefix(t, second)); a != b {
		t.Fatalf("two identical turns produced different cacheable prefixes:\n%q\n%q", a, b)
	}
}

// TestVolatileChangesLeaveThePrefixUntouched is the other half, and the half
// that catches a leak: each of these changes between turns, so any of them
// rendered above the breakpoint would re-bill the whole prefix on every request
// — expensive, and invisible short of reading cache_read_input_tokens.
func TestVolatileChangesLeaveThePrefixUntouched(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*prompt.Input)
	}{
		{"cwd", func(in *prompt.Input) { in.Env.Cwd = "/somewhere/else" }},
		{"platform", func(in *prompt.Input) { in.Env.Platform = "windows" }},
		{"git branch", func(in *prompt.Input) { in.Env.GitBranch = "topic" }},
		{"date", func(in *prompt.Input) { in.Env.Now = in.Env.Now.AddDate(0, 0, 1) }},
		{"mode", func(in *prompt.Input) { in.ModeInstructions = "Auto mode. Edit and run freely." }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edited := base()
			tc.edit(&edited)

			was, now := prompt.Build(base()), prompt.Build(edited)
			if a, b := bytesOf(prefix(t, was)), bytesOf(prefix(t, now)); a != b {
				t.Errorf("changing the %s moved the cacheable prefix:\n%q\n%q", tc.name, a, b)
			}
			// Without this the test passes for an edit that changed nothing at
			// all — a field Build never reads, or one this table sets to the
			// value it already had.
			if a, b := bytesOf(was), bytesOf(now); a == b {
				t.Errorf("changing the %s changed no block; the edit never reached the prompt, "+
					"so the assertion above compared two identical inputs", tc.name)
			}
		})
	}
}

// TestPrefixHoldsNothingButTheTextItWasGiven closes what comparing two prompts
// cannot see. Two builds a microsecond apart agree on today's date, so a Build
// that called time.Now() or os.Getwd() itself would satisfy every assertion
// above while blowing the cache on the first turn after midnight.
func TestPrefixHoldsNothingButTheTextItWasGiven(t *testing.T) {
	blocks := prompt.Build(prompt.Input{})

	if n := len(blocks); n != 1 {
		t.Fatalf("an empty Input produced %d blocks, want 1 — with nothing supplied, the identity "+
			"prompt is the whole of it: %v", n, blocks)
	}
	// It also has to be flagged: with no AGENTS.md there is no later stable block
	// to carry the breakpoint, and a prompt with none is one nothing caches.
	if !blocks[0].Cache {
		t.Error("the identity block carries no breakpoint when it is the only stable block")
	}

	// Against the file rather than against a second Build: comparing two prompts
	// is what cannot see this, and an assertion on the block count alone reads as
	// a check while passing for anything appended to the text.
	embedded, err := os.ReadFile("system.md")
	if err != nil {
		t.Fatalf("reading the embedded prompt: %v", err)
	}
	if got, want := blocks[0].Text, strings.TrimSpace(string(embedded)); got != want {
		t.Errorf("the identity block is not system.md as it stands on disk, so Build reached for "+
			"something itself and put it above the breakpoint:\n got %q\nwant %q", got, want)
	}
}

func TestInstructionsSitInsideThePrefix(t *testing.T) {
	for _, tc := range []struct {
		name         string
		instructions []string
	}{
		{"different text", []string{"Repo rules: run the tests.", "Package rules: no cgo, ever."}},
		// The same characters in one block rather than two. Block boundaries go
		// on the wire, so a composition that merges two AGENTS.md files is a
		// different prefix and costs a cache miss like any other edit.
		{"same text, split differently", []string{"Repo rules: run the tests.Package rules: no cgo."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			edited := base()
			edited.Instructions = tc.instructions

			was, now := prompt.Build(base()), prompt.Build(edited)
			if a, b := bytesOf(prefix(t, was)), bytesOf(prefix(t, now)); a == b {
				t.Fatalf("changing the instructions left the cacheable prefix identical (%q); they "+
					"are stable within a session and belong above the breakpoint, so this says "+
					"either that they landed below it or that the prefix is a constant", a)
			}
		})
	}
}

// TestModelDoesNotChangeThePrompt records the state of the seam: one prompt
// serves every model, and Input.Model is where a measured variant would land.
func TestModelDoesNotChangeThePrompt(t *testing.T) {
	other := base()
	other.Model = "gpt-5"

	if a, b := bytesOf(prompt.Build(base())), bytesOf(prompt.Build(other)); a != b {
		t.Errorf("the model id changed the prompt:\n%q\n%q", a, b)
	}
}

// TestNoBlockIsEmpty guards the shape an adapter refuses outright: a system
// block with no text is a 400 from the provider, and Anthropic's adapter fails
// the request rather than dropping the block, so an empty one wedges every turn.
func TestNoBlockIsEmpty(t *testing.T) {
	blank := prompt.Input{
		Instructions:     []string{"   ", "\n\t"},
		ModeInstructions: " \n ",
		Env:              prompt.Env{Cwd: "  "},
	}
	for _, blocks := range [][]llm.SystemBlock{prompt.Build(prompt.Input{}), prompt.Build(blank)} {
		for i, block := range blocks {
			if strings.TrimSpace(block.Text) == "" {
				t.Errorf("block %d has no text: %v", i, blocks)
			}
		}
	}
}

func TestEnvironmentOmitsWhatTheCallerCouldNotAnswer(t *testing.T) {
	in := base()
	in.Env = prompt.Env{Cwd: "/repo", Platform: "linux"}

	blocks := prompt.Build(in)
	env := blocks[len(blocks)-1].Text

	if strings.Contains(env, "Git branch") || strings.Contains(env, "Date") {
		t.Errorf("the environment block names a field nothing supplied:\n%s", env)
	}
	if !strings.Contains(env, "Platform: linux") {
		t.Errorf("the environment block dropped a field that was supplied:\n%s", env)
	}
}

// prefix is the run of blocks a provider caches: everything up to and including
// the last one carrying a breakpoint.
func prefix(t *testing.T, blocks []llm.SystemBlock) []llm.SystemBlock {
	t.Helper()

	last := -1
	for i, block := range blocks {
		if block.Cache {
			last = i
		}
	}
	if last < 0 {
		t.Fatalf("no block carries a breakpoint, so there is no cacheable prefix and every "+
			"comparison of one would be a comparison of nothing: %v", blocks)
	}
	return blocks[:last+1]
}

// bytesOf renders blocks as the bytes they contribute, separated by one no
// prompt holds, so two prompts differing only in where the text was split still
// compare unequal. Where the breakpoints sit is prefix's half of the comparison.
func bytesOf(blocks []llm.SystemBlock) string {
	var out strings.Builder
	for _, block := range blocks {
		out.WriteString(block.Text)
		out.WriteByte(0)
	}
	return out.String()
}
