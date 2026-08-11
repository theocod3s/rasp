package config_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/theocod3s/rasp/internal/config"
)

// TestCommentsParse covers the JSONC half of design §10: `//` and `/* */` are
// comments, and everything inside a string is not.
//
// The string cases are the ones that matter. A comment stripper written the
// obvious way — scan for "//", cut to end of line — silently truncates
// "https://openrouter.ai/api/v1" to "https:", and the resulting config is
// valid JSON that is quietly wrong.
func TestCommentsParse(t *testing.T) {
	tests := []struct {
		name  string
		file  string
		model string
	}{
		{
			name:  "line comment on its own line",
			file:  "{\n  // the model this repo wants\n  \"model\": \"a/b\"\n}",
			model: "a/b",
		},
		{
			name:  "line comment after a value",
			file:  "{\n  \"model\": \"a/b\" // trailing\n}",
			model: "a/b",
		},
		{
			name:  "block comment",
			file:  "{\n  /* why: b is cheaper\n     and faster */\n  \"model\": \"a/b\"\n}",
			model: "a/b",
		},
		{
			name:  "block comment inline between tokens",
			file:  `{"model": /* here */ "a/b"}`,
			model: "a/b",
		},
		{
			name:  "a URL is not a comment",
			file:  `{"model": "https://example.com/api/v1"}`,
			model: "https://example.com/api/v1",
		},
		{
			name:  "a block comment opener inside a string is not a comment",
			file:  `{"model": "a/*b*/c"}`,
			model: "a/*b*/c",
		},
		{
			name:  "an escaped quote does not end the string",
			file:  `{"model": "a\"//b"}`,
			model: `a"//b`,
		},
		{
			name:  "a trailing comma is tolerated",
			file:  "{\n  \"model\": \"a/b\",\n}",
			model: "a/b",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := load(t, config.Sources{ProjectDir: project(t, tc.file)})
			if res.Config.Model != tc.model {
				t.Errorf("model = %q, want %q", res.Config.Model, tc.model)
			}
		})
	}
}

// TestConfigFileIsDotJSON pins the extension. JSONC tolerance comes from the
// editor, which applies it by filename in this ecosystem, so renaming the file
// to .jsonc would cost the tolerance rather than declare it (design §10).
func TestConfigFileIsDotJSON(t *testing.T) {
	if got := filepath.Ext(config.File); got != ".json" {
		t.Errorf("config.File = %q, want a .json extension", config.File)
	}

	dir := t.TempDir()
	path, err := config.ProjectPath(dir)
	if err != nil {
		t.Fatalf("ProjectPath: %v", err)
	}
	if want := filepath.Join(dir, ".rasp", "config.json"); path != want {
		t.Errorf("ProjectPath = %q, want %q", path, want)
	}
}

// TestSyntaxErrorNamesTheLine is the reason comments are replaced with spaces
// rather than deleted. The error below sits on line 6 of the file the user
// wrote; a stripper that shortens the text would report it against line 3 and
// send them to a line that is fine.
func TestSyntaxErrorNamesTheLine(t *testing.T) {
	file := strings.Join([]string{
		`{`,
		`  // three lines of comment,`,
		`  // so that deleting them rather than`,
		`  /* blanking them would shift what follows */`,
		`  "model": "a/b",`,
		`  "mode" "manual"`,
		`}`,
	}, "\n")

	dir := project(t, file)
	_, err := config.Load(config.Sources{
		GlobalPath: filepath.Join(t.TempDir(), config.File),
		ProjectDir: dir,
		Getenv:     env{}.lookup,
	})
	if err == nil {
		t.Fatal("Load succeeded on a file with a syntax error")
	}

	want := filepath.Join(dir, ".rasp", config.File) + ":6:"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to name %q", err, want)
	}
}

// TestUnterminatedBlockCommentFails guards the direction that matters: a
// comment that never closes must not swallow the rest of the file quietly.
func TestUnterminatedBlockCommentFails(t *testing.T) {
	_, err := config.Load(config.Sources{
		GlobalPath: filepath.Join(t.TempDir(), config.File),
		ProjectDir: project(t, "{\n  \"model\": \"a/b\"\n  /* never closed\n}"),
		Getenv:     env{}.lookup,
	})
	if err == nil {
		t.Fatal("Load succeeded on a file with an unterminated block comment")
	}
}
