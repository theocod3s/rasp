package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/tool"
)

// TestUpdateDrawsWhatATurnProduced walks one turn's events through Update and
// reads the frame back, which is the whole of what the skeleton owes the
// conversation view that replaces it.
func TestUpdateDrawsWhatATurnProduced(t *testing.T) {
	reply := &llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.Block{{Type: llm.BlockText, Text: "Reading it now."}},
	}

	var m tea.Model = Model{}
	for _, ev := range []agent.Event{
		{Kind: agent.EventAssistantDelta, Message: reply},
		{Kind: agent.EventAssistantEnd, Message: reply},
		{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"},
		{Kind: agent.EventToolStart, CallID: "call_2", Tool: "write"},
		{Kind: agent.EventToolEnd, CallID: "call_2", Result: &tool.Result{IsError: true}},
	} {
		m, _ = m.Update(agentMsg{event: ev})
	}

	frame := m.View().Content
	for _, want := range []string{"Reading it now.", "read: running", "write: failed", "working…"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the frame does not mention %q:\n%s", want, frame)
		}
	}

	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventError, Err: errors.New("the stream broke")}})
	m, _ = m.Update(agentMsg{event: agent.Event{Kind: agent.EventTurnEnd}})

	frame = m.View().Content
	if !strings.Contains(frame, "the stream broke") {
		t.Errorf("the frame does not carry the turn's error:\n%s", frame)
	}
	if strings.Contains(frame, "working…") {
		t.Errorf("the frame still says the turn is running after it ended:\n%s", frame)
	}
	// The reply is committed once. A delta that outlived its step would draw it
	// twice, which is what holding the streaming message apart prevents.
	if n := strings.Count(frame, "Reading it now."); n != 1 {
		t.Errorf("the frame holds the reply %d time(s):\n%s", n, frame)
	}
}
