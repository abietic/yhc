package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/internal/tui/attachments"
)

// Exact indexed filenames win over range syntax, preserving literal '#' names.
func (a *App) fileMentionQuery(query string) (string, attachments.LineRange, error) {
	for _, path := range a.mentionIndex.files {
		if strings.EqualFold(query, path) {
			return query, attachments.LineRange{}, nil
		}
	}
	index := strings.LastIndexByte(query, '#')
	if index < 0 {
		return query, attachments.LineRange{}, nil
	}
	lines, err := attachments.ParseLineRange(query[index:])
	if err != nil {
		// A partially typed literal filename containing '#' still completes.
		for _, path := range a.mentionIndex.files {
			if mentionMatches(query, path) {
				return query, attachments.LineRange{}, nil
			}
		}
		return "", attachments.LineRange{}, err
	}
	if index == 0 {
		return "", attachments.LineRange{}, fmt.Errorf("file range needs a path")
	}
	return query[:index], lines, nil
}

func (a *App) renderFileMentionRangeError() string {
	if a.focus != FocusEditor || a.inputMode != InputNormal {
		return ""
	}
	_, _, query, active := a.activeMentionToken()
	if !active || strings.HasPrefix(query, "skill:") || strings.HasPrefix(query, "mcp:") {
		return ""
	}
	query = strings.TrimPrefix(strings.ToLower(query), "file:")
	if _, _, err := a.fileMentionQuery(query); err == nil {
		return ""
	}
	profile := a.renderEnvironment.normalized().profile
	return strings.Join(profile.wrap("Invalid line range: use #N or #N-M (positive, inclusive)", a.hintContentWidth(), true), "\n")
}

func loadFileRangeMention(elementID, path string, lines attachments.LineRange) tea.Cmd {
	return func() tea.Msg {
		excerpt, err := attachments.ReadFileRange(path, lines)
		return composerMentionPayloadMsg{
			elementID: elementID, data: excerpt.Content, mimeType: "text/plain",
			totalLines: excerpt.TotalLines, err: err,
		}
	}
}
