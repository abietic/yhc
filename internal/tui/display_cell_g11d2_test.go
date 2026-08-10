package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/internal/tui/terminalcap"
)

func TestG11D2FinalizeFrameGeometryBoundsAndDiagnostic(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	view := "\x1b[31m🏷\tक्ष\x1b]8;;https://example.test\x1b\\ linked"
	bounded, diagnostic := finalizeFrameGeometry(view, 6, profile)
	if diagnostic == nil {
		t.Fatal("missing first-overflow diagnostic")
	}
	if diagnostic.FirstOverflowRow != 0 || diagnostic.MeasuredWidth <= diagnostic.Limit {
		t.Fatalf("diagnostic = %#v", diagnostic)
	}
	if !strings.Contains(diagnostic.diagnosticSummary(), profile.Identity()) ||
		!strings.Contains(diagnostic.diagnosticSummary(), "Unicode:") ||
		!strings.Contains(diagnostic.diagnosticSummary(), "Ambiguous width:") ||
		!strings.Contains(diagnostic.diagnosticSummary(), "Emoji policy:") ||
		!strings.Contains(diagnostic.diagnosticSummary(), "Tabs:") {
		t.Fatalf("diagnostic summary omitted selected profile: %q", diagnostic.diagnosticSummary())
	}
	for _, line := range strings.Split(bounded, "\n") {
		if got := profile.width(line); got > 6 {
			t.Fatalf("bounded line width = %d, want <= 6: %q", got, line)
		}
		assertWidthProfileControlStateClosed(t, profile, line)
	}
}

func TestG11D2FinalFrameUsesSelectedProfileAtRepresentativeWidths(t *testing.T) {
	app := New(Config{Resumed: true, Model: "test-model", StatusLineHook: func(_, _ string) (string, string) {
		return "\x1b[35m🏷\t状态", "\x1b]8;;https://example.test\x1b\\右侧"
	}})
	app.chat.AppendUser("generic non-assistant 🏷\tक्ष history")
	app.chat.AppendOrUpdateAssistant("assistant streaming 🏷\tक्ष output")
	for _, width := range []int{40, 80, 120, 150, 180} {
		updateAppSilent(app, teaWindowSize(width, 30))
		for row, line := range strings.Split(app.renderView(), "\n") {
			if got := app.renderEnvironment.profile.width(line); got > width {
				t.Fatalf("width=%d row=%d measured=%d line=%q", width, row, got, line)
			}
			assertWidthProfileControlStateClosed(t, app.renderEnvironment.profile, line)
		}
	}
	app.terminalCaps.Color = terminalcap.ColorNone
	if view := app.renderView(); strings.Contains(view, "\x1b") {
		t.Fatalf("no-color final frame retained control bytes: %q", view)
	}
}

func TestG11D2LayoutUsesOwningColumnForTabs(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	atOrigin := fitLayoutColumnLine(profile, "\tX", 4, 0)
	afterPrefix := fitLayoutColumnLine(profile, "\tX", 4, 1)
	if atOrigin == afterPrefix {
		t.Fatalf("tab projection ignored owning start column: origin=%q prefixed=%q", atOrigin, afterPrefix)
	}
	for _, line := range []string{atOrigin, afterPrefix} {
		if got := profile.measure(line, 0); got != 4 {
			t.Fatalf("fitted tab line width = %d, want 4: %q", got, line)
		}
	}
}

func TestG11D2StatusHookAlignmentAndCrowdedFallback(t *testing.T) {
	profile := DefaultDisplayCellProfile()
	left := "\x1b[31mleft\x1b[0m"
	right := "\x1b]8;;https://example.test\x1b\\右侧\x1b]8;;\x1b\\"
	width := profile.width(left) + 3 + profile.width(right)

	aligned := alignStatusLine(profile, left, right, width)
	if got := profile.width(aligned); got != width {
		t.Fatalf("aligned status width = %d, want %d: %q", got, width, aligned)
	}
	if !strings.HasSuffix(aligned, right) {
		t.Fatalf("right status segment was not right aligned: %q", aligned)
	}
	bounded, diagnostic := finalizeFrameGeometry(aligned, width, profile)
	if diagnostic != nil {
		t.Fatalf("aligned status unexpectedly overflowed: %#v", diagnostic)
	}
	assertWidthProfileControlStateClosed(t, profile, bounded)

	crowdedWidth := profile.width(left)
	crowded := alignStatusLine(profile, left, right, crowdedWidth)
	if got := profile.width(crowded); got > crowdedWidth {
		t.Fatalf("crowded status width = %d, want <= %d: %q", got, crowdedWidth, crowded)
	}
	if strings.Contains(crowded, "右侧") {
		t.Fatalf("crowded status retained right segment: %q", crowded)
	}
	bounded, diagnostic = finalizeFrameGeometry(crowded, crowdedWidth, profile)
	if diagnostic != nil {
		t.Fatalf("crowded fallback unexpectedly overflowed: %#v", diagnostic)
	}
	assertWidthProfileControlStateClosed(t, profile, bounded)
}

func TestG11D2MigratedCallSitesSelectOnlyProfileGeometry(t *testing.T) {
	targets := map[string]map[string]bool{
		"app.go": {
			"App.finalizeView":      false,
			"finalizeFrameGeometry": false,
			"App.renderStatus":      false,
			"alignStatusLine":       false,
			"truncateStatusSegment": false,
		},
		"layout.go": {
			"renderLayoutBands":   false,
			"joinLayoutColumns":   false,
			"fitLayoutColumnLine": false,
		},
		"responsive_sidebar.go": {
			"App.renderWideSidebar": false,
		},
		"chat.go": {
			"ChatView.renderItem":                         false,
			"AssistantMessage.RenderLinesWithEnvironment": false,
			"truncateStickyPrompt":                        false,
		},
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
			key := function.Name.Name
			if function.Recv != nil && len(function.Recv.List) == 1 {
				receiver := function.Recv.List[0].Type
				if pointer, ok := receiver.(*ast.StarExpr); ok {
					receiver = pointer.X
				}
				if identifier, ok := receiver.(*ast.Ident); ok {
					key = identifier.Name + "." + key
				}
			}
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
					switch identifier.Name {
					case "terminalLayoutSafetyWidth", "truncateDisplay":
						t.Errorf("%s %s selects legacy geometry helper %s", fileName, key, identifier.Name)
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
				case "xansi.StringWidth", "xansi.Truncate",
					"ansi.StringWidth", "ansi.Truncate", "lipgloss.Width":
					t.Errorf(
						"%s %s selects second geometry method %s.%s",
						fileName, key, qualifier.Name, selector.Sel.Name,
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

func teaWindowSize(width, height int) tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: width, Height: height}
}
