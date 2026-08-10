package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/tui/keybindings"
)

// helpCategory groups keybindings or commands by logical section.
type helpCategory struct {
	title   string
	entries []helpEntry
}

// helpEntry is a single row in the help overlay (key/command + description).
type helpEntry struct {
	key  string
	desc string
}

// HelpOverlay renders a modal help screen showing keybindings and commands.
// Inspired by the reference HelpV2 component with General + Commands tabs.
type HelpOverlay struct {
	visible     bool
	styles      Styles
	environment RenderEnvironment
	lines       []string // pre-rendered content lines
	offset      int      // scroll offset
	totalLines  int
	resolver    *keybindings.Resolver
	registry    *commands.Registry
	cmdCtx      *commands.CommandContext
}

// NewHelpOverlay creates a new help overlay.
func NewHelpOverlay(styles Styles, resolver *keybindings.Resolver) *HelpOverlay {
	return &HelpOverlay{
		styles:      styles,
		environment: defaultRenderEnvironment(styles),
		resolver:    resolver,
	}
}

// SetStyles restyles both the overlay chrome and pre-rendered open content.
func (h *HelpOverlay) SetStyles(styles Styles) {
	h.SetRenderEnvironment(h.environment.withStyles(styles))
}

func (h *HelpOverlay) SetRenderEnvironment(env RenderEnvironment) {
	h.environment = env.normalized()
	h.styles = h.environment.styles
	if h.visible {
		h.lines = h.buildContent(h.registry, h.cmdCtx)
		h.totalLines = len(h.lines)
	}
}

// Show makes the help overlay visible and builds its content.
func (h *HelpOverlay) Show(registry *commands.Registry) {
	h.ShowFor(registry, nil)
}

// ShowFor builds help from the same runtime capability snapshot as dispatch.
func (h *HelpOverlay) ShowFor(registry *commands.Registry, cmdCtx *commands.CommandContext) {
	h.visible = true
	h.offset = 0
	h.registry = registry
	h.cmdCtx = cmdCtx
	h.lines = h.buildContent(registry, cmdCtx)
	h.totalLines = len(h.lines)
}

// Close hides the help overlay.
func (h *HelpOverlay) Close() {
	h.visible = false
}

// Visible returns whether the overlay is currently shown.
func (h *HelpOverlay) Visible() bool {
	return h.visible
}

// HandleKey processes key events for the help overlay.
// Returns true if the overlay consumed the key (and should close on dismiss keys).
func (h *HelpOverlay) HandleKey(msg tea.KeyPressMsg, viewHeight int) (dismissed bool) {
	maxOffset := h.totalLines - viewHeight + 4 // +4 for chrome (title, footer, borders)
	if maxOffset < 0 {
		maxOffset = 0
	}

	if h.resolver != nil {
		resolution := h.resolver.ResolveEvent(msg, keybindings.ContextHelp, keybindings.ContextScroll)
		switch resolution.Kind {
		case keybindings.ResolutionChordStarted, keybindings.ResolutionChordCancelled:
			return false
		case keybindings.ResolutionMatch:
			switch resolution.Action {
			case keybindings.ActionHelpDismiss:
				h.Close()
				return true
			case keybindings.ActionScrollLineUp:
				h.offset--
			case keybindings.ActionScrollLineDown:
				h.offset++
			case keybindings.ActionScrollPageUp, keybindings.ActionScrollHalfUp:
				h.offset -= viewHeight - 2
			case keybindings.ActionScrollPageDown, keybindings.ActionScrollHalfDown:
				h.offset += viewHeight - 2
			case keybindings.ActionScrollTop:
				h.offset = 0
			case keybindings.ActionScrollBottom:
				h.offset = maxOffset
			}
			if h.offset < 0 {
				h.offset = 0
			}
			if h.offset > maxOffset {
				h.offset = maxOffset
			}
			return false
		}
	}

	switch msg.String() {
	case "q", "?":
		h.Close()
		return true
	}
	return false
}

// Overlay renders the help dialog on top of the base view.
func (h *HelpOverlay) Overlay(base string, width, height int) string {
	if !h.visible {
		return base
	}
	profile := h.environment.normalized().profile

	dialogWidth := width - 6
	if dialogWidth > 80 {
		dialogWidth = 80
	}
	if dialogWidth < 40 {
		dialogWidth = 40
	}

	// Compute visible area height
	maxContentHeight := height - 8 // leave room for title, footer, borders
	if maxContentHeight < 5 {
		maxContentHeight = 5
	}

	// Slice visible lines from offset
	end := h.offset + maxContentHeight
	if end > h.totalLines {
		end = h.totalLines
	}
	start := h.offset
	if start > h.totalLines {
		start = h.totalLines
	}
	visibleLines := h.lines[start:end]

	// Build dialog content
	var parts []string

	// Title
	title := h.styles.DialogTitle.Render("  " + identity.ProductName + " Help")
	parts = append(parts, title)
	parts = append(parts, "")

	// Content lines
	parts = append(parts, visibleLines...)

	// Scroll indicator
	scrollInfo := ""
	if h.totalLines > maxContentHeight {
		pct := 0
		if h.totalLines-maxContentHeight > 0 {
			pct = (h.offset * 100) / (h.totalLines - maxContentHeight)
		}
		scrollInfo = fmt.Sprintf(" (%d%%)", pct)
	}

	parts = append(parts, "")
	parts = append(parts, strings.Repeat("─", dialogWidth-4))
	helpText := h.styles.DialogHelp.Render("  " + h.actionKeys(
		keybindings.ContextHelp, keybindings.ActionHelpDismiss, "esc",
	) + "/q dismiss  " + h.actionKeys(
		keybindings.ContextHelp, keybindings.ActionScrollLineDown, "j",
	) + "/" + h.actionKeys(
		keybindings.ContextHelp, keybindings.ActionScrollLineUp, "k",
	) + " scroll  " + h.actionKeys(
		keybindings.ContextScroll, keybindings.ActionScrollPageUp, "PgUp",
	) + "/" + h.actionKeys(
		keybindings.ContextScroll, keybindings.ActionScrollPageDown, "PgDn",
	) + " page" + scrollInfo)
	parts = append(parts, helpText)

	dialog := contentRenderStyleWidth(
		profile,
		h.styles.DialogBorder,
		dialogWidth,
		strings.Join(parts, "\n"),
	)
	view, _ := modalCenteredOverlay(profile, base, dialog, width, height)
	return view
}

// buildContent generates the pre-rendered content lines for the help overlay.
func (h *HelpOverlay) buildContent(
	registry *commands.Registry,
	cmdCtx *commands.CommandContext,
) []string {
	var lines []string

	// Section 1: General keybindings
	categories := h.keybindingCategories()
	for _, cat := range categories {
		lines = append(lines, h.renderCategory(cat)...)
		lines = append(lines, "") // blank line between categories
	}

	// Section 2: Slash commands
	lines = append(lines, h.styles.Bold.Render("  Commands"))
	lines = append(lines, h.styles.Subtle.Render("  Type / followed by a command name"))
	lines = append(lines, "")

	if registry != nil {
		cmdList := registry.ListForContext(
			context.Background(),
			commands.EntrypointTUI,
			cmdCtx,
		)
		if len(cmdList) > 0 {
			// Find longest command name for alignment
			maxName := 0
			for _, cmd := range cmdList {
				name := "/" + cmd.Name
				if len(name) > maxName {
					maxName = len(name)
				}
			}
			if maxName > 20 {
				maxName = 20
			}

			for _, category := range commands.CommandCategoriesInDisplayOrder() {
				grouped := false
				for _, cmd := range cmdList {
					if cmd.Category != category {
						continue
					}
					if !grouped {
						lines = append(lines, h.styles.Bold.Render("  "+string(category)))
						grouped = true
					}
					name := "/" + cmd.Name
					padding := maxName - len(name) + 2
					if padding < 2 {
						padding = 2
					}
					line := "  " + h.styles.Highlight.Render(name) + strings.Repeat(" ", padding) + h.styles.Subtle.Render(cmd.Description)
					lines = append(lines, line)
				}
			}
		}
	} else {
		lines = append(lines, h.styles.Subtle.Render("  No commands registered"))
	}

	return lines
}

// keybindingCategories returns the categorized keybinding entries.
func (h *HelpOverlay) keybindingCategories() []helpCategory {
	return []helpCategory{
		{
			title: "Input",
			entries: []helpEntry{
				{key: h.actionKeys(keybindings.ContextChat, keybindings.ActionChatSubmit, "enter"), desc: "Send message"},
				{key: h.actionKeys(keybindings.ContextChat, keybindings.ActionChatNewline, "ctrl+j"), desc: "Insert newline"},
				{key: h.actionKeys(keybindings.ContextChat, keybindings.ActionChatImagePaste, "ctrl+v"), desc: "Paste image"},
				{key: "/", desc: "Enter command mode"},
				{key: "!", desc: "Enter shell mode"},
				{key: h.actionKeys(keybindings.ContextChat, keybindings.ActionChatCancel, "esc"), desc: "Cancel / clear input"},
				{key: h.actionKeys(keybindings.ContextChat, keybindings.ActionHistoryPrevious, "ctrl+p"), desc: "Previous history"},
				{key: h.actionKeys(keybindings.ContextChat, keybindings.ActionHistoryNext, "ctrl+n"), desc: "Next history"},
				{key: h.actionKeys(keybindings.ContextChat, keybindings.ActionHistorySearch, "ctrl+r"), desc: "Reverse history search"},
				{key: h.actionKeys(keybindings.ContextChat, keybindings.ActionChatExternalEditor, "ctrl+g"), desc: "Open external editor"},
				{key: h.actionKeys(keybindings.ContextChat, keybindings.ActionChatUndo, "ctrl+z"), desc: "Undo composer edit"},
			},
		},
		{
			title: "Navigation",
			entries: []helpEntry{
				{key: h.actionKeys(keybindings.ContextScroll, keybindings.ActionScrollPageUp, "PgUp") + " / " + h.actionKeys(keybindings.ContextScroll, keybindings.ActionScrollPageDown, "PgDn"), desc: "Scroll chat by page"},
				{key: h.actionKeys(keybindings.ContextGlobal, keybindings.ActionAppToggleTranscript, "ctrl+o"), desc: "Expand / verbose view"},
				{key: h.actionKeys(keybindings.ContextGlobal, keybindings.ActionAppToggleTodos, "ctrl+t"), desc: "Task panel"},
				{key: h.actionKeys(keybindings.ContextGlobal, keybindings.ActionAppGlobalSearch, "ctrl+f"), desc: "Search conversation"},
				{key: h.actionKeys(keybindings.ContextGlobal, keybindings.ActionAppQuickOpen, "ctrl+k"), desc: "Command palette"},
				{key: h.actionKeys(keybindings.ContextChat, keybindings.ActionTaskBackground, "ctrl+b"), desc: "Agent / background tasks"},
				{key: h.actionKeys(keybindings.ContextChat, keybindings.ActionChatPreviousAgent, "alt+left") + " / " + h.actionKeys(keybindings.ContextChat, keybindings.ActionChatNextAgent, "alt+right"), desc: "Switch Agent thread"},
			},
		},
		{
			title: "Mode & Control",
			entries: []helpEntry{
				{key: h.actionKeys(keybindings.ContextChat, keybindings.ActionChatCycleMode, "shift+tab"), desc: "Cycle permission mode"},
				{key: h.actionKeys(keybindings.ContextGlobal, keybindings.ActionAppInterrupt, "ctrl+c"), desc: "Interrupt / double to quit"},
				{key: h.actionKeys(keybindings.ContextGlobal, keybindings.ActionAppExit, "ctrl+d"), desc: "Quit"},
			},
		},
		{
			title: "Permission Dialog",
			entries: []helpEntry{
				{key: "up / down", desc: "Navigate options"},
				{key: "enter", desc: "Select option"},
				{key: "a / y", desc: "Allow"},
				{key: "d / n", desc: "Deny"},
				{key: "s", desc: "Allow for session"},
				{key: "A", desc: "Always allow (persist)"},
				{key: "tab", desc: "Toggle feedback input"},
				{key: "esc", desc: "Deny and dismiss"},
			},
		},
		{
			title: "Expand View (" + h.actionKeys(keybindings.ContextGlobal, keybindings.ActionAppToggleTranscript, "ctrl+o") + ")",
			entries: []helpEntry{
				{key: h.actionKeys(keybindings.ContextTranscript, keybindings.ActionScrollLineUp, "up") + " / " + h.actionKeys(keybindings.ContextTranscript, keybindings.ActionScrollLineDown, "down"), desc: "Scroll line by line"},
				{key: h.actionKeys(keybindings.ContextScroll, keybindings.ActionScrollPageUp, "PgUp") + " / " + h.actionKeys(keybindings.ContextScroll, keybindings.ActionScrollPageDown, "PgDn"), desc: "Scroll by page"},
				{key: h.actionKeys(keybindings.ContextTranscript, keybindings.ActionTranscriptSearch, "ctrl+f"), desc: "Search within expanded content"},
				{key: h.actionKeys(keybindings.ContextTranscript, keybindings.ActionTranscriptToggleRaw, "r"), desc: "Toggle raw / expanded history"},
				{key: h.actionKeys(keybindings.ContextTranscript, keybindings.ActionTranscriptExit, "esc"), desc: "Exit expand view"},
				{key: "q", desc: "Exit expand view"},
			},
		},
	}
}

func (h *HelpOverlay) actionKeys(context keybindings.Context, action keybindings.Action, fallback string) string {
	if h.resolver == nil {
		return fallback
	}
	keys := h.resolver.GetKeysForAction(context, action)
	if len(keys) == 0 {
		return "unbound"
	}
	if len(keys) > 2 {
		keys = keys[:2]
	}
	return strings.Join(keys, " / ")
}

// renderCategory renders a single category with title and entries.
func (h *HelpOverlay) renderCategory(cat helpCategory) []string {
	var lines []string
	lines = append(lines, h.styles.Bold.Render("  "+cat.title))
	profile := h.environment.normalized().profile

	// Find longest key for alignment
	maxKey := 0
	for _, e := range cat.entries {
		if width := profile.measure(e.key, 4); width > maxKey {
			maxKey = width
		}
	}
	if maxKey > 18 {
		maxKey = 18
	}

	keyStyle := h.styles.DialogTitle

	for _, e := range cat.entries {
		padding := maxKey - profile.measure(e.key, 4) + 2
		if padding < 2 {
			padding = 2
		}
		line := "    " + keyStyle.Render(e.key) + strings.Repeat(" ", padding) + h.styles.Subtle.Render(e.desc)
		lines = append(lines, line)
	}

	return lines
}
