package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine"
	"github.com/charmbracelet/x/ansi"
)

func fileRangeComposer(t *testing.T, path, content, query string) *App {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: dir, TranscriptDir: t.TempDir()})
	t.Cleanup(eng.Close)
	originalLocations := resolveHistoryLocations
	locations := historyLocations{projectRoot: dir, compatibilityConfigDir: t.TempDir()}
	resolveHistoryLocations = func() (historyLocations, error) { return locations, nil }
	t.Cleanup(func() { resolveHistoryLocations = originalLocations })
	app := New(Config{Engine: eng, KeybindingsConfigDir: t.TempDir()})
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	app.mentionIndex = composerMentionIndex{loaded: true, files: []string{path}}
	app.textarea.SetValue(query)
	app.textarea.CursorEnd()
	app.updateMentionHints()
	return app
}

func attachFileRange(t *testing.T, app *App) {
	t.Helper()
	if len(app.mentionHints) != 1 {
		t.Fatalf("file range candidates = %#v", app.mentionHints)
	}
	cmd := app.acceptMentionHint()
	if cmd == nil {
		t.Fatal("file range did not schedule asynchronous loading")
	}
	app.Update(cmd())
}

func TestComposerFileRangeSelectsOnlyRequestedLines(t *testing.T) {
	for _, query := range []string{"inspect @ma#2-3", "inspect @file:ma#2-3"} {
		t.Run(query, func(t *testing.T) {
			app := fileRangeComposer(t, "main.go", "outside-before\n第二行\r\nthird line\noutside-after\n", query)
			attachFileRange(t, app)
			if len(app.composerElements) != 1 || app.composerElements[0].Data != "第二行\r\nthird line\n" {
				t.Fatalf("range content = %#v", app.composerElements)
			}
			display, prompt, err := app.composerSubmissionPrompt()
			if err != nil || display != "inspect @main.go#2-3" {
				t.Fatalf("range display=%q err=%v", display, err)
			}
			_, block, found := strings.Cut(prompt, "<composer_context>\n")
			if !found {
				t.Fatal("range not included in accepted prompt input")
			}
			block, _, _ = strings.Cut(block, "\n</composer_context>")
			var metadata map[string]any
			if err := json.Unmarshal([]byte(block), &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata["source"] != "main.go" || metadata["start_line"] != float64(2) || metadata["end_line"] != float64(3) || metadata["content"] != "第二行\r\nthird line\n" {
				t.Fatalf("range metadata=%#v", metadata)
			}
			if strings.Contains(prompt, "outside-before") || strings.Contains(prompt, "outside-after") {
				t.Fatal("unselected file content leaked into the prompt")
			}
			if saved := expandComposerElementsForPersistence(app.textarea.Value(), app.composerElements); strings.Contains(saved, "第二行") || !strings.Contains(saved, "@main.go#2-3") {
				t.Fatalf("range history=%q", saved)
			}
		})
	}
}

func TestComposerFileRangePreviewAndRemoval(t *testing.T) {
	app := fileRangeComposer(t, "main.go", "before\nselected body\nafter\n", "inspect @ma#2")
	attachFileRange(t, app)
	preview := ansi.Strip(app.renderHintSection())
	for _, want := range []string{"lines 2-2", "selected body", "14 B", "Backspace"} {
		if !strings.Contains(preview, want) {
			t.Errorf("preview missing %q: %q", want, preview)
		}
	}
	app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if len(app.composerElements) != 0 || app.textarea.Value() != "inspect " {
		t.Fatalf("range removal: draft=%q elements=%#v", app.textarea.Value(), app.composerElements)
	}
	app.undoComposerEdit()
	if len(app.composerElements) != 1 || app.composerElements[0].Data != "selected body\n" || app.textarea.Value() != "inspect @main.go#2 " {
		t.Fatal("undo did not restore the captured range and its label")
	}
}

func TestComposerFileRangeUsesCapturedSnapshotAndSanitizedHistory(t *testing.T) {
	app := fileRangeComposer(t, "main.go", "before\nselected body\nafter\n", "@ma#2")
	attachFileRange(t, app)
	if err := os.WriteFile(filepath.Join(app.engine.GetCWD(), "main.go"), []byte("changed disk content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	leader := app.activeThreadViewID()
	if err := app.switchThreadView("range-agent", engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	if err := app.switchThreadView(leader, engine.ThreadModeLiveAttach); err != nil {
		t.Fatal(err)
	}
	_, prompt, err := app.composerSubmissionPrompt()
	if err != nil || !strings.Contains(prompt, "selected body") || strings.Contains(prompt, "changed disk content") {
		t.Fatalf("range was reread or lost after thread switch: %q %v", prompt, err)
	}
	app.recordComposerHistory(app.textarea.Value())
	app.textarea.Reset()
	app.composerElements = nil
	app.historyIdx = len(app.history)
	app.navigateHistory(-1)
	if len(app.composerElements) != 0 || !strings.Contains(app.textarea.Value(), "@main.go#2") {
		t.Fatal("history restored a rich payload or lost the visible reference")
	}
}

func TestComposerFileRangeRemovedWhileLoadingRejectsLateResult(t *testing.T) {
	app := fileRangeComposer(t, "main.go", "one\ntwo\n", "@ma#2")
	cmd := app.acceptMentionHint()
	if cmd == nil {
		t.Fatal("range did not start loading")
	}
	app.handleEditorKey(tea.KeyPressMsg{Code: tea.KeyBackspace})
	app.Update(cmd())
	if len(app.composerElements) != 0 || app.textarea.Value() != "" {
		t.Fatal("late read restored a removed range")
	}
}

func TestComposerFileRangePreservesLiteralHashFilenames(t *testing.T) {
	for _, path := range []string{"report#2-3", "Report#2-3"} {
		t.Run(path, func(t *testing.T) {
			app := fileRangeComposer(t, path, "one\ntwo\n", "@"+path)
			attachFileRange(t, app)
			if app.composerElements[0].Data != "one\ntwo\n" || app.composerElements[0].StartLine != 0 {
				t.Fatal("exact literal filename was parsed as a selector")
			}
			app = fileRangeComposer(t, path, "one\ntwo\n", "@"+path+"#2")
			attachFileRange(t, app)
			if app.composerElements[0].Data != "two\n" || app.composerElements[0].StartLine != 2 {
				t.Fatal("range on a literal hash filename was not resolved")
			}
		})
	}
}

func TestComposerFileRangeRejectsInconsistentSubmissionMetadata(t *testing.T) {
	app := fileRangeComposer(t, "main.go", "one\ntwo\n", "@ma#2")
	attachFileRange(t, app)
	app.composerElements[0].EndLine = 3
	if _, _, err := app.composerSubmissionPrompt(); err == nil {
		t.Fatal("submission accepted inconsistent line metadata")
	}
}

func TestComposerFileRangePreviewWidthGolden(t *testing.T) {
	var got strings.Builder
	for _, width := range []int{40, 80, 120, 180} {
		content := "outside-before\n" + strings.Repeat("界", 90) + "\n\x1b]52;c;fixture\a\nthird selected line\nselected-but-preview-hidden\noutside-after\n"
		app := fileRangeComposer(t, "main.go", content, "@ma#2-5")
		app.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		attachFileRange(t, app)
		rendered := normalizeAppLayoutGolden(app.renderHintSection())
		for _, want := range []string{"lines 2-5", "Preview clipped", "Backspace", `\u001b]52`} {
			if !strings.Contains(rendered, want) {
				t.Errorf("width %d lost %q: %s", width, want, rendered)
			}
		}
		if strings.Contains(app.renderComposerContextPreview(), "\x1b]52") || strings.Contains(rendered, "selected-but-preview-hidden") {
			t.Fatal("preview emitted a control sequence or exceeded its line bound")
		}
		for _, row := range strings.Split(rendered, "\n") {
			if app.renderEnvironment.profile.measure(row, 0) > width {
				t.Fatalf("preview overflows width %d", width)
			}
		}
		_, prompt, err := app.composerSubmissionPrompt()
		if err != nil || !strings.Contains(prompt, "selected-but-preview-hidden") {
			t.Fatal("preview clipping truncated the submitted range")
		}
		got.WriteString("== " + strconv.Itoa(width) + " ==\n" + rendered + "\n")
	}
	path := "testdata/composer_file_range.golden"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got.String() {
		t.Fatalf("file range preview golden mismatch:\n%s", got.String())
	}
}

func TestComposerFileRangeInvalidSelectorDoesNotAttachWholeFile(t *testing.T) {
	for _, suffix := range []string{"#0", "#3-2", "#2-", "#2-x", "#999999999999999999999"} {
		t.Run(suffix, func(t *testing.T) {
			app := fileRangeComposer(t, "main.go", "sensitive unselected body\n", "@main.go"+suffix)
			if len(app.mentionHints) != 0 || !strings.Contains(ansi.Strip(app.renderHintSection()), "Invalid line range") {
				t.Fatalf("invalid range candidates=%#v hint=%q", app.mentionHints, app.renderHintSection())
			}
			if len(app.composerElements) != 0 {
				t.Fatal("invalid selector attached whole file")
			}
		})
	}
}

func TestComposerFileRangePastEOFDoesNotFallBack(t *testing.T) {
	app := fileRangeComposer(t, "main.go", "first\nsecond\n", "@main.go#1-3")
	attachFileRange(t, app)
	if len(app.composerElements) != 0 {
		t.Fatalf("past-EOF range attached content: %#v", app.composerElements)
	}
}
