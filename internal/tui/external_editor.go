package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	charmeditor "github.com/charmbracelet/x/editor"

	"github.com/abietic/yhc/internal/identity"
)

// externalEditorCommand keeps x/editor's editor-specific option handling while
// adding the conventional VISUAL-over-EDITOR precedence that x/editor v0.2.0
// does not yet provide. Composer and Plan editing must share this resolver.
func externalEditorCommand(
	path string,
	options ...charmeditor.Option,
) (*exec.Cmd, error) {
	visual := strings.Fields(os.Getenv("VISUAL"))
	if len(visual) == 0 || os.Getenv("SNAP_REVISION") != "" {
		return charmeditor.Command(identity.CommandName, path, options...)
	}

	editorPath := visual[0]
	editorName := filepath.Base(editorPath)
	args := append([]string(nil), visual[1:]...)
	appendPath := true
	for _, option := range options {
		optionArgs, pathInArgs := option(editorName, path)
		args = append(args, optionArgs...)
		if pathInArgs {
			appendPath = false
		}
	}
	if appendPath {
		args = append(args, path)
	}
	//nolint:gosec // VISUAL is an explicit user-selected executable, like EDITOR in x/editor.
	return exec.CommandContext(
		context.Background(),
		editorPath,
		args...,
	), nil
}

func externalEditorDisplayName() string {
	for _, variable := range []string{"VISUAL", "EDITOR"} {
		fields := strings.Fields(os.Getenv(variable))
		if len(fields) > 0 {
			return filepath.Base(fields[0])
		}
	}
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	return "nano"
}

// restoreTerminalCapabilitiesCmd restores presentation state after Bubble Tea
// reacquires the terminal. Bubble Tea v2 restores App-owned terminal modes
// from the next declarative View; this command only resets observed focus,
// requests a full repaint, and restarts the virtual cursor when enabled.
func (a *App) restoreTerminalCapabilitiesCmd() tea.Cmd {
	if a == nil {
		return nil
	}
	a.focusState.Reset()
	cmds := []tea.Cmd{tea.ClearScreen}
	if !a.reducedMotion {
		cmds = append(cmds, textarea.Blink)
	}
	return tea.Sequence(cmds...)
}

func (a *App) activePlanEditorIdentityMatches(
	identity planEditorIdentity,
) bool {
	if a == nil ||
		a.threadAttention == nil ||
		a.activeThreadViewID() != identity.threadID {
		return false
	}
	request, ok := a.threadAttention.get(a.threadAttention.activeID)
	if !ok ||
		request.Kind != threadAttentionPlan ||
		request.ThreadID != identity.threadID ||
		request.PlanApproval == nil {
		return false
	}
	approval := request.PlanApproval
	return approval.RequestID == identity.requestID &&
		approval.PlanRevision == identity.planRevision &&
		approval.PlanFileIdentity == identity.planPath
}

func (a *App) applyPlanEditorResult(
	message planEditorFinishedMsg,
) tea.Cmd {
	var restore tea.Cmd
	if message.terminalReleased {
		restore = a.restoreTerminalCapabilitiesCmd()
	}
	a.externalEditorActive = a.planDialog.EditorActive()
	if !a.activePlanEditorIdentityMatches(message.identity) {
		a.showNotification(
			"Plan editor result is stale; current approval was not changed",
			NotifyWarning,
		)
		return restore
	}
	applied, err := a.planDialog.applyEditorResult(message)
	a.externalEditorActive = a.planDialog.EditorActive()
	if !applied {
		a.showNotification(
			"Plan editor result is stale; current approval was not changed",
			NotifyWarning,
		)
		return restore
	}
	if err != nil {
		a.showNotification("Plan editor failed: "+err.Error(), NotifyError)
		return restore
	}
	a.updateLayout()
	return restore
}
