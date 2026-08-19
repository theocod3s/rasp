package permission

import "maps"

// Mode names a permission preset. A mode is nothing but the set it stands for,
// which is what keeps the loop that runs the tools from branching on one
// (design §7).
type Mode string

const (
	ModePlan   Mode = "plan"
	ModeManual Mode = "manual" // the default
	ModeAuto   Mode = "auto"
)

// Presets is design §7.2's table: what each mode answers before a config
// override is merged onto it. A fresh copy per call, so a caller editing the set
// it was handed edits nobody else's.
//
// Yolo has no entry here, and the absence is the design: it arms a bypass ahead
// of the ladder rather than naming the most permissive set (see Service.yolo).
// A mode with no entry comes back missing, for the caller to answer for, rather
// than resolving to a set that allows.
func Presets() map[Mode]PermissionSet {
	out := make(map[Mode]PermissionSet, len(presets))
	for mode, set := range presets {
		set.Bash = maps.Clone(set.Bash)
		set.MCP = maps.Clone(set.MCP)
		out[mode] = set
	}
	return out
}

var presets = map[Mode]PermissionSet{
	ModePlan: {
		Read: RuleAllow, Write: RuleDeny, Edit: RuleDeny, Fetch: RuleAsk,

		// Everything this mode allows is a command that only reads, and a `>`
		// turns any of them into a write (design §7.3a).
		DenyRedirection: true,

		Bash: PatternRules{
			// An unlisted command asks rather than fails: this mode is for
			// investigating, and the list below cannot anticipate every tool a
			// repository is built with.
			"*": RuleAsk,

			// search
			"rg*": RuleAllow, "grep*": RuleAllow, "ag*": RuleAllow,
			"fd*": RuleAllow, "find *": RuleAllow,

			// read-only version control
			"git status*": RuleAllow, "git diff*": RuleAllow,
			"git log*": RuleAllow, "git show*": RuleAllow,
			"git blame*": RuleAllow, "git branch": RuleAllow,
			"git remote -v": RuleAllow,

			// inspect
			"ls*": RuleAllow, "cat*": RuleAllow, "head*": RuleAllow,
			"tail*": RuleAllow, "wc*": RuleAllow, "file *": RuleAllow,
			"stat*": RuleAllow, "tree*": RuleAllow,

			// language tooling that only reads
			"go list*": RuleAllow, "go doc*": RuleAllow,
			"go env*": RuleAllow, "go vet*": RuleAllow,
			"npm ls*": RuleAllow, "cargo tree*": RuleAllow, "pip show*": RuleAllow,

			// environment
			"pwd": RuleAllow, "which*": RuleAllow, "echo*": RuleAllow,
			"env": RuleAllow, "date": RuleAllow,

			// Carve-backs. Each pins down more characters than the allow it sits
			// under, so design §7.3's ordering hands it the command first — and
			// each is here because the broader spelling is one this mode wants.
			"find * -delete*": RuleAsk, "find * -exec*": RuleAsk,
			"git checkout*": RuleAsk, "git reset*": RuleAsk,
			"git clean*": RuleAsk, "git push*": RuleAsk, "git stash*": RuleAsk,
			"go test*":  RuleAsk, // runs whatever the _test.go files run
			"go build*": RuleAsk, // writes build artifacts
			"sed -i*":   RuleAsk, "perl -i*": RuleAsk,
		},
		MCP: PatternRules{"*": RuleAsk},
	},

	ModeManual: {
		Read: RuleAllow, Write: RuleAsk, Edit: RuleAsk, Fetch: RuleAsk,
		Bash: PatternRules{
			"*":           RuleAsk,
			"git status*": RuleAllow,
			"git diff*":   RuleAllow,
			"git log*":    RuleAllow,
			"ls*":         RuleAllow,
			"rg*":         RuleAllow,
		},
		MCP: PatternRules{"*": RuleAsk},
	},

	ModeAuto: {
		Read: RuleAllow, Write: RuleAllow, Edit: RuleAllow, Fetch: RuleAllow,
		Bash: PatternRules{
			"*": RuleAllow,

			// Auto means "do not interrupt me for ordinary work", not "never
			// stop" — and giving that last part up is what makes yolo a separate
			// thing rather than a looser auto.
			"rm -rf*":   RuleAsk,
			"sudo*":     RuleAsk,
			"git push*": RuleAsk,
			"* | sh*":   RuleAsk,
		},
		MCP: PatternRules{"*": RuleAllow},
	},
}

// Merge lays over onto base and returns the result, which is how design §10's
// `modes.<name>` reaches a preset: a user adds one bash pattern without
// restating the map.
//
// An empty rule is one the override did not write, so base answers; a rule that
// is not a rule is carried through, so Compile names it rather than a table
// quietly missing the entry somebody wrote. DenyRedirection is the one answer an
// override can only add — a guard a pattern table could switch off is not one.
func Merge(base, over PermissionSet) PermissionSet {
	merged := base
	if over.Read != "" {
		merged.Read = over.Read
	}
	if over.Write != "" {
		merged.Write = over.Write
	}
	if over.Edit != "" {
		merged.Edit = over.Edit
	}
	if over.Fetch != "" {
		merged.Fetch = over.Fetch
	}
	merged.Bash = mergePatterns(base.Bash, over.Bash)
	merged.MCP = mergePatterns(base.MCP, over.MCP)
	merged.DenyRedirection = base.DenyRedirection || over.DenyRedirection
	return merged
}

// mergePatterns is per pattern, not per table: an override naming `go test*`
// keeps every other pattern the preset listed. Always a fresh map, so neither
// argument can be written through the result.
func mergePatterns(base, over PatternRules) PatternRules {
	if len(over) == 0 {
		return maps.Clone(base)
	}
	merged := make(PatternRules, len(base)+len(over))
	maps.Copy(merged, base)
	maps.Copy(merged, over)
	return merged
}

// Patterns reads a pattern table the way a config file writes one, with its
// rules still strings. Nothing is checked here: a rule that is not one reaches
// Compile, which names both the pattern and what was written.
func Patterns(rules map[string]string) PatternRules {
	if len(rules) == 0 {
		return nil
	}
	out := make(PatternRules, len(rules))
	for text, rule := range rules {
		out[text] = Rule(rule)
	}
	return out
}
