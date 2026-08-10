package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

type p411PromotionProjectionFixture struct {
	name        string
	profile     DisplayCellProfile
	content     string
	innerWidth  int
	innerOrigin int
	align       string
	directAlign bool
	wantPlain   []string
}

func TestP411FixedBoxProfileOwnedInnerProjection(t *testing.T) {
	t.Parallel()

	narrow := DefaultDisplayCellProfile()
	widePolicy := defaultDisplayCellPolicy()
	widePolicy.AmbiguousWidth = "wide"
	wide := newDisplayCellProfile(widePolicy)
	fixtures := []p411PromotionProjectionFixture{
		{
			name:    "tab uses bordered padded origin",
			profile: narrow, content: "\tX",
			innerWidth: 6, innerOrigin: 2,
			wantPlain: []string{"  X   "},
		},
		{
			name:    "combining cluster stays whole",
			profile: narrow, content: "e\u0301X",
			innerWidth: 4, innerOrigin: 1,
			wantPlain: []string{"e\u0301X  "},
		},
		{
			name:    "emoji ZWJ cluster stays whole",
			profile: narrow, content: "👩‍💻X",
			innerWidth: 4, innerOrigin: 2,
			wantPlain: []string{"👩‍💻X "},
		},
		{
			name:    "ambiguous scalar follows narrow profile",
			profile: narrow, content: "·X",
			innerWidth: 3, innerOrigin: 1,
			wantPlain: []string{"·X "},
		},
		{
			name:    "ambiguous scalar follows wide profile",
			profile: wide, content: "·X",
			innerWidth: 3, innerOrigin: 1,
			wantPlain: []string{"·X"},
		},
		{
			name:    "profile owns wrapping and row padding",
			profile: narrow, content: "ab cd",
			innerWidth: 3, innerOrigin: 1,
			wantPlain: []string{"ab ", "cd "},
		},
		{
			name:    "right alignment expands tab at aligned origin",
			profile: narrow, content: "\tX",
			innerWidth: 7, innerOrigin: 1, align: "right", directAlign: true,
			wantPlain: []string{"   X   "},
		},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			var rows []string
			if fixture.directAlign {
				rows = []string{fixture.profile.padAligned(
					fixture.content,
					fixture.innerWidth,
					fixture.align,
					fixture.innerOrigin,
				)}
			} else {
				rows = contentWrapLines(
					fixture.profile,
					fixture.content,
					fixture.innerWidth,
					fixture.innerOrigin,
				)
				for index := range rows {
					rows[index] = fixture.profile.padAligned(
						rows[index],
						fixture.innerWidth,
						fixture.align,
						fixture.innerOrigin,
					)
				}
			}
			if got := rows; !equalStrings(got, fixture.wantPlain) {
				t.Fatalf("projected rows = %#v, want %#v", got, fixture.wantPlain)
			}
			for index, row := range rows {
				if got := fixture.profile.measure(row, fixture.innerOrigin); got != fixture.innerWidth {
					t.Fatalf("row %d width = %d, want %d: %q", index, got, fixture.innerWidth, row)
				}
			}
		})
	}
}

func TestP411FixedBoxUsesNonZeroOriginTab(t *testing.T) {
	t.Parallel()

	profile := DefaultDisplayCellProfile()
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1)
	const bodyWidth = 8

	current := stripANSIForTest(contentRenderStyleWidth(
		profile,
		style,
		bodyWidth,
		"\tX",
	))
	originZero := stripANSIForTest(style.
		Width(bodyWidth + style.GetHorizontalBorderSize()).
		Render(profile.expandTabs("\tX", 0)))
	innerOrigin := style.GetBorderLeftSize() + style.GetPaddingLeft()
	want := stripANSIForTest(style.
		Width(bodyWidth + style.GetHorizontalBorderSize()).
		Render(profile.expandTabs("\tX", innerOrigin)))
	if current != want {
		t.Fatalf("fixed box = %q, want actual-origin projection %q", current, want)
	}
	if current == originZero || !strings.Contains(current, "│   X") {
		t.Fatalf("fixed box retained origin-zero tab projection: current=%q origin-zero=%q", current, originZero)
	}
	projection := contentProjectFixedBox(profile, style, bodyWidth, 0, "\tX")
	if projection.outer != (layoutRect{Width: 10, Height: 3}) ||
		projection.inner != (layoutRect{X: 2, Y: 1, Width: 6, Height: 1}) {
		t.Fatalf("fixed-box geometry = outer:%+v inner:%+v", projection.outer, projection.inner)
	}
	for row, line := range strings.Split(current, "\n") {
		if got := profile.measure(line, 0); got != projection.outer.Width {
			t.Fatalf("row %d width = %d, want %d: %q", row, got, projection.outer.Width, line)
		}
	}
}

func TestP411FixedBoxDifferentialBoundariesAreExact(t *testing.T) {
	t.Parallel()

	profile := DefaultDisplayCellProfile()
	fixtures := []struct {
		name      string
		style     lipgloss.Style
		bodyWidth int
		content   string
	}{
		{name: "plain combining", style: lipgloss.NewStyle(), bodyWidth: 6, content: "e\u0301X"},
		{name: "rounded emoji", style: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()), bodyWidth: 6, content: "👩‍💻X"},
		{name: "Indic conjunct", style: lipgloss.NewStyle().Padding(0, 2, 0, 1), bodyWidth: 8, content: "क्ष X"},
		{name: "centered wrap", style: lipgloss.NewStyle().AlignHorizontal(lipgloss.Center), bodyWidth: 5, content: "ab cd"},
		{name: "SGR and OSC8", style: lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 1), bodyWidth: 10, content: "\x1b[31m赤\x1b[0m \x1b]8;;https://example.test\x1b\\link\x1b]8;;\x1b\\"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			rendered := contentRenderStyleWidth(profile, fixture.style, fixture.bodyWidth, fixture.content)
			plain := stripANSIForTest(rendered)
			wantOuter := fixture.bodyWidth + fixture.style.GetHorizontalBorderSize()
			for row, line := range strings.Split(plain, "\n") {
				got := profile.measure(line, 0)
				if got != wantOuter {
					t.Fatalf("row %d outer width = %d, want %d: %q", row, got, wantOuter, line)
				}
			}
		})
	}
}

func TestP411FixedBoxGeometryMatrix(t *testing.T) {
	t.Parallel()

	profile := DefaultDisplayCellProfile()
	fixtures := []struct {
		name                 string
		style                lipgloss.Style
		bodyWidth, height    int
		content              string
		wantOuter, wantInner layoutRect
	}{
		{
			name:  "narrow whole-cluster omission",
			style: lipgloss.NewStyle(), bodyWidth: 1, content: "中",
			wantOuter: layoutRect{Width: 1, Height: 1},
			wantInner: layoutRect{Width: 1, Height: 1},
		},
		{
			name:      "two-cell body",
			style:     lipgloss.NewStyle(),
			bodyWidth: 2, content: "中",
			wantOuter: layoutRect{Width: 2, Height: 1},
			wantInner: layoutRect{Width: 2, Height: 1},
		},
		{
			name:      "center aligned three-cell body",
			style:     lipgloss.NewStyle().AlignHorizontal(lipgloss.Center),
			bodyWidth: 3, content: "X",
			wantOuter: layoutRect{Width: 3, Height: 1},
			wantInner: layoutRect{Width: 3, Height: 1},
		},
		{
			name:      "rounded natural height",
			style:     lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1),
			bodyWidth: 8, content: "👩‍💻X",
			wantOuter: layoutRect{Width: 10, Height: 3},
			wantInner: layoutRect{X: 2, Y: 1, Width: 6, Height: 1},
		},
		{
			name:      "one-sided top border",
			style:     lipgloss.NewStyle().BorderStyle(lipgloss.NormalBorder()).BorderTop(true),
			bodyWidth: 4, content: "e\u0301X",
			wantOuter: layoutRect{Width: 4, Height: 2},
			wantInner: layoutRect{Y: 1, Width: 4, Height: 1},
		},
		{
			name:      "right aligned four-cell body",
			style:     lipgloss.NewStyle().AlignHorizontal(lipgloss.Right),
			bodyWidth: 4, content: "X",
			wantOuter: layoutRect{Width: 4, Height: 1},
			wantInner: layoutRect{Width: 4, Height: 1},
		},
		{
			name:      "fixed height with padding and border",
			style:     lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(1, 2).Align(lipgloss.Center, lipgloss.Center),
			bodyWidth: 10, height: 7, content: "X",
			wantOuter: layoutRect{Width: 12, Height: 7},
			wantInner: layoutRect{X: 3, Y: 2, Width: 6, Height: 3},
		},
		{
			name:      "fixed height clips border without widening",
			style:     lipgloss.NewStyle().Border(lipgloss.RoundedBorder()),
			bodyWidth: 20, height: 1, content: "hidden",
			wantOuter: layoutRect{Width: 22, Height: 1},
			wantInner: layoutRect{X: 1, Y: 1, Width: 20},
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			projection := contentProjectFixedBox(
				profile,
				fixture.style,
				fixture.bodyWidth,
				fixture.height,
				fixture.content,
			)
			if projection.outer != fixture.wantOuter || projection.inner != fixture.wantInner {
				t.Fatalf("geometry = outer:%+v inner:%+v, want outer:%+v inner:%+v", projection.outer, projection.inner, fixture.wantOuter, fixture.wantInner)
			}
			if len(projection.rows) != projection.outer.Height {
				t.Fatalf("row count = %d, want %d", len(projection.rows), projection.outer.Height)
			}
			for row, line := range projection.rows {
				if got := profile.measure(line, 0); got != projection.outer.Width {
					t.Fatalf("row %d width = %d, want %d: %q", row, got, projection.outer.Width, line)
				}
			}
		})
	}
}

func TestP411FixedBoxWrapperDimensionSemantics(t *testing.T) {
	t.Parallel()

	profile := DefaultDisplayCellProfile()
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder())
	if got := contentRenderStyleWidth(profile, style, 4, "X"); got == "" {
		t.Fatal("width-only wrapper lost natural-height projection")
	}
	for name, dimensions := range map[string][2]int{
		"zero width":      {0, 1},
		"negative width":  {-1, 1},
		"zero height":     {4, 0},
		"negative height": {4, -1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := contentRenderStyleBox(
				profile,
				style,
				dimensions[0],
				dimensions[1],
				"X",
			); got != "" {
				t.Fatalf("fixed-box wrapper = %q, want empty", got)
			}
		})
	}

	got := contentRenderStyleBox(profile, style, 4, 2, "hidden")
	if rows := strings.Split(got, "\n"); len(rows) != 2 {
		t.Fatalf("positive fixed height rendered %d rows, want 2", len(rows))
	}
}

func TestP411FixedBoxProfileMatrix(t *testing.T) {
	t.Parallel()

	narrow := DefaultDisplayCellProfile()
	widePolicy := defaultDisplayCellPolicy()
	widePolicy.AmbiguousWidth = "wide"
	wide := newDisplayCellProfile(widePolicy)
	fixtures := []struct {
		name      string
		profile   DisplayCellProfile
		content   string
		wantCells int
	}{
		{name: "post Unicode 15", profile: narrow, content: "\U0001FAE8", wantCells: 2},
		{name: "Indic conjunct", profile: narrow, content: "क्ष", wantCells: 2},
		{name: "VS15", profile: narrow, content: "✈︎", wantCells: 1},
		{name: "VS16", profile: narrow, content: "✈️", wantCells: 2},
		{name: "ZWJ emoji", profile: narrow, content: "👩‍💻", wantCells: 2},
		{name: "lone regional indicator", profile: narrow, content: "🇨", wantCells: 1},
		{name: "paired regional indicators", profile: narrow, content: "🇨🇳", wantCells: 2},
		{name: "combining only", profile: narrow, content: "\u0301", wantCells: 0},
		{name: "ambiguous narrow", profile: narrow, content: "·", wantCells: 1},
		{name: "ambiguous wide", profile: wide, content: "·", wantCells: 2},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()
			if got := fixture.profile.width(fixture.content); got != fixture.wantCells {
				t.Fatalf("fixture width = %d, want %d", got, fixture.wantCells)
			}
			bodyWidth := max(fixture.wantCells+1, 1)
			projection := contentProjectFixedBox(
				fixture.profile,
				lipgloss.NewStyle().Border(lipgloss.RoundedBorder()),
				bodyWidth,
				0,
				fixture.content,
			)
			assertP411ProjectionExact(t, fixture.profile, projection)
			if plain := stripANSIForTest(projection.rendered); !strings.Contains(plain, fixture.content) {
				t.Fatalf("projection lost whole cluster %q: %q", fixture.content, plain)
			}
		})
	}

	omitted := contentProjectFixedBox(
		narrow,
		lipgloss.NewStyle(),
		1,
		0,
		"👩‍💻X",
	)
	if plain := stripANSIForTest(omitted.rendered); strings.Contains(plain, "👩‍💻") || !strings.Contains(plain, "X") {
		t.Fatalf("impossible cluster omission = %q, want whole emoji omitted and X retained", plain)
	}
}

func TestP411FixedBoxAlignmentMatrix(t *testing.T) {
	t.Parallel()

	profile := DefaultDisplayCellProfile()
	for name, fixture := range map[string]struct {
		style lipgloss.Style
		want  []string
	}{
		"left top": {
			style: lipgloss.NewStyle(),
			want:  []string{"X  ", "   ", "   "},
		},
		"center center": {
			style: lipgloss.NewStyle().Align(lipgloss.Center, lipgloss.Center),
			want:  []string{"   ", " X ", "   "},
		},
		"right bottom": {
			style: lipgloss.NewStyle().Align(lipgloss.Right, lipgloss.Bottom),
			want:  []string{"   ", "   ", "  X"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			projection := contentProjectFixedBox(
				profile,
				fixture.style,
				3,
				3,
				"X",
			)
			if got := strings.Split(stripANSIForTest(projection.rendered), "\n"); !equalStrings(got, fixture.want) {
				t.Fatalf("aligned rows = %#v, want %#v", got, fixture.want)
			}
			assertP411ProjectionExact(t, profile, projection)
		})
	}
}

func TestP411FixedBoxBalancesControlsAtBorders(t *testing.T) {
	t.Parallel()

	profile := DefaultDisplayCellProfile()
	style := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 1)
	for name, content := range map[string]string{
		"SGR":  "\x1b[31mabcdef\x1b[0m",
		"OSC8": "\x1b]8;;https://example.test\x1b\\abcdef\x1b]8;;\x1b\\",
		"both": "\x1b]8;;https://example.test\x1b\\\x1b[31mabcdef\x1b[0m\x1b]8;;\x1b\\",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			projection := contentProjectFixedBox(profile, style, 5, 0, content)
			assertP411ProjectionExact(t, profile, projection)
			if plain := stripANSIForTest(projection.rendered); !strings.Contains(plain, "abc") || !strings.Contains(plain, "def") {
				t.Fatalf("wrapped control content = %q", plain)
			}
			for row, line := range projection.rows {
				assertWidthProfileControlStateClosed(t, profile, line)
				if row > 0 && row < len(projection.rows)-1 && !strings.Contains(stripANSIForTest(line), "│") {
					t.Fatalf("row %d lost fixed border: %q", row, line)
				}
			}
		})
	}

	unsafe := contentProjectFixedBox(profile, lipgloss.NewStyle(), 4, 0, "A\aB")
	if strings.Contains(unsafe.rendered, "\a") || !strings.Contains(unsafe.rendered, "A�B") {
		t.Fatalf("unsafe control projection = %q", unsafe.rendered)
	}
}

func TestP411ProductionFixedBoxStylesAvoidUnsupportedGeometry(t *testing.T) {
	t.Parallel()

	for _, theme := range []ThemeName{ThemePolarNight, ThemeDaybreak, ThemeDarkAnsi} {
		styles := StylesForTheme(theme)
		for name, style := range map[string]lipgloss.Style{
			"dialog":       styles.DialogBorder,
			"editor":       styles.EditorBorder,
			"hint":         styles.HintBorder,
			"placeholder":  styles.Placeholder,
			"user-message": styles.UserMessageBlock,
			"welcome": lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				Padding(0, 1),
			"mcp-approval": lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				Padding(1, 2),
		} {
			t.Run(string(theme)+"/"+name, func(t *testing.T) {
				t.Parallel()
				top, right, bottom, left := style.GetMargin()
				if style.GetWidth() != 0 || style.GetHeight() != 0 ||
					style.GetMaxWidth() != 0 || style.GetMaxHeight() != 0 ||
					top != 0 || right != 0 || bottom != 0 || left != 0 ||
					style.GetInline() || style.GetTransform() != nil {
					t.Fatalf("unsupported fixed-box geometry is configured: %#v", style)
				}
			})
		}
	}
}

func TestP411FixedBoxSelectionUsesSameVisibleFrame(t *testing.T) {
	t.Parallel()

	environment := defaultRenderEnvironment(defaultStyles())
	message := &UserMessage{content: "\t👩‍💻e\u0301"}
	normal := stripANSIForTest(message.RenderWithEnvironment(20, environment))
	selection := message.renderSelection(HistoryRenderContext{
		Width: 20, Environment: environment, Mode: HistoryRenderRich,
	})
	if !selection.annotated {
		t.Fatal("selection path did not publish annotations")
	}
	annotated := stripANSIForTest(selectionStripMarkers(selection.rendered))
	if annotated != normal {
		t.Fatalf("selection frame differs from normal frame:\nnormal=%q\nselection=%q", normal, annotated)
	}
	for row, line := range strings.Split(normal, "\n") {
		if got := environment.profile.measure(line, 0); got != 20 {
			t.Fatalf("row %d width = %d, want 20: %q", row, got, line)
		}
	}

	chat := NewChatView(environment.styles)
	chat.SetRenderEnvironment(environment)
	chat.SetSize(20, 6)
	chat.AppendUser(message.content)
	frame := chat.Render(20, 6)
	projection := chat.currentViewportProjection()
	if projection == nil || projection.width != 20 || projection.height != 6 {
		t.Fatalf("published chat projection = %#v", projection)
	}
	frameRows := strings.Split(frame, "\n")
	wantTranscriptWidth := chat.renderWidth()
	first, last := -1, -1
	for row, descriptor := range projection.rows {
		if descriptor.kind != chatViewportRowTranscript {
			continue
		}
		if first < 0 {
			first = row
		}
		last = row
		if got := environment.profile.measure(frameRows[row], 0); got != wantTranscriptWidth {
			t.Fatalf("published transcript row %d width = %d, want %d", row, got, wantTranscriptWidth)
		}
		if inverse := chat.ItemPointToViewportRow(descriptor.itemIdx, descriptor.lineInItem); inverse != row {
			t.Fatalf("row %d inverse = %d", row, inverse)
		}
	}
	if first < 0 || last < first {
		t.Fatalf("published rows contain no transcript: %#v", projection.rows)
	}
	firstMetadata, _, ok := chat.selectionMetadata(0, projection.rows[first].lineInItem)
	if !ok {
		t.Fatal("first fixed-box row lacks selection metadata")
	}
	lastMetadata, _, ok := chat.selectionMetadata(0, projection.rows[last].lineInItem)
	if !ok {
		t.Fatal("last fixed-box row lacks selection metadata")
	}
	startCell, _, ok := selectionRowCellBounds(firstMetadata)
	if !ok || chat.viewportPosToItemPoint(startCell, first) == nil {
		t.Fatalf("first selectable boundary is not routed: row=%d metadata=%#v", first, firstMetadata)
	}
	_, endCell, ok := selectionRowCellBounds(lastMetadata)
	if !ok || chat.nearestSelectableViewportPoint(endCell, last) == nil {
		t.Fatalf("last selectable boundary is not routed: row=%d metadata=%#v", last, lastMetadata)
	}
	selected := &Selection{}
	selected.startForChat(startCell, first, chat)
	selected.updateForChat(endCell, last, chat)
	selected.finishForChat(endCell, last, chat)
	extracted := selected.ExtractTextFromChat(chat)
	if !strings.Contains(extracted, "👩‍💻") || !strings.Contains(extracted, "e\u0301") || strings.Contains(extracted, userMessageBarGlyph) {
		t.Fatalf("selection extraction does not match projected content: %q", extracted)
	}
}

func TestP411FixedBoxModalPlacementUsesProjectedFrame(t *testing.T) {
	t.Parallel()

	profile := DefaultDisplayCellProfile()
	projection := contentProjectFixedBox(
		profile,
		lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1),
		8,
		0,
		"\t👩‍💻",
	)
	frame, geometry := modalCenteredOverlay(profile, "", projection.rendered, 40, 10)
	if geometry.outer.Width != projection.outer.Width || geometry.outer.Height != projection.outer.Height {
		t.Fatalf("modal geometry = %+v, fixed projection = %+v", geometry.outer, projection.outer)
	}
	frameRows := strings.Split(frame, "\n")
	for row := geometry.outer.Y; row < geometry.outer.Y+geometry.outer.Height; row++ {
		if got := profile.measure(frameRows[row], 0); got != 40 {
			t.Fatalf("overlay row %d width = %d, want 40", row, got)
		}
	}
}

func assertP411ProjectionExact(
	t *testing.T,
	profile DisplayCellProfile,
	projection contentFixedBoxProjection,
) {
	t.Helper()
	if projection.outer.X != 0 || projection.outer.Y != 0 ||
		len(projection.rows) != projection.outer.Height {
		t.Fatalf("projection geometry = outer:%+v rows:%d", projection.outer, len(projection.rows))
	}
	for row, line := range projection.rows {
		if got := profile.measure(line, 0); got != projection.outer.Width {
			t.Fatalf("row %d width = %d, want %d: %q", row, got, projection.outer.Width, line)
		}
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
