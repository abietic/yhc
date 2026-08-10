package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
)

func TestP201PlanActionsDeduplicatePreviousMode(t *testing.T) {
	tests := []struct {
		name string
		mode permission.Mode
		want []permission.Mode
	}{
		{
			name: "default",
			mode: permission.ModeDefault,
			want: []permission.Mode{
				permission.ModeDefault,
				permission.ModeAcceptEdits,
				permission.ModeBypassPermissions,
				permission.ModePlan,
			},
		},
		{
			name: "accept edits",
			mode: permission.ModeAcceptEdits,
			want: []permission.Mode{
				permission.ModeAcceptEdits,
				permission.ModeBypassPermissions,
				permission.ModePlan,
			},
		},
		{
			name: "bypass",
			mode: permission.ModeBypassPermissions,
			want: []permission.Mode{
				permission.ModeBypassPermissions,
				permission.ModeAcceptEdits,
				permission.ModePlan,
			},
		},
		{
			name: "dont ask",
			mode: permission.ModeDontAsk,
			want: []permission.Mode{
				permission.ModeDontAsk,
				permission.ModeAcceptEdits,
				permission.ModeBypassPermissions,
				permission.ModePlan,
			},
		},
		{
			name: "auto",
			mode: permission.ModeAuto,
			want: []permission.Mode{
				permission.ModeAuto,
				permission.ModeAcceptEdits,
				permission.ModeBypassPermissions,
				permission.ModePlan,
			},
		},
		{
			name: "bubble",
			mode: permission.ModeBubble,
			want: []permission.Mode{
				permission.ModeBubble,
				permission.ModeAcceptEdits,
				permission.ModeBypassPermissions,
				permission.ModePlan,
			},
		},
		{
			name: "invalid falls back",
			mode: permission.Mode("invalid"),
			want: []permission.Mode{
				permission.ModeDefault,
				permission.ModeAcceptEdits,
				permission.ModeBypassPermissions,
				permission.ModePlan,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := buildPlanOptions(test.mode)
			if len(options) != len(test.want) {
				t.Fatalf("option count = %d, want %d: %#v", len(options), len(test.want), options)
			}
			seen := make(map[permission.Mode]struct{}, len(options))
			for index, option := range options {
				if option.targetMode != test.want[index] {
					t.Fatalf(
						"option %d target = %q, want %q",
						index,
						option.targetMode,
						test.want[index],
					)
				}
				if _, duplicate := seen[option.targetMode]; duplicate {
					t.Fatalf("duplicate target %q in %#v", option.targetMode, options)
				}
				seen[option.targetMode] = struct{}{}
			}
			if options[0].response != PermissionAllow ||
				options[0].confirmed ||
				options[len(options)-1].response != PermissionDeny ||
				options[len(options)-1].targetMode != permission.ModePlan {
				t.Fatalf("action boundary = %#v", options)
			}
		})
	}
}

func TestP201PlanActionsUseReturnModeWithoutFileIdentity(t *testing.T) {
	dialog := NewPlanDialog(defaultStyles())
	dialog.Show("main", "legacy-session", "legacy-agent", &engine.PlanApprovalRequest{
		ReturnMode: permission.ModeBypassPermissions,
	}, make(chan PermissionResponse, 1))

	if len(dialog.options) == 0 ||
		dialog.options[0].targetMode != permission.ModeBypassPermissions {
		t.Fatalf("fallback approval options = %#v", dialog.options)
	}
}

func TestP20R1BypassConfirmationBackPreservesPresentationAndCancelIsExplicit(t *testing.T) {
	dialog := NewPlanDialog(defaultStyles())
	dialog.Show("main", "s", "", &engine.PlanApprovalRequest{RequestID: "r", PlanRevision: 1}, make(chan PermissionResponse, 1))
	dialog.viewport.offset = 3
	dialog.feedbackEditor.SetValue("retained draft")
	cursor := dialog.feedbackCursorOffset()
	dialog.feedbackUndo = []textEditorSnapshot{captureTextEditorSnapshot(dialog.feedbackEditor)}
	dialog.focus = planFocusActions
	dialog.selectedIdx = 2 // bypass for the default target set.
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if dialog.focus != planFocusBypassConfirmation {
		t.Fatalf("focus = %v", dialog.focus)
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if dialog.focus != planFocusActions || dialog.selectedIdx != 2 || dialog.viewport.offset != 3 || dialog.Feedback() != "retained draft" || dialog.feedbackCursorOffset() != cursor || len(dialog.feedbackUndo) != 1 {
		t.Fatalf("Back lost presentation state")
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}) // default No
	if dialog.focus != planFocusActions || dialog.selectedIdx != 2 || dialog.viewport.offset != 3 || dialog.Feedback() != "retained draft" || dialog.feedbackCursorOffset() != cursor || len(dialog.feedbackUndo) != 1 {
		t.Fatal("No lost presentation state")
	}
	dialog.ForceClose()
	result := dialog.PlanResult()
	if result == nil || result.Outcome != engine.PlanApprovalCancel || result.Feedback != "" {
		t.Fatalf("ForceClose result = %#v", result)
	}
	for _, focus := range []planDialogFocus{planFocusActions, planFocusReview} {
		other := NewPlanDialog(defaultStyles())
		other.Show("main", "s", "", &engine.PlanApprovalRequest{RequestID: "r", PlanRevision: 1}, make(chan PermissionResponse, 1))
		other.focus = focus
		other.feedbackEditor.SetValue("retained draft")
		other.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
		if got := other.PlanResult(); got == nil || got.Outcome != engine.PlanApprovalCancel || got.Feedback != "" {
			t.Fatalf("%v Esc result = %#v", focus, got)
		}
	}
}

func TestP20R3BypassConfirmationIsVisibleAndRequiresDistinctChoice(t *testing.T) {
	dialog, responseCh := p201PlanDialog(
		t,
		permission.ModeDefault,
		48,
	)
	dialog.focus = planFocusActions
	dialog.selectedIdx = 2

	done, _ := dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || dialog.focus != planFocusBypassConfirmation ||
		dialog.PlanResult() != nil {
		t.Fatalf(
			"first bypass action closed dialog: done=%v focus=%v result=%#v",
			done,
			dialog.focus,
			dialog.PlanResult(),
		)
	}
	rendered := xansi.Strip(dialog.Overlay("", 80, 24))
	for _, required := range []string{
		"Bypass permissions disables all tool permission checks.",
		"❯ No, return to actions",
		"Yes, bypass permissions",
		"Confirm bypass",
	} {
		if !strings.Contains(rendered, required) {
			t.Fatalf("confirmation frame missing %q:\n%s", required, rendered)
		}
	}

	done, _ = dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || dialog.focus != planFocusActions || dialog.PlanResult() != nil {
		t.Fatalf(
			"default No settled bypass: done=%v focus=%v result=%#v",
			done,
			dialog.focus,
			dialog.PlanResult(),
		)
	}

	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	rendered = xansi.Strip(dialog.Overlay("", 80, 24))
	if !strings.Contains(rendered, "❯ Yes, bypass permissions") {
		t.Fatalf("explicit Yes selection is not visible:\n%s", rendered)
	}
	done, _ = dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	result := dialog.PlanResult()
	if !done || result == nil ||
		result.Outcome != engine.PlanApprovalApprove ||
		result.TargetMode != permission.ModeBypassPermissions ||
		!result.Confirmed {
		t.Fatalf("explicit Yes result: done=%v result=%#v", done, result)
	}
	if response := <-responseCh; response != PermissionAllow {
		t.Fatalf("explicit Yes response = %v", response)
	}
}

func TestP201PlanInvalidOverlayClearsPublishedGeometry(t *testing.T) {
	dialog, _ := p201PlanDialog(t, permission.ModeDefault, 48)
	dialog.Overlay("", 80, 24)
	if dialog.geometry.review.Height == 0 || len(dialog.geometry.actions) == 0 {
		t.Fatalf("initial geometry = %#v", dialog.geometry)
	}

	if rendered := dialog.Overlay("", 0, 0); rendered != "" {
		t.Fatalf("invalid overlay = %q", rendered)
	}
	if dialog.geometry.review != (layoutRect{}) ||
		len(dialog.geometry.actions) != 0 {
		t.Fatalf("stale geometry = %#v", dialog.geometry)
	}
}

func TestP201PlanFocusRoutesKeysByRegion(t *testing.T) {
	dialog, responseCh := p201PlanDialog(
		t,
		permission.ModeDontAsk,
		48,
	)
	dialog.Overlay("", 80, 24)
	if dialog.focus != planFocusReview || dialog.selectedIdx != 0 {
		t.Fatalf("initial focus=%s selection=%d", dialog.focus, dialog.selectedIdx)
	}

	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if dialog.viewport.offset != 1 || dialog.selectedIdx != 0 {
		t.Fatalf(
			"review Down offset=%d selection=%d",
			dialog.viewport.offset,
			dialog.selectedIdx,
		)
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if dialog.viewport.offset <= 1 {
		t.Fatalf("review PageDown offset=%d", dialog.viewport.offset)
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
	if dialog.viewport.offset != 0 {
		t.Fatalf("review Home offset=%d", dialog.viewport.offset)
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if dialog.viewport.offset != dialog.viewport.maxOffset() ||
		dialog.viewport.offset == 0 {
		t.Fatalf(
			"review End offset=%d max=%d",
			dialog.viewport.offset,
			dialog.viewport.maxOffset(),
		)
	}

	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if dialog.focus != planFocusActions {
		t.Fatalf("Tab focus=%s", dialog.focus)
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if dialog.focus != planFocusReview {
		t.Fatalf("Shift+Tab focus=%s", dialog.focus)
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	reviewOffset := dialog.viewport.offset
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if dialog.selectedIdx != 1 || dialog.viewport.offset != reviewOffset {
		t.Fatalf(
			"actions Down selection=%d offset=%d",
			dialog.selectedIdx,
			dialog.viewport.offset,
		)
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if dialog.viewport.offset >= reviewOffset {
		t.Fatalf(
			"actions PageUp offset=%d, started %d",
			dialog.viewport.offset,
			reviewOffset,
		)
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	if dialog.selectedIdx != len(dialog.options)-1 {
		t.Fatalf("actions End selection=%d", dialog.selectedIdx)
	}
	done, _ := dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || dialog.focus != planFocusFeedback {
		t.Fatalf("Revise action done=%v focus=%s", done, dialog.focus)
	}
	dialog.viewport.offset = dialog.viewport.maxOffset()
	feedbackOffset := dialog.viewport.offset
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if dialog.viewport.offset >= feedbackOffset {
		t.Fatalf(
			"feedback PageUp offset=%d, started %d",
			dialog.viewport.offset,
			feedbackOffset,
		)
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("cover rollback"))})
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if dialog.focus != planFocusActions ||
		dialog.Feedback() != "cover rollback" {
		t.Fatalf(
			"feedback Esc focus=%s draft=%q",
			dialog.focus,
			dialog.Feedback(),
		)
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	done, _ = dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || dialog.IsVisible() {
		t.Fatalf("feedback submit done=%v visible=%v", done, dialog.IsVisible())
	}
	select {
	case response := <-responseCh:
		if response != PermissionDeny {
			t.Fatalf("feedback response=%v", response)
		}
	default:
		t.Fatal("feedback response was not delivered")
	}
}

func TestP201PlanPointerUsesPublishedHitboxes(t *testing.T) {
	dialog, _ := p201PlanDialog(t, permission.ModeDefault, 48)
	dialog.Overlay("", 80, 24)
	if dialog.geometry.review.Height == 0 ||
		len(dialog.geometry.actions) != len(dialog.options) {
		t.Fatalf("published geometry = %#v", dialog.geometry)
	}

	actionPoint := dialog.geometry.actions[0]
	dialog.HandleMouse(tuiMouseMsg{
		X:      actionPoint.X + 1,
		Y:      actionPoint.Y,
		Button: tea.MouseWheelDown,
		Action: mouseActionPress,
	})
	if dialog.viewport.offset != 0 {
		t.Fatalf("wheel over actions offset=%d", dialog.viewport.offset)
	}

	reviewPoint := dialog.geometry.review
	dialog.HandleMouse(tuiMouseMsg{
		X:      reviewPoint.X,
		Y:      reviewPoint.Y,
		Button: tea.MouseWheelDown,
		Action: mouseActionPress,
	})
	if dialog.viewport.offset == 0 {
		t.Fatal("wheel inside Review did not scroll")
	}

	last := len(dialog.geometry.actions) - 1
	actionPoint = dialog.geometry.actions[last]
	dialog.HandleMouse(tuiMouseMsg{
		X:      actionPoint.X + 2,
		Y:      actionPoint.Y,
		Button: tea.MouseLeft,
		Action: mouseActionPress,
	})
	if dialog.focus != planFocusActions || dialog.selectedIdx != last {
		t.Fatalf("action click focus=%s selection=%d", dialog.focus, dialog.selectedIdx)
	}
	dialog.HandleMouse(tuiMouseMsg{
		X:      reviewPoint.X,
		Y:      reviewPoint.Y,
		Button: tea.MouseLeft,
		Action: mouseActionPress,
	})
	if dialog.focus != planFocusReview || dialog.selectedIdx != last {
		t.Fatalf("review click focus=%s selection=%d", dialog.focus, dialog.selectedIdx)
	}
}

func TestP201PlanModalPointerDoesNotLeakToChat(t *testing.T) {
	app := New(Config{Resumed: true})
	app.width = 80
	app.height = 24
	for index := range 30 {
		app.chat.AppendUser(fmt.Sprintf("chat row %02d", index))
	}
	app.updateLayout()
	app.chat.Render(app.layout.chatRect.Width, app.layout.chatRect.Height)
	app.chat.ScrollToTop()
	beforeIndex := app.chat.offsetIdx
	beforeLine := app.chat.offsetLine

	dialog, _ := p201PlanDialog(t, permission.ModeDefault, 48)
	app.planDialog = dialog
	app.pushDialog(StatePlanApproval)
	_ = app.renderView()
	review := app.planDialog.geometry.review
	_, _ = app.Update(tuiMouseMsg{
		X:      review.X,
		Y:      review.Y,
		Button: tea.MouseWheelDown,
		Action: mouseActionPress,
	})
	if app.planDialog.viewport.offset == 0 {
		t.Fatal("modal review did not receive its wheel event")
	}
	if app.chat.offsetIdx != beforeIndex || app.chat.offsetLine != beforeLine {
		t.Fatalf(
			"modal wheel leaked to chat: (%d,%d) -> (%d,%d)",
			beforeIndex,
			beforeLine,
			app.chat.offsetIdx,
			app.chat.offsetLine,
		)
	}
}

func TestP201PlanResponsiveLayoutAndStatePreservation(t *testing.T) {
	viewports := []struct {
		name          string
		width, height int
	}{
		{name: "compact", width: 40, height: 12},
		{name: "standard", width: 80, height: 24},
		{name: "wide", width: 132, height: 30},
		{name: "tall", width: 80, height: 40},
	}
	for _, viewport := range viewports {
		t.Run(viewport.name, func(t *testing.T) {
			for _, focus := range []planDialogFocus{
				planFocusReview,
				planFocusActions,
				planFocusFeedback,
			} {
				dialog, _ := p201PlanDialog(t, permission.ModeDefault, 48)
				dialog.focus = focus
				if focus == planFocusFeedback {
					dialog.selectedIdx = len(dialog.options) - 1
					dialog.feedbackEditor.SetValue("compact draft")
					setTextareaRuneCursor(
						&dialog.feedbackEditor,
						len([]rune(dialog.Feedback())),
					)
					dialog.feedbackEditor.Focus()
				}
				rendered := dialog.Overlay("", viewport.width, viewport.height)
				lines := strings.Split(rendered, "\n")
				if len(lines) != viewport.height {
					t.Fatalf(
						"%s line count=%d, want %d",
						focus,
						len(lines),
						viewport.height,
					)
				}
				for index, line := range lines {
					if width := xansi.StringWidth(line); width > viewport.width {
						t.Fatalf(
							"%s line %d width=%d, limit=%d: %q",
							focus,
							index,
							width,
							viewport.width,
							xansi.Strip(line),
						)
					}
				}
				if dialog.geometry.review.Height < 1 ||
					len(dialog.geometry.actions) != len(dialog.options) {
					t.Fatalf("%s layout geometry=%#v", focus, dialog.geometry)
				}
				plain := xansi.Strip(rendered)
				if !strings.Contains(plain, "\n "+focus.String()+" ·") {
					t.Fatalf("%s sticky focus footer missing:\n%s", focus, plain)
				}
				if viewport.height >= 22 &&
					!strings.Contains(plain, "plan.md") {
					t.Fatalf("%s editor/path footer missing:\n%s", focus, plain)
				}
			}
		})
	}

	dialog, _ := p201PlanDialog(t, permission.ModeDefault, 72)
	dialog.Overlay("", 80, 24)
	dialog.viewport.offset = dialog.viewport.maxOffset() - 1
	dialog.focus = planFocusActions
	dialog.selectedIdx = 2
	offset := dialog.viewport.offset
	dialog.SetStyles(StylesForTheme(ThemeDaybreak))
	dialog.Overlay("", 80, 24)
	if dialog.focus != planFocusActions ||
		dialog.selectedIdx != 2 ||
		dialog.viewport.offset != offset {
		t.Fatalf(
			"theme changed state focus=%s selection=%d offset=%d",
			dialog.focus,
			dialog.selectedIdx,
			dialog.viewport.offset,
		)
	}
	dialog.Overlay("", 80, 40)
	if dialog.focus != planFocusActions ||
		dialog.selectedIdx != 2 ||
		dialog.viewport.offset > offset ||
		dialog.viewport.offset != dialog.viewport.maxOffset() {
		t.Fatalf(
			"resize did not preserve/clamp focus=%s selection=%d offset=%d max=%d",
			dialog.focus,
			dialog.selectedIdx,
			dialog.viewport.offset,
			dialog.viewport.maxOffset(),
		)
	}
	clampedOffset := dialog.viewport.offset
	dialog.Overlay("", 40, 12)
	if dialog.focus != planFocusActions ||
		dialog.selectedIdx != 2 ||
		dialog.viewport.offset != clampedOffset {
		t.Fatalf(
			"compact resize changed state focus=%s selection=%d offset=%d, want %d",
			dialog.focus,
			dialog.selectedIdx,
			dialog.viewport.offset,
			clampedOffset,
		)
	}
}

func TestP201PlanDialogGoldenStates(t *testing.T) {
	dialog, _ := p201PlanDialog(t, permission.ModeDefault, 16)
	var actual strings.Builder
	for _, state := range []struct {
		name  string
		focus planDialogFocus
	}{
		{name: "review", focus: planFocusReview},
		{name: "actions", focus: planFocusActions},
		{name: "feedback", focus: planFocusFeedback},
	} {
		dialog.focus = state.focus
		dialog.selectedIdx = min(1, len(dialog.options)-1)
		dialog.feedbackEditor.Reset()
		dialog.feedbackEditor.Blur()
		if state.focus == planFocusFeedback {
			dialog.selectedIdx = len(dialog.options) - 1
			dialog.feedbackEditor.SetValue("cover rollback")
			setTextareaRuneCursor(
				&dialog.feedbackEditor,
				len([]rune(dialog.Feedback())),
			)
			dialog.feedbackEditor.Focus()
		}
		actual.WriteString("== " + state.name + " ==\n")
		actual.WriteString(normalizeP201PlanGolden(
			dialog.Overlay("", 64, 18),
		))
		actual.WriteString("\n")
	}

	path := "testdata/plan_dialog_states.golden"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(actual.String()), 0o600); err != nil {
			t.Fatalf("update Plan dialog golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Plan dialog golden: %v", err)
	}
	if actual.String() != string(want) {
		t.Fatalf(
			"Plan dialog golden mismatch:\n--- got ---\n%s--- want ---\n%s",
			actual.String(),
			want,
		)
	}
}

func TestG241BypassConfirmationOwnsKeyboard(t *testing.T) {
	dialog, responseCh := p201PlanDialog(t, permission.ModeDefault, 48)
	dialog.viewport.offset = 3
	dialog.feedbackEditor.SetValue("retained draft")
	cursor := dialog.feedbackCursorOffset()
	dialog.feedbackUndo = []textEditorSnapshot{
		captureTextEditorSnapshot(dialog.feedbackEditor),
	}
	dialog.focus = planFocusActions
	dialog.selectedIdx = 2
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if dialog.focus != planFocusBypassConfirmation {
		t.Fatalf("focus = %v", dialog.focus)
	}

	assertIsolated := func(label string) {
		t.Helper()
		if dialog.focus != planFocusBypassConfirmation ||
			dialog.selectedIdx != 2 ||
			dialog.viewport.offset != 3 ||
			dialog.Feedback() != "retained draft" ||
			dialog.feedbackCursorOffset() != cursor ||
			len(dialog.feedbackUndo) != 1 ||
			dialog.PlanResult() != nil ||
			dialog.EditorActive() {
			t.Fatalf(
				"%s mutated confirmation state: focus=%v selection=%d offset=%d draft=%q result=%#v editor=%v",
				label,
				dialog.focus,
				dialog.selectedIdx,
				dialog.viewport.offset,
				dialog.Feedback(),
				dialog.PlanResult(),
				dialog.EditorActive(),
			)
		}
		select {
		case response := <-responseCh:
			t.Fatalf("%s emitted response %v", label, response)
		default:
		}
	}

	for name, msg := range map[string]tea.KeyPressMsg{
		"pgup":      {Code: tea.KeyPgUp},
		"pgdown":    {Code: tea.KeyPgDown},
		"home":      {Code: tea.KeyHome},
		"end":       {Code: tea.KeyEnd},
		"ctrl+g":    {Code: 'g', Mod: tea.ModCtrl},
		"text":      {Code: 'x', Text: "x"},
		"space":     {Code: tea.KeySpace},
		"backspace": {Code: tea.KeyBackspace},
		"delete":    {Code: tea.KeyDelete},
		"left":      {Code: tea.KeyLeft},
		"right":     {Code: tea.KeyRight},
	} {
		dialog.HandleKey(msg)
		assertIsolated(name)
		if dialog.bypassConfirmYes {
			t.Fatalf("%s toggled the confirmation selection", name)
		}
	}

	for name, msg := range map[string]tea.KeyPressMsg{
		"up":        {Code: tea.KeyUp},
		"k":         {Code: 'k', Text: "k"},
		"down":      {Code: tea.KeyDown},
		"j":         {Code: 'j', Text: "j"},
		"tab":       {Code: tea.KeyTab},
		"shift+tab": {Code: tea.KeyTab, Mod: tea.ModShift},
	} {
		before := dialog.bypassConfirmYes
		dialog.HandleKey(msg)
		if dialog.bypassConfirmYes == before {
			t.Fatalf("%s did not toggle the confirmation selection", name)
		}
		assertIsolated(name)
	}

	// Even toggle count leaves No selected; Enter returns to actions.
	done, _ := dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || dialog.focus != planFocusActions ||
		dialog.selectedIdx != 2 ||
		dialog.viewport.offset != 3 ||
		dialog.Feedback() != "retained draft" ||
		dialog.PlanResult() != nil {
		t.Fatalf(
			"No settled the confirmation: done=%v focus=%v result=%#v",
			done,
			dialog.focus,
			dialog.PlanResult(),
		)
	}

	// Esc returns to actions even with Yes selected and never settles.
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	done, _ = dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if done || dialog.focus != planFocusActions ||
		dialog.selectedIdx != 2 ||
		dialog.viewport.offset != 3 ||
		dialog.Feedback() != "retained draft" ||
		dialog.PlanResult() != nil {
		t.Fatalf(
			"Esc settled the confirmation: done=%v focus=%v result=%#v",
			done,
			dialog.focus,
			dialog.PlanResult(),
		)
	}

	// Explicit Yes emits the confirmed bypass result exactly once.
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	done, _ = dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	result := dialog.PlanResult()
	if !done || result == nil ||
		result.Outcome != engine.PlanApprovalApprove ||
		result.TargetMode != permission.ModeBypassPermissions ||
		!result.Confirmed ||
		result.Feedback != "" {
		t.Fatalf("explicit Yes result: done=%v result=%#v", done, result)
	}
	if response := <-responseCh; response != PermissionAllow {
		t.Fatalf("explicit Yes response = %v", response)
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	dialog.HandleMouse(tuiMouseMsg{
		X:      0,
		Y:      0,
		Button: tea.MouseLeft,
		Action: mouseActionPress,
	})
	select {
	case extra := <-responseCh:
		t.Fatalf("confirmation emitted a second response %v", extra)
	default:
	}
}

func TestG241BypassConfirmationPublishesOnlyConfirmationHitboxes(t *testing.T) {
	dialog, responseCh := p201PlanDialog(t, permission.ModeDefault, 48)
	dialog.focus = planFocusActions
	dialog.selectedIdx = len(dialog.options) - 1
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if dialog.focus != planFocusFeedback {
		t.Fatalf("feedback focus = %v", dialog.focus)
	}
	dialog.feedbackEditor.SetValue("retained draft")
	cursor := dialog.feedbackCursorOffset()
	dialog.feedbackUndo = []textEditorSnapshot{
		captureTextEditorSnapshot(dialog.feedbackEditor),
	}
	dialog.Overlay("", 80, 24)
	staleReview := dialog.geometry.review
	staleActions := append([]layoutRect(nil), dialog.geometry.actions...)
	staleFeedback := dialog.geometry.feedback
	if staleReview.Height == 0 ||
		len(staleActions) == 0 ||
		staleFeedback.Height == 0 {
		t.Fatalf("pre-confirmation geometry = %#v", dialog.geometry)
	}

	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	dialog.selectedIdx = 2
	dialog.viewport.offset = 2
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	assertIsolated := func(label string) {
		t.Helper()
		if dialog.focus != planFocusBypassConfirmation ||
			dialog.selectedIdx != 2 ||
			dialog.viewport.offset != 2 ||
			dialog.Feedback() != "retained draft" ||
			dialog.feedbackCursorOffset() != cursor ||
			len(dialog.feedbackUndo) != 1 ||
			dialog.PlanResult() != nil {
			t.Fatalf(
				"%s escaped the confirmation: focus=%v selection=%d offset=%d draft=%q result=%#v",
				label,
				dialog.focus,
				dialog.selectedIdx,
				dialog.viewport.offset,
				dialog.Feedback(),
				dialog.PlanResult(),
			)
		}
		select {
		case response := <-responseCh:
			t.Fatalf("%s emitted response %v", label, response)
		default:
		}
	}

	// Before the first confirmation render, all geometry still belongs to the
	// previous feedback frame. It must be inert rather than interpreted as a
	// current confirmation control.
	for label, rect := range map[string]layoutRect{
		"pre-render review":   staleReview,
		"pre-render action":   staleActions[0],
		"pre-render feedback": staleFeedback,
	} {
		dialog.HandleMouse(tuiMouseMsg{
			X: rect.X, Y: rect.Y,
			Button: tea.MouseLeft, Action: mouseActionPress,
		})
		assertIsolated(label)
	}

	var geometry planDialogGeometry
	for _, frame := range []struct {
		name   string
		height int
	}{
		{name: "compact", height: 18},
		{name: "standard", height: 24},
	} {
		dialog.Overlay("", 80, frame.height)
		geometry = dialog.geometry
		if geometry.outer.Width == 0 ||
			geometry.bypassNo.Height != 1 ||
			geometry.bypassYes.Height != 1 ||
			geometry.bypassNo.Y >= geometry.bypassYes.Y {
			t.Fatalf("%s confirmation hitboxes = %#v", frame.name, geometry)
		}
		if geometry.review != (layoutRect{}) ||
			geometry.feedback != (layoutRect{}) ||
			len(geometry.actions) != 0 {
			t.Fatalf("%s confirmation frame leaked geometry = %#v", frame.name, geometry)
		}
	}

	// Wheel, motion, and release are exact no-ops, even over live hitboxes.
	dialog.HandleMouse(tuiMouseMsg{
		X: staleReview.X, Y: staleReview.Y,
		Button: tea.MouseWheelDown, Action: mouseActionPress,
	})
	dialog.HandleMouse(tuiMouseMsg{
		X: geometry.bypassYes.X, Y: geometry.bypassYes.Y,
		Button: tea.MouseWheelUp, Action: mouseActionPress,
	})
	dialog.HandleMouse(tuiMouseMsg{
		X: geometry.bypassYes.X, Y: geometry.bypassYes.Y,
		Button: tea.MouseLeft, Action: mouseActionMotion,
	})
	dialog.HandleMouse(tuiMouseMsg{
		X: geometry.bypassYes.X, Y: geometry.bypassYes.Y,
		Button: tea.MouseLeft, Action: mouseActionRelease,
	})
	assertIsolated("wheel/motion/release")

	// Stale review, action, and feedback coordinates no longer hit anything.
	// Rows now occupied by the current No/Yes hitboxes act as those hitboxes;
	// only genuinely non-overlapping stale rows must be exact no-ops.
	dialog.HandleMouse(tuiMouseMsg{
		X: staleReview.X, Y: staleReview.Y,
		Button: tea.MouseLeft, Action: mouseActionPress,
	})
	assertIsolated("stale review")
	clickedStale := 0
	for index, rect := range staleActions {
		if rect.Y >= geometry.bypassNo.Y-1 && rect.Y <= geometry.bypassYes.Y {
			continue
		}
		clickedStale++
		dialog.HandleMouse(tuiMouseMsg{
			X: rect.X + 1, Y: rect.Y,
			Button: tea.MouseLeft, Action: mouseActionPress,
		})
		assertIsolated(fmt.Sprintf("stale action %d", index))
	}
	if clickedStale == 0 {
		t.Fatal("no stale action row was outside the confirmation rows")
	}

	// Clicks between and around the No/Yes rows are exact no-ops.
	dialog.HandleMouse(tuiMouseMsg{
		X: geometry.bypassNo.X, Y: geometry.bypassNo.Y - 1,
		Button: tea.MouseLeft, Action: mouseActionPress,
	})
	dialog.HandleMouse(tuiMouseMsg{
		X: geometry.bypassYes.X, Y: geometry.bypassYes.Y + 1,
		Button: tea.MouseLeft, Action: mouseActionPress,
	})
	assertIsolated("outside hitboxes")

	// Clicking No returns to actions with no settlement.
	dialog.HandleMouse(tuiMouseMsg{
		X: geometry.bypassNo.X + 2, Y: geometry.bypassNo.Y,
		Button: tea.MouseLeft, Action: mouseActionPress,
	})
	if dialog.focus != planFocusActions ||
		dialog.selectedIdx != 2 ||
		dialog.viewport.offset != 2 ||
		dialog.PlanResult() != nil {
		t.Fatalf(
			"No click settled the confirmation: focus=%v result=%#v",
			dialog.focus,
			dialog.PlanResult(),
		)
	}

	// A previous confirmation frame cannot authorize a later confirmation
	// before that later frame publishes its own current hitboxes.
	staleYes := geometry.bypassYes
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	dialog.HandleMouse(tuiMouseMsg{
		X: staleYes.X + 2, Y: staleYes.Y,
		Button: tea.MouseLeft, Action: mouseActionPress,
	})
	assertIsolated("No then re-enter before render")

	// Esc has the same stale-frame boundary.
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	dialog.HandleMouse(tuiMouseMsg{
		X: staleYes.X + 2, Y: staleYes.Y,
		Button: tea.MouseLeft, Action: mouseActionPress,
	})
	assertIsolated("Esc then re-enter before render")

	// Clicking a Yes hitbox published by the new frame emits the confirmed
	// bypass result exactly once.
	dialog.Overlay("", 80, 24)
	yes := dialog.geometry.bypassYes
	if yes.Height != 1 {
		t.Fatalf("re-rendered Yes hitbox = %#v", dialog.geometry)
	}
	dialog.HandleMouse(tuiMouseMsg{
		X: yes.X + 2, Y: yes.Y,
		Button: tea.MouseLeft, Action: mouseActionPress,
	})
	result := dialog.PlanResult()
	if result == nil ||
		result.Outcome != engine.PlanApprovalApprove ||
		result.TargetMode != permission.ModeBypassPermissions ||
		!result.Confirmed {
		t.Fatalf("Yes click result = %#v", result)
	}
	if response := <-responseCh; response != PermissionAllow {
		t.Fatalf("Yes click response = %v", response)
	}
	dialog.HandleMouse(tuiMouseMsg{
		X: yes.X + 2, Y: yes.Y,
		Button: tea.MouseLeft, Action: mouseActionPress,
	})
	select {
	case extra := <-responseCh:
		t.Fatalf("Yes click emitted a second response %v", extra)
	default:
	}
}

func TestG241BypassConfirmationClipsAndClearsGeometryOnResize(t *testing.T) {
	dialog, responseCh := p201PlanDialog(t, permission.ModeDefault, 48)
	dialog.focus = planFocusActions
	dialog.selectedIdx = 2
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	dialog.Overlay("", 80, 24)
	yes := dialog.geometry.bypassYes
	if yes.Height == 0 {
		t.Fatalf("initial Yes hitbox = %#v", dialog.geometry)
	}

	// An invalid frame clears every published hitbox, including the
	// confirmation rows, so stale coordinates cannot activate Yes.
	if rendered := dialog.Overlay("", 0, 0); rendered != "" {
		t.Fatalf("invalid overlay = %q", rendered)
	}
	if dialog.geometry.outer != (layoutRect{}) ||
		dialog.geometry.bypassNo != (layoutRect{}) ||
		dialog.geometry.bypassYes != (layoutRect{}) {
		t.Fatalf("invalid frame geometry = %#v", dialog.geometry)
	}
	dialog.HandleMouse(tuiMouseMsg{
		X: yes.X, Y: yes.Y,
		Button: tea.MouseLeft, Action: mouseActionPress,
	})
	if dialog.focus != planFocusBypassConfirmation || dialog.PlanResult() != nil {
		t.Fatalf(
			"stale Yes coordinate escaped: focus=%v result=%#v",
			dialog.focus,
			dialog.PlanResult(),
		)
	}

	// A frame too short for the confirmation rows clips both hitboxes away.
	if rendered := dialog.Overlay("", 80, 6); rendered == "" {
		t.Fatal("short overlay rendered nothing")
	}
	if dialog.geometry.bypassNo.Height != 0 ||
		dialog.geometry.bypassYes.Height != 0 {
		t.Fatalf("clipped confirmation geometry = %#v", dialog.geometry)
	}
	dialog.HandleMouse(tuiMouseMsg{
		X: yes.X, Y: yes.Y,
		Button: tea.MouseLeft, Action: mouseActionPress,
	})
	if dialog.focus != planFocusBypassConfirmation || dialog.PlanResult() != nil {
		t.Fatalf(
			"clipped Yes coordinate escaped: focus=%v result=%#v",
			dialog.focus,
			dialog.PlanResult(),
		)
	}
	select {
	case response := <-responseCh:
		t.Fatalf("clipped frame emitted response %v", response)
	default:
	}
}

func normalizeP201PlanGolden(rendered string) string {
	lines := strings.Split(xansi.Strip(rendered), "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " \t")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

func p201PlanDialog(
	t *testing.T,
	returnMode permission.Mode,
	stepCount int,
) (*PlanDialog, chan PermissionResponse) {
	t.Helper()
	var plan strings.Builder
	plan.WriteString("# Review target\n\n")
	for index := range stepCount {
		fmt.Fprintf(
			&plan,
			"- Step %02d keeps the review viewport deterministic.\n",
			index+1,
		)
	}
	planBytes := []byte(plan.String())
	planPath := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(planPath, planBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	responseCh := make(chan PermissionResponse, 1)
	dialog := NewPlanDialog(defaultStyles())
	dialog.Show("main", "session", "agent", &engine.PlanApprovalRequest{
		RequestID:         "p20-1",
		PlanRevision:      1,
		PlanFileIdentity:  planPath,
		InitialPlanDigest: engine.PlanBytesDigest(planBytes),
		ReturnMode:        returnMode,
	}, responseCh)
	return dialog, responseCh
}
