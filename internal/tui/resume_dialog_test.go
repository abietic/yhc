package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/cloudwego/eino/schema"
)

func TestResumeCommandOpensSessionPicker(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "transcripts")
	writePickerSession(t, transcriptDir, "session-older", "older migration discussion", time.Now().Add(-2*time.Hour))
	writePickerSession(t, transcriptDir, "session-newer", "newer permission work", time.Now().Add(-10*time.Minute))

	eng := engine.NewQueryEngine(engine.QueryEngineConfig{SessionID: "session-current", TranscriptDir: transcriptDir, CWD: dir})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng})
	cmd := app.sendSlashCommand("/resume")
	if cmd == nil || app.state != StateResume {
		t.Fatalf("/resume did not open picker: state=%v", app.state)
		return
	}
	loaded, ok := cmd().(resumeSessionsLoadedMsg)
	if !ok {
		t.Fatalf("unexpected picker load message")
	}
	app.Update(loaded)
	if len(app.resume.filtered) != 2 {
		t.Fatalf("picker sessions = %#v", app.resume.filtered)
	}
	if app.resume.filtered[0].SessionID != "session-newer" {
		t.Fatalf("sessions not sorted by last activity: %#v", app.resume.filtered)
	}
	view := app.resume.Overlay("", 100, 30)
	if !strings.Contains(view, "newer permission work") || !strings.Contains(view, "10m ago") {
		t.Fatalf("picker does not show summary and activity: %q", view)
	}
	resumeCmd := app.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if resumeCmd == nil {
		t.Fatal("selecting a session did not start resume")
		return
	}
	finished, ok := resumeCmd().(resumeSessionActionFinishedMsg)
	if !ok {
		t.Fatal("selection returned unexpected command")
		return
	}
	if finished.err != nil {
		t.Fatalf("picker action failed: %v", finished.err)
	}
	app.Update(finished)
	if eng.SessionID() != "session-newer" {
		t.Fatalf("picker resumed %q, want session-newer", eng.SessionID())
	}
}

func TestResumePickerFiltersAndSelectsSession(t *testing.T) {
	dialog := NewResumeDialog(defaultStyles())
	dialog.Show("session-current")
	dialog.SetSessions([]session.SessionInfo{
		{SessionID: "session-current", Summary: "current"},
		{SessionID: "session-one", Summary: "permission fixes"},
		{SessionID: "session-two", Summary: "MCP transport"},
	}, nil)

	dialog.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("mcp"))})
	if len(dialog.filtered) != 1 || dialog.filtered[0].SessionID != "session-two" {
		t.Fatalf("unexpected filtered sessions: %#v", dialog.filtered)
	}
	selection, done, _ := dialog.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !done || selection.Info.SessionID != "session-two" || selection.Mode != sessionPickerResume {
		t.Fatalf("selection = %#v, done=%v", selection, done)
	}
}

func TestResumePickerRequiresExplicitLegacyStoppedConfirmation(t *testing.T) {
	dialog := NewResumeDialog(defaultStyles())
	dialog.Show("current")
	dialog.SetSessions([]session.SessionInfo{{
		SessionID:   "legacy-session",
		Summary:     "legacy work",
		ReadOnly:    true,
		NeedsImport: true,
	}}, nil)

	selection, done, _ := dialog.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || selection.valid() {
		t.Fatalf("first enter selected legacy session: selection=%#v done=%v", selection, done)
	}
	if view := stripANSIForTest(dialog.Overlay("", 100, 30)); !strings.Contains(view, "producer is stopped") || !strings.Contains(view, "Press Y") {
		t.Fatalf("legacy confirmation not rendered: %q", view)
	}

	selection, done, _ = dialog.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if done || selection.valid() {
		t.Fatalf("confirmation cancel closed picker: selection=%#v done=%v", selection, done)
	}
	_, done, _ = dialog.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done {
		t.Fatal("second confirmation setup unexpectedly closed picker")
	}
	selection, done, _ = dialog.handleKey(tea.KeyPressMsg{Code: 'y'})
	if !done || !selection.valid() || !selection.ConfirmLegacyStopped ||
		selection.Info.SessionID != "legacy-session" {
		t.Fatalf("confirmed selection=%#v done=%v", selection, done)
	}
}

func TestResumePickerMergesMovingPagesAndPreservesSelection(t *testing.T) {
	dialog := NewResumeDialog(defaultStyles())
	dialog.Show("current")
	_, generation := dialog.beginPage(true)
	dialog.SetPage(&session.SessionPage{
		Sessions: []session.SessionInfo{
			{SessionID: "one", TranscriptPath: "/tmp/one.jsonl", Summary: "one"},
			{SessionID: "two", TranscriptPath: "/tmp/two.jsonl", Summary: "two"},
		},
		NextCursor: "next",
		HasMore:    true,
	}, generation, true, nil)
	dialog.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	if dialog.selectionKey != "/tmp/two.jsonl" {
		t.Fatalf("selection key = %q", dialog.selectionKey)
	}

	dialog.SetPage(&session.SessionPage{Sessions: []session.SessionInfo{
		{SessionID: "two", TranscriptPath: "/tmp/two.jsonl", Summary: "two updated"},
		{SessionID: "three", TranscriptPath: "/tmp/three.jsonl", Summary: "three"},
	}}, generation, false, nil)
	if len(dialog.sessions) != 3 {
		t.Fatalf("merged sessions = %#v", dialog.sessions)
	}
	if dialog.sessions[1].Summary != "two updated" {
		t.Fatalf("moving row was not updated: %#v", dialog.sessions[1])
	}
	if dialog.selected != 1 || dialog.filtered[dialog.selected].SessionID != "two" {
		t.Fatalf("selection moved: selected=%d rows=%#v", dialog.selected, dialog.filtered)
	}
}

func TestResumePickerRapidSearchRejectsStaleGeneration(t *testing.T) {
	dialog := NewResumeDialog(defaultStyles())
	dialog.Show("current")
	_, generation := dialog.beginPage(true)
	dialog.SetPage(&session.SessionPage{}, generation, true, nil)

	_, _, firstCommand := dialog.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("a"))})
	_, _, secondCommand := dialog.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune("b"))})
	if firstCommand == nil || secondCommand == nil || dialog.query != "ab" {
		t.Fatalf("rapid query = %q, commands nil=%v/%v", dialog.query, firstCommand == nil, secondCommand == nil)
	}
	first := firstCommand().(resumeSessionPageRequestMsg)
	second := secondCommand().(resumeSessionPageRequestMsg)
	if first.generation >= second.generation || second.query.Filter.Search != "ab" {
		t.Fatalf("requests = %#v then %#v", first, second)
	}
	dialog.SetPage(&session.SessionPage{Sessions: []session.SessionInfo{{SessionID: "stale"}}}, first.generation, true, nil)
	if len(dialog.sessions) != 0 {
		t.Fatalf("stale page was applied: %#v", dialog.sessions)
	}
}

func TestResumePickerScopeAndSortProduceFreshQueries(t *testing.T) {
	dialog := NewResumeDialog(defaultStyles())
	dialog.Show("current")
	_, generation := dialog.beginPage(true)
	dialog.SetPage(&session.SessionPage{}, generation, true, nil)

	_, _, scopeCommand := dialog.handleKey(tea.KeyPressMsg{Code: tea.KeyTab})
	scopeRequest := scopeCommand().(resumeSessionPageRequestMsg)
	if scopeRequest.query.Scope != session.SessionScopeAll || !scopeRequest.reset {
		t.Fatalf("scope request = %#v", scopeRequest)
	}
	dialog.SetPage(&session.SessionPage{}, scopeRequest.generation, true, nil)
	_, _, sortCommand := dialog.handleKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	sortRequest := sortCommand().(resumeSessionPageRequestMsg)
	if sortRequest.query.Sort != session.SortOldestFirst || sortRequest.generation <= scopeRequest.generation {
		t.Fatalf("sort request = %#v", sortRequest)
	}
}

func TestResumeCommandLoadsAdditionalPageNearEnd(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "transcripts")
	base := time.Now().Add(-time.Hour)
	for index := 0; index < 30; index++ {
		id := fmt.Sprintf("session-%02d", index)
		writePickerSession(t, transcriptDir, id, "prompt "+id, base.Add(time.Duration(index)*time.Minute))
	}

	eng := engine.NewQueryEngine(engine.QueryEngineConfig{SessionID: "current", TranscriptDir: transcriptDir, CWD: dir})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng})
	loaded := app.openResumeSelector()().(resumeSessionsLoadedMsg)
	app.Update(loaded)
	if len(app.resume.sessions) != 25 || !app.resume.hasMore {
		t.Fatalf("first page = %d, hasMore=%v", len(app.resume.sessions), app.resume.hasMore)
	}
	app.resume.selected = len(app.resume.filtered) - 3
	_, _, requestCommand := app.resume.handleKey(tea.KeyPressMsg{Code: tea.KeyDown})
	batch := requestCommand().(tea.BatchMsg)
	var request resumeSessionPageRequestMsg
	for _, command := range batch {
		if candidate, ok := command().(resumeSessionPageRequestMsg); ok {
			request = candidate
			break
		}
	}
	if request.generation == 0 {
		t.Fatal("near-end navigation did not request another page")
	}
	_, loadCommand := app.Update(request)
	second := loadCommand().(resumeSessionsLoadedMsg)
	app.Update(second)
	if len(app.resume.sessions) != 30 || app.resume.hasMore {
		t.Fatalf("merged page = %d, hasMore=%v", len(app.resume.sessions), app.resume.hasMore)
	}
}

func TestResumePickerLoadsRecentPreviewLazily(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "transcripts")
	writePickerSession(t, transcriptDir, "selected", "recent question", time.Now())
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{SessionID: "current", TranscriptDir: transcriptDir, CWD: dir})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng})

	firstPage := app.openResumeSelector()().(resumeSessionsLoadedMsg)
	_, previewBatch := app.Update(firstPage)
	if previewBatch == nil {
		t.Fatal("initial page did not schedule a lazy preview")
	}
	var previewRequest resumeSessionPreviewRequestMsg
	scheduled := previewBatch()
	if request, ok := scheduled.(resumeSessionPreviewRequestMsg); ok {
		previewRequest = request
	}
	if batch, ok := scheduled.(tea.BatchMsg); ok {
		for _, command := range batch {
			if request, ok := command().(resumeSessionPreviewRequestMsg); ok {
				previewRequest = request
			}
		}
	}
	if previewRequest.key == "" {
		t.Fatal("preview request missing")
	}
	_, load := app.Update(previewRequest)
	loaded := load().(resumeSessionPreviewLoadedMsg)
	app.Update(loaded)
	view := stripANSIForTest(app.resume.Overlay("", 100, 34))
	if !strings.Contains(view, "recent question") || !strings.Contains(view, "answer") {
		t.Fatalf("lazy preview not rendered: %q", view)
	}
}

func TestResumePickerFullTranscriptSearchReturnsToPicker(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "transcripts")
	writePickerSession(t, transcriptDir, "selected", "needle transcript", time.Now())
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{SessionID: "current", TranscriptDir: transcriptDir, CWD: dir})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng})
	app.Update(tea.WindowSizeMsg{Width: 100, Height: 34})
	app.Update(app.openResumeSelector()().(resumeSessionsLoadedMsg))

	requestCommand := app.handleKey(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	request := requestCommand().(resumeSessionTranscriptRequestMsg)
	_, load := app.Update(request)
	loaded := load().(resumeSessionTranscriptLoadedMsg)
	app.Update(loaded)
	if app.state != StateExpand || app.hasDialog(StateResume) {
		t.Fatalf("transcript state=%v resumeDialog=%v", app.state, app.hasDialog(StateResume))
	}
	if text := stripANSIForTest(app.expandContent); !strings.Contains(text, "needle transcript") {
		t.Fatalf("transcript overlay = %q", text)
	}
	app.handleKey(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	for _, character := range "needle" {
		app.handleKey(tea.KeyPressMsg{Code: tea.KeyExtended, Text: string([]rune{character})})
	}
	if !app.expandSearch.Visible() || app.expandSearch.MatchCount() == 0 {
		t.Fatal("transcript search did not find content")
	}
	app.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	app.handleKey(tea.KeyPressMsg{Code: tea.KeyEsc})
	if !app.hasDialog(StateResume) || app.state != StateResume {
		t.Fatalf("picker was not restored: state=%v dialog=%v", app.state, app.hasDialog(StateResume))
	}
}

func TestResumePickerForkModeAndMetadataRendering(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "transcripts")
	writePickerSession(t, transcriptDir, "parent", "fork source", time.Now())
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{SessionID: "current", TranscriptDir: transcriptDir, CWD: dir})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng})
	app.Update(app.openResumeSelector()().(resumeSessionsLoadedMsg))
	app.resume.filtered[0].Model = "gpt-5"
	app.resume.filtered[0].Provider = "openai"
	app.resume.filtered[0].AgentID = "agent-1"
	app.resume.filtered[0].AgentRole = "reviewer"
	app.resume.filtered[0].Status = "completed"
	app.resume.filtered[0].WorktreeBranch = "agent/review"
	view := stripANSIForTest(app.resume.Overlay("", 110, 36))
	for _, expected := range []string{"openai:gpt-5", "agent reviewer", "status completed", "worktree agent/review"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("metadata %q missing from %q", expected, view)
		}
	}

	app.handleKey(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	if app.resume.mode != sessionPickerFork {
		t.Fatalf("picker mode = %q", app.resume.mode)
	}
	action := app.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	finished := action().(resumeSessionActionFinishedMsg)
	if finished.err != nil || finished.forkedID == "" || finished.forkedID == "parent" {
		t.Fatalf("fork action = %#v", finished)
	}
	app.Update(finished)
	if eng.SessionID() != finished.forkedID {
		t.Fatalf("engine session = %q, want %q", eng.SessionID(), finished.forkedID)
	}
}

func TestResumePickerForkFailureKeepsSourceAndPickerActive(t *testing.T) {
	dir := t.TempDir()
	transcriptDir := filepath.Join(dir, "transcripts")
	writePickerSession(t, transcriptDir, "parent", "fork source", time.Now())
	eng := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID: "current", TranscriptDir: transcriptDir, CWD: dir,
	})
	t.Cleanup(eng.Close)
	app := New(Config{Engine: eng})
	app.Update(app.openResumeSelector()().(resumeSessionsLoadedMsg))
	app.handleKey(tea.KeyPressMsg{Code: 'f', Mod: tea.ModCtrl})
	if err := os.Remove(filepath.Join(transcriptDir, "parent.jsonl")); err != nil {
		t.Fatal(err)
	}

	action := app.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	finished := action().(resumeSessionActionFinishedMsg)
	if finished.err == nil || finished.forkedID != "" {
		t.Fatalf("failed fork action = %#v", finished)
	}
	app.Update(finished)
	if eng.SessionID() != "current" ||
		!app.hasDialog(StateResume) ||
		!app.resume.visible ||
		app.resume.err == "" {
		t.Fatalf(
			"failed fork state = session %q dialog %v visible %v err %q",
			eng.SessionID(),
			app.hasDialog(StateResume),
			app.resume.visible,
			app.resume.err,
		)
	}
}

func TestP1310ResumePickerRejectsUnsupportedSessionWithoutRemapOrRewrite(
	t *testing.T,
) {
	tests := []struct {
		name     string
		metadata *session.SessionMetadataFull
	}{
		{name: "unpinned"},
		{
			name: "retired legacy",
			metadata: &session.SessionMetadataFull{
				QueryKernelVersion: "legacy/v1",
			},
		},
		{
			name: "unknown version",
			metadata: &session.SessionMetadataFull{
				QueryKernelVersion: "project_graph/v2",
				QueryKernelStage:   "full",
			},
		},
		{
			name: "invalid stage",
			metadata: &session.SessionMetadataFull{
				QueryKernelVersion: "project_graph/v1",
				QueryKernelStage:   "future-stage",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			transcriptDir := filepath.Join(dir, "transcripts")
			const sourceSessionID = "unsupported-tui-session"
			recorder := transcript.NewRecorder(sourceSessionID, transcriptDir)
			if err := recorder.Replace([]*schema.Message{
				{Role: schema.User, Content: "old prompt"},
				{Role: schema.Assistant, Content: "old answer"},
			}); err != nil {
				t.Fatal(err)
			}
			if test.metadata != nil {
				now := time.Now().UTC()
				test.metadata.SessionID = sourceSessionID
				test.metadata.ThreadID = sourceSessionID
				test.metadata.CWD = dir
				test.metadata.CreatedAt = now
				test.metadata.UpdatedAt = now
				if err := session.WriteSessionMetadata(
					recorder,
					test.metadata,
				); err != nil {
					t.Fatal(err)
				}
			}
			if err := recorder.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(recorder.Path())
			if err != nil {
				t.Fatal(err)
			}

			eng := engine.NewQueryEngine(engine.QueryEngineConfig{
				SessionID:     "current",
				TranscriptDir: transcriptDir,
				CWD:           dir,
			})
			t.Cleanup(eng.Close)
			app := New(Config{Engine: eng})
			app.Update(app.openResumeSelector()().(resumeSessionsLoadedMsg))
			if len(app.resume.filtered) != 1 ||
				app.resume.filtered[0].SessionID != sourceSessionID {
				t.Fatalf(
					"resume rows = %#v, want unsupported source",
					app.resume.filtered,
				)
			}

			action := app.handleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
			if action == nil {
				t.Fatal("unsupported session selection did not return an action")
			}
			finished, ok := action().(resumeSessionActionFinishedMsg)
			if !ok {
				t.Fatal("unsupported session selection returned unexpected message")
			}
			if finished.err == nil ||
				!strings.Contains(finished.err.Error(), "query kernel") {
				t.Fatalf("resume error = %v", finished.err)
			}
			if eng.SessionID() != "current" {
				t.Fatalf(
					"rejected resume activated session %q",
					eng.SessionID(),
				)
			}
			after, err := os.ReadFile(recorder.Path())
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("rejected resume rewrote source transcript")
			}

			app.Update(finished)
			if !app.hasDialog(StateResume) ||
				!app.resume.visible ||
				app.resume.err == "" {
				t.Fatalf(
					"rejected resume did not keep picker active: dialog=%v visible=%v err=%q",
					app.hasDialog(StateResume),
					app.resume.visible,
					app.resume.err,
				)
			}
		})
	}
}

func writePickerSession(t *testing.T, dir, id, prompt string, modified time.Time) {
	t.Helper()
	recorder := transcript.NewRecorder(id, dir)
	if err := recorder.ReplaceWithReplacements([]*schema.Message{
		{Role: schema.User, Content: prompt},
		{Role: schema.Assistant, Content: "answer"},
	}, nil); err != nil {
		t.Fatal(err)
		return
	}
	writeTUIProjectGraphRootMetadata(t, recorder, &session.SessionMetadataFull{
		SessionID: id,
		ThreadID:  id,
		CreatedAt: modified,
		UpdatedAt: modified,
	})
	if err := recorder.Flush(); err != nil {
		t.Fatal(err)
		return
	}
	if err := os.Chtimes(recorder.Path(), modified, modified); err != nil {
		t.Fatal(err)
		return
	}
}

func writeTUIProjectGraphRootMetadata(
	t *testing.T,
	recorder *transcript.Recorder,
	metadata *session.SessionMetadataFull,
) {
	t.Helper()
	metadata.QueryKernelVersion = "project_graph/v1"
	metadata.QueryKernelStage = "full"
	if err := session.WriteSessionMetadata(recorder, metadata); err != nil {
		t.Fatal(err)
	}
}
