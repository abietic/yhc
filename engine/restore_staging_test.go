package engine

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/containment"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/skills"
	"github.com/abietic/yhc/tools"
)

func TestP511RestoreStagingRebindsOwnedGuestBeforeActivation(t *testing.T) {
	hostRoot := t.TempDir()
	restoredRoot := filepath.Join(hostRoot, "restored")
	if err := os.Mkdir(restoredRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptDir := t.TempDir()
	const sessionID = "p511-contained-restore"
	writeP234aRestoreSource(t, transcriptDir, sessionID, restoredRoot)
	selection, err := NewSandboxSelection(
		containment.ProfileWorkspaceWrite,
		containment.SelectionDefault,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	config := p234aEngineConfig(t, transcriptDir, "p511-host")
	config.CWD = hostRoot
	config.SandboxSelection = selection
	eng := NewRestoreStagingQueryEngine(config)
	initial := eng.ExecutionBindingMatrix()
	if initial == nil {
		t.Fatal("initial execution bindings are unavailable")
	}

	if _, err := eng.ResumeSession(t.Context(), sessionID); err != nil {
		t.Fatalf("staged resume: %v", err)
	}
	t.Cleanup(func() { _ = eng.AbortRestoreStaging() })
	canonicalRoot, err := canonicalExistingDirectory(restoredRoot)
	if err != nil {
		t.Fatal(err)
	}
	rebound := eng.ExecutionBindingMatrix()
	if got := rebound.Guest().Policy().Spec().CWD; got != canonicalRoot {
		t.Fatalf("restored Guest CWD = %q, want %q", got, canonicalRoot)
	}
	if eng.GetCWD() != restoredRoot {
		t.Fatalf("restored engine CWD = %q, want operational root %q", eng.GetCWD(), restoredRoot)
	}
	if rebound.Guest().Digest() == initial.Guest().Digest() {
		t.Fatal("restored Guest retained the constructor-root binding")
	}
	if got := eng.shellManager.GuestBindingDigest(); got != rebound.Guest().Digest() {
		t.Fatalf("restored shell binding = %q, want %q", got, rebound.Guest().Digest())
	}
	if rebound.ShellHooks().Digest() != initial.ShellHooks().Digest() ||
		rebound.StdioMCP().Digest() != initial.StdioMCP().Digest() {
		t.Fatal("restore replaced independent ambient hook or stdio MCP authority")
	}
}

func TestP511RestoreRejectsExternalShellManagerRootReplacement(t *testing.T) {
	hostRoot := t.TempDir()
	restoredRoot := filepath.Join(hostRoot, "restored")
	if err := os.Mkdir(restoredRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	transcriptDir := t.TempDir()
	const sessionID = "p511-external-shell-restore"
	writeP234aRestoreSource(t, transcriptDir, sessionID, restoredRoot)
	selection, err := NewSandboxSelection(
		containment.ProfileWorkspaceWrite,
		containment.SelectionDefault,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	config := p234aEngineConfig(t, transcriptDir, "p511-external-host")
	config.CWD = hostRoot
	config.SandboxSelection = selection
	config.ShellManager = tools.NewShellManager()
	eng := NewRestoreStagingQueryEngine(config)
	initialDigest := eng.shellManager.GuestBindingDigest()
	initialCWD := eng.GetCWD()

	if _, err := eng.ResumeSession(t.Context(), sessionID); err == nil {
		t.Fatal("restore replaced an externally owned Guest shell binding")
	}
	if got := eng.shellManager.GuestBindingDigest(); got != initialDigest {
		t.Fatalf("external shell binding changed from %q to %q", initialDigest, got)
	}
	if eng.GetCWD() != initialCWD {
		t.Fatalf("failed restore changed engine CWD to %q", eng.GetCWD())
	}
	if err := eng.AbortRestoreStaging(); err != nil {
		t.Fatal(err)
	}
}

func TestP234aRestoreStagingAbortIsIdempotentAndNonPersisting(t *testing.T) {
	t.Parallel()

	transcriptDir := t.TempDir()
	const sessionID = "p234a-abort"
	sourceCWD := t.TempDir()
	writeP234aRestoreSource(t, transcriptDir, sessionID, sourceCWD)
	path := filepath.Join(transcriptDir, sessionID+".jsonl")
	before := readP234aTranscript(t, path)

	eng := newP234aRestoreStagingEngine(t, transcriptDir, "abort-host")
	resumed, err := eng.ResumeSession(t.Context(), sessionID)
	if err != nil {
		t.Fatalf("staged resume: %v", err)
	}
	if resumed.SessionID != sessionID || eng.SessionID() != sessionID {
		t.Fatalf(
			"staged session mismatch: resumed=%q engine=%q",
			resumed.SessionID,
			eng.SessionID(),
		)
	}
	if afterResume := readP234aTranscript(t, path); !bytes.Equal(before, afterResume) {
		t.Fatal("staged resume changed durable transcript bytes")
	}
	if err := eng.AbortRestoreStaging(); err != nil {
		t.Fatalf("abort staging: %v", err)
	}
	if err := eng.AbortRestoreStaging(); err != nil {
		t.Fatalf("repeat abort staging: %v", err)
	}
	eng.Close()
	if afterAbort := readP234aTranscript(t, path); !bytes.Equal(before, afterAbort) {
		t.Fatal("staging abort or close changed durable transcript bytes")
	}
	if _, err := eng.ResumeSession(t.Context(), sessionID); !errors.Is(
		err,
		ErrRestoreStagingTransition,
	) {
		t.Fatalf("resume after abort error = %v", err)
	}
}

func TestP234aRestoreStagingCommitRestoresOrdinaryClose(t *testing.T) {
	t.Parallel()

	transcriptDir := t.TempDir()
	const sessionID = "p234a-commit"
	writeP234aRestoreSource(t, transcriptDir, sessionID, t.TempDir())
	path := filepath.Join(transcriptDir, sessionID+".jsonl")
	before := readP234aTranscript(t, path)

	eng := newP234aRestoreStagingEngine(t, transcriptDir, "commit-host")
	if _, err := eng.ResumeSession(t.Context(), sessionID); err != nil {
		t.Fatalf("staged resume: %v", err)
	}
	if err := eng.CommitRestoreStaging(); err != nil {
		t.Fatalf("commit staging: %v", err)
	}
	if err := eng.CommitRestoreStaging(); err != nil {
		t.Fatalf("repeat commit staging: %v", err)
	}
	if afterCommit := readP234aTranscript(t, path); !bytes.Equal(before, afterCommit) {
		t.Fatal("staging commit persisted before ordinary close")
	}
	if err := eng.AbortRestoreStaging(); !errors.Is(
		err,
		ErrRestoreStagingTransition,
	) {
		t.Fatalf("abort committed engine error = %v", err)
	}
	eng.Close()
	afterClose := readP234aTranscript(t, path)
	if bytes.Equal(before, afterClose) {
		t.Fatal("committed staging engine did not restore ordinary close persistence")
	}
}

func TestCommittedCanonicalResumePreservesExistingRecoveryOrdering(t *testing.T) {
	const sessionID = "canonical-admission-commit"
	sourceCWD, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	transcriptDir := filepath.Join(sourceCWD, ".yhc", "transcripts")
	writeP234aRestoreSource(t, transcriptDir, sessionID, sourceCWD)
	path := filepath.Join(transcriptDir, sessionID+".jsonl")
	before := readP234aTranscript(t, path)

	info, err := session.AdmitSessionResume(t.Context(), session.ResumeAdmissionRequest{
		SessionID: sessionID,
		CWD:       sourceCWD,
	})
	if err != nil {
		t.Fatal(err)
	}
	eng := newP234aRestoreStagingEngine(t, transcriptDir, "admission-host")
	if _, err := eng.SessionService().ResumeInfo(t.Context(), info); err != nil {
		t.Fatalf("staged canonical resume: %v", err)
	}
	if afterResume := readP234aTranscript(t, path); !bytes.Equal(before, afterResume) {
		t.Fatal("admitted canonical resume persisted before staging commit")
	}
	if err := eng.CommitRestoreStaging(); err != nil {
		t.Fatal(err)
	}
	if afterCommit := readP234aTranscript(t, path); !bytes.Equal(before, afterCommit) {
		t.Fatal("admitted canonical resume changed existing commit ordering")
	}
	eng.Close()
	if afterClose := readP234aTranscript(t, path); bytes.Equal(before, afterClose) {
		t.Fatal("committed canonical resume did not restore ordinary close persistence")
	}
}

func TestP234aRestoreStagingCommitBeforeResumeRemainsAbortable(t *testing.T) {
	t.Parallel()

	eng := newP234aRestoreStagingEngine(t, t.TempDir(), "commit-before-resume")
	if err := eng.CommitRestoreStaging(); !errors.Is(
		err,
		ErrRestoreStagingTransition,
	) {
		t.Fatalf("commit before resume error = %v", err)
	}
	if err := eng.AbortRestoreStaging(); err != nil {
		t.Fatalf("abort after rejected commit = %v", err)
	}
}

func TestP234aRestoreStagingRejectsRuntimeBeforeCommit(t *testing.T) {
	t.Parallel()

	transcriptDir := t.TempDir()
	const sessionID = "p234a-inert"
	writeP234aRestoreSource(t, transcriptDir, sessionID, t.TempDir())
	path := filepath.Join(transcriptDir, sessionID+".jsonl")
	before := readP234aTranscript(t, path)
	model := &canonicalScriptModel{}
	eng := newP234aRestoreStagingEngineWithModel(
		t,
		transcriptDir,
		"inert-host",
		model,
	)
	defer eng.AbortRestoreStaging() //nolint:errcheck
	if _, err := eng.ResumeSession(t.Context(), sessionID); err != nil {
		t.Fatalf("staged resume: %v", err)
	}

	events, terminal := eng.SubmitMessage(t.Context(), "must not run")
	for range events {
	}
	if !errors.Is(terminal.Err, ErrRestoreStagingTransition) {
		t.Fatalf("pre-commit terminal = %#v", terminal)
	}
	if model.callCount != 0 {
		t.Fatalf("pre-commit model calls = %d, want zero", model.callCount)
	}
	events, terminal = eng.SubmitMessage(t.Context(), "/new")
	for range events {
	}
	if !errors.Is(terminal.Err, ErrRestoreStagingTransition) ||
		eng.SessionID() != sessionID ||
		!bytes.Equal(before, readP234aTranscript(t, path)) {
		t.Fatalf(
			"pre-commit /new escaped staging: terminal=%#v session=%q",
			terminal,
			eng.SessionID(),
		)
	}
}

func TestP234aOrdinaryResumeAndClosePersistenceRemainUnchanged(t *testing.T) {
	t.Parallel()

	transcriptDir := t.TempDir()
	const sessionID = "p234a-ordinary"
	writeP234aRestoreSource(t, transcriptDir, sessionID, t.TempDir())
	path := filepath.Join(transcriptDir, sessionID+".jsonl")
	before := readP234aTranscript(t, path)

	eng := newP234aOrdinaryEngine(t, transcriptDir, "ordinary-host", nil)
	if err := eng.AbortRestoreStaging(); !errors.Is(
		err,
		ErrRestoreStagingTransition,
	) {
		t.Fatalf("ordinary abort error = %v", err)
	}
	if _, err := eng.ResumeSession(t.Context(), sessionID); err != nil {
		t.Fatalf("ordinary resume: %v", err)
	}
	afterResume := readP234aTranscript(t, path)
	if bytes.Equal(before, afterResume) {
		t.Fatal("ordinary resume no longer persists its target checkpoint")
	}
	eng.Close()
	afterClose := readP234aTranscript(t, path)
	if len(afterClose) <= len(afterResume) {
		t.Fatal("ordinary close no longer persists its checkpoint")
	}
}

func TestP234aSessionServiceReplaySnapshotIsReadOnly(t *testing.T) {
	t.Parallel()

	transcriptDir := t.TempDir()
	const sessionID = "p234a-service-snapshot"
	sourceCWD := t.TempDir()
	writeP234aRestoreSource(t, transcriptDir, sessionID, sourceCWD)
	path := filepath.Join(transcriptDir, sessionID+".jsonl")
	before := readP234aTranscript(t, path)
	eng := newP234aOrdinaryEngine(t, transcriptDir, "snapshot-host", nil)
	defer eng.Close()

	snapshot, err := eng.SessionService().ReplaySnapshot(t.Context(), session.SessionInfo{
		SessionID:      sessionID,
		TranscriptDir:  transcriptDir,
		TranscriptPath: path,
		CWD:            sourceCWD,
	})
	if err != nil {
		t.Fatalf("service replay snapshot: %v", err)
	}
	if snapshot.SessionID != sessionID || len(snapshot.Items()) != 2 {
		t.Fatalf("service snapshot = %#v, items=%d", snapshot, len(snapshot.Items()))
	}
	if !bytes.Equal(before, readP234aTranscript(t, path)) {
		t.Fatal("session service replay snapshot changed source transcript")
	}
}

func TestP234aRestoreStagingDefersRuntimeInputRecoveryUntilCommit(t *testing.T) {
	t.Parallel()

	for _, commit := range []bool{false, true} {
		name := "abort"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			transcriptDir := t.TempDir()
			sessionID := "p234a-runtime-input-" + name
			writeP234aRestoreSource(t, transcriptDir, sessionID, t.TempDir())
			transcriptPath := filepath.Join(transcriptDir, sessionID+".jsonl")
			ledgerPath := RuntimeInputPersistencePath(transcriptPath)
			coordinator, err := NewRuntimeInputCoordinator(
				RuntimeInputCoordinatorConfig{
					SessionID: sessionID,
					ThreadID:  sessionID,
					Path:      ledgerPath,
				},
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			item := runtimePromptTestItem(
				"recover-me",
				RuntimePriorityNext,
				false,
			)
			item.Scope = RuntimeInputScope{
				SessionID: sessionID,
				ThreadID:  sessionID,
			}
			if _, err := coordinator.Enqueue(item); err != nil {
				t.Fatal(err)
			}
			if _, found, err := coordinator.claimByID(item.ID); err != nil || !found {
				t.Fatalf("claim runtime item: found=%v err=%v", found, err)
			}
			before := readP234aTranscript(t, ledgerPath)

			eng := newP234aRestoreStagingEngine(
				t,
				transcriptDir,
				sessionID,
			)
			if afterConstruction := readP234aTranscript(t, ledgerPath); !bytes.Equal(
				before,
				afterConstruction,
			) {
				t.Fatal("staging construction persisted runtime input recovery")
			}
			if _, err := eng.ResumeSession(t.Context(), sessionID); err != nil {
				t.Fatalf("staged resume: %v", err)
			}
			if afterResume := readP234aTranscript(t, ledgerPath); !bytes.Equal(
				before,
				afterResume,
			) {
				t.Fatal("staged resume persisted runtime input recovery")
			}
			if !commit {
				if err := eng.AbortRestoreStaging(); err != nil {
					t.Fatal(err)
				}
				if afterAbort := readP234aTranscript(t, ledgerPath); !bytes.Equal(
					before,
					afterAbort,
				) {
					t.Fatal("staging abort persisted runtime input recovery")
				}
				return
			}
			if err := eng.CommitRestoreStaging(); err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			if afterCommit := readP234aTranscript(t, ledgerPath); bytes.Equal(
				before,
				afterCommit,
			) {
				t.Fatal("staging commit did not persist runtime input recovery")
			}
			items := eng.RuntimeItems()
			if len(items) != 1 ||
				items[0].ID != item.ID ||
				items[0].State != RuntimeItemPending {
				t.Fatalf("committed runtime items = %#v", items)
			}
		})
	}
}

func TestP234aRestoreStagingAbortDoesNotCreateSessionCatalog(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	transcriptDir := filepath.Join(root, "transcripts")
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const sessionID = "p234a-no-catalog"
	sourceCWD := t.TempDir()
	writeP234aRestoreSource(t, transcriptDir, sessionID, sourceCWD)
	catalogPath := filepath.Join(root, "catalog", "roots.json")
	config := p234aEngineConfig(t, transcriptDir, sessionID)
	config.SessionCatalogPath = catalogPath
	eng := NewRestoreStagingQueryEngine(config)
	if _, err := eng.ResumeSessionInfo(t.Context(), session.SessionInfo{
		SessionID:      sessionID,
		TranscriptDir:  transcriptDir,
		TranscriptPath: filepath.Join(transcriptDir, sessionID+".jsonl"),
		CWD:            sourceCWD,
	}); err != nil {
		t.Fatalf("staged resume info: %v", err)
	}
	if err := eng.AbortRestoreStaging(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(catalogPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging abort catalog stat error = %v", err)
	}
}

func TestP234aRestoreStagingDefersRuntimeActivationUntilCommit(t *testing.T) {
	t.Parallel()

	transcriptDir := t.TempDir()
	const sessionID = "p234a-deferred-activation"
	sourceCWD := t.TempDir()
	writeP234aRestoreSource(t, transcriptDir, sessionID, sourceCWD)
	hookDir := filepath.Join(sourceCWD, ".claude")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(hookDir, "hooks.json"),
		[]byte(`{
			"UserPromptSubmit": [{
				"matcher": "*",
				"hooks": [{"command": "printf deferred"}]
			}]
		}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	registry := tools.NewRegistry()
	executor := hooks.NewExecutor()
	config := p234aEngineConfig(t, transcriptDir, "activation-host")
	config.ChatModel = &canonicalScriptModel{}
	config.ToolRegistry = registry
	config.MCPManager = nil
	config.HookExecutor = executor
	config.EnableLongSessionServices = true
	eng := NewRestoreStagingQueryEngine(config)
	initialMCP := eng.mcpManager

	if eng.settingsWatcher != nil || eng.backgroundServices != nil {
		t.Fatal("staging constructor activated runtime services")
	}
	if snapshot := executor.ShellHookSnapshot(); snapshot != nil {
		t.Fatalf("staging constructor registered shell hooks: %#v", snapshot)
	}
	if _, err := eng.ResumeSession(t.Context(), sessionID); err != nil {
		t.Fatalf("staged resume: %v", err)
	}
	if eng.settingsWatcher != nil || eng.backgroundServices != nil {
		t.Fatal("staged resume activated runtime services before commit")
	}
	if eng.mcpManager != initialMCP {
		t.Fatal("staged resume replaced the inert MCP manager before commit")
	}
	if snapshot := executor.ShellHookSnapshot(); snapshot != nil {
		t.Fatalf("staged resume registered shell hooks before commit: %#v", snapshot)
	}

	if err := eng.CommitRestoreStaging(); err != nil {
		t.Fatalf("commit staging: %v", err)
	}
	defer eng.Close()
	if eng.settingsWatcher == nil || eng.backgroundServices == nil {
		t.Fatal("commit did not activate deferred runtime services")
	}
	if eng.mcpManager == initialMCP {
		t.Fatal("commit did not initialize the resumed project MCP manager")
	}
	snapshot := executor.ShellHookSnapshot()
	if snapshot == nil ||
		snapshot.Source != filepath.Join(hookDir, "hooks.json") ||
		len(snapshot.UserPromptHooks) != 1 {
		t.Fatalf("committed shell-hook snapshot = %#v", snapshot)
	}
}

func TestP234aRestoreStagingCommitAbortRace(t *testing.T) {
	const iterations = 4
	for iteration := 0; iteration < iterations; iteration++ {
		transcriptDir := t.TempDir()
		sessionID := "p234a-race-" + time.Now().UTC().Format("150405.000000000")
		writeP234aRestoreSource(t, transcriptDir, sessionID, t.TempDir())
		path := filepath.Join(transcriptDir, sessionID+".jsonl")
		before := readP234aTranscript(t, path)
		eng := newP234aRestoreStagingEngine(
			t,
			transcriptDir,
			"race-host",
		)
		if _, err := eng.ResumeSession(t.Context(), sessionID); err != nil {
			t.Fatalf("iteration %d staged resume: %v", iteration, err)
		}

		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		var commitErr error
		var abortErr error
		go func() {
			defer wait.Done()
			<-start
			commitErr = eng.CommitRestoreStaging()
		}()
		go func() {
			defer wait.Done()
			<-start
			abortErr = eng.AbortRestoreStaging()
		}()
		close(start)
		wait.Wait()

		switch {
		case commitErr == nil && errors.Is(abortErr, ErrRestoreStagingTransition):
			eng.Close()
			if bytes.Equal(before, readP234aTranscript(t, path)) {
				t.Fatalf("iteration %d committed close did not persist", iteration)
			}
		case abortErr == nil && errors.Is(commitErr, ErrRestoreStagingTransition):
			eng.Close()
			if !bytes.Equal(before, readP234aTranscript(t, path)) {
				t.Fatalf("iteration %d aborted race persisted", iteration)
			}
		default:
			t.Fatalf(
				"iteration %d race results: commit=%v abort=%v",
				iteration,
				commitErr,
				abortErr,
			)
		}
	}
}

func TestP234aRestoreStagingCommitCloseRace(t *testing.T) {
	const iterations = 4
	for iteration := 0; iteration < iterations; iteration++ {
		transcriptDir := t.TempDir()
		sessionID := "p234a-commit-close-" + time.Now().UTC().Format("150405.000000000")
		writeP234aRestoreSource(t, transcriptDir, sessionID, t.TempDir())
		path := filepath.Join(transcriptDir, sessionID+".jsonl")
		before := readP234aTranscript(t, path)
		eng := newP234aRestoreStagingEngine(t, transcriptDir, "race-host")
		if _, err := eng.ResumeSession(t.Context(), sessionID); err != nil {
			t.Fatalf("iteration %d staged resume: %v", iteration, err)
		}

		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		var commitErr error
		go func() {
			defer wait.Done()
			<-start
			commitErr = eng.CommitRestoreStaging()
		}()
		go func() {
			defer wait.Done()
			<-start
			eng.Close()
		}()
		close(start)
		wait.Wait()

		after := readP234aTranscript(t, path)
		switch {
		case commitErr == nil:
			if bytes.Equal(before, after) {
				t.Fatalf("iteration %d committed close did not persist", iteration)
			}
		case errors.Is(commitErr, ErrRestoreStagingTransition):
			if !bytes.Equal(before, after) {
				t.Fatalf("iteration %d closing staging owner persisted", iteration)
			}
		default:
			t.Fatalf("iteration %d commit-close error = %v", iteration, commitErr)
		}
	}
}

func TestP234aRestoreStagingAbortCloseRace(t *testing.T) {
	const iterations = 4
	for iteration := 0; iteration < iterations; iteration++ {
		transcriptDir := t.TempDir()
		sessionID := "p234a-abort-close-" + time.Now().UTC().Format("150405.000000000")
		writeP234aRestoreSource(t, transcriptDir, sessionID, t.TempDir())
		path := filepath.Join(transcriptDir, sessionID+".jsonl")
		before := readP234aTranscript(t, path)
		eng := newP234aRestoreStagingEngine(t, transcriptDir, "race-host")
		if _, err := eng.ResumeSession(t.Context(), sessionID); err != nil {
			t.Fatalf("iteration %d staged resume: %v", iteration, err)
		}

		start := make(chan struct{})
		var wait sync.WaitGroup
		wait.Add(2)
		var abortErr error
		go func() {
			defer wait.Done()
			<-start
			abortErr = eng.AbortRestoreStaging()
		}()
		go func() {
			defer wait.Done()
			<-start
			eng.Close()
		}()
		close(start)
		wait.Wait()

		if abortErr != nil {
			t.Fatalf("iteration %d abort-close error = %v", iteration, abortErr)
		}
		if !bytes.Equal(before, readP234aTranscript(t, path)) {
			t.Fatalf("iteration %d abort-close race persisted", iteration)
		}
	}
}

func writeP234aRestoreSource(
	t *testing.T,
	transcriptDir string,
	sessionID string,
	cwd string,
) {
	t.Helper()
	writeProjectGraphSession(
		t,
		transcriptDir,
		sessionID,
		&session.SessionMetadataFull{
			SessionID:          sessionID,
			ThreadID:           sessionID,
			QueryKernelVersion: queryKernelVersionProjectGraph,
			QueryKernelStage:   string(queryKernelStageFull),
			CreatedAt:          time.Now().UTC(),
			CWD:                cwd,
		},
	)
}

func newP234aRestoreStagingEngine(
	t *testing.T,
	transcriptDir string,
	sessionID string,
) *QueryEngine {
	t.Helper()
	return newP234aRestoreStagingEngineWithModel(
		t,
		transcriptDir,
		sessionID,
		nil,
	)
}

func newP234aRestoreStagingEngineWithModel(
	t *testing.T,
	transcriptDir string,
	sessionID string,
	model *canonicalScriptModel,
) *QueryEngine {
	t.Helper()
	config := p234aEngineConfig(t, transcriptDir, sessionID)
	config.ChatModel = model
	return NewRestoreStagingQueryEngine(config)
}

func newP234aOrdinaryEngine(
	t *testing.T,
	transcriptDir string,
	sessionID string,
	model *canonicalScriptModel,
) *QueryEngine {
	t.Helper()
	config := p234aEngineConfig(t, transcriptDir, sessionID)
	config.ChatModel = model
	return NewQueryEngine(config)
}

func p234aEngineConfig(
	t *testing.T,
	transcriptDir string,
	sessionID string,
) QueryEngineConfig {
	t.Helper()
	agentRunner := tools.NewAgentRunner(1)
	agentRunner.SetOutputDir(filepath.Join(t.TempDir(), "agent-output"))
	return QueryEngineConfig{
		SessionID:     sessionID,
		ThreadID:      sessionID,
		TranscriptDir: transcriptDir,
		CWD:           t.TempDir(),
		MCPManager:    tools.NewMCPToolManager(),
		SkillRegistry: skills.NewSkillRegistry(),
		AgentRunner:   agentRunner,
	}
}

func readP234aTranscript(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
