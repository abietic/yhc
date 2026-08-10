package permission

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/abietic/yhc/internal/identity"
	"github.com/abietic/yhc/internal/statemigration"
	"github.com/abietic/yhc/internal/statepath"
)

// Review audit journal for the P22.2b shadow-measurement slice.
//
// The journal is explicit local-user storage. It is never permission
// authority: records are redacted typed measurements only and contain no raw
// inputs, paths, digests, nonces, credentials, transcript, session, or agent
// identifiers.

// ReviewAuditSchemaVersion is the only accepted record schema version.
const ReviewAuditSchemaVersion = 1

const (
	reviewAuditActiveSegment      = "events.jsonl"
	reviewAuditNumberedSegmentFmt = "events.%d.jsonl"
	reviewAuditLockName           = "events.lock"
	reviewAuditLockGuardName      = "events.lock.guard"

	defaultReviewAuditSegmentMaxBytes = 1 << 20 // 1 MiB
	defaultReviewAuditMaxSegments     = 8
	defaultReviewAuditLockTimeout     = 2 * time.Second
	defaultReviewAuditStaleLock       = 30 * time.Second

	// reviewAuditMaxLineBytes bounds one encoded record line.
	reviewAuditMaxLineBytes = 16 << 10 // 16 KiB

	reviewAuditDirMode     = 0o700
	reviewAuditSegmentMode = 0o600
)

// ReviewAuditKind identifies the typed measurement a record carries.
type ReviewAuditKind string

const (
	ReviewAuditKindEligible             ReviewAuditKind = "eligible"
	ReviewAuditKindAttempt              ReviewAuditKind = "attempt"
	ReviewAuditKindTerminal             ReviewAuditKind = "terminal"
	ReviewAuditKindComparison           ReviewAuditKind = "comparison"
	ReviewAuditKindCorpusGroundTruth    ReviewAuditKind = "corpus_ground_truth"
	ReviewAuditKindStorageRecovery      ReviewAuditKind = "storage_recovery"
	ReviewAuditKindDispatcherDiagnostic ReviewAuditKind = "dispatcher_diagnostic"
)

// ReviewAuditDispatcherDiagnostic identifies one typed dispatcher evidence
// loss or lifecycle counter. It never carries raw permission inputs.
type ReviewAuditDispatcherDiagnostic string

const (
	ReviewAuditDiagnosticEnqueueDrop ReviewAuditDispatcherDiagnostic = "enqueue_drop"
	ReviewAuditDiagnosticSinkFailure ReviewAuditDispatcherDiagnostic = "sink_failure"
	ReviewAuditDiagnosticFlushExpiry ReviewAuditDispatcherDiagnostic = "shutdown_flush_expiry"
	ReviewAuditDiagnosticAfterClose  ReviewAuditDispatcherDiagnostic = "enqueue_after_close"
)

// ReviewAuditRecord is one typed redacted journal entry. Only the fields
// below exist; there are no raw or free-form payload fields.
type ReviewAuditRecord struct {
	SchemaVersion        int                             `json:"schema_version"`
	EventID              string                          `json:"event_id"`
	OccurredAt           time.Time                       `json:"occurred_at"`
	Kind                 ReviewAuditKind                 `json:"kind"`
	CanonicalTool        string                          `json:"canonical_tool,omitempty"`
	ActionKind           string                          `json:"action_kind,omitempty"`
	DeterministicClass   string                          `json:"deterministic_class,omitempty"`
	ReviewerStatus       string                          `json:"reviewer_status,omitempty"`
	ReviewerDecision     string                          `json:"reviewer_decision,omitempty"`
	ReasonCode           string                          `json:"reason_code,omitempty"`
	LatencyMS            int64                           `json:"latency_ms,omitempty"`
	Provider             string                          `json:"provider,omitempty"`
	Model                string                          `json:"model,omitempty"`
	DataBoundary         string                          `json:"data_boundary,omitempty"`
	ComparisonSource     string                          `json:"comparison_source,omitempty"`
	ExpectedDecision     string                          `json:"expected_decision,omitempty"`
	CorpusID             string                          `json:"corpus_id,omitempty"`
	CorpusCaseID         string                          `json:"corpus_case_id,omitempty"`
	RecoveredBytes       int64                           `json:"recovered_bytes,omitempty"`
	DispatcherDiagnostic ReviewAuditDispatcherDiagnostic `json:"dispatcher_diagnostic,omitempty"`
	DiagnosticCount      uint64                          `json:"diagnostic_count,omitempty"`
}

// ReviewAuditSink is the consumer-facing append contract. Implementations
// should honor context cancellation. QueryEngine serializes calls through a
// bounded dispatcher, so sink latency or failure never settles or blocks a
// permission request; the sink only records measurements.
type ReviewAuditSink interface {
	Record(ctx context.Context, record ReviewAuditRecord) error
}

// ReviewAuditStoreOptions configures a ReviewAuditStore. Zero values select
// the frozen defaults (1 MiB segments, 8 segments, 2s lock timeout, 30s
// stale-lock policy, real clock, default directory).
type ReviewAuditStoreOptions struct {
	// Dir overrides the store directory. Empty selects DefaultReviewAuditDir.
	Dir string
	// SegmentMaxBytes is the rotation threshold for the active segment.
	SegmentMaxBytes int64
	// MaxSegments is the total retained segment count including the active one.
	MaxSegments int
	// Now supplies timestamps; nil uses time.Now.
	Now func() time.Time
	// LockTimeout bounds cross-process lock acquisition.
	LockTimeout time.Duration
	// StaleLockAfter is the age at which a leftover lock is broken.
	StaleLockAfter time.Duration
}

// ReviewAuditLoadResult reports the retained valid records plus corruption
// and tail-repair counters. It never carries raw corrupt bytes.
type ReviewAuditLoadResult struct {
	Records            []ReviewAuditRecord
	MalformedRecords   int
	PartialTailRecords int
	TailRepairs        int
}

// ReviewAuditDeleteResult reports how many owned segments Delete removed.
type ReviewAuditDeleteResult struct {
	SegmentsRemoved int   `json:"segments_removed"`
	BytesRemoved    int64 `json:"bytes_removed"`
}

// ReviewAuditStore is a bounded redacted JSONL journal in one validated
// local directory. Safe for concurrent use within and across processes.
type ReviewAuditStore struct {
	dir             string
	canonicalRoot   string
	segmentMaxBytes int64
	maxSegments     int
	now             func() time.Time
	lockTimeout     time.Duration
	staleLockAfter  time.Duration

	// Test-only deterministic interleaving hooks. Production stores leave
	// these nil.
	testAfterSegmentLstat func(string)
	testAfterStaleLock    func()
}

var _ ReviewAuditSink = (*ReviewAuditStore)(nil)

// DefaultReviewAuditDir resolves the journal directory: the
// YHC_PERMISSION_REVIEW_AUDIT_DIR override (with its legacy alias) when set,
// otherwise
// ~/.yhc/permission-review-audit/v1.
func DefaultReviewAuditDir() (string, error) {
	resolution, err := defaultReviewAuditDirectory()
	if err != nil {
		return "", err
	}
	return resolution.dir, nil
}

type reviewAuditDirectoryResolution struct {
	dir           string
	canonicalRoot string
}

func defaultReviewAuditDirectory() (reviewAuditDirectoryResolution, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return reviewAuditDirectoryResolution{}, fmt.Errorf("review audit: resolve home directory: %w", err)
	}
	roots, err := statepath.UserRoots(home)
	if err != nil {
		return reviewAuditDirectoryResolution{}, fmt.Errorf("review audit: resolve state roots: %w", err)
	}
	selection, err := statepath.ResolveOverride(
		identity.RuntimeEnvPermissionReviewAuditDir.Pair(),
		statepath.Roots{
			Canonical: filepath.Join(roots.Canonical, "permission-review-audit", "v1"),
			Legacy:    filepath.Join(roots.Legacy, "permission-review-audit", "v1"),
		},
	)
	if err != nil {
		return reviewAuditDirectoryResolution{}, fmt.Errorf("review audit: resolve directory: %w", err)
	}
	resolution := reviewAuditDirectoryResolution{dir: selection.Effective}
	if selection.Migratable {
		resolution.canonicalRoot = roots.Canonical
	}
	return resolution, nil
}

// NewReviewAuditStore validates the explicit or default directory and
// creates a secure store. The directory must not be a symlink; it is created
// when missing and its mode is repaired to 0700.
func NewReviewAuditStore(opts ReviewAuditStoreOptions) (*ReviewAuditStore, error) {
	dir := opts.Dir
	canonicalRoot := ""
	if dir == "" {
		resolved, err := defaultReviewAuditDirectory()
		if err != nil {
			return nil, err
		}
		dir = resolved.dir
		canonicalRoot = resolved.canonicalRoot
	}
	store := &ReviewAuditStore{
		dir:             dir,
		canonicalRoot:   canonicalRoot,
		segmentMaxBytes: opts.SegmentMaxBytes,
		maxSegments:     opts.MaxSegments,
		now:             opts.Now,
		lockTimeout:     opts.LockTimeout,
		staleLockAfter:  opts.StaleLockAfter,
	}
	if store.segmentMaxBytes <= 0 {
		store.segmentMaxBytes = defaultReviewAuditSegmentMaxBytes
	}
	if store.maxSegments <= 0 {
		store.maxSegments = defaultReviewAuditMaxSegments
	}
	if store.maxSegments < 2 {
		store.maxSegments = 2
	}
	if store.now == nil {
		store.now = time.Now
	}
	if store.lockTimeout <= 0 {
		store.lockTimeout = defaultReviewAuditLockTimeout
	}
	if store.staleLockAfter <= 0 {
		store.staleLockAfter = defaultReviewAuditStaleLock
	}
	if err := store.ensureDir(); err != nil {
		return nil, err
	}
	return store, nil
}

// Record validates the record, repairs a partial active tail (appending one
// typed storage_recovery record before the caller record when truncation
// occurred), rotates before the active segment exceeds its bound, and
// appends the caller record durably. An empty EventID is filled with 16
// random bytes as 32 lowercase hex; a zero OccurredAt is filled from the
// store clock. Errors never include record contents.
func (s *ReviewAuditStore) Record(ctx context.Context, record ReviewAuditRecord) error {
	if record.SchemaVersion == 0 {
		record.SchemaVersion = ReviewAuditSchemaVersion
	}
	if record.EventID == "" {
		id, err := newReviewAuditEventID()
		if err != nil {
			return err
		}
		record.EventID = id
	}
	if record.OccurredAt.IsZero() {
		record.OccurredAt = s.now().UTC()
	} else {
		record.OccurredAt = record.OccurredAt.UTC()
	}
	if err := validateReviewAuditRecord(&record); err != nil {
		return err
	}
	line, err := encodeReviewAuditRecord(&record)
	if err != nil {
		return err
	}
	if err := s.ensureDir(); err != nil {
		return err
	}
	return s.withLock(ctx, func(root *os.Root) error {
		recoveryLine, err := s.repairActiveTail(root)
		if err != nil {
			return err
		}
		if len(recoveryLine) > 0 {
			if err := s.rotateIfNeeded(root, int64(len(recoveryLine))); err != nil {
				return err
			}
			if err := s.appendLine(root, recoveryLine); err != nil {
				return err
			}
		}
		if err := s.rotateIfNeeded(root, int64(len(line))); err != nil {
			return err
		}
		return s.appendLine(root, line)
	})
}

// Load returns the valid records retained in the current segment window,
// oldest first. Malformed newline-terminated records are skipped and
// counted; raw corrupt bytes are never returned. A missing directory or
// segment is treated as empty. A trailing partial (non-newline-terminated)
// tail is skipped and counted; Record repairs it on the next append.
func (s *ReviewAuditStore) Load() (ReviewAuditLoadResult, error) {
	var result ReviewAuditLoadResult
	exists, err := s.checkDir()
	if err != nil {
		return result, err
	}
	if !exists {
		return result, nil
	}
	err = s.withLock(context.Background(), func(root *os.Root) error {
		segments, err := s.existingSegments(root)
		if err != nil {
			return err
		}
		for _, segment := range segments {
			data, err := s.readSegment(root, segment)
			if err != nil {
				return err
			}
			lines := bytes.Split(data, []byte("\n"))
			if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
				lines = lines[:len(lines)-1] // trailing newline terminator
			}
			for i, raw := range lines {
				if i == len(lines)-1 && !bytes.HasSuffix(data, []byte("\n")) {
					result.PartialTailRecords++
					continue
				}
				if len(raw)+1 > reviewAuditMaxLineBytes {
					result.MalformedRecords++
					continue
				}
				record, err := decodeReviewAuditRecord(raw)
				if err != nil {
					result.MalformedRecords++
					continue
				}
				if record.Kind == ReviewAuditKindStorageRecovery {
					result.TailRepairs++
				}
				result.Records = append(result.Records, record)
			}
		}
		return nil
	})
	return result, err
}

// Delete removes exactly the owned segment names under the validated
// directory and reports the count and bytes removed. Unknown neighbor files
// are preserved and the directory is left in place.
func (s *ReviewAuditStore) Delete() (ReviewAuditDeleteResult, error) {
	var result ReviewAuditDeleteResult
	exists, err := s.checkDir()
	if err != nil {
		return result, err
	}
	if !exists {
		return result, nil
	}
	err = s.withLock(context.Background(), func(root *os.Root) error {
		for i := s.maxSegments - 1; i >= 0; i-- {
			name := s.segmentName(i)
			info, err := root.Lstat(name)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return fmt.Errorf("review audit: stat segment: %w", err)
			}
			if err := checkRegularSegment(info); err != nil {
				return err
			}
			if err := root.Remove(name); err != nil {
				return fmt.Errorf("review audit: remove segment: %w", err)
			}
			result.SegmentsRemoved++
			result.BytesRemoved += info.Size()
		}
		return nil
	})
	return result, err
}

// segmentName maps index 0 to the active segment and 1..maxSegments-1 to
// numbered segments.
func (s *ReviewAuditStore) segmentName(index int) string {
	if index == 0 {
		return reviewAuditActiveSegment
	}
	return fmt.Sprintf(reviewAuditNumberedSegmentFmt, index)
}

// existingSegments returns the present owned segments oldest first. Missing
// ones are skipped; symlink or non-regular owned segments are rejected.
func (s *ReviewAuditStore) existingSegments(root *os.Root) ([]string, error) {
	segments := make([]string, 0, s.maxSegments)
	for i := s.maxSegments - 1; i >= 0; i-- {
		name := s.segmentName(i)
		info, err := root.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("review audit: stat segment: %w", err)
		}
		if err := checkRegularSegment(info); err != nil {
			return nil, err
		}
		segments = append(segments, name)
	}
	return segments, nil
}

// repairActiveTail truncates a partial (non-newline-terminated) active tail
// to the last newline and returns one typed storage_recovery line with the
// recovered byte count. The caller rotates and appends the returned line
// before appending its own record.
func (s *ReviewAuditStore) repairActiveTail(root *os.Root) ([]byte, error) {
	file, info, exists, err := s.openExistingRegularFile(
		root,
		reviewAuditActiveSegment,
		os.O_RDWR,
	)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	defer file.Close()
	if info.Size() == 0 {
		return nil, nil
	}
	data, err := s.readOpenedSegment(file, info)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return nil, nil
	}
	lastNewline := bytes.LastIndexByte(data, '\n')
	kept := lastNewline + 1 // 0 when the file has no newline at all
	recovered := int64(len(data) - kept)
	if err := file.Truncate(int64(kept)); err != nil {
		return nil, fmt.Errorf("review audit: truncate partial tail: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("review audit: sync truncated segment: %w", err)
	}
	if err := checkReviewAuditFileIdentity(
		root,
		reviewAuditActiveSegment,
		info,
	); err != nil {
		return nil, err
	}
	id, err := newReviewAuditEventID()
	if err != nil {
		return nil, err
	}
	recovery := ReviewAuditRecord{
		SchemaVersion:  ReviewAuditSchemaVersion,
		EventID:        id,
		OccurredAt:     s.now().UTC(),
		Kind:           ReviewAuditKindStorageRecovery,
		RecoveredBytes: recovered,
	}
	if err := validateReviewAuditRecord(&recovery); err != nil {
		return nil, err
	}
	line, err := encodeReviewAuditRecord(&recovery)
	if err != nil {
		return nil, err
	}
	return line, nil
}

// rotateIfNeeded rotates segments when appending lineBytes would push a
// non-empty active segment past its bound. The oldest segment is deleted so
// at most maxSegments are retained. Destinations are shifted highest-first
// and never exist at rename time, keeping replacement safe on Windows.
func (s *ReviewAuditStore) rotateIfNeeded(
	root *os.Root,
	lineBytes int64,
) error {
	info, err := root.Lstat(reviewAuditActiveSegment)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("review audit: stat segment: %w", err)
	}
	if err := checkRegularSegment(info); err != nil {
		return err
	}
	if info.Size() == 0 || info.Size()+lineBytes <= s.segmentMaxBytes {
		return nil
	}
	// Validate every owned source and destination before any mutation so an
	// intermediate symlink or special file can never be renamed into the
	// retained window.
	if _, err := s.existingSegments(root); err != nil {
		return err
	}
	oldest := s.segmentName(s.maxSegments - 1)
	if err := removeRegularSegment(root, oldest); err != nil {
		return err
	}
	for i := s.maxSegments - 2; i >= 1; i-- {
		from := s.segmentName(i)
		to := s.segmentName(i + 1)
		if _, err := root.Lstat(from); os.IsNotExist(err) {
			continue
		}
		if err := root.Rename(from, to); err != nil {
			return fmt.Errorf("review audit: rotate segment: %w", err)
		}
	}
	if err := root.Rename(reviewAuditActiveSegment, s.segmentName(1)); err != nil {
		return fmt.Errorf("review audit: rotate segment: %w", err)
	}
	return nil
}

// appendLine appends one encoded line to the active segment, repairs the
// segment mode to 0600, and synchronizes the write.
func (s *ReviewAuditStore) appendLine(root *os.Root, line []byte) error {
	file, info, err := s.openOrCreateRegularFile(
		root,
		reviewAuditActiveSegment,
		os.O_WRONLY|os.O_APPEND,
	)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(reviewAuditSegmentMode); err != nil {
		return fmt.Errorf("review audit: secure segment: %w", err)
	}
	if _, err := file.Write(line); err != nil {
		return fmt.Errorf("review audit: append record: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("review audit: sync segment: %w", err)
	}
	return checkReviewAuditFileIdentity(
		root,
		reviewAuditActiveSegment,
		info,
	)
}

// withLock pins the validated store directory for the complete operation and
// runs fn while holding the cross-process lock. The O_EXCL sentinel is
// serialized by an OS advisory lock on a stable coordination file, which
// makes stale-sentinel removal ownership-safe across processes.
func (s *ReviewAuditStore) withLock(
	ctx context.Context,
	fn func(*os.Root) error,
) error {
	root, exists, closeRoot, revalidate, err := s.openRoot(false)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("review audit: store directory disappeared")
	}
	defer closeRoot() //nolint:errcheck

	release, err := s.acquireLock(ctx, root)
	if err != nil {
		return err
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	operationErr := fn(root)
	identityErr := revalidate()
	release()
	released = true
	return errors.Join(operationErr, identityErr, revalidate())
}

func (s *ReviewAuditStore) ensureDir() error {
	if s.canonicalRoot == "" {
		return ensureReviewAuditDir(s.dir)
	}
	store, exists, err := statemigration.OpenCanonicalStore(
		s.canonicalRoot,
		reviewAuditStateRel,
		true,
	)
	if err != nil || !exists {
		return errors.New("review audit: canonical state root is invalid")
	}
	defer store.Close() //nolint:errcheck
	if err := store.Revalidate(); err != nil {
		return errors.New("review audit: canonical state root is invalid")
	}
	return nil
}

func (s *ReviewAuditStore) checkDir() (bool, error) {
	_, exists, closeRoot, revalidate, err := s.openRoot(false)
	if err != nil || !exists {
		return exists, err
	}
	defer closeRoot() //nolint:errcheck
	if err := revalidate(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *ReviewAuditStore) openRoot(
	create bool,
) (*os.Root, bool, func() error, func() error, error) {
	if s.canonicalRoot == "" {
		if create {
			if err := ensureReviewAuditDir(s.dir); err != nil {
				return nil, false, nil, nil, err
			}
		}
		root, exists, err := openReviewAuditRoot(s.dir)
		if err != nil || !exists {
			return root, exists, closeReviewAuditRoot(root), func() error { return nil }, err
		}
		return root, true, root.Close, func() error { return nil }, nil
	}
	store, exists, err := statemigration.OpenCanonicalStore(
		s.canonicalRoot,
		reviewAuditStateRel,
		create,
	)
	if err != nil || !exists {
		return nil, exists, func() error { return nil }, func() error { return err }, err
	}
	return store.Root(), true, store.Close, store.Revalidate, nil
}

func closeReviewAuditRoot(root *os.Root) func() error {
	return func() error {
		if root == nil {
			return nil
		}
		return root.Close()
	}
}

func (s *ReviewAuditStore) acquireLock(
	ctx context.Context,
	root *os.Root,
) (func(), error) {
	deadline := time.Now().Add(s.lockTimeout)
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("review audit: acquire lock: %w", err)
		}
		guard, _, err := s.openOrCreateRegularFile(
			root,
			reviewAuditLockGuardName,
			os.O_RDWR,
		)
		if err != nil {
			return nil, err
		}
		if err := guard.Chmod(reviewAuditSegmentMode); err != nil {
			guard.Close()
			return nil, fmt.Errorf("review audit: secure coordination file: %w", err)
		}
		locked, err := tryLockReviewAuditGuard(guard)
		if err != nil {
			guard.Close()
			return nil, fmt.Errorf("review audit: lock coordination file: %w", err)
		}
		if !locked {
			guard.Close()
			if !time.Now().Before(deadline) {
				return nil, errors.New("review audit: lock timeout")
			}
			time.Sleep(5 * time.Millisecond)
			continue
		}

		releaseGuard := func() {
			_ = unlockReviewAuditGuard(guard)
			_ = guard.Close()
		}
		info, err := root.Lstat(reviewAuditLockName)
		if os.IsNotExist(err) {
			release, createErr := createReviewAuditLockSentinel(root, guard)
			if createErr == nil {
				return release, nil
			}
			releaseGuard()
			if os.IsExist(createErr) {
				continue
			}
			return nil, createErr
		}
		if err != nil {
			releaseGuard()
			return nil, fmt.Errorf("review audit: stat lock: %w", err)
		}
		if err := checkRegularLock(info); err != nil {
			releaseGuard()
			return nil, err
		}
		if s.now().Sub(info.ModTime()) > s.staleLockAfter {
			if s.testAfterStaleLock != nil {
				s.testAfterStaleLock()
			}
			current, currentErr := root.Lstat(reviewAuditLockName)
			if currentErr != nil {
				releaseGuard()
				if os.IsNotExist(currentErr) {
					continue
				}
				return nil, fmt.Errorf("review audit: restat stale lock: %w", currentErr)
			}
			if !os.SameFile(info, current) {
				releaseGuard()
				continue
			}
			if err := root.Remove(reviewAuditLockName); err != nil {
				releaseGuard()
				return nil, fmt.Errorf("review audit: remove stale lock: %w", err)
			}
			release, createErr := createReviewAuditLockSentinel(root, guard)
			if createErr == nil {
				return release, nil
			}
			releaseGuard()
			if os.IsExist(createErr) {
				continue
			}
			return nil, createErr
		}
		releaseGuard()
		if !time.Now().Before(deadline) {
			return nil, errors.New("review audit: lock timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func createReviewAuditLockSentinel(
	root *os.Root,
	guard *os.File,
) (func(), error) {
	file, err := root.OpenFile(
		reviewAuditLockName,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		reviewAuditSegmentMode,
	)
	if err != nil {
		return nil, fmt.Errorf("review audit: create lock: %w", err)
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil {
		_ = root.Remove(reviewAuditLockName)
		return nil, fmt.Errorf("review audit: stat created lock: %w", statErr)
	}
	if closeErr != nil {
		_ = root.Remove(reviewAuditLockName)
		return nil, fmt.Errorf("review audit: close created lock: %w", closeErr)
	}
	current, err := root.Lstat(reviewAuditLockName)
	if err != nil || !os.SameFile(info, current) {
		_ = root.Remove(reviewAuditLockName)
		if err != nil {
			return nil, fmt.Errorf("review audit: verify created lock: %w", err)
		}
		return nil, errors.New("review audit: created lock identity changed")
	}
	return func() {
		current, currentErr := root.Lstat(reviewAuditLockName)
		if currentErr == nil && os.SameFile(info, current) {
			_ = root.Remove(reviewAuditLockName)
		}
		_ = unlockReviewAuditGuard(guard)
		_ = guard.Close()
	}, nil
}

// ensureReviewAuditDir creates the store directory when missing, rejects a
// symlink or non-directory, and repairs the mode to 0700 through a pinned root
// handle.
func ensureReviewAuditDir(dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case os.IsNotExist(err):
		if err := os.MkdirAll(dir, reviewAuditDirMode); err != nil {
			return fmt.Errorf("review audit: create directory: %w", err)
		}
	case err != nil:
		return fmt.Errorf("review audit: stat directory: %w", err)
	case info.Mode()&os.ModeSymlink != 0:
		return errors.New("review audit: store directory must not be a symlink")
	case !info.IsDir():
		return errors.New("review audit: store path is not a directory")
	}
	root, exists, err := openReviewAuditRoot(dir)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("review audit: store directory disappeared")
	}
	defer root.Close()
	if err := root.Chmod(".", reviewAuditDirMode); err != nil {
		return fmt.Errorf("review audit: secure directory: %w", err)
	}
	return nil
}

// openReviewAuditRoot opens and identity-checks the final store directory.
// The returned os.Root pins that directory across later path replacement and
// prevents relative file operations from escaping it.
func openReviewAuditRoot(dir string) (*os.Root, bool, error) {
	before, err := os.Lstat(dir)
	switch {
	case os.IsNotExist(err):
		return nil, false, nil
	case err != nil:
		return nil, false, fmt.Errorf("review audit: stat directory: %w", err)
	case before.Mode()&os.ModeSymlink != 0:
		return nil, false, errors.New("review audit: store directory must not be a symlink")
	case !before.IsDir():
		return nil, false, errors.New("review audit: store path is not a directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, false, fmt.Errorf("review audit: open directory: %w", err)
	}
	after, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, false, fmt.Errorf("review audit: verify directory: %w", err)
	}
	if !after.IsDir() || !os.SameFile(before, after) {
		root.Close()
		return nil, false, errors.New("review audit: store directory identity changed")
	}
	return root, true, nil
}

func (s *ReviewAuditStore) openExistingRegularFile(
	root *os.Root,
	name string,
	flag int,
) (*os.File, os.FileInfo, bool, error) {
	before, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("review audit: stat segment: %w", err)
	}
	if err := checkRegularSegment(before); err != nil {
		return nil, nil, false, err
	}
	if s.testAfterSegmentLstat != nil &&
		name != reviewAuditLockGuardName &&
		name != reviewAuditLockName {
		s.testAfterSegmentLstat(name)
	}
	file, err := root.OpenFile(name, flag, reviewAuditSegmentMode)
	if err != nil {
		return nil, nil, false, fmt.Errorf("review audit: open segment: %w", err)
	}
	opened, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, false, fmt.Errorf("review audit: stat opened segment: %w", err)
	}
	if err := checkRegularSegment(opened); err != nil {
		file.Close()
		return nil, nil, false, err
	}
	if !os.SameFile(before, opened) {
		file.Close()
		return nil, nil, false, errors.New("review audit: segment identity changed")
	}
	if err := checkReviewAuditFileIdentity(root, name, opened); err != nil {
		file.Close()
		return nil, nil, false, err
	}
	return file, opened, true, nil
}

func (s *ReviewAuditStore) openOrCreateRegularFile(
	root *os.Root,
	name string,
	flag int,
) (*os.File, os.FileInfo, error) {
	for {
		file, info, exists, err := s.openExistingRegularFile(root, name, flag)
		if err != nil {
			return nil, nil, err
		}
		if exists {
			return file, info, nil
		}
		file, err = root.OpenFile(
			name,
			flag|os.O_CREATE|os.O_EXCL,
			reviewAuditSegmentMode,
		)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("review audit: create segment: %w", err)
		}
		info, err = file.Stat()
		if err != nil {
			file.Close()
			_ = root.Remove(name)
			return nil, nil, fmt.Errorf("review audit: stat created segment: %w", err)
		}
		if err := checkRegularSegment(info); err != nil {
			file.Close()
			_ = root.Remove(name)
			return nil, nil, err
		}
		if err := checkReviewAuditFileIdentity(root, name, info); err != nil {
			file.Close()
			_ = root.Remove(name)
			return nil, nil, err
		}
		if err := file.Chmod(reviewAuditSegmentMode); err != nil {
			file.Close()
			_ = root.Remove(name)
			return nil, nil, fmt.Errorf("review audit: secure segment: %w", err)
		}
		return file, info, nil
	}
}

func checkReviewAuditFileIdentity(
	root *os.Root,
	name string,
	expected os.FileInfo,
) error {
	current, err := root.Lstat(name)
	if err != nil {
		return fmt.Errorf("review audit: verify segment identity: %w", err)
	}
	if err := checkRegularSegment(current); err != nil {
		return err
	}
	if !os.SameFile(expected, current) {
		return errors.New("review audit: segment identity changed")
	}
	return nil
}

func (s *ReviewAuditStore) maxReadableSegmentBytes() int64 {
	if s.segmentMaxBytes > reviewAuditMaxLineBytes {
		return s.segmentMaxBytes
	}
	return reviewAuditMaxLineBytes
}

func (s *ReviewAuditStore) readOpenedSegment(
	file *os.File,
	info os.FileInfo,
) ([]byte, error) {
	maxBytes := s.maxReadableSegmentBytes()
	if info.Size() > maxBytes {
		return nil, errors.New("review audit: segment exceeds bounded size")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("review audit: seek segment: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("review audit: read segment: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, errors.New("review audit: segment exceeds bounded size")
	}
	return data, nil
}

func (s *ReviewAuditStore) readSegment(
	root *os.Root,
	name string,
) ([]byte, error) {
	file, info, exists, err := s.openExistingRegularFile(root, name, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.New("review audit: segment disappeared")
	}
	defer file.Close()
	return s.readOpenedSegment(file, info)
}

// checkRegularSegment rejects symlink or non-regular owned segments.
func checkRegularSegment(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("review audit: segment must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return errors.New("review audit: segment is not a regular file")
	}
	return nil
}

func checkRegularLock(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("review audit: lock must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return errors.New("review audit: lock is not a regular file")
	}
	return nil
}

// removeRegularSegment removes one owned segment, rejecting symlinks and
// non-regular files; missing is a no-op.
func removeRegularSegment(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("review audit: stat segment: %w", err)
	}
	if err := checkRegularSegment(info); err != nil {
		return err
	}
	if err := root.Remove(name); err != nil {
		return fmt.Errorf("review audit: remove segment: %w", err)
	}
	return nil
}

// newReviewAuditEventID returns 16 random bytes encoded as 32 lowercase hex.
func newReviewAuditEventID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("review audit: generate event id: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

// encodeReviewAuditRecord marshals one record as a bounded JSONL line.
func encodeReviewAuditRecord(record *ReviewAuditRecord) ([]byte, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("review audit: encode record: %w", err)
	}
	if len(encoded)+1 > reviewAuditMaxLineBytes {
		return nil, errors.New("review audit: encoded record exceeds 16 KiB line bound")
	}
	return append(encoded, '\n'), nil
}

var reviewAuditJSONFields = map[string]struct{}{
	"schema_version":        {},
	"event_id":              {},
	"occurred_at":           {},
	"kind":                  {},
	"canonical_tool":        {},
	"action_kind":           {},
	"deterministic_class":   {},
	"reviewer_status":       {},
	"reviewer_decision":     {},
	"reason_code":           {},
	"latency_ms":            {},
	"provider":              {},
	"model":                 {},
	"data_boundary":         {},
	"comparison_source":     {},
	"expected_decision":     {},
	"corpus_id":             {},
	"corpus_case_id":        {},
	"recovered_bytes":       {},
	"dispatcher_diagnostic": {},
	"diagnostic_count":      {},
}

// decodeReviewAuditRecord accepts one exact JSON object. Unknown, duplicate,
// or trailing fields are malformed; errors never include input bytes.
func decodeReviewAuditRecord(raw []byte) (ReviewAuditRecord, error) {
	var record ReviewAuditRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return record, errors.New("review audit: record is not an object")
	}
	seen := make(map[string]struct{}, len(reviewAuditJSONFields))
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return record, errors.New("review audit: malformed record field")
		}
		field, ok := token.(string)
		if !ok {
			return record, errors.New("review audit: malformed record field")
		}
		if _, ok := reviewAuditJSONFields[field]; !ok {
			return record, errors.New("review audit: unknown record field")
		}
		if _, duplicate := seen[field]; duplicate {
			return record, errors.New("review audit: duplicate record field")
		}
		seen[field] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return record, errors.New("review audit: malformed record value")
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return record, errors.New("review audit: null record value")
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') {
		return record, errors.New("review audit: malformed record object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return record, errors.New("review audit: trailing record data")
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return record, errors.New("review audit: malformed typed record")
	}
	if err := validateReviewAuditRecord(&record); err != nil {
		return record, err
	}
	return record, nil
}

var (
	reviewAuditEventIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
	reviewAuditTokenRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:+-]{0,127}$`)
)

var reviewAuditUnavailableReasons = map[string]struct{}{
	"projection_unavailable": {},
	"identity_unavailable":   {},
	"capacity_exceeded":      {},
	"cancelled":              {},
	"timeout":                {},
	"reviewer_unavailable":   {},
	"invalid_result":         {},
	"binding_changed":        {},
}

var reviewAuditDispatcherDiagnostics = map[ReviewAuditDispatcherDiagnostic]struct{}{
	ReviewAuditDiagnosticEnqueueDrop: {},
	ReviewAuditDiagnosticSinkFailure: {},
	ReviewAuditDiagnosticFlushExpiry: {},
	ReviewAuditDiagnosticAfterClose:  {},
}

// validateReviewAuditRecord enforces the schema and per-kind invariants.
// Error messages name the kind and field but never include field values.
func validateReviewAuditRecord(record *ReviewAuditRecord) error {
	if record.SchemaVersion != ReviewAuditSchemaVersion {
		return fmt.Errorf("review audit: unsupported schema_version for %s record", record.Kind)
	}
	if !reviewAuditEventIDRE.MatchString(record.EventID) {
		return fmt.Errorf("review audit: invalid event_id for %s record", record.Kind)
	}
	if record.OccurredAt.IsZero() {
		return fmt.Errorf("review audit: zero occurred_at for %s record", record.Kind)
	}
	if record.OccurredAt.Location() != time.UTC {
		return fmt.Errorf("review audit: non-UTC occurred_at for %s record", record.Kind)
	}
	if record.LatencyMS < 0 {
		return fmt.Errorf("review audit: negative latency_ms for %s record", record.Kind)
	}
	if record.RecoveredBytes < 0 {
		return fmt.Errorf("review audit: negative recovered_bytes for %s record", record.Kind)
	}
	tokens := map[string]string{
		"canonical_tool":        record.CanonicalTool,
		"action_kind":           record.ActionKind,
		"deterministic_class":   record.DeterministicClass,
		"reviewer_status":       record.ReviewerStatus,
		"reviewer_decision":     record.ReviewerDecision,
		"reason_code":           record.ReasonCode,
		"comparison_source":     record.ComparisonSource,
		"expected_decision":     record.ExpectedDecision,
		"corpus_id":             record.CorpusID,
		"corpus_case_id":        record.CorpusCaseID,
		"dispatcher_diagnostic": string(record.DispatcherDiagnostic),
	}
	for field, value := range tokens {
		if value != "" && !reviewAuditTokenRE.MatchString(value) {
			return fmt.Errorf("review audit: unsafe %s for %s record", field, record.Kind)
		}
	}
	if record.Kind != ReviewAuditKindDispatcherDiagnostic &&
		(record.DispatcherDiagnostic != "" || record.DiagnosticCount != 0) {
		return errors.New("review audit: non-dispatcher record has dispatcher diagnostic fields")
	}
	switch record.Kind {
	case ReviewAuditKindEligible:
		if record.DeterministicClass != "review" {
			return errors.New("review audit: eligible record requires deterministic_class review")
		}
		if record.CanonicalTool == "" || record.ActionKind == "" {
			return errors.New("review audit: eligible record requires action identity")
		}
		if record.ReviewerStatus != "" || record.ReviewerDecision != "" ||
			record.ReasonCode != "" || record.LatencyMS != 0 ||
			record.Provider != "" || record.Model != "" ||
			record.DataBoundary != "" || record.ComparisonSource != "" ||
			record.ExpectedDecision != "" || record.CorpusID != "" ||
			record.CorpusCaseID != "" || record.RecoveredBytes != 0 {
			return errors.New("review audit: eligible record has fields outside its schema")
		}
	case ReviewAuditKindAttempt:
		if !IsSafeReviewRoute(ApprovalReviewerRoute{
			Provider:     record.Provider,
			Model:        record.Model,
			DataBoundary: record.DataBoundary,
		}) {
			return errors.New("review audit: attempt record requires a safe reviewer route")
		}
		if record.CanonicalTool != "" || record.ActionKind != "" ||
			record.DeterministicClass != "" || record.ReviewerStatus != "" ||
			record.ReviewerDecision != "" || record.ReasonCode != "" ||
			record.LatencyMS != 0 || record.ComparisonSource != "" ||
			record.ExpectedDecision != "" || record.CorpusID != "" ||
			record.CorpusCaseID != "" || record.RecoveredBytes != 0 {
			return errors.New("review audit: attempt record has fields outside its schema")
		}
	case ReviewAuditKindTerminal:
		switch record.ReviewerStatus {
		case "completed":
			reasons, ok := reviewerReasons[record.ReviewerDecision]
			if !ok {
				return errors.New("review audit: terminal record has invalid reviewer_decision")
			}
			if _, ok := reasons[record.ReasonCode]; !ok {
				return errors.New("review audit: terminal record has invalid reason_code")
			}
		case "unavailable":
			if _, ok := reviewAuditUnavailableReasons[record.ReasonCode]; !ok {
				return errors.New("review audit: unavailable terminal record has invalid reason_code")
			}
			if record.ReviewerDecision != "" {
				return errors.New("review audit: unavailable terminal record forbids reviewer_decision")
			}
		default:
			return errors.New("review audit: terminal record requires completed or unavailable status")
		}
		if record.CanonicalTool != "" || record.ActionKind != "" ||
			record.DeterministicClass != "" || record.Provider != "" ||
			record.Model != "" || record.DataBoundary != "" ||
			record.ComparisonSource != "" || record.ExpectedDecision != "" ||
			record.CorpusID != "" || record.CorpusCaseID != "" ||
			record.RecoveredBytes != 0 {
			return errors.New("review audit: terminal record has fields outside its schema")
		}
	case ReviewAuditKindComparison:
		if record.ComparisonSource != "legacy_classifier" && record.ComparisonSource != "human" {
			return errors.New("review audit: comparison record has invalid comparison_source")
		}
		if record.ExpectedDecision != "allow" && record.ExpectedDecision != "deny" {
			return errors.New("review audit: comparison record has invalid expected_decision")
		}
		if record.CanonicalTool != "" || record.ActionKind != "" ||
			record.DeterministicClass != "" || record.ReviewerStatus != "" ||
			record.ReviewerDecision != "" || record.ReasonCode != "" ||
			record.LatencyMS != 0 || record.Provider != "" || record.Model != "" ||
			record.DataBoundary != "" || record.CorpusID != "" ||
			record.CorpusCaseID != "" || record.RecoveredBytes != 0 {
			return errors.New("review audit: comparison record has fields outside its schema")
		}
	case ReviewAuditKindCorpusGroundTruth:
		if record.ComparisonSource != "versioned_corpus" {
			return errors.New("review audit: corpus_ground_truth record requires versioned_corpus source")
		}
		if record.CorpusID == "" || record.CorpusCaseID == "" {
			return errors.New("review audit: corpus_ground_truth record requires corpus and case ids")
		}
		if record.ExpectedDecision != "allow" && record.ExpectedDecision != "deny" {
			return errors.New("review audit: corpus_ground_truth record has invalid expected_decision")
		}
		if record.CanonicalTool != "" || record.ActionKind != "" ||
			record.DeterministicClass != "" || record.ReviewerStatus != "" ||
			record.ReviewerDecision != "" || record.ReasonCode != "" ||
			record.LatencyMS != 0 || record.Provider != "" || record.Model != "" ||
			record.DataBoundary != "" || record.RecoveredBytes != 0 {
			return errors.New("review audit: corpus_ground_truth record has fields outside its schema")
		}
	case ReviewAuditKindStorageRecovery:
		if record.CanonicalTool != "" || record.ActionKind != "" ||
			record.DeterministicClass != "" || record.ReviewerStatus != "" ||
			record.ReviewerDecision != "" || record.ReasonCode != "" ||
			record.LatencyMS != 0 || record.Provider != "" || record.Model != "" ||
			record.DataBoundary != "" || record.ComparisonSource != "" ||
			record.ExpectedDecision != "" || record.CorpusID != "" || record.CorpusCaseID != "" {
			return errors.New("review audit: storage_recovery record allows only recovered_bytes")
		}
		if record.RecoveredBytes <= 0 {
			return errors.New("review audit: storage_recovery record requires recovered_bytes")
		}
	case ReviewAuditKindDispatcherDiagnostic:
		if _, ok := reviewAuditDispatcherDiagnostics[record.DispatcherDiagnostic]; !ok {
			return errors.New("review audit: dispatcher_diagnostic record has invalid dispatcher_diagnostic")
		}
		if record.DiagnosticCount == 0 {
			return errors.New("review audit: dispatcher_diagnostic record requires diagnostic_count")
		}
		if record.CanonicalTool != "" || record.ActionKind != "" ||
			record.DeterministicClass != "" || record.ReviewerStatus != "" ||
			record.ReviewerDecision != "" || record.ReasonCode != "" ||
			record.LatencyMS != 0 || record.Provider != "" || record.Model != "" ||
			record.DataBoundary != "" || record.ComparisonSource != "" ||
			record.ExpectedDecision != "" || record.CorpusID != "" ||
			record.CorpusCaseID != "" || record.RecoveredBytes != 0 {
			return errors.New("review audit: dispatcher_diagnostic record has fields outside its schema")
		}
	default:
		return errors.New("review audit: unknown record kind")
	}
	return nil
}
