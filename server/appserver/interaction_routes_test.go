package appserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/tools"
)

func TestServerProtocolV2PlanReviewExactLimit(t *testing.T) {
	server, httpServer, owned := newInteractionRouteTestSession(t)
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	content := strings.Repeat("p", 1<<20)
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write Plan: %v", err)
	}
	requestID := "plan-exact-limit"
	request := engine.PermissionPromptRequest{
		Kind:      engine.PermissionInteractionKindPlanApproval,
		Source:    "project_graph",
		ToolName:  "ExitPlanMode",
		ToolUseID: requestID,
		SessionID: owned.id,
		ThreadID:  owned.threadID,
		PlanApproval: &engine.PlanApprovalRequest{
			RequestID:         requestID,
			PlanRevision:      7,
			PlanFileIdentity:  path,
			InitialPlanDigest: engine.PlanBytesDigest([]byte(content)),
		},
	}
	owned.permissions.observeEvent(request, "turn-plan")
	owned.permissions.prepare(request)

	response := getBearer(
		t,
		httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/"+requestID+"/plan",
		"test-token",
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Plan review status = %d: %s", response.StatusCode, readBody(t, response))
	}
	var body struct {
		Content  string `json:"content"`
		Revision uint64 `json:"revision"`
		Digest   string `json:"digest"`
	}
	decodeResponse(t, response, &body)
	_ = response.Body.Close()
	if body.Content != content || body.Revision != 7 || body.Digest != engine.PlanBytesDigest([]byte(content)) {
		t.Fatalf("Plan review = revision %d digest %q content bytes %d", body.Revision, body.Digest, len(body.Content))
	}
}

func TestServerProtocolV2PlanReviewDigestAuthorizesExactApproval(t *testing.T) {
	server, httpServer, owned := newInteractionRouteTestSession(t)
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	content := []byte("# Reviewed Plan\n\nUse the exact returned digest.\n")
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write Plan: %v", err)
	}
	request := engine.PermissionPromptRequest{
		Kind: engine.PermissionInteractionKindPlanApproval, Source: "project_graph",
		ToolName: "ExitPlanMode", ToolUseID: "plan-http-approve",
		SessionID: owned.id, ThreadID: owned.threadID,
		PlanApproval: &engine.PlanApprovalRequest{
			RequestID: "plan-http-approve", PlanRevision: 9, PlanFileIdentity: path,
			InitialPlanDigest: engine.PlanBytesDigest(content),
		},
	}
	resultCh := waitForBrokerResult(context.Background(), owned.permissions, request)
	owned.permissions.observeEvent(request, "turn-plan-http")

	reviewResponse := getBearer(
		t,
		httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/"+request.ToolUseID+"/plan",
		"test-token",
	)
	if reviewResponse.StatusCode != http.StatusOK {
		t.Fatalf("Plan review status = %d: %s", reviewResponse.StatusCode, readBody(t, reviewResponse))
	}
	var review PlanReviewResponse
	decodeResponse(t, reviewResponse, &review)
	_ = reviewResponse.Body.Close()

	resolveResponse := doJSON(
		t,
		httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/"+request.ToolUseID+"/resolve",
		"test-token",
		http.MethodPost,
		ResolveInteractionRequest{Kind: engine.PermissionInteractionKindPlanApproval, PlanApproval: &ResolvePlanApprovalResult{
			Outcome: "approve", Revision: review.Revision, TargetMode: "default",
			ReviewedDigest: review.Digest,
		}},
	)
	if resolveResponse.StatusCode != http.StatusOK {
		t.Fatalf("Plan resolve status = %d: %s", resolveResponse.StatusCode, readBody(t, resolveResponse))
	}
	_ = resolveResponse.Body.Close()
	result := receiveBrokerResult(t, resultCh)
	if !result.Allowed() || result.PlanApproval == nil ||
		result.PlanApproval.ReviewedPlanDigest != review.Digest ||
		result.PlanApproval.PlanRevision != review.Revision {
		t.Fatalf("engine Plan result = %#v", result)
	}
}

func TestServerProtocolV2PlanReviewRejectsLimitPlusOne(t *testing.T) {
	server, httpServer, owned := newInteractionRouteTestSession(t)
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	content := strings.Repeat("x", (1<<20)+1)
	path := filepath.Join(t.TempDir(), "oversize-plan.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write Plan: %v", err)
	}
	seedPlanReviewRequest(owned, "plan-too-large", path, 3, engine.PlanBytesDigest([]byte(content)))

	response := getBearer(
		t,
		httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/plan-too-large/plan",
		"test-token",
	)
	assertInteractionAPIError(t, response, http.StatusRequestEntityTooLarge, "plan_review_too_large", path, content[:64])
	_ = response.Body.Close()
}

func TestServerProtocolV2PlanReviewUnavailableAndChanged(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		server, httpServer, owned := newInteractionRouteTestSession(t)
		defer httpServer.Close()
		defer shutdownTestServer(t, server)

		path := filepath.Join(t.TempDir(), "missing-plan.md")
		seedPlanReviewRequest(owned, "plan-missing", path, 4, engine.PlanBytesDigest([]byte("missing")))
		response := getBearer(
			t,
			httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/plan-missing/plan",
			"test-token",
		)
		assertInteractionAPIError(t, response, http.StatusConflict, "plan_review_unavailable", path)
		_ = response.Body.Close()
	})

	t.Run("bytes changed", func(t *testing.T) {
		server, httpServer, owned := newInteractionRouteTestSession(t)
		defer httpServer.Close()
		defer shutdownTestServer(t, server)

		path := filepath.Join(t.TempDir(), "changed-plan.md")
		initial := []byte("initial plan")
		if err := os.WriteFile(path, initial, 0o600); err != nil {
			t.Fatalf("write initial Plan: %v", err)
		}
		seedPlanReviewRequest(owned, "plan-changed", path, 5, engine.PlanBytesDigest(initial))
		if err := os.WriteFile(path, []byte("changed private plan content"), 0o600); err != nil {
			t.Fatalf("change Plan: %v", err)
		}
		response := getBearer(
			t,
			httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/plan-changed/plan",
			"test-token",
		)
		assertInteractionAPIError(t, response, http.StatusConflict, "plan_review_changed", path, "changed private plan content")
		_ = response.Body.Close()
	})
}

func TestServerProtocolV2PlanReviewRejectsNonPlanAndExpired(t *testing.T) {
	server, httpServer, owned := newInteractionRouteTestSession(t)
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	permissionRequest := engine.PermissionPromptRequest{
		Kind:      engine.PermissionInteractionKindPermission,
		Source:    "coordinator",
		ToolName:  "Write",
		ToolUseID: "permission-not-plan",
		SessionID: owned.id,
		ThreadID:  owned.threadID,
		Presentation: &engine.PermissionPresentation{
			Version: 1,
			GrantScopes: []engine.PermissionInteractionDecision{
				engine.PermissionAllowOnce,
			},
		},
	}
	owned.permissions.observeEvent(permissionRequest, "turn-permission")
	owned.permissions.prepare(permissionRequest)
	response := getBearer(
		t,
		httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/permission-not-plan/plan",
		"test-token",
	)
	assertInteractionAPIError(t, response, http.StatusConflict, "plan_review_unavailable")
	_ = response.Body.Close()

	seedPlanReviewRequest(owned, "plan-expired", filepath.Join(t.TempDir(), "plan.md"), 6, engine.PlanBytesDigest(nil))
	owned.permissions.fail("plan-expired")
	response = getBearer(
		t,
		httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/plan-expired/plan",
		"test-token",
	)
	assertInteractionAPIError(t, response, http.StatusNotFound, "interaction_not_found")
	_ = response.Body.Close()

	response = getBearer(
		t,
		httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/unknown/plan",
		"test-token",
	)
	assertInteractionAPIError(t, response, http.StatusNotFound, "interaction_not_found")
	_ = response.Body.Close()
}

func TestServerProtocolV2PlanReviewRejectsRendererSuppliedIdentity(t *testing.T) {
	server, httpServer, owned := newInteractionRouteTestSession(t)
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	content := []byte("request-owned Plan")
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write Plan: %v", err)
	}
	seedPlanReviewRequest(owned, "plan-no-renderer-identity", path, 2, engine.PlanBytesDigest(content))
	response := getBearer(
		t,
		httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/plan-no-renderer-identity/plan?path="+path,
		"test-token",
	)
	assertInteractionAPIError(t, response, http.StatusBadRequest, "invalid_request", path)
	_ = response.Body.Close()
}

func TestServerProtocolV2ResolveInteractionReplacesPermissionRoute(t *testing.T) {
	server, httpServer, owned := newInteractionRouteTestSession(t)
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	request := permissionBrokerTestRequest("permission-v2-route")
	request.SessionID = owned.id
	request.ThreadID = owned.threadID
	owned.permissions.observeEvent(request, "turn-permission-route")
	owned.permissions.prepare(request)

	legacy := doJSON(
		t,
		httpServer.URL+"/v1/sessions/"+owned.id+"/permissions/"+request.ToolUseID,
		"test-token",
		http.MethodPost,
		permissionResolution(engine.PermissionAllowOnce),
	)
	if legacy.StatusCode != http.StatusNotFound {
		t.Fatalf("legacy permission route status = %d: %s", legacy.StatusCode, readBody(t, legacy))
	}
	_ = legacy.Body.Close()

	resolved := doJSON(
		t,
		httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/"+request.ToolUseID+"/resolve",
		"test-token",
		http.MethodPost,
		permissionResolution(engine.PermissionAllowSession),
	)
	if resolved.StatusCode != http.StatusOK {
		t.Fatalf("resolve status = %d: %s", resolved.StatusCode, readBody(t, resolved))
	}
	var body ResolveInteractionResponse
	decodeResponse(t, resolved, &body)
	_ = resolved.Body.Close()
	if !body.Accepted {
		t.Fatalf("resolve response = %#v", body)
	}

	duplicate := doJSON(
		t,
		httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/"+request.ToolUseID+"/resolve",
		"test-token",
		http.MethodPost,
		permissionResolution(engine.PermissionAllowOnce),
	)
	assertInteractionAPIError(t, duplicate, http.StatusNotFound, "interaction_not_found")
	_ = duplicate.Body.Close()
}

func TestServerProtocolV2ResolveInteractionRejectsUnknownAndMismatchedBodies(t *testing.T) {
	server, httpServer, owned := newInteractionRouteTestSession(t)
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	request := permissionBrokerTestRequest("permission-strict-body")
	request.SessionID = owned.id
	request.ThreadID = owned.threadID
	owned.permissions.observeEvent(request, "turn-strict-body")
	owned.permissions.prepare(request)
	endpoint := httpServer.URL + "/v1/sessions/" + owned.id + "/interactions/" + request.ToolUseID + "/resolve"

	unknown := doRawInteractionJSON(
		t,
		endpoint,
		`{"kind":"permission","permission":{"decision":"allow_once","unknown":true}}`,
	)
	assertInteractionAPIError(t, unknown, http.StatusBadRequest, "invalid_request")
	_ = unknown.Body.Close()

	missingKind := doRawInteractionJSON(
		t,
		endpoint,
		`{"permission":{"decision":"allow_once"}}`,
	)
	assertInteractionAPIError(t, missingKind, http.StatusBadRequest, "invalid_interaction_result")
	_ = missingKind.Body.Close()

	mismatched := doRawInteractionJSON(
		t,
		endpoint,
		`{"kind":"question","permission":{"decision":"allow_once"}}`,
	)
	assertInteractionAPIError(t, mismatched, http.StatusBadRequest, "invalid_interaction_result")
	_ = mismatched.Body.Close()

	twoVariants := doRawInteractionJSON(
		t,
		endpoint,
		`{"kind":"permission","permission":{"decision":"allow_once"},`+
			`"repeated_tool":{"outcome":"continue"}}`,
	)
	assertInteractionAPIError(t, twoVariants, http.StatusBadRequest, "invalid_interaction_result")
	_ = twoVariants.Body.Close()

	duplicateVariant := doRawInteractionJSON(
		t,
		endpoint,
		`{"kind":"permission","permission":{"decision":"deny"},`+
			`"permission":{"decision":"allow_once"}}`,
	)
	assertInteractionAPIError(t, duplicateVariant, http.StatusBadRequest, "invalid_request")
	_ = duplicateVariant.Body.Close()

	valid := doJSON(
		t,
		endpoint,
		"test-token",
		http.MethodPost,
		permissionResolution(engine.PermissionAllowOnce),
	)
	if valid.StatusCode != http.StatusOK {
		t.Fatalf("valid resolve after rejections = %d: %s", valid.StatusCode, readBody(t, valid))
	}
	_ = valid.Body.Close()
}

func TestServerProtocolV2PublishesOnlyResolvableTwoSourceInteraction(t *testing.T) {
	server, httpServer, owned := newInteractionRouteTestSession(t)
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	eventRequest := engine.PermissionRequestEvent{
		Kind:              engine.PermissionInteractionKindPermission,
		Source:            "coordinator",
		ToolName:          "write_alias",
		CanonicalToolName: "Write",
		ToolUseID:         "permission-two-source-publication",
		Input:             map[string]any{"path": "file.txt"},
		Presentation:      permissionBrokerTestRequest("unused").Presentation,
	}
	prompt := permissionPromptRequest(
		owned.id,
		owned.threadID,
		owned.engine.AgentID(),
		eventRequest,
	)
	baseCursor := owned.events.latestCursor()
	events := make(chan engine.QueryEvent, 1)
	events <- engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
			SessionID: owned.id,
			ThreadID:  owned.threadID,
			TurnID:    "turn-two-source-publication",
		},
		Type:              engine.EventPermissionRequest,
		PermissionRequest: &eventRequest,
	}
	close(events)
	driven := make(chan error, 1)
	go func() {
		_, err := owned.driveEvents(
			context.Background(),
			"turn-two-source-publication",
			events,
			engine.Terminal{Reason: engine.TerminalCompleted},
		)
		driven <- err
	}()
	waitForPermissionWaiter(t, owned.permissions, prompt.ToolUseID, func(waiter *permissionWaiter) bool {
		return waiter.eventObserved && !waiter.callbackObserved
	})
	if cursor := owned.events.latestCursor(); cursor != baseCursor {
		t.Fatalf("event-only interaction was published at cursor %d, base %d", cursor, baseCursor)
	}

	owned.permissions.prepare(prompt)
	select {
	case err := <-driven:
		if err != nil {
			t.Fatalf("drive events: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("driveEvents did not publish after callback observation")
	}
	replay, _, unsubscribe, _, err := owned.events.subscribe(baseCursor)
	if err != nil {
		t.Fatalf("subscribe interaction replay: %v", err)
	}
	unsubscribe()
	interactionEvents := make([]WireEvent, 0, 1)
	activityEvents := make([]WireEvent, 0, 1)
	for _, event := range replay {
		switch event.Type {
		case "interaction_requested":
			interactionEvents = append(interactionEvents, event)
		case "activity":
			activityEvents = append(activityEvents, event)
		}
	}
	if len(interactionEvents) != 1 || len(activityEvents) != 1 ||
		strings.Contains(string(activityEvents[0].Data), eventRequest.ToolUseID) {
		t.Fatalf("published events = %#v", replay)
	}
	resolve := doJSON(
		t,
		httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/"+prompt.ToolUseID+"/resolve",
		"test-token",
		http.MethodPost,
		permissionResolution(engine.PermissionAllowOnce),
	)
	if resolve.StatusCode != http.StatusOK {
		t.Fatalf("first visible resolve = %d: %s", resolve.StatusCode, readBody(t, resolve))
	}
	_ = resolve.Body.Close()
}

func TestServerProtocolV2SnapshotProjectsFourInteractionsWithoutRawRequests(t *testing.T) {
	server, httpServer, owned := newInteractionRouteTestSession(t)
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	const secret = "RAW_INTERACTION_SECRET_8472"
	permissionRequest := permissionBrokerTestRequest("permission-snapshot")
	permissionRequest.SessionID = owned.id
	permissionRequest.ThreadID = owned.threadID
	permissionRequest.Input = map[string]any{"path": "/private/" + secret}
	permissionRequest.Message = secret
	seedInteractionWaiter(owned, permissionRequest, "turn-permission")

	questionRequest := engine.PermissionPromptRequest{
		Kind: engine.PermissionInteractionKindQuestion, Source: "callback",
		ToolName: "AskUserQuestion", CanonicalToolName: "AskUserQuestion",
		ToolUseID: "question-snapshot", SessionID: owned.id, ThreadID: owned.threadID,
		Message: secret,
		Input: map[string]any{
			"questions": []tools.UserQuestion{{
				Header: "Choice", Question: "Choose one",
				Options: []tools.QuestionOption{{Label: "One"}, {Label: "Two"}},
			}},
			"private": secret,
		},
	}
	seedInteractionWaiter(owned, questionRequest, "turn-question")

	planRequest := planBrokerTestRequest("plan-snapshot")
	planRequest.SessionID = owned.id
	planRequest.ThreadID = owned.threadID
	planRequest.PlanApproval.PlanFileIdentity = "/private/" + secret + "/plan.md"
	planRequest.Input = map[string]any{"private": secret}
	seedInteractionWaiter(owned, planRequest, "turn-plan")

	repeatedRequest := engine.PermissionPromptRequest{
		Kind: engine.PermissionInteractionKindRepeatedTool, Source: "repeated_tool_guard", Attempt: 3,
		ToolName: "Bash", ToolUseID: "repeated-snapshot", SessionID: owned.id, ThreadID: owned.threadID,
		Message: engine.RepeatedToolInteractionPromptMessage, Input: map[string]any{"command": secret},
	}
	seedInteractionWaiter(owned, repeatedRequest, "turn-repeated")

	response := getBearer(t, httpServer.URL+"/v1/sessions/"+owned.id+"/snapshot", "test-token")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status = %d: %s", response.StatusCode, readBody(t, response))
	}
	encoded, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	for _, forbidden := range []string{
		secret, `"permissions"`, `"input"`, `"plan_file_identity"`, `"initial_plan_digest"`,
		`"source"`, `"message"`,
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("snapshot leaked %q: %s", forbidden, encoded)
		}
	}
	var snapshot SessionSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(snapshot.Interactions) != 4 {
		t.Fatalf("interactions = %#v", snapshot.Interactions)
	}
	if snapshot.Interactions[0].Permission == nil || !snapshot.Interactions[0].Permission.Available ||
		snapshot.Interactions[1].Question == nil || snapshot.Interactions[1].Question.Questions[0].ID != "q-1" ||
		snapshot.Interactions[2].PlanApproval == nil || snapshot.Interactions[2].PlanApproval.Revision != 7 ||
		snapshot.Interactions[3].RepeatedTool == nil || snapshot.Interactions[3].RepeatedTool.Attempt != 3 {
		t.Fatalf("typed interactions = %#v", snapshot.Interactions)
	}
}

func TestServerProtocolV2McpAuthProjectsOrdinaryPermission(t *testing.T) {
	server, httpServer, owned := newInteractionRouteTestSession(t)
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	request := permissionBrokerTestRequest("mcp-auth-permission")
	request.SessionID = owned.id
	request.ThreadID = owned.threadID
	request.ToolName = "McpAuth"
	request.CanonicalToolName = "McpAuth"
	request.Presentation.ToolLabel = "McpAuth"
	seedInteractionWaiter(owned, request, "turn-mcp-auth")

	response := getBearer(t, httpServer.URL+"/v1/sessions/"+owned.id+"/snapshot", "test-token")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status = %d: %s", response.StatusCode, readBody(t, response))
	}
	var snapshot SessionSnapshot
	decodeResponse(t, response, &snapshot)
	_ = response.Body.Close()
	if len(snapshot.Interactions) != 1 ||
		snapshot.Interactions[0].Kind != engine.PermissionInteractionKindPermission ||
		snapshot.Interactions[0].Permission == nil ||
		snapshot.Interactions[0].Permission.ToolLabel != "McpAuth" ||
		snapshot.Interactions[0].Question != nil ||
		snapshot.Interactions[0].PlanApproval != nil ||
		snapshot.Interactions[0].RepeatedTool != nil {
		t.Fatalf("McpAuth interaction = %#v", snapshot.Interactions)
	}
}

func TestServerProtocolV2SSEPublishesOnlyTypedInteractionProjection(t *testing.T) {
	server, httpServer, owned := newInteractionRouteTestSession(t)
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	const secret = "RAW_SSE_SECRET_5931"
	cursor := owned.events.latestCursor()
	presentation := permissionBrokerTestRequest("unused").Presentation
	eventRequest := engine.PermissionRequestEvent{
		Kind: engine.PermissionInteractionKindPermission, Source: "coordinator",
		ToolName: "write_alias", CanonicalToolName: "Write", ToolUseID: "permission-sse",
		Input: map[string]any{"path": "/private/" + secret}, Message: secret, Presentation: presentation,
	}
	prompt := permissionPromptRequest(owned.id, owned.threadID, "agent-1", eventRequest)
	owned.permissions.observeEvent(prompt, "turn-sse")
	owned.permissions.prepare(prompt)
	owned.publishEngine(engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
			SessionID: owned.id, ThreadID: owned.threadID, TurnID: "turn-sse",
			Sequence: 1, Timestamp: time.Now().UTC(),
		},
		Type: engine.EventPermissionRequest, PermissionRequest: &eventRequest,
	}, "turn-sse")
	if status := owned.permissions.resolve("permission-sse", permissionResolution(engine.PermissionAllowOnce)); status != interactionResolveAccepted {
		t.Fatalf("resolve interaction for SSE = %v", status)
	}
	owned.publishEngine(engine.QueryEvent{
		RuntimeEventEnvelope: engine.RuntimeEventEnvelope{
			SessionID: owned.id, ThreadID: owned.threadID, TurnID: "turn-sse",
			Sequence: 2, Timestamp: time.Now().UTC(),
		},
		Type: engine.EventPermissionResolved,
		PermissionResolved: &engine.PermissionResolvedEvent{
			ToolUseID: "permission-sse", Decision: string(engine.PermissionAllowOnce),
			Reason: secret, Message: secret, Kind: engine.PermissionInteractionKindPermission,
		},
	}, "turn-sse")

	request, err := http.NewRequest(
		http.MethodGet,
		httpServer.URL+"/v1/sessions/"+owned.id+"/events?after="+strconv.FormatUint(cursor, 10),
		nil,
	)
	if err != nil {
		t.Fatalf("new events request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer test-token")
	ctx, cancel := context.WithCancel(request.Context())
	defer cancel()
	response, err := http.DefaultClient.Do(request.WithContext(ctx))
	if err != nil {
		t.Fatalf("events request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("events status = %d: %s", response.StatusCode, readBody(t, response))
	}
	scanner := bufio.NewScanner(response.Body)
	var events []WireEvent
	var activityEvents []WireEvent
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var wire WireEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &wire); err != nil {
			t.Fatalf("decode event: %v", err)
		}
		if wire.Type == "activity" {
			activityEvents = append(activityEvents, wire)
			continue
		}
		events = append(events, wire)
		if len(events) == 2 {
			break
		}
	}
	cancel()
	if len(events) != 2 {
		t.Fatalf("interaction events = %#v", events)
	}
	if len(activityEvents) == 0 || strings.Contains(string(activityEvents[0].Data), secret) ||
		strings.Contains(string(activityEvents[0].Data), "permission-sse") {
		t.Fatalf("semantic Activity events = %#v", activityEvents)
	}
	wire := events[0]
	if wire.ProtocolVersion != 2 || wire.Type != "interaction_requested" {
		t.Fatalf("wire event = %#v", wire)
	}
	encoded := string(wire.Data)
	for _, forbidden := range []string{secret, `"input"`, `"message"`, `"source"`, "permission_request"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("interaction event leaked %q: %s", forbidden, encoded)
		}
	}
	var interaction InteractionSnapshot
	if err := json.Unmarshal(wire.Data, &interaction); err != nil {
		t.Fatalf("decode interaction: %v", err)
	}
	if interaction.RequestID != "permission-sse" || interaction.TurnID != "turn-sse" ||
		interaction.Permission == nil || interaction.Permission.ToolLabel != "Write" {
		t.Fatalf("interaction = %#v", interaction)
	}
	resolved := events[1]
	if resolved.Type != "interaction_resolved" || string(resolved.Data) != `{"request_id":"permission-sse"}` {
		t.Fatalf("resolved event = %#v", resolved)
	}
	if strings.Contains(string(resolved.Data), secret) || strings.Contains(string(resolved.Data), "decision") ||
		strings.Contains(string(resolved.Data), "message") || strings.Contains(string(resolved.Data), "reason") {
		t.Fatalf("resolved event leaked internal result: %s", resolved.Data)
	}
}

func TestServerProtocolV2SnapshotDeduplicatesAndRemovesLostResolve(t *testing.T) {
	server, httpServer, owned := newInteractionRouteTestSession(t)
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	request := permissionBrokerTestRequest("permission-lost-response")
	request.SessionID = owned.id
	request.ThreadID = owned.threadID
	seedInteractionWaiter(owned, request, "turn-lost-response")
	seedInteractionWaiter(owned, request, "turn-lost-response")

	before := getBearer(t, httpServer.URL+"/v1/sessions/"+owned.id+"/snapshot", "test-token")
	var beforeSnapshot SessionSnapshot
	decodeResponse(t, before, &beforeSnapshot)
	_ = before.Body.Close()
	if len(beforeSnapshot.Interactions) != 1 || beforeSnapshot.Interactions[0].RequestID != request.ToolUseID {
		t.Fatalf("deduplicated interactions = %#v", beforeSnapshot.Interactions)
	}

	resolved := doJSON(
		t,
		httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/"+request.ToolUseID+"/resolve",
		"test-token",
		http.MethodPost,
		permissionResolution(engine.PermissionAllowOnce),
	)
	if resolved.StatusCode != http.StatusOK {
		t.Fatalf("resolve status = %d: %s", resolved.StatusCode, readBody(t, resolved))
	}
	_ = resolved.Body.Close()

	after := getBearer(t, httpServer.URL+"/v1/sessions/"+owned.id+"/snapshot", "test-token")
	var afterSnapshot SessionSnapshot
	decodeResponse(t, after, &afterSnapshot)
	_ = after.Body.Close()
	if len(afterSnapshot.Interactions) != 0 {
		t.Fatalf("resolved interaction survived snapshot = %#v", afterSnapshot.Interactions)
	}

	duplicate := doJSON(
		t,
		httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/"+request.ToolUseID+"/resolve",
		"test-token",
		http.MethodPost,
		permissionResolution(engine.PermissionAllowOnce),
	)
	assertInteractionAPIError(t, duplicate, http.StatusNotFound, "interaction_not_found")
	_ = duplicate.Body.Close()
}

func TestServerProtocolV2ConflictingInteractionFailsClosedWithoutSnapshot(t *testing.T) {
	server, httpServer, owned := newInteractionRouteTestSession(t)
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	request := permissionBrokerTestRequest("permission-conflict-snapshot")
	request.SessionID = owned.id
	request.ThreadID = owned.threadID
	seedInteractionWaiter(owned, request, "turn-conflict-snapshot")
	conflict := clonePromptRequest(request)
	conflict.Input["path"] = "different"
	owned.permissions.observeEvent(conflict, "turn-conflict-snapshot")

	snapshotResponse := getBearer(t, httpServer.URL+"/v1/sessions/"+owned.id+"/snapshot", "test-token")
	var snapshot SessionSnapshot
	decodeResponse(t, snapshotResponse, &snapshot)
	_ = snapshotResponse.Body.Close()
	if len(snapshot.Interactions) != 0 {
		t.Fatalf("conflicted interaction survived = %#v", snapshot.Interactions)
	}
	resolve := doJSON(
		t,
		httpServer.URL+"/v1/sessions/"+owned.id+"/interactions/"+request.ToolUseID+"/resolve",
		"test-token",
		http.MethodPost,
		permissionResolution(engine.PermissionAllowOnce),
	)
	assertInteractionAPIError(t, resolve, http.StatusNotFound, "interaction_not_found")
	_ = resolve.Body.Close()
}

func seedPlanReviewRequest(owned *session, requestID, path string, revision uint64, digest string) {
	request := engine.PermissionPromptRequest{
		Kind:      engine.PermissionInteractionKindPlanApproval,
		Source:    "project_graph",
		ToolName:  "ExitPlanMode",
		ToolUseID: requestID,
		SessionID: owned.id,
		ThreadID:  owned.threadID,
		PlanApproval: &engine.PlanApprovalRequest{
			RequestID:         requestID,
			PlanRevision:      revision,
			PlanFileIdentity:  path,
			InitialPlanDigest: digest,
		},
	}
	owned.permissions.observeEvent(request, "turn-"+requestID)
	owned.permissions.prepare(request)
}

func seedInteractionWaiter(owned *session, request engine.PermissionPromptRequest, turnID string) {
	owned.permissions.observeEvent(request, turnID)
	owned.permissions.prepare(request)
}

func assertInteractionAPIError(
	t *testing.T,
	response *http.Response,
	wantStatus int,
	wantCode string,
	forbidden ...string,
) {
	t.Helper()
	if response.StatusCode != wantStatus {
		t.Fatalf("status = %d, want %d: %s", response.StatusCode, wantStatus, readBody(t, response))
	}
	var envelope ErrorEnvelope
	decodeResponse(t, response, &envelope)
	_ = response.Body.Close()
	if envelope.Error.Code != wantCode {
		t.Fatalf("error code = %q, want %q", envelope.Error.Code, wantCode)
	}
	encoded := envelope.Error.Code + "\n" + envelope.Error.Message
	for _, value := range forbidden {
		if value != "" && strings.Contains(encoded, value) {
			t.Fatalf("error leaked forbidden value %q: %s", value, encoded)
		}
	}
}

func doRawInteractionJSON(t *testing.T, endpoint, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new interaction request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("resolve interaction: %v", err)
	}
	return response
}

func newInteractionRouteTestSession(t *testing.T) (*Server, *httptest.Server, *session) {
	t.Helper()
	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	create := doJSON(
		t,
		httpServer.URL+"/v1/sessions",
		"test-token",
		http.MethodPost,
		CreateSessionRequest{CWD: t.TempDir()},
	)
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d: %s", create.StatusCode, readBody(t, create))
	}
	var summary SessionSummary
	decodeResponse(t, create, &summary)
	_ = create.Body.Close()
	owned, ok := server.getSession(summary.ID)
	if !ok {
		t.Fatalf("created session %q missing", summary.ID)
	}
	return server, httpServer, owned
}
