// Package tool defines the Tool interface and Result, generates JSON Schema by
// reflection over a tagged input struct, and owns the registry along with the
// per-turn snapshot the loop takes from it.
//
// Does not contain: any actual tool, any UI, and no MCP protocol. It is the
// machinery tools plug into, never a tool itself.
package tool
