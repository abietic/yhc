package acp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/schema"
	acpsdk "github.com/coder/acp-go-sdk"
)

// --- Tests for session management enhancements ---

func TestACP_ResumeSession(t *testing.T) {
	cwd := t.TempDir()
	transcriptDir := acpSessionTranscriptDir(cwd)
	const oldSessionID = "session-resume-test"

	// Pre-create a transcript on disk.
	recorder := transcript.NewRecorder(oldSessionID, transcriptDir)
	if err := recorder.ReplaceWithReplacements([]*schema.Message{
		{Role: schema.User, Content: "previous user message"},
		{Role: schema.Assistant, Content: "previous assistant response"},
	}, nil); err != nil {
		t.Fatal(err)
		return
	}
	planRoot, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(
		planRoot,
		".claude",
		"plans",
		oldSessionID+".md",
	)
	writeACPProjectGraphRootMetadata(
		t,
		recorder,
		&session.SessionMetadataFull{
			SessionID: oldSessionID, ThreadID: oldSessionID, CWD: cwd,
			PermissionMode: string(permission.ModePlan),
			PlanState: &session.PersistedPlanState{
				Version:          session.PersistedPlanStateVersion,
				Phase:            string(engine.PlanPhaseActive),
				PlanFileIdentity: planPath,
				ReturnMode:       string(permission.ModeDefault),
				Revision:         3,
			},
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			MessageCount: 2,
		},
	)
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "resumed response"},
		},
	}

	agent, err := NewAgent(Config{
		CWD:          cwd,
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
	})
	if err != nil {
		t.Fatal(err)
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

	// Initialize
	_, err = clientConn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	// Engine reconstruction is substantially slower under the race detector.
	// Give the operation its own deadline instead of spending the initialize
	// request's remaining budget.
	resumeCtx, resumeCancel := context.WithTimeout(
		context.Background(),
		60*time.Second,
	)
	defer resumeCancel()
	_, err = clientConn.ResumeSession(resumeCtx, acpsdk.ResumeSessionRequest{
		SessionId: acpsdk.SessionId(oldSessionID),
		Cwd:       cwd,
	})
	if err != nil {
		t.Fatalf("ResumeSession: %v", err)
		return
	}

	// Verify the session is now in the active sessions map.
	agent.mu.Lock()
	sess, ok := agent.sessions[acpsdk.SessionId(oldSessionID)]
	agent.mu.Unlock()
	if !ok {
		t.Fatal("expected session to be in active sessions after resume")
	}
	if sess.Engine == nil {
		t.Fatal("expected engine to be created for resumed session")
		return
	}

	// Verify the session has loaded the previous messages.
	msgs := sess.Engine.GetMessages()
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages from transcript, got %d", len(msgs))
	}
	if msgs[0].Content != "previous user message" {
		t.Fatalf("expected first message to be 'previous user message', got %q", msgs[0].Content)
	}
	if state := sess.Engine.PlanState(); state.Phase != engine.PlanPhaseActive ||
		state.PlanFileIdentity != planPath || state.Revision != 3 ||
		sess.Engine.PermissionMode() != permission.ModePlan {
		t.Fatalf("ACP resumed Plan state = %#v mode=%q", state, sess.Engine.PermissionMode())
	}
}

func TestACP_ResumeSession_AlreadyActive(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "hello"},
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

	// Create a session first
	sess, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
		return
	}

	// Resume the same session should succeed (idempotent)
	_, err = conn.ResumeSession(ctx, acpsdk.ResumeSessionRequest{
		SessionId: sess.SessionId,
		Cwd:       t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ResumeSession on active session should not error: %v", err)
		return
	}
}

func TestACPResumeReturnsImportRequiredWithoutPrivateMigration(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("YHC_SESSION_CATALOG", "")
	legacyDir := filepath.Join(project, ".eino-agent", "transcripts")
	legacyCatalog := filepath.Join(home, ".eino-agent", "session-roots.json")
	const sessionID = "acp-legacy-import"
	recorder := transcript.NewRecorder(sessionID, legacyDir)
	if err := recorder.Record([]*schema.Message{{Role: schema.User, Content: "legacy ACP"}}, false); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.RegisterSessionRoot(legacyCatalog, project, legacyDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	legacyBefore, err := os.ReadFile(filepath.Join(legacyDir, sessionID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	for _, operation := range []struct {
		name string
		run  func(*Agent) error
	}{
		{
			name: "resume",
			run: func(agent *Agent) error {
				_, err := agent.ResumeSession(t.Context(), acpsdk.ResumeSessionRequest{
					SessionId: sessionID,
					Cwd:       project,
				})
				return err
			},
		},
		{
			name: "load",
			run: func(agent *Agent) error {
				_, err := agent.LoadSession(t.Context(), acpsdk.LoadSessionRequest{
					SessionId: sessionID,
					Cwd:       project,
				})
				return err
			},
		},
	} {
		t.Run(operation.name, func(t *testing.T) {
			agent, err := NewAgent(Config{CWD: project})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(agent.Close)
			constructionCalls := 0
			agent.createRestoreStagingEngineFn = func(string, string) (*engine.QueryEngine, error) {
				constructionCalls++
				return nil, errors.New("restore construction must not run")
			}

			err = operation.run(agent)
			var requestErr *acpsdk.RequestError
			if !errors.As(err, &requestErr) || requestErr.Code != CodeLegacySessionImportRequired ||
				requestErr.Message != "legacy_session_import_required" {
				t.Fatalf("ACP %s error = %#v", operation.name, err)
			}
			data, ok := requestErr.Data.(map[string]any)
			if !ok || len(data) != 1 || data["sessionId"] != sessionID {
				t.Fatalf("ACP %s error data = %#v", operation.name, requestErr.Data)
			}
			if constructionCalls != 0 {
				t.Fatalf("ACP %s constructed restore engine %d time(s)", operation.name, constructionCalls)
			}
			if len(agent.sessions) != 0 {
				t.Fatalf("ACP %s registered a refused session", operation.name)
			}
		})
	}

	legacyAfter, err := os.ReadFile(filepath.Join(legacyDir, sessionID+".jsonl"))
	if err != nil || string(legacyAfter) != string(legacyBefore) {
		t.Fatalf("ACP refusal changed legacy transcript: %v", err)
	}
	for _, path := range []string{filepath.Join(project, ".yhc"), filepath.Join(home, ".yhc")} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("ACP refusal created %q: %v", path, statErr)
		}
	}
}

func TestP172ACPResumeMissingSessionDoesNotCreateCheckpoint(t *testing.T) {
	cwd := t.TempDir()
	const missingSessionID = "missing-session"

	agent, err := NewAgent(Config{
		CWD:          cwd,
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.mockModel = &mockChatModel{}
	t.Cleanup(agent.Close)

	_, err = agent.ResumeSession(context.Background(), acpsdk.ResumeSessionRequest{
		SessionId: acpsdk.SessionId(missingSessionID),
		Cwd:       cwd,
	})
	if err == nil {
		t.Fatal("ResumeSession unexpectedly accepted a missing transcript")
	}

	transcriptPath := transcript.NewRecorder(
		missingSessionID,
		acpSessionTranscriptDir(cwd),
	).Path()
	if _, statErr := os.Stat(transcriptPath); !os.IsNotExist(statErr) {
		t.Fatalf("failed resume created transcript %q: %v", transcriptPath, statErr)
	}

	agent.mu.Lock()
	_, active := agent.sessions[acpsdk.SessionId(missingSessionID)]
	agent.mu.Unlock()
	if active {
		t.Fatal("failed resume registered an active ACP session")
	}
}

func TestACP_LoadSession(t *testing.T) {
	cwd := t.TempDir()
	transcriptDir := acpSessionTranscriptDir(cwd)
	const loadSessionID = "session-load-test"

	// Pre-create a transcript on disk.
	recorder := transcript.NewRecorder(loadSessionID, transcriptDir)
	if err := recorder.ReplaceWithReplacements([]*schema.Message{
		{Role: schema.User, Content: "load test prompt"},
		{Role: schema.Assistant, Content: "load test response"},
	}, nil); err != nil {
		t.Fatal(err)
		return
	}
	planRoot, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(
		planRoot,
		".claude",
		"plans",
		loadSessionID+".md",
	)
	writeACPProjectGraphRootMetadata(
		t,
		recorder,
		&session.SessionMetadataFull{
			SessionID: loadSessionID, ThreadID: loadSessionID, CWD: cwd,
			PermissionMode: string(permission.ModePlan),
			PlanState: &session.PersistedPlanState{
				Version:           session.PersistedPlanStateVersion,
				Phase:             string(engine.PlanPhaseAwaitingApproval),
				PlanFileIdentity:  planPath,
				ReturnMode:        string(permission.ModeAcceptEdits),
				ApprovalRequestID: "exit-old",
				Revision:          8,
			},
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			MessageCount: 2,
		},
	)
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "new response"},
		},
	}

	agent, err := NewAgent(Config{
		CWD:          cwd,
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	agent.mockModel = mockModel

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Test LoadSession directly
	_, err = agent.LoadSession(ctx, acpsdk.LoadSessionRequest{
		SessionId:  acpsdk.SessionId(loadSessionID),
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
		return
	}

	// Verify the session is now in active sessions.
	agent.mu.Lock()
	sess, ok := agent.sessions[acpsdk.SessionId(loadSessionID)]
	agent.mu.Unlock()
	if !ok {
		t.Fatal("expected session to be active after LoadSession")
	}

	// Verify messages were loaded.
	msgs := sess.Engine.GetMessages()
	if len(msgs) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(msgs))
	}
	if state := sess.Engine.PlanState(); state.Phase != engine.PlanPhaseActive ||
		state.ApprovalRequestID != "" || state.Revision != 9 ||
		state.PlanFileIdentity != planPath {
		t.Fatalf("ACP loaded normalized Plan state = %#v", state)
	}
}

func TestACP_DeleteSession(t *testing.T) {
	cwd := t.TempDir()
	transcriptDir := acpSessionTranscriptDir(cwd)
	const deleteSessionID = "session-delete-test"

	if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(transcriptDir, deleteSessionID+".jsonl")
	ownedPaths := []string{
		transcriptPath,
		transcriptPath + ".tmp",
		transcriptPath + ".runtime-inputs.json",
		transcriptPath + ".project-graph-checkpoint.json",
	}
	for _, path := range ownedPaths {
		if err := os.WriteFile(path, []byte(`{"type":"message"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	neighborPath := transcriptPath + ".unowned"
	if err := os.WriteFile(neighborPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	agent, err := NewAgent(Config{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = agent.UnstableDeleteSession(ctx, acpsdk.UnstableDeleteSessionRequest{
		SessionId: acpsdk.SessionId(deleteSessionID),
	})
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	for _, path := range ownedPaths {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("owned session path %q still exists: %v", path, statErr)
		}
	}
	if data, readErr := os.ReadFile(neighborPath); readErr != nil {
		t.Fatalf("unowned neighbor was removed: %v", readErr)
	} else if string(data) != "keep" {
		t.Fatalf("unowned neighbor changed to %q", data)
	}
}

func TestACPDeleteSessionUsesObservedCrossCWDSessionRoot(t *testing.T) {
	projectA := t.TempDir()
	projectB := t.TempDir()
	sentinelPath := filepath.Join(projectA, "delete-sentinel")
	if err := os.WriteFile(sentinelPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	agent, err := NewAgent(Config{
		CWD:          projectA,
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.mockModel = &mockChatModel{}
	t.Cleanup(agent.Close)

	created, err := agent.NewSession(t.Context(), acpsdk.NewSessionRequest{
		Cwd:        projectB,
		McpServers: []acpsdk.McpServer{},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if _, err := agent.CloseSession(t.Context(), acpsdk.CloseSessionRequest{
		SessionId: created.SessionId,
	}); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	ownedPaths := acpDeleteSessionOwnedPaths(t, projectB, string(created.SessionId))
	if _, err := agent.UnstableDeleteSession(
		t.Context(),
		acpsdk.UnstableDeleteSessionRequest{SessionId: created.SessionId},
	); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	assertACPDeleteSessionPathsRemoved(t, ownedPaths)
	if got, err := os.ReadFile(sentinelPath); err != nil || string(got) != "keep" {
		t.Fatalf("default project sentinel = %q, %v", got, err)
	}
}

func TestACPDeleteSessionRebuildsRootFromList(t *testing.T) {
	projectA := t.TempDir()
	projectB := t.TempDir()
	const sessionID = "list-rebuild-root"
	ownedPaths := acpDeleteSessionFixture(t, projectB, sessionID)
	sentinelPath := filepath.Join(projectA, "delete-sentinel")
	if err := os.WriteFile(sentinelPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	agent, err := NewAgent(Config{CWD: projectA})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Close)

	listed, err := agent.ListSessions(t.Context(), acpsdk.ListSessionsRequest{Cwd: &projectB})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].SessionId != sessionID {
		t.Fatalf("ListSessions = %#v", listed.Sessions)
	}
	if _, err := agent.UnstableDeleteSession(
		t.Context(),
		acpsdk.UnstableDeleteSessionRequest{SessionId: sessionID},
	); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	assertACPDeleteSessionPathsRemoved(t, ownedPaths)
	if got, err := os.ReadFile(sentinelPath); err != nil || string(got) != "keep" {
		t.Fatalf("default project sentinel = %q, %v", got, err)
	}
}

func TestACPDeleteSessionRejectsAmbiguousObservedRoot(t *testing.T) {
	projectA := t.TempDir()
	projectB := t.TempDir()
	projectC := t.TempDir()
	const sessionID = "ambiguous-observed-root"
	projectBPaths := acpDeleteSessionFixture(t, projectB, sessionID)
	projectCPaths := acpDeleteSessionFixture(t, projectC, sessionID)
	before := acpDeleteSessionPathSnapshot(t, append(projectBPaths, projectCPaths...))

	agent, err := NewAgent(Config{CWD: projectA})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Close)
	for _, cwd := range []string{projectB, projectC} {
		listed, listErr := agent.ListSessions(
			t.Context(),
			acpsdk.ListSessionsRequest{Cwd: &cwd},
		)
		if listErr != nil {
			t.Fatalf("ListSessions(%q): %v", cwd, listErr)
		}
		if len(listed.Sessions) != 1 || listed.Sessions[0].SessionId != sessionID {
			t.Fatalf("ListSessions(%q) = %#v", cwd, listed.Sessions)
		}
	}

	_, err = agent.UnstableDeleteSession(
		t.Context(),
		acpsdk.UnstableDeleteSessionRequest{SessionId: sessionID},
	)
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) || requestErr.Code != CodeSessionConflict {
		t.Fatalf("DeleteSession error = %#v", err)
	}
	for _, root := range []string{projectB, projectC} {
		for _, value := range []string{
			requestErr.Message,
			requestErr.Error(),
			fmt.Sprint(requestErr.Data),
		} {
			if strings.Contains(value, root) {
				t.Fatalf("DeleteSession error leaked project root %q: %#v", root, requestErr)
			}
		}
	}
	assertACPDeleteSessionPathSnapshot(t, before)
}

func acpDeleteSessionFixture(t *testing.T, cwd, sessionID string) []string {
	t.Helper()
	recorder := transcript.NewRecorder(
		sessionID,
		acpSessionTranscriptDir(cwd),
	)
	if err := recorder.Record([]*schema.Message{{
		Role: schema.User, Content: "delete fixture prompt",
	}}, false); err != nil {
		t.Fatal(err)
	}
	writeACPProjectGraphRootMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: sessionID,
		ThreadID:  sessionID,
		CWD:       cwd,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	return acpDeleteSessionOwnedPaths(t, cwd, sessionID)
}

func acpDeleteSessionOwnedPaths(t *testing.T, cwd, sessionID string) []string {
	t.Helper()
	transcriptPath := transcript.NewRecorder(
		sessionID,
		acpSessionTranscriptDir(cwd),
	).Path()
	ownedPaths := []string{
		transcriptPath,
		transcriptPath + ".tmp",
		transcriptPath + ".runtime-inputs.json",
		transcriptPath + ".project-graph-checkpoint.json",
	}
	for _, path := range ownedPaths[1:] {
		if err := os.WriteFile(path, []byte(`{"type":"owned"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return ownedPaths
}

func assertACPDeleteSessionPathsRemoved(t *testing.T, paths []string) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned session path %q still exists: %v", path, err)
		}
	}
}

func acpDeleteSessionPathSnapshot(t *testing.T, paths []string) map[string][]byte {
	t.Helper()
	snapshot := make(map[string][]byte, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		snapshot[path] = data
	}
	return snapshot
}

func assertACPDeleteSessionPathSnapshot(t *testing.T, before map[string][]byte) {
	t.Helper()
	for path, want := range before {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("session path %q changed: %v", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("session path %q changed from %q to %q", path, want, got)
		}
	}
}

func TestACP_DeleteSession_ActiveRejectedWithoutMutation(t *testing.T) {
	cwd := t.TempDir()
	transcriptDir := acpSessionTranscriptDir(cwd)
	const deleteSessionID = "session-delete-active"

	recorder := transcript.NewRecorder(deleteSessionID, transcriptDir)
	if err := recorder.ReplaceWithReplacements([]*schema.Message{
		{Role: schema.User, Content: "delete test prompt"},
		{Role: schema.Assistant, Content: "delete test response"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	writeACPProjectGraphRootMetadata(
		t,
		recorder,
		&session.SessionMetadataFull{
			SessionID: deleteSessionID,
			ThreadID:  deleteSessionID,
			CWD:       cwd,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		},
	)
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
	}
	transcriptPath := recorder.Path()
	sidecarPath := transcriptPath + ".runtime-inputs.json"

	agent, err := NewAgent(Config{
		CWD:          cwd,
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.mockModel = &mockChatModel{
		responses: []*schema.Message{{Role: schema.Assistant, Content: "hello"}},
	}
	t.Cleanup(agent.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := agent.LoadSession(ctx, acpsdk.LoadSessionRequest{
		SessionId:  acpsdk.SessionId(deleteSessionID),
		Cwd:        cwd,
		McpServers: []acpsdk.McpServer{},
	}); err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if err := os.WriteFile(sidecarPath, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	agent.mu.Lock()
	activeBefore := agent.sessions[acpsdk.SessionId(deleteSessionID)]
	agent.mu.Unlock()
	if activeBefore == nil {
		t.Fatal("session should be active before delete")
	}

	if _, err := agent.UnstableDeleteSession(
		ctx,
		acpsdk.UnstableDeleteSessionRequest{
			SessionId: acpsdk.SessionId(deleteSessionID),
		},
	); err == nil {
		t.Fatal("expected active session deletion to fail")
	}

	agent.mu.Lock()
	activeAfter := agent.sessions[acpsdk.SessionId(deleteSessionID)]
	agent.mu.Unlock()
	if activeAfter != activeBefore {
		t.Fatal("active session registry changed after rejected deletion")
	}
	for _, path := range []string{transcriptPath, sidecarPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("active session path %q changed after rejected deletion: %v", path, err)
		}
	}
}

func TestACP_DeleteSession_SerializesSessionRegistration(t *testing.T) {
	cwd := t.TempDir()
	transcriptDir := acpSessionTranscriptDir(cwd)
	const sessionID = "session-delete-registration-race"
	if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(transcriptDir, sessionID+".jsonl")
	if err := os.WriteFile(transcriptPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	agent, err := NewAgent(Config{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Close)

	started := make(chan struct{})
	deleteDone := make(chan error, 1)
	agent.sessionLifecycleMu.Lock()
	go func() {
		close(started)
		_, deleteErr := agent.UnstableDeleteSession(
			context.Background(),
			acpsdk.UnstableDeleteSessionRequest{SessionId: sessionID},
		)
		deleteDone <- deleteErr
	}()
	<-started

	active := newSession(sessionID, nil, cwd)
	agent.mu.Lock()
	agent.sessions[sessionID] = active
	agent.mu.Unlock()
	agent.sessionLifecycleMu.Unlock()

	if err := <-deleteDone; err == nil {
		t.Fatal("expected deletion to observe the registered active session")
	}
	agent.mu.Lock()
	activeAfter := agent.sessions[sessionID]
	agent.mu.Unlock()
	if activeAfter != active {
		t.Fatal("serialized deletion changed the active session registry")
	}
	if data, err := os.ReadFile(transcriptPath); err != nil {
		t.Fatalf("serialized rejection removed transcript: %v", err)
	} else if string(data) != "keep" {
		t.Fatalf("serialized rejection changed transcript to %q", data)
	}
}

func writeACPProjectGraphRootMetadata(
	t *testing.T,
	recorder *transcript.Recorder,
	metadata *session.SessionMetadataFull,
) {
	t.Helper()
	metadata.QueryKernelVersion = "project_graph/v1"
	metadata.QueryKernelStage = "full"
	if err := session.WriteSessionMetadata(recorder, metadata); err != nil {
		t.Fatal(err)
	}
}

func TestACP_DeleteSession_UnsafeIDRejectedWithoutMutation(t *testing.T) {
	cwd := t.TempDir()
	transcriptDir := acpSessionTranscriptDir(cwd)
	if err := os.MkdirAll(transcriptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	victimPath := filepath.Join(filepath.Dir(transcriptDir), "victim.jsonl")
	if err := os.WriteFile(victimPath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	agent, err := NewAgent(Config{CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(agent.Close)

	if _, err := agent.UnstableDeleteSession(
		context.Background(),
		acpsdk.UnstableDeleteSessionRequest{SessionId: "../victim"},
	); err == nil {
		t.Fatal("expected unsafe session ID deletion to fail")
	}
	if data, err := os.ReadFile(victimPath); err != nil {
		t.Fatalf("unsafe deletion touched victim: %v", err)
	} else if string(data) != "keep" {
		t.Fatalf("unsafe deletion changed victim to %q", data)
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if len(agent.sessions) != 0 {
		t.Fatalf("unsafe deletion mutated active sessions: %v", agent.sessions)
	}
}

func TestACP_DeleteSession_NonExistent(t *testing.T) {
	agent, err := NewAgent(Config{
		CWD:          t.TempDir(),
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	agent.mockModel = &mockChatModel{}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Deleting a non-existent session should not error.
	_, err = agent.UnstableDeleteSession(ctx, acpsdk.UnstableDeleteSessionRequest{
		SessionId: "does-not-exist",
	})
	if err != nil {
		t.Fatalf("DeleteSession on non-existent should not error: %v", err)
		return
	}
}

func TestACP_InitializeCapabilities(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "hello"},
		},
	}

	conn, _ := setupTestACP(t, mockModel)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
		ProtocolVersion:    acpsdk.ProtocolVersionNumber,
		ClientCapabilities: acpsdk.ClientCapabilities{},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
		return
	}

	// Verify capabilities advertise session support.
	if !resp.AgentCapabilities.LoadSession {
		t.Error("expected LoadSession capability after durable replay landed")
	}
	if resp.AgentCapabilities.SessionCapabilities.List == nil {
		t.Error("expected List session capability to be non-nil")
	}
	if resp.AgentCapabilities.SessionCapabilities.Resume == nil {
		t.Error("expected Resume session capability to be non-nil")
	}
	if resp.AgentCapabilities.SessionCapabilities.Close == nil {
		t.Error("expected Close session capability to be non-nil")
	}
	if resp.AgentCapabilities.SessionCapabilities.Delete == nil {
		t.Error("expected Delete session capability to be non-nil")
	}
}

func TestACP_PromptResponse_IncludesMetadata(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "metadata response"},
		},
	}

	conn, _ := setupTestACP(t, mockModel)

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

	resp, err := conn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("hello")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
		return
	}

	if resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("expected end_turn, got %q", resp.StopReason)
	}

	// Verify Usage is populated.
	if resp.Usage == nil {
		t.Fatal("expected Usage to be non-nil in PromptResponse")
		return
	}

	// Verify Meta has model info.
	if resp.Meta == nil {
		t.Fatal("expected Meta to be non-nil in PromptResponse")
		return
	}
	if _, ok := resp.Meta["model"]; !ok {
		t.Error("expected Meta to contain 'model' key")
	}
	if _, ok := resp.Meta["session_id"]; !ok {
		t.Error("expected Meta to contain 'session_id' key")
	}
}

func TestACP_PermissionTimeout(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			// Model calls a tool
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "call_timeout_1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "Read",
						Arguments: `{"file_path": "/tmp/timeout_test.txt"}`,
					},
				}},
			},
			// Final response
			{Role: schema.Assistant, Content: "Timeout happened."},
		},
	}

	// Create agent with permissions enabled and very short timeout.
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
	agent.permissionTimeout = 100 * time.Millisecond // very short timeout

	c2aR, c2aW := io.Pipe()
	a2cR, a2cW := io.Pipe()

	// Client that never responds to permission requests (blocks until context done)
	slowClient := &slowPermissionClient{blocked: make(chan struct{})}
	agentConn := acpsdk.NewAgentSideConnection(agent, a2cW, c2aR)
	agent.SetConnection(agentConn)
	clientConn := acpsdk.NewClientSideConnection(slowClient, c2aW, a2cR)

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

	// Prompt should not deadlock — the permission timeout should kick in.
	_, err = clientConn.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sess.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("do something requiring permission")},
	})
	if err != nil {
		t.Fatalf("Prompt should complete even with permission timeout: %v", err)
		return
	}
}

// slowPermissionClient simulates a client that takes too long to respond to
// permission requests. It responds to all other methods normally.
type slowPermissionClient struct {
	mu      sync.Mutex
	updates []acpsdk.SessionNotification
	blocked chan struct{}
}

func (c *slowPermissionClient) ReadTextFile(ctx context.Context, p acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	return acpsdk.ReadTextFileResponse{Content: "mock"}, nil
}

func (c *slowPermissionClient) WriteTextFile(ctx context.Context, p acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.WriteTextFileResponse{}, nil
}

func (c *slowPermissionClient) RequestPermission(ctx context.Context, p acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	// Signal that we were called, then block until context is cancelled.
	select {
	case c.blocked <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return acpsdk.RequestPermissionResponse{}, ctx.Err()
}

func (c *slowPermissionClient) SessionUpdate(ctx context.Context, n acpsdk.SessionNotification) error {
	c.mu.Lock()
	c.updates = append(c.updates, n)
	c.mu.Unlock()
	return nil
}

func (c *slowPermissionClient) CreateTerminal(ctx context.Context, p acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{TerminalId: "test-terminal"}, nil
}

func (c *slowPermissionClient) KillTerminal(ctx context.Context, p acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	return acpsdk.KillTerminalResponse{}, nil
}

func (c *slowPermissionClient) ReleaseTerminal(ctx context.Context, p acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, nil
}

func (c *slowPermissionClient) TerminalOutput(ctx context.Context, p acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.TerminalOutputResponse{Output: "ok", Truncated: false}, nil
}

func (c *slowPermissionClient) WaitForTerminalExit(ctx context.Context, p acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.WaitForTerminalExitResponse{}, nil
}

func TestACP_CancelAcknowledgment(t *testing.T) {
	// Model that blocks until context is canceled.
	blockingModel := &blockingChatModel{blocked: make(chan struct{})}

	agent, err := NewAgent(Config{
		ProviderFlag: "mock",
		ModelFlag:    "mock-model",
		APIKeyFlag:   "mock-key",
		YoloMode:     true,
		MaxTurns:     10,
		CWD:          t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
		return
	}
	agent.mockModel = blockingModel

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

	// Start prompt in goroutine.
	type promptResult struct {
		resp acpsdk.PromptResponse
		err  error
	}
	promptDone := make(chan promptResult, 1)
	go func() {
		resp, err := clientConn.Prompt(ctx, acpsdk.PromptRequest{
			SessionId: sess.SessionId,
			Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("slow task")},
		})
		promptDone <- promptResult{resp, err}
	}()

	// Wait for model to start blocking.
	select {
	case <-blockingModel.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for model to start")
	}

	// Cancel the prompt.
	err = clientConn.Cancel(ctx, acpsdk.CancelNotification{SessionId: sess.SessionId})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
		return
	}

	// Prompt should finish.
	select {
	case result := <-promptDone:
		if result.err != nil {
			t.Logf("prompt returned error (may be expected): %v", result.err)
		} else if result.resp.StopReason != acpsdk.StopReasonCancelled {
			t.Logf("stop reason: %q (expected %q)", result.resp.StopReason, acpsdk.StopReasonCancelled)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for prompt to finish after cancel")
	}

	// A cancelled ACP request can return to the client before the server-side
	// prompt handler has finished its durable transcript/session cleanup.
	// Close joins that handler before TempDir cleanup removes the session CWD.
	agent.Close()
}

func TestConsumeACPQueryEventsDrainsAfterHandlerError(t *testing.T) {
	events := make(chan engine.QueryEvent)
	handled := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	handlerErr := errors.New("client delivery failed")
	resolver := &recordingACPPermissionResolver{}

	go func() {
		events <- engine.QueryEvent{Type: engine.EventAssistant}
		close(handled)
		<-release
		events <- engine.QueryEvent{
			Type: engine.EventPermissionRequest,
			PermissionRequest: &engine.PermissionRequestEvent{
				Source:    "project_graph",
				ToolUseID: "permission-after-delivery-error",
				PlanApproval: &engine.PlanApprovalRequest{
					RequestID:    "permission-after-delivery-error",
					PlanRevision: 7,
				},
			},
		}
		events <- engine.QueryEvent{
			Type: engine.EventTerminal,
			TerminalInfo: &engine.Terminal{
				Reason: engine.TerminalAbortedStreaming,
			},
		}
		close(events)
	}()

	var (
		terminal engine.TerminalReason
		err      error
		calls    int
	)
	go func() {
		terminal, err = consumeACPQueryEvents(
			events,
			nil,
			func(engine.QueryEvent) error {
				calls++
				return handlerErr
			},
			func(evt engine.QueryEvent, cause error) {
				settleACPProjectGraphPermissionAfterDeliveryFailure(
					resolver,
					evt,
					cause,
				)
			},
		)
		close(done)
	}()

	<-handled
	select {
	case <-done:
		t.Fatal("event consumer returned before the engine producer closed")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("event consumer did not finish after producer close")
	}
	if !errors.Is(err, handlerErr) {
		t.Fatalf("handler error = %v, want %v", err, handlerErr)
	}
	if terminal != engine.TerminalAbortedStreaming {
		t.Fatalf("terminal reason = %q", terminal)
	}
	if calls != 1 {
		t.Fatalf("transport handler calls = %d, want 1", calls)
	}
	if resolver.toolUseID != "permission-after-delivery-error" ||
		resolver.calls != 1 ||
		resolver.result.Decision != engine.PermissionCancelled ||
		resolver.result.PlanApproval == nil ||
		resolver.result.PlanApproval.RequestID !=
			"permission-after-delivery-error" ||
		resolver.result.PlanApproval.PlanRevision != 7 ||
		resolver.result.PlanApproval.Outcome != engine.PlanApprovalCancel ||
		resolver.result.PlanApproval.TargetMode != permission.ModePlan {
		t.Fatalf(
			"permission settlement = (%q, %#v)",
			resolver.toolUseID,
			resolver.result,
		)
	}
}

func TestACPDeliveryFailureCancelsProjectGraphPlanExactlyOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const sessionID = "acp-delivery-plan-cancel"
	if err := tools.SavePlan(sessionID, "", "# ACP delivery Plan\n"); err != nil {
		t.Fatal(err)
	}
	executions := 0
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{
		Info:                 &schema.ToolInfo{Name: "ExitPlanMode"},
		IsPlanModeTransition: true,
		Execute: func(string) (string, error) {
			executions++
			return "exited", nil
		},
	})
	root := t.TempDir()
	query := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:      sessionID,
		ThreadID:       sessionID + "-thread",
		TranscriptDir:  filepath.Join(root, "transcripts"),
		CWD:            root,
		PermissionMode: permission.ModePlan,
		ChatModel: &mockChatModel{responses: []*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:   sessionID + "-exit",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "ExitPlanMode",
					Arguments: `{}`,
				},
			}},
		}}},
		ToolRegistry:  registry,
		ToolSelection: &tools.ToolSelection{Names: []string{"ExitPlanMode"}},
		MaxTurns:      2,
		PermissionPrompt: func(
			context.Context,
			engine.PermissionPromptRequest,
		) engine.PermissionInteractionResult {
			t.Fatal("ProjectGraph called the blocking ACP Plan adapter")
			return engine.PermissionInteractionResult{
				Decision: engine.PermissionDeny,
			}
		},
	})
	t.Cleanup(query.Close)

	events, _ := query.SubmitMessage(context.Background(), "review Plan")
	var requestEvent engine.QueryEvent
	for event := range events {
		if event.Type == engine.EventPermissionRequest {
			requestEvent = event
		}
	}
	if requestEvent.PermissionRequest == nil ||
		requestEvent.PermissionRequest.PlanApproval == nil {
		t.Fatalf("ProjectGraph Plan request = %#v", requestEvent)
	}

	cause := errors.New("ACP client event delivery failed")
	settleACPProjectGraphPermissionAfterDeliveryFailure(
		query,
		requestEvent,
		cause,
	)
	settleACPProjectGraphPermissionAfterDeliveryFailure(
		query,
		requestEvent,
		cause,
	)
	items := query.RuntimeItems()
	if len(items) != 1 ||
		items[0].PermissionDecision == nil ||
		items[0].PermissionDecision.Result.Decision !=
			engine.PermissionCancelled ||
		items[0].PermissionDecision.Result.PlanApproval == nil ||
		items[0].PermissionDecision.Result.PlanApproval.Outcome !=
			engine.PlanApprovalCancel {
		t.Fatalf("ACP delivery failure runtime items = %#v", items)
	}

	item, ok, err := query.ClaimNextRuntimeItem()
	if err != nil || !ok {
		t.Fatalf("claim ACP cancellation item=%#v ok=%v err=%v", item, ok, err)
	}
	resumed, _ := query.SubmitRuntimeItem(context.Background(), item)
	for range resumed {
	}
	if executions != 0 ||
		query.PlanState().Phase != engine.PlanPhaseActive ||
		query.PermissionMode() != permission.ModePlan ||
		query.GetApprovalTracker().Count() != 0 ||
		len(query.RuntimeItems()) != 0 {
		t.Fatalf(
			"ACP delivery cancel executions=%d state=%#v mode=%q grants=%d items=%#v",
			executions,
			query.PlanState(),
			query.PermissionMode(),
			query.GetApprovalTracker().Count(),
			query.RuntimeItems(),
		)
	}
}

type recordingACPPermissionResolver struct {
	toolUseID string
	result    engine.PermissionInteractionResult
	calls     int
}

func (r *recordingACPPermissionResolver) ResolvePermissionInteraction(
	toolUseID string,
	result engine.PermissionInteractionResult,
) bool {
	r.calls++
	r.toolUseID = toolUseID
	r.result = result
	return true
}

func TestACP_EventStreaming_ToolCallLifecycle(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{{
					ID:   "call_evt_1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "Read",
						Arguments: `{"file_path": "/tmp/event_test.txt"}`,
					},
				}},
			},
			{Role: schema.Assistant, Content: "Event streaming test complete."},
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
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("read file")},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
		return
	}

	updates := client.getUpdates()

	var hasToolCallStart, hasToolCallFailed, hasText bool
	for _, u := range updates {
		if u.Update.ToolCall != nil {
			hasToolCallStart = true
			if u.Update.ToolCall.Title != "Read" {
				t.Errorf("tool call start title mismatch: got %q", u.Update.ToolCall.Title)
			}
		}
		if u.Update.ToolCallUpdate != nil &&
			u.Update.ToolCallUpdate.Status != nil &&
			*u.Update.ToolCallUpdate.Status == acpsdk.ToolCallStatusFailed {
			hasToolCallFailed = true
		}
		if u.Update.AgentMessageChunk != nil {
			hasText = true
		}
	}

	if !hasToolCallStart {
		t.Error("expected tool_call start event")
	}
	if !hasToolCallFailed {
		t.Error("expected failed terminal update for missing file")
	}
	if !hasText {
		t.Error("expected agent_message_chunk event")
	}
}

func TestACP_SessionCreatedAt(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "hello"},
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
		t.Fatal(err)
		return
	}
	agent.mockModel = mockModel

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	before := time.Now()
	sessResp, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatal(err)
		return
	}
	after := time.Now()

	agent.mu.Lock()
	sess := agent.sessions[sessResp.SessionId]
	agent.mu.Unlock()

	if sess == nil {
		t.Fatal("session should exist")
		return
	}
	if sess.CreatedAt.Before(before) || sess.CreatedAt.After(after) {
		t.Fatalf("session CreatedAt %v not between %v and %v", sess.CreatedAt, before, after)
	}
}

// Verify that disconnected/erroring clients don't block the agent.
func TestACP_DisconnectedClient_NoBlock(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "response despite disconnect"},
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
		t.Fatal(err)
		return
	}
	agent.mockModel = mockModel
	// No connection set — simulates disconnected client.
	agent.conn = nil

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sessResp, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
	if err != nil {
		t.Fatal(err)
		return
	}

	// Prompt should complete even without a connection.
	resp, err := agent.Prompt(ctx, acpsdk.PromptRequest{
		SessionId: sessResp.SessionId,
		Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("hello")},
	})
	if err != nil {
		t.Fatalf("Prompt with nil conn should not error: %v", err)
		return
	}
	if resp.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("expected end_turn, got %q", resp.StopReason)
	}
}

// Verify concurrent access to sessions doesn't race.
func TestACP_ConcurrentSessionAccess(t *testing.T) {
	mockModel := &mockChatModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, Content: "a"},
			{Role: schema.Assistant, Content: "b"},
			{Role: schema.Assistant, Content: "c"},
			{Role: schema.Assistant, Content: "d"},
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
		t.Fatal(err)
		return
	}
	agent.mockModel = mockModel

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create multiple sessions concurrently.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{}})
			if err != nil {
				t.Errorf("concurrent NewSession: %v", err)
			}
		}()
	}
	wg.Wait()

	// Verify all sessions exist.
	agent.mu.Lock()
	count := len(agent.sessions)
	agent.mu.Unlock()
	if count != 4 {
		t.Fatalf("expected 4 sessions, got %d", count)
	}
}

func TestACP_PermissionRegistryUsesActualSessionCWDAndCanonicalProjectIdentity(t *testing.T) {
	configuredRoot := t.TempDir()
	projectRoot := t.TempDir()
	aliasParent := t.TempDir()
	alias := filepath.Join(aliasParent, "project-alias")
	if err := os.Symlink(projectRoot, alias); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	otherRoot := t.TempDir()
	agent, err := NewAgent(Config{
		ProviderFlag: "mock", ModelFlag: "mock-model", APIKeyFlag: "mock-key",
		YoloMode: true, CWD: configuredRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.mockModel = &mockChatModel{}
	t.Cleanup(agent.Close)

	ctx := context.Background()
	first, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: projectRoot})
	if err != nil {
		t.Fatal(err)
	}
	second, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: alias})
	if err != nil {
		t.Fatal(err)
	}
	third, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: otherRoot})
	if err != nil {
		t.Fatal(err)
	}

	agent.mu.Lock()
	firstEngine := agent.sessions[first.SessionId].Engine
	secondEngine := agent.sessions[second.SessionId].Engine
	agent.mu.Unlock()
	if firstEngine.GetCWD() != projectRoot || secondEngine.GetCWD() != alias {
		t.Fatalf("engine CWDs = %q and %q, want operational roots %q and %q", firstEngine.GetCWD(), secondEngine.GetCWD(), projectRoot, alias)
	}
	canonicalProjectRoot, err := filepath.EvalSymlinks(projectRoot)
	if err != nil {
		t.Fatal(err)
	}
	if firstEngine.ExecutionBindingMatrix().Guest().Policy().Spec().CWD != canonicalProjectRoot ||
		secondEngine.ExecutionBindingMatrix().Guest().Policy().Spec().CWD != canonicalProjectRoot {
		t.Fatal("ACP operational CWD aliases did not share the canonical Guest root")
	}
	shared, ok := agent.permissionRegistry.CoordinatorForProject(projectRoot)
	if !ok || shared.EngineCount() != 2 {
		count := 0
		if shared != nil {
			count = shared.EngineCount()
		}
		t.Fatalf("same-project coordinator = %#v, engines = %d", shared, count)
	}
	if aliasCoordinator, found := agent.permissionRegistry.CoordinatorForProject(alias); !found || aliasCoordinator != shared {
		t.Fatal("canonical project alias did not reuse the coordinator")
	}
	if agent.permissionRegistry.ActiveProjectCount() != 2 {
		t.Fatalf("project runtimes = %d, want 2", agent.permissionRegistry.ActiveProjectCount())
	}

	_, _ = agent.CloseSession(ctx, acpsdk.CloseSessionRequest{SessionId: first.SessionId})
	if shared.EngineCount() != 1 || agent.permissionRegistry.ActiveProjectCount() != 2 {
		t.Fatal("non-final session close released the shared project runtime")
	}
	_, _ = agent.CloseSession(ctx, acpsdk.CloseSessionRequest{SessionId: second.SessionId})
	if _, found := agent.permissionRegistry.CoordinatorForProject(projectRoot); found {
		t.Fatal("final same-project session close retained the coordinator")
	}
	_, _ = agent.CloseSession(ctx, acpsdk.CloseSessionRequest{SessionId: third.SessionId})
	if agent.permissionRegistry.ActiveProjectCount() != 0 {
		t.Fatalf("project runtimes after close = %d", agent.permissionRegistry.ActiveProjectCount())
	}
}

// --- Compile-time interface checks ---
var (
	_ acpsdk.Agent       = (*Agent)(nil)
	_ acpsdk.AgentLoader = (*Agent)(nil)
)

func TestACP_ToolCallRawInputForwarded(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		arguments string
		check     func(*testing.T, any)
	}{
		{
			name:      "non-empty object",
			toolName:  "Read",
			arguments: `{"file_path": "/tmp/raw_input_test.txt"}`,
			check: func(t *testing.T, raw any) {
				t.Helper()
				m, ok := raw.(map[string]any)
				if !ok {
					t.Fatalf("rawInput type = %T, want map[string]any", raw)
				}
				if m["file_path"] != "/tmp/raw_input_test.txt" {
					t.Fatalf("rawInput file_path = %v", m["file_path"])
				}
			},
		},
		{
			name:      "empty object",
			toolName:  "TaskList",
			arguments: `{}`,
			check: func(t *testing.T, raw any) {
				t.Helper()
				m, ok := raw.(map[string]any)
				if !ok {
					t.Fatalf("rawInput type = %T, want map[string]any", raw)
				}
				if len(m) != 0 {
					t.Fatalf("rawInput = %#v, want empty object", raw)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockModel := &mockChatModel{
				responses: []*schema.Message{
					{
						Role: schema.Assistant,
						ToolCalls: []schema.ToolCall{{
							ID:   "call_raw_1",
							Type: "function",
							Function: schema.FunctionCall{
								Name:      tt.toolName,
								Arguments: tt.arguments,
							},
						}},
					},
					{Role: schema.Assistant, Content: "done."},
				},
			}

			conn, client := setupTestACP(t, mockModel)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if _, err := conn.Initialize(ctx, acpsdk.InitializeRequest{
				ProtocolVersion:    acpsdk.ProtocolVersionNumber,
				ClientCapabilities: acpsdk.ClientCapabilities{},
			}); err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			sess, err := conn.NewSession(ctx, acpsdk.NewSessionRequest{
				Cwd:        t.TempDir(),
				McpServers: []acpsdk.McpServer{},
			})
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			if _, err = conn.Prompt(ctx, acpsdk.PromptRequest{
				SessionId: sess.SessionId,
				Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("read file")},
			}); err != nil {
				t.Fatalf("Prompt: %v", err)
			}

			var raw any
			for _, u := range client.getUpdates() {
				if u.Update.ToolCallUpdate != nil &&
					u.Update.ToolCallUpdate.RawInput != nil {
					raw = u.Update.ToolCallUpdate.RawInput
				}
			}
			if raw == nil {
				t.Fatal("tool input update omitted rawInput")
			}
			tt.check(t, raw)
		})
	}
}
