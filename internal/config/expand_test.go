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

// expanderOver builds an Expander over a global config holding one api_key, the
// shape design §10 sends through the resolver. Global on purpose: a project
// config is refused, and that has its own test.
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

func expand(t *testing.T, e *config.Expander) string {
	t.Helper()
	got, err := e.Expand(t.Context(), apiKey)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	return got
}

// TestEveryFormResolves covers all five forms design §10 defines, and the
// literal that is none of them.
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

		// Inline, not whole-value only: a base URL wants `https://${HOST}/v1`
		// far more than a credential does.
		{"a reference inside text", "Bearer ${KEY}!", "Bearer from-env!"},
		{"two references", "$KEY-$KEY", "from-env-from-env"},

		// Secrets contain dollars, and without an escape `sk-a$bc` would
		// resolve an unset variable.
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

// TestMessageFormRefusesToStart: the config author's own sentence is what the
// user sees.
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

// TestBareReferenceToAnUnsetVariableFails, where the shell would give the empty
// string.
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

// TestAFailingCommandIsActionable: which setting, which command, what the
// command said.
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

// TestASecretReachesTheCaller runs against a real subprocess rather than a stub:
// the point is the whole path, not the parser.
func TestASecretReachesTheCaller(t *testing.T) {
	e := expanderOver(t, "$(echo hunter2)", env{}, config.ExpanderOptions{})

	if got := expand(t, e); got != "hunter2" {
		t.Errorf("Expand = %q, want %q", got, "hunter2")
	}
}

func TestOutputIsTrimmed(t *testing.T) {
	e := expanderOver(t, "$(printf 'hunter2\n')", env{}, config.ExpanderOptions{})

	if got := expand(t, e); got != "hunter2" {
		t.Errorf("Expand = %q, want the newline gone", got)
	}
}

// TestAQuotedParenDoesNotEndTheCommand, which would run half of it.
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

	// Past the window, a rotated credential is picked up without a restart.
	clock = clock.Add(31 * time.Second)
	if got := expand(t, e); got != "secret-2" {
		t.Errorf("Expand after the window = %q, want it run again", got)
	}
}

// TestAFailingCommandIsNotCached: unlocking the vault has to take effect.
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

// TestConcurrentExpansionIsSafe. Credentials are re-resolved per model call and
// tool calls run in parallel, so this is the ordinary case. Meaningful under
// -race, which `just ci` runs.
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

// TestALiteralWithGrammarCharactersIsUnchanged: they are ordinary characters in
// a generated secret.
func TestALiteralWithGrammarCharactersIsUnchanged(t *testing.T) {
	const value = "sk-ant-a{b}c(d)e:-f"
	e := expanderOver(t, value, env{}, config.ExpanderOptions{})

	if got := expand(t, e); got != value {
		t.Errorf("Expand = %q, want it untouched", got)
	}
}

// TestExpandingAnAbsentKeyFails, rather than returning an empty credential.
func TestExpandingAnAbsentKeyFails(t *testing.T) {
	e := expanderOver(t, "literal", env{}, config.ExpanderOptions{})

	if _, err := e.Expand(t.Context(), "providers.nobody.api_key"); err == nil {
		t.Error("Expand succeeded for a key that is not in the config")
	}
}

// TestADefaultIsAValueNotText. The round-trip fuzz property cannot catch this
// one, because the wrong parse is a stable one.
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

// TestAnEmptyCommandIsRejected, rather than handed to `sh -c ""`.
func TestAnEmptyCommandIsRejected(t *testing.T) {
	e := expanderOver(t, "$()", env{}, config.ExpanderOptions{})

	if _, err := e.Expand(t.Context(), apiKey); err == nil {
		t.Error("Expand accepted $() as a command")
	}
}

// TestAnEmptyMessageStillSaysSomething, `${VAR:?}` being legal.
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

// TestAnEscapedQuoteInACommand, which must not open a quoted run that never
// closes.
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

	// "$(op read …) failed" is the wording the timeout branch reaches for, and it
	// blames the credential helper for the user's own Esc.
	for _, unwanted := range []string{"did not finish", "failed", "op read"} {
		if strings.Contains(err.Error(), unwanted) {
			t.Errorf("a cancelled turn was reported as %q:\n%s", unwanted, err)
		}
	}
}

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

// TestACommandThatLeavesSomethingRunningReturnsAtOnce — the ordinary case, not
// an exotic one: `pass` starts a gpg-agent that outlives it by design.
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

// TestACommandThatNeverFinishesTimesOut is the other half.
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

func TestLargeOutputDoesNotDeadlock(t *testing.T) {
	const size = 300 << 10
	// No newlines: the result is trimmed, which would skew the length assertion.
	e := expanderOver(t, fmt.Sprintf("$(head -c %d /dev/zero | tr '\\0' x)", size),
		env{}, config.ExpanderOptions{})

	if got := len(expand(t, e)); got != size {
		t.Errorf("got %d bytes, want %d", got, size)
	}
}

// TestAnEscapedDollarDoesNotStartACommand. The brace matcher has to read `$$(`
// the way parseValue does, or the two disagree about where a reference ends and
// a well-formed value is rejected with a message naming the wrong problem.
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

// TestSetButEmptySaysSo. The operators treat the two alike, which is the shell's
// rule; the message must not.
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
// ordinary, and its owner is told about a variable they never wrote — so the
// workaround belongs in the message, not in a document they are not reading.
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
