package tui

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/schema"
)

type ResumeDialog struct {
	visible           bool
	loading           bool
	loadingMore       bool
	err               string
	query             string
	selected          int
	selectionKey      string
	sessions          []session.SessionInfo
	filtered          []session.SessionInfo
	seen              map[string]int
	currentID         string
	styles            Styles
	environment       RenderEnvironment
	geometry          modalFrameGeometry
	branchFilter      bool   // Ctrl+B toggle: filter to current git branch
	currentBranch     string // cached current git branch name
	scope             session.SessionScope
	sort              session.SortOrder
	nextCursor        string
	hasMore           bool
	generation        uint64
	paged             bool
	mode              sessionPickerMode
	confirmLegacyKey  string
	previews          map[string]sessionPreviewState
	transcriptLoading bool
}

func NewResumeDialog(styles Styles) *ResumeDialog {
	return &ResumeDialog{
		styles:      styles,
		environment: defaultRenderEnvironment(styles),
	}
}

func (d *ResumeDialog) SetStyles(styles Styles) {
	d.SetRenderEnvironment(d.environment.withStyles(styles))
}

func (d *ResumeDialog) SetRenderEnvironment(env RenderEnvironment) {
	d.environment = env.normalized()
	d.styles = d.environment.styles
}

func (d *ResumeDialog) Show(currentID string) {
	d.visible, d.loading = true, true
	d.err, d.query, d.currentID = "", "", currentID
	d.selected, d.sessions, d.filtered = 0, nil, nil
	d.selectionKey, d.nextCursor = "", ""
	d.seen = make(map[string]int)
	d.loadingMore, d.hasMore = false, false
	d.branchFilter = false
	d.currentBranch = detectGitBranch()
	d.scope = session.SessionScopeRepository
	d.sort = session.SortNewestFirst
	d.mode = sessionPickerResume
	d.confirmLegacyKey = ""
	d.previews = make(map[string]sessionPreviewState)
	d.transcriptLoading = false
}

func (d *ResumeDialog) SetSessions(sessions []session.SessionInfo, err error) {
	d.loading = false
	d.paged = false
	if err != nil {
		d.err = err.Error()
		return
	}
	d.sessions = append([]session.SessionInfo(nil), sessions...)
	d.applyFilter()
}

func (d *ResumeDialog) beginPage(reset bool) (session.SessionQuery, uint64) {
	d.paged = true
	if reset {
		d.generation++
		d.loading = true
		d.loadingMore = false
		d.err = ""
		d.sessions = nil
		d.filtered = nil
		d.seen = make(map[string]int)
		d.nextCursor = ""
		d.hasMore = false
	} else {
		if d.loading || d.loadingMore || !d.hasMore || d.nextCursor == "" {
			return session.SessionQuery{}, 0
		}
		d.loadingMore = true
	}
	filter := session.ListFilter{Search: strings.TrimSpace(d.query)}
	if d.branchFilter && d.currentBranch != "" {
		filter.GitBranch = d.currentBranch
	}
	return session.SessionQuery{
		Scope:  d.scope,
		Sort:   d.sort,
		Filter: filter,
		Limit:  25,
		Cursor: d.nextCursor,
	}, d.generation
}

func (d *ResumeDialog) SetPage(page *session.SessionPage, generation uint64, reset bool, err error) {
	if generation != d.generation {
		return
	}
	d.loading, d.loadingMore = false, false
	if err != nil {
		d.err = err.Error()
		return
	}
	d.err = ""
	if reset {
		d.sessions = nil
		d.filtered = nil
		d.seen = make(map[string]int)
	}
	if page == nil {
		d.nextCursor, d.hasMore = "", false
		d.applyFilter()
		return
	}
	for _, info := range page.Sessions {
		key := info.StableKey()
		if index, ok := d.seen[key]; ok {
			d.sessions[index] = info
			continue
		}
		d.seen[key] = len(d.sessions)
		d.sessions = append(d.sessions, info)
	}
	d.nextCursor, d.hasMore = page.NextCursor, page.HasMore
	d.applyFilter()
	if d.selectionKey != "" {
		for index := range d.filtered {
			if d.filtered[index].StableKey() == d.selectionKey {
				d.selected = index
				break
			}
		}
	}
}

func (d *ResumeDialog) Close() { d.visible = false }

func (d *ResumeDialog) handleKey(msg tea.KeyPressMsg) (sessionPickerSelection, bool, tea.Cmd) {
	if d.confirmLegacyKey != "" {
		switch msg.String() {
		case "y", "Y":
			info, ok := d.confirmedLegacyInfo()
			d.confirmLegacyKey = ""
			if !ok {
				return sessionPickerSelection{}, false, nil
			}
			d.Close()
			return sessionPickerSelection{
				Mode:                 sessionPickerResume,
				Info:                 info,
				ConfirmLegacyStopped: true,
			}, true, nil
		case "n", "N", "esc", "ctrl+c":
			d.confirmLegacyKey = ""
			return sessionPickerSelection{}, false, nil
		default:
			return sessionPickerSelection{}, false, nil
		}
	}
	switch msg.String() {
	case "esc", "ctrl+c":
		d.Close()
		return sessionPickerSelection{}, true, nil
	}
	if d.transcriptLoading {
		return sessionPickerSelection{}, false, nil
	}
	var command tea.Cmd
	switch msg.String() {
	case "up", "k":
		if len(d.filtered) > 0 {
			d.selected--
			if d.selected < 0 {
				d.selected = len(d.filtered) - 1
			}
			d.captureSelection()
		}
		command = d.previewRequest()
	case "down", "j":
		if len(d.filtered) > 0 {
			d.selected = (d.selected + 1) % len(d.filtered)
			d.captureSelection()
		}
		command = tea.Batch(d.maybeLoadMore(), d.previewRequest())
	case "pgup":
		d.move(-8)
		command = d.previewRequest()
	case "pgdown":
		d.move(8)
		command = tea.Batch(d.maybeLoadMore(), d.previewRequest())
	case "ctrl+f":
		if d.mode == sessionPickerResume {
			d.mode = sessionPickerFork
		} else {
			d.mode = sessionPickerResume
		}
	case "ctrl+t":
		command = d.transcriptRequest()
	case "tab":
		d.scope = nextSessionScope(d.scope)
		command = d.resetQueryAfter(0)
	case "ctrl+s":
		d.sort = nextSessionSort(d.sort)
		command = d.resetQueryAfter(0)
	case "ctrl+b":
		if d.currentBranch != "" {
			d.branchFilter = !d.branchFilter
			if d.paged {
				command = d.resetQueryAfter(0)
			} else {
				d.applyFilter()
			}
		}
	case "enter":
		if d.selected >= 0 && d.selected < len(d.filtered) {
			info := d.filtered[d.selected]
			if d.mode == sessionPickerResume && (info.ReadOnly || info.NeedsImport) {
				d.confirmLegacyKey = info.StableKey()
				return sessionPickerSelection{}, false, nil
			}
			selection := sessionPickerSelection{Mode: d.mode, Info: info}
			d.Close()
			return selection, true, nil
		}
	case "backspace":
		if d.query != "" {
			runes := []rune(d.query)
			d.query = string(runes[:len(runes)-1])
			if d.paged {
				command = d.resetQueryAfter(120 * time.Millisecond)
			} else {
				d.applyFilter()
			}
		}
	default:
		if msg.Text != "" {
			d.query += msg.Text
			if d.paged {
				command = d.resetQueryAfter(120 * time.Millisecond)
			} else {
				d.applyFilter()
			}
		}
	}
	return sessionPickerSelection{}, false, command
}

func (d *ResumeDialog) confirmedLegacyInfo() (session.SessionInfo, bool) {
	for _, info := range d.filtered {
		if info.StableKey() == d.confirmLegacyKey &&
			(info.ReadOnly || info.NeedsImport) {
			return info, true
		}
	}
	return session.SessionInfo{}, false
}

func (d *ResumeDialog) move(delta int) {
	if len(d.filtered) == 0 {
		return
	}
	d.selected += delta
	if d.selected < 0 {
		d.selected = 0
	} else if d.selected >= len(d.filtered) {
		d.selected = len(d.filtered) - 1
	}
	d.captureSelection()
}

func (d *ResumeDialog) captureSelection() {
	if d.selected >= 0 && d.selected < len(d.filtered) {
		d.selectionKey = d.filtered[d.selected].StableKey()
	}
}

func (d *ResumeDialog) resetQueryAfter(delay time.Duration) tea.Cmd {
	query, generation := d.beginPage(true)
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return resumeSessionPageRequestMsg{query: query, generation: generation, reset: true}
	})
}

func (d *ResumeDialog) maybeLoadMore() tea.Cmd {
	if len(d.filtered) == 0 || d.selected < len(d.filtered)-3 {
		return nil
	}
	query, generation := d.beginPage(false)
	if generation == 0 {
		return nil
	}
	return func() tea.Msg {
		return resumeSessionPageRequestMsg{query: query, generation: generation}
	}
}

func (d *ResumeDialog) previewRequest() tea.Cmd {
	if d.selected < 0 || d.selected >= len(d.filtered) {
		return nil
	}
	info := d.filtered[d.selected]
	key := info.StableKey()
	if key == "" {
		return nil
	}
	if _, exists := d.previews[key]; exists {
		return nil
	}
	d.previews[key] = sessionPreviewState{loading: true}
	generation := d.generation
	return func() tea.Msg {
		return resumeSessionPreviewRequestMsg{info: info, key: key, generation: generation}
	}
}

func (d *ResumeDialog) SetPreview(key string, generation uint64, result *transcript.RecentResult, err error) {
	if generation != d.generation || key == "" {
		return
	}
	state := sessionPreviewState{}
	if err != nil {
		state.err = err.Error()
	} else if result != nil {
		state.messages = append([]*schema.Message(nil), result.Messages...)
		state.truncated = result.Truncated
		state.corruptions = result.Corruptions
	}
	d.previews[key] = state
}

func (d *ResumeDialog) transcriptRequest() tea.Cmd {
	if d.selected < 0 || d.selected >= len(d.filtered) {
		return nil
	}
	info := d.filtered[d.selected]
	key := info.StableKey()
	if key == "" {
		return nil
	}
	d.transcriptLoading = true
	generation := d.generation
	return func() tea.Msg {
		return resumeSessionTranscriptRequestMsg{info: info, key: key, generation: generation}
	}
}

func nextSessionScope(scope session.SessionScope) session.SessionScope {
	switch scope {
	case session.SessionScopeCWD:
		return session.SessionScopeRepository
	case session.SessionScopeRepository:
		return session.SessionScopeAll
	default:
		return session.SessionScopeCWD
	}
}

func nextSessionSort(order session.SortOrder) session.SortOrder {
	switch order {
	case session.SortNewestFirst:
		return session.SortOldestFirst
	case session.SortOldestFirst:
		return session.SortMostMessages
	default:
		return session.SortNewestFirst
	}
}

func (d *ResumeDialog) applyFilter() {
	query := strings.ToLower(strings.TrimSpace(d.query))
	d.filtered = d.filtered[:0]
	for _, info := range d.sessions {
		if info.SessionID == d.currentID {
			continue
		}
		// Branch filter: only show sessions matching current git branch
		if d.branchFilter && d.currentBranch != "" {
			if info.GitBranch != d.currentBranch {
				continue
			}
		}
		haystack := strings.ToLower(strings.Join([]string{info.SessionID, info.Summary, info.CustomTitle, info.FirstPrompt, info.GitBranch, info.CWD, info.Tag, info.BranchName, info.ParentSessionID}, " "))
		if query == "" || strings.Contains(haystack, query) {
			d.filtered = append(d.filtered, info)
		}
	}
	if len(d.filtered) == 0 {
		d.selected = 0
	} else if d.selected >= len(d.filtered) {
		d.selected = len(d.filtered) - 1
	}
}

func (d *ResumeDialog) Overlay(base string, width, height int) string {
	d.geometry = modalFrameGeometry{}
	if !d.visible {
		return base
	}
	profile := d.environment.normalized().profile
	dialogWidth := width - 10
	if dialogWidth > 90 {
		dialogWidth = 90
	}
	if dialogWidth < 48 {
		dialogWidth = 48
	}
	maxRows := height - 12
	if maxRows > 12 {
		maxRows = 12
	}
	if maxRows < 3 {
		maxRows = 3
	}
	// Each entry may take 2 lines (summary + preview), halve visible count
	maxEntries := maxRows / 2
	if maxEntries < 3 {
		maxEntries = 3
	}
	lines := []string{d.renderDialogTitle(), d.styles.Subtle.Render("Search: ") + d.query + "█", strings.Repeat("─", dialogWidth-4)}
	if d.confirmLegacyKey != "" {
		info, ok := d.confirmedLegacyInfo()
		if !ok {
			d.confirmLegacyKey = ""
			lines = append(lines, d.styles.Error.Render("The selected legacy session is no longer available."))
		} else {
			lines = append(
				lines,
				d.styles.Warning.Render("Legacy session import requires explicit confirmation."),
				"Session: "+info.SessionID,
				"Confirm that the archived producer is stopped.",
				"YHC will copy one stable bundle and will not merge later legacy writes.",
			)
		}
	} else if d.transcriptLoading {
		lines = append(lines, "Loading full transcript...")
	} else if d.loading {
		lines = append(lines, "Loading saved sessions...")
	} else if d.err != "" {
		lines = append(lines, d.styles.Error.Render("Unable to list sessions: "+d.err))
	} else if len(d.filtered) == 0 {
		lines = append(lines, d.styles.Subtle.Render("No matching saved sessions."))
	} else {
		start := d.selected - maxEntries/2
		if start < 0 {
			start = 0
		}
		end := start + maxEntries
		if end > len(d.filtered) {
			end = len(d.filtered)
			start = end - maxEntries
			if start < 0 {
				start = 0
			}
		}
		for i := start; i < end; i++ {
			info := d.filtered[i]
			summary := strings.Join(strings.Fields(info.Summary), " ")
			if summary == "" {
				summary = info.SessionID
			}
			if info.ReadOnly || info.NeedsImport {
				summary += " [import required]"
			}
			age := formatRelativeTime(info.LastModified, time.Now())
			// Branch indicator: show tree prefix for branched sessions
			branchPrefix := ""
			if info.ParentSessionID != "" {
				branchPrefix = "↳ "
				if info.BranchName != "" {
					branchPrefix = "↳ [" + info.BranchName + "] "
				}
			}
			// Git branch indicator suffix
			branchTag := ""
			if info.GitBranch != "" && !d.branchFilter {
				branchTag = " ⎇ " + info.GitBranch
			}
			branchWidth := profile.measure(branchPrefix, 2)
			ageWidth := profile.measure(age, 0)
			available := 12
			for candidate := max(12, dialogWidth); candidate >= 12; candidate-- {
				branchTagWidth := profile.measure(
					branchTag,
					2+branchWidth+candidate,
				)
				if branchWidth+candidate+branchTagWidth+ageWidth+9 <= dialogWidth {
					available = candidate
					break
				}
			}
			summaryStart := 2 + branchWidth
			summary = modalEllipsize(
				profile,
				summary,
				available,
				summaryStart,
				"…",
			)
			summary = profile.padAligned(summary, available, "left", summaryStart)
			line := fmt.Sprintf(
				"  %s%s%s  %s",
				branchPrefix,
				summary,
				d.styles.Subtle.Render(branchTag),
				age,
			)
			if i == d.selected {
				line = d.styles.Selected.Render("> " + strings.TrimPrefix(line, "  "))
			}
			lines = append(lines, line)
			// Show first prompt preview as dim second line
			if info.FirstPrompt != "" {
				preview := strings.Join(strings.Fields(info.FirstPrompt), " ")
				previewAvail := available + branchWidth - 2
				if previewAvail < 12 {
					previewAvail = 12
				}
				preview = modalEllipsize(profile, preview, previewAvail, 4, "…")
				previewLine := "    " + d.styles.Subtle.Render(preview)
				lines = append(lines, previewLine)
			}
		}
		if d.loadingMore {
			lines = append(lines, d.styles.Subtle.Render("  Loading more sessions..."))
		}
	}
	helpText := "↑/↓ navigate  type filter  Tab scope  Ctrl+F mode  Ctrl+T transcript  Enter"
	if d.confirmLegacyKey != "" {
		helpText = "Press Y to import and resume  N/Esc to cancel"
	}
	// Session detail panel: show metadata for the focused session
	if d.confirmLegacyKey == "" && !d.loading && d.err == "" && d.selected >= 0 && d.selected < len(d.filtered) {
		lines = append(lines, strings.Repeat("─", dialogWidth-4))
		detailLines := d.renderSessionDetail(d.filtered[d.selected], dialogWidth)
		lines = append(lines, detailLines...)
		key := d.filtered[d.selected].StableKey()
		if preview, ok := d.previews[key]; ok {
			lines = append(lines, d.renderModalSessionPreviewLines(preview, dialogWidth)...)
		}
	}
	lines = append(lines, strings.Repeat("─", dialogWidth-4), d.styles.DialogHelp.Render(helpText))
	dialog := contentRenderStyleWidth(
		profile,
		d.styles.DialogBorder,
		dialogWidth,
		strings.Join(lines, "\n"),
	)
	view, geometry := modalCenteredOverlay(profile, base, dialog, width, height)
	d.geometry = geometry
	return view
}

func formatRelativeTime(then, now time.Time) string {
	if then.IsZero() {
		return "unknown"
	}
	delta := now.Sub(then)
	if delta < 0 {
		delta = 0
	}
	switch {
	case delta < time.Minute:
		return "just now"
	case delta < time.Hour:
		return fmt.Sprintf("%dm ago", int(delta.Minutes()))
	case delta < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(delta.Hours()))
	case delta < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(delta.Hours()/24))
	default:
		return then.Format("2006-01-02")
	}
}

// renderDialogTitle shows the title with an optional branch filter indicator.
func (d *ResumeDialog) renderDialogTitle() string {
	action := "Resume"
	if d.mode == sessionPickerFork {
		action = "Fork"
	}
	title := d.styles.DialogTitle.Render(action+" a session") + "  " +
		d.styles.Subtle.Render(string(d.scope)+" · "+sessionSortLabel(d.sort))
	if d.branchFilter && d.currentBranch != "" {
		title += "  " + d.styles.Subtle.Render("⎇ "+d.currentBranch)
	}
	return title
}

func sessionSortLabel(order session.SortOrder) string {
	switch order {
	case session.SortOldestFirst:
		return "oldest"
	case session.SortMostMessages:
		return "largest"
	default:
		return "recent"
	}
}

// renderSessionDetail renders a compact detail panel for the focused session.
func (d *ResumeDialog) renderSessionDetail(info session.SessionInfo, _ int) []string {
	profile := d.environment.normalized().profile
	var details []string

	// Row 1: session ID (truncated) + created time
	idStr := modalEllipsize(profile, info.SessionID, 12, 6, "…")
	var row1Parts []string
	row1Parts = append(row1Parts, d.styles.Subtle.Render("ID: ")+idStr)
	if !info.CreatedAt.IsZero() {
		row1Parts = append(row1Parts, d.styles.Subtle.Render("Created: ")+info.CreatedAt.Format("2006-01-02 15:04"))
	}
	// Duration (if we have both created and last modified)
	if !info.CreatedAt.IsZero() && !info.LastModified.IsZero() {
		dur := info.LastModified.Sub(info.CreatedAt)
		if dur > 0 {
			row1Parts = append(row1Parts, d.styles.Subtle.Render("Duration: ")+formatDuration(dur))
		}
	}
	details = append(details, "  "+strings.Join(row1Parts, "  "))

	// Row 2: branch + CWD + tag
	var row2Parts []string
	if info.GitBranch != "" {
		row2Parts = append(row2Parts, d.styles.Subtle.Render("⎇ ")+info.GitBranch)
	}
	if info.CWD != "" {
		cwdStart := 2
		if len(row2Parts) > 0 {
			cwdStart += profile.measure(strings.Join(row2Parts, "  "), cwdStart) + 2
		}
		label := d.styles.Subtle.Render("Dir: ")
		cwdStart += profile.measure(label, cwdStart)
		cwd := modalTailEllipsize(profile, info.CWD, 30, cwdStart, "…")
		row2Parts = append(row2Parts, d.styles.Subtle.Render("Dir: ")+cwd)
	}
	if info.Tag != "" {
		row2Parts = append(row2Parts, d.styles.Subtle.Render("#")+info.Tag)
	}
	if info.Model != "" {
		model := info.Model
		if info.Provider != "" {
			model = info.Provider + ":" + model
		}
		row2Parts = append(row2Parts, d.styles.Subtle.Render("Model: ")+model)
	}
	if len(row2Parts) > 0 {
		details = append(details, "  "+strings.Join(row2Parts, "  "))
	}

	var lineage []string
	appendLineage := func(label, value string, limit int) {
		start := 2
		if len(lineage) > 0 {
			start += profile.measure(strings.Join(lineage, "  "), start) + 2
		}
		valueStart := start + profile.measure(label, start)
		lineage = append(
			lineage,
			label+modalEllipsize(profile, value, limit, valueStart, "…"),
		)
	}
	if info.ParentSessionID != "" {
		appendLineage("parent ", info.ParentSessionID, 12)
	}
	if info.AgentID != "" {
		agent := firstNonEmptyString(info.AgentName, info.AgentRole, info.AgentID)
		appendLineage("agent ", agent, 20)
	}
	if info.Status != "" {
		lineage = append(lineage, "status "+info.Status)
	}
	if info.WorktreeBranch != "" {
		lineage = append(lineage, "worktree "+info.WorktreeBranch)
	} else if info.WorktreePath != "" {
		lineage = append(lineage, "worktree "+info.WorktreePath)
	}
	if len(lineage) > 0 {
		details = append(details, "  "+d.styles.Subtle.Render(strings.Join(lineage, "  ")))
	}

	// Row 3: file size as rough message count indicator
	if info.FileSize > 0 {
		// Rough estimate: ~500 bytes per message entry average
		msgEstimate := info.FileSize / 500
		if msgEstimate < 1 {
			msgEstimate = 1
		}
		details = append(details, "  "+d.styles.Subtle.Render(fmt.Sprintf("~%d messages  (%s)", msgEstimate, formatFileSize(info.FileSize))))
	}

	return details
}

func (d *ResumeDialog) renderModalSessionPreviewLines(
	state sessionPreviewState,
	width int,
) []string {
	switch {
	case state.loading:
		return []string{"  Loading recent transcript..."}
	case state.err != "":
		return []string{"  Preview unavailable: " + state.err}
	case len(state.messages) == 0:
		return []string{"  No transcript preview available."}
	}
	profile := d.environment.normalized().profile
	limit := width - 12
	if limit < 20 {
		limit = 20
	}
	lines := make([]string, 0, len(state.messages)+1)
	for _, message := range state.messages {
		if message == nil {
			continue
		}
		label, content := sessionMessagePreview(message)
		content = strings.Join(strings.Fields(sanitizeGenericHistoryText(content)), " ")
		if content == "" {
			continue
		}
		label = modalEllipsize(profile, label, 9, 2, "…")
		label = profile.padAligned(label, 9, "left", 2)
		content = modalEllipsize(profile, content, limit, 12, "…")
		lines = append(lines, "  "+label+" "+content)
	}
	if state.truncated {
		lines = append(lines, "  ... recent tail")
	}
	return lines
}

// formatDuration formats a duration as a human-readable string.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) - hours*60
	if hours < 24 {
		if mins > 0 {
			return fmt.Sprintf("%dh%dm", hours, mins)
		}
		return fmt.Sprintf("%dh", hours)
	}
	days := hours / 24
	return fmt.Sprintf("%dd%dh", days, hours-days*24)
}

// formatFileSize formats bytes into a human-readable size.
func formatFileSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
}

// detectGitBranch returns the current git branch name, or empty string on failure.
func detectGitBranch() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "branch", "--show-current")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
