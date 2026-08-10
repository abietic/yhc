package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/memdir"
	"github.com/abietic/yhc/engine/permission"
	enginesession "github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	engineworktree "github.com/abietic/yhc/engine/worktree"
	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

func TestMigrateStateRealBinary(t *testing.T) {
	binary := buildMigrateStateBinary(t)
	home, project := t.TempDir(), t.TempDir()
	var err error
	project, err = filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	clearMigrateStateRuntimeEnvironment(t)
	fixtures := newMigrateStateBinaryFixtures(t, home, project)
	before := captureMigrateStateLegacyManifest(t, home, project)

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			wantReady := migrateStateStatus(fixture.owner, fixture.scope, "ready")
			if got := runMigrateStateBinary(t, binary, home, project, "inspect", "--scope", fixture.scope, "--owner", fixture.owner); got != wantReady {
				t.Fatalf("initial inspect = %q, want %q", got, wantReady)
			}
			wantImported := migrateStateStatus(fixture.owner, fixture.scope, "imported")
			if got := runMigrateStateBinary(t, binary, home, project, "apply", "--scope", fixture.scope, "--owner", fixture.owner); got != wantImported {
				t.Fatalf("apply = %q, want %q", got, wantImported)
			}
			wantCollision := migrateStateStatus(fixture.owner, fixture.scope, "destination_exists")
			if got := runMigrateStateBinary(t, binary, home, project, "inspect", "--scope", fixture.scope, "--owner", fixture.owner); got != wantCollision {
				t.Fatalf("restarted inspect = %q, want %q", got, wantCollision)
			}
			if got := runMigrateStateBinary(t, binary, home, project, "apply", "--scope", fixture.scope, "--owner", fixture.owner); got != wantCollision {
				t.Fatalf("restarted apply = %q, want %q", got, wantCollision)
			}
			fixture.verify(t)
		})
	}
	assertMigrateStateLegacyManifest(t, home, project, before)
	assertMigrateStatePrivateSentinelsExcluded(t, home, project)

	t.Run("preexisting destination remains byte-identical", func(t *testing.T) {
		collisionHome, collisionProject := t.TempDir(), t.TempDir()
		var err error
		collisionHome, err = filepath.EvalSymlinks(collisionHome)
		if err != nil {
			t.Fatal(err)
		}
		roots, err := statepath.UserRoots(collisionHome)
		if err != nil {
			t.Fatal(err)
		}
		legacy := filepath.Join(roots.Legacy, keybindingsMigrationFilename)
		canonical := filepath.Join(roots.Canonical, keybindingsMigrationFilename)
		writeMigrateStateFile(t, legacy, []byte(validMigrateStateKeybindings), 0o600)
		if err := os.Chmod(roots.Legacy, 0o700); err != nil {
			t.Fatal(err)
		}
		original := []byte("canonical collision sentinel\n")
		writeMigrateStateFile(t, canonical, original, 0o600)
		if err := os.Chmod(roots.Canonical, 0o700); err != nil {
			t.Fatal(err)
		}
		if got := runMigrateStateBinary(t, binary, collisionHome, collisionProject, "apply", "--scope", "user", "--owner", "keybindings"); got != migrateStateStatus("keybindings", "user", "destination_exists") {
			t.Fatalf("collision apply = %q", got)
		}
		got, err := os.ReadFile(canonical)
		if err != nil || !bytes.Equal(got, original) {
			t.Fatalf("collision destination = %q err=%v", got, err)
		}
	})
}

func TestMigrateStateStateContinuityRealBinary(t *testing.T) {
	binary := buildMigrateStateBinary(t)
	fixture := newStateContinuityBinaryFixture(t)
	legacyBefore := captureMigrateStateLegacyManifest(
		t,
		fixture.home,
		fixture.project,
	)
	gitBefore := captureStateContinuityGitState(t, fixture)

	listOutput := runYHCBinary(
		t,
		binary,
		fixture,
		"sessions", "list", "--output-format", "json",
	)
	assertStateContinuityLegacySessionListed(t, listOutput, fixture.sessionID)
	canonicalBeforeImport := captureStateContinuityCanonicalRegistration(t, fixture)

	var commandOutputs strings.Builder
	commandOutputs.WriteString(listOutput)
	for _, inspection := range []struct {
		args []string
		want string
	}{
		{
			args: []string{"migrate-state", "inspect", "--owner", "session", "--session", fixture.sessionID},
			want: "owner=session scope=project status=ready\n",
		},
		{
			args: []string{"migrate-state", "inspect", "--owner", "cron"},
			want: "owner=cron scope=project status=ready count=1\n",
		},
		{
			args: []string{"migrate-state", "inspect", "--owner", "worktree"},
			want: "owner=worktree scope=project status=ready count=1\nrecord=legacy-live status=dirty\n",
		},
		{
			args: []string{"migrate-state", "inspect", "--scope", "project", "--owner", "approvals"},
			want: "owner=approvals scope=project status=ready\n",
		},
	} {
		output := runYHCBinary(t, binary, fixture, inspection.args...)
		commandOutputs.WriteString(output)
		if output != inspection.want {
			t.Fatalf("inspect %v = %q, want %q", inspection.args, output, inspection.want)
		}
	}

	headlessOutput, exitCode := runYHCBinaryFailure(
		t,
		binary,
		fixture,
		"exec", "must-not-run",
		"--resume", fixture.sessionID,
		"--output-format", "json",
		"--provider", "unsupported-provider-that-must-not-initialize",
	)
	commandOutputs.WriteString(headlessOutput)
	assertStateContinuityImportRequired(t, headlessOutput, exitCode, fixture)
	for _, args := range [][]string{
		{"migrate-state", "apply", "--owner", "session", "--session", fixture.sessionID},
		{"migrate-state", "apply", "--owner", "cron"},
		{"migrate-state", "apply", "--scope", "project", "--owner", "worktree"},
	} {
		output, gotExit := runYHCBinaryFailure(t, binary, fixture, args...)
		commandOutputs.WriteString(output)
		if gotExit != ExitUsage {
			t.Fatalf("unavailable apply %v exit=%d output=%q", args, gotExit, output)
		}
	}
	assertStateContinuityPreImportState(t, fixture, canonicalBeforeImport)
	assertMigrateStateLegacyManifest(t, fixture.home, fixture.project, legacyBefore)

	output := runYHCBinary(
		t,
		binary,
		fixture,
		"migrate-state", "apply", "--owner", "session",
		"--session", fixture.sessionID,
		"--confirm-legacy-stopped",
	)
	commandOutputs.WriteString(output)
	if output != "owner=session scope=project status=imported\n" {
		t.Fatalf("session import = %q", output)
	}
	resumeOutput := runYHCBinary(
		t,
		binary,
		fixture,
		"sessions", "resume", fixture.sessionID, "--output-format", "json",
	)
	commandOutputs.WriteString(resumeOutput)
	assertStateContinuityCanonicalResume(t, resumeOutput, fixture)
	if output = runYHCBinary(
		t,
		binary,
		fixture,
		"migrate-state", "apply", "--owner", "session",
		"--session", fixture.sessionID,
		"--confirm-legacy-stopped",
	); output != "owner=session scope=project status=destination_exists\n" {
		t.Fatalf("repeat session import = %q", output)
	}
	commandOutputs.WriteString(output)

	renameOutput := runYHCBinary(
		t,
		binary,
		fixture,
		"sessions", "rename", fixture.sessionID, "canonical-continuation",
		"--output-format", "json",
	)
	commandOutputs.WriteString(renameOutput)
	for _, operation := range []struct {
		args []string
		want string
	}{
		{
			args: []string{"migrate-state", "apply", "--scope", "project", "--owner", "approvals"},
			want: "owner=approvals scope=project status=imported\n",
		},
		{
			args: []string{"migrate-state", "apply", "--owner", "cron", "--confirm-legacy-stopped"},
			want: "owner=cron scope=project status=imported count=1\n",
		},
	} {
		output = runYHCBinary(t, binary, fixture, operation.args...)
		commandOutputs.WriteString(output)
		if output != operation.want {
			t.Fatalf("apply %v = %q, want %q", operation.args, output, operation.want)
		}
	}

	worktreeOutput := runYHCBinary(
		t,
		binary,
		fixture,
		"migrate-state", "inspect", "--owner", "worktree",
	)
	commandOutputs.WriteString(worktreeOutput)
	if worktreeOutput != "owner=worktree scope=project status=ready count=1\nrecord=legacy-live status=dirty\n" {
		t.Fatalf("final worktree inspect = %q", worktreeOutput)
	}
	assertStateContinuityCanonicalArtifacts(t, fixture)
	assertMigrateStateLegacyManifest(t, fixture.home, fixture.project, legacyBefore)
	assertStateContinuityGitState(t, fixture, gitBefore)
	if strings.Contains(commandOutputs.String(), stateContinuityPrivateSentinel) {
		t.Fatal("YHC command output exposed a private fixture value")
	}
}

const stateContinuityPrivateSentinel = "state-continuity-private-sentinel"

type stateContinuityBinaryFixture struct {
	home, project                string
	sessionID                    string
	legacyTranscriptDir          string
	canonicalTranscriptDir       string
	legacyCatalog                string
	canonicalCatalog             string
	legacyCron                   string
	canonicalCron                string
	legacyApprovals              string
	canonicalApprovals           string
	worktreePath                 string
	worktreeBranch               string
	worktreeRecordPath           string
	baseCommit, repositoryCommon string
	projectRoots, userRoots      statepath.Roots
}

func newStateContinuityBinaryFixture(t *testing.T) stateContinuityBinaryFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projectRoots, err := statepath.ProjectRoots(project)
	if err != nil {
		t.Fatal(err)
	}
	userRoots, err := statepath.UserRoots(home)
	if err != nil {
		t.Fatal(err)
	}
	fixture := stateContinuityBinaryFixture{
		home:                   home,
		project:                project,
		sessionID:              "state-continuity-session",
		legacyTranscriptDir:    filepath.Join(projectRoots.Legacy, "transcripts"),
		canonicalTranscriptDir: filepath.Join(projectRoots.Canonical, "transcripts"),
		legacyCatalog:          filepath.Join(userRoots.Legacy, "session-roots.json"),
		canonicalCatalog:       filepath.Join(userRoots.Canonical, "session-roots.json"),
		legacyCron:             filepath.Join(projectRoots.Legacy, "scheduled_tasks.json"),
		canonicalCron:          filepath.Join(projectRoots.Canonical, "scheduled_tasks.json"),
		legacyApprovals:        filepath.Join(projectRoots.Legacy, "approvals.json"),
		canonicalApprovals:     filepath.Join(projectRoots.Canonical, "approvals.json"),
		worktreeBranch:         "eino-agent/worktree/legacy-live",
		projectRoots:           projectRoots,
		userRoots:              userRoots,
	}

	runStateContinuityGit(t, project, "init", "-b", "master")
	runStateContinuityGit(t, project, "config", "user.email", "state-continuity@example.invalid")
	runStateContinuityGit(t, project, "config", "user.name", "State Continuity")
	writeMigrateStateFile(
		t,
		filepath.Join(project, ".gitignore"),
		[]byte(".eino-agent/\n.yhc/\n.mcp.json\n"),
		0o644,
	)
	writeMigrateStateFile(t, filepath.Join(project, "tracked.txt"), []byte("base\n"), 0o644)
	runStateContinuityGit(t, project, "add", ".gitignore", "tracked.txt")
	runStateContinuityGit(t, project, "commit", "-m", "state continuity base")
	fixture.baseCommit = strings.TrimSpace(runStateContinuityGit(t, project, "rev-parse", "HEAD"))
	common := strings.TrimSpace(runStateContinuityGit(t, project, "rev-parse", "--git-common-dir"))
	if !filepath.IsAbs(common) {
		common = filepath.Join(project, common)
	}
	fixture.repositoryCommon, err = filepath.EvalSymlinks(common)
	if err != nil {
		t.Fatal(err)
	}

	recorder := transcript.NewRecorder(fixture.sessionID, fixture.legacyTranscriptDir)
	if err := recorder.RecordMessages([]*schema.Message{
		{Role: schema.User, Content: "legacy state continuity prompt"},
		{Role: schema.Assistant, Content: "legacy state continuity answer"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := enginesession.WriteSessionMetadata(recorder, &enginesession.SessionMetadataFull{
		SessionID:          fixture.sessionID,
		ThreadID:           fixture.sessionID,
		CWD:                project,
		QueryKernelVersion: "project_graph/v1",
		QueryKernelStage:   "full",
		CreatedAt:          time.Date(2026, 8, 10, 6, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	transcriptFile, err := os.OpenFile(
		filepath.Join(fixture.legacyTranscriptDir, fixture.sessionID+".jsonl"),
		os.O_APPEND|os.O_WRONLY,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transcriptFile.WriteString("{\"timestamp\":\"recoverable-truncated-tail"); err != nil {
		_ = transcriptFile.Close()
		t.Fatal(err)
	}
	if err := transcriptFile.Close(); err != nil {
		t.Fatal(err)
	}
	writeStateContinuityWorkBoard(t, fixture.legacyTranscriptDir, fixture.sessionID)
	if err := enginesession.RegisterSessionRoot(
		fixture.legacyCatalog,
		project,
		fixture.legacyTranscriptDir,
		time.Date(2026, 8, 10, 6, 5, 0, 0, time.UTC),
	); err != nil {
		t.Fatal(err)
	}

	tracker := permission.NewApprovalTracker()
	tracker.Approve(permission.ApprovalKey{
		ToolName:       "Bash",
		CommandPattern: stateContinuityPrivateSentinel,
		ExactCommand:   true,
	}, "user", false)
	if err := tracker.SaveTo(fixture.legacyApprovals); err != nil {
		t.Fatal(err)
	}
	writeMigrateStateFile(
		t,
		fixture.legacyCron,
		[]byte(`{"tasks":[{"id":"state-task","cron":"* * * * *","prompt":"state-continuity-private-sentinel","createdAt":1}]}`),
		0o600,
	)

	fixture.worktreePath = filepath.Join(
		projectRoots.Legacy,
		"worktrees",
		"v1",
		"trees",
		"legacy-live",
	)
	runStateContinuityGit(
		t,
		project,
		"worktree", "add", "-b", fixture.worktreeBranch,
		fixture.worktreePath,
		fixture.baseCommit,
	)
	writeMigrateStateFile(
		t,
		filepath.Join(fixture.worktreePath, "legacy-dirty.txt"),
		[]byte(stateContinuityPrivateSentinel+"\n"),
		0o600,
	)
	fixture.worktreeRecordPath = filepath.Join(
		projectRoots.Legacy,
		"worktrees",
		"v1",
		"records",
		"legacy-live.json",
	)
	recordData, err := json.MarshalIndent(engineworktree.Record{
		Version: engineworktree.RecordVersion,
		ID:      "legacy-live",
		Owner: engineworktree.Owner{
			Kind:      engineworktree.OwnerAgent,
			ID:        "legacy-agent",
			SessionID: fixture.sessionID,
			ThreadID:  "legacy-thread",
		},
		RepositoryIdentity: fixture.repositoryCommon,
		RepoRoot:           project,
		Path:               fixture.worktreePath,
		Branch:             fixture.worktreeBranch,
		BaseCommit:         fixture.baseCommit,
		State:              engineworktree.StateReady,
		Revision:           1,
		CreatedAt:          time.Date(2026, 8, 10, 6, 10, 0, 0, time.UTC),
		UpdatedAt:          time.Date(2026, 8, 10, 6, 10, 0, 0, time.UTC),
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeMigrateStateFile(t, fixture.worktreeRecordPath, append(recordData, '\n'), 0o600)
	writeMigrateStatePrivateSentinels(t, home, project)
	for _, root := range []string{projectRoots.Legacy, userRoots.Legacy} {
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return fixture
}

func writeStateContinuityWorkBoard(t *testing.T, directory, sessionID string) {
	t.Helper()
	board := map[string]any{
		"revision":     7,
		"next_todo_id": 1,
		"items":        []any{},
	}
	compatibility := map[string]any{
		"next_task_id": 1,
		"tasks":        []any{},
		"todo_scopes":  []any{},
	}
	artifacts := map[string]any{
		sessionID + ".workboard-v2.json": map[string]any{
			"version": 2, "session_id": sessionID, "board_id": "state-board",
			"board": board, "compatibility": compatibility,
		},
		sessionID + ".workboard-legacy-backup-v1.json": map[string]any{
			"version": 1, "session_id": sessionID, "board_id": "state-board",
			"board": board, "compatibility": compatibility,
		},
		sessionID + ".workboard-authority-v1.json": map[string]any{
			"version": 1, "session_id": sessionID, "minimum_reader": "workboard/v2",
		},
	}
	for name, artifact := range artifacts {
		data, err := json.Marshal(artifact)
		if err != nil {
			t.Fatal(err)
		}
		writeMigrateStateFile(t, filepath.Join(directory, name), append(data, '\n'), 0o600)
	}
}

type stateContinuityGitState struct {
	branchCommit, worktreeList, worktreeStatus string
}

func captureStateContinuityGitState(
	t *testing.T,
	fixture stateContinuityBinaryFixture,
) stateContinuityGitState {
	t.Helper()
	return stateContinuityGitState{
		branchCommit: strings.TrimSpace(runStateContinuityGit(
			t, fixture.project, "rev-parse", "refs/heads/"+fixture.worktreeBranch,
		)),
		worktreeList: runStateContinuityGit(t, fixture.project, "worktree", "list", "--porcelain"),
		worktreeStatus: runStateContinuityGit(
			t, fixture.worktreePath, "status", "--porcelain=v1", "--untracked-files=all",
		),
	}
}

func assertStateContinuityGitState(
	t *testing.T,
	fixture stateContinuityBinaryFixture,
	want stateContinuityGitState,
) {
	t.Helper()
	if got := captureStateContinuityGitState(t, fixture); got != want {
		t.Fatalf("legacy Git state changed:\n got %#v\nwant %#v", got, want)
	}
}

func runStateContinuityGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func runYHCBinary(
	t *testing.T,
	binary string,
	fixture stateContinuityBinaryFixture,
	args ...string,
) string {
	t.Helper()
	output, err := executeYHCBinary(binary, fixture, args...)
	if err != nil {
		t.Fatalf("yhc %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return output
}

func runYHCBinaryFailure(
	t *testing.T,
	binary string,
	fixture stateContinuityBinaryFixture,
	args ...string,
) (string, int) {
	t.Helper()
	output, err := executeYHCBinary(binary, fixture, args...)
	if err == nil {
		t.Fatalf("yhc %s unexpectedly succeeded: %s", strings.Join(args, " "), output)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("yhc %s did not return an exit status: %v", strings.Join(args, " "), err)
	}
	return output, exitErr.ExitCode()
}

func executeYHCBinary(
	binary string,
	fixture stateContinuityBinaryFixture,
	args ...string,
) (string, error) {
	command := exec.Command(binary, args...)
	command.Dir = fixture.project
	command.Env = []string{"HOME=" + fixture.home, "PATH=" + os.Getenv("PATH")}
	output, err := command.CombinedOutput()
	return string(output), err
}

func assertStateContinuityLegacySessionListed(
	t *testing.T,
	output string,
	sessionID string,
) {
	t.Helper()
	var envelope struct {
		Status string            `json:"status"`
		Result sessionListOutput `json:"result"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Status != "completed" || len(envelope.Result.Sessions) != 1 {
		t.Fatalf("legacy sessions list = %#v", envelope)
	}
	session := envelope.Result.Sessions[0]
	if session.SessionID != sessionID || !session.ReadOnly || !session.NeedsImport {
		t.Fatalf("legacy session projection = %#v", session)
	}
}

type stateContinuityCanonicalRegistration struct {
	rootMode, catalogMode, lockMode fs.FileMode
	catalogModTime, lockModTime     int64
	catalogDigest, lockDigest       string
}

func captureStateContinuityCanonicalRegistration(
	t *testing.T,
	fixture stateContinuityBinaryFixture,
) stateContinuityCanonicalRegistration {
	t.Helper()
	if _, err := os.Lstat(fixture.projectRoots.Canonical); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy discovery created project state %q: %v", fixture.projectRoots.Canonical, err)
	}
	entries, err := os.ReadDir(fixture.userRoots.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 ||
		entries[0].Name() != filepath.Base(fixture.canonicalCatalog) ||
		entries[1].Name() != filepath.Base(fixture.canonicalCatalog)+".lock" {
		t.Fatalf("legacy discovery canonical user artifacts = %v", entries)
	}
	rootInfo, err := os.Stat(fixture.userRoots.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	catalogInfo, err := os.Stat(fixture.canonicalCatalog)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := os.ReadFile(fixture.canonicalCatalog)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(catalog, []byte(identity.LegacyDirName)) ||
		bytes.Contains(catalog, []byte(stateContinuityPrivateSentinel)) {
		t.Fatal("canonical catalog captured legacy or private fixture data")
	}
	digest := sha256.Sum256(catalog)
	lockPath := fixture.canonicalCatalog + ".lock"
	lockInfo, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if rootInfo.Mode().Perm() != 0o700 || catalogInfo.Mode().Perm() != 0o600 ||
		lockInfo.Mode().Perm() != 0o600 {
		t.Fatalf(
			"canonical registration modes root=%o catalog=%o lock=%o",
			rootInfo.Mode().Perm(),
			catalogInfo.Mode().Perm(),
			lockInfo.Mode().Perm(),
		)
	}
	lock, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	lockSum := sha256.Sum256(lock)
	return stateContinuityCanonicalRegistration{
		rootMode:       rootInfo.Mode().Perm(),
		catalogMode:    catalogInfo.Mode().Perm(),
		catalogModTime: catalogInfo.ModTime().UnixNano(),
		catalogDigest:  hex.EncodeToString(digest[:]),
		lockMode:       lockInfo.Mode().Perm(),
		lockModTime:    lockInfo.ModTime().UnixNano(),
		lockDigest:     hex.EncodeToString(lockSum[:]),
	}
}

func assertStateContinuityPreImportState(
	t *testing.T,
	fixture stateContinuityBinaryFixture,
	want stateContinuityCanonicalRegistration,
) {
	t.Helper()
	if got := captureStateContinuityCanonicalRegistration(t, fixture); got != want {
		t.Fatalf("refused pre-import operations changed canonical registration: got=%#v want=%#v", got, want)
	}
}

func assertStateContinuityImportRequired(
	t *testing.T,
	output string,
	exitCode int,
	fixture stateContinuityBinaryFixture,
) {
	t.Helper()
	var envelope headlessEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode headless refusal: %v; output=%q", err, output)
	}
	if exitCode != ExitFailure || envelope.Error == nil ||
		envelope.Error.Code != "legacy_session_import_required" ||
		!strings.Contains(envelope.Error.Message, fixture.sessionID) ||
		strings.Contains(envelope.Error.Message, fixture.legacyTranscriptDir) ||
		strings.Contains(envelope.Error.Message, "unsupported-provider") {
		t.Fatalf("headless refusal exit=%d envelope=%#v", exitCode, envelope)
	}
}

func assertStateContinuityCanonicalResume(
	t *testing.T,
	output string,
	fixture stateContinuityBinaryFixture,
) {
	t.Helper()
	var envelope struct {
		Status string              `json:"status"`
		Result sessionResumeOutput `json:"result"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Status != "completed" || envelope.Result.SessionID != fixture.sessionID ||
		envelope.Result.MessageCount != 2 || envelope.Result.CWD != fixture.project {
		t.Fatalf("canonical resume = %#v", envelope)
	}
}

func assertStateContinuityCanonicalArtifacts(
	t *testing.T,
	fixture stateContinuityBinaryFixture,
) {
	t.Helper()
	resumed, err := enginesession.ResumeSession(t.Context(), enginesession.ResumeOptions{
		SessionID:  fixture.sessionID,
		SessionDir: fixture.canonicalTranscriptDir,
		ProjectDir: fixture.project,
	})
	if err != nil || len(resumed.Messages) != 2 {
		t.Fatalf("canonical transcript resume=%#v err=%v", resumed, err)
	}
	canonicalTranscript, err := os.ReadFile(filepath.Join(
		fixture.canonicalTranscriptDir,
		fixture.sessionID+".jsonl",
	))
	if err != nil || !bytes.Contains(canonicalTranscript, []byte("canonical-continuation")) {
		t.Fatalf("canonical continuation missing: %v", err)
	}
	legacyTranscript, err := os.ReadFile(filepath.Join(
		fixture.legacyTranscriptDir,
		fixture.sessionID+".jsonl",
	))
	if err != nil || bytes.Contains(legacyTranscript, []byte("canonical-continuation")) {
		t.Fatalf("legacy transcript received canonical continuation: %v", err)
	}
	for _, suffix := range []string{
		".workboard-v2.json",
		".workboard-authority-v1.json",
		".workboard-legacy-backup-v1.json",
	} {
		legacy, legacyErr := os.ReadFile(filepath.Join(
			fixture.legacyTranscriptDir,
			fixture.sessionID+suffix,
		))
		canonical, canonicalErr := os.ReadFile(filepath.Join(
			fixture.canonicalTranscriptDir,
			fixture.sessionID+suffix,
		))
		if legacyErr != nil || canonicalErr != nil || !bytes.Equal(legacy, canonical) {
			t.Fatalf("canonical WorkBoard %s mismatch: legacy=%v canonical=%v", suffix, legacyErr, canonicalErr)
		}
	}
	legacyCron, err := os.ReadFile(fixture.legacyCron)
	if err != nil {
		t.Fatal(err)
	}
	canonicalCron, err := os.ReadFile(fixture.canonicalCron)
	if err != nil || !bytes.Equal(legacyCron, canonicalCron) {
		t.Fatalf("canonical cron mismatch: %v", err)
	}
	legacyApprovals, err := os.ReadFile(fixture.legacyApprovals)
	if err != nil {
		t.Fatal(err)
	}
	canonicalApprovals, err := os.ReadFile(fixture.canonicalApprovals)
	if err != nil || !bytes.Equal(legacyApprovals, canonicalApprovals) {
		t.Fatalf("canonical approvals mismatch: %v", err)
	}
	for _, path := range []string{
		fixture.canonicalCatalog,
		fixture.canonicalApprovals,
		fixture.canonicalCron,
		filepath.Join(fixture.canonicalTranscriptDir, fixture.sessionID+".jsonl"),
	} {
		assertMigrateStatePrivateFile(t, path)
	}
	assertMigrateStatePrivateTree(t, fixture.projectRoots.Canonical)
	assertMigrateStatePrivateTree(t, fixture.userRoots.Canonical)
	for _, root := range []struct {
		path string
		want []string
	}{
		{fixture.project, []string{".eino-agent", ".git", ".gitignore", ".mcp.json", ".yhc", "tracked.txt"}},
		{fixture.home, []string{".claude", ".eino-agent", ".yhc"}},
	} {
		entries, err := os.ReadDir(root.path)
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		sort.Strings(names)
		if strings.Join(names, "\x00") != strings.Join(root.want, "\x00") {
			t.Fatalf("unexpected state roots under %s: %v", root.path, names)
		}
	}
}

const (
	keybindingsMigrationFilename = "keybindings.json"
	validMigrateStateKeybindings = `{"bindings":[{"context":"Chat","bindings":{"alt+up":"chat:nextAgent"}}]}`
)

type migrateStateBinaryFixture struct {
	name, owner, scope string
	verify             func(*testing.T)
}

func newMigrateStateBinaryFixtures(t *testing.T, home, project string) []migrateStateBinaryFixture {
	t.Helper()
	projectRoots, err := statepath.ProjectRoots(project)
	if err != nil {
		t.Fatal(err)
	}
	userRoots, err := statepath.UserRoots(home)
	if err != nil {
		t.Fatal(err)
	}

	writeMigrateStateFile(t, filepath.Join(projectRoots.Legacy, "settings.json"), []byte(`{"model":"gpt-4o-mini"}`), 0o600)
	writeMigrateStateFile(t, filepath.Join(userRoots.Legacy, "settings.json"), []byte(`{"theme":"dark"}`), 0o600)
	writeMigrateStateFile(t, filepath.Join(userRoots.Legacy, keybindingsMigrationFilename), []byte(validMigrateStateKeybindings), 0o600)
	writeMigrateStateFile(t, filepath.Join(projectRoots.Legacy, "history"), []byte("first command\nsecond command\n"), 0o644)

	writeMigrateStateFile(t, filepath.Join(projectRoots.Legacy, "approvals.json"), []byte(`[{"tool_name":"Bash","command_pattern":"go test ./...","exact_command":true,"approved_at":"2026-08-10T04:00:00Z","reason":"user"}]`), 0o644)

	for _, request := range []struct{ owner, scope string }{
		{"memory", "user"},
		{"agent-memory", "user"},
		{"agent-memory", "project"},
		{"agent-memory-local", "project"},
	} {
		spec, err := memdir.MemoryMigrationSpec(request.owner, request.scope, project)
		if err != nil {
			t.Fatalf("%s %s spec: %v", request.owner, request.scope, err)
		}
		root := userRoots.Legacy
		if request.scope == "project" {
			root = projectRoots.Legacy
		}
		writeMigrateStateFile(t, filepath.Join(root, filepath.FromSlash(spec.SourceRel), "reviewer", "MEMORY.md"), []byte("reviewer memory\n"), 0o644)
		if request.owner == "memory" {
			// Automatic memory is a direct tree, unlike named-agent memories.
			path := filepath.Join(root, filepath.FromSlash(spec.SourceRel), "reviewer", "MEMORY.md")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			writeMigrateStateFile(t, filepath.Join(root, filepath.FromSlash(spec.SourceRel), "MEMORY.md"), []byte("automatic memory\n"), 0o644)
		}
	}

	auditLegacy := filepath.Join(userRoots.Legacy, "permission-review-audit", "v1")
	audit, err := permission.NewReviewAuditStore(permission.ReviewAuditStoreOptions{
		Dir: auditLegacy, Now: func() time.Time { return time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC) }, LockTimeout: time.Second, StaleLockAfter: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.Record(context.Background(), permission.ReviewAuditRecord{SchemaVersion: permission.ReviewAuditSchemaVersion, EventID: "0123456789abcdef0123456789abcdef", OccurredAt: time.Date(2026, 8, 10, 4, 0, 0, 0, time.UTC), Kind: permission.ReviewAuditKindEligible, CanonicalTool: "Bash", ActionKind: "filesystem_read", DeterministicClass: "review"}); err != nil {
		t.Fatal(err)
	}
	writeMigrateStatePrivateSentinels(t, home, project)
	if err := os.Chmod(userRoots.Legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(projectRoots.Legacy, 0o700); err != nil {
		t.Fatal(err)
	}

	return []migrateStateBinaryFixture{
		{"project settings", "settings", "project", func(t *testing.T) {
			assertMigrateStateJSON(t, filepath.Join(projectRoots.Canonical, "settings.json"), "model", "gpt-4o-mini")
		}},
		{"user settings", "settings", "user", func(t *testing.T) {
			assertMigrateStateJSON(t, filepath.Join(userRoots.Canonical, "settings.json"), "theme", "dark")
		}},
		{"user keybindings", "keybindings", "user", func(t *testing.T) {
			assertMigrateStateJSON(t, filepath.Join(userRoots.Canonical, keybindingsMigrationFilename), "bindings", nil)
		}},
		{"user memory", "memory", "user", func(t *testing.T) {
			assertMigrateStateTree(t, filepath.Join(userRoots.Canonical, filepath.FromSlash(mustMigrateStateMemorySpec(t, "memory", "user", project).TargetRel)), "MEMORY.md", "automatic memory\n")
		}},
		{"user agent memory", "agent-memory", "user", func(t *testing.T) {
			assertMigrateStateTree(t, filepath.Join(userRoots.Canonical, "agent-memory"), filepath.Join("reviewer", "MEMORY.md"), "reviewer memory\n")
		}},
		{"project agent memory", "agent-memory", "project", func(t *testing.T) {
			assertMigrateStateTree(t, filepath.Join(projectRoots.Canonical, "agent-memory"), filepath.Join("reviewer", "MEMORY.md"), "reviewer memory\n")
		}},
		{"project local agent memory", "agent-memory-local", "project", func(t *testing.T) {
			assertMigrateStateTree(t, filepath.Join(projectRoots.Canonical, "agent-memory-local"), filepath.Join("reviewer", "MEMORY.md"), "reviewer memory\n")
		}},
		{"project approvals", "approvals", "project", func(t *testing.T) {
			assertMigrateStateApprovals(t, filepath.Join(projectRoots.Canonical, "approvals.json"))
		}},
		{"project history", "history", "project", func(t *testing.T) {
			assertMigrateStateTree(t, projectRoots.Canonical, "history", "first command\nsecond command\n")
		}},
		{"user permission review audit", "permission-review-audit", "user", func(t *testing.T) {
			assertMigrateStateAudit(t, filepath.Join(userRoots.Canonical, "permission-review-audit", "v1"))
		}},
	}
}

func buildMigrateStateBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "yhc")
	command := exec.Command("go", "build", "-o", binary, "./cmd/yhc")
	command.Dir = migrateStateRepositoryRoot(t)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build yhc: %v\n%s", err, output)
	}
	return binary
}

func migrateStateRepositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(dir, "..", "..", ".."))
}

func runMigrateStateBinary(t *testing.T, binary, home, project string, args ...string) string {
	t.Helper()
	command := exec.Command(binary, append([]string{"migrate-state"}, args...)...)
	command.Dir = project
	command.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("yhc %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func migrateStateStatus(owner, scope, status string) string {
	return "owner=" + owner + " scope=" + scope + " status=" + status + "\n"
}

func mustMigrateStateMemorySpec(t *testing.T, owner, scope, project string) statemigration.ArtifactSpec {
	t.Helper()
	spec, err := memdir.MemoryMigrationSpec(owner, scope, project)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func clearMigrateStateRuntimeEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"YHC_CONFIG_DIR", "EINO_AGENT_CONFIG_DIR", "YHC_REMOTE_MEMORY_DIR", "EINO_AGENT_REMOTE_MEMORY_DIR",
		"YHC_MEMORY_PATH_OVERRIDE", "EINO_AGENT_MEMORY_PATH_OVERRIDE", "YHC_PERMISSION_REVIEW_AUDIT_DIR", "EINO_AGENT_PERMISSION_REVIEW_AUDIT_DIR",
	} {
		t.Setenv(name, "")
	}
}

func writeMigrateStateFile(t *testing.T, path string, content []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func writeMigrateStatePrivateSentinels(t *testing.T, home, project string) {
	t.Helper()
	for path, content := range map[string]string{
		filepath.Join(home, ".claude", "credentials.json"):      "credential-private-sentinel",
		filepath.Join(home, ".claude", "mcp_oauth_tokens.json"): "oauth-private-sentinel",
		filepath.Join(home, ".claude", ".mcp.json"):             "mcp-private-sentinel",
		filepath.Join(home, ".claude", "hooks", "private.sh"):   "hook-private-sentinel",
		filepath.Join(project, ".mcp.json"):                     "project-mcp-private-sentinel",
	} {
		writeMigrateStateFile(t, path, []byte(content), 0o600)
	}
}

func assertMigrateStateJSON(t *testing.T, path, key string, want any) {
	t.Helper()
	assertMigrateStatePrivateFile(t, path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	got, ok := object[key]
	if !ok {
		t.Fatalf("%s missing key %q", path, key)
	}
	if want != nil && got != want {
		t.Fatalf("%s[%q] = %#v, want %#v", path, key, got, want)
	}
}

func assertMigrateStateApprovals(t *testing.T, path string) {
	t.Helper()
	assertMigrateStatePrivateFile(t, path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(data, &entries); err != nil || len(entries) != 1 || entries[0]["tool_name"] != "Bash" {
		t.Fatalf("canonical approvals=%s err=%v", data, err)
	}
}

func assertMigrateStateTree(t *testing.T, root, relative, want string) {
	t.Helper()
	path := filepath.Join(root, relative)
	data, err := os.ReadFile(path)
	if err != nil || string(data) != want {
		t.Fatalf("artifact %s = %q err=%v", path, data, err)
	}
	assertMigrateStatePrivateTree(t, root)
}

func assertMigrateStatePrivateFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("canonical file %s mode=%v err=%v, want 0600", path, fileMode(info), err)
	}
}

func assertMigrateStatePrivateTree(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		want := fs.FileMode(0o600)
		if entry.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			return errors.New("canonical artifact is not private: " + path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func fileMode(info fs.FileInfo) fs.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}

func assertMigrateStateAudit(t *testing.T, directory string) {
	t.Helper()
	assertMigrateStatePrivateTree(t, directory)
	for _, forbidden := range []string{"events.lock", "events.lock.guard"} {
		if _, err := os.Lstat(filepath.Join(directory, forbidden)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("audit coordination file copied: %s err=%v", forbidden, err)
		}
	}
	store, err := permission.NewReviewAuditStore(permission.ReviewAuditStoreOptions{Dir: directory})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || len(loaded.Records) != 1 || loaded.Records[0].Kind != permission.ReviewAuditKindEligible {
		t.Fatalf("canonical audit load=%#v err=%v", loaded, err)
	}
}

type migrateStateManifestEntry struct {
	relative, kind, digest string
	mode                   fs.FileMode
	modTime                int64
}

func captureMigrateStateLegacyManifest(t *testing.T, home, project string) []migrateStateManifestEntry {
	t.Helper()
	paths := []struct{ label, root string }{
		{"home-legacy", filepath.Join(home, identity.LegacyDirName)},
		{"home-claude", filepath.Join(home, ".claude")},
		{"project-legacy", filepath.Join(project, identity.LegacyDirName)},
		{"project-mcp", filepath.Join(project, ".mcp.json")},
	}
	var manifest []migrateStateManifestEntry
	for _, source := range paths {
		err := filepath.WalkDir(source.root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(source.root, path)
			if err != nil {
				return err
			}
			kind := "dir"
			digest := ""
			if !entry.IsDir() {
				kind = "file"
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				sum := sha256.Sum256(data)
				digest = hex.EncodeToString(sum[:])
			}
			manifest = append(manifest, migrateStateManifestEntry{relative: filepath.Join(source.label, relative), kind: kind, digest: digest, mode: info.Mode().Perm(), modTime: info.ModTime().UnixNano()})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(manifest, func(i, j int) bool { return manifest[i].relative < manifest[j].relative })
	return manifest
}

func assertMigrateStateLegacyManifest(t *testing.T, home, project string, want []migrateStateManifestEntry) {
	t.Helper()
	got := captureMigrateStateLegacyManifest(t, home, project)
	if len(got) != len(want) {
		t.Fatalf("legacy manifest entries=%d, want %d\ngot=%#v\nwant=%#v", len(got), len(want), got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("legacy manifest[%d]=%#v, want %#v", index, got[index], want[index])
		}
	}
}

func assertMigrateStatePrivateSentinelsExcluded(t *testing.T, home, project string) {
	t.Helper()
	for _, root := range []string{filepath.Join(home, identity.ProjectDirName), filepath.Join(project, identity.ProjectDirName)} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			for _, forbidden := range []string{"credentials.json", "mcp_oauth_tokens.json", ".mcp.json", "private.sh"} {
				if entry.Name() == forbidden {
					return errors.New("private sentinel copied: " + path)
				}
			}
			if entry.IsDir() {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), "private-sentinel") {
				return errors.New("private sentinel content copied: " + path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
