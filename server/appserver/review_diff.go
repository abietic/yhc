package appserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	reviewDiffTimeout     = 5 * time.Second
	reviewDiffOutputLimit = 1 << 20
	reviewGitMetaLimit    = 64 << 10
	reviewGitErrorLimit   = 16 << 10
)

type boundedDigestWriter struct {
	buffer    bytes.Buffer
	digest    hash.Hash
	limit     int
	total     int64
	truncated bool
	cancel    context.CancelFunc
}

func newBoundedDigestWriter(
	limit int,
	cancel context.CancelFunc,
) *boundedDigestWriter {
	return &boundedDigestWriter{
		digest: sha256.New(),
		limit:  limit,
		cancel: cancel,
	}
}

func (w *boundedDigestWriter) Write(value []byte) (int, error) {
	remaining := w.limit - w.buffer.Len()
	if remaining <= 0 {
		if len(value) > 0 {
			w.truncated = true
			w.cancel()
		}
		return len(value), nil
	}
	keep := len(value)
	if keep > remaining {
		keep = remaining
	}
	_, _ = w.buffer.Write(value[:keep])
	_, _ = w.digest.Write(value[:keep])
	w.total += int64(keep)
	if keep < len(value) {
		w.truncated = true
		w.cancel()
	}
	return len(value), nil
}

func (w *boundedDigestWriter) sum() string {
	return "sha256:" + hex.EncodeToString(w.digest.Sum(nil))
}

type gitOutput struct {
	text      string
	hash      string
	total     int64
	truncated bool
}

func (s *Server) handleReviewDiff(w http.ResponseWriter, r *http.Request) {
	owned, ok := s.getSession(r.PathValue("session_id"))
	if !ok {
		writeError(w, http.StatusNotFound, "session_not_found", "session not found")
		return
	}
	ignoreWhitespace := false
	if raw := strings.TrimSpace(r.URL.Query().Get("ignore_whitespace")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(
				w,
				http.StatusBadRequest,
				"invalid_ignore_whitespace",
				"ignore_whitespace must be true or false",
			)
			return
		}
		ignoreWhitespace = parsed
	}

	summary := owned.summary()
	ctx, cancel := context.WithTimeout(r.Context(), reviewDiffTimeout)
	defer cancel()
	sources, err := reviewSources(ctx, summary.CWD, ignoreWhitespace)
	if err != nil {
		writeError(
			w,
			http.StatusServiceUnavailable,
			"review_diff_unavailable",
			"could not inspect workspace changes",
		)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, ReviewDiffResponse{
		CWD:         summary.CWD,
		GeneratedAt: s.now().UTC(),
		Sources:     sources,
	})
}

func reviewSources(
	ctx context.Context,
	cwd string,
	ignoreWhitespace bool,
) ([]ReviewDiffSource, error) {
	probe, err := runGit(ctx, cwd, reviewGitMetaLimit, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return []ReviewDiffSource{}, nil
		}
		return nil, err
	}
	if strings.TrimSpace(probe.text) != "true" {
		return []ReviewDiffSource{}, nil
	}

	rootOutput, err := runGit(ctx, cwd, reviewGitMetaLimit, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	root, err := validateCWD(strings.TrimSpace(rootOutput.text))
	if err != nil {
		return nil, fmt.Errorf("validate Git worktree root: %w", err)
	}
	relative, err := filepath.Rel(root, cwd)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("git worktree root does not own the session cwd")
	}

	headOutput, err := runGit(ctx, root, reviewGitMetaLimit, "rev-parse", "--verify", "HEAD")
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return []ReviewDiffSource{}, nil
		}
		return nil, err
	}
	headRef := strings.TrimSpace(headOutput.text)
	args := []string{
		"diff",
		"--no-ext-diff",
		"--no-textconv",
		"--no-color",
		"--full-index",
		"--find-renames",
	}
	if ignoreWhitespace {
		args = append(args, "--ignore-all-space")
	}
	args = append(args, "HEAD", "--", ".")
	diff, err := runGit(ctx, cwd, reviewDiffOutputLimit, args...)
	if err != nil {
		return nil, err
	}
	return []ReviewDiffSource{{
		ID:             "worktree",
		Kind:           "git_worktree",
		RepositoryRoot: root,
		BaseRef:        "HEAD",
		HeadRef:        headRef,
		Diff:           diff.text,
		DiffHash:       diff.hash,
		TotalBytes:     diff.total,
		Truncated:      diff.truncated,
	}}, nil
}

func runGit(ctx context.Context, cwd string, limit int, args ...string) (gitOutput, error) {
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stdout := newBoundedDigestWriter(limit, cancel)
	stderr := newBoundedDigestWriter(reviewGitErrorLimit, cancel)
	command := exec.CommandContext(commandCtx, "git", append([]string{"-C", cwd}, args...)...)
	command.Env = append(
		reviewGitEnvironment(),
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_PAGER=cat",
		"PAGER=cat",
		"LC_ALL=C",
	)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		if stdout.truncated && ctx.Err() == nil {
			return gitOutput{
				text:      stdout.buffer.String(),
				hash:      stdout.sum(),
				total:     stdout.total,
				truncated: true,
			}, nil
		}
		if ctx.Err() != nil {
			return gitOutput{}, ctx.Err()
		}
		if stderr.truncated {
			return gitOutput{}, fmt.Errorf("git %s exceeded the error output limit", args[0])
		}
		detail := strings.TrimSpace(stderr.buffer.String())
		if detail == "" {
			return gitOutput{}, fmt.Errorf("git %s: %w", args[0], err)
		}
		return gitOutput{}, fmt.Errorf("git %s: %w: %s", args[0], err, detail)
	}
	return gitOutput{
		text:      stdout.buffer.String(),
		hash:      stdout.sum(),
		total:     stdout.total,
		truncated: stdout.truncated,
	}, nil
}

func reviewGitEnvironment() []string {
	current := os.Environ()
	clean := make([]string, 0, len(current))
	for _, value := range current {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "GIT_") ||
			key == "PAGER" ||
			key == "LC_ALL" {
			continue
		}
		clean = append(clean, value)
	}
	return clean
}
