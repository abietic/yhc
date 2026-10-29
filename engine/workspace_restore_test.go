package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/filehistory"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func newRestoreEngineFixture(t *testing.T, decision PermissionInteractionDecision) (*QueryEngine, string, string, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	if !filehistory.RestoreFilesystemSupported() {
		t.Skip("guarded restore unsupported")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "dirty.txt")
	if err := os.WriteFile(path, []byte("user dirty\r\nlast"), 0o640); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	tools.RegisterDefaults(registry)
	calls, prompts := &atomic.Int32{}, &atomic.Int32{}
	fixture := &funcModel{fn: func(ctx context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		n := calls.Add(1)
		tool := ""
		input := map[string]string{"file_path": path}
		switch n {
		case 1, 6:
			tool = "Read"
		case 2, 4, 7:
			tool = "Write"
			input["content"] = "agent content\n"
		default:
			return &schema.Message{Role: schema.Assistant, Content: "fixture done"}, nil
		}
		if n == 4 {
			seen := false
			for _, message := range messages {
				if message.Role == schema.System && strings.Contains(message.Content, "Local workspace recovery changed captured files:") {
					seen = true
				}
			}
			if !seen {
				t.Error("next model request omitted the reread notice")
			}
		}
		args, _ := json.Marshal(input)
		return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: fmt.Sprintf("restore-fixture-%d", n), Type: "function", Function: schema.FunctionCall{Name: tool, Arguments: string(args)}}}}, nil
	}}
	eng := NewQueryEngine(QueryEngineConfig{SessionID: "restore-engine", CWD: root, TranscriptDir: t.TempDir(), ChatModel: fixture, Model: "fixture", ToolRegistry: registry, CommandEntrypoint: commands.EntrypointTUI, PermissionMode: permission.ModeBypassPermissions, PermissionPrompt: func(_ context.Context, request PermissionPromptRequest) PermissionInteractionResult {
		prompts.Add(1)
		if request.ToolName != "WorkspaceRestore" || request.DecisionConstraint != PermissionAllowOnceOnly || request.Source != "workspace_restore" || request.ToolUseID == "" {
			t.Errorf("invalid restore prompt: %+v", request)
		}
		if _, hasBytes := request.Input["content"]; hasBytes {
			t.Error("permission request contains captured bytes")
		}
		return PermissionInteractionResult{Decision: decision}
	}})
	t.Cleanup(eng.Close)
	var turnID string
	events, _ := eng.SubmitMessage(t.Context(), "Update the captured file")
	for event := range events {
		if event.Type == EventTerminal {
			if event.TerminalInfo.Reason != TerminalCompleted {
				t.Fatalf("fixture terminal: %+v", event.TerminalInfo)
			}
			turnID = event.TurnID
		}
	}
	history := eng.WorkspaceChanges()
	if len(history.Turns) != 1 || !history.Turns[0].RestoreBoundaryKnown || !history.Turns[0].RestoreEligible {
		t.Fatalf("production capture is not eligible: %+v", history.Turns)
	}
	if len(history.Records) != 1 {
		t.Fatalf("production file captures=%d", len(history.Records))
	}
	return eng, path, turnID, calls, prompts
}

func TestWorkspaceRestoreProductionPromptAndReadGuard(t *testing.T) {
	eng, path, turn, calls, prompts := newRestoreEngineFixture(t, PermissionAllowOnce)
	before, _ := json.Marshal(eng.GetMessages())
	preview, err := eng.PrepareWorkspaceRestore(turn, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Files) != 1 || string(preview.Files[0].Target.Data) != "user dirty\r\nlast" {
		t.Fatalf("preview: %+v", preview)
	}
	if prompts.Load() != 0 || calls.Load() != 3 {
		t.Fatal("preview dispatched work")
	}
	if _, err := eng.ConfirmWorkspaceRestore(t.Context(), preview.ID, false); err == nil {
		t.Fatal("unconfirmed preview applied")
	}
	status, err := eng.ConfirmWorkspaceRestore(t.Context(), preview.ID, true)
	if err != nil || status.State != "completed" || status.Finished != 1 {
		t.Fatalf("restore status=%+v err=%v", status, err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != "user dirty\r\nlast" {
		t.Fatalf("restored actual bytes=%q err=%v", actual, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatal("original mode not restored")
	}
	after, _ := json.Marshal(eng.GetMessages())
	if !bytes.Equal(before, after) || calls.Load() != 3 || prompts.Load() != 1 {
		t.Fatal("restore changed conversation, called model, or skipped independent permission")
	}
	// The real next model turn attempts Write directly. Its old process-global
	// read observation must not authorize replacing the newly restored file.
	events, _ := eng.SubmitMessage(t.Context(), "Try the write without reading")
	for range events {
	}
	actual, _ = os.ReadFile(path)
	if string(actual) != "user dirty\r\nlast" {
		t.Fatalf("stale read credit authorized write: %q", actual)
	}
	events, _ = eng.SubmitMessage(t.Context(), "Read it first, then write")
	for range events {
	}
	actual, _ = os.ReadFile(path)
	if string(actual) != "agent content\n" {
		t.Fatalf("fresh Read did not authorize Write: %q", actual)
	}
}

func TestWorkspaceRestoreRequiresFreshAllowOnceEvenInBypass(t *testing.T) {
	for _, decision := range []PermissionInteractionDecision{PermissionAllowSession, PermissionAllowAlways, PermissionDeny, PermissionCancelled} {
		t.Run(string(decision), func(t *testing.T) {
			eng, path, turn, _, prompts := newRestoreEngineFixture(t, decision)
			preview, err := eng.PrepareWorkspaceRestore(turn, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := eng.ConfirmWorkspaceRestore(t.Context(), preview.ID, true); err == nil {
				t.Fatal("non allow-once decision applied restore")
			}
			actual, _ := os.ReadFile(path)
			if string(actual) != "agent content\n" || prompts.Load() != 1 {
				t.Fatal("denied restore changed file or bypassed prompt")
			}
		})
	}
}

func TestWorkspaceRestorePolicyAndDirectShellFences(t *testing.T) {
	t.Run("unavailable execution policy", func(t *testing.T) {
		eng, path, turn, calls, prompts := newRestoreEngineFixture(t, PermissionAllowOnce)
		cwd := filepath.Dir(path)
		readRoots, tempRoots, deniedRoots, err := workspaceGuestRoots(cwd, nil)
		if err != nil {
			t.Fatal(err)
		}
		adapter := workspaceGuestAdapter()
		spec := workspaceGuestSpec(cwd, containmentEntrypoint(commands.EntrypointTUI), containment.SelectionDefault,
			adapter.Family(), adapter.CapabilityGeneration(), containment.StateUnavailable,
			containment.RootIdentity{Path: cwd}, readRoots, []string{cwd}, tempRoots, deniedRoots)
		policy, err := containment.NewExecutionPolicySnapshot(&spec)
		if err != nil {
			t.Fatal(err)
		}
		eng.mu.Lock()
		eng.executionPolicy = policy
		eng.mu.Unlock()
		beforeCalls := calls.Load()
		if _, err := eng.PrepareWorkspaceRestore(turn, "", ""); err == nil || !strings.Contains(err.Error(), "execution policy") {
			t.Fatalf("unavailable execution policy admitted restore: %v", err)
		}
		actual, err := os.ReadFile(path)
		if err != nil || string(actual) != "agent content\n" || prompts.Load() != 0 || calls.Load() != beforeCalls {
			t.Fatal("unavailable execution policy crossed permission, model, or write boundary")
		}
	})
	t.Run("policy drift", func(t *testing.T) {
		eng, path, turn, _, prompts := newRestoreEngineFixture(t, PermissionAllowOnce)
		preview, err := eng.PrepareWorkspaceRestore(turn, "", "")
		if err != nil {
			t.Fatal(err)
		}
		eng.permissionRulesReloadMu.Lock()
		eng.permissionRules = permission.NewRulesEngine([]permission.PermissionRule{{ToolName: "Write", Action: permission.ActionDeny}})
		eng.permissionRulesReloadMu.Unlock()
		if _, err := eng.ConfirmWorkspaceRestore(t.Context(), preview.ID, true); err == nil {
			t.Fatal("stale policy applied")
		}
		actual, _ := os.ReadFile(path)
		if string(actual) != "agent content\n" || prompts.Load() != 0 {
			t.Fatal("policy rejection crossed prompt/write boundary")
		}
	})
	t.Run("external shell", func(t *testing.T) {
		eng, _, turn, _, _ := newRestoreEngineFixture(t, PermissionAllowOnce)
		preview, err := eng.PrepareWorkspaceRestore(turn, "", "")
		if err != nil {
			t.Fatal(err)
		}
		release, err := eng.BeginExternalWorkspaceWrite()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := eng.ConfirmWorkspaceRestore(t.Context(), preview.ID, true); err == nil {
			t.Fatal("active direct shell allowed restore")
		}
		release()
		if _, err := eng.PrepareWorkspaceRestore(turn, "", ""); err == nil {
			t.Fatal("uncaptured direct shell was treated as covered")
		}
	})
}

func TestWorkspaceRestorePendingRestartBlocksModelsAndNeedsNewPermission(t *testing.T) {
	eng, path, turn, calls, prompts := newRestoreEngineFixture(t, PermissionAllowOnce)
	preview, err := eng.PrepareWorkspaceRestore(turn, "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, controller := eng.lockWorkspaceRevisions()
	op := controller.restorePreview.operation
	op.ConfirmationID = "expired-confirmation"
	op.PermissionRevision = "expired-permission"
	if err := controller.journal.SaveRestoreOperation(op); err != nil {
		controller.mu.Unlock()
		t.Fatal(err)
	}
	controller.mu.Unlock()
	// Model admission must stop before any provider dispatch; opening history
	// itself must not resume the persisted applying intent.
	events, terminal := eng.SubmitMessage(t.Context(), "Continue")
	for range events {
	}
	if terminal.Reason != TerminalPersistenceError || calls.Load() != 3 {
		t.Fatal("pending recovery admitted a provider call")
	}
	if err := eng.GetCommandRegistry().Register(&commands.Command{Name: "restore-write-spy", Kind: commands.CommandKindRuntimeMutation, Entrypoints: commands.EntrypointsTUI, ExecutionOwner: commands.ExecutionOwnerEngine, SideEffect: commands.SideEffectWorkspace, ResultKind: commands.ResultKindText, ModelInvocation: commands.ModelInvocationNone, Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
		t.Error("pending recovery dispatched a workspace command")
		return &commands.CommandResult{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"/compact", "/fork", "/restore-write-spy"} {
		events, _ := eng.SubmitMessage(t.Context(), command)
		var rejected bool
		for event := range events {
			if event.CommandResult != nil && event.CommandResult.Status == CommandResultFailed {
				rejected = true
			}
		}
		if !rejected || calls.Load() != 3 {
			t.Fatalf("pending recovery dispatched %s", command)
		}
	}
	root, dir, sessionID := eng.config.CWD, eng.config.TranscriptDir, eng.config.SessionID
	eng.Close()
	resumed := NewQueryEngine(QueryEngineConfig{SessionID: sessionID, CWD: root, TranscriptDir: dir, CommandEntrypoint: commands.EntrypointTUI, PermissionMode: permission.ModeBypassPermissions, PermissionPrompt: func(_ context.Context, request PermissionPromptRequest) PermissionInteractionResult {
		prompts.Add(1)
		return PermissionInteractionResult{Decision: PermissionAllowOnce}
	}})
	t.Cleanup(resumed.Close)
	actual, _ := os.ReadFile(path)
	if string(actual) != "agent content\n" {
		t.Fatal("restart applied recovery without confirmation")
	}
	recovery, err := resumed.PrepareWorkspaceRestore("", "", "restore")
	if err != nil {
		t.Fatal(err)
	}
	if recovery.ID == preview.ID {
		t.Fatal("restart reused ephemeral confirmation")
	}
	if _, err := resumed.ConfirmWorkspaceRestore(t.Context(), recovery.ID, true); err != nil {
		t.Fatal(err)
	}
	if prompts.Load() != 1 {
		t.Fatal("persisted authorization replaced fresh prompt")
	}
	actual, _ = os.ReadFile(path)
	if string(actual) != "user dirty\r\nlast" {
		t.Fatal("confirmed restart recovery did not restore exact bytes")
	}
}

func TestWorkspaceExternalWriterFailsClosedWhenGapCannotPersist(t *testing.T) {
	eng, _, _, _, _ := newRestoreEngineFixture(t, PermissionAllowOnce)
	_, controller := eng.lockWorkspaceRevisions()
	if err := controller.journal.Close(); err != nil {
		controller.mu.Unlock()
		t.Fatal(err)
	}
	controller.mu.Unlock()
	release, err := eng.BeginExternalWorkspaceWrite()
	if err == nil || release != nil || eng.hasExternalWorkspaceWriters() {
		t.Fatal("failed gap persistence admitted terminal execution")
	}
}

func TestWorkspaceExternalWriterReleaseIsIdempotent(t *testing.T) {
	eng, _, _, _, _ := newRestoreEngineFixture(t, PermissionAllowOnce)
	first, err := eng.BeginExternalWorkspaceWrite()
	if err != nil {
		t.Fatal(err)
	}
	second, err := eng.BeginExternalWorkspaceWrite()
	if err != nil {
		t.Fatal(err)
	}
	first()
	first()
	if !eng.hasExternalWorkspaceWriters() {
		t.Fatal("double release consumed another writer lease")
	}
	second()
	if eng.hasExternalWorkspaceWriters() {
		t.Fatal("writer lease leaked")
	}
}

func TestWorkspaceRestoreRejectsForeignReadScopeBeforeModelDispatch(t *testing.T) {
	eng, path, turn, calls, _ := newRestoreEngineFixture(t, PermissionAllowOnce)
	foreign := tools.NewFileReadGuards()
	foreignCtx := tools.WithFileReadGuards(t.Context(), foreign)
	input, _ := json.Marshal(map[string]any{"file_path": path})
	if _, err := tools.ReadTool().ExecuteCtx(foreignCtx, string(input)); err != nil {
		t.Fatal(err)
	}
	preview, err := eng.PrepareWorkspaceRestore(turn, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ConfirmWorkspaceRestore(t.Context(), preview.ID, true); err != nil {
		t.Fatal(err)
	}
	events, terminal := eng.SubmitMessage(foreignCtx, "Use the old isolated scope")
	for range events {
	}
	if terminal.Reason != TerminalPersistenceError || calls.Load() != 3 {
		t.Fatal("foreign stale scope reached model dispatch")
	}
	if tools.BoundFileReadGuards(foreignCtx) != foreign {
		t.Fatal("engine replaced the caller's isolated owner")
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != "user dirty\r\nlast" {
		t.Fatal("foreign stale scope wrote restored bytes")
	}
}

func TestWorkspaceExternalWriterRejectsChildAndRelativeWorkspace(t *testing.T) {
	for _, test := range []struct{ name, cwd, agent string }{{"relative", ".", ""}, {"child", t.TempDir(), "child"}} {
		t.Run(test.name, func(t *testing.T) {
			eng := NewQueryEngine(QueryEngineConfig{SessionID: "unsupported-shell", CWD: test.cwd, AgentID: test.agent, TranscriptDir: t.TempDir()})
			defer eng.Close()
			release, err := eng.BeginExternalWorkspaceWrite()
			if err == nil || release != nil || eng.hasExternalWorkspaceWriters() {
				t.Fatal("unsupported terminal scope acquired a writer lease")
			}
		})
	}
}

func TestWorkspaceExternalShellUsesEngineOwnerAndTaintsLaterCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Bash fixture")
	}
	eng, path, _, _, _ := newRestoreEngineFixture(t, PermissionAllowOnce)
	release, err := eng.BeginExternalWorkspaceWrite()
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := eng.ExternalWorkspaceShellContext(t.Context())
	if err != nil {
		release()
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"command": "printf owner > shell-owner.txt"})
	if _, err := tools.BashTool().ExecuteCtx(ctx, string(input)); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	actual, err := os.ReadFile(filepath.Join(filepath.Dir(path), "shell-owner.txt"))
	if err != nil || string(actual) != "owner" {
		t.Fatal("terminal command used a process-global working directory")
	}
	if len(eng.shellManager.ListShells()) == 0 {
		t.Fatal("terminal command escaped the engine's shell lifecycle owner")
	}
	// A shell may leave descendants even after its foreground command returns.
	// A later typed-write turn cannot upgrade that history into complete proof.
	if err := eng.workspaceCaptureBoundary(t.Context(), "later-file-turn"); err != nil {
		t.Fatal(err)
	}
	history := eng.WorkspaceChanges()
	if len(history.Turns) < 2 || history.Turns[len(history.Turns)-1].RestoreEligible {
		t.Fatal("later capture ignored an earlier unobserved writer")
	}
}
