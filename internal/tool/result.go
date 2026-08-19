package tool

// Result is what a tool returns, split by audience: the model reads Content, the
// UI reads Title and Details. Tools return data and the UI decides how to draw
// it, which is what makes a headless run free; returning terminal output instead
// is pi's mistake, flagged in their own analysis, because those tools cannot be
// reused by any other frontend (design §3.4).
//
// A failing tool is not a Go error. A test that fails, a file that is not there,
// a command that exits non-zero: each is an observation the model needs, so it
// comes back as Result{IsError: true} with a nil error and the model adapts. A Go
// error means "this tool could not run at all", and the loop turns even that into
// an error result rather than propagating it (design §3.4, §12).
type Result struct {
	// Content and IsError are the model's entire view: the loop copies them into
	// an llm.Block of type tool_result, which has no field for Title or Details
	// to land in.
	Content string
	IsError bool

	Title string // one-line summary for a collapsed tool card

	// Details is the tool's own payload for the UI — a computed diff, an exit
	// code, a match list — which the model never sees, so a diff here costs no
	// tokens.
	//
	// Deliberately any rather than a marker interface: an MCP tool's structured
	// output is arbitrary decoded JSON, and narrowing this would force the MCP
	// adapter to wrap what it is required to pass through untouched.
	Details any
}

// DiffDetails is what a tool that changed a file puts in Details, and what the
// diff renderer draws (design §3.4).
type DiffDetails struct {
	Path                 string // relative to the workspace root
	Unified              string
	Additions, Deletions int

	// Fuzzy records that the text was found by a whitespace-normalized rung of
	// the edit ladder rather than byte for byte, so the UI can say so: the model
	// asked for one thing and got a match on another, and it must be told rather
	// than left to assume the file now reads as it wrote it.
	Fuzzy bool
}
