package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/provider"
)

type g11E4SelectionItem struct {
	text string
}

func (item *g11E4SelectionItem) Render(int, Styles) string { return item.text }
func (item *g11E4SelectionItem) Finished() bool            { return true }
func (item *g11E4SelectionItem) Version() uint64           { return 1 }
func (item *g11E4SelectionItem) NoSelectPrefix() int       { return 0 }
func (item *g11E4SelectionItem) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	annotated, ok := selectionAnnotateVisibleRows(
		ctx.normalized().displayCellProfile(),
		item.text,
		0,
	)
	return selectionAnnotatedRender{rendered: annotated, annotated: ok}
}

func TestG11E4AppProjectsExactEnvironmentToPickersSearchAndSelection(t *testing.T) {
	profile := g11D1Profile(8)
	app := New(Config{
		Resumed:            true,
		DisplayCellProfile: &profile,
	})
	assertG11E4Environments(t, app, app.renderEnvironment)

	updateAppSilent(app, tea.WindowSizeMsg{Width: 80, Height: 24})
	resized := app.renderEnvironment
	assertG11E4Environments(t, app, resized)

	if err := app.applyTheme(string(ThemeDaybreak)); err != nil {
		t.Fatal(err)
	}
	themed := app.renderEnvironment
	assertG11E4Environments(t, app, themed)
	if themed.profile.Identity() != profile.Identity() ||
		themed.geometryGen != resized.geometryGen ||
		themed.themeGen != resized.themeGen+1 {
		t.Fatalf("theme environment = %#v after %#v", themed.identity(), resized.identity())
	}

	for threadID, view := range app.threadViews.views {
		assertG11D1Environment(t, threadID+"/chat", view.Chat.environment, themed)
		assertG11D1Environment(t, threadID+"/search", view.Search.environment, themed)
	}
	future, err := app.threadViews.activate("g11e4-future", engine.ThreadModeReplayOnly)
	if err != nil {
		t.Fatal(err)
	}
	assertG11D1Environment(t, "future/chat", future.Chat.environment, themed)
	assertG11D1Environment(t, "future/search", future.Search.environment, themed)
}

func assertG11E4Environments(t *testing.T, app *App, want RenderEnvironment) {
	t.Helper()
	for name, got := range map[string]RenderEnvironment{
		"help":            app.help.environment,
		"search":          app.search.environment,
		"message select":  app.msgSelector.environment,
		"model picker":    app.modelPicker.environment,
		"command palette": app.commandPalette.environment,
		"Agent picker":    app.agentPicker.environment,
		"expand search":   app.expandSearch.environment,
	} {
		assertG11D1Environment(t, name, got, want)
	}
}

func TestG11E4PickerHintSearchAndInteractionProfileMatrix(t *testing.T) {
	profile := g11D1Profile(8)
	fixture := g11E3FixtureText()

	for _, width := range []int{40, 80, 120, 180} {
		t.Run(strings.Repeat("w", width/40), func(t *testing.T) {
			app := New(Config{
				Resumed:            true,
				DisplayCellProfile: &profile,
			})
			updateAppSilent(app, tea.WindowSizeMsg{Width: width, Height: 30})
			app.inputMode = InputCommand
			app.focus = FocusEditor
			app.commandHintIdx = 0
			app.commandHints = []*commands.Command{{
				Name:        fixture,
				Aliases:     []string{fixture},
				Description: fixture,
			}}
			app.fileHintIdx = 0
			app.fileHints = []string{fixture}
			app.mentionHintIdx = 0
			app.mentionHints = []composerMentionHint{{
				Kind:        composerElementKindMCPResource,
				Label:       fixture,
				Description: fixture,
			}}
			app.queuedInputPreview = []threadQueuedInput{{
				ID:      "queued-1",
				Content: fixture,
			}}
			app.historySearch = composerHistorySearch{
				Active: true,
				Query:  fixture,
			}

			app.search.Show()
			app.search.input.SetValue(fixture)
			app.search.query = fixture
			app.search.matches = []SearchMatch{{ItemIndex: 0, Text: fixture}}
			app.search.matchIdx = 0
			app.expandSearch.Show()
			app.expandSearch.input.SetValue(fixture)
			app.expandSearch.query = fixture
			app.expandSearch.matches = []ExpandSearchMatch{{LineIndex: 0}}
			app.expandSearch.matchIdx = 0

			app.commandPalette.visible = true
			app.commandPalette.filtered = []commandPaletteItem{{command: &commands.Command{
				Name:        fixture,
				Description: fixture,
			}}}
			app.modelPicker.visible = true
			app.modelPicker.items = []modelPickerItem{
				{isHeader: true, provider: fixture},
				{entry: provider.RuntimeInventoryEntry{
					Provider:    fixture,
					Selector:    fixture,
					DisplayName: fixture,
				}},
			}
			app.modelPicker.cursor = 1
			app.agentPicker.Show([]engine.RuntimeThreadCatalogEntry{{
				ThreadID:    "child-" + fixture,
				AgentID:     fixture,
				Name:        fixture,
				Description: fixture,
				Status:      engine.RuntimeThreadRunning,
				Mode:        engine.ThreadModeLiveAttach,
			}}, "child-"+fixture, "leader")

			app.help.visible = true
			app.help.lines = []string{fixture, fixture + fixture}
			app.help.totalLines = len(app.help.lines)
			app.msgSelector.active = true
			app.msgSelector.userIndices = []int{0, 1}
			app.msgSelector.selectedPos = 1
			app.bypassConfirmIdx = 1

			base := strings.Repeat("\n", 29)
			surfaces := map[string]string{
				"command hints":    app.renderCommandHints(),
				"file hints":       app.renderFileHints(),
				"mention hints":    app.renderMentionHints(),
				"queued input":     app.renderQueuedInputRows(),
				"history search":   app.renderHistorySearch(),
				"search":           app.search.Render(width),
				"expand search":    app.expandSearch.Render(width),
				"command palette":  app.commandPalette.Overlay(base, width, 30),
				"model picker":     app.modelPicker.Overlay(base, width, 30),
				"Agent picker":     app.agentPicker.Overlay(base, width, 30),
				"help":             app.help.Overlay(base, width, 30),
				"bypass":           app.bypassConfirmOverlay(base),
				"message hint":     app.msgSelector.RenderHintBar(width),
				"message selected": app.msgSelector.RenderSelectedHighlight(fixture, width),
			}
			for name, surface := range surfaces {
				assertG11E3Rows(t, name, profile, surface, width)
			}
		})
	}
}

func TestG11E4PickerAndResidualDialogsRemainKeyboardOnly(t *testing.T) {
	cases := []struct {
		name   string
		state  AppState
		mutate func(*App)
		got    func(*App) int
	}{
		{
			name: "command palette", state: StateCommandPalette,
			mutate: func(app *App) {
				app.commandPalette.visible = true
				app.commandPalette.cursor = 2
			},
			got: func(app *App) int { return app.commandPalette.cursor },
		},
		{
			name: "model picker", state: StateModelPicker,
			mutate: func(app *App) {
				app.modelPicker.visible = true
				app.modelPicker.cursor = 2
			},
			got: func(app *App) int { return app.modelPicker.cursor },
		},
		{
			name: "Agent picker", state: StateAgentPicker,
			mutate: func(app *App) {
				app.agentPicker.visible = true
				app.agentPicker.cursor = 2
			},
			got: func(app *App) int { return app.agentPicker.cursor },
		},
		{
			name: "help", state: StateHelp,
			mutate: func(app *App) {
				app.help.visible = true
				app.help.offset = 2
			},
			got: func(app *App) int { return app.help.offset },
		},
		{
			name: "bypass", state: StateBypassConfirm,
			mutate: func(app *App) { app.bypassConfirmIdx = 1 },
			got:    func(app *App) int { return app.bypassConfirmIdx },
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			app := New(Config{Resumed: true})
			updateAppSilent(app, tea.WindowSizeMsg{Width: 80, Height: 24})
			test.mutate(app)
			before := test.got(app)
			app.pushDialog(test.state)
			updateAppSilent(app, tuiMouseMsg{
				X:      10,
				Y:      10,
				Button: tea.MouseLeft,
				Action: mouseActionPress,
			})
			if got := test.got(app); got != before {
				t.Fatalf("mouse changed keyboard-only state: got %d want %d", got, before)
			}
			if app.state != test.state {
				t.Fatalf("mouse leaked through dialog: state=%v want %v", app.state, test.state)
			}
		})
	}
}

func TestG11E4ExpandMouseMapsSearchChromeToContentRows(t *testing.T) {
	profile := g11D1Profile(8)
	app := New(Config{
		Resumed:            true,
		DisplayCellProfile: &profile,
	})
	updateAppSilent(app, tea.WindowSizeMsg{Width: 40, Height: 5})
	app.state = StateExpand
	app.expandLines = []string{"first", "second", "third", "fourth", "fifth"}
	app.expandSearch.Show()

	// Search and status chrome do not start a content selection.
	updateAppSilent(app, tuiMouseMsg{
		X: 0, Y: 0, Button: tea.MouseLeft, Action: mouseActionPress,
	})
	if app.selection.expandAnchor != nil {
		t.Fatalf("search row started content selection: %#v", app.selection.expandAnchor)
	}
	updateAppSilent(app, tuiMouseMsg{
		X: 0, Y: 4, Button: tea.MouseLeft, Action: mouseActionPress,
	})
	if app.selection.expandAnchor != nil {
		t.Fatalf("status row started content selection: %#v", app.selection.expandAnchor)
	}

	// The first screen content row maps to expandLines[0], not [1].
	updateAppSilent(app, tuiMouseMsg{
		X: 0, Y: 1, Button: tea.MouseLeft, Action: mouseActionPress,
	})
	updateAppSilent(app, tuiMouseMsg{
		X: 3, Y: 2, Button: tea.MouseNone, Action: mouseActionMotion,
	})
	if app.selection.expandAnchor == nil || app.selection.expandAnchor.row != 0 ||
		app.selection.expandFocus == nil || app.selection.expandFocus.row != 1 {
		t.Fatalf(
			"search-visible mouse mapping = anchor:%#v focus:%#v",
			app.selection.expandAnchor,
			app.selection.expandFocus,
		)
	}
	if got := app.selection.ExtractExpandText(app.expandLines, profile); got != "first\nsec" {
		t.Fatalf("search-visible mouse extraction = %q", got)
	}

	// Offset is added only after subtracting the search row. Leaving through
	// chrome during a drag clamps release to the nearest content edge.
	app.selection = &Selection{}
	app.expandOffset = 2
	updateAppSilent(app, tuiMouseMsg{
		X: 0, Y: 1, Button: tea.MouseLeft, Action: mouseActionPress,
	})
	if app.selection.expandAnchor == nil || app.selection.expandAnchor.row != 2 {
		t.Fatalf("offset mouse anchor = %#v", app.selection.expandAnchor)
	}
	if row, ok := app.expandMouseSelectionRow(tuiMouseMsg{
		Y: 0, Action: mouseActionRelease,
	}); !ok || row != 2 {
		t.Fatalf("search-edge release row = %d ok=%v", row, ok)
	}
	if row, ok := app.expandMouseSelectionRow(tuiMouseMsg{
		Y: 4, Action: mouseActionRelease,
	}); !ok || row != 4 {
		t.Fatalf("status-edge release row = %d ok=%v", row, ok)
	}
}

func TestG11E4SelectionUsesCellBoundariesWithoutSplittingClusters(t *testing.T) {
	profile := g11D1Profile(8)
	fixtures := []string{
		"界",
		"e\u0301",
		"क्ष",
		"✈︎",
		"✈️",
		"👩‍💻",
		"🇨🇳",
		"\U0001F1E8",
	}
	for _, fixture := range fixtures {
		line := "a" + fixture + "z"
		clusters := profile.clusters(line, 0)
		if len(clusters) != 3 {
			t.Fatalf("%q clusters = %#v", fixture, clusters)
		}
		target := clusters[1]
		endCell := target.endColumn
		if endCell == target.startColumn {
			endCell++
		}
		got := selectionSliceCells(
			profile,
			line,
			target.startColumn,
			endCell,
		)
		if got != fixture {
			t.Fatalf("selection split %q: got %q", fixture, got)
		}
		if target.cells > 1 {
			got = selectionSliceCells(
				profile,
				line,
				target.startColumn+1,
				target.endColumn,
			)
			if got != fixture {
				t.Fatalf("interior cell split %q: got %q", fixture, got)
			}
		}
		highlighted := selectionHighlightCells(
			profile,
			line,
			target.startColumn,
			endCell,
		)
		if stripped := xansi.Strip(highlighted); stripped != line {
			t.Fatalf("highlight changed source %q: %q", line, stripped)
		}
	}

	controlled := "\x1b[31ma界e\u0301\t👩‍💻z\x1b[0m" +
		"\x1b]8;;https://example.test\x1b\\link\x1b]8;;\x1b\\"
	plain := selectionPlainLine(controlled)
	if strings.ContainsAny(plain, "\x1b\x07") {
		t.Fatalf("plain selection retained terminal controls: %q", plain)
	}
	whole := selectionSliceCells(profile, controlled, 0, selectionLineCells(profile, controlled))
	if whole != strings.TrimRight(plain, " \t") {
		t.Fatalf("whole selection = %q want %q", whole, plain)
	}

	selection := &Selection{
		expandAnchor: &expandSelPoint{row: 0, col: 1},
		expandFocus:  &expandSelPoint{row: 1, col: 2},
	}
	got := selection.ExtractExpandText([]string{"a界z", "e\u0301x"}, profile)
	if got != "界z\ne\u0301x" {
		t.Fatalf("expand selection = %q", got)
	}

	chat := newChatViewWithRenderEnvironment(newRenderEnvironment(defaultStyles(), profile))
	chat.SetSize(40, 3)
	item := &g11E4SelectionItem{text: "a界e\u0301\t👩‍💻z"}
	chat.appendChatItem(item)
	renderedLine := chat.GetItemLine(0, 0)
	clusters := profile.clusters(selectionPlainLine(renderedLine), 0)
	var wide measuredDisplayCellCluster
	for _, cluster := range clusters {
		if cluster.source == "界" {
			wide = cluster
			break
		}
	}
	if wide.source == "" {
		t.Fatalf("chat fixture lost wide cluster: %q", renderedLine)
	}
	got = chat.RenderItemRange(
		0,
		0,
		wide.startColumn+1,
		0,
		0,
		wide.endColumn,
	)
	if got != "界" {
		t.Fatalf("chat selection split wide cluster: %q", got)
	}

	app := New(Config{
		Resumed:            true,
		DisplayCellProfile: &profile,
	})
	updateAppSilent(app, tea.WindowSizeMsg{Width: 40, Height: 5})
	app.expandSearch.Show()
	app.selection.expandAnchor = &expandSelPoint{row: 0, col: 1}
	app.selection.expandFocus = &expandSelPoint{row: 0, col: 3}
	expanded := "search\na界z\nbbb\nccc\nstatus"
	highlighted := app.applyExpandHighlight(expanded)
	lines := strings.Split(highlighted, "\n")
	if strings.Contains(lines[0], "\x1b[7m") ||
		!strings.Contains(lines[1], "\x1b[7m") ||
		xansi.Strip(highlighted) != expanded {
		t.Fatalf("expand search row translation changed selection: %q", highlighted)
	}

	app.expandOffset = 1
	app.selection.expandAnchor = &expandSelPoint{row: 0, col: 3}
	app.selection.expandFocus = &expandSelPoint{row: 9, col: 1}
	highlighted = app.applyExpandHighlight(expanded)
	lines = strings.Split(highlighted, "\n")
	if !strings.Contains(lines[1], "\x1b[7m") ||
		!strings.Contains(lines[3], "\x1b[7m") {
		t.Fatalf("clipped expand selection did not reset edge columns: %q", highlighted)
	}
}

func TestG11E4MigratedPathsRejectLegacyGeometryOwners(t *testing.T) {
	targets := map[string]map[string]bool{
		"app.go": {
			"App.renderHintBox":                 false,
			"App.renderCommandHints":            false,
			"App.renderFileHints":               false,
			"App.applyViewportHighlight":        false,
			"App.expandMouseSelectionRow":       false,
			"App.applyExpandHighlight":          false,
			"App.projectModalRenderEnvironment": false,
		},
		"composer_mentions.go": {
			"App.renderMentionHints": false,
		},
		"search.go": {
			"SearchOverlay.Render": false,
		},
		"expand_search.go": {
			"ExpandSearchOverlay.Render": false,
		},
		"command_palette.go": {
			"CommandPalette.Overlay":    false,
			"CommandPalette.renderItem": false,
		},
		"model_picker.go": {
			"ModelPicker.Overlay":     false,
			"ModelPicker.renderItems": false,
		},
		"agent_thread_picker.go": {
			"AgentThreadPicker.Overlay":               false,
			"AgentThreadPicker.renderItemWithProfile": false,
		},
		"help.go": {
			"HelpOverlay.Overlay":        false,
			"HelpOverlay.renderCategory": false,
		},
		"bypass_confirm.go": {
			"App.bypassConfirmOverlay": false,
		},
		"message_selector.go": {
			"MessageSelector.RenderHintBar":           false,
			"MessageSelector.RenderSelectedHighlight": false,
		},
		"queued_input.go": {
			"App.renderQueuedInputRows": false,
		},
		"composer_history_search.go": {
			"App.renderHistorySearch": false,
		},
		"thread_navigation.go": {
			"App.activeThreadDisplayLabel": false,
		},
		"selection_geometry.go": {
			"selectionLineCells":        false,
			"selectionCellByteBoundary": false,
			"selectionSliceCells":       false,
			"selectionHighlightCells":   false,
		},
	}
	rejectedIdentifiers := map[string]bool{
		"overlayCentered": true,
		"truncateDisplay": true,
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
					if rejectedIdentifiers[identifier.Name] {
						t.Errorf(
							"%s %s calls legacy geometry owner %s",
							fileName,
							key,
							identifier.Name,
						)
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
					"ansi.StringWidth", "ansi.Truncate", "ansi.Wrap",
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
