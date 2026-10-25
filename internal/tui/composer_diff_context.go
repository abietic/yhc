package tui

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
)

const composerElementKindGitDiff = "git_diff"

// The selector owns a transient, engine-captured snapshot. Only an explicit
// Enter copies one complete patch into the active rich draft.
type composerDiffContext struct {
	requestID uint64
	engine    *engine.QueryEngine
	threadID  string
	revision  uint64
	cwd       string
	source    commands.DiffContextSource
	cancel    context.CancelFunc
	loading   bool
	files     []commands.DiffContextFile
	reason    string
	file      int
	hunk      int
	hunks     bool
}

type composerDiffContextMsg struct {
	requestID uint64
	snapshot  commands.DiffContextSnapshot
	files     []commands.DiffContextFile
	err       error
}

var composerDiffSources = [...]commands.DiffContextSource{commands.DiffContextAll, commands.DiffContextStaged, commands.DiffContextUnstaged}

// CancelContextCapture releases the selector's pending Git process when the
// owning terminal program stops, including renderer failure or external cancel.
func (a *App) CancelContextCapture() {
	a.closeComposerDiffContext()
}

func (a *App) closeComposerDiffContext() {
	if a.diffContext.cancel != nil {
		a.diffContext.cancel()
	}
	a.diffContext = composerDiffContext{}
}

func (a *App) diffContextDraftCurrent() bool {
	d := &a.diffContext
	return d.engine != nil && d.engine == a.engine && d.threadID == a.activeThreadViewID() &&
		d.revision == a.composerRevision && d.cwd == a.engine.GetCWD() &&
		a.composerContextEditable() && !a.running && a.isLeaderThreadView()
}

func (a *App) openComposerDiffContext(source commands.DiffContextSource) tea.Cmd {
	state, modal := a.activeDialogState()
	if modal && state != StateContextManager {
		return nil
	}
	if !a.composerContextEditable() || a.running || a.engine == nil || !a.isLeaderThreadView() ||
		(!modal && a.state != StateChat && a.state != StateWelcome) {
		a.showNotification("Git context requires an idle, editable leader draft", NotifyWarning)
		return nil
	}
	validSource := false
	for _, candidate := range composerDiffSources {
		validSource = validSource || source == candidate
	}
	if !validSource {
		return nil
	}
	a.dismissMentionHints()
	a.cancelArgumentCompletion()
	a.pushDialog(StateDiffContext)
	return a.loadComposerDiffContext(source)
}

func (a *App) loadComposerDiffContext(source commands.DiffContextSource) tea.Cmd {
	a.closeComposerDiffContext()
	a.diffContextSerial++
	if a.diffContextSerial == 0 {
		a.diffContextSerial++
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.diffContext = composerDiffContext{
		requestID: a.diffContextSerial, engine: a.engine, threadID: a.activeThreadViewID(),
		revision: a.composerRevision, cwd: a.engine.GetCWD(), source: source,
		cancel: cancel, loading: true,
	}
	request := a.diffContext
	return func() tea.Msg {
		defer cancel()
		msg := composerDiffContextMsg{requestID: request.requestID}
		if request.engine.GetCWD() != request.cwd {
			msg.err = fmt.Errorf("workspace changed before diff capture")
			return msg
		}
		msg.snapshot, msg.err = request.engine.CaptureWorkspaceDiff(ctx, source)
		if msg.err == nil && msg.snapshot.State == commands.WorkspaceDiffReady {
			msg.files, msg.err = commands.ParseDiffContext(msg.snapshot.Patch)
		}
		// The raw aggregate is not needed after parsing. Keep only the bounded
		// file/hunk projections until selection or cancellation.
		msg.snapshot.Patch = ""
		return msg
	}
}

func (a *App) handleComposerDiffContext(msg composerDiffContextMsg) {
	d := &a.diffContext
	if !a.hasDialog(StateDiffContext) || d.requestID != msg.requestID {
		return
	}
	if !a.diffContextDraftCurrent() || (msg.snapshot.CWD != "" && msg.snapshot.CWD != d.cwd) {
		a.removeDialog(StateDiffContext)
		return
	}
	d.loading, d.cancel = false, nil
	switch {
	case msg.err != nil:
		d.reason = msg.err.Error()
	case msg.snapshot.Source != d.source:
		d.reason = "Diff source changed; reopen the selector"
	case msg.snapshot.State != commands.WorkspaceDiffReady:
		d.reason = msg.snapshot.Reason
		if d.reason == "" {
			d.reason = "Git diff unavailable: " + string(msg.snapshot.State)
		}
	case len(msg.files) == 0:
		d.reason = "No tracked changes; use @file for untracked files"
	default:
		d.files = msg.files
	}
}

func (a *App) handleComposerDiffContextKey(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "esc" || msg.String() == "ctrl+c" {
		a.popDialog(StateDiffContext)
		return nil
	}
	if !a.diffContextDraftCurrent() {
		a.removeDialog(StateDiffContext)
		a.showNotification("Context draft changed; reopen the Git selector", NotifyWarning)
		return nil
	}
	d := &a.diffContext
	switch msg.String() {
	case "left", "right":
		index := 0
		for i, source := range composerDiffSources {
			if source == d.source {
				index = i
			}
		}
		delta := 1
		if msg.String() == "left" {
			delta = -1
		}
		return a.loadComposerDiffContext(composerDiffSources[(index+delta+len(composerDiffSources))%len(composerDiffSources)])
	case "tab", "shift+tab":
		d.hunks = !d.hunks
		d.hunk = 0
		return nil
	}
	if d.loading || len(d.files) == 0 {
		return nil
	}
	count, index := len(d.files), d.file
	if d.hunks {
		count, index = len(d.files[d.file].Hunks), d.hunk
	}
	switch msg.String() {
	case "up", "k", "ctrl+p":
		index--
	case "down", "j", "ctrl+n":
		index++
	case "home":
		index = 0
	case "end":
		index = count - 1
	case "enter":
		a.attachComposerDiffContext()
		return nil
	default:
		return nil
	}
	index = max(0, min(index, count-1))
	if d.hunks {
		d.hunk = index
	} else {
		d.file, d.hunk = index, 0
	}
	return nil
}

func (a *App) attachComposerDiffContext() {
	d := &a.diffContext
	if !a.diffContextDraftCurrent() || d.loading || d.file < 0 || d.file >= len(d.files) {
		return
	}
	file := d.files[d.file]
	if file.UnavailableReason != "" {
		a.showNotification(file.UnavailableReason, NotifyWarning)
		return
	}
	patch, name := file.Patch, fmt.Sprintf("%s | %s | file", file.Path, d.source)
	source := fmt.Sprintf("git:%s:%s", d.source, file.Path)
	if d.hunks {
		if d.hunk < 0 || d.hunk >= len(file.Hunks) {
			return
		}
		hunk := file.Hunks[d.hunk]
		patch = hunk.Patch
		name = fmt.Sprintf("%s | %s | %s", file.Path, d.source, hunk.Header)
		source += fmt.Sprintf("#hunk=%d", d.hunk+1)
	}
	if patch == "" || len(patch) > commands.MaxDiffContextBytes {
		a.showNotification("Diff context exceeds the 5 MiB attachment limit", NotifyWarning)
		return
	}
	if len(a.composerElements) >= maxThreadComposerElements || a.composerPayloadBytes()+len(patch) > maxComposerPayloadBytes {
		a.showNotification("Composer attachments exceed the 32 item or 10 MiB draft limit", NotifyWarning)
		return
	}
	before := a.captureComposerUndoEntry()
	anchor := before.CursorOffset
	for _, element := range a.composerElements {
		if anchor > element.Start && anchor < element.End {
			a.showNotification("Move the cursor outside the existing attachment reference", NotifyWarning)
			return
		}
	}
	a.popDialog(StateDiffContext)
	a.nextComposerElementID++
	label := fmt.Sprintf("[Diff #%d]", a.nextComposerElementID)
	item := threadComposerElement{
		ID: fmt.Sprintf("diff-%d", a.nextComposerElementID), Kind: composerElementKindGitDiff,
		Label: label, Name: name, Value: source, MIMEType: "text/x-diff", Data: patch,
		Start: anchor, End: anchor + utf8.RuneCountInString(label),
	}
	a.textarea.InsertString(label + " ")
	a.reconcileComposerElements(before.Text, a.textarea.Value())
	a.composerElements = append(a.composerElements, item)
	a.markComposerChanged()
	a.recordComposerUndo(before)
	a.contextManager.selectedID = item.ID
	a.syncInputModeFromText()
	a.updateLayout()
	a.showToast("Captured Git diff attached to draft")
}

func (a *App) composerDiffContextOverlay(base string, width, height int) string {
	d := &a.diffContext
	profile := a.renderEnvironment.normalized().profile
	inner := max(1, min(88, width-6))
	mode := "Files"
	if d.hunks {
		mode = "Hunks"
	}
	parts := []string{a.styles.DialogTitle.Render("Git diff context"), fmt.Sprintf("%s | %s", d.source, mode)}
	help := []string{"Left/Right source | Tab file/hunk", "Enter attach | Esc cancel"}
	available := max(1, height-len(parts)-len(help)-2)
	switch {
	case d.loading:
		parts = append(parts, "Capturing Git snapshot...")
	case d.reason != "":
		parts = append(parts, composerContextLabel(d.reason))
	case len(d.files) > 0:
		file := d.files[d.file]
		index, count := d.file, len(d.files)
		if d.hunks {
			parts = append(parts, composerContextLabel(file.Path))
			index, count = d.hunk, len(file.Hunks)
			available--
		}
		visible := max(1, min(6, available-1))
		start := max(0, index-visible+1)
		for i := start; i < min(count, start+visible); i++ {
			var label string
			if d.hunks {
				label = file.Hunks[i].Header
			} else {
				label = d.files[i].Path
				if d.files[i].UnavailableReason != "" {
					label += " (unavailable)"
				}
			}
			row := fmt.Sprintf("%d. %s", i+1, composerContextLabel(label))
			row = modalEllipsize(profile, row, inner, 0, "...")
			if i == index {
				row = a.styles.Selected.Render(row)
			}
			parts = append(parts, row)
		}
		if count == 0 {
			parts = append(parts, "No text hunks; Tab for file patch")
		} else {
			parts = append(parts, fmt.Sprintf("%d of %d | captured snapshot", index+1, count))
		}
		if file.UnavailableReason != "" {
			parts = append(parts, file.UnavailableReason)
		} else if height >= 15 && count > 0 {
			body := file.Patch
			preview := body
			if d.hunks {
				body = file.Hunks[d.hunk].Patch
				preview = file.Hunks[d.hunk].Body
			} else if len(file.Hunks) > 0 {
				preview = file.Hunks[0].Body
			}
			parts = append(parts, fmt.Sprintf("%d B | text/x-diff", len(body)))
			prefix, _ := composerContextPrefix(preview, 1024)
			lines := strings.Split(strings.TrimSuffix(prefix, "\n"), "\n")
			for _, line := range lines[:min(3, len(lines))] {
				parts = append(parts, composerContextPreviewLiteral(line))
			}
			parts = append(parts, "Preview clipped; full patch kept")
		}
	default:
		parts = append(parts, "No tracked changes")
	}
	// Keep controls visible even on a short terminal; previews yield first.
	parts = parts[:min(len(parts), max(1, height-len(help)-2))]
	parts = append(parts, help...)
	for i, part := range parts {
		parts[i] = modalProjectLine(profile, part, inner, 0)
	}
	dialog := contentRenderStyleWidth(profile, a.styles.DialogBorder, inner+2, strings.Join(parts, "\n"))
	view, _ := modalCenteredOverlay(profile, base, dialog, width, height)
	return view
}
