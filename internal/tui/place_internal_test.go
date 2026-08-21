package tui

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTheBranchIsReadOffHead. No subprocess: a footer is drawn before the first
// frame and again on every keystroke, and shelling out to git for it would put
// a process spawn on that path. HEAD is one small file with one line in it.
func TestTheBranchIsReadOffHead(t *testing.T) {
	for _, tc := range []struct {
		name string
		head string
		want string
	}{
		{name: "a branch", head: "ref: refs/heads/main\n", want: "main"},
		{name: "a branch with slashes in it", head: "ref: refs/heads/feature/auth\n", want: "feature/auth"},
		// A detached HEAD holds the commit rather than a ref. There is no branch
		// to name, and the last one checked out is not it.
		{name: "a detached head", head: "9dd313bfa5e0d0b9f3a1c7e2d4b6a8c0e2f4a6b8\n"},
		{name: "a ref that is not a branch", head: "ref: refs/tags/v0.2.0\n"},
		{name: "nothing at all", head: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := repo(t, tc.head)

			if got := branchAt(dir); got != tc.want {
				t.Errorf("HEAD reading %q gives the branch %q, want %q", tc.head, got, tc.want)
			}
		})
	}
}

// TestTheBranchIsFoundFromAnywhereInTheRepository. The workspace root is
// wherever rasp was launched, which is usually not the root of the checkout —
// a footer that only named a branch from the top would be blank for most of
// the sessions it is drawn in.
func TestTheBranchIsFoundFromAnywhereInTheRepository(t *testing.T) {
	root := repo(t, "ref: refs/heads/main\n")
	deep := filepath.Join(root, "internal", "tui")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("making the subdirectory: %v", err)
	}

	if got := branchAt(deep); got != "main" {
		t.Errorf("a session started in %s reads the branch %q, want main", deep, got)
	}
}

// TestALinkedWorktreeIsStillARepository. A worktree and a submodule keep a
// `.git` file naming the real git directory instead of holding one, and a
// worktree is a checkout rasp is expected to run in — this repository is
// developed in them. Reading the file as "not a repository" would leave the
// branch blank exactly where it is most useful.
func TestALinkedWorktreeIsStillARepository(t *testing.T) {
	real := filepath.Join(t.TempDir(), "worktrees", "feature")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("making the git directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(real, "HEAD"), []byte("ref: refs/heads/feature\n"), 0o644); err != nil {
		t.Fatalf("writing HEAD: %v", err)
	}

	for _, tc := range []struct {
		name string
		// gitdir names the real git directory from the worktree's own, which is
		// where a relative one is resolved. nil is a .git file that names none.
		gitdir func(worktree string) string
		want   string
	}{
		{
			name:   "an absolute gitdir",
			gitdir: func(string) string { return real },
			want:   "feature",
		},
		{
			name: "a relative gitdir, as `git worktree add` writes one",
			gitdir: func(worktree string) string {
				rel, err := filepath.Rel(worktree, real)
				if err != nil {
					t.Fatalf("making %s relative to %s: %v", real, worktree, err)
				}
				return rel
			},
			want: "feature",
		},
		{name: "a file that says nothing about a repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			content := "not a git file\n"
			if tc.gitdir != nil {
				content = "gitdir: " + tc.gitdir(dir) + "\n"
			}
			if err := os.WriteFile(filepath.Join(dir, ".git"), []byte(content), 0o644); err != nil {
				t.Fatalf("writing the .git file: %v", err)
			}

			if got := branchAt(dir); got != tc.want {
				t.Errorf("a .git file reading %q gives the branch %q, want %q", content, got, tc.want)
			}
		})
	}
}

// TestAWorkspaceWithNoRepositoryIsSilent. Every one of these is an ordinary
// place to run rasp, not a fault to report: the footer simply has no branch to
// draw.
func TestAWorkspaceWithNoRepositoryIsSilent(t *testing.T) {
	bare := t.TempDir()

	unreadable := t.TempDir()
	// A directory where HEAD should be, which is the portable way to make a file
	// this process cannot read: a chmod is a no-op for a test running as root.
	if err := os.MkdirAll(filepath.Join(unreadable, ".git", "HEAD"), 0o755); err != nil {
		t.Fatalf("making the unreadable HEAD: %v", err)
	}

	for _, tc := range []struct{ name, dir string }{
		{name: "no repository above it", dir: bare},
		{name: "a HEAD that cannot be read", dir: unreadable},
		{name: "a directory that does not exist", dir: filepath.Join(bare, "gone")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := branchAt(tc.dir); got != "" {
				t.Errorf("%s names the branch %q", tc.name, got)
			}
		})
	}
}

// TestAUIToldNothingLooksNowhereUp is the negative control the rest of this
// file rests on: these tests run inside rasp's own checkout, so a lookup that
// fell back to the process's directory would find a real branch — and every
// assertion above would still pass while the footer named a repository the
// session has nothing to do with.
func TestAUIToldNothingLooksNowhereUp(t *testing.T) {
	if path, branch := place(""); path != "" || branch != "" {
		t.Errorf("a session with no workspace root drew %q (%q)", path, branch)
	}
	// And the control is a real one: the same lookup from this package's own
	// directory does find something.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("reading the working directory: %v", err)
	}
	if branchAt(wd) == "" {
		t.Skip("these tests are not running inside a git checkout, so the assertion above is about " +
			"a lookup that would have found nothing anyway")
	}
}

// repo is a directory with a git directory in it holding head.
func repo(t *testing.T, head string) string {
	t.Helper()

	dir := t.TempDir()
	git := filepath.Join(dir, ".git")
	if err := os.MkdirAll(git, 0o755); err != nil {
		t.Fatalf("making the git directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(git, "HEAD"), []byte(head), 0o644); err != nil {
		t.Fatalf("writing HEAD: %v", err)
	}
	return dir
}
