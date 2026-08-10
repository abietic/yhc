package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const defaultGitOutputLimit = 16 << 20

var gitObjectPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

type GitSource interface {
	Resolve(ctx context.Context, rev string) (string, error)
	MergeBase(ctx context.Context, left, right string) (string, error)
	NameStatus(ctx context.Context, base, head string) ([]byte, error)
	BinaryDiff(ctx context.Context, base, head string) ([]byte, error)
	UntrackedCount(ctx context.Context) (int, error)
}

type trackedWorktreeSource interface {
	TrackedWorktreeClean(ctx context.Context) (bool, error)
}

type worktreeHeadTreeSource struct {
	source  TreeSource
	head    string
	changed map[string]struct{}
}

func (source worktreeHeadTreeSource) ListFiles(ctx context.Context, revision string) ([]string, error) {
	if revision == source.head {
		return source.source.ListFiles(ctx, "")
	}
	return source.source.ListFiles(ctx, revision)
}

func (source worktreeHeadTreeSource) ReadFile(
	ctx context.Context,
	revision string,
	name string,
) ([]byte, error) {
	if revision == source.head {
		if _, changed := source.changed[name]; changed {
			return source.source.ReadFile(ctx, "", name)
		}
	}
	return source.source.ReadFile(ctx, revision, name)
}

type GitChange struct {
	Status string
	Path   string
}

type GitSnapshot struct {
	BaseRef          string
	Base             string
	Head             string
	DiffDigest       string
	Changed          []GitChange
	OutsideUntracked int
}

func resolveSnapshot(
	ctx context.Context,
	root string,
	baseRef string,
	head string,
	source GitSource,
) (GitSnapshot, error) {
	if strings.TrimSpace(root) == "" {
		return GitSnapshot{}, errors.New("repository root is required")
	}
	if strings.TrimSpace(baseRef) == "" {
		return GitSnapshot{}, errors.New("base ref is required")
	}
	if source == nil {
		return GitSnapshot{}, errors.New("git source is required")
	}
	if head != "" {
		checker, ok := source.(trackedWorktreeSource)
		if !ok {
			return GitSnapshot{}, errors.New("explicit head requires tracked worktree inspection")
		}
		clean, err := checker.TrackedWorktreeClean(ctx)
		if err != nil {
			return GitSnapshot{}, fmt.Errorf("inspect tracked worktree: %w", err)
		}
		if !clean {
			return GitSnapshot{}, errors.New("explicit head requires a clean comparison; tracked worktree is dirty")
		}
	}

	headRevision := "HEAD"
	if head != "" {
		headRevision = head
	}
	resolvedHead, err := source.Resolve(ctx, headRevision)
	if err != nil {
		return GitSnapshot{}, fmt.Errorf("resolve head: %w", err)
	}
	if resolvedHead == "" {
		return GitSnapshot{}, errors.New("resolve head: empty commit")
	}
	base, err := source.MergeBase(ctx, baseRef, resolvedHead)
	if err != nil {
		return GitSnapshot{}, fmt.Errorf("resolve merge base: %w", err)
	}
	if base == "" {
		return GitSnapshot{}, errors.New("resolve merge base: empty commit")
	}

	comparisonHead := ""
	if head != "" {
		comparisonHead = resolvedHead
	}
	nameStatus, err := source.NameStatus(ctx, base, comparisonHead)
	if err != nil {
		return GitSnapshot{}, fmt.Errorf("read changed paths: %w", err)
	}
	changed, err := parseNameStatus(nameStatus)
	if err != nil {
		return GitSnapshot{}, fmt.Errorf("parse changed paths: %w", err)
	}
	binaryDiff, err := source.BinaryDiff(ctx, base, comparisonHead)
	if err != nil {
		return GitSnapshot{}, fmt.Errorf("read tracked diff: %w", err)
	}
	digest := sha256.Sum256(binaryDiff)

	untracked := 0
	if head == "" {
		untracked, err = source.UntrackedCount(ctx)
		if err != nil {
			return GitSnapshot{}, fmt.Errorf("count untracked paths: %w", err)
		}
		if untracked < 0 {
			return GitSnapshot{}, errors.New("count untracked paths: negative result")
		}
	}

	return GitSnapshot{
		BaseRef:          baseRef,
		Base:             base,
		Head:             resolvedHead,
		DiffDigest:       hex.EncodeToString(digest[:]),
		Changed:          changed,
		OutsideUntracked: untracked,
	}, nil
}

func parseNameStatus(data []byte) ([]GitChange, error) {
	if len(data) == 0 {
		return nil, nil
	}
	if data[len(data)-1] != 0 {
		return nil, errors.New("truncated NUL-delimited name-status output")
	}
	fields := bytes.Split(data[:len(data)-1], []byte{0})
	changes := make([]GitChange, 0, len(fields)/2)
	seen := make(map[string]struct{})
	for index := 0; index < len(fields); {
		status := string(fields[index])
		index++
		if !validGitStatus(status) {
			return nil, fmt.Errorf("invalid git status %q", status)
		}

		if status[0] == 'R' || status[0] == 'C' {
			if index+1 >= len(fields) {
				return nil, fmt.Errorf("truncated %s record", status)
			}
			from, err := normalizeGitPath(string(fields[index]))
			if err != nil {
				return nil, err
			}
			to, err := normalizeGitPath(string(fields[index+1]))
			if err != nil {
				return nil, err
			}
			index += 2
			for _, change := range []GitChange{
				{Status: status + "-from", Path: from},
				{Status: status + "-to", Path: to},
			} {
				if err := appendUniqueChange(&changes, seen, change); err != nil {
					return nil, err
				}
			}
			continue
		}

		if index >= len(fields) {
			return nil, fmt.Errorf("truncated %s record", status)
		}
		name, err := normalizeGitPath(string(fields[index]))
		if err != nil {
			return nil, err
		}
		index++
		if err := appendUniqueChange(&changes, seen, GitChange{Status: status, Path: name}); err != nil {
			return nil, err
		}
	}
	return changes, nil
}

func validGitStatus(status string) bool {
	if len(status) == 0 || len(status) > 16 {
		return false
	}
	for index, character := range status {
		if index == 0 {
			if character < 'A' || character > 'Z' {
				return false
			}
			continue
		}
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func normalizeGitPath(name string) (string, error) {
	if name == "" || strings.Contains(name, "\x00") || strings.Contains(name, `\`) || path.IsAbs(name) {
		return "", fmt.Errorf("invalid repository path %q", name)
	}
	clean := path.Clean(name)
	if clean != name || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid repository path %q", name)
	}
	return clean, nil
}

func appendUniqueChange(changes *[]GitChange, seen map[string]struct{}, change GitChange) error {
	key := change.Status + "\x00" + change.Path
	if _, ok := seen[key]; ok {
		return fmt.Errorf("duplicate changed path/status %q %q", change.Status, change.Path)
	}
	seen[key] = struct{}{}
	*changes = append(*changes, change)
	return nil
}

type commandGitSource struct {
	root        string
	outputLimit int
}

func (source commandGitSource) Resolve(ctx context.Context, rev string) (string, error) {
	if err := source.ensureReady(ctx); err != nil {
		return "", err
	}
	if strings.TrimSpace(rev) == "" {
		return "", errors.New("git revision is required")
	}
	output, err := source.run(ctx, "rev-parse", "--verify", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve git revision %q: %w", rev, err)
	}
	commit := strings.TrimSpace(string(output))
	if commit == "" {
		return "", fmt.Errorf("resolve git revision %q: empty result", rev)
	}
	return commit, nil
}

func (source commandGitSource) MergeBase(ctx context.Context, left, right string) (string, error) {
	if err := source.ensureReady(ctx); err != nil {
		return "", err
	}
	output, err := source.run(ctx, "merge-base", left, right)
	if err != nil {
		return "", fmt.Errorf("resolve merge base: %w", err)
	}
	commit := strings.TrimSpace(string(output))
	if commit == "" {
		return "", errors.New("resolve merge base: empty result")
	}
	return commit, nil
}

func (source commandGitSource) NameStatus(ctx context.Context, base, head string) ([]byte, error) {
	if err := source.ensureReady(ctx); err != nil {
		return nil, err
	}
	args := []string{"diff", "--name-status", "-z", "--find-renames", "--no-ext-diff", base}
	if head != "" {
		args = append(args, head)
	}
	args = append(args, "--")
	return source.run(ctx, args...)
}

func (source commandGitSource) BinaryDiff(ctx context.Context, base, head string) ([]byte, error) {
	if err := source.ensureReady(ctx); err != nil {
		return nil, err
	}
	args := []string{"diff", "--binary", "--full-index", "--no-ext-diff", base}
	if head != "" {
		args = append(args, head)
	}
	args = append(args, "--")
	return source.run(ctx, args...)
}

func (source commandGitSource) UntrackedCount(ctx context.Context) (int, error) {
	if err := source.ensureReady(ctx); err != nil {
		return 0, err
	}
	output, err := source.run(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return 0, err
	}
	if len(output) == 0 {
		return 0, nil
	}
	if output[len(output)-1] != 0 {
		return 0, errors.New("truncated NUL-delimited untracked output")
	}
	fields := bytes.Split(output[:len(output)-1], []byte{0})
	for _, field := range fields {
		if _, err := normalizeGitPath(string(field)); err != nil {
			return 0, err
		}
	}
	return len(fields), nil
}

func (source commandGitSource) TrackedWorktreeClean(ctx context.Context) (bool, error) {
	if err := source.ensureReady(ctx); err != nil {
		return false, err
	}
	for _, args := range [][]string{
		{"diff", "--quiet", "--no-ext-diff", "--"},
		{"diff", "--cached", "--quiet", "--no-ext-diff", "--"},
	} {
		clean, err := source.runQuietStatus(ctx, args...)
		if err != nil || !clean {
			return clean, err
		}
	}
	return true, nil
}

func (source commandGitSource) ListFiles(ctx context.Context, revision string) ([]string, error) {
	if err := source.ensureReady(ctx); err != nil {
		return nil, err
	}
	var (
		output []byte
		err    error
	)
	if revision == "" {
		output, err = source.run(ctx, "ls-files", "-z")
	} else {
		if !gitObjectPattern.MatchString(revision) {
			return nil, errors.New("tree revision must be a resolved commit")
		}
		output, err = source.run(ctx, "ls-tree", "-r", "-z", "--name-only", revision, "--")
	}
	if err != nil {
		return nil, err
	}
	names, err := parseTreePaths(output)
	if err != nil {
		return nil, err
	}
	if revision != "" {
		return names, nil
	}

	root, err := os.OpenRoot(source.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	current := names[:0]
	for _, name := range names {
		_, statErr := root.Lstat(name)
		switch {
		case statErr == nil:
			current = append(current, name)
		case errors.Is(statErr, os.ErrNotExist):
			continue
		default:
			return nil, fmt.Errorf("inspect tracked path %q: %w", name, statErr)
		}
	}
	return current, nil
}

func (source commandGitSource) ReadFile(
	ctx context.Context,
	revision string,
	name string,
) ([]byte, error) {
	if err := validateRepositoryPath(name); err != nil {
		return nil, err
	}
	if revision != "" {
		if !gitObjectPattern.MatchString(revision) {
			return nil, errors.New("tree revision must be a resolved commit")
		}
		return source.run(ctx, "show", revision+":"+name)
	}

	root, err := os.OpenRoot(source.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, errors.New("tracked Go source is not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, errors.New("tracked Go source changed during open")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxGoSourceFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxGoSourceFileBytes {
		return nil, fmt.Errorf("tracked Go source exceeds %d bytes", maxGoSourceFileBytes)
	}
	return data, nil
}

func parseTreePaths(output []byte) ([]string, error) {
	if len(output) == 0 {
		return nil, nil
	}
	if output[len(output)-1] != 0 {
		return nil, errors.New("truncated NUL-delimited tree output")
	}
	fields := bytes.Split(output[:len(output)-1], []byte{0})
	names := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		name, err := normalizeGitPath(string(field))
		if err != nil {
			return nil, err
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("duplicate tree path %q", name)
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

func (source commandGitSource) ensureReady(ctx context.Context) error {
	for _, marker := range []string{"MERGE_HEAD", "rebase-merge", "rebase-apply"} {
		output, err := source.runUnchecked(ctx, "rev-parse", "--git-path", marker)
		if err != nil {
			return fmt.Errorf("inspect repository state: %w", err)
		}
		markerPath := strings.TrimSpace(string(output))
		if markerPath == "" {
			return errors.New("inspect repository state: empty git path")
		}
		if !filepath.IsAbs(markerPath) {
			markerPath = filepath.Join(source.root, markerPath)
		}
		_, statErr := os.Stat(markerPath)
		switch {
		case statErr == nil:
			return fmt.Errorf("repository has an in-progress merge or rebase (%s)", marker)
		case errors.Is(statErr, os.ErrNotExist):
			continue
		default:
			return fmt.Errorf("inspect repository state: %w", statErr)
		}
	}
	return nil
}

func (source commandGitSource) run(ctx context.Context, args ...string) ([]byte, error) {
	return source.runUnchecked(ctx, args...)
}

func (source commandGitSource) runUnchecked(ctx context.Context, args ...string) ([]byte, error) {
	if strings.TrimSpace(source.root) == "" {
		return nil, errors.New("git source root is required")
	}
	limit := source.outputLimit
	if limit <= 0 {
		limit = defaultGitOutputLimit
	}
	stdout := newBoundedCommandBuffer(limit)
	stderr := newBoundedCommandBuffer(limit)
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = source.root
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if stdout.overflow || stderr.overflow {
		return nil, fmt.Errorf("git output exceeds %d bytes", limit)
	}
	if err != nil {
		exitCode := -1
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exitCode = exitError.ExitCode()
		}
		return nil, fmt.Errorf("git command failed with exit code %s: %w", strconv.Itoa(exitCode), err)
	}
	return append([]byte(nil), stdout.buffer.Bytes()...), nil
}

func (source commandGitSource) runQuietStatus(ctx context.Context, args ...string) (bool, error) {
	if strings.TrimSpace(source.root) == "" {
		return false, errors.New("git source root is required")
	}
	limit := source.outputLimit
	if limit <= 0 {
		limit = defaultGitOutputLimit
	}
	stdout := newBoundedCommandBuffer(limit)
	stderr := newBoundedCommandBuffer(limit)
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = source.root
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if stdout.overflow || stderr.overflow {
		return false, fmt.Errorf("git output exceeds %d bytes", limit)
	}
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git worktree inspection failed: %w", err)
}

type boundedCommandBuffer struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func newBoundedCommandBuffer(limit int) *boundedCommandBuffer {
	return &boundedCommandBuffer{limit: limit}
}

func (buffer *boundedCommandBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.overflow = true
		return written, nil
	}
	if len(data) > remaining {
		buffer.overflow = true
		data = data[:remaining]
	}
	_, _ = buffer.buffer.Write(data)
	return written, nil
}
