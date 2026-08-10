package tui

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func g11D3AwayChat(t *testing.T, width, height int) *ChatView {
	t.Helper()
	chat := NewChatView(StylesForTheme(ThemePolarNight))
	chat.SetSize(width, height)
	for range 20 {
		chat.AppendUser("history")
	}
	chat.Render(width, height)
	chat.ScrollToTop()
	return chat
}

func TestG11D3PillGeometryLabelsAndBounds(t *testing.T) {
	for _, width := range []int{40, 80, 120, 150, 180} {
		for _, tc := range []struct {
			name   string
			mutate func(*ChatView)
			want   string
		}{
			{name: "zero", want: "Jump to bottom"},
			{name: "one", mutate: func(c *ChatView) { c.AppendUser("new") }, want: "1 new message"},
			{name: "many", mutate: func(c *ChatView) { c.AppendUser("new"); c.AppendUser("new") }, want: "2 new messages"},
		} {
			t.Run(fmt.Sprintf("%d/%s", width, tc.name), func(t *testing.T) {
				chat := g11D3AwayChat(t, width, 10)
				if tc.mutate != nil {
					tc.mutate(chat)
				}
				geometry := chat.pillGeometry(width, 10)
				if !geometry.visible || geometry.label != tc.want || geometry.row != 9 {
					t.Fatalf("geometry = %#v", geometry)
				}
				if got := chat.environment.profile.measure(geometry.renderedRun, geometry.start); got != geometry.end-geometry.start {
					t.Fatalf("run cells=%d bounds=[%d,%d)", got, geometry.start, geometry.end)
				}
				if geometry.start < 0 || geometry.end > width || geometry.start >= geometry.end {
					t.Fatalf("geometry escapes rectangle: %#v", geometry)
				}
				lines := strings.Split(chat.Render(width, 10), "\n")
				if lines[geometry.row] != geometry.renderedLine {
					t.Fatalf("rendered row = %q, want published %q", lines[geometry.row], geometry.renderedLine)
				}
				app := newTestApp(width, 24)
				app.chat = chat
				app.layout.chatRect.Width = width
				app.layout.chatHeight = 10
				if !app.pillClickHits(geometry.start, geometry.row) ||
					!app.pillClickHits(geometry.end-1, geometry.row) {
					t.Fatalf("published boundary did not hit: %#v", geometry)
				}
				if app.pillClickHits(geometry.start-1, geometry.row) ||
					app.pillClickHits(geometry.end, geometry.row) ||
					app.pillClickHits(geometry.start, geometry.row-1) {
					t.Fatalf("outside published boundary hit: %#v", geometry)
				}
			})
		}
	}
}

func TestG11D3PillGeometryProfileGlyphMatrix(t *testing.T) {
	chat := g11D3AwayChat(t, 80, 8)
	for _, label := range []string{
		"ASCII", "A\tB", "中文", "e\u0301", "क्ष", "❤︎", "❤️", "👩🏽‍💻", "🇺🇸", "🇺", "🏷", "✦",
		"\x1b[35mANSI\x1b[0m", "\x1b]8;;https://example.test\x1b\\OSC\x1b]8;;\x1b\\",
	} {
		geometry := chat.buildPillGeometry(chatPillModel{visible: true, label: label, action: chatPillActionFollow}, 80, 8, chat.environment.identity())
		if got := chat.environment.profile.measure(geometry.renderedRun, geometry.start); got != geometry.end-geometry.start {
			t.Fatalf("label=%q measured=%d bounds=[%d,%d)", label, got, geometry.start, geometry.end)
		}
		if strings.Contains(geometry.renderedRun, "\t") {
			t.Fatalf("label=%q retained a tab at start column %d", label, geometry.start)
		}
		assertWidthProfileControlStateClosed(t, chat.environment.profile, geometry.renderedRun)
	}
}

func TestG11D3PillGeometryExpandsTabsAtPublishedOrigin(t *testing.T) {
	chat := g11D3AwayChat(t, 80, 8)
	model := chatPillModel{visible: true, label: "A\tB", action: chatPillActionFollow}
	geometry := chat.buildPillGeometry(model, 80, 8, chat.environment.identity())
	raw := chat.styles.UserMessageBlock.Render(chat.styles.Dim.Render(model.renderText()))
	want := chat.environment.profile.expandTabs(raw, geometry.start)
	want = chat.environment.profile.balanceControlLines([]string{want})[0]
	if geometry.renderedRun != want {
		t.Fatalf("rendered tab run=%q, want expansion at column %d: %q", geometry.renderedRun, geometry.start, want)
	}
}

func TestG11D3PillGeometryCacheSeparatesResizeAndEnvironment(t *testing.T) {
	chat := g11D3AwayChat(t, 80, 8)
	baseline := chat.followState
	first := chat.pillGeometry(80, 8)
	resized := chat.pillGeometry(120, 8)
	if first.width == resized.width || resized.profileID != chat.environment.profile.Identity() {
		t.Fatalf("resize did not recompute geometry: first=%#v resized=%#v", first, resized)
	}
	chat.SetRenderEnvironment(chat.environment.withStyles(StylesForTheme(ThemeDaybreak)))
	themed := chat.pillGeometry(120, 8)
	if themed.environment == resized.environment || themed.profileID != chat.environment.profile.Identity() {
		t.Fatalf("theme did not separate geometry cache: resized=%#v themed=%#v", resized, themed)
	}
	policy := defaultDisplayCellPolicy()
	policy.TabStop = 8
	profiled := chat.environment
	profiled.profile = newDisplayCellProfile(policy)
	chat.SetRenderEnvironment(profiled)
	withProfile := chat.pillGeometry(120, 8)
	if withProfile.profileID == themed.profileID || withProfile.environment == themed.environment {
		t.Fatalf("profile did not separate geometry cache: themed=%#v profiled=%#v", themed, withProfile)
	}
	if chat.followState != baseline {
		t.Fatalf("geometry recomputation changed follow semantics: before=%#v after=%#v", baseline, chat.followState)
	}
}

func TestG11D3PillGeometryPublishedHitBounds(t *testing.T) {
	app := newTestApp(80, 24)
	app.chat.SetSize(app.layout.chatRect.Width, app.layout.chatHeight)
	for range 20 {
		app.chat.AppendUser("history")
	}
	app.chat.Render(app.layout.chatRect.Width, app.layout.chatHeight)
	app.chat.ScrollToTop()
	app.chat.Render(app.layout.chatRect.Width, app.layout.chatHeight)
	geometry := app.chat.currentViewportProjection().pill
	if !app.pillClickHits(geometry.start, geometry.row) || !app.pillClickHits(geometry.end-1, geometry.row) {
		t.Fatalf("published boundary did not hit: %#v", geometry)
	}
	if app.pillClickHits(geometry.start-1, geometry.row) || app.pillClickHits(geometry.end, geometry.row) {
		t.Fatalf("outside published boundary hit: %#v", geometry)
	}
}

func TestG11D3PillGeometrySourceOwners(t *testing.T) {
	files := map[string][]string{
		"chat.go": {"Render"},
		"app.go":  {"pillClickHits"},
	}
	for file, names := range files {
		found := make(map[string]bool, len(names))
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !containsString(names, fn.Name.Name) {
				continue
			}
			found[fn.Name.Name] = true
			var forbidden []string
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if ok {
					if identifier, ok := call.Fun.(*ast.Ident); ok &&
						(identifier.Name == "truncateDisplay" ||
							identifier.Name == "terminalLayoutSafetyWidth") {
						forbidden = append(forbidden, identifier.Name)
					}
				}
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				receiver, _ := selector.X.(*ast.Ident)
				if receiver != nil && (receiver.Name == "lipgloss" || receiver.Name == "ansi" || receiver.Name == "xansi") &&
					(selector.Sel.Name == "Width" || selector.Sel.Name == "StringWidth" || selector.Sel.Name == "Truncate") {
					forbidden = append(forbidden, receiver.Name+"."+selector.Sel.Name)
				}
				if file == "app.go" && (selector.Sel.Name == "pillModel" || selector.Sel.Name == "renderText") {
					forbidden = append(forbidden, selector.Sel.Name)
				}
				return true
			})
			if len(forbidden) > 0 {
				t.Fatalf("%s.%s selected independent geometry owners: %v", file, fn.Name.Name, forbidden)
			}
		}
		for _, name := range names {
			if !found[name] {
				t.Fatalf("%s.%s source owner was not found", file, name)
			}
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
