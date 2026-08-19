package edit_test

import "github.com/theocod3s/rasp/internal/tool/edit"

// corpus is the golden edit corpus: the ladder's regression suite, and the
// signal a system-prompt change is measured against rather than judged by feel
// (prd §8, S4).
//
// A Go table rather than files under testdata, because the corpus is made of
// bytes no text pipeline can be trusted with — trailing spaces, CRLF, tabs
// against spaces, a non-breaking space, curly quotes. Written as escapes they
// survive an editor, gofmt, a .gitattributes normalization and a copy-paste out
// of a terminal, and `%q` emits exactly this form, so importing a case off a
// transcript is one verb rather than a format. The cost is real and accepted:
// raw string literals are unusable here (Go discards \r inside them), so a
// multi-line source reads as escapes rather than as code.
//
// Every case carries provenance, and every case names the outcome the ladder
// must reach — the rung that placed the text, or the typed refusal and the
// field it reports. "It succeeded" is not an assertion this corpus makes.
var corpus = []corpusCase{
	// The exact rung: old_string appears byte for byte, whitespace included.
	{
		name:       "exact/one occurrence",
		provenance: "hand-written: taxonomy/exact",
		src:        "package main\n\nfunc main() {}\n",
		old:        "main() {}",
		new:        "main() { run() }",
		rung:       edit.Exact,
		want:       "package main\n\nfunc main() { run() }\n",
		count:      1,
	},
	{
		name:       "exact/indentation is part of the match",
		provenance: "hand-written: taxonomy/exact",
		src:        "if x {\n\t\treturn 1\n}\n",
		old:        "\t\treturn 1",
		new:        "\t\treturn 2",
		rung:       edit.Exact,
		want:       "if x {\n\t\treturn 2\n}\n",
		count:      1,
	},
	{
		name:       "exact/a whole line, newline included",
		provenance: "hand-written: taxonomy/exact",
		src:        "\t\tif err != nil {\n",
		old:        "\t\tif err != nil {\n",
		new:        "\t\tif err == nil {\n",
		rung:       edit.Exact,
		want:       "\t\tif err == nil {\n",
		count:      1,
	},
	{
		name:       "exact/deleting a line",
		provenance: "hand-written: taxonomy/exact",
		src:        "keep\ndrop\nkeep\n",
		old:        "drop\n",
		new:        "",
		rung:       edit.Exact,
		want:       "keep\nkeep\n",
		count:      1,
	},
	{
		name:       "exact/the replacement contains the text it replaces",
		provenance: "hand-written: taxonomy/exact",
		src:        "log(x)\n",
		old:        "log(x)",
		new:        "log(x); log(y)",
		rung:       edit.Exact,
		want:       "log(x); log(y)\n",
		count:      1,
	},
	{
		name:       "exact/emptying the file",
		provenance: "hand-written: taxonomy/degenerate",
		src:        "x",
		old:        "x",
		new:        "",
		rung:       edit.Exact,
		want:       "",
		count:      1,
	},
	{
		name:       "exact/no newline at end of file",
		provenance: "hand-written: taxonomy/degenerate",
		src:        "package main\n\nvar version = \"0.1.0\"",
		old:        "0.1.0",
		new:        "0.2.0",
		rung:       edit.Exact,
		want:       "package main\n\nvar version = \"0.2.0\"",
		count:      1,
	},
	{
		name:       "exact/overlapping occurrences count as they replace",
		provenance: "hand-written: taxonomy/ambiguity",
		src:        "aaa\n",
		old:        "aa",
		new:        "b",
		rung:       edit.Exact,
		want:       "ba\n",
		count:      1,
	},
	{
		name:       "exact/replace_all takes every occurrence",
		provenance: "hand-written: taxonomy/ambiguity",
		src:        "func serve() {\n\tport := 8080\n}\n\nfunc probe() {\n\tport := 8080\n}\n",
		old:        "8080",
		new:        "9090",
		replaceAll: true,
		rung:       edit.Exact,
		want:       "func serve() {\n\tport := 9090\n}\n\nfunc probe() {\n\tport := 9090\n}\n",
		count:      2,
	},
	{
		name:       "exact/replace_all with a single occurrence",
		provenance: "hand-written: taxonomy/ambiguity",
		src:        "a\nb\n",
		old:        "b",
		new:        "c",
		replaceAll: true,
		rung:       edit.Exact,
		want:       "a\nc\n",
		count:      1,
	},
	{
		name:       "exact/outranks a normalized candidate elsewhere in the file",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "func a() {\n\treturn 1\n}\n\nfunc b() {\n        return 1\n}\n",
		old:        "\treturn 1",
		new:        "\treturn 2",
		rung:       edit.Exact,
		want:       "func a() {\n\treturn 2\n}\n\nfunc b() {\n        return 1\n}\n",
		count:      1,
	},
	{
		name:       "exact/crlf on both sides",
		provenance: "hand-written: taxonomy/crlf",
		src:        "a\r\nb\r\nc\r\n",
		old:        "b\r\n",
		new:        "B\r\n",
		rung:       edit.Exact,
		want:       "a\r\nB\r\nc\r\n",
		count:      1,
	},
	{
		// Curly quotes are content like any other, and match when both sides carry
		// them. The failure below is a mismatch, not the characters.
		name:       "exact/curly quotes the file already holds",
		provenance: "hand-written: taxonomy/smart-quotes",
		src:        "\tfmt.Println(\u201chello\u201d)\n",
		old:        "\u201chello\u201d",
		new:        "\u201cworld\u201d",
		rung:       edit.Exact,
		want:       "\tfmt.Println(\u201cworld\u201d)\n",
		count:      1,
	},

	// The normalized rung: whole lines equal once the whitespace at their ends is
	// ignored, with the replacement re-indented to the file.
	{
		name:       "normalized/spaces where the file has tabs",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "func f() {\n\tif x {\n\t\treturn 1\n\t}\n}\n",
		old:        "    if x {\n        return 1\n    }",
		new:        "    if x {\n        return 2\n    }",
		rung:       edit.Normalized,
		want:       "func f() {\n\tif x {\n\t\treturn 2\n\t}\n}\n",
		count:      1,
	},
	{
		name:       "normalized/tabs where the file has spaces",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "def f():\n    if x:\n        return 1\n",
		old:        "\tif x:\n\t\treturn 1",
		new:        "\tif x:\n\t\treturn 2",
		rung:       edit.Normalized,
		want:       "def f():\n    if x:\n        return 2\n",
		count:      1,
	},
	{
		name:       "normalized/no indentation at all",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "func f() {\n\tif x {\n\t\treturn 1\n\t}\n}\n",
		old:        "if x {\nreturn 1\n}",
		new:        "if x {\nreturn 2\n}",
		rung:       edit.Normalized,
		want:       "func f() {\n\tif x {\n\t\treturn 2\n\t}\n}\n",
		count:      1,
	},
	{
		name:       "normalized/deeper than the file",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "func f() {\n\tif x {\n\t\treturn 1\n\t}\n}\n",
		old:        "\t\t\t\treturn 1",
		new:        "\t\t\t\treturn 2",
		rung:       edit.Normalized,
		want:       "func f() {\n\tif x {\n\t\treturn 2\n\t}\n}\n",
		count:      1,
	},
	{
		name:       "normalized/trailing spaces nobody can see",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "\tport := 8080\n",
		old:        "\tport := 8080   ",
		new:        "\tport := 9090",
		rung:       edit.Normalized,
		want:       "\tport := 9090\n",
		count:      1,
	},
	{
		// TrimSpace is Unicode's definition, so a non-breaking space pasted at the
		// head of a line is drift like any other. Inside the line it is content —
		// the case below.
		name:       "normalized/a non-breaking space in the indentation",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "\tport := 8080\n",
		old:        "\u00a0port := 8080",
		new:        "port := 9090",
		rung:       edit.Normalized,
		want:       "\tport := 9090\n",
		count:      1,
	},
	{
		name:       "normalized/only the trailing space drifted, so nothing else moves",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "def f():\n    if x:\n        return 1\n",
		old:        "    if x: \n        return 1",
		new:        "    if x:\n        return 2",
		rung:       edit.Normalized,
		want:       "def f():\n    if x:\n        return 2\n",
		count:      1,
	},
	{
		name:       "normalized/deleting a line whose indentation drifted",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "keep\n\tdrop\nkeep\n",
		old:        "    drop\n",
		new:        "",
		rung:       edit.Normalized,
		want:       "keep\nkeep\n",
		count:      1,
	},
	{
		name:       "normalized/new_string spans more lines than old_string",
		provenance: "hand-written: taxonomy/reindentation",
		src:        "func f() {\n\tif x {\n\t\treturn 1\n\t}\n}\n",
		old:        "  return 1",
		new:        "  log(1)\n  return 1",
		rung:       edit.Normalized,
		want:       "func f() {\n\tif x {\n\t\tlog(1)\n\t\treturn 1\n\t}\n}\n",
		count:      1,
	},
	{
		name:       "normalized/four spaces re-indented into the file's tabs",
		provenance: "hand-written: taxonomy/reindentation",
		src:        "\tfunc f() {\n\t\tbody()\n\t}\n",
		old:        "    func f() {\n        body()\n    }",
		new:        "    func f() {\n        first()\n        second()\n    }",
		rung:       edit.Normalized,
		want:       "\tfunc f() {\n\t\tfirst()\n\t\tsecond()\n\t}\n",
		count:      1,
	},
	{
		name:       "normalized/tabs re-indented into the file's two spaces",
		provenance: "hand-written: taxonomy/reindentation",
		src:        "a:\n  b:\n    c: 1\n",
		old:        "\tb:\n\t\tc: 1",
		new:        "\tb:\n\t\tc: 2\n\t\td: 3",
		rung:       edit.Normalized,
		want:       "a:\n  b:\n    c: 2\n    d: 3\n",
		count:      1,
	},
	{
		name:       "normalized/a replacement line shallower than the one it answers to",
		provenance: "hand-written: taxonomy/reindentation",
		src:        "func f() {\n\t\tif x {\n\t\t\treturn 1\n\t\t}\n}\n",
		old:        "  if x {\n    return 1\n  }",
		new:        "if x {\n  return 1\n}",
		rung:       edit.Normalized,
		want:       "func f() {\n\tif x {\n\t\treturn 1\n\t}\n}\n",
		count:      1,
	},
	{
		name:       "normalized/a blank line in the replacement takes no indentation",
		provenance: "hand-written: taxonomy/reindentation",
		src:        "\tone\n",
		old:        "  one",
		new:        "  one\n   \n  two",
		rung:       edit.Normalized,
		want:       "\tone\n\n\ttwo\n",
		count:      1,
	},
	{
		name:       "normalized/replace_all re-indents each match to its own depth",
		provenance: "hand-written: taxonomy/reindentation",
		src:        "func a() {\n\tport := 8080\n}\n\nfunc b() {\n\t\tport := 8080\n}\n",
		old:        "    port := 8080",
		new:        "    port := 9090",
		replaceAll: true,
		rung:       edit.Normalized,
		want:       "func a() {\n\tport := 9090\n}\n\nfunc b() {\n\t\tport := 9090\n}\n",
		count:      2,
	},
	{
		// old_string stopping mid-line leaves the terminator that closed the run in
		// place, so the file's own \r\n survives. The case below is the other half.
		name:       "normalized/the file's crlf survives an lf old_string",
		provenance: "hand-written: taxonomy/crlf",
		src:        "a\r\nb\r\nc\r\n",
		old:        "  b",
		new:        "B",
		rung:       edit.Normalized,
		want:       "a\r\nB\r\nc\r\n",
		count:      1,
	},
	{
		// Carrying the newline takes new_string's terminator with it, exactly as the
		// exact rung would, so an lf replacement lands one lf line in a crlf file.
		// Pinned rather than endorsed: normalizing endings would be a change to make
		// deliberately, and this case is what would tell anyone it had been made.
		name:       "normalized/new_string's lf terminator replaces the file's crlf",
		provenance: "hand-written: taxonomy/crlf",
		src:        "a\r\nb\r\nc\r\n",
		old:        "b\n",
		new:        "B\n",
		rung:       edit.Normalized,
		want:       "a\r\nB\nc\r\n",
		count:      1,
	},
	{
		name:       "normalized/a crlf old_string against an lf file",
		provenance: "hand-written: taxonomy/crlf",
		src:        "a\nb\nc\n",
		old:        "b\r\n",
		new:        "B\r\n",
		rung:       edit.Normalized,
		want:       "a\nB\r\nc\n",
		count:      1,
	},

	// Ambiguity: more than one occurrence without replace_all is a refusal at
	// whichever rung found them, never a replacement of the first.
	{
		name:       "ambiguous/two exact occurrences",
		provenance: "hand-written: taxonomy/ambiguity",
		src:        "func serve() {\n\tport := 8080\n}\n\nfunc probe() {\n\tport := 8080\n}\n",
		old:        "8080",
		new:        "9090",
		err:        &edit.AmbiguousError{Count: 2},
	},
	{
		name:       "ambiguous/three exact occurrences are counted as three",
		provenance: "hand-written: taxonomy/ambiguity",
		src:        "a\na\na\n",
		old:        "a",
		new:        "b",
		err:        &edit.AmbiguousError{Count: 3},
	},
	{
		// Spaces against the file's tabs, so the exact rung counts nothing and the
		// refusal reached is the normalized one.
		name:       "ambiguous/two normalized occurrences at different depths",
		provenance: "hand-written: taxonomy/ambiguity",
		src:        "func a() {\n\tport := 8080\n}\n\nfunc b() {\n\t\tport := 8080\n}\n",
		old:        "    port := 8080",
		new:        "    port := 9090",
		err:        &edit.AmbiguousError{Count: 2},
	},

	// Smart quotes. The ladder does not normalize them today, and these cases
	// record what it does rather than what it should: a mismatch is a miss, and
	// the near-miss rung is all the model gets back. A quote-normalizing rung
	// would change these expectations, deliberately and in its own change.
	{
		name:       "notfound/curly double quotes where the file has straight ones",
		provenance: "hand-written: taxonomy/smart-quotes",
		src:        "func greet() {\n\tfmt.Println(\"hello\")\n}\n",
		old:        "fmt.Println(\u201chello\u201d)",
		new:        "fmt.Println(\u201cworld\u201d)",
		err:        &edit.NotFoundError{},
	},
	{
		// Wrapped in lines the file does hold, so the miss has somewhere to point,
		// and what comes back is the file's own straight quotes.
		name:       "notfound/curly quotes inside a block the file otherwise holds",
		provenance: "hand-written: taxonomy/smart-quotes",
		src:        "func greet() {\n\tfmt.Println(\"hello\")\n}\n",
		old:        "func greet() {\n\tfmt.Println(\u201chello\u201d)\n}",
		new:        "func greet() {\n\tfmt.Println(\u201cbye\u201d)\n}",
		err: &edit.NotFoundError{
			Line: 1,
			Actual: "1 | func greet() {\n" +
				"2 | " + edit.TabGlyph + "fmt.Println(\"hello\")\n" +
				"3 | }\n",
		},
	},
	{
		name:       "notfound/a curly apostrophe",
		provenance: "hand-written: taxonomy/smart-quotes",
		src:        "\t// don't repeat this\n",
		old:        "// don\u2019t repeat this",
		new:        "// do not repeat this",
		err:        &edit.NotFoundError{},
	},

	// Everything else the ladder refuses, and where it points when it does.
	{
		name:       "notfound/whitespace inside the line differs",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "\tport :=  8080\n",
		old:        "port := 8080",
		new:        "port := 9090",
		err:        &edit.NotFoundError{},
	},
	{
		name:       "notfound/a non-breaking space inside the line",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "\tport := 8080\n",
		old:        "port :=\u00a08080",
		new:        "port := 9090",
		err:        &edit.NotFoundError{},
	},
	{
		name:       "notfound/part of a line",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "\tport := 8080\n",
		old:        "  := 8080",
		new:        "  := 9090",
		err:        &edit.NotFoundError{},
	},
	{
		name:       "notfound/the head of a line with the rest left off",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "\tif err != nil {\n\t\treturn err\n\t}\n",
		old:        "  if err != nil\n    return err",
		new:        "  if err != nil\n    return nil",
		err:        &edit.NotFoundError{Line: 1},
	},
	{
		// The block matches at its ends and not in the middle, so what comes back is
		// the file's own line with the trailing spaces made visible.
		name:       "notfound/the model wrote the line it wants",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "func f() {\n\tif x {\n\t\treturn 1  \n\t}\n}\n",
		old:        "\tif x {\n\t\treturn 2\n\t}",
		new:        "\tif x {\n\t\treturn 3\n\t}",
		err: &edit.NotFoundError{
			Line: 2,
			Actual: "2 | " + edit.TabGlyph + "if x {\n" +
				"3 | " + edit.TabGlyph + edit.TabGlyph + "return 1" + edit.SpaceGlyph + edit.SpaceGlyph + "\n" +
				"4 | " + edit.TabGlyph + "}\n",
		},
	},
	{
		name:       "notfound/one line of three drifted in content",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "one\ntwo\nthree\n",
		old:        "one\nTWO\nthree",
		new:        "1\n2\n3",
		err:        &edit.NotFoundError{Line: 1},
	},
	{
		name:       "notfound/old_string is longer than the file",
		provenance: "hand-written: taxonomy/degenerate",
		src:        "one\ntwo\n",
		old:        "one\ntwo\nthree\nfour",
		new:        "x",
		err:        &edit.NotFoundError{Line: 1},
	},
	{
		name:       "notfound/nothing like it in the file",
		provenance: "hand-written: taxonomy/degenerate",
		src:        "alpha\nbeta\ngamma\n",
		old:        "nothing like it",
		new:        "x",
		err:        &edit.NotFoundError{},
	},
	{
		name:       "notfound/old_string trims to nothing",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "x\ny\nz\n",
		old:        "   \n",
		new:        "q",
		err:        &edit.NotFoundError{},
	},
	{
		name:       "notfound/an empty file",
		provenance: "hand-written: taxonomy/degenerate",
		src:        "",
		old:        "x",
		new:        "y",
		err:        &edit.NotFoundError{},
	},
	{
		name:       "refused/old_string is empty",
		provenance: "hand-written: taxonomy/degenerate",
		src:        "a\n",
		old:        "",
		new:        "b",
		err:        edit.ErrEmpty,
	},
	{
		name:       "refused/new_string is old_string",
		provenance: "hand-written: taxonomy/degenerate",
		src:        "a\n",
		old:        "a",
		new:        "a",
		err:        edit.ErrUnchanged,
	},
	{
		name:       "refused/a normalized match that re-indents to what the file holds",
		provenance: "hand-written: taxonomy/whitespace-drift",
		src:        "\tx := 1\n",
		old:        "x := 1  ",
		new:        "x := 1",
		err:        edit.ErrUnchanged,
	},
}
