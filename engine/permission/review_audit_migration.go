package permission

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

const (
	reviewAuditStateRel          = "permission-review-audit/v1"
	reviewAuditMigrationMaxFiles = defaultReviewAuditMaxSegments + 2
	reviewAuditMigrationMaxBytes = int64(defaultReviewAuditMaxSegments) *
		(defaultReviewAuditSegmentMaxBytes + reviewAuditMaxLineBytes)
)

// ErrReviewAuditMigrationUnavailable reports that an exact audit override
// owns the effective path and therefore the default legacy root is ineligible.
var ErrReviewAuditMigrationUnavailable = errors.New("review audit migration is unavailable")

// ReviewAuditMigrationSpec returns the exact default-root audit artifact. It
// never traverses another YHC, Eino Agent, or Claude state subtree.
func ReviewAuditMigrationSpec(roots statepath.Roots) (statemigration.ArtifactSpec, error) {
	selection, err := statepath.ResolveOverride(
		identity.RuntimeEnvPermissionReviewAuditDir.Pair(),
		statepath.Roots{
			Canonical: filepath.Join(roots.Canonical, filepath.FromSlash(reviewAuditStateRel)),
			Legacy:    filepath.Join(roots.Legacy, filepath.FromSlash(reviewAuditStateRel)),
		},
	)
	if err != nil || !selection.Migratable {
		return statemigration.ArtifactSpec{}, ErrReviewAuditMigrationUnavailable
	}
	return statemigration.ArtifactSpec{
		Owner:              "permission-review-audit",
		Scope:              "user",
		SourceRel:          reviewAuditStateRel,
		TargetRel:          reviewAuditStateRel,
		Kind:               statemigration.DirectoryTree,
		LegacyMode:         statemigration.LegacyPrivate,
		MaxFiles:           reviewAuditMigrationMaxFiles,
		MaxBytes:           reviewAuditMigrationMaxBytes,
		Validate:           validateReviewAuditMigrationSnapshot,
		Stage:              stageReviewAuditMigration,
		Quiescent:          reviewAuditMigrationQuiescent,
		AcquireSourceLease: acquireReviewAuditMigrationLease,
	}, nil
}

func validateReviewAuditMigrationSnapshot(
	_ context.Context,
	snapshot statemigration.Snapshot,
) error {
	hasActive := false
	hasNumbered := false
	err := snapshot.Walk(func(relative string, entry fs.DirEntry) error {
		if relative == "." {
			if !entry.IsDir() {
				return errors.New("review audit migration: artifact root is not a directory")
			}
			return nil
		}
		if entry.IsDir() || strings.Contains(relative, "/") {
			return errors.New("review audit migration: nested or unknown entry")
		}
		switch relative {
		case reviewAuditLockGuardName, reviewAuditLockName:
			reader, info, err := snapshot.Open(relative)
			if err != nil {
				return errors.New("review audit migration: invalid coordination file")
			}
			closeErr := reader.Close()
			if closeErr != nil || info.Size() != 0 {
				return errors.New("review audit migration: invalid coordination file")
			}
			return nil
		default:
			index, ok := reviewAuditSegmentIndex(relative)
			if !ok {
				return errors.New("review audit migration: unknown entry")
			}
			if index == 0 {
				hasActive = true
			} else {
				hasNumbered = true
			}
			return validateReviewAuditMigrationSegment(snapshot, relative, index == 0)
		}
	})
	if err != nil {
		return err
	}
	if hasNumbered && !hasActive {
		return errors.New("review audit migration: numbered segment without active segment")
	}
	return nil
}

func validateReviewAuditMigrationSegment(
	snapshot statemigration.Snapshot,
	name string,
	active bool,
) error {
	reader, _, err := snapshot.Open(name)
	if err != nil {
		return errors.New("review audit migration: open segment")
	}
	data, readErr := io.ReadAll(io.LimitReader(reader, defaultReviewAuditSegmentMaxBytes+reviewAuditMaxLineBytes+1))
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || len(data) > defaultReviewAuditSegmentMaxBytes+reviewAuditMaxLineBytes {
		return errors.New("review audit migration: segment exceeds bound")
	}
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	partial := len(data) > 0 && !bytes.HasSuffix(data, []byte("\n"))
	for index, raw := range lines {
		if partial && index == len(lines)-1 {
			if !active || !safeReviewAuditMigrationTail(raw) {
				return errors.New("review audit migration: unsafe partial tail")
			}
			continue
		}
		if len(raw)+1 > reviewAuditMaxLineBytes {
			return errors.New("review audit migration: record exceeds bound")
		}
		if _, err := decodeReviewAuditRecord(raw); err != nil {
			return errors.New("review audit migration: invalid record")
		}
	}
	return nil
}

func safeReviewAuditMigrationTail(raw []byte) bool {
	if len(raw) == 0 || len(raw)+1 > reviewAuditMaxLineBytes ||
		!utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 || bytes.IndexByte(raw, '\n') >= 0 ||
		!bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return false
	}
	lower := strings.ToLower(string(raw))
	for _, forbidden := range []string{
		`"input"`, `"path"`, `"credential`, `"password`, `"secret`,
		`"api_key`, `"access_token`, `"refresh_token`, `"session`,
		`"transcript`, `"agent_id`,
	} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	return true
}

func reviewAuditMigrationQuiescent(
	_ context.Context,
	snapshot statemigration.Snapshot,
) (bool, error) {
	hasGuard := false
	hasSentinel := false
	if err := snapshot.Walk(func(relative string, _ fs.DirEntry) error {
		switch relative {
		case reviewAuditLockGuardName:
			hasGuard = true
		case reviewAuditLockName:
			hasSentinel = true
		}
		return nil
	}); err != nil {
		return false, err
	}
	return hasGuard && !hasSentinel, nil
}

func stageReviewAuditMigration(
	_ context.Context,
	snapshot statemigration.Snapshot,
	stage *os.Root,
) error {
	return snapshot.Walk(func(relative string, entry fs.DirEntry) error {
		if relative == "." || relative == reviewAuditLockGuardName || relative == reviewAuditLockName {
			return nil
		}
		if entry.IsDir() {
			return errors.New("review audit migration: unexpected directory")
		}
		if _, ok := reviewAuditSegmentIndex(relative); !ok {
			return errors.New("review audit migration: unexpected entry")
		}
		input, _, err := snapshot.Open(relative)
		if err != nil {
			return errors.New("review audit migration: open source segment")
		}
		output, err := stage.OpenFile(relative, os.O_CREATE|os.O_EXCL|os.O_WRONLY, reviewAuditSegmentMode)
		if err != nil {
			_ = input.Close()
			return errors.New("review audit migration: create staged segment")
		}
		_, copyErr := io.Copy(output, input)
		closeErr := errors.Join(output.Sync(), output.Close(), input.Close())
		return errors.Join(copyErr, closeErr)
	})
}

func acquireReviewAuditMigrationLease(
	ctx context.Context,
	dir string,
) (func(), bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	root, exists, err := openReviewAuditRoot(dir)
	if err != nil || !exists {
		if root != nil {
			_ = root.Close()
		}
		return nil, false, err
	}
	guardInfo, err := root.Lstat(reviewAuditLockGuardName)
	if os.IsNotExist(err) {
		_ = root.Close()
		return nil, false, nil
	}
	if err != nil || checkRegularLock(guardInfo) != nil {
		_ = root.Close()
		return nil, false, err
	}
	guard, err := root.OpenFile(reviewAuditLockGuardName, os.O_RDWR, 0)
	if err != nil {
		_ = root.Close()
		return nil, false, err
	}
	opened, statErr := guard.Stat()
	current, pathErr := root.Lstat(reviewAuditLockGuardName)
	if statErr != nil || pathErr != nil || !os.SameFile(guardInfo, opened) ||
		!os.SameFile(guardInfo, current) || checkRegularLock(opened) != nil ||
		checkRegularLock(current) != nil {
		_ = guard.Close()
		_ = root.Close()
		return nil, false, errors.New("review audit migration: coordination identity changed")
	}
	locked, err := tryLockReviewAuditGuard(guard)
	if err != nil || !locked {
		_ = guard.Close()
		_ = root.Close()
		return nil, false, err
	}
	release := func() {
		_ = unlockReviewAuditGuard(guard)
		_ = guard.Close()
		_ = root.Close()
	}
	if lockInfo, lockErr := root.Lstat(reviewAuditLockName); lockErr == nil {
		if checkRegularLock(lockInfo) != nil {
			release()
			return nil, false, errors.New("review audit migration: invalid lock sentinel")
		}
		release()
		return nil, false, nil
	} else if !os.IsNotExist(lockErr) {
		release()
		return nil, false, lockErr
	}
	return release, true, nil
}

func reviewAuditSegmentIndex(name string) (int, bool) {
	if name == reviewAuditActiveSegment {
		return 0, true
	}
	if !strings.HasPrefix(name, "events.") || !strings.HasSuffix(name, ".jsonl") {
		return 0, false
	}
	raw := strings.TrimSuffix(strings.TrimPrefix(name, "events."), ".jsonl")
	index, err := strconv.Atoi(raw)
	return index, err == nil && index > 0 && index < defaultReviewAuditMaxSegments && strconv.Itoa(index) == raw
}

func isReviewAuditSegmentName(name string) bool {
	_, ok := reviewAuditSegmentIndex(name)
	return ok
}
