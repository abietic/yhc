package tui

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/aymanbagabas/go-udiff"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/filehistory"
)

// ChangeReviewAction is an intent only. The App validates identity and owns any
// draft mutation or stage operation after receiving it.
type ChangeReviewAction uint8

const (
	ChangeReviewNone ChangeReviewAction = iota
	ChangeReviewClose
	ChangeReviewStage
)

type ChangeReviewSelection struct {
	TurnID, RevisionID string
	HunkIndexes        []int
	WholeFile          bool
}

const (
	changeReviewMaxBytes        = 2 << 20
	changeReviewMaxLines        = 20000
	changeReviewMaxPreviewBytes = 64 << 10
)

type changeReviewRowKind uint8

const (
	changeReviewTurnRow changeReviewRowKind = iota
	changeReviewFileRow
	changeReviewGapRow
)

type changeReviewRow struct {
	kind      changeReviewRowKind
	turn      filehistory.TurnRecord
	header    filehistory.RevisionHeader
	collapsed bool
}

// ChangeReviewPanel is a bounded projection of captured Write/Edit history.
// It deliberately never inspects Git or the live workspace.
type ChangeReviewPanel struct {
	styles      Styles
	environment RenderEnvironment
	visible     bool
	snapshot    engine.WorkspaceChangeSnapshot
	load        func(string) (filehistory.RevisionRecord, bool)
	rows        []changeReviewRow
	selected    int
	preview     *filehistory.RevisionRecord
	hunks       []string
	marked      map[int]bool
	hunkCursor  int
	lineOffset  int
	listTop     int
	fallback    bool
	feedback    string
	cache       map[changeReviewCacheKey][]string
}

type changeReviewCacheKey struct {
	id     string
	width  int
	env    renderEnvironmentIdentity
	cursor int
}

func NewChangeReviewPanel(styles Styles) *ChangeReviewPanel {
	return &ChangeReviewPanel{styles: styles, environment: defaultRenderEnvironment(styles), marked: map[int]bool{}, cache: map[changeReviewCacheKey][]string{}}
}

func (p *ChangeReviewPanel) SetRenderEnvironment(env RenderEnvironment) {
	prior := p.environment.identity()
	p.environment = env.normalized()
	p.styles = p.environment.styles
	if prior != p.environment.identity() {
		p.cache = map[changeReviewCacheKey][]string{}
	}
}

func (p *ChangeReviewPanel) Show(snapshot engine.WorkspaceChangeSnapshot, load func(string) (filehistory.RevisionRecord, bool)) {
	p.visible, p.load = true, load
	p.Refresh(snapshot)
}

func (p *ChangeReviewPanel) Refresh(snapshot engine.WorkspaceChangeSnapshot) {
	previous := p.snapshot
	selectedKey := ""
	collapsed := make(map[string]bool)
	if p.selected >= 0 && p.selected < len(p.rows) {
		selectedKey = changeReviewRowKey(p.rows[p.selected])
	}
	for _, row := range p.rows {
		if row.kind == changeReviewTurnRow {
			collapsed[row.turn.TurnID] = row.collapsed
		}
	}
	sameOwner := previous.SessionID == snapshot.SessionID && previous.ThreadID == snapshot.ThreadID && previous.OwnerGeneration == snapshot.OwnerGeneration
	p.snapshot = snapshot
	priorID, priorCursor, priorOffset := "", p.hunkCursor, p.lineOffset
	if p.preview != nil {
		priorID = p.preview.ID
	}
	p.rows = changeReviewRows(snapshot)
	if sameOwner {
		for i := range p.rows {
			row := &p.rows[i]
			if row.kind == changeReviewTurnRow {
				row.collapsed = collapsed[row.turn.TurnID]
			}
			if changeReviewRowKey(*row) == selectedKey {
				p.selected = i
			}
		}
	} else {
		p.selected, p.listTop = 0, 0
		p.cache = map[changeReviewCacheKey][]string{}
	}
	if priorID != "" && p.preview != nil && previous.SessionID == snapshot.SessionID && previous.ThreadID == snapshot.ThreadID && previous.OwnerGeneration == snapshot.OwnerGeneration && changeReviewSnapshotHas(snapshot, priorID) {
		p.hunkCursor, p.lineOffset = priorCursor, priorOffset
	} else {
		p.preview, p.hunks, p.marked, p.hunkCursor, p.lineOffset, p.fallback = nil, nil, map[int]bool{}, 0, 0, false
	}
	p.selected = p.clampVisible(p.selected)
}

func changeReviewSnapshotHas(snapshot engine.WorkspaceChangeSnapshot, id string) bool {
	for _, header := range snapshot.Records {
		if header.ID == id {
			return true
		}
	}
	return false
}

func (p *ChangeReviewPanel) SetFeedback(feedback string) { p.feedback = feedback }
func (p *ChangeReviewPanel) Close() {
	*p = ChangeReviewPanel{styles: p.styles, environment: p.environment, marked: map[int]bool{}, cache: map[changeReviewCacheKey][]string{}}
}
func (p *ChangeReviewPanel) IsVisible() bool { return p.visible }

func changeReviewRowKey(row changeReviewRow) string {
	if row.kind == changeReviewTurnRow {
		return "turn:" + row.turn.TurnID
	}
	return "record:" + row.header.ID
}

func changeReviewRows(snapshot engine.WorkspaceChangeSnapshot) []changeReviewRow {
	turns := make(map[string]filehistory.TurnRecord, len(snapshot.Turns))
	for _, turn := range snapshot.Turns {
		turns[turn.TurnID] = turn
	}
	used := make(map[string]bool, len(turns))
	byTurn := make(map[string][]filehistory.RevisionHeader)
	for _, record := range snapshot.Records {
		byTurn[record.TurnID] = append(byTurn[record.TurnID], record)
	}
	rows := make([]changeReviewRow, 0, len(snapshot.Turns)+len(snapshot.Records))
	// Records are journal ordered; walking backward gives a newest-first timeline.
	for i := len(snapshot.Turns) - 1; i >= 0; i-- {
		turn := snapshot.Turns[i]
		used[turn.TurnID] = true
		rows = append(rows, changeReviewRow{kind: changeReviewTurnRow, turn: turn})
		entries := byTurn[turn.TurnID]
		for j := len(entries) - 1; j >= 0; j-- {
			kind := changeReviewFileRow
			if !entries[j].BeforeCaptured || !entries[j].AfterCaptured {
				kind = changeReviewGapRow
			}
			rows = append(rows, changeReviewRow{kind: kind, turn: turn, header: entries[j]})
		}
	}
	// An orphan record is explicitly represented rather than assigned a made-up turn state.
	for i := len(snapshot.Records) - 1; i >= 0; i-- {
		header := snapshot.Records[i]
		if used[header.TurnID] {
			continue
		}
		kind := changeReviewFileRow
		if !header.BeforeCaptured || !header.AfterCaptured {
			kind = changeReviewGapRow
		}
		rows = append(rows, changeReviewRow{kind: kind, header: header})
	}
	return rows
}

func (p *ChangeReviewPanel) HandleKey(msg tea.KeyPressMsg) (ChangeReviewAction, ChangeReviewSelection) {
	if !p.visible {
		return ChangeReviewNone, ChangeReviewSelection{}
	}
	key := msg.String()
	if p.preview != nil {
		switch key {
		case "esc":
			p.preview, p.hunks, p.marked, p.fallback = nil, nil, map[int]bool{}, false
			return ChangeReviewNone, ChangeReviewSelection{}
		case "tab":
			if len(p.hunks) > 0 {
				p.hunkCursor = (p.hunkCursor + 1) % len(p.hunks)
				p.lineOffset = 0
			}
		case " ", "space":
			if len(p.hunks) > 0 {
				p.marked[p.hunkCursor] = !p.marked[p.hunkCursor]
			}
		case "a":
			selection := ChangeReviewSelection{TurnID: p.preview.TurnID, RevisionID: p.preview.ID}
			for i := range p.hunks {
				if p.marked[i] {
					selection.HunkIndexes = append(selection.HunkIndexes, i)
				}
			}
			selection.WholeFile = len(selection.HunkIndexes) == 0
			return ChangeReviewStage, selection
		case "up", "k":
			p.lineOffset = max(0, p.lineOffset-1)
		case "down", "j":
			p.lineOffset++
		case "pgup":
			p.lineOffset = max(0, p.lineOffset-5)
		case "pgdown":
			p.lineOffset += 5
		case "home":
			p.lineOffset = 0
		case "end":
			p.lineOffset = 1 << 30
		}
		return ChangeReviewNone, ChangeReviewSelection{}
	}
	switch key {
	case "esc":
		p.Close()
		return ChangeReviewClose, ChangeReviewSelection{}
	case "up", "k":
		p.selected = p.visibleMove(-1)
	case "down", "j":
		p.selected = p.visibleMove(1)
	case "pgup":
		for i := 0; i < 5; i++ {
			p.selected = p.visibleMove(-1)
		}
	case "pgdown":
		for i := 0; i < 5; i++ {
			p.selected = p.visibleMove(1)
		}
	case "home":
		if visible := p.visibleRows(); len(visible) > 0 {
			p.selected = visible[0]
		}
	case "end":
		if visible := p.visibleRows(); len(visible) > 0 {
			p.selected = visible[len(visible)-1]
		}
	case "enter":
		p.openSelected()
	}
	return ChangeReviewNone, ChangeReviewSelection{}
}

func (p *ChangeReviewPanel) openSelected() {
	if p.selected < 0 || p.selected >= len(p.rows) {
		return
	}
	row := &p.rows[p.selected]
	if row.kind == changeReviewTurnRow {
		row.collapsed = !row.collapsed
		return
	}
	if row.kind != changeReviewFileRow || p.load == nil || !changeReviewTerminal(row.header.Outcome) {
		return
	}
	record, ok := p.load(row.header.ID)
	if !ok || !record.Before.Captured || !record.After.Captured {
		return
	}
	hunks, err := changeReviewHunks(record)
	if err != nil {
		if strings.Contains(err.Error(), "diff limit") || strings.Contains(err.Error(), "not UTF-8") {
			p.preview, p.hunks, p.fallback, p.lineOffset = &record, nil, true, 0
			return
		}
		p.feedback = "Captured change cannot be reviewed: " + err.Error()
		return
	}
	p.preview, p.hunks, p.marked, p.hunkCursor, p.lineOffset, p.fallback = &record, hunks, map[int]bool{}, 0, 0, false
}

func changeReviewTerminal(outcome string) bool {
	switch outcome {
	case "succeeded", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// changeReviewHunks returns raw unified hunk chunks. It has a strict input
// budget because udiff's bounded LCS is still unsuitable for arbitrary captures.
func changeReviewHunks(record filehistory.RevisionRecord) ([]string, error) {
	if !record.Before.Captured || !record.After.Captured {
		return nil, fmt.Errorf("before/after capture is incomplete")
	}
	if !changeReviewTerminal(record.Outcome) {
		return nil, fmt.Errorf("tool outcome is still pending")
	}
	before, after := string(record.Before.Data), string(record.After.Data)
	if len(before)+len(after) > changeReviewMaxBytes || strings.Count(before, "\n")+strings.Count(after, "\n")+2 > changeReviewMaxLines {
		return nil, fmt.Errorf("capture exceeds diff limit; stage whole file only")
	}
	if !utf8.ValidString(before) || !utf8.ValidString(after) {
		return nil, fmt.Errorf("capture is not UTF-8; stage whole file only")
	}
	path := changeReviewSafeLabel(record.Path)
	text, err := udiff.ToUnified("a/"+path, "b/"+path, before, udiff.Lines(before, after), 3)
	if err != nil {
		return nil, fmt.Errorf("build captured diff: %w", err)
	}
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	first := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "@@ ") {
			first = i
			break
		}
	}
	if first < 0 {
		return nil, nil
	}
	head := append([]string(nil), lines[:first]...)
	if record.Before.Mode != record.After.Mode {
		head = append(head, fmt.Sprintf("mode %04o -> %04o", record.Before.Mode, record.After.Mode))
	}
	var chunks []string
	for i := first; i < len(lines); {
		end := i + 1
		for end < len(lines) && !strings.HasPrefix(lines[end], "@@ ") {
			end++
		}
		chunks = append(chunks, strings.Join(append(append([]string(nil), head...), lines[i:end]...), "\n")+"\n")
		i = end
	}
	return chunks, nil
}

// changeReviewContext emits captured bytes only. Whole-file context is bounded
// literal data for oversized/non-text captures, never a reconstructed diff.
func changeReviewContext(record filehistory.RevisionRecord, selection ChangeReviewSelection) (string, error) {
	if selection.TurnID != "" && selection.TurnID != record.TurnID {
		return "", fmt.Errorf("turn selection does not match record")
	}
	if selection.RevisionID != "" && selection.RevisionID != record.ID {
		return "", fmt.Errorf("revision selection does not match record")
	}
	if !record.Before.Captured || !record.After.Captured || !changeReviewTerminal(record.Outcome) {
		return "", fmt.Errorf("captured before/after terminal result required")
	}
	if selection.WholeFile || len(selection.HunkIndexes) == 0 {
		if len(record.Before.Data)+len(record.After.Data) > changeReviewMaxBytes {
			return "", fmt.Errorf("captured whole-file context exceeds limit")
		}
		return changeReviewLiteralState("before", record.Path, record.Before) + "\n--- raw before ---\n" + string(record.Before.Data) + "\n--- raw after ---\n" + changeReviewLiteralState("after", record.Path, record.After) + "\n" + string(record.After.Data), nil
	}
	hunks, err := changeReviewHunks(record)
	if err != nil {
		return "", err
	}
	var chosen bytes.Buffer
	seen := make(map[int]bool)
	for _, index := range selection.HunkIndexes {
		if seen[index] {
			return "", fmt.Errorf("duplicate hunk index")
		}
		seen[index] = true
		if index < 0 || index >= len(hunks) {
			return "", fmt.Errorf("hunk index %d is unavailable", index)
		}
		chosen.WriteString(hunks[index])
	}
	return chosen.String(), nil
}

func changeReviewLiteralState(label, path string, state filehistory.RevisionFileState) string {
	return fmt.Sprintf("%s %s exists=%t mode=%04o bytes=%d final-newline=%t", label, changeReviewSafeLabel(path), state.Exists, state.Mode, len(state.Data), bytes.HasSuffix(state.Data, []byte{'\n'}))
}

func changeReviewSafeLabel(label string) string {
	return strconv.QuoteToGraphic(label)
}

func (p *ChangeReviewPanel) Overlay(base string, width, height int) string {
	if !p.visible {
		return base
	}
	if width <= 0 || height <= 0 {
		return ""
	}
	lines := p.renderLines(width, height)
	view, _ := modalCenteredOverlay(p.environment.normalized().profile, base, strings.Join(lines, "\n"), width, height)
	return view
}

func (p *ChangeReviewPanel) renderLines(width, height int) []string {
	profile := p.environment.normalized().profile
	inner := max(1, width-2)
	lines := []string{p.styles.DialogTitle.Render("Captured changes")}
	if width >= 52 {
		lines = append(lines, p.styles.DialogHelp.Render("Captured Write/Edit only; external effects excluded"))
	} else {
		lines = append(lines, p.styles.DialogHelp.Render("Write/Edit captures only"))
	}
	if p.snapshot.Warning != "" {
		lines = append(lines, p.styles.Warning.Render(changeReviewDisplay(p.snapshot.Warning)))
	}
	contentRows := height - 3
	if p.snapshot.Warning != "" {
		contentRows--
	}
	if p.feedback != "" {
		contentRows--
	}
	contentRows = max(1, contentRows)
	if p.preview != nil {
		lines = append(lines, p.renderPreview(inner, contentRows)...)
	} else {
		lines = append(lines, p.renderList(contentRows)...)
	}
	if p.feedback != "" {
		lines = append(lines, p.styles.Error.Render(changeReviewDisplay(p.feedback)))
	}
	footer := "↑↓ move Enter view r restore Esc close"
	if p.preview != nil {
		footer = "Tab hunk a add r restore Esc list"
	}
	if p.snapshot.Restore != nil && (p.snapshot.Restore.State == "applying" || p.snapshot.Restore.State == "partial") {
		footer = "t retry b rollback Esc close"
	}
	lines = append(lines, p.styles.DialogHelp.Render(footer))
	if len(lines) > height {
		lines = lines[:height]
	}
	for i := range lines {
		lines[i] = modalEllipsize(profile, lines[i], width, 0, "…")
	}
	return lines
}

func (p *ChangeReviewPanel) renderList(capacity int) []string {
	var lines []string
	visible := p.visibleRows()
	p.selected = p.clampVisible(p.selected)
	selectedPosition := 0
	for i, index := range visible {
		if index == p.selected {
			selectedPosition = i
			break
		}
	}
	if selectedPosition < p.listTop {
		p.listTop = selectedPosition
	}
	if selectedPosition >= p.listTop+capacity {
		p.listTop = selectedPosition - capacity + 1
	}
	p.listTop = max(0, min(p.listTop, max(0, len(visible)-capacity)))
	for _, i := range visible[p.listTop:min(len(visible), p.listTop+capacity)] {
		row := p.rows[i]
		prefix := "  "
		if i == p.selected {
			prefix = "❯ "
		}
		switch row.kind {
		case changeReviewTurnRow:
			state := "pending"
			if !row.turn.FinishedAt.IsZero() {
				state = row.turn.TerminalReason
			}
			lines = append(lines, prefix+fmt.Sprintf("%s turn %s %s", changeReviewShortTime(row.turn.StartedAt), changeReviewShort(row.turn.TurnID), changeReviewDisplay(state)))
		case changeReviewFileRow:
			lines = append(lines, prefix+fmt.Sprintf("%s %s [%s] %s", changeReviewShortTime(row.header.Timestamp), changeReviewDisplay(row.header.ToolName), changeReviewDisplay(row.header.Outcome), changeReviewDisplay(row.header.Path)))
		case changeReviewGapRow:
			reason := changeReviewDisplay(row.header.Reason)
			if reason == "" {
				reason = "capture gap"
			}
			if row.turn.TurnID == "" {
				lines = append(lines, prefix+"unknown turn: "+changeReviewDisplay(row.header.ToolName)+" "+changeReviewDisplay(row.header.Path)+" ("+reason+")")
			} else {
				lines = append(lines, prefix+"cannot review "+changeReviewDisplay(row.header.ToolName)+" "+changeReviewDisplay(row.header.Path)+": "+reason)
			}
		}
	}
	if len(lines) == 0 {
		return []string{p.styles.Placeholder.Render("No captured Write/Edit records.")}
	}
	return lines
}

func (p *ChangeReviewPanel) visibleRows() []int {
	result := make([]int, 0, len(p.rows))
	for i, row := range p.rows {
		if row.kind == changeReviewTurnRow || !p.turnCollapsed(row.header.TurnID) {
			result = append(result, i)
		}
	}
	return result
}

func (p *ChangeReviewPanel) clampVisible(index int) int {
	visible := p.visibleRows()
	if len(visible) == 0 {
		return 0
	}
	for _, candidate := range visible {
		if candidate == index {
			return index
		}
	}
	return visible[0]
}

func (p *ChangeReviewPanel) visibleMove(delta int) int {
	visible := p.visibleRows()
	current := p.clampVisible(p.selected)
	for i, candidate := range visible {
		if candidate == current {
			return visible[min(max(0, i+delta), len(visible)-1)]
		}
	}
	return current
}

func (p *ChangeReviewPanel) turnCollapsed(turnID string) bool {
	for _, row := range p.rows {
		if row.kind == changeReviewTurnRow && row.turn.TurnID == turnID {
			return row.collapsed
		}
	}
	return false
}

func (p *ChangeReviewPanel) renderPreview(width, capacity int) []string {
	meta := []string{changeReviewDisplay(p.preview.ToolName) + " " + changeReviewSafeLabel(p.preview.Path), fmt.Sprintf("%s %s %s", changeReviewShort(p.preview.TurnID), p.preview.Outcome, changeReviewShortTime(p.preview.Timestamp))}
	if p.fallback {
		return append(meta, "Diff exceeds preview limit; a adds whole capture.", changeReviewLiteralState("before", p.preview.Path, p.preview.Before), changeReviewLiteralState("after", p.preview.Path, p.preview.After))
	}
	if len(p.hunks) == 0 {
		return append(meta, changeReviewLiteralState("before", p.preview.Path, p.preview.Before), changeReviewLiteralState("after", p.preview.Path, p.preview.After), "No text changes; a adds captured state.")
	}
	key := changeReviewCacheKey{id: p.preview.ID, width: width, env: p.environment.identity(), cursor: p.hunkCursor}
	body := p.cache[key]
	if body == nil {
		body = p.environment.normalized().profile.wrap(changeReviewPreviewText(p.hunks[p.hunkCursor]), max(1, width), true)
		if len(p.cache) >= 8 {
			p.cache = map[changeReviewCacheKey][]string{}
		}
		p.cache[key] = body
	}
	mark := "[ ]"
	if p.marked[p.hunkCursor] {
		mark = "[x]"
	}
	meta = append(meta, fmt.Sprintf("%s hunk %d/%d", mark, p.hunkCursor+1, len(p.hunks)))
	available := max(1, capacity-len(meta))
	p.lineOffset = min(p.lineOffset, max(0, len(body)-available))
	return append(meta, body[p.lineOffset:min(len(body), p.lineOffset+available)]...)
}

func changeReviewShort(value string) string {
	if utf8.RuneCountInString(value) > 12 {
		return changeReviewDisplay(string([]rune(value)[:12])) + "…"
	}
	return changeReviewDisplay(value)
}

func changeReviewShortTime(value time.Time) string {
	if value.IsZero() {
		return "time unknown"
	}
	return value.UTC().Format("15:04:05Z")
}

// Metadata occupies one terminal row. Quote control characters rather than
// allowing filenames or source labels to forge rows or terminal escape output.
func changeReviewDisplay(value string) string {
	quoted := strconv.QuoteToGraphic(value)
	return quoted[1 : len(quoted)-1]
}

func changeReviewPreviewText(raw string) string {
	if len(raw) <= changeReviewMaxPreviewBytes {
		return sideQuestionDialogLiteral(raw)
	}
	end := changeReviewMaxPreviewBytes
	for end > 0 && !utf8.RuneStart(raw[end]) {
		end--
	}
	return sideQuestionDialogLiteral(raw[:end]) + "\n[Preview clipped; adding keeps the complete captured hunk.]"
}
