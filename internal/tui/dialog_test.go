package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
)

func TestPermissionDialogDisplaysToolTitle(t *testing.T) {
	dialog := NewPermissionDialog(defaultStyles())
	dialog.Show("Bash", `{"command":"go test ./..."}`, `only Bash command "go test ./..."`, make(chan PermissionResponse, 1))
	view := dialog.Overlay("", 100, 30)
	if !strings.Contains(view, "Bash command") {
		t.Fatalf("permission dialog did not display tool-specific title: %q", view)
	}
}

func TestPermissionDialogShowsArrowIndicator(t *testing.T) {
	dialog := NewPermissionDialog(defaultStyles())
	dialog.Show("Read", `{"file_path":"/tmp/test.go"}`, "", make(chan PermissionResponse, 1))
	view := dialog.Overlay("", 100, 30)
	plain := stripANSIForTest(view)
	if !strings.Contains(plain, "❯") {
		t.Fatalf("expected ❯ selection indicator, got %q", plain)
	}
	if !strings.Contains(plain, "Yes") {
		t.Fatalf("expected Yes option, got %q", plain)
	}
	if !strings.Contains(plain, "No") {
		t.Fatalf("expected No option, got %q", plain)
	}
}

func TestPermissionDialogArrowKeyNavigation(t *testing.T) {
	ch := make(chan PermissionResponse, 1)
	dialog := NewPermissionDialog(defaultStyles())
	dialog.Show("Bash", `{"command":"echo hi"}`, "", ch)

	// Initial selection is 0 (Yes)
	if dialog.selectedIdx != 0 {
		t.Fatalf("expected initial selectedIdx=0, got %d", dialog.selectedIdx)
	}

	// Press down to move to index 1
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if dialog.selectedIdx != 1 {
		t.Fatalf("expected selectedIdx=1 after down, got %d", dialog.selectedIdx)
	}

	// Press down again to move to index 2 (No)
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if dialog.selectedIdx != 2 {
		t.Fatalf("expected selectedIdx=2 after second down, got %d", dialog.selectedIdx)
	}

	// Press down at bottom wraps to 0
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if dialog.selectedIdx != 0 {
		t.Fatalf("expected selectedIdx=0 after wrap, got %d", dialog.selectedIdx)
	}

	// Press up at top wraps to last
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	if dialog.selectedIdx != 2 {
		t.Fatalf("expected selectedIdx=2 after up wrap, got %d", dialog.selectedIdx)
	}
}

func TestPermissionDialogEnterSelectsOption(t *testing.T) {
	ch := make(chan PermissionResponse, 1)
	dialog := NewPermissionDialog(defaultStyles())
	dialog.Show("Read", `{}`, "", ch)

	// Move to "No" (index 2)
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})

	// Press enter
	done, _ := dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done {
		t.Fatal("expected dialog to close after enter")
	}

	resp := <-ch
	if resp != PermissionDeny {
		t.Fatalf("expected PermissionDeny from No option, got %d", resp)
	}
}

func TestPermissionDialogSingleKeyAccelerators(t *testing.T) {
	ch := make(chan PermissionResponse, 1)
	dialog := NewPermissionDialog(defaultStyles())
	dialog.Show("Bash", `{}`, "", ch)

	// 'a' should still work as accelerator for Allow
	done, _ := dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'a'})})
	if !done {
		t.Fatal("expected 'a' accelerator to close dialog")
	}
	resp := <-ch
	if resp != PermissionAllow {
		t.Fatalf("expected PermissionAllow from 'a' key, got %d", resp)
	}
}

func TestPermissionDialogToolSpecificTitle(t *testing.T) {
	tests := []struct {
		tool     string
		expected string
	}{
		{"Bash", "Bash command"},
		{"Read", "File access"},
		{"Write", "File access"},
		{"Edit", "File access"},
		{"Agent", "Agent dispatch"},
		{"WebFetch", "Tool use"},
	}
	for _, tt := range tests {
		got := dialogTitle(tt.tool)
		if got != tt.expected {
			t.Errorf("dialogTitle(%q) = %q, want %q", tt.tool, got, tt.expected)
		}
	}
}

func TestPermissionDialogSessionScopeInOptions(t *testing.T) {
	dialog := NewPermissionDialog(defaultStyles())
	dialog.Show("Bash", `{"command":"go test ./..."}`, `Bash command "go test"`, make(chan PermissionResponse, 1))

	view := dialog.Overlay("", 100, 30)
	plain := stripANSIForTest(view)
	if !strings.Contains(plain, `don't ask again for Bash command "go test"`) {
		t.Fatalf("expected session scope in option label, got %q", plain)
	}
}

func TestBuildOptionsGeneric(t *testing.T) {
	opts := buildOptions("Read", "")
	if len(opts) != 3 {
		t.Fatalf("expected 3 options, got %d", len(opts))
	}
	if opts[0].response != PermissionAllow {
		t.Fatalf("first option should be Allow, got %d", opts[0].response)
	}
	if opts[1].response != PermissionAllowSession {
		t.Fatalf("second option should be AllowSession, got %d", opts[1].response)
	}
	if opts[2].response != PermissionDeny {
		t.Fatalf("third option should be Deny, got %d", opts[2].response)
	}
}

func TestPermissionDialogRepeatedToolHasOnlyOneCallChoices(t *testing.T) {
	dialog := NewPermissionDialog(defaultStyles())
	dialog.ShowRepeatedTool("Bash", "This is a repeated identical tool call.", 3, make(chan PermissionResponse, 1))
	plain := stripANSIForTest(dialog.Overlay("", 100, 30))
	for _, expected := range []string{"Repeated tool call", "Run this call once", "Stop and change strategy", "Attempt 3"} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("repeated-tool dialog missing %q: %q", expected, plain)
		}
	}
	if strings.Contains(plain, "don't ask again") {
		t.Fatalf("repeated-tool dialog exposed persistent permission: %q", plain)
	}
}

func TestPermissionDialogRepeatedToolRejectsSessionAndAlwaysShortcuts(t *testing.T) {
	ch := make(chan PermissionResponse, 1)
	dialog := NewPermissionDialog(defaultStyles())
	dialog.ShowRepeatedTool("Bash", "repeated", 3, ch)
	for _, msg := range []tea.KeyPressMsg{{Code: 's', Text: "s"}, {Code: 'A', Text: "A"}} {
		done, _ := dialog.HandleKey(msg)
		if done {
			t.Fatalf("shortcut %q closed repeated-tool dialog", msg.String())
		}
	}
	select {
	case response := <-ch:
		t.Fatalf("repeated-tool shortcut produced response %v", response)
	default:
	}
}

func TestPlanDialogUsesEngineIdentityAndExplicitTargets(t *testing.T) {
	planPath := filepath.Join(t.TempDir(), "canonical-plan.md")
	if err := os.WriteFile(planPath, []byte("# Canonical plan"), 0o600); err != nil {
		t.Fatal(err)
	}
	responseCh := make(chan PermissionResponse, 1)
	dialog := NewPlanDialog(defaultStyles())
	dialog.Show("main", "wrong-session", "wrong-agent", &engine.PlanApprovalRequest{
		RequestID: "plan-1", PlanRevision: 4, PlanFileIdentity: planPath,
		ReturnMode: permission.ModeDontAsk,
	}, responseCh)

	if dialog.planPath != planPath ||
		dialog.plan != "# Canonical plan" ||
		dialog.ReviewedPlanDigest() !=
			engine.PlanBytesDigest([]byte("# Canonical plan")) {
		t.Fatalf("dialog identity = path:%q plan:%q", dialog.planPath, dialog.plan)
	}
	if target, confirmed := dialog.ApprovalTarget(); target != permission.ModeDontAsk || confirmed {
		t.Fatalf("initial target = %q confirmed=%v", target, confirmed)
	}
	if err := os.WriteFile(
		planPath,
		[]byte("# Canonical plan\n\nExternally edited"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	dialog.ReloadPlan()
	if dialog.plan != "# Canonical plan\n\nExternally edited" ||
		dialog.ReviewedPlanDigest() !=
			engine.PlanBytesDigest([]byte("# Canonical plan\n\nExternally edited")) {
		t.Fatalf(
			"reloaded identity = plan:%q digest:%q",
			dialog.plan,
			dialog.ReviewedPlanDigest(),
		)
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if target, confirmed := dialog.ApprovalTarget(); target != permission.ModeAcceptEdits || confirmed {
		t.Fatalf("accept-edits target = %q confirmed=%v", target, confirmed)
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if target, confirmed := dialog.ApprovalTarget(); target != permission.ModeBypassPermissions || confirmed {
		t.Fatalf("bypass target = %q confirmed=%v", target, confirmed)
	}
	done, _ := dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || dialog.focus != planFocusBypassConfirmation {
		t.Fatal("bypass did not open confirmation")
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyUp})
	done, _ = dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || <-responseCh != PermissionAllow || dialog.PlanResult() == nil || !dialog.PlanResult().Confirmed {
		t.Fatal("confirmed bypass did not approve")
	}
}
