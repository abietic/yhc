package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/abietic/yhc/engine/commands"
)

func TestTerminalRestoreArtifactsBlockModelAndEffectfulCommands(t *testing.T) {
	eng, path, turn, calls, _ := newRestoreEngineFixture(t, PermissionAllowOnce)
	preview, err := eng.PrepareWorkspaceRestore(turn, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.ConfirmWorkspaceRestore(t.Context(), preview.ID, true); err != nil {
		t.Fatal(err)
	}
	_, controller := eng.lockWorkspaceRevisions()
	op, found, err := controller.journal.LoadRestoreOperation()
	controller.mu.Unlock()
	if err != nil || !found {
		t.Fatal("terminal operation", err)
	}
	p := op.Paths[0]
	backup := filepath.Join(eng.GetCWD(), p.BackupPath)
	if err := os.WriteFile(backup, p.Original.Data, os.FileMode(p.Original.Mode)); err != nil {
		t.Fatal(err)
	}
	if err := eng.GetCommandRegistry().Register(&commands.Command{Name: "restore-artifact-spy", Kind: commands.CommandKindRuntimeMutation, Entrypoints: commands.EntrypointsTUI, ExecutionOwner: commands.ExecutionOwnerEngine, SideEffect: commands.SideEffectWorkspace, ResultKind: commands.ResultKindText, ModelInvocation: commands.ModelInvocationNone, Execute: func(context.Context, *commands.CommandContext) (*commands.CommandResult, error) {
		t.Error("unverified terminal state dispatched workspace command")
		return &commands.CommandResult{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"Continue", "/compact", "/fork", "/restore-artifact-spy"} {
		events, terminal := eng.SubmitMessage(t.Context(), input)
		rejected := false
		for event := range events {
			if event.CommandResult != nil && event.CommandResult.Status == CommandResultFailed {
				rejected = true
			}
		}
		if input == "Continue" {
			rejected = terminal.Reason == TerminalPersistenceError
		}
		if !rejected || calls.Load() != 3 {
			t.Fatalf("terminal artifact admitted %s: provider calls=%d", input, calls.Load())
		}
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != "user dirty\r\nlast" {
		t.Fatal("admission changed restored file", err)
	}
	actual, err = os.ReadFile(backup)
	if err != nil || string(actual) != string(p.Original.Data) {
		t.Fatal("admission cleaned or changed unverified sibling", err)
	}
}
