package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func backgroundFeedbackFixture(t *testing.T, body string) (*ShellManager, string) {
	t.Helper()
	manager := NewShellManager()
	t.Cleanup(func() { _ = manager.KillAll() })
	dir := t.TempDir()
	marker := filepath.Join(dir, "command-finished")
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
	command := body + "\nyhc_feedback_code=$?\n: > " + quoted + "\n(exit \"$yhc_feedback_code\")"
	result, err := runBackgroundCommand(t.Context(), manager, dir, command, "feedback fixture")
	if err != nil {
		t.Fatal(err)
	}
	_, tail, ok := strings.Cut(result, "Shell ID: ")
	if !ok {
		t.Fatal("background start omitted its handle")
	}
	id := strings.SplitN(tail, "\n", 2)[0]
	t.Cleanup(func() { backgroundShells.Delete(id) })
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("background command did not reach its independent completion marker")
		case <-tick.C:
		}
	}
	// The same-shell command lock is an independent barrier after the marker.
	if _, err := manager.ExecuteAt(context.Background(), id, dir, ":", time.Second); err != nil {
		t.Fatal(err)
	}
	return manager, id
}

func waitBackgroundFeedback(t *testing.T, manager *ShellManager, id, status string) string {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		result, err := executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id}))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(result, "Status: "+status) {
			return result
		}
		select {
		case <-deadline.C:
			t.Fatalf("finished command has no %s feedback: %s", status, result)
		case <-tick.C:
		}
	}
}

func TestBackgroundBashRetainsOutputAndFailure(t *testing.T) {
	manager, id := backgroundFeedbackFixture(t, "printf 'stdout-proof\\n'; printf 'stderr-proof\\n' >&2; (exit 7)")
	result := waitBackgroundFeedback(t, manager, id, "failed")
	for _, want := range []string{"stdout-proof", "stderr-proof", "[exit code: 7]"} {
		if !strings.Contains(result, want) {
			t.Errorf("background feedback lost %q", want)
		}
	}
	next, err := executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id}))
	if err != nil || !strings.Contains(next, "Status: failed") || !strings.Contains(next, "[exit code: 7]") {
		t.Fatal("reading output erased the terminal state")
	}
	if strings.Contains(next, "stdout-proof") {
		t.Fatal("a repeated read replayed already consumed output")
	}
}

func TestBackgroundBashOwnerFilterAndCleanup(t *testing.T) {
	manager, id := backgroundFeedbackFixture(t, "printf 'keep-proof\\nskip-proof\\n'; (exit 9)")
	other := NewShellManager()
	t.Cleanup(func() { _ = other.KillAll() })
	input := fileToolInput(t, map[string]any{"bash_id": id})
	if result, err := executeBashOutput(other, input); err != nil || !strings.Contains(result, "No background shell found") || strings.Contains(result, "keep-proof") {
		t.Fatal("another manager accessed background results")
	}
	if _, err := executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id, "filter": "["})); err == nil {
		t.Fatal("invalid filter was accepted")
	}
	// Invalid/foreign reads cannot consume the owner's pending output. Wait
	// using the completion record, without draining it through BashOutput.
	entryValue, _ := backgroundShells.Load(id)
	entry := entryValue.(*backgroundShellEntry)
	select {
	case <-entry.done:
	case <-time.After(3 * time.Second):
		t.Fatal("background result was not published")
	}
	result, err := executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id, "filter": "^keep-proof$"}))
	if err != nil || !strings.Contains(result, "keep-proof") || strings.Contains(result, "skip-proof") || !strings.Contains(result, "[exit code: 9]") {
		t.Fatal("filter lost pending output or concealed terminal facts")
	}
	if err := manager.KillAll(); err != nil {
		t.Fatal(err)
	}
	if _, exists := backgroundShells.Load(id); exists {
		t.Fatal("closing the owner retained its background output")
	}
}

func TestBackgroundBashTerminalSnapshotsAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name, status, marker string
		result               *ShellResult
		err                  error
	}{
		{name: "completed", status: "completed", result: &ShellResult{}},
		{name: "failed", status: "failed", marker: "[exit code: 7]", result: &ShellResult{ExitCode: 7}},
		{name: "timeout", status: "timed_out", marker: "[timeout]", result: &ShellResult{TimedOut: true}},
		{name: "cancelled", status: "canceled", marker: "[canceled]", result: &ShellResult{Canceled: true}},
		{name: "execution-error", status: "failed", marker: "[execution failed]", err: errors.New("execution unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewShellManager()
			id := "test-bg-" + tc.name
			entry := &backgroundShellEntry{owner: manager.backgroundOwner, status: "running"}
			backgroundShells.Store(id, entry)
			t.Cleanup(func() { backgroundShells.Delete(id) })
			input := fileToolInput(t, map[string]any{"bash_id": id})
			running, err := executeBashOutput(manager, input)
			if err != nil || !strings.Contains(running, "Status: running") || strings.Contains(running, "Status: completed") {
				t.Fatal("pending work was reported as completed")
			}
			if tc.result != nil {
				tc.result.Stdout = "start-proof" + strings.Repeat("界", 20000) + "end-proof"
			}
			entry.finish("command", tc.result, tc.err)
			result, err := executeBashOutput(manager, input)
			if err != nil || !strings.Contains(result, "Status: "+tc.status) || !strings.Contains(result, tc.marker) || len(result) > bashMaxOutputDefault {
				t.Fatal("background terminal snapshot lost status or exceeded its preview budget")
			}
			if tc.result != nil && (!strings.Contains(result, "start-proof") || !strings.Contains(result, "end-proof")) {
				t.Fatal("background preview lost stream ends")
			}
		})
	}
}

func TestBackgroundKillDoesNotDeleteAnotherOwnersResult(t *testing.T) {
	manager, id := backgroundFeedbackFixture(t, "printf 'owner-proof\\n'")
	other := NewShellManager()
	t.Cleanup(func() { _ = other.KillAll() })
	if _, err := other.ExecuteAt(t.Context(), id, t.TempDir(), ":", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := executeKillShell(other, fileToolInput(t, map[string]any{"shell_id": id})); err != nil {
		t.Fatal(err)
	}
	if _, ok := backgroundShells.Load(id); !ok {
		t.Fatal("killing a same-ID shell in another manager erased the owner's result")
	}
	result := waitBackgroundFeedback(t, manager, id, "completed")
	if !strings.Contains(result, "owner-proof") {
		t.Fatal("owner output was lost after another manager's KillShell")
	}
}
