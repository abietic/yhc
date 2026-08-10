package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
)

func TestSessionServiceQueriesRenamesAndExportsPersistedSessions(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	fixed := time.Date(2026, 7, 20, 6, 7, 8, 0, time.UTC)
	writeServiceSession(t, transcriptDir, "current", "current prompt")
	writeServiceSession(t, transcriptDir, "saved", "needle plan discussion")

	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "current",
		CWD:           root,
		TranscriptDir: transcriptDir,
		Clock:         func() time.Time { return fixed },
	})
	t.Cleanup(eng.Close)
	service := eng.SessionService()
	if service == nil {
		t.Fatal("session service is nil")
	}

	page, err := service.Query(t.Context(), session.SessionQuery{
		Scope:  session.SessionScopeCWD,
		Limit:  10,
		Filter: session.ListFilter{Search: "needle"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].SessionID != "saved" {
		t.Fatalf("search page = %#v", page)
	}

	renamed, err := service.Rename(t.Context(), "saved", "release plan")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.SessionID != "saved" || renamed.Name != "release plan" {
		t.Fatalf("rename result = %#v", renamed)
	}
	loaded, err := transcript.NewRecorder("saved", transcriptDir).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if countTranscriptMetadata(loaded, "customTitle", "release plan") != 1 {
		t.Fatalf("custom title metadata = %#v", loaded.Metadata)
	}

	exported, err := service.Export(t.Context(), "saved", "saved.txt")
	if err != nil {
		t.Fatal(err)
	}
	if exported.Path != filepath.Join(root, "saved.md") || exported.MessageCount != 2 {
		t.Fatalf("export result = %#v", exported)
	}
	content, err := os.ReadFile(exported.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "needle plan discussion") {
		t.Fatalf("export content = %q", content)
	}
	if err := os.WriteFile(exported.Path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	overwritten, err := service.Export(t.Context(), "saved", exported.Path)
	if err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(overwritten.Path)
	if err != nil || strings.Contains(string(content), "stale") {
		t.Fatalf("atomic overwrite content = %q, err=%v", content, err)
	}
	temps, err := filepath.Glob(filepath.Join(root, ".yhc-export-*.tmp"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("export temp files = %#v, err=%v", temps, err)
	}
	defaultExport, err := service.Export(t.Context(), "saved", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "yhc-session-saved-20260720-060708.md"); defaultExport.Path != want {
		t.Fatalf("default export path = %q, want %q", defaultExport.Path, want)
	}
}

func TestNewSessionsWriteOnlyYHCTranscriptAndCatalogRoots(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	var err error
	project, err = filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	canonicalCatalog := filepath.Join(home, ".yhc", "session-roots.json")
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "new-session",
		CWD:                project,
		SessionCatalogPath: canonicalCatalog,
	})
	t.Cleanup(eng.Close)

	if got, want := eng.GetTranscriptDir(), filepath.Join(project, ".yhc", "transcripts"); got != want {
		t.Fatalf("new session transcript directory = %q, want %q", got, want)
	}
	if _, err := eng.SessionService().Query(t.Context(), session.SessionQuery{Scope: session.SessionScopeCWD, Limit: 10}); err != nil {
		t.Fatal(err)
	}
	roots, err := session.LoadSessionRoots(canonicalCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0].CWD != project || roots[0].TranscriptDir != filepath.Join(project, ".yhc", "transcripts") {
		t.Fatalf("canonical catalog roots = %#v", roots)
	}
	for _, path := range []string{
		filepath.Join(project, ".eino-agent", "transcripts"),
		filepath.Join(home, ".eino-agent", "session-roots.json"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("new session wrote legacy path %q: %v", path, err)
		}
	}
}

func TestEmptyCWDUsesIsolatedTestTranscriptResolver(t *testing.T) {
	eng := NewQueryEngine(QueryEngineConfig{SessionID: "empty-cwd-isolation"})
	t.Cleanup(eng.Close)
	if got := eng.GetTranscriptDir(); !filepath.IsAbs(got) || filepath.Base(got) != "transcripts" {
		t.Fatalf("isolated empty-CWD transcript directory = %q", got)
	}
}

func TestProductionEmptyCWDTranscriptDefaultRemainsCanonicalRelative(t *testing.T) {
	if got, want := defaultEmptyCWDTranscriptDir(), filepath.Join(".yhc", "transcripts"); got != want {
		t.Fatalf("default empty-CWD transcript directory = %q, want %q", got, want)
	}
}

func TestLegacySessionMutationRequiresImport(t *testing.T) {
	project := t.TempDir()
	canonicalDir := filepath.Join(project, ".yhc", "transcripts")
	legacyDir := filepath.Join(project, ".eino-agent", "transcripts")
	canonicalCatalog := filepath.Join(t.TempDir(), "canonical.json")
	legacyCatalog := filepath.Join(t.TempDir(), "legacy.json")
	writeServiceSession(t, legacyDir, "legacy", "legacy prompt")
	if err := session.RegisterSessionRoot(legacyCatalog, project, legacyDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDir, "legacy.jsonl")
	legacyWorkBoardPath := filepath.Join(legacyDir, "legacy.workboard-v2.json")
	if err := os.WriteFile(legacyWorkBoardPath, []byte("legacy-workboard-sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyTranscriptBefore, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyWorkBoardBefore, err := os.ReadFile(legacyWorkBoardPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyCatalogBefore, err := os.ReadFile(legacyCatalog)
	if err != nil {
		t.Fatal(err)
	}

	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:                "current",
		CWD:                      project,
		TranscriptDir:            canonicalDir,
		SessionCatalogPath:       canonicalCatalog,
		LegacySessionCatalogPath: legacyCatalog,
	})
	t.Cleanup(eng.Close)
	service := eng.SessionService()
	info, err := service.Resolve(t.Context(), "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if !info.ReadOnly || !info.NeedsImport {
		t.Fatalf("resolved legacy row = %#v", info)
	}
	forged := info
	forged.ReadOnly = false
	forged.NeedsImport = false

	for name, invoke := range map[string]func() error{
		"resume":      func() error { _, err := service.Resume(t.Context(), "legacy"); return err },
		"resume info": func() error { _, err := service.ResumeInfo(t.Context(), forged); return err },
		"rename":      func() error { _, err := service.Rename(t.Context(), "legacy", "blocked"); return err },
		"export": func() error {
			_, err := service.Export(t.Context(), "legacy", filepath.Join(project, "legacy.md"))
			return err
		},
		"fork info": func() error { _, _, err := service.ForkInfo(t.Context(), forged, "blocked"); return err },
		"delete":    func() error { _, err := service.Delete(t.Context(), "legacy"); return err },
		"recover workboard": func() error {
			_, err := service.RecoverWorkBoard(t.Context(), SessionWorkBoardRecoveryRequest{
				SessionID: "legacy", BoardID: "legacy-board", Revision: 1, AcknowledgeDataLoss: true,
			})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := invoke(); !errors.Is(err, session.ErrLegacySessionImportRequired) {
				t.Fatalf("%s error = %v, want ErrLegacySessionImportRequired", name, err)
			}
		})
	}
	legacyTranscriptAfter, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(legacyTranscriptAfter) != string(legacyTranscriptBefore) {
		t.Fatal("legacy transcript changed before import")
	}
	legacyWorkBoardAfter, err := os.ReadFile(legacyWorkBoardPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(legacyWorkBoardAfter) != string(legacyWorkBoardBefore) {
		t.Fatal("legacy WorkBoard changed before import")
	}
	legacyCatalogAfter, err := os.ReadFile(legacyCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if string(legacyCatalogAfter) != string(legacyCatalogBefore) {
		t.Fatal("legacy catalog changed before import")
	}
	if _, err := os.Stat(filepath.Join(project, "legacy.md")); !os.IsNotExist(err) {
		t.Fatalf("legacy export was created: %v", err)
	}
}

func TestInteractiveResumeImportsOnlyAfterConfirmation(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	legacyDir := filepath.Join(project, ".eino-agent", "transcripts")
	canonicalDir := filepath.Join(project, ".yhc", "transcripts")
	legacyCatalog := filepath.Join(home, ".eino-agent", "session-roots.json")
	canonicalCatalog := filepath.Join(home, ".yhc", "session-roots.json")
	writeServiceSession(t, legacyDir, "legacy-confirm", "legacy confirmation prompt")
	if err := session.RegisterSessionRoot(legacyCatalog, project, legacyDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDir, "legacy-confirm.jsonl")
	legacyBefore, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}

	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:                "current",
		CWD:                      project,
		TranscriptDir:            canonicalDir,
		SessionCatalogPath:       canonicalCatalog,
		LegacySessionCatalogPath: legacyCatalog,
	})
	t.Cleanup(eng.Close)
	info, err := eng.SessionService().Resolve(t.Context(), "legacy-confirm")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SessionService().ImportLegacyAndResumeInfo(
		t.Context(), info, false,
	); !errors.Is(err, session.ErrSessionImportAttestationRequired) {
		t.Fatalf("unconfirmed import error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(canonicalDir, "legacy-confirm.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unconfirmed import created canonical transcript: %v", err)
	}

	resumed, err := eng.SessionService().ImportLegacyAndResumeInfo(
		t.Context(), info, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SessionID != "legacy-confirm" || eng.SessionID() != "legacy-confirm" {
		t.Fatalf("resumed=%#v engine=%q", resumed, eng.SessionID())
	}
	if len(resumed.Messages) != 2 || resumed.Messages[0].Content != "legacy confirmation prompt" {
		t.Fatalf("resumed messages = %#v", resumed.Messages)
	}
	canonical, err := os.ReadFile(filepath.Join(canonicalDir, "legacy-confirm.jsonl"))
	if err != nil || !bytes.Contains(canonical, []byte("legacy confirmation prompt")) {
		t.Fatalf("canonical transcript missing imported conversation: %v", err)
	}
	legacyAfter, err := os.ReadFile(legacyPath)
	if err != nil || string(legacyAfter) != string(legacyBefore) {
		t.Fatalf("legacy transcript changed: %v", err)
	}
}

func TestSessionServiceResumeInfoRejectsTamperedImportedBundle(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	legacyDir := filepath.Join(project, ".eino-agent", "transcripts")
	canonicalDir := filepath.Join(project, ".yhc", "transcripts")
	legacyCatalog := filepath.Join(home, ".eino-agent", "session-roots.json")
	canonicalCatalog := filepath.Join(home, ".yhc", "session-roots.json")
	writeServiceSession(t, legacyDir, "tampered-import", "legacy remains immutable")
	if err := session.RegisterSessionRoot(legacyCatalog, project, legacyDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDir, "tampered-import.jsonl")
	legacyBefore, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	userRoots, err := session.DefaultSessionImportUserRoots()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.ImportSessionForResume(t.Context(), session.ImportRequest{
		Target: session.LegacySessionTarget{
			SessionID:     "tampered-import",
			CWD:           project,
			TranscriptDir: legacyDir,
			ReadOnly:      true,
			NeedsImport:   true,
		},
		UserRoots:            userRoots,
		ConfirmLegacyStopped: true,
	}); err != nil {
		t.Fatal(err)
	}
	canonicalPath := filepath.Join(canonicalDir, "tampered-import.jsonl")
	if err := os.Chmod(canonicalPath, 0o644); err != nil {
		t.Fatal(err)
	}

	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:                "current",
		CWD:                      project,
		TranscriptDir:            canonicalDir,
		SessionCatalogPath:       canonicalCatalog,
		LegacySessionCatalogPath: legacyCatalog,
	})
	t.Cleanup(eng.Close)
	page, err := eng.SessionService().Query(t.Context(), session.SessionQuery{
		Scope: session.SessionScopeCWD,
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	var selected session.SessionInfo
	for _, info := range page.Sessions {
		if info.SessionID == "tampered-import" {
			selected = info
			break
		}
	}
	if selected.SessionID == "" {
		t.Fatalf("tampered imported row missing from %#v", page.Sessions)
	}
	if _, err := eng.SessionService().ResumeInfo(t.Context(), selected); !errors.Is(err, session.ErrSessionImportUnsafe) {
		t.Fatalf("tampered imported resume error = %v", err)
	}
	if eng.SessionID() != "current" {
		t.Fatalf("tampered imported resume changed active session to %q", eng.SessionID())
	}
	legacyAfter, err := os.ReadFile(legacyPath)
	if err != nil || string(legacyAfter) != string(legacyBefore) {
		t.Fatalf("tampered imported resume changed legacy transcript: %v", err)
	}
}

func TestSessionServiceMissingTargetFailsWithoutCreatingTranscript(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	writeServiceSession(t, transcriptDir, "current", "current prompt")
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "current",
		CWD:           root,
		TranscriptDir: transcriptDir,
	})
	t.Cleanup(eng.Close)

	if _, err := eng.SessionService().Rename(t.Context(), "missing", "name"); err == nil {
		t.Fatal("rename accepted a missing session")
	}
	if _, err := os.Stat(filepath.Join(transcriptDir, "missing.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("missing rename created transcript: %v", err)
	}
	if _, err := eng.SessionService().Export(t.Context(), "missing", "missing.md"); err == nil {
		t.Fatal("export accepted a missing session")
	}
	if _, err := os.Stat(filepath.Join(root, "missing.md")); !os.IsNotExist(err) {
		t.Fatalf("failed export created output: %v", err)
	}
	blocked := filepath.Join(root, "blocked.md")
	if err := os.Mkdir(blocked, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.SessionService().Export(t.Context(), "", blocked); err == nil {
		t.Fatal("export replaced a directory target")
	}
	if info, err := os.Stat(blocked); err != nil || !info.IsDir() {
		t.Fatalf("failed export changed directory target: info=%#v err=%v", info, err)
	}
}

func TestSessionServiceCancellationPrecedesQueryAndWorkspaceMutation(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	writeServiceSession(t, transcriptDir, "current", "prompt")
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:     "current",
		CWD:           root,
		TranscriptDir: transcriptDir,
	})
	t.Cleanup(eng.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := eng.SessionService().Query(ctx, session.SessionQuery{}); err == nil {
		t.Fatal("query ignored cancellation")
	}
	if _, err := eng.SessionService().Rename(ctx, "", "new name"); err == nil {
		t.Fatal("rename ignored cancellation")
	}
	if _, err := eng.SessionService().Export(ctx, "", "cancelled.md"); err == nil {
		t.Fatal("export ignored cancellation")
	}
	if _, err := eng.SessionService().CreateFork(ctx, SessionForkRequest{
		OperationID: "cancelled-fork",
	}); err == nil {
		t.Fatal("fork ignored cancellation")
	}
	if _, err := os.Stat(filepath.Join(root, "cancelled.md")); !os.IsNotExist(err) {
		t.Fatalf("canceled export created output: %v", err)
	}
	children, err := filepath.Glob(filepath.Join(transcriptDir, "*.jsonl"))
	if err != nil || len(children) != 1 {
		t.Fatalf("canceled fork transcripts = %#v, err=%v", children, err)
	}
}

func TestSessionServiceCreateForkCommitsOneRestartableChild(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	sourceID := "fork-service-source"
	source := transcript.NewRecorder(sourceID, transcriptDir)
	messages := []*schema.Message{
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "answer"},
	}
	if err := source.RecordLifecycleBoundary(
		transcript.LifecycleCheckpoint,
		messages,
		[]transcript.Replacement{{ToolUseID: "tool-1", Replacement: "preview"}},
		map[string]transcript.FileState{
			filepath.Join(root, "source.go"): {
				Path:    filepath.Join(root, "source.go"),
				WasRead: true,
			},
		},
	); err != nil {
		t.Fatal(err)
	}
	writeProjectGraphRootTestMetadata(t, source, &session.SessionMetadataFull{
		SessionID: sourceID,
		ThreadID:  "source-thread",
		AgentID:   "source-agent",
	})
	sourceBefore, err := os.ReadFile(source.Path())
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 7, 20, 8, 9, 10, 0, time.UTC)
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:      sourceID,
		ThreadID:       "source-thread",
		AgentID:        "source-agent",
		CWD:            root,
		TranscriptDir:  transcriptDir,
		Model:          "fork-model",
		PermissionMode: permission.ModeAcceptEdits,
		AdditionalDirs: []string{filepath.Join(root, "extra")},
		WorktreePath:   filepath.Join(root, "worktree"),
		WorktreeBranch: "source-worktree",
		Clock:          func() time.Time { return fixed },
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages(messages)
	eng.planMu.Lock()
	eng.planState = PlanState{
		Phase:            PlanPhaseActive,
		PlanFileIdentity: filepath.Join(root, "plan.md"),
		ReturnMode:       permission.ModeAcceptEdits,
		Revision:         7,
	}
	eng.planMu.Unlock()

	created, err := eng.SessionService().CreateFork(t.Context(), SessionForkRequest{
		BranchName:  "review",
		OperationID: "operation-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	retried, err := eng.SessionService().CreateFork(t.Context(), SessionForkRequest{
		BranchName:  "ignored-on-retry",
		OperationID: "operation-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if retried.Branch.NewSessionID != created.Branch.NewSessionID {
		t.Fatalf("retry created another child: first=%#v retry=%#v", created, retried)
	}
	transcripts, err := filepath.Glob(filepath.Join(transcriptDir, "*.jsonl"))
	if err != nil || len(transcripts) != 2 {
		t.Fatalf("fork transcripts = %#v, err=%v", transcripts, err)
	}
	child, err := transcript.NewRecorder(
		created.Branch.NewSessionID,
		transcriptDir,
	).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	metadata := session.ReadSessionMetadataFull(child)
	if metadata == nil ||
		metadata.ParentSessionID != sourceID ||
		metadata.ParentThreadID != "source-thread" ||
		metadata.ParentAgentID != "source-agent" ||
		metadata.PermissionMode != string(permission.ModeAcceptEdits) ||
		metadata.QueryKernelVersion == "" ||
		metadata.PlanState == nil ||
		metadata.PlanState.Phase != string(PlanPhaseActive) ||
		metadata.PlanState.PlanFileIdentity != tools.GetPlanFilePath(
			created.Branch.NewSessionID,
			"",
		) ||
		len(metadata.AdditionalDirs) != 1 ||
		metadata.WorktreePath != "" ||
		metadata.WorktreeBranch != "" {
		t.Fatalf("fork metadata = %#v", metadata)
	}
	if len(child.Replacements) != 1 || len(child.FileSnapshots) != 1 {
		t.Fatalf("fork auxiliary state = %#v", child)
	}
	sourceAfter, err := os.ReadFile(source.Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceAfter) != string(sourceBefore) {
		t.Fatal("fork mutated source transcript")
	}
	resumed, err := session.ResumeSession(t.Context(), session.ResumeOptions{
		SessionID:        created.Branch.NewSessionID,
		SessionDir:       transcriptDir,
		ProjectDir:       root,
		ValidateMessages: true,
	})
	if err != nil || len(resumed.Messages) != len(messages) {
		t.Fatalf("restart fork = %#v, err=%v", resumed, err)
	}
}

func TestP1310SessionServiceRejectsUnsupportedSelectedForkBeforePersistence(
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
				QueryKernelVersion: queryKernelVersionLegacy,
			},
		},
		{
			name: "unknown version",
			metadata: &session.SessionMetadataFull{
				QueryKernelVersion: "project_graph/v2",
				QueryKernelStage:   string(queryKernelStageFull),
			},
		},
		{
			name: "invalid stage",
			metadata: &session.SessionMetadataFull{
				QueryKernelVersion: queryKernelVersionProjectGraph,
				QueryKernelStage:   "future-stage",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			transcriptDir := filepath.Join(root, "transcripts")
			writeServiceSession(t, transcriptDir, "current", "current")

			sourceID := "selected-source"
			source := transcript.NewRecorder(sourceID, transcriptDir)
			if err := source.RecordMessages([]*schema.Message{
				{Role: schema.User, Content: "question"},
				{Role: schema.Assistant, Content: "answer"},
			}); err != nil {
				t.Fatal(err)
			}
			if test.metadata != nil {
				test.metadata.SessionID = sourceID
				test.metadata.ThreadID = sourceID
				if err := session.WriteSessionMetadata(
					source,
					test.metadata,
				); err != nil {
					t.Fatal(err)
				}
			}
			if err := source.Close(); err != nil {
				t.Fatal(err)
			}
			sourceBefore, err := os.ReadFile(source.Path())
			if err != nil {
				t.Fatal(err)
			}
			filesBefore, err := filepath.Glob(
				filepath.Join(transcriptDir, "*.jsonl"),
			)
			if err != nil {
				t.Fatal(err)
			}

			eng := NewQueryEngine(QueryEngineConfig{
				SessionID:     "current",
				CWD:           root,
				TranscriptDir: transcriptDir,
			})
			t.Cleanup(eng.Close)
			_, err = eng.SessionService().CreateFork(
				t.Context(),
				SessionForkRequest{
					Source: &session.SessionInfo{
						SessionID:      sourceID,
						CWD:            root,
						TranscriptDir:  transcriptDir,
						TranscriptPath: source.Path(),
					},
					OperationID: "rejected-selected-source",
				},
			)
			if err == nil ||
				!strings.Contains(err.Error(), "query kernel") {
				t.Fatalf("selected source error = %v", err)
			}

			sourceAfter, err := os.ReadFile(source.Path())
			if err != nil {
				t.Fatal(err)
			}
			if string(sourceAfter) != string(sourceBefore) {
				t.Fatal("rejected selected fork mutated its source transcript")
			}
			filesAfter, err := filepath.Glob(
				filepath.Join(transcriptDir, "*.jsonl"),
			)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(filesAfter, "\x00") !=
				strings.Join(filesBefore, "\x00") {
				t.Fatalf(
					"rejected selected fork created transcript: before=%#v after=%#v",
					filesBefore,
					filesAfter,
				)
			}
		})
	}
}

func TestSessionServiceForkActivationFailureCompensatesChild(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	sourceID := "fork-rollback-source"
	writeServiceSession(t, transcriptDir, sourceID, "question")
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: sourceID, CWD: root, TranscriptDir: transcriptDir,
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "answer"},
	})
	service := eng.SessionService()
	service.activateForkFn = func(
		context.Context,
		session.ResumeOptions,
		string,
	) (*session.ResumedSession, error) {
		return nil, errors.New("injected activation failure")
	}

	_, created, err := service.forkAndActivateForTurn(
		t.Context(),
		SessionForkRequest{OperationID: "rollback-operation"},
		"",
	)
	if err == nil || created == nil {
		t.Fatalf("activation result = %#v, created=%#v", err, created)
	}
	if _, statErr := os.Stat(created.Branch.TranscriptPath); !os.IsNotExist(statErr) {
		t.Fatalf("failed activation left child transcript: %v", statErr)
	}
	if eng.SessionID() != sourceID {
		t.Fatalf("failed activation changed source identity to %q", eng.SessionID())
	}
	service.activateForkFn = nil
	resumed, retry, err := service.forkAndActivateForTurn(
		t.Context(),
		SessionForkRequest{OperationID: "rollback-operation"},
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SessionID != retry.Branch.NewSessionID ||
		eng.SessionID() != retry.Branch.NewSessionID {
		t.Fatalf("retry activation = resumed %#v fork %#v", resumed, retry)
	}
}

func TestSessionServiceDiscardForkRequiresMatchingLineage(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	sourceID := "fork-lineage-source"
	writeServiceSession(t, transcriptDir, sourceID, "question")
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: sourceID, CWD: root, TranscriptDir: transcriptDir,
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "answer"},
	})
	created, err := eng.SessionService().CreateFork(
		t.Context(),
		SessionForkRequest{OperationID: "lineage-operation"},
	)
	if err != nil {
		t.Fatal(err)
	}
	child := transcript.NewRecorder(
		created.Branch.NewSessionID,
		transcriptDir,
	)
	if err := child.RecordMetadata("parent_session_id", "foreign-parent"); err != nil {
		t.Fatal(err)
	}
	if err := child.Close(); err != nil {
		t.Fatal(err)
	}

	if err := eng.SessionService().DiscardFork(created); err == nil ||
		!strings.Contains(err.Error(), "matching child lineage") {
		t.Fatalf("lineage rollback error = %v", err)
	}
	if _, err := os.Stat(created.Branch.TranscriptPath); err != nil {
		t.Fatalf("lineage mismatch removed child: %v", err)
	}
}

func TestSessionServiceForkRejectsNonDurableActiveBoundary(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	sourceID := "fork-stale-source"
	writeServiceSession(t, transcriptDir, sourceID, "persisted question")
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: sourceID, CWD: root, TranscriptDir: transcriptDir,
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{{
		Role: schema.User, Content: "live but not durable",
	}})

	if _, err := eng.SessionService().CreateFork(
		t.Context(),
		SessionForkRequest{OperationID: "stale-boundary"},
	); err == nil || !strings.Contains(err.Error(), "durable fork boundary") {
		t.Fatalf("stale boundary error = %v", err)
	}
	transcripts, err := filepath.Glob(filepath.Join(transcriptDir, "*.jsonl"))
	if err != nil || len(transcripts) != 1 {
		t.Fatalf("stale fork transcripts = %#v, err=%v", transcripts, err)
	}
	if eng.SessionID() != sourceID || len(eng.GetMessages()) != 1 {
		t.Fatalf(
			"stale fork changed live source: session=%q messages=%#v",
			eng.SessionID(),
			eng.GetMessages(),
		)
	}
}

func TestSessionServiceForkActivationSurvivesPostCommitCancellation(t *testing.T) {
	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	sourceID := "fork-cancel-source"
	writeServiceSession(t, transcriptDir, sourceID, "question")
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID: sourceID, CWD: root, TranscriptDir: transcriptDir,
	})
	t.Cleanup(eng.Close)
	eng.SetResumedMessages([]*schema.Message{
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "answer"},
	})
	ctx, cancel := context.WithCancel(t.Context())
	service := eng.SessionService()
	service.branchFn = func(opts session.BranchOptions) (*session.BranchResult, error) {
		result, err := session.BranchSession(opts)
		cancel()
		return result, err
	}

	resumed, created, err := service.forkAndActivateForTurn(
		ctx,
		SessionForkRequest{OperationID: "post-commit-cancel"},
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil ||
		resumed.SessionID != created.Branch.NewSessionID ||
		eng.SessionID() != created.Branch.NewSessionID {
		t.Fatalf(
			"post-commit activation = ctx %v resumed %#v fork %#v engine %q",
			ctx.Err(),
			resumed,
			created,
			eng.SessionID(),
		)
	}
}

func TestSessionServiceRejectsAmbiguousIDsAcrossTranscriptRoots(t *testing.T) {
	root := t.TempDir()
	currentDir := filepath.Join(root, "current-transcripts")
	importedDir := filepath.Join(root, "imported-transcripts")
	catalog := filepath.Join(root, "session-roots.json")
	writeServiceSession(t, currentDir, "current", "current prompt")
	writeServiceSession(t, currentDir, "duplicate", "local duplicate")
	writeServiceSession(t, importedDir, "duplicate", "imported duplicate")
	if err := session.RegisterSessionRoot(catalog, root, importedDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	eng := NewQueryEngine(QueryEngineConfig{
		SessionID:          "current",
		CWD:                root,
		TranscriptDir:      currentDir,
		SessionCatalogPath: catalog,
	})
	t.Cleanup(eng.Close)

	for operation, invoke := range map[string]func() error{
		"resolve": func() error {
			_, err := eng.SessionService().Resolve(t.Context(), "duplicate")
			return err
		},
		"resume": func() error {
			_, err := eng.SessionService().Resume(t.Context(), "duplicate")
			return err
		},
		"rename": func() error {
			_, err := eng.SessionService().Rename(t.Context(), "duplicate", "blocked")
			return err
		},
		"export": func() error {
			_, err := eng.SessionService().Export(t.Context(), "duplicate", "blocked.md")
			return err
		},
	} {
		t.Run(operation, func(t *testing.T) {
			if err := invoke(); err == nil || !strings.Contains(err.Error(), "ambiguous") {
				t.Fatalf("%s error = %v", operation, err)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(root, "blocked.md")); !os.IsNotExist(err) {
		t.Fatalf("ambiguous export created output: %v", err)
	}
	for _, dir := range []string{currentDir, importedDir} {
		loaded, err := transcript.NewRecorder("duplicate", dir).LoadFull()
		if err != nil {
			t.Fatal(err)
		}
		if countTranscriptMetadata(loaded, "customTitle", "blocked") != 0 {
			t.Fatalf("ambiguous rename mutated %s", dir)
		}
	}
}

func TestSessionExportReplacementFailurePreservesExistingTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "report.md")
	if err := os.WriteFile(target, []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}
	replaceErr := errors.New("replace failed")
	err := writeSessionExportFileWithReplace(
		target,
		[]byte("next"),
		func(_, _ string) error { return replaceErr },
	)
	if !errors.Is(err, replaceErr) {
		t.Fatalf("replace error = %v", err)
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil || string(content) != "previous" {
		t.Fatalf("existing target = %q, err=%v", content, readErr)
	}
	temps, globErr := filepath.Glob(filepath.Join(root, ".yhc-export-*.tmp"))
	if globErr != nil || len(temps) != 0 {
		t.Fatalf("export temp files = %#v, err=%v", temps, globErr)
	}
}

func TestSessionExportRefusesSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform-specific privileges on Windows")
	}
	root := t.TempDir()
	destination := filepath.Join(root, "destination.md")
	target := filepath.Join(root, "report.md")
	if err := os.WriteFile(destination, []byte("previous"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(destination, target); err != nil {
		t.Fatal(err)
	}
	if err := writeSessionExportFile(target, []byte("next")); err == nil {
		t.Fatal("export accepted a symlink target")
	}
	content, err := os.ReadFile(destination)
	if err != nil || string(content) != "previous" {
		t.Fatalf("symlink destination = %q, err=%v", content, err)
	}
}

func writeServiceSession(t *testing.T, dir, id, prompt string) {
	t.Helper()
	recorder := transcript.NewRecorder(id, dir)
	if err := recorder.RecordMessages(
		[]*schema.Message{
			{Role: schema.User, Content: prompt},
			{Role: schema.Assistant, Content: "answer"},
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := session.WriteSessionMetadata(
		recorder,
		&session.SessionMetadataFull{
			SessionID:          id,
			ThreadID:           id,
			QueryKernelVersion: queryKernelVersionProjectGraph,
			QueryKernelStage:   string(queryKernelStageFull),
			CreatedAt:          time.Now().UTC(),
		},
	); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
}
