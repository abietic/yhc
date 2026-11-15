package tools

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abietic/yhc/engine/containment"
)

func TestBashCompleteOutputRetainedBeforePreview(t *testing.T) {
	result := &ShellResult{Stdout: strings.Repeat("x", 6000) + "\nMIDDLE_ASSERTION\n" + strings.Repeat("y", 6000), ExitCode: 7}
	path := filepath.Join(t.TempDir(), "full.txt")
	calls := 0
	ctx := WithToolOutputPersister(context.Background(), func(_ context.Context, name string, parts ...string) (string, error) {
		calls++
		if name != "Bash" || strings.Join(parts, "") != result.Stdout+"\n[exit code: 7]\n" {
			t.Fatal("persister did not receive complete execution evidence")
		}
		return path, nil
	})
	preview := formatShellResult("check", result)
	if strings.Contains(preview, "MIDDLE_ASSERTION") {
		t.Fatal("fixture did not put the assertion in the omitted middle")
	}
	got := preview + persistTruncatedShellOutput(ctx, result)
	if calls != 1 || !strings.Contains(got, "[exit code: 7]") || !strings.Contains(got, "Full output saved to: "+path) {
		t.Fatal("bounded preview did not retain outcome and output receipt")
	}
}

func TestBashCompleteOutputUnavailablePreservesOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result ShellResult
		path   string
		err    error
	}{
		{name: "storage failure", err: errors.New("private provider or path detail")},
		{name: "relative receipt", path: "foreign.txt"},
		{name: "newline receipt", path: "/tmp/output\nforeign.txt"},
		{name: "capture truncated", result: ShellResult{Truncated: true}},
		{name: "timed out", result: ShellResult{TimedOut: true}},
		{name: "canceled", result: ShellResult{Canceled: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.result
			result.Stdout = strings.Repeat("x", 12000)
			result.ExitCode = 7
			calls := 0
			ctx := WithToolOutputPersister(context.Background(), func(context.Context, string, ...string) (string, error) {
				calls++
				return tc.path, tc.err
			})
			notice := persistTruncatedShellOutput(ctx, &result)
			if !strings.Contains(notice, "Full output unavailable") || strings.Contains(notice, "Full output saved") || strings.Contains(notice, "private provider") {
				t.Fatal("storage failure advertised a path or disclosed diagnostics")
			}
			if (result.Truncated || result.TimedOut || result.Canceled) && calls != 0 {
				t.Fatal("incomplete capture was offered as complete output")
			}
			_, marker := shellResultOutcome(&result)
			if !strings.Contains(formatShellResult("check", &result)+notice, marker) {
				t.Fatal("unavailable output hid execution outcome")
			}
		})
	}
}

func TestBashContainedOutputCannotUseHostPersister(t *testing.T) {
	binding, _ := p51GuestBinding(t)
	manager, err := NewShellManagerWithGuestBinding(binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{
		containment.WithSnapshot(context.Background(), binding.Policy()),
		WithShellManager(context.Background(), manager), // manager authority without a context snapshot
	} {
		calls := 0
		ctx = WithToolOutputPersister(ctx, func(context.Context, string, ...string) (string, error) {
			calls++
			return "/host/private/output.txt", nil
		})
		notice := persistTruncatedShellOutput(ctx, &ShellResult{Stdout: strings.Repeat("x", 12000)})
		if calls != 0 || !strings.Contains(notice, "unavailable within this execution boundary") || strings.Contains(notice, "/host/") {
			t.Fatal("contained execution used host output storage")
		}
	}
}

func TestBackgroundCompleteOutputReceiptSurvivesFilterAndConsumption(t *testing.T) {
	manager := NewShellManager()
	id := "output-receipt"
	entry := &backgroundShellEntry{owner: manager.backgroundOwner, status: "running"}
	backgroundShells.Store(id, entry)
	t.Cleanup(func() { backgroundShells.Delete(id) })
	path := filepath.Join(t.TempDir(), "full.txt")
	calls := 0
	ctx := WithToolOutputPersister(context.Background(), func(context.Context, string, ...string) (string, error) {
		calls++
		return path, nil
	})
	entry.finishWithContext(ctx, "check", &ShellResult{Stdout: strings.Repeat("x", 12000), ExitCode: 7}, nil)
	for range 2 {
		got, err := executeBashOutput(manager, fileToolInput(t, map[string]any{"bash_id": id, "filter": "never-match"}))
		if err != nil || !strings.Contains(got, "[exit code: 7]") || !strings.Contains(got, "Full output saved to: "+path) {
			t.Fatal("filter or consumption hid the output receipt")
		}
	}
	if calls != 1 {
		t.Fatal("reading background output persisted it again")
	}
}
