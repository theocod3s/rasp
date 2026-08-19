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

	// MaxOutputTokens caps one reply, and is sent as written: a cap above what the
	// model accepts is the API's to refuse. Not the compaction reserve, which is
	// derived from it — see Context.ReserveTokens.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`

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
	Files []string `json:"files,omitempty"`

	// ReserveTokens is the room compaction keeps free for the reply: design §11's
	// `max(maxOutput, 4096) + 12_000`, so it is derived from MaxOutputTokens and
	// larger than it. Reading either for the other would cap replies at the
	// reserve, or compact against the cap alone.
	ReserveTokens int `json:"reserve_tokens,omitempty"`
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

// Permission rules, as a config file writes them, repeated here for the reason
// the mode names are. permission.Compile refuses an unreadable one too; this
// side checks as well because it is the only one that still knows which file
// wrote it.
const (
	ruleAllow = "allow"
	ruleAsk   = "ask"
	ruleDeny  = "deny"
)

var ruleNames = []string{ruleAllow, ruleAsk, ruleDeny}

// Defaults are the built-in values, the lowest layer of the precedence chain. A
// tree, because the merge has to see all five layers the same way.
//
// small_model is set here rather than left to §11's fall back to model: shipping
// it unset would summarize 100k tokens on a flagship model, repeatedly, on
// exactly the long sessions where compaction fires.
//
// max_output_tokens is 16384 rather than the 8192 it was because hitting the cap
// fails a run instead of shortening a reply (decisions.md), so the wall belongs
// where truncating a text answer takes deliberate effort (design §10).
func Defaults() map[string]any {
	return map[string]any{
		"model":             "anthropic/claude-opus-5",
		"small_model":       "anthropic/claude-haiku-4-5",
		"mode":              ModeManual,
		"max_output_tokens": jsonNumber(16384),
		"mcp": map[string]any{
			"max_total_tools": jsonNumber(60),
		},
		"context": map[string]any{
			// AGENTS.md is the name rasp discovers; CLAUDE.md is accepted
			// (internals §5.1), and this is the order they are tried.
			"files":          []any{"AGENTS.md", "CLAUDE.md"},
			"reserve_tokens": jsonNumber(16384),
		},
		"ui": map[string]any{
			"theme": "auto",
			"diff":  "unified",
		},
	}
}
