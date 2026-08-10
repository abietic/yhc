package tui

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestG11E3ToolHistoryProfileMatrixBoundsRichAndExpandedRows(t *testing.T) {
	profile := g11D1Profile(8)
	styles := defaultStyles()
	fixture := g11E3FixtureText()

	for _, width := range []int{40, 80, 120, 180} {
		env := newRenderEnvironment(styles, profile)
		ctx := HistoryRenderContext{
			Width:       width,
			Styles:      styles,
			Environment: env,
			Mode:        HistoryRenderRich,
		}
		for _, tool := range g11E3ToolFixtures(fixture) {
			t.Run(tool.name+"/"+strings.Repeat("w", width/40), func(t *testing.T) {
				item := adaptChatItem("g11e3:"+tool.name, tool)
				rich := item.Render(ctx)
				assertG11E3Rows(t, tool.name+"/rich", profile, rich, width)

				expandedCtx := ctx
				expandedCtx.Mode = HistoryRenderExpanded
				expanded := item.Render(expandedCtx)
				assertG11E3Rows(t, tool.name+"/expanded", profile, expanded, width)

				rawCtx := ctx
				rawCtx.Mode = HistoryRenderRaw
				raw := item.Raw(rawCtx)
				if raw != xansi.Strip(raw) {
					t.Fatalf("%s raw projection retained terminal controls: %q", tool.name, raw)
				}
				if strings.TrimSpace(raw) == "" {
					t.Fatalf("%s raw projection is empty", tool.name)
				}
			})
		}
	}
}

func TestG11E3DirectContentSurfacesUseSelectedProfile(t *testing.T) {
	profile := g11D1Profile(8)
	styles := defaultStyles()
	fixture := g11E3FixtureText()

	for _, width := range []int{40, 80, 120, 180} {
		t.Run(strings.Repeat("w", width/40), func(t *testing.T) {
			env := newRenderEnvironment(styles, profile)
			diff := renderStructuredDiffWithProfile(
				profile,
				styles,
				"/tmp/"+fixture+".go",
				"old "+fixture,
				"new "+fixture,
				width,
			)
			assertG11E3Rows(t, "diff", profile, diff, width)

			entry := ErrorEntry{
				Severity:  SeverityError,
				Category:  CategoryTool,
				Title:     "title " + fixture,
				Message:   "message " + fixture,
				Details:   "details " + fixture,
				Context:   "context " + fixture,
				Timestamp: time.Unix(1, 0),
				Suggestions: []SuggestedAction{{
					Label:       "label " + fixture,
					Description: "description " + fixture,
					Command:     "command " + fixture,
				}},
			}
			message := NewErrorMessage(entry, styles)
			message.expanded = true
			errorView := message.RenderWithEnvironment(width, env)
			assertG11E3Rows(t, "error/rich", profile, errorView, width)
			raw := message.RenderRaw(HistoryRenderContext{
				Width:       width,
				Environment: env,
			})
			if raw != xansi.Strip(raw) || strings.TrimSpace(raw) == "" {
				t.Fatalf("error raw projection is not nonempty/control-free: %q", raw)
			}

			app := New(Config{
				Resumed:            true,
				DisplayCellProfile: &profile,
			})
			updateAppSilent(app, tea.WindowSizeMsg{Width: width, Height: 30})
			app.model = fixture
			app.welcomeGreeting = fixture
			app.welcomeTip = fixture
			welcome := app.renderWelcomeBudget(30)
			assertG11E3Rows(t, "welcome", profile, welcome, width)
			if bounds, ok := app.welcomeMascotBounds(); ok {
				if bounds.x < 0 || bounds.x+bounds.width > width {
					t.Fatalf("welcome mascot bounds = %#v outside width %d", bounds, width)
				}
			}

			app.expandConversation = true
			app.expandRaw = true
			app.expandLines = []string{
				fixture,
				strings.Repeat(fixture+" ", 8),
			}
			expandedView := app.renderExpandView()
			assertG11E3Rows(t, "expanded/raw", profile, expandedView, width)
			if !strings.Contains(xansi.Strip(expandedView), "[RAW]") {
				t.Fatalf("expanded/raw status lost semantic mode: %q", expandedView)
			}

			stack := NewNotificationStack()
			now := time.Unix(1_000, 0)
			stack.PushAt(now, fixture, NotifyWarning)
			stack.PushAt(now, "newest "+fixture, NotifyError)
			notifications := stack.RenderWithEnvironment(env, width)
			assertG11E3Rows(t, "notifications", profile, notifications, width)
			single := stack.RenderSingleLineWithEnvironment(env, width)
			assertG11E3Rows(t, "notification/single", profile, single, width)
			if !strings.Contains(xansi.Strip(single), "(+1)") {
				t.Fatalf("single notification lost stack count: %q", single)
			}
		})
	}
}

func TestG11E3ExactEnvironmentInvalidatesFinishedToolCache(t *testing.T) {
	profile := g11D1Profile(8)
	env := newRenderEnvironment(defaultStyles(), profile)
	chat := newChatViewWithRenderEnvironment(env)
	chat.AppendToolStart("g11e3", "UnknownProfileTool", `{"label":"a\tb"}`)
	chat.UpdateToolResult("g11e3", "UnknownProfileTool", "left\t界")
	tool := chat.toolsByID["g11e3"]
	if tool == nil || !tool.Finished() {
		t.Fatal("finished tool fixture was not installed")
	}

	first := chat.renderItem(tool, 40)
	if first.environment != env.identity() {
		t.Fatalf("initial tool cache identity = %#v want %#v", first.environment, env.identity())
	}
	if reused := chat.renderItem(tool, 40); reused != first {
		t.Fatal("exact tool width/environment did not reuse frozen cache")
	}

	resized := env.withGeometry()
	chat.SetRenderEnvironment(resized)
	afterResize := chat.renderItem(tool, 40)
	if afterResize == first || afterResize.environment != resized.identity() {
		t.Fatalf("resize reused stale tool cache: %#v", afterResize)
	}

	themed := resized.withStyles(StylesForTheme(ThemeDaybreak))
	chat.SetRenderEnvironment(themed)
	afterTheme := chat.renderItem(tool, 40)
	if afterTheme == afterResize || afterTheme.environment != themed.identity() {
		t.Fatalf("theme reused stale tool cache: %#v", afterTheme)
	}

	defaultEnv := newRenderEnvironment(themed.styles, g11D1Profile(4))
	profiled := tool.RenderWithEnvironment(40, themed)
	defaulted := tool.RenderWithEnvironment(40, defaultEnv)
	if profiled == defaulted {
		t.Fatalf("tool rich projection ignored selected tab profile: %q", profiled)
	}
}

func TestG11E3NotificationLifecycleIsAppOwnedAndRenderPure(t *testing.T) {
	profile := g11D1Profile(8)
	env := newRenderEnvironment(defaultStyles(), profile)
	now := time.Now()
	stack := NewNotificationStack()
	stack.items = []Notification{
		{
			Message:   "expired",
			Severity:  NotifyInfo,
			CreatedAt: now.Add(-time.Minute),
			Duration:  time.Second,
		},
		{
			Message:   "active\t界",
			Severity:  NotifySuccess,
			CreatedAt: now,
			Duration:  time.Minute,
		},
	}
	stack.PruneAt(now)
	before := stack.Active()
	rendered := stack.RenderSingleLineWithEnvironment(env, 20)
	if strings.Contains(rendered, "expired") || !strings.Contains(xansi.Strip(rendered), "active") {
		t.Fatalf("notification lifecycle changed during geometry projection: %q", rendered)
	}
	if after := stack.Active(); !reflect.DeepEqual(after, before) {
		t.Fatalf("render mutated App-pruned notification state: before=%#v after=%#v", before, after)
	}
	assertG11E3Rows(t, "notification/lifecycle", profile, rendered, 20)
}

func TestG11E3MigratedPathsRejectSecondGeometryOwners(t *testing.T) {
	targets := map[string]map[string]bool{
		"content_geometry.go": {
			"contentProjectLine": false,
			"contentEllipsize":   false,
			"contentWrapLines":   false,
			"contentProjectRows": false,
		},
		"tools.go": {
			"formatToolArgsWithProfile":        false,
			"truncateBashCommandWithProfile":   false,
			"formatSearchArgsWithProfile":      false,
			"formatKeyValueArgsWithProfile":    false,
			"formatArgValueWithProfile":        false,
			"truncateSingleLineWithProfile":    false,
			"renderIndentedResultWithProfile":  false,
			"renderHighlightedReadWithProfile": false,
		},
		"agent_trace.go": {
			"renderAgentToolTraceWithProfile":  false,
			"boundedAgentTraceLineWithProfile": false,
		},
		"tool_history_renderer.go": {
			"genericToolHistoryRenderer.Render": false,
			"renderGenericHistoryHeader":        false,
			"renderGenericHistoryExpanded":      false,
			"genericHistoryPreview":             false,
			"wrapGenericHistoryContent":         false,
		},
		"tool_history_agent.go": {
			"agentToolHistoryRenderer.Render": false,
			"renderAgentHistoryHeader":        false,
			"agentTraceHistoryItem.Render":    false,
		},
		"tool_history_bash.go": {
			"bashToolHistoryRenderer.Render": false,
			"renderBashHistoryHeader":        false,
		},
		"tool_history_read_search.go": {
			"readSearchToolHistoryRenderer.Render": false,
			"renderReadSearchHeader":               false,
			"readSearchHeaderDetail":               false,
		},
		"tool_history_edit_write.go": {
			"editWriteToolHistoryRenderer.Render":   false,
			"renderEditWriteHeader":                 false,
			"renderEditWriteDiff":                   false,
			"truncateHistoryRenderLinesWithProfile": false,
		},
		"tool_history_mcp.go": {
			"mcpToolHistoryRenderer.Render": false,
			"renderMCPHistoryHeader":        false,
			"compactMCPHistoryArguments":    false,
			"flattenMCPHistoryJSON":         false,
			"boundMCPHistoryPreviewWidth":   false,
			"renderMCPHistorySection":       false,
			"wrapMCPHistoryContent":         false,
		},
		"tool_history_plan_task.go": {
			"planTaskTodoToolHistoryRenderer.Render": false,
			"renderPlanTaskHeader":                   false,
			"renderPlanTaskTodoExpanded":             false,
			"boundPlanTaskHistoryPreview":            false,
			"wrapPlanTaskHistoryContent":             false,
		},
		"tool_history_web.go": {
			"webToolHistoryRenderer.Render": false,
			"renderWebHistoryHeader":        false,
			"renderWebHistoryExpanded":      false,
			"boundWebHistoryPreview":        false,
			"wrapWebHistoryContent":         false,
			"webHistoryURLDisplay":          false,
		},
		"diff.go": {
			"renderStructuredDiffWithProfile":        false,
			"renderStructuredDiffBoundedWithProfile": false,
		},
		"error_display.go": {
			"ErrorMessage.RenderWithEnvironment": false,
			"ErrorMessage.RenderRaw":             false,
			"ErrorMessage.RenderExpanded":        false,
			"renderErrorEntryWithProfile":        false,
			"wrapErrorTextWithProfile":           false,
		},
		"notifications.go": {
			"NotificationStack.RenderWithEnvironment":           false,
			"NotificationStack.RenderSingleLineWithEnvironment": false,
			"renderNotificationLine":                            false,
		},
		"welcome.go": {
			"App.renderWelcomeBudget":        false,
			"truncateDisplayWithProfile":     false,
			"truncatePathDisplayWithProfile": false,
			"centerLineWithProfile":          false,
			"maxLineWidthWithProfile":        false,
			"App.welcomeMascotBounds":        false,
		},
		"app.go": {
			"App.renderExpandView": false,
			"App.activeToast":      false,
		},
		"chat.go": {
			"ToolMessage.RenderWithEnvironment": false,
		},
	}
	rejectedIdentifiers := map[string]bool{
		"boundedAgentTraceLine":       true,
		"formatToolArgs":              true,
		"renderAgentToolTrace":        true,
		"renderErrorEntry":            true,
		"renderHighlightedRead":       true,
		"renderIndentedResult":        true,
		"renderStructuredDiff":        true,
		"renderStructuredDiffBounded": true,
		"truncate":                    true,
		"truncateBashCommand":         true,
		"truncateHistoryRenderLines":  true,
		"truncateSingleLine":          true,
		"wrapErrorText":               true,
	}
	rejectedMethods := map[string]bool{
		"RenderSingleLine": true,
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
				if slice, ok := node.(*ast.SliceExpr); ok {
					identifier, stringLike := slice.X.(*ast.Ident)
					allowedMascotPrefix := fileName == "welcome.go" &&
						key == "App.welcomeMascotBounds" &&
						stringLike &&
						identifier.Name == "line"
					allowedReadPrefixParser := fileName == "tools.go" &&
						key == "renderHighlightedReadWithProfile" &&
						stringLike &&
						identifier.Name == "line"
					if stringLike && !allowedMascotPrefix && !allowedReadPrefixParser {
						switch identifier.Name {
						case "content", "detail", "dl", "header", "line",
							"message", "msg", "prefix", "text", "value":
							t.Errorf(
								"%s %s slices visible text %s by byte index",
								fileName,
								key,
								identifier.Name,
							)
						}
					}
				}
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
				if rejectedMethods[selector.Sel.Name] {
					t.Errorf(
						"%s %s calls legacy geometry method %s",
						fileName,
						key,
						selector.Sel.Name,
					)
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

func g11E3FixtureText() string {
	parts := make([]string, 0, len(g11E1ModalFixtures))
	for _, fixture := range g11E1ModalFixtures {
		parts = append(parts, fixture.text)
	}
	return strings.Join(parts, " ")
}

func g11E3ToolFixtures(fixture string) []*ToolMessage {
	jsonText := func(value any) string {
		encoded, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		return string(encoded)
	}
	success := func(name, input, output string) *ToolMessage {
		return &ToolMessage{
			name:    name,
			input:   input,
			output:  output,
			status:  ToolSuccess,
			version: 1,
		}
	}
	agent := success(
		"Agent",
		jsonText(map[string]any{"description": fixture, "prompt": fixture}),
		fixture,
	)
	agent.agentTrace = &agentToolTrace{
		AgentID:          "agent-" + fixture,
		Status:           "completed",
		Summary:          fixture,
		RecentActivities: []agentToolTraceActivity{{ToolName: "Read", Description: fixture}},
	}
	return []*ToolMessage{
		success("UnknownProfileTool", jsonText(map[string]any{"label": fixture}), fixture),
		agent,
		success("Bash", jsonText(map[string]any{"command": "printf " + fixture}), fixture),
		success("Glob", jsonText(map[string]any{"pattern": fixture, "path": "/tmp"}), fixture),
		success("Edit", jsonText(map[string]any{
			"file_path":  "/tmp/e3.go",
			"old_string": "old " + fixture,
			"new_string": "new " + fixture,
		}), "updated"),
		success("mcp__demo__echo", jsonText(map[string]any{"value": fixture}), jsonText(map[string]any{
			"content": []any{map[string]any{"type": "text", "text": fixture}},
		})),
		success("TodoWrite", jsonText(map[string]any{"todos": []any{map[string]any{
			"content": fixture,
			"status":  "in_progress",
		}}}), fixture),
		success("WebFetch", jsonText(map[string]any{
			"url":      "https://example.test/e3",
			"raw_mode": true,
		}), jsonText(map[string]any{
			"result": fixture,
			"bytes":  len(fixture),
			"code":   200,
		})),
	}
}

func assertG11E3Rows(
	t *testing.T,
	name string,
	profile DisplayCellProfile,
	view string,
	width int,
) {
	t.Helper()
	for row, line := range strings.Split(view, "\n") {
		if got := profile.measure(line, 0); got > width {
			t.Fatalf(
				"%s row %d width = %d, want <= %d: %q",
				name,
				row,
				got,
				width,
				line,
			)
		}
		assertWidthProfileControlStateClosed(t, profile, line)
	}
}
