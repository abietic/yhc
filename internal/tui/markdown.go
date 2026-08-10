package tui

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	gtext "github.com/yuin/goldmark/text"
)

type markdownTheme struct {
	name         ThemeName
	ansi         bool
	brand        string
	sky          string
	violet       string
	element      string
	borderSubtle string
	inactive     string
	subtle       string
	success      string
	error        string
	warning      string
}

type rendererKey struct {
	width                int
	theme                markdownTheme
	colorProfile         termenv.Profile
	displayCellProfileID string
	themeGen             uint64
	geometryGen          uint64
	selection            bool
}

var (
	streamingMarkdownParser   = goldmark.New(goldmark.WithExtensions(extension.GFM))
	streamingMarkdownParserMu sync.Mutex
)

func markdownThemeForName(name ThemeName) markdownTheme {
	name = canonicalThemeName(name)
	if !isSupportedTheme(name) {
		name = ThemePolarNight
	}
	palette := getPalette(name)
	return markdownTheme{
		name:         name,
		ansi:         name == ThemeDarkAnsi || name == ThemeLightAnsi,
		brand:        tuiColorString(palette.brand),
		sky:          tuiColorString(palette.auroraSky),
		violet:       tuiColorString(palette.permission),
		element:      tuiColorString(palette.element),
		borderSubtle: tuiColorString(palette.hintBorder),
		inactive:     tuiColorString(palette.inactive),
		subtle:       tuiColorString(palette.subtle),
		success:      tuiColorString(palette.green),
		error:        tuiColorString(palette.red),
		warning:      tuiColorString(palette.warning),
	}
}

func (t markdownTheme) colorProfile() termenv.Profile {
	if t.ansi {
		return termenv.ANSI
	}
	return termenv.TrueColor
}

func getRendererWithProfile(
	width int,
	theme markdownTheme,
	profile DisplayCellProfile,
) *markdownRendererEntry {
	env := defaultRenderEnvironment(StylesForTheme(theme.name))
	env.profile = profile
	return getRendererWithEnvironment(width, theme, env)
}

func getRendererWithEnvironment(
	width int,
	theme markdownTheme,
	env RenderEnvironment,
) *markdownRendererEntry {
	return getRendererWithEnvironmentMode(width, theme, env, false)
}

func getSelectionRendererWithEnvironment(
	width int,
	theme markdownTheme,
	env RenderEnvironment,
) *markdownRendererEntry {
	return getRendererWithEnvironmentMode(width, theme, env, true)
}

func getRendererWithEnvironmentMode(
	width int,
	theme markdownTheme,
	env RenderEnvironment,
	selection bool,
) *markdownRendererEntry {
	// Package tests retain a synthetic non-empty profile ID seam for proving
	// cache separation. Production environments are normalized at construction;
	// zero-value compatibility input still receives a valid profile here.
	if env.profile.id == "" {
		env = env.normalized()
	} else {
		env = env.withRendererPool()
	}
	environment := env.identity()
	key := rendererKey{
		width:                width,
		theme:                theme,
		colorProfile:         theme.colorProfile(),
		displayCellProfileID: environment.displayCellProfileID,
		themeGen:             environment.themeGen,
		geometryGen:          environment.geometryGen,
		selection:            selection,
	}
	return env.rendererPool.acquire(key, func() (*glamour.TermRenderer, error) {
		style := markdownStyle(theme)
		if selection {
			style = markdownSelectionStyle(style)
		}
		// Glamour's custom Chroma formatter always emits a terminal256 stream and
		// registers one process-global theme. ANSI themes use the plain code-block
		// fallback so their complete Markdown output remains ANSI-16-only.
		if theme.ansi {
			style.CodeBlock.Chroma = nil
		}
		return glamour.NewTermRenderer(
			glamour.WithStyles(style),
			glamour.WithWordWrap(width),
		)
	})
}

func markdownSelectionStyle(style ansi.StyleConfig) ansi.StyleConfig {
	presentationPrefix := func(value string) string {
		if value == "" {
			return ""
		}
		return selectionPresentation(value)
	}
	semanticBlock := func(block *ansi.StyleBlock) {
		block.BlockPrefix += selectionMarkSemanticStart
		block.BlockSuffix = selectionMarkSemanticEnd +
			selectionMarkHardBoundary + block.BlockSuffix
	}

	for _, block := range []*ansi.StyleBlock{
		&style.Paragraph,
		&style.H1,
		&style.H2,
		&style.H3,
		&style.H4,
		&style.H5,
		&style.H6,
	} {
		semanticBlock(block)
	}

	style.Item.BlockPrefix = presentationPrefix(style.Item.BlockPrefix)
	style.Item.BlockSuffix = selectionMarkHardBoundary +
		style.Item.BlockSuffix
	style.List.BlockPrefix += selectionMarkSemanticStart
	style.List.BlockSuffix = selectionMarkSemanticEnd +
		selectionMarkHardBoundary + style.List.BlockSuffix
	style.Enumeration.BlockPrefix = presentationPrefix(
		style.Enumeration.BlockPrefix,
	)
	style.Task.Ticked = presentationPrefix(style.Task.Ticked)
	style.Task.Unticked = presentationPrefix(style.Task.Unticked)
	if style.BlockQuote.IndentToken != nil {
		token := presentationPrefix(*style.BlockQuote.IndentToken)
		style.BlockQuote.IndentToken = &token
	}
	style.HorizontalRule.Format = selectionPresentation(
		style.HorizontalRule.Format,
	)
	style.DefinitionDescription.BlockPrefix = presentationPrefix(
		style.DefinitionDescription.BlockPrefix,
	) + selectionMarkSemanticStart
	style.DefinitionDescription.BlockSuffix = selectionMarkSemanticEnd +
		selectionMarkHardBoundary +
		style.DefinitionDescription.BlockSuffix

	style.CodeBlock.BlockPrefix = selectionMarkHardRowsStart +
		selectionMarkSemanticStart + style.CodeBlock.BlockPrefix
	style.CodeBlock.BlockSuffix = selectionMarkSemanticEnd +
		selectionMarkHardRowsEnd + selectionMarkHardBoundary +
		style.CodeBlock.BlockSuffix
	return style
}

// markdownStyle maps the accepted P19.3.0 Markdown surfaces to semantic
// Revontuli colors. Other legacy renderer colors remain for later slices.
func markdownStyle(theme markdownTheme) ansi.StyleConfig {
	boolPtr := func(b bool) *bool { return &b }
	strPtr := func(s string) *string { return &s }
	uintPtr := func(u uint) *uint { return &u }
	colorPtr := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	legacyColor := func(legacy, ansi16 string) *string {
		if theme.ansi {
			return colorPtr(ansi16)
		}
		return strPtr(legacy)
	}
	quoteToken := lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.brand)).
		Render("▎") + " "

	return ansi.StyleConfig{
		Document: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: legacyColor("252", "7"),
			},
			Margin: uintPtr(0),
		},
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: colorPtr(theme.inactive),
			},
			Indent:      uintPtr(1),
			IndentToken: strPtr(quoteToken),
		},
		Paragraph: ansi.StyleBlock{},
		List: ansi.StyleList{
			StyleBlock:  ansi.StyleBlock{},
			LevelIndent: 2,
		},
		Heading: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				BlockSuffix: "\n",
				Color:       colorPtr(theme.sky),
				Bold:        boolPtr(true),
			},
		},
		H1: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color:       colorPtr(theme.brand),
				Bold:        boolPtr(true),
				BlockSuffix: "\n══════════════════════════════════════",
			},
		},
		H2: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color:     colorPtr(theme.sky),
				Bold:      boolPtr(true),
				Underline: boolPtr(true),
			},
		},
		H3: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: colorPtr(theme.violet),
				Bold:  boolPtr(true),
			},
		},
		H4: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color: colorPtr(theme.inactive),
				Bold:  boolPtr(true),
			},
		},
		H5: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color:  colorPtr(theme.inactive),
				Italic: boolPtr(true),
			},
		},
		H6: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Color:  colorPtr(theme.subtle),
				Italic: boolPtr(true),
			},
		},
		Text:          ansi.StylePrimitive{},
		Strikethrough: ansi.StylePrimitive{CrossedOut: boolPtr(true)},
		Emph:          ansi.StylePrimitive{Italic: boolPtr(true)},
		Strong:        ansi.StylePrimitive{Bold: boolPtr(true)},
		HorizontalRule: ansi.StylePrimitive{
			Color:  colorPtr(theme.borderSubtle),
			Format: "\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n",
		},
		Item:        ansi.StylePrimitive{BlockPrefix: "• "},
		Enumeration: ansi.StylePrimitive{BlockPrefix: ". "},
		Task: ansi.StyleTask{
			StylePrimitive: ansi.StylePrimitive{
				Color: legacyColor("78", theme.success),
			},
			Ticked:   "☑ ",
			Unticked: "☐ ",
		},
		Link: ansi.StylePrimitive{
			Color: legacyColor("30", theme.sky), Underline: boolPtr(true),
		},
		LinkText: ansi.StylePrimitive{
			Color: legacyColor("35", theme.violet), Bold: boolPtr(true),
		},
		Image: ansi.StylePrimitive{
			Color: legacyColor("212", theme.violet), Underline: boolPtr(true),
		},
		ImageText: ansi.StylePrimitive{
			Color:  legacyColor("243", theme.inactive),
			Format: "Image: {{.text}} →",
		},
		Code: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{
				Prefix:          " ",
				Suffix:          " ",
				Color:           colorPtr(theme.sky),
				BackgroundColor: colorPtr(theme.element),
			},
		},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{
					Color: legacyColor("244", theme.inactive),
				},
				Margin: uintPtr(2),
			},
			Chroma: &ansi.Chroma{
				Text:                ansi.StylePrimitive{},
				Error:               ansi.StylePrimitive{BackgroundColor: colorPtr(theme.error)},
				Comment:             ansi.StylePrimitive{Color: colorPtr(theme.subtle)},
				CommentPreproc:      ansi.StylePrimitive{Color: colorPtr(theme.subtle)},
				Keyword:             ansi.StylePrimitive{Color: colorPtr(theme.violet)},
				KeywordReserved:     ansi.StylePrimitive{Color: colorPtr(theme.violet)},
				KeywordNamespace:    ansi.StylePrimitive{Color: colorPtr(theme.violet)},
				KeywordType:         ansi.StylePrimitive{Color: colorPtr(theme.sky)},
				Operator:            ansi.StylePrimitive{Color: colorPtr(theme.brand)},
				Punctuation:         ansi.StylePrimitive{Color: colorPtr(theme.inactive)},
				Name:                ansi.StylePrimitive{},
				NameBuiltin:         ansi.StylePrimitive{Color: colorPtr(theme.warning)},
				NameTag:             ansi.StylePrimitive{Color: colorPtr(theme.violet)},
				NameAttribute:       ansi.StylePrimitive{Color: colorPtr(theme.sky)},
				NameClass:           ansi.StylePrimitive{Color: colorPtr(theme.brand), Underline: boolPtr(true), Bold: boolPtr(true)},
				NameDecorator:       ansi.StylePrimitive{Color: colorPtr(theme.warning)},
				NameFunction:        ansi.StylePrimitive{Color: colorPtr(theme.brand)},
				LiteralNumber:       ansi.StylePrimitive{Color: colorPtr(theme.sky)},
				LiteralString:       ansi.StylePrimitive{Color: colorPtr(theme.success)},
				LiteralStringEscape: ansi.StylePrimitive{Color: colorPtr(theme.sky)},
				GenericDeleted:      ansi.StylePrimitive{Color: colorPtr(theme.error)},
				GenericEmph:         ansi.StylePrimitive{Italic: boolPtr(true)},
				GenericInserted:     ansi.StylePrimitive{Color: colorPtr(theme.success)},
				GenericStrong:       ansi.StylePrimitive{Bold: boolPtr(true)},
				GenericSubheading:   ansi.StylePrimitive{Color: colorPtr(theme.subtle)},
				Background:          ansi.StylePrimitive{BackgroundColor: colorPtr(theme.element)},
			},
		},
		Table: ansi.StyleTable{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{
					Color: legacyColor("250", theme.inactive),
				},
			},
			CenterSeparator: strPtr("┼"),
			ColumnSeparator: strPtr("│"),
			RowSeparator:    strPtr("─"),
		},
		DefinitionDescription: ansi.StylePrimitive{BlockPrefix: "\n🠶 "},
	}
}

// --- Unlabeled code block normalization ---

// labelUnlabeledCodeBlocks rewrites fenced code blocks that have no language
// tag so they explicitly use "text" as the language. This prevents chroma's
// auto-detection from applying syntax highlighting to code blocks where the
// author did not specify a language — matching the acceptance criterion that
// unlabeled code blocks should render as plain monospace.
//
// Only opening fences are modified; closing fences and fenced blocks that
// already have a language annotation are left untouched.
func labelUnlabeledCodeBlocks(content string) string {
	if !strings.Contains(content, "```") && !strings.Contains(content, "~~~") {
		return content
	}

	lines := strings.Split(content, "\n")
	inFence := false
	var fenceChar byte
	modified := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inFence {
			// Check if this line closes the fence
			if len(trimmed) >= 3 && trimmed[0] == fenceChar {
				allSame := true
				for j := range trimmed {
					if trimmed[j] != fenceChar {
						allSame = false
						break
					}
				}
				if allSame {
					inFence = false
				}
			}
			continue
		}

		// Check if this is an opening fence
		if len(trimmed) < 3 {
			continue
		}
		ch := trimmed[0]
		if ch != '`' && ch != '~' {
			continue
		}
		run := 0
		for run < len(trimmed) && trimmed[run] == ch {
			run++
		}
		if run < 3 {
			continue
		}

		// This is a fence opening. Check if it has a language tag.
		rest := strings.TrimSpace(trimmed[run:])
		if rest == "" {
			// No language tag — add "text" to prevent chroma auto-detection.
			// Preserve original indentation by replacing only the fence portion.
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + strings.Repeat(string(ch), run) + "text"
			modified = true
		}
		inFence = true
		fenceChar = ch
	}

	if !modified {
		return content
	}
	return strings.Join(lines, "\n")
}

// prepareForGlamour applies all content transformations before passing
// markdown content to glamour for rendering: labels unlabeled code blocks
// to prevent chroma auto-detection, then normalizes soft breaks.
func prepareForGlamour(content string) string {
	return normalizeSoftBreaks(labelUnlabeledCodeBlocks(content))
}

// --- Plain-text fast path ---

var mdSyntaxRe = regexp.MustCompile("[#*`|\\[>\\-_~]|^\\d+\\. |\\n\\d+\\. ")

func hasMarkdownSyntax(content string) bool {
	return mdSyntaxRe.MatchString(content)
}

// wrapPlainText performs simple word wrapping on plain text at the given width.
// Follows CommonMark soft-break semantics: single \n becomes a space (soft break).
// Paragraph separators (\n\n) are preserved only when both adjacent paragraphs
// are substantial (>= minParagraphLen chars); otherwise they are collapsed as
// streaming artifacts to prevent character-per-line display.
func wrapPlainText(
	profile DisplayCellProfile,
	s string,
	width int,
) string {
	if width <= 0 {
		return s
	}

	const minParagraphLen = 3

	// Split into paragraphs on \n\n (paragraph breaks).
	paragraphs := strings.Split(s, "\n\n")
	var result strings.Builder
	prevParaLen := 0
	for pi, para := range paragraphs {
		// Within a paragraph, normalize single \n to space (CommonMark soft break).
		normalized := strings.ReplaceAll(para, "\n", " ")
		trimmed := strings.TrimSpace(normalized)

		if pi > 0 {
			// Preserve paragraph break only if both sides are substantial.
			if prevParaLen >= minParagraphLen && len(trimmed) >= minParagraphLen {
				result.WriteString("\n\n")
			} else {
				// Streaming artifact — collapse to space.
				result.WriteByte(' ')
			}
		}

		// Word-wrap the normalized paragraph.
		col := 0
		for j, word := range strings.Fields(normalized) {
			wlen := profile.width(word)
			if j == 0 {
				result.WriteString(word)
				col = wlen
			} else if col+1+wlen > width {
				result.WriteByte('\n')
				result.WriteString(word)
				col = wlen
			} else {
				result.WriteByte(' ')
				result.WriteString(word)
				col += 1 + wlen
			}
		}
		prevParaLen = len(trimmed)
	}
	return result.String()
}

// normalizeSoftBreaks normalizes single \n within paragraphs to spaces before
// passing content to glamour. This prevents goldmark from misinterpreting
// streaming content as setext headings (e.g., "word1\nword2\n-" where the
// trailing "-" causes goldmark to parse "word1\nword2" as an H2 heading,
// rendering each word on its own line).
//
// Also collapses blank-line paragraph separators (\n\n) when the surrounding
// content is short (< minParagraphLen chars), treating them as streaming
// artifacts rather than intentional paragraph breaks. This prevents the
// character-per-line display that occurs when model streaming APIs insert
// \n\n between individual tokens.
//
// Preserves \n for: fenced code blocks, and lines that begin block-level
// markdown constructs (lists, headings, quotes, etc.).
func normalizeSoftBreaks(content string) string {
	if !strings.Contains(content, "\n") {
		return content
	}

	lines := strings.Split(content, "\n")
	if len(lines) <= 1 {
		return content
	}

	// Minimum trimmed length for a line to be considered part of an
	// intentional paragraph boundary. Below this threshold, blank lines
	// between content are treated as streaming artifacts and collapsed.
	const minParagraphLen = 3

	var result strings.Builder
	result.Grow(len(content))
	result.WriteString(lines[0])

	inFence := isFenceLineLight(lines[0])
	// Track last non-empty line for paragraph break decisions.
	lastNonEmpty := strings.TrimSpace(lines[0])

	for i := 1; i < len(lines); i++ {
		line := lines[i]
		prevLine := lines[i-1]

		// Toggle fenced code block state
		if inFence {
			// Always preserve \n inside fenced code blocks
			result.WriteByte('\n')
			result.WriteString(line)
			if isFenceLineLight(line) {
				inFence = false
			}
			if strings.TrimSpace(line) != "" {
				lastNonEmpty = strings.TrimSpace(line)
			}
			continue
		}

		if isFenceLineLight(line) {
			result.WriteByte('\n')
			result.WriteString(line)
			inFence = true
			lastNonEmpty = strings.TrimSpace(line)
			continue
		}

		// Handle blank lines (paragraph separators).
		// Collapse to soft break when surrounding content is short (streaming artifact)
		// AND neither side is a block-level markdown construct.
		if line == "" || prevLine == "" {
			if line == "" {
				// Empty line: write it only if we'll keep the paragraph break.
				// Defer decision to when we hit the next non-empty line.
				result.WriteByte('\n')
				result.WriteString(line)
				continue
			}
			// prevLine == "" : we're the first non-empty line after a blank line.
			// Decide: keep paragraph break or collapse as streaming artifact?
			trimmedLine := strings.TrimSpace(line)
			// Always preserve blank lines around block-level constructs
			// (headings, HRs, lists, quotes, etc.) regardless of length.
			if isBlockLevelLine(line) || isBlockLevelLine(lastNonEmpty) {
				result.WriteByte('\n')
				result.WriteString(line)
			} else if len(lastNonEmpty) >= minParagraphLen && len(trimmedLine) >= minParagraphLen {
				// Both sides are substantial — preserve paragraph break.
				result.WriteByte('\n')
				result.WriteString(line)
			} else {
				// At least one side is short — streaming artifact, collapse.
				// Remove the previously written blank line(s) and join with space.
				// Trim trailing \n sequences from result (the blank lines we wrote).
				s := result.String()
				s = strings.TrimRight(s, "\n")
				result.Reset()
				result.WriteString(s)
				result.WriteByte(' ')
				result.WriteString(line)
			}
			if trimmedLine != "" {
				lastNonEmpty = trimmedLine
			}
			continue
		}

		// Preserve \n before block-level constructs
		if isBlockLevelLine(line) {
			result.WriteByte('\n')
			result.WriteString(line)
			lastNonEmpty = strings.TrimSpace(line)
			continue
		}

		// Preserve \n after block-level constructs (the next line continues
		// or follows a block, e.g., table rows, list items).
		if isBlockLevelLine(prevLine) {
			result.WriteByte('\n')
			result.WriteString(line)
			lastNonEmpty = strings.TrimSpace(line)
			continue
		}

		// Soft break: normalize to space
		result.WriteByte(' ')
		result.WriteString(line)
		if strings.TrimSpace(line) != "" {
			lastNonEmpty = strings.TrimSpace(line)
		}
	}

	return result.String()
}

// isFenceLine checks if a line opens/closes a fenced code block (``` or ~~~).
// Note: a more rigorous version exists later in this file for boundary detection;
// this lightweight version is sufficient for the soft-break normalizer since
// false negatives simply preserve more newlines (safe).
func isFenceLineLight(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

// isBlockLevelLine checks if a line starts a block-level markdown construct
// that should NOT be joined with the previous line.
func isBlockLevelLine(line string) bool {
	if len(line) == 0 {
		return false
	}
	// ATX headings
	if line[0] == '#' {
		return true
	}
	// Block quotes
	if line[0] == '>' {
		return true
	}
	// Table rows (lines starting with |)
	if line[0] == '|' {
		return true
	}
	// Unordered list markers: "- ", "* ", "+ "
	if len(line) >= 2 && (line[0] == '-' || line[0] == '*' || line[0] == '+') && line[1] == ' ' {
		return true
	}
	// Thematic breaks and setext underlines: lines of only -, *, =, or _
	// (possibly with spaces). These are block markers.
	trimmed := strings.TrimSpace(line)
	if len(trimmed) > 0 && isThematicOrSetext(trimmed) {
		return true
	}
	// Ordered list markers: "1. ", "2. " etc.
	if len(line) >= 3 && line[0] >= '0' && line[0] <= '9' {
		for j := 1; j < len(line); j++ {
			if line[j] >= '0' && line[j] <= '9' {
				continue
			}
			if line[j] == '.' && j+1 < len(line) && line[j+1] == ' ' {
				return true
			}
			break
		}
	}
	// Indented code blocks (4+ spaces)
	if strings.HasPrefix(line, "    ") || line[0] == '\t' {
		return true
	}
	return false
}

// isThematicOrSetext returns true if the line is only composed of one repeated
// character from {-, *, _, =} optionally interspersed with spaces.
func isThematicOrSetext(s string) bool {
	if len(s) == 0 {
		return false
	}
	var ch byte
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			continue
		}
		if s[i] == '-' || s[i] == '*' || s[i] == '_' || s[i] == '=' {
			if ch == 0 {
				ch = s[i]
			} else if s[i] != ch {
				return false
			}
		} else {
			return false
		}
	}
	return ch != 0
}

// --- StreamingMarkdown ---

// StreamingMarkdown keeps a source-backed stable region and mutable tail so
// each streaming flush only re-renders the last top-level Markdown block.
// The region split combines Claude Code's parser-derived block boundary with
// Crush's synchronous rendered-prefix cache. Active lists, tables, and fenced
// blocks remain in the tail until a following block proves them complete.
//
// Two renders concatenated are NOT equal to a single render of the whole
// document — glamour's wrap state resets between calls. The boundary check
// is therefore deliberately conservative; on any doubt it falls back to a
// full render and leaves the cache untouched.
//
// Zero value is ready to use. Width is set on first Render call.
type StreamingMarkdown struct {
	width               int
	theme               markdownTheme
	profile             DisplayCellProfile
	environment         renderEnvironmentIdentity
	stablePrefix        string // newline-complete source before the mutable block
	stableRender        string // rendered stablePrefix (trimmed)
	stableCacheIdentity markdownRenderIdentity
	fullCache           string // cached full render
	fullCacheIdentity   markdownRenderIdentity
	lastFullRender      int64 // UnixMilli of last expensive full render (for throttling)
	finalized           bool
	finalizedSource     string
	selection           bool
	compatibilityPool   *markdownRendererPool
}

type markdownCompleteness uint8

const (
	markdownStreamingIncomplete markdownCompleteness = iota
	markdownStableComplete
	markdownFinalizedComplete
)

type markdownRenderIdentity struct {
	source               string
	width                int
	theme                markdownTheme
	colorProfile         termenv.Profile
	displayCellProfileID string
	themeGen             uint64
	geometryGen          uint64
	completeness         markdownCompleteness
}

func newMarkdownRenderIdentity(
	source string,
	width int,
	theme markdownTheme,
	profile DisplayCellProfile,
	environment renderEnvironmentIdentity,
	completeness markdownCompleteness,
) markdownRenderIdentity {
	return markdownRenderIdentity{
		source:               source,
		width:                width,
		theme:                theme,
		colorProfile:         theme.colorProfile(),
		displayCellProfileID: profile.id,
		themeGen:             environment.themeGen,
		geometryGen:          environment.geometryGen,
		completeness:         completeness,
	}
}

// ForceRender resets the throttle timestamp so the next Render call
// performs a full re-render regardless of time elapsed.
func (s *StreamingMarkdown) ForceRender() {
	s.lastFullRender = 0
}

// Render renders markdown content with crush's streaming prefix algorithm.
// Acquires per-renderer mutex internally to serialize goldmark access.
func (s *StreamingMarkdown) Render(content string, width int, themeName ThemeName) string {
	env := s.compatibilityRenderEnvironment(StylesForTheme(themeName))
	return s.renderWithEnvironment(content, width, env)
}

func (s *StreamingMarkdown) compatibilityRenderEnvironment(styles Styles) RenderEnvironment {
	env := defaultRenderEnvironment(styles)
	if s.compatibilityPool == nil {
		s.compatibilityPool = env.rendererPool
	} else {
		env.rendererPool = s.compatibilityPool
	}
	return env
}

// renderWithProfile is the package test seam for cache-generation and reflow
// behavior. Production App paths use renderWithEnvironment.
func (s *StreamingMarkdown) renderWithProfile(
	content string,
	width int,
	themeName ThemeName,
	profile DisplayCellProfile,
) {
	env := s.compatibilityRenderEnvironment(StylesForTheme(themeName))
	env.profile = profile
	s.renderWithEnvironment(content, width, env)
}

// renderWithEnvironment is the production projection boundary.
func (s *StreamingMarkdown) renderWithEnvironment(content string, width int, env RenderEnvironment) string {
	return s.renderWithEnvironmentMode(content, width, env, false)
}

func (s *StreamingMarkdown) renderSelectionWithEnvironment(
	content string,
	width int,
	env RenderEnvironment,
) (string, bool) {
	if selectionAnnotationsCollide(content) {
		return s.renderWithEnvironmentMode(content, width, env, false), false
	}
	return s.renderWithEnvironmentMode(content, width, env, true), true
}

func (s *StreamingMarkdown) renderWithEnvironmentMode(
	content string,
	width int,
	env RenderEnvironment,
	selection bool,
) string {
	// renderWithProfile remains a cache-identity seam and can deliberately use
	// a synthetic non-empty profile ID. Production environments are normalized
	// at construction, while zero-value compatibility input falls back here.
	if env.profile.id == "" {
		env = env.normalized()
	} else {
		env = env.withRendererPool()
	}
	themeName, profile := env.styles.theme, env.profile
	environment := env.identity()
	if s.selection != selection {
		s.resetRenderCache()
		s.selection = selection
	}
	if content == "" {
		return ""
	}
	if width < 10 {
		width = 10
	}
	if profile.id == "" {
		profile = DefaultDisplayCellProfile()
	}

	theme := markdownThemeForName(themeName)
	if s.theme != theme || s.profile != profile ||
		s.environment != environment {
		s.resetRenderCache()
		s.theme = theme
		s.profile = profile
		s.environment = environment
	}

	if s.finalized && content != s.finalizedSource {
		// A new append-only stream started after the previous item finalized.
		s.resetInternal()
	}

	completeness := markdownStreamingIncomplete
	if s.finalized {
		completeness = markdownFinalizedComplete
	}
	fullIdentity := newMarkdownRenderIdentity(
		content,
		width,
		theme,
		profile,
		environment,
		completeness,
	)

	// Fast path: exact match with full cache.
	if s.fullCacheIdentity == fullIdentity {
		return s.fullCache
	}

	// Plain-text fast path: no markdown syntax → word-wrap and return
	if !hasMarkdownSyntax(content) {
		if width != s.width ||
			(s.stablePrefix != "" && !strings.HasPrefix(content, s.stablePrefix)) {
			s.resetRenderCache()
		}
		wrapped := wrapPlainText(profile, content, width)
		if selection {
			wrapped = selectionAnnotatePlainMarkdownRendered(
				profile,
				wrapped,
			)
		}
		s.fullCache = wrapped
		s.fullCacheIdentity = fullIdentity
		s.width = width
		return wrapped
	}

	entry := getRendererWithEnvironment(width, theme, env)
	if selection {
		entry = getSelectionRendererWithEnvironment(width, theme, env)
	}
	if entry == nil {
		return renderSafeMarkdownLiteral(content, width, profile)
	}

	// The entry retains its renderer and lock even if the pool evicts its index
	// ownership while this render is in flight.
	entry.mu.Lock()
	defer entry.mu.Unlock()
	renderer := entry.renderer

	full := func() string {
		return renderMarkdownFragment(
			content,
			width,
			theme,
			profile,
			completeness,
			renderer,
			selection,
		)
	}

	// A terminal stream is rendered once from its complete source. This drops
	// fragment seams and gives future width changes one canonical reflow input.
	if s.finalized {
		out := full()
		s.width = width
		s.fullCache = out
		s.fullCacheIdentity = fullIdentity
		s.lastFullRender = time.Now().UnixMilli()
		return out
	}

	// Width change OR content not a prefix-extension: drop cache,
	// full render, try to seed a fresh boundary.
	if width != s.width || !strings.HasPrefix(content, s.stablePrefix) {
		s.resetInternal()
		s.width = width
		out := full()
		s.tryAdvanceFromEmpty(content, width, renderer, profile, environment)
		s.fullCache = out
		s.fullCacheIdentity = fullIdentity
		s.lastFullRender = time.Now().UnixMilli()
		return out
	}

	stableLen := len(s.stablePrefix)
	mutableSource := content[stableLen:]
	boundary := findSafeMarkdownBoundary(mutableSource)
	var result string
	if boundary > 0 {
		// Promote every complete top-level block before the final mutable one.
		newChunk := mutableSource[:boundary]
		newChunkRender := s.renderFragment(
			newChunk,
			renderer,
			profile,
			markdownStableComplete,
		)
		s.stableRender = glueRenders(profile, s.stableRender, newChunkRender)
		s.stablePrefix = content[:stableLen+boundary]
		s.stableCacheIdentity = newMarkdownRenderIdentity(
			s.stablePrefix,
			width,
			theme,
			profile,
			environment,
			markdownStableComplete,
		)

		trail := mutableSource[boundary:]
		if trail == "" {
			result = s.stableRender
		} else {
			result = glueRenders(
				profile,
				s.stableRender,
				s.renderFragment(
					trail,
					renderer,
					profile,
					markdownStreamingIncomplete,
				),
			)
		}
	} else if s.stablePrefix != "" {
		// The last block is still mutable, but the committed source never needs
		// to be parsed or rendered again.
		result = glueRenders(
			profile,
			s.stableRender,
			s.renderFragment(
				mutableSource,
				renderer,
				profile,
				markdownStreamingIncomplete,
			),
		)
	} else {
		// No block is stable yet. Throttle whole-document work to ~15fps.
		now := time.Now().UnixMilli()
		if s.fullCache != "" &&
			s.fullCacheIdentity.completeness == markdownStreamingIncomplete &&
			s.fullCacheIdentity.width == width &&
			s.fullCacheIdentity.theme == theme &&
			s.fullCacheIdentity.colorProfile == theme.colorProfile() &&
			s.fullCacheIdentity.displayCellProfileID == profile.id &&
			s.fullCacheIdentity.themeGen == env.themeGen &&
			s.fullCacheIdentity.geometryGen == env.geometryGen &&
			strings.HasPrefix(content, s.fullCacheIdentity.source) &&
			now-s.lastFullRender < 66 {
			return s.fullCache
		}
		result = full()
		s.lastFullRender = now
	}

	s.fullCache = result
	s.fullCacheIdentity = fullIdentity
	return result
}

// tryAdvanceFromEmpty seeds the cache from a fresh state.
func (s *StreamingMarkdown) tryAdvanceFromEmpty(
	content string,
	width int,
	renderer *glamour.TermRenderer,
	profile DisplayCellProfile,
	environment renderEnvironmentIdentity,
) {
	boundary := findSafeMarkdownBoundary(content)
	if boundary <= 0 {
		return
	}
	prefix := content[:boundary]
	s.stablePrefix = prefix
	s.stableRender = s.renderFragment(
		prefix,
		renderer,
		profile,
		markdownStableComplete,
	)
	s.stableCacheIdentity = newMarkdownRenderIdentity(
		prefix,
		width,
		s.theme,
		profile,
		environment,
		markdownStableComplete,
	)
	s.width = width
}

func (s *StreamingMarkdown) renderFragment(
	text string,
	renderer *glamour.TermRenderer,
	profile DisplayCellProfile,
	completeness markdownCompleteness,
) string {
	if text == "" {
		return ""
	}
	return renderMarkdownFragment(
		text,
		s.width,
		s.theme,
		profile,
		completeness,
		renderer,
		s.selection,
	)
}

// Finalize ends the append-only lifecycle. The next render is forced through
// the complete source instead of concatenating cached fragments.
func (s *StreamingMarkdown) Finalize(content string) {
	s.resetInternal()
	s.finalized = true
	s.finalizedSource = content
}

// Reset clears the cache.
func (s *StreamingMarkdown) Reset() {
	s.resetInternal()
	s.theme = markdownTheme{}
	s.profile = DisplayCellProfile{}
	s.environment = renderEnvironmentIdentity{}
}

func (s *StreamingMarkdown) resetInternal() {
	s.resetRenderCache()
	s.finalized = false
	s.finalizedSource = ""
}

func (s *StreamingMarkdown) resetRenderCache() {
	s.width = 0
	s.stablePrefix = ""
	s.stableRender = ""
	s.stableCacheIdentity = markdownRenderIdentity{}
	s.fullCache = ""
	s.fullCacheIdentity = markdownRenderIdentity{}
	s.lastFullRender = 0
}

func renderMarkdownFragment(
	content string,
	width int,
	theme markdownTheme,
	profile DisplayCellProfile,
	completeness markdownCompleteness,
	renderer *glamour.TermRenderer,
	selection bool,
) string {
	prepared := prepareForGlamour(content)
	stripped, tables, ok := extractTableIslands(prepared, completeness)
	if !ok {
		return renderSafeMarkdownLiteral(content, width, profile)
	}
	out, err := renderer.Render(stripped)
	if err != nil {
		return renderSafeMarkdownLiteral(content, width, profile)
	}
	out = strings.TrimSuffix(out, "\n")
	out = stripGlamourHyperlinks(profile, out)
	out, ok = spliceTableIslandsMode(
		out,
		tables,
		width,
		theme,
		profile,
		selection,
	)
	if !ok {
		return renderSafeMarkdownLiteral(content, width, profile)
	}
	return trimGlamourMargins(profile, out)
}

func stripGlamourHyperlinks(
	profile DisplayCellProfile,
	rendered string,
) string {
	var plain strings.Builder
	for _, cluster := range profile.clusters(rendered, 0) {
		if cluster.control && isOSC8Sequence(cluster.source) {
			continue
		}
		plain.WriteString(cluster.source)
	}
	return plain.String()
}

// --- Render helpers ---

// glueRenders concatenates two glamour-rendered fragments with a single
// blank line separator. Trimming both sides prevents doubled margins.
func glueRenders(
	profile DisplayCellProfile,
	prefix, trail string,
) string {
	prefix = trimGlamourMargins(profile, prefix)
	trail = trimGlamourMargins(profile, trail)
	switch {
	case prefix == "" && trail == "":
		return ""
	case prefix == "":
		return trail
	case trail == "":
		return prefix
	default:
		return prefix + "\n\n" + trail
	}
}

// trimGlamourMargins strips leading/trailing newlines and trailing whitespace
// from glamour output. Uses ANSI-aware stripping to handle styled padding
// that glamour adds to fill lines to the document width.
func trimGlamourMargins(profile DisplayCellProfile, s string) string {
	s = strings.TrimLeft(s, "\n")
	// Strip trailing whitespace: glamour pads lines with ANSI-styled spaces
	// (e.g. \x1b[38;5;252m \x1b[0m) that strings.TrimRight can't remove.
	// Process line-by-line to remove per-line trailing padding.
	lines := strings.Split(s, "\n")
	// Glamour can prefix a fragment with an ANSI-styled padding-only line.
	var leadingMarkers strings.Builder
	for len(lines) > 0 {
		stripped := selectionStripMarkers(xansi.Strip(lines[0]))
		if strings.TrimSpace(stripped) != "" {
			break
		}
		for _, marker := range selectionMarkerSequence(lines[0]) {
			leadingMarkers.WriteString(marker)
		}
		lines = lines[1:]
	}
	// Trim trailing empty/whitespace-only lines
	var trailingMarkerGroups [][]string
	for len(lines) > 0 {
		stripped := selectionStripMarkers(
			xansi.Strip(lines[len(lines)-1]),
		)
		if strings.TrimSpace(stripped) != "" {
			break
		}
		trailingMarkerGroups = append(
			trailingMarkerGroups,
			selectionMarkerSequence(lines[len(lines)-1]),
		)
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 0 {
		lines[0] = leadingMarkers.String() + lines[0]
		for index := len(trailingMarkerGroups) - 1; index >= 0; index-- {
			for _, marker := range trailingMarkerGroups[index] {
				lines[len(lines)-1] += marker
			}
		}
	}
	// Trim trailing whitespace from each remaining line (ANSI-aware)
	for i, line := range lines {
		stripped := selectionStripMarkers(xansi.Strip(line))
		trimmed := strings.TrimRight(stripped, " \t")
		if len(trimmed) < len(stripped) {
			// Preserve control balancing while removing only the visible
			// trailing cells selected by the App profile.
			if selectionAnnotationsCollide(line) {
				lines[i] = selectionTrimAnnotatedPadding(
					profile,
					line,
					profile.width(trimmed),
				)
			} else {
				lines[i] = profile.truncate(line, profile.width(trimmed))
			}
		}
	}
	return strings.Join(lines, "\n")
}

// --- Streaming block boundary detection ---

// findSafeMarkdownBoundary returns the source start of the final top-level
// Markdown block. Everything before that block is immutable under append-only
// streaming and can be promoted into the stable region. The final block stays
// mutable, which naturally holds active lists, tables, setext headings, HTML,
// and fenced code until a following block appears.
func findSafeMarkdownBoundary(content string) int {
	if content == "" {
		return -1
	}

	source := []byte(content)
	streamingMarkdownParserMu.Lock()
	doc := streamingMarkdownParser.Parser().Parse(gtext.NewReader(source))
	first := doc.FirstChild()
	if first == nil || first.NextSibling() == nil {
		streamingMarkdownParserMu.Unlock()
		return -1
	}
	start, ok := markdownBlockSourceStart(doc.LastChild(), source)
	streamingMarkdownParserMu.Unlock()
	if !ok || start <= 0 || start > len(content) || content[start-1] != '\n' {
		return findConservativeMarkdownBoundary(content)
	}
	if hasGlobalMarkdownReferenceHazard(content[:start]) {
		return -1
	}
	return start
}

func findConservativeMarkdownBoundary(content string) int {
	for p := blankLineBefore(content, len(content)); p > 0; p = blankLineBefore(content, p-1) {
		if isSafeBoundaryAt(content, p) {
			return p
		}
	}
	return -1
}

// blankLineBefore returns the byte offset of the first character AFTER the
// latest blank-line separator that ends strictly before until.
func blankLineBefore(content string, until int) int {
	if until <= 0 {
		return -1
	}
	end := until
	for end > 0 {
		nl := strings.LastIndexByte(content[:end], '\n')
		if nl < 0 {
			return -1
		}
		prev := strings.LastIndexByte(content[:nl], '\n')
		for prev >= 0 {
			gap := content[prev+1 : nl]
			if isBlankOrSpaces(gap) {
				return nl + 1
			}
			break
		}
		end = nl
	}
	return -1
}

// isBlankOrSpaces reports whether s consists entirely of spaces and tabs.
func isBlankOrSpaces(s string) bool {
	for i := range len(s) {
		if s[i] != ' ' && s[i] != '\t' {
			return false
		}
	}
	return true
}

// isSafeBoundaryAt reports whether content[:p] is a safe stable prefix.
func isSafeBoundaryAt(content string, p int) bool {
	prefix := content[:p]

	// Even number of fence lines (no open fenced block).
	if countFenceLines(prefix)%2 != 0 {
		return false
	}

	// Anywhere-in-prefix hazards: list, HTML block, link ref definition.
	if prefixHasOpenHazard(prefix) {
		return false
	}

	// Last non-blank line must not open a construct.
	lastLine := lastNonBlankLine(prefix)
	if lastLine != "" && lineOpensConstruct(lastLine) {
		return false
	}

	// Next line after boundary must not look like a setext underline.
	if rest := content[p:]; rest != "" {
		first := firstNonBlankLine(rest)
		if isSetextUnderlineCandidate(first) {
			return false
		}
	}

	return true
}

// prefixHasOpenHazard checks for list markers, HTML block openers, and
// link reference definitions anywhere in prefix (outside fenced blocks).
func prefixHasOpenHazard(prefix string) bool {
	inFence := false
	for line := range splitLines(prefix) {
		if isFenceLine(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		if isListItemMarker(trimmed) {
			return true
		}
		if isHTMLBlockOpener(line) {
			return true
		}
		if isLinkRefDefinition(line) {
			return true
		}
	}
	return false
}

// countFenceLines counts lines that open or close a fenced code block.
func countFenceLines(s string) int {
	n := 0
	for line := range splitLines(s) {
		if isFenceLine(line) {
			n++
		}
	}
	return n
}

// isFenceLine reports whether line opens or closes a fenced code block.
func isFenceLine(line string) bool {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) {
		return false
	}
	c := line[i]
	if c != '`' && c != '~' {
		return false
	}
	run := 0
	for i < len(line) && line[i] == c {
		i++
		run++
	}
	return run >= 3
}

// lastNonBlankLine returns the last non-blank line of s.
func lastNonBlankLine(s string) string {
	last := ""
	for line := range splitLines(s) {
		if strings.TrimSpace(line) != "" {
			last = line
		}
	}
	return last
}

// firstNonBlankLine returns the first non-blank line of s.
func firstNonBlankLine(s string) string {
	for line := range splitLines(s) {
		if strings.TrimSpace(line) != "" {
			return line
		}
	}
	return ""
}

// splitLines yields lines of s without terminators.
func splitLines(s string) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		start := 0
		for i := 0; i < len(s); i++ {
			if s[i] == '\n' {
				if !yield(s[start:i]) {
					return
				}
				start = i + 1
			}
		}
		if start <= len(s)-1 {
			yield(s[start:])
		}
	}
}

// lineOpensConstruct reports whether line keeps a markdown construct open.
func lineOpensConstruct(line string) bool {
	if len(line) > 0 && line[0] == '\t' {
		return true
	}
	if strings.HasPrefix(line, "    ") {
		return true
	}

	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return false
	}

	// Block quote
	if trimmed[0] == '>' {
		return true
	}

	// List item
	if isListItemMarker(trimmed) {
		return true
	}

	// Table: any pipe in the line
	if strings.ContainsRune(line, '|') {
		return true
	}

	// Setext underline
	if isSetextUnderlineCandidate(trimmed) {
		return true
	}

	return false
}

// isListItemMarker reports whether line (already left-trimmed) starts
// with a CommonMark list-item marker followed by a space or tab.
func isListItemMarker(line string) bool {
	if line == "" {
		return false
	}
	c := line[0]
	if c == '-' || c == '*' || c == '+' {
		if len(line) >= 2 && (line[1] == ' ' || line[1] == '\t') {
			return true
		}
		return false
	}
	// Ordered list: digits followed by '.' or ')' and a space.
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i > 9 {
		return false
	}
	if i >= len(line) {
		return false
	}
	if line[i] != '.' && line[i] != ')' {
		return false
	}
	if i+1 >= len(line) {
		return false
	}
	return line[i+1] == ' ' || line[i+1] == '\t'
}

// isSetextUnderlineCandidate reports whether line consists entirely of
// '=' or '-' characters (with optional whitespace).
func isSetextUnderlineCandidate(line string) bool {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	if i == len(line) {
		return false
	}
	c := line[i]
	if c != '=' && c != '-' {
		return false
	}
	j := i
	for j < len(line) && line[j] == c {
		j++
	}
	for j < len(line) {
		if line[j] != ' ' && line[j] != '\t' {
			return false
		}
		j++
	}
	return j-i >= 1
}

// isHTMLBlockOpener reports whether line begins one of the seven CommonMark
// HTML block patterns.
func isHTMLBlockOpener(line string) bool {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	rest := line[i:]
	if len(rest) < 2 || rest[0] != '<' {
		return false
	}

	if strings.HasPrefix(rest, "<!--") {
		return true
	}
	if strings.HasPrefix(rest, "<?") {
		return true
	}
	if strings.HasPrefix(rest, "<![CDATA[") {
		return true
	}
	if len(rest) >= 3 && rest[1] == '!' && isASCIILetter(rest[2]) {
		return true
	}

	low := strings.ToLower(rest)
	for _, t := range []string{"<script", "<pre", "<style", "<textarea"} {
		if strings.HasPrefix(low, t) {
			next := byte(0)
			if len(low) > len(t) {
				next = low[len(t)]
			}
			if next == 0 || next == ' ' || next == '\t' || next == '>' {
				return true
			}
		}
	}

	j := 1
	if j < len(rest) && rest[j] == '/' {
		j++
	}
	if j >= len(rest) || !isASCIILetter(rest[j]) {
		return false
	}
	return true
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// isLinkRefDefinition reports whether line matches a CommonMark link
// reference definition opener.
func isLinkRefDefinition(line string) bool {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) || line[i] != '[' {
		return false
	}
	i++
	labelStart := i
	for i < len(line) && line[i] != ']' {
		i++
	}
	if i >= len(line) || i == labelStart {
		return false
	}
	i++
	if i >= len(line) || line[i] != ':' {
		return false
	}
	i++
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	return i < len(line)
}

var referenceStyleLinkPattern = regexp.MustCompile(`\[[^\]\n]+\][ \t]*\[[^\]\n]*\]`)

func markdownBlockSourceStart(node gast.Node, source []byte) (int, bool) {
	if node.Kind() == gast.KindFencedCodeBlock {
		return lastFencedBlockStart(string(source))
	}

	start := len(source)
	found := false
	_ = gast.Walk(node, func(current gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering {
			return gast.WalkContinue, nil
		}
		if current.Type() == gast.TypeBlock {
			lines := current.Lines()
			for i := 0; i < lines.Len(); i++ {
				segment := lines.At(i)
				if segment.Start < start {
					start = segment.Start
					found = true
				}
			}
		}
		if textNode, ok := current.(*gast.Text); ok && textNode.Segment.Start < start {
			start = textNode.Segment.Start
			found = true
		}
		return gast.WalkContinue, nil
	})
	if found {
		return sourceLineStart(source, start), true
	}
	return lastNonBlankSourceLineStart(source)
}

func sourceLineStart(source []byte, offset int) int {
	if offset <= 0 {
		return 0
	}
	if offset > len(source) {
		offset = len(source)
	}
	return strings.LastIndexByte(string(source[:offset]), '\n') + 1
}

func lastNonBlankSourceLineStart(source []byte) (int, bool) {
	last := -1
	forEachSourceLine(string(source), func(start int, line string) bool {
		if strings.TrimSpace(line) != "" {
			last = start
		}
		return true
	})
	return last, last >= 0
}

func lastFencedBlockStart(source string) (int, bool) {
	openStart := -1
	openChar := byte(0)
	openRun := 0
	lastStart := -1
	forEachSourceLine(source, func(start int, line string) bool {
		char, run, rest, ok := markdownFenceMarker(line)
		if !ok {
			return true
		}
		if openStart < 0 {
			openStart = start
			openChar = char
			openRun = run
			return true
		}
		if char == openChar && run >= openRun && strings.TrimSpace(line[rest:]) == "" {
			lastStart = openStart
			openStart = -1
			openChar = 0
			openRun = 0
		}
		return true
	})
	if openStart >= 0 {
		return openStart, true
	}
	return lastStart, lastStart >= 0
}

func markdownFenceMarker(line string) (char byte, run, rest int, ok bool) {
	line = strings.TrimSuffix(line, "\r")
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) || (line[i] != '`' && line[i] != '~') {
		return 0, 0, 0, false
	}
	char = line[i]
	for i < len(line) && line[i] == char {
		i++
		run++
	}
	return char, run, i, run >= 3
}

func hasGlobalMarkdownReferenceHazard(source string) bool {
	openChar := byte(0)
	openRun := 0
	hazard := false
	forEachSourceLine(source, func(_ int, line string) bool {
		char, run, rest, isFence := markdownFenceMarker(line)
		if isFence {
			if openChar == 0 {
				openChar = char
				openRun = run
				return true
			}
			if char == openChar && run >= openRun && strings.TrimSpace(line[rest:]) == "" {
				openChar = 0
				openRun = 0
			}
			return true
		}
		if openChar != 0 {
			return true
		}
		if isLinkRefDefinition(line) || referenceStyleLinkPattern.MatchString(line) {
			hazard = true
			return false
		}
		return true
	})
	return hazard
}

func forEachSourceLine(source string, yield func(start int, line string) bool) {
	start := 0
	for start < len(source) {
		end := strings.IndexByte(source[start:], '\n')
		if end < 0 {
			yield(start, source[start:])
			return
		}
		end += start
		if !yield(start, source[start:end]) {
			return
		}
		start = end + 1
	}
}
