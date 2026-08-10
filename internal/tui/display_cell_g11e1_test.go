package tui

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/tools"
)

var g11E1ModalFixtures = []struct {
	name string
	text string
}{
	{name: "ascii", text: "ascii"},
	{name: "cjk", text: "界面"},
	{name: "combining", text: "e\u0301"},
	{name: "indic", text: "क्ष"},
	{name: "vs15", text: "✈︎"},
	{name: "vs16", text: "✈️"},
	{name: "zwj", text: "👩‍💻"},
	{name: "paired-flag", text: "🇨🇳"},
	{name: "lone-regional-indicator", text: "\U0001F1E8"},
	{name: "bare-label", text: "🏷"},
	{name: "sparkle", text: "✦"},
	{name: "tab", text: "\tcell"},
	{name: "ansi", text: "\x1b[31mred\x1b[0m"},
	{name: "osc", text: "\x1b]8;;https://example.test\x1b\\link\x1b]8;;\x1b\\"},
}

func TestG11E1AppProjectsExactEnvironmentToEveryModal(t *testing.T) {
	profile := g11D1Profile(8)
	app := New(Config{
		Resumed:            true,
		DisplayCellProfile: &profile,
	})
	assertG11E1ModalEnvironments(t, app, app.renderEnvironment)

	app.dialog.visible, app.dialog.selectedIdx = true, 1
	app.resume.visible, app.resume.selected = true, 2
	app.mcpApproval.visible, app.mcpApproval.selectedIdx = true, 1
	app.mcpSettings.visible, app.mcpSettings.cursor = true, 2
	app.planDialog.visible = true
	app.planDialog.focus, app.planDialog.selectedIdx = planFocusActions, 1
	app.questionDialog.visible, app.questionDialog.currentIdx = true, 1
	before := g11E1ModalSemanticSnapshot(app)

	updateAppSilent(app, tea.WindowSizeMsg{Width: 80, Height: 24})
	resized := app.renderEnvironment
	assertG11E1ModalEnvironments(t, app, resized)
	if got := g11E1ModalSemanticSnapshot(app); got != before {
		t.Fatalf("resize changed modal semantics: got %#v want %#v", got, before)
	}

	if err := app.applyTheme(string(ThemeDaybreak)); err != nil {
		t.Fatal(err)
	}
	themed := app.renderEnvironment
	assertG11E1ModalEnvironments(t, app, themed)
	if got := g11E1ModalSemanticSnapshot(app); got != before {
		t.Fatalf("theme changed modal semantics: got %#v want %#v", got, before)
	}
	if themed.profile.Identity() != profile.Identity() ||
		themed.geometryGen != resized.geometryGen ||
		themed.themeGen != resized.themeGen+1 {
		t.Fatalf("theme environment = %#v after %#v", themed.identity(), resized.identity())
	}
}

type g11E1ModalSemanticState struct {
	permissionVisible  bool
	permissionSelected int
	resumeVisible      bool
	resumeSelected     int
	approvalVisible    bool
	approvalSelected   int
	settingsVisible    bool
	settingsSelected   int
	planVisible        bool
	planFocus          planDialogFocus
	planSelected       int
	questionVisible    bool
	questionSelected   int
}

func g11E1ModalSemanticSnapshot(app *App) g11E1ModalSemanticState {
	return g11E1ModalSemanticState{
		permissionVisible:  app.dialog.visible,
		permissionSelected: app.dialog.selectedIdx,
		resumeVisible:      app.resume.visible,
		resumeSelected:     app.resume.selected,
		approvalVisible:    app.mcpApproval.visible,
		approvalSelected:   app.mcpApproval.selectedIdx,
		settingsVisible:    app.mcpSettings.visible,
		settingsSelected:   app.mcpSettings.cursor,
		planVisible:        app.planDialog.visible,
		planFocus:          app.planDialog.focus,
		planSelected:       app.planDialog.selectedIdx,
		questionVisible:    app.questionDialog.visible,
		questionSelected:   app.questionDialog.currentIdx,
	}
}

func assertG11E1ModalEnvironments(
	t *testing.T,
	app *App,
	want RenderEnvironment,
) {
	t.Helper()
	for name, got := range map[string]RenderEnvironment{
		"permission":   app.dialog.environment,
		"resume":       app.resume.environment,
		"mcp approval": app.mcpApproval.environment,
		"mcp settings": app.mcpSettings.environment,
		"plan":         app.planDialog.environment,
		"question":     app.questionDialog.environment,
	} {
		assertG11D1Environment(t, name, got, want)
	}
}

func TestG11E1ModalProfileMatrixBoundsEveryFinalRow(t *testing.T) {
	profile := g11D1Profile(8)
	fixtureParts := make([]string, 0, len(g11E1ModalFixtures))
	for _, fixture := range g11E1ModalFixtures {
		fixtureParts = append(fixtureParts, fixture.text)
	}
	fixture := strings.Join(fixtureParts, " ")

	for _, width := range []int{40, 80, 120, 180} {
		t.Run(strings.Repeat("w", width/40), func(t *testing.T) {
			const height = 30
			views, geometries := g11E1RenderedModalMatrix(t, profile, fixture, width, height)
			for name, view := range views {
				assertG11E1Frame(t, name, profile, view, width, height)
				rect := geometries[name]
				if rect.X < 0 || rect.Y < 0 ||
					rect.X+rect.Width > width ||
					rect.Y+rect.Height > height {
					t.Errorf("%s outer rectangle = %#v outside %dx%d", name, rect, width, height)
				}
			}
		})
	}
}

func g11E1RenderedModalMatrix(
	t *testing.T,
	profile DisplayCellProfile,
	fixture string,
	width, height int,
) (map[string]string, map[string]layoutRect) {
	t.Helper()
	styles := StylesForTheme(ThemePolarNight)
	env := newRenderEnvironment(styles, profile)
	base := strings.Repeat("base "+fixture+"\n", height-1) + "base " + fixture

	plan := NewPlanDialog(styles)
	plan.SetRenderEnvironment(env)
	plan.visible = true
	plan.plan = "# " + fixture
	plan.planPath = "/tmp/" + fixture
	plan.options = buildPlanOptions(permission.ModeDefault)
	plan.focus = planFocusFeedback
	plan.feedbackEditor.SetValue(fixture)
	plan.feedbackEditor.Focus()
	planView := plan.Overlay(base, width, height)

	permissionDialog := NewPermissionDialog(styles)
	permissionDialog.SetRenderEnvironment(env)
	toolInput, err := json.Marshal(map[string]string{"command": fixture})
	if err != nil {
		t.Fatal(err)
	}
	permissionDialog.Show("Bash", string(toolInput), "", make(chan PermissionResponse, 1))
	permissionView := permissionDialog.Overlay(base, width, height)

	approval := NewMCPApprovalDialog(styles)
	approval.SetRenderEnvironment(env)
	approval.Show(MCPApprovalRequest{
		ServerName: fixture,
		Source:     fixture,
		Tools:      []string{fixture},
	}, make(chan MCPApprovalResponse, 1))
	approvalView := approval.Overlay(base, width, height)

	settings := NewMCPSettingsPanel(styles)
	settings.SetRenderEnvironment(env)
	settings.visible = true
	settings.subView = mcpViewToolList
	settings.selectedServer = fixture
	settings.toolItems = []mcpToolEntry{{name: fixture, description: fixture}}
	settingsView := settings.Overlay(base, width, height)

	resume := NewResumeDialog(styles)
	resume.SetRenderEnvironment(env)
	resume.visible = true
	resume.mode = sessionPickerResume
	resume.scope = session.SessionScopeRepository
	resumeInfo := session.SessionInfo{
		SessionID:       fixture,
		Summary:         fixture,
		FirstPrompt:     fixture,
		GitBranch:       fixture,
		CWD:             "/tmp/" + fixture,
		ParentSessionID: fixture,
		BranchName:      fixture,
		AgentID:         fixture,
		AgentName:       fixture,
		LastModified:    time.Unix(1, 0),
	}
	resume.filtered = []session.SessionInfo{resumeInfo}
	resume.previews = map[string]sessionPreviewState{
		resumeInfo.StableKey(): {
			messages: []*schema.Message{{
				Role:    schema.Assistant,
				Content: strings.Repeat(fixture, 3),
			}},
		},
	}
	resumeView := resume.Overlay(base, width, height)

	questionInput, err := json.Marshal(map[string]any{
		"questions": []tools.UserQuestion{{
			Header:   fixture,
			Question: fixture,
			Options: []tools.QuestionOption{{
				Label: fixture, Description: fixture,
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	question := NewQuestionDialog(styles)
	question.SetRenderEnvironment(env)
	question.Show(string(questionInput), make(chan PermissionResponse, 1))
	questionView := question.Overlay(base, width, height)

	return map[string]string{
			"plan":         planView,
			"permission":   permissionView,
			"mcp-approval": approvalView,
			"mcp-settings": settingsView,
			"resume":       resumeView,
			"question":     questionView,
		}, map[string]layoutRect{
			"plan":         plan.geometry.outer,
			"permission":   permissionDialog.geometry.outer,
			"mcp-approval": approval.geometry.outer,
			"mcp-settings": settings.geometry.outer,
			"resume":       resume.geometry.outer,
			"question":     question.geometry.outer,
		}
}

func assertG11E1Frame(
	t *testing.T,
	name string,
	profile DisplayCellProfile,
	view string,
	width, height int,
) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != height {
		t.Errorf("%s row count = %d, want %d", name, len(lines), height)
	}
	for row, line := range lines {
		if got := profile.width(line); got > width {
			t.Errorf("%s row %d width = %d, want <= %d: %q", name, row, got, width, line)
		}
		assertWidthProfileControlStateClosed(t, profile, line)
	}
}

func TestG11E1ProfileProjectsEveryFixtureWithoutSplittingControls(t *testing.T) {
	profile := g11D1Profile(8)
	for _, fixture := range g11E1ModalFixtures {
		t.Run(fixture.name, func(t *testing.T) {
			for _, width := range []int{1, 2, 5, 12} {
				line := modalEllipsize(profile, fixture.text, width, 3, "…")
				if got := profile.measure(line, 3); got > width {
					t.Fatalf("width=%d measured=%d line=%q", width, got, line)
				}
				assertWidthProfileControlStateClosed(t, profile, line)
			}
		})
	}
}

func TestG11E1CenteredGeometryMeasuresFinalOuterBoxAtItsOrigin(t *testing.T) {
	profile := g11D1Profile(8)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(1, 2).
		Width(20).
		Render("\t界 e\u0301")
	const width, height = 40, 12
	view, geometry := modalCenteredOverlay(profile, "", box, width, height)
	lines := strings.Split(box, "\n")
	wantWidth := 0
	for _, line := range lines {
		wantWidth = max(wantWidth, profile.measure(line, geometry.outer.X))
	}
	if geometry.outer.Width != wantWidth ||
		geometry.outer.Height != len(lines) {
		t.Fatalf("outer geometry = %#v, want width=%d height=%d", geometry.outer, wantWidth, len(lines))
	}
	chosenImbalance := modalCenterImbalance(width, geometry.outer.X, geometry.outer.Width)
	for candidate := 0; candidate <= width; candidate++ {
		candidateWidth := 0
		for _, line := range lines {
			candidateWidth = max(candidateWidth, profile.measure(line, candidate))
		}
		if candidate+candidateWidth > width {
			continue
		}
		if imbalance := modalCenterImbalance(width, candidate, candidateWidth); imbalance < chosenImbalance {
			t.Fatalf(
				"chosen origin %d imbalance %d; candidate %d has %d",
				geometry.outer.X,
				chosenImbalance,
				candidate,
				imbalance,
			)
		}
	}
	assertG11E1Frame(t, "centered", profile, view, width, height)
}

func modalCenterImbalance(width, start, contentWidth int) int {
	imbalance := start - (width - start - contentWidth)
	if imbalance < 0 {
		return -imbalance
	}
	return imbalance
}

func TestG11E1VerticalOverflowKeepsHeadRows(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	lines := []string{"first", "second", "third", "fourth"}

	bottom, bottomGeometry := modalBottomOverlay(profile, "base", lines[:2], 20, 5)
	if bottomGeometry.outer.Y != 3 || !strings.Contains(strings.Split(bottom, "\n")[3], "first") {
		t.Fatalf("fitting bottom overlay = %#v %q", bottomGeometry.outer, bottom)
	}
	bottom, bottomGeometry = modalBottomOverlay(profile, "base", lines, 20, 3)
	bottomLines := strings.Split(bottom, "\n")
	if bottomGeometry.outer.Y != 0 ||
		len(bottomLines) != 3 ||
		bottomLines[0] != "first" ||
		bottomLines[2] != "third" {
		t.Fatalf("overflow bottom overlay = %#v %q", bottomGeometry.outer, bottom)
	}

	centered, centeredGeometry := modalCenteredOverlay(
		profile,
		"base",
		strings.Join(lines, "\n"),
		20,
		3,
	)
	centeredLines := strings.Split(centered, "\n")
	if centeredGeometry.outer.Y != 0 ||
		!strings.Contains(centeredLines[0], "first") ||
		!strings.Contains(centeredLines[2], "third") {
		t.Fatalf("overflow centered overlay = %#v %q", centeredGeometry.outer, centered)
	}

	top, topGeometry := modalTopFrame(profile, lines, 20, 3)
	topLines := strings.Split(top, "\n")
	if topGeometry.outer.Y != 0 ||
		topLines[0] != "first" ||
		topLines[2] != "third" {
		t.Fatalf("overflow top frame = %#v %q", topGeometry.outer, top)
	}
}

func TestG11E1PlanFeedbackRowsPublishRenderedProfileGeometry(t *testing.T) {
	profile := g11D1Profile(8)
	dialog := NewPlanDialog(defaultStyles())
	dialog.SetRenderEnvironment(newRenderEnvironment(defaultStyles(), profile))
	dialog.visible = true
	dialog.plan = "Review \t界 e\u0301"
	dialog.options = buildPlanOptions(permission.ModeDefault)
	dialog.focus = planFocusFeedback
	dialog.feedbackEditor.SetValue("\t界 e\u0301 👩‍💻")
	dialog.feedbackEditor.Focus()

	const width, height = 40, 24
	view := dialog.Overlay("", width, height)
	rect := dialog.geometry.feedback
	if rect.X != 3 || rect.Height <= 0 {
		t.Fatalf("feedback rectangle = %#v", rect)
	}
	lines := strings.Split(view, "\n")
	measured := 0
	for row := rect.Y; row < rect.Y+rect.Height; row++ {
		measured = max(measured, profile.width(lines[row]))
	}
	wantWidth := min(max(1, width-3), max(1, measured-3))
	if rect.Width != wantWidth {
		t.Fatalf("feedback width = %d, want %d from rendered rows", rect.Width, wantWidth)
	}

	dialog.focus = planFocusReview
	dialog.HandleMouse(tuiMouseMsg{
		X:      rect.X,
		Y:      rect.Y,
		Button: tea.MouseLeft,
		Action: mouseActionPress,
	})
	if dialog.focus != planFocusFeedback || !dialog.feedbackEditor.Focused() {
		t.Fatalf("feedback hit did not consume published geometry: focus=%v", dialog.focus)
	}
}

func TestG11E1NonPlanModalMouseRemainsKeyboardOnly(t *testing.T) {
	cases := []struct {
		name   string
		state  AppState
		mutate func(*App)
		got    func(*App) int
	}{
		{
			name: "permission", state: StatePermission,
			mutate: func(app *App) { app.dialog.selectedIdx = 1 },
			got:    func(app *App) int { return app.dialog.selectedIdx },
		},
		{
			name: "resume", state: StateResume,
			mutate: func(app *App) { app.resume.selected = 2 },
			got:    func(app *App) int { return app.resume.selected },
		},
		{
			name: "mcp-approval", state: StateMCPApproval,
			mutate: func(app *App) { app.mcpApproval.selectedIdx = 1 },
			got:    func(app *App) int { return app.mcpApproval.selectedIdx },
		},
		{
			name: "mcp-settings", state: StateMCPSettings,
			mutate: func(app *App) { app.mcpSettings.cursor = 2 },
			got:    func(app *App) int { return app.mcpSettings.cursor },
		},
		{
			name: "question", state: StateAskUser,
			mutate: func(app *App) { app.questionDialog.optionIdx = 2 },
			got:    func(app *App) int { return app.questionDialog.optionIdx },
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			app := New(Config{Resumed: true})
			test.mutate(app)
			before := test.got(app)
			app.pushDialog(test.state)
			updateAppSilent(app, tuiMouseMsg{
				X:      1,
				Y:      1,
				Button: tea.MouseLeft,
				Action: mouseActionPress,
			})
			if got := test.got(app); got != before {
				t.Fatalf("mouse changed keyboard-only selection: got %d want %d", got, before)
			}
			if app.state != test.state {
				t.Fatalf("mouse leaked through modal: state=%v want %v", app.state, test.state)
			}
		})
	}
}

func TestG11E1MigratedModalPathsHaveOneGeometryOwner(t *testing.T) {
	targets := map[string]map[string]bool{
		"plan_dialog.go": {
			"PlanDialog.Overlay":                  false,
			"PlanDialog.renderBypassConfirmation": false,
		},
		"dialog.go": {
			"PermissionDialog.Overlay":           false,
			"PermissionDialog.renderToolContent": false,
		},
		"mcp_approval.go": {
			"MCPApprovalDialog.Overlay": false,
		},
		"mcp_settings.go": {
			"MCPSettingsPanel.Overlay":            false,
			"MCPSettingsPanel.renderServerItems":  false,
			"MCPSettingsPanel.renderToolItems":    false,
			"MCPSettingsPanel.serverStatusDetail": false,
		},
		"resume_dialog.go": {
			"ResumeDialog.Overlay":                        false,
			"ResumeDialog.renderSessionDetail":            false,
			"ResumeDialog.renderModalSessionPreviewLines": false,
		},
		"question_dialog.go": {
			"QuestionDialog.Overlay":            false,
			"QuestionDialog.renderQuestionView": false,
			"QuestionDialog.renderSubmitView":   false,
		},
	}
	rejectedFunctions := map[string]bool{
		"truncateDisplay":           true,
		"truncatePathDisplay":       true,
		"terminalLayoutSafetyWidth": true,
		"truncateRunes":             true,
		"overlayCentered":           true,
		"wrapText":                  true,
	}
	visibleByteNames := map[string]bool{
		"line": true, "source": true, "desc": true, "summary": true,
		"preview": true, "msg": true, "cl": true, "ol": true, "nl": true,
		"idStr": true, "cwd": true, "content": true, "label": true,
	}

	for fileName, functions := range targets {
		fileSet := token.NewFileSet()
		file, err := parser.ParseFile(fileSet, fileName, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", fileName, err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok {
				continue
			}
			key := g11E1FunctionKey(function)
			if _, ok := functions[key]; !ok {
				continue
			}
			functions[key] = true
			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch typed := node.(type) {
				case *ast.CallExpr:
					if identifier, ok := typed.Fun.(*ast.Ident); ok {
						if rejectedFunctions[identifier.Name] {
							t.Errorf("%s %s calls legacy geometry owner %s", fileName, key, identifier.Name)
						}
						return true
					}
					selector, ok := typed.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					qualifier, ok := selector.X.(*ast.Ident)
					if !ok {
						return true
					}
					switch qualifier.Name + "." + selector.Sel.Name {
					case "lipgloss.Width", "lipgloss.Place", "lipgloss.PlaceHorizontal",
						"xansi.StringWidth", "xansi.Truncate",
						"ansi.StringWidth", "ansi.Truncate":
						t.Errorf(
							"%s %s selects second geometry owner %s.%s",
							fileName,
							key,
							qualifier.Name,
							selector.Sel.Name,
						)
					}
				case *ast.SliceExpr:
					if identifier, ok := typed.X.(*ast.Ident); ok && visibleByteNames[identifier.Name] {
						t.Errorf("%s %s byte-slices visible text %s", fileName, key, identifier.Name)
					}
				}
				return true
			})
		}
		for function, found := range functions {
			if !found {
				t.Errorf("%s did not contain migrated function %s", fileName, function)
			}
		}
	}
}

func g11E1FunctionKey(function *ast.FuncDecl) string {
	key := function.Name.Name
	if function.Recv == nil || len(function.Recv.List) != 1 {
		return key
	}
	receiver := function.Recv.List[0].Type
	if pointer, ok := receiver.(*ast.StarExpr); ok {
		receiver = pointer.X
	}
	if identifier, ok := receiver.(*ast.Ident); ok {
		return identifier.Name + "." + key
	}
	return key
}
