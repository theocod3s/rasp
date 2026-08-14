package llm_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"slices"
	"strings"
	"testing"
)

// TestEveryBlockTypeIsScrubbed makes good on Block.MarshalJSON's claim that an
// untaught block type coming out empty is a loud way to be told to add a case.
// Nothing about it was loud until this existed.
//
// The block types are read out of the source rather than copied here, for the
// reason arch_test.go parses design §2: a copy is a second list to keep in step,
// and the drift it allows is the bug being guarded against.
func TestEveryBlockTypeIsScrubbed(t *testing.T) {
	// The whole package: a new variant is as likely to arrive in a new file.
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(info fs.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing the package: %v", err)
	}
	pkg, ok := pkgs["llm"]
	if !ok {
		t.Fatalf("parsed %d packages and none of them is llm; this test is reading the wrong "+
			"directory and would pass whatever the code did", len(pkgs))
	}

	var declared, handled []string
	for _, file := range pkg.Files {
		declared = append(declared, blockTypeConstants(file)...)
		handled = append(handled, scrubbedBlockTypes(file)...)
	}
	if len(declared) < 4 {
		t.Fatalf("found %d BlockType constants (%v); message.go declares at least four, so the "+
			"parse has gone wrong rather than the source", len(declared), declared)
	}

	if len(handled) == 0 {
		t.Fatal("found no switch on b.Type in Block.MarshalJSON; this test is reading the wrong " +
			"thing and would pass whatever the code did")
	}

	for _, name := range declared {
		if !slices.Contains(handled, name) {
			t.Errorf("%s has no case in Block.MarshalJSON's field-scrubbing switch, so a block of "+
				"that type keeps every other type's fields — or, under the default arm, loses its "+
				"own. Add a case naming the fields it owns.", name)
		}
	}
}

// blockTypeConstants collects every constant that is a BlockType, in all three
// ways Go lets one be written. All three, because a version understanding only
// the spelled-out form was green against `BlockRedacted =
// BlockType("redacted_thinking")` — the check failing at its one job.
func blockTypeConstants(file *ast.File) []string {
	var names []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}

		var carried bool // the type the last spec named, carried down the group
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}

			converted := convertsToBlockType(value.Values)

			switch typed := value.Type.(type) {
			case *ast.Ident:
				carried = typed.Name == "BlockType"
			case nil:
				// Go carries the previous type only when a spec gives neither type
				// nor value, so anything else ends the inheritance — including an
				// ordinary untyped constant, once read as a block type here. A
				// conversion keeps the run alive because it is a BlockType by any
				// reading; a string does because that is one with the type left off.
				if len(value.Values) > 0 && !converted {
					carried = carried && onlyStrings(value.Values)
				}
			default:
				carried = false
			}

			// The only signal left for `const BlockRedacted = "..."`, which says
			// nothing to the type system and everything to a reader.
			named := strings.HasPrefix(constName(value), "Block") && onlyStrings(value.Values)

			if !carried && !converted && !named {
				continue
			}
			for _, name := range value.Names {
				names = append(names, name.Name)
			}
		}
	}
	return names
}

// receiverIs reports whether fn is a method on the named type. Without it, a
// second MarshalJSON switching on some other .Type field would mask a block type
// missing from the real switch.
func receiverIs(fn *ast.FuncDecl, name string) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return false
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name
}

func convertsToBlockType(values []ast.Expr) bool {
	for _, expr := range values {
		call, ok := expr.(*ast.CallExpr)
		if !ok {
			continue
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "BlockType" {
			return true
		}
	}
	return false
}

func constName(value *ast.ValueSpec) string {
	if len(value.Names) == 0 {
		return ""
	}
	return value.Names[0].Name
}

// onlyStrings reports whether every value is a string literal, which is how a
// block type is written with the type left off. A count that shares the group is
// not one.
func onlyStrings(values []ast.Expr) bool {
	for _, expr := range values {
		lit, ok := expr.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return false
		}
	}
	return true
}

func scrubbedBlockTypes(file *ast.File) []string {
	var handled []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "MarshalJSON" || !receiverIs(fn, "Block") {
			continue
		}
		ast.Inspect(fn, func(node ast.Node) bool {
			sw, ok := node.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			// The one on b.Type, not the one below it on which field is stray.
			if selector, ok := sw.Tag.(*ast.SelectorExpr); !ok || selector.Sel.Name != "Type" {
				return true
			}
			for _, stmt := range sw.Body.List {
				clause, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range clause.List {
					if ident, ok := expr.(*ast.Ident); ok {
						handled = append(handled, ident.Name)
					}
				}
			}
			return false
		})
	}

	return handled
}

// TestBlockTypeConstantsReadsEveryForm pins the reader in both directions. The
// second half is not hypothetical: an earlier version read `blockLimit` as a
// block type and failed with an instruction nobody could follow.
func TestBlockTypeConstantsReadsEveryForm(t *testing.T) {
	const src = `package llm

type BlockType string

const (
	BlockText     BlockType = "text"
	BlockThinking           = "thinking"
	BlockToolUse            = BlockType("tool_use")
	redactedThinking        = "redacted_thinking"
	blockLimit              = 4
)

const BlockToolResult BlockType = "tool_result"

const BlockAlone = "on its own, with no type at all"
`

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "snippet.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing the snippet: %v", err)
	}

	// A real BlockType group holds only block types, so the interesting half is
	// that BlockThinking and BlockToolUse are found at all.
	want := []string{
		"BlockText",     // the type spelled out
		"BlockThinking", // the type carried down the group
		"BlockToolUse",  // a conversion
		// Carried past a conversion, and named so only the carry can find it —
		// a differently-named variant is what the name signal cannot see.
		"redactedThinking",
		"BlockToolResult", // its own declaration, typed
		"BlockAlone",      // its own declaration, untyped: only the name says so
	}
	got := blockTypeConstants(file)
	if !slices.Equal(got, want) {
		t.Errorf("blockTypeConstants:\n got %v\nwant %v", got, want)
	}
	if slices.Contains(got, "blockLimit") {
		t.Error("blockLimit is a count that shares the group, not a block type")
	}
}
