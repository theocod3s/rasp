package wakelock

// Deliberately does not compile, and only for GOOS=windows — the _windows
// filename suffix is itself the build constraint. Nothing imports this package,
// so `just binary` cannot reach it; only the `go build ./...` line inside
// `just cross-compile` can. If the windows matrix cells go red and nothing else
// does, that line is doing the work it was added for.
func broken() {
	var x int = "not an int"
	_ = x
}
