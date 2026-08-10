package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
)

func TestG11E2AppProjectsExactEnvironmentWithoutSemanticMutation(t *testing.T) {
	profile := g11D1Profile(8)
	app := New(Config{
		Resumed:            true,
		DisplayCellProfile: &profile,
	})
	assertG11E2Environments(t, app, app.renderEnvironment)

	app.agentWizard.visible = true
	app.agentWizard.step = WizardStepReview
	app.agentWizard.fieldFocus = 1
	app.agentWizard.nameInput.SetValue("agent")
	app.backgroundTasks.visible = true
	app.backgroundTasks.subView = bgTaskViewOutput
	app.backgroundTasks.cursor = 2
	app.backgroundTasks.outputOffset = 3
	app.backgroundTasks.detailTab = agentDetailOutput
	app.teamsPanel.visible = true
	app.teamsPanel.subView = teamsViewDetail
	app.teamsPanel.cursor = 4
	app.teamsPanel.detailOffset = 5
	before := g11E2SemanticSnapshot(app)

	updateAppSilent(app, tea.WindowSizeMsg{Width: 80, Height: 24})
	resized := app.renderEnvironment
	assertG11E2Environments(t, app, resized)
	if got := g11E2SemanticSnapshot(app); got != before {
		t.Fatalf("resize changed Agent/task semantics: got %#v want %#v", got, before)
	}

	if err := app.applyTheme(string(ThemeDaybreak)); err != nil {
		t.Fatal(err)
	}
	themed := app.renderEnvironment
	assertG11E2Environments(t, app, themed)
	if got := g11E2SemanticSnapshot(app); got != before {
		t.Fatalf("theme changed Agent/task semantics: got %#v want %#v", got, before)
	}
	if themed.profile.Identity() != profile.Identity() ||
		themed.geometryGen != resized.geometryGen ||
		themed.themeGen != resized.themeGen+1 {
		t.Fatalf("theme environment = %#v after %#v", themed.identity(), resized.identity())
	}
}

type g11E2SemanticState struct {
	wizardVisible bool
	wizardStep    AgentWizardStep
	wizardFocus   int
	wizardName    string
	bgVisible     bool
	bgView        bgTaskSubView
	bgCursor      int
	bgOffset      int
	bgTab         agentDetailTab
	teamsVisible  bool
	teamsView     teamsSubView
	teamsCursor   int
	teamsOffset   int
}

func g11E2SemanticSnapshot(app *App) g11E2SemanticState {
	return g11E2SemanticState{
		wizardVisible: app.agentWizard.visible,
		wizardStep:    app.agentWizard.step,
		wizardFocus:   app.agentWizard.fieldFocus,
		wizardName:    app.agentWizard.nameInput.Value(),
		bgVisible:     app.backgroundTasks.visible,
		bgView:        app.backgroundTasks.subView,
		bgCursor:      app.backgroundTasks.cursor,
		bgOffset:      app.backgroundTasks.outputOffset,
		bgTab:         app.backgroundTasks.detailTab,
		teamsVisible:  app.teamsPanel.visible,
		teamsView:     app.teamsPanel.subView,
		teamsCursor:   app.teamsPanel.cursor,
		teamsOffset:   app.teamsPanel.detailOffset,
	}
}

func assertG11E2Environments(t *testing.T, app *App, want RenderEnvironment) {
	t.Helper()
	for name, got := range map[string]RenderEnvironment{
		"wizard":         app.agentWizard.environment,
		"background":     app.backgroundTasks.environment,
		"teams":          app.teamsPanel.environment,
		"task-panel-app": app.renderEnvironment,
	} {
		assertG11D1Environment(t, name, got, want)
	}
}

func TestG11E2AgentAndTaskProfileMatrixBoundsEveryFinalRow(t *testing.T) {
	profile := g11D1Profile(8)
	fixtureParts := make([]string, 0, len(g11E1ModalFixtures))
	for _, fixture := range g11E1ModalFixtures {
		fixtureParts = append(fixtureParts, fixture.text)
	}
	fixture := strings.Join(fixtureParts, " ")

	for _, width := range []int{40, 80, 120, 180} {
		t.Run(strings.Repeat("w", width/40), func(t *testing.T) {
			const height = 30
			views, geometries := g11E2RenderedSurfaceMatrix(
				t,
				profile,
				fixture,
				width,
				height,
			)
			for name, view := range views {
				assertG11E1Frame(t, name, profile, view, width, height)
				rect := geometries[name]
				if rect.X < 0 || rect.Y < 0 ||
					rect.X+rect.Width > width ||
					rect.Y+rect.Height > height {
					t.Errorf("%s rectangle = %#v outside %dx%d", name, rect, width, height)
				}
			}
		})
	}
}

func g11E2RenderedSurfaceMatrix(
	t *testing.T,
	profile DisplayCellProfile,
	fixture string,
	width, height int,
) (map[string]string, map[string]layoutRect) {
	t.Helper()
	styles := StylesForTheme(ThemePolarNight)
	env := newRenderEnvironment(styles, profile)
	base := strings.Repeat("base "+fixture+"\n", height-1) + "base " + fixture

	wizard := NewAgentWizard(styles)
	wizard.SetRenderEnvironment(env)
	wizard.ShowEdit(fixture, fixture, fixture, strings.Repeat(fixture, 2), fixture)
	wizard.step = WizardStepReview
	wizardView := wizard.Overlay(base, width, height)

	now := time.Unix(1, 0)
	detail := agentDetailFixture(now)
	detail.Agent.Name = fixture
	detail.Agent.Description = fixture
	detail.Agent.Task = fixture
	detail.Agent.Progress.Summary = fixture
	detail.Messages[0].Content = fixture
	snapshot := agentListFixture(detail)

	background := NewBackgroundTasksPanel(styles)
	background.SetRenderEnvironment(env)
	background.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	background.SetDetailProvider(func(string) (engine.AgentDetailSnapshot, bool) {
		return detail, true
	})
	background.Show()
	backgroundView := background.Overlay(base, width, height)

	teams := NewTeamsPanel(styles)
	teams.SetRenderEnvironment(env)
	teams.SetExplorerSnapshotProvider(func() engine.TaskExplorerSnapshot { return snapshot })
	teams.Show()
	teamsView := teams.Overlay(base, width, height)

	queryEngine := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: t.TempDir()})
	taskApp := New(Config{
		Engine:             queryEngine,
		Model:              "test-model",
		Resumed:            true,
		DisplayCellProfile: &profile,
	})
	updateAppSilent(taskApp, tea.WindowSizeMsg{Width: width, Height: height})
	taskApp.taskExplorer.SetSnapshotProvider(func() engine.TaskExplorerSnapshot {
		return p313ExplorerSnapshot(engine.TaskExplorerWorkItem{
			BoardID: "board", WorkItemID: "1", Title: fixture,
			Status: "in_progress",
		})
	})
	taskApp.enterTaskPanel()
	taskView := taskApp.renderTaskPanel()

	return map[string]string{
			"wizard":     wizardView,
			"background": backgroundView,
			"teams":      teamsView,
			"task-panel": taskView,
		}, map[string]layoutRect{
			"wizard":     wizard.geometry.outer,
			"background": background.geometry.outer,
			"teams":      teams.geometry.outer,
			"task-panel": taskApp.layout.overlayRect,
		}
}

func TestG11E2TransientGeometryResetsWhenModalIsHidden(t *testing.T) {
	base := "base"
	cases := []struct {
		name   string
		render func() string
		rect   func() layoutRect
	}{
		{
			name: "wizard",
			render: func() string {
				wizard := NewAgentWizard(defaultStyles())
				wizard.geometry.outer = layoutRect{X: 1, Y: 1, Width: 2, Height: 2}
				view := wizard.Overlay(base, 40, 12)
				if wizard.geometry.outer != (layoutRect{}) {
					t.Fatalf("wizard retained hidden geometry: %#v", wizard.geometry.outer)
				}
				return view
			},
			rect: func() layoutRect { return layoutRect{} },
		},
		{
			name: "background",
			render: func() string {
				panel := NewBackgroundTasksPanel(defaultStyles())
				panel.geometry.outer = layoutRect{X: 1, Y: 1, Width: 2, Height: 2}
				view := panel.Overlay(base, 40, 12)
				if panel.geometry.outer != (layoutRect{}) {
					t.Fatalf("background retained hidden geometry: %#v", panel.geometry.outer)
				}
				return view
			},
			rect: func() layoutRect { return layoutRect{} },
		},
		{
			name: "teams",
			render: func() string {
				panel := NewTeamsPanel(defaultStyles())
				panel.geometry.outer = layoutRect{X: 1, Y: 1, Width: 2, Height: 2}
				view := panel.Overlay(base, 40, 12)
				if panel.geometry.outer != (layoutRect{}) {
					t.Fatalf("teams retained hidden geometry: %#v", panel.geometry.outer)
				}
				return view
			},
			rect: func() layoutRect { return layoutRect{} },
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := test.render(); got != base {
				t.Fatalf("hidden modal changed base: got %q want %q", got, base)
			}
			if got := test.rect(); got != (layoutRect{}) {
				t.Fatalf("hidden geometry = %#v", got)
			}
		})
	}
}

func TestG11E2AgentAndTaskSurfacesRemainKeyboardOnly(t *testing.T) {
	cases := []struct {
		name   string
		state  AppState
		mutate func(*App)
		got    func(*App) int
	}{
		{
			name: "wizard", state: StateAgentWizard,
			mutate: func(app *App) { app.agentWizard.step = WizardStepReview },
			got:    func(app *App) int { return int(app.agentWizard.step) },
		},
		{
			name: "background", state: StateBackgroundTasks,
			mutate: func(app *App) { app.backgroundTasks.cursor = 2 },
			got:    func(app *App) int { return app.backgroundTasks.cursor },
		},
		{
			name: "teams", state: StateTeams,
			mutate: func(app *App) { app.teamsPanel.cursor = 2 },
			got:    func(app *App) int { return app.teamsPanel.cursor },
		},
		{
			name: "task-panel", state: StateTaskPanel,
			mutate: func(app *App) { app.taskPanelOffset = 2 },
			got:    func(app *App) int { return app.taskPanelOffset },
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			app := New(Config{Resumed: true})
			test.mutate(app)
			before := test.got(app)
			app.state = test.state
			updateAppSilent(app, tuiMouseMsg{
				X:      1,
				Y:      1,
				Button: tea.MouseLeft,
				Action: mouseActionPress,
			})
			if got := test.got(app); got != before {
				t.Fatalf("mouse changed keyboard-only state: got %d want %d", got, before)
			}
			if app.state != test.state {
				t.Fatalf("mouse leaked through surface: state=%v want %v", app.state, test.state)
			}
		})
	}
}

func TestG11E2MigratedPathsUseSelectedProfileAndOneGeometryOwner(t *testing.T) {
	targets := map[string]map[string]bool{
		"agent_wizard.go": {
			"AgentWizard.Overlay":          false,
			"AgentWizard.renderStepReview": false,
		},
		"background_tasks.go": {
			"BackgroundTasksPanel.Overlay":                 false,
			"BackgroundTasksPanel.rebuildAgentDetailLines": false,
			"BackgroundTasksPanel.renderOutputView":        false,
			"BackgroundTasksPanel.renderListItems":         false,
			"BackgroundTasksPanel.truncateText":            false,
		},
		"teams.go": {
			"TeamsPanel.Overlay":                 false,
			"TeamsPanel.rebuildAgentDetailLines": false,
			"TeamsPanel.renderListView":          false,
			"TeamsPanel.renderDetailView":        false,
			"TeamsPanel.renderListItems":         false,
			"TeamsPanel.truncateText":            false,
		},
		"agent_detail.go": {
			"agentDetailControl.viewWithProfile":  false,
			"renderAgentDetailTabsWithProfile":    false,
			"buildAgentDetailLinesWithProfile":    false,
			"appendAgentDetailWrappedWithProfile": false,
			"agentDetailControlHelpWithProfile":   false,
		},
		"agent_transcript_page.go": {
			"buildAgentTranscriptPageLinesWithProfile": false,
			"agentTranscriptMessageLinesWithProfile":   false,
		},
		"app.go": {
			"App.projectModalRenderEnvironment": false,
			"App.renderTaskPanel":               false,
			"App.truncateTaskPanelText":         false,
		},
	}
	rejectedFunctions := map[string]bool{
		"overlayCentered":       true,
		"truncateBgTaskText":    true,
		"truncateTeamsText":     true,
		"truncateTaskPanelText": true,
		"truncateRunes":         true,
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
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if identifier, ok := call.Fun.(*ast.Ident); ok {
					if rejectedFunctions[identifier.Name] {
						t.Errorf("%s %s calls legacy geometry owner %s", fileName, key, identifier.Name)
					}
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				qualifier, ok := selector.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch qualifier.Name + "." + selector.Sel.Name {
				case "lipgloss.Width", "lipgloss.Place", "lipgloss.PlaceHorizontal",
					"xansi.StringWidth", "xansi.Truncate", "xansi.Wrap",
					"ansi.StringWidth", "ansi.Truncate",
					"utf8.RuneCountInString":
					t.Errorf(
						"%s %s selects second geometry owner %s.%s",
						fileName,
						key,
						qualifier.Name,
						selector.Sel.Name,
					)
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
