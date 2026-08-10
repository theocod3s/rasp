// Command rasp is a coding agent for the terminal.
package main

import "fmt"

// version is injected at build time via -ldflags "-X main.version=...".
// See design §14.
var version = "dev"

func main() {
	// The Cobra root, flag parsing and subcommand wiring arrive with the
	// milestones that need them — run.go first, then config.go, with
	// session.go and mcp.go later still. Until then this keeps the tree
	// buildable and the version stamp honest.
	fmt.Printf("rasp %s\n", version)
}
