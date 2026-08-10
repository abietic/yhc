package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

func TestP233CommandSnapshotChangedOnlyAndRetrySettlement(t *testing.T) {
	t.Parallel()

	agent, wire := newP231WireCaptureAgent(t)
	acpSession := installP233CommandSession(t, agent, "command-snapshot")

	if err := agent.publishCommandSnapshot(t.Context(), acpSession, true); err != nil {
		t.Fatal(err)
	}
	firstDigest := acpSession.commandDigest
	if firstDigest == "" || !acpSession.commandSnapshotWasDelivered {
		t.Fatalf(
			"initial delivery state = digest %q delivered %v",
			firstDigest,
			acpSession.commandSnapshotWasDelivered,
		)
	}
	if err := agent.publishCommandSnapshot(t.Context(), acpSession, false); err != nil {
		t.Fatal(err)
	}
	if got := len(p233AvailableCommandUpdates(t, wire.Bytes())); got != 1 {
		t.Fatalf("unchanged update count = %d, want 1", got)
	}

	registerP233ACPCommand(t, acpSession.Engine, "p233-visible", "/p233-visible <path>")
	if err := agent.publishCommandSnapshot(t.Context(), acpSession, false); err != nil {
		t.Fatal(err)
	}
	updates := p233AvailableCommandUpdates(t, wire.Bytes())
	if len(updates) != 2 {
		t.Fatalf("changed update count = %d, want 2", len(updates))
	}
	p233RequireAvailableCommand(t, updates[1], "p233-visible", "<path>")
	if acpSession.commandDigest == firstDigest {
		t.Fatal("visible row change did not advance the full projection digest")
	}

	if err := agent.publishCommandSnapshot(t.Context(), acpSession, true); err != nil {
		t.Fatal(err)
	}
	if got := len(p233AvailableCommandUpdates(t, wire.Bytes())); got != 3 {
		t.Fatalf("forced update count = %d, want 3", got)
	}

	failingAgent := newP231FailingWireAgent(t)
	failingSession := installP233CommandSession(t, failingAgent, "command-retry")
	if err := failingAgent.publishCommandSnapshot(
		t.Context(),
		failingSession,
		false,
	); err == nil {
		t.Fatal("failed delivery returned nil")
	}
	if failingSession.commandSnapshotWasDelivered ||
		failingSession.commandDigest != "" {
		t.Fatalf(
			"failed delivery committed state: digest %q delivered %v",
			failingSession.commandDigest,
			failingSession.commandSnapshotWasDelivered,
		)
	}

	retryWire := attachP233WireCapture(t, failingAgent)
	if err := failingAgent.publishCommandSnapshot(
		t.Context(),
		failingSession,
		false,
	); err != nil {
		t.Fatal(err)
	}
	if got := len(p233AvailableCommandUpdates(t, retryWire.Bytes())); got != 1 {
		t.Fatalf("retry update count = %d, want 1", got)
	}
}

func TestP233CommandSnapshotRollbackFlag(t *testing.T) {
	t.Parallel()

	agent, wire := newP231WireCaptureAgent(t)
	agent.config.DisableACPCommandUpdates = true
	acpSession := installP233CommandSession(t, agent, "command-disabled")
	if err := agent.publishCommandSnapshot(t.Context(), acpSession, true); err != nil {
		t.Fatal(err)
	}
	if len(wire.Bytes()) != 0 {
		t.Fatalf("disabled command updates wrote wire bytes: %s", wire.Bytes())
	}
	if acpSession.commandSnapshotWasDelivered ||
		acpSession.commandDigest != "" {
		t.Fatal("disabled command updates committed delivery state")
	}
}

func TestP233CommandSnapshotProtocolBoundaries(t *testing.T) {
	cwd := t.TempDir()
	agent, wire := newP233ProtocolAgent(t, cwd, false)

	created, err := agent.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(p233AvailableCommandUpdates(t, wire.Bytes())); got != 1 {
		t.Fatalf("new-session updates = %d, want 1", got)
	}

	if _, err := agent.ResumeSession(t.Context(), acpsdk.ResumeSessionRequest{
		SessionId:  created.SessionId,
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = agent.LoadSession(t.Context(), acpsdk.LoadSessionRequest{
		SessionId:  created.SessionId,
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	})
	var conflict *acpsdk.RequestError
	if !errors.As(err, &conflict) || conflict.Code != CodeSessionConflict {
		t.Fatalf("active load conflict = %v", err)
	}
	if got := len(p233AvailableCommandUpdates(t, wire.Bytes())); got != 2 {
		t.Fatalf("new/active-resume updates = %d, want 2", got)
	}

	agent.mu.Lock()
	acpSession := agent.sessions[created.SessionId]
	agent.mu.Unlock()
	registerP233ACPCommand(t, acpSession.Engine, "p233-config", "/p233-config")
	currentModel := acpSession.Engine.GetModelName()
	if _, err := agent.SetSessionConfigOption(
		t.Context(),
		acpsdk.SetSessionConfigOptionRequest{
			ValueId: &acpsdk.SetSessionConfigOptionValueId{
				SessionId: created.SessionId,
				ConfigId:  "model",
				Value:     acpsdk.SessionConfigValueId(currentModel),
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	if got := len(p233AvailableCommandUpdates(t, wire.Bytes())); got != 3 {
		t.Fatalf("config-boundary updates = %d, want 3", got)
	}

	registerP233ACPCommand(t, acpSession.Engine, "p233-mode", "/p233-mode")
	if _, err := agent.SetSessionMode(t.Context(), acpsdk.SetSessionModeRequest{
		SessionId: created.SessionId,
		ModeId:    acpsdk.SessionModeId(permission.ModeDefault),
	}); err != nil {
		t.Fatal(err)
	}
	if got := len(p233AvailableCommandUpdates(t, wire.Bytes())); got != 4 {
		t.Fatalf("mode-boundary updates = %d, want 4", got)
	}

	registerP233ACPCommand(t, acpSession.Engine, "p233-prompt", "/p233-prompt")
	if _, err := agent.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: created.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("settle commands")},
	}); err != nil {
		t.Fatal(err)
	}
	if got := len(p233AvailableCommandUpdates(t, wire.Bytes())); got != 5 {
		t.Fatalf("prompt-boundary updates = %d, want 5", got)
	}
	if _, err := agent.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: created.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("no command change")},
	}); err != nil {
		t.Fatal(err)
	}
	if got := len(p233AvailableCommandUpdates(t, wire.Bytes())); got != 5 {
		t.Fatalf("unchanged prompt update count = %d, want 5", got)
	}

	forked, err := agent.UnstableForkSession(
		t.Context(),
		acpsdk.UnstableForkSessionRequest{
			SessionId: created.SessionId,
			Cwd:       cwd,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if forked.SessionId == "" {
		t.Fatal("fork returned an empty child ID")
	}
	updates := p233AvailableCommandUpdates(t, wire.Bytes())
	if len(updates) != 6 {
		t.Fatalf("fork-boundary updates = %d, want 6", len(updates))
	}
}

func TestP233InitialCommandDeliveryFailureCleanup(t *testing.T) {
	t.Run("new removes only new durable artifacts", func(t *testing.T) {
		cwd := t.TempDir()
		agent, _ := newP233ProtocolAgent(t, cwd, true)
		if _, err := agent.NewSession(t.Context(), acpsdk.NewSessionRequest{
			Cwd:        cwd,
			McpServers: []acpsdk.McpServer{},
		}); err == nil {
			t.Fatal("new session succeeded after required command delivery failed")
		}
		agent.mu.Lock()
		active := len(agent.sessions)
		agent.mu.Unlock()
		if active != 0 {
			t.Fatalf("failed new session left %d active sessions", active)
		}
		transcriptDir := acpSessionTranscriptDir(cwd)
		entries, err := os.ReadDir(transcriptDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".jsonl") {
				t.Fatalf("failed new session left transcript %q", entry.Name())
			}
		}
	})

	t.Run("restored resume and load retain durable transcript", func(t *testing.T) {
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
		if _, err := source.CloseSession(t.Context(), acpsdk.CloseSessionRequest{
			SessionId: created.SessionId,
		}); err != nil {
			t.Fatal(err)
		}
		path := transcript.NewRecorder(
			string(created.SessionId),
			acpSessionTranscriptDir(cwd),
		).Path()
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("durable source transcript: %v", err)
		}

		target, _ := newP233ProtocolAgent(t, cwd, true)
		if _, err := target.ResumeSession(t.Context(), acpsdk.ResumeSessionRequest{
			SessionId:  created.SessionId,
			Cwd:        cwd,
			McpServers: []acpsdk.McpServer{},
		}); err == nil {
			t.Fatal("restored resume succeeded after command delivery failed")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("resume failure removed durable transcript: %v", err)
		}
		if _, err := target.LoadSession(t.Context(), acpsdk.LoadSessionRequest{
			SessionId:  created.SessionId,
			Cwd:        cwd,
			McpServers: []acpsdk.McpServer{},
		}); err == nil {
			t.Fatal("restored load succeeded after command delivery failed")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("load failure removed durable transcript: %v", err)
		}
		target.mu.Lock()
		active := len(target.sessions)
		target.mu.Unlock()
		if active != 0 {
			t.Fatalf("failed restore left %d active sessions", active)
		}
	})

	t.Run("active resume and load retain live session", func(t *testing.T) {
		agent := newP231FailingWireAgent(t)
		acpSession := installP233CommandSession(t, agent, "active-retain")
		if _, err := agent.ResumeSession(t.Context(), acpsdk.ResumeSessionRequest{
			SessionId: acpSession.ID,
		}); err == nil {
			t.Fatal("active resume succeeded after command delivery failed")
		}
		if _, err := agent.LoadSession(t.Context(), acpsdk.LoadSessionRequest{
			SessionId: acpSession.ID,
		}); err == nil {
			t.Fatal("active load succeeded after command delivery failed")
		}
		agent.mu.Lock()
		retained := agent.sessions[acpSession.ID]
		agent.mu.Unlock()
		if retained != acpSession {
			t.Fatal("active load/resume failure removed or replaced the session")
		}
	})

	t.Run("fork removes child and durable branch", func(t *testing.T) {
		cwd := t.TempDir()
		agent, _ := newP233ProtocolAgent(t, cwd, false)
		agent.SetConnection(nil)
		created, err := agent.NewSession(t.Context(), acpsdk.NewSessionRequest{
			Cwd:        cwd,
			McpServers: []acpsdk.McpServer{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := agent.Prompt(t.Context(), acpsdk.PromptRequest{
			SessionId: created.SessionId,
			Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("persist source")},
		}); err != nil {
			t.Fatal(err)
		}
		attachP233FailingConnection(t, agent)
		if _, err := agent.UnstableForkSession(
			t.Context(),
			acpsdk.UnstableForkSessionRequest{
				SessionId: created.SessionId,
				Cwd:       cwd,
			},
		); err == nil {
			t.Fatal("fork succeeded after required command delivery failed")
		}
		agent.mu.Lock()
		active := len(agent.sessions)
		retained := agent.sessions[created.SessionId]
		agent.mu.Unlock()
		if active != 1 || retained == nil {
			t.Fatalf("fork cleanup active sessions = %d source=%v", active, retained != nil)
		}
		listed, err := session.ListSessions(session.ListOptions{
			Dir:           cwd,
			TranscriptDir: acpSessionTranscriptDir(cwd),
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(listed) != 1 || listed[0].SessionID != string(created.SessionId) {
			t.Fatalf("durable sessions after failed fork = %#v", listed)
		}
	})
}

func TestP233CommittedBoundaryRetainsStateOnCommandDeliveryFailure(t *testing.T) {
	t.Run("config", func(t *testing.T) {
		cwd := t.TempDir()
		agent, writer := newP233SelectiveProtocolAgent(t, cwd)
		created := newP233Session(t, agent, cwd)
		agent.mu.Lock()
		acpSession := agent.sessions[created.SessionId]
		agent.mu.Unlock()
		registerP233ACPCommand(t, acpSession.Engine, "p233-config-fail", "/p233-config-fail")
		deliveredDigest := acpSession.commandDigest
		writer.setFailAvailableCommands(true)

		_, err := agent.SetSessionConfigOption(
			t.Context(),
			acpsdk.SetSessionConfigOptionRequest{
				ValueId: &acpsdk.SetSessionConfigOptionValueId{
					SessionId: created.SessionId,
					ConfigId:  "model",
					Value:     "claude-sonnet-4-5",
				},
			},
		)
		if err == nil {
			t.Fatal("config returned success after command delivery failed")
		}
		if got := acpSession.Engine.GetModelName(); got != "claude-sonnet-4-5" {
			t.Fatalf("committed model = %q", got)
		}
		p233RequireRetainedUncommittedDigest(
			t,
			agent,
			acpSession,
			deliveredDigest,
		)
	})

	t.Run("mode", func(t *testing.T) {
		cwd := t.TempDir()
		agent, writer := newP233SelectiveProtocolAgent(t, cwd)
		created := newP233Session(t, agent, cwd)
		agent.mu.Lock()
		acpSession := agent.sessions[created.SessionId]
		agent.mu.Unlock()
		registerP233ACPCommand(t, acpSession.Engine, "p233-mode-fail", "/p233-mode-fail")
		deliveredDigest := acpSession.commandDigest
		writer.setFailAvailableCommands(true)

		if _, err := agent.SetSessionMode(t.Context(), acpsdk.SetSessionModeRequest{
			SessionId: created.SessionId,
			ModeId:    acpsdk.SessionModeId(permission.ModePlan),
		}); err == nil {
			t.Fatal("mode returned success after command delivery failed")
		}
		if got := acpSession.Engine.PermissionMode(); got != permission.ModePlan {
			t.Fatalf("committed mode = %q", got)
		}
		p233RequireRetainedUncommittedDigest(
			t,
			agent,
			acpSession,
			deliveredDigest,
		)
	})

	t.Run("prompt", func(t *testing.T) {
		cwd := t.TempDir()
		agent, writer := newP233SelectiveProtocolAgent(t, cwd)
		created := newP233Session(t, agent, cwd)
		agent.mu.Lock()
		acpSession := agent.sessions[created.SessionId]
		agent.mu.Unlock()
		registerP233ACPCommand(t, acpSession.Engine, "p233-prompt-fail", "/p233-prompt-fail")
		deliveredDigest := acpSession.commandDigest
		writer.setFailAvailableCommands(true)

		if _, err := agent.Prompt(t.Context(), acpsdk.PromptRequest{
			SessionId: created.SessionId,
			Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("commit before delivery failure")},
		}); err == nil {
			t.Fatal("prompt returned success after command delivery failed")
		}
		if got := len(acpSession.Engine.GetMessages()); got != 2 {
			t.Fatalf("committed prompt messages = %d, want 2", got)
		}
		p233RequireRetainedUncommittedDigest(
			t,
			agent,
			acpSession,
			deliveredDigest,
		)
	})
}

func TestP233PromptSettlementRefreshesOnEngineFailureAndCancellation(t *testing.T) {
	t.Run("engine failure", func(t *testing.T) {
		cwd := t.TempDir()
		agent, wire := newP233ProtocolAgent(t, cwd, false)
		agent.mockModel = p233FailingModel{}
		created := newP233Session(t, agent, cwd)
		agent.mu.Lock()
		acpSession := agent.sessions[created.SessionId]
		agent.mu.Unlock()
		registerP233ACPCommand(t, acpSession.Engine, "p233-engine-failure", "/p233-engine-failure")

		if _, err := agent.Prompt(t.Context(), acpsdk.PromptRequest{
			SessionId: created.SessionId,
			Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("fail model")},
		}); err != nil {
			t.Fatal(err)
		}
		updates := p233AvailableCommandUpdates(t, wire.Bytes())
		if len(updates) != 2 {
			t.Fatalf("engine-failure updates = %d, want 2", len(updates))
		}
		p233RequireAvailableCommand(
			t,
			updates[1],
			"p233-engine-failure",
			"",
		)
	})

	t.Run("cancellation", func(t *testing.T) {
		cwd := t.TempDir()
		blocking := &p233CancellationModel{started: make(chan struct{})}
		agent, wire := newP233ProtocolAgent(t, cwd, false)
		agent.mockModel = blocking
		created := newP233Session(t, agent, cwd)
		agent.mu.Lock()
		acpSession := agent.sessions[created.SessionId]
		agent.mu.Unlock()
		registerP233ACPCommand(t, acpSession.Engine, "p233-cancel", "/p233-cancel")

		type promptResult struct {
			response acpsdk.PromptResponse
			err      error
		}
		done := make(chan promptResult, 1)
		go func() {
			response, err := agent.Prompt(t.Context(), acpsdk.PromptRequest{
				SessionId: created.SessionId,
				Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("cancel model")},
			})
			done <- promptResult{response: response, err: err}
		}()
		select {
		case <-blocking.started:
		case <-t.Context().Done():
			t.Fatal("prompt did not reach blocking model")
		}
		if err := agent.Cancel(t.Context(), acpsdk.CancelNotification{
			SessionId: created.SessionId,
		}); err != nil {
			t.Fatal(err)
		}
		select {
		case result := <-done:
			if result.err != nil {
				t.Fatal(result.err)
			}
			if result.response.StopReason != acpsdk.StopReasonCancelled {
				t.Fatalf("stop reason = %q", result.response.StopReason)
			}
		case <-t.Context().Done():
			t.Fatal("cancelled prompt did not settle")
		}
		updates := p233AvailableCommandUpdates(t, wire.Bytes())
		if len(updates) != 2 {
			t.Fatalf("cancel updates = %d, want 2", len(updates))
		}
		p233RequireAvailableCommand(t, updates[1], "p233-cancel", "")
	})
}

func TestP233PromptReloadSuccessAndFailureCommandSnapshots(t *testing.T) {
	cwd := t.TempDir()
	pluginRoot := t.TempDir()
	pluginDir := filepath.Join(pluginRoot, "p233-plugin")
	p233WritePluginFixture(t, pluginDir, false, false)

	agent, wire := newP231WireCaptureAgent(t)
	sessionID := acpsdk.SessionId("p233-reload")
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:         string(sessionID),
		ThreadID:          string(sessionID),
		CWD:               cwd,
		TranscriptDir:     filepath.Join(cwd, "transcripts"),
		ChatModel:         &mockChatModel{},
		PermissionMode:    permission.ModeBypassPermissions,
		CommandEntrypoint: commands.EntrypointACP,
		PluginDirs:        []string{pluginRoot},
	})
	acpSession := newSession(sessionID, eng, cwd)
	agent.mu.Lock()
	agent.sessions[sessionID] = acpSession
	agent.mu.Unlock()
	if err := agent.publishCommandSnapshot(t.Context(), acpSession, true); err != nil {
		t.Fatal(err)
	}
	initial := p233AvailableCommandUpdates(t, wire.Bytes())
	if len(initial) != 1 {
		t.Fatalf("initial updates = %d, want 1", len(initial))
	}
	var resolverCalls atomic.Int64
	if err := acpSession.Engine.GetCommandRegistry().Register(&commands.Command{
		Name:        "p233-reload-probe",
		Description: "P23.3 reload recomputation probe",
		Usage:       "/p233-reload-probe",
		Entrypoints: commands.EntrypointsACP,
		ResolveAvailability: func(
			context.Context,
			*commands.CommandContext,
		) (commands.AvailabilityState, string) {
			resolverCalls.Add(1)
			return commands.AvailabilitySupported, ""
		},
		Execute: func(
			context.Context,
			*commands.CommandContext,
		) (*commands.CommandResult, error) {
			return &commands.CommandResult{}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := agent.publishCommandSnapshot(t.Context(), acpSession, false); err != nil {
		t.Fatal(err)
	}
	baselineCalls := resolverCalls.Load()
	if baselineCalls == 0 {
		t.Fatal("initial probe snapshot did not resolve availability")
	}

	p233WritePluginFixture(t, pluginDir, true, false)
	if _, err := acpSession.Engine.ReloadPromptCommands(); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("settle successful reload")},
	}); err != nil {
		t.Fatal(err)
	}
	afterSuccess := p233AvailableCommandUpdates(t, wire.Bytes())
	if len(afterSuccess) != 2 {
		t.Fatalf("successful invisible reload updates = %d, want unchanged 2", len(afterSuccess))
	}
	if resolverCalls.Load() <= baselineCalls {
		t.Fatal("successful reload settlement did not recompute the ACP snapshot")
	}
	if acpSession.Engine.GetCommandRegistry().Get("p233-plugin:second") == nil {
		t.Fatal("successful reload did not commit the new prompt command")
	}
	successCalls := resolverCalls.Load()

	p233WritePluginFixture(t, pluginDir, true, true)
	if _, err := acpSession.Engine.ReloadPromptCommands(); err == nil {
		t.Fatal("invalid prompt-command generation was accepted")
	}
	if _, err := agent.Prompt(t.Context(), acpsdk.PromptRequest{
		SessionId: sessionID,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("settle failed reload")},
	}); err != nil {
		t.Fatal(err)
	}
	afterFailure := p233AvailableCommandUpdates(t, wire.Bytes())
	if len(afterFailure) != 2 {
		t.Fatalf("failed reload updates = %d, want unchanged 2", len(afterFailure))
	}
	if resolverCalls.Load() <= successCalls {
		t.Fatal("failed reload settlement did not recompute the retained ACP snapshot")
	}
	if acpSession.Engine.GetCommandRegistry().Get("p233-plugin:second") == nil {
		t.Fatal("failed reload changed the previously committed generation")
	}
}

func installP233CommandSession(
	t *testing.T,
	agent *Agent,
	sessionID acpsdk.SessionId,
) *Session {
	t.Helper()
	cwd := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:         string(sessionID),
		ThreadID:          string(sessionID),
		CWD:               cwd,
		TranscriptDir:     filepath.Join(cwd, "transcripts"),
		CommandEntrypoint: commands.EntrypointACP,
	})
	acpSession := newSession(sessionID, eng, cwd)
	agent.mu.Lock()
	agent.sessions[sessionID] = acpSession
	agent.mu.Unlock()
	return acpSession
}

func p233WritePluginFixture(
	t *testing.T,
	pluginDir string,
	includeSecond bool,
	breakSecond bool,
) {
	t.Helper()
	commandDir := filepath.Join(pluginDir, "commands")
	if err := os.MkdirAll(commandDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(commandDir, "first.md"),
		[]byte("first prompt"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	commandsJSON := `{"name":"first","filePath":"commands/first.md"}`
	if includeSecond {
		secondPath := "commands/second.md"
		if breakSecond {
			secondPath = "commands/missing.md"
		} else if err := os.WriteFile(
			filepath.Join(commandDir, "second.md"),
			[]byte("second prompt"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		commandsJSON += `,{"name":"second","filePath":"` + secondPath + `"}`
	}
	manifest := `{"name":"p233-plugin","version":"1.0.0","commands":[` +
		commandsJSON +
		`]}`
	if err := os.WriteFile(
		filepath.Join(pluginDir, "plugin.json"),
		[]byte(manifest),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
}

func registerP233ACPCommand(
	t *testing.T,
	eng *engine.QueryEngine,
	name string,
	usage string,
) {
	t.Helper()
	err := eng.GetCommandRegistry().Register(&commands.Command{
		Name:        name,
		Description: "P23.3 " + name,
		Usage:       usage,
		Entrypoints: commands.EntrypointsACP,
		Execute: func(
			context.Context,
			*commands.CommandContext,
		) (*commands.CommandResult, error) {
			return &commands.CommandResult{}, nil
		},
	})
	if err != nil {
		t.Fatalf("register /%s: %v", name, err)
	}
}

func p233AvailableCommandUpdates(
	t *testing.T,
	wire []byte,
) []map[string]any {
	t.Helper()
	if len(bytes.TrimSpace(wire)) == 0 {
		return nil
	}
	var messages []map[string]any
	if err := jsonUnmarshalP233Wire(wire, &messages); err != nil {
		t.Fatal(err)
	}
	updates := make([]map[string]any, 0)
	for _, message := range messages {
		params, _ := message["params"].(map[string]any)
		update, _ := params["update"].(map[string]any)
		if update["sessionUpdate"] == "available_commands_update" {
			updates = append(updates, update)
		}
	}
	return updates
}

func jsonUnmarshalP233Wire(wire []byte, target any) error {
	lines := bytes.Split(bytes.TrimSpace(wire), []byte{'\n'})
	messages := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var message map[string]any
		if err := json.Unmarshal(line, &message); err != nil {
			return err
		}
		messages = append(messages, message)
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func p233RequireAvailableCommand(
	t *testing.T,
	update map[string]any,
	name string,
	hint string,
) {
	t.Helper()
	rows, _ := update["availableCommands"].([]any)
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if row["name"] != name {
			continue
		}
		if hint == "" {
			if _, present := row["input"]; present {
				t.Fatalf("command %q unexpectedly exposed input: %#v", name, row)
			}
		} else {
			input, _ := row["input"].(map[string]any)
			if got := input["hint"]; got != hint {
				t.Fatalf("command %q hint = %#v, want %q", name, got, hint)
			}
		}
		if got := row["description"]; got != "P23.3 "+name {
			t.Fatalf("command %q description = %#v", name, got)
		}
		return
	}
	t.Fatalf("available commands missing %q: %#v", name, rows)
}

func newP233Session(
	t *testing.T,
	agent *Agent,
	cwd string,
) acpsdk.NewSessionResponse {
	t.Helper()
	created, err := agent.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func p233RequireRetainedUncommittedDigest(
	t *testing.T,
	agent *Agent,
	acpSession *Session,
	deliveredDigest string,
) {
	t.Helper()
	agent.mu.Lock()
	retained := agent.sessions[acpSession.ID]
	agent.mu.Unlock()
	if retained != acpSession {
		t.Fatal("delivery failure removed or replaced the active session")
	}
	if acpSession.commandDigest != deliveredDigest ||
		!acpSession.commandSnapshotWasDelivered {
		t.Fatalf(
			"delivery failure changed digest state: digest %q delivered %v",
			acpSession.commandDigest,
			acpSession.commandSnapshotWasDelivered,
		)
	}
}

func newP233ProtocolAgent(
	t *testing.T,
	cwd string,
	failWrites bool,
) (*Agent, *p231WireBuffer) {
	t.Helper()
	agent, err := NewAgent(Config{
		ProviderFlag: "mock",
		ModelFlag:    "claude-sonnet-4-5-20250929",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
		CWD:          cwd,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.mockModel = &mockChatModel{}
	wire := newP231WireBuffer()
	if failWrites {
		attachP233FailingConnection(t, agent)
	} else {
		attachP233Writer(t, agent, wire)
	}
	t.Cleanup(agent.Close)
	return agent, wire
}

func attachP233WireCapture(t *testing.T, agent *Agent) *p231WireBuffer {
	t.Helper()
	wire := newP231WireBuffer()
	attachP233Writer(t, agent, wire)
	return wire
}

func attachP233FailingConnection(t *testing.T, agent *Agent) {
	t.Helper()
	attachP233Writer(t, agent, p231FailingWriter{})
}

func attachP233Writer(t *testing.T, agent *Agent, writer io.Writer) {
	t.Helper()
	inboundReader, inboundWriter := io.Pipe()
	agent.SetConnection(acpsdk.NewAgentSideConnection(
		agent,
		writer,
		inboundReader,
	))
	t.Cleanup(func() {
		_ = inboundWriter.Close()
	})
}

type p233SelectiveWriter struct {
	mu                    sync.Mutex
	wire                  *p231WireBuffer
	failAvailableCommands bool
}

func (w *p233SelectiveWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	fail := w.failAvailableCommands &&
		bytes.Contains(
			p,
			[]byte(`"sessionUpdate":"available_commands_update"`),
		)
	w.mu.Unlock()
	if fail {
		return 0, errors.New("available commands delivery failed")
	}
	return w.wire.Write(p)
}

func (w *p233SelectiveWriter) setFailAvailableCommands(fail bool) {
	w.mu.Lock()
	w.failAvailableCommands = fail
	w.mu.Unlock()
}

func newP233SelectiveProtocolAgent(
	t *testing.T,
	cwd string,
) (*Agent, *p233SelectiveWriter) {
	t.Helper()
	agent, err := NewAgent(Config{
		ProviderFlag: "mock",
		ModelFlag:    "claude-sonnet-4-5-20250929",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
		CWD:          cwd,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.mockModel = &mockChatModel{}
	writer := &p233SelectiveWriter{wire: newP231WireBuffer()}
	attachP233Writer(t, agent, writer)
	t.Cleanup(agent.Close)
	return agent, writer
}

type p233FailingModel struct{}

func (p233FailingModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return nil, errors.New("P23.3 model failure")
}

func (p233FailingModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("P23.3 model failure")
}

type p233CancellationModel struct {
	once    sync.Once
	started chan struct{}
}

func (m *p233CancellationModel) Generate(
	ctx context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	m.once.Do(func() { close(m.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (m *p233CancellationModel) Stream(
	ctx context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.once.Do(func() { close(m.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}
