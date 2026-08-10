package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type compactCommandModel struct{}

func (m *compactCommandModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "summary of prior work"}, nil
}

func (m *compactCommandModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "summary of prior work"}}), nil
}

func runSlashCommandForTest(t *testing.T, app *App, command string) {
	t.Helper()
	cmd := app.sendSlashCommand(command)
	if cmd == nil {
		t.Fatalf("%s did not start engine action", command)
		return
	}
	started, ok := cmd().(engineStartMsg)
	if !ok {
		t.Fatalf("%s returned unexpected command message", command)
	}
	for event := range started.events {
		app.handleEngineEvent(event)
	}
}

func TestClearCommandClearsEngineViewAndTranscript(t *testing.T) {
	dir := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID: "clear-session", TranscriptDir: filepath.Join(dir, "transcripts"), CWD: dir,
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{{Role: schema.User, Content: "old question"}})
	if err := eng.GetTranscript().ReplaceWithReplacements(eng.GetMessages(), nil); err != nil {
		t.Fatal(err)
		return
	}

	app := New(Config{Engine: eng})
	app.chat.AppendUser("old question")
	runSlashCommandForTest(t, app, "/clear")

	if len(eng.GetMessages()) != 0 {
		t.Fatalf("engine history was not cleared: %#v", eng.GetMessages())
	}
	if len(app.chat.items) != 1 {
		t.Fatalf("expected only clear confirmation in TUI, got %d items", len(app.chat.items))
	}
	loaded, err := eng.GetTranscript().LoadFull()
	if err != nil {
		t.Fatal(err)
		return
	}
	if len(loaded.Messages) != 0 {
		t.Fatalf("persisted transcript was not cleared: %#v", loaded.Messages)
	}
}

func TestNewCommandRebindsTUIToDurableEmptySession(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "transcripts")
	sourceID := "tui-new-source"
	recorder := transcript.NewRecorder(sourceID, transcriptDir)
	sourceMessages := []*schema.Message{{Role: schema.User, Content: "old session"}}
	if err := recorder.RecordLifecycleBoundary(
		transcript.LifecycleCheckpoint,
		sourceMessages,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     sourceID,
		TranscriptDir: transcriptDir,
		CWD:           dir,
	})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng})
	app.chat.AppendUser("old session")

	runSlashCommandForTest(t, app, "/new")

	if eng.SessionID() == sourceID || eng.ThreadID() != eng.SessionID() {
		t.Fatalf(
			"new TUI identity = session %q thread %q",
			eng.SessionID(),
			eng.ThreadID(),
		)
	}
	if app.leaderThreadViewID() != eng.ThreadID() {
		t.Fatalf(
			"TUI leader view = %q, engine thread = %q",
			app.leaderThreadViewID(),
			eng.ThreadID(),
		)
	}
	if len(eng.GetMessages()) != 0 || len(app.chat.items) != 1 {
		t.Fatalf(
			"new TUI state: messages=%#v chat=%#v",
			eng.GetMessages(),
			app.chat.items,
		)
	}
	source, err := recorder.LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Messages) != 1 || source.Messages[0].Content != "old session" {
		t.Fatalf("new TUI erased source transcript: %#v", source.Messages)
	}
}

func TestSessionsExportUsesEngineSessionService(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "transcripts")
	recorder := transcript.NewRecorder("tui-export", transcriptDir)
	if err := recorder.RecordLifecycleBoundary(
		transcript.LifecycleCheckpoint,
		[]*schema.Message{
			{Role: schema.User, Content: "durable export prompt"},
			{Role: schema.Assistant, Content: "durable export answer"},
		},
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteSessionMetadata(
		recorder,
		&session.SessionMetadataFull{
			SessionID:          "tui-export",
			QueryKernelVersion: "project_graph/v1",
			QueryKernelStage:   "full",
			CreatedAt:          time.Now().UTC(),
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     "tui-export",
		TranscriptDir: transcriptDir,
		CWD:           dir,
	})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng})

	runSlashCommandForTest(t, app, `/sessions export current "tui report.txt"`)

	content, err := os.ReadFile(filepath.Join(dir, "tui report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "durable export prompt") {
		t.Fatalf("export content = %q", content)
	}
}

func TestCompactCommandRunsEngineCompactionAndReportsCompletion(t *testing.T) {
	dir := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID: "compact-session", TranscriptDir: filepath.Join(dir, "transcripts"), CWD: dir,
		ChatModel: &compactCommandModel{}, Model: "test-model",
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{
		{Role: schema.User, Content: strings.Repeat("question ", 200)},
		{Role: schema.Assistant, Content: strings.Repeat("answer ", 200)},
	})

	app := New(Config{Engine: eng})
	runSlashCommandForTest(t, app, "/compact")

	messages := eng.GetMessages()
	if len(messages) < 2 || messages[0].Extra == nil || messages[0].Extra["subtype"] != "compact_boundary" {
		t.Fatalf("manual compaction did not replace engine history: %#v", messages)
		return
	}
	last, ok := app.chat.items[len(app.chat.items)-1].(*CompactBoundaryMessage)
	if !ok {
		t.Fatalf("TUI did not report compaction completion: %#v", app.chat.items)
	}
	_ = last
	var boundaries int
	for _, item := range app.chat.items {
		if _, ok := item.(*CompactBoundaryMessage); ok {
			boundaries++
		}
	}
	if boundaries != 1 {
		t.Fatalf("compact result rendered %d boundaries, want 1", boundaries)
	}
}

func TestModelCommandMutatesOnlyAfterEngineExecution(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     "model-command-owner",
		CWD:           t.TempDir(),
		TranscriptDir: t.TempDir(),
		Model:         "model-before",
		ModelResolver: engine.ModelResolverFunc(func(modelSpec string) (provider.ResolvedConfig, error) {
			return provider.ResolvedConfig{Config: provider.Config{
				Provider: provider.ProviderAgenticClaude,
				Model:    modelSpec,
			}}, nil
		}),
		CommandEntrypoint: commands.EntrypointTUI,
	})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng, Model: "model-before"})

	cmd := app.sendSlashCommand("/model model-after")
	if cmd == nil {
		t.Fatal("/model did not delegate to the engine")
	}
	if got := eng.GetModelName(); got != "model-before" {
		t.Fatalf("TUI mutated model before engine execution: %q", got)
	}
	started, ok := cmd().(engineStartMsg)
	if !ok {
		t.Fatalf("/model returned %T, want engineStartMsg", started)
	}
	for event := range started.events {
		app.handleEngineEvent(event)
	}
	if got := eng.GetModelName(); got != "model-after" {
		t.Fatalf("engine model = %q, want model-after", got)
	}
	if app.model != "model-after" {
		t.Fatalf("TUI model projection = %q, want model-after", app.model)
	}
}

func TestP165aModelPickerRejectsBeforeEngineAndProjectionMutation(t *testing.T) {
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:         "model-picker-capability",
		CWD:               t.TempDir(),
		TranscriptDir:     t.TempDir(),
		Model:             "claude-sonnet-4-6",
		CommandEntrypoint: commands.EntrypointTUI,
		ModelResolver: engine.ModelResolverFunc(func(modelSpec string) (provider.ResolvedConfig, error) {
			if modelSpec != "claude-sonnet-4-6" {
				return provider.ResolvedConfig{}, errors.New("model is not configured")
			}
			return provider.ResolvedConfig{Config: provider.Config{
				Provider: provider.ProviderAgenticClaude,
				Model:    modelSpec,
			}}, nil
		}),
	})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng, Model: "claude-sonnet-4-6"})

	app.applyModelSelection("missing-model")
	if got := eng.GetModelName(); got != "claude-sonnet-4-6" {
		t.Fatalf("rejected picker model mutated engine to %q", got)
	}
	if app.model != "claude-sonnet-4-6" {
		t.Fatalf("rejected picker model mutated TUI projection to %q", app.model)
	}
}

func TestHiddenCommandsDoNotBypassRegistryInTUI(t *testing.T) {
	dir := t.TempDir()
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     "hidden-command-session",
		TranscriptDir: filepath.Join(dir, "transcripts"),
		CWD:           dir,
	})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng})
	app.chat.AppendUser("prior question")

	if cmd := app.sendSlashCommand("/mode plan"); cmd != nil {
		t.Fatal("hidden /mode unexpectedly started engine work")
	}
	if eng.PermissionMode() != permission.ModeDefault {
		t.Fatalf("hidden /mode changed permission mode to %q", eng.PermissionMode())
	}

	for _, input := range []string{"/rewrite", "/retry"} {
		if cmd := app.sendSlashCommand(input); cmd != nil {
			t.Fatalf("hidden %s unexpectedly started engine work", input)
		}
		if app.state == StateMessageSelect {
			t.Fatalf("hidden %s opened the message selector", input)
		}
	}

	var sawMode, sawRewrite, sawRetry bool
	for _, item := range app.chat.items {
		system, ok := item.(*SystemMessage)
		if !ok {
			continue
		}
		sawMode = sawMode || strings.Contains(system.content, "/mode was removed in P21.0")
		sawRewrite = sawRewrite || strings.Contains(system.content, "/rewrite was removed in P21.0")
		sawRetry = sawRetry || strings.Contains(system.content, "/retry was removed in P21.0")
	}
	if !sawMode || !sawRewrite || !sawRetry {
		t.Fatalf(
			"missing compatibility output: mode=%v rewrite=%v retry=%v items=%#v",
			sawMode,
			sawRewrite,
			sawRetry,
			app.chat.items,
		)
	}
}

func TestTUILocalCommandsUseNormalizedRegistryNames(t *testing.T) {
	for _, input := range []string{"/TEAM", "/TeAmS"} {
		t.Run(input, func(t *testing.T) {
			app := New(Config{})
			if cmd := app.sendSlashCommand(input); cmd == nil {
				t.Fatalf("%s did not return the teams refresh command", input)
			}
			if app.state != StateTeams {
				t.Fatalf("%s state = %v, want teams", input, app.state)
			}
		})
	}

	t.Run("team remains available while leader runs", func(t *testing.T) {
		app := New(Config{})
		app.running = true
		if cmd := app.sendSlashCommand("/team"); cmd == nil {
			t.Fatal("running /team did not schedule monitor refresh")
		}
		if app.state != StateTeams || !app.teamsPanel.Visible() {
			t.Fatalf("running /team state=%v visible=%v", app.state, app.teamsPanel.Visible())
		}
		for _, item := range app.chat.items {
			if system, ok := item.(*SystemMessage); ok && strings.Contains(system.content, "Cannot run command") {
				t.Fatalf("read-only monitor was blocked while running: %q", system.content)
			}
		}
	})

	t.Run("queue", func(t *testing.T) {
		app := New(Config{})
		if cmd := app.sendSlashCommand("/Queue LIST"); cmd != nil {
			t.Fatal("queue list unexpectedly returned asynchronous work")
		}
		for _, item := range app.chat.items {
			if system, ok := item.(*SystemMessage); ok &&
				strings.Contains(system.content, "No queued input") {
				return
			}
		}
		t.Fatalf("normalized queue command did not use local projection: %#v", app.chat.items)
	})

	t.Run("search", func(t *testing.T) {
		app := New(Config{})
		if cmd := app.sendSlashCommand("/SEARCH term"); cmd != nil {
			t.Fatal("search unexpectedly returned asynchronous work")
		}
		if app.state != StateSearch || !app.search.Visible() {
			t.Fatalf("normalized search state = %v visible=%v", app.state, app.search.Visible())
		}
	})
}

func TestSnipBoundaryRendersLightweightHistoryMarker(t *testing.T) {
	app := New(Config{})
	app.handleEngineEvent(engine.QueryEvent{
		Type: engine.EventCompactBoundary,
		CompactBoundaryMessage: &schema.Message{
			Role: schema.System,
			Extra: map[string]any{
				"subtype":      "snip_boundary",
				"tokens_freed": 1250,
			},
		},
	})

	if len(app.chat.items) != 1 {
		t.Fatalf("expected one history snip marker, got %d items", len(app.chat.items))
	}
	marker, ok := app.chat.items[0].(*CompactBoundaryMessage)
	if !ok {
		t.Fatalf("expected compact boundary marker, got %T", app.chat.items[0])
	}
	if marker.stats != "History snipped, ~1.2k tokens freed" {
		t.Fatalf("unexpected history snip marker: %q", marker.stats)
	}
}

func TestResumeCommandReloadsEngineAndVisibleTranscript(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "transcripts")
	const oldSessionID = "11111111-1111-4111-8111-111111111111"
	const newSessionID = "22222222-2222-4222-8222-222222222222"
	recorder := transcript.NewRecorder(oldSessionID, transcriptDir)
	want := []*schema.Message{
		{Role: schema.User, Content: "remember this"},
		{Role: schema.Assistant, Content: "restored answer"},
	}
	if err := recorder.ReplaceWithReplacements(want, nil); err != nil {
		t.Fatal(err)
		return
	}
	writeTUIProjectGraphRootMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: oldSessionID,
		ThreadID:  oldSessionID,
		CWD:       dir,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
		return
	}

	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID: newSessionID, TranscriptDir: transcriptDir, CWD: dir,
	})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng})
	runSlashCommandForTest(t, app, "/resume "+oldSessionID)

	if eng.SessionID() != oldSessionID {
		t.Fatalf("resumed session ID = %q", eng.SessionID())
	}
	if eng.GetTranscript().SessionID != oldSessionID {
		t.Fatalf("transcript recorder still targets %q", eng.GetTranscript().SessionID)
	}
	if len(app.chat.items) < 3 {
		t.Fatalf("visible transcript was not restored: %#v", app.chat.items)
	}
	if user, ok := app.chat.items[0].(*UserMessage); !ok || user.content != "remember this" {
		t.Fatalf("first restored item is incorrect: %#v", app.chat.items[0])
	}
}
