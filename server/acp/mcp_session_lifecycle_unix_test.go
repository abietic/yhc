//go:build darwin || linux

package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	acpsdk "github.com/coder/acp-go-sdk"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/abietic/yhc/engine/transcript"
)

const (
	acpMCPHelperModeEnv    = "YHC_ACP_MCP_HELPER"
	acpMCPHelperToolsEnv   = "YHC_ACP_MCP_TOOLS"
	acpMCPHelperRecordEnv  = "YHC_ACP_MCP_RECORD"
	acpMCPHelperControlEnv = "YHC_ACP_MCP_CONTROL"
	acpMCPHelperSecretEnv  = "YHC_ACP_MCP_SECRET"
	acpMCPHelperDelayEnv   = "YHC_ACP_MCP_DELAY"
)

type acpMCPHelperRecord struct {
	PID    int      `json:"pid"`
	CWD    string   `json:"cwd"`
	Secret string   `json:"secret"`
	Args   []string `json:"args"`
}

type acpManualDeadlineContext struct {
	context.Context
	done chan struct{}
	once sync.Once
}

func newACPManualDeadlineContext() *acpManualDeadlineContext {
	return &acpManualDeadlineContext{
		Context: context.Background(),
		done:    make(chan struct{}),
	}
}

func (c *acpManualDeadlineContext) Done() <-chan struct{} {
	return c.done
}

func (c *acpManualDeadlineContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func (c *acpManualDeadlineContext) trigger() {
	c.once.Do(func() {
		close(c.done)
	})
}

func TestP235ACPStdioNewInvokeCloseAndPrivacy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	model := &mockChatModel{responses: []*schema.Message{{
		Role: schema.Assistant, Content: "done",
	}}}
	conn, _, agent := setupTestACPWithAgent(t, model)
	cwd := t.TempDir()
	recordPath := filepath.Join(t.TempDir(), "helper.json")
	const secret = "mcp-private-secret-value"
	descriptor := acpMCPTestDescriptor(
		t,
		"session-server",
		"echo",
		recordPath,
		"",
		secret,
	)

	created, err := conn.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{descriptor},
	})
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	record := waitForACPMCPHelperRecord(t, recordPath, 0)
	resolvedCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if record.CWD != resolvedCWD ||
		record.Secret != secret ||
		!containsString(record.Args, "-test.run=^TestP235ACPStdioMCPHelper$") {
		t.Fatalf("helper process input = %#v", record)
	}

	active := activeACPSession(t, agent, created.SessionId)
	if !containsString(active.Engine.GetToolNames(), "mcp__session-server__echo") {
		t.Fatalf("model tools = %v", active.Engine.GetToolNames())
	}
	result, err := active.Engine.GetMCPManager().CallServerTool(
		t.Context(),
		"session-server",
		"echo",
		map[string]any{"value": "hello"},
	)
	if err != nil || result != "echo:hello" {
		t.Fatalf("MCP invocation = %q, %v", result, err)
	}

	if _, err := conn.CloseSession(t.Context(), acpsdk.CloseSessionRequest{
		SessionId: created.SessionId,
	}); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}
	waitForACPProcessGone(t, record.PID)
	assertTreeExcludesStrings(t, cwd, secret, os.Args[0], recordPath)
}

func TestP235ACPStdioMultiServerFailureRollsBackEveryChild(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	conn, _, agent := setupTestACPWithAgent(t, &mockChatModel{})
	recordPath := filepath.Join(t.TempDir(), "helper.json")
	valid := acpMCPTestDescriptor(
		t,
		"valid-server",
		"echo",
		recordPath,
		"",
		"rollback-private-value",
	)
	invalid := validACPStdioServer("invalid-server")
	invalid.Stdio.Command = "/definitely/missing/eino-agent-mcp-server"

	_, err := conn.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        t.TempDir(),
		McpServers: []acpsdk.McpServer{valid, invalid},
	})
	requireACPMCPSetupError(t, err, "connect_failed", 1)
	record := waitForACPMCPHelperRecord(t, recordPath, 0)
	waitForACPProcessGone(t, record.PID)
	agent.mu.Lock()
	sessionCount := len(agent.sessions)
	agent.mu.Unlock()
	if sessionCount != 0 {
		t.Fatalf("failed setup registered %d sessions", sessionCount)
	}
}

func TestP235ACPStdioDiscoveredCollisionAbortsBeforeSessionVisibility(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	conn, _, agent := setupTestACPWithAgent(t, &mockChatModel{})
	recordPath := filepath.Join(t.TempDir(), "helper.json")
	descriptor := acpMCPTestDescriptor(
		t,
		"collision-server",
		"a.b|a_b",
		recordPath,
		"",
		"collision-private-value",
	)

	_, err := conn.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        t.TempDir(),
		McpServers: []acpsdk.McpServer{descriptor},
	})
	requireACPMCPSetupError(t, err, "tool_name_collision", 0)
	record := waitForACPMCPHelperRecord(t, recordPath, 0)
	waitForACPProcessGone(t, record.PID)
	agent.mu.Lock()
	sessionCount := len(agent.sessions)
	agent.mu.Unlock()
	if sessionCount != 0 {
		t.Fatalf("collision setup registered %d sessions", sessionCount)
	}
}

func TestP235ACPStdioSourceCollisionFailsBeforeLaunch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	conn, _, agent := setupTestACPWithAgent(t, &mockChatModel{})
	cwd := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(cwd, ".mcp.json"),
		[]byte(`{"mcpServers":{"same.name":{"command":"/not/launched"}}}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(t.TempDir(), "helper.json")
	descriptor := acpMCPTestDescriptor(
		t,
		"same_name",
		"echo",
		recordPath,
		"",
		"source-collision-private-value",
	)

	_, err := conn.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{descriptor},
	})
	requireACPMCPSetupError(t, err, "server_name_conflict", 0)
	if _, statErr := os.Stat(recordPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("client server launched before source collision: %v", statErr)
	}
	agent.mu.Lock()
	sessionCount := len(agent.sessions)
	agent.mu.Unlock()
	if sessionCount != 0 {
		t.Fatalf("source collision registered %d sessions", sessionCount)
	}
}

func TestP235ACPStdioSetupCancellationAndTimeoutCleanChildren(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, _, agent := setupTestACPWithAgent(t, &mockChatModel{})

	t.Run("canceled before launch", func(t *testing.T) {
		recordPath := filepath.Join(t.TempDir(), "helper.json")
		descriptor := acpMCPTestDescriptor(
			t,
			"canceled-server",
			"echo",
			recordPath,
			"",
			"canceled-private-value",
		)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{
			Cwd:        t.TempDir(),
			McpServers: []acpsdk.McpServer{descriptor},
		})
		requireACPMCPSetupError(t, err, "setup_canceled", 0)
		if _, statErr := os.Stat(recordPath); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("canceled setup launched child: %v", statErr)
		}
	})

	t.Run("deadline after launch", func(t *testing.T) {
		recordPath := filepath.Join(t.TempDir(), "helper.json")
		descriptor := acpMCPTestDescriptor(
			t,
			"timeout-server",
			"echo",
			recordPath,
			"",
			"timeout-private-value",
		)
		descriptor.Stdio.Env = append(descriptor.Stdio.Env, acpsdk.EnvVariable{
			Name: acpMCPHelperDelayEnv, Value: "2s",
		})
		ctx := newACPManualDeadlineContext()
		cwd := t.TempDir()
		result := make(chan error, 1)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{
				Cwd:        cwd,
				McpServers: []acpsdk.McpServer{descriptor},
			})
			result <- err
		}()
		t.Cleanup(func() {
			ctx.trigger()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("NewSession did not return after deadline trigger")
			}
		})

		record := waitForACPMCPHelperRecord(t, recordPath, 0)
		if err := syscall.Kill(record.PID, 0); err != nil {
			t.Fatalf("MCP helper process %d is not alive: %v", record.PID, err)
		}
		ctx.trigger()
		var err error
		select {
		case err = <-result:
		case <-time.After(5 * time.Second):
			t.Fatal("NewSession did not return after deadline trigger")
		}
		requireACPMCPSetupError(t, err, "setup_timeout", 0)
		waitForACPProcessGone(t, record.PID)
	})

	agent.mu.Lock()
	sessionCount := len(agent.sessions)
	agent.mu.Unlock()
	if sessionCount != 0 {
		t.Fatalf("failed setup registered %d sessions", sessionCount)
	}
}

func TestP235ACPStdioLoadDeliveryFailureAbortsPreparedGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	transcriptDir := acpSessionTranscriptDir(cwd)
	const sessionID = "p235-load-mcp-abort"
	path := p234bWriteReplayTranscript(
		t,
		transcriptDir,
		sessionID,
		p234bReplayRecord("entry-user", &schema.Message{
			Role: schema.User, Content: "must remain unchanged",
		}),
	)
	p234bAppendProjectGraphMetadata(t, transcriptDir, sessionID, cwd)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	agent := p234bNewReplayAgent(t, cwd)
	p234bAttachReplayWriter(t, agent, p234bFailUpdateWriter{
		sessionUpdate: "user_message_chunk",
	})
	recordPath := filepath.Join(t.TempDir(), "helper.json")
	descriptor := acpMCPTestDescriptor(
		t,
		"abort-server",
		"echo",
		recordPath,
		"",
		"abort-private-value",
	)

	if _, err := agent.LoadSession(t.Context(), acpsdk.LoadSessionRequest{
		SessionId:  sessionID,
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{descriptor},
	}); err == nil {
		t.Fatal("LoadSession succeeded after replay delivery failure")
	}
	record := waitForACPMCPHelperRecord(t, recordPath, 0)
	waitForACPProcessGone(t, record.PID)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed MCP load rewrote durable transcript")
	}
	agent.mu.Lock()
	_, active := agent.sessions[sessionID]
	agent.mu.Unlock()
	if active {
		t.Fatal("failed MCP load registered an active session")
	}
}

func TestP235ACPStdioResumeDeliveryFailureAbortsPreparedGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	source, _ := newP233ProtocolAgent(t, cwd, false)
	source.SetConnection(nil)
	created, err := source.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: created.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("persist resume state")},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.CloseSession(t.Context(), acpsdk.CloseSessionRequest{
		SessionId: created.SessionId,
	}); err != nil {
		t.Fatal(err)
	}
	path := transcript.NewRecorder(
		string(created.SessionId),
		acpSessionTranscriptDir(cwd),
	).Path()
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	target, _ := newP233ProtocolAgent(t, cwd, true)
	recordPath := filepath.Join(t.TempDir(), "helper.json")
	descriptor := acpMCPTestDescriptor(
		t,
		"resume-abort-server",
		"echo",
		recordPath,
		"",
		"resume-abort-private-value",
	)
	_, resumeErr := target.ResumeSession(t.Context(), acpsdk.ResumeSessionRequest{
		SessionId:  created.SessionId,
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{descriptor},
	})
	if resumeErr == nil {
		t.Fatal("ResumeSession succeeded after command delivery failure")
	}
	record := waitForACPMCPHelperRecord(t, recordPath, 0)
	waitForACPProcessGone(t, record.PID)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed MCP resume rewrote durable transcript")
	}
	target.mu.Lock()
	_, active := target.sessions[created.SessionId]
	target.mu.Unlock()
	if active {
		t.Fatal("failed MCP resume registered an active session")
	}
}

func TestP235ACPStdioLoadResumeAndExactActiveReconnect(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	model := &mockChatModel{responses: []*schema.Message{{
		Role: schema.Assistant, Content: "durable answer",
	}}}
	conn, client, agent := setupTestACPWithAgent(t, model)
	cwd := t.TempDir()
	recordPath := filepath.Join(t.TempDir(), "helper.json")
	descriptor := acpMCPTestDescriptor(
		t,
		"restore-server",
		"echo|shutdown",
		recordPath,
		"",
		"restore-private-value",
	)

	created, err := conn.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{descriptor},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstRecord := waitForACPMCPHelperRecord(t, recordPath, 0)
	if _, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: created.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("persist me")},
	}); err != nil {
		t.Fatalf("Prompt() error = %v", err)
	}
	if _, err := conn.CloseSession(t.Context(), acpsdk.CloseSessionRequest{
		SessionId: created.SessionId,
	}); err != nil {
		t.Fatal(err)
	}
	waitForACPProcessGone(t, firstRecord.PID)

	beforeLoad := len(client.getUpdates())
	if _, err := conn.LoadSession(t.Context(), acpsdk.LoadSessionRequest{
		SessionId: created.SessionId,
		Cwd:       cwd,
		McpServers: []acpsdk.McpServer{
			descriptor,
		},
	}); err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	secondRecord := waitForACPMCPHelperRecord(t, recordPath, firstRecord.PID)
	loadUpdates := client.getUpdates()[beforeLoad:]
	if !hasConversationUpdate(loadUpdates) {
		t.Fatalf("load emitted no durable conversation: %#v", loadUpdates)
	}
	loaded := activeACPSession(t, agent, created.SessionId)
	result, err := loaded.Engine.GetMCPManager().CallServerTool(
		t.Context(),
		"restore-server",
		"echo",
		map[string]any{"value": "loaded"},
	)
	if err != nil || result != "echo:loaded" {
		t.Fatalf("loaded MCP invocation = %q, %v", result, err)
	}
	if _, err := conn.CloseSession(t.Context(), acpsdk.CloseSessionRequest{
		SessionId: created.SessionId,
	}); err != nil {
		t.Fatal(err)
	}
	waitForACPProcessGone(t, secondRecord.PID)

	beforeResume := len(client.getUpdates())
	if _, err := conn.ResumeSession(t.Context(), acpsdk.ResumeSessionRequest{
		SessionId:  created.SessionId,
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{descriptor},
	}); err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	thirdRecord := waitForACPMCPHelperRecord(t, recordPath, secondRecord.PID)
	if updates := client.getUpdates()[beforeResume:]; hasConversationUpdate(updates) {
		t.Fatalf("resume replayed conversation: %#v", updates)
	}

	active := activeACPSession(t, agent, created.SessionId)
	_, _ = active.Engine.GetMCPManager().CallServerTool(
		t.Context(),
		"restore-server",
		"shutdown",
		map[string]any{},
	)
	waitForACPProcessGone(t, thirdRecord.PID)
	waitForToolAbsent(t, active.Engine.GetToolNames, "mcp__restore-server__echo")

	if _, err := conn.ResumeSession(t.Context(), acpsdk.ResumeSessionRequest{
		SessionId:  created.SessionId,
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{descriptor},
	}); err != nil {
		t.Fatalf("exact active ResumeSession() reconnect error = %v", err)
	}
	fourthRecord := waitForACPMCPHelperRecord(t, recordPath, thirdRecord.PID)
	if !containsString(active.Engine.GetToolNames(), "mcp__restore-server__echo") {
		t.Fatalf("reconnected model tools = %v", active.Engine.GetToolNames())
	}

	changed := descriptor
	changed.Stdio = &acpsdk.McpServerStdio{
		Name:    descriptor.Stdio.Name,
		Command: descriptor.Stdio.Command,
		Args:    append([]string(nil), descriptor.Stdio.Args...),
		Env:     append([]acpsdk.EnvVariable(nil), descriptor.Stdio.Env...),
	}
	changed.Stdio.Env = append(changed.Stdio.Env, acpsdk.EnvVariable{
		Name: "CHANGED", Value: "private-mismatch",
	})
	_, err = conn.ResumeSession(t.Context(), acpsdk.ResumeSessionRequest{
		SessionId:  created.SessionId,
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{changed},
	})
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) ||
		requestErr.Code != CodeSessionConflict ||
		containsRequestErrorFact(requestErr, "private-mismatch") {
		t.Fatalf("mismatched active resume error = %#v", err)
	}
	if _, err := conn.ResumeSession(t.Context(), acpsdk.ResumeSessionRequest{
		SessionId: created.SessionId,
		Cwd:       cwd,
	}); err != nil {
		t.Fatalf("empty active resume did not preserve setup: %v", err)
	}

	if _, err := conn.CloseSession(t.Context(), acpsdk.CloseSessionRequest{
		SessionId: created.SessionId,
	}); err != nil {
		t.Fatal(err)
	}
	waitForACPProcessGone(t, fourthRecord.PID)
	assertTreeExcludesStrings(
		t,
		cwd,
		"restore-private-value",
		os.Args[0],
		recordPath,
	)
}

func TestP235ACPStdioDynamicCollisionRemovesWholeServerGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	conn, _, agent := setupTestACPWithAgent(t, &mockChatModel{})
	recordPath := filepath.Join(t.TempDir(), "helper.json")
	controlPath := filepath.Join(t.TempDir(), "control")
	descriptor := acpMCPTestDescriptor(
		t,
		"dynamic-server",
		"echo",
		recordPath,
		controlPath,
		"dynamic-private-value",
	)
	created, err := conn.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        t.TempDir(),
		McpServers: []acpsdk.McpServer{descriptor},
	})
	if err != nil {
		t.Fatal(err)
	}
	record := waitForACPMCPHelperRecord(t, recordPath, 0)
	active := activeACPSession(t, agent, created.SessionId)
	if err := os.WriteFile(controlPath, []byte("collision"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForToolAbsent(t, active.Engine.GetToolNames, "mcp__dynamic-server__echo")
	waitForACPProcessGone(t, record.PID)
	if count := active.Engine.GetMCPManager().ServerToolCount("dynamic-server"); count != 0 {
		t.Fatalf("dynamic server retained %d manager tools", count)
	}
	if _, err := conn.CloseSession(t.Context(), acpsdk.CloseSessionRequest{
		SessionId: created.SessionId,
	}); err != nil {
		t.Fatal(err)
	}
}

func acpMCPTestDescriptor(
	t *testing.T,
	name string,
	toolNames string,
	recordPath string,
	controlPath string,
	secret string,
) acpsdk.McpServer {
	t.Helper()
	command, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	env := []acpsdk.EnvVariable{
		{Name: acpMCPHelperModeEnv, Value: "1"},
		{Name: acpMCPHelperToolsEnv, Value: toolNames},
		{Name: acpMCPHelperRecordEnv, Value: recordPath},
		{Name: acpMCPHelperSecretEnv, Value: secret},
	}
	if controlPath != "" {
		env = append(env, acpsdk.EnvVariable{
			Name: acpMCPHelperControlEnv, Value: controlPath,
		})
	}
	return acpsdk.McpServer{Stdio: &acpsdk.McpServerStdio{
		Name:    name,
		Command: command,
		Args:    []string{"-test.run=^TestP235ACPStdioMCPHelper$"},
		Env:     env,
	}}
}

func TestP235ACPStdioMCPHelper(*testing.T) {
	if os.Getenv(acpMCPHelperModeEnv) != "1" {
		return
	}
	record := acpMCPHelperRecord{
		PID:    os.Getpid(),
		Secret: os.Getenv(acpMCPHelperSecretEnv),
		Args:   append([]string(nil), os.Args[1:]...),
	}
	record.CWD, _ = os.Getwd()
	encoded, _ := json.Marshal(record)
	_ = os.WriteFile(os.Getenv(acpMCPHelperRecordEnv), encoded, 0o600)
	if delay, err := time.ParseDuration(os.Getenv(acpMCPHelperDelayEnv)); err == nil &&
		delay > 0 {
		time.Sleep(delay)
	}

	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name: "eino-agent-test-mcp", Version: "1",
	}, &sdkmcp.ServerOptions{
		Capabilities: &sdkmcp.ServerCapabilities{
			Tools: &sdkmcp.ToolCapabilities{ListChanged: true},
		},
	})
	addHelperTools(server, strings.Split(os.Getenv(acpMCPHelperToolsEnv), "|"))
	if controlPath := os.Getenv(acpMCPHelperControlEnv); controlPath != "" {
		go watchACPHelperControl(server, controlPath)
	}
	session, err := server.Connect(context.Background(), &sdkmcp.StdioTransport{}, nil)
	if err != nil {
		os.Exit(2)
	}
	_ = session.Wait()
	os.Exit(0)
}

func addHelperTools(server *sdkmcp.Server, names []string) {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		server.AddTool(&sdkmcp.Tool{
			Name:        name,
			Description: "test MCP tool",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"value": map[string]any{"type": "string"},
				},
			},
		}, func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			if name == "shutdown" {
				go func() {
					time.Sleep(20 * time.Millisecond)
					os.Exit(0)
				}()
				return &sdkmcp.CallToolResult{
					Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "shutting down"}},
				}, nil
			}
			var arguments map[string]any
			_ = json.Unmarshal(request.Params.Arguments, &arguments)
			value, _ := arguments["value"].(string)
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "echo:" + value}},
			}, nil
		})
	}
}

func watchACPHelperControl(server *sdkmcp.Server, path string) {
	for {
		content, err := os.ReadFile(path)
		if err == nil && strings.TrimSpace(string(content)) == "collision" {
			addHelperTools(server, []string{"a.b", "a_b"})
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func activeACPSession(
	t *testing.T,
	agent *Agent,
	sessionID acpsdk.SessionId,
) *Session {
	t.Helper()
	agent.mu.Lock()
	defer agent.mu.Unlock()
	active := agent.sessions[sessionID]
	if active == nil {
		t.Fatalf("session %q is not active", sessionID)
	}
	return active
}

func waitForACPMCPHelperRecord(
	t *testing.T,
	path string,
	previousPID int,
) acpMCPHelperRecord {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil {
			var record acpMCPHelperRecord
			if json.Unmarshal(content, &record) == nil &&
				record.PID > 0 &&
				record.PID != previousPID {
				return record
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for MCP helper record %q", path)
	return acpMCPHelperRecord{}
}

func waitForACPProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("MCP helper process %d survived cleanup", pid)
}

func waitForToolAbsent(t *testing.T, names func() []string, target string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !containsString(names(), target) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tool %q remained visible: %v", target, names())
}

func hasConversationUpdate(updates []acpsdk.SessionNotification) bool {
	for _, notification := range updates {
		update := notification.Update
		if update.UserMessageChunk != nil ||
			update.AgentMessageChunk != nil ||
			update.ToolCall != nil ||
			update.ToolCallUpdate != nil {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func requireACPMCPSetupError(
	t *testing.T,
	err error,
	reason string,
	descriptor int,
) {
	t.Helper()
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("setup error = %#v", err)
	}
	data, ok := requestErr.Data.(map[string]any)
	if !ok ||
		data["input"] != acpSessionMCPDescriptorInput ||
		data["reason"] != reason ||
		asInt(data["descriptor"]) != descriptor {
		t.Fatalf("setup request error = %#v", requestErr)
	}
	for _, private := range []string{
		"rollback-private-value",
		"collision-private-value",
		"/definitely/missing/eino-agent-mcp-server",
	} {
		if containsRequestErrorFact(requestErr, private) {
			t.Fatalf("setup error leaked descriptor: %v", requestErr)
		}
	}
}

func asInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case json.Number:
		result, _ := strconv.Atoi(string(typed))
		return result
	default:
		return -1
	}
}

func assertTreeExcludesStrings(
	t *testing.T,
	root string,
	values ...string,
) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, value := range values {
			if value != "" && strings.Contains(string(content), value) {
				return fmt.Errorf("durable file %q contains private setup bytes", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
