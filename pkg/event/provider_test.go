package event

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"
)

// providerConstants reads the value of every Provider constant declared in
// event.go, so a constant missing from Providers() is caught.
func providerConstants(t *testing.T) []Provider {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "event.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found []Provider
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			if ident, ok := vs.Type.(*ast.Ident); !ok || ident.Name != "Provider" {
				continue
			}
			for _, value := range vs.Values {
				lit, ok := value.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("Provider constant with a non-literal value: %#v", value)
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatal(err)
				}
				found = append(found, Provider(s))
			}
		}
	}
	return found
}

func TestProvidersMatchesConstants(t *testing.T) {
	constants := providerConstants(t)
	if len(constants) == 0 {
		t.Fatal("found no Provider constants in event.go")
	}
	listed := Providers()
	for _, p := range constants {
		if !slices.Contains(listed, p) {
			t.Errorf("constant %q is missing from Providers()", p)
		}
	}
	for _, p := range listed {
		if !slices.Contains(constants, p) {
			t.Errorf("Providers() lists %q, which has no constant", p)
		}
	}
	if len(listed) != len(constants) {
		t.Errorf("Providers() has %d entries, event.go declares %d constants", len(listed), len(constants))
	}
}

// Valid is the strict check for data from other programs: only the exact
// canonical id passes.
func TestProviderValidIsExact(t *testing.T) {
	for _, p := range Providers() {
		if !p.Valid() {
			t.Errorf("%q is not valid", p)
		}
	}
	for _, p := range []Provider{"", "Claude", "CODEX", " claude", "claude ", "codexx", "open code", "unknown"} {
		if p.Valid() {
			t.Errorf("%q is valid, want only exact canonical ids", p)
		}
	}
}
