package tui

import (
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

const (
	doubleClickThreshold = 400 * time.Millisecond
	clickTolerance       = 2
	selectionEdgeTick    = 50 * time.Millisecond
)

type selectionEdgeScrollTickMsg struct {
	generation uint64
}

type selectionEdgeScrollState struct {
	generation uint64
	active     bool
	direction  int
	x          int
}

// selItemPoint represents a position within a specific chat item.
// This is scroll-independent: survives scrolling and item height changes.
type selItemPoint struct {
	itemIdx    int
	lineInItem int
	col        int
}

type selectionEndpointIdentity struct {
	cacheKey string
	version  uint64
}

type selectionContentIdentity struct {
	contentGen  uint64
	environment renderEnvironmentIdentity
	renderWidth int
	anchor      selectionEndpointIdentity
	focus       selectionEndpointIdentity
	hasFocus    bool
}

// Selection tracks mouse text selection state for copy-on-select.
// Uses item-based coordinates that are stable across scrolling and
// item height mutations (streaming growth, tool collapse).
type Selection struct {
	anchor     *selItemPoint
	focus      *selItemPoint
	isDragging bool
	identity   *selectionContentIdentity

	// Expand-view flat-line selection (separate from item-based)
	expandAnchor *expandSelPoint
	expandFocus  *expandSelPoint

	// Multi-click detection (uses raw viewport coords for timing only)
	lastClickTime time.Time
	lastClickX    int
	lastClickY    int
	clickCount    int
}

// HasSelection returns true if there is a non-empty selection.
func (s *Selection) HasSelection() bool {
	if s.anchor == nil || s.focus == nil {
		return false
	}
	return *s.anchor != *s.focus
}

// IsDragging returns whether a drag is in progress.
func (s *Selection) IsDragging() bool {
	return s.isDragging
}

// Clear removes the selection.
func (s *Selection) Clear() {
	s.anchor = nil
	s.focus = nil
	s.isDragging = false
	s.identity = nil
}

// getItemBounds returns normalized selection bounds in item coordinates
// (start <= end in reading order).
func (s *Selection) getItemBounds() (start, end selItemPoint) {
	if s.anchor == nil || s.focus == nil {
		return selItemPoint{}, selItemPoint{}
	}
	a, f := *s.anchor, *s.focus
	if a.itemIdx < f.itemIdx ||
		(a.itemIdx == f.itemIdx && a.lineInItem < f.lineInItem) ||
		(a.itemIdx == f.itemIdx && a.lineInItem == f.lineInItem && a.col <= f.col) {
		return a, f
	}
	return f, a
}

// HandleMouseForChat processes mouse events for text selection, using the
// ChatView to resolve viewport coordinates to item-based coordinates.
// chatX, chatY are viewport-relative (0,0 = top-left of chat area).
// Returns true if the event was consumed.
func (s *Selection) HandleMouseForChat(msg tuiMouseMsg, chatX, chatY int, chat *ChatView) bool {
	if msg.Shift {
		return false
	}
	if (s.anchor != nil || s.focus != nil) && !s.ensureCompatible(chat) {
		// A stale motion or release remains owned by selection so it cannot
		// fall through to a link, expansion, or Agent action.
		return msg.Action == mouseActionMotion ||
			msg.Action == mouseActionRelease
	}

	switch {
	case msg.Button == tea.MouseLeft && msg.Action == mouseActionPress:
		s.handlePressForChat(chatX, chatY, chat)
		return false
	case msg.Action == mouseActionMotion && s.isDragging:
		s.updateForChat(chatX, chatY, chat)
		return true
	case msg.Button == tea.MouseLeft && msg.Action == mouseActionRelease:
		if s.isDragging {
			s.finishForChat(chatX, chatY, chat)
			return true
		}
		// Double/triple click selections are complete on press, but their
		// matching release must still consume/copy before business actions.
		return s.HasSelection()
	}
	return false
}

func (s *Selection) handlePressForChat(col, row int, chat *ChatView) {
	now := time.Now()
	if now.Sub(s.lastClickTime) <= doubleClickThreshold &&
		intAbs(col-s.lastClickX) <= clickTolerance &&
		intAbs(row-s.lastClickY) <= clickTolerance {
		s.clickCount++
	} else {
		s.clickCount = 1
	}
	s.lastClickTime = now
	s.lastClickX = col
	s.lastClickY = row

	switch s.clickCount {
	case 2:
		s.selectWordForChat(col, row, chat)
	case 3:
		s.selectLineForChat(row, chat)
		s.clickCount = 0
	default:
		s.startForChat(col, row, chat)
	}
}

func (s *Selection) startForChat(col, row int, chat *ChatView) {
	s.Clear()
	pt := chat.viewportPosToItemPoint(col, row)
	if pt == nil {
		return
	}
	s.anchor = pt
	s.focus = nil
	s.isDragging = true
	s.identity, _ = chat.selectionIdentityFor(*pt, nil)
}

func (s *Selection) updateForChat(col, row int, chat *ChatView) {
	if s.anchor == nil {
		return
	}
	pt := chat.viewportPosToItemPoint(col, row)
	if pt == nil {
		pt = chat.nearestSelectableViewportPoint(col, row)
		if pt == nil {
			return
		}
	}
	if s.focus == nil && *pt == *s.anchor {
		return
	}
	s.focus = pt
	s.identity, _ = chat.selectionIdentityFor(*s.anchor, s.focus)
}

func (s *Selection) finishForChat(col, row int, chat *ChatView) {
	s.isDragging = false
	if s.focus == nil {
		s.anchor = nil
		return
	}
	pt := chat.viewportPosToItemPoint(col, row)
	if pt == nil {
		pt = chat.nearestSelectableViewportPoint(col, row)
	}
	if pt != nil {
		s.focus = pt
		s.identity, _ = chat.selectionIdentityFor(*s.anchor, s.focus)
	}
}

func (s *Selection) selectWordForChat(col, row int, chat *ChatView) {
	pt := chat.viewportPosToItemPoint(col, row)
	if pt == nil {
		return
	}
	clusters := selectionLogicalLineClusters(chat, *pt)
	clicked := -1
	for index, cluster := range clusters {
		if pt.lineInItem == cluster.start.lineInItem &&
			pt.col >= cluster.start.col && pt.col < cluster.end.col {
			clicked = index
			break
		}
	}
	if clicked < 0 || selectionClusterWhitespace(clusters[clicked].text) {
		return
	}
	start, end := clicked, clicked+1
	if selectionClusterWord(clusters[clicked].text) {
		for start > 0 && selectionClusterWord(clusters[start-1].text) {
			start--
		}
		for end < len(clusters) && selectionClusterWord(clusters[end].text) {
			end++
		}
	}
	anchor, focus := clusters[start].start, clusters[end-1].end
	s.anchor = &anchor
	s.focus = &focus
	s.isDragging = false
	s.identity, _ = chat.selectionIdentityFor(*s.anchor, s.focus)
}

func (s *Selection) selectLineForChat(row int, chat *ChatView) {
	projection := chat.currentViewportProjection()
	if projection == nil || row < 0 || row >= len(projection.rows) {
		return
	}
	descriptor := projection.rows[row]
	if descriptor.kind != chatViewportRowTranscript {
		return
	}
	pt := chat.nearestSelectionPointInRow(
		descriptor.itemIdx,
		descriptor.lineInItem,
		0,
	)
	if pt == nil {
		return
	}
	clusters := selectionLogicalLineClusters(chat, *pt)
	if len(clusters) == 0 {
		return
	}
	anchor, focus := clusters[0].start, clusters[len(clusters)-1].end
	s.anchor = &anchor
	s.focus = &focus
	s.isDragging = false
	s.identity, _ = chat.selectionIdentityFor(*s.anchor, s.focus)
}

type selectionLogicalCluster struct {
	text       string
	start, end selItemPoint
}

func selectionLogicalLineClusters(
	chat *ChatView,
	point selItemPoint,
) []selectionLogicalCluster {
	if point.itemIdx < 0 || point.itemIdx >= len(chat.items) {
		return nil
	}
	entry := chat.renderItem(chat.items[point.itemIdx], chat.renderWidth())
	if point.lineInItem < 0 || point.lineInItem >= len(entry.selection) {
		return nil
	}
	startLine := point.lineInItem
	for startLine > 0 &&
		entry.selection[startLine-1].boundary == selectionBoundarySoft {
		startLine--
	}
	endLine := point.lineInItem
	for endLine < len(entry.selection)-1 &&
		entry.selection[endLine].boundary == selectionBoundarySoft {
		endLine++
	}

	profile := chat.environment.normalized().profile
	var result []selectionLogicalCluster
	for line := startLine; line <= endLine; line++ {
		for _, span := range entry.selection[line].spans {
			for _, cluster := range profile.clusters(span.text, span.startCell) {
				if cluster.cells <= 0 || cluster.control {
					continue
				}
				result = append(result, selectionLogicalCluster{
					text: cluster.source,
					start: selItemPoint{
						itemIdx: point.itemIdx, lineInItem: line,
						col: cluster.startColumn,
					},
					end: selItemPoint{
						itemIdx: point.itemIdx, lineInItem: line,
						col: cluster.endColumn,
					},
				})
			}
		}
	}
	return result
}

func selectionClusterWhitespace(value string) bool {
	runes := []rune(value)
	if len(runes) == 0 {
		return false
	}
	for _, r := range runes {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func selectionClusterWord(value string) bool {
	runes := []rune(value)
	if len(runes) == 0 ||
		(!unicode.IsLetter(runes[0]) && !unicode.IsDigit(runes[0]) &&
			runes[0] != '_') {
		return false
	}
	for _, r := range runes[1:] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) &&
			!unicode.IsMark(r) && r != '_' {
			return false
		}
	}
	return true
}

// UpdateFocusForEdgeScroll updates the focus point during edge-scroll drag.
func (s *Selection) UpdateFocusForEdgeScroll(col, clampedRow int, chat *ChatView) {
	if s.anchor == nil || !s.ensureCompatible(chat) {
		return
	}
	pt := chat.viewportPosToItemPoint(col, clampedRow)
	if pt == nil {
		pt = chat.nearestSelectableViewportPoint(col, clampedRow)
	}
	if pt != nil {
		s.focus = pt
		s.identity, _ = chat.selectionIdentityFor(*s.anchor, s.focus)
	}
}

func (a *App) handleSelectionEdgeMotion(chatX, chatY int) tea.Cmd {
	projection := a.chat.currentViewportProjection()
	if projection == nil || projection.contentRect.Height == 0 {
		a.stopSelectionEdgeScroll()
		return nil
	}

	direction := 0
	clampedRow := chatY
	switch {
	case chatY <= projection.contentRect.Y:
		direction = -1
		clampedRow = projection.contentRect.Y
	case chatY >= projection.contentRect.bottom()-1:
		direction = 1
		clampedRow = projection.contentRect.bottom() - 1
	}
	if direction == 0 {
		a.stopSelectionEdgeScroll()
		a.selection.updateForChat(chatX, chatY, a.chat)
		return nil
	}

	if a.selectionEdgeScroll.active &&
		a.selectionEdgeScroll.direction == direction {
		a.selectionEdgeScroll.x = chatX
		a.selection.UpdateFocusForEdgeScroll(
			chatX,
			clampedRow,
			a.chat,
		)
		return nil
	}

	a.stopSelectionEdgeScroll()
	a.selectionEdgeScroll.generation++
	a.selectionEdgeScroll.active = true
	a.selectionEdgeScroll.direction = direction
	a.selectionEdgeScroll.x = chatX
	generation := a.selectionEdgeScroll.generation
	if !a.stepSelectionEdgeScroll() {
		a.stopSelectionEdgeScroll()
		return nil
	}
	return selectionEdgeScrollTick(generation)
}

func (a *App) handleSelectionEdgeScrollTick(
	msg selectionEdgeScrollTickMsg,
) tea.Cmd {
	if !a.selectionEdgeScroll.active ||
		msg.generation != a.selectionEdgeScroll.generation {
		return nil
	}
	if !a.stepSelectionEdgeScroll() {
		a.stopSelectionEdgeScroll()
		return nil
	}
	return selectionEdgeScrollTick(msg.generation)
}

func (a *App) stepSelectionEdgeScroll() bool {
	if a == nil || a.state != StateChat || a.selection == nil ||
		!a.selection.IsDragging() ||
		!a.selection.ensureCompatible(a.chat) {
		return false
	}
	if _, modal := a.activeDialogState(); modal {
		return false
	}
	projection := a.chat.currentViewportProjection()
	if projection == nil || projection.contentRect.Height == 0 {
		return false
	}

	beforeFollowing := a.chat.followState.following
	beforeIdx, beforeLine := a.chat.offsetIdx, a.chat.offsetLine
	switch a.selectionEdgeScroll.direction {
	case -1:
		a.chat.ScrollUp(1)
	case 1:
		a.chat.ScrollDown(1)
	default:
		return false
	}
	moved := beforeFollowing != a.chat.followState.following ||
		beforeIdx != a.chat.offsetIdx ||
		beforeLine != a.chat.offsetLine

	if a.chat.viewDirty {
		a.chat.Render(a.layout.chatRect.Width, a.layout.chatHeight)
	}
	if updated := a.chat.currentViewportProjection(); updated != nil &&
		updated.contentRect.Height > 0 {
		row := updated.contentRect.Y
		if a.selectionEdgeScroll.direction > 0 {
			row = updated.contentRect.bottom() - 1
		}
		a.selection.UpdateFocusForEdgeScroll(
			a.selectionEdgeScroll.x,
			row,
			a.chat,
		)
	}
	return moved && a.selection.IsDragging()
}

func (a *App) stopSelectionEdgeScroll() {
	if a == nil || !a.selectionEdgeScroll.active {
		return
	}
	a.selectionEdgeScroll.generation++
	a.selectionEdgeScroll.active = false
	a.selectionEdgeScroll.direction = 0
	a.selectionEdgeScroll.x = 0
}

func selectionEdgeScrollTick(generation uint64) tea.Cmd {
	return tea.Tick(selectionEdgeTick, func(time.Time) tea.Msg {
		return selectionEdgeScrollTickMsg{generation: generation}
	})
}

// ExtractTextFromChat extracts selected text using item coordinates.
func (s *Selection) ExtractTextFromChat(chat *ChatView) string {
	if !s.HasSelection() || !s.ensureCompatible(chat) {
		return ""
	}
	start, end := s.getItemBounds()
	return chat.RenderItemRange(start.itemIdx, start.lineInItem, start.col, end.itemIdx, end.lineInItem, end.col)
}

// GetViewportHighlightRange returns the selection bounds translated to
// viewport-relative rows for highlight rendering.
func (s *Selection) GetViewportHighlightRange(chat *ChatView) (startRow, startCol, endRow, endCol int, ok bool) {
	if !s.HasSelection() || !s.ensureCompatible(chat) {
		return 0, 0, 0, 0, false
	}
	start, end := s.getItemBounds()

	sr := chat.ItemPointToViewportRow(start.itemIdx, start.lineInItem)
	er := chat.ItemPointToViewportRow(end.itemIdx, end.lineInItem)

	p := chat.currentViewportProjection()
	if p == nil {
		return 0, 0, 0, 0, false
	}
	height := p.height
	if sr >= height && er >= height {
		return 0, 0, 0, 0, false
	}
	if sr < 0 && er < 0 {
		return 0, 0, 0, 0, false
	}

	sc := start.col
	ec := end.col
	if sr < 0 {
		sr = 0
		sc = 0
	}
	if er >= height {
		er = height - 1
		ec = 9999
	}
	return sr, sc, er, ec, true
}

func (s *Selection) ensureCompatible(chat *ChatView) bool {
	if s.anchor == nil {
		return false
	}
	if s.identity == nil {
		identity, ok := chat.selectionIdentityFor(*s.anchor, s.focus)
		if !ok {
			s.Clear()
			return false
		}
		s.identity = identity
		return true
	}
	if chat.selectionIdentityCompatible(s.identity, s.anchor, s.focus) {
		return true
	}
	s.Clear()
	return false
}

// --- Helpers ---

func findWordBoundaries(line string, col int) (int, int) {
	runes := []rune(line)
	if len(runes) == 0 || col < 0 || col >= len(runes) {
		return col, col
	}
	if unicode.IsSpace(runes[col]) {
		return col, col
	}
	isWordChar := func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
	}
	start := col
	for start > 0 && isWordChar(runes[start-1]) {
		start--
	}
	end := col
	for end < len(runes) && isWordChar(runes[end]) {
		end++
	}
	if start == end {
		start = col
		end = col + 1
	}
	return start, end
}

func intAbs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// --- Expand View (flat-line) selection support ---
// The expand view uses simple absolute row coordinates since it is a flat
// array of pre-rendered lines (not item-based).

// expandAnchor/expandFocus store flat-line coords for expand view.
type expandSelPoint struct {
	col, row int
}

// HandleExpandMouse processes mouse events for the expand view flat-line selection.
func (s *Selection) HandleExpandMouse(msg tuiMouseMsg, col, row int, lines []string) bool {
	if msg.Shift {
		return false
	}
	switch {
	case msg.Button == tea.MouseLeft && msg.Action == mouseActionPress:
		s.expandAnchor = &expandSelPoint{col: col, row: row}
		s.expandFocus = nil
		s.isDragging = true
		return false
	case msg.Action == mouseActionMotion && s.isDragging && s.expandAnchor != nil:
		if s.expandFocus == nil && col == s.expandAnchor.col && row == s.expandAnchor.row {
			return true
		}
		s.expandFocus = &expandSelPoint{col: col, row: row}
		return true
	case msg.Button == tea.MouseLeft && msg.Action == mouseActionRelease:
		if s.isDragging && s.expandAnchor != nil {
			s.isDragging = false
			if s.expandFocus == nil {
				s.expandAnchor = nil
			} else {
				s.expandFocus = &expandSelPoint{col: col, row: row}
			}
			return true
		}
	}
	return false
}

// ExtractExpandText extracts selected text from flat expand-view lines.
func (s *Selection) ExtractExpandText(
	lines []string,
	profile DisplayCellProfile,
) string {
	if s.expandAnchor == nil || s.expandFocus == nil {
		return ""
	}
	if *s.expandAnchor == *s.expandFocus {
		return ""
	}
	start, end := s.expandAnchor, s.expandFocus
	if start.row > end.row || (start.row == end.row && start.col > end.col) {
		start, end = end, start
	}
	var result strings.Builder
	for row := start.row; row <= end.row && row < len(lines); row++ {
		if row < 0 {
			continue
		}
		lineCells := selectionLineCells(profile, lines[row])
		cs, ce := 0, lineCells
		if row == start.row {
			cs = start.col
		}
		if row == end.row {
			ce = end.col
		}
		if cs < 0 {
			cs = 0
		}
		if ce > lineCells {
			ce = lineCells
		}
		if cs > ce {
			cs = ce
		}
		if result.Len() > 0 {
			result.WriteByte('\n')
		}
		result.WriteString(selectionSliceCells(profile, lines[row], cs, ce))
	}
	return result.String()
}

// HasExpandSelection returns true if expand-view selection exists.
func (s *Selection) HasExpandSelection() bool {
	return s.expandAnchor != nil && s.expandFocus != nil && *s.expandAnchor != *s.expandFocus
}

// GetExpandBounds returns normalized expand-view selection bounds.
func (s *Selection) GetExpandBounds() (startRow, startCol, endRow, endCol int) {
	if s.expandAnchor == nil || s.expandFocus == nil {
		return 0, 0, 0, 0
	}
	a, f := s.expandAnchor, s.expandFocus
	if a.row < f.row || (a.row == f.row && a.col <= f.col) {
		return a.row, a.col, f.row, f.col
	}
	return f.row, f.col, a.row, a.col
}

// --- Keyboard Selection ---

// ExtendUp extends the focus point one line up (Shift+Up).
// Only works when a selection already exists.
func (s *Selection) ExtendUp(chat *ChatView) {
	if s.focus == nil || !s.ensureCompatible(chat) {
		return
	}
	if point := selectionAdjacentSemanticRow(chat, *s.focus, -1); point != nil {
		s.focus = point
		s.identity, _ = chat.selectionIdentityFor(*s.anchor, s.focus)
	}
}

// ExtendDown extends the focus point one line down (Shift+Down).
func (s *Selection) ExtendDown(chat *ChatView) {
	if s.focus == nil || !s.ensureCompatible(chat) {
		return
	}
	if point := selectionAdjacentSemanticRow(chat, *s.focus, 1); point != nil {
		s.focus = point
		s.identity, _ = chat.selectionIdentityFor(*s.anchor, s.focus)
	}
}

func selectionAdjacentSemanticRow(
	chat *ChatView,
	from selItemPoint,
	direction int,
) *selItemPoint {
	if direction == 0 {
		return nil
	}
	item, line := from.itemIdx, from.lineInItem
	for item >= 0 && item < len(chat.items) {
		entry := chat.renderItem(chat.items[item], chat.renderWidth())
		line += direction
		for line >= 0 && line < len(entry.selection) {
			if start, end, ok := selectionRowCellBounds(entry.selection[line]); ok {
				col := min(max(from.col, start), end)
				return &selItemPoint{
					itemIdx: item, lineInItem: line, col: col,
				}
			}
			line += direction
		}
		item += direction
		if item < 0 || item >= len(chat.items) {
			return nil
		}
		if direction > 0 {
			line = -1
		} else {
			line = len(chat.renderItem(
				chat.items[item],
				chat.renderWidth(),
			).selection)
		}
	}
	return nil
}
