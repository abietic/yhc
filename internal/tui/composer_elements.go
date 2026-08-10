package tui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/internal/tui/attachments"
)

const (
	composerElementKindPaste = "paste"
	composerElementKindImage = "image"

	maxComposerPayloadBytes      = 2 * attachments.MaxAttachmentBytes
	maxRichComposerHistory       = 32
	largePastePlaceholderPattern = "[Pasted Content %d chars]"
)

type composerImageLoadedMsg struct {
	requestID uint64
	threadID  string
	revision  uint64
	anchor    int
	name      string
	mimeType  string
	data      []byte
	fallback  string
	found     bool
	err       error
}

type composerDraftImage struct {
	MIMEType string
	Data     []byte
	Detail   engine.PromptImageDetail
}

type composerImageLoadRequest struct {
	ID       uint64
	ThreadID string
	Revision uint64
	Anchor   int
	Fallback string
}

func (a *App) handleComposerPaste(msg tea.PasteMsg) tea.Cmd {
	if a.composerInputBlocked() {
		return nil
	}
	pasted := strings.ReplaceAll(msg.Content, "\r\n", "\n")
	pasted = strings.ReplaceAll(pasted, "\r", "\n")
	if path, ok := attachments.NormalizePastedImagePath(pasted); ok {
		return a.loadComposerImagePath(path, pasted)
	}
	if !attachments.IsLargePaste(pasted) {
		a.insertPastedComposerText(pasted)
		return nil
	}

	if len(pasted) > attachments.MaxAttachmentBytes {
		a.showNotification("Paste is too large (maximum 5 MiB)", NotifyError)
		return nil
	}
	if a.composerPayloadBytes()+len(pasted) > maxComposerPayloadBytes {
		a.showNotification("Composer attachments exceed the 10 MiB draft limit", NotifyError)
		return nil
	}
	if len(a.composerElements) >= maxThreadComposerElements {
		a.showNotification("Composer attachment limit reached (32)", NotifyError)
		return nil
	}

	before := a.captureComposerUndoEntry()
	charCount := utf8.RuneCountInString(pasted)
	placeholder := a.nextLargePastePlaceholder(charCount)
	start := textareaCursorRuneOffset(a.textarea.Value(), a.textarea.Line(), a.textarea.LineInfo().StartColumn+a.textarea.LineInfo().ColumnOffset)
	a.textarea.InsertString(placeholder)
	a.nextComposerElementID++
	a.composerElements = append(a.composerElements, threadComposerElement{
		ID:    fmt.Sprintf("paste-%d", a.nextComposerElementID),
		Kind:  composerElementKindPaste,
		Label: placeholder,
		Value: pasted,
		Start: start,
		End:   start + utf8.RuneCountInString(placeholder),
	})
	a.markComposerChanged()
	a.recordComposerUndo(before)
	a.syncInputModeFromText()
	a.updateLayout()
	a.showToast(fmt.Sprintf("Pasted %d lines (%d chars)", strings.Count(pasted, "\n")+1, charCount))
	return nil
}

func (a *App) insertPastedComposerText(pasted string) {
	before := a.captureComposerUndoEntry()
	a.textarea.InsertString(pasted)
	a.reconcileComposerElements(before.Text, a.textarea.Value())
	a.markComposerChanged()
	a.recordComposerUndo(before)
	a.syncInputModeFromText()
}

func (a *App) loadComposerImagePath(path, fallback string) tea.Cmd {
	request, ok := a.beginComposerImageLoad(fallback)
	if !ok {
		return nil
	}
	return func() tea.Msg {
		attachment, err := attachments.ResolveAttachment(path)
		if err != nil || attachment.Type != "image" {
			return composerImageLoadedMsg{
				requestID: request.ID, threadID: request.ThreadID, revision: request.Revision,
				anchor: request.Anchor, fallback: request.Fallback, err: err,
			}
		}
		data, decodeErr := base64.StdEncoding.DecodeString(attachment.Content)
		return composerImageLoadedMsg{
			requestID: request.ID, threadID: request.ThreadID, revision: request.Revision,
			anchor: request.Anchor, name: attachment.Name, mimeType: attachment.MimeType,
			data: data, fallback: request.Fallback, found: decodeErr == nil, err: decodeErr,
		}
	}
}

func (a *App) pasteClipboardImage() tea.Cmd {
	request, ok := a.beginComposerImageLoad("")
	if !ok {
		return nil
	}
	return func() tea.Msg {
		reader := a.clipboardImageReader
		if reader == nil {
			reader = attachments.ReadClipboardImage
		}
		result := reader(context.Background())
		if result.Error != nil {
			clearBytes(result.Data)
			return composerImageLoadedMsg{
				requestID: request.ID, threadID: request.ThreadID, revision: request.Revision,
				anchor: request.Anchor, err: result.Error,
			}
		}
		if !result.HasImage || len(result.Data) == 0 {
			clearBytes(result.Data)
			return composerImageLoadedMsg{
				requestID: request.ID, threadID: request.ThreadID, revision: request.Revision,
				anchor: request.Anchor,
			}
		}
		if len(result.Data) > attachments.MaxAttachmentBytes {
			clearBytes(result.Data)
			return composerImageLoadedMsg{
				requestID: request.ID, threadID: request.ThreadID, revision: request.Revision,
				anchor: request.Anchor, err: fmt.Errorf("clipboard image is too large (maximum 5 MiB)"),
			}
		}
		format := strings.ToLower(result.Format)
		if format == "jpg" {
			format = "jpeg"
		}
		switch format {
		case "png", "jpeg", "gif", "webp":
		default:
			clearBytes(result.Data)
			return composerImageLoadedMsg{
				requestID: request.ID, threadID: request.ThreadID, revision: request.Revision,
				anchor: request.Anchor, err: fmt.Errorf("unsupported clipboard image format %q", format),
			}
		}
		return composerImageLoadedMsg{
			requestID: request.ID, threadID: request.ThreadID, revision: request.Revision,
			anchor: request.Anchor, name: fmt.Sprintf("clipboard.%s", format),
			mimeType: "image/" + format, data: result.Data, found: true,
		}
	}
}

func (a *App) handleComposerImageLoaded(msg composerImageLoadedMsg) {
	request := a.composerImageLoadPending
	if request == nil || request.ID != msg.requestID {
		clearBytes(msg.data)
		return
	}
	a.composerImageLoadPending = nil
	if msg.threadID != a.activeThreadViewID() ||
		msg.threadID != request.ThreadID ||
		msg.revision != a.composerRevision ||
		msg.revision != request.Revision ||
		msg.anchor != request.Anchor {
		clearBytes(msg.data)
		return
	}
	if !msg.found {
		clearBytes(msg.data)
		if msg.fallback != "" {
			setTextareaRuneCursor(&a.textarea, msg.anchor)
			a.insertPastedComposerText(msg.fallback)
			return
		}
		if msg.err != nil {
			a.showNotification("Unable to paste image from clipboard", NotifyError)
		} else {
			a.showNotification("Clipboard does not contain an image", NotifyWarning)
		}
		return
	}
	if msg.err != nil {
		clearBytes(msg.data)
		a.showNotification("Unable to paste image from clipboard", NotifyError)
		return
	}
	if err := a.addComposerImageAt(msg.name, msg.mimeType, msg.data, msg.anchor); err != nil {
		clearBytes(msg.data)
		a.showNotification(err.Error(), NotifyError)
		return
	}
	a.showToast("Attached image: " + msg.name)
}

func (a *App) beginComposerImageLoad(fallback string) (composerImageLoadRequest, bool) {
	if a == nil || a.composerImageLoadPending != nil || a.composerAdmissionPending != nil {
		if a != nil {
			a.showNotification("Wait for the current composer operation to finish", NotifyWarning)
		}
		return composerImageLoadRequest{}, false
	}
	if !a.isLeaderThreadView() {
		a.showNotification("Image input is available only in the leader thread", NotifyWarning)
		return composerImageLoadRequest{}, false
	}
	lineInfo := a.textarea.LineInfo()
	a.composerImageLoadSerial++
	if a.composerImageLoadSerial == 0 {
		a.composerImageLoadSerial++
	}
	request := composerImageLoadRequest{
		ID:       a.composerImageLoadSerial,
		ThreadID: a.activeThreadViewID(),
		Revision: a.composerRevision,
		Anchor: textareaCursorRuneOffset(
			a.textarea.Value(), a.textarea.Line(), lineInfo.StartColumn+lineInfo.ColumnOffset,
		),
		Fallback: fallback,
	}
	a.composerImageLoadPending = &request
	return request, true
}

func (a *App) addComposerImage(name, _, mimeType, data string) error {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return fmt.Errorf("decode image attachment: %w", err)
	}
	lineInfo := a.textarea.LineInfo()
	anchor := textareaCursorRuneOffset(
		a.textarea.Value(), a.textarea.Line(), lineInfo.StartColumn+lineInfo.ColumnOffset,
	)
	if err := a.addComposerImageAt(name, mimeType, raw, anchor); err != nil {
		clearBytes(raw)
		return err
	}
	return nil
}

func (a *App) addComposerImageAt(name, mimeType string, data []byte, anchor int) error {
	if len(data) == 0 || mimeType == "" {
		return fmt.Errorf("image attachment has no content")
	}
	if name == "" {
		name = "image"
	}
	if len(a.composerElements) >= maxThreadComposerElements {
		return fmt.Errorf("composer attachment limit reached (32)")
	}
	if len(data) > attachments.MaxAttachmentBytes {
		return fmt.Errorf("image is too large (maximum 5 MiB)")
	}
	if a.composerPayloadBytes()+len(data) > maxComposerPayloadBytes {
		return fmt.Errorf("composer attachments exceed the 10 MiB draft limit")
	}
	placeholder := a.nextImagePlaceholder()
	before := a.captureComposerUndoEntry()
	setTextareaRuneCursor(&a.textarea, anchor)
	start := anchor
	a.textarea.InsertString(placeholder)
	a.nextComposerElementID++
	id := fmt.Sprintf("image-%d", a.nextComposerElementID)
	if a.draftMedia == nil {
		a.draftMedia = make(map[string]*composerDraftImage)
	}
	a.draftMedia[id] = &composerDraftImage{
		MIMEType: mimeType,
		Data:     data,
		Detail:   engine.PromptImageDetailAuto,
	}
	a.composerElements = append(a.composerElements, threadComposerElement{
		ID: id, Kind: composerElementKindImage,
		Label: placeholder, Name: name, MIMEType: mimeType,
		Start: start, End: start + utf8.RuneCountInString(placeholder),
	})
	a.markComposerChanged()
	a.recordComposerUndo(before)
	a.syncInputModeFromText()
	a.updateLayout()
	return nil
}

func (a *App) nextImagePlaceholder() string {
	used := make(map[string]struct{}, len(a.composerElements))
	for _, element := range a.composerElements {
		if element.Kind == composerElementKindImage {
			used[element.Label] = struct{}{}
		}
	}
	for number := 1; ; number++ {
		candidate := fmt.Sprintf("[Image #%d]", number)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

func textareaCursorRuneOffset(value string, line, column int) int {
	lines := strings.Split(value, "\n")
	if line < 0 {
		line = 0
	}
	if line >= len(lines) {
		line = len(lines) - 1
	}
	offset := 0
	for i := 0; i < line; i++ {
		offset += utf8.RuneCountInString(lines[i]) + 1
	}
	lineRunes := utf8.RuneCountInString(lines[line])
	if column < 0 {
		column = 0
	}
	if column > lineRunes {
		column = lineRunes
	}
	return offset + column
}

func (a *App) nextLargePastePlaceholder(charCount int) string {
	base := fmt.Sprintf(largePastePlaceholderPattern, charCount)
	used := make(map[string]struct{}, len(a.composerElements))
	for _, element := range a.composerElements {
		if element.Kind == composerElementKindPaste {
			used[element.Label] = struct{}{}
		}
	}
	if _, exists := used[base]; !exists {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("[Pasted Content %d chars #%d]", charCount, suffix)
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

func (a *App) composerPayloadBytes() int {
	total := 0
	for _, image := range a.draftMedia {
		if image != nil {
			total += len(image.Data)
		}
	}
	for _, element := range a.composerElements {
		switch element.Kind {
		case composerElementKindImage:
		case composerElementKindPaste:
			total += len(element.Value)
		default:
			total += len(element.Data)
		}
	}
	return total
}

func (a *App) reconcileComposerElements(before, after string) {
	if len(a.composerElements) == 0 || before == after {
		return
	}
	oldRunes := []rune(before)
	newRunes := []rune(after)
	prefix := commonRunePrefix(oldRunes, newRunes)
	suffix := commonRuneSuffix(oldRunes[prefix:], newRunes[prefix:])
	oldEditEnd := len(oldRunes) - suffix
	newEditEnd := len(newRunes) - suffix
	delta := newEditEnd - oldEditEnd

	rebased := make([]threadComposerElement, 0, len(a.composerElements))
	for _, element := range a.composerElements {
		if element.Start < 0 || element.End <= element.Start || element.End > len(oldRunes) {
			continue
		}
		switch {
		case element.End <= prefix:
		case element.Start >= oldEditEnd:
			element.Start += delta
			element.End += delta
		default:
			continue
		}
		if composerElementMatches(newRunes, element) {
			rebased = append(rebased, element)
		}
	}
	a.composerElements = rebased
}

func commonRunePrefix(left, right []rune) int {
	limit := min(len(left), len(right))
	for i := 0; i < limit; i++ {
		if left[i] != right[i] {
			return i
		}
	}
	return limit
}

func commonRuneSuffix(left, right []rune) int {
	limit := min(len(left), len(right))
	for i := 0; i < limit; i++ {
		if left[len(left)-1-i] != right[len(right)-1-i] {
			return i
		}
	}
	return limit
}

func composerElementMatches(text []rune, element threadComposerElement) bool {
	return element.Start >= 0 && element.End > element.Start && element.End <= len(text) &&
		string(text[element.Start:element.End]) == element.Label
}

func (a *App) composerSubmissionTexts() (display, expanded string) {
	raw := a.textarea.Value()
	return strings.TrimSpace(raw), strings.TrimSpace(expandComposerElements(raw, a.composerElements))
}

func (a *App) composerDisplayElements() []threadComposerElement {
	_, elements := trimComposerEntry(a.textarea.Value(), a.composerElements)
	return sanitizedComposerElements(elements)
}

func (a *App) composerSubmissionPrompt() (display, prompt string, err error) {
	snapshot, snapshotErr := a.captureComposerSubmission()
	if snapshotErr != nil {
		return "", "", snapshotErr
	}
	return snapshot.Display, snapshot.Text, nil
}

type composerSubmissionSnapshot struct {
	Display    string
	Text       string
	SafeText   string
	Input      engine.UntrustedPromptInput
	Elements   []threadComposerElement
	HasImages  bool
	HasContext bool
}

type composerOrderedPart struct {
	kind  engine.QueuedPromptPartKind
	text  string
	image *composerDraftImage
}

func (a *App) captureComposerSubmission() (composerSubmissionSnapshot, error) {
	if a == nil {
		return composerSubmissionSnapshot{}, fmt.Errorf("composer is unavailable")
	}
	raw := a.textarea.Value()
	runes := []rune(raw)
	ordered := append([]threadComposerElement(nil), a.composerElements...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Start != ordered[j].Start {
			return ordered[i].Start < ordered[j].Start
		}
		return ordered[i].End < ordered[j].End
	})

	parts := make([]composerOrderedPart, 0, len(ordered)*2+1)
	contexts := make([]string, 0, len(ordered))
	imageCount := 0
	seenIDs := make(map[string]struct{}, len(ordered))
	cursor := 0
	appendText := func(value string) {
		if value == "" {
			return
		}
		if count := len(parts); count > 0 && parts[count-1].kind == engine.QueuedPromptPartText {
			parts[count-1].text += value
			return
		}
		parts = append(parts, composerOrderedPart{kind: engine.QueuedPromptPartText, text: value})
	}

	for _, element := range ordered {
		if strings.TrimSpace(element.ID) == "" {
			return composerSubmissionSnapshot{}, fmt.Errorf("composer element has no identity")
		}
		if _, exists := seenIDs[element.ID]; exists {
			return composerSubmissionSnapshot{}, fmt.Errorf("composer element %s is duplicated", element.Label)
		}
		seenIDs[element.ID] = struct{}{}
		if element.Start < cursor || !composerElementMatches(runes, element) {
			return composerSubmissionSnapshot{}, fmt.Errorf("composer element %s has an invalid range", element.Label)
		}
		appendText(string(runes[cursor:element.Start]))
		switch element.Kind {
		case composerElementKindPaste:
			if element.Value == "" {
				return composerSubmissionSnapshot{}, fmt.Errorf("%s has no pasted content", element.Label)
			}
			appendText(element.Value)
		case composerElementKindImage:
			image := a.draftMedia[element.ID]
			if image == nil || len(image.Data) == 0 || image.MIMEType == "" {
				return composerSubmissionSnapshot{}, fmt.Errorf("%s has no image content", element.Label)
			}
			if element.MIMEType != "" && element.MIMEType != image.MIMEType {
				return composerSubmissionSnapshot{}, fmt.Errorf("%s has inconsistent image metadata", element.Label)
			}
			parts = append(parts, composerOrderedPart{
				kind: engine.QueuedPromptPartImage, image: image,
			})
			imageCount++
		case composerElementKindFile, composerElementKindSkill, composerElementKindMCPResource:
			if element.Data == "" {
				return composerSubmissionSnapshot{}, fmt.Errorf("%s is still loading", element.Label)
			}
			appendText(element.Label)
			encoded, err := json.Marshal(map[string]string{
				"kind": element.Kind, "name": element.Name, "source": element.Value,
				"mime_type": element.MIMEType, "content": element.Data,
			})
			if err != nil {
				return composerSubmissionSnapshot{}, fmt.Errorf("encode %s: %w", element.Label, err)
			}
			contexts = append(contexts, "<composer_context>\n"+string(encoded)+"\n</composer_context>")
		default:
			return composerSubmissionSnapshot{}, fmt.Errorf("composer element %s has unsupported kind %q", element.Label, element.Kind)
		}
		cursor = element.End
	}
	appendText(string(runes[cursor:]))
	if len(contexts) > 0 {
		appendText("\n\n" + strings.Join(contexts, "\n\n"))
	}
	parts = trimComposerOrderedParts(parts)
	if len(parts) == 0 {
		return composerSubmissionSnapshot{}, nil
	}

	inputParts := make([]engine.UntrustedPromptPart, 0, len(parts))
	var textBuilder strings.Builder
	for _, part := range parts {
		switch part.kind {
		case engine.QueuedPromptPartText:
			inputParts = append(inputParts, engine.NewPromptTextPart(part.text))
			textBuilder.WriteString(part.text)
		case engine.QueuedPromptPartImage:
			inputParts = append(inputParts, engine.NewPromptImagePart(
				base64.StdEncoding.EncodeToString(part.image.Data),
				part.image.MIMEType,
				part.image.Detail,
			))
		}
	}
	display := strings.TrimSpace(raw)
	safeText := strings.TrimSpace(expandComposerElementsForPersistence(raw, ordered))
	if display == "" {
		display = safeText
	}
	return composerSubmissionSnapshot{
		Display:    display,
		Text:       strings.TrimSpace(textBuilder.String()),
		SafeText:   safeText,
		Input:      engine.NewUntrustedPromptInput(inputParts...),
		Elements:   a.composerDisplayElements(),
		HasImages:  imageCount > 0,
		HasContext: len(contexts) > 0,
	}, nil
}

func trimComposerOrderedParts(parts []composerOrderedPart) []composerOrderedPart {
	firstText := -1
	lastText := -1
	for i := range parts {
		if parts[i].kind == engine.QueuedPromptPartText {
			if firstText < 0 {
				firstText = i
			}
			lastText = i
		}
	}
	if firstText >= 0 {
		parts[firstText].text = strings.TrimLeftFunc(parts[firstText].text, unicode.IsSpace)
		parts[lastText].text = strings.TrimRightFunc(parts[lastText].text, unicode.IsSpace)
	}
	compacted := make([]composerOrderedPart, 0, len(parts))
	for _, part := range parts {
		if part.kind == engine.QueuedPromptPartText && part.text == "" {
			continue
		}
		if part.kind == engine.QueuedPromptPartText && len(compacted) > 0 &&
			compacted[len(compacted)-1].kind == engine.QueuedPromptPartText {
			compacted[len(compacted)-1].text += part.text
			continue
		}
		compacted = append(compacted, part)
	}
	return compacted
}

func sanitizedComposerElements(elements []threadComposerElement) []threadComposerElement {
	sanitized := make([]threadComposerElement, 0, len(elements))
	for _, element := range elements {
		if element.Kind == "" || element.Label == "" {
			continue
		}
		sanitized = append(sanitized, threadComposerElement{
			Kind:  element.Kind,
			Label: element.Label,
		})
	}
	return sanitized
}

func expandComposerElements(text string, elements []threadComposerElement) string {
	return expandComposerElementsWith(text, elements, func(element threadComposerElement) (string, bool) {
		if element.Kind == composerElementKindPaste {
			return element.Value, true
		}
		return "", false
	})
}

func expandComposerElementsForPersistence(text string, elements []threadComposerElement) string {
	return expandComposerElementsWith(text, elements, func(element threadComposerElement) (string, bool) {
		switch element.Kind {
		case composerElementKindPaste:
			return element.Value, true
		case composerElementKindImage:
			return element.Label + " (image content not restored)", true
		case composerElementKindFile, composerElementKindSkill, composerElementKindMCPResource:
			return element.Label, true
		default:
			return "", false
		}
	})
}

func expandComposerElementsWith(
	text string,
	elements []threadComposerElement,
	replacement func(threadComposerElement) (string, bool),
) string {
	if len(elements) == 0 {
		return text
	}
	ordered := append([]threadComposerElement(nil), elements...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })
	runes := []rune(text)
	var rebuilt strings.Builder
	cursor := 0
	for _, element := range ordered {
		if element.Start < cursor || !composerElementMatches(runes, element) {
			continue
		}
		value, replace := replacement(element)
		if !replace {
			continue
		}
		rebuilt.WriteString(string(runes[cursor:element.Start]))
		rebuilt.WriteString(value)
		cursor = element.End
	}
	if cursor == 0 {
		return text
	}
	rebuilt.WriteString(string(runes[cursor:]))
	return rebuilt.String()
}

func trimComposerEntry(text string, elements []threadComposerElement) (string, []threadComposerElement) {
	runes := []rune(text)
	start := 0
	for start < len(runes) && unicode.IsSpace(runes[start]) {
		start++
	}
	end := len(runes)
	for end > start && unicode.IsSpace(runes[end-1]) {
		end--
	}
	trimmed := string(runes[start:end])
	trimmedElements := make([]threadComposerElement, 0, len(elements))
	for _, element := range elements {
		if element.Start < start || element.End > end {
			continue
		}
		element.Start -= start
		element.End -= start
		if composerElementMatches([]rune(trimmed), element) {
			trimmedElements = append(trimmedElements, element)
		}
	}
	return trimmed, trimmedElements
}

func (a *App) recordComposerHistory(expanded string) {
	raw, elements := trimComposerEntry(a.textarea.Value(), a.composerElements)
	safe := strings.TrimSpace(expandComposerElementsForPersistence(raw, elements))
	if safe == "" {
		safe = strings.TrimSpace(expanded)
	}
	if safe == "" {
		return
	}
	a.history = append(a.history, safe)
}

func (a *App) restoreComposerHistoryEntry(text string, elements []threadComposerElement) {
	a.textarea.SetValue(text)
	a.textarea.CursorEnd()
	restored := make([]threadComposerElement, 0, len(elements))
	for _, element := range elements {
		if element.Kind == composerElementKindPaste {
			restored = append(restored, element)
		}
	}
	a.composerElements = cloneThreadComposerElements(restored)
	a.markComposerChanged()
	a.gcDraftMedia()
}
