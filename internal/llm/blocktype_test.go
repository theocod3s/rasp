package llm_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
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
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "message.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing message.go: %v", err)
	}

	declared := blockTypeConstants(file)
	if len(declared) < 4 {
		t.Fatalf("found %d BlockType constants (%v); message.go declares at least four, so the "+
			"parse has gone wrong rather than the source", len(declared), declared)
	}

	handled := scrubbedBlockTypes(t, file)
	for _, name := range declared {
		if !slices.Contains(handled, name) {
			t.Errorf("%s has no case in Block.MarshalJSON's field-scrubbing switch, so a block of "+
				"that type keeps every other type's fields — or, under the default arm, loses its "+
				"own. Add a case naming the fields it owns.", name)
		}
	}
}

// blockTypeConstants collects the names of every constant declared with type
// BlockType.
func blockTypeConstants(file *ast.File) []string {
	var names []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if ident, ok := value.Type.(*ast.Ident); !ok || ident.Name != "BlockType" {
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
func scrubbedBlockTypes(t *testing.T, file *ast.File) []string {
	t.Helper()

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

	if len(handled) == 0 {
		t.Fatal("found no switch on b.Type in Block.MarshalJSON; this test is reading the wrong " +
			"thing and would pass whatever the code did")
	}
	return handled
}
