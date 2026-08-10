package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestP26CanonicalModelRoundHasOnlyDeferredCollector(t *testing.T) {
	file := parseP26Source(t, "model_round.go")
	modelRoundInput := findP26Type(t, file, "canonicalModelRoundInput")
	assertP26FieldsAbsent(t, modelRoundInput, "deferToolExecution", "hookExecutor")

	modelRoundResult := findP26Type(t, file, "canonicalModelRoundResult")
	assertP26FieldsAbsent(t, modelRoundResult, "streamingExecutor")

	modelRound := findP26Func(t, file, "runCanonicalModelRound")
	collectorName := ""
	collectorConfigCount := 0
	processStreamCount := 0
	discardCount := 0
	forbiddenCalls := map[string]struct{}{
		"CancelToolInteraction":          {},
		"ExecuteCommittedToolCalls":      {},
		"ReleaseTool":                    {},
		"ToolContext":                    {},
		"WithProgressFn":                 {},
		"canonicalToolInterruptBehavior": {},
		"executeToolCall":                {},
		"isToolConcurrencySafe":          {},
		"reserveRepeatedToolCall":        {},
	}

	ast.Inspect(modelRound.Body, func(node ast.Node) bool {
		if assignment, ok := node.(*ast.AssignStmt); ok {
			if name := p26AssignedCallName(assignment, "NewStreamingToolExecutor"); name != "" {
				if collectorName != "" {
					t.Fatalf("runCanonicalModelRound constructs more than one stream collector")
				}
				collectorName = name
			}
		}

		if call, ok := node.(*ast.CallExpr); ok {
			callName := p26CallName(call)
			if _, forbidden := forbiddenCalls[callName]; forbidden {
				t.Fatalf("runCanonicalModelRound retains tool-dispatch call %q", callName)
			}
			switch callName {
			case "ProcessStream":
				processStreamCount++
				if len(call.Args) < 3 || p26IdentName(call.Args[2]) != collectorName {
					t.Fatalf("ProcessStream must receive the sole deferred collector")
				}
			case "Discard":
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if ok && p26IdentName(selector.X) == collectorName {
					discardCount++
				}
			}
		}

		literal, ok := node.(*ast.CompositeLit)
		if !ok || p26TypeName(literal.Type) != "StreamingToolExecutorConfig" {
			return true
		}
		collectorConfigCount++
		deferred := false
		for _, element := range literal.Elts {
			field, ok := element.(*ast.KeyValueExpr)
			if !ok {
				t.Fatal("model round collector must use named configuration fields")
			}
			name := p26IdentName(field.Key)
			switch name {
			case "Ctx":
				if p26IdentName(field.Value) != "modelCtx" {
					t.Fatal("model round collector must use the model cancellation context")
				}
			case "DeferExecution":
				value, ok := field.Value.(*ast.Ident)
				deferred = ok && value.Name == "true"
			default:
				t.Fatalf("model round collector retains non-classification field %q", name)
			}
		}
		if !deferred {
			t.Fatal("model round collector must set DeferExecution: true")
		}
		return true
	})

	if collectorName == "" {
		t.Fatal("runCanonicalModelRound does not construct its deferred stream collector")
	}
	if collectorConfigCount != 1 {
		t.Fatalf("model round StreamingToolExecutorConfig count = %d, want 1", collectorConfigCount)
	}
	if processStreamCount != 1 {
		t.Fatalf("model round ProcessStream call count = %d, want 1", processStreamCount)
	}
	if discardCount != 1 {
		t.Fatalf("model round deferred collector Discard call count = %d, want 1", discardCount)
	}
}

func TestP26ProjectGraphRemainsOnlyModelDerivedToolDispatchOwner(t *testing.T) {
	engineDir := p26EngineDir(t)
	files, err := filepath.Glob(filepath.Join(engineDir, "*.go"))
	if err != nil {
		t.Fatalf("list engine source: %v", err)
	}

	modelCallFiles := make(map[string]int)
	toolCallFiles := make(map[string]int)
	productionCallGraph := make(map[string][]string)
	committedDispatchSites := make(map[string]int)
	committedDispatchCalls := 0
	directDispatchSites := make(map[string]int)
	directDispatchCalls := 0
	legacyBatchCalls := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file := parseP26Path(t, path)
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch p26CallName(call) {
			case "runCanonicalModelRound":
				modelCallFiles[filepath.Base(path)]++
			case "runCanonicalToolRound":
				toolCallFiles[filepath.Base(path)]++
			case "ExecuteCommittedToolCalls":
				committedDispatchCalls++
			case "executeToolCall":
				directDispatchCalls++
			case "executeToolBatch":
				legacyBatchCalls++
			}
			return true
		})
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			caller := function.Name.Name
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if callee := p26CallName(call); callee != "" {
					productionCallGraph[caller] = append(productionCallGraph[caller], callee)
				}
				switch p26CallName(call) {
				case "ExecuteCommittedToolCalls":
					committedDispatchSites[filepath.Base(path)+":"+caller]++
				case "executeToolCall":
					directDispatchSites[filepath.Base(path)+":"+caller]++
				}
				return true
			})
		}
	}

	assertP26ExactCallSites(t, "runCanonicalModelRound", modelCallFiles, map[string]int{
		"graph.go":              1,
		"graph_query_kernel.go": 1,
	})
	assertP26ExactCallSites(t, "runCanonicalToolRound", toolCallFiles, map[string]int{
		"graph.go":              1,
		"graph_query_kernel.go": 1,
	})
	if committedDispatchCalls != 1 {
		t.Fatalf("production ExecuteCommittedToolCalls count = %d, want 1", committedDispatchCalls)
	}
	assertP26ExactCallSites(t, "ExecuteCommittedToolCalls", committedDispatchSites, map[string]int{
		"tool_round.go:runCanonicalToolRound": 1,
	})
	if directDispatchCalls != 3 {
		t.Fatalf("production executeToolCall count = %d, want 3", directDispatchCalls)
	}
	assertP26ExactCallSites(t, "executeToolCall", directDispatchSites, map[string]int{
		"tool_orchestration.go:executeToolBatch": 2,
		"tool_round.go:runCanonicalToolRound":    1,
	})
	if legacyBatchCalls != 0 {
		t.Fatalf("production executeToolBatch call count = %d, want 0", legacyBatchCalls)
	}
	assertP26NoDispatchReachable(t, "runCanonicalModelRound", productionCallGraph)
}

func p26EngineDir(t *testing.T) string {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate P26 source gate")
	}
	return filepath.Dir(testFile)
}

func parseP26Source(t *testing.T, name string) *ast.File {
	t.Helper()
	return parseP26Path(t, filepath.Join(p26EngineDir(t), name))
}

func parseP26Path(t *testing.T, path string) *ast.File {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filepath.Base(path), err)
	}
	return file
}

func findP26Type(t *testing.T, file *ast.File, name string) *ast.StructType {
	t.Helper()
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range general.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != name {
				continue
			}
			if structType, ok := typeSpec.Type.(*ast.StructType); ok {
				return structType
			}
		}
	}
	t.Fatalf("type %s not found", name)
	return nil
}

func assertP26FieldsAbsent(t *testing.T, structType *ast.StructType, names ...string) {
	t.Helper()
	for _, field := range structType.Fields.List {
		for _, fieldName := range field.Names {
			for _, name := range names {
				if fieldName.Name == name {
					t.Fatalf("forbidden field %s remains", name)
				}
			}
		}
	}
}

func findP26Func(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == name {
			return function
		}
	}
	t.Fatalf("function %s not found", name)
	return nil
}

func p26AssignedCallName(assignment *ast.AssignStmt, callName string) string {
	if len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
		return ""
	}
	call, ok := assignment.Rhs[0].(*ast.CallExpr)
	if !ok || p26CallName(call) != callName {
		return ""
	}
	return p26IdentName(assignment.Lhs[0])
}

func assertP26NoDispatchReachable(
	t *testing.T,
	root string,
	callGraph map[string][]string,
) {
	t.Helper()
	visited := make(map[string]bool)
	var visit func(string, []string)
	visit = func(caller string, path []string) {
		if visited[caller] {
			return
		}
		visited[caller] = true
		for _, callee := range callGraph[caller] {
			nextPath := append(append([]string(nil), path...), callee)
			if callee == "ExecuteCommittedToolCalls" || callee == "executeToolCall" {
				t.Fatalf("model round reaches tool dispatch through %s", strings.Join(nextPath, " -> "))
			}
			if _, ok := callGraph[callee]; ok {
				visit(callee, nextPath)
			}
		}
	}
	visit(root, []string{root})
}

func assertP26ExactCallSites(
	t *testing.T,
	function string,
	actual map[string]int,
	expected map[string]int,
) {
	t.Helper()
	for file, count := range actual {
		if expected[file] != count {
			t.Fatalf("%s call count in %s = %d, want %d", function, file, count, expected[file])
		}
	}
	for file, count := range expected {
		if actual[file] != count {
			t.Fatalf("%s call count in %s = %d, want %d", function, file, actual[file], count)
		}
	}
}

func p26CallName(call *ast.CallExpr) string {
	switch function := call.Fun.(type) {
	case *ast.Ident:
		return function.Name
	case *ast.SelectorExpr:
		return function.Sel.Name
	default:
		return ""
	}
}

func p26TypeName(expression ast.Expr) string {
	switch value := expression.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	default:
		return ""
	}
}

func p26IdentName(expression ast.Expr) string {
	if identifier, ok := expression.(*ast.Ident); ok {
		return identifier.Name
	}
	return ""
}
