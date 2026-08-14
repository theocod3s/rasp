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

// TestEveryBlockTypeIsScrubbed is the check behind a claim Block.MarshalJSON
// makes about itself: that a block type nobody taught it about comes out empty,
// and that this is a loud way to be told to add a case. Nothing about it was
// loud — no error, no failing test — so the claim was decoration, which in this
// package is the one thing a rule is not allowed to be.
//
// The list of block types is read out of the source rather than written down
// here, for the reason arch_test.go parses design §2 rather than copying it: a
// copy is a second list to keep in step, and the drift it allows is exactly the
// bug being guarded against. Adding a fifth BlockType constant without adding a
// case to the switch fails here, before the payload is lost to an append-only
// session file and turns up as a provider rejection nowhere near the cause.
func TestEveryBlockTypeIsScrubbed(t *testing.T) {
	// The whole package, not just the file the type lives in today: a new variant
	// is as likely to arrive in a new file, and a check that reads one file would
	// pass while missing it.
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

// blockTypeConstants collects the names of every constant that is a BlockType,
// in any of the three ways Go lets one be written: with the type spelled out, by
// repeating the previous spec's type implicitly, or through a conversion.
//
// All three, because the point of reading the source is to see what a future
// author actually wrote. A version of this that only understood the spelled-out
// form was green against `BlockRedacted = BlockType("redacted_thinking")`, which
// is the check failing at the one job it has.
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
				// Go carries the previous spec's type only when a spec gives
				// neither type nor value, so anything else in the group ends the
				// inheritance — including an ordinary untyped constant, which was
				// once read as a block type and failed this test with an
				// impossible instruction. A conversion keeps the run alive, since
				// it is a BlockType by any reading; a string keeps it because that
				// is what a block type looks like with the type left off.
				if len(value.Values) > 0 && !converted {
					carried = carried && onlyStrings(value.Values)
				}
			default:
				carried = false
			}

			// The name is the last signal, and the only one left for a constant
			// declared on its own with no type: `const BlockRedacted = "..."` says
			// nothing to the type system and everything to a reader. This package
			// names every block type Block*, so a string constant that does is
			// treated as one — misname something and the cost is a case to add.
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
// second MarshalJSON in the package that switches on some other .Type field would
// contribute its cases here, and a block type missing from the real switch could
// be reported as covered.
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

// convertsToBlockType reports whether any value is a BlockType(...) conversion.
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

// constName is the first name a spec declares, which for a constant is the only
// one worth asking about.
func constName(value *ast.ValueSpec) string {
	if len(value.Names) == 0 {
		return ""
	}
	return value.Names[0].Name
}

// onlyStrings reports whether every value is a string literal, which is how a
// block type is written even when its author leaves the type off. A constant that
// shares the group and holds anything else — a count, a flag — is not one.
func onlyStrings(values []ast.Expr) bool {
	for _, expr := range values {
		lit, ok := expr.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return false
		}
	}
	return true
}

// scrubbedBlockTypes collects the case names of the switch on b.Type inside
// Block.MarshalJSON — the switch that clears the fields belonging to the other
// variants.
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
			// The one switching on b.Type, not the one below it switching on
			// which field is out of place.
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

// TestBlockTypeConstantsReadsEveryForm pins the reader itself, in both
// directions: every way a block type can be written is found, and a constant
// that merely shares the group is left alone. The second half is not
// hypothetical — an earlier version read `blockLimit` as a block type and failed
// with an instruction nobody could follow, which is worse than the gap it was
// closing.
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

	// A constant with no type of its own counts while its value is a string, which
	// is how a block type is written when the author leaves the type off; a count
	// is not one, and ends the run. A real BlockType group holds only block types,
	// so the interesting half of this is that BlockThinking and BlockToolUse are
	// found at all.
	want := []string{
		"BlockText",     // the type spelled out
		"BlockThinking", // the type carried down the group
		"BlockToolUse",  // a conversion
		// Carried past a conversion, which is still a BlockType — and named so
		// that only the carry can find it, since a differently-named variant is
		// exactly what the name signal cannot see.
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
