package tui

import (
	"os"
	"path/filepath"
	"strings"
)

// place is where the session is working, as the footer says it: the workspace
// root abbreviated to ~, and the branch checked out there.
//
// A caller that named no root gets neither, rather than the process's own
// directory: the root is the path every tool call is checked against
// (internal/workspace), and a UI that guessed one would name somewhere the
// session is not confined to.
func place(cwd string) (path, branch string) {
	if cwd == "" {
		return "", ""
	}
	return abbreviateHome(cwd), branchAt(cwd)
}

// abbreviateHome shortens path to ~ where it names the user's home directory
// or somewhere under it, and leaves path alone otherwise — including when the
// home directory itself could not be read, where an unabbreviated cwd is still
// a correct one.
func abbreviateHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+string(os.PathSeparator)); ok {
		return "~" + string(os.PathSeparator) + rest
	}
	return path
}

// branchAt is the branch checked out in the repository holding dir, and empty
// for everything else: a detached HEAD, a directory no repository holds, a
// `.git` this process cannot read. Silent in all of those — a footer is not
// where a git problem earns a line.
//
// Read once, when the session starts, and never again. The footer is redrawn on
// every keystroke and ten times a second while a turn runs, so following a
// checkout would cost a stat per frame to notice something that moves a handful
// of times a day; a session whose branch changes under it names the one it
// started on until rasp is restarted.
//
// The walk leaves the workspace root on purpose — rasp started in a package
// directory is still inside the repository — and is not a tool's file access:
// it opens two fixed names, never a path anything asked for.
func branchAt(dir string) string {
	for dir != "" {
		if git := gitDir(dir); git != "" {
			return headBranch(git)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// gitDir is the repository directory belonging to dir, and empty where dir is
// not the root of one. A linked worktree and a submodule both keep a `.git`
// *file* holding `gitdir: <path>` rather than the directory itself — and a
// worktree is a checkout rasp is expected to run in, so the redirection is
// followed rather than read as "no repository here".
func gitDir(dir string) string {
	path := filepath.Join(dir, ".git")
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return path
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir: ")
	if !ok {
		return ""
	}
	if filepath.IsAbs(target) {
		return target
	}
	return filepath.Join(dir, target)
}

// headBranch reads the branch out of a repository's HEAD. A detached HEAD holds
// a commit id where the ref would be, and names no branch at all — which is the
// state a footer must not answer with a stale name.
func headBranch(git string) string {
	head, err := os.ReadFile(filepath.Join(git, "HEAD"))
	if err != nil {
		return ""
	}
	branch, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/")
	if !ok {
		return ""
	}
	return branch
}
