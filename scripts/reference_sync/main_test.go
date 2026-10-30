package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAcquireLockReleasesAndAllowsNextRun(t *testing.T) {
	t.Parallel()

	memoryRoot := t.TempDir()
	release, err := acquireLock(memoryRoot)
	if err != nil {
		t.Fatalf("acquireLock() error = %v", err)
	}
	if _, err := acquireLock(memoryRoot); err == nil || !strings.Contains(err.Error(), "appears to be running") {
		t.Fatalf("second acquireLock() error = %v, want active-lock error", err)
	}
	release()

	secondRelease, err := acquireLock(memoryRoot)
	if err != nil {
		t.Fatalf("acquireLock() after release error = %v", err)
	}
	secondRelease()
}

func TestAcquireLockUsesUnlockedPersistentFile(t *testing.T) {
	t.Parallel()

	memoryRoot := t.TempDir()
	lockPath := filepath.Join(memoryRoot, ".lock")
	if err := os.WriteFile(lockPath, []byte("persistent lock file"), 0o600); err != nil {
		t.Fatalf("WriteFile(lock) error = %v", err)
	}

	release, err := acquireLock(memoryRoot)
	if err != nil {
		t.Fatalf("acquireLock() should lock an existing unlocked file: %v", err)
	}
	defer release()
	content, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("ReadFile(lock) error = %v", err)
	}
	if string(content) != "persistent lock file" {
		t.Fatalf("lock file content = %q, want it preserved", content)
	}
}

func TestAcquireLockPreservesActiveLock(t *testing.T) {
	t.Parallel()

	memoryRoot := t.TempDir()
	release, err := acquireLock(memoryRoot)
	if err != nil {
		t.Fatalf("first acquireLock() error = %v", err)
	}
	defer release()

	if _, err := acquireLock(memoryRoot); err == nil || !strings.Contains(err.Error(), "appears to be running") {
		t.Fatalf("second acquireLock() error = %v, want active-lock error", err)
	}

	lockPath := filepath.Join(memoryRoot, ".lock")
	if info, err := os.Stat(lockPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("active lock file stat = %v, error = %v; want persistent regular file", info, err)
	}
}

func TestGitClientRunHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (gitClient{dir: t.TempDir(), ctx: ctx}).run("status")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("gitClient.run() error = %v, want context.Canceled", err)
	}
}

func TestRunSyncContextReleasesLockAfterCancellation(t *testing.T) {
	t.Parallel()

	memoryRoot := filepath.Join(t.TempDir(), ".sync-memory")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	code := runSyncContext(
		ctx,
		t.TempDir(),
		[]Repository{{ID: "codex", Path: "codex", Remote: "https://github.com/openai/codex.git", Upstream: "origin/main", Sync: "enabled"}},
		t.TempDir(),
		memoryRoot,
		0,
		ModelSummary{},
		SubagentSummary{},
		false,
		&stdout,
		&stderr,
	)
	if code == 0 || !strings.Contains(stderr.String(), "sync cancelled") {
		t.Fatalf("runSyncContext() = %d, stderr = %q; want cancellation reported", code, stderr.String())
	}

	release, err := acquireLock(memoryRoot)
	if err != nil {
		t.Fatalf("sync lock remained held after cancellation: %v", err)
	}
	release()
}

func TestRunSyncContextCancellationDuringPostUpdateSummaryPersistsFailureAndReleasesLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX Codex executable")
	}

	root := t.TempDir()
	remote := filepath.Join(root, "codex.git")
	seed := filepath.Join(root, "seed")
	referenceRoot := filepath.Join(root, "references")
	checkout := filepath.Join(referenceRoot, "codex")
	memoryRoot := filepath.Join(root, ".sync-memory")
	projectDir := filepath.Join(root, "project")
	fakeBin := filepath.Join(root, "bin")
	for _, dir := range []string{seed, referenceRoot, projectDir, fakeBin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	runReferenceSyncGit(t, root, "init", "--bare", remote)
	runReferenceSyncGit(t, seed, "init", "--initial-branch=main")
	runReferenceSyncGit(t, seed, "config", "user.name", "Reference Sync Test")
	runReferenceSyncGit(t, seed, "config", "user.email", "reference-sync-test@example.invalid")
	runReferenceSyncGit(t, seed, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runReferenceSyncGit(t, seed, "add", "README.md")
	runReferenceSyncGit(t, seed, "commit", "-m", "baseline")
	runReferenceSyncGit(t, seed, "remote", "add", "origin", remote)
	runReferenceSyncGit(t, seed, "push", "-u", "origin", "main")
	runReferenceSyncGit(t, root, "clone", "--branch", "main", remote, checkout)
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("updated reference\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runReferenceSyncGit(t, seed, "add", "README.md")
	runReferenceSyncGit(t, seed, "commit", "-m", "upstream update")
	runReferenceSyncGit(t, seed, "push", "origin", "main")

	codexPath := filepath.Join(fakeBin, "codex")
	codexScript := strings.Join([]string{
		"#!/bin/sh",
		"set -eu",
		"script_dir=$(CDPATH= cd \"$(dirname \"$0\")\" && pwd)",
		"if [ ! -e \"$script_dir/first-call\" ]; then",
		"  : > \"$script_dir/first-call\"",
		"  cat >/dev/null",
		"  printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"Evidence-backed change analysis\"}}'",
		"  exit 0",
		"fi",
		"cat >/dev/null",
		": > \"$script_dir/post-update-started\"",
		"exec sleep 60",
	}, "\n") + "\n"
	if err := os.WriteFile(codexPath, []byte(codexScript), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Git preparation and the first summary are not part of the cancellation
	// oracle. Bound the whole fixture by the runner budget, with time left for
	// owned-process cleanup, rather than requiring all setup to finish in 10s.
	testCtx := t.Context()
	if deadline, ok := t.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		cleanupHeadroom := min(5*time.Second, max(0, time.Until(deadline)/10))
		testCtx, deadlineCancel = context.WithDeadline(testCtx, deadline.Add(-cleanupHeadroom))
		defer deadlineCancel()
	}
	ctx, cancel := context.WithCancel(testCtx)
	defer cancel()
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		defer close(done)
		done <- runSyncContext(
			ctx,
			projectDir,
			[]Repository{{ID: "codex", Path: "codex", Remote: remote, Upstream: "origin/main", Sync: "enabled"}},
			referenceRoot,
			memoryRoot,
			10,
			ModelSummary{Backend: summaryBackendCodex, Model: "test-model", TimeoutSeconds: 60},
			SubagentSummary{Backend: summaryBackendCodex, Model: "test-model", TimeoutSeconds: 60},
			false,
			&stdout,
			&stderr,
		)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	startedPath := filepath.Join(fakeBin, "post-update-started")
	var startupDeadline *time.Timer
	var startupTimeout <-chan time.Time
	defer func() {
		if startupDeadline != nil {
			startupDeadline.Stop()
		}
	}()
waitForPostUpdate:
	for {
		select {
		case code := <-done:
			t.Fatalf("runSyncContext() = %d before post-update subagent started; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		case <-testCtx.Done():
			cancel()
			<-done
			t.Fatalf("fixture runner budget expired before post-update startup: %v", testCtx.Err())
		case <-startupTimeout:
			// A delayed observer can see both the marker and timer ready. The
			// marker, not polling latency, decides whether cancellation is safe.
			if _, err := os.Stat(startedPath); err == nil {
				break waitForPostUpdate
			}
			cancel()
			<-done
			t.Fatal("post-update subagent did not start within 10s of the persisted update")
		case <-ticker.C:
			if _, err := os.Stat(startedPath); err == nil {
				break waitForPostUpdate
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("observe post-update start: %v", err)
			}
			if startupDeadline == nil {
				// Updates are atomically renamed only after Git preparation, the
				// first summary and fast-forward have completed. Start the local
				// missing-marker watchdog at this actual stage boundary.
				updates, err := filepath.Glob(filepath.Join(memoryRoot, "updates", "*.md"))
				if err != nil {
					t.Fatal(err)
				}
				if len(updates) > 0 {
					startupDeadline = time.NewTimer(10 * time.Second)
					startupTimeout = startupDeadline.C
				}
			}
		}
	}
	cancel()
	if code := <-done; code == 0 {
		t.Fatalf("runSyncContext() = 0 after post-update cancellation; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}

	if got := strings.TrimSpace(runReferenceSyncGit(t, checkout, "rev-parse", "HEAD")); got != strings.TrimSpace(runReferenceSyncGit(t, seed, "rev-parse", "HEAD")) {
		t.Fatalf("reference HEAD = %s, want fetched upstream %s", got, runReferenceSyncGit(t, seed, "rev-parse", "HEAD"))
	}
	updates, err := filepath.Glob(filepath.Join(memoryRoot, "updates", "*.md"))
	if err != nil || len(updates) != 1 {
		t.Fatalf("updates = %v, error = %v; want one persisted update", updates, err)
	}
	summaries, err := filepath.Glob(filepath.Join(memoryRoot, "summaries", "*.md"))
	if err != nil || len(summaries) != 1 {
		t.Fatalf("summaries = %v, error = %v; want one persisted failure summary", summaries, err)
	}
	summary, err := os.ReadFile(summaries[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(summary), "Status: `failed`") || !strings.Contains(string(summary), "context canceled") {
		t.Fatalf("post-update summary = %s, want canceled failure", summary)
	}
	runs, err := filepath.Glob(filepath.Join(memoryRoot, "runs", "*.md"))
	if err != nil || len(runs) != 1 {
		t.Fatalf("run records = %v, error = %v; want one run record", runs, err)
	}
	runRecord, err := os.ReadFile(runs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(runRecord), "Status: `failed`") {
		t.Fatalf("run record = %s, want failed summary status", runRecord)
	}
	release, err := acquireLock(memoryRoot)
	if err != nil {
		t.Fatalf("sync lock remained held after canceled summary: %v", err)
	}
	release()
}

func runReferenceSyncGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func TestValidateConfigRejectsFrozenReferenceUpdates(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"claude-code-ripe", "grok-bot-0.18-reconstructed"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			cfg := Config{
				Version:   1,
				MemoryDir: ".sync-memory",
				Repositories: []Repository{
					{
						ID:       id,
						Path:     id,
						Remote:   "https://github.com/example/reference.git",
						Upstream: "origin/main",
						Sync:     "enabled",
					},
				},
			}

			err := validateConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), "must remain frozen") {
				t.Fatalf("validateConfig() error = %v, want frozen-reference error", err)
			}
		})
	}
}

func TestValidateConfigRejectsEscapingPath(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Version:   1,
		MemoryDir: ".sync-memory",
		Repositories: []Repository{
			{
				ID:       "codex",
				Path:     "../codex",
				Remote:   "https://github.com/openai/codex.git",
				Upstream: "origin/main",
				Sync:     "enabled",
			},
		},
	}

	err := validateConfig(cfg)
	if err == nil || !strings.Contains(err.Error(), "relative path") {
		t.Fatalf("validateConfig() error = %v, want relative-path error", err)
	}
}

func TestValidateConfigRequiresSupportedSummaryBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "unknown model summary backend",
			cfg: Config{
				Version:      1,
				ModelSummary: ModelSummary{Backend: "deepseek", Model: "deepseek-v4-flash"},
			},
			want: "model_summary.backend",
		},
		{
			name: "codex model is required",
			cfg: Config{
				Version:      1,
				ModelSummary: ModelSummary{Backend: summaryBackendCodex},
			},
			want: "model_summary.model",
		},
		{
			name: "post update codex model is required",
			cfg: Config{
				Version:         1,
				SubagentSummary: SubagentSummary{Backend: summaryBackendCodex},
			},
			want: "subagent_summary.model",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfig(tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateConfig() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestNormalizeRemote(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"git@github.com:openai/codex.git":       "github.com/openai/codex",
		"ssh://git@github.com/openai/codex.git": "github.com/openai/codex",
		"https://github.com/openai/codex.git/":  "github.com/openai/codex",
	}
	for input, want := range tests {
		if got := normalizeRemote(input); got != want {
			t.Errorf("normalizeRemote(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseAheadBehind(t *testing.T) {
	t.Parallel()

	ahead, behind, err := parseAheadBehind("3\t7\n")
	if err != nil {
		t.Fatalf("parseAheadBehind() error = %v", err)
	}
	if ahead != 3 || behind != 7 {
		t.Fatalf("parseAheadBehind() = %d/%d, want 3/7", ahead, behind)
	}
}

func TestRenderUpdateMemoryIncludesChangeSummary(t *testing.T) {
	t.Parallel()

	data := renderUpdateMemory(syncResult{
		Repository:  "codex",
		Remote:      "https://github.com/openai/codex.git",
		Upstream:    "origin/main",
		Status:      "updated",
		Before:      "1111111",
		After:       "2222222",
		CommitCount: 4,
		ShortStat:   "4 files changed, 10 insertions(+), 2 deletions(-)",
		CommitSubjects: []string{
			"1111111\t2026-08-25\tfirst change",
			"2222222\t2026-08-25\tlast change",
		},
		DiffStat: "file.go | 12 +++++++++---",
	})

	text := string(data)
	for _, want := range []string{
		"# Reference update: codex",
		"1111111..2222222",
		"4 files changed, 10 insertions(+), 2 deletions(-)",
		"first change",
		"file.go | 12 +++++++++---",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("renderUpdateMemory() missing %q in:\n%s", want, text)
		}
	}
}
