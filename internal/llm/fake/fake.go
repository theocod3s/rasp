package fake

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/theocod3s/rasp/internal/llm"
)

// ProviderID is what a scripted turn records as its provider.
const ProviderID = "fake"

// Provider is a scripted llm.Provider: each call to Stream plays the next turn
// of the script.
type Provider struct {
	turns [][]Step

	mu       sync.Mutex
	played   int
	requests []llm.Request
}

var _ llm.Provider = (*Provider)(nil)

// New builds a provider from a flat list of steps. A turn is the run of steps up
// to and including a Done or Fail, so a two-turn script is one list:
//
//	p := fake.New(
//		fake.Text("Let me look at that."),
//		fake.ToolCall("read", `{"path":"main.go"}`),
//		fake.Done(llm.StopToolUse),
//
//		fake.Text("It prints hello."),
//		fake.Done(llm.StopEndTurn),
//	)
//
// The boundary is where the stream ends rather than a nesting the caller keeps in
// step, which is why there is no Turn() to forget.
//
// Every turn is played through llm.CheckStream here, and New panics with what it
// said — so a mis-scripted turn is reported against the script rather than as a
// loop test failing several steps later. This is the whole of the fake's contract
// compliance: it holds for whatever a test scripts, not only for the shapes this
// package's own tests cover.
//
// New() with no steps is legal, and is the assertion that the loop never reaches
// the provider: every Stream call on it panics.
//
// Tool call ids are stamped here, `call_1` upward in script order across every
// turn, so a test can name one without reading it back out of an event.
func New(script ...Step) *Provider {
	var (
		p     Provider
		turn  []Step
		calls int
	)
	for _, step := range script {
		if step.kind == kindToolCall || step.kind == kindUnfinishedToolCall {
			calls++
			step.id = fmt.Sprintf("call_%d", calls)
		}
		turn = append(turn, step)
		if step.kind == kindDone || step.kind == kindFail {
			p.turns = append(p.turns, turn)
			turn = nil
		}
	}
	// Trailing steps are a turn that never ends: kept, so the check below rejects
	// it rather than a rule duplicated from it.
	if len(turn) > 0 {
		p.turns = append(p.turns, turn)
	}

	for i, steps := range p.turns {
		if _, err := llm.CheckStream(play(context.Background(), llm.Request{}, steps, withoutHooks)); err != nil {
			panic(fmt.Sprintf("fake.New: turn %d of the script would break the stream contract: %v", i+1, err))
		}
	}
	return &p
}

func (p *Provider) ID() string { return ProviderID }

// Efforts is every rung, so a test asking for depth tests the thing it named
// rather than this list. A test about a provider refusing a rung wants an adapter,
// or a double of its own.
func (p *Provider) Efforts() []llm.Effort { return llm.EffortLadder() }

// Stream plays the next turn of the script.
//
// Running past the end of it panics rather than ending the turn with an
// EventError: a turn nobody scripted is a bug in the test or in the loop, and an
// error indistinguishable from a scripted one would have a loop retrying it until
// the test timed out.
func (p *Provider) Stream(ctx context.Context, req llm.Request) llm.StreamResponse {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.played >= len(p.turns) {
		panic(fmt.Sprintf("fake: Stream called %d time(s), and the script holds %d turn(s)",
			p.played+1, len(p.turns)))
	}
	steps := p.turns[p.played]
	p.played++
	p.requests = append(p.requests, record(req))

	return play(ctx, req, steps, withHooks)
}

// Requests is every Request handed to Stream, in order — what a test asserts the
// loop built, since a request is otherwise visible nowhere above the provider.
func (p *Provider) Requests() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.requests)
}

// record copies the slices a request arrived with, because a slice header shares
// its elements: a caller editing the transcript it holds after the turn would
// otherwise rewrite what a test reads back as "the request the loop sent".
func record(req llm.Request) llm.Request {
	req.System = slices.Clone(req.System)
	req.Tools = slices.Clone(req.Tools)
	req.Messages = slices.Clone(req.Messages)
	for i := range req.Messages {
		req.Messages[i].Content = slices.Clone(req.Messages[i].Content)
	}
	return req
}
