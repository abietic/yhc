package tui

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	enginemcp "github.com/abietic/yhc/engine/mcp"
	"github.com/abietic/yhc/internal/tui/attachments"
)

const (
	composerElementKindFile        = "file"
	composerElementKindSkill       = "skill"
	composerElementKindMCPResource = "mcp_resource"

	maxMentionIndexFiles = 5000
	maxMentionHints      = 50
)

type composerMentionHint struct {
	Kind        string
	Label       string
	Name        string
	Value       string
	Source      string
	MIMEType    string
	Data        string
	Description string
}

type (
	mcpResourceListFunc func(context.Context) ([]enginemcp.MCPResource, error)
	mcpResourceReadFunc func(context.Context, string, string) ([]enginemcp.ResourceContent, error)
)

type composerMentionIndex struct {
	loaded    bool
	loading   bool
	files     []string
	resources []enginemcp.MCPResource
	listMCP   mcpResourceListFunc
	readMCP   mcpResourceReadFunc
}

type composerMentionIndexLoadedMsg struct {
	files     []string
	resources []enginemcp.MCPResource
	err       error
}

type composerMentionPayloadMsg struct {
	elementID string
	data      string
	mimeType  string
	err       error
}

func (a *App) updateMentionHints() {
	_, _, query, active := a.activeMentionToken()
	if !active || a.inputMode != InputNormal {
		a.dismissMentionHints()
		return
	}

	query = strings.ToLower(query)
	sourceFilter := ""
	for _, prefix := range []string{"skill:", "mcp:", "file:"} {
		if strings.HasPrefix(query, prefix) {
			sourceFilter = strings.TrimSuffix(prefix, ":")
			query = strings.TrimPrefix(query, prefix)
			break
		}
	}

	hints := make([]composerMentionHint, 0, maxMentionHints)
	if sourceFilter == "" || sourceFilter == "file" {
		for _, path := range a.mentionIndex.files {
			label := "@" + filepath.ToSlash(path)
			if mentionMatches(query, label, path) {
				hints = append(hints, composerMentionHint{
					Kind: composerElementKindFile, Label: label, Name: filepath.Base(path),
					Value: filepath.ToSlash(path), Source: a.absoluteMentionPath(path),
					MIMEType: "text/plain", Description: "file",
				})
			}
		}
	}
	if (sourceFilter == "" || sourceFilter == "skill") && a.engine != nil && a.engine.GetSkillRegistry() != nil {
		for _, skill := range a.engine.GetSkillRegistry().List() {
			label := "@skill:" + skill.Name
			if mentionMatches(query, label, skill.Name, skill.Description) {
				hints = append(hints, composerMentionHint{
					Kind: composerElementKindSkill, Label: label, Name: skill.Name,
					Value: skill.FilePath, Source: skill.FilePath, MIMEType: "text/markdown",
					Data: skill.Content, Description: skill.Description,
				})
			}
		}
	}
	if sourceFilter == "" || sourceFilter == "mcp" {
		for _, resource := range a.mentionIndex.resources {
			name := resource.Name
			if name == "" {
				name = resource.URI
			}
			label := "@mcp:" + resource.ServerName + "/" + name
			if mentionMatches(query, label, name, resource.URI, resource.Description) {
				hints = append(hints, composerMentionHint{
					Kind: composerElementKindMCPResource, Label: label,
					Name: resource.ServerName + "/" + name, Value: resource.URI,
					Source: resource.ServerName, MIMEType: resource.MimeType,
					Description: resource.Description,
				})
			}
		}
	}

	sort.SliceStable(hints, func(i, j int) bool {
		left := mentionScore(query, hints[i])
		right := mentionScore(query, hints[j])
		if left != right {
			return left < right
		}
		return hints[i].Label < hints[j].Label
	})
	if len(hints) > maxMentionHints {
		hints = hints[:maxMentionHints]
	}
	a.mentionHints = hints
	if len(hints) == 0 {
		a.mentionHintIdx = -1
	} else if a.mentionHintIdx < 0 || a.mentionHintIdx >= len(hints) {
		a.mentionHintIdx = 0
	}
}

func mentionMatches(query string, values ...string) bool {
	if query == "" {
		return true
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func mentionScore(query string, hint composerMentionHint) int {
	label := strings.ToLower(strings.TrimPrefix(hint.Label, "@"))
	switch {
	case query == "":
		return mentionKindOrder(hint.Kind)
	case strings.HasPrefix(label, query):
		return mentionKindOrder(hint.Kind)
	case strings.Contains(label, query):
		return 10 + mentionKindOrder(hint.Kind)
	default:
		return 20 + mentionKindOrder(hint.Kind)
	}
}

func mentionKindOrder(kind string) int {
	switch kind {
	case composerElementKindFile:
		return 0
	case composerElementKindSkill:
		return 1
	default:
		return 2
	}
}

func (a *App) activeMentionToken() (start, end int, query string, ok bool) {
	value := a.textarea.Value()
	lineInfo := a.textarea.LineInfo()
	cursor := textareaCursorRuneOffset(value, a.textarea.Line(), lineInfo.StartColumn+lineInfo.ColumnOffset)
	runes := []rune(value)
	start = cursor
	for start > 0 && !mentionDelimiter(runes[start-1]) {
		start--
	}
	if start >= cursor || runes[start] != '@' {
		return 0, 0, "", false
	}
	for _, element := range a.composerElements {
		if start < element.End && cursor > element.Start {
			return 0, 0, "", false
		}
	}
	return start, cursor, string(runes[start+1 : cursor]), true
}

func mentionDelimiter(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune("()[]{}<>,;\"'`", r)
}

func (a *App) ensureMentionIndex() tea.Cmd {
	if _, _, _, active := a.activeMentionToken(); !active || a.mentionIndex.loaded || a.mentionIndex.loading {
		return nil
	}
	a.mentionIndex.loading = true
	cwd := a.mentionCWD()
	listMCP := a.mentionIndex.listMCP
	return func() tea.Msg {
		files, fileErr := buildMentionFileIndex(cwd)
		var resources []enginemcp.MCPResource
		var mcpErr error
		if listMCP != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			resources, mcpErr = listMCP(ctx)
		}
		if fileErr != nil {
			return composerMentionIndexLoadedMsg{files: files, resources: resources, err: fileErr}
		}
		return composerMentionIndexLoadedMsg{files: files, resources: resources, err: mcpErr}
	}
}

func (a *App) handleMentionIndexLoaded(msg composerMentionIndexLoadedMsg) {
	a.mentionIndex.loading = false
	a.mentionIndex.loaded = true
	a.mentionIndex.files = append([]string(nil), msg.files...)
	a.mentionIndex.resources = append([]enginemcp.MCPResource(nil), msg.resources...)
	a.updateMentionHints()
	if msg.err != nil {
		a.showNotification("Some mention sources are unavailable: "+msg.err.Error(), NotifyWarning)
	}
}

func buildMentionFileIndex(root string) ([]string, error) {
	files := make([]string, 0, 256)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if path != root && skipMentionDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= maxMentionIndexFiles {
			return fs.SkipAll
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr == nil {
			files = append(files, filepath.ToSlash(relative))
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func skipMentionDirectory(name string) bool {
	switch name {
	case ".git", ".reference", "node_modules", "vendor", "dist", "build":
		return true
	default:
		return false
	}
}

func (a *App) mentionCWD() string {
	if a.engine != nil && a.engine.GetCWD() != "" {
		return a.engine.GetCWD()
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

func (a *App) absoluteMentionPath(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(a.mentionCWD(), filepath.FromSlash(path))
}

func (a *App) dismissMentionHints() {
	a.mentionHints = nil
	a.mentionHintIdx = -1
}

func (a *App) acceptMentionHint() tea.Cmd {
	if a.mentionHintIdx < 0 || a.mentionHintIdx >= len(a.mentionHints) {
		return nil
	}
	start, end, _, active := a.activeMentionToken()
	if !active {
		a.dismissMentionHints()
		return nil
	}
	hint := a.mentionHints[a.mentionHintIdx]
	if len(a.composerElements) >= maxThreadComposerElements {
		a.showNotification("Composer attachment limit reached (32)", NotifyError)
		return nil
	}
	if hint.Data != "" && a.composerPayloadBytes()+len(hint.Data) > maxComposerPayloadBytes {
		a.showNotification("Composer attachments exceed the 10 MiB draft limit", NotifyError)
		return nil
	}

	before := a.captureComposerUndoEntry()
	runes := []rune(before.Text)
	replacement := []rune(hint.Label + " ")
	updated := string(append(append(append([]rune(nil), runes[:start]...), replacement...), runes[end:]...))
	a.textarea.SetValue(updated)
	a.reconcileComposerElements(before.Text, updated)
	a.setTextareaRuneCursor(start + len(replacement))
	a.nextComposerElementID++
	elementID := fmt.Sprintf("mention-%d", a.nextComposerElementID)
	a.composerElements = append(a.composerElements, threadComposerElement{
		ID: elementID, Kind: hint.Kind, Label: hint.Label, Name: hint.Name,
		Value: hint.Value, MIMEType: hint.MIMEType, Data: hint.Data,
		Start: start, End: start + utf8.RuneCountInString(hint.Label),
	})
	a.markComposerChanged()
	a.recordComposerUndo(before)
	a.dismissMentionHints()
	a.syncInputModeFromText()

	switch hint.Kind {
	case composerElementKindFile:
		a.showToast("Loading file mention: " + hint.Value)
		return loadFileMention(elementID, hint.Source)
	case composerElementKindMCPResource:
		a.showToast("Loading MCP resource: " + hint.Name)
		return a.loadMCPResourceMention(elementID, hint.Source, hint.Value)
	default:
		return nil
	}
}

func loadFileMention(elementID, path string) tea.Cmd {
	return func() tea.Msg {
		attachment, err := attachments.ResolveAttachment(path)
		if err != nil {
			return composerMentionPayloadMsg{elementID: elementID, err: err}
		}
		if attachment.Type != "file" || strings.IndexByte(attachment.Content, 0) >= 0 {
			return composerMentionPayloadMsg{elementID: elementID, err: fmt.Errorf("file mention is not text: %s", path)}
		}
		return composerMentionPayloadMsg{
			elementID: elementID, data: attachment.Content, mimeType: attachment.MimeType,
		}
	}
}

func (a *App) loadMCPResourceMention(elementID, server, uri string) tea.Cmd {
	readMCP := a.mentionIndex.readMCP
	return func() tea.Msg {
		if readMCP == nil {
			return composerMentionPayloadMsg{elementID: elementID, err: fmt.Errorf("MCP resource reader is unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		contents, err := readMCP(ctx, server, uri)
		if err != nil {
			return composerMentionPayloadMsg{elementID: elementID, err: err}
		}
		data, mimeType := flattenMCPResource(contents)
		if data == "" {
			return composerMentionPayloadMsg{elementID: elementID, err: fmt.Errorf("MCP resource is empty: %s", uri)}
		}
		return composerMentionPayloadMsg{elementID: elementID, data: data, mimeType: mimeType}
	}
}

func flattenMCPResource(contents []enginemcp.ResourceContent) (string, string) {
	var parts []string
	mimeType := ""
	for _, content := range contents {
		if mimeType == "" {
			mimeType = content.MimeType
		}
		switch {
		case content.Text != "":
			parts = append(parts, content.Text)
		case content.Blob != "":
			parts = append(parts, "[base64 resource content]\n"+content.Blob)
		}
	}
	if mimeType == "" {
		mimeType = "text/plain"
	}
	return strings.Join(parts, "\n\n"), mimeType
}

func (a *App) handleMentionPayload(msg composerMentionPayloadMsg) {
	index := -1
	for i := range a.composerElements {
		if a.composerElements[i].ID == msg.elementID {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	if msg.err != nil {
		a.composerElements = append(a.composerElements[:index], a.composerElements[index+1:]...)
		a.markComposerChanged()
		a.showNotification("Mention could not be attached: "+msg.err.Error(), NotifyError)
		return
	}
	if len(msg.data) > attachments.MaxAttachmentBytes || a.composerPayloadBytes()+len(msg.data) > maxComposerPayloadBytes {
		a.composerElements = append(a.composerElements[:index], a.composerElements[index+1:]...)
		a.markComposerChanged()
		a.showNotification("Mention content exceeds the composer attachment limit", NotifyError)
		return
	}
	a.composerElements[index].Data = msg.data
	if msg.mimeType != "" {
		a.composerElements[index].MIMEType = msg.mimeType
	}
	a.markComposerChanged()
	a.showToast("Attached mention: " + a.composerElements[index].Name)
}

func (a *App) setTextareaRuneCursor(position int) {
	setTextareaRuneCursor(&a.textarea, position)
}

func (a *App) renderMentionHints() string {
	if len(a.mentionHints) == 0 || a.focus != FocusEditor {
		return ""
	}
	const maxVisible = 10
	start := max(0, a.mentionHintIdx-maxVisible/2)
	end := min(len(a.mentionHints), start+maxVisible)
	start = max(0, end-maxVisible)
	profile := a.renderEnvironment.normalized().profile
	width := a.hintContentWidth()
	var lines []string
	for i := start; i < end; i++ {
		hint := a.mentionHints[i]
		line := fmt.Sprintf(" %-8s %s", mentionKindLabel(hint.Kind), hint.Label)
		if hint.Description != "" {
			line += "  " + hint.Description
		}
		line = contentEllipsize(profile, line, width, 2, "...")
		if i == a.mentionHintIdx {
			line = a.styles.Selected.Render(line)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func mentionKindLabel(kind string) string {
	switch kind {
	case composerElementKindSkill:
		return "skill"
	case composerElementKindMCPResource:
		return "mcp"
	default:
		return "file"
	}
}
