package permission

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// PatternRules maps a glob to the rule it carries. The glob is matched against
// one literal string — the whole bash command line, or the tool name for MCP
// (design §7.1).
type PatternRules map[string]Rule

// PermissionSet is one mode's answers as they are written: a rule per action,
// and a pattern table for the two actions that need one (design §7.1). A rule
// left unset means ask, which is what an unmatched pattern means as well.
type PermissionSet struct {
	Read  Rule
	Write Rule
	Edit  Rule
	Fetch Rule
	Bash  PatternRules
	MCP   PatternRules
}

// Compile checks a set and orders its patterns, once, so that resolving a call
// is a walk down a sorted slice rather than a walk over a map.
//
// Every fault in the set is reported together, so a config with three typos in
// it takes one edit rather than three. That check has to happen here because
// nothing above it does: internal/config carries these rules as plain strings,
// and a rule the ladder cannot read denies at the moment the tool runs, naming
// the call rather than the line that broke it.
//
// It returns the Rules interface rather than the compiled type because that is
// what Service.SetRules takes, and there is nothing else to do with one.
func Compile(set PermissionSet) (Rules, error) {
	var errs []error
	for _, scalar := range []struct {
		action string
		rule   Rule
	}{
		{"read", set.Read},
		{"write", set.Write},
		{"edit", set.Edit},
		{"fetch", set.Fetch},
	} {
		switch scalar.rule {
		case RuleAllow, RuleAsk, RuleDeny, "":
		default:
			errs = append(errs, fmt.Errorf("%s: %q is not a rule; write allow, ask or deny",
				scalar.action, scalar.rule))
		}
	}

	bash, err := compilePatterns("bash", set.Bash)
	errs = append(errs, err)
	mcp, err := compilePatterns("mcp", set.MCP)
	errs = append(errs, err)

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &compiledSet{
		read:  set.Read,
		write: set.Write,
		edit:  set.Edit,
		fetch: set.Fetch,
		bash:  bash,
		mcp:   mcp,
	}, nil
}

// compiledSet answers rungs 1 and 2 from a set whose patterns are already sorted
// by descending specificity.
type compiledSet struct {
	read, write, edit, fetch Rule
	bash, mcp                []pattern
}

// pattern is one glob, its rule, and the key it was sorted on.
type pattern struct {
	text        string
	rule        Rule
	specificity int
	matcher     glob
}

func compilePatterns(bucket string, rules PatternRules) ([]pattern, error) {
	var errs []error
	out := make([]pattern, 0, len(rules))

	// Sorted rather than ranged over: map order is random, so an unsorted walk
	// would report the same broken config in a different order each run.
	for _, text := range slices.Sorted(maps.Keys(rules)) {
		rule := rules[text]
		switch {
		case text == "":
			errs = append(errs, fmt.Errorf("%s: the empty pattern matches only an empty "+
				"string, so it can never answer for a real call; write \"*\" for a catch-all", bucket))
			continue
		case rule != RuleAllow && rule != RuleAsk && rule != RuleDeny:
			errs = append(errs, fmt.Errorf("%s pattern %q: %q is not a rule; write allow, ask or deny",
				bucket, text, rule))
			continue
		}
		out = append(out, pattern{
			text:        text,
			rule:        rule,
			specificity: specificity(text),
			matcher:     parseGlob(text),
		})
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}

	slices.SortFunc(out, func(a, b pattern) int {
		if a.specificity != b.specificity {
			return b.specificity - a.specificity
		}
		return strings.Compare(a.text, b.text)
	})
	return out, nil
}

func (c *compiledSet) Resolve(req Request) Rule {
	switch req.Action {
	case ActionRead:
		return orAsk(c.read)
	case ActionWrite:
		return orAsk(c.write)
	case ActionEdit:
		return orAsk(c.edit)
	case ActionFetch:
		return orAsk(c.fetch)
	case ActionExecute:
		// Bash and MCP share this action, so what separates them is the string
		// there is to match: a command line for bash, and for MCP the tool name,
		// `mcp__<server>__<tool>` as design §8.2 spells it and as the user sees
		// it everywhere else.
		switch {
		case req.Command != "":
			return matchRule(c.bash, req.Command)
		case req.Tool != "":
			return matchRule(c.mcp, req.Tool)
		}
	}
	// An action this set has no bucket for, and an execute naming neither a
	// command nor a tool: there is nothing here to match, and the answer to a
	// question this cannot read is to put it to the user.
	return RuleAsk
}

func matchRule(patterns []pattern, s string) Rule {
	for _, p := range patterns {
		if p.matcher.match(s) {
			return p.rule
		}
	}
	return RuleAsk
}

func orAsk(r Rule) Rule {
	if r == "" {
		return RuleAsk
	}
	return r
}
