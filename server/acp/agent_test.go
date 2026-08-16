package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	acpsdk "github.com/coder/acp-go-sdk"
)

// --- Mock ChatModel ---

type mockChatModel struct {
	mu        sync.Mutex
	responses []*schema.Message
	callIdx   int
}

type asyncRewakeTrackingModel struct {
	mu          sync.Mutex
	rewakeCalls int
}

func (m *asyncRewakeTrackingModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("side-model generation disabled in async rewake test")
}

func (m *asyncRewakeTrackingModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.record(input)
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
}

func (m *asyncRewakeTrackingModel) record(input []*schema.Message) {
	for _, message := range input {
		if message != nil && strings.Contains(message.Content, "<async-hook-response>") {
			m.mu.Lock()
			m.rewakeCalls++
			m.mu.Unlock()
			return
		}
	}
}

func (m *asyncRewakeTrackingModel) RewakeCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rewakeCalls
}

func (m *mockChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.next(), nil
}

func (m *mockChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.next()}), nil
}

func (m *mockChatModel) next() *schema.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.callIdx < len(m.responses) {
		msg := m.responses[m.callIdx]
		m.callIdx++
		return msg
	}
	return &schema.Message{Role: schema.Assistant, Content: "done"}
}

func (m *mockChatModel) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callIdx
}

// --- Mock ACP Client (IDE side) ---

type testClient struct {
	mu                       sync.Mutex
	updates                  []acpsdk.SessionNotification
	extensions               []extensionNotification
	permissionOptionID       string
	permissionRequestCallIDs []acpsdk.ToolCallId
	permissionHandler        func(context.Context, acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error)
}

type extensionNotification struct {
	Method string
	Params json.RawMessage
}

func (c *testClient) ReadTextFile(ctx context.Context, p acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	return acpsdk.ReadTextFileResponse{Content: "mock file content"}, nil
}

func (c *testClient) WriteTextFile(ctx context.Context, p acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.WriteTextFileResponse{}, nil
}

func (c *testClient) RequestPermission(ctx context.Context, p acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	c.mu.Lock()
	c.permissionRequestCallIDs = append(c.permissionRequestCallIDs, p.ToolCall.ToolCallId)
	selected := c.permissionOptionID
	handler := c.permissionHandler
	c.mu.Unlock()
	if handler != nil {
		return handler(ctx, p)
	}
	if selected != "" {
		for _, option := range p.Options {
			if string(option.OptionId) == selected {
				return acpsdk.RequestPermissionResponse{
					Outcome: acpsdk.RequestPermissionOutcome{
						Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: option.OptionId},
					},
				}, nil
			}
		}
	}
	// Auto-approve once by default.
	if len(p.Options) > 0 {
		return acpsdk.RequestPermissionResponse{
			Outcome: acpsdk.RequestPermissionOutcome{
				Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: p.Options[0].OptionId},
			},
		}, nil
	}
	return acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.RequestPermissionOutcome{Cancelled: &acpsdk.RequestPermissionOutcomeCancelled{}},
	}, nil
}

func (c *testClient) SessionUpdate(ctx context.Context, n acpsdk.SessionNotification) error {
	c.mu.Lock()
	c.updates = append(c.updates, n)
	c.mu.Unlock()
	return nil
}

func (c *testClient) CreateTerminal(ctx context.Context, p acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{TerminalId: "test-terminal"}, nil
}

func (c *testClient) KillTerminal(ctx context.Context, p acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	return acpsdk.KillTerminalResponse{}, nil
}

func (c *testClient) ReleaseTerminal(ctx context.Context, p acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, nil
}

func (c *testClient) TerminalOutput(ctx context.Context, p acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.TerminalOutputResponse{Output: "ok", Truncated: false}, nil
}

func (c *testClient) WaitForTerminalExit(ctx context.Context, p acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.WaitForTerminalExitResponse{}, nil
}

func (c *testClient) getUpdates() []acpsdk.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]acpsdk.SessionNotification, len(c.updates))
	copy(out, c.updates)
	return out
}

func (c *testClient) HandleExtensionMethod(_ context.Context, method string, params json.RawMessage) (any, error) {
	c.mu.Lock()
	c.extensions = append(c.extensions, extensionNotification{Method: method, Params: params})
	c.mu.Unlock()
	return nil, nil
}

func (c *testClient) getExtensions() []extensionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]extensionNotification, len(c.extensions))
	copy(out, c.extensions)
	return out
}

func TestACPProjectsTypedCommandResult(t *testing.T) {
	_, client, agent := setupTestACPWithAgent(t, &mockChatModel{})
	before := len(client.getUpdates())

	err := agent.streamEvent(context.Background(), "command-visibility", engine.QueryEvent{
		Type: engine.EventCommandResult,
		CommandResult: &engine.CommandResultEvent{
			Command: "clear",
			Status:  engine.CommandResultSucceeded,
			Output:  "Conversation cleared.",
		},
	})
	if err != nil {
		t.Fatalf("stream typed command result: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	updates := client.getUpdates()
	for len(updates) < before+1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		updates = client.getUpdates()
	}
	if after := len(updates); after != before+1 {
		t.Fatalf("ACP command result updates: before=%d after=%d", before, after)
	}
}

func TestACPProjectsPermissionReviewLifecycleWithoutOpaqueFields(t *testing.T) {
	conn, client, agent := setupTestACPWithAgent(t, &mockChatModel{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
	}); err != nil {
		t.Fatal(err)
	}
	const secret = "secret-control"
	for _, event := range []engine.PermissionReviewEvent{
		{
			Phase:         engine.PermissionReviewChecking,
			CanonicalTool: "Read",
			RequestID:     secret,
		},
		{
			Phase:         engine.PermissionReviewCompleted,
			CanonicalTool: "Read",
			Decision:      "approve",
			ReasonCode:    "expected_safe",
			RequestID:     secret,
		},
		{
			Phase:         engine.PermissionReviewUnavailable,
			CanonicalTool: "Read\n" + secret,
			ReasonCode:    "timeout\n" + secret,
			RequestID:     secret,
		},
	} {
		if err := agent.streamEvent(
			ctx,
			"permission-review",
			engine.QueryEvent{
				Type:             engine.EventPermissionReview,
				PermissionReview: &event,
			},
		); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(time.Second)
	for len(client.getExtensions()) < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	extensions := client.getExtensions()
	if len(extensions) < 3 {
		t.Fatalf("permission review extensions = %#v", extensions)
	}
	extensions = extensions[len(extensions)-3:]
	wantStatuses := []string{
		"permission_review_checking",
		"permission_review_completed",
		"permission_review_unavailable",
	}
	for index, extension := range extensions {
		if extension.Method != "_session/status" {
			t.Fatalf("extension[%d] method = %q", index, extension.Method)
		}
		var payload map[string]any
		if err := json.Unmarshal(extension.Params, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["status"] != wantStatuses[index] {
			t.Fatalf("extension[%d] = %#v", index, payload)
		}
		if message, _ := payload["message"].(string); strings.Contains(message, secret) {
			t.Fatalf("extension[%d] leaked opaque or unsafe field: %#v", index, payload)
		}
	}
}

func TestP462ACPProjectsOneFallbackStatusWithoutAssistantChunk(t *testing.T) {
	conn, client, agent := setupTestACPWithAgent(t, &mockChatModel{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
	}); err != nil {
		t.Fatal(err)
	}
	beforeExtensions := len(client.getExtensions())
	beforeUpdates := len(client.getUpdates())
	for _, attempt := range []*engine.ModelAttemptEvent{
		{AttemptID: "primary", Profile: "primary", Phase: engine.ModelAttemptStarted},
		{AttemptID: "discarded", AttemptIndex: 0, Profile: "primary", Phase: engine.ModelAttemptDiscarded},
		{
			AttemptID: "alternate", AttemptIndex: 1,
			Profile: "fallback.profile", Phase: engine.ModelAttemptStarted,
			SwitchCount: 1, APIModel: "secret-api-model",
		},
	} {
		if err := agent.streamEvent(ctx, "fallback-session", engine.QueryEvent{
			Type: engine.EventModelAttempt, ModelAttempt: attempt,
		}); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(time.Second)
	for len(client.getExtensions()) < beforeExtensions+1 &&
		time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	extensions := client.getExtensions()[beforeExtensions:]
	if len(extensions) != 1 {
		t.Fatalf("fallback extensions = %#v", extensions)
	}
	if extensions[0].Method != "_session/status" {
		t.Fatalf("fallback extension method = %q", extensions[0].Method)
	}
	var payload map[string]any
	if err := json.Unmarshal(extensions[0].Params, &payload); err != nil {
		t.Fatal(err)
	}
	const notice = "Model fallback: profile fallback.profile after overload (switch 1)"
	if payload["status"] != "model_fallback" || payload["message"] != notice {
		t.Fatalf("fallback status = %#v", payload)
	}
	if strings.Contains(string(extensions[0].Params), "secret-api-model") {
		t.Fatalf("fallback status leaked API model: %s", extensions[0].Params)
	}
	if updates := client.getUpdates(); len(updates) != beforeUpdates {
		t.Fatalf("fallback synthesized assistant update = %#v", updates[beforeUpdates:])
	}
}

func TestP462ACPRejectsUnsafeFallbackProfile(t *testing.T) {
	conn, client, agent := setupTestACPWithAgent(t, &mockChatModel{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
	}); err != nil {
		t.Fatal(err)
	}
	before := len(client.getExtensions())
	if err := agent.streamEvent(ctx, "fallback-session", engine.QueryEvent{
		Type: engine.EventModelAttempt,
		ModelAttempt: &engine.ModelAttemptEvent{
			AttemptID: "unsafe", AttemptIndex: 1,
			Profile: "safe\nsecret-control", Phase: engine.ModelAttemptStarted,
			SwitchCount: 1,
		},
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if after := len(client.getExtensions()); after != before {
		t.Fatalf("unsafe fallback extensions: before=%d after=%d", before, after)
	}
}

func TestP165aACPExecutionControlsValidateAndReportEffectiveState(t *testing.T) {
	_, client, agent := setupTestACPWithAgent(t, &mockChatModel{})
	created, err := agent.NewSession(context.Background(), acpsdk.NewSessionRequest{
		Cwd: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := acpConfigOptionValue(created.ConfigOptions, "model"); !ok || got != "mock-model" {
		t.Fatalf("new-session model option = %q, %v", got, ok)
	}
	if created.Modes == nil ||
		created.Modes.CurrentModeId != acpsdk.SessionModeId(permission.ModeBypassPermissions) {
		t.Fatalf("new-session effective modes = %#v", created.Modes)
	}
	initialUpdates := waitForACPUpdates(t, client, 0, 1)
	if !slices.ContainsFunc(initialUpdates, func(update acpsdk.SessionNotification) bool {
		return update.Update.AvailableCommandsUpdate != nil
	}) {
		t.Fatalf("new-session command projection = %#v", initialUpdates)
	}
	_, err = agent.LoadSession(context.Background(), acpsdk.LoadSessionRequest{
		SessionId: created.SessionId,
		Cwd:       t.TempDir(),
	})
	var loadConflict *acpsdk.RequestError
	if !errors.As(err, &loadConflict) ||
		loadConflict.Code != CodeSessionConflict {
		t.Fatalf("active load conflict = %v", err)
	}
	valueRequest := func(configID, value string) acpsdk.SetSessionConfigOptionRequest {
		return acpsdk.SetSessionConfigOptionRequest{
			ValueId: &acpsdk.SetSessionConfigOptionValueId{
				SessionId: created.SessionId,
				ConfigId:  acpsdk.SessionConfigId(configID),
				Value:     acpsdk.SessionConfigValueId(value),
			},
		}
	}
	malformed := valueRequest("model", "claude-sonnet-4-6")
	malformed.Boolean = &acpsdk.SetSessionConfigOptionBoolean{
		SessionId: created.SessionId,
		ConfigId:  acpsdk.SessionConfigId("model"),
		Value:     true,
	}
	if _, err := agent.SetSessionConfigOption(context.Background(), malformed); err == nil {
		t.Fatal("ACP accepted an ambiguous config-option union")
	}

	modelResponse, err := agent.SetSessionConfigOption(
		context.Background(),
		valueRequest("model", "claude-sonnet-4-6"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := acpConfigOptionValue(modelResponse.ConfigOptions, "model"); !ok || got != "claude-sonnet-4-6" {
		t.Fatalf("effective ACP model option = %q, %v", got, ok)
	}
	if got, ok := acpConfigOptionValue(modelResponse.ConfigOptions, "effort"); !ok || got != "default" {
		t.Fatalf("capability-gated ACP effort option = %q, %v", got, ok)
	}
	agent.mu.Lock()
	sess := agent.sessions[created.SessionId]
	agent.mu.Unlock()
	if sess == nil || sess.Engine.GetModelName() != "claude-sonnet-4-6" {
		t.Fatalf("ACP effective model = %#v", sess)
	}
	if _, err := agent.SetSessionConfigOption(
		context.Background(),
		valueRequest("model", "missing-model"),
	); err == nil {
		t.Fatal("ACP accepted a model absent from its resolved inventory")
	}
	if got := sess.Engine.GetModelName(); got != "claude-sonnet-4-6" {
		t.Fatalf("rejected ACP model mutated state to %q", got)
	}

	effortResponse, err := agent.SetSessionConfigOption(
		context.Background(),
		valueRequest("effort", "high"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := acpConfigOptionValue(effortResponse.ConfigOptions, "effort"); !ok || got != "high" {
		t.Fatalf("effective ACP effort option = %q, %v", got, ok)
	}
	if got := sess.Engine.ReasoningEffort(); got != "high" {
		t.Fatalf("ACP effective reasoning effort = %q", got)
	}

	beforeModeUpdates := len(client.getUpdates())
	if _, err := agent.SetSessionMode(context.Background(), acpsdk.SetSessionModeRequest{
		SessionId: created.SessionId,
		ModeId:    acpsdk.SessionModeId(permission.ModeDefault),
	}); err != nil {
		t.Fatal(err)
	}
	modeUpdates := waitForACPUpdates(t, client, beforeModeUpdates, 1)
	if !slices.ContainsFunc(modeUpdates, func(update acpsdk.SessionNotification) bool {
		return update.Update.CurrentModeUpdate != nil &&
			update.Update.CurrentModeUpdate.CurrentModeId == acpsdk.SessionModeId(permission.ModeDefault)
	}) {
		t.Fatalf("direct ACP mode projection = %#v", modeUpdates)
	}
	if _, err := agent.SetSessionMode(context.Background(), acpsdk.SetSessionModeRequest{
		SessionId: created.SessionId,
		ModeId:    acpsdk.SessionModeId(permission.ModeBypassPermissions),
	}); err == nil {
		t.Fatal("ACP protocol mode change entered bypass without explicit confirmation")
	}
	if got := sess.Engine.PermissionMode(); got != permission.ModeDefault {
		t.Fatalf("rejected ACP bypass mutated mode to %q", got)
	}

	beforeSlashUpdates := len(client.getUpdates())
	events, _ := sess.Engine.SubmitMessage(context.Background(), "/permissions mode plan")
	for event := range events {
		if err := agent.streamEvent(context.Background(), created.SessionId, event); err != nil {
			t.Fatal(err)
		}
	}
	var projectedPlan bool
	for _, update := range waitForACPUpdates(t, client, beforeSlashUpdates, 2) {
		if update.Update.CurrentModeUpdate != nil &&
			update.Update.CurrentModeUpdate.CurrentModeId == acpsdk.SessionModeId(permission.ModePlan) {
			projectedPlan = true
		}
	}
	if !projectedPlan || sess.Engine.PermissionMode() != permission.ModePlan {
		t.Fatalf("slash mode projection/effective state = %v / %q", projectedPlan, sess.Engine.PermissionMode())
	}

	beforeEffortUpdates := len(client.getUpdates())
	events, _ = sess.Engine.SubmitMessage(context.Background(), "/effort max")
	for event := range events {
		if err := agent.streamEvent(context.Background(), created.SessionId, event); err != nil {
			t.Fatal(err)
		}
	}
	var projectedEffort bool
	for _, update := range waitForACPUpdates(t, client, beforeEffortUpdates, 2) {
		if update.Update.ConfigOptionUpdate == nil {
			continue
		}
		if got, ok := acpConfigOptionValue(update.Update.ConfigOptionUpdate.ConfigOptions, "effort"); ok && got == "max" {
			projectedEffort = true
		}
	}
	if !projectedEffort || sess.Engine.ReasoningEffort() != "max" {
		t.Fatalf("slash effort projection/effective state = %v / %q", projectedEffort, sess.Engine.ReasoningEffort())
	}
}

func acpConfigOptionValue(
	options []acpsdk.SessionConfigOption,
	id string,
) (string, bool) {
	for _, option := range options {
		if option.Select != nil && string(option.Select.Id) == id {
			return string(option.Select.CurrentValue), true
		}
	}
	return "", false
}

func waitForACPUpdates(
	t *testing.T,
	client *testClient,
	before int,
	minimum int,
) []acpsdk.SessionNotification {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		updates := client.getUpdates()
		if len(updates) >= before+minimum {
			return updates[before:]
		}
		if time.Now().After(deadline) {
			t.Fatalf(
				"timed out waiting for %d ACP updates after %d; got %d",
				minimum,
				before,
				len(updates)-before,
			)
		}
		time.Sleep(time.Millisecond)
	}
}

// --- Test helpers ---

// setupTestACP creates an in-process ACP agent+client pair using io.Pipe().
// The agent uses a mock ChatModel so no real LLM is needed.
func setupTestACP(t *testing.T, mockModel model.BaseChatModel) (*acpsdk.ClientSideConnection, *testClient) {
	t.Helper()
	conn, client, _ := setupTestACPWithAgent(t, mockModel)
	return conn, client
}

// setupTestACPWithAgent creates an in-process ACP agent+client pair using io.Pipe()
// and returns the connection, test client, and the underlying *Agent for white-box tests.
// The agent uses a mock ChatModel so no real LLM is needed.
func setupTestACPWithAgent(t *testing.T, mockModel model.BaseChatModel) (*acpsdk.ClientSideConnection, *testClient, *Agent) {
	t.Helper()

	agent, err := NewAgent(Config{
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true, // bypass permissions for testing
		MaxTurns:     10,
		CWD:          t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
		return nil, nil, nil
	}

	// Override createEngine to use our mock model
	agent.mockModel = mockModel

	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	client := &testClient{}

	agentConn := acpsdk.NewAgentSideConnection(agent, a2cW, c2aR)
	agent.SetConnection(agentConn)

	clientConn := acpsdk.NewClientSideConnection(client, c2aW, a2cR)

	t.Cleanup(func() {
		agent.Close()
		_ = c2aW.Close()
		_ = a2cW.Close()
	})

	return clientConn, client, agent
}

// --- Tests ---

func TestACP_Initialize(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "hello"},
		},
	}

	conn, _ := setupTestACP(t, mockModel)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{
			Fs:       acpsdk.FileSystemCapabilities{ReadTextFile: true, WriteTextFile: true},
			Terminal: true,
		},
	})
	if err != nil {
		t.Fatalf("Initialize error: %v", err)
		return
	}
	if resp.ProtocolVersion != acpsdk.ProtocolVersionNumber {
		t.Fatalf("protocol version mismatch: got %d want %d", resp.ProtocolVersion, acpsdk.ProtocolVersionNumber)
	}
}

func TestACP_AllowAlwaysPersistsRuleThroughEngineCoordinator(t *testing.T) {
	model := &mockChatModel{responses: []*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID: "call-always-1", Type: "function",
				Function: schema.FunctionCall{Name: "Bash", Arguments: `{"command":"echo approved"}`},
			}},
		},
		{Role: schema.Assistant, Content: "done"},
	}}
	conn, client, agent := setupTestACPWithAgent(t, model)
	agent.config.YoloMode = false
	client.mu.Lock()
	client.permissionOptionID = "allow_always"
	client.mu.Unlock()
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
	}); err != nil {
		t.Fatal(err)
	}
	session, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: root, McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: session.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("run the command")},
	}); err != nil {
		t.Fatal(err)
	}

	settings, err := os.ReadFile(filepath.Join(root, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatalf("read persisted permission rule: %v", err)
	}
	exactRule, err := permission.BuildExactRuleFromInvocation(
		"Bash",
		map[string]any{"command": "echo approved"},
		root,
	)
	if err != nil {
		t.Fatal(err)
	}
	encodedRule, err := json.Marshal(exactRule.Value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(settings), string(encodedRule)) != 1 ||
		strings.Contains(string(settings), `"Bash(echo *)"`) {
		t.Fatalf(
			"always decision did not persist only exact rule %s: %s",
			encodedRule,
			settings,
		)
	}
	client.mu.Lock()
	callIDs := append([]acpsdk.ToolCallId(nil), client.permissionRequestCallIDs...)
	client.mu.Unlock()
	if len(callIDs) != 1 || callIDs[0] != "call-always-1" {
		t.Fatalf("ACP permission call IDs = %#v", callIDs)
	}
	var permissionStatuses []string
	for _, extension := range client.getExtensions() {
		if extension.Method != "_session/status" {
			continue
		}
		var payload map[string]any
		if json.Unmarshal(extension.Params, &payload) == nil {
			if status, _ := payload["status"].(string); strings.HasPrefix(status, "permission_") || status == "waiting_for_permission" {
				permissionStatuses = append(permissionStatuses, status)
			}
		}
	}
	if !reflect.DeepEqual(permissionStatuses, []string{"waiting_for_permission", "permission_allow_always"}) {
		t.Fatalf("ACP permission status order = %#v", permissionStatuses)
	}
}

func TestP512ACPPermissionOptionsAllowOnceOnly(t *testing.T) {
	opts := acpPermissionOptions(engine.PermissionPromptRequest{DecisionConstraint: engine.PermissionAllowOnceOnly})
	if len(opts) != 2 || opts[0].OptionId != "allow" || opts[1].OptionId != "reject" {
		t.Fatalf("constrained ACP options = %#v", opts)
	}
}

func TestP138ACPDrivesProjectGraphPermissionResume(t *testing.T) {
	root := t.TempDir()
	outputPath := filepath.Join(root, "graph-acp.txt")
	model := &mockChatModel{responses: []*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   "graph-acp-write",
				Type: "function",
				Function: schema.FunctionCall{
					Name: "Write",
					Arguments: fmt.Sprintf(
						`{"file_path":%q,"content":"written once"}`,
						outputPath,
					),
				},
			}},
		},
		{Role: schema.Assistant, Content: "done"},
	}}
	conn, client, agent := setupTestACPWithAgent(t, model)
	agent.config.YoloMode = false

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
	}); err != nil {
		t.Fatal(err)
	}
	session, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{
		Cwd:        root,
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: session.SessionId,
		Prompt: []acpsdk.ContentBlock{
			acpsdk.TextBlock("write the graph-owned file"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("stop reason = %q", response.StopReason)
	}
	content, err := os.ReadFile(outputPath)
	if err != nil {
		agent.mu.Lock()
		active := agent.sessions[session.SessionId]
		agent.mu.Unlock()
		var messages []*schema.Message
		if active != nil && active.Engine != nil {
			messages = active.Engine.GetMessages()
		}
		messageData, _ := json.Marshal(messages)
		t.Fatalf(
			"read Graph-owned output: %v; messages=%s",
			err,
			messageData,
		)
	}
	if string(content) != "written once" {
		t.Fatalf("written content = %q", content)
	}
	if model.CallCount() != 2 {
		t.Fatalf("model calls = %d, want 2", model.CallCount())
	}
	client.mu.Lock()
	callIDs := append(
		[]acpsdk.ToolCallId(nil),
		client.permissionRequestCallIDs...,
	)
	client.mu.Unlock()
	if !reflect.DeepEqual(
		callIDs,
		[]acpsdk.ToolCallId{"graph-acp-write"},
	) {
		t.Fatalf("project graph permission calls = %#v", callIDs)
	}
}

func TestACP_ProjectGraphSerializesDistinctExactAllowAlwaysDecisions(
	t *testing.T,
) {
	external := t.TempDir()
	firstPath := filepath.Join(external, "first.txt")
	secondPath := filepath.Join(external, "second.txt")
	if err := os.WriteFile(firstPath, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	model := &mockChatModel{responses: []*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "call-source", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: fmt.Sprintf(`{"file_path":%q}`, firstPath)}},
				{ID: "call-follower", Type: "function", Function: schema.FunctionCall{Name: "Read", Arguments: fmt.Sprintf(`{"file_path":%q}`, secondPath)}},
			},
		},
		{Role: schema.Assistant, Content: "done"},
	}}
	conn, client, agent := setupTestACPWithAgent(t, model)
	agent.config.YoloMode = false
	client.mu.Lock()
	client.permissionHandler = func(ctx context.Context, request acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
		for _, option := range request.Options {
			if option.OptionId == "allow_always" {
				return acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{
					Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: option.OptionId},
				}}, nil
			}
		}
		return acpsdk.RequestPermissionResponse{}, fmt.Errorf(
			"allow_always option missing",
		)
	}
	client.mu.Unlock()

	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := conn.Initialize(ctx, acpsdk.InitializeRequest{ProtocolVersion: acpsdk.ProtocolVersionNumber}); err != nil {
		t.Fatal(err)
	}
	session, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: root, McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: session.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("read both files")},
	}); err != nil {
		t.Fatal(err)
	}

	client.mu.Lock()
	callIDs := append([]acpsdk.ToolCallId(nil), client.permissionRequestCallIDs...)
	client.mu.Unlock()
	if !reflect.DeepEqual(
		callIDs,
		[]acpsdk.ToolCallId{"call-source", "call-follower"},
	) {
		t.Fatalf("permission calls = %#v", callIDs)
	}
	settings, err := os.ReadFile(filepath.Join(root, ".claude", "settings.local.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{firstPath, secondPath} {
		exactRule, buildErr := permission.BuildExactRuleFromInvocation(
			"Read",
			map[string]any{"file_path": path},
			root,
		)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		encodedRule, marshalErr := json.Marshal(exactRule.Value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if strings.Count(string(settings), string(encodedRule)) != 1 {
			t.Fatalf(
				"settings = %s, want one exact rule %s",
				settings,
				encodedRule,
			)
		}
	}
	if strings.Contains(string(settings), "/*)") {
		t.Fatalf("settings widened exact ACP grants: %s", settings)
	}
}

func TestACP_NewSession(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "hello"},
		},
	}

	conn, _ := setupTestACP(t, mockModel)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Initialize first
	_, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	// Create new session
	sess, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{
		Cwd:        t.TempDir(),
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}
	if sess.SessionId == "" {
		t.Fatal("expected non-empty session ID")
	}
}

func TestACP_Prompt_SimpleTextResponse(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "Hello! I'm your assistant."},
		},
	}

	conn, client := setupTestACP(t, mockModel)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Initialize
	_, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	// Create session
	sess, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{
		Cwd:        t.TempDir(),
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}

	// Send prompt
	promptResp, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("Hello!")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
		return
	}
	if promptResp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("unexpected stop reason: %q", promptResp.StopReason)
	}

	// Check that we received session updates with the assistant message
	updates := client.getUpdates()
	if len(updates) == 0 {
		t.Fatal("expected at least one session update with assistant message")
	}

	// Verify at least one update contains text
	var gotText bool
	for _, u := range updates {
		if u.Update.AgentMessageChunk != nil {
			gotText = true
			break
		}
	}
	if !gotText {
		t.Logf("updates: %+v", updates)
		t.Fatal("expected at least one AgentMessageChunk update")
	}
}

func TestACP_Prompt_WithToolCall(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			// First call: model calls a tool
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "Read",
						Arguments: `{"file_path": "/tmp/test.txt"}`,
					},
				}},
			},
			// Second call: model returns final text
			{Role: schema.Assistant, Content: "I read the file for you."},
		},
	}

	conn, client := setupTestACP(t, mockModel)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Initialize
	_, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	// Create session
	sess, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{
		Cwd:        t.TempDir(),
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}

	// Send prompt
	promptResp, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("Read the file /tmp/test.txt")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
		return
	}
	if promptResp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("unexpected stop reason: %q", promptResp.StopReason)
	}

	// Check updates include tool call start and tool result
	updates := client.getUpdates()
	var hasToolCallStart, hasToolTerminal bool
	for _, u := range updates {
		data, _ := json.Marshal(u.Update)
		t.Logf("update: %s", string(data))
		if u.Update.ToolCall != nil {
			hasToolCallStart = true
		}
		if u.Update.ToolCallUpdate != nil &&
			u.Update.ToolCallUpdate.Status != nil &&
			(*u.Update.ToolCallUpdate.Status == acpsdk.ToolCallStatusCompleted ||
				*u.Update.ToolCallUpdate.Status == acpsdk.ToolCallStatusFailed) {
			hasToolTerminal = true
		}
	}
	if !hasToolCallStart {
		t.Fatal("expected a tool call start update (tool_call)")
	}
	if !hasToolTerminal {
		t.Fatal("expected a completed-or-failed tool terminal update")
	}
}

func TestACP_ListSessions(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "hello"},
		},
	}

	conn, _ := setupTestACP(t, mockModel)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Initialize
	_, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	// Create two sessions in the same requested CWD.
	cwd := t.TempDir()
	sess1, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: cwd, McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession 1: %v", err)
		return
	}
	sess2, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: cwd, McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession 2: %v", err)
		return
	}

	// List sessions
	list, err := conn.ListSessions(ctx, acpsdk.ListSessionsRequest{Cwd: &cwd})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
		return
	}
	if len(list.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(list.Sessions))
	}

	// Close one
	_, err = conn.CloseSession(ctx, acpsdk.CloseSessionRequest{SessionId: sess1.SessionId})
	if err != nil {
		t.Fatalf("CloseSession: %v", err)
		return
	}

	// List again
	list, err = conn.ListSessions(ctx, acpsdk.ListSessionsRequest{Cwd: &cwd})
	if err != nil {
		t.Fatalf("ListSessions after close: %v", err)
		return
	}
	if len(list.Sessions) != 1 {
		t.Fatalf("expected 1 session after close, got %d", len(list.Sessions))
	}
	if list.Sessions[0].SessionId != sess2.SessionId {
		t.Fatalf("remaining session ID mismatch: got %q want %q", list.Sessions[0].SessionId, sess2.SessionId)
	}
}

func TestACPResumeSlashRejectsIdentityChangeBeforeMutation(t *testing.T) {
	cwd := t.TempDir()
	transcriptDir := acpSessionTranscriptDir(cwd)
	const oldSessionID = "session-acp-old"
	recorder := transcript.NewRecorder(oldSessionID, transcriptDir)
	if err := recorder.ReplaceWithReplacements([]*schema.Message{
		{Role: schema.User, Content: "old ACP prompt"},
		{Role: schema.Assistant, Content: "old ACP answer"},
	}, nil); err != nil {
		t.Fatal(err)
		return
	}
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	agent, err := NewAgent(Config{CWD: cwd, ProviderFlag: "mock", ModelFlag: "mock-model", APIKeyFlag: "mock", YoloMode: true})
	if err != nil {
		t.Fatal(err)
		return
	}
	agent.mockModel = &mockChatModel{}
	listed, err := agent.ListSessions(context.Background(), acpsdk.ListSessionsRequest{})
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].Title == nil || *listed.Sessions[0].Title != "old ACP prompt" || listed.Sessions[0].UpdatedAt == nil {
		t.Fatalf("ACP persisted session metadata is incomplete: %#v", listed.Sessions)
		return
	}
	eng, err := agent.createEngine("session-acp-new")
	if err != nil {
		t.Fatal(err)
		return
	}
	t.Cleanup(eng.Close)
	if !eng.LongSessionServicesEnabled() {
		t.Fatal("ACP interactive engine did not enable long-session services")
	}
	for _, input := range []string{
		"/resume " + oldSessionID,
		"/sessions resume " + oldSessionID,
		"/sessions list",
		"/fork acp-child",
	} {
		events, _ := eng.SubmitMessage(context.Background(), input)
		var commandResult *engine.CommandResultEvent
		for event := range events {
			if event.Type == engine.EventCommandResult {
				commandResult = event.CommandResult
			}
		}
		if commandResult == nil ||
			commandResult.Status != engine.CommandResultUnsupported {
			t.Fatalf("ACP slash %q result = %#v", input, commandResult)
		}
	}
	if eng.SessionID() != "session-acp-new" {
		t.Fatalf("ACP slash identity command mutated identity to %q", eng.SessionID())
	}
	if got := eng.GetMessages(); len(got) != 0 {
		t.Fatalf("ACP slash identity command mutated messages: %#v", got)
	}
}

func TestACP_Cancel(t *testing.T) {
	// Model that blocks until context is canceled
	blockingModel := &blockingChatModel{blocked: make(chan struct{})}

	conn, _ := setupTestACP(t, blockingModel)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Initialize
	_, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	// Create session
	sess, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}

	// Start prompt in goroutine
	type promptResult struct {
		resp acpsdk.PromptResponse
		err  error
	}
	promptDone := make(chan promptResult, 1)
	go func() {
		resp, err := conn.Prompt(ctx, acpsdk.PromptRequest{
			SessionId: sess.SessionId,
			Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("do something slow")},
		})
		promptDone <- promptResult{resp, err}
	}()

	// Wait for model to start blocking
	select {
	case <-blockingModel.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for model to start")
	}

	// Cancel the prompt
	err = conn.Cancel(ctx, acpsdk.CancelNotification{SessionId: sess.SessionId})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
		return
	}

	// Prompt should finish with cancelled stop reason
	select {
	case result := <-promptDone:
		if result.err != nil {
			t.Logf("prompt returned error (may be expected for cancel): %v", result.err)
		} else if result.resp.StopReason != acpsdk.StopReasonCancelled {
			t.Logf("stop reason after cancel: %q (expected %q)", result.resp.StopReason, acpsdk.StopReasonCancelled)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for prompt to finish after cancel")
	}
}

func TestACPIdleAndLateCancelDoNotStopNextPrompt(t *testing.T) {
	mockModel := &mockChatModel{responses: []*schema.Message{
		{Role: schema.Assistant, Content: "first"},
		{Role: schema.Assistant, Content: "second"},
	}}
	conn, _, agent := setupTestACPWithAgent(t, mockModel)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	}); err != nil {
		t.Fatal(err)
	}
	session, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{
		Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}

	assertNoPendingStop := func(stage string) {
		t.Helper()
		agent.mu.Lock()
		active := agent.sessions[session.SessionId]
		agent.mu.Unlock()
		if active == nil {
			t.Fatalf("%s: session is unavailable", stage)
		}
		if items := active.Engine.RuntimeItems(); len(items) != 0 {
			t.Fatalf("%s: idle cancel left runtime items: %#v", stage, items)
		}
	}

	if err := conn.Cancel(ctx, acpsdk.CancelNotification{SessionId: session.SessionId}); err != nil {
		t.Fatalf("idle Cancel: %v", err)
	}
	assertNoPendingStop("before first prompt")
	if _, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: session.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("first prompt")},
	}); err != nil {
		t.Fatalf("first Prompt: %v", err)
	}

	if err := conn.Cancel(ctx, acpsdk.CancelNotification{SessionId: session.SessionId}); err != nil {
		t.Fatalf("late Cancel: %v", err)
	}
	assertNoPendingStop("between prompts")
	if _, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: session.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("second prompt")},
	}); err != nil {
		t.Fatalf("second Prompt: %v", err)
	}
	if calls := mockModel.CallCount(); calls != 2 {
		t.Fatalf("model calls = %d, want 2", calls)
	}
}

func TestACPActiveCancelIsScopedToCurrentPrompt(t *testing.T) {
	blockingModel := newFirstCallBlockingChatModel()
	t.Cleanup(blockingModel.release)
	conn, _, agent := setupTestACPWithAgent(t, blockingModel)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	}); err != nil {
		t.Fatal(err)
	}
	session, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{
		Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}

	type promptResult struct {
		response acpsdk.PromptResponse
		err      error
	}
	firstDone := make(chan promptResult, 1)
	go func() {
		response, promptErr := conn.Prompt(ctx, acpsdk.PromptRequest{
			SessionId: session.SessionId,
			Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("first prompt")},
		})
		firstDone <- promptResult{response: response, err: promptErr}
	}()

	select {
	case <-blockingModel.firstStarted:
	case <-ctx.Done():
		t.Fatal("first prompt did not reach the model")
	}
	if err := conn.Cancel(ctx, acpsdk.CancelNotification{SessionId: session.SessionId}); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	select {
	case <-blockingModel.firstCancelled:
	case <-ctx.Done():
		t.Fatal("active prompt context was not cancelled")
	}

	agent.mu.Lock()
	active := agent.sessions[session.SessionId]
	agent.mu.Unlock()
	if active == nil {
		t.Fatal("session is unavailable")
	}
	if items := active.Engine.RuntimeItems(); len(items) != 0 {
		t.Fatalf("active cancel leaked engine-wide runtime items: %#v", items)
	}

	blockingModel.release()
	select {
	case result := <-firstDone:
		if result.err != nil && !strings.Contains(result.err.Error(), context.Canceled.Error()) {
			t.Fatalf("first Prompt: %v", result.err)
		}
		if result.err == nil && result.response.StopReason != acpsdk.StopReasonCancelled {
			t.Fatalf("first stop reason = %q, want %q", result.response.StopReason, acpsdk.StopReasonCancelled)
		}
	case <-ctx.Done():
		t.Fatal("first prompt did not finish after cancellation")
	}

	second, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: session.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("second prompt")},
	})
	if err != nil {
		t.Fatalf("second Prompt: %v", err)
	}
	if second.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("second stop reason = %q, want %q", second.StopReason, acpsdk.StopReasonEndTurn)
	}
}

func TestACP_EmptyPrompt(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "should not be called"},
		},
	}

	conn, _ := setupTestACP(t, mockModel)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Initialize
	_, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	// Create session
	sess, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}

	// Send empty prompt
	resp, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{}, // empty
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
		return
	}
	if resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("expected end_turn for empty prompt, got %q", resp.StopReason)
	}
}

// --- Blocking model for cancel test ---

type blockingChatModel struct {
	blocked chan struct{}
	once    sync.Once
}

func (m *blockingChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	m.once.Do(func() { close(m.blocked) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *blockingChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.once.Do(func() { close(m.blocked) })
	<-ctx.Done()
	return nil, ctx.Err()
}

type firstCallBlockingChatModel struct {
	mu             sync.Mutex
	calls          int
	firstStarted   chan struct{}
	firstCancelled chan struct{}
	releaseFirst   chan struct{}
	releaseOnce    sync.Once
}

func newFirstCallBlockingChatModel() *firstCallBlockingChatModel {
	return &firstCallBlockingChatModel{
		firstStarted:   make(chan struct{}),
		firstCancelled: make(chan struct{}),
		releaseFirst:   make(chan struct{}),
	}
}

func (m *firstCallBlockingChatModel) Generate(
	ctx context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	return m.next(ctx)
}

func (m *firstCallBlockingChatModel) Stream(
	ctx context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.next(ctx)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *firstCallBlockingChatModel) next(ctx context.Context) (*schema.Message, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call == 1 {
		close(m.firstStarted)
		<-ctx.Done()
		close(m.firstCancelled)
		<-m.releaseFirst
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &schema.Message{Role: schema.Assistant, Content: "second"}, nil
}

func (m *firstCallBlockingChatModel) release() {
	m.releaseOnce.Do(func() { close(m.releaseFirst) })
}

func TestACP_MultiplePromptsInSession(t *testing.T) {
	callCount := 0
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "First response"},
			{Role: schema.Assistant, Content: "Second response"},
		},
	}

	conn, client := setupTestACP(t, mockModel)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	sess, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}

	// First prompt
	_, err = conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("Hello")},
	})
	if err != nil {
		t.Fatalf("Prompt 1: %v", err)
		return
	}
	callCount++

	// Second prompt in same session
	_, err = conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("Follow up")},
	})
	if err != nil {
		t.Fatalf("Prompt 2: %v", err)
		return
	}
	callCount++

	// Verify we got updates from both prompts
	updates := client.getUpdates()
	var textUpdates []string
	for _, u := range updates {
		if u.Update.AgentMessageChunk != nil && u.Update.AgentMessageChunk.Content.Text != nil {
			textUpdates = append(textUpdates, u.Update.AgentMessageChunk.Content.Text.Text)
		}
	}
	if len(textUpdates) < 2 {
		t.Fatalf("expected at least 2 text updates from two prompts, got %d: %v", len(textUpdates), textUpdates)
	}
	_ = callCount
}

func TestACP_PromptToNonExistentSession(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "should not reach"},
		},
	}

	conn, _ := setupTestACP(t, mockModel)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	// Prompt without creating a session
	_, err = conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: "non-existent-session",
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("Hello")},
	})
	if err == nil {
		t.Fatal("expected error when prompting non-existent session")
		return
	}
}

func TestACP_MaxTurns(t *testing.T) {
	// Model always responds with a tool call, triggering max turns
	mockModel := &infiniteToolCallModel{}

	conn, _ := setupTestACP(t, mockModel)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	sess, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}

	resp, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("keep calling tools")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
		return
	}
	if resp.StopReason != acpsdk.StopReasonMaxTurnRequests {
		t.Fatalf("expected stop reason %q, got %q", acpsdk.StopReasonMaxTurnRequests, resp.StopReason)
	}
}

func TestACPMaxTurnsZeroUnlimitedAndNegativeRejected(t *testing.T) {
	agent, err := NewAgent(Config{MaxTurns: 0})
	if err != nil {
		t.Fatalf("zero MaxTurns: %v", err)
		return
	}
	if agent.config.MaxTurns != 0 {
		t.Fatalf("MaxTurns = %d, want unlimited (0)", agent.config.MaxTurns)
	}
	if _, err := NewAgent(Config{MaxTurns: -1}); err == nil {
		t.Fatal("negative MaxTurns was accepted")
		return
	}
}

func TestACPApprovalReviewShadowRequiresExplicitRoute(t *testing.T) {
	for _, cfg := range []Config{
		{ApprovalReviewShadow: true, ApprovalReviewModel: "review-model"},
		{ApprovalReviewShadow: true, ApprovalReviewProvider: "openai"},
		{
			ApprovalReviewShadow:   true,
			ApprovalReviewProvider: "openai",
			ApprovalReviewModel:    "review-model",
			ApprovalReviewTimeout:  -time.Second,
		},
	} {
		if _, err := NewAgent(cfg); err == nil {
			t.Fatalf("invalid review shadow config was accepted: %#v", cfg)
		}
	}
	agent, err := NewAgent(Config{
		ApprovalReviewShadow:   true,
		ApprovalReviewProvider: "openai",
		ApprovalReviewModel:    "review-model",
	})
	if err != nil {
		t.Fatalf("valid review shadow config: %v", err)
	}
	if !agent.config.ApprovalReviewShadow {
		t.Fatal("review shadow opt-in was lost")
	}
}

func TestACPApprovalReviewAuditRequiresShadowAndOwnsOneStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	for _, cfg := range []Config{
		{ApprovalReviewAudit: true},
		{ApprovalReviewAuditDir: dir},
	} {
		agent, err := NewAgent(cfg)
		if agent != nil || err == nil {
			t.Fatalf("invalid audit config was accepted: %#v", cfg)
		}
		if strings.Contains(err.Error(), dir) {
			t.Fatalf("audit config error leaked absolute path: %v", err)
		}
	}

	agent, err := NewAgent(Config{
		ApprovalReviewShadow:   true,
		ApprovalReviewProvider: "openai",
		ApprovalReviewModel:    "review-model",
		ApprovalReviewAudit:    true,
		ApprovalReviewAuditDir: dir,
	})
	if err != nil {
		t.Fatalf("valid audit config: %v", err)
	}
	if agent.approvalReviewAudit == nil {
		t.Fatal("ACP agent did not create its shared audit store")
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("audit directory mode: info=%v err=%v", info, err)
	}
	owned := agent.approvalReviewAudit
	if agent.approvalReviewAudit != owned {
		t.Fatal("ACP agent replaced its process-local audit store")
	}
}

func TestACP_PermissionDelegation(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			// Model calls a tool
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "call_perm_1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "Read",
						Arguments: `{"file_path": "/tmp/perm_test.txt"}`,
					},
				}},
			},
			// Final response after tool completes
			{Role: schema.Assistant, Content: "File read with permission."},
		},
	}

	// Create agent WITHOUT YoloMode so permission delegation is active
	agent, err := NewAgent(Config{
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     false,
		MaxTurns:     10,
		CWD:          t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
		return
	}
	agent.mockModel = mockModel

	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	permClient := &permissionTrackingClient{}
	agentConn := acpsdk.NewAgentSideConnection(agent, a2cW, c2aR)
	agent.SetConnection(agentConn)
	clientConn := acpsdk.NewClientSideConnection(permClient, c2aW, a2cR)

	t.Cleanup(func() {
		agent.Close()
		_ = c2aW.Close()
		_ = a2cW.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = clientConn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	sess, err := clientConn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}

	_, err = clientConn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("read a file")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
		return
	}

	// Verify permission was requested
	permClient.mu.Lock()
	count := permClient.permRequestCount
	lifecycle := append([]string(nil), permClient.lifecycle...)
	permClient.mu.Unlock()
	if count == 0 {
		t.Fatal("expected at least one permission request, got 0")
	}
	startIndex := slices.Index(lifecycle, "start:call_perm_1")
	permissionIndex := slices.Index(lifecycle, "permission:call_perm_1")
	terminalIndex := slices.Index(lifecycle, "terminal:call_perm_1")
	if startIndex < 0 ||
		permissionIndex <= startIndex ||
		terminalIndex <= permissionIndex {
		t.Fatalf("permission lifecycle order = %#v", lifecycle)
	}
	startCount := 0
	terminalCount := 0
	for _, event := range lifecycle {
		switch event {
		case "start:call_perm_1":
			startCount++
		case "terminal:call_perm_1":
			terminalCount++
		}
	}
	if startCount != 1 || terminalCount != 1 {
		t.Fatalf("permission lifecycle multiplicity = %#v", lifecycle)
	}
}

// --- infiniteToolCallModel: always returns a tool call ---

type infiniteToolCallModel struct {
	mu      sync.Mutex
	callIdx int
}

func (m *infiniteToolCallModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.next(), nil
}

func (m *infiniteToolCallModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.next()}), nil
}

func (m *infiniteToolCallModel) next() *schema.Message {
	m.mu.Lock()
	m.callIdx++
	id := fmt.Sprintf("call_inf_%d", m.callIdx)
	m.mu.Unlock()
	return &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:   id,
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Read",
				Arguments: `{"file_path": "/tmp/infinite.txt"}`,
			},
		}},
	}
}

// --- permissionTrackingClient: tracks permission requests ---

type permissionTrackingClient struct {
	mu               sync.Mutex
	permRequestCount int
	updates          []acpsdk.SessionNotification
	lifecycle        []string
}

func (c *permissionTrackingClient) ReadTextFile(ctx context.Context, p acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	return acpsdk.ReadTextFileResponse{Content: "mock content"}, nil
}

func (c *permissionTrackingClient) WriteTextFile(ctx context.Context, p acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.WriteTextFileResponse{}, nil
}

func (c *permissionTrackingClient) RequestPermission(ctx context.Context, p acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	c.mu.Lock()
	c.permRequestCount++
	c.lifecycle = append(
		c.lifecycle,
		"permission:"+string(p.ToolCall.ToolCallId),
	)
	c.mu.Unlock()
	// Auto-approve
	if len(p.Options) > 0 {
		return acpsdk.RequestPermissionResponse{
			Outcome: acpsdk.RequestPermissionOutcome{
				Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: p.Options[0].OptionId},
			},
		}, nil
	}
	return acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.RequestPermissionOutcome{Cancelled: &acpsdk.RequestPermissionOutcomeCancelled{}},
	}, nil
}

func (c *permissionTrackingClient) SessionUpdate(ctx context.Context, n acpsdk.SessionNotification) error {
	c.mu.Lock()
	c.updates = append(c.updates, n)
	if n.Update.ToolCall != nil {
		c.lifecycle = append(
			c.lifecycle,
			"start:"+string(n.Update.ToolCall.ToolCallId),
		)
	}
	if n.Update.ToolCallUpdate != nil &&
		n.Update.ToolCallUpdate.Status != nil {
		status := *n.Update.ToolCallUpdate.Status
		if status == acpsdk.ToolCallStatusCompleted ||
			status == acpsdk.ToolCallStatusFailed {
			c.lifecycle = append(
				c.lifecycle,
				"terminal:"+string(
					n.Update.ToolCallUpdate.ToolCallId,
				),
			)
		}
	}
	c.mu.Unlock()
	return nil
}

func (c *permissionTrackingClient) CreateTerminal(ctx context.Context, p acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{TerminalId: "test-terminal"}, nil
}

func (c *permissionTrackingClient) KillTerminal(ctx context.Context, p acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	return acpsdk.KillTerminalResponse{}, nil
}

func (c *permissionTrackingClient) ReleaseTerminal(ctx context.Context, p acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, nil
}

func (c *permissionTrackingClient) TerminalOutput(ctx context.Context, p acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.TerminalOutputResponse{Output: "ok", Truncated: false}, nil
}

func (c *permissionTrackingClient) WaitForTerminalExit(ctx context.Context, p acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.WaitForTerminalExitResponse{}, nil
}

func TestACP_MultipleToolCalls(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			// Model calls two tools at once
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "Read",
							Arguments: `{"file_path": "/tmp/a.txt"}`,
						},
					},
					{
						ID:   "call_2",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "Read",
							Arguments: `{"file_path": "/tmp/b.txt"}`,
						},
					},
				},
			},
			// Final response
			{Role: schema.Assistant, Content: "I read both files."},
		},
	}

	conn, client := setupTestACP(t, mockModel)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	sess, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}

	_, err = conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("Read both files")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
		return
	}

	updates := client.getUpdates()
	var toolCallStarts []string
	var toolInputUpdates []string
	var toolTerminalUpdates []string
	for _, u := range updates {
		if u.Update.ToolCall != nil {
			toolCallStarts = append(toolCallStarts, string(u.Update.ToolCall.ToolCallId))
		}
		if u.Update.ToolCallUpdate != nil {
			status := u.Update.ToolCallUpdate.Status
			switch {
			case u.Update.ToolCallUpdate.RawInput != nil:
				toolInputUpdates = append(
					toolInputUpdates,
					string(u.Update.ToolCallUpdate.ToolCallId),
				)
			case status != nil &&
				(*status == acpsdk.ToolCallStatusCompleted ||
					*status == acpsdk.ToolCallStatusFailed):
				toolTerminalUpdates = append(
					toolTerminalUpdates,
					string(u.Update.ToolCallUpdate.ToolCallId),
				)
			}
		}
	}

	if len(toolCallStarts) != 2 {
		t.Fatalf("expected 2 tool call starts, got %d: %v", len(toolCallStarts), toolCallStarts)
	}
	if len(toolInputUpdates) != 2 {
		t.Fatalf(
			"expected 2 tool input updates, got %d: %v",
			len(toolInputUpdates),
			toolInputUpdates,
		)
	}
	if len(toolTerminalUpdates) != 2 {
		t.Fatalf(
			"expected 2 tool terminal updates, got %d: %v",
			len(toolTerminalUpdates),
			toolTerminalUpdates,
		)
	}
}

func TestACPHookStatusExtensionEvents(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "done"},
		},
	}

	agent, err := NewAgent(Config{
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
		CWD:          t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
		return
	}
	agent.mockModel = mockModel

	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	client := &testClient{}

	agentConn := acpsdk.NewAgentSideConnection(agent, a2cW, c2aR)
	agent.SetConnection(agentConn)

	clientConn := acpsdk.NewClientSideConnection(client, c2aW, a2cR)

	t.Cleanup(func() {
		agent.Close()
		_ = c2aW.Close()
		_ = a2cW.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err = clientConn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	sess, err := clientConn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}

	_, err = clientConn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("hello")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
		return
	}

	// Directly invoke streamEvent with a hook status event to verify the mapping.
	err = agent.streamEvent(ctx, sess.SessionId, engine.QueryEvent{
		Type: engine.EventHookStatus,
		HookStatus: &engine.HookStatusEvent{
			HookName:      "pre_tool_use: echo checking",
			StatusMessage: "Running lint check...",
			Phase:         "running",
		},
	})
	if err != nil {
		t.Fatalf("streamEvent(EventHookStatus): %v", err)
	}
	err = agent.streamEvent(ctx, sess.SessionId, engine.QueryEvent{
		Type: engine.EventHookResponse,
		HookResponse: &engine.HookResponseEvent{
			HookID: "async-hook-1", HookName: "lint", HookEvent: "PreToolUse",
			StatusMessage: "Running lint check...", Phase: "completed", Outcome: "failed", ExitCode: 1,
		},
	})
	if err != nil {
		t.Fatalf("streamEvent(EventHookResponse): %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	exts := client.getExtensions()
	var foundRunning, foundFailed bool
	for _, ext := range exts {
		if ext.Method == "_session/status" {
			var payload map[string]any
			if json.Unmarshal(ext.Params, &payload) == nil {
				if status, _ := payload["status"].(string); status == "hook_running" {
					if msg, _ := payload["message"].(string); msg == "Running lint check..." {
						foundRunning = true
					}
				}
				if status, _ := payload["status"].(string); status == "hook_failed" {
					if msg, _ := payload["message"].(string); msg == "Running lint check..." {
						foundFailed = true
					}
				}
			}
		}
	}
	if !foundRunning || !foundFailed {
		t.Errorf("expected _session/status extension with status=hook_running and message='Running lint check...', got extensions: %+v", exts)
	}
}

func TestACPAsyncRewakeWaitsForNextInboundPrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell hook assertion requires a POSIX shell")
	}
	model := &asyncRewakeTrackingModel{}
	clientConn, client, agent := setupTestACPWithAgent(t, model)
	releaseFile := filepath.Join(t.TempDir(), "release")
	writeACPAsyncRewakeHook(t, agent.config.CWD, releaseFile)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := clientConn.Initialize(ctx, acpsdk.InitializeRequest{ProtocolVersion: acpsdk.ProtocolVersionNumber}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	session, err := clientConn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: agent.config.CWD, McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := clientConn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: session.SessionId, Prompt: []acpsdk.ContentBlock{acpsdk.TextBlock("hello")},
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if calls := model.RewakeCalls(); calls != 0 {
		t.Fatalf("rewake model calls before hook release = %d, want 0", calls)
	}
	if err := os.WriteFile(releaseFile, []byte("release"), 0o600); err != nil {
		t.Fatalf("release hook: %v", err)
	}
	deadline := time.Now().Add(4 * time.Second)
	pending := false
	for !pending && time.Now().Before(deadline) {
		agent.mu.Lock()
		active := agent.sessions[session.SessionId]
		agent.mu.Unlock()
		pending = active != nil && len(active.Engine.RuntimeItems()) > 0
		time.Sleep(10 * time.Millisecond)
	}
	if !pending {
		t.Fatal("async rewake was not durably queued")
	}
	if calls := model.RewakeCalls(); calls != 0 {
		t.Fatalf("ACP fabricated a hidden rewake turn: calls=%d", calls)
	}
	if _, err := clientConn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: session.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("continue")},
	}); err != nil {
		t.Fatalf("next Prompt: %v", err)
	}
	if calls := model.RewakeCalls(); calls != 1 {
		t.Fatalf("rewake model calls at next inbound boundary = %d, want 1", calls)
	}

	wanted := map[string]bool{"hook_failed": false}
	for _, extension := range client.getExtensions() {
		if extension.Method != "_session/status" {
			continue
		}
		var payload map[string]any
		if json.Unmarshal(extension.Params, &payload) == nil {
			if status, _ := payload["status"].(string); status != "" {
				if _, ok := wanted[status]; ok {
					wanted[status] = true
				}
			}
		}
	}
	for status, found := range wanted {
		if !found {
			t.Errorf("missing ACP async hook status %q in %+v", status, client.getExtensions())
		}
	}
}

func writeACPAsyncRewakeHook(t *testing.T, cwd, releaseFile string) {
	t.Helper()
	hooksDir := filepath.Join(cwd, ".claude")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("MkdirAll hooks: %v", err)
	}
	hookConfig := map[string]any{"UserPromptSubmit": []any{map[string]any{
		"matcher": "*", "hooks": []any{map[string]any{
			"command":     fmt.Sprintf("while [ ! -f %q ]; do sleep 0.01; done; printf 'rewake policy' >&2; exit 2", releaseFile),
			"asyncRewake": true, "status_message": "Waiting for policy",
		}},
	}}}
	data, err := json.Marshal(hookConfig)
	if err != nil {
		t.Fatalf("Marshal hook config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), data, 0o600); err != nil {
		t.Fatalf("WriteFile hooks: %v", err)
	}
}

func TestAgentToolSelectionPreservesCLIState(t *testing.T) {
	unset := (&Agent{config: Config{ToolsFlag: []string{"Read"}}}).toolSelection()
	if unset != nil {
		t.Fatalf("unset tools flag produced selection %#v", unset)
	}

	explicit := (&Agent{config: Config{ToolsFlag: []string{"Bash,Read"}, ToolsFlagSet: true}}).toolSelection()
	if explicit == nil || len(explicit.Names) != 2 || explicit.Names[0] != "Bash" || explicit.Names[1] != "Read" {
		t.Fatalf("explicit tools flag produced selection %#v", explicit)
	}

	empty := (&Agent{config: Config{ToolsFlag: []string{""}, ToolsFlagSet: true}}).toolSelection()
	if empty == nil || empty.Preset != "" || len(empty.Names) != 0 {
		t.Fatalf("explicit empty tools flag produced selection %#v", empty)
	}
}
