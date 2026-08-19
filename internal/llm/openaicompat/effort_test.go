package openaicompat

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/llm"
)

// TestEffortsIsTheWholeLadder pins the list a picker will draw from and a refusal
// reads. Written out rather than derived, because the claim is a fact about this
// API rather than about the ladder: `reasoning_effort` has a member for every
// rung, which is what made the ladder the union of the two protocols.
func TestEffortsIsTheWholeLadder(t *testing.T) {
	want := []llm.Effort{
		llm.EffortNone, llm.EffortMinimal, llm.EffortLow,
		llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax,
	}

	got := New(Config{ProviderID: testProvider}).Efforts()
	if !slices.Equal(got, want) {
		t.Fatalf("Efforts() = %v, want %v", got, want)
	}

	got[0] = "tampered"
	if again := New(Config{ProviderID: testProvider}).Efforts(); !slices.Equal(again, want) {
		t.Errorf("Efforts() = %v after a caller wrote to an earlier result, want %v", again, want)
	}
}

// TestEveryRungIsSentOrNamed walks the whole ladder against the one list the
// adapter keeps, which is what stops a picker offering a rung the request path
// refuses. Every rung is sendable here today, so the refusing half is driven by a
// rung that is not on the ladder at all — otherwise this test would go quiet the
// moment a new rung arrived unmapped, which is exactly when it matters.
func TestEveryRungIsSentOrNamed(t *testing.T) {
	sent := 0
	for _, rung := range llm.EffortLadder() {
		req := ask()
		req.Effort = rung

		params, err := buildParams(req)
		if !slices.Contains(supported, rung) {
			if err == nil {
				t.Errorf("effort %q was accepted; the turn would run at whatever depth the API "+
					"defaults to, with nothing to say so", rung)
			}
			continue
		}
		sent++
		if err != nil {
			t.Errorf("effort %q: %v, but it is offered", rung, err)
			continue
		}
		if got := string(params.ReasoningEffort); got != string(rung) {
			t.Errorf("effort %q went out as %q", rung, got)
		}
	}
	if sent != len(llm.EffortLadder()) {
		t.Fatalf("%d of %d rungs were sent; the rest never reached the assertion",
			sent, len(llm.EffortLadder()))
	}
}

// TestAnUnmappedRungIsRefused is the branch above cannot reach while the ladder
// and the enum agree. A rung with no entry in wireEffort must fail the request
// naming itself, never arrive as the nearest one that would have worked
// (decisions.md).
func TestAnUnmappedRungIsRefused(t *testing.T) {
	req := ask()
	req.Effort = llm.Effort("exhaustive")

	_, err := buildParams(req)
	if err == nil {
		t.Fatal("no error; a rung this adapter cannot express went out as something else")
	}
	if !strings.Contains(err.Error(), "exhaustive") {
		t.Errorf("error = %v, want one naming the level it could not send", err)
	}
}

// TestARefusedRungIsNamedBeforeAnythingElseFails pins where the guard sits. Both
// faults below are real; reporting the other one first sends the caller back for a
// second round of a refusal that was already true the first time.
func TestARefusedRungIsNamedBeforeAnythingElseFails(t *testing.T) {
	req := ask()
	req.Effort = llm.Effort("exhaustive")
	req.System = []llm.SystemBlock{{Text: ""}}

	_, err := buildParams(req)
	if err == nil {
		t.Fatal("no error, though the request carries two things this adapter refuses")
	}
	if !strings.Contains(err.Error(), "exhaustive") || strings.Contains(err.Error(), "system block") {
		t.Errorf("error = %v, want the one naming the level it could not send", err)
	}
}

// TestUnsetEffortSendsNoDepthField: absence, not emptiness. An unset Effort leaves
// the depth to whatever the API does by default rather than to a rung this adapter
// picked, and an unset field is absent from the JSON rather than zero in it —
// which is invisible from the Go side.
func TestUnsetEffortSendsNoDepthField(t *testing.T) {
	for _, tc := range []struct {
		name   string
		effort llm.Effort
		want   any
	}{
		{name: "unset"},
		{name: "high", effort: llm.EffortHigh, want: "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := replay(t, "text.sse")

			req := ask()
			req.Effort = tc.effort
			for range client.Stream(context.Background(), req) {
			}
			if got := client.sent(t)["reasoning_effort"]; got != tc.want {
				t.Errorf("reasoning_effort = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEffortDoesNotVaryByModel: the list is per protocol, so nothing here reads a
// model id. Deciding otherwise needs a catalog, which rasp has none of and will
// not have (scope.md) — a model that will not take the rung answers with an API
// error, which is the correct outcome.
func TestEffortDoesNotVaryByModel(t *testing.T) {
	for _, model := range []string{"openai/gpt-4o-mini", "qwen2.5-coder:1.5b", "openrouter/auto"} {
		req := ask()
		req.Model = model
		req.Effort = llm.EffortXHigh

		params, err := buildParams(req)
		if err != nil {
			t.Errorf("model %q: %v", model, err)
			continue
		}
		if got := string(params.ReasoningEffort); got != "xhigh" {
			t.Errorf("model %q sent effort %q, want it unchanged", model, got)
		}
	}
}
