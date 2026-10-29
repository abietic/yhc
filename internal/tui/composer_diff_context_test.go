package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/session"
	"github.com/charmbracelet/x/ansi"
)

const diffContextFixture = "diff --git a/file.txt b/file.txt\nindex 1234567..abcdef0 100644\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@ first\n-old one\n+KEEP_HUNK\n@@ -20 +20 @@ second\n-old two\n+DROP_HUNK\n"

type tuiDiffRunner struct{ patch string }

func (r *tuiDiffRunner) Run(context.Context, string, ...string) (engine.WorkspaceCommandResult, error) {
	return engine.WorkspaceCommandResult{}, fmt.Errorf("unbounded capture is forbidden")
}

func (r *tuiDiffRunner) RunBounded(_ context.Context, _ string, _ int, args ...string) (engine.WorkspaceCommandResult, error) {
	if args[0] == "rev-parse" {
		return engine.WorkspaceCommandResult{Stdout: "true\n"}, nil
	}
	return engine.WorkspaceCommandResult{Stdout: r.patch}, nil
}

func diffContextApp(t *testing.T, width int) (*App, *tuiDiffRunner) {
	t.Helper()
	app := outputContextApp(t, width)
	runner := &tuiDiffRunner{patch: diffContextFixture}
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: t.TempDir(), TranscriptDir: t.TempDir(), WorkspaceCommandRunner: runner, CommandEntrypoint: commands.EntrypointTUI})
	t.Cleanup(eng.Close)
	app.SetEngine(eng)
	return app, runner
}

func TestComposerDiffContextCapturedHunkManagerUndo(t *testing.T) {
	app, runner := diffContextApp(t, 80)
	before := app.captureComposerUndoEntry()
	contextManagerKey(app, 'c', tea.ModAlt)
	cmd := app.handleComposerContextManagerKey(tea.KeyPressMsg{Code: 'd'})
	if cmd == nil || app.state != StateDiffContext {
		t.Fatal("manager did not open the Git selector")
	}
	app.Update(cmd())
	runner.patch = "later source mutation"
	contextManagerKey(app, tea.KeyTab, 0)
	contextManagerKey(app, tea.KeyEnter, 0)
	if app.state != StateContextManager || len(app.composerElements) != 1 {
		t.Fatalf("selection did not return to manager: state=%v elements=%#v", app.state, app.composerElements)
	}
	item := app.composerElements[0]
	if item.Kind != "git_diff" || !strings.Contains(item.Data, "+KEEP_HUNK") || strings.Contains(item.Data, "DROP_HUNK") || !strings.Contains(item.Data, "--- a/file.txt") {
		t.Fatalf("selected hunk was not self-contained: %#v", item)
	}
	_, prompt, err := app.composerSubmissionPrompt()
	if err != nil || !strings.Contains(prompt, `"kind":"git_diff"`) || !strings.Contains(prompt, "KEEP_HUNK") || strings.Contains(prompt, "DROP_HUNK") || strings.Contains(prompt, "later source") {
		t.Fatalf("hunk submission: %q, %v", prompt, err)
	}
	if app.estimateComposerContextTokens(item).tokens == 0 || !strings.Contains(ansi.Strip(app.View().Content), "Git diff") {
		t.Fatal("diff missing from context preview/token estimate")
	}
	app.contextManager.filter = len(composerContextTypes) - 1
	if got := app.composerContextVisibleItems(app.composerElements); len(got) != 1 || got[0].ID != item.ID {
		t.Fatal("Diffs filter lost the new attachment")
	}
	contextManagerKey(app, tea.KeyDelete, 0)
	if len(app.composerElements) != 0 || app.textarea.Value() != before.Text {
		t.Fatal("manager removal changed surrounding draft")
	}
	contextManagerKey(app, 'z', tea.ModCtrl)
	if len(app.composerElements) != 1 || app.composerElements[0].Data != item.Data {
		t.Fatal("undo did not restore captured hunk")
	}
	contextManagerKey(app, tea.KeyEsc, 0)
	app.textarea.CursorEnd()
	if !strings.Contains(ansi.Strip(app.renderComposerContextPreview()), "Git diff") || !app.removeComposerContext() {
		t.Fatal("diff reference lacked cursor preview/backspace removal")
	}
	if len(app.engine.GetMessages()) != 0 || app.running {
		t.Fatal("selection or removal made a model call")
	}
}

func TestComposerDiffContextFullFileAndCancelFences(t *testing.T) {
	for _, change := range []string{"close", "edit", "engine", "source", "cwd"} {
		t.Run(change, func(t *testing.T) {
			app, _ := diffContextApp(t, 80)
			before := app.captureComposerUndoEntry()
			cmd := app.openComposerDiffContext(commands.DiffContextAll)
			msg := cmd()
			switch change {
			case "close":
				contextManagerKey(app, tea.KeyEsc, 0)
			case "edit":
				app.markComposerChanged()
			case "engine":
				app.SetEngine(nil)
			case "source":
				_ = app.handleComposerDiffContextKey(tea.KeyPressMsg{Code: tea.KeyRight})
			case "cwd":
				result := msg.(composerDiffContextMsg)
				result.snapshot.CWD = "other-root"
				msg = result
			}
			app.Update(msg)
			app.handleComposerDiffContextKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			if !reflect.DeepEqual(before, app.captureComposerUndoEntry()) {
				t.Fatal("a stale result mutated the draft")
			}
		})
	}
	app, _ := diffContextApp(t, 80)
	app.Update(app.openComposerDiffContext(commands.DiffContextStaged)())
	contextManagerKey(app, tea.KeyEnter, 0)
	if len(app.composerElements) != 1 || app.composerElements[0].Data != diffContextFixture || !strings.Contains(app.composerElements[0].Value, "staged") {
		t.Fatal("full-file selection truncated the patch or lost source")
	}
}

func TestComposerDiffContextUnavailableAndModalIsolation(t *testing.T) {
	app, runner := diffContextApp(t, 80)
	runner.patch = "diff --git a/img b/img\nindex 1234567..abcdef0 100644\nBinary files a/img and b/img differ\n"
	before := app.captureComposerUndoEntry()
	app.Update(app.openComposerDiffContext(commands.DiffContextAll)())
	contextManagerKey(app, tea.KeyEnter, 0)
	if !reflect.DeepEqual(before, app.captureComposerUndoEntry()) || !strings.Contains(ansi.Strip(app.View().Content), "binary") {
		t.Fatal("binary patch was selectable")
	}
	app.pushDialog(StateHelp)
	contextManagerKey(app, tea.KeyEnter, 0)
	if !reflect.DeepEqual(before, app.captureComposerUndoEntry()) {
		t.Fatal("hidden diff selector consumed another dialog's Enter")
	}
	app.removeDialog(StateHelp)
	contextManagerKey(app, tea.KeyEsc, 0)
	runner.patch = diffContextFixture
	app.Update(app.openComposerDiffContext(commands.DiffContextAll)())
	app.Update(tea.PasteMsg{Content: "must not reach draft"})
	if !reflect.DeepEqual(before, app.captureComposerUndoEntry()) {
		t.Fatal("paste edited draft behind selector")
	}
}

func TestComposerDiffContextLimitsHistoryAndSearchOwnership(t *testing.T) {
	app, _ := diffContextApp(t, 80)
	contextManagerKey(app, 'c', tea.ModAlt)
	contextManagerKey(app, '/', 0)
	updateAppSilent(app, tea.KeyPressMsg{Code: 'd', Text: "d"})
	if app.state != StateContextManager || app.contextManager.query.Value() != "d" {
		t.Fatal("diff shortcut stole search input")
	}
	contextManagerKey(app, tea.KeyEsc, 0)
	contextManagerKey(app, tea.KeyEsc, 0)
	for i := 0; i < maxThreadComposerElements; i++ {
		app.composerElements = append(app.composerElements, threadComposerElement{ID: fmt.Sprint(i)})
	}
	app.Update(app.openComposerDiffContext(commands.DiffContextAll)())
	app.handleComposerDiffContextKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(app.composerElements) != maxThreadComposerElements || app.textarea.Value() != "diagnose " {
		t.Fatal("attachment count limit was bypassed")
	}
	contextManagerKey(app, tea.KeyEsc, 0)
	app.composerElements = []threadComposerElement{{Data: strings.Repeat("x", maxComposerPayloadBytes)}}
	app.Update(app.openComposerDiffContext(commands.DiffContextAll)())
	app.handleComposerDiffContextKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(app.composerElements) != 1 || app.textarea.Value() != "diagnose " {
		t.Fatal("draft payload limit was bypassed")
	}
	contextManagerKey(app, tea.KeyEsc, 0)
	app.composerElements = nil
	app.Update(app.openComposerDiffContext(commands.DiffContextAll)())
	contextManagerKey(app, tea.KeyEnter, 0)
	snapshot, err := app.captureComposerSubmission()
	if err != nil || strings.Contains(snapshot.SafeText, "KEEP_HUNK") || !strings.Contains(snapshot.SafeText, "diff content not restored") {
		t.Fatalf("unsafe history: %q, %v", snapshot.SafeText, err)
	}
	if err := app.persistSessionViewState(); err != nil {
		t.Fatal(err)
	}
	// No patch or source path is stored in the presentation sidecar.
	path := session.SessionViewStatePath(app.engine.GetTranscriptDir(), app.engine.SessionID())
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "KEEP_HUNK") || strings.Contains(string(data), "git:all") {
		t.Fatalf("sidecar leaked diff payload: %s, %v", path, err)
	}
}

func TestComposerDiffContextResponsiveGolden(t *testing.T) {
	var snapshots []string
	for _, width := range []int{40, 80, 120, 180} {
		app, _ := diffContextApp(t, width)
		app.Update(app.openComposerDiffContext(commands.DiffContextAll)())
		contextManagerKey(app, tea.KeyTab, 0)
		view := ansi.Strip(app.composerDiffContextOverlay("", width, 24))
		for _, want := range []string{"Git diff context", "Hunks", "KEEP_HUNK", "Esc cancel"} {
			if !strings.Contains(view, want) {
				t.Fatalf("width %d omitted %q: %s", width, want, view)
			}
		}
		rows := strings.Split(view, "\n")
		for i := range rows {
			if app.renderEnvironment.profile.measure(rows[i], 0) > width {
				t.Fatalf("diff selector exceeded %d columns", width)
			}
			rows[i] = strings.TrimRight(rows[i], " ")
		}
		snapshots = append(snapshots, fmt.Sprintf("== %d ==\n%s", width, strings.Trim(strings.Join(rows, "\n"), "\n")))
		for _, height := range []int{6, 12} {
			compact := ansi.Strip(app.composerDiffContextOverlay("", width, height))
			if !strings.Contains(compact, "Esc cancel") || strings.Contains(compact, "KEEP_HUNK") {
				t.Fatalf("compact view lost controls or kept optional preview: %s", compact)
			}
		}
	}
	got := strings.Join(snapshots, "\n") + "\n"
	path := filepath.Join("testdata", "composer_diff_context.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil || string(want) != got {
		t.Fatalf("diff context golden mismatch: %v\n%s", err, got)
	}
}

type cancelDiffRunner struct{ started chan struct{} }

func (r cancelDiffRunner) Run(context.Context, string, ...string) (engine.WorkspaceCommandResult, error) {
	return engine.WorkspaceCommandResult{}, fmt.Errorf("unbounded capture")
}

func (r cancelDiffRunner) RunBounded(ctx context.Context, _ string, _ int, _ ...string) (engine.WorkspaceCommandResult, error) {
	close(r.started)
	<-ctx.Done()
	return engine.WorkspaceCommandResult{}, ctx.Err()
}

func TestComposerDiffContextCancellationStopsGit(t *testing.T) {
	app := outputContextApp(t, 80)
	started := make(chan struct{})
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{CWD: t.TempDir(), TranscriptDir: t.TempDir(), WorkspaceCommandRunner: cancelDiffRunner{started}})
	t.Cleanup(eng.Close)
	app.SetEngine(eng)
	cmd := app.openComposerDiffContext(commands.DiffContextAll)
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("capture did not start")
	}
	app.CancelContextCapture()
	select {
	case result := <-done:
		app.Update(result)
	case <-time.After(5 * time.Second):
		t.Fatal("program shutdown did not cancel the Git runner")
	}
	if len(app.composerElements) != 0 {
		t.Fatal("canceled capture attached a patch")
	}
}
