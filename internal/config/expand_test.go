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
