package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tui/chat"
)

// yoloConfirm is the word that arms the bypass. Typed rather than pressed: the
// keys that answer a permission prompt are one press each, and one press is too
// little deliberation for switching every one of those prompts off. Turning it
// back off takes no confirmation at all — leaving should always be easy.
const yoloConfirm = "confirm"

const yoloWarning = "/yolo turns off every permission check for the rest of this session. Edits, " +
	"writes and any command at all run the moment the model asks for them, with nothing put to " +
	"you first and nothing refused — including the destructive commands auto still stops for. " +
	"It goes off again with /yolo, and it is off the next time rasp starts: it lives in this " +
	"process and is written nowhere. Type `/yolo confirm` to turn it on."

// yoloCaret marks the line the user is typing on while the bypass is armed.
// Both this and the status badge: a status line is one line among many on a full
// screen and is scrolled past, where the caret is where the eyes already are —
// so the risk stays visible while you type rather than only when you look down
// (design §7.8).
const yoloCaret = "⚠"

func yolo(m Model, args string) (Model, tea.Cmd) {
	if m.permissions == nil {
		return m.say("This session has no permission service, so there is nothing here for /yolo " +
			"to turn off."), nil
	}
	if m.status.yolo {
		// The confirmation word means "on" everywhere it is written — /help and the
		// warning both say so — so it must not be what turns it off for someone
		// typing it to check. Bare /yolo is the way out, and stays one word.
		if args == yoloConfirm {
			return m.say("Yolo is already on. /yolo on its own turns it off."), nil
		}
		return m.setYolo(false), nil
	}
	// Exactly the word and nothing else: parseCommand has already trimmed what
	// follows the name (command.go), so anything left over is a line that meant
	// something other than this.
	if args != yoloConfirm {
		return m.say(yoloWarning), nil
	}
	return m.setYolo(true), nil
}

// caret is what the input line is drawn behind.
func (m Model) caret() string {
	if !m.status.yolo {
		return chat.Caret
	}
	return yoloStyle.Render(yoloCaret) + " " + chat.Caret
}

// setYolo moves the bypass, the badge and what the model is told together, for
// the reason setMode does (mode.go): a screen saying one thing while the ladder
// does another is worse here than anywhere else in the UI.
func (m Model) setYolo(on bool) Model {
	m.permissions.SetYolo(on)
	m.status.yolo = on
	m.reminder = yoloReminder(on, m.status.modeName())
	return m.say(m.reminder)
}

// yoloReminder is what arming tells the model, in the shape design §7.5 gives a
// mode change — naming the constraint that moved rather than a mode, because no
// mode changed and the one underneath is still where turning it off lands.
func yoloReminder(on bool, mode permission.Mode) string {
	if on {
		return "[Yolo is on. Nothing is put to the user before it runs: every edit, every write " +
			"and every command is approved the moment you ask for it. Take the care the prompts " +
			"were taking.]"
	}
	return "[Yolo is off. The session is gated again, in " + string(mode) + ", and anything that " +
		"mode does not allow outright goes to the user before it runs.]"
}
