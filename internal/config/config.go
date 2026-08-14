package config

// Config is the resolved settings file, design §10's schema in Go. Every field
// is optional; the defaults layer supplies what a user leaves out. Values are
// held exactly as written, so a secret-bearing string may still read
// `$(op read …)` here — expanding it is the resolver's job.
type Config struct {
	// Schema is the `$schema` key. rasp never reads it; editors do.
	Schema string `json:"$schema,omitempty"`

	Model      string `json:"model,omitempty"`
	SmallModel string `json:"small_model,omitempty"`
	Mode       string `json:"mode,omitempty"`

	Providers map[string]Provider `json:"providers,omitempty"`

	// Modes overrides a built-in permission preset, deep-merged onto it, so a
	// user adds one bash pattern without restating the map. `modes.yolo` is never
	// present: yolo short-circuits ahead of pattern evaluation, so an override
	// could only create the false impression of a constraint (design §10).
	Modes map[string]ModePermissions `json:"modes,omitempty"`

	MCP     MCP     `json:"mcp,omitzero"`
	Context Context `json:"context,omitzero"`
	UI      UI      `json:"ui,omitzero"`
}

// Provider is one entry under `providers`. An empty BaseURL means the adapter's
// default endpoint.
type Provider struct {
	APIKey  string   `json:"api_key,omitempty"`
	BaseURL string   `json:"base_url,omitempty"`
	Models  []string `json:"models,omitempty"`
}

// ModePermissions mirrors a permission set without naming its types.
// internal/permission owns those (design §7.1) and config is a leaf package that
// may not import it (design §1), so the rules stay strings here.
type ModePermissions struct {
	Read  string            `json:"read,omitempty"`
	Write string            `json:"write,omitempty"`
	Edit  string            `json:"edit,omitempty"`
	Fetch string            `json:"fetch,omitempty"`
	Bash  map[string]string `json:"bash,omitempty"`
	MCP   map[string]string `json:"mcp,omitempty"`
}

// MCP holds the server table and the tool-count budget.
type MCP struct {
	MaxTotalTools int                  `json:"max_total_tools,omitempty"`
	Servers       map[string]MCPServer `json:"servers,omitempty"`
}

// MCPServer describes one stdio server to spawn. Timeout stays the string the
// user wrote (`"10s"`); internal/mcp parses it, being the package that has to
// live with what it means.
type MCPServer struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Tools   []string          `json:"tools,omitempty"`
	Timeout string            `json:"timeout,omitempty"`
}

// Context configures the system prompt and the room held back for the reply.
type Context struct {
	Files         []string `json:"files,omitempty"`
	ReserveTokens int      `json:"reserve_tokens,omitempty"`
}

// UI is the one section with no effect on what the model is sent.
type UI struct {
	Theme string `json:"theme,omitempty"`
	Diff  string `json:"diff,omitempty"`
}

// Mode names. internal/permission owns the Mode type and its presets (design
// §7.1); config knows only the names, to reject one of them from a project file
// and catch a typo before startup. Importing permission would make config a
// non-leaf package, so a mode added there has to be added here too.
const (
	ModePlan   = "plan"
	ModeManual = "manual"
	ModeAuto   = "auto"
	ModeYolo   = "yolo"
)

// modeNames is every mode a config file may name, in cycle order with yolo last.
// Whether a given layer may select one is validate.go's business.
var modeNames = []string{ModePlan, ModeManual, ModeAuto, ModeYolo}

// Defaults are the built-in values, the lowest layer of the precedence chain. A
// tree, because the merge has to see all five layers the same way.
//
// small_model is set here rather than left to §11's fall back to model: shipping
// it unset would summarize 100k tokens on a flagship model, repeatedly, on
// exactly the long sessions where compaction fires.
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
			// (prd §6.5), and this is the order they are tried.
			"files":          []any{"AGENTS.md", "CLAUDE.md"},
			"reserve_tokens": jsonNumber(16384),
		},
		"ui": map[string]any{
			"theme": "auto",
			"diff":  "unified",
		},
	}
}
