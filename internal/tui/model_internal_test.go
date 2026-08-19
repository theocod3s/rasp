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
// reads the frame back. The events are shaped as the loop emits them, tool name
// on both ends of a call included (agent/tools.go).
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
		{Kind: agent.EventToolEnd, CallID: "call_2", Tool: "write", Result: &tool.Result{IsError: true}},
	} {
		m, _ = m.Update(agentMsg{event: ev})
	}

	frame := m.View().Content
	for _, want := range []string{"Reading it now.", "read: running", "write: failed", "working…"} {
		if !strings.Contains(frame, want) {
			t.Errorf("the frame does not mention %q:\n%s", want, frame)
		}
	}
	// A call that ended is drawn where it started rather than at the end of the
	// conversation, which is what makes a transcript readable a step later.
	if running, failed := strings.Index(frame, "read: running"), strings.Index(frame, "write: failed"); running > failed {
		t.Errorf("the call that finished jumped ahead of the one still running:\n%s", frame)
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
	// The reply is committed once. A delta whose item the end event failed to
	// replace would draw it twice.
	if n := strings.Count(frame, "Reading it now."); n != 1 {
		t.Errorf("the frame holds the reply %d time(s):\n%s", n, frame)
	}
}

// TestEachStepsReplyIsAnItemOfItsOwn. A turn runs as many steps as the model
// asks for and every one of them ends with an assistant message, so the deltas
// of one step have to replace each other and the next step's have to start
// somewhere new. Getting that wrong loses everything the model said before it
// called a tool.
func TestEachStepsReplyIsAnItemOfItsOwn(t *testing.T) {
	first, second := reply("Reading it now."), reply("The header is parsed twice.")

	var m tea.Model = Model{}
	for _, ev := range []agent.Event{
		{Kind: agent.EventAssistantDelta, Message: first},
		{Kind: agent.EventAssistantEnd, Message: first},
		{Kind: agent.EventToolStart, CallID: "call_1", Tool: "read"},
		{Kind: agent.EventToolEnd, CallID: "call_1", Tool: "read", Result: &tool.Result{}},
		{Kind: agent.EventAssistantDelta, Message: second},
		{Kind: agent.EventAssistantEnd, Message: second},
	} {
		m, _ = m.Update(agentMsg{event: ev})
	}

	frame := m.View().Content
	for _, want := range []string{"Reading it now.", "read: done", "The header is parsed twice."} {
		if !strings.Contains(frame, want) {
			t.Errorf("the frame does not mention %q:\n%s", want, frame)
		}
	}
	if root, held := m.(Model), 3; root.chat.Len() != held {
		t.Errorf("the conversation holds %d items; two replies and the call between them are %d",
			root.chat.Len(), held)
	}
}

func reply(text string) *llm.Message {
	return &llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.Block{{Type: llm.BlockText, Text: text}},
	}
}
