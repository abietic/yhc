package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// Structured diff rendering with hunk headers, line numbers, and colored output.
// Mirrors the StructuredDiff/Fallback.tsx behavior from the reference project.

// contextLines is the number of context lines to show around changes.
const contextLines = 3

// diffLineType represents the type of a diff line.
type diffLineType int

const (
	diffLineContext diffLineType = iota
	diffLineAdd
	diffLineRemove
)

// diffHunkLine represents a single line within a diff hunk.
type diffHunkLine struct {
	Type    diffLineType
	Content string
	OldNum  int // 0 means no old line number (for additions)
	NewNum  int // 0 means no new line number (for removals)
}

// diffHunk represents a single hunk in a unified diff.
type diffHunk struct {
	OldStart int
	OldCount int
	NewStart int
	NewCount int
	Lines    []diffHunkLine
}

// computeUnifiedDiff computes a unified diff between old and new text,
// producing hunks with the specified number of context lines.
func computeUnifiedDiff(oldText, newText string, ctx int) []diffHunk {
	oldLines := splitDiffInput(oldText)
	newLines := splitDiffInput(newText)

	// Compute LCS-based edit script
	ops := computeEditScript(oldLines, newLines)

	// Group into hunks with context
	return groupIntoHunks(ops, oldLines, newLines, ctx)
}

// editOp represents a single operation in an edit script.
type editOp struct {
	kind    diffLineType // diffLineContext, diffLineAdd, diffLineRemove
	oldIdx  int          // index in old (0-based), -1 if add
	newIdx  int          // index in new (0-based), -1 if remove
	content string
}

// computeEditScript produces a sequence of edit operations using LCS.
func computeEditScript(old, new []string) []editOp {
	m, n := len(old), len(new)

	// Build LCS table
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if old[i-1] == new[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	// Backtrack to produce ops
	var ops []editOp
	i, j := m, n
	for i > 0 || j > 0 {
		if i > 0 && j > 0 && old[i-1] == new[j-1] {
			ops = append(ops, editOp{diffLineContext, i - 1, j - 1, old[i-1]})
			i--
			j--
		} else if j > 0 && (i == 0 || dp[i][j-1] >= dp[i-1][j]) {
			ops = append(ops, editOp{diffLineAdd, -1, j - 1, new[j-1]})
			j--
		} else {
			ops = append(ops, editOp{diffLineRemove, i - 1, -1, old[i-1]})
			i--
		}
	}

	// Reverse (built backwards)
	for l, r := 0, len(ops)-1; l < r; l, r = l+1, r-1 {
		ops[l], ops[r] = ops[r], ops[l]
	}
	return ops
}

// groupIntoHunks groups edit operations into hunks with context lines.
func groupIntoHunks(ops []editOp, _, _ []string, ctx int) []diffHunk {
	if len(ops) == 0 {
		return nil
	}

	// Find change ranges (indices of non-context ops)
	type changeRange struct {
		start, end int // indices into ops
	}
	var ranges []changeRange
	inChange := false
	var cur changeRange
	for i, op := range ops {
		if op.kind != diffLineContext {
			if !inChange {
				cur.start = i
				inChange = true
			}
			cur.end = i
		} else if inChange {
			ranges = append(ranges, cur)
			inChange = false
		}
	}
	if inChange {
		ranges = append(ranges, cur)
	}

	if len(ranges) == 0 {
		return nil // no changes
	}

	// Merge overlapping/adjacent ranges (within 2*ctx distance)
	type hunkSpan struct {
		start, end int // indices into ops (inclusive range for lines to include)
	}
	var spans []hunkSpan
	for _, r := range ranges {
		// Expand by context
		start := r.start - ctx
		if start < 0 {
			start = 0
		}
		end := r.end + ctx
		if end >= len(ops) {
			end = len(ops) - 1
		}

		if len(spans) > 0 && start <= spans[len(spans)-1].end+1 {
			// Merge with previous
			spans[len(spans)-1].end = end
		} else {
			spans = append(spans, hunkSpan{start, end})
		}
	}

	// Convert spans to hunks
	var hunks []diffHunk
	for _, span := range spans {
		hunk := buildHunk(ops[span.start:span.end+1], ops, span.start)
		hunks = append(hunks, hunk)
	}
	return hunks
}

// buildHunk constructs a diffHunk from a slice of edit operations.
func buildHunk(spanOps, _ []editOp, _ int) diffHunk {
	var lines []diffHunkLine
	var oldStart, newStart int
	oldCount, newCount := 0, 0
	firstOld, firstNew := true, true

	for _, op := range spanOps {
		switch op.kind {
		case diffLineContext:
			oldNum := op.oldIdx + 1
			newNum := op.newIdx + 1
			if firstOld {
				oldStart = oldNum
				firstOld = false
			}
			if firstNew {
				newStart = newNum
				firstNew = false
			}
			oldCount++
			newCount++
			lines = append(lines, diffHunkLine{
				Type:    diffLineContext,
				Content: op.content,
				OldNum:  oldNum,
				NewNum:  newNum,
			})
		case diffLineRemove:
			oldNum := op.oldIdx + 1
			if firstOld {
				oldStart = oldNum
				firstOld = false
			}
			if firstNew {
				// For the new start, use the next new line that appears
				newStart = findNextNewStart(spanOps, op)
			}
			oldCount++
			lines = append(lines, diffHunkLine{
				Type:    diffLineRemove,
				Content: op.content,
				OldNum:  oldNum,
				NewNum:  0,
			})
		case diffLineAdd:
			newNum := op.newIdx + 1
			if firstNew {
				newStart = newNum
				firstNew = false
			}
			if firstOld {
				// For the old start, use the next old line that appears
				oldStart = findNextOldStart(spanOps, op)
			}
			newCount++
			lines = append(lines, diffHunkLine{
				Type:    diffLineAdd,
				Content: op.content,
				OldNum:  0,
				NewNum:  newNum,
			})
		}
	}

	if oldStart == 0 {
		oldStart = 1
	}
	if newStart == 0 {
		newStart = 1
	}

	return diffHunk{
		OldStart: oldStart,
		OldCount: oldCount,
		NewStart: newStart,
		NewCount: newCount,
		Lines:    lines,
	}
}

func findNextNewStart(ops []editOp, _ editOp) int {
	for _, op := range ops {
		if op.newIdx >= 0 {
			return op.newIdx + 1
		}
	}
	return 1
}

func findNextOldStart(ops []editOp, _ editOp) int {
	for _, op := range ops {
		if op.oldIdx >= 0 {
			return op.oldIdx + 1
		}
	}
	return 1
}

// splitDiffInput splits text into lines for diffing.
// Handles empty string as zero lines.
func splitDiffInput(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// renderStructuredDiff renders a complete structured diff output with file header,
// hunk headers, line numbers, and colored content.
func renderStructuredDiff(s Styles, filePath, oldText, newText string, width int) string {
	return renderStructuredDiffWithProfile(
		DefaultDisplayCellProfile(),
		s,
		filePath,
		oldText,
		newText,
		width,
	)
}

func renderStructuredDiffWithProfile(
	profile DisplayCellProfile,
	s Styles,
	filePath, oldText, newText string,
	width int,
) string {
	hunks := computeUnifiedDiff(oldText, newText, contextLines)
	if len(hunks) == 0 {
		return ""
	}

	bodyWidth := width - 6 // Account for "  ⎿  " prefix
	if bodyWidth < 20 {
		bodyWidth = 20
	}

	var out []string

	// File path header
	if filePath != "" {
		header := s.Subtle.Render("  ⎿  ") + s.Bold.Render(shortenPath(filePath))
		out = append(out, header)
	}

	// Find max line number for gutter width across all hunks
	maxLineNum := 0
	for _, hunk := range hunks {
		endOld := hunk.OldStart + hunk.OldCount - 1
		endNew := hunk.NewStart + hunk.NewCount - 1
		if endOld > maxLineNum {
			maxLineNum = endOld
		}
		if endNew > maxLineNum {
			maxLineNum = endNew
		}
	}
	gutterDigits := len(fmt.Sprintf("%d", maxLineNum))
	if gutterDigits < 1 {
		gutterDigits = 1
	}

	// Gutter width: oldNum + separator + newNum + space + sigil + space
	// e.g., " 10 | 12  + "
	gutterWidth := gutterDigits + 1 + gutterDigits + 3 // "old sep new  sigil "

	indent := "     " // 5 spaces to match gutter after first line

	for hi, hunk := range hunks {
		// Hunk header: @@ -oldStart,oldCount +newStart,newCount @@
		hunkHeader := fmt.Sprintf("@@ -%d,%d +%d,%d @@",
			hunk.OldStart, hunk.OldCount, hunk.NewStart, hunk.NewCount)

		prefix := indent
		if hi == 0 && filePath == "" {
			prefix = s.Subtle.Render("  ⎿  ")
		} else if hi == 0 && filePath != "" {
			prefix = indent
		}
		out = append(out, prefix+s.Subtle.Render(hunkHeader))

		// Render each line
		contentWidth := bodyWidth - gutterWidth
		if contentWidth < 10 {
			contentWidth = 10
		}

		for _, line := range hunk.Lines {
			gutter := formatDiffGutter(line, gutterDigits)
			content := contentProjectLine(
				profile,
				line.Content,
				contentWidth,
				len(indent)+gutterWidth,
			)

			var rendered string
			switch line.Type {
			case diffLineRemove:
				// Full line with red background
				gutterStyled := s.DiffRemoved.Render(gutter)
				contentStyled := s.DiffRemoved.Render(content)
				rendered = gutterStyled + contentStyled
			case diffLineAdd:
				// Full line with green background
				gutterStyled := s.DiffAdded.Render(gutter)
				contentStyled := s.DiffAdded.Render(content)
				rendered = gutterStyled + contentStyled
			case diffLineContext:
				// Dim context line
				gutterStyled := s.Subtle.Render(gutter)
				contentStyled := s.Dim.Render(content)
				rendered = gutterStyled + contentStyled
			}

			out = append(out, indent+rendered)
		}
	}

	return strings.Join(out, "\n")
}

// formatDiffGutter formats the line number gutter for a diff line.
// Format: "oldNum|newNum sigil " where sigil is +/-/space.
func formatDiffGutter(line diffHunkLine, digits int) string {
	var oldStr, newStr string

	if line.OldNum > 0 {
		oldStr = fmt.Sprintf("%*d", digits, line.OldNum)
	} else {
		oldStr = strings.Repeat(" ", digits)
	}

	if line.NewNum > 0 {
		newStr = fmt.Sprintf("%*d", digits, line.NewNum)
	} else {
		newStr = strings.Repeat(" ", digits)
	}

	var sigil string
	switch line.Type {
	case diffLineAdd:
		sigil = "+"
	case diffLineRemove:
		sigil = "-"
	default:
		sigil = " "
	}

	return fmt.Sprintf("%s%s%s %s ", oldStr, lipgloss.NewStyle().Faint(true).Render("|"), newStr, sigil)
}

// renderStructuredEditDiff renders a structured diff for the Edit tool,
// extracting file_path, old_string, and new_string from the tool input JSON.
// Returns empty string if the input cannot be parsed or there are no changes.
func renderStructuredEditDiff(styles Styles, input string, width int) string {
	var params struct {
		FilePath  string `json:"file_path"`
		OldString string `json:"old_string"`
		NewString string `json:"new_string"`
	}

	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return ""
	}
	if params.OldString == "" && params.NewString == "" {
		return ""
	}

	return renderStructuredDiff(styles, params.FilePath, params.OldString, params.NewString, width)
}

// renderStructuredDiffBounded keeps the first and last semantic diff rows in
// the normal viewport. Expanded and transcript projections pass maxRows <= 0
// and retain the complete stored diff.
func renderStructuredDiffBounded(styles Styles, filePath, oldText, newText string, width, maxRows int) string {
	return renderStructuredDiffBoundedWithProfile(
		DefaultDisplayCellProfile(),
		styles,
		filePath,
		oldText,
		newText,
		width,
		maxRows,
	)
}

func renderStructuredDiffBoundedWithProfile(
	profile DisplayCellProfile,
	styles Styles,
	filePath, oldText, newText string,
	width, maxRows int,
) string {
	rendered := renderStructuredDiffWithProfile(profile, styles, filePath, oldText, newText, width)
	if rendered == "" || maxRows <= 0 {
		return rendered
	}
	lines := strings.Split(rendered, "\n")
	if len(lines) <= maxRows || maxRows < 3 {
		return rendered
	}
	head := (maxRows - 1) / 2
	tail := maxRows - head - 1
	bounded := make([]string, 0, maxRows)
	bounded = append(bounded, lines[:head]...)
	bounded = append(bounded, "     "+styles.Subtle.Render(fmt.Sprintf("… +%d diff lines (expand for details)", len(lines)-head-tail)))
	bounded = append(bounded, lines[len(lines)-tail:]...)
	return strings.Join(bounded, "\n")
}
