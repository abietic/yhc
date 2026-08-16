package appserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

func TestPermissionBrokerResolvesExactlyOnce(t *testing.T) {
	broker := newPermissionBroker()
	request := permissionBrokerTestRequest("call-1")
	resultCh := make(chan engine.PermissionInteractionResult, 1)
	go func() {
		resultCh <- broker.wait(context.Background(), request)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		broker.mu.Lock()
		_, pending := broker.pending[request.ToolUseID]
		broker.mu.Unlock()
		if pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("permission request was not registered")
		}
		time.Sleep(time.Millisecond)
	}
	broker.observeEvent(request, "turn-1")
	if broker.resolve(request.ToolUseID, permissionResolution(engine.PermissionAllowSession)) != interactionResolveAccepted {
		t.Fatal("first resolution was rejected")
	}
	if broker.resolve(request.ToolUseID, permissionResolution(engine.PermissionDeny)) != interactionResolveNotFound {
		t.Fatal("duplicate resolution was accepted")
	}
	select {
	case result := <-resultCh:
		if result.Decision != engine.PermissionAllowSession {
			t.Fatalf("decision = %q", result.Decision)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for permission result")
	}
}

func TestP512PermissionBrokerBindsAllowOnceConstraint(t *testing.T) {
	unconstrained := permissionBrokerTestRequest("critical-bash")
	unconstrained.ToolName = "Bash"
	unconstrained.CanonicalToolName = "Bash"
	unconstrained.Presentation.ToolLabel = "Bash"
	unconstrained.Presentation.Evidence[0].Value = "May make destructive changes"

	constrained := clonePromptRequest(unconstrained)
	constrained.DecisionConstraint = engine.PermissionAllowOnceOnly
	constrained.Presentation.GrantScopes = []engine.PermissionInteractionDecision{
		engine.PermissionAllowOnce,
	}
	reconstructed := permissionPromptRequest(
		"session-1",
		"thread-1",
		"agent-1",
		engine.PermissionRequestEvent{
			Kind:               constrained.Kind,
			Source:             constrained.Source,
			ToolName:           constrained.ToolName,
			CanonicalToolName:  constrained.CanonicalToolName,
			ToolUseID:          constrained.ToolUseID,
			Presentation:       constrained.Presentation,
			DecisionConstraint: constrained.DecisionConstraint,
		},
	)
	if reconstructed.DecisionConstraint != engine.PermissionAllowOnceOnly {
		t.Fatalf("reconstructed constraint = %q", reconstructed.DecisionConstraint)
	}
	normalDigest, normalOK := permissionRequestDigest(unconstrained)
	constrainedDigest, constrainedOK := permissionRequestDigest(constrained)
	if !normalOK || !constrainedOK || normalDigest == constrainedDigest {
		t.Fatalf("permission digests normal=%q/%v constrained=%q/%v", normalDigest, normalOK, constrainedDigest, constrainedOK)
	}

	projected, ok := projectInteraction(constrained, "turn-critical")
	if !ok || projected.Permission == nil || !projected.Permission.Available ||
		len(projected.Permission.GrantScopes) != 1 ||
		projected.Permission.GrantScopes[0] != string(engine.PermissionAllowOnce) {
		t.Fatalf("constrained projection = %#v, ok=%v", projected, ok)
	}

	broker := newPermissionBroker()
	resultCh := waitForBrokerResult(context.Background(), broker, constrained)
	broker.observeEvent(constrained, "turn-critical")
	waitForPermissionWaiter(t, broker, constrained.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.eventObserved && waiter.callbackObserved
	})
	for _, forged := range []engine.PermissionInteractionDecision{
		engine.PermissionAllowSession,
		engine.PermissionAllowAlways,
	} {
		if status := broker.resolve(constrained.ToolUseID, ResolveInteractionRequest{
			Kind:       engine.PermissionInteractionKindPermission,
			Permission: &ResolvePermissionResult{Decision: string(forged)},
		}); status != interactionResolveInvalid {
			t.Fatalf("forged %s status = %v", forged, status)
		}
	}
	if status := broker.resolve(constrained.ToolUseID, ResolveInteractionRequest{
		Kind:       engine.PermissionInteractionKindPermission,
		Permission: &ResolvePermissionResult{Decision: string(engine.PermissionAllowOnce)},
	}); status != interactionResolveAccepted {
		t.Fatalf("AllowOnce status = %v", status)
	}
	if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionAllowOnce {
		t.Fatalf("AllowOnce result = %#v", result)
	}

	conflictBroker := newPermissionBroker()
	conflictResult := make(chan engine.PermissionInteractionResult, 1)
	go func() { conflictResult <- conflictBroker.wait(context.Background(), unconstrained) }()
	conflictBroker.observeEvent(constrained, "turn-conflict")
	if result := receiveBrokerResult(t, conflictResult); result.Decision != engine.PermissionCancelled ||
		result.Message != "permission request conflict" {
		t.Fatalf("constraint conflict result = %#v", result)
	}
}

func TestPermissionBrokerRejectsUntypedOrMismatchedProducerIdentity(t *testing.T) {
	valid := permissionBrokerTestRequest("typed-identity")
	if _, ok := permissionRequestDigest(valid); !ok {
		t.Fatal("valid typed request was rejected")
	}
	for name, mutate := range map[string]func(*engine.PermissionPromptRequest){
		"empty kind": func(request *engine.PermissionPromptRequest) {
			request.Kind = ""
		},
		"empty source": func(request *engine.PermissionPromptRequest) {
			request.Source = ""
		},
		"permission attempt": func(request *engine.PermissionPromptRequest) {
			request.Attempt = 1
		},
		"permission carries plan": func(request *engine.PermissionPromptRequest) {
			request.PlanApproval = &engine.PlanApprovalRequest{RequestID: request.ToolUseID}
		},
	} {
		t.Run(name, func(t *testing.T) {
			request := clonePromptRequest(valid)
			mutate(&request)
			if _, ok := permissionRequestDigest(request); ok {
				t.Fatalf("invalid request admitted: %#v", request)
			}
		})
	}

	repeated := engine.PermissionPromptRequest{
		Kind: engine.PermissionInteractionKindRepeatedTool, Source: "repeated_tool_guard",
		Attempt: 3, ToolUseID: "typed-repeated",
	}
	if _, ok := permissionRequestDigest(repeated); !ok {
		t.Fatal("valid repeated request was rejected")
	}
	repeated.Attempt = 2
	if _, ok := permissionRequestDigest(repeated); ok {
		t.Fatal("repeated request with mismatched attempt was admitted")
	}
}

func TestRepeatedPromptPreservesTypedIdentity(t *testing.T) {
	broker := newPermissionBroker()
	event := engine.PermissionRequestEvent{
		Kind: engine.PermissionInteractionKindRepeatedTool, Attempt: 3, Source: "repeated_tool_guard",
		ToolName: "Bash", ToolUseID: "repeated-1",
		Message: engine.RepeatedToolInteractionPromptMessage,
	}
	request := permissionPromptRequest("session-1", "thread-1", "agent-1", event)
	broker.observeEvent(request, "turn-1")
	type repeatedResult struct {
		allowed bool
		reason  string
	}
	resultCh := make(chan repeatedResult, 1)
	go func() {
		allowed, reason := broker.repeatedPrompt(context.Background(), "Bash", "repeated-1", 3, &engine.ToolUseContext{
			SessionID: "session-1",
			ThreadID:  "thread-1",
			AgentID:   "agent-1",
		})
		resultCh <- repeatedResult{allowed: allowed, reason: reason}
	}()
	waitForPermissionWaiter(t, broker, "repeated-1", func(pending *permissionWaiter) bool {
		return pending.eventObserved && pending.callbackObserved
	})
	broker.mu.Lock()
	pendingRequest := broker.pending["repeated-1"].request
	broker.mu.Unlock()
	if pendingRequest.Kind != engine.PermissionInteractionKindRepeatedTool ||
		pendingRequest.Source != "repeated_tool_guard" || pendingRequest.Attempt != 3 ||
		pendingRequest.Message != engine.RepeatedToolInteractionPromptMessage ||
		pendingRequest.SessionID != "session-1" || pendingRequest.ThreadID != "thread-1" ||
		pendingRequest.AgentID != "agent-1" {
		t.Fatalf("repeated request = %#v", pendingRequest)
	}
	if broker.resolve("repeated-1", ResolveInteractionRequest{
		Kind:         engine.PermissionInteractionKindRepeatedTool,
		RepeatedTool: &ResolveRepeatedToolResult{Outcome: "stop"},
	}) != interactionResolveAccepted {
		t.Fatal("repeated-tool resolution was rejected")
	}
	select {
	case result := <-resultCh:
		if result.allowed || result.reason != repeatedToolStopMessage {
			t.Fatalf("repeated stop result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("repeated prompt did not finish")
	}
}

func TestPermissionBrokerInteractionWaitsForBothObservations(t *testing.T) {
	broker := newPermissionBroker()
	request := permissionBrokerTestRequest("two-source-visible")
	broker.observeEvent(request, "turn-two-source")
	if _, visible := broker.interaction(request.ToolUseID); visible {
		t.Fatal("event-only interaction became visible before callback observation")
	}

	ready := make(chan InteractionSnapshot, 1)
	go func() {
		if interaction, ok := broker.awaitInteraction(
			context.Background(),
			request.ToolUseID,
		); ok {
			ready <- interaction
		}
		close(ready)
	}()
	select {
	case interaction := <-ready:
		t.Fatalf("event-only interaction became ready: %#v", interaction)
	case <-time.After(20 * time.Millisecond):
	}

	broker.prepare(request)
	select {
	case interaction := <-ready:
		if interaction.RequestID != request.ToolUseID ||
			interaction.Kind != engine.PermissionInteractionKindPermission {
			t.Fatalf("ready interaction = %#v", interaction)
		}
	case <-time.After(time.Second):
		t.Fatal("two-source interaction did not become ready")
	}
}

func TestPermissionBrokerRepeatedToolAllowsContinueOnceAndRejectsOtherOutcomes(t *testing.T) {
	broker := newPermissionBroker()
	request := engine.PermissionPromptRequest{
		Kind: engine.PermissionInteractionKindRepeatedTool, Attempt: 3, Source: "repeated_tool_guard",
		ToolName: "Bash", ToolUseID: "repeated-continue", Message: engine.RepeatedToolInteractionPromptMessage,
		SessionID: "session-1", ThreadID: "thread-1", AgentID: "agent-1",
	}
	resultCh := waitForBrokerResult(context.Background(), broker, request)
	broker.observeEvent(request, "turn-repeated-continue")
	waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.eventObserved && waiter.callbackObserved
	})
	if status := broker.resolve(request.ToolUseID, ResolveInteractionRequest{
		Kind:         engine.PermissionInteractionKindRepeatedTool,
		RepeatedTool: &ResolveRepeatedToolResult{Outcome: "allow_session"},
	}); status != interactionResolveInvalid {
		t.Fatalf("invalid repeated outcome status = %v", status)
	}
	if status := broker.resolve(request.ToolUseID, ResolveInteractionRequest{
		Kind:         engine.PermissionInteractionKindRepeatedTool,
		RepeatedTool: &ResolveRepeatedToolResult{Outcome: "continue"},
	}); status != interactionResolveAccepted {
		t.Fatalf("continue status = %v", status)
	}
	if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionAllowOnce || result.Message != "" {
		t.Fatalf("continue result = %#v", result)
	}
}

func TestPermissionPromptRequestPreservesTypedIdentity(t *testing.T) {
	event := engine.PermissionRequestEvent{
		Kind: engine.PermissionInteractionKindQuestion, Attempt: 2, Source: "callback",
		ToolName: "AskUserQuestion", CanonicalToolName: "AskUserQuestion", ToolUseID: "question-1",
	}
	request := permissionPromptRequest("session-1", "thread-1", "agent-1", event)
	if request.Kind != engine.PermissionInteractionKindQuestion || request.Attempt != 2 || request.Source != "callback" ||
		request.SessionID != "session-1" || request.ThreadID != "thread-1" || request.AgentID != "agent-1" ||
		request.CanonicalToolName != "AskUserQuestion" || request.Presentation != nil {
		t.Fatalf("typed prompt request = %#v", request)
	}
}

func TestPermissionPromptRequestClonesPresentation(t *testing.T) {
	presentation := &engine.PermissionPresentation{
		Version:   1,
		ToolLabel: "Write",
		Summary:   "Allow this tool action?",
		Evidence: []engine.PermissionPresentationEvidence{{
			Label: "Access",
			Value: "May change data",
		}},
		GrantScopes: []engine.PermissionInteractionDecision{
			engine.PermissionAllowOnce,
			engine.PermissionAllowSession,
			engine.PermissionAllowAlways,
		},
	}
	event := engine.PermissionRequestEvent{
		Kind: engine.PermissionInteractionKindPermission, Source: "coordinator",
		ToolName: "write_alias", CanonicalToolName: "Write", ToolUseID: "permission-1",
		Presentation: presentation,
	}
	request := permissionPromptRequest("session-1", "thread-1", "agent-1", event)
	if request.Presentation == nil || request.Presentation == presentation {
		t.Fatalf("presentation clone = %#v", request.Presentation)
	}
	request.Presentation.Evidence[0].Value = "mutated"
	request.Presentation.GrantScopes[0] = engine.PermissionDeny
	if presentation.Evidence[0].Value != "May change data" ||
		presentation.GrantScopes[0] != engine.PermissionAllowOnce {
		t.Fatalf("event presentation was aliased: %#v", presentation)
	}
}

func TestPermissionBrokerEventFirst(t *testing.T) {
	broker := newPermissionBroker()
	request := permissionBrokerTestRequest("event-first")
	broker.observeEvent(request, "turn-event-first")

	resultCh := waitForBrokerResult(context.Background(), broker, request)
	waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.eventObserved && waiter.callbackObserved && waiter.turnID == "turn-event-first"
	})
	if broker.resolve(request.ToolUseID, permissionResolution(engine.PermissionAllowOnce)) != interactionResolveAccepted {
		t.Fatal("event-first did not settle")
	}
	if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionAllowOnce {
		t.Fatalf("result = %#v", result)
	}
}

func TestPermissionBrokerCallbackFirst(t *testing.T) {
	broker := newPermissionBroker()
	request := permissionBrokerTestRequest("callback-first")
	resultCh := waitForBrokerResult(context.Background(), broker, request)
	waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.callbackObserved && !waiter.eventObserved
	})
	if broker.resolve(request.ToolUseID, permissionResolution(engine.PermissionAllowOnce)) != interactionResolveNotFound {
		t.Fatal("callback-only request settled before event identity arrived")
	}

	broker.observeEvent(request, "turn-callback-first")
	if broker.resolve(request.ToolUseID, permissionResolution(engine.PermissionAllowSession)) != interactionResolveAccepted {
		t.Fatal("callback-first did not settle")
	}
	if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionAllowSession {
		t.Fatalf("result = %#v", result)
	}
}

func TestPermissionBrokerConflict(t *testing.T) {
	broker := newPermissionBroker()
	request := permissionBrokerTestRequest("conflict")
	resultCh := waitForBrokerResult(context.Background(), broker, request)
	waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.callbackObserved
	})
	broker.mu.Lock()
	frozen := broker.pending[request.ToolUseID]
	broker.mu.Unlock()

	conflict := clonePromptRequest(request)
	conflict.Input["path"] = "different"
	broker.observeEvent(conflict, "turn-conflict")
	if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionCancelled {
		t.Fatalf("conflict result = %#v", result)
	}
	if frozen == nil || frozen.request.Input["path"] != "file.txt" ||
		frozen.request.ToolName != "write_alias" {
		t.Fatalf("frozen request was overwritten: %#v", frozen)
	}
	if broker.resolve(request.ToolUseID, permissionResolution(engine.PermissionAllowOnce)) != interactionResolveNotFound {
		t.Fatal("conflict resolved")
	}
	broker.observeEvent(request, "turn-conflict")
	broker.prepare(request)
	assertPermissionRetired(t, broker, request.ToolUseID)
}

func TestPermissionBrokerEmptyTurnID(t *testing.T) {
	broker := newPermissionBroker()
	request := permissionBrokerTestRequest("empty-turn")
	resultCh := waitForBrokerResult(context.Background(), broker, request)
	waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.callbackObserved
	})

	broker.observeEvent(request, "")
	if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionCancelled {
		t.Fatalf("empty-turn result = %#v", result)
	}
	broker.observeEvent(request, "late-valid-turn")
	broker.prepare(request)
	if broker.resolve(request.ToolUseID, permissionResolution(engine.PermissionAllowOnce)) != interactionResolveNotFound {
		t.Fatal("empty turn revived")
	}
	assertPermissionRetired(t, broker, request.ToolUseID)
}

func TestPermissionBrokerDuplicateEqualObservation(t *testing.T) {
	broker := newPermissionBroker()
	request := permissionBrokerTestRequest("duplicate")
	broker.observeEvent(request, "turn-duplicate")
	broker.observeEvent(request, "turn-duplicate")
	first := waitForBrokerResult(context.Background(), broker, request)
	second := waitForBrokerResult(context.Background(), broker, request)
	waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.eventObserved && waiter.callbackObserved
	})

	if broker.resolve(request.ToolUseID, permissionResolution(engine.PermissionAllowOnce)) != interactionResolveAccepted {
		t.Fatal("equal duplicate did not remain idempotent")
	}
	for index, resultCh := range []<-chan engine.PermissionInteractionResult{first, second} {
		if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionAllowOnce {
			t.Fatalf("result %d = %#v", index, result)
		}
	}
	if broker.resolve(request.ToolUseID, permissionResolution(engine.PermissionDeny)) != interactionResolveNotFound {
		t.Fatal("duplicate settlement succeeded")
	}
}

func TestPermissionBrokerLateSourceCannotReviveCancelledWaiter(t *testing.T) {
	broker := newPermissionBroker()
	request := permissionBrokerTestRequest("late")
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := waitForBrokerResult(ctx, broker, request)
	waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.callbackObserved
	})
	cancel()
	if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionCancelled {
		t.Fatalf("cancel result = %#v", result)
	}

	broker.observeEvent(request, "late-turn")
	broker.prepare(request)
	if broker.resolve(request.ToolUseID, permissionResolution(engine.PermissionAllowOnce)) != interactionResolveNotFound {
		t.Fatal("late source revived a cancelled waiter")
	}
	assertPermissionRetired(t, broker, request.ToolUseID)
}

func TestPermissionBrokerAcceptedResolveWinsBeforeCancellation(t *testing.T) {
	broker := newPermissionBroker()
	request := permissionBrokerTestRequest("resolve-before-cancel")
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := waitForBrokerResult(ctx, broker, request)
	broker.observeEvent(request, "turn-resolve-before-cancel")
	waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.eventObserved && waiter.callbackObserved
	})

	if broker.resolve(request.ToolUseID, permissionResolution(engine.PermissionAllowAlways)) != interactionResolveAccepted {
		t.Fatal("resolution was not accepted")
	}
	cancel()
	if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionAllowAlways {
		t.Fatalf("accepted result was lost to cancellation: %#v", result)
	}
}

func TestPermissionBrokerSecondTurnIDConflicts(t *testing.T) {
	broker := newPermissionBroker()
	request := permissionBrokerTestRequest("turn-conflict")
	broker.observeEvent(request, "turn-1")
	resultCh := waitForBrokerResult(context.Background(), broker, request)
	waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.eventObserved && waiter.callbackObserved
	})

	broker.observeEvent(request, "turn-2")
	if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionCancelled {
		t.Fatalf("turn conflict result = %#v", result)
	}
	assertPermissionRetired(t, broker, request.ToolUseID)
}

func TestPermissionBrokerCloseRetiresWaiter(t *testing.T) {
	broker := newPermissionBroker()
	request := permissionBrokerTestRequest("close")
	resultCh := waitForBrokerResult(context.Background(), broker, request)
	waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.callbackObserved
	})

	broker.close()
	if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionCancelled {
		t.Fatalf("close result = %#v", result)
	}
	broker.observeEvent(request, "late-after-close")
	broker.prepare(request)
	if broker.resolve(request.ToolUseID, permissionResolution(engine.PermissionAllowOnce)) != interactionResolveNotFound {
		t.Fatal("closed broker accepted a late resolution")
	}
	assertPermissionRetired(t, broker, request.ToolUseID)
}

func TestPermissionBrokerUnavailablePresentationAllowsOnceOnly(t *testing.T) {
	broker := newPermissionBroker()
	request := permissionBrokerTestRequest("unavailable-scope")
	request.Presentation = &engine.PermissionPresentation{
		Version:     1,
		Unavailable: true,
		GrantScopes: []engine.PermissionInteractionDecision{engine.PermissionAllowOnce},
	}
	broker.observeEvent(request, "turn-unavailable")
	resultCh := waitForBrokerResult(context.Background(), broker, request)
	waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.eventObserved && waiter.callbackObserved
	})

	for _, forbidden := range []engine.PermissionInteractionDecision{
		engine.PermissionAllowSession,
		engine.PermissionAllowAlways,
	} {
		if broker.resolve(request.ToolUseID, permissionResolution(forbidden)) != interactionResolveInvalid {
			t.Fatalf("unavailable presentation accepted %q", forbidden)
		}
	}
	waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return !waiter.settled
	})
	if broker.resolve(request.ToolUseID, permissionResolution(engine.PermissionAllowOnce)) != interactionResolveAccepted {
		t.Fatal("unavailable presentation rejected allow_once")
	}
	if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionAllowOnce {
		t.Fatalf("result = %#v", result)
	}
}

func TestPermissionBrokerPermissionRejectsTamperedFieldsWithoutSettlement(t *testing.T) {
	broker := newPermissionBroker()
	request := permissionBrokerTestRequest("permission-tampered")
	resultCh := waitForBrokerResult(context.Background(), broker, request)
	broker.observeEvent(request, "turn-permission-tampered")
	waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.eventObserved && waiter.callbackObserved
	})

	invalid := []ResolveInteractionRequest{
		{Kind: engine.PermissionInteractionKindPermission, Permission: &ResolvePermissionResult{Decision: string(engine.PermissionAllowOnce), Message: "allowed decisions carry no message"}},
		{Kind: engine.PermissionInteractionKindPermission, Permission: &ResolvePermissionResult{Decision: "unknown"}},
		{Kind: engine.PermissionInteractionKindPermission, Permission: &ResolvePermissionResult{Decision: string(engine.PermissionDeny), Message: strings.Repeat("x", maxInteractionMessageBytes+1)}},
		{
			Kind:         engine.PermissionInteractionKindPermission,
			Permission:   &ResolvePermissionResult{Decision: string(engine.PermissionDeny)},
			RepeatedTool: &ResolveRepeatedToolResult{Outcome: "stop"},
		},
	}
	for index, input := range invalid {
		if status := broker.resolve(request.ToolUseID, input); status != interactionResolveInvalid {
			t.Fatalf("invalid permission %d status = %v", index, status)
		}
	}
	if status := broker.resolve(request.ToolUseID, ResolveInteractionRequest{
		Kind:       engine.PermissionInteractionKindPermission,
		Permission: &ResolvePermissionResult{Decision: string(engine.PermissionDeny), Message: "  not now  "},
	}); status != interactionResolveAccepted {
		t.Fatalf("bounded denial status = %v", status)
	}
	if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionDeny || result.Message != "not now" {
		t.Fatalf("denial result = %#v", result)
	}
}

func TestPermissionBrokerQuestionReconstructsOriginalAnswers(t *testing.T) {
	broker := newPermissionBroker()
	request := questionBrokerTestRequest("question-submit")
	resultCh := waitForBrokerResult(context.Background(), broker, request)
	broker.observeEvent(request, "turn-question-submit")
	waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.eventObserved && waiter.callbackObserved
	})

	status := broker.resolve(request.ToolUseID, ResolveInteractionRequest{
		Kind: engine.PermissionInteractionKindQuestion,
		Question: &ResolveQuestionResult{
			Outcome: "submit",
			Answers: []QuestionAnswerResult{
				{QuestionID: "q-1", Text: "free answer"},
				{QuestionID: "q-2", OptionIDs: []string{"q-2-o-2", "q-2-o-1"}, Text: "Other answer"},
			},
		},
	})
	if status != interactionResolveAccepted {
		t.Fatalf("question resolve status = %v", status)
	}
	result := receiveBrokerResult(t, resultCh)
	if result.Decision != engine.PermissionAllowOnce {
		t.Fatalf("question decision = %#v", result)
	}
	questions, ok := result.UpdatedInput["questions"].([]tools.UserQuestion)
	if !ok || len(questions) != 2 || questions[0].Question != "Free question" || questions[1].Options[0].Label != "Alpha" {
		t.Fatalf("reconstructed questions = %#v", result.UpdatedInput["questions"])
	}
	answers, ok := result.UpdatedInput["answers"].(map[string]string)
	if !ok || answers["Free question"] != "free answer" ||
		answers["Choose several"] != "Alpha, Beta, Other answer" {
		t.Fatalf("reconstructed answers = %#v", result.UpdatedInput["answers"])
	}
}

func TestPermissionBrokerQuestionRejectsInvalidWireAnswersWithoutSettlement(t *testing.T) {
	tests := []struct {
		name   string
		result ResolveQuestionResult
	}{
		{name: "missing answer", result: ResolveQuestionResult{Outcome: "submit", Answers: []QuestionAnswerResult{{QuestionID: "q-1", Text: "answer"}}}},
		{name: "out of order", result: ResolveQuestionResult{Outcome: "submit", Answers: []QuestionAnswerResult{{QuestionID: "q-2", Text: "answer"}, {QuestionID: "q-1", OptionIDs: []string{"q-2-o-1"}}}}},
		{name: "unknown option", result: ResolveQuestionResult{Outcome: "submit", Answers: []QuestionAnswerResult{{QuestionID: "q-1", Text: "answer"}, {QuestionID: "q-2", OptionIDs: []string{"q-2-o-9"}}}}},
		{name: "duplicate option", result: ResolveQuestionResult{Outcome: "submit", Answers: []QuestionAnswerResult{{QuestionID: "q-1", Text: "answer"}, {QuestionID: "q-2", OptionIDs: []string{"q-2-o-1", "q-2-o-1"}}}}},
		{name: "blank free text", result: ResolveQuestionResult{Outcome: "submit", Answers: []QuestionAnswerResult{{QuestionID: "q-1", Text: "  "}, {QuestionID: "q-2", OptionIDs: []string{"q-2-o-1"}}}}},
		{name: "answer too large", result: ResolveQuestionResult{Outcome: "submit", Answers: []QuestionAnswerResult{{QuestionID: "q-1", Text: strings.Repeat("x", maxInteractionAnswerBytes+1)}, {QuestionID: "q-2", OptionIDs: []string{"q-2-o-1"}}}}},
		{name: "discuss with answers", result: ResolveQuestionResult{Outcome: "discuss", Answers: []QuestionAnswerResult{{QuestionID: "q-1", Text: "answer"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broker := newPermissionBroker()
			request := questionBrokerTestRequest("question-invalid")
			resultCh := waitForBrokerResult(context.Background(), broker, request)
			broker.observeEvent(request, "turn-question-invalid")
			waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
				return waiter.eventObserved && waiter.callbackObserved
			})
			if status := broker.resolve(request.ToolUseID, ResolveInteractionRequest{Kind: engine.PermissionInteractionKindQuestion, Question: &test.result}); status != interactionResolveInvalid {
				t.Fatalf("invalid question status = %v", status)
			}
			waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
				return !waiter.settled
			})
			if status := broker.resolve(request.ToolUseID, ResolveInteractionRequest{Kind: engine.PermissionInteractionKindQuestion, Question: &ResolveQuestionResult{Outcome: "cancel"}}); status != interactionResolveAccepted {
				t.Fatalf("cancel after invalid result = %v", status)
			}
			if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionCancelled {
				t.Fatalf("cancel result = %#v", result)
			}
		})
	}
}

func TestPermissionBrokerQuestionEnforcesSingleChoiceCardinality(t *testing.T) {
	tests := []struct {
		name       string
		answer     QuestionAnswerResult
		wantValid  bool
		wantAnswer string
	}{
		{name: "known option", answer: QuestionAnswerResult{QuestionID: "q-1", OptionIDs: []string{"q-1-o-2"}}, wantValid: true, wantAnswer: "Beta"},
		{name: "other text", answer: QuestionAnswerResult{QuestionID: "q-1", Text: "Another choice"}, wantValid: true, wantAnswer: "Another choice"},
		{name: "option plus other", answer: QuestionAnswerResult{QuestionID: "q-1", OptionIDs: []string{"q-1-o-1"}, Text: "Another"}},
		{name: "two options", answer: QuestionAnswerResult{QuestionID: "q-1", OptionIDs: []string{"q-1-o-1", "q-1-o-2"}}},
		{name: "empty", answer: QuestionAnswerResult{QuestionID: "q-1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broker := newPermissionBroker()
			request := singleQuestionBrokerTestRequest("question-single")
			resultCh := waitForBrokerResult(context.Background(), broker, request)
			broker.observeEvent(request, "turn-question-single")
			waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
				return waiter.eventObserved && waiter.callbackObserved
			})
			status := broker.resolve(request.ToolUseID, ResolveInteractionRequest{
				Kind:     engine.PermissionInteractionKindQuestion,
				Question: &ResolveQuestionResult{Outcome: "submit", Answers: []QuestionAnswerResult{test.answer}},
			})
			if test.wantValid {
				if status != interactionResolveAccepted {
					t.Fatalf("valid single choice status = %v", status)
				}
				result := receiveBrokerResult(t, resultCh)
				answers := result.UpdatedInput["answers"].(map[string]string)
				if answers["Choose one"] != test.wantAnswer {
					t.Fatalf("single answer = %#v", answers)
				}
				return
			}
			if status != interactionResolveInvalid {
				t.Fatalf("invalid single choice status = %v", status)
			}
			if status := broker.resolve(request.ToolUseID, ResolveInteractionRequest{
				Kind:     engine.PermissionInteractionKindQuestion,
				Question: &ResolveQuestionResult{Outcome: "cancel"},
			}); status != interactionResolveAccepted {
				t.Fatalf("cancel status = %v", status)
			}
			_ = receiveBrokerResult(t, resultCh)
		})
	}
}

func TestPermissionBrokerQuestionEnforcesAggregateAnswerBound(t *testing.T) {
	broker := newPermissionBroker()
	request := engine.PermissionPromptRequest{
		Kind: engine.PermissionInteractionKindQuestion, Source: "callback",
		ToolName: "AskUserQuestion", ToolUseID: "question-aggregate",
		SessionID: "session-1", ThreadID: "thread-1",
		Input: map[string]any{"questions": []tools.UserQuestion{
			{Question: "Question one"}, {Question: "Question two"}, {Question: "Question three"},
		}},
	}
	resultCh := waitForBrokerResult(context.Background(), broker, request)
	broker.observeEvent(request, "turn-question-aggregate")
	waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.eventObserved && waiter.callbackObserved
	})
	text := strings.Repeat("x", 12<<10)
	if status := broker.resolve(request.ToolUseID, ResolveInteractionRequest{
		Kind: engine.PermissionInteractionKindQuestion,
		Question: &ResolveQuestionResult{Outcome: "submit", Answers: []QuestionAnswerResult{
			{QuestionID: "q-1", Text: text}, {QuestionID: "q-2", Text: text}, {QuestionID: "q-3", Text: text},
		}},
	}); status != interactionResolveInvalid {
		t.Fatalf("aggregate overflow status = %v", status)
	}
	if status := broker.resolve(request.ToolUseID, ResolveInteractionRequest{
		Kind:     engine.PermissionInteractionKindQuestion,
		Question: &ResolveQuestionResult{Outcome: "cancel"},
	}); status != interactionResolveAccepted {
		t.Fatalf("cancel status = %v", status)
	}
	_ = receiveBrokerResult(t, resultCh)
}

func TestPermissionBrokerQuestionDiscussAndCancelAreNonGranting(t *testing.T) {
	tests := []struct {
		outcome string
		want    engine.PermissionInteractionDecision
	}{
		{outcome: "discuss", want: engine.PermissionDeny},
		{outcome: "cancel", want: engine.PermissionCancelled},
	}
	for _, test := range tests {
		t.Run(test.outcome, func(t *testing.T) {
			broker := newPermissionBroker()
			request := questionBrokerTestRequest("question-" + test.outcome)
			resultCh := waitForBrokerResult(context.Background(), broker, request)
			broker.observeEvent(request, "turn-question-"+test.outcome)
			waitForPermissionWaiter(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
				return waiter.eventObserved && waiter.callbackObserved
			})
			if status := broker.resolve(request.ToolUseID, ResolveInteractionRequest{
				Kind:     engine.PermissionInteractionKindQuestion,
				Question: &ResolveQuestionResult{Outcome: test.outcome},
			}); status != interactionResolveAccepted {
				t.Fatalf("%s status = %v", test.outcome, status)
			}
			result := receiveBrokerResult(t, resultCh)
			if result.Decision != test.want || result.UpdatedInput != nil || result.Allowed() {
				t.Fatalf("%s result = %#v", test.outcome, result)
			}
		})
	}
}

func TestPermissionBrokerInvalidFrozenQuestionFailsClosed(t *testing.T) {
	broker := newPermissionBroker()
	request := questionBrokerTestRequest("question-invalid-frozen")
	request.Input = map[string]any{"questions": []tools.UserQuestion{
		{Question: "One"}, {Question: "Two"}, {Question: "Three"}, {Question: "Four"}, {Question: "Five"},
	}}
	resultCh := waitForBrokerResult(context.Background(), broker, request)
	broker.observeEvent(request, "turn-question-invalid-frozen")
	if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionCancelled {
		t.Fatalf("invalid frozen question result = %#v", result)
	}
	assertPermissionRetired(t, broker, request.ToolUseID)
	if _, ok := broker.interaction(request.ToolUseID); ok {
		t.Fatal("invalid frozen question remained projectable")
	}
}

func TestPermissionBrokerPlanApprovalRequiresDeliveredReviewIdentity(t *testing.T) {
	broker := newPermissionBroker()
	request := planBrokerTestRequest("plan-approve")
	resultCh := waitForBrokerResult(context.Background(), broker, request)
	broker.observeEvent(request, "turn-plan-approve")
	waiter := waitForPermissionWaiterValue(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.eventObserved && waiter.callbackObserved
	})
	if !broker.recordPlanReview(request.ToolUseID, waiter, 7, request.PlanApproval.InitialPlanDigest) {
		t.Fatal("Plan review identity was not recorded")
	}
	status := broker.resolve(request.ToolUseID, ResolveInteractionRequest{
		Kind: engine.PermissionInteractionKindPlanApproval,
		PlanApproval: &ResolvePlanApprovalResult{
			Outcome: "approve", Revision: 7, TargetMode: string(permission.ModeDefault),
			ReviewedDigest: request.PlanApproval.InitialPlanDigest,
		},
	})
	if status != interactionResolveAccepted {
		t.Fatalf("Plan resolve status = %v", status)
	}
	result := receiveBrokerResult(t, resultCh)
	if result.Decision != engine.PermissionAllowOnce || result.PlanApproval == nil ||
		result.PlanApproval.RequestID != request.ToolUseID || result.PlanApproval.PlanRevision != 7 ||
		result.PlanApproval.Outcome != engine.PlanApprovalApprove ||
		result.PlanApproval.ReviewedPlanDigest != request.PlanApproval.InitialPlanDigest ||
		result.PlanApproval.TargetMode != permission.ModeDefault {
		t.Fatalf("Plan result = %#v", result)
	}
}

func TestPermissionBrokerPlanRejectsTamperedResultsWithoutSettlement(t *testing.T) {
	tests := []struct {
		name     string
		noReview bool
		result   ResolvePlanApprovalResult
	}{
		{name: "review not delivered", noReview: true, result: validPlanResolveResult()},
		{name: "revision changed", result: func() ResolvePlanApprovalResult { value := validPlanResolveResult(); value.Revision++; return value }()},
		{name: "digest changed", result: func() ResolvePlanApprovalResult {
			value := validPlanResolveResult()
			value.ReviewedDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
			return value
		}()},
		{name: "target unknown", result: func() ResolvePlanApprovalResult {
			value := validPlanResolveResult()
			value.TargetMode = "unknown"
			return value
		}()},
		{name: "bypass unconfirmed", result: func() ResolvePlanApprovalResult {
			value := validPlanResolveResult()
			value.TargetMode = string(permission.ModeBypassPermissions)
			return value
		}()},
		{name: "default spuriously confirmed", result: func() ResolvePlanApprovalResult {
			value := validPlanResolveResult()
			value.Confirmed = true
			return value
		}()},
		{name: "revision missing feedback", result: func() ResolvePlanApprovalResult {
			value := validPlanResolveResult()
			value.Outcome = "revise"
			return value
		}()},
		{name: "approval carries feedback", result: func() ResolvePlanApprovalResult {
			value := validPlanResolveResult()
			value.Feedback = "not allowed"
			return value
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broker := newPermissionBroker()
			request := planBrokerTestRequest("plan-invalid")
			resultCh := waitForBrokerResult(context.Background(), broker, request)
			broker.observeEvent(request, "turn-plan-invalid")
			waiter := waitForPermissionWaiterValue(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
				return waiter.eventObserved && waiter.callbackObserved
			})
			if !test.noReview && !broker.recordPlanReview(request.ToolUseID, waiter, 7, request.PlanApproval.InitialPlanDigest) {
				t.Fatal("record Plan review")
			}
			if status := broker.resolve(request.ToolUseID, ResolveInteractionRequest{Kind: engine.PermissionInteractionKindPlanApproval, PlanApproval: &test.result}); status != interactionResolveInvalid {
				t.Fatalf("invalid Plan status = %v", status)
			}
			if test.noReview && !broker.recordPlanReview(request.ToolUseID, waiter, 7, request.PlanApproval.InitialPlanDigest) {
				t.Fatal("record Plan review after invalid result")
			}
			cancel := validPlanResolveResult()
			cancel.Outcome = "cancel"
			if status := broker.resolve(request.ToolUseID, ResolveInteractionRequest{Kind: engine.PermissionInteractionKindPlanApproval, PlanApproval: &cancel}); status != interactionResolveAccepted {
				t.Fatalf("cancel after invalid Plan result = %v", status)
			}
			if result := receiveBrokerResult(t, resultCh); result.Decision != engine.PermissionDeny ||
				result.PlanApproval == nil || result.PlanApproval.Outcome != engine.PlanApprovalCancel {
				t.Fatalf("Plan cancel result = %#v", result)
			}
		})
	}
}

func TestPermissionBrokerPlanAcceptsConfirmedBypassAndRevisionFeedback(t *testing.T) {
	tests := []struct {
		name    string
		result  ResolvePlanApprovalResult
		outcome engine.PlanApprovalOutcome
		allowed bool
	}{
		{
			name: "confirmed bypass",
			result: ResolvePlanApprovalResult{
				Outcome: "approve", Revision: 7, TargetMode: string(permission.ModeBypassPermissions),
				ReviewedDigest: planBrokerDigest, Confirmed: true,
			},
			outcome: engine.PlanApprovalApprove, allowed: true,
		},
		{
			name: "revision feedback",
			result: ResolvePlanApprovalResult{
				Outcome: "revise", Revision: 7, TargetMode: string(permission.ModeDefault),
				ReviewedDigest: planBrokerDigest, Feedback: "Please change the rollout order.",
			},
			outcome: engine.PlanApprovalRevise,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broker := newPermissionBroker()
			request := planBrokerTestRequest("plan-valid")
			resultCh := waitForBrokerResult(context.Background(), broker, request)
			broker.observeEvent(request, "turn-plan-valid")
			waiter := waitForPermissionWaiterValue(t, broker, request.ToolUseID, func(waiter *permissionWaiter) bool {
				return waiter.eventObserved && waiter.callbackObserved
			})
			if !broker.recordPlanReview(request.ToolUseID, waiter, 7, planBrokerDigest) {
				t.Fatal("record Plan review")
			}
			if status := broker.resolve(request.ToolUseID, ResolveInteractionRequest{Kind: engine.PermissionInteractionKindPlanApproval, PlanApproval: &test.result}); status != interactionResolveAccepted {
				t.Fatalf("valid Plan status = %v", status)
			}
			result := receiveBrokerResult(t, resultCh)
			if result.Allowed() != test.allowed || result.PlanApproval == nil || result.PlanApproval.Outcome != test.outcome {
				t.Fatalf("Plan result = %#v", result)
			}
		})
	}
}

func permissionBrokerTestRequest(id string) engine.PermissionPromptRequest {
	return engine.PermissionPromptRequest{
		Kind:              engine.PermissionInteractionKindPermission,
		Source:            "coordinator",
		ToolName:          "write_alias",
		CanonicalToolName: "Write",
		ToolUseID:         id,
		Input:             map[string]any{"path": "file.txt"},
		Message:           "permission message",
		SessionID:         "session-1",
		ThreadID:          "thread-1",
		AgentID:           "agent-1",
		Presentation: &engine.PermissionPresentation{
			Version:   1,
			ToolLabel: "Write",
			Summary:   "Allow this tool action?",
			Evidence: []engine.PermissionPresentationEvidence{{
				Label: "Access",
				Value: "May change data",
			}},
			GrantScopes: []engine.PermissionInteractionDecision{
				engine.PermissionAllowOnce,
				engine.PermissionAllowSession,
				engine.PermissionAllowAlways,
			},
		},
	}
}

func questionBrokerTestRequest(id string) engine.PermissionPromptRequest {
	return engine.PermissionPromptRequest{
		Kind:              engine.PermissionInteractionKindQuestion,
		Source:            "callback",
		ToolName:          "AskUserQuestion",
		CanonicalToolName: "AskUserQuestion",
		ToolUseID:         id,
		SessionID:         "session-1",
		ThreadID:          "thread-1",
		AgentID:           "agent-1",
		Input: map[string]any{"questions": []tools.UserQuestion{
			{Question: "Free question"},
			{
				Question: "Choose several", MultiSelect: true,
				Options: []tools.QuestionOption{
					{Label: "Alpha", Description: "First"},
					{Label: "Beta", Description: "Second"},
				},
			},
		}},
	}
}

func singleQuestionBrokerTestRequest(id string) engine.PermissionPromptRequest {
	return engine.PermissionPromptRequest{
		Kind: engine.PermissionInteractionKindQuestion, Source: "callback",
		ToolName: "AskUserQuestion", ToolUseID: id,
		SessionID: "session-1", ThreadID: "thread-1",
		Input: map[string]any{"questions": []tools.UserQuestion{{
			Question: "Choose one",
			Options:  []tools.QuestionOption{{Label: "Alpha"}, {Label: "Beta"}},
		}}},
	}
}

const planBrokerDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func planBrokerTestRequest(id string) engine.PermissionPromptRequest {
	return engine.PermissionPromptRequest{
		Kind:      engine.PermissionInteractionKindPlanApproval,
		Source:    "project_graph",
		ToolName:  "ExitPlanMode",
		ToolUseID: id,
		SessionID: "session-1",
		ThreadID:  "thread-1",
		AgentID:   "agent-1",
		PlanApproval: &engine.PlanApprovalRequest{
			RequestID: id, PlanRevision: 7, PlanFileIdentity: "/private/plan.md",
			InitialPlanDigest: planBrokerDigest, ReturnMode: permission.ModeDefault,
		},
	}
}

func validPlanResolveResult() ResolvePlanApprovalResult {
	return ResolvePlanApprovalResult{
		Outcome: "approve", Revision: 7, TargetMode: string(permission.ModeDefault),
		ReviewedDigest: planBrokerDigest,
	}
}

func permissionResolution(decision engine.PermissionInteractionDecision) ResolveInteractionRequest {
	return ResolveInteractionRequest{
		Kind:       engine.PermissionInteractionKindPermission,
		Permission: &ResolvePermissionResult{Decision: string(decision)},
	}
}

func waitForBrokerResult(
	ctx context.Context,
	broker *permissionBroker,
	request engine.PermissionPromptRequest,
) <-chan engine.PermissionInteractionResult {
	resultCh := make(chan engine.PermissionInteractionResult, 1)
	go func() {
		resultCh <- broker.wait(ctx, request)
	}()
	return resultCh
}

func waitForPermissionWaiter(
	t *testing.T,
	broker *permissionBroker,
	requestID string,
	ready func(*permissionWaiter) bool,
) {
	waitForPermissionWaiterValue(t, broker, requestID, ready)
}

func waitForPermissionWaiterValue(
	t *testing.T,
	broker *permissionBroker,
	requestID string,
	ready func(*permissionWaiter) bool,
) *permissionWaiter {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		broker.mu.Lock()
		waiter := broker.pending[requestID]
		isReady := waiter != nil && ready(waiter)
		broker.mu.Unlock()
		if isReady {
			return waiter
		}
		if time.Now().After(deadline) {
			t.Fatalf("permission waiter %q did not reach expected state", requestID)
		}
		time.Sleep(time.Millisecond)
	}
}

func receiveBrokerResult(
	t *testing.T,
	resultCh <-chan engine.PermissionInteractionResult,
) engine.PermissionInteractionResult {
	t.Helper()
	select {
	case result := <-resultCh:
		return result
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for permission result")
		return engine.PermissionInteractionResult{}
	}
}

func assertPermissionRetired(t *testing.T, broker *permissionBroker, requestID string) {
	t.Helper()
	broker.mu.Lock()
	_, pending := broker.pending[requestID]
	_, retired := broker.retired[requestID]
	broker.mu.Unlock()
	if pending || !retired {
		t.Fatalf("request %q pending=%v retired=%v", requestID, pending, retired)
	}
}
