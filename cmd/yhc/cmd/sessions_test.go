package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine"
	enginesession "github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

func TestSessionsCLIProviderFreeListAndStableJSON(t *testing.T) {
	root, transcriptDir := prepareSessionCLIProject(t)
	writeSessionCLITranscript(t, transcriptDir, "saved-session", "inspect session output")
	t.Setenv("PROV", "unsupported-provider-that-must-not-be-read")

	stdout, stderr, err := executeSessionCLI(context.Background(), t, "sessions", "list", "--output-format", "json")
	if err != nil {
		t.Fatalf("sessions list: %v; stderr=%q", err, stderr)
	}
	var envelope struct {
		SchemaVersion int               `json:"schema_version"`
		Operation     string            `json:"operation"`
		Status        string            `json:"status"`
		ExitCode      int               `json:"exit_code"`
		Result        sessionListOutput `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode list output: %v; %q", err, stdout)
	}
	if envelope.SchemaVersion != sessionAdministrationEnvelopeSchemaVersion ||
		envelope.Operation != "sessions.list" || envelope.Status != "completed" ||
		envelope.ExitCode != ExitSuccess {
		t.Fatalf("list envelope = %#v", envelope)
	}
	if len(envelope.Result.Sessions) != 1 || envelope.Result.Sessions[0].SessionID != "saved-session" ||
		envelope.Result.Sessions[0].Title != "inspect session output" {
		t.Fatalf("list result = %#v", envelope.Result)
	}
	assertSessionTranscriptNames(t, root, "saved-session.jsonl")
}

func TestExplicitCatalogOverrideRemainsExactAndNonMigratable(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	override := filepath.Join(t.TempDir(), "only-this-catalog.json")
	t.Chdir(project)
	t.Setenv("HOME", home)
	t.Setenv("YHC_SESSION_CATALOG", override)
	legacyDir := filepath.Join(project, ".eino-agent", "transcripts")
	writeSessionCLITranscript(t, legacyDir, "legacy-session", "legacy must stay hidden")
	legacyCatalog := filepath.Join(home, ".eino-agent", "session-roots.json")
	if err := enginesession.RegisterSessionRoot(legacyCatalog, project, legacyDir, time.Now()); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeSessionCLI(context.Background(), t, "sessions", "list", "--output-format", "json")
	if err != nil {
		t.Fatalf("sessions list: %v; stderr=%q", err, stderr)
	}
	var output struct {
		Result sessionListOutput `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatal(err)
	}
	if len(output.Result.Sessions) != 0 {
		t.Fatalf("explicit catalog override discovered legacy rows: %#v", output.Result.Sessions)
	}
	if _, err := os.Stat(override); err != nil {
		t.Fatalf("explicit catalog was not used: %v", err)
	}
	for _, path := range []string{
		filepath.Join(home, ".yhc", "session-roots.json"),
		filepath.Join(home, ".eino-agent", "session-roots.json"),
	} {
		if path == legacyCatalog {
			continue
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("default catalog was created despite explicit override: %q: %v", path, err)
		}
	}
}

func TestNonInteractiveResumeReturnsImportRequiredWithoutWrites(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	t.Setenv("HOME", home)
	t.Setenv("YHC_SESSION_CATALOG", "")
	legacyDir := filepath.Join(project, ".eino-agent", "transcripts")
	legacyCatalog := filepath.Join(home, ".eino-agent", "session-roots.json")
	writeSessionCLITranscript(t, legacyDir, "legacy-session", "legacy resume")
	if err := enginesession.RegisterSessionRoot(legacyCatalog, project, legacyDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	legacyBefore, err := os.ReadFile(filepath.Join(legacyDir, "legacy-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	catalogBefore, err := os.ReadFile(legacyCatalog)
	if err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeSessionCLI(
		context.Background(), t,
		"sessions", "resume", "legacy-session", "--output-format", "json",
	)
	if ExitCode(err) != ExitFailure || stderr != "" {
		t.Fatalf("legacy resume: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	var envelope sessionAdministrationEnvelope
	if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if envelope.Error == nil || envelope.Error.Code != "legacy_session_import_required" ||
		!strings.Contains(envelope.Error.Message, "legacy-session") {
		t.Fatalf("legacy resume envelope = %#v", envelope)
	}
	legacyAfter, err := os.ReadFile(filepath.Join(legacyDir, "legacy-session.jsonl"))
	if err != nil || string(legacyAfter) != string(legacyBefore) {
		t.Fatalf("legacy transcript changed: %v", err)
	}
	catalogAfter, err := os.ReadFile(legacyCatalog)
	if err != nil || string(catalogAfter) != string(catalogBefore) {
		t.Fatalf("legacy catalog changed: %v", err)
	}
	for _, path := range []string{
		filepath.Join(project, ".yhc"),
		filepath.Join(home, ".yhc"),
	} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("refused legacy resume created %q: %v", path, statErr)
		}
	}
}

func TestHeadlessResumeRejectsLegacyBeforeRuntimeInitialization(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	t.Setenv("HOME", home)
	t.Setenv("YHC_SESSION_CATALOG", "")
	legacyDir := filepath.Join(project, ".eino-agent", "transcripts")
	legacyCatalog := filepath.Join(home, ".eino-agent", "session-roots.json")
	writeSessionCLITranscript(t, legacyDir, "headless-legacy", "legacy headless")
	if err := enginesession.RegisterSessionRoot(legacyCatalog, project, legacyDir, time.Now()); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDir, "headless-legacy.jsonl")
	legacyBefore, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err = runHeadless(t.Context(), "must not run", headlessOptions{
		Runtime: runtimeFlags{
			provider: "unsupported-provider-that-must-not-initialize",
		},
		Resume:       "headless-legacy",
		OutputFormat: string(outputFormatJSON),
		Stdin:        strings.NewReader(""),
		Stdout:       &stdout,
		Stderr:       &stderr,
	})
	if ExitCode(err) != ExitFailure || stderr.String() != "" {
		t.Fatalf("headless legacy resume: stdout=%q stderr=%q err=%v", stdout.String(), stderr.String(), err)
	}
	var envelope headlessEnvelope
	if decodeErr := json.Unmarshal(stdout.Bytes(), &envelope); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if envelope.Error == nil || envelope.Error.Code != "legacy_session_import_required" ||
		!strings.Contains(envelope.Error.Message, "headless-legacy") ||
		strings.Contains(envelope.Error.Message, legacyDir) {
		t.Fatalf("headless legacy envelope = %#v", envelope)
	}
	legacyAfter, err := os.ReadFile(legacyPath)
	if err != nil || string(legacyAfter) != string(legacyBefore) {
		t.Fatalf("headless refusal changed legacy transcript: %v", err)
	}
	for _, path := range []string{filepath.Join(project, ".yhc"), filepath.Join(home, ".yhc")} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("headless refusal created %q: %v", path, statErr)
		}
	}
}

func TestSessionsCLILifecycleUsesCanonicalServiceWithoutTUI(t *testing.T) {
	root, transcriptDir := prepareSessionCLIProject(t)
	writeSessionCLITranscript(t, transcriptDir, "source-session", "source prompt")

	stdout, stderr, err := executeSessionCLI(
		context.Background(),
		t,
		"sessions", "resume", "source-session", "--output-format", "json",
	)
	if err != nil {
		t.Fatalf("sessions resume: %v; stderr=%q", err, stderr)
	}
	var resumed struct {
		Operation string              `json:"operation"`
		Status    string              `json:"status"`
		Result    sessionResumeOutput `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &resumed); err != nil {
		t.Fatal(err)
	}
	if resumed.Operation != "sessions.resume" || resumed.Status != "completed" ||
		resumed.Result.SessionID != "source-session" || resumed.Result.MessageCount != 2 {
		t.Fatalf("resume output = %#v", resumed)
	}

	stdout, stderr, err = executeSessionCLI(
		context.Background(),
		t,
		"sessions", "rename", "source-session", "release", "investigation",
	)
	if err != nil || stdout != "Session source-session renamed to: release investigation\n" {
		t.Fatalf("sessions rename: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}

	exportPath := filepath.Join(root, "session-report.md")
	stdout, stderr, err = executeSessionCLI(
		context.Background(),
		t,
		"sessions", "export", "source-session", exportPath, "--output-format", "json",
	)
	if err != nil {
		t.Fatalf("sessions export: %v; stderr=%q", err, stderr)
	}
	var exported struct {
		Operation string              `json:"operation"`
		Result    sessionExportOutput `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &exported); err != nil {
		t.Fatal(err)
	}
	if exported.Operation != "sessions.export" || exported.Result.Path != exportPath ||
		exported.Result.SessionID != "source-session" || exported.Result.MessageCount != 2 {
		t.Fatalf("export output = %#v", exported)
	}
	if data, readErr := os.ReadFile(exportPath); readErr != nil || !bytes.Contains(data, []byte("source prompt")) {
		t.Fatalf("exported markdown read=%v content=%q", readErr, data)
	}

	stdout, stderr, err = executeSessionCLI(
		context.Background(),
		t,
		"sessions", "fork", "source-session", "review-alternative", "--output-format", "json",
	)
	if err != nil {
		t.Fatalf("sessions fork: %v; stderr=%q", err, stderr)
	}
	var forked struct {
		Operation string            `json:"operation"`
		Status    string            `json:"status"`
		Result    sessionForkOutput `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &forked); err != nil {
		t.Fatal(err)
	}
	if forked.Operation != "sessions.fork" || forked.Status != "completed" ||
		forked.Result.ParentSessionID != "source-session" || forked.Result.SessionID == "" ||
		forked.Result.BranchName != "review-alternative" || forked.Result.MessagesCopied != 2 {
		t.Fatalf("fork output = %#v", forked)
	}
	if _, statErr := os.Stat(filepath.Join(transcriptDir, forked.Result.SessionID+".jsonl")); statErr != nil {
		t.Fatalf("fork transcript missing: %v", statErr)
	}
	assertSessionTranscriptNames(
		t,
		root,
		forked.Result.SessionID+".jsonl",
		"source-session.jsonl",
	)
}

func TestSessionsCLIWorkBoardRecoveryAndDelete(t *testing.T) {
	root, transcriptDir := prepareSessionCLIProject(t)
	const sessionID = "workboard-administration"
	runtime := engine.NewQueryEngine(engine.QueryEngineConfig{
		SessionID:     sessionID,
		ThreadID:      sessionID,
		CWD:           root,
		TranscriptDir: transcriptDir,
	})
	if _, err := runtime.GetTaskManager().CreateWithError(
		"baseline",
		"recovery baseline",
		"",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.GetTaskManager().CreateWithError(
		"discarded",
		"post-cutover mutation",
		"",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	runtime.Close()

	authorityPath := filepath.Join(
		transcriptDir,
		sessionID+".workboard-v2.json",
	)
	var before struct {
		BoardID string `json:"board_id"`
		Board   struct {
			Revision uint64 `json:"revision"`
		} `json:"board"`
	}
	data, err := os.ReadFile(authorityPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &before); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, err := executeSessionCLI(
		context.Background(),
		t,
		"sessions",
		"recover-workboard",
		sessionID,
		"--board-id",
		before.BoardID,
		"--revision",
		fmt.Sprint(before.Board.Revision),
		"--acknowledge-data-loss",
		"--output-format",
		"json",
	)
	if err != nil {
		t.Fatalf(
			"recover WorkBoard: stdout=%q stderr=%q err=%v",
			stdout,
			stderr,
			err,
		)
	}
	var recovered struct {
		Operation string                         `json:"operation"`
		Status    string                         `json:"status"`
		Result    sessionWorkBoardRecoveryOutput `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.Operation != "sessions.recover-workboard" ||
		recovered.Status != "completed" ||
		recovered.Result.SessionID != sessionID ||
		recovered.Result.PreviousBoardID != before.BoardID ||
		recovered.Result.PreviousRevision != before.Board.Revision ||
		recovered.Result.RecoveredBoardID == "" ||
		recovered.Result.RecoveredBoardID == before.BoardID {
		t.Fatalf("recovery output = %+v", recovered)
	}

	stdout, stderr, err = executeSessionCLI(
		context.Background(),
		t,
		"sessions",
		"delete",
		sessionID,
		"--output-format",
		"json",
	)
	if err != nil {
		t.Fatalf(
			"delete WorkBoard Session: stdout=%q stderr=%q err=%v",
			stdout,
			stderr,
			err,
		)
	}
	var deleted struct {
		Operation string              `json:"operation"`
		Status    string              `json:"status"`
		Result    sessionDeleteOutput `json:"result"`
	}
	if err := json.Unmarshal([]byte(stdout), &deleted); err != nil {
		t.Fatal(err)
	}
	if deleted.Operation != "sessions.delete" ||
		deleted.Status != "completed" ||
		deleted.Result.SessionID != sessionID ||
		!deleted.Result.TranscriptRemoved ||
		!deleted.Result.WorkBoardAuthorityRemoved ||
		!deleted.Result.CleanupCompleted {
		t.Fatalf("delete output = %+v", deleted)
	}
	assertSessionTranscriptNames(t, root)
	for _, suffix := range []string{
		".workboard-v2.json",
		".workboard-authority-v1.json",
		".workboard-legacy-backup-v1.json",
	} {
		if _, err := os.Lstat(
			filepath.Join(transcriptDir, sessionID+suffix),
		); !os.IsNotExist(err) {
			t.Fatalf("delete left WorkBoard artifact %s: %v", suffix, err)
		}
	}
}

func TestSessionsCLIFailureCancellationAndUnsupportedMutations(t *testing.T) {
	root, _ := prepareSessionCLIProject(t)
	stdout, stderr, err := executeSessionCLI(
		context.Background(),
		t,
		"sessions", "resume", "missing", "--output-format", "json",
	)
	if ExitCode(err) != ExitFailure || stderr != "" {
		t.Fatalf("missing session: stdout=%q stderr=%q err=%v exit=%d", stdout, stderr, err, ExitCode(err))
	}
	var failed sessionAdministrationEnvelope
	if decodeErr := json.Unmarshal([]byte(stdout), &failed); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if failed.Status != "failed" || failed.ExitCode != ExitFailure || failed.Error == nil ||
		failed.Error.Code != "session_error" {
		t.Fatalf("failure envelope = %#v", failed)
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	stdout, stderr, err = executeSessionCLI(
		cancelledCtx,
		t,
		"sessions", "list", "--output-format", "json",
	)
	if ExitCode(err) != ExitCancelled || stderr != "" {
		t.Fatalf("cancelled list: stdout=%q stderr=%q err=%v exit=%d", stdout, stderr, err, ExitCode(err))
	}
	var cancelled sessionAdministrationEnvelope
	if decodeErr := json.Unmarshal([]byte(stdout), &cancelled); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if cancelled.Status != "cancelled" || cancelled.ExitCode != ExitCancelled ||
		cancelled.Error == nil || cancelled.Error.Code != "cancelled" {
		t.Fatalf("cancelled envelope = %#v", cancelled)
	}

	for _, operation := range []string{"archive"} {
		_, _, err = executeSessionCLI(context.Background(), t, "sessions", operation, "anything")
		if ExitCode(err) != ExitUsage {
			t.Fatalf("sessions %s exit=%d err=%v", operation, ExitCode(err), err)
		}
	}
	assertSessionTranscriptNames(t, root)
}

func TestSessionsCLIJSONUsageFailuresUseAdministrationEnvelope(t *testing.T) {
	prepareSessionCLIProject(t)
	tests := []struct {
		name      string
		operation string
		args      []string
	}{
		{
			name:      "missing subcommand",
			operation: "sessions",
			args:      []string{"sessions", "--output-format", "json"},
		},
		{
			name:      "missing resume ID",
			operation: "sessions.resume",
			args:      []string{"sessions", "resume", "--output-format", "json"},
		},
		{
			name:      "unknown subcommand",
			operation: "sessions",
			args:      []string{"sessions", "archive", "anything", "--output-format", "json"},
		},
		{
			name:      "invalid limit",
			operation: "sessions.list",
			args:      []string{"sessions", "list", "--limit", "0", "--output-format", "json"},
		},
		{
			name:      "unknown flag",
			operation: "sessions.list",
			args:      []string{"sessions", "list", "--output-format", "json", "--unknown"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, err := executeSessionCLI(context.Background(), t, tt.args...)
			if ExitCode(err) != ExitUsage || stderr != "" {
				t.Fatalf("usage failure: stdout=%q stderr=%q err=%v exit=%d", stdout, stderr, err, ExitCode(err))
			}
			var envelope sessionAdministrationEnvelope
			if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil {
				t.Fatalf("decode usage envelope: %v; output=%q", decodeErr, stdout)
			}
			if envelope.SchemaVersion != sessionAdministrationEnvelopeSchemaVersion ||
				envelope.Operation != tt.operation || envelope.Status != "failed" ||
				envelope.ExitCode != ExitUsage || envelope.Error == nil ||
				envelope.Error.Code != "usage_error" {
				t.Fatalf("usage envelope = %#v", envelope)
			}
		})
	}
}

func TestSessionsCLIMutationsRejectAmbiguousCatalogIdentity(t *testing.T) {
	root, _ := prepareSessionCLIProject(t)
	catalogPath := os.Getenv("YHC_SESSION_CATALOG")
	dirs := []string{
		filepath.Join(root, "catalog-a"),
		filepath.Join(root, "catalog-b"),
	}
	for _, dir := range dirs {
		writeSessionCLITranscript(t, dir, "duplicate-session", "ambiguous source")
		if err := enginesession.RegisterSessionRoot(catalogPath, root, dir, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		operation string
		args      []string
	}{
		{operation: "sessions.resume", args: []string{"resume", "duplicate-session"}},
		{operation: "sessions.rename", args: []string{"rename", "duplicate-session", "name"}},
		{operation: "sessions.export", args: []string{"export", "duplicate-session", filepath.Join(root, "duplicate.md")}},
		{operation: "sessions.fork", args: []string{"fork", "duplicate-session", "branch"}},
	}
	for _, tt := range tests {
		t.Run(tt.operation, func(t *testing.T) {
			args := append([]string{"sessions"}, tt.args...)
			args = append(args, "--output-format", "json")
			stdout, stderr, err := executeSessionCLI(context.Background(), t, args...)
			if ExitCode(err) != ExitFailure || stderr != "" {
				t.Fatalf("ambiguous identity: stdout=%q stderr=%q err=%v exit=%d", stdout, stderr, err, ExitCode(err))
			}
			var envelope sessionAdministrationEnvelope
			if decodeErr := json.Unmarshal([]byte(stdout), &envelope); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if envelope.Operation != tt.operation || envelope.Error == nil ||
				envelope.Error.Code != "session_error" ||
				!strings.Contains(envelope.Error.Message, "ambiguous") {
				t.Fatalf("ambiguous envelope = %#v", envelope)
			}
		})
	}
}

func prepareSessionCLIProject(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("YHC_SESSION_CATALOG", filepath.Join(root, "session-roots.json"))
	return root, filepath.Join(root, ".yhc", "transcripts")
}

func executeSessionCLI(ctx context.Context, t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	root := newRootCommand()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	return stdout.String(), stderr.String(), err
}

func writeSessionCLITranscript(t *testing.T, dir, id, prompt string) {
	t.Helper()
	recorder := transcript.NewRecorder(id, dir)
	if err := recorder.RecordMessages([]*schema.Message{
		{Role: schema.User, Content: prompt},
		{Role: schema.Assistant, Content: "answer"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := enginesession.WriteSessionMetadata(recorder, &enginesession.SessionMetadataFull{
		SessionID:          id,
		ThreadID:           id,
		CWD:                filepath.Dir(filepath.Dir(dir)),
		QueryKernelVersion: "project_graph/v1",
		QueryKernelStage:   "full",
		CreatedAt:          time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertSessionTranscriptNames(t *testing.T, root string, want ...string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".yhc", "transcripts"))
	if err != nil {
		if os.IsNotExist(err) && len(want) == 0 {
			return
		}
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			got = append(got, entry.Name())
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("session transcripts = %v, want %v", got, want)
	}
}
