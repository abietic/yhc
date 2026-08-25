package appserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewDiffUsesOwnedSessionCWDAndBoundsOutput(t *testing.T) {
	root := t.TempDir()
	runReviewGit(t, root, "init", "-q")
	runReviewGit(t, root, "config", "user.email", "desktop@example.com")
	runReviewGit(t, root, "config", "user.name", "Desktop Test")
	tracked := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runReviewGit(t, root, "add", "tracked.txt")
	runReviewGit(t, root, "commit", "-qm", "initial")
	if err := os.WriteFile(
		tracked,
		[]byte(strings.Repeat("after review line\n", 80_000)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "untracked.txt"),
		[]byte("must not be synthesized into the tracked diff"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	create := doJSON(
		t,
		httpServer.URL+"/v1/sessions",
		"test-token",
		http.MethodPost,
		map[string]string{"workspace_handle": registerWorkspace(t, httpServer.URL, "test-token", root).WorkspaceHandle},
	)
	var summary SessionSummary
	decodeResponse(t, create, &summary)
	_ = create.Body.Close()

	response := getBearer(
		t,
		httpServer.URL+"/v1/sessions/"+summary.ID+
			"/review-diff?ignore_whitespace=false&cwd=/tmp/not-owned",
		"test-token",
	)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("review diff = %d: %s", response.StatusCode, readBody(t, response))
	}
	var review ReviewDiffResponse
	decodeResponse(t, response, &review)
	if review.CWD != summary.CWD || len(review.Sources) != 1 {
		t.Fatalf("review = %+v", review)
	}
	source := review.Sources[0]
	if source.ID != "worktree" ||
		source.Kind != "git_worktree" ||
		source.BaseRef != "HEAD" ||
		!strings.HasPrefix(source.DiffHash, "sha256:") ||
		!source.Truncated ||
		source.TotalBytes != int64(len(source.Diff)) ||
		len(source.Diff) != reviewDiffOutputLimit {
		t.Fatalf("source = %+v", source)
	}
	if !strings.Contains(source.Diff, "tracked.txt") ||
		strings.Contains(source.Diff, "untracked.txt") {
		t.Fatalf("unexpected diff projection: %q", source.Diff[:min(len(source.Diff), 1000)])
	}
}

func TestReviewDiffScopesChangesToOwnedSessionDirectory(t *testing.T) {
	root := t.TempDir()
	owned := filepath.Join(root, "owned")
	sibling := filepath.Join(root, "sibling")
	if err := os.MkdirAll(owned, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	runReviewGit(t, root, "init", "-q")
	runReviewGit(t, root, "config", "user.email", "desktop@example.com")
	runReviewGit(t, root, "config", "user.name", "Desktop Test")
	if err := os.WriteFile(filepath.Join(owned, "owned.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "private.txt"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runReviewGit(t, root, "add", ".")
	runReviewGit(t, root, "commit", "-qm", "initial")
	if err := os.WriteFile(filepath.Join(owned, "owned.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "private.txt"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	create := doJSON(
		t,
		httpServer.URL+"/v1/sessions",
		"test-token",
		http.MethodPost,
		map[string]string{"workspace_handle": registerWorkspace(t, httpServer.URL, "test-token", owned).WorkspaceHandle},
	)
	var summary SessionSummary
	decodeResponse(t, create, &summary)
	_ = create.Body.Close()
	response := getBearer(
		t,
		httpServer.URL+"/v1/sessions/"+summary.ID+"/review-diff",
		"test-token",
	)
	var review ReviewDiffResponse
	decodeResponse(t, response, &review)
	_ = response.Body.Close()
	if len(review.Sources) != 1 {
		t.Fatalf("review sources = %+v", review.Sources)
	}
	diff := review.Sources[0].Diff
	if !strings.Contains(diff, "owned/owned.txt") ||
		strings.Contains(diff, "sibling/private.txt") {
		t.Fatalf("session-scoped diff = %q", diff)
	}
}

func TestReviewDiffReturnsEmptyForNonGitAndRejectsInvalidQuery(t *testing.T) {
	server, err := New(Config{
		Token: "test-token",
		Factory: func(_ context.Context, input EngineOptions) (SessionEngine, error) {
			return newFakeSessionEngine(input, false), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer shutdownTestServer(t, server)

	create := doJSON(
		t,
		httpServer.URL+"/v1/sessions",
		"test-token",
		http.MethodPost,
		map[string]string{"workspace_handle": registerWorkspace(t, httpServer.URL, "test-token", t.TempDir()).WorkspaceHandle},
	)
	var summary SessionSummary
	decodeResponse(t, create, &summary)
	_ = create.Body.Close()

	nonGit := getBearer(
		t,
		httpServer.URL+"/v1/sessions/"+summary.ID+"/review-diff",
		"test-token",
	)
	var review ReviewDiffResponse
	decodeResponse(t, nonGit, &review)
	_ = nonGit.Body.Close()
	if len(review.Sources) != 0 {
		t.Fatalf("non-Git review = %+v", review)
	}

	invalid := getBearer(
		t,
		httpServer.URL+"/v1/sessions/"+summary.ID+
			"/review-diff?ignore_whitespace=sometimes",
		"test-token",
	)
	if invalid.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid query = %d: %s", invalid.StatusCode, readBody(t, invalid))
	}
	_ = invalid.Body.Close()

	missing := getBearer(
		t,
		httpServer.URL+
			"/v1/sessions/33333333-3333-4333-8333-333333333333/review-diff",
		"test-token",
	)
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing session = %d: %s", missing.StatusCode, readBody(t, missing))
	}
	_ = missing.Body.Close()
}

func TestBoundedDigestWriterCancelsAtLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	writer := newBoundedDigestWriter(4, cancel)
	written, err := writer.Write([]byte("abcdef"))
	if err != nil || written != 6 {
		t.Fatalf("write = %d, %v", written, err)
	}
	if ctx.Err() != context.Canceled ||
		writer.buffer.String() != "abcd" ||
		writer.total != 4 ||
		!writer.truncated {
		t.Fatalf(
			"writer = content %q, total %d, truncated %v, ctx %v",
			writer.buffer.String(),
			writer.total,
			writer.truncated,
			ctx.Err(),
		)
	}
}

func TestReviewGitEnvironmentDropsRepositoryOverrides(t *testing.T) {
	t.Setenv("GIT_DIR", "/tmp/not-the-owned-repository")
	t.Setenv("GIT_WORK_TREE", "/tmp/not-the-owned-worktree")
	t.Setenv("GIT_EXTERNAL_DIFF", "/tmp/not-an-executable")
	t.Setenv("PAGER", "not-a-pager")
	t.Setenv("LC_ALL", "not-a-locale")

	for _, value := range reviewGitEnvironment() {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "GIT_") || key == "PAGER" || key == "LC_ALL" {
			t.Fatalf("unsafe inherited Git environment: %q", value)
		}
	}
}

func runReviewGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
