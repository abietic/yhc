package acp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/permission"
	"github.com/cloudwego/eino/schema"
	acpsdk "github.com/coder/acp-go-sdk"
)

func TestACPSandboxSelectionIsServerOwned(t *testing.T) {
	agent, err := NewAgent(Config{})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := agent.resolveSandboxSelection(nil)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Profile != containment.ProfileWorkspaceWrite || selection.Source != containment.SelectionDefault {
		t.Fatalf("ACP default selection = %#v", selection)
	}

	agent, err = NewAgent(Config{SandboxProfileFlag: "danger-full-access", SandboxProfileFlagSet: true})
	if err != nil {
		t.Fatal(err)
	}
	selection, err = agent.resolveSandboxSelection(&config.Config{Sandbox: &config.SandboxConfig{GuestProfile: "workspace-write"}})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Profile != containment.ProfileDangerFullAccess || selection.Source != containment.SelectionCLI {
		t.Fatalf("ACP CLI selection = %#v", selection)
	}
}

func TestP511ACPEmitsOneDangerDiagnostic(t *testing.T) {
	cwd := t.TempDir()
	agent, err := NewAgent(Config{
		CWD: cwd, SandboxProfileFlag: "danger-full-access", SandboxProfileFlagSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.mockModel = &mockChatModel{}
	var output bytes.Buffer
	agent.sandboxDiagnosticOut = &output
	first, err := agent.createEngine("p51-danger-one", cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := agent.createEngine("p51-danger-two", cwd)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if got := strings.Count(output.String(), "sandbox-danger-full-access"); got != 1 {
		t.Fatalf("ACP danger diagnostics = %d, output %q", got, output.String())
	}
	if strings.Contains(output.String(), cwd) {
		t.Fatalf("ACP danger diagnostic leaked CWD: %q", output.String())
	}
}

func TestP511ACPEngineUsesServerOwnedBindingMatrix(t *testing.T) {
	defaultAdapter := containment.AdapterDarwinSeatbelt
	if runtime.GOOS == "linux" {
		defaultAdapter = containment.AdapterLinuxBubblewrap
	}
	for _, test := range []struct {
		name    string
		config  Config
		profile containment.Profile
		adapter containment.AdapterFamily
	}{
		{name: "default workspace", config: Config{}, profile: containment.ProfileWorkspaceWrite, adapter: defaultAdapter},
		{name: "explicit danger", config: Config{SandboxProfileFlag: "danger-full-access", SandboxProfileFlagSet: true}, profile: containment.ProfileDangerFullAccess, adapter: containment.AdapterAmbientHost},
	} {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			test.config.CWD = cwd
			agent, err := NewAgent(test.config)
			if err != nil {
				t.Fatal(err)
			}
			agent.mockModel = &mockChatModel{}
			eng, err := agent.createEngine("p51-acp", cwd)
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			bindings := eng.ExecutionBindingMatrix()
			if bindings.Guest().Policy().Spec().Profile != test.profile || bindings.Guest().AdapterFamily() != test.adapter ||
				bindings.ShellHooks().AdapterFamily() != containment.AdapterAmbientHost || bindings.StdioMCP().AdapterFamily() != containment.AdapterAmbientHost {
				t.Fatalf("ACP binding matrix Guest=%#v hook=%#v MCP=%#v", bindings.Guest().Diagnostic(), bindings.ShellHooks().Diagnostic(), bindings.StdioMCP().Diagnostic())
			}
			if test.profile == containment.ProfileDangerFullAccess && eng.GuestExecutionProof() != (containment.ExecutionProof{}) {
				t.Fatal("ACP danger Guest fabricated containment proof")
			}
		})
	}
}

func TestP512ACPAutoOrdinaryBashEntrypoint(t *testing.T) {
	conn, client, _, sessionID, root, available := p512ACPEntrypointSession(t,
		"ordinary-bash",
		`{"command":"printf p512-ordinary > ordinary.txt"}`,
	)
	client.permissionHandler = func(
		_ context.Context,
		request acpsdk.RequestPermissionRequest,
	) (acpsdk.RequestPermissionResponse, error) {
		if !available {
			return acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{
				Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: "reject"},
			}}, nil
		}
		return acpsdk.RequestPermissionResponse{},
			fmt.Errorf("ordinary Bash unexpectedly requested permission: %#v", request)
	}

	response, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("run ordinary Bash")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("ordinary ACP Bash stop reason = %q", response.StopReason)
	}
	client.mu.Lock()
	calls := append([]acpsdk.ToolCallId(nil), client.permissionRequestCallIDs...)
	client.mu.Unlock()
	if available && len(calls) != 0 {
		t.Fatalf("complete Darwin Guest proof permission calls = %#v", calls)
	}
	if available {
		contents, readErr := os.ReadFile(filepath.Join(root, "ordinary.txt"))
		if readErr != nil || string(contents) != "p512-ordinary" {
			t.Fatalf("ordinary ACP Bash output=%q err=%v", contents, readErr)
		}
	}
	if !available && len(calls) != 1 {
		t.Fatalf("unavailable Guest Bash did not fail closed: calls=%#v", calls)
	}
	if !available {
		if _, statErr := os.Stat(filepath.Join(root, "ordinary.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("unavailable Guest Bash created output: %v", statErr)
		}
	}
}

func TestP512ACPCriticalBashEntrypoint(t *testing.T) {
	conn, client, _, sessionID, _, _ := p512ACPEntrypointSession(t,
		"critical-bash",
		`{"command":"rm -f /"}`,
	)
	var options []string
	client.permissionHandler = func(
		_ context.Context,
		request acpsdk.RequestPermissionRequest,
	) (acpsdk.RequestPermissionResponse, error) {
		for _, option := range request.Options {
			options = append(options, string(option.OptionId))
		}
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{
			Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: "reject"},
		}}, nil
	}

	if _, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("run critical Bash")},
	}); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	calls := append([]acpsdk.ToolCallId(nil), client.permissionRequestCallIDs...)
	client.mu.Unlock()
	if len(calls) != 1 || !slices.Equal(options, []string{"allow", "reject"}) {
		t.Fatalf("critical Bash live permission calls/options = %#v/%#v", calls, options)
	}
}

func TestP512ACPCriticalBashRejectsForgedPersistentDecision(t *testing.T) {
	conn, client, agent, sessionID, _, _ := p512ACPEntrypointSession(t,
		"critical-bash-forged-always",
		`{"command":"rm -f /"}`,
	)
	client.permissionHandler = func(
		_ context.Context,
		request acpsdk.RequestPermissionRequest,
	) (acpsdk.RequestPermissionResponse, error) {
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.RequestPermissionOutcome{
			Selected: &acpsdk.RequestPermissionOutcomeSelected{OptionId: "allow_always"},
		}}, nil
	}
	if _, err := conn.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("forge persistent critical Bash approval")},
	}); err != nil {
		t.Fatal(err)
	}
	agent.mu.Lock()
	eng := agent.sessions[sessionID].Engine
	agent.mu.Unlock()
	if approvals := eng.GetApprovalTracker().List(); len(approvals) != 0 {
		t.Fatalf("forged ACP persistent decision created approvals: %#v", approvals)
	}
}

func p512ACPEntrypointSession(
	t *testing.T,
	callID, arguments string,
) (*acpsdk.ClientSideConnection, *testClient, *Agent, acpsdk.SessionId, string, bool) {
	t.Helper()
	model := &mockChatModel{responses: []*schema.Message{{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID: callID, Type: "function",
			Function: schema.FunctionCall{Name: "Bash", Arguments: arguments},
		}},
	}, {Role: schema.Assistant, Content: "done"}}}
	conn, client, agent := setupTestACPWithAgent(t, model)
	root := t.TempDir()
	agent.config.YoloMode = false
	agent.config.CWD = root
	agent.config.PermissionModeFlag = string(permission.ModeAuto)
	agent.config.SandboxProfileFlag = string(containment.ProfileWorkspaceWrite)
	agent.config.SandboxProfileFlagSet = true
	if _, err := conn.Initialize(t.Context(), acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
	}); err != nil {
		t.Fatal(err)
	}
	session, err := conn.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd: root, McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.mu.Lock()
	engine := agent.sessions[session.SessionId].Engine
	agent.mu.Unlock()
	guest := engine.ExecutionBindingMatrix().Guest()
	if guest.Policy().Spec().Entrypoint != containment.EntrypointACP {
		t.Fatalf("Guest entrypoint = %q, want ACP", guest.Policy().Spec().Entrypoint)
	}
	return conn, client, agent, session.SessionId, root,
		guest.Availability() == containment.BindingAvailable && guest.AdapterFamily() == containment.AdapterDarwinSeatbelt
}
