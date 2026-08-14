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

			switch typed := value.Type.(type) {
			case *ast.Ident:
				carried = typed.Name == "BlockType"
			case nil:
				// Keeps carried as it was: an untyped spec in a group inherits.
			default:
				carried = false
			}

			isBlockType := carried
			for _, expr := range value.Values {
				call, ok := expr.(*ast.CallExpr)
				if !ok {
					continue
				}
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "BlockType" {
					isBlockType = true
				}
			}
			if !isBlockType {
				continue
			}
			for _, name := range value.Names {
				names = append(names, name.Name)
			}
		}
	}
	return names
}

// scrubbedBlockTypes collects the case names of the switch on b.Type inside
// Block.MarshalJSON — the switch that clears the fields belonging to the other
// variants.
func scrubbedBlockTypes(file *ast.File) []string {
	var handled []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "MarshalJSON" {
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
