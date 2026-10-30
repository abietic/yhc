package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/charmbracelet/x/ansi"
	"github.com/cloudwego/eino/schema"
)

func TestComposerContextBudgetVisibleAndPassive(t *testing.T) {
	app := outputContextApp(t, 80)
	contextManagerFixture(app, []string{"@file", "[Pasted Content]"}, []string{"file", "paste"}, []string{strings.Repeat("x", 1600), "完整的粘贴文本"})
	before := app.captureComposerUndoEntry()
	app.openComposerContextManager()
	contextManagerKey(app, tea.KeyHome, 0)
	view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
	for _, want := range []string{"Attachments: ~", "Last input:", "tokens", "~"} {
		if !strings.Contains(view, want) {
			t.Errorf("budget panel missing %q: %s", want, view)
		}
	}
	if after := app.captureComposerUndoEntry(); !reflect.DeepEqual(before, after) || app.running || len(app.engine.GetMessages()) != 0 {
		t.Fatal("budget inspection mutated or submitted the draft")
	}
}

func TestComposerContextBudgetCountsCompleteSubmittedText(t *testing.T) {
	for _, kind := range []string{"file", "skill", "mcp_resource", "terminal_output", "paste"} {
		t.Run(kind, func(t *testing.T) {
			app := outputContextApp(t, 80)
			label := "@文"
			body := strings.Repeat("中文<>&\"\n", 300)
			item := threadComposerElement{ID: "one", Kind: kind, Label: label, Name: "名称", Value: "path/文", Data: body, MIMEType: "text/plain", Start: 0, End: 2}
			if kind == "file" {
				item.StartLine, item.EndLine, item.TotalLines = 20, 21, 50
			}
			if kind == "paste" {
				item.Value, item.Data = body, ""
			}
			app.textarea.SetValue(label)
			app.composerElements = []threadComposerElement{item}
			_, prompt, err := app.composerSubmissionPrompt()
			if err != nil {
				t.Fatal(err)
			}
			// Independent oracle: for a draft containing only this attachment,
			// its attributed text is the complete emitted prompt, not its preview.
			want := (len(strings.TrimSpace(prompt)) + 3) / 4
			app.openComposerContextManager()
			view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
			if !strings.Contains(view, fmt.Sprintf("Attachments: ~%d tokens", want)) || !strings.Contains(view, fmt.Sprintf("~%dt", want)) || want < 500 {
				t.Fatalf("count did not use full UTF-8/escaped submission (%d): %s", want, view)
			}
			if kind == "file" && (!strings.Contains(prompt, `"start_line":20`) || !strings.Contains(prompt, `"end_line":21`)) {
				t.Fatal("file-range envelope metadata changed")
			}
		})
	}
}

func contextBudgetEngine(t testing.TB, model string, measured bool) *engine.QueryEngine {
	t.Helper()
	root := t.TempDir()
	if measured {
		recorder := transcript.NewRecorder("context-budget", root)
		if err := recorder.RecordMessages([]*schema.Message{{Role: schema.Assistant, Content: "fixture response", ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 32000, CompletionTokens: 10, TotalTokens: 32010}}}}); err != nil {
			t.Fatal(err)
		}
		if err := recorder.Flush(); err != nil {
			t.Fatal(err)
		}
		if err := recorder.Close(); err != nil {
			t.Fatal(err)
		}
	}
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{SessionID: "context-budget", CWD: t.TempDir(), TranscriptDir: root, Model: model})
	t.Cleanup(eng.Close)
	return eng
}

func TestComposerContextBudgetKeepsProviderFactsAndThreadOwnership(t *testing.T) {
	for _, model := range []string{"gpt-4o", "unknown-fixture"} {
		t.Run(model, func(t *testing.T) {
			app := outputContextApp(t, 80)
			app.SetEngine(contextBudgetEngine(t, model, true))
			contextManagerFixture(app, []string{"@file"}, []string{"file"}, []string{"captured"})
			app.openComposerContextManager()
			view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
			if !strings.Contains(view, "Last input: 32.0k tokens") {
				t.Fatalf("provider usage was replaced by an estimate: %s", view)
			}
			if model == "gpt-4o" && !strings.Contains(view, "Window: 128.0k") || model == "unknown-fixture" && !strings.Contains(view, "Window: unavailable") {
				t.Fatalf("unknown window was invented or known capacity was lost: %s", view)
			}
			if err := app.switchThreadView("child-budget", engine.ThreadModeLiveAttach); err != nil {
				t.Fatal(err)
			}
			contextManagerFixture(app, []string{"@child"}, []string{"file"}, []string{"child content"})
			app.openComposerContextManager()
			view = ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
			if !strings.Contains(view, "Window: unavailable") || !strings.Contains(view, "Last input: unavailable") || strings.Contains(view, "32.0k") || strings.Contains(view, "128.0k") {
				t.Fatalf("leader budget leaked into child projection: %s", view)
			}
		})
	}
}

func TestComposerContextBudgetPartialLoadFilterRemoveAndUndo(t *testing.T) {
	app := outputContextApp(t, 80)
	app.SetEngine(contextBudgetEngine(t, "gpt-4o", false))
	contextManagerFixture(app, []string{"@loading", "@ready", "[Output #1]"}, []string{"file", "file", "terminal_output"}, []string{"", "ready body", "output body"})
	app.openComposerContextManager()
	contextManagerKey(app, tea.KeyHome, 0)
	view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
	if !strings.Contains(view, "(partial)") || !strings.Contains(view, "Tokens: pending") || !strings.Contains(view, "Last input: unavailable") {
		t.Fatalf("loading/unknown usage looked complete: %s", view)
	}
	app.Update(composerMentionPayloadMsg{elementID: "context-0", data: strings.Repeat("loaded", 300), mimeType: "text/plain"})
	before := app.captureComposerUndoEntry()
	view = ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
	if strings.Contains(view, "(partial)") || strings.Contains(view, "Tokens: pending") {
		t.Fatal("completed payload did not refresh cached estimate")
	}
	var total string
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "Attachments:") {
			total = strings.TrimSpace(line)
		}
	}
	contextManagerSearch(app, "Output")
	filtered := ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
	if !strings.Contains(filtered, total) {
		t.Fatal("filter hid attached bytes from the total budget")
	}
	contextManagerKey(app, 'a', tea.ModCtrl)
	contextManagerKey(app, tea.KeyDelete, 0)
	removed := ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
	if strings.Contains(removed, total) || len(app.composerElements) != 2 {
		t.Fatal("removal did not refresh the draft-wide total")
	}
	contextManagerKey(app, 'z', tea.ModCtrl)
	restored := ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
	if !strings.Contains(restored, total) || !reflect.DeepEqual(before, app.captureComposerUndoEntry()) {
		t.Fatal("undo did not restore estimates and original snapshot")
	}
}

func TestComposerContextBudgetImageWeightAndOverWindow(t *testing.T) {
	app := outputContextApp(t, 80)
	if err := app.addComposerImageAt("pic.png", "image/png", []byte("captured image"), len([]rune(app.textarea.Value()))); err != nil {
		t.Fatal(err)
	}
	app.openComposerContextManager()
	view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
	for _, want := range []string{"Attachments: ~32 tokens*", "~32t*", "generic image", "Generic image estimate"} {
		if !strings.Contains(view, want) {
			t.Errorf("generic image estimate missing %q: %s", want, view)
		}
	}
	if strings.Contains(view, "captured image") {
		t.Fatal("image bytes appeared in budget projection")
	}
	if got := composerContextWindowShare(24000, 12000); got != "200.0%" {
		t.Fatalf("over-window estimate was clamped: %s", got)
	}
	if got := composerContextWindowShare(1, 128000); got != "<0.1%" {
		t.Fatalf("nonzero share looked zero: %s", got)
	}
}

func TestComposerContextBudgetPreservesInternalPasteWhitespace(t *testing.T) {
	for _, body := range []string{strings.Repeat(" ", 400), strings.Repeat("\t\n ", 100) + "中文" + strings.Repeat(" \n", 100)} {
		app := outputContextApp(t, 80)
		contextManagerFixture(app, []string{"[Pasted Content]"}, []string{"paste"}, []string{body})
		_, prompt, err := app.composerSubmissionPrompt()
		if err != nil || !strings.Contains(prompt, body) {
			t.Fatalf("fixture did not retain internal whitespace: %q %v", prompt, err)
		}
		app.openComposerContextManager()
		view := ansi.Strip(app.composerContextManagerOverlay("", 80, 24))
		want := (len(body) + 3) / 4
		if !strings.Contains(view, fmt.Sprintf("Attachments: ~%d tokens", want)) {
			t.Fatalf("internal whitespace omitted from raw attachment estimate (%d): %s", want, view)
		}
	}
}

func TestComposerContextBudgetCompactImageLabel(t *testing.T) {
	app := outputContextApp(t, 40)
	if err := app.addComposerImageAt("pic.png", "image/png", []byte("fixture"), len([]rune(app.textarea.Value()))); err != nil {
		t.Fatal(err)
	}
	app.openComposerContextManager()
	view := ansi.Strip(app.composerContextManagerOverlay("", 40, 12))
	if !strings.Contains(view, "~32t") || !strings.Contains(view, "generic") {
		t.Fatalf("compact image estimate lacked its qualification: %s", view)
	}
}

func TestComposerContextBudgetGolden(t *testing.T) {
	var snapshots []string
	for _, width := range []int{40, 80, 120, 180} {
		app := outputContextApp(t, width)
		app.SetEngine(contextBudgetEngine(t, "gpt-4o", true))
		contextManagerFixture(app, []string{"@captured.go", "@pending"}, []string{"file", "file"}, []string{strings.Repeat("界", 1200) + "\nnext line", ""})
		if err := app.addComposerImageAt("capture.png", "image/png", []byte("fixture-image"), len([]rune(app.textarea.Value()))); err != nil {
			t.Fatal(err)
		}
		app.openComposerContextManager()
		contextManagerKey(app, tea.KeyEnd, 0)
		view := ansi.Strip(app.composerContextManagerOverlay("", width, 24))
		rows := strings.Split(view, "\n")
		if len(rows) > 24 {
			t.Fatal("budget exceeded viewport height")
		}
		for i, line := range rows {
			if app.renderEnvironment.profile.measure(line, 0) > width {
				t.Fatal("budget exceeded viewport width")
			}
			rows[i] = strings.TrimRight(line, " ")
		}
		snapshots = append(snapshots, fmt.Sprintf("== %d ==\n%s", width, strings.Trim(strings.Join(rows, "\n"), "\n")))
	}
	got := strings.Join(snapshots, "\n") + "\n"
	path := filepath.Join("testdata", "composer_context_budget.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Fatalf("budget golden mismatch:\n%s", got)
	}
}

func BenchmarkComposerContextBudgetCachedRepaint(b *testing.B) {
	app := outputContextApp(b, 80)
	contextManagerFixture(app, []string{"@large-one", "@large-two"}, []string{"file", "file"}, []string{strings.Repeat("x", 5<<20), strings.Repeat("y", 5<<20)})
	app.openComposerContextManager()
	contextManagerKey(app, 's', 0)
	app.composerContextManagerOverlay("", 80, 24)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.composerContextManagerOverlay("", 80, 24)
	}
}
