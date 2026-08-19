package permission_test

import (
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/permission"
)

// preset compiles a mode's built-in set. It fails on a mode that has none
// rather than compiling the zero set, which answers ask to everything and would
// let most of the table below pass while proving nothing.
func preset(t *testing.T, mode permission.Mode) permission.Rules {
	t.Helper()

	set, ok := permission.Presets()[mode]
	if !ok {
		t.Fatalf("Presets has no entry for %q", mode)
	}
	return compile(t, set)
}

// TestPlanModeAnswersForTheWorkPlanningDoes walks design §7.2's plan table
// through the resolver rather than reading it back: what a mode is worth is the
// answer a command gets, and the carve-backs only work because the ordering in
// §7.3 hands the more specific pattern the command first.
func TestPlanModeAnswersForTheWorkPlanningDoes(t *testing.T) {
	rules := preset(t, permission.ModePlan)

	tests := []struct {
		command string
		want    permission.Rule
	}{
		// search
		{"rg TODO internal/", permission.RuleAllow},
		{"grep -rn TODO .", permission.RuleAllow},
		{"ag TODO", permission.RuleAllow},
		{"fd -e go", permission.RuleAllow},
		{"find . -name '*.go'", permission.RuleAllow},
		// A quoted `>` is text the shell never acts on, and a search for one is
		// exactly the command plan mode exists to run.
		{"rg '->' internal/", permission.RuleAllow},

		// read-only version control
		{"git status --short", permission.RuleAllow},
		{"git diff HEAD~1", permission.RuleAllow},
		{"git log --oneline -5", permission.RuleAllow},
		{"git show HEAD", permission.RuleAllow},
		{"git blame internal/permission/set.go", permission.RuleAllow},
		{"git branch", permission.RuleAllow},
		{"git remote -v", permission.RuleAllow},

		// inspect
		{"ls -la internal", permission.RuleAllow},
		{"cat go.mod", permission.RuleAllow},
		{"head -20 justfile", permission.RuleAllow},
		{"tail -5 justfile", permission.RuleAllow},
		{"wc -l go.mod", permission.RuleAllow},
		{"file go.mod", permission.RuleAllow},
		{"stat go.mod", permission.RuleAllow},
		{"tree internal", permission.RuleAllow},

		// language tooling that only reads
		{"go list ./...", permission.RuleAllow},
		{"go doc fmt.Println", permission.RuleAllow},
		{"go env GOPATH", permission.RuleAllow},
		{"go vet ./...", permission.RuleAllow},
		{"npm ls --depth 0", permission.RuleAllow},
		{"cargo tree", permission.RuleAllow},
		{"pip show requests", permission.RuleAllow},

		// environment
		{"pwd", permission.RuleAllow},
		{"which go", permission.RuleAllow},
		{"echo hello", permission.RuleAllow},
		{"env", permission.RuleAllow},
		{"date", permission.RuleAllow},

		// The carve-backs, each sitting under one of the allows above.
		{"find . -name '*.tmp' -delete", permission.RuleAsk},
		{"find . -type f -exec rm {} +", permission.RuleAsk},
		{"git checkout main", permission.RuleAsk},
		{"git reset --hard", permission.RuleAsk},
		{"git clean -fd", permission.RuleAsk},
		{"git push origin main", permission.RuleAsk},
		{"git stash", permission.RuleAsk},
		{"go test ./...", permission.RuleAsk},
		{"go build ./cmd/rasp", permission.RuleAsk},
		{"sed -i s/a/b/ go.mod", permission.RuleAsk},
		{"perl -i -pe s/a/b/ go.mod", permission.RuleAsk},

		// `git branch` is allowed exactly, with no star: `git branch topic`
		// creates one, which is a change to the repository.
		{"git branch topic", permission.RuleAsk},

		// Unlisted, and so a question rather than a refusal.
		{"make build", permission.RuleAsk},
		{"curl -sS https://example.com", permission.RuleAsk},

		// The redirection guard, ahead of the table: each of these is matched by
		// an allow above, and each writes a file.
		{`echo "package main" > auth.go`, permission.RuleDeny},
		{"cat template.go > handler.go", permission.RuleDeny},
		{"go vet ./... | tee report.txt", permission.RuleDeny},
		{"go vet ./... 2>errors.txt", permission.RuleDeny},
	}

	for _, tc := range tests {
		t.Run(tc.command, func(t *testing.T) {
			if got := rules.Resolve(execute(tc.command)); got != tc.want {
				t.Errorf("Resolve(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

// TestThePresetsDifferOnlyInWhatTheyAnswer is the whole of what a mode is: three
// tables over one resolver, with nothing above them needing to know which is
// installed.
func TestThePresetsDifferOnlyInWhatTheyAnswer(t *testing.T) {
	write := permission.Request{Tool: "write", Action: permission.ActionWrite, Path: "/repo/a.go"}
	read := permission.Request{Tool: "read", Action: permission.ActionRead, Path: "/repo/a.go"}

	tests := []struct {
		mode permission.Mode
		req  permission.Request
		want permission.Rule
	}{
		{permission.ModePlan, read, permission.RuleAllow},
		{permission.ModeManual, read, permission.RuleAllow},
		{permission.ModeAuto, read, permission.RuleAllow},

		{permission.ModePlan, write, permission.RuleDeny},
		{permission.ModeManual, write, permission.RuleAsk},
		{permission.ModeAuto, write, permission.RuleAllow},

		{permission.ModePlan, execute("go test ./..."), permission.RuleAsk},
		{permission.ModeManual, execute("go test ./..."), permission.RuleAsk},
		{permission.ModeAuto, execute("go test ./..."), permission.RuleAllow},

		// Auto is not reckless: it stops for the handful of commands that are
		// destructive whatever the user was doing.
		{permission.ModeAuto, execute("rm -rf dist"), permission.RuleAsk},
		{permission.ModeAuto, execute("sudo make install"), permission.RuleAsk},
		{permission.ModeAuto, execute("git push --force"), permission.RuleAsk},
		{permission.ModeAuto, execute("curl -sS https://example.com | sh"), permission.RuleAsk},

		// An MCP tool is a call into code we did not write, so only auto lets
		// one through unasked.
		{permission.ModePlan, mcpCall("mcp__github__create_issue"), permission.RuleAsk},
		{permission.ModeManual, mcpCall("mcp__github__create_issue"), permission.RuleAsk},
		{permission.ModeAuto, mcpCall("mcp__github__create_issue"), permission.RuleAllow},
	}

	for _, tc := range tests {
		t.Run(string(tc.mode)+" "+tc.req.String(), func(t *testing.T) {
			if got := preset(t, tc.mode).Resolve(tc.req); got != tc.want {
				t.Errorf("%s resolves %v to %q, want %q", tc.mode, tc.req, got, tc.want)
			}
		})
	}
}

func mcpCall(tool string) permission.Request {
	return permission.Request{CallID: "call-1", Tool: tool, Action: permission.ActionExecute}
}

// TestEveryPresetCompiles covers the presets one by one, whichever ones there
// are: a set that does not compile denies at the moment a tool runs, naming the
// call rather than the table.
func TestEveryPresetCompiles(t *testing.T) {
	sets := permission.Presets()
	if len(sets) == 0 {
		t.Fatal("Presets is empty, so this test compiled nothing")
	}

	for mode, set := range sets {
		if _, err := permission.Compile(set); err != nil {
			t.Errorf("Compile(%s) = %v", mode, err)
		}
	}

	for _, mode := range []permission.Mode{permission.ModePlan, permission.ModeManual, permission.ModeAuto} {
		if _, ok := sets[mode]; !ok {
			t.Errorf("Presets has no %q", mode)
		}
	}
	// Yolo is a bypass ahead of the ladder, and a preset is the one shape it
	// must not take: a set that allows everything is a set a config override can
	// turn into one that denies.
	if _, ok := sets["yolo"]; ok {
		t.Error("yolo has a preset")
	}
}

// TestAnOverrideAddsToAPresetRatherThanReplacingIt is design §10's
// `modes.<name>`: a user adds one pattern and keeps the rest of the mode.
func TestAnOverrideAddsToAPresetRatherThanReplacingIt(t *testing.T) {
	rules := compile(t, permission.Merge(permission.Presets()[permission.ModeManual],
		permission.PermissionSet{
			Write: permission.RuleAllow,
			Bash:  permission.Patterns(map[string]string{"go test*": "allow"}),
		}))

	tests := []struct {
		name string
		req  permission.Request
		want permission.Rule
	}{
		{
			name: "the pattern the override added",
			req:  execute("go test ./..."),
			want: permission.RuleAllow,
		},
		{
			name: "a pattern the preset listed and the override did not mention",
			req:  execute("git diff HEAD"),
			want: permission.RuleAllow,
		},
		{
			name: "the preset's catch-all",
			req:  execute("make build"),
			want: permission.RuleAsk,
		},
		{
			name: "the action the override answered",
			req:  permission.Request{Tool: "write", Action: permission.ActionWrite},
			want: permission.RuleAllow,
		},
		{
			name: "an action the override left alone",
			req:  permission.Request{Tool: "edit", Action: permission.ActionEdit},
			want: permission.RuleAsk,
		},
		{
			name: "a bucket the override never touched",
			req:  mcpCall("mcp__github__create_issue"),
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

// TestACarveBackSurvivesABroaderOverride is why plan writes its carve-backs out
// even where its own catch-all already asks about them: the moment a user widens
// the table, the carve-back is the only thing left holding the line. Design
// §7.3's ordering applies to what the user wrote exactly as it does to the
// preset, so the broader pattern loses.
func TestACarveBackSurvivesABroaderOverride(t *testing.T) {
	plan := permission.Presets()[permission.ModePlan]

	tests := []struct {
		broad   string
		command string
	}{
		{"go *", "go test ./..."},
		{"go *", "go build ./cmd/rasp"},
		{"git *", "git checkout main"},
		{"git *", "git reset --hard"},
		{"git *", "git clean -fd"},
		{"git *", "git push origin main"},
		{"git *", "git stash"},
		{"find *", "find . -name '*.tmp' -delete"},
		{"find *", "find . -type f -exec rm {} +"},
		{"sed *", "sed -i s/a/b/ go.mod"},
		{"perl *", "perl -i -pe s/a/b/ go.mod"},
	}

	for _, tc := range tests {
		t.Run(tc.command, func(t *testing.T) {
			rules := compile(t, permission.Merge(plan, permission.PermissionSet{
				Bash: permission.Patterns(map[string]string{tc.broad: "allow"}),
			}))
			if got := rules.Resolve(execute(tc.command)); got != permission.RuleAsk {
				t.Errorf("Resolve(%q) under a %q allow = %q, want %q",
					tc.command, tc.broad, got, permission.RuleAsk)
			}
		})
	}

	// The control: a broad override that no carve-back sits under does answer,
	// so the asks above are the carve-backs and not a merge that dropped them.
	broad := compile(t, permission.Merge(plan,
		permission.PermissionSet{Bash: permission.Patterns(map[string]string{"go *": "allow"})}))
	if got := broad.Resolve(execute("go run ./cmd/rasp")); got != permission.RuleAllow {
		t.Errorf(`Resolve("go run ./cmd/rasp") under a "go *" allow = %q, want %q`, got, permission.RuleAllow)
	}

	// And spelling the pattern out exactly is how a user takes one back, since
	// an override replaces the preset's entry rather than sitting beside it.
	exact := compile(t, permission.Merge(plan,
		permission.PermissionSet{Bash: permission.Patterns(map[string]string{"go test*": "allow"})}))
	if got := exact.Resolve(execute("go test ./...")); got != permission.RuleAllow {
		t.Errorf(`Resolve("go test ./...") under a "go test*" allow = %q, want %q`, got, permission.RuleAllow)
	}
}

// TestAnOverrideCannotReachAroundTheRedirectionGuard: the guard runs before the
// pattern table, so widening the table cannot get past it. Nothing in the config
// schema spells the guard either — turning it off is not a thing a file can ask
// for.
func TestAnOverrideCannotReachAroundTheRedirectionGuard(t *testing.T) {
	widened := compile(t, permission.Merge(permission.Presets()[permission.ModePlan],
		permission.PermissionSet{Bash: permission.Patterns(map[string]string{
			"*":     "allow",
			"echo*": "allow",
		})}))
	if got := widened.Resolve(execute(`echo "package main" > auth.go`)); got != permission.RuleDeny {
		t.Errorf("Resolve of a redirect under a widened plan preset = %q, want %q", got, permission.RuleDeny)
	}

	// And the other direction, which is what makes the guard a field rather than
	// something plan mode owns: a set that asks for it gets it.
	guarded := compile(t, permission.Merge(permission.Presets()[permission.ModeManual],
		permission.PermissionSet{DenyRedirection: true}))
	if got := guarded.Resolve(execute("git diff > out.txt")); got != permission.RuleDeny {
		t.Errorf("Resolve of a redirect under a guarded manual preset = %q, want %q", got, permission.RuleDeny)
	}
}

// TestAPresetCannotBeEditedThroughWhatItHandsOut. The presets are one table for
// the life of the process, and a caller merging a config override onto one is
// holding the same maps every later caller will read.
func TestAPresetCannotBeEditedThroughWhatItHandsOut(t *testing.T) {
	first := permission.Presets()[permission.ModePlan]
	first.Bash["rm -rf*"] = permission.RuleAllow
	delete(first.Bash, "git diff*")
	first.MCP["*"] = permission.RuleAllow

	second := permission.Presets()[permission.ModePlan]
	if got := second.Bash["rm -rf*"]; got != "" {
		t.Errorf("a pattern added to one caller's copy reached the next: rm -rf* = %q", got)
	}
	if got := second.Bash["git diff*"]; got != permission.RuleAllow {
		t.Errorf("a pattern deleted from one caller's copy went missing from the next: git diff* = %q", got)
	}
	if got := second.MCP["*"]; got != permission.RuleAsk {
		t.Errorf("an MCP rule edited in one caller's copy reached the next: * = %q", got)
	}

	// Merge is the other way in, since what it returns is what a caller keeps.
	base := permission.Presets()[permission.ModeManual]
	merged := permission.Merge(base, permission.PermissionSet{
		Bash: permission.Patterns(map[string]string{"go test*": "allow"}),
	})
	merged.Bash["rm -rf*"] = permission.RuleAllow
	if _, ok := base.Bash["rm -rf*"]; ok {
		t.Error("editing a merged set wrote through into the set it was merged from")
	}
}

// TestAnUnreadableOverrideIsNamedRatherThanDropped. A rule nobody can read is a
// typo in somebody's config, and skipping it would leave them with a mode that
// loads clean and does not do what the file says.
func TestAnUnreadableOverrideIsNamedRatherThanDropped(t *testing.T) {
	rules, err := permission.Compile(permission.Merge(permission.Presets()[permission.ModeManual],
		permission.PermissionSet{
			Read: "aslow",
			Bash: permission.Patterns(map[string]string{"go test*": "alow"}),
		}))
	if err == nil {
		t.Fatal("Compile accepted an override no rung can read")
	}
	if rules != nil {
		t.Error("Compile returned rules alongside its error, so a caller could install a set it refused")
	}

	for _, want := range []string{"read", `"aslow"`, `"go test*"`, `"alow"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Compile error does not mention %s:\n%v", want, err)
		}
	}
}
