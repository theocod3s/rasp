package config

// Config is the resolved settings file, design §10's schema in Go.
//
// Every field is optional in the file — the built-in defaults layer supplies
// what a user leaves out, so a zero Config is never what a caller receives from
// Load. Values are held exactly as they were written: a secret-bearing string
// may still read `$(op read …)` here, because expanding it is the resolver's
// job and not this type's.
type Config struct {
	// Schema is the `$schema` key. rasp never reads it; editors do.
	Schema string `json:"$schema,omitempty"`

	Model      string `json:"model,omitempty"`
	SmallModel string `json:"small_model,omitempty"`
	Mode       string `json:"mode,omitempty"`

	Providers map[string]Provider `json:"providers,omitempty"`

	// Modes overrides a built-in permission preset, deep-merged onto it, so a
	// user adds one bash pattern without restating the map. `modes.yolo` is
	// never present here: it is dropped during validation, because yolo
	// short-circuits ahead of pattern evaluation and an override could only
	// create the false impression of a constraint (design §10).
	Modes map[string]ModePermissions `json:"modes,omitempty"`

	MCP     MCP     `json:"mcp,omitzero"`
	Context Context `json:"context,omitzero"`
	UI      UI      `json:"ui,omitzero"`
}

// Provider is one entry under `providers`. An empty BaseURL means the adapter's
// own default endpoint.
type Provider struct {
	APIKey  string   `json:"api_key,omitempty"`
	BaseURL string   `json:"base_url,omitempty"`
	Models  []string `json:"models,omitempty"`
}

// ModePermissions mirrors the shape of a permission set without naming its
// types. internal/permission owns those (design §7.1) and config is a leaf
// package that may not import it (design §1), so the rules stay strings here
// and are interpreted where they are enforced.
type ModePermissions struct {
	Read  string            `json:"read,omitempty"`
	Write string            `json:"write,omitempty"`
	Edit  string            `json:"edit,omitempty"`
	Fetch string            `json:"fetch,omitempty"`
	Bash  map[string]string `json:"bash,omitempty"`
	MCP   map[string]string `json:"mcp,omitempty"`
}

// MCP holds the server table and the tool-count budget that keeps a chatty
// server from eating the context window.
type MCP struct {
	MaxTotalTools int                  `json:"max_total_tools,omitempty"`
	Servers       map[string]MCPServer `json:"servers,omitempty"`
}

// MCPServer describes one stdio server to spawn. Timeout stays the string the
// user wrote (`"10s"`); internal/mcp parses it, since that is the package that
// has to live with what it means.
type MCPServer struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Tools   []string          `json:"tools,omitempty"`
	Timeout string            `json:"timeout,omitempty"`
}

// Context configures what goes into the system prompt and how much room is
// held back for the reply.
type Context struct {
	Files         []string `json:"files,omitempty"`
	ReserveTokens int      `json:"reserve_tokens,omitempty"`
}

// UI holds presentation settings. It is the one section with no effect on what
// the model is sent.
type UI struct {
	Theme string `json:"theme,omitempty"`
	Diff  string `json:"diff,omitempty"`
}

// Mode names. internal/permission owns the Mode type and its presets
// (design §7.1); config knows only the names, because it has to reject one of
// them from a project file and because a typo silently resolving to something
// else is worth catching before startup. Importing permission would make config
// a non-leaf package, so this list is duplicated deliberately — if a mode is
// ever added there, it has to be added here too.
const (
	ModePlan   = "plan"
	ModeManual = "manual"
	ModeAuto   = "auto"
	ModeYolo   = "yolo"
)

// modeNames is every mode a config file may name, in cycle order with yolo
// last. Membership is checked; whether a given layer may select it is
// validate.go's business.
var modeNames = []string{ModePlan, ModeManual, ModeAuto, ModeYolo}

// Defaults are the built-in values, the lowest layer of the precedence chain.
//
// It returns a tree rather than a Config because that is the form every other
// layer takes, and the merge has to see all five layers the same way.
//
// small_model is set here rather than left to §11's fall back to model. That
// fallback exists for anyone who clears the key, but shipping it unset would
// mean summarizing 100k tokens on a flagship model, repeatedly, on exactly the
// long sessions where compaction fires — a cost nobody would ever see.
func Defaults() map[string]any {
	return map[string]any{
		"model":       "anthropic/claude-opus-5",
		"small_model": "anthropic/claude-haiku-4-5",
		"mode":        ModeManual,
		"mcp": map[string]any{
			"max_total_tools": jsonNumber(60),
		},
		"context": map[string]any{
			// AGENTS.md is the name rasp discovers; CLAUDE.md is accepted
			// (prd §6.5). Order is the order they are tried.
			"files":          []any{"AGENTS.md", "CLAUDE.md"},
			"reserve_tokens": jsonNumber(16384),
		},
		"ui": map[string]any{
			"theme": "auto",
			"diff":  "unified",
		},
	}
}
