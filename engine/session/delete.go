// Package session — delete.go implements session deletion and cleanup.
// Removes session transcript files and associated metadata, handles lineage
// updates (parent leaf status), and supports bulk deletion by criteria.
package session

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/abietic/yhc/engine/internal/workboard"
)

const (
	runtimeInputSidecarSuffix = ".runtime-inputs.json"
	projectGraphSidecarSuffix = ".project-graph-checkpoint.json"
	mediaSidecarSuffix        = ".media"
	maxMediaDeleteEntries     = 262_144
)

var (
	mediaDigestNamePattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	mediaPrefixNamePattern = regexp.MustCompile(`^[a-f0-9]{2}$`)
	mediaTempNamePattern   = regexp.MustCompile(`^\.tmp-[A-Za-z0-9_-]{43}$`)
	manifestTempPattern    = regexp.MustCompile(`^\.manifest\.tmp-[A-Za-z0-9_-]{43}$`)
)

var ErrSessionCleanupPending = errors.New("session cleanup pending")

type CleanupPendingError struct {
	SessionID string
	Cause     error
}

func (e *CleanupPendingError) Error() string {
	return fmt.Sprintf(
		"session %s cleanup pending: %v",
		e.SessionID,
		e.Cause,
	)
}

func (e *CleanupPendingError) Unwrap() error {
	return errors.Join(ErrSessionCleanupPending, e.Cause)
}

// DeleteOptions configures how a session is deleted.
type DeleteOptions struct {
	// SessionID is the session to delete.
	SessionID string
	// Dir is the session storage directory. If empty, uses GetSessionDir with ProjectDir.
	Dir string
	// ProjectDir is used to resolve Dir when Dir is empty.
	ProjectDir string
	// beforeMutation is a deterministic link-replacement test seam.
	beforeMutation func()
	// removeWorkBoard is a deterministic post-transcript cleanup seam.
	removeWorkBoard func(string) error
}

// DeleteResult contains information about a completed deletion.
type DeleteResult struct {
	// SessionID is the ID of the deleted session.
	SessionID string
	// TranscriptRemoved is true if the transcript file was successfully removed.
	TranscriptRemoved bool
	// ParentUpdated is true if a parent session's leaf status was updated.
	ParentUpdated bool
	// BytesFreed is the size of the removed transcript file.
	BytesFreed int64
	// MediaRemoved is true when a validated private media sidecar existed and
	// was removed after the transcript.
	MediaRemoved bool
	// WorkBoardShadowRemoved is true when the optional P31.1a observation
	// sidecar existed and was removed after the transcript.
	WorkBoardShadowRemoved bool
	// WorkBoardAuthorityRemoved is true when one or more P31.1b authority,
	// marker, or backup artifacts were removed after the transcript.
	WorkBoardAuthorityRemoved bool
	// CleanupCompleted reports that no validated owned artifact remains.
	CleanupCompleted bool
}

// DeleteSession removes a session's transcript file and updates lineage.
// If the session has a parent, the parent's leaf status is recalculated.
// If the session has children (branches), those children become orphaned
// (their parent_session_id still references the deleted session, but no error occurs).
//
// Returns an error if the session does not exist or cannot be removed.
func DeleteSession(opts DeleteOptions) (*DeleteResult, error) {
	if !isValidSessionFileID(opts.SessionID) {
		return nil, errors.New("invalid session ID")
	}

	dir := opts.Dir
	if dir == "" {
		dir = GetSessionDir(opts.ProjectDir)
	}

	transcriptPath, info, err := preflightSessionDeleteTargets(
		dir,
		opts.SessionID,
		false,
	)
	if err != nil {
		return nil, err
	}
	mediaPlan, err := preflightMediaDelete(transcriptPath + mediaSidecarSuffix)
	if err != nil {
		return nil, err
	}
	if mediaPlan != nil {
		defer mediaPlan.close()
		if err := mediaPlan.revalidate(); err != nil {
			return nil, err
		}
	}
	workBoardPlan, err := preflightWorkBoardDelete(
		filepath.Dir(transcriptPath),
		opts.SessionID,
		info != nil,
	)
	if err != nil {
		return nil, err
	}
	if info == nil && mediaPlan == nil && workBoardPlan == nil {
		return nil, fmt.Errorf(
			"session %s does not exist: %w",
			opts.SessionID,
			os.ErrNotExist,
		)
	}
	workBoardShadowPath, err := workboard.SidecarPath(
		filepath.Dir(transcriptPath),
		opts.SessionID,
	)
	if err != nil {
		return nil, err
	}
	workBoardShadowInfo, err := preflightWorkBoardShadowDelete(
		workBoardShadowPath,
	)
	if err != nil {
		return nil, err
	}

	bytesFreed := int64(0)
	if info != nil {
		bytesFreed = info.Size()
	}

	// Check for parent session to potentially update leaf status.
	parentSessionID := ""
	if info != nil {
		headData, readErr := readFileHead(transcriptPath, 32*1024)
		if readErr == nil {
			parentSessionID = extractMetaValue(
				string(headData),
				"parent_session_id",
			)
		}
	}
	if opts.beforeMutation != nil {
		opts.beforeMutation()
	}
	_, currentInfo, err := preflightSessionDeleteTargets(
		dir,
		opts.SessionID,
		false,
	)
	if err != nil ||
		(info == nil) != (currentInfo == nil) ||
		(info != nil && !os.SameFile(info, currentInfo)) {
		return nil, errors.New(
			"session delete target changed before mutation",
		)
	}
	if mediaPlan != nil {
		if err := mediaPlan.revalidate(); err != nil {
			return nil, err
		}
	}
	if err := revalidateWorkBoardShadowDelete(
		workBoardShadowPath,
		workBoardShadowInfo,
	); err != nil {
		return nil, err
	}
	if workBoardPlan != nil {
		if err := workBoardPlan.revalidate(); err != nil {
			return nil, err
		}
	}

	// Remove the transcript file.
	transcriptRemoved := false
	if info != nil {
		if err := os.Remove(transcriptPath); err != nil {
			return nil, fmt.Errorf("remove transcript: %w", err)
		}
		transcriptRemoved = true
	}
	result := &DeleteResult{
		SessionID:         opts.SessionID,
		TranscriptRemoved: transcriptRemoved,
		BytesFreed:        bytesFreed,
	}
	cleanupFailure := func(cause error) (*DeleteResult, error) {
		return result, &CleanupPendingError{
			SessionID: opts.SessionID,
			Cause:     cause,
		}
	}

	// Also remove any .tmp file that might exist from an interrupted operation.
	_ = os.Remove(transcriptPath + ".tmp")
	for _, suffix := range []string{
		runtimeInputSidecarSuffix,
		projectGraphSidecarSuffix,
	} {
		if removeErr := os.Remove(transcriptPath + suffix); removeErr != nil &&
			!errors.Is(removeErr, os.ErrNotExist) {
			return cleanupFailure(fmt.Errorf(
				"remove session sidecar %q: %w",
				suffix,
				removeErr,
			))
		}
	}
	if workBoardPlan != nil {
		removed, removeErr := workBoardPlan.remove(opts.removeWorkBoard)
		if removeErr != nil {
			return cleanupFailure(removeErr)
		}
		result.WorkBoardAuthorityRemoved = removed.authority
		result.WorkBoardShadowRemoved = removed.shadow
	}
	workBoardShadowRemoved := false
	if workBoardPlan == nil && workBoardShadowInfo != nil {
		if err := os.Remove(workBoardShadowPath); err != nil {
			return cleanupFailure(fmt.Errorf(
				"remove WorkBoard shadow sidecar: %w",
				err,
			))
		}
		workBoardShadowRemoved = true
		result.WorkBoardShadowRemoved = true
	}
	mediaRemoved := false
	if mediaPlan != nil {
		if err := mediaPlan.remove(); err != nil {
			return cleanupFailure(err)
		}
		mediaRemoved = true
	}
	result.MediaRemoved = mediaRemoved
	result.WorkBoardShadowRemoved = result.WorkBoardShadowRemoved || workBoardShadowRemoved
	result.CleanupCompleted = true

	// If the deleted session had a parent, check if the parent should be marked as leaf.
	// A parent becomes a leaf if this was its only child branch.
	if parentSessionID != "" {
		remaining, err := ListBranches(parentSessionID, dir)
		if err == nil && len(remaining) == 0 {
			// Parent has no more children — it is now a leaf.
			// We don't rewrite the parent's metadata here since IsLeaf is derived
			// from ListBranches at read time. The lineage is consistent.
			result.ParentUpdated = true
		}
	}

	return result, nil
}

func preflightWorkBoardShadowDelete(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("preflight WorkBoard shadow sidecar: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("WorkBoard shadow sidecar is not a regular file")
	}
	return info, nil
}

func revalidateWorkBoardShadowDelete(path string, expected os.FileInfo) error {
	if expected == nil {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("revalidate WorkBoard shadow sidecar: %w", err)
		}
		_ = info
		return errors.New("WorkBoard shadow sidecar appeared before deletion")
	}
	current, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("revalidate WorkBoard shadow sidecar: %w", err)
	}
	if current.Mode()&os.ModeSymlink != 0 ||
		!current.Mode().IsRegular() ||
		!os.SameFile(expected, current) {
		return errors.New("WorkBoard shadow sidecar changed before deletion")
	}
	return nil
}

type workBoardDeleteTarget struct {
	kind   string
	path   string
	info   os.FileInfo
	remove bool
}

type workBoardDeletePlan struct {
	sessionID string
	targets   []workBoardDeleteTarget
}

type workBoardDeleteRemoved struct {
	authority bool
	shadow    bool
}

func preflightWorkBoardDelete(
	dir string,
	sessionID string,
	transcriptPresent bool,
) (*workBoardDeletePlan, error) {
	shadowPath, err := workboard.SidecarPath(dir, sessionID)
	if err != nil {
		return nil, err
	}
	targets := []workBoardDeleteTarget{
		{
			kind: "marker",
			path: filepath.Join(
				dir,
				sessionID+workboard.AuthorityMarkerSuffix,
			),
		},
		{
			kind: "authority",
			path: filepath.Join(
				dir,
				sessionID+workboard.AuthorityRecordSuffix,
			),
		},
		{
			kind: "backup",
			path: filepath.Join(
				dir,
				sessionID+workboard.LegacyBackupSuffix,
			),
		},
		{kind: "shadow", path: shadowPath},
	}
	existing := 0
	present := make(map[string]bool)
	for index := range targets {
		info, statErr := os.Lstat(targets[index].path)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return nil, fmt.Errorf(
				"preflight WorkBoard %s: %w",
				targets[index].kind,
				statErr,
			)
		}
		if info.Mode()&os.ModeSymlink != 0 ||
			!info.Mode().IsRegular() ||
			info.Mode().Perm() != 0o600 {
			return nil, fmt.Errorf(
				"preflight WorkBoard %s is not a regular mode-0600 file",
				targets[index].kind,
			)
		}
		data, readErr := readBoundedDeleteArtifact(targets[index].path)
		if readErr != nil {
			return nil, readErr
		}
		switch targets[index].kind {
		case "marker":
			_, readErr = workboard.DecodeAuthorityMarker(data, sessionID)
		case "authority":
			_, readErr = workboard.DecodeAuthorityRecord(data, sessionID)
		case "backup":
			_, readErr = workboard.DecodeLegacyBackup(data, sessionID)
		case "shadow":
			if !transcriptPresent {
				_, readErr = workboard.Decode(data, sessionID)
			}
		}
		if readErr != nil {
			return nil, readErr
		}
		targets[index].info = info
		targets[index].remove = true
		present[targets[index].kind] = true
		existing++
	}
	if existing == 0 {
		return nil, nil
	}
	if present["marker"] &&
		(!present["authority"] || !present["backup"]) {
		return nil, errors.New(
			"preflight WorkBoard marker has an incomplete authority set",
		)
	}
	return &workBoardDeletePlan{
		sessionID: sessionID,
		targets:   targets,
	}, nil
}

func (p *workBoardDeletePlan) revalidate() error {
	if p == nil {
		return nil
	}
	for _, target := range p.targets {
		current, err := os.Lstat(target.path)
		if !target.remove {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			return fmt.Errorf(
				"WorkBoard %s appeared before deletion",
				target.kind,
			)
		}
		if err != nil ||
			current.Mode()&os.ModeSymlink != 0 ||
			!current.Mode().IsRegular() ||
			current.Mode().Perm() != 0o600 ||
			!os.SameFile(target.info, current) {
			return fmt.Errorf(
				"WorkBoard %s changed before deletion",
				target.kind,
			)
		}
	}
	return nil
}

func (p *workBoardDeletePlan) remove(
	remove func(string) error,
) (workBoardDeleteRemoved, error) {
	var removed workBoardDeleteRemoved
	if p == nil {
		return removed, nil
	}
	if remove == nil {
		remove = os.Remove
	}
	for _, target := range p.targets {
		if !target.remove {
			continue
		}
		current, err := os.Lstat(target.path)
		if err != nil ||
			current.Mode()&os.ModeSymlink != 0 ||
			!current.Mode().IsRegular() ||
			!os.SameFile(target.info, current) {
			return removed, fmt.Errorf(
				"WorkBoard %s changed during deletion",
				target.kind,
			)
		}
		if err := remove(target.path); err != nil {
			return removed, fmt.Errorf(
				"remove WorkBoard %s: %w",
				target.kind,
				err,
			)
		}
		if target.kind == "shadow" {
			removed.shadow = true
		} else {
			removed.authority = true
		}
	}
	return removed, nil
}

func readBoundedDeleteArtifact(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(
		file,
		workboard.MaxEncodedJSONBytes+1,
	))
	if err != nil {
		return nil, err
	}
	if len(data) > workboard.MaxEncodedJSONBytes {
		return nil, fmt.Errorf(
			"WorkBoard artifact exceeds %d bytes",
			workboard.MaxEncodedJSONBytes,
		)
	}
	return data, nil
}

type mediaDeletePlan struct {
	path     string
	root     *os.Root
	rootInfo os.FileInfo
	entries  map[string]os.FileInfo
	files    []string
	dirs     []string
}

func preflightMediaDelete(path string) (*mediaDeletePlan, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("preflight media sidecar: %w", err)
	}
	if err := validateMediaDirectoryInfo(info); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("preflight media sidecar: %w", err)
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close() //nolint:errcheck
		return nil, errors.New("preflight media sidecar changed while opening")
	}
	plan := &mediaDeletePlan{
		path:     path,
		root:     root,
		rootInfo: info,
		entries:  make(map[string]os.FileInfo),
	}
	if err := plan.scan(); err != nil {
		plan.close()
		return nil, err
	}
	return plan, nil
}

func (p *mediaDeletePlan) scan() error {
	if p == nil || p.root == nil {
		return nil
	}
	return fs.WalkDir(p.root.FS(), ".", func(
		path string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return fmt.Errorf("preflight media sidecar entry: %w", walkErr)
		}
		info, err := p.root.Lstat(path)
		if err != nil {
			return fmt.Errorf("preflight media sidecar entry: %w", err)
		}
		if path == "." {
			return validateMediaDirectoryInfo(info)
		}
		if len(p.entries) >= maxMediaDeleteEntries {
			return errors.New("preflight media sidecar entry limit exceeded")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("preflight media sidecar contains a symbolic link")
		}
		if !validMediaSidecarPath(path, info.IsDir()) {
			return errors.New("preflight media sidecar contains an unexpected entry")
		}
		if info.IsDir() {
			if err := validateMediaDirectoryInfo(info); err != nil {
				return err
			}
			p.dirs = append(p.dirs, path)
		} else {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
				return errors.New("preflight media sidecar contains an unsafe file")
			}
			p.files = append(p.files, path)
		}
		p.entries[path] = info
		return nil
	})
}

func validMediaSidecarPath(path string, directory bool) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	parts := strings.Split(clean, "/")
	if directory {
		switch {
		case clean == "blobs", clean == "blobs/sha256":
			return true
		case len(parts) == 3 &&
			parts[0] == "blobs" &&
			parts[1] == "sha256":
			return mediaPrefixNamePattern.MatchString(parts[2])
		default:
			return false
		}
	}
	switch {
	case clean == "manifest.json":
		return true
	case len(parts) == 1:
		return manifestTempPattern.MatchString(parts[0])
	case len(parts) == 4 &&
		parts[0] == "blobs" &&
		parts[1] == "sha256" &&
		mediaPrefixNamePattern.MatchString(parts[2]):
		return (mediaDigestNamePattern.MatchString(parts[3]) &&
			strings.HasPrefix(parts[3], parts[2])) ||
			mediaTempNamePattern.MatchString(parts[3])
	default:
		return false
	}
}

func validateMediaDirectoryInfo(info os.FileInfo) error {
	if info == nil ||
		!info.IsDir() ||
		info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 {
		return errors.New("preflight media sidecar contains an unsafe directory")
	}
	return nil
}

func (p *mediaDeletePlan) revalidate() error {
	if p == nil || p.root == nil {
		return nil
	}
	current, err := os.Lstat(p.path)
	if err != nil ||
		validateMediaDirectoryInfo(current) != nil ||
		!os.SameFile(current, p.rootInfo) {
		return errors.New("preflight media sidecar changed before deletion")
	}
	seen := make(map[string]os.FileInfo)
	err = fs.WalkDir(p.root.FS(), ".", func(
		path string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		info, statErr := p.root.Lstat(path)
		if statErr != nil {
			return statErr
		}
		seen[path] = info
		return nil
	})
	if err != nil || len(seen) != len(p.entries) {
		return errors.New("preflight media sidecar changed before deletion")
	}
	for path, expected := range p.entries {
		current := seen[path]
		if current == nil ||
			current.Mode() != expected.Mode() ||
			!os.SameFile(current, expected) {
			return errors.New("preflight media sidecar changed before deletion")
		}
	}
	return nil
}

func (p *mediaDeletePlan) remove() error {
	if p == nil || p.root == nil {
		return nil
	}
	for _, path := range p.files {
		if err := p.root.Remove(path); err != nil {
			return fmt.Errorf("remove media sidecar file: %w", err)
		}
	}
	slices.SortFunc(p.dirs, func(left, right string) int {
		return strings.Count(right, "/") - strings.Count(left, "/")
	})
	for _, path := range p.dirs {
		if err := p.root.Remove(path); err != nil {
			return fmt.Errorf("remove media sidecar directory: %w", err)
		}
	}
	if err := p.root.Close(); err != nil {
		p.root = nil
		return fmt.Errorf("close media sidecar: %w", err)
	}
	p.root = nil
	current, err := os.Lstat(p.path)
	if err != nil ||
		!current.IsDir() ||
		!os.SameFile(current, p.rootInfo) {
		return errors.New("media sidecar root changed during deletion")
	}
	if err := os.Remove(p.path); err != nil {
		return fmt.Errorf("remove media sidecar root: %w", err)
	}
	return nil
}

func (p *mediaDeletePlan) close() {
	if p == nil || p.root == nil {
		return
	}
	_ = p.root.Close()
	p.root = nil
}

func preflightSessionDeleteTargets(
	dir string,
	sessionID string,
	requireTranscript bool,
) (string, os.FileInfo, error) {
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", nil, fmt.Errorf("resolve session directory: %w", err)
	}
	resolvedDir, err = filepath.Abs(resolvedDir)
	if err != nil {
		return "", nil, fmt.Errorf("resolve session directory: %w", err)
	}

	transcriptPath := filepath.Join(resolvedDir, sessionID+".jsonl")
	targets := []struct {
		path        string
		description string
		required    bool
	}{
		{
			path:        transcriptPath,
			description: "transcript",
			required:    requireTranscript,
		},
		{path: transcriptPath + ".tmp", description: "sidecar \".tmp\""},
		{path: transcriptPath + runtimeInputSidecarSuffix, description: "sidecar \"" + runtimeInputSidecarSuffix + "\""},
		{path: transcriptPath + projectGraphSidecarSuffix, description: "sidecar \"" + projectGraphSidecarSuffix + "\""},
	}

	var transcriptInfo os.FileInfo
	for _, target := range targets {
		if !pathWithin(resolvedDir, target.path) {
			return "", nil, fmt.Errorf("session %s %s escapes session directory", sessionID, target.description)
		}

		info, err := os.Lstat(target.path)
		if errors.Is(err, os.ErrNotExist) && !target.required {
			continue
		}
		if err != nil {
			if target.required && errors.Is(err, os.ErrNotExist) {
				return "", nil, fmt.Errorf("session %s does not exist: %w", sessionID, err)
			}
			return "", nil, fmt.Errorf("preflight session %s: %w", target.description, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", nil, fmt.Errorf("preflight session %s is not a regular file", target.description)
		}
		if target.path == transcriptPath {
			transcriptInfo = info
		}
	}

	return transcriptPath, transcriptInfo, nil
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// BulkDeleteOptions configures bulk session deletion.
type BulkDeleteOptions struct {
	// Dir is the session storage directory.
	Dir string
	// ProjectDir is used to resolve Dir when Dir is empty.
	ProjectDir string
	// OlderThan deletes sessions last modified before this time.
	OlderThan time.Time
	// GitBranch deletes sessions associated with this git branch.
	GitBranch string
	// Model deletes sessions that used this model (matched via metadata).
	Model string
}

// BulkDeleteResult contains results of a bulk delete operation.
type BulkDeleteResult struct {
	// Deleted is the list of successfully deleted session IDs.
	Deleted []string
	// Errors maps session IDs to errors encountered during deletion.
	Errors map[string]error
	// TotalBytesFreed is the sum of bytes freed across all deleted sessions.
	TotalBytesFreed int64
}

// BulkDeleteSessions deletes multiple sessions matching the given criteria.
// At least one filter criterion must be specified to prevent accidental deletion of all sessions.
// Returns partial results if some deletions fail.
func BulkDeleteSessions(opts BulkDeleteOptions) (*BulkDeleteResult, error) {
	if opts.OlderThan.IsZero() && opts.GitBranch == "" && opts.Model == "" {
		return nil, errors.New("at least one filter criterion must be specified for bulk delete")
	}

	dir := opts.Dir
	if dir == "" {
		dir = GetSessionDir(opts.ProjectDir)
	}

	// List all sessions to identify candidates.
	sessions, err := ListSessions(ListOptions{TranscriptDir: dir})
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	result := &BulkDeleteResult{
		Errors: make(map[string]error),
	}

	for _, sess := range sessions {
		if !matchesBulkDeleteCriteria(sess, opts) {
			continue
		}

		deleteResult, err := DeleteSession(DeleteOptions{
			SessionID: sess.SessionID,
			Dir:       dir,
		})
		if err != nil {
			result.Errors[sess.SessionID] = err
			continue
		}

		result.Deleted = append(result.Deleted, sess.SessionID)
		result.TotalBytesFreed += deleteResult.BytesFreed
	}

	return result, nil
}

// matchesBulkDeleteCriteria checks if a session matches the bulk delete criteria.
// All specified criteria are ANDed.
func matchesBulkDeleteCriteria(sess SessionInfo, opts BulkDeleteOptions) bool {
	if !opts.OlderThan.IsZero() {
		if !sess.LastModified.Before(opts.OlderThan) {
			return false
		}
	}

	if opts.GitBranch != "" {
		if sess.GitBranch != opts.GitBranch {
			return false
		}
	}

	if opts.Model != "" {
		// Model info may be in the summary or custom title for lite reads.
		// For a more accurate match, we'd need full metadata. For now, we
		// do a best-effort substring match on available fields.
		modelLower := strings.ToLower(opts.Model)
		if !strings.Contains(strings.ToLower(sess.Summary), modelLower) &&
			!strings.Contains(strings.ToLower(sess.CustomTitle), modelLower) {
			return false
		}
	}

	return true
}
