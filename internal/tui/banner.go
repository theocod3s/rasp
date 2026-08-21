package tui

import (
	"os"

	"github.com/theocod3s/rasp/internal/permission"
	"github.com/theocod3s/rasp/internal/tui/chat"
)

// banner builds the session's identity block from cfg. Built here rather than
// read straight off cfg by chat.Banner itself, so NO_COLOR — an environment
// variable — is read exactly once, at start-up, rather than by an item the
// view is otherwise free to treat as frozen forever (chat/view.go).
func banner(cfg Config) chat.Banner {
	mode := cfg.Mode
	if mode == "" {
		mode = permission.ModeManual
	}
	// The convention is presence, not value — NO_COLOR="" still means no colour
	// — so this is LookupEnv's ok rather than a check against the empty string.
	_, noColor := os.LookupEnv("NO_COLOR")
	return chat.Banner{
		Version: cfg.Version,
		Model:   cfg.Model,
		Mode:    string(mode),
		Cwd:     abbreviateHome(cfg.Cwd),
		NoColor: noColor,
	}
}
