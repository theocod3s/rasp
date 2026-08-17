package anthropic

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/theocod3s/rasp/internal/llm"
)

// TestEffortsIsTheLadderMinusWhatTheEnumLacks pins the list a picker will draw
// from and a refusal reads. Written out rather than derived, so the two rungs
// Anthropic cannot express are named somewhere a reader can see them.
func TestEffortsIsTheLadderMinusWhatTheEnumLacks(t *testing.T) {
	want := []llm.Effort{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax}

	got := New(Config{APIKey: "test"}).Efforts()
	if !slices.Equal(got, want) {
		t.Fatalf("Efforts() = %v, want %v", got, want)
	}

	got[0] = "tampered"
	if again := New(Config{APIKey: "test"}).Efforts(); !slices.Equal(again, want) {
		t.Errorf("Efforts() = %v after a caller wrote to an earlier result, want %v", again, want)
	}
}

// TestEveryRungIsSentOrNamed walks the whole ladder against the one list the
// adapter keeps, which is what stops a picker offering a rung the request path
// refuses. A rung this API cannot express fails and says which — never quietly
// arriving as the nearest one that would have worked.
func TestEveryRungIsSentOrNamed(t *testing.T) {
	var sent, refused int
	for _, rung := range llm.EffortLadder() {
		req := ask()
		req.Effort = rung

		params, err := buildParams(req)
		if slices.Contains(efforts(), rung) {
			sent++
			if err != nil {
				t.Errorf("effort %q: %v, but it is offered", rung, err)
				continue
			}
			if got := string(params.OutputConfig.Effort); got != string(rung) {
				t.Errorf("effort %q went out as %q", rung, got)
			}
			continue
		}
		refused++
		if err == nil {
			t.Errorf("effort %q was accepted; the turn would run at whatever depth the API "+
				"defaults to, with nothing to say so", rung)
			continue
		}
		if !strings.Contains(err.Error(), string(rung)) {
			t.Errorf("error for %q = %v, want one naming the level it could not send", rung, err)
		}
	}
	if sent == 0 || refused == 0 {
		t.Fatalf("%d rungs sent and %d refused; one branch never ran, so half of this test "+
			"asserted nothing", sent, refused)
	}
}

// TestThinkingAndEffortAreIndependent: two neutral fields onto two sibling wire
// fields. Read off the socket, because an unset one is absent from the JSON
// rather than zero in it, and absence is invisible from the Go side.
func TestThinkingAndEffortAreIndependent(t *testing.T) {
	for _, tc := range []struct {
		name         string
		thinking     bool
		effort       llm.Effort
		wantThinking bool
		wantEffort   string
	}{
		{name: "neither"},
		{name: "thinking alone", thinking: true, wantThinking: true},
		{name: "effort alone", effort: llm.EffortHigh, wantEffort: "high"},
		{name: "both", thinking: true, effort: llm.EffortMax, wantThinking: true, wantEffort: "max"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := replay(t, "text.sse")

			req := ask()
			req.Thinking = llm.ThinkingConfig{Enabled: tc.thinking}
			req.Effort = tc.effort
			for range client.Stream(context.Background(), req) {
			}
			sent := client.sent(t)

			thinking, ok := sent["thinking"].(map[string]any)
			if ok != tc.wantThinking {
				t.Fatalf("thinking = %v, want present: %v", sent["thinking"], tc.wantThinking)
			}
			// Adaptive is the shape the models taking this API accept, and the one
			// the SDK does not warn about on stderr.
			if ok && thinking["type"] != "adaptive" {
				t.Errorf("thinking = %v, want the adaptive shape", thinking)
			}

			// Absence, not emptiness: an unset Effort sends no effort key at all,
			// leaving the depth to whatever the API does by default rather than to a
			// rung this adapter picked.
			config, _ := sent["output_config"].(map[string]any)
			level, hasEffort := config["effort"].(string)
			if hasEffort != (tc.wantEffort != "") || level != tc.wantEffort {
				t.Errorf("output_config = %v, want effort %q", sent["output_config"], tc.wantEffort)
			}
		})
	}
}

// TestEffortDoesNotVaryByModel: the list is per protocol, so nothing here reads a
// model id. xhigh is the rung that makes the difference visible — Anthropic's
// enum carries it and only some models accept it, and it is offered to all of
// them anyway. Deciding otherwise needs a catalog, which would break
// `openrouter/auto` (scope.md); a model that will not take it answers with an
// API error, which is the correct outcome.
func TestEffortDoesNotVaryByModel(t *testing.T) {
	for _, model := range []string{"claude-opus-5", "claude-3-haiku-20240307", "openrouter/auto"} {
		req := ask()
		req.Model = model
		req.Effort = llm.EffortXHigh

		params, err := buildParams(req)
		if err != nil {
			t.Errorf("model %q: %v", model, err)
			continue
		}
		if got := string(params.OutputConfig.Effort); got != "xhigh" {
			t.Errorf("model %q sent effort %q, want it unchanged", model, got)
		}
	}
}

// TestTheSDKIsKeptOffTheTerminal: stdout belongs to the UI, and stderr is no
// safer once a full-screen UI owns the screen.
func TestTheSDKIsKeptOffTheTerminal(t *testing.T) {
	client := replay(t, "text.sse")

	req := ask()
	// One of the two models the SDK's deprecation warning fires for. ask() names a
	// model it says nothing about, so this would pass on any thinking shape.
	req.Model = "claude-opus-4-6"
	req.Thinking = llm.ThinkingConfig{Enabled: true}
	req.Effort = llm.EffortHigh

	stdout, stderr := terminal(t, func() {
		for range client.Stream(context.Background(), req) {
		}
	})
	if stdout != "" || stderr != "" {
		t.Errorf("the adapter wrote to the terminal: stdout %q, stderr %q", stdout, stderr)
	}

	// The same request through the thinking shape this adapter avoids, plus a
	// marker on stdout. Without it, a capture wired to nothing reads exactly like
	// an adapter that stayed quiet.
	params, err := buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	params.Thinking = sdk.ThinkingConfigParamUnion{OfEnabled: &sdk.ThinkingConfigEnabledParam{BudgetTokens: 1024}}

	stdout, stderr = terminal(t, func() {
		fmt.Fprint(os.Stdout, "control")
		stream := client.api.Messages.NewStreaming(context.Background(), params)
		defer stream.Close()
		for stream.Next() {
		}
	})
	if stdout != "control" {
		t.Errorf("the stdout capture returned %q, so the check above could not have failed", stdout)
	}
	if !strings.Contains(stderr, "deprecated") {
		t.Errorf("the SDK warned nowhere the capture could see it (%q), so the check above "+
			"could not have failed", stderr)
	}
}

// terminal runs fn with the process's stdout and stderr replaced by pipes, and
// returns what was written to each.
func terminal(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()

	outR, outW := osPipe(t)
	errR, errW := osPipe(t)
	prevOut, prevErr := os.Stdout, os.Stderr
	restore := func() { os.Stdout, os.Stderr = prevOut, prevErr }
	os.Stdout, os.Stderr = outW, errW
	defer restore()

	fn()

	restore()
	// Closed before the read: io.ReadAll on a pipe whose writer is still open
	// blocks until it is, which would hang the test rather than fail it.
	outW.Close()
	errW.Close()
	return readAll(t, outR), readAll(t, errR)
}

func osPipe(t *testing.T) (r, w *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	return r, w
}

func readAll(t *testing.T, f *os.File) string {
	t.Helper()
	defer f.Close()
	captured, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("reading a captured stream: %v", err)
	}
	return string(captured)
}
