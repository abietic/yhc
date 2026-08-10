package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"

	"github.com/abietic/yhc/engine"
)

// g11AEvidenceCells is deliberately test-only. It does not use the production
// width profile, Lip Gloss, or x/ansi: it removes only CSI and OSC 8 controls,
// segments EGCs with uniseg, and applies an explicit frozen fixture map.
var g11AEvidenceCells = map[string]int{
	"🖥": 1, "⚙": 1, "🏷": 2, "✦": 1, "中": 2, "क्ष": 2,
	"❤︎": 1, "❤️": 2, "⚠": 1, "⚠⚠": 2, "⚠️": 2,
	"👩🏽‍💻": 2, "🇺🇸": 2, "🇺": 1, "é": 1,
}

func g11AOracleStripControls(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '\x1b' || i+1 >= len(s) {
			out.WriteByte(s[i])
			i++
			continue
		}
		switch s[i+1] {
		case '[': // 7-bit CSI: ESC [ parameters/intermediates final-byte.
			i += 2
			for i < len(s) {
				if s[i] >= 0x40 && s[i] <= 0x7e {
					i++
					break
				}
				i++
			}
		case ']': // OSC 8 (and malformed OSC) terminates at BEL or ST.
			i += 2
			for i < len(s) {
				if s[i] == '\a' {
					i++
					break
				}
				if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
		default:
			// This fixture oracle intentionally has no terminal parser fallback.
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}

func g11AOracleCells(s string) int {
	plain := g11AOracleStripControls(s)
	width := 0
	for plain != "" {
		fixture, cells := "", 0
		for candidate, candidateCells := range g11AEvidenceCells {
			if strings.HasPrefix(plain, candidate) && len(candidate) > len(fixture) {
				fixture, cells = candidate, candidateCells
			}
		}
		if fixture != "" {
			width += cells
			plain = plain[len(fixture):]
			continue
		}
		clusters := uniseg.NewGraphemes(plain)
		if !clusters.Next() {
			break
		}
		cluster := clusters.Str()
		// Every non-fixture cluster below is ASCII table chrome/text in this test.
		width += utf8.RuneCountInString(cluster)
		plain = plain[len(cluster):]
	}
	return width
}

func g11AOracleColumns(line string, needle rune) []int {
	plain := g11AOracleStripControls(line)
	column := 0
	var columns []int
	for plain != "" {
		fixture, cells := "", 0
		for candidate, candidateCells := range g11AEvidenceCells {
			if strings.HasPrefix(plain, candidate) && len(candidate) > len(fixture) {
				fixture, cells = candidate, candidateCells
			}
		}
		if fixture != "" {
			column += cells
			plain = plain[len(fixture):]
			continue
		}
		clusters := uniseg.NewGraphemes(plain)
		if !clusters.Next() {
			break
		}
		cluster := clusters.Str()
		if cluster == string(needle) {
			columns = append(columns, column)
		}
		column += utf8.RuneCountInString(cluster)
		plain = plain[len(cluster):]
	}
	return columns
}

func TestG11AWidthMethodCurrentMatrix(t *testing.T) {
	tests := []struct {
		row, cluster          string
		oracle, layout, table int
	}{
		{"desktop", "🖥", 1, 1, 1},
		{"gear", "⚙", 1, 1, 1},
		{"label", "🏷", 2, 1, 2},
		{"sparkle", "✦", 1, 1, 1},
		{"cjk", "中", 2, 2, 2},
		{"indic", "क्ष", 2, 1, 2},
		{"text-heart", "❤︎", 1, 1, 1},
		{"emoji-heart", "❤️", 2, 2, 2},
		{"warning", "⚠", 1, 1, 1},
		{"two-warnings", "⚠⚠", 2, 2, 2},
		{"emoji-warning", "⚠️", 2, 2, 2},
		{"zwj", "👩🏽‍💻", 2, 2, 2},
		{"flag", "🇺🇸", 2, 2, 2},
		{"lone-ri", "🇺", 1, 2, 1},
		{"nfd", "é", 1, 1, 1},
		{"ansi-gear", "\x1b[1m⚙\x1b[22m", 1, 1, 1},
		{"osc8-label", "\x1b]8;;https://example.test\x1b\\🏷\x1b]8;;\x1b\\", 2, 1, 2},
	}
	profile := DefaultDisplayCellProfile()
	for _, test := range tests {
		t.Run(test.row, func(t *testing.T) {
			wantOracle := g11AOracleCells(test.cluster)
			if wantOracle != test.oracle {
				t.Fatalf("cluster=%q method/profile=independent-oracle expected=%d actual=%d layout-mode=unit terminal-width=0 row=%s", test.cluster, test.oracle, wantOracle, test.row)
			}
			actual := []struct {
				method    string
				got, want int
			}{
				{"layout-xansi", xansi.StringWidth(test.cluster), test.layout},
				{"table-profile:" + profile.id, profile.width(test.cluster), test.table},
			}
			for _, method := range actual {
				if method.got != method.want {
					t.Fatalf("cluster=%q method/profile=%s expected=%d actual=%d oracle=%d layout-mode=unit terminal-width=0 row=%s", test.cluster, method.method, method.want, method.got, wantOracle, test.row)
				}
			}
		})
	}
}

func TestG11ATableLifecycleAndResizeCharacterization(t *testing.T) {
	const table = "| `eino-agent` | **Codebase** | Mixed |\n| --- | --- | --- |\n| 🏷 | क्ष | plain **bold** `code` suffix |"
	stream := &StreamingMarkdown{}
	incomplete := stream.Render(table, 72, ThemePolarNight)
	if plain := xansi.Strip(incomplete); strings.Contains(plain, "┌") || !strings.Contains(plain, "| `eino-agent` |") {
		t.Fatalf("cluster=table method/profile=streaming-incomplete expected=literal actual=%q layout-mode=standard terminal-width=72 row=active", plain)
	}
	semanticSource := table + "\n\nfollowing sibling"
	promoted := stream.Render(semanticSource, 72, ThemePolarNight)
	if plain := xansi.Strip(promoted); !strings.Contains(plain, "┌") {
		t.Fatalf("cluster=table method/profile=stable-prefix expected=semantic actual=%q layout-mode=standard terminal-width=72 row=promoted", plain)
	}
	stream.Finalize(semanticSource)
	for _, width := range []int{32, 72} {
		rendered := stream.Render(semanticSource, width, ThemePolarNight)
		plain := xansi.Strip(rendered)
		for _, want := range []string{"eino-agent", "Codebase", "plain", "bold", "code", "suffix"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("cluster=%q method/profile=finalized expected=visible actual=missing layout-mode=standard terminal-width=%d row=final", want, width)
			}
		}
		if strings.Contains(plain, "`eino-agent`") || strings.Contains(plain, "**Codebase**") {
			t.Fatalf("cluster=markdown-marker method/profile=finalized expected=none actual=%q layout-mode=standard terminal-width=%d row=final", plain, width)
		}
	}
	parsed := parseMarkdownTable(table)
	if parsed == nil || len(parsed.rows) != 1 || len(parsed.rows[0][2].runs) < 4 {
		t.Fatalf("cluster=mixed-cell method/profile=Goldmark expected=semantic-runs actual=%#v layout-mode=unit terminal-width=72 row=mixed", parsed)
	}
}

func TestG11ACrossRunEGCControlSplitCharacterization(t *testing.T) {
	theme := markdownThemeForName(ThemePolarNight)
	for _, test := range []struct {
		name string
		run  tableRun
	}{
		{name: "bold", run: tableRun{text: "\u0301", bold: true}},
		{name: "code", run: tableRun{text: "\u0301", code: true}},
		{name: "link", run: tableRun{text: "\u0301", linkURL: "https://example.test"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rendered := renderTableCell(tableCell{runs: []tableRun{
				{text: "e"},
				test.run,
			}}, theme)
			if got := g11AOracleCells(rendered); got != 1 {
				t.Fatalf("cluster=é method/profile=oracle expected=1 actual=%d layout-mode=unit terminal-width=0 row=%s", got, test.name)
			}
			if plain := g11AOracleStripControls(rendered); plain != "e\u0301" {
				t.Fatalf("cluster=é method/profile=control-strip expected=%q actual=%q layout-mode=unit terminal-width=0 row=%s", "e\u0301", plain, test.name)
			}
			clusters := uniseg.NewGraphemes(g11AOracleStripControls(rendered))
			clusterCount := 0
			for clusters.Next() {
				clusterCount++
			}
			if clusterCount != 1 {
				t.Fatalf("cluster=é method/profile=uniseg expected=1 actual=%d layout-mode=unit terminal-width=0 row=%s", clusterCount, test.name)
			}
			e := strings.Index(rendered, "e")
			mark := strings.Index(rendered, "\u0301")
			if e < 0 || mark <= e || !strings.Contains(rendered[e+1:mark], "\x1b") {
				t.Fatalf("cluster=é method/profile=renderTableCell expected=control-between-scalars actual=%q layout-mode=unit terminal-width=0 row=%s", rendered, test.name)
			}
		})
	}
	plain := renderTableCell(tableCell{runs: []tableRun{{text: "a"}, {text: "b"}}}, theme)
	if plain != "ab" {
		t.Fatalf("cluster=ab method/profile=renderTableCell expected=no-control actual=%q layout-mode=unit terminal-width=0 row=adjacent-plain", plain)
	}
}

func TestG11D2WideAppTableSidebarProfileColumns(t *testing.T) {
	query := responsiveTestEngine(t)
	app := New(Config{Engine: query, Resumed: true, Model: "test-model"})
	explorer := p313ExplorerSnapshot()
	explorer.Executions = []engine.TaskExplorerExecution{
		p313Execution("g11a-agent", 1, engine.TaskExplorerExecutionRunning),
	}
	explorer.Executions[0].Task = "live sidebar row"
	installP313ExplorerSnapshot(app, &explorer)
	app.chat.AppendOrUpdateAssistant("| `eino-agent` | **Codebase** |\n| --- | --- |\n| 🏷 | क्ष |")
	app.chat.FinishAssistant()
	updateAppSilent(app, tea.WindowSizeMsg{Width: 180, Height: 30})
	view := app.renderView()
	plain := xansi.Strip(view)
	for _, want := range []string{"eino-agent", "Codebase", "🏷", "क्ष", "WORK", "RUN", "live sidebar row"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("cluster=%q method/profile=wide-app expected=visible actual=missing layout-mode=%s terminal-width=180 row=frame", want, app.layout.mode)
		}
	}
	if app.layout.mode != layoutModeWide || app.layout.sidebarRect.Width == 0 {
		t.Fatalf("cluster=sidebar method/profile=layout expected=wide actual=%#v layout-mode=%s terminal-width=180 row=frame", app.layout.sidebarRect, app.layout.mode)
	}
	// The sidebar separator shares the App-selected profile grid with the
	// semantic table row, so its physical column is the allocated rectangle X.
	found := false
	for row, line := range strings.Split(view, "\n") {
		visible := g11AOracleStripControls(line)
		if !strings.Contains(visible, "🏷") || !strings.Contains(visible, "क्ष") {
			continue
		}
		columns := g11AOracleColumns(line, '│')
		if len(columns) == 0 {
			t.Fatalf("cluster=sidebar method/profile=oracle expected=separator actual=none layout-mode=%s terminal-width=180 row=%d", app.layout.mode, row)
		}
		got := columns[len(columns)-1]
		want := app.layout.sidebarRect.X
		if got != want {
			t.Fatalf("cluster=🏷+क्ष method/profile=table-profile-vs-final-frame expected=%d actual=%d layout-mode=%s terminal-width=180 row=%d visible=%q", want, got, app.layout.mode, row, visible)
		}
		found = true
	}
	if !found {
		t.Fatal("cluster=sidebar method/profile=oracle expected=live-row actual=absent layout-mode=wide terminal-width=180 row=frame")
	}
}
