package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/abietic/yhc/engine"
)

// ChatItem is the legacy render contract retained while M4 migrates message
// families to HistoryItem. ChatView adapts every ChatItem before rendering.
type ChatItem interface {
	Render(width int, styles Styles) string
	Finished() bool
	Version() uint64     // monotonic mutation counter for cache invalidation
	NoSelectPrefix() int // columns to exclude from selection (gutter, icons)
}

// renderCacheEntry holds a cached render result for one item.
type renderCacheEntry struct {
	version     uint64
	width       int
	lines       []string // pre-split for O(1) line access
	selection   []selectionRowMetadata
	cacheKey    string
	height      int    // len(lines)
	frozen      bool   // Finished() items never change again
	themeGen    uint64 // active ChatView style generation
	geometryGen uint64
	environment renderEnvironmentIdentity
}

// chatViewportRowKind describes one final published chat row. Only transcript
// rows can participate in application-managed selection.
type chatViewportRowKind uint8

const (
	chatViewportRowEmpty chatViewportRowKind = iota
	chatViewportRowSticky
	chatViewportRowPadding
	chatViewportRowTranscript
	chatViewportRowItemGap
	chatViewportRowPill
)

type chatViewportRow struct {
	kind       chatViewportRowKind
	itemIdx    int
	lineInItem int
}

// chatViewportProjection is the immutable, viewport-bounded counterpart of
// the exact string returned by Render.
type chatViewportProjection struct {
	width, height int
	environment   renderEnvironmentIdentity
	frameGen      uint64
	contentGen    uint64
	contentRect   layoutRect
	rows          []chatViewportRow
	pill          chatPillGeometry
}

// ChatView manages the scrollable list of chat messages.
type ChatView struct {
	items        []ChatItem
	width        int
	height       int
	followState  chatFollowState
	appendIntent chatAppendIntent
	styles       Styles
	environment  RenderEnvironment
	// themeGen invalidates both streaming and frozen per-item renders when the
	// live theme changes without discarding semantic chat state.
	themeGen uint64

	// Item-index scrolling (replaces flat offset)
	offsetIdx  int // index of first visible item
	offsetLine int // lines already scrolled within that item

	// Semantic item registry and render cache. The registry preserves legacy
	// pointer lookup while the cache itself uses explicit HistoryItem identity.
	historyItems     map[ChatItem]HistoryItem
	historyCacheKeys map[ChatItem]string
	historyKeyOwners map[string]ChatItem
	historySequence  uint64
	renderCache      map[string]*renderCacheEntry

	// Viewport cache — avoids re-assembly when nothing changed between View() calls
	viewDirty bool
	// contentDirty distinguishes endpoint/reflow invalidation from a final-frame
	// only change such as scroll, follow, sticky chrome, or a pill overlay.
	contentDirty bool
	viewCache    string
	// viewCacheEnvironment binds the assembled viewport to the same immutable
	// geometry input as its per-item entries.
	viewCacheEnvironment renderEnvironmentIdentity
	viewCacheWidth       int
	viewCacheHeight      int
	viewportProjection   *chatViewportProjection
	frameGeneration      uint64
	contentGeneration    uint64
	pillGeometryCache    chatPillGeometry

	// For streaming assistant/thinking messages
	currentAssistant *AssistantMessage
	currentThinking  *ThinkingMessage

	// For tool tracking by ID
	toolsByID map[string]*ToolMessage

	// Spinner count passed from app for tool animation
	spinnerCount int

	// Scroll animation state
	scrollRemaining int // lines remaining to scroll in animation
}

// chatAppendIntent makes the origin of every top-level append explicit. Only
// live appends contribute to the presentation-only unseen epoch.
type chatAppendIntent uint8

const (
	chatAppendLive chatAppendIntent = iota
	chatAppendHydration
)

type chatFollowState struct {
	following     bool
	appendEpoch   uint64
	baselineEpoch uint64
	baselineValid bool
}

func newChatFollowState() chatFollowState { return chatFollowState{following: true} }

func (s *chatFollowState) recordAppend(intent chatAppendIntent) {
	if intent == chatAppendLive && s.appendEpoch < ^uint64(0) {
		s.appendEpoch++
	}
}

func (s *chatFollowState) leave() {
	if s.following {
		s.following = false
		s.baselineEpoch = s.appendEpoch
		s.baselineValid = true
	}
}

func (s *chatFollowState) follow() {
	s.following = true
	s.baselineEpoch = 0
	s.baselineValid = false
}

func (s *chatFollowState) restoreAway() {
	s.following = false
	s.baselineEpoch = 0
	s.baselineValid = false
}

func (s chatFollowState) unseen() uint64 {
	if !s.baselineValid || s.appendEpoch < s.baselineEpoch {
		return 0
	}
	return s.appendEpoch - s.baselineEpoch
}

type chatPillAction uint8

const (
	chatPillActionNone chatPillAction = iota
	chatPillActionFollow
)

type chatPillModel struct {
	visible bool
	label   string
	action  chatPillAction
}

func (p chatPillModel) renderText() string { return " " + p.label + " ↓ " }

// chatPillGeometry is the sole presentation projection for the semantic
// jump-to-bottom pill. Its row and columns are chat-relative: start is
// inclusive and end is exclusive.
type chatPillGeometry struct {
	visible      bool
	renderedRun  string
	renderedLine string
	row          int
	start        int
	end          int
	action       chatPillAction
	profileID    string
	width        int
	height       int
	label        string
	environment  renderEnvironmentIdentity
}

func (g chatPillGeometry) hits(x, y int) bool {
	return g.visible && y == g.row && x >= g.start && x < g.end
}

// RenderedContent returns the last rendered view content for text extraction.
func (c *ChatView) RenderedContent() string {
	return c.viewCache
}

// ViewportTopRow returns the absolute row offset of the first visible line
// in the full content. Used to convert viewport-relative coordinates to
// absolute content coordinates for selection tracking across scrolls.
func (c *ChatView) ViewportTopRow() int {
	rw := c.renderWidth()
	absRow := 0
	for idx := 0; idx < c.offsetIdx && idx < len(c.items); idx++ {
		entry := c.renderItem(c.items[idx], rw)
		absRow += entry.height + 1 // +1 for gap between items
	}
	absRow += c.offsetLine
	return absRow
}

// RenderAllLines renders all items into a flat line array for full-content
// text extraction. This is used when the selection spans across scrolled
// regions that are no longer visible in the viewport.
func (c *ChatView) RenderAllLines() []string {
	if len(c.items) == 0 {
		return nil
	}
	rw := c.renderWidth()
	var lines []string
	for idx, item := range c.items {
		entry := c.renderItem(item, rw)
		lines = append(lines, entry.lines...)
		if idx < len(c.items)-1 {
			lines = append(lines, "") // gap
		}
	}
	return lines
}

// Height returns the viewport height.
func (c *ChatView) Height() int {
	return c.height
}

// viewportPosToItemPoint converts a viewport-relative (col, row) position
// to an item-based coordinate. Returns nil if the position is outside content.
func (c *ChatView) viewportPosToItemPoint(col, row int) *selItemPoint {
	p := c.currentViewportProjection()
	if p == nil || row < 0 || row >= len(p.rows) {
		return nil
	}
	descriptor := p.rows[row]
	if descriptor.kind != chatViewportRowTranscript {
		return nil
	}
	metadata, _, ok := c.selectionMetadata(
		descriptor.itemIdx,
		descriptor.lineInItem,
	)
	if !ok || !selectionCellSelectable(metadata, col) {
		return nil
	}
	return &selItemPoint{
		itemIdx:    descriptor.itemIdx,
		lineInItem: descriptor.lineInItem,
		col:        selectionClampCell(metadata, col),
	}
}

// ItemPointToViewportRow converts an item-based coordinate to a viewport-relative
// row. Returns negative if above viewport, >= height if below.
func (c *ChatView) ItemPointToViewportRow(itemIdx, lineInItem int) int {
	p := c.currentViewportProjection()
	if p == nil {
		return -1
	}
	for row, descriptor := range p.rows {
		if descriptor.kind == chatViewportRowTranscript && descriptor.itemIdx == itemIdx && descriptor.lineInItem == lineInItem {
			return row
		}
	}
	first, last := -1, -1
	for row, descriptor := range p.rows {
		if descriptor.kind != chatViewportRowTranscript {
			continue
		}
		if first < 0 {
			first = row
		}
		last = row
	}
	if first < 0 {
		return -1
	}
	firstPoint, lastPoint := p.rows[first], p.rows[last]
	if itemIdx < firstPoint.itemIdx || (itemIdx == firstPoint.itemIdx && lineInItem < firstPoint.lineInItem) {
		return -1
	}
	if itemIdx > lastPoint.itemIdx || (itemIdx == lastPoint.itemIdx && lineInItem > lastPoint.lineInItem) {
		return p.height
	}
	return -1
}

func (c *ChatView) currentViewportProjection() *chatViewportProjection {
	if c == nil {
		return nil
	}
	p := c.viewportProjection
	if c.viewDirty || c.viewCache == "" || p == nil ||
		p.environment != c.environment.identity() ||
		p.width != c.viewCacheWidth || p.height != c.viewCacheHeight ||
		p.frameGen != c.frameGeneration || p.contentGen != c.contentGeneration ||
		len(p.rows) != p.height {
		return nil
	}
	return p
}

func (c *ChatView) nearestSelectableViewportPoint(col, row int) *selItemPoint {
	p := c.currentViewportProjection()
	if p == nil {
		return nil
	}
	row = min(max(row, 0), len(p.rows)-1)
	for distance := 0; distance < len(p.rows); distance++ {
		for _, candidate := range []int{row - distance, row + distance} {
			if candidate >= 0 && candidate < len(p.rows) {
				descriptor := p.rows[candidate]
				if descriptor.kind != chatViewportRowTranscript {
					continue
				}
				if point := c.nearestSelectionPointInRow(
					descriptor.itemIdx,
					descriptor.lineInItem,
					col,
				); point != nil {
					return point
				}
			}
		}
	}
	return nil
}

func (c *ChatView) selectionMetadata(
	itemIdx, lineInItem int,
) (selectionRowMetadata, *renderCacheEntry, bool) {
	if itemIdx < 0 || itemIdx >= len(c.items) {
		return selectionRowMetadata{}, nil, false
	}
	entry := c.renderItem(c.items[itemIdx], c.renderWidth())
	if lineInItem < 0 || lineInItem >= len(entry.selection) {
		return selectionRowMetadata{}, entry, false
	}
	row := entry.selection[lineInItem]
	return row, entry, row.selectable
}

func (c *ChatView) nearestSelectionPointInRow(
	itemIdx, lineInItem, col int,
) *selItemPoint {
	row, _, ok := c.selectionMetadata(itemIdx, lineInItem)
	if !ok {
		return nil
	}
	start, end, ok := selectionRowCellBounds(row)
	if !ok {
		return nil
	}
	col = min(max(col, start), end)
	return &selItemPoint{
		itemIdx: itemIdx, lineInItem: lineInItem, col: col,
	}
}

func selectionCellSelectable(row selectionRowMetadata, cell int) bool {
	if !row.selectable {
		return false
	}
	for _, span := range row.spans {
		if cell >= span.startCell && cell < span.endCell {
			return true
		}
	}
	return false
}

func selectionClampCell(row selectionRowMetadata, cell int) int {
	for _, span := range row.spans {
		if cell >= span.startCell && cell < span.endCell {
			return min(max(cell, span.startCell), span.endCell)
		}
	}
	return cell
}

func (c *ChatView) selectionIdentityFor(
	anchor selItemPoint,
	focus *selItemPoint,
) (*selectionContentIdentity, bool) {
	projection := c.currentViewportProjection()
	if projection == nil {
		return nil, false
	}
	_, anchorEntry, ok := c.selectionMetadata(
		anchor.itemIdx,
		anchor.lineInItem,
	)
	if !ok {
		return nil, false
	}
	identity := &selectionContentIdentity{
		contentGen:  projection.contentGen,
		environment: projection.environment,
		renderWidth: c.renderWidth(),
		anchor: selectionEndpointIdentity{
			cacheKey: anchorEntry.cacheKey,
			version:  anchorEntry.version,
		},
	}
	if focus == nil {
		return identity, true
	}
	_, focusEntry, ok := c.selectionMetadata(
		focus.itemIdx,
		focus.lineInItem,
	)
	if !ok {
		return nil, false
	}
	identity.focus = selectionEndpointIdentity{
		cacheKey: focusEntry.cacheKey,
		version:  focusEntry.version,
	}
	identity.hasFocus = true
	return identity, true
}

func (c *ChatView) selectionIdentityCompatible(
	identity *selectionContentIdentity,
	anchor *selItemPoint,
	focus *selItemPoint,
) bool {
	if identity == nil || anchor == nil {
		return false
	}
	projection := c.currentViewportProjection()
	if projection == nil ||
		projection.contentGen != identity.contentGen ||
		projection.environment != identity.environment ||
		c.renderWidth() != identity.renderWidth {
		return false
	}
	current, ok := c.selectionIdentityFor(*anchor, focus)
	if !ok ||
		current.contentGen != identity.contentGen ||
		current.environment != identity.environment ||
		current.renderWidth != identity.renderWidth ||
		current.anchor != identity.anchor ||
		current.hasFocus != identity.hasFocus {
		return false
	}
	return !current.hasFocus || current.focus == identity.focus
}

func (c *ChatView) selectionHighlightRanges(
	itemIdx, lineInItem, startCell, endCell int,
) [][2]int {
	row, _, ok := c.selectionMetadata(itemIdx, lineInItem)
	if !ok {
		return nil
	}
	return selectionRowRanges(row, startCell, endCell)
}

// GetItemLine returns a specific rendered line from an item.
func (c *ChatView) GetItemLine(itemIdx, lineInItem int) string {
	if itemIdx < 0 || itemIdx >= len(c.items) {
		return ""
	}
	rw := c.renderWidth()
	entry := c.renderItem(c.items[itemIdx], rw)
	if lineInItem < 0 || lineInItem >= entry.height {
		return ""
	}
	return entry.lines[lineInItem]
}

// RenderItemRange extracts text from the given item range, slicing at the
// specified line/col boundaries.
func (c *ChatView) RenderItemRange(startItem, startLine, startCol, endItem, endLine, endCol int) string {
	if len(c.items) == 0 {
		return ""
	}
	if startItem < 0 {
		startItem = 0
		startLine = 0
		startCol = 0
	}
	if endItem >= len(c.items) {
		endItem = len(c.items) - 1
	}
	if startItem > endItem {
		return ""
	}
	if _, _, ok := c.selectionMetadata(startItem, startLine); !ok {
		return ""
	}
	if _, _, ok := c.selectionMetadata(endItem, endLine); !ok {
		return ""
	}
	rw := c.renderWidth()
	var (
		result      strings.Builder
		emitted     bool
		lastItem    = -1
		pendingHard bool
	)
	for idx := startItem; idx <= endItem; idx++ {
		entry := c.renderItem(c.items[idx], rw)
		fromLine := 0
		if idx == startItem {
			fromLine = startLine
		}
		toLine := entry.height - 1
		if idx == endItem {
			toLine = endLine
		}
		if fromLine < 0 {
			fromLine = 0
		}
		if toLine >= entry.height {
			toLine = entry.height - 1
		}
		for li := fromLine; li <= toLine; li++ {
			if li < 0 || li >= len(entry.selection) {
				continue
			}
			row := entry.selection[li]
			cs, ce, selectable := selectionRowCellBounds(row)
			if !selectable {
				if row.boundary == selectionBoundaryHard {
					pendingHard = true
				}
				continue
			}
			if idx == startItem && li == startLine {
				cs = startCol
			}
			if idx == endItem && li == endLine {
				ce = endCol
			}
			text := selectionRowText(
				c.environment.profile,
				row,
				cs,
				ce,
			)
			if text != "" {
				if emitted && (idx != lastItem || pendingHard) {
					result.WriteByte('\n')
				}
				result.WriteString(text)
				emitted = true
				lastItem = idx
				pendingHard = false
			}
			if row.boundary == selectionBoundaryHard {
				pendingHard = true
			}
		}
	}
	return result.String()
}

func NewChatView(styles Styles) *ChatView {
	return newChatViewWithRenderEnvironment(defaultRenderEnvironment(styles))
}

func newChatViewWithRenderEnvironment(env RenderEnvironment) *ChatView {
	env = env.normalized()
	return &ChatView{
		contentDirty:     true,
		followState:      newChatFollowState(),
		styles:           env.styles,
		environment:      env,
		toolsByID:        make(map[string]*ToolMessage),
		historyItems:     make(map[ChatItem]HistoryItem),
		historyCacheKeys: make(map[ChatItem]string),
		historyKeyOwners: make(map[string]ChatItem),
		renderCache:      make(map[string]*renderCacheEntry),
	}
}

// SetStyles applies a live theme and invalidates every cached render,
// including frozen finished items.
func (c *ChatView) SetStyles(styles Styles) {
	c.SetRenderEnvironment(c.environment.withStyles(styles))
}

// SetRenderEnvironment invalidates presentation caches without changing chat semantics.
func (c *ChatView) SetRenderEnvironment(env RenderEnvironment) {
	c.environment = env.normalized()
	c.styles = c.environment.styles
	c.themeGen = c.environment.themeGen // retained for compatibility tests
	c.invalidateContent()
}

func (c *ChatView) SetSize(width, height int) {
	widthChanged := c.width != width
	c.width = width
	c.height = height
	if widthChanged {
		c.invalidateContent()
		return
	}
	c.invalidateFrame()
}

func (c *ChatView) invalidateFrame() { c.viewDirty = true }

func (c *ChatView) invalidateContent() {
	c.contentDirty = true
	c.viewDirty = true
}

// SetSpinnerCount updates the spinner counter for tool animation.
// Only marks view dirty when following (at bottom), since scrolled-away
// views use stale cache and won't show animation changes anyway.
func (c *ChatView) SetSpinnerCount(count int) {
	c.spinnerCount = count
	if c.followState.following {
		c.invalidateFrame()
	}
}

// ResetFollow re-enables auto-follow (snap to bottom on next render).
func (c *ChatView) ResetFollow() {
	if c.followState.following {
		return
	}
	c.followState.follow()
	c.invalidateFrame()
}

func (c *ChatView) restoreAway() {
	c.followState.restoreAway()
	c.invalidateFrame()
}

// Reset removes all rendered conversation state while preserving dimensions
// and styles. It is used by /clear and when replacing the view after /resume.
func (c *ChatView) Reset() {
	c.items = nil
	c.offsetIdx = 0
	c.offsetLine = 0
	c.followState.follow()
	c.currentAssistant = nil
	c.currentThinking = nil
	c.toolsByID = make(map[string]*ToolMessage)
	c.historyItems = make(map[ChatItem]HistoryItem)
	c.historyCacheKeys = make(map[ChatItem]string)
	c.historyKeyOwners = make(map[string]ChatItem)
	c.historySequence = 0
	c.renderCache = make(map[string]*renderCacheEntry)
	c.invalidateContent()
	c.viewCache = ""
	c.viewCacheEnvironment = renderEnvironmentIdentity{}
}

// RemoveModelAttempt removes assistant, thinking, and uncommitted tool
// presentation owned by one failed model attempt. Runtime tombstones carry
// the exact identity; no positional "last message" heuristic is used.
func (c *ChatView) RemoveModelAttempt(
	attemptID string,
) (bool, []string) {
	attemptID = strings.TrimSpace(attemptID)
	if attemptID == "" {
		return false, nil
	}
	removed := false
	removedToolIDs := make([]string, 0)
	retained := c.items[:0]
	for _, item := range c.items {
		owned := false
		switch typed := item.(type) {
		case *AssistantMessage:
			owned = typed.attemptID == attemptID
		case *ThinkingMessage:
			owned = typed.attemptID == attemptID
		case *ToolMessage:
			owned = typed.attemptID == attemptID
			if owned && typed.toolCallID != "" {
				if indexed := c.toolsByID[typed.toolCallID]; indexed == typed {
					delete(c.toolsByID, typed.toolCallID)
				}
				removedToolIDs = append(removedToolIDs, typed.toolCallID)
			}
		}
		if owned {
			c.forgetHistoryItem(item)
			removed = true
			continue
		}
		retained = append(retained, item)
	}
	c.items = retained
	if c.currentAssistant != nil &&
		c.currentAssistant.attemptID == attemptID {
		c.currentAssistant = nil
	}
	if c.currentThinking != nil &&
		c.currentThinking.attemptID == attemptID {
		c.currentThinking = nil
	}
	if !removed {
		return false, nil
	}
	if len(c.items) == 0 {
		c.offsetIdx = 0
	} else if c.offsetIdx >= len(c.items) {
		c.offsetIdx = len(c.items) - 1
	}
	c.offsetLine = 0
	c.invalidateContent()
	return true, removedToolIDs
}

// AppendHistoryItem adds one live native semantic item. Existing message
// families use appendChatItem until their dedicated M4 renderer is ported.
func (c *ChatView) AppendHistoryItem(item HistoryItem) {
	c.appendHistoryItem(item, chatAppendLive)
}

func (c *ChatView) appendHydratedHistoryItem(item HistoryItem) {
	c.appendHistoryItem(item, chatAppendHydration)
}

func (c *ChatView) appendHistoryItem(item HistoryItem, intent chatAppendIntent) {
	if item == nil {
		return
	}
	legacy := &semanticChatItem{item: item}
	c.items = append(c.items, legacy)
	c.registerHistoryItem(legacy, item)
	c.followState.recordAppend(intent)
	c.invalidateContent()
}

func (c *ChatView) appendChatItem(item ChatItem) {
	c.appendChatItemWithIntent(item, c.appendIntent)
}

func (c *ChatView) appendChatItemWithIntent(item ChatItem, intent chatAppendIntent) {
	if item == nil {
		return
	}
	c.items = append(c.items, item)
	c.registerHistoryItem(item, nil)
	c.followState.recordAppend(intent)
	c.invalidateContent()
}

func (c *ChatView) withHydrationIntent(fn func()) {
	previous := c.appendIntent
	c.appendIntent = chatAppendHydration
	defer func() { c.appendIntent = previous }()
	fn()
}

// Following reports whether the presentation viewport remains at the live end.
func (c *ChatView) Following() bool { return c != nil && c.followState.following }

func (c *ChatView) pillModel() chatPillModel {
	if c == nil || c.followState.following || len(c.items) == 0 {
		return chatPillModel{}
	}
	unseen := c.followState.unseen()
	if unseen == 0 {
		return chatPillModel{visible: true, label: "Jump to bottom", action: chatPillActionFollow}
	}
	if unseen == 1 {
		return chatPillModel{visible: true, label: "1 new message", action: chatPillActionFollow}
	}
	return chatPillModel{
		visible: true,
		label:   fmt.Sprintf("%d new messages", unseen),
		action:  chatPillActionFollow,
	}
}

// pillGeometry publishes a cached cell-geometry projection from the semantic
// model and exact App render environment. Mouse routing consumes this same
// result; it never reproduces label, style, or centering logic.
func (c *ChatView) pillGeometry(width, height int) chatPillGeometry {
	if c == nil || width <= 0 || height <= 0 {
		return chatPillGeometry{}
	}
	model := c.pillModel()
	environment := c.environment.identity()
	current := c.pillGeometryCache
	if current.width == width && current.height == height &&
		current.label == model.label && current.action == model.action &&
		current.visible == model.visible && current.environment == environment {
		return current
	}

	geometry := c.buildPillGeometry(model, width, height, environment)
	c.pillGeometryCache = geometry
	return geometry
}

func (c *ChatView) buildPillGeometry(
	model chatPillModel,
	width, height int,
	environment renderEnvironmentIdentity,
) chatPillGeometry {
	geometry := chatPillGeometry{
		visible:     model.visible,
		row:         height - 1,
		action:      model.action,
		profileID:   environment.displayCellProfileID,
		width:       width,
		height:      height,
		label:       model.label,
		environment: environment,
	}
	if model.visible {
		profile := c.environment.normalized().profile
		run := c.styles.UserMessageBlock.Render(c.styles.Dim.Render(model.renderText()))
		geometry.renderedRun, geometry.start, geometry.end = centeredChatPillRun(profile, run, width)
		geometry.renderedLine = strings.Repeat(" ", geometry.start) + geometry.renderedRun
	}
	return geometry
}

func centeredChatPillRun(
	profile DisplayCellProfile,
	run string,
	width int,
) (rendered string, start, end int) {
	bestStart := -1
	bestScore := width + 1
	bestCells := 0
	for candidate := 0; candidate <= width; candidate++ {
		expanded := profile.expandTabs(run, candidate)
		cells := profile.measure(expanded, candidate)
		right := width - candidate - cells
		if right < 0 {
			continue
		}
		score := candidate - right
		if score < 0 {
			score = -score
		}
		if score >= bestScore {
			continue
		}
		bestStart = candidate
		bestScore = score
		bestCells = cells
		rendered = expanded
	}
	if bestStart < 0 {
		rendered = profile.truncateAt(run, width, 0)
		return rendered, 0, profile.measure(rendered, 0)
	}
	rendered = profile.balanceControlLines([]string{rendered})[0]
	return rendered, bestStart, bestStart + bestCells
}

func (c *ChatView) registerHistoryItem(item ChatItem, semantic HistoryItem) HistoryItem {
	if existing := c.historyItems[item]; existing != nil {
		return existing
	}
	if semantic == nil {
		c.historySequence++
		id := fmt.Sprintf("history:%d", c.historySequence)
		if tool, ok := item.(*ToolMessage); ok && tool.toolCallID != "" {
			id = "tool:" + tool.toolCallID
		}
		semantic = adaptChatItem(id, item)
	}

	key := semantic.ID()
	if key == "" {
		c.historySequence++
		key = fmt.Sprintf("history:%d", c.historySequence)
	}
	if owner, exists := c.historyKeyOwners[key]; exists && owner != item {
		base := key
		for {
			c.historySequence++
			key = fmt.Sprintf("%s#%d", base, c.historySequence)
			if _, duplicate := c.historyKeyOwners[key]; !duplicate {
				break
			}
		}
	}
	c.historyItems[item] = semantic
	c.historyCacheKeys[item] = key
	c.historyKeyOwners[key] = item
	return semantic
}

func (c *ChatView) historyItem(item ChatItem) HistoryItem {
	return c.registerHistoryItem(item, nil)
}

func (c *ChatView) historyCacheKey(item ChatItem) string {
	c.historyItem(item)
	return c.historyCacheKeys[item]
}

func (c *ChatView) forgetHistoryItem(item ChatItem) {
	key := c.historyCacheKeys[item]
	delete(c.renderCache, key)
	delete(c.historyItems, item)
	delete(c.historyCacheKeys, item)
	if c.historyKeyOwners[key] == item {
		delete(c.historyKeyOwners, key)
	}
}

// AppendUser adds a user message to the chat.
func (c *ChatView) AppendUser(content string) {
	c.appendChatItem(&UserMessage{content: content})
	c.retireCurrentAssistant()
	c.invalidateContent()
}

func (c *ChatView) AppendUserWithComposer(content string, elements []threadComposerElement) {
	c.appendChatItem(&UserMessage{
		content: content, composerElements: cloneThreadComposerElements(elements),
	})
	c.retireCurrentAssistant()
	c.invalidateContent()
}

// AppendSystem adds a system/info message.
func (c *ChatView) AppendSystem(content string) {
	c.appendChatItem(&SystemMessage{content: content})
	c.invalidateContent()
}

// AppendCompactBoundary adds a compact boundary marker with dedicated styling.
func (c *ChatView) AppendCompactBoundary(stats string) {
	c.appendChatItem(&CompactBoundaryMessage{stats: stats})
	c.invalidateContent()
}

// AppendCompactBoundaryWithStats adds a compact boundary marker with structured metrics.
func (c *ChatView) AppendCompactBoundaryWithStats(messagesCompacted, tokensBefore, tokensAfter, contextPercent int) {
	c.appendChatItem(&CompactBoundaryMessage{
		messagesCompacted: messagesCompacted,
		tokensBefore:      tokensBefore,
		tokensAfter:       tokensAfter,
		contextPercent:    contextPercent,
	})
	c.invalidateContent()
}

// AppendInterruption adds a styled interruption marker.
func (c *ChatView) AppendInterruption() {
	c.appendChatItem(&InterruptionMessage{})
	c.invalidateContent()
}

// AppendCompactSummary adds a structured summary message for resumed sessions.
func (c *ChatView) AppendCompactSummary(messageCount int) {
	c.appendChatItem(&CompactSummaryMessage{messageCount: messageCount})
	c.invalidateContent()
}

// AppendHelp adds a styled help menu to the chat.
func (c *ChatView) AppendHelp(entries []HelpEntry) {
	c.appendChatItem(&HelpMessage{entries: entries})
	c.invalidateContent()
}

// FinishAssistant marks the current assistant message as finished.
func (c *ChatView) FinishAssistant() {
	if c.currentAssistant != nil {
		if c.currentAssistant.Finalize() {
			c.invalidateContent()
		}
	}
	c.FinishThinking()
}

// FinishThinking marks the current thinking block as complete and collapsed.
func (c *ChatView) FinishThinking() {
	if c.currentThinking != nil && !c.currentThinking.finished {
		c.currentThinking.finished = true
		c.currentThinking.finishedAt = time.Now()
		c.currentThinking.expanded = false
		c.currentThinking.version++
		c.invalidateContent()
	}
}

// retireCurrentAssistant marks the current assistant as finished (if any)
// and clears the pointer. Called when a non-assistant item follows.
func (c *ChatView) retireCurrentAssistant() {
	c.FinishThinking()
	if c.currentAssistant != nil && c.currentAssistant.content != "" {
		c.currentAssistant.Finalize()
	}
	c.currentAssistant = nil
}

// AppendOrUpdateAssistant sets the full assistant message content.
func (c *ChatView) AppendOrUpdateAssistant(content string) {
	c.AppendOrUpdateAssistantAttempt(content, "")
}

func (c *ChatView) AppendOrUpdateAssistantAttempt(
	content string,
	attemptID string,
) {
	if c.currentAssistant == nil ||
		c.currentAssistant.finished ||
		c.currentAssistant.attemptID != attemptID {
		c.currentAssistant = &AssistantMessage{
			attemptID: attemptID,
			version:   1,
		}
		c.appendChatItem(c.currentAssistant)
	}
	c.currentAssistant.ReplaceContent(content)
	c.invalidateContent()
}

// StreamThinkingDelta appends streaming reasoning content to the current thinking message.
func (c *ChatView) StreamThinkingDelta(delta string) {
	c.StreamThinkingDeltaAttempt(delta, "")
}

func (c *ChatView) StreamThinkingDeltaAttempt(
	delta string,
	attemptID string,
) {
	if delta == "" {
		return
	}
	if c.currentThinking == nil ||
		c.currentThinking.finished ||
		c.currentThinking.attemptID != attemptID {
		c.currentThinking = &ThinkingMessage{
			attemptID: attemptID,
			startedAt: time.Now(),
			expanded:  true,
			version:   1,
		}
		c.appendChatItem(c.currentThinking)
	}
	c.currentThinking.AppendDelta(delta)
	c.invalidateContent()
}

// StreamAssistantDelta appends streaming content to the current assistant message.
func (c *ChatView) StreamAssistantDelta(delta string) {
	c.StreamAssistantDeltaAttempt(delta, "")
}

func (c *ChatView) StreamAssistantDeltaAttempt(
	delta string,
	attemptID string,
) {
	c.FinishThinking()
	if c.currentAssistant == nil ||
		c.currentAssistant.finished ||
		c.currentAssistant.attemptID != attemptID {
		c.currentAssistant = &AssistantMessage{
			attemptID: attemptID,
			version:   1,
		}
		c.appendChatItem(c.currentAssistant)
	}
	c.currentAssistant.AppendDelta(delta)
	c.invalidateContent()
}

// AppendOrUpdateTool creates or updates a tool item by ID.
// If toolCallID is provided, uses it for deduplication.
// If toolCallID is empty (streaming incremental chunks), updates the last running tool.
func (c *ChatView) AppendOrUpdateTool(toolCallID, toolName, input string) {
	c.AppendOrUpdateToolAttempt(toolCallID, toolName, input, "")
}

// AppendOrUpdateToolAttempt creates or updates an attempt-owned tool
// projection. Failed model attempts can emit tool-call chunks, but those
// chunks remain presentation-only until the canonical tool round commits.
func (c *ChatView) AppendOrUpdateToolAttempt(
	toolCallID, toolName, input, attemptID string,
) {
	if toolCallID != "" {
		if existing, ok := c.toolsByID[toolCallID]; ok {
			if attemptID == "" || existing.attemptID == attemptID {
				if toolName != "" {
					existing.name = toolName
				}
				if input != "" && input != "{}" {
					existing.input = input
					existing.updateDescription()
				}
				existing.version++
				c.invalidateContent()
				return
			}
		}
	} else {
		// No ID — try to update the last running tool (streaming argument chunks)
		for i := len(c.items) - 1; i >= 0; i-- {
			if t, ok := c.items[i].(*ToolMessage); ok && t.status == ToolRunning {
				if attemptID != "" && t.attemptID != attemptID {
					continue
				}
				if toolName != "" {
					t.name = toolName
				}
				if input != "" && input != "{}" {
					t.input += input // append streaming args
					t.updateDescription()
				}
				t.version++
				c.invalidateContent()
				return
			}
		}
	}

	tool := &ToolMessage{
		toolCallID: toolCallID,
		name:       toolName,
		input:      input,
		status:     ToolRunning,
		attemptID:  attemptID,
		version:    1,
	}
	tool.updateDescription()
	c.appendChatItem(tool)
	if toolCallID != "" {
		c.toolsByID[toolCallID] = tool
	}
	c.retireCurrentAssistant()
	c.invalidateContent()
}

// AppendToolStart adds a new running tool item, tracked by ID.
func (c *ChatView) AppendToolStart(toolCallID, toolName, input string) {
	tool := &ToolMessage{
		toolCallID: toolCallID,
		name:       toolName,
		input:      input,
		status:     ToolRunning,
		version:    1,
	}
	tool.updateDescription()
	c.appendChatItem(tool)
	if toolCallID != "" {
		c.toolsByID[toolCallID] = tool
	}
	c.retireCurrentAssistant()
	c.invalidateContent()
}

// findTool locates a tool message by ID first, then falls back to last running match by name.
func (c *ChatView) findTool(toolCallID, toolName string) *ToolMessage {
	if toolCallID != "" {
		if t, ok := c.toolsByID[toolCallID]; ok {
			return t
		}
	}
	for i := len(c.items) - 1; i >= 0; i-- {
		if t, ok := c.items[i].(*ToolMessage); ok {
			if t.name == toolName && t.status == ToolRunning {
				return t
			}
		}
	}
	return nil
}

// UpdateToolResult updates a tool with its result.
func (c *ChatView) UpdateToolResult(toolCallID, toolName, result string) {
	tool := c.findTool(toolCallID, toolName)
	if tool != nil {
		tool.output = result
		tool.status = ToolSuccess
		tool.version++
	} else {
		newTool := &ToolMessage{
			toolCallID: toolCallID,
			name:       toolName,
			output:     result,
			status:     ToolSuccess,
			version:    1,
		}
		newTool.updateDescription()
		c.appendChatItem(newTool)
		if toolCallID != "" {
			c.toolsByID[toolCallID] = newTool
		}
	}
	c.retireCurrentAssistant()
	c.tryCollapseTools()
	c.invalidateContent()
}

// UpdateToolProgress shows streaming progress on a running tool (e.g. Bash stdout).
// Updates the tool's output field temporarily so it renders the live output.
func (c *ChatView) UpdateToolProgress(toolCallID, toolName, content string) {
	tool := c.findTool(toolCallID, toolName)
	if tool != nil && tool.status == ToolRunning {
		tool.output = content
		tool.version++
		c.invalidateContent()
	}
}

// UpdateAgentToolTrace attaches one bounded child runtime summary to the
// spawning Agent tool item. Full transcript content stays in Agent detail.
func (c *ChatView) UpdateAgentToolTrace(toolCallID string, trace agentToolTrace) bool {
	tool := c.findTool(toolCallID, "Agent")
	if tool == nil || tool.name != "Agent" {
		return false
	}
	cloned := cloneAgentToolTrace(trace)
	if equalAgentToolTrace(tool.agentTrace, &cloned) {
		return false
	}
	tool.agentTrace = &cloned
	tool.version++
	c.invalidateContent()
	return true
}

func (c *ChatView) agentTraceIdentity(
	toolCallID, agentID, parentToolUseID string,
) (engine.RuntimeExecutionKey, bool, bool) {
	tool := c.findTool(toolCallID, "Agent")
	if tool == nil ||
		tool.agentTrace == nil ||
		tool.agentTrace.AgentID != agentID ||
		tool.agentTrace.ParentToolUseID != parentToolUseID ||
		!tool.agentTrace.IdentityObserved {
		return engine.RuntimeExecutionKey{}, false, false
	}
	return tool.agentTrace.ExecutionKey, true, tool.agentTrace.IdentityResolved
}

// LatestAgentTraceTarget returns the newest exact execution identity represented
// in parent chat. Unresolved retained traces fail closed.
func (c *ChatView) LatestAgentTraceTarget() (engine.RuntimeExecutionKey, bool) {
	for i := len(c.items) - 1; i >= 0; i-- {
		if tool, ok := c.items[i].(*ToolMessage); ok &&
			tool.agentTrace != nil &&
			tool.agentTrace.IdentityResolved {
			return tool.agentTrace.ExecutionKey, true
		}
	}
	return engine.RuntimeExecutionKey{}, false
}

// AgentTraceTargetAtViewportRow resolves clicks on the final underlined link
// line without making the whole tool output consume selection clicks.
func (c *ChatView) AgentTraceTargetAtViewportRow(
	row int,
) (engine.RuntimeExecutionKey, bool) {
	projection := c.currentViewportProjection()
	if projection == nil || row < 0 || row >= len(projection.rows) {
		return engine.RuntimeExecutionKey{}, false
	}
	descriptor := projection.rows[row]
	if descriptor.kind != chatViewportRowTranscript ||
		descriptor.itemIdx < 0 || descriptor.itemIdx >= len(c.items) {
		return engine.RuntimeExecutionKey{}, false
	}
	tool, ok := c.items[descriptor.itemIdx].(*ToolMessage)
	if !ok || tool.agentTrace == nil || !tool.agentTrace.IdentityResolved {
		return engine.RuntimeExecutionKey{}, false
	}
	entry := c.renderItem(tool, c.renderWidth())
	if descriptor.lineInItem != entry.height-1 {
		return engine.RuntimeExecutionKey{}, false
	}
	return tool.agentTrace.ExecutionKey, true
}

// UpdateToolError marks a tool as failed.
func (c *ChatView) UpdateToolError(toolCallID, toolName, errMsg string) {
	tool := c.findTool(toolCallID, toolName)
	if tool != nil {
		tool.output = errMsg
		tool.status = ToolError
		tool.version++
	} else {
		newTool := &ToolMessage{
			toolCallID: toolCallID,
			name:       toolName,
			output:     errMsg,
			status:     ToolError,
			version:    1,
		}
		newTool.updateDescription()
		c.appendChatItem(newTool)
		if toolCallID != "" {
			c.toolsByID[toolCallID] = newTool
		}
	}
	c.invalidateContent()
}

// tryCollapseTools checks if the last 2+ items are consecutive finished
// collapsible tools (Read, Grep, Glob) and replaces them with a ToolGroupMessage.
// If the previous item is already a ToolGroupMessage, the last tool is absorbed into it.
func (c *ChatView) tryCollapseTools() {
	if len(c.items) < 2 {
		return
	}
	last := c.items[len(c.items)-1]
	lastTool, ok := last.(*ToolMessage)
	if !ok || !lastTool.Finished() || !isCollapsibleTool(lastTool.name) {
		return
	}

	// Check if the item before is already a group — absorb into it
	prev := c.items[len(c.items)-2]
	if group, ok := prev.(*ToolGroupMessage); ok {
		group.tools = append(group.tools, lastTool)
		group.version++
		// Remove old render cache for the group (version changed)
		delete(c.renderCache, c.historyCacheKey(group))
		c.forgetHistoryItem(lastTool)
		// Remove the last item (absorbed into group)
		c.items = c.items[:len(c.items)-1]
		return
	}

	// Check if the previous item is also a collapsible finished tool
	prevTool, ok := prev.(*ToolMessage)
	if !ok || !prevTool.Finished() || !isCollapsibleTool(prevTool.name) {
		return
	}

	// Create a new group from the last two items
	group := &ToolGroupMessage{
		tools:   []*ToolMessage{prevTool, lastTool},
		version: 1,
	}
	// Remove both individual items, add the group
	c.items = c.items[:len(c.items)-2]
	c.appendChatItemWithIntent(group, chatAppendHydration)
	// Clean up render cache for replaced items
	c.forgetHistoryItem(prevTool)
	c.forgetHistoryItem(lastTool)
}

// --- Scrolling ---

// ScrollUp scrolls the chat up by n lines.
func (c *ChatView) ScrollUp(n int) {
	if n <= 0 || !c.canScrollAway() {
		return
	}
	if c.followState.following {
		c.computeFollowOffset(c.renderWidth(), c.height)
	}
	c.followState.leave()
	c.invalidateFrame()
	for n > 0 {
		if c.offsetLine > 0 {
			step := n
			if step > c.offsetLine {
				step = c.offsetLine
			}
			c.offsetLine -= step
			n -= step
		} else if c.offsetIdx > 0 {
			c.offsetIdx--
			entry := c.renderItem(c.items[c.offsetIdx], c.renderWidth())
			c.offsetLine = entry.height // position at bottom of prev item
			// Don't consume n here — next iteration will subtract from offsetLine
		} else {
			break // at top
		}
	}
}

// ScrollDown scrolls the chat down by n lines.
func (c *ChatView) ScrollDown(n int) {
	// Already following (at bottom) — nothing to do.
	if n <= 0 || c.followState.following {
		return
	}
	c.invalidateFrame()
	rw := c.renderWidth()
	for n > 0 {
		if c.offsetIdx >= len(c.items) {
			break
		}
		entry := c.renderItem(c.items[c.offsetIdx], rw)
		remaining := entry.height - c.offsetLine
		if n < remaining {
			c.offsetLine += n
			n = 0
		} else {
			n -= remaining
			c.offsetIdx++
			c.offsetLine = 0
			// Skip the gap line
			if n > 0 && c.offsetIdx < len(c.items) {
				n--
			}
		}
	}
	// Clamp at the follow offset: scrolling further only reveals top padding
	// (a mostly blank screen), so snap straight to the bottom instead of
	// rendering degenerate frames before follow recomputes.
	if !c.followState.following {
		maxIdx, maxLine := c.followOffset(rw, c.height)
		if c.offsetIdx >= len(c.items) || c.offsetIdx > maxIdx ||
			(c.offsetIdx == maxIdx && c.offsetLine >= maxLine) {
			c.followState.follow()
			c.offsetIdx = 0
			c.offsetLine = 0
		}
	}
}

// followOffset computes the scroll offset that shows the bottom screenful
// without mutating the view.
func (c *ChatView) followOffset(width, height int) (int, int) {
	totalNeeded := 0
	for i := len(c.items) - 1; i >= 0; i-- {
		entry := c.renderItem(c.items[i], width)
		itemCost := entry.height
		if i < len(c.items)-1 {
			itemCost++ // gap line between items
		}
		totalNeeded += itemCost
		if totalNeeded >= height {
			return i, totalNeeded - height
		}
	}
	return 0, 0
}

// ScrollToTop scrolls to the top.
func (c *ChatView) ScrollToTop() {
	if !c.canScrollAway() {
		return
	}
	c.followState.leave()
	c.offsetIdx = 0
	c.offsetLine = 0
	c.invalidateFrame()
}

// ScrollToBottom scrolls to the bottom.
func (c *ChatView) ScrollToBottom() {
	if c.followState.following {
		return // already at bottom
	}
	c.followState.follow()
	c.invalidateFrame()
}

// ScrollAnimated initiates an animated scroll over multiple frames.
// Small scrolls (|n| <= 5) are applied immediately; larger ones animate.
func (c *ChatView) ScrollAnimated(lines int) {
	if lines > -6 && lines < 6 {
		if lines > 0 {
			c.ScrollDown(lines)
		} else {
			c.ScrollUp(-lines)
		}
		c.scrollRemaining = 0
		return
	}
	c.scrollRemaining = lines
}

// AnimateStep advances the scroll animation by one frame step.
// Returns true if animation is still in progress.
func (c *ChatView) AnimateStep() bool {
	if c.scrollRemaining == 0 {
		return false
	}
	// Scroll a fraction per frame (ease-out feel: larger steps first)
	step := c.scrollRemaining / 3
	if step == 0 {
		if c.scrollRemaining > 0 {
			step = 1
		} else {
			step = -1
		}
	}
	if step > 0 {
		c.ScrollDown(step)
	} else {
		c.ScrollUp(-step)
	}
	c.scrollRemaining -= step
	return c.scrollRemaining != 0
}

// FinishScrollAnimation applies the remaining scroll immediately.
func (c *ChatView) FinishScrollAnimation() {
	remaining := c.scrollRemaining
	c.scrollRemaining = 0
	if remaining > 0 {
		c.ScrollDown(remaining)
	} else if remaining < 0 {
		c.ScrollUp(-remaining)
	}
}

// ScrollToItem scrolls the view so that the item at the given index is visible.
func (c *ChatView) ScrollToItem(idx int) {
	if idx < 0 || idx >= len(c.items) || !c.canScrollAway() {
		return
	}
	maxIdx, maxLine := c.followOffset(c.renderWidth(), c.height)
	if idx == maxIdx && maxLine == 0 {
		return
	}
	c.followState.leave()
	c.offsetIdx = idx
	c.offsetLine = 0
	c.invalidateFrame()
}

func (c *ChatView) canScrollAway() bool {
	if c == nil || len(c.items) == 0 || c.height <= 0 {
		return false
	}
	idx, line := c.followOffset(c.renderWidth(), c.height)
	return idx > 0 || line > 0
}

// Items returns the chat items slice for external iteration (e.g. search).
func (c *ChatView) Items() []ChatItem {
	return c.items
}

// HistoryItems returns the semantic projection in transcript order. The slice
// is defensive; individual items remain owned by ChatView/runtime adapters.
func (c *ChatView) HistoryItems() []HistoryItem {
	items := make([]HistoryItem, 0, len(c.items))
	for _, item := range c.items {
		items = append(items, c.historyItem(item))
	}
	return items
}

// TruncateFrom removes all items from the given index onward.
// Used by the rewrite feature to truncate the visible conversation.
func (c *ChatView) TruncateFrom(fromIdx int) {
	if fromIdx < 0 || fromIdx >= len(c.items) {
		return
	}
	// Clean up render cache for removed items
	for i := fromIdx; i < len(c.items); i++ {
		c.forgetHistoryItem(c.items[i])
		// Also remove from toolsByID if it's a tool message
		if tm, ok := c.items[i].(*ToolMessage); ok && tm.toolCallID != "" {
			delete(c.toolsByID, tm.toolCallID)
		}
	}
	c.items = c.items[:fromIdx]
	c.currentAssistant = nil
	c.currentThinking = nil
	c.followState.follow()
	c.invalidateContent()
}

// ToggleExpand toggles the newest item that exposes semantic expansion.
func (c *ChatView) ToggleExpand() {
	for i := len(c.items) - 1; i >= 0; i-- {
		item, ok := historyCapabilitySource(c.items[i]).(HistoryExpandedItem)
		if !ok {
			continue
		}
		item.ToggleExpanded()
		c.invalidateContent()
		return
	}
}

// GetLastExpandableContent returns the full content and title of the last
// tool or thinking item for the dedicated expand view.
func (c *ChatView) GetLastExpandableContent() (title, content string, ok bool) {
	for i := len(c.items) - 1; i >= 0; i-- {
		item, supported := historyCapabilitySource(c.items[i]).(HistoryExpandedItem)
		if !supported {
			continue
		}
		title, content = item.ExpandedContent()
		return title, content, true
	}
	return "", "", false
}

// RenderAllExpanded renders the full chat history with all tool results and
// thinking blocks expanded. Used for the dedicated expand view.
func (c *ChatView) RenderAllExpanded(width int) string {
	return c.renderAllHistory(width, HistoryRenderExpanded)
}

// RenderAllRaw returns copy-friendly history without ANSI control sequences.
func (c *ChatView) RenderAllRaw(width int) string {
	return c.renderAllHistory(width, HistoryRenderRaw)
}

// RenderAllTranscript returns the semantic transcript projection. Items that
// do not specialize it fall back to their raw projection.
func (c *ChatView) RenderAllTranscript(width int) string {
	return c.renderAllHistory(width, HistoryRenderTranscript)
}

func (c *ChatView) renderAllHistory(width int, mode HistoryRenderMode) string {
	if len(c.items) == 0 {
		return ""
	}
	rw := width - 2
	if rw < 10 {
		rw = 10
	}
	if rw > 120 {
		rw = 120
	}

	var parts []string
	for _, item := range c.items {
		semantic := c.historyItem(item)
		parts = append(parts, renderHistoryItem(semantic, HistoryRenderContext{
			Width:       rw,
			Environment: c.environment,
			Mode:        mode,
		}))
	}
	return strings.Join(parts, "\n\n")
}

// --- Render Cache ---

func (c *ChatView) renderWidth() int {
	rw := c.width - 2
	if rw < 10 {
		rw = 10
	}
	// Cap content width for readability on wide terminals (crush pattern).
	if rw > 120 {
		rw = 120
	}
	return rw
}

func (c *ChatView) renderItem(item ChatItem, width int) *renderCacheEntry {
	semantic := c.historyItem(item)
	cacheKey := c.historyCacheKey(item)
	entry, ok := c.renderCache[cacheKey]
	environment := c.environment.identity()
	fresh := ok && entry.environment == environment
	if fresh && entry.frozen {
		if entry.width == width && entry.version == semantic.Version() {
			return entry
		}
		// Exact width or version changed — re-render even frozen items.
	}

	// When user has scrolled away, use stale cache for streaming items.
	// This makes scroll O(1) during active streaming.
	if !c.followState.following && !semantic.Finished() && fresh && entry.width == width {
		return entry
	}

	ver := semantic.Version()
	if animated, ok := historyCapabilitySource(item).(HistoryAnimatableItem); ok {
		ver = animated.HistoryAnimationVersion(uint64(c.spinnerCount))
	}

	if fresh && entry.version == ver && entry.width == width {
		return entry // cache hit
	}

	// Cache miss — prepare only the item whose animation version changed.
	if animated, ok := historyCapabilitySource(item).(HistoryAnimatableItem); ok {
		animated.PrepareHistoryAnimation(uint64(c.spinnerCount))
	}
	ctx := HistoryRenderContext{
		Width:       width,
		Environment: c.environment,
		Mode:        HistoryRenderRich,
	}
	var (
		lines         []string
		selectionRows []selectionRowMetadata
	)
	if selectable, ok := historyCapabilitySource(item).(historySelectionRenderItem); ok {
		result := selectable.renderSelection(ctx)
		annotatedLines := strings.Split(result.rendered, "\n")
		for index, line := range annotatedLines {
			if result.annotated {
				annotatedLines[index] = selectionTruncateAnnotatedLine(
					c.environment.profile,
					line,
					width,
				)
			} else {
				annotatedLines[index] = c.environment.profile.truncate(
					line,
					width,
				)
			}
		}
		if result.annotated {
			lines, selectionRows, _ = parseSelectionAnnotations(
				c.environment.profile,
				strings.Join(annotatedLines, "\n"),
			)
		} else {
			lines = annotatedLines
		}
	} else if am, isAssistant := item.(*AssistantMessage); isAssistant {
		// Legacy compatibility for out-of-tree items. Built-in selectable
		// renderers implement historySelectionRenderItem.
		lines = am.RenderLinesWithEnvironment(width, c.environment)
	} else {
		lines = strings.Split(renderHistoryItem(semantic, ctx), "\n")
	}
	// Every history row shares the App-selected cell grid, including finished
	// and streaming assistant output. This is a presentation boundary only.
	for i, line := range lines {
		lines[i] = c.environment.profile.truncate(line, width)
	}
	if len(selectionRows) != len(lines) {
		selectionRows = make([]selectionRowMetadata, len(lines))
	}

	// Only freeze if the item is truly done rendering
	canFreeze := semantic.Finished()

	entry = &renderCacheEntry{
		version:     ver,
		width:       width,
		lines:       lines,
		selection:   selectionRows,
		cacheKey:    cacheKey,
		height:      len(lines),
		frozen:      canFreeze,
		themeGen:    c.themeGen,
		geometryGen: c.environment.geometryGen,
		environment: environment,
	}
	c.renderCache[cacheKey] = entry
	return entry
}

// --- O(viewport) Render ---

// Render renders the visible portion of the chat — O(viewport), not O(all items).
func (c *ChatView) Render(width, height int) string {
	if width < 10 {
		width = 10
	}
	chatHeight := height
	environment := c.environment.identity()
	if projection := c.currentViewportProjection(); projection != nil &&
		c.viewCacheEnvironment == environment &&
		c.viewCacheWidth == width && c.viewCacheHeight == chatHeight &&
		projection.width == width && projection.height == chatHeight {
		return c.viewCache
	}
	if len(c.items) == 0 {
		empty := contentRenderStyleBox(
			c.environment.normalized().profile,
			c.styles.Placeholder.Align(lipgloss.Center, lipgloss.Center),
			width,
			height,
			"No messages yet. Type a prompt to begin.",
		)
		c.publishViewportFrame(empty, width, height, make([]chatViewportRow, height), chatPillGeometry{})
		return empty
	}

	renderWidth := c.renderWidth()

	// Sticky prompt header: when scrolled up, show last user prompt at top.
	// Reference: StickyPromptHeader in FullscreenLayout.tsx — 1 fixed line.
	var stickyLine string
	if !c.followState.following {
		if text := c.findLastUserPromptBefore(c.offsetIdx); text != "" {
			// Truncate to width, collapse whitespace
			text = strings.Join(strings.Fields(text), " ")
			maxW := width - 4 // "▎ " prefix + margin
			text = truncateStickyPrompt(c.environment.profile, text, maxW)
			barStyle, bodyStyle := userMessagePanelRunStyles(c.styles)
			stickyLine = contentRenderStyleWidth(
				c.environment.profile,
				c.styles.UserMessageBlock,
				width,
				barStyle.Render(userMessageBarGlyph)+
					bodyStyle.
						Foreground(c.styles.Subtle.GetForeground()).
						Faint(c.styles.Subtle.GetFaint()).
						Render(" "+text),
			)
			height-- // sticky header takes 1 line from budget
		}
	}

	// If follow mode, compute offset to show bottom
	if c.followState.following {
		c.computeFollowOffset(renderWidth, height)
	}

	// Clamp offsetIdx
	if c.offsetIdx >= len(c.items) {
		c.offsetIdx = len(c.items) - 1
		c.offsetLine = 0
	}
	if c.offsetIdx < 0 {
		c.offsetIdx = 0
		c.offsetLine = 0
	}

	// Render only visible items starting from offsetIdx.
	// Pre-allocate to avoid repeated grow.
	budget := height
	lines := make([]string, 0, height)
	rows := make([]chatViewportRow, 0, chatHeight)

	for idx := c.offsetIdx; idx < len(c.items) && budget > 0; idx++ {
		entry := c.renderItem(c.items[idx], renderWidth)

		startLine := 0
		if idx == c.offsetIdx {
			startLine = c.offsetLine
			if startLine >= entry.height {
				startLine = 0
			}
		}

		available := entry.lines[startLine:]
		take := len(available)
		if take > budget {
			take = budget
		}
		lines = append(lines, available[:take]...)
		for line := 0; line < take; line++ {
			rows = append(rows, chatViewportRow{kind: chatViewportRowTranscript, itemIdx: idx, lineInItem: startLine + line})
		}
		budget -= take

		// Gap between items
		if budget > 0 && idx < len(c.items)-1 {
			lines = append(lines, "")
			rows = append(rows, chatViewportRow{kind: chatViewportRowItemGap})
			budget--
		}
	}

	// Pad remainder — bottom-gravity: empty space goes at the TOP
	if len(lines) < height {
		pad := make([]string, height-len(lines))
		lines = append(pad, lines...)
		padding := make([]chatViewportRow, len(pad))
		for i := range padding {
			padding[i].kind = chatViewportRowPadding
		}
		rows = append(padding, rows...)
	}

	if stickyLine != "" {
		lines = append([]string{stickyLine}, lines...)
		rows = append([]chatViewportRow{{kind: chatViewportRowSticky}}, rows...)
	}

	// New messages pill overlays its published final row using shared
	// profile-cell geometry. Sticky headers are already part of this rectangle.
	if geometry := c.pillGeometry(width, chatHeight); geometry.visible &&
		geometry.row >= 0 && geometry.row < len(lines) {
		lines[geometry.row] = geometry.renderedLine
		rows[geometry.row] = chatViewportRow{kind: chatViewportRowPill}
	}

	result := strings.Join(lines, "\n")
	c.publishViewportFrame(result, width, chatHeight, rows, c.pillGeometry(width, chatHeight))
	return result
}

func (c *ChatView) publishViewportFrame(frame string, width, height int, rows []chatViewportRow, pill chatPillGeometry) {
	if len(rows) != height {
		rows = make([]chatViewportRow, height)
		for i := range rows {
			rows[i].kind = chatViewportRowEmpty
		}
	}
	publishedRows := append([]chatViewportRow(nil), rows...)
	if c.frameGeneration < ^uint64(0) {
		c.frameGeneration++
	}
	if c.contentDirty && c.contentGeneration < ^uint64(0) {
		c.contentGeneration++
	}
	first, last := -1, -1
	for i, row := range publishedRows {
		if row.kind == chatViewportRowTranscript {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	contentRect := layoutRect{Width: width}
	if first >= 0 {
		contentRect.Y, contentRect.Height = first, last-first+1
	}
	projection := &chatViewportProjection{
		width: width, height: height, environment: c.environment.identity(),
		frameGen: c.frameGeneration, contentGen: c.contentGeneration,
		contentRect: contentRect, rows: publishedRows, pill: pill,
	}
	c.viewCache, c.viewCacheEnvironment = frame, projection.environment
	c.viewCacheWidth, c.viewCacheHeight = width, height
	c.viewportProjection = projection
	c.viewDirty = false
	c.contentDirty = false
}

// findLastUserPromptBefore walks backward from idx-1 to find the most recent
// user message above the viewport. The item at idx is already visible and must
// not also be rendered as a sticky header.
func (c *ChatView) findLastUserPromptBefore(idx int) string {
	for i := idx - 1; i >= 0; i-- {
		if um, ok := c.items[i].(*UserMessage); ok {
			return um.content
		}
	}
	return ""
}

// computeFollowOffset walks from the end to find the scroll position that shows the bottom.
// Since renderItem caches results, repeated calls within the same frame for the same
// version are O(1) map lookups. Only the actively streaming item re-renders.
func (c *ChatView) computeFollowOffset(width, height int) {
	c.offsetIdx, c.offsetLine = c.followOffset(width, height)
}

// --- Message Types ---

// UserMessage represents a user input.
type UserMessage struct {
	content          string
	composerElements []threadComposerElement
}

const userMessageBarGlyph = "▎"

func userMessagePanelRunStyles(styles Styles) (bar, body lipgloss.Style) {
	body = styles.UserMessageBlock.Padding(0)
	bar = body.
		Foreground(styles.UserPrefix.GetForeground()).
		Bold(styles.UserPrefix.GetBold())
	return bar, body
}

func (m *UserMessage) Render(width int, styles Styles) string {
	return m.RenderWithEnvironment(width, defaultRenderEnvironment(styles))
}

func (m *UserMessage) RenderWithEnvironment(
	width int,
	env RenderEnvironment,
) string {
	env = env.normalized()
	styles := env.styles
	content := m.content
	contentWidth := width - 4
	if contentWidth < 10 {
		contentWidth = 10
	}
	if env.profile.width(content) > contentWidth {
		content = wrapTextWithProfile(env.profile, content, contentWidth)
	}
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	barStyle, bodyStyle := userMessagePanelRunStyles(styles)
	bar := barStyle.Render(userMessageBarGlyph)
	for _, line := range lines {
		result = append(result, bar+bodyStyle.Render(" "+line))
	}
	inner := strings.Join(result, "\n")
	return contentRenderStyleWidth(env.profile, styles.UserMessageBlock, width, inner)
}

func (m *UserMessage) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	ctx = ctx.normalized()
	env := ctx.Environment
	contentWidth := ctx.Width - 4
	if contentWidth < 10 {
		contentWidth = 10
	}
	content, ok := selectionAnnotatedWrappedText(
		env.profile,
		m.content,
		contentWidth,
	)
	if !ok {
		return selectionAnnotatedRender{
			rendered: m.RenderWithEnvironment(ctx.Width, env),
		}
	}
	lines := strings.Split(content, "\n")
	result := make([]string, 0, len(lines))
	barStyle, bodyStyle := userMessagePanelRunStyles(env.styles)
	bar := selectionPresentation(
		barStyle.Render(userMessageBarGlyph) + bodyStyle.Render(" "),
	)
	for _, line := range lines {
		result = append(result, bar+bodyStyle.Render(line))
	}
	return selectionAnnotatedRender{
		rendered: contentRenderStyleWidth(
			env.profile,
			env.styles.UserMessageBlock,
			ctx.Width,
			strings.Join(result, "\n"),
		),
		annotated: true,
	}
}

func (m *UserMessage) Finished() bool  { return true }
func (m *UserMessage) Version() uint64 { return 1 }

func (m *UserMessage) RenderRaw(HistoryRenderContext) string { return m.content }

// ThinkingMessage represents streamed model reasoning content.
type ThinkingMessage struct {
	attemptID  string
	builder    strings.Builder
	content    string
	startedAt  time.Time
	finishedAt time.Time
	finished   bool
	expanded   bool
	version    uint64
}

func (m *ThinkingMessage) AppendDelta(delta string) {
	m.builder.WriteString(delta)
	m.content = m.builder.String()
	m.version++
}

func (m *ThinkingMessage) Render(width int, styles Styles) string {
	return m.RenderWithEnvironment(width, defaultRenderEnvironment(styles))
}

func (m *ThinkingMessage) RenderWithEnvironment(
	width int,
	env RenderEnvironment,
) string {
	return m.renderWithEnvironment(width, env, m.expanded)
}

func (m *ThinkingMessage) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	ctx = ctx.normalized()
	return m.renderWithEnvironmentMode(
		ctx.Width,
		ctx.Environment,
		m.expanded,
		true,
	)
}

func (m *ThinkingMessage) renderWithEnvironment(
	width int,
	env RenderEnvironment,
	expanded bool,
) string {
	return m.renderWithEnvironmentMode(
		width,
		env,
		expanded,
		false,
	).rendered
}

func (m *ThinkingMessage) renderWithEnvironmentMode(
	width int,
	env RenderEnvironment,
	expanded bool,
	selection bool,
) selectionAnnotatedRender {
	env = env.normalized()
	styles := env.styles
	if selection && selectionAnnotationsCollide(m.content) {
		return m.renderWithEnvironmentMode(width, env, expanded, false)
	}
	if m.startedAt.IsZero() {
		m.startedAt = time.Now()
	}
	elapsed := time.Since(m.startedAt)
	if m.finished && !m.finishedAt.IsZero() {
		elapsed = m.finishedAt.Sub(m.startedAt)
	}
	if elapsed < 0 {
		elapsed = 0
	}
	summary := fmt.Sprintf("  Thinking (%s)", formatDurationShort(elapsed))
	if m.finished && !expanded {
		if selection {
			summary = selectionPresentation("  ") +
				selectionSemantic(strings.TrimPrefix(summary, "  "))
		}
		return selectionAnnotatedRender{
			rendered:  styles.Subtle.Render(summary),
			annotated: selection,
		}
	}

	header := styles.Subtle.Render("  Thinking...")
	if selection {
		header = selectionPresentation(styles.Subtle.Render("  ")) +
			styles.Subtle.Render(selectionSemantic("Thinking..."))
	}
	contentWidth := width - 4
	if contentWidth < 10 {
		contentWidth = 10
	}
	source := strings.TrimSpace(m.content)
	body := wrapTextWithProfile(env.profile, source, contentWidth)
	if selection {
		var ok bool
		body, ok = selectionAnnotatedWrappedText(
			env.profile,
			source,
			contentWidth,
		)
		if !ok {
			return m.renderWithEnvironmentMode(width, env, expanded, false)
		}
	}
	if body == "" {
		return selectionAnnotatedRender{
			rendered: header, annotated: selection,
		}
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if selection {
			lines[i] = selectionPresentation("    ") +
				styles.Subtle.Render(line)
		} else {
			lines[i] = "    " + styles.Subtle.Render(line)
		}
	}
	if m.finished {
		if selection {
			lines[len(lines)-1] += selectionMarkHardBoundary
			lines = append(
				lines,
				selectionPresentation("    ")+
					styles.Subtle.Render(
						selectionSemantic("(expand for details)"),
					),
			)
		} else {
			lines = append(
				lines,
				"    "+styles.Subtle.Render("(expand for details)"),
			)
		}
	}
	separator := "\n"
	if selection {
		separator = selectionHardBreak()
	}
	return selectionAnnotatedRender{
		rendered:  header + separator + strings.Join(lines, "\n"),
		annotated: selection,
	}
}

func (m *ThinkingMessage) Finished() bool  { return m.finished }
func (m *ThinkingMessage) Version() uint64 { return m.version }

func (m *ThinkingMessage) RenderRaw(HistoryRenderContext) string {
	return m.content
}

func (m *ThinkingMessage) RenderExpanded(ctx HistoryRenderContext) string {
	ctx = ctx.normalized()
	return m.renderWithEnvironment(ctx.Width, ctx.Environment, true)
}

func (m *ThinkingMessage) Expanded() bool { return m.expanded }

func (m *ThinkingMessage) ToggleExpanded() bool {
	m.expanded = !m.expanded
	m.version++
	return m.expanded
}

func (m *ThinkingMessage) ExpandedContent() (string, string) {
	return "Thinking", m.content
}

func (m *ThinkingMessage) RenderTranscript(HistoryRenderContext) string {
	return m.content
}

func formatDurationShort(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// AssistantMessage represents an AI response (may be streaming).
type AssistantMessage struct {
	attemptID string
	builder   strings.Builder // accumulates streaming deltas efficiently
	content   string          // materialized content
	finished  bool
	version   uint64

	// Per-message streaming markdown renderer (crush pattern: synchronous
	// prefix-cached glamour). Each message owns its own cache so that
	// streaming flushes only re-render the trailing partial.
	streamingMd StreamingMarkdown
}

// AppendDelta efficiently appends a streaming delta.
func (m *AssistantMessage) AppendDelta(delta string) {
	m.builder.WriteString(delta)
	m.content = m.builder.String()
	m.version++
}

// ReplaceContent applies an authoritative full-message snapshot while keeping
// subsequent deltas consistent with the same source buffer.
func (m *AssistantMessage) ReplaceContent(content string) {
	m.builder.Reset()
	m.builder.WriteString(content)
	m.content = content
	m.finished = false
	m.streamingMd.Reset()
	m.version++
}

// Finalize seals the append-only stream and invalidates stitched Markdown
// fragments so the terminal item is rendered canonically from m.content.
func (m *AssistantMessage) Finalize() bool {
	if m.finished {
		return false
	}
	m.finished = true
	m.streamingMd.Finalize(m.content)
	m.version++
	return true
}

func (m *AssistantMessage) Render(width int, styles Styles) string {
	lines := m.RenderLines(width, styles)
	return strings.Join(lines, "\n")
}

func (m *AssistantMessage) RenderWithEnvironment(width int, env RenderEnvironment) string {
	return strings.Join(m.RenderLinesWithEnvironment(width, env), "\n")
}

// RenderLines returns pre-split lines to avoid Join→Split roundtrip in renderItem.
// Uses crush's synchronous per-message streamingMarkdown: the prefix cache makes
// re-rendering cheap enough to do in the render path without async glamour.
func (m *AssistantMessage) RenderLines(width int, styles Styles) []string {
	return m.RenderLinesWithEnvironment(width, defaultRenderEnvironment(styles))
}

func (m *AssistantMessage) RenderLinesWithEnvironment(width int, env RenderEnvironment) []string {
	env = env.normalized()
	styles := env.styles
	if m.content == "" {
		return []string{"  " + styles.Subtle.Render("...")}
	}

	// Cap content width for readability (crush pattern: cappedMessageWidth)
	contentWidth := width - 4
	if contentWidth > 120 {
		contentWidth = 120
	}
	if contentWidth < 10 {
		contentWidth = 10
	}

	// Synchronous glamour render via per-message streaming cache.
	// During streaming, the prefix cache means only the trailing partial
	// is re-rendered each flush. Once finished, the full result is cached.
	rendered := m.streamingMd.renderWithEnvironment(m.content, contentWidth, env)

	// Format: filled-star brand prefix on the first line, indented continuation.
	renderedLines := strings.Split(rendered, "\n")
	result := make([]string, 0, len(renderedLines))
	for i, line := range renderedLines {
		line = env.profile.truncate(line, contentWidth)
		if i == 0 {
			result = append(result, styles.AssistantPrefix.Render(assistantIdentityGlyph)+" "+line)
		} else {
			result = append(result, "  "+line)
		}
	}
	return result
}

func (m *AssistantMessage) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	ctx = ctx.normalized()
	env := ctx.Environment
	if m.content == "" {
		return selectionAnnotatedRender{
			rendered: selectionPresentation("  ") +
				env.styles.Subtle.Render(selectionSemantic("...")),
			annotated: true,
		}
	}
	contentWidth := ctx.Width - 4
	if contentWidth > 120 {
		contentWidth = 120
	}
	if contentWidth < 10 {
		contentWidth = 10
	}
	rendered, annotated := m.streamingMd.renderSelectionWithEnvironment(
		m.content,
		contentWidth,
		env,
	)
	renderedLines := strings.Split(rendered, "\n")
	result := make([]string, 0, len(renderedLines))
	for index, line := range renderedLines {
		line = selectionTruncateAnnotatedLine(
			env.profile,
			line,
			contentWidth,
		)
		if index == 0 {
			result = append(
				result,
				selectionPresentation(
					env.styles.AssistantPrefix.Render(
						assistantIdentityGlyph,
					)+" ",
				)+line,
			)
		} else {
			result = append(result, selectionPresentation("  ")+line)
		}
	}
	return selectionAnnotatedRender{
		rendered:  strings.Join(result, "\n"),
		annotated: annotated,
	}
}

func (m *AssistantMessage) Finished() bool  { return m.finished }
func (m *AssistantMessage) Version() uint64 { return m.version }

func (m *AssistantMessage) RenderRaw(HistoryRenderContext) string {
	return m.content
}

// SystemMessage represents an informational message.
type SystemMessage struct {
	content string
}

func (m *SystemMessage) Render(width int, styles Styles) string {
	return styles.SystemMessage.Render(systemIdentityGlyph + " " + m.content)
}

func (m *SystemMessage) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	ctx = ctx.normalized()
	if selectionAnnotationsCollide(m.content) {
		return selectionAnnotatedRender{rendered: m.Render(ctx.Width, ctx.Styles)}
	}
	return selectionAnnotatedRender{
		rendered: ctx.Styles.SystemMessage.Render(
			selectionPresentation(systemIdentityGlyph+" ") +
				selectionSemantic(selectionAnnotateTabs(m.content)),
		),
		annotated: true,
	}
}

func (m *SystemMessage) Finished() bool  { return true }
func (m *SystemMessage) Version() uint64 { return 1 }

func (m *SystemMessage) RenderRaw(HistoryRenderContext) string { return m.content }

// CompactBoundaryMessage represents a compaction boundary with dedicated styling.
// Renders a visually distinctive summary when context compaction occurs, showing
// key metrics: messages removed, tokens freed, and post-compact context usage.
type CompactBoundaryMessage struct {
	stats string // optional stats string (used for manual /compact command result)

	// Structured compaction metadata (populated by auto-compact events).
	messagesCompacted int // number of messages removed by compaction
	tokensBefore      int // estimated tokens before compaction (0 = unknown)
	tokensAfter       int // estimated tokens after compaction (0 = unknown)
	contextPercent    int // post-compact context usage percentage (0 = unknown)
}

func (m *CompactBoundaryMessage) Render(width int, styles Styles) string {
	// Build the separator line to available width.
	sepWidth := width
	if sepWidth > 50 {
		sepWidth = 50
	}
	if sepWidth < 10 {
		sepWidth = 10
	}
	sep := styles.Dim.Render(strings.Repeat("\u2500", sepWidth))

	// Title line.
	icon := styles.SystemMessage.Render("\u2726")
	title := styles.Bold.Render("Context compacted")
	hint := styles.Subtle.Render("(expand for history)")
	header := icon + " " + title + " " + hint

	// Build detail line with available metrics.
	var parts []string
	if m.messagesCompacted > 0 {
		parts = append(parts, fmt.Sprintf("%d messages removed", m.messagesCompacted))
	}
	if m.tokensBefore > 0 && m.tokensAfter > 0 {
		freed := m.tokensBefore - m.tokensAfter
		if freed > 0 {
			parts = append(parts, fmt.Sprintf("~%s tokens freed", formatTokensShort(freed)))
		}
	}
	if m.contextPercent > 0 {
		parts = append(parts, fmt.Sprintf("context now %d%%", m.contextPercent))
	}

	// Fallback to the raw stats string if no structured data.
	detail := strings.Join(parts, " \u00b7 ")
	if detail == "" && m.stats != "" {
		detail = m.stats
	}

	var sb strings.Builder
	sb.WriteString(sep)
	sb.WriteByte('\n')
	sb.WriteString(header)
	if detail != "" {
		sb.WriteString("\n  ")
		sb.WriteString(styles.Dim.Render(detail))
	}
	sb.WriteByte('\n')
	sb.WriteString(sep)
	return sb.String()
}

func (m *CompactBoundaryMessage) Finished() bool  { return true }
func (m *CompactBoundaryMessage) Version() uint64 { return 1 }

func (m *CompactBoundaryMessage) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	ctx = ctx.normalized()
	rendered := m.Render(ctx.Width, ctx.Styles)
	prefixes := []int{-1, 2, -1}
	if m.messagesCompacted > 0 ||
		(m.tokensBefore > 0 && m.tokensAfter > 0 &&
			m.tokensBefore > m.tokensAfter) ||
		m.contextPercent > 0 || m.stats != "" {
		prefixes = []int{-1, 2, 2, -1}
	}
	annotated, ok := selectionAnnotateKnownRows(
		ctx.displayCellProfile(),
		rendered,
		prefixes,
	)
	if !ok {
		return selectionAnnotatedRender{rendered: rendered}
	}
	return selectionAnnotatedRender{rendered: annotated, annotated: true}
}

// formatTokensShort formats a token count compactly for display (e.g., "12k", "1.2M").
func formatTokensShort(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 10_000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}

// InterruptionMessage represents a user-initiated interruption with dedicated styling.
// Rendered as: ⏎ Request interrupted.
type InterruptionMessage struct{}

func (m *InterruptionMessage) Render(width int, styles Styles) string {
	return styles.Warning.Render("⏎") + " " + styles.Subtle.Render("Request interrupted.")
}

func (m *InterruptionMessage) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	ctx = ctx.normalized()
	return selectionAnnotatedRender{
		rendered: selectionPresentation(
			ctx.Styles.Warning.Render("⏎")+" ",
		) + ctx.Styles.Subtle.Render(selectionSemantic("Request interrupted.")),
		annotated: true,
	}
}

func (m *InterruptionMessage) Finished() bool  { return true }
func (m *InterruptionMessage) Version() uint64 { return 1 }

// CompactSummaryMessage renders a structured summary header for resumed sessions.
// Reference: "● Summarized conversation" with metadata.
type CompactSummaryMessage struct {
	messageCount int
}

func (m *CompactSummaryMessage) Render(width int, styles Styles) string {
	icon := styles.ToolSuccess.Render("●")
	title := styles.Bold.Render("Summarized conversation")
	header := icon + " " + title
	detail := fmt.Sprintf("Summarized %d messages up to this point", m.messageCount)
	body := "  ⎿  " + styles.Subtle.Render(detail) + "\n" +
		"     " + styles.Subtle.Render("(expand for history)")
	return header + "\n" + body
}

func (m *CompactSummaryMessage) Finished() bool  { return true }
func (m *CompactSummaryMessage) Version() uint64 { return 1 }

func (m *CompactSummaryMessage) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	ctx = ctx.normalized()
	rendered := m.Render(ctx.Width, ctx.Styles)
	annotated, ok := selectionAnnotateKnownRows(
		ctx.displayCellProfile(),
		rendered,
		[]int{2, 5, 5},
	)
	if !ok {
		return selectionAnnotatedRender{rendered: rendered}
	}
	return selectionAnnotatedRender{rendered: annotated, annotated: true}
}

// HelpEntry is a single row in the help menu.
type HelpEntry struct {
	Name string
	Desc string
}

// HelpMessage renders a styled help menu with aligned columns.
type HelpMessage struct {
	entries []HelpEntry
}

func (m *HelpMessage) Render(width int, styles Styles) string {
	if len(m.entries) == 0 {
		return styles.SystemMessage.Render(systemIdentityGlyph + " No commands available")
	}
	// Find max name width for alignment
	maxName := 0
	for _, e := range m.entries {
		if len(e.Name) > maxName {
			maxName = len(e.Name)
		}
	}

	var sb strings.Builder
	sb.WriteString(styles.Bold.Render("Available commands:"))
	sb.WriteByte('\n')
	for _, e := range m.entries {
		name := "/" + e.Name
		pad := maxName - len(e.Name) + 2
		if pad < 2 {
			pad = 2
		}
		line := "  " + styles.Bold.Render(name) + strings.Repeat(" ", pad) + styles.Subtle.Render(e.Desc)
		sb.WriteString(line)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (m *HelpMessage) Finished() bool  { return true }
func (m *HelpMessage) Version() uint64 { return 1 }

func (m *HelpMessage) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	ctx = ctx.normalized()
	for _, entry := range m.entries {
		if selectionAnnotationsCollide(entry.Name, entry.Desc) {
			return selectionAnnotatedRender{
				rendered: m.Render(ctx.Width, ctx.Styles),
			}
		}
	}
	rendered := m.Render(ctx.Width, ctx.Styles)
	prefixes := make([]int, len(m.entries)+1)
	if len(m.entries) == 0 {
		prefixes = []int{4}
	} else {
		for index := 1; index < len(prefixes); index++ {
			prefixes[index] = 2
		}
	}
	annotated, ok := selectionAnnotateKnownRows(
		ctx.displayCellProfile(),
		rendered,
		prefixes,
	)
	if !ok {
		return selectionAnnotatedRender{rendered: rendered}
	}
	return selectionAnnotatedRender{rendered: annotated, annotated: true}
}

// ToolGroupMessage groups consecutive collapsible tool calls (Read, Grep, Glob)
// into a single summary line when collapsed, or shows individual items when expanded.
// Reference: CollapsedReadSearchContent.tsx
type ToolGroupMessage struct {
	tools    []*ToolMessage
	expanded bool
	version  uint64
}

func (m *ToolGroupMessage) Render(width int, styles Styles) string {
	if m.expanded {
		return m.renderExpanded(width, styles)
	}
	return m.renderCompact(styles)
}

func (m *ToolGroupMessage) renderCompact(styles Styles) string {
	if len(m.tools) == 0 {
		return ""
	}
	if isReadSearchToolGroup(m.tools) {
		return renderReadSearchGroupSummary(m.tools, styles)
	}
	// Build summary: count by tool name
	counts := make(map[string]int)
	for _, t := range m.tools {
		counts[t.name]++
	}
	var parts []string
	// Deterministic order: Read, Grep, Glob, then others
	for _, name := range []string{"Read", "Grep", "Glob"} {
		if n, ok := counts[name]; ok {
			unit := "file"
			if name == "Grep" { //nolint:staticcheck
				unit = "pattern"
			} else if name == "Glob" {
				unit = "pattern"
			}
			if n > 1 {
				unit += "s"
			}
			parts = append(parts, fmt.Sprintf("%s %d %s", name, n, unit))
			delete(counts, name)
		}
	}
	for name, n := range counts {
		parts = append(parts, fmt.Sprintf("%s ×%d", name, n))
	}

	icon := styles.ToolSuccess.Render("●")
	summary := styles.Subtle.Render(strings.Join(parts, ", "))
	return fmt.Sprintf("%s %s", icon, summary)
}

func (m *ToolGroupMessage) renderExpanded(width int, styles Styles) string {
	parts := make([]string, 0, len(m.tools))
	for _, tool := range m.tools {
		parts = append(parts, tool.render(width, styles, true))
	}
	return strings.Join(parts, "\n\n")
}

func (m *ToolGroupMessage) Finished() bool  { return true }
func (m *ToolGroupMessage) Version() uint64 { return m.version }

func (m *ToolGroupMessage) RenderRaw(ctx HistoryRenderContext) string {
	return m.RenderTranscript(ctx)
}

func (m *ToolGroupMessage) RenderCompact(ctx HistoryRenderContext) string {
	return m.renderCompact(ctx.Styles)
}

func (m *ToolGroupMessage) RenderExpanded(ctx HistoryRenderContext) string {
	return m.renderExpanded(ctx.Width, ctx.Styles)
}

func (m *ToolGroupMessage) Expanded() bool { return m.expanded }

func (m *ToolGroupMessage) ToggleExpanded() bool {
	m.expanded = !m.expanded
	m.version++
	return m.expanded
}

func (m *ToolGroupMessage) ExpandedContent() (string, string) {
	parts := make([]string, 0, len(m.tools))
	for _, tool := range m.tools {
		_, content := tool.ExpandedContent()
		parts = append(parts, content)
	}
	return "Grouped tools", strings.Join(parts, "\n\n")
}

func (m *ToolGroupMessage) RenderTranscript(ctx HistoryRenderContext) string {
	parts := make([]string, 0, len(m.tools))
	for _, tool := range m.tools {
		parts = append(parts, tool.RenderTranscript(ctx))
	}
	return strings.Join(parts, "\n\n")
}

func (m *ToolGroupMessage) NestedHistoryItems() []HistoryItem {
	items := make([]HistoryItem, 0, len(m.tools))
	for i, tool := range m.tools {
		id := fmt.Sprintf("group-tool:%d", i)
		if tool.toolCallID != "" {
			id = fmt.Sprintf("group-tool:%s:%d", tool.toolCallID, i)
		}
		items = append(items, adaptChatItem(id, tool))
	}
	return items
}

func (m *ToolGroupMessage) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	ctx = ctx.normalized()
	for _, tool := range m.tools {
		if selectionAnnotationsCollide(
			tool.input,
			tool.output,
			tool.description,
		) {
			return selectionAnnotatedRender{
				rendered: m.Render(ctx.Width, ctx.Styles),
			}
		}
	}
	if m.expanded {
		parts := make([]string, 0, len(m.tools))
		for _, tool := range m.tools {
			result := tool.renderSelectionHistory(ctx, true)
			if !result.annotated {
				return selectionAnnotatedRender{
					rendered: m.Render(ctx.Width, ctx.Styles),
				}
			}
			parts = append(parts, result.rendered)
		}
		return selectionAnnotatedRender{
			rendered: strings.Join(
				parts,
				selectionMarkHardBoundary+"\n\n",
			),
			annotated: true,
		}
	}
	rendered := m.Render(ctx.Width, ctx.Styles)
	annotated, ok := selectionAnnotateRenderedRows(
		ctx.displayCellProfile(),
		rendered,
		2,
	)
	if !ok {
		return selectionAnnotatedRender{rendered: rendered}
	}
	return selectionAnnotatedRender{rendered: annotated, annotated: true}
}

// isCollapsibleTool returns true for tools that can be grouped into a collapsed summary.
func isCollapsibleTool(name string) bool {
	switch name {
	case "Read", "Grep", "Glob":
		return true
	}
	return false
}

// ToolStatus represents the state of a tool execution.
type ToolStatus int

const (
	ToolPending ToolStatus = iota
	ToolRunning
	ToolSuccess
	ToolError
)

// ToolMessage represents a tool invocation and its result.
type ToolMessage struct {
	toolCallID   string
	attemptID    string
	name         string
	input        string
	output       string
	status       ToolStatus
	expanded     bool
	description  string // cached description (parsed from input JSON once)
	version      uint64
	spinnerCount int // set by ChatView before rendering
	agentTrace   *agentToolTrace
}

func (m *ToolMessage) Render(width int, styles Styles) string {
	return m.render(width, styles, m.expanded)
}

func (m *ToolMessage) render(width int, styles Styles, expanded bool) string {
	return m.renderHistory(HistoryRenderContext{
		Width:  width,
		Styles: styles,
		Mode:   HistoryRenderRich,
	}, expanded)
}

func (m *ToolMessage) RenderWithEnvironment(
	width int,
	env RenderEnvironment,
) string {
	env = env.normalized()
	return m.renderHistory(HistoryRenderContext{
		Width:       width,
		Styles:      env.styles,
		Environment: env,
		Mode:        HistoryRenderRich,
	}, m.expanded)
}

func (m *ToolMessage) renderHistory(ctx HistoryRenderContext, expanded bool) string {
	return toolHistoryRendererFor(m.name).Render(toolHistoryRenderState{
		Context:       ctx,
		Name:          m.name,
		Input:         m.input,
		Output:        m.output,
		Status:        m.status,
		DisplayStatus: m.displayStatus(),
		Expanded:      expanded,
		SpinnerCount:  m.spinnerCount,
		AgentTrace:    m.agentTrace,
	})
}

func (m *ToolMessage) Finished() bool {
	if m.agentTrace != nil && m.agentTrace.active() {
		return false
	}
	return m.status == ToolSuccess || m.status == ToolError
}

func (m *ToolMessage) visuallyRunning() bool {
	if m.agentTrace != nil {
		return m.agentTrace.Status == "running" || m.agentTrace.Status == "waiting_input"
	}
	return m.status == ToolRunning
}

func (m *ToolMessage) displayStatus() ToolStatus {
	if m.agentTrace == nil {
		return m.status
	}
	switch m.agentTrace.Status {
	case "running", "waiting_input":
		return ToolRunning
	case "paused":
		return ToolPending
	case "failed", "aborted", "killed":
		return ToolError
	case "completed":
		return ToolSuccess
	default:
		return m.status
	}
}

func (m *ToolMessage) Version() uint64 {
	return m.version
}

func (m *ToolMessage) RenderRaw(ctx HistoryRenderContext) string {
	ctx.Mode = HistoryRenderRaw
	return m.renderHistory(ctx, true)
}

func (m *ToolMessage) RenderCompact(ctx HistoryRenderContext) string {
	ctx.Mode = HistoryRenderCompact
	return m.renderHistory(ctx, false)
}

func (m *ToolMessage) RenderExpanded(ctx HistoryRenderContext) string {
	ctx.Mode = HistoryRenderExpanded
	return m.renderHistory(ctx, true)
}

func (m *ToolMessage) Expanded() bool { return m.expanded }

func (m *ToolMessage) ToggleExpanded() bool {
	m.expanded = !m.expanded
	m.version++
	return m.expanded
}

func (m *ToolMessage) ExpandedContent() (string, string) {
	return "Tool: " + m.name, m.output
}

func (m *ToolMessage) RenderTranscript(ctx HistoryRenderContext) string {
	ctx.Mode = HistoryRenderTranscript
	return m.renderHistory(ctx, true)
}

func (m *ToolMessage) NestedHistoryItems() []HistoryItem {
	return agentNestedHistoryItems(m)
}

func (m *ToolMessage) renderSelection(
	ctx HistoryRenderContext,
) selectionAnnotatedRender {
	return m.renderSelectionHistory(ctx, m.expanded)
}

func (m *ToolMessage) renderSelectionHistory(
	ctx HistoryRenderContext,
	expanded bool,
) selectionAnnotatedRender {
	ctx = ctx.normalized()
	if selectionAnnotationsCollide(m.input, m.output, m.description) {
		return selectionAnnotatedRender{
			rendered: m.renderHistory(ctx, expanded),
		}
	}
	ctx.selection = true
	rendered := m.renderHistory(ctx, expanded)
	annotated, ok := selectionAnnotateRenderedRows(
		ctx.displayCellProfile(),
		rendered,
		m.NoSelectPrefix(),
	)
	if !ok {
		return selectionAnnotatedRender{rendered: rendered}
	}
	return selectionAnnotatedRender{rendered: annotated, annotated: true}
}

func (m *ToolMessage) PrepareHistoryAnimation(frame uint64) {
	m.spinnerCount = int(frame)
}

func (m *ToolMessage) HistoryAnimationVersion(frame uint64) uint64 {
	if m.visuallyRunning() {
		return m.version + frame
	}
	return m.version
}

// updateDescription parses tool input JSON once and caches the description.
func (m *ToolMessage) updateDescription() {
	m.description = getToolDescription(m.name, m.input, m.status)
}

func truncateStickyPrompt(profile DisplayCellProfile, text string, width int) string {
	if width <= 3 || profile.width(text) <= width {
		return text
	}
	tail := profile.truncate("…", width)
	return profile.truncate(text, width-profile.width(tail)) + tail
}

// --- Helpers ---

func truncate(s string, max int) string {
	if max <= 3 {
		return "..."
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-3]) + "..."
}

func wrapTextWithProfile(
	profile DisplayCellProfile,
	s string,
	width int,
) string {
	if width <= 0 {
		return s
	}
	var result strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if profile.width(line) <= width {
			if result.Len() > 0 {
				result.WriteByte('\n')
			}
			result.WriteString(line)
			continue
		}
		words := strings.Fields(line)
		currentLine := ""
		currentLen := 0
		for _, word := range words {
			wordLen := profile.width(word)
			if currentLine == "" {
				currentLine = word
				currentLen = wordLen
			} else if currentLen+1+wordLen <= width {
				currentLine += " " + word
				currentLen += 1 + wordLen
			} else {
				if result.Len() > 0 {
					result.WriteByte('\n')
				}
				result.WriteString(currentLine)
				currentLine = word
				currentLen = wordLen
			}
		}
		if currentLine != "" {
			if result.Len() > 0 {
				result.WriteByte('\n')
			}
			result.WriteString(currentLine)
		}
	}
	return result.String()
}

// NoSelectPrefix implementations — tool messages exclude the 5-char gutter.
func (m *UserMessage) NoSelectPrefix() int            { return 0 }
func (m *ThinkingMessage) NoSelectPrefix() int        { return 0 }
func (m *AssistantMessage) NoSelectPrefix() int       { return 0 }
func (m *SystemMessage) NoSelectPrefix() int          { return 0 }
func (m *CompactBoundaryMessage) NoSelectPrefix() int { return 0 }
func (m *InterruptionMessage) NoSelectPrefix() int    { return 0 }
func (m *CompactSummaryMessage) NoSelectPrefix() int  { return 0 }
func (m *HelpMessage) NoSelectPrefix() int            { return 0 }
func (m *ToolGroupMessage) NoSelectPrefix() int       { return 5 }
func (m *ToolMessage) NoSelectPrefix() int            { return 5 }

func (m *UserMessage) Selectable() bool            { return true }
func (m *ThinkingMessage) Selectable() bool        { return true }
func (m *AssistantMessage) Selectable() bool       { return true }
func (m *SystemMessage) Selectable() bool          { return true }
func (m *CompactBoundaryMessage) Selectable() bool { return true }
func (m *InterruptionMessage) Selectable() bool    { return true }
func (m *CompactSummaryMessage) Selectable() bool  { return true }
func (m *HelpMessage) Selectable() bool            { return true }
func (m *ToolGroupMessage) Selectable() bool       { return true }
func (m *ToolMessage) Selectable() bool            { return true }
