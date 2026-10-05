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

type transientSummaryGenerator struct {
	calls   int
	prompts []string
}

func (g *transientSummaryGenerator) Generate(_ context.Context, prompt string) (string, error) {
	g.calls++
	g.prompts = append(g.prompts, prompt)
	if g.calls == 1 {
		return "", errors.New("connection reset by peer")
	}
	return "Evidence-backed analysis", nil
}

func (*transientSummaryGenerator) Identity() (string, string) { return "test", "same-model" }

func TestModelAnalysisRetriesSameBoundedEvidence(t *testing.T) {
	t.Parallel()
	g := &transientSummaryGenerator{}
	results := []syncResult{{Repository: "codex", Status: "pending_summary", Before: "a", After: "b", ChangeDiff: "bounded evidence"}}
	err := generateModelSummaries(context.Background(), t.TempDir(), ModelSummary{MaxInputBytes: 2048, TimeoutSeconds: 1}, results,
		func(context.Context, string) (modelSummaryGenerator, error) { return g, nil })
	if err != nil || g.calls != 2 || results[0].SummaryStatus != "generated" {
		t.Fatalf("calls=%d status=%s error=%v; want transient retry", g.calls, results[0].SummaryStatus, err)
	}
	if g.prompts[0] != g.prompts[1] || len(g.prompts[0]) > 2048 {
		t.Fatal("retry changed or expanded evidence")
	}
}

func TestCodexFailureKeepsTerminalJSONErrorAndProcessCause(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process fixture")
	}
	dir := t.TempDir()
	command := filepath.Join(dir, "codex")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' 'Reading prompt from stdin...' >&2\ni=0; while [ $i -lt 30 ]; do echo 'WARN state db discrepancy: falling_back' >&2; i=$((i+1)); done\nprintf '%s\\n' '{\"type\":\"turn.failed\",\"error\":{\"message\":\"connection reset by peer token=secret-value\"}}'\nexit 1\n"
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	g := &codexModelSummaryGenerator{command: command, modelName: "test", workDir: dir}
	_, err := g.Generate(context.Background(), "evidence")
	var exitError *exec.ExitError
	if err == nil || !strings.Contains(err.Error(), "connection reset by peer") || !errors.As(err, &exitError) {
		t.Fatalf("error=%v; want terminal cause, not warning prefix", err)
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatal("unredacted terminal error")
	}
}

func TestSyncRecoversFailedSummaryEvenWithNoNewUpdates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process fixture")
	}
	root := t.TempDir()
	memory := filepath.Join(root, "memory")
	now := time.Date(2026, 10, 4, 19, 33, 18, 0, time.UTC)
	update := filepath.Join(memory, "updates", memoryFilename(now, "codex"))
	if err := writeMemoryFile(update, []byte("# Reference update: codex\n\n## Model analysis\n\nExact historical evidence")); err != nil {
		t.Fatal(err)
	}
	failed := postUpdateSummaryResult{Status: "failed", InputFiles: 1, Error: "connection reset by peer"}
	if err := persistPostUpdateSummary(memory, now, &failed, ""); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(memory, failed.Path))
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ninput=$(cat)\ncase \"$input\" in *\"Exact historical evidence\"*) ;; *) exit 42 ;; esac\nprintf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"Recovered historical analysis\"}}'\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	code := runSyncContext(context.Background(), root, nil, root, memory, 10, ModelSummary{}, SubagentSummary{Backend: "codex", Model: "test"}, false, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "recovered") {
		t.Fatalf("code=%d stdout=%s stderr=%s; want old failed summary recovered", code, stdout.String(), stderr.String())
	}
	after, _ := os.ReadFile(filepath.Join(memory, failed.Path))
	if !bytes.Equal(before, after) {
		t.Fatal("recovery overwrote historical failed attempt")
	}
	stdout.Reset()
	stderr.Reset()
	code = runSyncContext(context.Background(), root, nil, root, memory, 10, ModelSummary{}, SubagentSummary{Backend: "codex", Model: "test"}, false, &stdout, &stderr)
	if code != 0 || strings.Contains(stdout.String(), "recovered") {
		t.Fatalf("successful job replayed: %d %s %s", code, stdout.String(), stderr.String())
	}
}

func TestFetchRetriesWithCommandOnlyTransportFallbackAndGuards(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process fixture")
	}
	for _, scenario := range []string{"https", "ssh", "dirty", "changed", "auth"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			remote := "https://github.com/example/reference.git"
			if scenario == "ssh" {
				remote = "git@github.com:example/reference.git"
			}
			script := "#!/bin/sh\nset -eu\ncase \"$*\" in\n" +
				" *'remote get-url origin'*) echo '" + remote + "';;\n" +
				" *'rev-parse HEAD'*) if [ -f tried ] && [ '" + scenario + "' = changed ]; then echo b; else echo a; fi;;\n" +
				" *'status --porcelain'*) if [ -f tried ] && [ '" + scenario + "' = dirty ]; then echo ' M file'; fi;;\n" +
				" *'fetch --prune --no-tags origin'*) printf '%s\\n' \"$*\" >> calls; if [ ! -f tried ]; then touch tried; if [ '" + scenario + "' = auth ]; then echo 'Permission denied (publickey)' >&2; else echo 'RPC failed; curl 92 HTTP/2 stream closed' >&2; fi; exit 128; fi;;\n" +
				" *) exit 43;;\nesac\n"
			if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			attempts, err := (gitClient{dir: dir}).fetch(remote, "a", "origin/main", FetchPolicy{Retry: RetryPolicy{Attempts: 2, BackoffSeconds: 1}})
			data, _ := os.ReadFile(filepath.Join(dir, "calls"))
			calls := strings.Split(strings.TrimSpace(string(data)), "\n")
			for _, call := range calls {
				if !strings.HasSuffix(call, "origin +refs/heads/main:refs/remotes/origin/main") {
					t.Fatalf("fetch escaped configured branch: %s", call)
				}
			}
			switch scenario {
			case "auth":
				if err == nil || attempts != 1 || len(calls) != 1 {
					t.Fatalf("auth amplified: %d %v %s", attempts, err, data)
				}
			case "dirty", "changed":
				var guard *fetchGuardError
				if !errors.As(err, &guard) || len(calls) != 1 {
					t.Fatalf("retry bypassed guard: %v %s", err, data)
				}
			case "https", "ssh":
				if err != nil || attempts != 2 || len(calls) != 2 || !strings.Contains(calls[1], "-c http.version=HTTP/1.1") {
					t.Fatalf("fallback missing: %d %v %s", attempts, err, data)
				}
				if scenario == "ssh" && !strings.Contains(calls[1], "url.https://github.com/.insteadOf=git@github.com:") {
					t.Fatal("missing SSH fallback")
				}
			}
		})
	}
}

func TestRetryBudgetAndCancellation(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"cancel", "exhaust", "quota", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			_, attempts, err := retryTransient(ctx, RetryPolicy{Attempts: 2, BackoffSeconds: 1}, func() (string, error) {
				calls++
				switch scenario {
				case "cancel":
					cancel()
					return "", errors.New("connection reset")
				case "quota":
					return "", errors.New("usage limit reached, retry later")
				case "unknown":
					return "", errors.New("invalid JSON response")
				default:
					return "", context.DeadlineExceeded
				}
			})
			want := 1
			if scenario == "exhaust" {
				want = 2
			}
			if err == nil || calls != want || attempts != want {
				t.Fatalf("calls=%d attempts=%d err=%v", calls, attempts, err)
			}
			if scenario == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestSummaryRecoveryRejectsChangedAndEscapingInputs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "updates", "one.md")
	if err := writeMemoryFile(path, []byte("original evidence")); err != nil {
		t.Fatal(err)
	}
	job, err := newSummaryJob(root, time.Now().UTC(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tampered evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := job.paths(root); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed evidence accepted: %v", err)
	}
	for _, path := range []string{"../private.md", "updates/../../private.md", "/private.md"} {
		if _, err := updateInput(root, path); err == nil {
			t.Fatalf("escaping input accepted: %s", path)
		}
	}
	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "outside.md")
		if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "updates", "link.md")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		if _, err := updateInput(root, "updates/link.md"); err == nil {
			t.Fatal("symlink escape accepted")
		}
	}
}

func TestRetryConfigurationRejectsUnboundedBudgets(t *testing.T) {
	t.Parallel()
	for _, policy := range []RetryPolicy{{Attempts: 4}, {Attempts: -1}, {BackoffSeconds: 31}, {BackoffSeconds: -1}} {
		if err := policy.validate("test"); err == nil {
			t.Fatalf("invalid policy accepted: %#v", policy)
		}
	}
}

func TestPartialUpdateWriteKeepsSuccessfulSubsetRecoverable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Now().UTC()
	// Independently cause the second atomic rename to fail (destination directory).
	bad := filepath.Join(root, "updates", memoryFilename(now, "second"))
	if err := os.MkdirAll(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	paths, err := writeSyncUpdates(root, now, []syncResult{{Repository: "first", Status: "updated"}, {Repository: "second", Status: "updated"}})
	if err == nil || len(paths) != 1 {
		t.Fatalf("partial write=%v paths=%v", err, paths)
	}
	jobs, _ := filepath.Glob(filepath.Join(root, "summary-jobs", "*.json"))
	if len(jobs) != 1 {
		t.Fatalf("partial durable updates orphaned: jobs=%v", jobs)
	}
	data, err := os.ReadFile(jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "-first.md") || strings.Contains(string(data), "-second.md") {
		t.Fatalf("wrong recoverable subset: %s", data)
	}
}

func TestSavedSummarySettlesPendingJobWithoutModelRedispatch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	when := time.Date(2026, 10, 5, 1, 0, 0, 0, time.UTC)
	update := filepath.Join(root, "updates", memoryFilename(when, "codex"))
	if err := writeMemoryFile(update, []byte("evidence")); err != nil {
		t.Fatal(err)
	}
	job, err := newSummaryJob(root, when, []string{update})
	if err != nil {
		t.Fatal(err)
	}
	result := postUpdateSummaryResult{Status: "generated", SourceBatch: job.Batch, SourceFiles: []string{job.Inputs[0].Path}, InputFiles: 1}
	job.Summary = filepath.ToSlash(filepath.Join("summaries", memoryFilename(when, "updates-subagent")))
	if err := job.save(root); err != nil {
		t.Fatal(err)
	}
	// Independently seed the crash point: result exists but job remains pending.
	if err := persistPostUpdateSummary(root, when, &result, "Evidence-backed saved summary"); err != nil {
		t.Fatal(err)
	}
	called := false
	var stdout bytes.Buffer
	err = recoverPendingSummaries(context.Background(), root, root, nil, SubagentSummary{}, func(context.Context, string, string) (modelSummaryGenerator, error) {
		called = true
		return nil, errors.New("must not redispatch")
	}, &stdout)
	if err != nil || called || !strings.Contains(stdout.String(), "no model redispatch") {
		t.Fatalf("recovery=%v called=%v stdout=%s", err, called, stdout.String())
	}
	data, _ := os.ReadFile(filepath.Join(root, "summary-jobs", job.Batch+".json"))
	if !strings.Contains(string(data), "\"status\": \"completed\"") {
		t.Fatalf("completion not settled: %s", data)
	}
	runs, err := filepath.Glob(filepath.Join(root, "runs", "*-summary-recovery.md"))
	if err != nil || len(runs) != 1 {
		t.Fatalf("settle recovery audit missing: runs=%v err=%v", runs, err)
	}
}

func TestFetchOnlyConfiguredBranchDoesNotFollowTagsOrOtherTopics(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	seed, remote, checkout := filepath.Join(root, "seed"), filepath.Join(root, "remote.git"), filepath.Join(root, "checkout")
	if err := os.Mkdir(seed, 0o700); err != nil {
		t.Fatal(err)
	}
	runReferenceSyncGit(t, root, "init", "--bare", remote)
	runReferenceSyncGit(t, seed, "init", "--initial-branch=main")
	runReferenceSyncGit(t, seed, "config", "user.name", "Reference Test")
	runReferenceSyncGit(t, seed, "config", "user.email", "test@example.invalid")
	runReferenceSyncGit(t, seed, "config", "commit.gpgsign", "false")
	file := filepath.Join(seed, "file")
	if err := os.WriteFile(file, []byte("baseline"), 0o600); err != nil {
		t.Fatal(err)
	}
	runReferenceSyncGit(t, seed, "add", "file")
	runReferenceSyncGit(t, seed, "commit", "-m", "baseline")
	runReferenceSyncGit(t, seed, "remote", "add", "origin", remote)
	runReferenceSyncGit(t, seed, "push", "origin", "main")
	runReferenceSyncGit(t, root, "clone", "--branch", "main", remote, checkout)
	before := strings.TrimSpace(runReferenceSyncGit(t, checkout, "rev-parse", "HEAD"))
	configBefore := runReferenceSyncGit(t, checkout, "config", "--local", "--list")
	if err := os.WriteFile(file, []byte("upstream"), 0o600); err != nil {
		t.Fatal(err)
	}
	runReferenceSyncGit(t, seed, "add", "file")
	runReferenceSyncGit(t, seed, "commit", "-m", "upstream")
	runReferenceSyncGit(t, seed, "branch", "unrelated-topic")
	runReferenceSyncGit(t, seed, "tag", "unneeded-tag")
	runReferenceSyncGit(t, seed, "push", "origin", "main", "unrelated-topic", "refs/tags/unneeded-tag")
	g := gitClient{dir: checkout, ctx: context.Background()}
	if _, err := g.fetch(remote, before, "origin/main", FetchPolicy{}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"refs/remotes/origin/unrelated-topic", "refs/tags/unneeded-tag"} {
		if _, err := g.run("show-ref", "--verify", ref); err == nil {
			t.Fatalf("fetch escaped configured branch: %s", ref)
		}
	}
	after, err := g.run("rev-parse", "origin/main")
	if err != nil || after == before {
		t.Fatalf("upstream was not fetched: after=%s err=%v", after, err)
	}
	head, _ := g.run("rev-parse", "HEAD")
	if head != before {
		t.Fatal("fetch merged without model analysis")
	}
	if configAfter := runReferenceSyncGit(t, checkout, "config", "--local", "--list"); configAfter != configBefore {
		t.Fatal("fetch mutated checkout config")
	}
}
