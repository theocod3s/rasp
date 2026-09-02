package tui

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/agent"
	"github.com/theocod3s/rasp/internal/llm"
	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tui/chat"
	"github.com/theocod3s/rasp/internal/tui/styles"
)

// Model is the root Bubble Tea model.
//
// Every field belongs to Update. View runs on the same goroutine but the pump
// and the turn do not, so a field written anywhere else is a data race against
// rendering — which is why the whole agent side arrives as a message and Update
// takes a value receiver (design §6 rule 1).
type Model struct {
	width, height int

	turner Turner

	// permissions answers the questions the permission service opened. nil is a
	// UI nothing composed a gate onto, where a question that arrives anyway is
	// drawn as a notice saying so rather than as a prompt (prompt.go).
	permissions Permissions

	// depth is the effort setting /effort drives, and nil is a UI composed
	// without a provider behind it (effort.go).
	depth Depth

	// asked is the questions waiting on the user, oldest first, and opened is
	// when the first of them was drawn — the two halves of prompt.go's state.
	asked  []permission.Request
	opened time.Time

	// reminder is what a mode switch has yet to tell the model, carried by the
	// next turn because nothing is sent between turns (mode.go).
	reminder string

	// ctx is the program's, and every turn's context descends from it. Bubble Tea
	// answers a signal by ending the event loop rather than by calling Update, so
	// a turn interrupted that way is never offered the key that cancels it.
	ctx context.Context

	// cancel stops the running turn, and is nil when none is (design §6 rule 7).
	cancel context.CancelFunc

	// input is what the user has typed and not yet sent.
	input string

	// chat is the conversation and its render cache. Items land here as their
	// events arrive, which is what keeps a reply, the tools it asked for and the
	// next reply in the order they happened.
	chat chat.View

	// replies counts the assistant messages that have finished, and so names the
	// one still arriving: every delta of a step replaces the same item, and the
	// next step's deltas start a new one.
	replies int

	// streaming is the reply the current step is still receiving, kept because
	// the turn may end without one: an interrupted or failed step never reaches
	// its assistant_end (agent/step.go), and settle needs what the user is
	// already looking at.
	streaming *llm.Message

	// status is the line under the conversation. It sits beside the chat rather
	// than inside it, so what it says can move without the conversation being
	// asked to draw itself again.
	status status

	// cards is every card drawn so far, keyed by cardKey, so one can be redrawn
	// without the event that made it: an elapsed time that has moved on, an
	// expansion the user has just asked for. Shared between copies of the model
	// as chat.View's own map is, and for the same reason.
	cards map[string]card

	// open is whether the reader has overridden how much of a card is shown, and
	// which way. Its zero value is "they have not", which is what lets a card
	// carry its own default: a file change opens and nothing else does.
	open openness

	// background is what the terminal answered the background query with, and
	// picks the palette a diff is drawn from.
	//
	// Nothing asks yet, so this is Dark for every real session: Bubble Tea sends
	// the query only for tea.RequestBackgroundColor, and issuing that command is
	// deliberately not done here. Glamour is still built with its own dark style
	// (chat/markdown.go), so a light terminal answering today would paint the
	// diffs light and leave every reply near-white on white — worse than the one
	// palette it has now. The command belongs in the change that gives glamour a
	// theme, and this side is ready for it.
	background styles.Background

	// beating says a redraw of the running cards is already on its way. Four
	// calls starting at once would otherwise leave four timers running, each
	// rescheduling itself.
	beating bool

	// armed is whether the last key was an Esc that asked to cancel the running
	// turn, so the next Esc confirms it instead of cancelling on the first press
	// a stray keystroke could send (design §6 rule 7). press clears it on any
	// other key, and finish clears it once the turn it was armed against ends,
	// however that happens — a stale arm from a turn that already finished
	// would confirm a cancel with nothing left to cancel.
	armed bool

	// quitArmed is Ctrl-C's own two-stage arm, the sibling to armed above and
	// cleared the same way, by press on any other key (design §6 rule 7). It
	// differs in one way: arming does not need a turn running, because a second
	// press is what guards the session itself, not one turn in flight — so
	// nothing clears it when a turn ends the way finish clears armed.
	quitArmed bool

	// turns counts turn goroutines still running. Bubble Tea's own shutdown
	// leaks a tea.Cmd goroutine rather than wait on one that can run as long as
	// a turn does, so Run waits on this instead — what stops ctrl+c returning
	// while the turn it just cancelled is still committing what it has (turn.go,
	// decisions.md). A pointer, shared by every copy of the model; allocated in
	// begin rather than here, so a model with no turn begun costs nothing extra.
	turns *sync.WaitGroup

	// now is the model's clock. nil is time.Now; a test sets it to name the
	// times the frames it compares were drawn between.
	now func() time.Time

	// started is when the running turn began, and is what the activity line's
	// elapsed time and spinner frame are measured from (activity.go). Stamped
	// wherever busy is raised rather than in begin alone: a model driven straight
	// from events has no begin behind it, and an unstamped start would date the
	// turn to the zero time.
	started time.Time

	busy bool
	err  error
}

// card is what the model needs to redraw one tool call: the item itself, and
// when the call started — which the item does not carry, because it renders an
// elapsed time rather than measuring one.
type card struct {
	item    chat.Call
	started time.Time
}

func newModel(ctx context.Context, turner Turner, cfg Config) Model {
	path, branch := place(cfg.Cwd)
	return Model{
		ctx:    ctx,
		turner: turner,
		depth:  cfg.Depth,
		status: status{model: cfg.Model, mode: cfg.Mode, path: path, branch: branch},
	}
}

func (m Model) clock() time.Time {
	if m.now == nil {
		return time.Now()
	}
	return m.now()
}

// busied marks a turn as running and stamps when it started, the two halves of
// one state. Idempotent, because a turn raises it once and every delta after
// that arrives with it already up.
func (m Model) busied() Model {
	if !m.busy {
		m.busy, m.started = true, m.clock()
	}
	return m
}

// replyKey and callKey are prefixed so that no provider call id can name an
// assistant message, whatever a server chooses to put in one.
func replyKey(n int) string    { return "reply/" + strconv.Itoa(n) }
func callKey(id string) string { return "call/" + id }

// cardKey is where a call's card lives, in both m.cards and the conversation:
// its own call id for an ordinary tool, so a batch of six reads draws six
// cards, and a name-only key for todos. The todos tool holds one list and
// rewrites the whole of it on every call (internal/tool/builtin/todos.go), so
// the card showing that list is one card too — every todos call, however many
// different call ids the model used to reach it, updates the same entry
// rather than appending a copy of a plan the next call already replaced.
func cardKey(id, name string) string {
	if name == "todos" {
		return name
	}
	return id
}

func (m Model) Init() tea.Cmd { return nil }

// Update routes the message and then makes sure the beat is running, in that
// order. The beat is armed here rather than by whatever started the turn
// because every route into a running turn — a keypress, an event arriving
// before one — comes through this method, and a route that forgot to ask would
// leave the activity line frozen while the turn ran on.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.route(msg)
	next, tick := next.animate()
	return next, tea.Batch(cmd, tick)
}

// animate schedules the beat that redraws the activity line and the running
// cards, and is a no-op with a beat already on its way or no turn to draw one
// for (beat, below).
func (m Model) animate() (Model, tea.Cmd) {
	if !m.busy {
		return m, nil
	}
	return m.pulse()
}

func (m Model) route(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.BackgroundColorMsg:
		return m.repaint(msg.IsDark()), nil
	case tea.KeyPressMsg:
		return m.press(msg)
	case agentMsg:
		return m.apply(msg.event)
	case promptMsg:
		return m.ask(msg.request), nil
	case tickMsg:
		return m.beat()
	case turnDone:
		return m.finish(msg), nil
	}
	return m, nil
}

// press matches on Code and Mod rather than on the key's printed name, so a
// binding is checked by the compiler instead of by a string that silently
// matches nothing.
func (m Model) press(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	key := msg.Key()
	ctrlC := key.Mod == tea.ModCtrl && key.Code == 'c'
	if key.Code != tea.KeyEscape {
		m.armed = false
	}
	if !ctrlC {
		m.quitArmed = false
	}
	switch {
	case ctrlC:
		return m.ctrlC()
	case key.Code == tea.KeyEscape:
		return m.escape(), nil
	case key.Mod == tea.ModShift && key.Code == tea.KeyTab:
		// Ahead of the question below, with the other keys that are not text:
		// the prompt takes the keyboard off the input line, not off the session.
		// A switch does not reopen the standing question either — that call was
		// already resolved to "ask" under the mode it was asked in, and answering
		// it again under a new one is the retroactivity design §7.4 rules out.
		return m.cycleMode(), nil
	case m.asking():
		// Every key while a question stands, not only the three that answer it:
		// the turn is blocked on the answer, so a line composed now has nowhere
		// to be sent (prompt.go).
		return m.answer(key), nil
	case key.Code == tea.KeyEnter:
		return m.submit()
	case key.Mod == tea.ModCtrl && key.Code == 'r':
		return m.expand(), nil
	case key.Code == tea.KeyBackspace:
		m.input = backspace(m.input)
	default:
		m.input += key.Text
	}
	return m, nil
}

// escape is Esc's two-stage cancel (design §6 rule 7): a turn with nothing
// running has nothing to arm, the first press against a running turn only
// arms, and the second confirms it by cancelling.
func (m Model) escape() Model {
	if !m.busy {
		return m
	}
	if !m.armed {
		m.armed = true
		return m
	}
	m.interrupt()
	m.armed = false
	return m
}

// ctrlC is Ctrl-C's two-stage arm, the sibling to escape: the first press
// cancels whatever turn is running — a no-op with none — and only arms the
// quit, so one slipped keystroke costs a turn rather than the session. The
// second press, while still armed, is what actually quits.
func (m Model) ctrlC() (Model, tea.Cmd) {
	if !m.quitArmed {
		// Here rather than left to the program's exit, so the turn is already
		// unwinding while the arm stands.
		m.interrupt()
		m.quitArmed = true
		return m, nil
	}
	return m, tea.Quit
}

func backspace(s string) string {
	if s == "" {
		return s
	}
	_, width := utf8.DecodeLastRuneInString(s)
	return s[:len(s)-width]
}

func (m Model) apply(ev agent.Event) (Model, tea.Cmd) {
	switch ev.Kind {
	case agent.EventAssistantDelta:
		m = m.busied()
		if ev.Message != nil {
			m.streaming = ev.Message
			m.chat.Set(replyKey(m.replies), m.replied(*ev.Message, false))
			m.status = m.status.streaming(ev.Message.Usage)
		}

	case agent.EventAssistantEnd:
		if ev.Message != nil {
			m.chat.Set(replyKey(m.replies), m.replied(*ev.Message, true))
			m = m.announce(*ev.Message)
			// The reconciliation point for whatever the deltas above reported: a
			// step's usage is final on the message that went into the transcript,
			// so this is what the counters land on however much of it arrived
			// early (status.go).
			m.status = m.status.call(ev.Message.Usage)
		}
		m.streaming = nil
		m.replies++

	case agent.EventToolStart:
		// A call running is a turn running: nothing dispatches one outside a turn,
		// so this is the state a model driven straight from events would otherwise
		// be missing — and the beat is armed off it (Update).
		m = m.busied().announced(ev.CallID, ev.Tool)
		key := cardKey(ev.CallID, ev.Tool)
		c := m.cards[key]
		c.started, c.item.State, c.item.Elapsed = m.clock(), chat.CallRunning, 0
		m = m.draw(key, c)

	case agent.EventToolEnd:
		m = m.announced(ev.CallID, ev.Tool)
		key := cardKey(ev.CallID, ev.Tool)
		c := m.cards[key]
		c.item.State, c.item.Result = chat.CallDone, ev.Result
		// Asked again now the result is here: whether the card opens itself is a
		// question about what it holds, and it held nothing until this event.
		c.item.Expanded = m.open.shows(c.item)
		if !c.started.IsZero() {
			c.item.Elapsed = m.clock().Sub(c.started)
		}
		m = m.draw(key, c)

	case agent.EventError:
		m = m.report(ev.Err)

	case agent.EventTurnEnd:
		// EventTurnEnd and turnDone (finish, turn.go) both mark the same turn
		// over, and arrive by different routes that make no promise about which
		// lands first — so busy and armed are cleared on both rather than one.
		// Leaving armed here would strand the hint on screen between this event
		// and finish for a turn there is nothing left to confirm a cancel on.
		m.busy = false
		m.armed = false
		m.status = m.status.turnEnded(ev.Usage)
		m = m.settle().dismissAll().closed()
	}
	return m, nil
}

// replied is one assistant message as the conversation draws it. The palette
// goes on here so a fourth call site cannot forget it and draw a step's
// thinking for the wrong background.
func (m Model) replied(msg llm.Message, done bool) chat.Message {
	return chat.Message{Content: msg, Done: done, Background: m.background}
}

// announce puts a card in the conversation for every call the step asked for,
// in the order the model asked for them. That order is not in the tool events
// themselves — a batch runs its calls at once, so tool_start arrives in
// whatever order they were scheduled — and it is on this message, which holds
// every tool_use block before the first of them runs (agent.Config.Events).
func (m Model) announce(msg llm.Message) Model {
	for _, block := range msg.Content {
		if block.Type == llm.BlockToolUse {
			m = m.announced(block.ID, block.Name)
		}
	}
	return m
}

// announced adds a queued card for a call the conversation has none for, so a
// tool event arriving without the assistant message that asked for it is drawn
// rather than dropped. A second todos call finds its key already taken by the
// first, so it leaves the existing card — still showing the prior list — up
// rather than resetting it to queued, which is what lets the card read as one
// continuously-updated list instead of flashing empty between calls.
func (m Model) announced(id, name string) Model {
	key := cardKey(id, name)
	if _, ok := m.cards[key]; ok {
		return m
	}
	if m.cards == nil {
		m.cards = make(map[string]card)
	}
	item := chat.Call{Name: name, Background: m.background}
	item.Expanded = m.open.shows(item)
	return m.draw(key, card{item: item})
}

// repaint moves every card onto the terminal's own palette. The whole list
// rather than the cards drawn after the answer, because a terminal answers the
// background query once the program is already running and a finished card's
// render is frozen at the colours it was first drawn in (internals §4.5).
func (m Model) repaint(isDark bool) Model {
	bg := styles.Dark
	if !isDark {
		bg = styles.Light
	}
	if bg == m.background {
		return m
	}

	m.background = bg
	for id, c := range m.cards {
		c.item.Background = bg
		m = m.draw(id, c)
	}
	return m
}

// draw stores c under key, a cardKey result rather than a raw call id, and
// puts it in the conversation at the matching callKey.
func (m Model) draw(key string, c card) Model {
	m.cards[key] = c
	m.chat.Set(callKey(key), c.item)
	return m
}

// openness is how much of every card is shown: what the cards themselves say
// until the reader takes a view, and then what the reader said.
//
// Modelled as the reader's opinion rather than as a bare "expanded" flag,
// because the default is the card's own — a diff opens itself — and a flag
// carrying both answers can only ever agree with one of them. It disagreed
// twice: the first press after a diff opened itself did nothing, and a card
// arriving after the reader collapsed the conversation opened anyway.
type openness int

const (
	openByDefault openness = iota
	openAll
	closeAll
)

// shows is whether a card is drawn open under this openness.
func (o openness) shows(c chat.Call) bool {
	switch o {
	case openAll:
		return true
	case closeAll:
		return false
	}
	return c.HasDiff()
}

// expand shows or hides every card's body. The whole conversation rather than
// the card under a selection, because nothing selects one yet: the transcript
// has no cursor over it, so there is no "this card" to mean.
//
// With no view taken yet, which way to go is read off the screen, so the press
// does what a reader looking at it expects. After that the reader's own last
// answer is the thing to flip — including on an empty conversation, where the
// screen says nothing and reading it would answer "open" forever.
func (m Model) expand() Model {
	switch {
	case m.open == openAll:
		m.open = closeAll
	case m.open == closeAll:
		m.open = openAll
	case m.anyShown():
		m.open = closeAll
	default:
		m.open = openAll
	}

	for id, c := range m.cards {
		c.item.Expanded = m.open.shows(c.item)
		m = m.draw(id, c)
	}
	return m
}

func (m Model) anyShown() bool {
	for _, c := range m.cards {
		if c.item.Expanded {
			return true
		}
	}
	return false
}

// tickInterval is how often the running parts of the frame are redrawn, at the
// tenth of a second a card shows an elapsed time to. It drives the spinner as
// well as the cards, which is why the activity line has no clock of its own.
// Nothing else in the frame moves with it: every finished item is frozen
// (internals §4.5), so a beat re-renders the cards still running and hands back
// a stored string for everything else.
const tickInterval = 100 * time.Millisecond

// tickMsg carries no time. The model reads its own clock instead, so the
// elapsed time a card shows and the moment a call was recorded as starting come
// from one source rather than two that can disagree.
type tickMsg struct{}

func (m Model) pulse() (Model, tea.Cmd) {
	if m.beating {
		return m, nil
	}
	m.beating = true
	return m, tea.Tick(tickInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

// beat moves the running cards on, and stops scheduling itself once the turn is
// over. A finished card is never written back here, so its render stays frozen
// at the text it ended on.
func (m Model) beat() (Model, tea.Cmd) {
	m.beating = false

	now := m.clock()
	for id, c := range m.cards {
		if c.item.State != chat.CallRunning {
			continue
		}
		c.item.Elapsed = now.Sub(c.started)
		m = m.draw(id, c)
	}

	// The turn is what the beat runs for, not the cards: the activity line
	// animates between two steps as well as during one, so stopping the moment a
	// batch finishes would freeze the spinner exactly where the model is
	// thinking. The turn ending is the whole of the stop condition — a card still
	// running past it is one whose tool_end went missing, and beating on would
	// redraw it ten times a second for the rest of the session.
	if !m.busy {
		return m, nil
	}
	return m.pulse()
}

// settle closes off a reply the turn ended in the middle of. Two things go
// wrong otherwise: the item is never finished, so every frame draws it again
// for the rest of the session, and its key stays live, so the next turn's first
// delta lands on it — drawing the new reply above the prompt that asked for it.
//
// On the turn's end and nowhere else. It is the last event a turn emits, where
// turnDone is a separate route into Update that can arrive before the events
// still in the pump; settling there would freeze a fragment and leave the real
// assistant_end to draw the reply a second time.
func (m Model) settle() Model {
	if m.streaming == nil {
		return m
	}
	m.chat.Set(replyKey(m.replies), m.replied(*m.streaming, true))
	m.streaming = nil
	m.replies++
	return m
}

func (m Model) View() tea.View {
	var b strings.Builder
	b.WriteString(m.chat.Render(m.width))
	if m.err != nil {
		// Blank first, when there is a transcript above to hold apart from — the
		// same one line of breathing room every item inside chat.View already gets
		// (chat/view.go), which this notice is drawn as though it were one of
		// without actually joining the conversation.
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		// Through the same notice path a command's own answer draws through,
		// styled in the accent that says "this went wrong" rather than as a bare
		// line — the one place left where an error was drawn in no colour at all.
		writeLine(&b, chat.Notice{
			Text:       "error: " + m.err.Error(),
			Kind:       chat.NoticeError,
			Background: m.background,
		}.Render(m.width))
	}
	switch {
	case m.busy:
		writeLine(&b, m.activity(m.width))
	case m.quitArmed:
		// The one arm that outlives a turn: ctrl+c guards the session rather than
		// what is running in it, so its hint has to be drawable with no activity
		// line to hang off (model.go quitArmed).
		writeLine(&b, hintQuit)
	}

	// The bottom chrome: the input inside its frame, and the footer under it.
	// The activity line stays above the frame — it belongs to the turn that is
	// running, not to the line the user is composing.
	rule := m.rule()
	writeLine(&b, rule)
	writeLine(&b, m.typing())
	writeLine(&b, rule)
	b.WriteString(m.status.Render(m.width, m.background))
	return tea.NewView(b.String())
}

func writeLine(b *strings.Builder, s string) {
	if s == "" {
		return
	}
	b.WriteString(s)
	b.WriteString("\n")
}
