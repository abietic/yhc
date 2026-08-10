package tui

import (
	"fmt"
	"image/color"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/internal/tui/keybindings"
	"github.com/abietic/yhc/internal/tui/terminalcap"
)

func TestP202FeedbackEditorSupportsComposerEditingContract(t *testing.T) {
	dialog, responseCh := p201PlanDialog(
		t,
		permission.ModeDefault,
		48,
	)
	enterP202Feedback(t, dialog)

	typeP202Feedback(dialog, "alpha 世界 e\u0301 👩‍💻")
	dialog.HandleKey(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	typeP202Feedback(dialog, "second word")
	if got, want := dialog.Feedback(),
		"alpha 世界 e\u0301 👩‍💻\nsecond word"; got != want {
		t.Fatalf("multiline feedback = %q, want %q", got, want)
	}

	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
	typeP202Feedback(dialog, ">")
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	dialog.HandleKey(tea.KeyPressMsg{Code: 'b', Mod: tea.ModAlt})
	typeP202Feedback(dialog, "X")
	if !strings.HasSuffix(dialog.Feedback(), "\n>second Xword") {
		t.Fatalf("word movement feedback = %q", dialog.Feedback())
	}

	dialog.HandleKey(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if !strings.HasSuffix(dialog.Feedback(), "\n>second word") {
		t.Fatalf("undo feedback = %q", dialog.Feedback())
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyHome})
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyDelete})
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if !strings.HasSuffix(dialog.Feedback(), "\nsecond wor") {
		t.Fatalf("delete/backspace feedback = %q", dialog.Feedback())
	}
	dialog.HandleKey(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if !strings.HasSuffix(dialog.Feedback(), "\nsecond word") {
		t.Fatalf("delete undo feedback = %q", dialog.Feedback())
	}

	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	cursorOffset := dialog.feedbackCursorOffset()
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if dialog.focus != planFocusActions ||
		dialog.Feedback() == "" ||
		dialog.feedbackCursorOffset() != cursorOffset {
		t.Fatalf(
			"Esc state = focus:%s feedback:%q cursor:%d, want cursor:%d",
			dialog.focus,
			dialog.Feedback(),
			dialog.feedbackCursorOffset(),
			cursorOffset,
		)
	}
	enterP202Feedback(t, dialog)
	if dialog.feedbackCursorOffset() != cursorOffset {
		t.Fatalf(
			"re-enter cursor = %d, want %d",
			dialog.feedbackCursorOffset(),
			cursorOffset,
		)
	}

	done, _ := dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || dialog.IsVisible() {
		t.Fatalf("submit done=%v visible=%v", done, dialog.IsVisible())
	}
	select {
	case response := <-responseCh:
		if response != PermissionDeny {
			t.Fatalf("response = %v, want deny/Revise projection", response)
		}
	default:
		t.Fatal("feedback response missing")
	}
}

func TestP202FeedbackUsesConfiguredSubmitNewlineAndUndoKeys(t *testing.T) {
	resolver := keybindings.NewResolver()
	resolver.SetBindings([]keybindings.Block{{
		Context: keybindings.ContextChat,
		Bindings: map[string]keybindings.Action{
			"alt+s":  keybindings.ActionChatSubmit,
			"alt+n":  keybindings.ActionChatNewline,
			"ctrl+u": keybindings.ActionChatUndo,
		},
	}})
	dialog, _ := p202PlanDialog(
		t,
		resolver,
		false,
		permission.ModeDefault,
		48,
	)
	enterP202Feedback(t, dialog)

	typeP202Feedback(dialog, "first")
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'n'}), Mod: tea.ModAlt})
	typeP202Feedback(dialog, "second")
	dialog.HandleKey(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if got := dialog.Feedback(); got != "first\n" {
		t.Fatalf("configured undo feedback = %q", got)
	}
	typeP202Feedback(dialog, "second")

	rendered := xansi.Strip(dialog.Overlay("", 80, 24))
	for _, hint := range []string{
		"alt+s send",
		"alt+n newline",
		"ctrl+u undo",
		"esc actions",
	} {
		if !strings.Contains(rendered, hint) {
			t.Fatalf("configured hint %q missing:\n%s", hint, rendered)
		}
	}
	if strings.Contains(rendered, "enter send") {
		t.Fatalf("stale default submit hint rendered:\n%s", rendered)
	}

	done, _ := dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{'s'}), Mod: tea.ModAlt})
	if !done || dialog.Feedback() != "first\nsecond" {
		t.Fatalf("configured submit done=%v feedback=%q", done, dialog.Feedback())
	}
}

func TestP202PointerFocusChangeClearsPendingFeedbackChord(t *testing.T) {
	resolver := keybindings.NewResolver()
	resolver.SetBindings([]keybindings.Block{{
		Context: keybindings.ContextChat,
		Bindings: map[string]keybindings.Action{
			"ctrl+x ctrl+s": keybindings.ActionChatSubmit,
		},
	}})
	dialog, _ := p202PlanDialog(
		t,
		resolver,
		false,
		permission.ModeDefault,
		32,
	)
	enterP202Feedback(t, dialog)
	dialog.Overlay("", 80, 24)

	done, _ := dialog.HandleKey(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if done {
		t.Fatal("pending chord unexpectedly settled feedback")
	}
	action := dialog.geometry.actions[0]
	dialog.HandleMouse(tuiMouseMsg{
		X:      action.X,
		Y:      action.Y,
		Button: tea.MouseLeft,
		Action: mouseActionPress,
	})
	if dialog.focus != planFocusActions {
		t.Fatalf("pointer focus = %s, want Actions", dialog.focus)
	}
	if got := resolver.ResolveEvent(
		tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl},
		keybindings.ContextChat,
	); got.Kind != keybindings.ResolutionNone {
		t.Fatalf("post-pointer chord resolution = %#v", got)
	}
}

func TestP202EmptyFeedbackReturnsToActionsWithoutSettling(t *testing.T) {
	dialog, responseCh := p201PlanDialog(
		t,
		permission.ModeDefault,
		16,
	)
	enterP202Feedback(t, dialog)
	typeP202Feedback(dialog, " \n")

	done, _ := dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || dialog.focus != planFocusActions || !dialog.IsVisible() {
		t.Fatalf(
			"empty submit = done:%v focus:%s visible:%v",
			done,
			dialog.focus,
			dialog.IsVisible(),
		)
	}
	select {
	case response := <-responseCh:
		t.Fatalf("empty feedback settled response %v", response)
	default:
	}
}

func TestP20R2NoColorFeedbackCaretIsRenderOnly(t *testing.T) {
	dialog, _ := p202PlanDialog(
		t,
		keybindings.NewResolver(),
		true,
		permission.ModeDefault,
		24,
	)
	dialog.feedbackNoColor = true
	enterP202Feedback(t, dialog)
	typeP202Feedback(dialog, "a世e\u0301👩‍💻")
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	wantValue := dialog.Feedback()
	wantCursor := dialog.feedbackCursorOffset()
	wantUndo := len(dialog.feedbackUndo)

	visible := xansi.Strip(dialog.Overlay("", 48, 24))
	if !strings.Contains(visible, "▏") {
		t.Fatalf("visible no-color caret missing: %q", visible)
	}
	if dialog.Feedback() != wantValue || dialog.feedbackCursorOffset() != wantCursor || len(dialog.feedbackUndo) != wantUndo {
		t.Fatalf("render changed editor: value=%q cursor=%d undo=%d", dialog.Feedback(), dialog.feedbackCursorOffset(), len(dialog.feedbackUndo))
	}

	dialog.feedbackEditor.SetVirtualCursor(false)
	hidden := xansi.Strip(dialog.Overlay("", 48, 24))
	if strings.Contains(hidden, "▏") {
		t.Fatalf("blink-hidden no-color caret remained visible: %q", hidden)
	}
	if dialog.Feedback() != wantValue || dialog.feedbackCursorOffset() != wantCursor || len(dialog.feedbackUndo) != wantUndo {
		t.Fatalf("blink render changed editor: value=%q cursor=%d undo=%d", dialog.Feedback(), dialog.feedbackCursorOffset(), len(dialog.feedbackUndo))
	}
}

func TestP20R2FeedbackCursorFinalCellsAcrossProfilesAndPositions(
	t *testing.T,
) {
	profiles := []struct {
		name  string
		theme ThemeName
	}{
		{name: "polar-night", theme: ThemePolarNight},
		{name: "daybreak", theme: ThemeDaybreak},
		{name: "snowy", theme: ThemeSnowy},
		{name: "aubergine", theme: ThemeAubergine},
		{name: "ansi-16", theme: ThemeDarkAnsi},
	}

	var golden strings.Builder
	for _, profile := range profiles {
		palette := getPalette(profile.theme)
		wantBackground := p20R2RenderedBackground(t, palette.brand)
		wantForeground := p20R2RenderedBackground(t, palette.element)
		if p20R2SameColor(wantBackground, wantForeground) {
			t.Fatalf(
				"%s cursor and input surface collapse to %s",
				profile.name,
				p20R2ColorString(wantBackground),
			)
		}
		for _, position := range p20R2CursorPositions {
			dialog, _ := p202PlanDialog(
				t,
				keybindings.NewResolver(),
				true,
				permission.ModeDefault,
				24,
			)
			dialog.SetStyles(StylesForTheme(profile.theme))
			enterP202Feedback(t, dialog)
			p20R2SetCursorPosition(dialog, position)

			rendered := dialog.Overlay("", 80, 24)
			p20R2AssertFrameBounds(t, rendered, 80, 24)
			cells := p20R2CellsWithBackground(
				t,
				rendered,
				80,
				dialog.geometry.feedback,
				wantBackground,
			)
			if len(cells) != 1 {
				t.Fatalf(
					"%s/%s final cursor cells=%d, want 1:\n%s",
					profile.name,
					position.name,
					len(cells),
					xansi.Strip(rendered),
				)
			}
			cell := cells[0]
			if !p20R2SameColor(cell.foreground, wantForeground) {
				t.Fatalf(
					"%s/%s final cursor foreground=%s, want %s",
					profile.name,
					position.name,
					p20R2ColorString(cell.foreground),
					p20R2ColorString(wantForeground),
				)
			}
			fmt.Fprintf(
				&golden,
				"%s/%s content=%q cell=%d,%d fg=%s bg=%s\n",
				profile.name,
				position.name,
				cell.content,
				cell.x,
				cell.y-dialog.geometry.feedback.Y,
				p20R2ColorString(cell.foreground),
				p20R2ColorString(cell.background),
			)
		}
	}

	if width := xansi.StringWidth(planFeedbackNoColorCaret); width != 1 {
		t.Fatalf("no-color caret width=%d, want 1", width)
	}
	for _, position := range p20R2CursorPositions {
		dialog, _ := p202PlanDialog(
			t,
			keybindings.NewResolver(),
			true,
			permission.ModeDefault,
			24,
		)
		dialog.feedbackNoColor = true
		enterP202Feedback(t, dialog)
		p20R2SetCursorPosition(dialog, position)

		rendered := xansi.Strip(dialog.Overlay("", 80, 24))
		p20R2AssertFrameBounds(t, rendered, 80, 24)
		plain := rendered
		if got := p20R2CaretCountInsideFeedback(
			plain,
			dialog.geometry.feedback,
		); got != 1 {
			t.Fatalf(
				"no-color/%s caret count=%d, want 1:\n%s",
				position.name,
				got,
				plain,
			)
		}
		if !strings.Contains(plain, position.noColorProjection) {
			t.Fatalf(
				"no-color/%s projection %q missing:\n%s",
				position.name,
				position.noColorProjection,
				plain,
			)
		}
		fmt.Fprintf(
			&golden,
			"no-color/%s projection=%q ansi=%t\n",
			position.name,
			position.noColorProjection,
			strings.Contains(rendered, "\x1b["),
		)
	}

	path := "testdata/plan_feedback_cursor.golden"
	actual := golden.String()
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(actual), 0o600); err != nil {
			t.Fatalf("update Plan feedback cursor golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Plan feedback cursor golden: %v", err)
	}
	if actual != string(want) {
		t.Fatalf(
			"Plan feedback cursor golden mismatch:\n--- got ---\n%s--- want ---\n%s",
			actual,
			want,
		)
	}
}

func TestP20R2FeedbackCursorFocusBlinkAndReducedMotion(t *testing.T) {
	dialog, _ := p202PlanDialog(
		t,
		keybindings.NewResolver(),
		false,
		permission.ModeDefault,
		24,
	)
	dialog.SetStyles(StylesForTheme(ThemePolarNight))
	enterP202Feedback(t, dialog)
	p20R2SetCursorPosition(dialog, p20R2CursorPositions[2])
	wantBackground := p20R2RenderedBackground(
		t,
		getPalette(ThemePolarNight).brand,
	)

	p20R2SetStaticCursor(dialog)
	visible := dialog.Overlay("", 80, 24)
	visibleCells := p20R2CellsWithBackground(
		t,
		visible,
		80,
		dialog.geometry.feedback,
		wantBackground,
	)
	if len(visibleCells) != 1 {
		t.Fatalf("blink-visible cursor cells=%d, want 1", len(visibleCells))
	}

	dialog.feedbackEditor.SetVirtualCursor(false)
	hidden := dialog.Overlay("", 80, 24)
	if cells := p20R2CellsWithBackground(
		t,
		hidden,
		80,
		dialog.geometry.feedback,
		wantBackground,
	); len(cells) != 0 {
		t.Fatalf("blink-hidden cursor cells=%d, want 0", len(cells))
	}

	p20R2SetStaticCursor(dialog)
	restored := dialog.Overlay("", 80, 24)
	restoredCells := p20R2CellsWithBackground(
		t,
		restored,
		80,
		dialog.geometry.feedback,
		wantBackground,
	)
	if len(restoredCells) != 1 ||
		restoredCells[0].x != visibleCells[0].x ||
		restoredCells[0].y != visibleCells[0].y {
		t.Fatalf(
			"restored cursor cells=%#v, want same cell %#v",
			restoredCells,
			visibleCells,
		)
	}

	dialog.feedbackEditor.Blur()
	blurred := dialog.Overlay("", 80, 24)
	if cells := p20R2CellsWithBackground(
		t,
		blurred,
		80,
		dialog.geometry.feedback,
		wantBackground,
	); len(cells) != 0 {
		t.Fatalf("blurred cursor cells=%d, want 0", len(cells))
	}
	dialog.feedbackEditor.Focus()

	reduced, _ := p202PlanDialog(
		t,
		keybindings.NewResolver(),
		true,
		permission.ModeDefault,
		24,
	)
	reduced.feedbackNoColor = true
	enterP202Feedback(t, reduced)
	p20R2SetCursorPosition(reduced, p20R2CursorPositions[1])
	if reduced.feedbackEditor.Styles().Cursor.Blink {
		t.Fatal("reduced-motion cursor is not static")
	}
	reducedFrame := xansi.Strip(reduced.Overlay("", 80, 24))
	if got := p20R2CaretCountInsideFeedback(
		reducedFrame,
		reduced.geometry.feedback,
	); got != 1 {
		t.Fatalf("reduced-motion no-color caret count=%d, want 1", got)
	}

	reduced.feedbackEditor.Blur()
	blurredFrame := xansi.Strip(reduced.Overlay("", 80, 24))
	if got := p20R2CaretCountInsideFeedback(
		blurredFrame,
		reduced.geometry.feedback,
	); got != 0 {
		t.Fatalf("blurred no-color caret count=%d, want 0", got)
	}
}

func TestP20R2FeedbackCursorGeometryAndUnicodeStateAcrossResizeTheme(
	t *testing.T,
) {
	dialog, _ := p202PlanDialog(
		t,
		keybindings.NewResolver(),
		true,
		permission.ModeDefault,
		72,
	)
	enterP202Feedback(t, dialog)
	value := "中 e\u0301 क्ष 👩‍💻 Q tail"
	dialog.feedbackEditor.SetValue(value)
	qOffset := strings.Index(value, "Q")
	if qOffset < 0 {
		t.Fatal("Unicode fixture lost Q marker")
	}
	setTextareaRuneCursor(
		&dialog.feedbackEditor,
		utf8.RuneCountInString(value[:qOffset]),
	)
	dialog.feedbackUndo = []textEditorSnapshot{{
		Text:         "previous 中 e\u0301 क्ष 👩‍💻",
		CursorOffset: 2,
	}}
	dialog.Overlay("", 80, 24)
	dialog.viewport.offset = min(1, dialog.viewport.maxOffset())

	wantBytes := []byte(dialog.Feedback())
	wantCursor := dialog.feedbackCursorOffset()
	wantUndo := len(dialog.feedbackUndo)
	wantOffset := dialog.viewport.offset
	viewports := []struct {
		width  int
		height int
	}{
		{width: 40, height: 12},
		{width: 80, height: 24},
		{width: 132, height: 30},
		{width: 80, height: 40},
	}
	themes := []ThemeName{
		ThemePolarNight,
		ThemeDaybreak,
		ThemeSnowy,
		ThemeAubergine,
	}

	for index, viewport := range viewports {
		theme := themes[index]
		dialog.SetStyles(StylesForTheme(theme))
		rendered := dialog.Overlay("", viewport.width, viewport.height)
		p20R2AssertFrameBounds(
			t,
			rendered,
			viewport.width,
			viewport.height,
		)
		wantBackground := p20R2RenderedBackground(
			t,
			getPalette(theme).brand,
		)
		if cells := p20R2CellsWithBackground(
			t,
			rendered,
			viewport.width,
			dialog.geometry.feedback,
			wantBackground,
		); len(cells) != 1 {
			t.Fatalf(
				"%s %dx%d cursor cells=%d, want 1",
				theme,
				viewport.width,
				viewport.height,
				len(cells),
			)
		}
		p20R2AssertEditorState(
			t,
			dialog,
			wantBytes,
			wantCursor,
			wantUndo,
			wantOffset,
		)
	}

	dialog.feedbackNoColor = true
	for _, viewport := range viewports {
		rendered := xansi.Strip(
			dialog.Overlay("", viewport.width, viewport.height),
		)
		p20R2AssertFrameBounds(
			t,
			rendered,
			viewport.width,
			viewport.height,
		)
		if got := p20R2CaretCountInsideFeedback(
			rendered,
			dialog.geometry.feedback,
		); got != 1 {
			t.Fatalf(
				"no-color %dx%d caret count=%d, want 1",
				viewport.width,
				viewport.height,
				got,
			)
		}
		if !strings.Contains(rendered, planFeedbackNoColorCaret+"Q") {
			t.Fatalf(
				"no-color %dx%d did not keep current Q visible after caret",
				viewport.width,
				viewport.height,
			)
		}
		p20R2AssertEditorState(
			t,
			dialog,
			wantBytes,
			wantCursor,
			wantUndo,
			wantOffset,
		)
	}
}

func TestP202FeedbackThemeResizeAndReloadPreserveState(t *testing.T) {
	dialog, _ := p201PlanDialog(
		t,
		permission.ModeDefault,
		72,
	)
	enterP202Feedback(t, dialog)
	typeP202Feedback(dialog, "保留 e\u0301 👩‍💻\nrollback detail")
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	dialog.Overlay("", 80, 24)
	dialog.viewport.offset = min(2, dialog.viewport.maxOffset())

	wantText := dialog.Feedback()
	wantCursor := dialog.feedbackCursorOffset()
	wantOffset := dialog.viewport.offset
	wantUndo := len(dialog.feedbackUndo)
	dialog.SetStyles(StylesForTheme(ThemeDaybreak))
	dialog.Overlay("", 132, 30)
	if dialog.Feedback() != wantText ||
		dialog.feedbackCursorOffset() != wantCursor ||
		len(dialog.feedbackUndo) != wantUndo ||
		dialog.viewport.offset != wantOffset {
		t.Fatalf(
			"theme/resize state = text:%q cursor:%d undo:%d offset:%d",
			dialog.Feedback(),
			dialog.feedbackCursorOffset(),
			len(dialog.feedbackUndo),
			dialog.viewport.offset,
		)
	}

	if err := os.WriteFile(
		dialog.planPath,
		[]byte("# Changed plan\n\nExternal editor simulation"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	dialog.ReloadPlan()
	if dialog.Feedback() != wantText ||
		dialog.feedbackCursorOffset() != wantCursor ||
		len(dialog.feedbackUndo) != wantUndo ||
		dialog.viewport.offset != wantOffset {
		t.Fatalf(
			"reload state = text:%q cursor:%d undo:%d offset:%d",
			dialog.Feedback(),
			dialog.feedbackCursorOffset(),
			len(dialog.feedbackUndo),
			dialog.viewport.offset,
		)
	}
}

func TestP202FeedbackPresentationAcrossThemesAndAccessibilityModes(
	t *testing.T,
) {
	for _, theme := range supportedThemeNames {
		t.Run(string(theme), func(t *testing.T) {
			dialog, _ := p202PlanDialog(
				t,
				keybindings.NewResolver(),
				true,
				permission.ModeDefault,
				24,
			)
			dialog.SetStyles(StylesForTheme(theme))
			enterP202Feedback(t, dialog)
			typeP202Feedback(dialog, "宽度 e\u0301 👩‍💻")
			if dialog.feedbackEditor.Styles().Cursor.Blink {
				t.Fatal("reduced motion feedback cursor is not static")
			}
			for _, viewport := range []struct {
				width, height int
			}{
				{40, 12},
				{80, 24},
				{132, 30},
				{80, 40},
			} {
				rendered := dialog.Overlay(
					"",
					viewport.width,
					viewport.height,
				)
				lines := strings.Split(rendered, "\n")
				if len(lines) != viewport.height {
					t.Fatalf(
						"%dx%d lines=%d",
						viewport.width,
						viewport.height,
						len(lines),
					)
				}
				for index, line := range lines {
					if width := xansi.StringWidth(line); width > viewport.width {
						t.Fatalf(
							"%dx%d line %d width=%d: %q",
							viewport.width,
							viewport.height,
							index,
							width,
							xansi.Strip(line),
						)
					}
				}
				if dialog.geometry.feedback.Height == 0 {
					t.Fatalf(
						"%dx%d feedback geometry missing",
						viewport.width,
						viewport.height,
					)
				}
			}
		})
	}

	dialog, _ := p202PlanDialog(
		t,
		keybindings.NewResolver(),
		true,
		permission.ModeDefault,
		24,
	)
	enterP202Feedback(t, dialog)
	typeP202Feedback(dialog, "no color feedback")
	rendered := xansi.Strip(dialog.Overlay("", 80, 24))
	if strings.Contains(rendered, "\x1b[") ||
		!strings.Contains(rendered, "no color feedback") {
		t.Fatalf("no-color feedback projection = %q", rendered)
	}

	caps := terminalcap.Capabilities{Color: terminalcap.ColorNone}
	app := New(Config{Resumed: true, TerminalCaps: &caps})
	app.width = 80
	app.height = 24
	app.updateLayout()
	app.planDialog = dialog
	app.pushDialog(StatePlanApproval)
	frame := app.renderView()
	if strings.Contains(frame, "\x1b[") ||
		!strings.Contains(frame, "no color feedback") {
		t.Fatalf("no-color final frame = %q", frame)
	}
}

func enterP202Feedback(t *testing.T, dialog *PlanDialog) {
	t.Helper()
	dialog.focus = planFocusActions
	dialog.selectedIdx = len(dialog.options) - 1
	done, _ := dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || dialog.focus != planFocusFeedback {
		t.Fatalf("enter Feedback done=%v focus=%s", done, dialog.focus)
	}
}

func typeP202Feedback(dialog *PlanDialog, value string) {
	dialog.HandleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune(value))})
}

func p202PlanDialog(
	t *testing.T,
	resolver *keybindings.Resolver,
	reducedMotion bool,
	returnMode permission.Mode,
	stepCount int,
) (*PlanDialog, chan PermissionResponse) {
	t.Helper()
	dialog, responseCh := p201PlanDialog(t, returnMode, stepCount)
	replacement := newPlanDialog(
		dialog.styles,
		resolver,
		reducedMotion,
		false,
	)
	replacement.Show(
		"main",
		"session",
		"agent",
		nil,
		responseCh,
	)
	replacement.plan = dialog.plan
	replacement.planPath = dialog.planPath
	replacement.reviewedPlanDigest = dialog.reviewedPlanDigest
	replacement.options = dialog.options
	return replacement, responseCh
}

type p20R2CursorPosition struct {
	name              string
	value             string
	placeholder       string
	runeOffset        int
	noColorProjection string
}

var p20R2CursorPositions = []p20R2CursorPosition{
	{
		name:              "empty",
		placeholder:       "PLACEHOLDER",
		noColorProjection: planFeedbackNoColorCaret + "PLACEHOLDER",
	},
	{
		name:              "start",
		value:             "Qalpha",
		noColorProjection: planFeedbackNoColorCaret + "Qalpha",
	},
	{
		name:              "middle",
		value:             "abQcd",
		runeOffset:        2,
		noColorProjection: "ab" + planFeedbackNoColorCaret + "Qcd",
	},
	{
		name:              "end",
		value:             "tail",
		runeOffset:        4,
		noColorProjection: "tail" + planFeedbackNoColorCaret,
	},
}

type p20R2TerminalCell struct {
	content    string
	foreground color.Color
	background color.Color
	x          int
	y          int
}

func p20R2SetCursorPosition(
	dialog *PlanDialog,
	position p20R2CursorPosition,
) {
	dialog.feedbackEditor.Placeholder = position.placeholder
	dialog.feedbackEditor.SetValue(position.value)
	setTextareaRuneCursor(&dialog.feedbackEditor, position.runeOffset)
	dialog.feedbackEditor.Focus()
	p20R2SetStaticCursor(dialog)
}

func p20R2SetStaticCursor(dialog *PlanDialog) {
	styles := dialog.feedbackEditor.Styles()
	styles.Cursor.Blink = false
	dialog.feedbackEditor.SetStyles(styles)
	dialog.feedbackEditor.SetVirtualCursor(true)
}

func p20R2RenderedBackground(
	t *testing.T,
	terminalColor tuiColor,
) color.Color {
	t.Helper()
	rendered := lipgloss.NewStyle().
		Background(terminalColor).
		Render("X")
	emulator := vt.NewEmulator(2, 1)
	if _, err := emulator.WriteString(rendered); err != nil {
		t.Fatalf("decode expected terminal color: %v", err)
	}
	cell := emulator.CellAt(0, 0)
	if cell == nil || cell.Style.Bg == nil {
		t.Fatalf("expected terminal color did not produce a background: %q", rendered)
	}
	return cell.Style.Bg
}

func p20R2CellsWithBackground(
	t *testing.T,
	rendered string,
	width int,
	rect layoutRect,
	want color.Color,
) []p20R2TerminalCell {
	t.Helper()
	lines := strings.Split(rendered, "\n")
	startY := max(0, rect.Y)
	endY := min(len(lines), rect.Y+rect.Height)
	startX := max(0, rect.X)
	endX := min(width, rect.X+rect.Width)
	var matched []p20R2TerminalCell
	for y := startY; y < endY; y++ {
		emulator := vt.NewEmulator(width, 1)
		if _, err := emulator.WriteString(lines[y]); err != nil {
			t.Fatalf("decode terminal row %d: %v", y, err)
		}
		for x := startX; x < endX; x++ {
			cell := emulator.CellAt(x, 0)
			if cell == nil {
				continue
			}
			foreground := cell.Style.Fg
			background := cell.Style.Bg
			if cell.Style.Attrs&uv.AttrReverse != 0 {
				foreground, background = background, foreground
			}
			if !p20R2SameColor(background, want) {
				continue
			}
			matched = append(matched, p20R2TerminalCell{
				content:    cell.Content,
				foreground: foreground,
				background: background,
				x:          x,
				y:          y,
			})
		}
	}
	return matched
}

func p20R2CaretCountInsideFeedback(
	rendered string,
	rect layoutRect,
) int {
	lines := strings.Split(rendered, "\n")
	startY := max(0, rect.Y)
	endY := min(len(lines), rect.Y+rect.Height)
	count := 0
	for y := startY; y < endY; y++ {
		count += strings.Count(lines[y], planFeedbackNoColorCaret)
	}
	return count
}

func p20R2AssertFrameBounds(
	t *testing.T,
	rendered string,
	width, height int,
) {
	t.Helper()
	lines := strings.Split(rendered, "\n")
	if len(lines) != height {
		t.Fatalf("%dx%d lines=%d", width, height, len(lines))
	}
	for index, line := range lines {
		if cells := xansi.StringWidth(line); cells > width {
			t.Fatalf(
				"%dx%d line %d width=%d: %q",
				width,
				height,
				index,
				cells,
				xansi.Strip(line),
			)
		}
		if !utf8.ValidString(line) {
			t.Fatalf("%dx%d line %d is invalid UTF-8", width, height, index)
		}
	}
}

func p20R2AssertEditorState(
	t *testing.T,
	dialog *PlanDialog,
	wantBytes []byte,
	wantCursor, wantUndo, wantOffset int,
) {
	t.Helper()
	if !utf8.ValidString(dialog.Feedback()) ||
		dialog.Feedback() != string(wantBytes) ||
		dialog.feedbackCursorOffset() != wantCursor ||
		len(dialog.feedbackUndo) != wantUndo ||
		dialog.viewport.offset != wantOffset ||
		dialog.focus != planFocusFeedback ||
		!dialog.feedbackEditor.Focused() {
		t.Fatalf(
			"editor state changed: value=%q cursor=%d undo=%d offset=%d focus=%s focused=%v",
			dialog.Feedback(),
			dialog.feedbackCursorOffset(),
			len(dialog.feedbackUndo),
			dialog.viewport.offset,
			dialog.focus,
			dialog.feedbackEditor.Focused(),
		)
	}
}

func p20R2SameColor(left, right color.Color) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	leftR, leftG, leftB, leftA := left.RGBA()
	rightR, rightG, rightB, rightA := right.RGBA()
	return leftR == rightR &&
		leftG == rightG &&
		leftB == rightB &&
		leftA == rightA
}

func p20R2ColorString(value color.Color) string {
	if value == nil {
		return "<default>"
	}
	red, green, blue, alpha := value.RGBA()
	return fmt.Sprintf(
		"#%02x%02x%02x/%02x",
		uint8(red>>8),
		uint8(green>>8),
		uint8(blue>>8),
		uint8(alpha>>8),
	)
}
