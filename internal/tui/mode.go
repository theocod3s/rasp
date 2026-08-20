package tui

import (
	"errors"

	"github.com/theocod3s/rasp/internal/permission"
)

var errNoPermissions = errors.New("this session has no permission service, so there is no mode to switch")

// cycleModes is the rotation Shift+Tab walks (design §7.8). Yolo is absent, and
// the absence is the mechanism: the cycle can only hand back an element of this
// array, and internal/permission has no yolo Mode for one to name — it arms a
// bypass ahead of the ladder instead (permission/service.go). Yolo is arrived at
// by being asked for.
var cycleModes = [...]permission.Mode{permission.ModePlan, permission.ModeManual, permission.ModeAuto}

// nextMode advances the cycle, and drops a mode outside it to manual: leaving
// one should always be easy, and manual is the mode that asks about everything.
func nextMode(mode permission.Mode) permission.Mode {
	for i, m := range cycleModes {
		if m == mode {
			return cycleModes[(i+1)%len(cycleModes)]
		}
	}
	return permission.ModeManual
}

// cycleMode is Shift+Tab, and does not wait for a running turn: a mode is read
// at the moment of each permission check, so the call already running finishes
// under the mode that approved it and the next meets this one (design §7.4).
func (m Model) cycleMode() Model {
	next := nextMode(m.status.modeName())
	if m.status.yolo {
		// The one press that leaves rather than advances. With the bypass armed
		// the mode under it is not what the session is running, so "the next one
		// along" would be advancing a name nothing is enforcing — and manual, which
		// asks about everything, is where leaving yolo should land.
		next = permission.ModeManual
	}
	switched, err := m.setMode(next)
	if err != nil {
		// Said out loud: a key that silently does nothing reads as a terminal
		// eating it, and the user leans on it again.
		return m.say(err.Error())
	}
	return switched
}

// setMode is the whole of a mode change, deliberately not three calls: the rules
// the ladder runs under, the line saying which mode the session is in, and the
// sentence the model is told (design §7.4, §7.5). Split up, a status line can say
// plan over a session still running the manual rules, and a model told nothing
// keeps proposing edits the ladder now refuses.
func (m Model) setMode(mode permission.Mode) (Model, error) {
	if m.permissions == nil {
		return m, errNoPermissions
	}
	if err := m.permissions.SetMode(mode); err != nil {
		return m, err
	}
	m.status.mode = mode
	// Installing a mode's rules ends the bypass in the service too
	// (permission/service.go); the badge goes with it, or the line keeps saying
	// yolo over a session that is gated again.
	m.status.yolo = false

	// The same words drawn and sent, so what the user reads and what the model is
	// told cannot drift.
	m.reminder = modeReminder(mode)
	return m.say(m.reminder), nil
}

// modeReminder is what a switch tells the model, in design §7.5's wording. A
// mode with no wording of its own is named rather than passed over in silence.
func modeReminder(mode permission.Mode) string {
	switch mode {
	case permission.ModePlan:
		return "[Mode changed to plan. You can no longer edit or write files. Investigate and " +
			"propose a plan; the user will switch you to manual or auto to carry it out.]"
	case permission.ModeManual:
		return "[Mode changed to manual. You can edit and write files again, and every change and " +
			"every command that is not plainly read-only is put to the user before it runs.]"
	case permission.ModeAuto:
		return "[Mode changed to auto. Edits, writes and ordinary commands run without asking. A " +
			"few destructive commands still stop for the user, so a refusal there is not a mistake.]"
	}
	return "[Mode changed to " + string(mode) + ".]"
}

// sending composes what the next turn carries: the reminder a switch left
// waiting, then what the user typed, cleared so no later turn repeats it.
//
// It waits for a turn because nothing is sent between turns — which also lands it
// behind the cache breakpoint, where mode text has to sit for Shift+Tab to stay a
// casual key (design §7.6). Only the last switch survives: the model needs the
// constraints it is under, not the route taken to them.
func (m Model) sending(text string) (Model, string) {
	if m.reminder == "" {
		return m, text
	}
	sent := m.reminder + "\n\n" + text
	m.reminder = ""
	return m, sent
}
