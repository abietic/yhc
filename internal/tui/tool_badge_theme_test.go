package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

type toolBadgeGoldenCase struct {
	label       string
	name        string
	displayName string
	input       string
	output      string
	foreground  func(colorPalette) tuiColor
}

var toolBadgeGoldenCases = []toolBadgeGoldenCase{
	{
		label: "bash", name: "Bash", displayName: "Bash",
		input: `{"command":"printf badge"}`, output: "badge",
		foreground: func(p colorPalette) tuiColor { return p.brand },
	},
	{
		label: "file", name: "Read", displayName: "Read",
		input: `{"file_path":"README.md"}`, output: "content",
		foreground: func(p colorPalette) tuiColor { return p.green },
	},
	{
		label: "search", name: "Grep", displayName: "Grep",
		input: `{"pattern":"badge"}`, output: "internal/tui/tools.go: badge",
		foreground: func(p colorPalette) tuiColor { return p.auroraSky },
	},
	{
		label: "agent", name: "Agent", displayName: "Agent",
		input: `{"description":"inspect badges"}`, output: "done",
		foreground: func(p colorPalette) tuiColor { return p.permission },
	},
	{
		label: "plan", name: "EnterPlanMode", displayName: "Plan",
		input: `{}`, output: "entered plan mode",
		foreground: func(p colorPalette) tuiColor { return p.permission },
	},
	{
		label: "mcp", name: "mcp__docs__lookup", displayName: "MCP",
		input: `{"query":"badge"}`, output: `{"content":[{"type":"text","text":"found"}]}`,
		foreground: func(p colorPalette) tuiColor { return p.brand },
	},
	{
		label: "web", name: "WebSearch", displayName: "Web",
		input: `{"query":"badge"}`, output: `{"results":[]}`,
		foreground: func(p colorPalette) tuiColor { return p.auroraSky },
	},
}

func TestToolBadgeThemeGolden(t *testing.T) {
	var got strings.Builder
	for _, theme := range []ThemeName{ThemePolarNight, ThemeDaybreak, ThemeDarkAnsi} {
		profile := toolBadgeColorProfile(theme)
		styles := StylesForTheme(theme)
		palette := getPalette(theme)

		fmt.Fprintf(&got, "[%s]\n", theme)
		for _, tc := range toolBadgeGoldenCases {
			rendered := renderToolBadgeHistoryCase(tc, styles)
			badge := toolNameStyled(styles, tc.displayName)
			if !strings.Contains(rendered, badge) {
				t.Errorf("%s %s history does not contain shared badge %q", theme, tc.label, badge)
			}
			if width := ansi.StringWidth(badge); width != ansi.StringWidth(tc.displayName) {
				t.Errorf(
					"%s %s badge width = %d, want %d",
					theme,
					tc.label,
					width,
					ansi.StringWidth(tc.displayName),
				)
			}

			sgr := lastSGRBefore(rendered, tc.displayName, false)
			wantColor := expectedColorSGR(
				profile,
				tuiColorString(tc.foreground(palette)),
				tuiColorString(palette.element),
			)
			if !strings.Contains(sgr, wantColor) {
				t.Errorf("%s %s SGR = %q, want color fragment %q", theme, tc.label, sgr, wantColor)
			}
			fmt.Fprintf(&got, "%s=%s\n", tc.label, sgr)
		}
		got.WriteByte('\n')
	}

	want, err := os.ReadFile("testdata/tool_badges.golden")
	if err != nil {
		t.Fatalf("read tool badge golden: %v\n--- got ---\n%s", err, got.String())
	}
	if strings.TrimSpace(got.String()) != strings.TrimSpace(string(want)) {
		t.Fatalf(
			"tool badge golden mismatch:\n--- got ---\n%s--- want ---\n%s",
			got.String(),
			want,
		)
	}
}

func TestToolBadgeKnownAliasesAndUnknownFallback(t *testing.T) {
	styles := StylesForTheme(ThemePolarNight)
	categoryAliases := []struct {
		category   string
		names      []string
		foreground tuiColor
	}{
		{
			category: "brand", names: []string{"Bash", "BashOutput", "KillShell", "Shell", "MCP"},
			foreground: styles.AssistantPrefix.GetForeground(),
		},
		{
			category: "green", names: []string{"Read", "Write", "Edit", "To-Do"},
			foreground: styles.ToolSuccess.GetForeground(),
		},
		{
			category: "sky", names: []string{"Grep", "Glob", "LS", "Explore", "Task", "Web", "WebFetch", "WebSearch"},
			foreground: styles.AuroraSky.GetForeground(),
		},
		{
			category: "violet", names: []string{"Agent", "Plan"},
			foreground: styles.DialogTitle.GetForeground(),
		},
	}
	for _, tc := range categoryAliases {
		for _, name := range tc.names {
			foreground, known := toolCategoryForeground(styles, name)
			if !known {
				t.Errorf("%s tool %q is not classified", tc.category, name)
				continue
			}
			if foreground != tc.foreground {
				t.Errorf(
					"%s tool %q foreground = %v, want semantic %v",
					tc.category,
					name,
					foreground,
					tc.foreground,
				)
			}
			rendered := toolNameStyled(styles, name)
			if background := lastSGRBefore(rendered, name, false); !strings.Contains(
				background,
				expectedColorSGR(
					termenv.TrueColor,
					fmt.Sprint(tc.foreground),
					tuiColorString(getPalette(ThemePolarNight).element),
				),
			) {
				t.Errorf("%s tool %q does not use its semantic foreground on Element: %q", tc.category, name, background)
			}
		}
	}

	const unknown = "CustomTool"
	if _, known := toolCategoryForeground(styles, unknown); known {
		t.Fatalf("unknown tool %q was classified", unknown)
	}
	if got, want := toolNameStyled(styles, unknown), styles.ToolName.Render(unknown); got != want {
		t.Fatalf("unknown tool badge = %q, want unchanged ToolName render %q", got, want)
	}
}

func TestToolBadgeDarkANSIUsesOnlyANSI16(t *testing.T) {
	styles := StylesForTheme(ThemeDarkAnsi)
	for _, tc := range toolBadgeGoldenCases {
		rendered := renderToolBadgeHistoryCase(tc, styles)
		badgeSGR := lastSGRBefore(rendered, tc.displayName, false)
		for _, forbidden := range []string{"38;2;", "48;2;", "38;5;", "48;5;"} {
			if strings.Contains(badgeSGR, forbidden) {
				t.Errorf("%s badge contains non-ANSI-16 escape %q", tc.label, forbidden)
			}
		}
	}
}

func renderToolBadgeHistoryCase(tc toolBadgeGoldenCase, styles Styles) string {
	return (&ToolMessage{
		name:    tc.name,
		input:   tc.input,
		output:  tc.output,
		status:  ToolSuccess,
		version: 1,
	}).Render(120, styles)
}

func toolBadgeColorProfile(theme ThemeName) termenv.Profile {
	switch theme {
	case ThemeDarkAnsi, ThemeLightAnsi:
		return termenv.ANSI
	default:
		return termenv.TrueColor
	}
}
