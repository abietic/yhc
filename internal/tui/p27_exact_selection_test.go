package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
)

type p272SelectionItem struct {
	annotated string
	version   uint64
}

func (i *p272SelectionItem) Render(int, Styles) string {
	lines, _, _ := parseSelectionAnnotations(
		DefaultDisplayCellProfile(),
		i.annotated,
	)
	return strings.Join(lines, "\n")
}

func (i *p272SelectionItem) Finished() bool      { return true }
func (i *p272SelectionItem) Version() uint64     { return i.version }
func (i *p272SelectionItem) NoSelectPrefix() int { return 0 }
func (i *p272SelectionItem) renderSelection(
	HistoryRenderContext,
) selectionAnnotatedRender {
	return selectionAnnotatedRender{
		rendered: i.annotated, annotated: true,
	}
}

func TestP272ExactExtractionTruthTable(t *testing.T) {
	first := &p272SelectionItem{
		version: 1,
		annotated: selectionPresentation("> ") +
			selectionMarkSemanticStart + "alpha \n" +
			selectionPresentation("> ") + "beta  " +
			selectionMarkSemanticEnd + selectionHardBreak() +
			selectionPresentation("> ") +
			selectionSemantic(selectionAnnotateTabs("中\tz")),
	}
	second := &p272SelectionItem{
		version:   1,
		annotated: selectionPresentation("• ") + selectionSemantic("tail"),
	}
	chat := NewChatView(defaultStyles())
	chat.SetSize(24, 6)
	chat.appendChatItem(first)
	chat.appendChatItem(second)
	entry := chat.renderItem(first, chat.renderWidth())
	if got := strings.Join(entry.lines, "\n"); got != "> alpha \n> beta  \n> 中    z" {
		t.Fatalf("published visible rows = %q", got)
	}
	if len(entry.selection) != 3 {
		t.Fatalf("selection metadata rows = %d, want 3", len(entry.selection))
	}

	start, _, ok := selectionRowCellBounds(entry.selection[0])
	if !ok {
		t.Fatal("first semantic row is not selectable")
	}
	_, end, ok := selectionRowCellBounds(entry.selection[2])
	if !ok {
		t.Fatal("last semantic row is not selectable")
	}
	if got := chat.RenderItemRange(0, 0, start, 0, 2, end); got != "alpha beta  \n中\tz" {
		t.Fatalf("whole-item extraction = %q", got)
	}

	secondEntry := chat.renderItem(second, chat.renderWidth())
	_, secondEnd, ok := selectionRowCellBounds(secondEntry.selection[0])
	if !ok {
		t.Fatal("second item row is not selectable")
	}
	if got := chat.RenderItemRange(
		0,
		0,
		start,
		1,
		0,
		secondEnd,
	); got != "alpha beta  \n中\tz\ntail" {
		t.Fatalf("cross-item extraction = %q", got)
	}

	wideStart := entry.selection[2].spans[0].startCell + 1
	if got := chat.RenderItemRange(
		0,
		2,
		wideStart,
		0,
		2,
		wideStart+1,
	); got != "中" {
		t.Fatalf("wide-cluster partial extraction = %q", got)
	}
}

func TestP272UserRendererPublishesSamePassSoftAndHardFacts(t *testing.T) {
	message := &UserMessage{content: "alpha  beta   gamma  \nhard  \nend"}
	chat := NewChatView(defaultStyles())
	chat.SetSize(16, 8)
	chat.appendChatItem(message)
	entry := chat.renderItem(message, chat.renderWidth())

	normalLines := strings.Split(
		message.RenderWithEnvironment(chat.renderWidth(), chat.environment),
		"\n",
	)
	for index := range normalLines {
		normalLines[index] = chat.environment.profile.truncate(
			normalLines[index],
			chat.renderWidth(),
		)
	}
	if !reflect.DeepEqual(
		plainSelectionTestLines(entry.lines),
		plainSelectionTestLines(normalLines),
	) {
		t.Fatalf(
			"annotated render changed visible rows:\nannotated=%#v\nnormal=%#v",
			entry.lines,
			normalLines,
		)
	}
	firstStart, _, ok := selectionRowCellBounds(entry.selection[0])
	if !ok {
		t.Fatal("user first row is not selectable")
	}
	_, lastEnd, ok := selectionRowCellBounds(
		entry.selection[len(entry.selection)-1],
	)
	if !ok {
		t.Fatal("user last row is not selectable")
	}
	if got := chat.RenderItemRange(
		0,
		0,
		firstStart,
		0,
		len(entry.selection)-1,
		lastEnd,
	); got != "alpha betagamma\nhard  \nend" {
		t.Fatalf("user semantic extraction = %q", got)
	}
}

func TestP272AssistantMarkdownSamePassMetadataKeepsVisibleRender(t *testing.T) {
	content := "# Heading\n\nalpha **beta** gamma delta epsilon zeta\n\n" +
		"- item one\n- item two\n\n```text\nline one\nline two\n```"
	message := &AssistantMessage{}
	message.ReplaceContent(content)
	message.Finalize()
	chat := NewChatView(defaultStyles())
	chat.SetSize(40, 18)
	chat.appendChatItem(message)
	entry := chat.renderItem(message, chat.renderWidth())

	normalLines := message.RenderLinesWithEnvironment(
		chat.renderWidth(),
		chat.environment,
	)
	for index := range normalLines {
		normalLines[index] = chat.environment.profile.truncate(
			normalLines[index],
			chat.renderWidth(),
		)
	}
	if !reflect.DeepEqual(
		plainSelectionTestLines(entry.lines),
		plainSelectionTestLines(normalLines),
	) {
		t.Fatalf(
			"annotated Markdown changed visible rows:\nannotated=%#v\nnormal=%#v",
			plainSelectionTestLines(entry.lines),
			plainSelectionTestLines(normalLines),
		)
	}

	firstLine, firstCell, lastLine, lastCell := p272ItemSemanticBounds(
		t,
		entry,
	)
	extracted := chat.RenderItemRange(
		0,
		firstLine,
		firstCell,
		0,
		lastLine,
		lastCell,
	)
	for _, semantic := range []string{
		"Heading",
		"alpha ",
		"beta",
		"item one",
		"item two",
		"line one",
		"line two",
	} {
		if !strings.Contains(extracted, semantic) {
			t.Fatalf("Markdown extraction lost %q: %q", semantic, extracted)
		}
	}
	for _, presentation := range []string{
		assistantIdentityGlyph,
		"•",
		"════════",
		"```",
	} {
		if strings.Contains(extracted, presentation) {
			t.Fatalf(
				"Markdown extraction included presentation %q: %q",
				presentation,
				extracted,
			)
		}
	}
}

func plainSelectionTestLines(lines []string) []string {
	plain := make([]string, len(lines))
	for index, line := range lines {
		plain[index] = selectionPlainLine(line)
	}
	return plain
}

func TestP272SemanticTableExcludesBordersAndPadding(t *testing.T) {
	content := "| Name | Value |\n| --- | ---: |\n| alpha | 中 |\n| beta | two |"
	message := &AssistantMessage{}
	message.ReplaceContent(content)
	message.Finalize()
	chat := NewChatView(defaultStyles())
	chat.SetSize(52, 12)
	chat.appendChatItem(message)
	entry := chat.renderItem(message, chat.renderWidth())

	firstLine, firstCell, lastLine, lastCell := p272ItemSemanticBounds(
		t,
		entry,
	)
	extracted := chat.RenderItemRange(
		0,
		firstLine,
		firstCell,
		0,
		lastLine,
		lastCell,
	)
	for _, semantic := range []string{"Name", "Value", "alpha", "中", "beta", "two"} {
		if !strings.Contains(extracted, semantic) {
			t.Fatalf("table extraction lost %q: %q", semantic, extracted)
		}
	}
	if strings.ContainsAny(extracted, "┌┬┐├┼┤│└┴┘─") {
		t.Fatalf("table extraction included border glyphs: %q", extracted)
	}
}

func p272ItemSemanticBounds(
	t *testing.T,
	entry *renderCacheEntry,
) (firstLine, firstCell, lastLine, lastCell int) {
	t.Helper()
	firstLine = -1
	for line, row := range entry.selection {
		start, end, ok := selectionRowCellBounds(row)
		if !ok {
			continue
		}
		if firstLine < 0 {
			firstLine, firstCell = line, start
		}
		lastLine, lastCell = line, end
	}
	if firstLine < 0 {
		t.Fatal("item published no semantic selection rows")
	}
	return firstLine, firstCell, lastLine, lastCell
}

func TestP272MultiClickUsesSemanticLogicalLineAndClusters(t *testing.T) {
	item := &p272SelectionItem{
		version: 1,
		annotated: selectionMarkSemanticStart + "hel\n" +
			"lo! " + selectionMarkSemanticEnd,
	}
	chat := NewChatView(defaultStyles())
	chat.SetSize(20, 3)
	chat.appendChatItem(item)
	chat.Render(20, 3)
	projection := chat.currentViewportProjection()
	secondRow := -1
	for row, descriptor := range projection.rows {
		if descriptor.kind == chatViewportRowTranscript &&
			descriptor.lineInItem == 1 {
			secondRow = row
			break
		}
	}
	if secondRow < 0 {
		t.Fatal("second soft-wrapped row was not published")
	}

	var selection Selection
	selection.selectWordForChat(0, secondRow, chat)
	if got := selection.ExtractTextFromChat(chat); got != "hello" {
		t.Fatalf("soft-wrapped double-click word = %q", got)
	}

	selection.Clear()
	selection.selectWordForChat(2, secondRow, chat)
	if got := selection.ExtractTextFromChat(chat); got != "!" {
		t.Fatalf("punctuation grapheme selection = %q", got)
	}

	selection.Clear()
	selection.selectLineForChat(secondRow, chat)
	if got := selection.ExtractTextFromChat(chat); got != "hello! " {
		t.Fatalf("triple-click logical line = %q", got)
	}

	start, end := selection.getItemBounds()
	selection.anchor, selection.focus = &end, &start
	selection.identity, _ = chat.selectionIdentityFor(*selection.anchor, selection.focus)
	if got := selection.ExtractTextFromChat(chat); got != "hello! " {
		t.Fatalf("reverse logical-line extraction = %q", got)
	}
}

func TestP272ContentIdentityClearsBeforeConsumerAndReleaseFallthrough(t *testing.T) {
	item := &p272SelectionItem{
		version:   1,
		annotated: selectionSemantic("alpha beta"),
	}
	chat := NewChatView(defaultStyles())
	chat.SetSize(20, 2)
	chat.appendChatItem(item)
	chat.Render(20, 2)
	projection := chat.currentViewportProjection()
	row := projection.contentRect.Y

	var selection Selection
	selection.startForChat(0, row, chat)
	selection.updateForChat(5, row, chat)
	if !selection.HasSelection() {
		t.Fatal("test selection was not created")
	}

	chat.SetSize(30, 2)
	chat.Render(30, 2)
	if got := selection.ExtractTextFromChat(chat); got != "" {
		t.Fatalf("stale extraction = %q, want empty", got)
	}
	if selection.HasSelection() {
		t.Fatal("resize/content identity drift did not clear selection")
	}

	selection.startForChat(0, row, chat)
	selection.updateForChat(5, row, chat)
	item.version++
	chat.invalidateContent()
	chat.Render(30, 2)
	consumed := selection.HandleMouseForChat(tuiMouseMsg{
		Button: tea.MouseLeft,
		Action: mouseActionRelease,
	}, 5, row, chat)
	if !consumed {
		t.Fatal("stale release must be consumed")
	}
	if selection.HasSelection() || selection.IsDragging() {
		t.Fatal("stale release left selection state active")
	}
}

func TestP272MalformedAnnotationMakesOnlyItemNonselectable(t *testing.T) {
	bad := &p272SelectionItem{
		version:   1,
		annotated: selectionMarkSemanticStart + "broken",
	}
	good := &p272SelectionItem{
		version:   1,
		annotated: selectionSemantic("good"),
	}
	chat := NewChatView(defaultStyles())
	chat.SetSize(20, 4)
	chat.appendChatItem(bad)
	chat.appendChatItem(good)
	frame := chat.Render(20, 4)
	for _, marker := range selectionAnnotationMarkers {
		if strings.Contains(frame, marker) {
			t.Fatalf("private annotation leaked into frame: %q", marker)
		}
	}
	if _, _, ok := chat.selectionMetadata(0, 0); ok {
		t.Fatal("malformed item row remained selectable")
	}
	if row, _, ok := chat.selectionMetadata(1, 0); !ok ||
		selectionRowText(chat.environment.profile, row, 0, 100) != "good" {
		t.Fatal("malformed sibling disabled a valid item's metadata")
	}
}

func TestP272TimerDrivenEdgeScrollIsGenerationFenced(t *testing.T) {
	app := p272EdgeScrollApp(t)
	projection := app.chat.currentViewportProjection()
	topRow, bottomRow := projection.contentRect.Y,
		projection.contentRect.bottom()-1
	app.selection.startForChat(0, topRow, app.chat)
	if !app.selection.IsDragging() {
		t.Fatal("edge-scroll fixture did not start a drag")
	}

	before := app.chat.ViewportTopRow()
	firstTick := app.handleSelectionEdgeMotion(3, bottomRow)
	if firstTick == nil || !app.selectionEdgeScroll.active {
		t.Fatal("edge entry did not schedule the 50ms loop")
	}
	if got := app.chat.ViewportTopRow(); got != before+1 {
		t.Fatalf("edge entry scrolled %d rows, want 1", got-before)
	}
	generation := app.selectionEdgeScroll.generation

	if repeated := app.handleSelectionEdgeMotion(4, bottomRow); repeated != nil {
		t.Fatal("same-edge motion scheduled a duplicate timer")
	}
	if got := app.chat.ViewportTopRow(); got != before+1 {
		t.Fatalf("same-edge motion added an immediate step: top=%d", got)
	}

	nextTick := app.handleSelectionEdgeScrollTick(
		selectionEdgeScrollTickMsg{generation: generation},
	)
	if nextTick == nil {
		t.Fatal("accepted edge tick did not continue the loop")
	}
	if got := app.chat.ViewportTopRow(); got != before+2 {
		t.Fatalf("accepted edge tick scrolled %d rows, want 2", got-before)
	}

	current := app.chat.currentViewportProjection()
	reverse := app.handleSelectionEdgeMotion(
		4,
		current.contentRect.Y,
	)
	if reverse == nil ||
		app.selectionEdgeScroll.generation == generation {
		t.Fatal("direction change did not fence the old generation")
	}
	afterReverse := app.chat.ViewportTopRow()
	if stale := app.handleSelectionEdgeScrollTick(
		selectionEdgeScrollTickMsg{generation: generation},
	); stale != nil {
		t.Fatal("stale edge generation scheduled another tick")
	}
	if got := app.chat.ViewportTopRow(); got != afterReverse {
		t.Fatalf(
			"stale edge generation moved viewport: %d -> %d",
			afterReverse,
			got,
		)
	}
}

func TestP272EdgeScrollStopsOnReleaseStateAndNoMovement(t *testing.T) {
	app := p272EdgeScrollApp(t)
	projection := app.chat.currentViewportProjection()
	topRow, bottomRow := projection.contentRect.Y,
		projection.contentRect.bottom()-1
	app.selection.startForChat(0, topRow, app.chat)
	if cmd := app.handleSelectionEdgeMotion(3, topRow); cmd != nil ||
		app.selectionEdgeScroll.active {
		t.Fatal("top boundary retained a no-movement timer")
	}

	if cmd := app.handleSelectionEdgeMotion(3, bottomRow); cmd == nil {
		t.Fatal("downward edge motion did not start")
	}
	releaseGeneration := app.selectionEdgeScroll.generation
	releaseProjection := app.chat.currentViewportProjection()
	releaseRow := releaseProjection.contentRect.bottom() - 1
	releasePoint := app.chat.nearestSelectableViewportPoint(3, releaseRow)
	if releasePoint == nil {
		t.Fatal("release fixture has no selectable edge point")
	}
	app.selection.anchor = releasePoint
	app.selection.focus = nil
	app.selection.identity, _ = app.chat.selectionIdentityFor(
		*releasePoint,
		nil,
	)
	_, _ = app.Update(tuiMouseMsg{
		X: 3, Y: app.layout.chatRect.Y + releaseRow,
		Button: tea.MouseLeft, Action: mouseActionRelease,
	})
	if app.selectionEdgeScroll.active {
		t.Fatal("release did not stop edge scroll")
	}
	afterRelease := app.chat.ViewportTopRow()
	_, _ = app.Update(selectionEdgeScrollTickMsg{
		generation: releaseGeneration,
	})
	if got := app.chat.ViewportTopRow(); got != afterRelease {
		t.Fatalf("released timer moved viewport: %d -> %d", afterRelease, got)
	}

	app = p272EdgeScrollApp(t)
	projection = app.chat.currentViewportProjection()
	app.selection.startForChat(0, projection.contentRect.Y, app.chat)
	if cmd := app.handleSelectionEdgeMotion(
		3,
		projection.contentRect.bottom()-1,
	); cmd == nil {
		t.Fatal("state-change fixture did not start edge scroll")
	}
	stateGeneration := app.selectionEdgeScroll.generation
	app.state = StateExpand
	if cmd := app.handleSelectionEdgeScrollTick(
		selectionEdgeScrollTickMsg{generation: stateGeneration},
	); cmd != nil || app.selectionEdgeScroll.active {
		t.Fatal("non-chat state retained edge-scroll work")
	}
}

func p272EdgeScrollApp(t *testing.T) *App {
	t.Helper()
	lines := make([]string, 48)
	for index := range lines {
		lines[index] = fmt.Sprintf("row %02d", index)
	}
	app := newTestApp(64, 18)
	app.chat.appendChatItem(p271Item{text: strings.Join(lines, "\n")})
	app.chat.ScrollToTop()
	_ = app.View()
	projection := app.chat.currentViewportProjection()
	if projection == nil || projection.contentRect.Height < 2 {
		t.Fatalf("edge-scroll fixture projection = %#v", projection)
	}
	return app
}

func TestP272AppMouseMotionCreatesSemanticSelection(t *testing.T) {
	app := newTestApp(100, 28)
	for index := 0; index < 40; index++ {
		app.chat.AppendSystem(fmt.Sprintf("selection target %02d", index))
	}
	_ = app.View()
	projection := app.chat.currentViewportProjection()
	row := projection.contentRect.Y
	_, _ = app.Update(tuiMouseMsg{
		X: 4, Y: app.layout.chatRect.Y + row,
		Button: tea.MouseLeft, Action: mouseActionPress,
	})
	_, _ = app.Update(tuiMouseMsg{
		X: 15, Y: app.layout.chatRect.Y + row,
		Button: tea.MouseLeft, Action: mouseActionMotion,
	})
	if !app.selection.HasSelection() || !app.selection.IsDragging() {
		t.Fatalf("semantic mouse drag state = %#v", app.selection)
	}
}

func TestP272BuiltInWrappingPublishesSoftAndHardBoundaries(t *testing.T) {
	started := time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	thinking := &ThinkingMessage{
		content:    "alpha beta gamma delta epsilon\nhard",
		startedAt:  started,
		finishedAt: started.Add(time.Second),
		finished:   true,
		expanded:   true,
		version:    1,
	}
	errorMessage := NewErrorMessage(ErrorEntry{
		Severity:  SeverityError,
		Category:  CategoryGeneral,
		Title:     "Failure",
		Message:   "alpha beta gamma delta epsilon\nhard",
		Timestamp: started,
	}, defaultStyles())
	tool := &ToolMessage{
		name:     "Custom",
		input:    "{}",
		output:   "alpha beta gamma delta epsilon\nhard",
		status:   ToolSuccess,
		expanded: true,
		version:  1,
	}
	transcript := newAgentTranscriptHistoryItem(
		engine.AgentTranscriptMessage{
			ID:        "p272-transcript",
			Role:      "assistant",
			Content:   "alpha beta gamma delta epsilon\nhard",
			Completed: true,
		},
	)

	tests := []struct {
		name string
		item ChatItem
		want string
	}{
		{
			name: "thinking", item: thinking,
			want: "alpha beta gammadelta epsilon\nhard",
		},
		{
			name: "error", item: errorMessage,
			want: "alpha beta gammadelta epsilon\nhard",
		},
		{
			name: "tool", item: tool,
			want: "alpha betagamma deltaepsilon\nhard",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chat := NewChatView(defaultStyles())
			chat.SetSize(22, 12)
			chat.appendChatItem(test.item)
			entry := chat.renderItem(test.item, chat.renderWidth())
			normalLines := strings.Split(
				test.item.Render(
					chat.renderWidth(),
					chat.environment.styles,
				),
				"\n",
			)
			for index, line := range normalLines {
				normalLines[index] = chat.environment.profile.truncate(
					line,
					chat.renderWidth(),
				)
			}
			if !reflect.DeepEqual(
				plainSelectionTestLines(entry.lines),
				plainSelectionTestLines(normalLines),
			) {
				t.Fatalf(
					"annotated render changed visible rows:\nannotated=%#v\nnormal=%#v",
					plainSelectionTestLines(entry.lines),
					plainSelectionTestLines(normalLines),
				)
			}
			got := p272ExtractSemanticBlock(
				t,
				chat,
				entry,
				"alpha",
				"hard",
			)
			if got != test.want {
				t.Fatalf("wrapped extraction = %q", got)
			}
		})
	}

	t.Run("agent transcript", func(t *testing.T) {
		chat := NewChatView(defaultStyles())
		chat.SetSize(22, 12)
		chat.AppendHistoryItem(transcript)
		item := chat.items[0]
		entry := chat.renderItem(item, chat.renderWidth())
		normalLines := strings.Split(
			transcript.Render(HistoryRenderContext{
				Width:       chat.renderWidth(),
				Environment: chat.environment,
				Mode:        HistoryRenderRich,
			}),
			"\n",
		)
		for index, line := range normalLines {
			normalLines[index] = chat.environment.profile.truncate(
				line,
				chat.renderWidth(),
			)
		}
		if !reflect.DeepEqual(
			plainSelectionTestLines(entry.lines),
			plainSelectionTestLines(normalLines),
		) {
			t.Fatalf(
				"annotated render changed visible rows:\nannotated=%#v\nnormal=%#v",
				plainSelectionTestLines(entry.lines),
				plainSelectionTestLines(normalLines),
			)
		}
		got := p272ExtractSemanticBlock(
			t,
			chat,
			entry,
			"alpha",
			"hard",
		)
		if got != "alpha beta gammadelta epsilon\nhard" {
			t.Fatalf("wrapped extraction = %q", got)
		}
	})
}

func TestP272StaticBuiltInsExcludeChromeWithoutDroppingText(t *testing.T) {
	help := &HelpMessage{entries: []HelpEntry{{
		Name: "model",
		Desc: "choose a model",
	}}}
	group := &ToolGroupMessage{
		tools: []*ToolMessage{
			{name: "Read", status: ToolSuccess},
			{name: "Glob", status: ToolSuccess},
		},
		version: 1,
	}
	tests := []struct {
		name         string
		item         ChatItem
		want         []string
		presentation []string
	}{
		{
			name: "help", item: help,
			want: []string{"Available commands:", "/model", "choose a model"},
		},
		{
			name: "tool group", item: group,
			want:         []string{"Explore", "2 operations", "1 read", "1 search"},
			presentation: []string{"●"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chat := NewChatView(defaultStyles())
			chat.SetSize(60, 8)
			chat.appendChatItem(test.item)
			entry := chat.renderItem(test.item, chat.renderWidth())
			firstLine, firstCell, lastLine, lastCell := p272ItemSemanticBounds(t, entry)
			got := chat.RenderItemRange(
				0,
				firstLine,
				firstCell,
				0,
				lastLine,
				lastCell,
			)
			for _, want := range test.want {
				if !strings.Contains(got, want) {
					t.Fatalf("static extraction lost %q: %q", want, got)
				}
			}
			for _, excluded := range test.presentation {
				if strings.Contains(got, excluded) {
					t.Fatalf(
						"static extraction included chrome %q: %q",
						excluded,
						got,
					)
				}
			}
		})
	}
}

func p272ExtractSemanticBlock(
	t *testing.T,
	chat *ChatView,
	entry *renderCacheEntry,
	startNeedle string,
	endNeedle string,
) string {
	t.Helper()
	startLine, endLine := -1, -1
	for line, row := range entry.selection {
		start, end, ok := selectionRowCellBounds(row)
		if !ok {
			continue
		}
		text := selectionRowText(
			chat.environment.profile,
			row,
			start,
			end,
		)
		if startLine < 0 && strings.Contains(text, startNeedle) {
			startLine = line
		}
		if strings.Contains(text, endNeedle) {
			endLine = line
		}
	}
	if startLine < 0 || endLine < startLine {
		t.Fatalf(
			"semantic block %q..%q not found in %#v",
			startNeedle,
			endNeedle,
			entry.selection,
		)
	}
	startCell, _, _ := selectionRowCellBounds(entry.selection[startLine])
	_, endCell, _ := selectionRowCellBounds(entry.selection[endLine])
	return chat.RenderItemRange(
		0,
		startLine,
		startCell,
		0,
		endLine,
		endCell,
	)
}
