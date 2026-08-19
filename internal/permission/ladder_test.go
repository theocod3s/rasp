package permission_test

import (
	"errors"
	"testing"

	"github.com/theocod3s/rasp/internal/permission"
)

// TestTheLadderAnswersInTheDocumentedOrder walks design §7.7 rung by rung. Each
// case arms the rung under test to answer one way and every rung below it to
// answer the other, so the assertion is about which one spoke rather than about
// the answer alone — the prompt count is the second half of that, since a rung
// that answered first is a rung that never let the question reach the user.
func TestTheLadderAnswersInTheDocumentedOrder(t *testing.T) {
	req := permission.Request{
		CallID: "call-1",
		Tool:   "write",
		Action: permission.ActionWrite,
		Path:   "/foo/a.go",
	}

	tests := []struct {
		name string

		// rule is what the mode preset says; empty installs no preset at all.
		rule      permission.Rule
		allowTool bool // rung 3: the tool is in the config allow-list
		granted   bool // rung 4: a session grant for this exact call
		answer    permission.Decision

		wantAllowed  bool
		wantPrompted bool
	}{
		{
			name:        "rung 1 allows, and no rung below is consulted",
			rule:        permission.RuleAllow,
			answer:      permission.DecisionReject,
			wantAllowed: true,
		},
		{
			name:        "rung 2 denies over an allow-list that would say yes",
			rule:        permission.RuleDeny,
			allowTool:   true,
			answer:      permission.DecisionOnce,
			wantAllowed: false,
		},
		{
			name:        "rung 2 denies over a session grant that would say yes",
			rule:        permission.RuleDeny,
			granted:     true,
			answer:      permission.DecisionOnce,
			wantAllowed: false,
		},
		{
			name:        "rung 3 allows before the user is asked",
			rule:        permission.RuleAsk,
			allowTool:   true,
			answer:      permission.DecisionReject,
			wantAllowed: true,
		},
		{
			name:        "rung 4 allows before the user is asked",
			rule:        permission.RuleAsk,
			granted:     true,
			answer:      permission.DecisionReject,
			wantAllowed: true,
		},
		{
			name:         "rung 5 asks when nothing above has answered",
			rule:         permission.RuleAsk,
			answer:       permission.DecisionOnce,
			wantAllowed:  true,
			wantPrompted: true,
		},
		{
			name:         "rung 5 refuses when the user does",
			rule:         permission.RuleAsk,
			answer:       permission.DecisionReject,
			wantAllowed:  false,
			wantPrompted: true,
		},
		{
			name:         "with no preset installed the question reaches the user",
			answer:       permission.DecisionOnce,
			wantAllowed:  true,
			wantPrompted: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var allowTools []string
			if tc.allowTool {
				allowTools = []string{req.Tool}
			}
			h := newHarness(t, tc.answer, allowTools...)

			// The grant is recorded before the preset is installed: a preset that
			// denies would refuse the grant itself.
			if tc.granted {
				grantOnce(t, h, req)
			}
			if tc.rule != "" {
				h.SetRules(fixed(tc.rule))
			}

			err := h.Ask(t.Context(), req)
			switch {
			case tc.wantAllowed && err != nil:
				t.Errorf("Ask = %v, want the call allowed", err)
			case !tc.wantAllowed && !errors.Is(err, permission.ErrDenied):
				t.Errorf("Ask = %v, want a denial wrapping ErrDenied", err)
			}

			if prompted := len(h.prompts()) > 0; prompted != tc.wantPrompted {
				t.Errorf("the user was asked = %v, want %v", prompted, tc.wantPrompted)
			}
		})
	}
}

// TestAGrantCoversTheCallItWasGivenForAndNothingElse is the rule that keeps
// "always allow writes in /foo" from covering ~/.ssh (prd §6.6). Each case
// changes exactly one component of the key, so a component dropped from it
// fails here and nowhere else.
func TestAGrantCoversTheCallItWasGivenForAndNothingElse(t *testing.T) {
	write := permission.Request{
		CallID: "granted",
		Tool:   "write",
		Action: permission.ActionWrite,
		Path:   "/foo/a.go",
	}
	bash := permission.Request{
		CallID:  "granted",
		Tool:    "bash",
		Action:  permission.ActionExecute,
		Command: "rm -rf dist",
	}

	tests := []struct {
		name      string
		granted   permission.Request
		next      permission.Request
		wantCover bool
	}{
		{
			name:      "the same call, asked again",
			granted:   write,
			next:      permission.Request{CallID: "next", Tool: "write", Action: permission.ActionWrite, Path: "/foo/a.go"},
			wantCover: true,
		},
		{
			name:    "another path",
			granted: write,
			next:    permission.Request{CallID: "next", Tool: "write", Action: permission.ActionWrite, Path: "/bar/a.go"},
		},
		{
			name:    "a path the granted one is a prefix of",
			granted: write,
			next:    permission.Request{CallID: "next", Tool: "write", Action: permission.ActionWrite, Path: "/foo/a.go.bak"},
		},
		{
			name:    "another tool, same path",
			granted: write,
			next:    permission.Request{CallID: "next", Tool: "edit", Action: permission.ActionWrite, Path: "/foo/a.go"},
		},
		{
			name:    "another action, same tool and path",
			granted: write,
			next:    permission.Request{CallID: "next", Tool: "write", Action: permission.ActionEdit, Path: "/foo/a.go"},
		},
		{
			name:      "the same command, run again",
			granted:   bash,
			next:      permission.Request{CallID: "next", Tool: "bash", Action: permission.ActionExecute, Command: "rm -rf dist"},
			wantCover: true,
		},
		{
			name:    "another command from the same tool",
			granted: bash,
			next:    permission.Request{CallID: "next", Tool: "bash", Action: permission.ActionExecute, Command: "rm -rf /"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "")
			grantOnce(t, h, tc.granted)
			h.answers(permission.DecisionReject)

			err := h.Ask(t.Context(), tc.next)
			prompted := len(h.prompts()) > 0

			if tc.wantCover {
				if err != nil {
					t.Errorf("Ask = %v, want the grant to cover it", err)
				}
				if prompted {
					t.Errorf("the user was asked about a call the grant covers")
				}
				return
			}
			if !errors.Is(err, permission.ErrDenied) {
				t.Errorf("Ask = %v, want the grant not to reach this call", err)
			}
			if !prompted {
				t.Errorf("the grant answered for a call it was not given for")
			}
		})
	}
}

// TestGrantsDoNotOutliveTheService is the in-memory half of the rule: a grant
// lives on the Service that recorded it, so the next process starts with none
// and asks again (prd §6.6). imports_test.go holds the structural half.
func TestGrantsDoNotOutliveTheService(t *testing.T) {
	req := permission.Request{
		CallID: "call-1",
		Tool:   "write",
		Action: permission.ActionWrite,
		Path:   "/foo/a.go",
	}

	h := newHarness(t, "")
	grantOnce(t, h, req)

	// The grant is real on the service that took it — without this the test
	// below would pass just as well against a Service that records nothing.
	h.answers(permission.DecisionReject)
	if err := h.Ask(t.Context(), req); err != nil {
		t.Fatalf("Ask on the granting service = %v, want the grant to answer", err)
	}
	if len(h.prompts()) > 0 {
		t.Fatalf("the granting service asked again, so there is no grant to outlive anything")
	}

	restarted := newHarness(t, permission.DecisionReject)
	if err := restarted.Ask(t.Context(), req); !errors.Is(err, permission.ErrDenied) {
		t.Errorf("Ask after a restart = %v, want the question asked again", err)
	}
	if len(restarted.prompts()) != 1 {
		t.Errorf("the restarted service asked %d times, want 1", len(restarted.prompts()))
	}
}

// TestClearingGrantsEndsTheirSession is the other boundary a grant must not
// cross: one process serves several sessions, and an approval given in the one
// the user just left has no standing in the next.
func TestClearingGrantsEndsTheirSession(t *testing.T) {
	req := permission.Request{
		CallID: "call-1",
		Tool:   "bash",
		Action: permission.ActionExecute,

		Command: "rm -rf dist",
	}

	h := newHarness(t, "")
	grantOnce(t, h, req)
	h.answers(permission.DecisionReject)

	h.ClearGrants()
	if err := h.Ask(t.Context(), req); !errors.Is(err, permission.ErrDenied) {
		t.Errorf("Ask in the next session = %v, want the question asked again", err)
	}
	if len(h.prompts()) != 1 {
		t.Errorf("the user was asked %d times after the grants were cleared, want 1", len(h.prompts()))
	}
}
