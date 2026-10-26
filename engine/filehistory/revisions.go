package filehistory

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	journalFile       = "revisions.jsonl"
	maxRevisionBytes  = 1 << 20
	maxJournalBytes   = 32 << 20
	maxLogicalRecords = 200
	maxFrameBytes     = 3 << 20
)

var (
	ErrJournalCapacity = errors.New("revision journal capacity reached")
	ErrJournalClosed   = errors.New("revision journal is sealed")
)

// RevisionFileState is one bounded, captured filesystem state.
type RevisionFileState struct {
	Captured bool   `json:"captured"`
	Exists   bool   `json:"exists"`
	Mode     uint32 `json:"mode"`
	Data     []byte `json:"data,omitempty"`
	Digest   string `json:"digest,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// RevisionRecord pairs a tool intent with its observed result. It never restores files.
type RevisionRecord struct {
	ID, SessionID, TurnID, ToolUseID, ToolName, Path, Outcome, Reason string
	Sequence                                                          uint64
	Timestamp                                                         time.Time
	Before, After                                                     RevisionFileState
}

// RevisionHeader omits bytes so callers can list revisions without mutable file data.
type RevisionHeader struct {
	ID, SessionID, TurnID, ToolUseID, ToolName, Path, Outcome, Reason string
	Sequence                                                          uint64
	Timestamp                                                         time.Time
	BeforeCaptured, AfterCaptured, Changed                            bool
}

type RevisionJournalSnapshot struct {
	Records  []RevisionHeader
	Turns    []TurnRecord
	Warning  string
	Revision uint64
}

type journalFrame struct {
	Version       int            `json:"version"`
	Type          string         `json:"type"`
	Root          string         `json:"root"`
	RootIdentity  string         `json:"root_identity"`
	SessionID     string         `json:"session_id"`
	FrameSequence uint64         `json:"frame_sequence"`
	Record        RevisionRecord `json:"record"`
	Turn          *TurnRecord    `json:"turn,omitempty"`
}

// RevisionJournal is a private, append-only observer of workspace files.
type RevisionJournal struct {
	mu sync.Mutex

	rootPath, dir, sessionID string
	rootIdentity             string
	root, dirRoot            *os.Root
	records                  map[string]RevisionRecord
	order                    []string
	turns                    map[string]TurnRecord
	turnOrder                []string
	frameSequence            uint64
	bytes                    int64
	warning                  string
	sealed                   bool
	closed                   bool
	syncDirectory            func(*os.Root) error
	captureOpened            func(string)
}

func OpenRevisionJournal(root, dir, sessionID string) (*RevisionJournal, error) {
	if !safeSessionID(sessionID) {
		return nil, fmt.Errorf("invalid revision journal session ID")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("make workspace root absolute: %w", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return nil, fmt.Errorf("canonicalize workspace root: %w", err)
	}
	info, err := os.Stat(canonicalRoot)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory")
	}
	r, err := os.OpenRoot(canonicalRoot)
	if err != nil {
		return nil, fmt.Errorf("open workspace root: %w", err)
	}
	rootIdentity, err := durableRootIdentity(r)
	if err != nil {
		r.Close()
		return nil, fmt.Errorf("read workspace root identity: %w", err)
	}
	opened, err := r.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		r.Close()
		return nil, fmt.Errorf("workspace root changed during open")
	}
	absoluteDir, err := filepath.Abs(dir)
	if err != nil {
		r.Close()
		return nil, fmt.Errorf("make revision journal directory absolute: %w", err)
	}
	j := &RevisionJournal{rootPath: canonicalRoot, rootIdentity: rootIdentity, dir: absoluteDir, sessionID: sessionID, root: r, records: map[string]RevisionRecord{}, turns: map[string]TurnRecord{}, syncDirectory: defaultSyncDirectory}
	if err := j.prepareDir(); err != nil {
		j.Close()
		return nil, err
	}
	if err := j.replay(); err != nil {
		j.Close()
		return nil, err
	}
	return j, nil
}

func safeSessionID(s string) bool {
	return s != "" && filepath.Base(s) == s && s != "." && s != ".." && !strings.ContainsAny(s, `/\\`)
}

func (j *RevisionJournal) prepareDir() error {
	if dirInfo, err := os.Lstat(j.dir); err == nil {
		if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o700 {
			return fmt.Errorf("revision journal directory must be a non-symlink 0700 directory")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat revision journal directory: %w", err)
	} else {
		return nil
	}
	if err := j.openDirRoot(); err != nil {
		return err
	}
	if info, err := j.dirRoot.Lstat(journalFile); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return fmt.Errorf("revision journal log must be a non-symlink 0600 regular file")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat revision journal log: %w", err)
	}
	return nil
}

func (j *RevisionJournal) openDirRoot() error {
	if j.dirRoot != nil {
		return nil
	}
	r, err := os.OpenRoot(j.dir)
	if err != nil {
		return fmt.Errorf("open revision journal directory: %w", err)
	}
	j.dirRoot = r
	return nil
}

func (j *RevisionJournal) ensureDirForAppend() error {
	if _, err := os.Lstat(j.dir); os.IsNotExist(err) {
		var missing []string
		for d := j.dir; ; d = filepath.Dir(d) {
			if _, err := os.Lstat(d); err == nil {
				break
			} else if !os.IsNotExist(err) {
				return err
			}
			missing = append(missing, d)
		}
		for n := len(missing) - 1; n >= 0; n-- {
			d := missing[n]
			if err := os.Mkdir(d, 0o700); err != nil {
				return err
			}
			parent, err := os.OpenRoot(filepath.Dir(d))
			if err != nil {
				return err
			}
			err = j.syncDirectory(parent)
			closeErr := parent.Close()
			if err != nil || closeErr != nil {
				return fmt.Errorf("sync revision journal parent directory")
			}
		}
	}
	info, err := os.Lstat(j.dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fmt.Errorf("revision journal directory must be a non-symlink 0700 directory")
	}
	return j.openDirRoot()
}

func defaultSyncDirectory(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func (j *RevisionJournal) replay() error {
	if j.dirRoot == nil {
		return nil
	}
	f, err := j.dirRoot.Open(journalFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat revision journal: %w", err)
	}
	info, statErr := f.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		f.Close()
		return fmt.Errorf("revision journal log must be a 0600 regular file")
	}
	b, readErr := io.ReadAll(io.LimitReader(f, maxJournalBytes+1))
	closeErr := f.Close()
	if readErr != nil || closeErr != nil || len(b) > maxJournalBytes {
		return fmt.Errorf("read revision journal")
	}
	j.bytes = int64(len(b))
	if len(b) == 0 {
		return nil
	}
	if b[len(b)-1] != '\n' {
		return fmt.Errorf("revision journal has truncated final frame")
	}
	for _, line := range bytes.Split(b[:len(b)-1], []byte{'\n'}) {
		if len(line) == 0 || len(line) > maxFrameBytes {
			return fmt.Errorf("invalid revision journal frame size")
		}
		var f journalFrame
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			return fmt.Errorf("invalid revision journal frame")
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			return fmt.Errorf("invalid revision journal frame")
		}
		if err := j.applyFrame(f); err != nil {
			return err
		}
	}
	return nil
}

func (j *RevisionJournal) applyFrame(f journalFrame) error {
	if f.Version != 1 || f.Root != j.rootPath || f.RootIdentity != j.rootIdentity || f.SessionID != j.sessionID || f.FrameSequence != j.frameSequence+1 {
		return fmt.Errorf("revision journal identity or sequence mismatch")
	}
	switch f.Type {
	case "before":
		if f.Turn != nil || validRecord(f.Record, j.sessionID) != nil {
			return fmt.Errorf("invalid revision journal record")
		}
		if len(j.order) >= maxLogicalRecords || f.Record.Sequence != uint64(len(j.order)+1) || f.Record.After.Captured || f.Record.Outcome != "" || !validStoredPath(f.Record.Path) {
			return fmt.Errorf("invalid revision journal before frame")
		}
		if _, exists := j.records[f.Record.ID]; exists {
			return fmt.Errorf("duplicate revision journal ID")
		}
		j.records[f.Record.ID] = cloneRecord(f.Record)
		j.order = append(j.order, f.Record.ID)
	case "after":
		if f.Turn != nil || validRecord(f.Record, j.sessionID) != nil {
			return fmt.Errorf("invalid revision journal record")
		}
		prior, exists := j.records[f.Record.ID]
		if !exists || validOutcome(prior.Outcome) || !sameIntent(prior, f.Record) || !sameState(prior.Before, f.Record.Before) || !validOutcome(f.Record.Outcome) || !validStoredPath(f.Record.Path) {
			return fmt.Errorf("invalid revision journal after frame")
		}
		j.records[f.Record.ID] = cloneRecord(f.Record)
	case "gap":
		if f.Turn != nil || validRecord(f.Record, j.sessionID) != nil {
			return fmt.Errorf("invalid revision journal record")
		}
		if len(j.order) >= maxLogicalRecords || f.Record.Sequence != uint64(len(j.order)+1) || f.Record.Before.Captured || f.Record.After.Captured || f.Record.Path != "" || f.Record.ID == "" || f.Record.Outcome != "" || f.Record.Reason == "" {
			return fmt.Errorf("invalid revision journal gap frame")
		}
		if _, exists := j.records[f.Record.ID]; exists {
			return fmt.Errorf("duplicate revision journal ID")
		}
		j.records[f.Record.ID] = cloneRecord(f.Record)
		j.order = append(j.order, f.Record.ID)
	case "turn_start":
		if !emptyRevisionRecord(f.Record) || !validTurnStart(f.Turn, j.sessionID) || len(j.turnOrder) >= maxLogicalRecords {
			return fmt.Errorf("invalid revision journal turn start")
		}
		if _, exists := j.turns[f.Turn.TurnID]; exists {
			return fmt.Errorf("duplicate revision journal turn")
		}
		j.turns[f.Turn.TurnID] = *f.Turn
		j.turnOrder = append(j.turnOrder, f.Turn.TurnID)
	case "turn_terminal":
		if !emptyRevisionRecord(f.Record) || !validTurnTerminal(f.Turn, j.sessionID) {
			return fmt.Errorf("invalid revision journal turn terminal")
		}
		prior, exists := j.turns[f.Turn.TurnID]
		if !exists || !prior.FinishedAt.IsZero() || !sameTurnStart(prior, *f.Turn) {
			return fmt.Errorf("invalid revision journal turn transition")
		}
		j.turns[f.Turn.TurnID] = *f.Turn
	default:
		return fmt.Errorf("unknown revision journal frame")
	}
	j.frameSequence = f.FrameSequence
	return nil
}

func validRecord(r RevisionRecord, sessionID string) error {
	if r.ID == "" || r.SessionID != sessionID || r.Timestamp.Location() != time.UTC || !validState(r.Before) || !validState(r.After) {
		return fmt.Errorf("invalid revision journal record")
	}
	return nil
}

func emptyRevisionRecord(r RevisionRecord) bool {
	return r.ID == "" && r.SessionID == "" && r.TurnID == "" && r.ToolUseID == "" && r.ToolName == "" && r.Path == "" && r.Outcome == "" && r.Reason == "" && r.Sequence == 0 && r.Timestamp.IsZero() && !r.Before.Captured && !r.Before.Exists && r.Before.Mode == 0 && len(r.Before.Data) == 0 && r.Before.Digest == "" && r.Before.Reason == "" && !r.After.Captured && !r.After.Exists && r.After.Mode == 0 && len(r.After.Data) == 0 && r.After.Digest == "" && r.After.Reason == ""
}

func validState(s RevisionFileState) bool {
	if !s.Captured {
		return !s.Exists && s.Mode == 0 && len(s.Data) == 0 && s.Digest == "" && s.Reason != ""
	}
	if !s.Exists {
		return s.Mode == 0 && len(s.Data) == 0 && s.Digest == "" && s.Reason == ""
	}
	if s.Mode&^0o777 != 0 || s.Reason != "" || len(s.Data) > maxRevisionBytes || !utf8.Valid(s.Data) || bytes.IndexByte(s.Data, 0) >= 0 {
		return false
	}
	sum := sha256.Sum256(s.Data)
	return s.Digest == hex.EncodeToString(sum[:])
}

func sameIntent(a, b RevisionRecord) bool {
	return a.ID == b.ID && a.SessionID == b.SessionID && a.TurnID == b.TurnID && a.ToolUseID == b.ToolUseID && a.ToolName == b.ToolName && a.Path == b.Path && a.Reason == b.Reason && a.Sequence == b.Sequence && a.Timestamp.Equal(b.Timestamp)
}

func validOutcome(outcome string) bool {
	return outcome == "succeeded" || outcome == "failed" || outcome == "cancelled"
}

func validStoredPath(path string) bool {
	if path == "" || !utf8.ValidString(path) || filepath.IsAbs(path) || filepath.VolumeName(path) != "" || filepath.Clean(path) != path {
		return false
	}
	path = filepath.Clean(path)
	if path == "." || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return false
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == ".git" {
			return false
		}
	}
	return true
}

func sameState(a, b RevisionFileState) bool {
	return a.Captured == b.Captured && a.Exists == b.Exists && a.Mode == b.Mode && a.Digest == b.Digest && a.Reason == b.Reason && bytes.Equal(a.Data, b.Data)
}

func (j *RevisionJournal) Begin(turnID, toolUseID, toolName, path string) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.sealed {
		return "", ErrJournalClosed
	}
	rel, err := j.cleanPath(path)
	if err != nil {
		return "", err
	}
	if len(j.order) >= maxLogicalRecords {
		j.warning = ErrJournalCapacity.Error()
		return "", ErrJournalCapacity
	}
	before, err := j.capture(rel)
	if err != nil {
		return "", err
	}
	id, err := randomID()
	if err != nil {
		return "", err
	}
	r := RevisionRecord{ID: id, SessionID: j.sessionID, TurnID: turnID, ToolUseID: toolUseID, ToolName: toolName, Path: rel, Sequence: uint64(len(j.order) + 1), Timestamp: time.Now().UTC(), Before: before, After: RevisionFileState{Captured: false, Reason: "pending"}}
	if err := j.append("before", r); err != nil {
		return "", err
	}
	j.records[id] = cloneRecord(r)
	j.order = append(j.order, id)
	return id, nil
}

func (j *RevisionJournal) Finish(id, outcome string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.sealed {
		return ErrJournalClosed
	}
	if !validOutcome(outcome) {
		return fmt.Errorf("invalid revision outcome")
	}
	r, ok := j.records[id]
	if !ok || r.Path == "" || validOutcome(r.Outcome) {
		return fmt.Errorf("unknown or finished revision")
	}
	after, err := j.capture(r.Path)
	if err != nil {
		return err
	}
	r.Outcome, r.After = outcome, after
	if err := j.append("after", r); err != nil {
		return err
	}
	j.records[id] = cloneRecord(r)
	return nil
}

func (j *RevisionJournal) RecordUncovered(turnID, toolUseID, toolName, reason string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.sealed {
		return ErrJournalClosed
	}
	if len(j.order) >= maxLogicalRecords {
		j.warning = ErrJournalCapacity.Error()
		return ErrJournalCapacity
	}
	id, err := randomID()
	if err != nil {
		return err
	}
	r := RevisionRecord{ID: id, SessionID: j.sessionID, TurnID: turnID, ToolUseID: toolUseID, ToolName: toolName, Reason: reason, Sequence: uint64(len(j.order) + 1), Timestamp: time.Now().UTC(), Before: RevisionFileState{Reason: "uncovered"}, After: RevisionFileState{Reason: "uncovered"}}
	if err := j.append("gap", r); err != nil {
		return err
	}
	j.records[id] = cloneRecord(r)
	j.order = append(j.order, id)
	return nil
}

func (j *RevisionJournal) cleanPath(path string) (string, error) {
	if path == "" || !utf8.ValidString(path) || filepath.IsAbs(path) {
		return "", fmt.Errorf("revision path must be workspace-relative")
	}
	rel := filepath.Clean(path)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("revision path escapes workspace")
	}
	for _, c := range strings.Split(rel, string(filepath.Separator)) {
		if c == ".git" {
			return "", fmt.Errorf("revision path cannot access .git")
		}
	}
	if err := j.noSymlinkComponents(rel); err != nil {
		return "", err
	}
	return rel, nil
}

func (j *RevisionJournal) noSymlinkComponents(rel string) error {
	parts := strings.Split(rel, string(filepath.Separator))
	for i := range parts {
		name := filepath.Join(parts[:i+1]...)
		info, err := j.root.Lstat(name)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lstat revision path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("revision path has symlink component")
		}
	}
	return nil
}

func (j *RevisionJournal) capture(rel string) (RevisionFileState, error) {
	if err := j.noSymlinkComponents(rel); err != nil {
		return RevisionFileState{}, err
	}
	info, err := j.root.Lstat(rel)
	if os.IsNotExist(err) {
		return RevisionFileState{Captured: true}, nil
	}
	if err != nil {
		return RevisionFileState{}, fmt.Errorf("lstat revision file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return RevisionFileState{Reason: "non_regular"}, nil
	}
	if info.Size() > maxRevisionBytes {
		return RevisionFileState{Reason: "too_large"}, nil
	}
	f, err := j.root.Open(rel)
	if err != nil {
		return RevisionFileState{}, fmt.Errorf("open revision file: %w", err)
	}
	opened, statErr := f.Stat()
	if statErr != nil || !os.SameFile(info, opened) || !sameCaptureMeta(info, opened) {
		f.Close()
		return RevisionFileState{Reason: "changed_during_capture"}, nil
	}
	if j.captureOpened != nil {
		j.captureOpened(rel)
	}
	b, readErr := io.ReadAll(io.LimitReader(f, maxRevisionBytes+1))
	ended, endStatErr := f.Stat()
	closeErr := f.Close()
	current, currentErr := j.root.Lstat(rel)
	if readErr != nil || closeErr != nil {
		return RevisionFileState{}, fmt.Errorf("read revision file")
	}
	if endStatErr != nil || currentErr != nil || !os.SameFile(opened, ended) || !os.SameFile(opened, current) || !sameCaptureMeta(opened, ended) || !sameCaptureMeta(opened, current) {
		return RevisionFileState{Reason: "changed_during_capture"}, nil
	}
	if len(b) > maxRevisionBytes {
		return RevisionFileState{Reason: "too_large"}, nil
	}
	if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
		return RevisionFileState{Reason: "binary"}, nil
	}
	sum := sha256.Sum256(b)
	return RevisionFileState{Captured: true, Exists: true, Mode: uint32(opened.Mode().Perm()), Data: b, Digest: hex.EncodeToString(sum[:])}, nil
}

func sameCaptureMeta(a, b os.FileInfo) bool {
	return a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (j *RevisionJournal) append(kind string, r RevisionRecord) error {
	return j.appendFrame(journalFrame{Version: 1, Type: kind, Root: j.rootPath, SessionID: j.sessionID, Record: r})
}

func (j *RevisionJournal) appendTurn(kind string, turn TurnRecord) error {
	return j.appendFrame(journalFrame{Version: 1, Type: kind, Root: j.rootPath, SessionID: j.sessionID, Turn: &turn})
}

func (j *RevisionJournal) appendFrame(f journalFrame) error {
	if err := j.checkRootIdentity(); err != nil {
		return j.seal(err)
	}
	f.RootIdentity = j.rootIdentity
	f.FrameSequence = j.frameSequence + 1
	b, err := json.Marshal(f)
	if err != nil || len(b)+1 > maxFrameBytes {
		return j.seal(fmt.Errorf("encode revision journal frame"))
	}
	if j.bytes+int64(len(b)+1) > maxJournalBytes {
		j.warning = ErrJournalCapacity.Error()
		return ErrJournalCapacity
	}
	if err := j.ensureDirForAppend(); err != nil {
		return j.seal(err)
	}
	if j.dirRoot == nil {
		return j.seal(fmt.Errorf("revision journal directory unavailable"))
	}
	created := false
	if info, err := j.dirRoot.Lstat(journalFile); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return j.seal(fmt.Errorf("revision journal log changed type"))
		}
	} else if os.IsNotExist(err) {
		created = true
	} else {
		return j.seal(err)
	}
	fh, err := j.dirRoot.OpenFile(journalFile, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return j.seal(err)
	}
	info, statErr := fh.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != j.bytes {
		fh.Close()
		return j.seal(fmt.Errorf("revision journal changed during append"))
	}
	_, writeErr := fh.Write(append(b, '\n'))
	syncErr := fh.Sync()
	closeErr := fh.Close()
	if created && writeErr == nil && syncErr == nil && closeErr == nil {
		syncErr = j.syncDirectory(j.dirRoot)
	}
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return j.seal(fmt.Errorf("revision journal append uncertainty"))
	}
	j.bytes += int64(len(b) + 1)
	j.frameSequence++
	return nil
}

func (j *RevisionJournal) seal(err error) error {
	j.sealed = true
	j.warning = err.Error()
	return err
}

func (j *RevisionJournal) Snapshot() RevisionJournalSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := RevisionJournalSnapshot{Warning: j.warning, Revision: j.frameSequence, Records: make([]RevisionHeader, 0, len(j.order)), Turns: make([]TurnRecord, 0, len(j.turnOrder))}
	for _, id := range j.order {
		r := j.records[id]
		reason := r.Reason
		if reason == "" && !r.Before.Captured {
			reason = r.Before.Reason
		}
		if reason == "" && !r.After.Captured {
			reason = r.After.Reason
		}
		s.Records = append(s.Records, RevisionHeader{ID: r.ID, SessionID: r.SessionID, TurnID: r.TurnID, ToolUseID: r.ToolUseID, ToolName: r.ToolName, Path: r.Path, Outcome: r.Outcome, Reason: reason, Sequence: r.Sequence, Timestamp: r.Timestamp, BeforeCaptured: r.Before.Captured, AfterCaptured: r.After.Captured, Changed: !sameState(r.Before, r.After)})
	}
	for _, id := range j.turnOrder {
		s.Turns = append(s.Turns, j.turns[id])
	}
	return s
}

func (j *RevisionJournal) Read(id string) (RevisionRecord, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	r, ok := j.records[id]
	return cloneRecord(r), ok
}

func (j *RevisionJournal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.sealed = true
	j.closed = true
	var errs []error
	if j.dirRoot != nil {
		errs = append(errs, j.dirRoot.Close())
	}
	errs = append(errs, j.root.Close())
	return errors.Join(errs...)
}

func cloneRecord(r RevisionRecord) RevisionRecord {
	r.Before.Data = append([]byte(nil), r.Before.Data...)
	r.After.Data = append([]byte(nil), r.After.Data...)
	return r
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate revision ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func (j *RevisionJournal) checkRootIdentity() error {
	current, err := os.Lstat(j.rootPath)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("workspace root is unavailable or replaced")
	}
	opened, err := j.root.Stat(".")
	if err != nil || !os.SameFile(current, opened) {
		return fmt.Errorf("workspace root was replaced")
	}
	identity, err := durableRootIdentity(j.root)
	if err != nil || identity != j.rootIdentity {
		return fmt.Errorf("workspace root identity changed")
	}
	return nil
}
