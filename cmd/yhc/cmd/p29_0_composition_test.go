package cmd

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/provider"
)

const p290SecretSentinel = "p290-" + "secret-" + "sentinel-7f1834d9"

func TestP290CLIRuntimeCompositionRootsStayUnified(t *testing.T) {
	files := map[string][]string{
		"root.go":          {"runTUI", "runPlainREPL", "buildEngineConfig", "buildEngineConfigForCWD"},
		"headless.go":      {"runHeadless"},
		"headless_goal.go": {"runHeadlessGoal"},
		"serve_app.go":     {"runServeApp"},
	}
	functions := make(map[string]*ast.FuncDecl)
	for path, names := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			for _, name := range names {
				if function.Name.Name == name {
					functions[name] = function
				}
			}
		}
	}

	for _, entrypoint := range []string{
		"runTUI",
		"runPlainREPL",
		"runHeadless",
		"runHeadlessGoal",
	} {
		if !p290FunctionCalls(functions[entrypoint], "", "buildEngineConfig") {
			t.Fatalf("%s no longer calls buildEngineConfig", entrypoint)
		}
	}
	if !p290FunctionCalls(functions["runServeApp"], "", "buildEngineConfigForCWD") {
		t.Fatal("runServeApp no longer calls the shared CWD-aware engine composition owner")
	}
	if !p290FunctionCalls(functions["buildEngineConfig"], "", "buildEngineConfigForCWD") {
		t.Fatal("buildEngineConfig no longer delegates to the shared CWD-aware composition owner")
	}
	if !p290FunctionCalls(functions["buildEngineConfigForCWD"], "provider", "NewConfiguredRuntime") {
		t.Fatal("buildEngineConfigForCWD no longer owns provider.NewConfiguredRuntime composition")
	}
}

func TestP290DiagnosticProjectionDoesNotExposeSecret(t *testing.T) {
	resolver := engine.ModelResolverFunc(func(string) (provider.ResolvedConfig, error) {
		return provider.ResolvedConfig{
			Config: provider.Config{
				Provider: provider.ProviderAgenticOpenAI,
				Model:    "gpt-4o",
				APIKey:   p290SecretSentinel,
				BaseURL: "https://user:" + p290SecretSentinel +
					"@example.com/v1?token=" + p290SecretSentinel,
			},
			Sources: provider.ResolutionSources{
				Provider: "explicit",
				Model:    "explicit",
				APIKey:   "explicit",
				BaseURL:  "explicit",
			},
		}, nil
	})
	queryEngine := engine.NewQueryEngine(engine.QueryEngineConfig{
		CWD:           t.TempDir(),
		Model:         "gpt-4o",
		ModelResolver: resolver,
	})
	defer queryEngine.Close()

	snapshot, err := queryEngine.DiagnosticsSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), p290SecretSentinel) {
		t.Fatal("diagnostic projection exposed the credential sentinel")
	}
	if !snapshot.Config.CredentialConfigured.Value {
		t.Fatal("diagnostic projection lost the non-secret configured boolean")
	}
	if snapshot.Config.Endpoint.Value != "https://example.com" {
		t.Fatalf("diagnostic endpoint = %q", snapshot.Config.Endpoint.Value)
	}
}

func p290FunctionCalls(function *ast.FuncDecl, qualifier, name string) bool {
	if function == nil {
		return false
	}
	found := false
	ast.Inspect(function.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch target := call.Fun.(type) {
		case *ast.Ident:
			if qualifier == "" && target.Name == name {
				found = true
			}
		case *ast.SelectorExpr:
			identifier, ok := target.X.(*ast.Ident)
			if ok && identifier.Name == qualifier && target.Sel.Name == name {
				found = true
			}
		}
		return !found
	})
	return found
}
