package tui

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
)

// settle is how long a test waits for something that should already have
// happened. Generous, because it is only ever reached on the failing path.
const settle = 5 * time.Second

// spy is a sender that records what the pump delivered, and notices two
// deliveries overlapping.
type spy struct {
	mu       sync.Mutex
	got      []agent.Event
	overlaps int
	inside   atomic.Int32

	// hold, when non-nil, parks every Send until release is called.
	hold    chan struct{}
	release sync.Once

	// want is closed once got reaches its length.
	target int
	want   chan struct{}
}

func newSpy(target int) *spy {
	return &spy{target: target, want: make(chan struct{})}
}

func (s *spy) Send(msg tea.Msg) {
	// A single drainer is the property under test, so the overlap is counted
	// rather than assumed away: two goroutines pumping would both be in here.
	if s.inside.Add(1) > 1 {
		s.mu.Lock()
		s.overlaps++
		s.mu.Unlock()
	}
	// Wide enough that a second pump would have to miss it, rather than merely
	// unlikely to hit it.
	time.Sleep(50 * time.Microsecond)

	if s.hold != nil {
		<-s.hold
	}

	s.mu.Lock()
	if ev, ok := msg.(agentMsg); ok {
		s.got = append(s.got, ev.event)
	}
	if len(s.got) == s.target {
		close(s.want)
	}
	s.mu.Unlock()
	s.inside.Add(-1)
}

// resume lets every parked Send through. Called from the test's own defer as
// well as its body: a spy still holding is a pump that cannot be stopped, so a
// test that fails early would hang in its cleanup rather than report.
func (s *spy) resume() {
	if s.hold != nil {
		s.release.Do(func() { close(s.hold) })
	}
}

func (s *spy) events() []agent.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agent.Event(nil), s.got...)
}

func (s *spy) overlapped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overlaps
}

// parked waits until the pump has taken an event out of the mailbox and entered
// a Send it cannot leave.
func parked(t *testing.T, s *spy) {
	t.Helper()
	for deadline := time.Now().Add(settle); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if s.inside.Load() > 0 {
			return
		}
	}
	t.Fatal("the pump never reached the spy, so the mailbox filled behind nothing")
}

func (s *spy) await(t *testing.T, what string) {
	t.Helper()
	select {
	case <-s.want:
	case <-time.After(settle):
		t.Fatalf("waited %s for %s; the pump delivered %d of %d", settle, what, len(s.events()), s.target)
	}
}

// TestThePumpIsOneGoroutineAndKeepsOrder holds the shape of the agent → UI
// bridge: one drainer, so events reach Update in the order the turn produced
// them and no consumer needs to reassemble anything.
func TestThePumpIsOneGoroutineAndKeepsOrder(t *testing.T) {
	const events = 400

	s := newSpy(events)
	b := newBridge()
	b.start(s)
	defer b.stop()

	for i := range events {
		// A lossless kind, so this asserts on every event rather than on
		// whichever survived.
		b.handle(agent.Event{Kind: agent.EventToolStart, CallID: fmt.Sprintf("call_%d", i)})
	}
	s.await(t, "every event the turn produced")

	if n := s.overlapped(); n != 0 {
		t.Errorf("two deliveries overlapped %d time(s), so more than one goroutine is draining the bridge", n)
	}
	got := s.events()
	if len(got) != events {
		t.Fatalf("the pump delivered %d event(s) of %d", len(got), events)
	}
	for i, ev := range got {
		if want := fmt.Sprintf("call_%d", i); ev.CallID != want {
			t.Fatalf("event %d of %d is %q, and the turn produced %q there; a bridge that reorders "+
				"leaves the UI to sort a stream out for itself", i, len(got), ev.CallID, want)
		}
	}
}

// TestAFullMailboxDropsDeltasAndNeverTheTurnsEnd is the bridge's back-pressure
// rule in both directions. A delta may be lost, because the next one carries the
// whole message again; a turn that ended may not, because nothing later says so
// and the UI would stay busy for the rest of the session.
func TestAFullMailboxDropsDeltasAndNeverTheTurnsEnd(t *testing.T) {
	const deltas = mailbox * 4

	// A mailbox's worth, plus the one the pump is parked on, plus the turn's end.
	// Exact rather than approximate: the pump takes one and blocks there for the
	// whole of the push, so nothing else can leave the mailbox to make room.
	s := newSpy(mailbox + 2)
	s.hold = make(chan struct{})

	b := newBridge()
	b.start(s)
	defer func() {
		s.resume()
		b.stop()
	}()

	// The pump has to be inside a Send before the flood starts, and waited for
	// rather than assumed: a pump that took its first delta out of a mailbox
	// already full leaves 128 behind it instead of 129, and the count above — the
	// only thing await can key on — is then one that never arrives.
	b.handle(agent.Event{Kind: agent.EventAssistantDelta})
	parked(t, s)

	// In a goroutine with a deadline rather than inline: a delta that blocks on a
	// full mailbox is this test's other failure, and inline it would hang the run
	// instead of reporting it.
	pushed := make(chan struct{})
	go func() {
		defer close(pushed)
		for range deltas {
			b.handle(agent.Event{Kind: agent.EventAssistantDelta})
		}
	}()
	select {
	case <-pushed:
	case <-time.After(settle):
		t.Fatalf("handing %d deltas to a bridge nobody is draining blocked; a delta is the one kind "+
			"a full mailbox may drop, precisely so the turn is not stalled by a slow UI", deltas)
	}

	ended := make(chan struct{})
	go func() {
		defer close(ended)
		b.handle(agent.Event{Kind: agent.EventTurnEnd})
	}()

	s.resume()
	s.await(t, "the turn's end")

	select {
	case <-ended:
	case <-time.After(settle):
		t.Fatal("the turn's end reached the UI but handle never returned")
	}
	got := s.events()

	// The check this test rests on: if nothing was dropped the mailbox was never
	// full, and everything below passes without having been tested.
	if len(got) >= deltas+1 {
		t.Fatalf("the pump delivered %d of %d events, so the mailbox never filled and this test "+
			"proved nothing about a full one", len(got), deltas+1)
	}
	if last := got[len(got)-1]; last.Kind != agent.EventTurnEnd {
		t.Fatalf("the last event to reach the UI is %q, and the turn ended with %q; the end has to "+
			"arrive after the deltas it follows", last.Kind, agent.EventTurnEnd)
	}
	ends := 0
	for _, ev := range got {
		if ev.Kind == agent.EventTurnEnd {
			ends++
		}
	}
	if ends != 1 {
		t.Fatalf("the UI saw the turn end %d time(s), and the turn ended once", ends)
	}
}

// TestDetachCopiesTheAccumulationAnEventCarries pins the copy the streaming test
// exercises but cannot name: a bridge handing on the provider's own pointer
// passes every assertion about content and races on the next delta.
func TestDetachCopiesTheAccumulationAnEventCarries(t *testing.T) {
	live := &llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.Block{{Type: llm.BlockText, Text: "Look"}},
	}

	ev := detach(agent.Event{Kind: agent.EventAssistantDelta, Message: live})
	if ev.Message == live {
		t.Fatal("detach handed back the provider's own message; it is mutated in place for the rest " +
			"of the step, so Update and View would be reading what the turn is still writing")
	}

	live.Content[0].Text += "ing at it."
	live.Content = append(live.Content, llm.Block{Type: llm.BlockText, Text: " And more."})

	if got := spoken(*ev.Message); got != "Look" {
		t.Errorf("the detached message reads %q after the provider went on writing; it read %q when "+
			"it was taken", got, "Look")
	}
}
