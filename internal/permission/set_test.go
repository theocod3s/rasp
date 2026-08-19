package permission_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/permission"
)

// bashRules is design §7.2's plan preset, cut down to the patterns that overlap
// each other, with one deny added so all three rules are represented.
func bashRules() permission.PatternRules {
	return permission.PatternRules{
		"*":               permission.RuleAsk,
		"find *":          permission.RuleAllow,
		"find * -delete*": permission.RuleAsk,
		"find * -exec*":   permission.RuleAsk,
		"git diff*":       permission.RuleAllow,
		"git push*":       permission.RuleAsk,
		"ls*":             permission.RuleAllow,
		"rm -rf*":         permission.RuleDeny,
	}
}

func compile(t *testing.T, set permission.PermissionSet) permission.Rules {
	t.Helper()

	rules, err := permission.Compile(set)
	if err != nil {
		t.Fatalf("Compile = %v, want the set to compile", err)
	}
	return rules
}

func execute(command string) permission.Request {
	return permission.Request{
		CallID:  "call-1",
		Tool:    "bash",
		Action:  permission.ActionExecute,
		Command: command,
	}
}

// TestTheMostSpecificPatternAnswers is design §7.3's rule over a table where
// every command is claimed by two patterns or more. The carve-backs are the
// point: `find *` allows the searches plan mode needs, and `find * -delete*`
// takes back the one spelling of find that destroys files.
func TestTheMostSpecificPatternAnswers(t *testing.T) {
	rules := compile(t, permission.PermissionSet{Bash: bashRules()})

	tests := []struct {
		name    string
		command string
		want    permission.Rule
	}{
		{
			name:    "a search runs under the broad allow",
			command: "find . -name '*.go'",
			want:    permission.RuleAllow,
		},
		{
			name:    "the carve-back wins over the allow it sits inside",
			command: "find . -type f -delete",
			want:    permission.RuleAsk,
		},
		{
			name:    "the second carve-back too",
			command: "find ./internal -exec rm {} +",
			want:    permission.RuleAsk,
		},
		{
			name:    "a carve-back does not leak onto a command that merely mentions it",
			command: "grep -r delete .",
			want:    permission.RuleAsk, // the catch-all, not the find rules
		},
		{
			name:    "an allow covers a path argument",
			command: "git diff internal/permission/set.go",
			want:    permission.RuleAllow,
		},
		{
			name:    "a sibling of an allowed subcommand is not allowed with it",
			command: "git push --force origin main",
			want:    permission.RuleAsk,
		},
		{
			name:    "a deny outranks the catch-all that also matches",
			command: "rm -rf dist",
			want:    permission.RuleDeny,
		},
		{
			name:    "a command matched by nothing but the catch-all asks",
			command: "cargo build --release",
			want:    permission.RuleAsk,
		},
		{
			name:    "a prefix that stops short of the pattern falls to the catch-all",
			command: "find",
			want:    permission.RuleAsk,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rules.Resolve(execute(tc.command)); got != tc.want {
				t.Errorf("Resolve(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

// TestOverlappingPatternsResolveTheSameWayEveryRun is the half of §7.3 that map
// iteration order would otherwise decide. Each command below is claimed by two
// patterns carrying different rules, and every compile has to pick the same one.
func TestOverlappingPatternsResolveTheSameWayEveryRun(t *testing.T) {
	commands := map[string]permission.Rule{
		"find . -name x":         permission.RuleAllow,
		"find . -type f -delete": permission.RuleAsk,
		"rm -rf dist":            permission.RuleDeny,
		"ls -la":                 permission.RuleAllow,
	}

	for range 50 {
		rules := compile(t, permission.PermissionSet{Bash: bashRules()})
		for command, want := range commands {
			if got := rules.Resolve(execute(command)); got != want {
				t.Fatalf("Resolve(%q) = %q, want %q", command, got, want)
			}
		}
	}
}

// TestATieBreaksLexicographically covers the other half of determinism: two
// patterns can pin down the same number of characters and both match, and the
// answer still cannot be a toss-up. The pair is contrived — real presets rarely
// tie — which is exactly why nothing else would catch the tie-break going.
func TestATieBreaksLexicographically(t *testing.T) {
	// Both score 7. "go *test" sorts first, so it answers where both match.
	rules := compile(t, permission.PermissionSet{Bash: permission.PatternRules{
		"go *test": permission.RuleDeny,
		"go test*": permission.RuleAllow,
	}})

	for range 50 {
		if got := rules.Resolve(execute("go test")); got != permission.RuleDeny {
			t.Fatalf("Resolve(%q) = %q, want %q", "go test", got, permission.RuleDeny)
		}
	}
	// The control: where only the second pattern matches, it still answers.
	if got := rules.Resolve(execute("go test ./...")); got != permission.RuleAllow {
		t.Errorf("Resolve(%q) = %q, want %q", "go test ./...", got, permission.RuleAllow)
	}
}

// TestEachActionResolvesAgainstItsOwnBucket walks the dispatch: four actions
// answered by a rule apiece, and one shared by bash and MCP, told apart by
// whether the call carries a command line.
func TestEachActionResolvesAgainstItsOwnBucket(t *testing.T) {
	rules := compile(t, permission.PermissionSet{
		Read:  permission.RuleAllow,
		Write: permission.RuleDeny,
		Edit:  permission.RuleAsk,
		Fetch: permission.RuleAllow,
		Bash:  permission.PatternRules{"*": permission.RuleDeny},
		MCP:   permission.PatternRules{"*": permission.RuleAsk, "mcp__github__get*": permission.RuleAllow},
	})

	tests := []struct {
		name string
		req  permission.Request
		want permission.Rule
	}{
		{
			name: "read",
			req:  permission.Request{Tool: "read", Action: permission.ActionRead, Path: "/foo/a.go"},
			want: permission.RuleAllow,
		},
		{
			name: "write",
			req:  permission.Request{Tool: "write", Action: permission.ActionWrite, Path: "/foo/a.go"},
			want: permission.RuleDeny,
		},
		{
			name: "edit",
			req:  permission.Request{Tool: "edit", Action: permission.ActionEdit, Path: "/foo/a.go"},
			want: permission.RuleAsk,
		},
		{
			name: "fetch",
			req:  permission.Request{Tool: "fetch", Action: permission.ActionFetch},
			want: permission.RuleAllow,
		},
		{
			name: "a command line is matched against the bash patterns",
			req:  execute("mcp__github__get_issue"), // the text alone does not move it
			want: permission.RuleDeny,
		},
		{
			name: "an execute with no command is matched against the MCP patterns, by tool name",
			req:  permission.Request{Tool: "mcp__github__get_issue", Action: permission.ActionExecute},
			want: permission.RuleAllow,
		},
		{
			name: "an MCP tool no pattern singles out takes the MCP catch-all",
			req:  permission.Request{Tool: "mcp__github__create_issue", Action: permission.ActionExecute},
			want: permission.RuleAsk,
		},
		{
			name: "an action this set has no bucket for is a question",
			req:  permission.Request{Tool: "teleport", Action: permission.Action("teleport")},
			want: permission.RuleAsk,
		},
		{
			name: "an execute naming neither a command nor a tool is a question",
			req:  permission.Request{Action: permission.ActionExecute},
			want: permission.RuleAsk,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := rules.Resolve(tc.req); got != tc.want {
				t.Errorf("Resolve(%v) = %q, want %q", tc.req, got, tc.want)
			}
		})
	}
}

// TestWhatASetLeavesUnsaidIsAQuestion is design §7.3's fail-closed default, in
// the three shapes a set can stay silent in.
func TestWhatASetLeavesUnsaidIsAQuestion(t *testing.T) {
	empty := compile(t, permission.PermissionSet{})
	if got := empty.Resolve(execute("rm -rf /")); got != permission.RuleAsk {
		t.Errorf("Resolve under an empty set = %q, want %q", got, permission.RuleAsk)
	}
	if got := empty.Resolve(permission.Request{Tool: "write", Action: permission.ActionWrite}); got != permission.RuleAsk {
		t.Errorf("Resolve of an unset action = %q, want %q", got, permission.RuleAsk)
	}

	// A table that matches nothing is not a table that allows everything.
	partial := compile(t, permission.PermissionSet{Bash: permission.PatternRules{"ls*": permission.RuleAllow}})
	if got := partial.Resolve(execute("rm -rf /")); got != permission.RuleAsk {
		t.Errorf("Resolve of an unmatched command = %q, want %q", got, permission.RuleAsk)
	}
}

// TestCompileNamesEveryFaultItFinds holds Compile to a whole report: every
// fault in the set, each naming the action or the pattern that carries it, in
// an order that does not move between runs.
func TestCompileNamesEveryFaultItFinds(t *testing.T) {
	broken := permission.PermissionSet{
		Read:  "Allow",
		Write: permission.RuleAllow,
		Bash: permission.PatternRules{
			"":        permission.RuleAllow,
			"rm -rf*": "Deny",
		},
		MCP: permission.PatternRules{"mcp__github__*": ""},
	}

	rules, err := permission.Compile(broken)
	if err == nil {
		t.Fatal("Compile = nil, want a set nothing validated to be refused")
	}
	if rules != nil {
		t.Errorf("Compile returned rules alongside its error, so a caller could install a set it refused")
	}

	for _, want := range []string{
		"read", `"Allow"`, // the misspelled action rule, and which action it is under
		"bash", "empty pattern", // the pattern that could never answer for a call
		`"rm -rf*"`, `"Deny"`, // the misspelled pattern rule, and which pattern carries it
		"mcp", `"mcp__github__*"`, // the pattern left with no rule at all
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Compile error does not mention %s:\n%v", want, err)
		}
	}

	// Reported in a fixed order, so one broken config reads the same way every
	// time it is loaded — the report is built by walking a map.
	for range 20 {
		_, again := permission.Compile(broken)
		if again.Error() != err.Error() {
			t.Fatalf("two compiles of one set reported different errors:\n%v\n%v", err, again)
		}
	}
}

// TestCompiledRulesAnswerThroughTheLadder is the seam itself: what Compile
// returns is what SetRules takes, and rungs 1 and 2 read it without knowing a
// pattern was ever involved.
func TestCompiledRulesAnswerThroughTheLadder(t *testing.T) {
	h := newHarness(t, permission.DecisionReject)
	h.SetRules(compile(t, permission.PermissionSet{Bash: bashRules()}))

	search := execute("find . -name '*.go'")
	if err := h.Ask(t.Context(), search); err != nil {
		t.Errorf("Ask for a command the preset allows = %v, want it allowed", err)
	}
	if len(h.prompts()) > 0 {
		t.Errorf("the user was asked about a command the preset allows")
	}

	destroy := execute("find . -type f -delete")
	destroy.CallID = "call-2"
	if err := h.Ask(t.Context(), destroy); !errors.Is(err, permission.ErrDenied) {
		t.Errorf("Ask for a carved-back command = %v, want the user's refusal", err)
	}
	if len(h.prompts()) != 1 {
		t.Errorf("the user was asked %d times, want 1: the carve-back did not reach the prompt",
			len(h.prompts()))
	}
}
