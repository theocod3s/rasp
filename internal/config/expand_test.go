package config_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/theocod3s/rasp/internal/config"
)

// expanderOver builds an Expander over a global config holding one api_key,
// which is the shape design §10 sends through the resolver. The global layer
// is deliberate: a project config is refused, and that has its own test.
func expanderOver(t *testing.T, value string, environ env, opts config.ExpanderOptions) *config.Expander {
	t.Helper()

	res := load(t, config.Sources{
		GlobalPath: global(t, fmt.Sprintf(`{"providers": {"anthropic": {"api_key": %q}}}`, value)),
	})
	if opts.Getenv == nil {
		opts.Getenv = environ.lookup
	}
	return config.NewExpander(res, opts)
}

const apiKey = "providers.anthropic.api_key"

// expand resolves the key and fails the test if it could not.
func expand(t *testing.T, e *config.Expander) string {
	t.Helper()
	got, err := e.Expand(t.Context(), apiKey)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	return got
}

// TestEveryFormResolves is the first acceptance criterion: all five forms
// design §10 defines, and the literal that is none of them.
func TestEveryFormResolves(t *testing.T) {
	environ := env{"KEY": "from-env", "EMPTY": ""}
	run := func(_ context.Context, command string) ([]byte, error) {
		return []byte("ran: " + command + "\n"), nil
	}

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"a literal", "sk-ant-literal", "sk-ant-literal"},
		{"a bare reference", "$KEY", "from-env"},
		{"a braced reference", "${KEY}", "from-env"},
		{"a default, unused", "${KEY:-fallback}", "from-env"},
		{"a default, used", "${MISSING:-fallback}", "fallback"},
		{"a default over an empty variable", "${EMPTY:-fallback}", "fallback"},
		{"an empty default asks for empty on purpose", "${MISSING:-}", ""},
		{"a message, not needed", "${KEY:?run 'op signin'}", "from-env"},
		{"a command", "$(op read op://vault/key)", "ran: op read op://vault/key"},

		// Substitution is inline, not whole-value only: a base URL wants
		// `https://${HOST}/v1` far more than a credential does.
		{"a reference inside text", "Bearer ${KEY}!", "Bearer from-env!"},
		{"two references", "$KEY-$KEY", "from-env-from-env"},

		// A literal dollar has to survive. Secrets contain them, and without
		// an escape `sk-a$bc` would resolve an unset variable.
		{"an escaped dollar", "sk-a$$bc", "sk-a$bc"},
		{"an escaped dollar beside a reference", "$$$KEY", "$from-env"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := expanderOver(t, tc.value, environ, config.ExpanderOptions{Run: run})
			if got := expand(t, e); got != tc.want {
				t.Errorf("Expand(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// TestMessageFormRefusesToStart is the other half of `${VAR:?msg}`: when the
// variable is missing, the config author's own sentence is what the user sees.
func TestMessageFormRefusesToStart(t *testing.T) {
	e := expanderOver(t, "${MISSING:?run 'op signin' first}", env{}, config.ExpanderOptions{})

	_, err := e.Expand(t.Context(), apiKey)
	if err == nil {
		t.Fatal("Expand succeeded with the variable unset")
	}
	for _, want := range []string{"run 'op signin' first", apiKey} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%s", want, err)
		}
	}
}

// TestBareReferenceToAnUnsetVariableFails. The shell would give the empty
// string; every value reaching this resolver is a credential, and an empty one
// comes back later as a 401 that points at nothing.
func TestBareReferenceToAnUnsetVariableFails(t *testing.T) {
	for _, value := range []string{"$MISSING", "${MISSING}"} {
		e := expanderOver(t, value, env{}, config.ExpanderOptions{})
		_, err := e.Expand(t.Context(), apiKey)
		if err == nil {
			t.Errorf("Expand(%q) succeeded with the variable unset", value)
			continue
		}
		if !strings.Contains(err.Error(), "MISSING") {
			t.Errorf("error does not name the variable:\n%s", err)
		}
	}
}

// TestUnsupportedFormsAreRejected is the second acceptance criterion. Passing
// `${VAR:=x}` through as literal text would surface much later as an API key
// made of punctuation.
func TestUnsupportedFormsAreRejected(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"assign", "${KEY:=fallback}", ":="},
		{"substitute when set", "${KEY:+other}", ":+"},
		{"an unknown operator", "${KEY:^x}", "not an operator"},
		{"no operator after the colon", "${KEY:}", "no operator"},
		{"an unterminated command", "$(op read", "unterminated"},
		{"an unterminated brace", "${KEY", "unterminated"},
		{"a lone dollar", "abc$", "lone"},
		{"a dollar before punctuation", "sk-a$-bc", "literal dollar"},
		{"an empty name", "${}", "not a valid variable name"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := expanderOver(t, tc.value, env{"KEY": "set"}, config.ExpanderOptions{})

			got, err := e.Expand(t.Context(), apiKey)
			if err == nil {
				t.Fatalf("Expand(%q) = %q, want an error", tc.value, got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not mention %q:\n%s", tc.want, err)
			}
			if !strings.Contains(err.Error(), apiKey) {
				t.Errorf("error does not name the key:\n%s", err)
			}
		})
	}
}

// TestAFailingCommandIsActionable is the third acceptance criterion. `exit
// status 1` from `op` says nothing; its stderr says "you are not currently
// signed in", and the key says which line to go and edit.
func TestAFailingCommandIsActionable(t *testing.T) {
	e := expanderOver(t, "$(op read op://vault/key)", env{}, config.ExpanderOptions{
		Run: func(context.Context, string) ([]byte, error) {
			return nil, errors.New("exit status 1: [ERROR] you are not currently signed in")
		},
	})

	_, err := e.Expand(t.Context(), apiKey)
	if err == nil {
		t.Fatal("Expand succeeded although the command failed")
	}
	for _, want := range []string{
		apiKey,                    // which setting
		"op read op://vault/key",  // which command
		"not currently signed in", // what the command said
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%s", want, err)
		}
	}
}

// TestASecretReachesTheCaller is the fourth acceptance criterion, run against
// a real subprocess rather than a stub — the point is that the whole path
// works, not that the parser does.
func TestASecretReachesTheCaller(t *testing.T) {
	e := expanderOver(t, "$(echo hunter2)", env{}, config.ExpanderOptions{})

	if got := expand(t, e); got != "hunter2" {
		t.Errorf("Expand = %q, want %q", got, "hunter2")
	}
}

// TestOutputIsTrimmed: every credential helper worth using prints a trailing
// newline, and no API accepts one.
func TestOutputIsTrimmed(t *testing.T) {
	e := expanderOver(t, "$(printf 'hunter2\n')", env{}, config.ExpanderOptions{})

	if got := expand(t, e); got != "hunter2" {
		t.Errorf("Expand = %q, want the newline gone", got)
	}
}

// TestAQuotedParenDoesNotEndTheCommand. Parenthesis counting that ignores
// quotes truncates the command and runs half of it — the same mistake a naive
// JSONC comment stripper makes with a `//` inside a URL.
func TestAQuotedParenDoesNotEndTheCommand(t *testing.T) {
	var got string
	e := expanderOver(t, `$(echo ")" done)`, env{}, config.ExpanderOptions{
		Run: func(_ context.Context, command string) ([]byte, error) {
			got = command
			return []byte("ok"), nil
		},
	})
	expand(t, e)

	if want := `echo ")" done`; got != want {
		t.Errorf("ran %q, want %q", got, want)
	}
}

// TestCommandsAreCached. prd §6.1 wants credentials re-resolved on every model
// call; forking `op read` per request would make that unaffordable.
func TestCommandsAreCached(t *testing.T) {
	var (
		mu    sync.Mutex
		runs  int
		clock = time.Unix(0, 0)
	)
	e := expanderOver(t, "$(op read op://vault/key)", env{}, config.ExpanderOptions{
		Run: func(context.Context, string) ([]byte, error) {
			mu.Lock()
			defer mu.Unlock()
			runs++
			return fmt.Appendf(nil, "secret-%d", runs), nil
		},
		Now: func() time.Time { return clock },
	})

	if got := expand(t, e); got != "secret-1" {
		t.Fatalf("first Expand = %q", got)
	}
	if got := expand(t, e); got != "secret-1" {
		t.Errorf("second Expand = %q, want the cached value", got)
	}
	if runs != 1 {
		t.Errorf("ran the command %d times, want 1", runs)
	}

	// Past the window, a rotated credential has to be picked up without a
	// restart — which is the whole reason the cache expires rather than
	// holding for the session.
	clock = clock.Add(31 * time.Second)
	if got := expand(t, e); got != "secret-2" {
		t.Errorf("Expand after the window = %q, want it run again", got)
	}
}

// TestAFailingCommandIsNotCached. A locked vault is fixed while rasp is
// running, and caching the failure would make unlocking it look ineffective.
func TestAFailingCommandIsNotCached(t *testing.T) {
	var runs int
	e := expanderOver(t, "$(op read op://vault/key)", env{}, config.ExpanderOptions{
		Run: func(context.Context, string) ([]byte, error) {
			if runs++; runs == 1 {
				return nil, errors.New("vault is locked")
			}
			return []byte("unlocked"), nil
		},
	})

	if _, err := e.Expand(t.Context(), apiKey); err == nil {
		t.Fatal("the first Expand should have failed")
	}
	if got := expand(t, e); got != "unlocked" {
		t.Errorf("Expand after the fix = %q, want it retried", got)
	}
}

// TestConcurrentExpansionIsSafe. Credentials are re-resolved per model call,
// and rasp runs tool calls in parallel, so this is the ordinary case rather
// than an exotic one. Meaningful under -race, which `just ci` runs.
func TestConcurrentExpansionIsSafe(t *testing.T) {
	e := expanderOver(t, "$(op read op://vault/key)", env{}, config.ExpanderOptions{
		Run: func(context.Context, string) ([]byte, error) { return []byte("secret"), nil },
	})

	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if got, err := e.Expand(t.Context(), apiKey); err != nil || got != "secret" {
				t.Errorf("Expand = %q, %v", got, err)
			}
		})
	}
	wg.Wait()
}

// TestALiteralWithGrammarCharactersIsUnchanged. Braces, parentheses and a `:-`
// are ordinary characters in a generated secret, and the grammar must not
// reach for them when there is no dollar to start it.
func TestALiteralWithGrammarCharactersIsUnchanged(t *testing.T) {
	const value = "sk-ant-a{b}c(d)e:-f"
	e := expanderOver(t, value, env{}, config.ExpanderOptions{})

	if got := expand(t, e); got != value {
		t.Errorf("Expand = %q, want it untouched", got)
	}
}

// TestExpandingAnAbsentKeyFails, rather than returning the empty string that a
// caller would then send as a credential.
func TestExpandingAnAbsentKeyFails(t *testing.T) {
	e := expanderOver(t, "literal", env{}, config.ExpanderOptions{})

	if _, err := e.Expand(t.Context(), "providers.nobody.api_key"); err == nil {
		t.Error("Expand succeeded for a key that is not in the config")
	}
}

// TestADefaultIsAValueNotText. `${A:-${B}}` means "A, or B". Taking the first
// closing brace instead ends the reference inside its own default and leaves
// the rest as literal text — which for a credential means handing the caller
// the nine characters `${B}` and calling it a key. The round-trip fuzz
// property cannot catch this: the wrong parse is a stable one.
func TestADefaultIsAValueNotText(t *testing.T) {
	environ := env{"TEAM_KEY": "sk-team", "KEY": "sk-set"}
	run := func(_ context.Context, command string) ([]byte, error) {
		return []byte("ran: " + command), nil
	}

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"a reference inside a default", "${MISSING:-${TEAM_KEY}}", "sk-team"},
		{"a bare reference inside a default", "${MISSING:-$TEAM_KEY}", "sk-team"},
		{"a command inside a default", "${MISSING:-$(op read k)}", "ran: op read k"},
		{"an escape inside a default", "${MISSING:-a$$b}", "a$b"},
		{"two levels", "${MISSING:-${ALSO_MISSING:-${TEAM_KEY}}}", "sk-team"},
		{"text around a nested reference", "${MISSING:-pre-${TEAM_KEY}-post}", "pre-sk-team-post"},
		{"the default is not reached when set", "${KEY:-${TEAM_KEY}}", "sk-set"},
		{"a brace inside a nested command", "${MISSING:-$(echo })}", "ran: echo }"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := expanderOver(t, tc.value, environ, config.ExpanderOptions{Run: run})
			if got := expand(t, e); got != tc.want {
				t.Errorf("Expand(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// TestAnUnresolvableNestedDefaultFails, rather than falling back to the text.
func TestAnUnresolvableNestedDefaultFails(t *testing.T) {
	e := expanderOver(t, "${MISSING:-${ALSO_MISSING}}", env{}, config.ExpanderOptions{})

	_, err := e.Expand(t.Context(), apiKey)
	if err == nil {
		t.Fatal("Expand succeeded although neither variable is set")
	}
	if !strings.Contains(err.Error(), "ALSO_MISSING") {
		t.Errorf("error does not name the inner variable:\n%s", err)
	}
}

// TestACommandThatPrintsNothingFails. Succeeding while printing nothing is the
// command form of the empty variable this resolver already refuses, and it is
// the likelier of the two: `op read` on an empty field exits 0.
func TestACommandThatPrintsNothingFails(t *testing.T) {
	for _, value := range []string{"$(true)", "$(printf '')"} {
		e := expanderOver(t, value, env{}, config.ExpanderOptions{})

		got, err := e.Expand(t.Context(), apiKey)
		if err == nil {
			t.Errorf("Expand(%q) = %q, want an error rather than an empty credential", value, got)
			continue
		}
		if !strings.Contains(err.Error(), "printed nothing") {
			t.Errorf("error does not say what happened:\n%s", err)
		}
	}
}

// TestAnEmptyCommandIsRejected: `$()` parses as a command and would otherwise
// be handed to `sh -c ""`.
func TestAnEmptyCommandIsRejected(t *testing.T) {
	e := expanderOver(t, "$()", env{}, config.ExpanderOptions{})

	if _, err := e.Expand(t.Context(), apiKey); err == nil {
		t.Error("Expand accepted $() as a command")
	}
}

// TestAnEmptyMessageStillSaysSomething. `${VAR:?}` is legal and carries no
// message; an error rendering as a bare colon tells the reader nothing.
func TestAnEmptyMessageStillSaysSomething(t *testing.T) {
	e := expanderOver(t, "${MISSING:?}", env{}, config.ExpanderOptions{})

	_, err := e.Expand(t.Context(), apiKey)
	if err == nil {
		t.Fatal("Expand succeeded with the variable unset")
	}
	if !strings.Contains(err.Error(), "MISSING") {
		t.Errorf("error does not name the variable:\n%s", err)
	}
}

// TestAnEscapedQuoteInACommand. `$(echo \")` is valid shell, and the quote it
// escapes must not open a run that never closes.
func TestAnEscapedQuoteInACommand(t *testing.T) {
	var got string
	e := expanderOver(t, `$(echo \" done)`, env{}, config.ExpanderOptions{
		Run: func(_ context.Context, command string) ([]byte, error) {
			got = command
			return []byte("ok"), nil
		},
	})
	expand(t, e)

	if want := `echo \" done`; got != want {
		t.Errorf("ran %q, want %q", got, want)
	}
}

// TestACancelledTurnIsNotReportedAsATimeout. Both contexts are cancelled when
// a turn is interrupted, so checking the derived one first would tell a user
// who pressed Esc that their credential helper is slow.
func TestACancelledTurnIsNotReportedAsATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	e := expanderOver(t, "$(op read op://vault/key)", env{}, config.ExpanderOptions{
		Run: func(ctx context.Context, _ string) ([]byte, error) {
			cancel()
			return nil, ctx.Err()
		},
	})

	_, err := e.Expand(ctx, apiKey)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to carry context.Canceled", err)
	}

	// A cancellation propagates as itself. Dressing it up as a failure of the
	// command — "$(op read …) failed" — blames the credential helper for the
	// user's own Esc, and it is the wording the timeout branch would reach for
	// if the parent were not checked first.
	for _, unwanted := range []string{"did not finish", "failed", "op read"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("a cancelled turn was reported as %q:\n%s", unwanted, err)
		}
	}
}

// TestNestingIsBounded. Each level parses a strict substring of the one above,
// so no config anyone wrote on purpose reaches this — it is here because the
// input is a file, and an error is a better answer than a stack overflow.
func TestNestingIsBounded(t *testing.T) {
	const depth = 64

	var value strings.Builder
	for i := range depth {
		fmt.Fprintf(&value, "${A%d:-", i)
	}
	value.WriteString("fallback")
	value.WriteString(strings.Repeat("}", depth))

	e := expanderOver(t, value.String(), env{}, config.ExpanderOptions{})

	_, err := e.Expand(t.Context(), apiKey)
	if err == nil {
		t.Fatal("Expand followed an unbounded chain of defaults")
	}
	if !strings.Contains(err.Error(), "nested") {
		t.Errorf("error does not say what the limit was:\n%s", err)
	}
}

// TestACommandThatLeavesSomethingRunningReturnsAtOnce. This is the ordinary
// case, not an exotic one: `pass` starts a gpg-agent that outlives it by
// design, and so do several `op` and ssh-agent wrappers. The agent inherits
// stdout, so waiting for end-of-file means waiting for the agent — which hung
// the turn, and, once exec was asked to force the pipes shut instead, threw
// the secret away on a race. The command has exited and its output is here;
// nothing else is ours to wait for.
func TestACommandThatLeavesSomethingRunningReturnsAtOnce(t *testing.T) {
	e := expanderOver(t, "$(sleep 5 & printf hunter2)", env{}, config.ExpanderOptions{})

	start := time.Now()
	got := expand(t, e)

	if got != "hunter2" {
		t.Errorf("Expand = %q, want the secret the command printed", got)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Expand took %s, want it back as soon as the command exited", elapsed)
	}
}

// TestACommandThatNeverFinishesTimesOut is the other half. A vault that is
// unreachable, or a helper waiting on a prompt nobody can see, must fail
// rather than hang the turn behind it.
func TestACommandThatNeverFinishesTimesOut(t *testing.T) {
	e := expanderOver(t, "$(sleep 30)", env{}, config.ExpanderOptions{})

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := e.Expand(ctx, apiKey); err == nil {
		t.Fatal("Expand waited out a command that never finished, and called it success")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Expand took %s to give up, want it bounded by the deadline", elapsed)
	}
}

// TestLargeOutputDoesNotDeadlock. A pipe holds about 64KB; a command that
// filled it while nothing was reading would block forever, which is why the
// drain runs alongside the command rather than after it.
func TestLargeOutputDoesNotDeadlock(t *testing.T) {
	const size = 300 << 10
	// No newlines: the result is trimmed, and a trailing one would make the
	// length assertion say something other than what it means.
	e := expanderOver(t, fmt.Sprintf("$(head -c %d /dev/zero | tr '\\0' x)", size),
		env{}, config.ExpanderOptions{})

	if got := len(expand(t, e)); got != size {
		t.Errorf("got %d bytes, want %d", got, size)
	}
}

// TestAnEscapedDollarDoesNotStartACommand. `$$` is a literal dollar, so `$$(`
// is a dollar followed by a parenthesis and not a substitution. The brace
// matcher has to read it the same way parseValue does, or the two disagree
// about where a reference ends and a well-formed value is rejected — with a
// message naming the wrong problem.
func TestAnEscapedDollarDoesNotStartACommand(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{"${MISSING:-$$(x}", "$(x"},
		{"${MISSING:-a$$(b}", "a$(b"},
		{"${MISSING:-$$}", "$"},
	}

	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			e := expanderOver(t, tc.value, env{}, config.ExpanderOptions{})
			if got := expand(t, e); got != tc.want {
				t.Errorf("Expand(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// TestSetButEmptySaysSo. The operators treat "set to empty" and "unset" alike,
// which is the shell's rule and the right one. The message must not: someone
// with `export ANTHROPIC_API_KEY=""` in their profile who is told the variable
// is not set will go and look, find it, and have nowhere left to go.
func TestSetButEmptySaysSo(t *testing.T) {
	e := expanderOver(t, "$ANTHROPIC_API_KEY", env{"ANTHROPIC_API_KEY": ""}, config.ExpanderOptions{})

	_, err := e.Expand(t.Context(), apiKey)
	if err == nil {
		t.Fatal("Expand succeeded on an empty variable")
	}
	if !strings.Contains(err.Error(), "set but empty") {
		t.Errorf("error does not distinguish empty from unset:\n%s", err)
	}
}

// TestABareBraceInADefaultIsAnOrdinaryCharacter. Only `${` opens a level.
// Counting every brace instead rejects `${KEY:-a{b}`, which is legal and ends
// in a perfectly good closing brace — and rejects it by claiming there is no
// closing brace at all, which leaves the reader nowhere to go.
func TestABareBraceInADefaultIsAnOrdinaryCharacter(t *testing.T) {
	tests := []struct{ value, want string }{
		{"${MISSING:-a{b}", "a{b"},
		{"${MISSING:-{}", "{"},
		{"${MISSING:-a{b{c}", "a{b{c"},
	}

	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			e := expanderOver(t, tc.value, env{}, config.ExpanderOptions{})
			if got := expand(t, e); got != tc.want {
				t.Errorf("Expand(%q) = %q, want %q", tc.value, got, tc.want)
			}
		})
	}
}

// TestAFloodOfStderrIsCapped. The stream is untrusted in size, and all of it
// would otherwise land in an error that reaches the UI and the log file.
func TestAFloodOfStderrIsCapped(t *testing.T) {
	e := expanderOver(t, `$(head -c 100000 /dev/zero | tr '\0' x >&2; exit 1)`,
		env{}, config.ExpanderOptions{})

	_, err := e.Expand(t.Context(), apiKey)
	if err == nil {
		t.Fatal("Expand succeeded although the command failed")
	}
	if len(err.Error()) > 16<<10 {
		t.Errorf("error is %d bytes; the captured stream is not bounded", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("the error does not say it was cut short:\n%.200s", err)
	}
}

// TestABadCommandInsideABraceNamesTheRealProblem. Collapsing the inner error
// into "no closing brace" points the reader at the one delimiter that is
// actually present, which is worse than saying nothing.
func TestABadCommandInsideABraceNamesTheRealProblem(t *testing.T) {
	tests := []struct{ value, want string }{
		{"${KEY:-$()}", "no command in it"},
		{"${KEY:-$(op read}", "no closing parenthesis"},
	}

	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			e := expanderOver(t, tc.value, env{}, config.ExpanderOptions{})

			_, err := e.Expand(t.Context(), apiKey)
			if err == nil {
				t.Fatalf("Expand(%q) succeeded", tc.value)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not name the real problem:\n%s", err)
			}
			if strings.Contains(err.Error(), "no closing brace") {
				t.Errorf("error blames the brace, which is present:\n%s", err)
			}
		})
	}
}

// TestTheUnsetMessageOffersTheEscape. A secret containing a literal dollar is
// ordinary, and someone who has exported one is told about a variable they
// never wrote. The workaround has to be in the message, not in a design
// document they are not reading.
func TestTheUnsetMessageOffersTheEscape(t *testing.T) {
	e := expanderOver(t, "sk-ant-x$yz", env{}, config.ExpanderOptions{})

	_, err := e.Expand(t.Context(), apiKey)
	if err == nil {
		t.Fatal("Expand succeeded on an unset variable")
	}
	if !strings.Contains(err.Error(), "$$") {
		t.Errorf("error does not name the escape:\n%s", err)
	}
}
