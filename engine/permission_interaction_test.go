package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestClonePermissionInteractionResultPreservesNilAndFreezesUpdatedInput(
	t *testing.T,
) {
	withoutUpdate := clonePermissionInteractionResult(
		PermissionInteractionResult{Decision: PermissionAllowOnce},
	)
	if withoutUpdate.UpdatedInput != nil {
		t.Fatalf("nil UpdatedInput became %#v", withoutUpdate.UpdatedInput)
	}

	nested := map[string]any{"answer": "A"}
	list := []any{nested}
	typed := []string{"first", "second"}
	original := PermissionInteractionResult{
		Decision: PermissionAllowOnce,
		UpdatedInput: map[string]any{
			"nested": nested,
			"list":   list,
			"typed":  typed,
		},
	}
	cloned := clonePermissionInteractionResult(original)
	nested["answer"] = "mutated"
	list[0] = map[string]any{"answer": "replaced"}
	typed[0] = "mutated"

	clonedNested, ok := cloned.UpdatedInput["nested"].(map[string]any)
	if !ok || clonedNested["answer"] != "A" {
		t.Fatalf("nested UpdatedInput was not frozen: %#v", cloned.UpdatedInput)
	}
	clonedList, ok := cloned.UpdatedInput["list"].([]any)
	if !ok || len(clonedList) != 1 {
		t.Fatalf("slice UpdatedInput was not frozen: %#v", cloned.UpdatedInput)
	}
	listNested, ok := clonedList[0].(map[string]any)
	if !ok || listNested["answer"] != "A" {
		t.Fatalf("nested slice value was not frozen: %#v", clonedList)
	}
	clonedTyped, ok := cloned.UpdatedInput["typed"].([]any)
	if !ok || len(clonedTyped) != 2 || clonedTyped[0] != "first" {
		t.Fatalf("typed slice was not durably cloned: %#v", cloned.UpdatedInput)
	}
	clonedNested["answer"] = "clone-only"
	if nested["answer"] != "mutated" {
		t.Fatalf("clone mutation leaked back to adapter input: %#v", nested)
	}

	invalid := clonePermissionInteractionResult(PermissionInteractionResult{
		Decision: PermissionAllowOnce,
		UpdatedInput: map[string]any{
			"unsupported": make(chan struct{}),
		},
	})
	if invalid.Decision != PermissionDeny ||
		invalid.UpdatedInput != nil ||
		!strings.Contains(invalid.Message, "durable JSON") {
		t.Fatalf("non-durable UpdatedInput did not fail closed: %#v", invalid)
	}
}

func TestPermissionCoordinatorCanonicalProjectRegistryLifecycle(t *testing.T) {
	root := t.TempDir()
	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "project-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	other := t.TempDir()
	registry := NewPermissionCoordinatorRegistry()

	first, firstIdentity := registry.acquire(root, "engine-a")
	throughAlias, aliasIdentity := registry.acquire(alias, "engine-b")
	isolated, isolatedIdentity := registry.acquire(other, "engine-c")
	if first != throughAlias {
		t.Fatal("canonical aliases did not reuse one project coordinator")
	}
	if first == isolated {
		t.Fatal("different project roots shared one coordinator")
	}
	if firstIdentity != aliasIdentity {
		t.Fatalf("alias identity mismatch: %#v != %#v", firstIdentity, aliasIdentity)
	}
	if registry.ActiveProjectCount() != 2 || first.EngineCount() != 2 {
		t.Fatalf("registry state = projects %d, engines %d", registry.ActiveProjectCount(), first.EngineCount())
	}

	registry.release(firstIdentity, "engine-a")
	if registry.ActiveProjectCount() != 2 || first.EngineCount() != 1 {
		t.Fatal("non-final engine release removed the shared project runtime")
	}
	registry.release(aliasIdentity, "engine-b")
	if _, ok := registry.CoordinatorForProject(root); ok {
		t.Fatal("final engine release retained an idle coordinator")
	}
	registry.release(isolatedIdentity, "engine-c")
	if registry.ActiveProjectCount() != 0 {
		t.Fatalf("active project count = %d, want 0", registry.ActiveProjectCount())
	}
}

func TestPermissionCoordinatorClassifierWinsUserRaceExactlyOnce(t *testing.T) {
	registry := NewPermissionCoordinatorRegistry()
	coordinator, _ := registry.acquire(t.TempDir(), "engine")
	adapterEntered := make(chan struct{})
	releaseAdapter := make(chan struct{})
	resultCh := make(chan PermissionInteractionResult, 1)
	var commitCount atomic.Int32
	var mu sync.Mutex
	var events []QueryEvent

	go func() {
		resultCh <- coordinator.request(
			context.Background(),
			"engine",
			PermissionPromptRequest{ToolName: "Bash", ToolUseID: "call-race", Input: map[string]any{"command": "go test ./..."}},
			func(context.Context, PermissionPromptRequest) PermissionInteractionResult {
				close(adapterEntered)
				<-releaseAdapter
				return PermissionInteractionResult{Decision: PermissionAllowSession, Message: "late user approval"}
			},
			func(event QueryEvent) {
				mu.Lock()
				events = append(events, event)
				mu.Unlock()
			},
			func(result PermissionInteractionResult) PermissionInteractionResult {
				commitCount.Add(1)
				return result
			},
			nil,
		)
	}()
	<-adapterEntered
	if !coordinator.resolve("engine", "call-race", PermissionInteractionResult{
		Decision: PermissionAllowOnce,
		Message:  "classifier approved",
	}, "classifier") {
		t.Fatal("classifier did not claim pending request")
	}
	result := <-resultCh
	close(releaseAdapter)
	if result.Decision != PermissionAllowOnce || result.Message != "classifier approved" {
		t.Fatalf("winner result = %#v", result)
	}
	if commitCount.Load() != 1 {
		t.Fatalf("commit count = %d, want 1", commitCount.Load())
	}
	if coordinator.PendingCount() != 0 {
		t.Fatalf("pending count = %d, want 0", coordinator.PendingCount())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0].Type != EventPermissionRequest || events[1].Type != EventPermissionResolved {
		t.Fatalf("event order = %#v", events)
	}
	if events[1].PermissionResolved.Decision != string(PermissionAllowOnce) || events[1].PermissionResolved.Reason != "classifier" {
		t.Fatalf("terminal event = %#v", events[1].PermissionResolved)
	}
}

func TestPermissionCoordinatorCancellationBeatsLateAdapter(t *testing.T) {
	registry := NewPermissionCoordinatorRegistry()
	coordinator, _ := registry.acquire(t.TempDir(), "engine")
	ctx, cancel := context.WithCancel(context.Background())
	adapterEntered := make(chan struct{})
	releaseAdapter := make(chan struct{})
	resultCh := make(chan PermissionInteractionResult, 1)
	var persisted atomic.Int32
	var eventsMu sync.Mutex
	var events []QueryEvent

	go func() {
		resultCh <- coordinator.request(ctx, "engine", PermissionPromptRequest{
			ToolName: "Bash", ToolUseID: "call-cancel", Input: map[string]any{"command": "go test ./..."},
		}, func(context.Context, PermissionPromptRequest) PermissionInteractionResult {
			close(adapterEntered)
			<-releaseAdapter
			return PermissionInteractionResult{Decision: PermissionAllowAlways}
		}, func(event QueryEvent) {
			eventsMu.Lock()
			events = append(events, event)
			eventsMu.Unlock()
		}, func(result PermissionInteractionResult) PermissionInteractionResult {
			if result.Decision == PermissionAllowAlways {
				persisted.Add(1)
			}
			return result
		}, nil)
	}()
	<-adapterEntered
	cancel()
	result := <-resultCh
	close(releaseAdapter)
	if result.Decision != PermissionCancelled {
		t.Fatalf("result = %#v, want cancelled", result)
	}
	if persisted.Load() != 0 {
		t.Fatalf("late adapter persisted %d grant(s)", persisted.Load())
	}
	if coordinator.resolve("engine", "call-cancel", PermissionInteractionResult{Decision: PermissionAllowOnce}, "late") {
		t.Fatal("late response reclaimed a terminal request")
	}
	eventsMu.Lock()
	defer eventsMu.Unlock()
	if len(events) != 2 || events[1].PermissionResolved.Decision != string(PermissionCancelled) {
		t.Fatalf("events = %#v", events)
	}
}

func TestPermissionCoordinatorFinalEngineReleaseSettlesBeforeRegistryRemoval(t *testing.T) {
	root := t.TempDir()
	registry := NewPermissionCoordinatorRegistry()
	coordinator, identity := registry.acquire(root, "engine")
	adapterEntered := make(chan struct{})
	adapterCancelled := make(chan struct{})
	resultCh := make(chan PermissionInteractionResult, 1)
	var orderMu sync.Mutex
	var order []string

	go func() {
		resultCh <- coordinator.request(context.Background(), "engine", PermissionPromptRequest{
			ToolName: "Bash", ToolUseID: "call-shutdown",
		}, func(ctx context.Context, _ PermissionPromptRequest) PermissionInteractionResult {
			close(adapterEntered)
			<-ctx.Done()
			close(adapterCancelled)
			return PermissionInteractionResult{Decision: PermissionCancelled}
		}, func(event QueryEvent) {
			orderMu.Lock()
			order = append(order, string(event.Type))
			orderMu.Unlock()
		}, nil, nil)
	}()
	<-adapterEntered
	registry.release(identity, "engine")
	result := <-resultCh
	<-adapterCancelled
	if result.Decision != PermissionCancelled {
		t.Fatalf("shutdown result = %#v", result)
	}
	if _, ok := registry.CoordinatorForProject(root); ok {
		t.Fatal("registry entry remained after terminal settlement")
	}
	orderMu.Lock()
	defer orderMu.Unlock()
	if len(order) != 2 || order[0] != string(EventPermissionRequest) || order[1] != string(EventPermissionResolved) {
		t.Fatalf("shutdown order = %#v", order)
	}
}

func TestQueryEngineStructuredSessionApprovalAndEvents(t *testing.T) {
	root := t.TempDir()
	var received PermissionPromptRequest
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: "session", ThreadID: "thread", CWD: root,
		PermissionPrompt: func(ctx context.Context, request PermissionPromptRequest) PermissionInteractionResult {
			received = request
			ReportPermissionPromptRequested(ctx, request.ToolName, request.Input, "duplicate")
			ReportPermissionPromptResolved(ctx, true, "duplicate")
			return PermissionInteractionResult{Decision: PermissionAllowSession, Message: "approved for session"}
		},
	})
	t.Cleanup(eng.Close)
	var events []QueryEvent
	outcome := executeToolCall(context.Background(), QueryParams{
		CanUseTool:   eng.wrappedCanUseTool,
		ToolRegistry: eng.toolRegistry,
		ToolExecutor: func(context.Context, string, string) (string, error) {
			return "ok", nil
		},
	}, nil, &ToolUseContext{SessionID: "session", ThreadID: "thread"}, &schema.ToolCall{
		ID: "call-session", Type: "function",
		Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"go test ./..."}`},
	}, func(event QueryEvent) {
		if event.Type == EventPermissionRequest || event.Type == EventPermissionResolved {
			events = append(events, event)
		}
	})
	if outcome == nil || outcome.Result == nil || outcome.Result.Content != "ok" {
		t.Fatalf("tool outcome = %#v", outcome)
	}
	if len(events) != 2 {
		t.Fatalf("permission events = %#v", events)
	}
	if events[1].PermissionResolved.Decision != string(PermissionAllowSession) {
		t.Fatalf("resolved decision = %#v", events[1].PermissionResolved)
	}
	if received.RootSessionID != "session" || received.SessionID != "session" || received.ThreadID != "thread" ||
		received.ProjectIdentity.Root != ResolvePermissionProjectIdentity(root).Root {
		t.Fatalf("request ownership metadata = %#v", received)
	}
	command, path, fingerprint := permissionInvocation(root, "Bash", map[string]any{"command": "go test ./..."})
	if !eng.approvalTracker.IsApprovedInvocationForRootSession("Bash", command, path, fingerprint, "session") {
		t.Fatal("session decision was not committed before tool execution")
	}
}

func TestPermissionSessionGrantCoalescesExactScopeWithinRootLineage(t *testing.T) {
	root := t.TempDir()
	registry := NewPermissionCoordinatorRegistry()
	approvals := permission.NewApprovalTracker()
	firstRelease := make(chan PermissionInteractionResult, 1)
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	first := NewQueryEngine(QueryEngineConfig{
		SessionID: "root", RootSessionID: "root", CWD: root,
		PermissionRegistry: registry, ApprovalTracker: approvals,
		PermissionPrompt: blockingPermissionPrompt(firstEntered, firstRelease),
	})
	second := NewQueryEngine(QueryEngineConfig{
		SessionID: "child", RootSessionID: "root", CWD: root,
		PermissionRegistry: registry, ApprovalTracker: approvals,
		PermissionPrompt: blockingPermissionPrompt(secondEntered, make(chan PermissionInteractionResult)),
	})
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)
	coordinator, ok := registry.CoordinatorForProject(root)
	if !ok {
		t.Fatal("shared project coordinator missing")
	}

	firstResult := make(chan permissionCallResult, 1)
	secondResult := make(chan permissionCallResult, 1)
	firstEvents := make(chan QueryEvent, 4)
	secondEvents := make(chan QueryEvent, 4)
	go func() {
		ctx := withPermissionPromptEmitter(withToolUseID(context.Background(), "call-a"), func(event QueryEvent) { firstEvents <- event })
		allowed, reason := first.wrappedCanUseTool(ctx, "Bash", map[string]any{"command": "go test ./..."}, nil)
		firstResult <- permissionCallResult{allowed: allowed, reason: reason}
	}()
	go func() {
		ctx := withPermissionPromptEmitter(withToolUseID(context.Background(), "call-b"), func(event QueryEvent) { secondEvents <- event })
		allowed, reason := second.wrappedCanUseTool(ctx, "Bash", map[string]any{"command": "go test ./..."}, nil)
		secondResult <- permissionCallResult{allowed: allowed, reason: reason}
	}()
	<-firstEntered
	<-secondEntered
	if coordinator.PendingCount() != 2 {
		t.Fatalf("pending count = %d, want 2", coordinator.PendingCount())
	}
	firstRelease <- PermissionInteractionResult{Decision: PermissionAllowSession}
	if result := awaitPermissionCallResult(t, firstResult); !result.allowed {
		t.Fatal("source session grant did not allow its request")
	}
	if result := awaitPermissionCallResult(t, secondResult); !result.allowed || !strings.Contains(result.reason, "session permission") {
		t.Fatalf("coalesced result = %#v", result)
	}
	if coordinator.PendingCount() != 0 {
		t.Fatalf("pending count = %d, want 0", coordinator.PendingCount())
	}
	if approvals.Count() != 1 {
		t.Fatalf("approval count = %d, want one source grant", approvals.Count())
	}
	assertPermissionEventPair(t, firstEvents, PermissionAllowSession, "adapter")
	assertPermissionEventPair(t, secondEvents, PermissionAllowSession, "coalesced")
	if second.ResolvePermissionInteraction("call-b", PermissionInteractionResult{Decision: PermissionAllowAlways}) {
		t.Fatal("late follower response reclaimed a coalesced request")
	}
}

func TestPermissionSessionGrantDoesNotCrossRootSession(t *testing.T) {
	root := t.TempDir()
	registry := NewPermissionCoordinatorRegistry()
	approvals := permission.NewApprovalTracker()
	firstRelease := make(chan PermissionInteractionResult, 1)
	secondRelease := make(chan PermissionInteractionResult, 1)
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	first := NewQueryEngine(QueryEngineConfig{
		SessionID: "root-a", RootSessionID: "root-a", CWD: root,
		PermissionRegistry: registry, ApprovalTracker: approvals,
		PermissionPrompt: blockingPermissionPrompt(firstEntered, firstRelease),
	})
	second := NewQueryEngine(QueryEngineConfig{
		SessionID: "root-b", RootSessionID: "root-b", CWD: root,
		PermissionRegistry: registry, ApprovalTracker: approvals,
		PermissionPrompt: blockingPermissionPrompt(secondEntered, secondRelease),
	})
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)
	coordinator, ok := registry.CoordinatorForProject(root)
	if !ok {
		t.Fatal("shared project coordinator missing")
	}

	firstResult := make(chan permissionCallResult, 1)
	secondResult := make(chan permissionCallResult, 1)
	go runPermissionCall(first, "call-a", "Bash", map[string]any{"command": "go test ./..."}, nil, firstResult)
	go runPermissionCall(second, "call-b", "Bash", map[string]any{"command": "go test ./..."}, nil, secondResult)
	<-firstEntered
	<-secondEntered
	firstRelease <- PermissionInteractionResult{Decision: PermissionAllowSession}
	if result := awaitPermissionCallResult(t, firstResult); !result.allowed {
		t.Fatal("source session grant did not allow its request")
	}
	if coordinator.PendingCount() != 1 {
		t.Fatalf("session grant crossed root lineage; pending = %d", coordinator.PendingCount())
	}
	select {
	case result := <-secondResult:
		t.Fatalf("other root resolved before its own decision: %#v", result)
	default:
	}
	secondRelease <- PermissionInteractionResult{Decision: PermissionDeny}
	if result := awaitPermissionCallResult(t, secondResult); result.allowed {
		t.Fatal("other root unexpectedly allowed")
	}
}

func TestPermissionSessionGrantCoalescesOnlyMatchingScope(t *testing.T) {
	projectRoot := t.TempDir()
	externalRoot := t.TempDir()
	for _, dir := range []string{
		filepath.Join(externalRoot, "pkg", "sub"),
		filepath.Join(externalRoot, "pkg-sibling"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		filepath.Join(externalRoot, "pkg", "source.go"),
		filepath.Join(externalRoot, "pkg", "sub", "match.go"),
		filepath.Join(externalRoot, "pkg-sibling", "near.go"),
		filepath.Join(externalRoot, "write.go"),
		filepath.Join(externalRoot, "other.go"),
	} {
		if err := os.WriteFile(path, []byte("package sample\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name     string
		toolName string
		source   map[string]any
		matching map[string]any
		nearMiss map[string]any
	}{
		{
			name: "exact command", toolName: "Bash",
			source:   map[string]any{"command": "go test ./..."},
			matching: map[string]any{"command": "go test ./..."},
			nearMiss: map[string]any{"command": "go test ./engine"},
		},
		{
			name: "recursive read path", toolName: "Read",
			source:   map[string]any{"file_path": filepath.Join(externalRoot, "pkg", "source.go")},
			matching: map[string]any{"file_path": filepath.Join(externalRoot, "pkg", "sub", "match.go")},
			nearMiss: map[string]any{"file_path": filepath.Join(externalRoot, "pkg-sibling", "near.go")},
		},
		{
			name: "recursive search path", toolName: "Grep",
			source:   map[string]any{"path": filepath.Join(externalRoot, "pkg"), "pattern": "TODO"},
			matching: map[string]any{"path": filepath.Join(externalRoot, "pkg", "sub"), "pattern": "FIXME"},
			nearMiss: map[string]any{"path": filepath.Join(externalRoot, "pkg-sibling"), "pattern": "TODO"},
		},
		{
			name: "exact write path", toolName: "Write",
			source:   map[string]any{"file_path": filepath.Join(externalRoot, "write.go"), "content": "one"},
			matching: map[string]any{"file_path": filepath.Join(externalRoot, "write.go"), "content": "two"},
			nearMiss: map[string]any{"file_path": filepath.Join(externalRoot, "other.go"), "content": "one"},
		},
		{
			name: "canonical input", toolName: "mcp__test__lookup",
			source:   map[string]any{"query": "scope", "limit": float64(2)},
			matching: map[string]any{"limit": float64(2), "query": "scope"},
			nearMiss: map[string]any{"query": "scope", "limit": float64(3)},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			toolRegistry := tools.NewRegistry()
			tools.RegisterDefaults(toolRegistry)
			if tools.IsMCPToolName(test.toolName) {
				toolRegistry.Register(tools.ToolImpl{
					Info: &schema.ToolInfo{
						Name: test.toolName,
						ParamsOneOf: schema.NewParamsOneOfByParams(
							map[string]*schema.ParameterInfo{
								"query": {
									Type:     schema.String,
									Required: true,
								},
								"limit": {Type: schema.Number},
							},
						),
					},
					Capabilities: tools.ToolCapabilities{
						Declared:   true,
						Origin:     tools.ToolOriginMCP,
						ActionKind: tools.ToolActionDynamic,
						Network:    true,
						Dynamic:    true,
					},
				})
			}
			registry := NewPermissionCoordinatorRegistry()
			approvals := permission.NewApprovalTracker()
			sourceDecision := make(chan PermissionInteractionResult, 1)
			nearDecision := make(chan PermissionInteractionResult, 1)
			sourceEntered := make(chan struct{})
			matchingEntered := make(chan struct{})
			nearEntered := make(chan struct{})

			source := NewQueryEngine(QueryEngineConfig{
				SessionID: "source", RootSessionID: "root", CWD: projectRoot,
				PermissionRegistry: registry, ApprovalTracker: approvals,
				PermissionPrompt: blockingPermissionPrompt(sourceEntered, sourceDecision),
				ToolRegistry:     toolRegistry,
			})
			matching := NewQueryEngine(QueryEngineConfig{
				SessionID: "matching", RootSessionID: "root", CWD: projectRoot,
				PermissionRegistry: registry, ApprovalTracker: approvals,
				PermissionPrompt: blockingPermissionPrompt(
					matchingEntered,
					make(chan PermissionInteractionResult),
				),
				ToolRegistry: toolRegistry,
			})
			near := NewQueryEngine(QueryEngineConfig{
				SessionID: "near", RootSessionID: "root", CWD: projectRoot,
				PermissionRegistry: registry, ApprovalTracker: approvals,
				PermissionPrompt: blockingPermissionPrompt(nearEntered, nearDecision),
				ToolRegistry:     toolRegistry,
			})
			t.Cleanup(source.Close)
			t.Cleanup(matching.Close)
			t.Cleanup(near.Close)

			sourceResult := make(chan permissionCallResult, 1)
			matchingResult := make(chan permissionCallResult, 1)
			nearResult := make(chan permissionCallResult, 1)
			go runPermissionCall(source, "source-call", test.toolName, test.source, nil, sourceResult)
			go runPermissionCall(matching, "matching-call", test.toolName, test.matching, nil, matchingResult)
			go runPermissionCall(near, "near-call", test.toolName, test.nearMiss, nil, nearResult)
			<-sourceEntered
			<-matchingEntered
			<-nearEntered

			sourceDecision <- PermissionInteractionResult{Decision: PermissionAllowSession}
			if result := awaitPermissionCallResult(t, sourceResult); !result.allowed {
				t.Fatalf("source result = %#v", result)
			}
			if result := awaitPermissionCallResult(t, matchingResult); !result.allowed {
				t.Fatalf("matching result = %#v", result)
			}
			coordinator, ok := registry.CoordinatorForProject(projectRoot)
			if !ok || coordinator.PendingCount() != 1 {
				t.Fatalf("near-miss pending state = coordinator:%v pending:%d", ok, coordinator.PendingCount())
			}
			select {
			case result := <-nearResult:
				t.Fatalf("near-miss coalesced: %#v", result)
			default:
			}
			nearDecision <- PermissionInteractionResult{Decision: PermissionDeny}
			if result := awaitPermissionCallResult(t, nearResult); result.allowed {
				t.Fatalf("near-miss result = %#v", result)
			}
		})
	}
}

func TestPermissionAlwaysGrantCoalescesOnlyExactPersistedRuleScope(t *testing.T) {
	tests := []struct {
		name     string
		build    func(t *testing.T, externalRoot string) (string, map[string]any, map[string]any, map[string]any)
		wantRule string
	}{
		{
			name: "exact bash command",
			build: func(_ *testing.T, _ string) (string, map[string]any, map[string]any, map[string]any) {
				return "Bash",
					map[string]any{"command": "npm test"},
					map[string]any{"command": "npm test"},
					map[string]any{"command": "go test ./..."}
			},
			wantRule: `"Bash(npm test)"`,
		},
		{
			name: "exact write path",
			build: func(t *testing.T, externalRoot string) (string, map[string]any, map[string]any, map[string]any) {
				allowedDir := filepath.Join(externalRoot, "allowed")
				nearDir := filepath.Join(externalRoot, "allowed-sibling")
				if err := os.MkdirAll(allowedDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(nearDir, 0o755); err != nil {
					t.Fatal(err)
				}
				return "Write",
					map[string]any{"file_path": filepath.Join(allowedDir, "source.go"), "content": "one"},
					map[string]any{"file_path": filepath.Join(allowedDir, "source.go"), "content": "two"},
					map[string]any{"file_path": filepath.Join(nearDir, "near.go"), "content": "three"}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectRoot := t.TempDir()
			externalRoot := t.TempDir()
			candidateRoot := projectRoot
			alias := filepath.Join(t.TempDir(), "project-alias")
			if err := os.Symlink(projectRoot, alias); err == nil {
				candidateRoot = alias
			}
			toolName, sourceInput, matchingInput, nearInput := test.build(t, externalRoot)

			registry := NewPermissionCoordinatorRegistry()
			sourceDecision := make(chan PermissionInteractionResult, 1)
			nearDecision := make(chan PermissionInteractionResult, 1)
			sourceEntered := make(chan struct{})
			matchingEntered := make(chan struct{})
			nearEntered := make(chan struct{})
			source := NewQueryEngine(QueryEngineConfig{
				SessionID: "source", RootSessionID: "source", CWD: projectRoot,
				PermissionRegistry: registry,
				PermissionPrompt:   blockingPermissionPrompt(sourceEntered, sourceDecision),
			})
			matching := NewQueryEngine(QueryEngineConfig{
				SessionID: "matching", RootSessionID: "matching", CWD: candidateRoot,
				PermissionRegistry: registry,
				PermissionPrompt:   blockingPermissionPrompt(matchingEntered, make(chan PermissionInteractionResult)),
			})
			near := NewQueryEngine(QueryEngineConfig{
				SessionID: "near", RootSessionID: "near", CWD: projectRoot,
				PermissionRegistry: registry,
				PermissionPrompt:   blockingPermissionPrompt(nearEntered, nearDecision),
			})
			t.Cleanup(source.Close)
			t.Cleanup(matching.Close)
			t.Cleanup(near.Close)
			if source.permissionCoordinator != matching.permissionCoordinator {
				t.Fatal("canonical project alias did not share the coordinator")
			}

			sourceResult := make(chan permissionCallResult, 1)
			matchingResult := make(chan permissionCallResult, 1)
			nearResult := make(chan permissionCallResult, 1)
			matchingEvents := make(chan QueryEvent, 4)
			go runPermissionCall(source, "source-call", toolName, sourceInput, nil, sourceResult)
			go runPermissionCall(matching, "matching-call", toolName, matchingInput, func(event QueryEvent) { matchingEvents <- event }, matchingResult)
			go runPermissionCall(near, "near-call", toolName, nearInput, nil, nearResult)
			<-sourceEntered
			<-matchingEntered
			<-nearEntered

			sourceDecision <- PermissionInteractionResult{Decision: PermissionAllowAlways}
			if result := awaitPermissionCallResult(t, sourceResult); !result.allowed {
				t.Fatalf("source result = %#v", result)
			}
			if result := awaitPermissionCallResult(t, matchingResult); !result.allowed || !strings.Contains(result.reason, "project permission") {
				t.Fatalf("matching result = %#v", result)
			}
			assertPermissionEventPair(t, matchingEvents, PermissionAllowAlways, "coalesced")
			if near.permissionCoordinator.PendingCount() != 1 {
				t.Fatalf("persisted rule crossed near-miss scope; pending = %d", near.permissionCoordinator.PendingCount())
			}
			if matching.ResolvePermissionInteraction("matching-call", PermissionInteractionResult{Decision: PermissionAllowAlways}) {
				t.Fatal("late matching response reclaimed a coalesced request")
			}

			settings, err := os.ReadFile(filepath.Join(projectRoot, ".claude", "settings.local.json"))
			if err != nil {
				t.Fatal(err)
			}
			if test.wantRule != "" && strings.Count(string(settings), test.wantRule) != 1 {
				t.Fatalf("persisted settings = %s, want one %s", settings, test.wantRule)
			}
			nearDecision <- PermissionInteractionResult{Decision: PermissionDeny}
			if result := awaitPermissionCallResult(t, nearResult); result.allowed {
				t.Fatalf("near-miss result = %#v", result)
			}
		})
	}
}

func TestUnrelatedProjectAlwaysGrantDoesNotRefreshPendingPolicySnapshot(
	t *testing.T,
) {
	root := t.TempDir()
	candidate := NewQueryEngine(QueryEngineConfig{
		SessionID: "candidate",
		CWD:       root,
		PermissionPrompt: func(context.Context, PermissionPromptRequest) PermissionInteractionResult {
			return PermissionInteractionResult{Decision: PermissionDeny}
		},
	})
	t.Cleanup(candidate.Close)

	candidateInput := map[string]any{"command": "printf approved"}
	initialAction, err := candidate.buildPermissionActionDescriptor(
		"Bash",
		candidateInput,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	initialPolicyID := candidate.effectivePolicySnapshot(nil).ID()

	sourceRule, err := permission.BuildExactRuleFromInvocation(
		"Bash",
		map[string]any{"command": "echo approved"},
		root,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := permission.PersistPermissionRules(
		root,
		[]string{sourceRule.Value},
		permission.ActionAllow,
		permission.DestLocalSettings,
	); err != nil {
		t.Fatal(err)
	}
	sourceRule.Rule.Source = "coalesced-grant"
	if _, allowed := candidate.permissionGrantEvaluator(initialAction, nil)(
		permissionCoalescingGrant{
			Decision:   PermissionAllowAlways,
			AlwaysRule: sourceRule.Rule,
		},
	); allowed {
		t.Fatal("unrelated exact project grant authorized pending action")
	}
	if currentPolicyID := candidate.effectivePolicySnapshot(nil).ID(); currentPolicyID != initialPolicyID {
		t.Fatalf(
			"unrelated exact project grant refreshed pending policy: %s -> %s",
			initialPolicyID,
			currentPolicyID,
		)
	}
}

func TestPermissionAlwaysGrantRespectsCurrentAskAndDenyPrecedence(t *testing.T) {
	for _, action := range []permission.PermissionAction{permission.ActionAsk, permission.ActionDeny} {
		t.Run(string(action), func(t *testing.T) {
			root := t.TempDir()
			registry := NewPermissionCoordinatorRegistry()
			sourceDecision := make(chan PermissionInteractionResult, 1)
			candidateDecision := make(chan PermissionInteractionResult, 1)
			sourceEntered := make(chan struct{})
			candidateEntered := make(chan struct{})
			source := NewQueryEngine(QueryEngineConfig{
				SessionID: "source", RootSessionID: "source", CWD: root,
				PermissionRegistry: registry,
				PermissionPrompt:   blockingPermissionPrompt(sourceEntered, sourceDecision),
			})
			candidate := NewQueryEngine(QueryEngineConfig{
				SessionID: "candidate", RootSessionID: "candidate", CWD: root,
				PermissionRegistry: registry,
				PermissionPrompt:   blockingPermissionPrompt(candidateEntered, candidateDecision),
			})
			t.Cleanup(source.Close)
			t.Cleanup(candidate.Close)

			sourceResult := make(chan permissionCallResult, 1)
			candidateResult := make(chan permissionCallResult, 1)
			go runPermissionCall(source, "source-call", "Bash", map[string]any{"command": "npm test"}, nil, sourceResult)
			go runPermissionCall(candidate, "candidate-call", "Bash", map[string]any{"command": "npm run secret"}, nil, candidateResult)
			<-sourceEntered
			<-candidateEntered

			candidate.mu.Lock()
			candidate.permissionRules = permission.NewRulesEngine(
				[]permission.PermissionRule{{
					ToolName:     "Bash",
					InputPattern: "npm run secret",
					Action:       action,
					Source:       permission.SourceProject,
				}},
			)
			candidate.mu.Unlock()

			sourceDecision <- PermissionInteractionResult{Decision: PermissionAllowAlways}
			if result := awaitPermissionCallResult(t, sourceResult); !result.allowed {
				t.Fatalf("source result = %#v", result)
			}
			if candidate.permissionCoordinator.PendingCount() != 1 {
				t.Fatalf("%s precedence was bypassed; pending = %d", action, candidate.permissionCoordinator.PendingCount())
			}
			select {
			case result := <-candidateResult:
				t.Fatalf("candidate bypassed %s rule: %#v", action, result)
			default:
			}
			candidateDecision <- PermissionInteractionResult{Decision: PermissionDeny}
			if result := awaitPermissionCallResult(t, candidateResult); result.allowed {
				t.Fatalf("candidate result = %#v", result)
			}
		})
	}
}

func TestPermissionAlwaysGrantDoesNotCrossCanonicalProject(t *testing.T) {
	registry := NewPermissionCoordinatorRegistry()
	sourceRoot := t.TempDir()
	candidateRoot := t.TempDir()
	sourceDecision := make(chan PermissionInteractionResult, 1)
	candidateDecision := make(chan PermissionInteractionResult, 1)
	sourceEntered := make(chan struct{})
	candidateEntered := make(chan struct{})
	source := NewQueryEngine(QueryEngineConfig{
		SessionID: "source", CWD: sourceRoot, PermissionRegistry: registry,
		PermissionPrompt: blockingPermissionPrompt(sourceEntered, sourceDecision),
	})
	candidate := NewQueryEngine(QueryEngineConfig{
		SessionID: "candidate", CWD: candidateRoot, PermissionRegistry: registry,
		PermissionPrompt: blockingPermissionPrompt(candidateEntered, candidateDecision),
	})
	t.Cleanup(source.Close)
	t.Cleanup(candidate.Close)

	sourceResult := make(chan permissionCallResult, 1)
	candidateResult := make(chan permissionCallResult, 1)
	go runPermissionCall(source, "source-call", "Bash", map[string]any{"command": "npm test"}, nil, sourceResult)
	go runPermissionCall(candidate, "candidate-call", "Bash", map[string]any{"command": "npm run lint"}, nil, candidateResult)
	<-sourceEntered
	<-candidateEntered
	sourceDecision <- PermissionInteractionResult{Decision: PermissionAllowAlways}
	if result := awaitPermissionCallResult(t, sourceResult); !result.allowed {
		t.Fatalf("source result = %#v", result)
	}
	if candidate.permissionCoordinator.PendingCount() != 1 {
		t.Fatalf("project grant crossed coordinator boundary; pending = %d", candidate.permissionCoordinator.PendingCount())
	}
	if _, err := os.Stat(filepath.Join(candidateRoot, ".claude", "settings.local.json")); !os.IsNotExist(err) {
		t.Fatalf("source grant wrote candidate project settings: %v", err)
	}
	candidateDecision <- PermissionInteractionResult{Decision: PermissionDeny}
	if result := awaitPermissionCallResult(t, candidateResult); result.allowed {
		t.Fatalf("candidate result = %#v", result)
	}
}

func TestPermissionNonDurableDecisionsDoNotCoalesce(t *testing.T) {
	for _, decision := range []PermissionInteractionDecision{
		PermissionAllowOnce,
		PermissionDeny,
		PermissionCancelled,
		PermissionTimedOut,
	} {
		t.Run(string(decision), func(t *testing.T) {
			root := t.TempDir()
			registry := NewPermissionCoordinatorRegistry()
			approvals := permission.NewApprovalTracker()
			sourceDecision := make(chan PermissionInteractionResult, 1)
			candidateDecision := make(chan PermissionInteractionResult, 1)
			sourceEntered := make(chan struct{})
			candidateEntered := make(chan struct{})
			source := NewQueryEngine(QueryEngineConfig{
				SessionID: "source", RootSessionID: "root", CWD: root,
				PermissionRegistry: registry, ApprovalTracker: approvals,
				PermissionPrompt: blockingPermissionPrompt(sourceEntered, sourceDecision),
			})
			candidate := NewQueryEngine(QueryEngineConfig{
				SessionID: "candidate", RootSessionID: "root", CWD: root,
				PermissionRegistry: registry, ApprovalTracker: approvals,
				PermissionPrompt: blockingPermissionPrompt(candidateEntered, candidateDecision),
			})
			t.Cleanup(source.Close)
			t.Cleanup(candidate.Close)

			sourceResult := make(chan permissionCallResult, 1)
			candidateResult := make(chan permissionCallResult, 1)
			input := map[string]any{"command": "go test ./..."}
			go runPermissionCall(source, "source-call", "Bash", input, nil, sourceResult)
			go runPermissionCall(candidate, "candidate-call", "Bash", input, nil, candidateResult)
			<-sourceEntered
			<-candidateEntered
			sourceDecision <- PermissionInteractionResult{Decision: decision}
			sourceOutcome := awaitPermissionCallResult(t, sourceResult)
			if sourceOutcome.allowed != (decision == PermissionAllowOnce) {
				t.Fatalf("source outcome = %#v", sourceOutcome)
			}
			if candidate.permissionCoordinator.PendingCount() != 1 {
				t.Fatalf("%s cascaded; pending = %d", decision, candidate.permissionCoordinator.PendingCount())
			}
			candidateDecision <- PermissionInteractionResult{Decision: PermissionDeny}
			if result := awaitPermissionCallResult(t, candidateResult); result.allowed {
				t.Fatalf("candidate result = %#v", result)
			}
		})
	}
}

func TestPermissionBypassHeadlessEngineNeverRegistersCoalescingCandidate(t *testing.T) {
	root := t.TempDir()
	registry := NewPermissionCoordinatorRegistry()
	sourceDecision := make(chan PermissionInteractionResult, 1)
	sourceEntered := make(chan struct{})
	source := NewQueryEngine(QueryEngineConfig{
		SessionID: "source", RootSessionID: "root", CWD: root,
		PermissionRegistry: registry,
		PermissionPrompt:   blockingPermissionPrompt(sourceEntered, sourceDecision),
	})
	bypass := NewQueryEngine(QueryEngineConfig{
		SessionID: "headless", RootSessionID: "root", CWD: root,
		PermissionMode:     permission.ModeBypassPermissions,
		PermissionRegistry: registry,
		CanUseTool: func(context.Context, string, map[string]any, *ToolUseContext) (bool, string) {
			return true, ""
		},
	})
	t.Cleanup(source.Close)
	t.Cleanup(bypass.Close)

	sourceResult := make(chan permissionCallResult, 1)
	go runPermissionCall(source, "source-call", "Bash", map[string]any{"command": "go test ./..."}, nil, sourceResult)
	<-sourceEntered

	bypassCtx := &ToolUseContext{Options: &ToolUseOptions{PermissionMode: permission.ModeBypassPermissions}}
	allowed, reason := bypass.wrappedCanUseTool(
		withToolUseID(context.Background(), "headless-call"),
		"Bash",
		map[string]any{"command": "go test ./..."},
		bypassCtx,
	)
	if !allowed || reason != "" {
		t.Fatalf("bypass result = allowed:%v reason:%q", allowed, reason)
	}
	if source.permissionCoordinator.PendingCount() != 1 {
		t.Fatalf("headless bypass registered a waiter; pending = %d", source.permissionCoordinator.PendingCount())
	}

	sourceDecision <- PermissionInteractionResult{Decision: PermissionAllowSession}
	if result := awaitPermissionCallResult(t, sourceResult); !result.allowed {
		t.Fatalf("source result = %#v", result)
	}
	if source.permissionCoordinator.PendingCount() != 0 {
		t.Fatalf("pending count after source settlement = %d", source.permissionCoordinator.PendingCount())
	}
}

func TestPermissionPendingRequestDoesNotReplayAfterEngineRestart(t *testing.T) {
	root := t.TempDir()
	transcriptDir := t.TempDir()
	registry := NewPermissionCoordinatorRegistry()
	entered := make(chan struct{})
	events := make(chan QueryEvent, 4)
	outcomeCh := make(chan *toolExecutionOutcome, 1)
	var executions atomic.Int32
	old := NewQueryEngine(QueryEngineConfig{
		SessionID: "restart-session", CWD: root, TranscriptDir: transcriptDir,
		PermissionRegistry: registry,
		PermissionPrompt:   blockingPermissionPrompt(entered, make(chan PermissionInteractionResult)),
	})

	go func() {
		outcomeCh <- executeToolCall(context.Background(), QueryParams{
			CanUseTool:   old.wrappedCanUseTool,
			ToolRegistry: old.toolRegistry,
			ToolExecutor: func(context.Context, string, string) (string, error) {
				executions.Add(1)
				return "executed", nil
			},
		}, nil, nil, &schema.ToolCall{
			ID: "restart-call", Type: "function",
			Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"go test ./..."}`},
		}, func(event QueryEvent) {
			if event.Type == EventPermissionRequest || event.Type == EventPermissionResolved {
				events <- event
			}
		})
	}()
	<-entered
	old.Close()
	select {
	case outcome := <-outcomeCh:
		if outcome == nil || outcome.Result == nil {
			t.Fatalf("cancelled outcome = %#v", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled permission call did not settle")
	}
	if executions.Load() != 0 {
		t.Fatalf("cancelled pre-restart tool executed %d time(s)", executions.Load())
	}
	assertPermissionEventPair(t, events, PermissionCancelled, "shutdown")
	if registry.ActiveProjectCount() != 0 {
		t.Fatalf("old registry retained %d project(s)", registry.ActiveProjectCount())
	}

	restartedRegistry := NewPermissionCoordinatorRegistry()
	restarted := NewQueryEngine(QueryEngineConfig{
		SessionID: "restart-session", CWD: root, TranscriptDir: transcriptDir,
		PermissionRegistry: restartedRegistry,
	})
	t.Cleanup(restarted.Close)
	if restarted.permissionCoordinator.PendingCount() != 0 {
		t.Fatalf("restarted engine rehydrated %d actionable waiter(s)", restarted.permissionCoordinator.PendingCount())
	}
	if executions.Load() != 0 {
		t.Fatalf("restarted engine replayed cancelled tool %d time(s)", executions.Load())
	}
}

type permissionCoalescingSubagentModel struct {
	calls    atomic.Int32
	filePath string
}

func (m *permissionCoalescingSubagentModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return m.next(), nil
}

func (m *permissionCoalescingSubagentModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.next()}), nil
}

func (m *permissionCoalescingSubagentModel) next() *schema.Message {
	if m.calls.Add(1) == 1 {
		return &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID: "child-read", Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: fmt.Sprintf(`{"file_path":%q}`, m.filePath),
				},
			}},
		}
	}
	return &schema.Message{Role: schema.Assistant, Content: "child complete"}
}

func TestPermissionSessionGrantCoalescesRealSubagentExecution(t *testing.T) {
	projectRoot := t.TempDir()
	externalRoot := t.TempDir()
	filePath := filepath.Join(externalRoot, "shared.go")
	if err := os.WriteFile(filePath, []byte("package shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info: &schema.ToolInfo{
			Name: "Read",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"file_path": {Type: schema.String, Required: true},
			}),
		},
		ExecuteCtx: func(context.Context, string) (string, error) { return "package shared", nil },
	})

	parentEntered := make(chan struct{})
	childEntered := make(chan struct{})
	parentDecision := make(chan PermissionInteractionResult, 1)
	prompt := func(ctx context.Context, request PermissionPromptRequest) PermissionInteractionResult {
		if request.SessionID == "root" {
			return blockingPermissionPrompt(parentEntered, parentDecision)(ctx, request)
		}
		return blockingPermissionPrompt(childEntered, make(chan PermissionInteractionResult))(ctx, request)
	}
	parent := NewQueryEngine(QueryEngineConfig{
		SessionID: "root", RootSessionID: "root", CWD: projectRoot,
		TranscriptDir:      t.TempDir(),
		ChatModel:          &permissionCoalescingSubagentModel{filePath: filePath},
		ToolRegistry:       registry,
		PermissionRegistry: NewPermissionCoordinatorRegistry(),
		PermissionPrompt:   prompt,
	})
	t.Cleanup(parent.Close)
	if parent.subagentExecutor == nil {
		t.Fatal("parent subagent executor was not configured")
	}
	parent.agentRunner.SetOutputDir(filepath.Join(t.TempDir(), "agent-output"))

	childResult := make(chan *tools.AgentExecResult, 1)
	childErr := make(chan error, 1)
	go func() {
		result, err := tools.RunAgent(context.Background(), parent.agentRunner, tools.AgentExecOptions{
			Task: "read the shared file", SessionID: "child", ThreadID: "child-thread",
			ParentSessionID: "root", ParentThreadID: "root", AgentID: "agent-child",
			AllowedTools: []string{"Read"}, MaxTurns: 3,
		})
		childResult <- result
		childErr <- err
	}()
	select {
	case <-childEntered:
	case err := <-childErr:
		t.Fatalf("subagent failed before permission prompt: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("subagent did not reach permission prompt")
	}

	parentResult := make(chan permissionCallResult, 1)
	go runPermissionCall(parent, "parent-read", "Read", map[string]any{"file_path": filePath}, nil, parentResult)
	<-parentEntered
	parentDecision <- PermissionInteractionResult{Decision: PermissionAllowSession}
	if result := awaitPermissionCallResult(t, parentResult); !result.allowed {
		t.Fatalf("parent result = %#v", result)
	}

	select {
	case err := <-childErr:
		if err != nil {
			t.Fatalf("subagent execution failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subagent did not resume after the parent grant")
	}
	result := <-childResult
	if result == nil || result.Result != "child complete" || !slices.Contains(result.ToolsUsed, "Read") {
		t.Fatalf("subagent result = %#v", result)
	}
	if parent.approvalTracker.Count() != 1 {
		t.Fatalf("session grant count = %d, want one source grant", parent.approvalTracker.Count())
	}
}

func TestPermissionCoalescingCandidateShutdownWinsDuringScan(t *testing.T) {
	registry := NewPermissionCoordinatorRegistry()
	coordinator, identity := registry.acquire(t.TempDir(), "source")
	coordinator.registerEngine("candidate")
	sourceDecision := make(chan PermissionInteractionResult, 1)
	sourceEntered := make(chan struct{})
	candidateEntered := make(chan struct{})
	evaluationStarted := make(chan struct{})
	releaseEvaluation := make(chan struct{})
	sourceResult := make(chan PermissionInteractionResult, 1)
	candidateResult := make(chan PermissionInteractionResult, 1)
	candidateEvents := make(chan QueryEvent, 4)
	var candidateGrantCommits atomic.Int32

	go func() {
		sourceResult <- coordinator.request(
			context.Background(),
			"source",
			PermissionPromptRequest{
				ToolName: "Bash", ToolUseID: "source-call", Input: map[string]any{"command": "go test ./..."},
				RootSessionID: "root", ProjectIdentity: identity,
			},
			blockingPermissionPrompt(sourceEntered, sourceDecision),
			nil,
			func(result PermissionInteractionResult) PermissionInteractionResult { return result },
			nil,
		)
	}()
	go func() {
		candidateResult <- coordinator.request(
			context.Background(),
			"candidate",
			PermissionPromptRequest{
				ToolName: "Bash", ToolUseID: "candidate-call", Input: map[string]any{"command": "go test ./..."},
				RootSessionID: "root", ProjectIdentity: identity,
			},
			blockingPermissionPrompt(candidateEntered, make(chan PermissionInteractionResult)),
			func(event QueryEvent) { candidateEvents <- event },
			func(result PermissionInteractionResult) PermissionInteractionResult {
				if result.Decision == PermissionAllowSession || result.Decision == PermissionAllowAlways {
					candidateGrantCommits.Add(1)
				}
				return result
			},
			func(
				permissionCoalescingGrant,
			) (PermissionActionDescriptor, bool) {
				close(evaluationStarted)
				<-releaseEvaluation
				return PermissionActionDescriptor{
					RequestedToolName: "Bash",
					CanonicalToolName: "Bash",
					Input: map[string]any{
						"command": "go test ./...",
					},
				}, true
			},
		)
	}()
	<-sourceEntered
	<-candidateEntered
	sourceDecision <- PermissionInteractionResult{Decision: PermissionAllowSession}
	<-evaluationStarted

	candidateReleased := make(chan struct{})
	go func() {
		registry.release(identity, "candidate")
		close(candidateReleased)
	}()
	result := <-candidateResult
	<-candidateReleased
	if result.Decision != PermissionCancelled {
		t.Fatalf("candidate result = %#v", result)
	}
	close(releaseEvaluation)
	if result := <-sourceResult; result.Decision != PermissionAllowSession {
		t.Fatalf("source result = %#v", result)
	}
	registry.release(identity, "source")

	if candidateGrantCommits.Load() != 0 {
		t.Fatalf("losing candidate committed %d grant(s)", candidateGrantCommits.Load())
	}
	assertPermissionEventPair(t, candidateEvents, PermissionCancelled, "shutdown")
	if coordinator.resolve("candidate", "candidate-call", PermissionInteractionResult{Decision: PermissionAllowAlways}, "late") {
		t.Fatal("late candidate response reclaimed shutdown request")
	}
	if registry.ActiveProjectCount() != 0 {
		t.Fatalf("registry retained %d project runtime(s)", registry.ActiveProjectCount())
	}
}

func TestRootAndChildShareSessionApprovalWithoutSharingOtherRoots(t *testing.T) {
	root := t.TempDir()
	registry := NewPermissionCoordinatorRegistry()
	approvals := permission.NewApprovalTracker()
	promptCalls := atomic.Int32{}
	prompt := func(context.Context, PermissionPromptRequest) PermissionInteractionResult {
		promptCalls.Add(1)
		return PermissionInteractionResult{Decision: PermissionAllowSession}
	}
	rootEngine := NewQueryEngine(QueryEngineConfig{
		SessionID: "root", RootSessionID: "root", CWD: root,
		PermissionRegistry: registry, ApprovalTracker: approvals, PermissionPrompt: prompt,
	})
	childEngine := NewQueryEngine(QueryEngineConfig{
		SessionID: "child", RootSessionID: "root", CWD: root,
		PermissionRegistry: registry, ApprovalTracker: approvals,
		PermissionPrompt: func(context.Context, PermissionPromptRequest) PermissionInteractionResult {
			t.Fatal("shared lineage should use the committed session approval")
			return PermissionInteractionResult{Decision: PermissionDeny}
		},
	})
	otherRoot := NewQueryEngine(QueryEngineConfig{
		SessionID: "other", RootSessionID: "other", CWD: root,
		PermissionRegistry: registry, ApprovalTracker: approvals,
		PermissionPrompt: func(context.Context, PermissionPromptRequest) PermissionInteractionResult {
			promptCalls.Add(1)
			return PermissionInteractionResult{Decision: PermissionDeny}
		},
	})
	t.Cleanup(rootEngine.Close)
	t.Cleanup(childEngine.Close)
	t.Cleanup(otherRoot.Close)

	input := map[string]any{"command": "go test ./..."}
	rootCtx := withToolUseID(context.Background(), "root-call")
	if allowed, _ := rootEngine.wrappedCanUseTool(rootCtx, "Bash", input, nil); !allowed {
		t.Fatal("root session approval failed")
	}
	if allowed, _ := childEngine.wrappedCanUseTool(withToolUseID(context.Background(), "child-call"), "Bash", input, nil); !allowed {
		t.Fatal("child did not inherit root-lineage session approval")
	}
	if allowed, _ := otherRoot.wrappedCanUseTool(withToolUseID(context.Background(), "other-call"), "Bash", input, nil); allowed {
		t.Fatal("session approval crossed into another root session")
	}
	if promptCalls.Load() != 2 {
		t.Fatalf("prompt calls = %d, want root plus other root", promptCalls.Load())
	}
}

func TestConcurrentProjectAlwaysGrantsFailClosedWithoutLostPersistence(
	t *testing.T,
) {
	root := t.TempDir()
	var ready sync.WaitGroup
	ready.Add(2)
	release := make(chan struct{})
	prompt := func(context.Context, PermissionPromptRequest) PermissionInteractionResult {
		ready.Done()
		<-release
		return PermissionInteractionResult{Decision: PermissionAllowAlways}
	}
	first := NewQueryEngine(QueryEngineConfig{
		SessionID: "first", CWD: root, PermissionPrompt: prompt,
	})
	second := NewQueryEngine(QueryEngineConfig{
		SessionID: "second", CWD: root, PermissionPrompt: prompt,
	})
	t.Cleanup(first.Close)
	t.Cleanup(second.Close)
	if first.permissionRegistry != second.permissionRegistry || first.permissionCoordinator != second.permissionCoordinator {
		t.Fatal("default same-project engines did not share the process-local coordinator")
	}

	type grantCall struct {
		engine  *QueryEngine
		callID  string
		command string
	}
	type grantResult struct {
		engine  *QueryEngine
		command string
		allowed bool
		reason  string
	}
	results := make(chan grantResult, 2)
	for _, call := range []grantCall{
		{engine: first, callID: "always-echo", command: "echo approved"},
		{engine: second, callID: "always-printf", command: "printf approved"},
	} {
		go func(call grantCall) {
			ctx := withToolUseID(context.Background(), call.callID)
			allowed, reason := call.engine.wrappedCanUseTool(
				ctx,
				"Bash",
				map[string]any{"command": call.command},
				nil,
			)
			results <- grantResult{
				engine:  call.engine,
				command: call.command,
				allowed: allowed,
				reason:  reason,
			}
		}(call)
	}
	ready.Wait()
	close(release)
	firstResult := <-results
	secondResult := <-results
	var denied grantResult
	switch {
	case firstResult.allowed && !secondResult.allowed:
		denied = secondResult
	case secondResult.allowed && !firstResult.allowed:
		denied = firstResult
	default:
		t.Fatalf(
			"concurrent results = %#v / %#v, want one allowed and one fail-closed",
			firstResult,
			secondResult,
		)
	}
	if !strings.Contains(denied.reason, "policy changed") {
		t.Fatalf("concurrent denial reason = %q", denied.reason)
	}

	settings, err := os.ReadFile(filepath.Join(root, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{
		`"Bash(echo approved)"`,
		`"Bash(printf approved)"`,
	} {
		if !strings.Contains(string(settings), rule) {
			t.Fatalf("persisted settings missing %s: %s", rule, settings)
		}
	}
	denied.engine.reloadPermissionRules()
	if allowed, reason := denied.engine.wrappedCanUseTool(
		withToolUseID(context.Background(), "retry-"+denied.command),
		"Bash",
		map[string]any{"command": denied.command},
		nil,
	); !allowed || reason != "" {
		t.Fatalf(
			"persisted exact grant did not authorize retry: (%v, %q)",
			allowed,
			reason,
		)
	}
}

type permissionCallResult struct {
	allowed bool
	reason  string
}

func runPermissionCall(
	engine *QueryEngine,
	toolUseID string,
	toolName string,
	input map[string]any,
	emit func(QueryEvent),
	result chan<- permissionCallResult,
) {
	ctx := withToolUseID(context.Background(), toolUseID)
	if emit != nil {
		ctx = withPermissionPromptEmitter(ctx, emit)
	}
	allowed, reason := engine.wrappedCanUseTool(ctx, toolName, input, nil)
	result <- permissionCallResult{allowed: allowed, reason: reason}
}

func awaitPermissionCallResult(t *testing.T, results <-chan permissionCallResult) permissionCallResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("permission call did not resolve")
		return permissionCallResult{}
	}
}

func assertPermissionEventPair(
	t *testing.T,
	events <-chan QueryEvent,
	decision PermissionInteractionDecision,
	reason string,
) {
	t.Helper()
	first := <-events
	second := <-events
	if first.Type != EventPermissionRequest || second.Type != EventPermissionResolved {
		t.Fatalf("permission event order = %s, %s", first.Type, second.Type)
	}
	if second.PermissionResolved == nil || second.PermissionResolved.Decision != string(decision) || second.PermissionResolved.Reason != reason {
		t.Fatalf("permission resolution = %#v", second.PermissionResolved)
	}
	select {
	case extra := <-events:
		t.Fatalf("duplicate permission event = %#v", extra)
	default:
	}
}

func blockingPermissionPrompt(entered chan<- struct{}, decisions <-chan PermissionInteractionResult) PermissionPromptFn {
	return func(ctx context.Context, _ PermissionPromptRequest) PermissionInteractionResult {
		entered <- struct{}{}
		select {
		case decision := <-decisions:
			return decision
		case <-ctx.Done():
			return PermissionInteractionResult{Decision: PermissionCancelled}
		}
	}
}
