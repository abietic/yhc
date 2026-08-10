package acp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestP290ACPCompositionRootsStayUnified(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "agent.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	functions := make(map[string]*ast.FuncDecl)
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		functions[function.Name.Name] = function
	}

	if !p290ACPFunctionCalls(
		functions["resolveModelRuntime"],
		"provider",
		"NewConfiguredRuntime",
	) {
		t.Fatal("resolveModelRuntime no longer owns provider.NewConfiguredRuntime composition")
	}
	for _, root := range []string{
		"createEngineWithSessionMCP",
		"createEngineForSessionWithConstructor",
	} {
		if !p290ACPFunctionCalls(functions[root], "a", "resolveModelRuntime") {
			t.Fatalf("%s no longer routes through resolveModelRuntime", root)
		}
	}
}

func p290ACPFunctionCalls(
	function *ast.FuncDecl,
	qualifier string,
	name string,
) bool {
	if function == nil {
		return false
	}
	found := false
	ast.Inspect(function.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if ok && identifier.Name == qualifier && selector.Sel.Name == name {
			found = true
		}
		return !found
	})
	return found
}
