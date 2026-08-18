package anthropic

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
		if slices.Contains(supported, rung) {
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

// TestARefusedRungIsNamedBeforeAnythingElseFails pins where the guard sits. Both
// faults below are real; reporting the other one first sends the caller back for
// a second round of a refusal that was already true the first time.
func TestARefusedRungIsNamedBeforeAnythingElseFails(t *testing.T) {
	req := ask()
	req.Effort = llm.EffortNone
	req.System = []llm.SystemBlock{{Text: ""}}

	_, err := buildParams(req)
	if err == nil {
		t.Fatal("no error, though the request carries two things this adapter refuses")
	}
	if !strings.Contains(err.Error(), string(llm.EffortNone)) || strings.Contains(err.Error(), "system block") {
		t.Errorf("error = %v, want the one naming the level it could not send", err)
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

// The child process is told which thinking shape to send and where to record
// that it ran; the presence of the first is what tells it that it is a child.
const (
	shapeVar  = "RASP_TEST_THINKING_SHAPE"
	ranVar    = "RASP_TEST_HELPER_RAN"
	childTest = "TestOneTurnAtAThinkingShape"
)

const (
	shapeAdaptive = "adaptive"

	// shapeEnabled has to be forced onto the params by hand, because buildParams
	// never sends it. It is the control: without a run that does reach the
	// terminal, one wired to nothing reads exactly like an adapter staying quiet.
	shapeEnabled = "enabled"
)

// TestTheSDKIsKeptOffTheTerminal: stdout belongs to the UI, and stderr is no
// safer once a full-screen UI owns the screen. The witness is the SDK's
// deprecation warning, which the thinking shape alone decides.
//
// Each turn runs in a child process. Capturing in-process means assigning to the
// os.Stdout and os.Stderr variables, which every other goroutine still alive in
// this suite reads unsynchronised — under -race, a data race rather than a check.
// The boundary also catches a write to the file descriptor itself, which
// swapping the variable would not see.
func TestTheSDKIsKeptOffTheTerminal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		shape    string
		wantWarn bool
	}{
		{name: "the shape the adapter sends", shape: shapeAdaptive},
		{name: "the shape it avoids", shape: shapeEnabled, wantWarn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr := runTurn(t, tc.shape)

			if stdout != "" {
				t.Errorf("the child wrote to stdout, which belongs to the UI: %q", stdout)
			}
			switch {
			case tc.wantWarn && !strings.Contains(stderr, "deprecated"):
				t.Errorf("the SDK warned nowhere this could see it (stderr %q); if it has stopped "+
					"warning at all, the case above needs a new witness rather than a passing grade", stderr)
			case !tc.wantWarn && stderr != "":
				t.Errorf("the adapter wrote to stderr: %q", stderr)
			}
		})
	}
}

// TestOneTurnAtAThinkingShape is the child half of the test above, and skips
// unless that test launched it.
func TestOneTurnAtAThinkingShape(t *testing.T) {
	shape, ok := os.LookupEnv(shapeVar)
	if !ok {
		t.Skipf("child process of TestTheSDKIsKeptOffTheTerminal, which sets %s", shapeVar)
	}
	client := replay(t, "text.sse")

	req := ask()
	// One of the two models the SDK's deprecation warning fires for. ask() names a
	// model it says nothing about, so both shapes would be silent on that one.
	req.Model = "claude-opus-4-6"
	req.Thinking = llm.ThinkingConfig{Enabled: true}
	req.Effort = llm.EffortHigh

	switch shape {
	case shapeAdaptive:
		for range client.Stream(context.Background(), req) {
		}
	case shapeEnabled:
		params, err := buildParams(req)
		if err != nil {
			t.Fatalf("buildParams: %v", err)
		}
		params.Thinking = sdk.ThinkingConfigParamUnion{OfEnabled: &sdk.ThinkingConfigEnabledParam{BudgetTokens: 1024}}
		stream := client.api.Messages.NewStreaming(context.Background(), params)
		defer stream.Close()
		for stream.Next() {
		}
	default:
		t.Fatalf("unknown shape %q", shape)
	}

	path := os.Getenv(ranVar)
	if err := os.WriteFile(path, []byte(shape), 0o600); err != nil {
		t.Fatalf("marking the run at %s: %v", path, err)
	}
}

// runTurn re-executes this test binary to take one turn at the given thinking
// shape, and returns what a terminal would have shown.
func runTurn(t *testing.T, shape string) (stdout, stderr string) {
	t.Helper()

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating this test binary: %v", err)
	}
	ranPath := filepath.Join(t.TempDir(), "ran")

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, exe, "-test.run", "^"+childTest+"$")
	cmd.Env = append(childEnv(), shapeVar+"="+shape, ranVar+"="+ranPath)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	if err := cmd.Run(); err != nil {
		t.Fatalf("child process: %v\nstdout:\n%s\nstderr:\n%s", err, out.String(), errs.String())
	}
	// A -test.run pattern matching nothing exits 0 having run nothing, which is
	// indistinguishable from a turn that stayed off the terminal.
	if _, err := os.Stat(ranPath); err != nil {
		t.Fatalf("%s never ran, so a silent terminal proves nothing: %v", childTest, err)
	}
	return withoutVerdict(out.String()), errs.String()
}

// childEnv is this process's environment without the variables the SDK resolves
// credentials from. A developer's own ANTHROPIC_API_KEY or profile would draw
// warnings of its own onto the child's stderr, which is the stream under test.
func childEnv() []string {
	var kept []string
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "ANTHROPIC_") || strings.HasPrefix(key, "RASP_") {
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

// withoutVerdict drops the lines a test binary prints about itself — its result,
// and its coverage under -cover — which are the harness talking rather than the
// turn under test.
func withoutVerdict(out string) string {
	var kept []string
	for line := range strings.SplitSeq(out, "\n") {
		if line == "PASS" || strings.HasPrefix(line, "coverage:") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
