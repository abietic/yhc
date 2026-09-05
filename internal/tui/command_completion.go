package tui

import (
	contextPkg "context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/commands"
)

func splitSlashCommandInput(query string) (name, arguments string, hasSeparator bool) {
	for index, r := range query {
		if unicode.IsSpace(r) {
			return query[:index], query[index:], true
		}
	}
	return query, "", false
}

func lastCommandArgument(arguments string) string {
	fields := strings.Fields(arguments)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

func commandNameMatches(cmd *commands.Command, query string) bool {
	if cmd == nil || query == "" {
		return cmd != nil && query == ""
	}
	if strings.HasPrefix(cmd.Name, query) {
		return true
	}
	for _, alias := range cmd.Aliases {
		if strings.HasPrefix(alias, query) {
			return true
		}
	}
	return false
}

func commandMatchRank(cmd *commands.Command, query string) int {
	if cmd == nil || query == "" {
		return 4
	}
	if cmd.Name == query {
		return 0
	}
	for _, alias := range cmd.Aliases {
		if alias == query {
			return 1
		}
	}
	if strings.HasPrefix(cmd.Name, query) {
		return 2
	}
	return 3
}

func commandSupportsFileHints(cmd *commands.Command) bool {
	if cmd == nil {
		return false
	}
	// These core commands explicitly accept workspace paths. Command metadata
	// is intentionally not inferred here: skills and unrelated core command
	// arguments must never cause filesystem reads merely while composing text.
	switch cmd.Name {
	case "add-dir", "export":
		return true
	default:
		return false
	}
}

func (a *App) commandForInput(name string) *commands.Command {
	if a == nil || a.commandRegistry == nil || name == "" {
		return nil
	}
	return a.commandRegistry.GetForContext(
		contextPkg.Background(),
		commands.EntrypointTUI,
		a.commandCapabilityContext(),
		strings.ToLower(name),
	)
}

// commandArgumentGhostHint returns a presentation-only argument hint for an
// exact supported command. It never changes the composer model or dispatches
// input; the command registry remains its sole argument-schema owner.
func (a *App) commandArgumentGhostHint() string {
	if a == nil || a.focus != FocusEditor || a.inputMode != InputCommand ||
		(a.state != StateWelcome && a.state != StateChat) ||
		a.suppressingHistoryHints() || a.historySearch.Active ||
		a.composerInputBlocked() || a.externalEditorActive ||
		a.composerImageLoadPending != nil {
		return ""
	}
	value := a.textarea.Value()
	if !strings.HasPrefix(value, "/") || strings.Contains(value, "\n") {
		return ""
	}
	line := a.textarea.LineInfo()
	if line.Height != 1 || a.textarea.Line() != 0 || line.RowOffset != 0 ||
		textareaCursorRuneOffset(value, a.textarea.Line(), line.StartColumn+line.ColumnOffset) != utf8.RuneCountInString(value) {
		return ""
	}
	name, arguments, hasSeparator := splitSlashCommandInput(strings.TrimPrefix(value, "/"))
	if !hasSeparator || strings.TrimSpace(arguments) != "" {
		return ""
	}
	cmd := a.commandForInput(name)
	if cmd == nil {
		return ""
	}
	return strings.Join(strings.Fields(cmd.ArgumentHint()), " ")
}

// renderCommandArgumentGhost replaces only the unused cells after the active
// textarea cursor on its first visual line. The textarea remains authoritative
// for value, cursor, undo, and structured composer elements.
func renderCommandArgumentGhost(
	profile DisplayCellProfile,
	content string,
	width int,
	column int,
	hint string,
) string {
	if width <= 0 || hint == "" {
		return content
	}
	first, rest, found := strings.Cut(content, "\n")
	if column >= width {
		return content
	}
	// Use display cells, independent of cursor blink, ANSI profile, or reset
	// spelling. Keep the real cursor cell and replace only unused padding.
	prefix := profile.truncate(first, column)
	first = contentProjectLine(profile, prefix+hint, width, 0)
	if !found {
		return first
	}
	return first + "\n" + rest
}
