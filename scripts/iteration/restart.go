package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
)

type AttemptInfo struct {
	ID           string `json:"id"`
	Reason       string `json:"reason"`
	PreviousPath string `json:"previous_path"`
}

type restartJournal struct {
	SchemaVersion int      `json:"schema_version"`
	Source        Evidence `json:"source"`
	Target        Evidence `json:"target"`
}

var attemptPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func validRestartReason(reason string) bool {
	return oneOf(reason, "environment_timeout", "environment_repaired")
}

func validateAttempt(plan Plan, attempt *AttemptInfo) error {
	if attempt == nil {
		return nil // Legacy evidence is the first, unnamed attempt.
	}
	if !attemptPattern.MatchString(attempt.ID) || !validRestartReason(attempt.Reason) ||
		attempt.PreviousPath != path.Join("build", "iteration", "history", plan.DiffDigest, attempt.ID) {
		return errors.New("invalid verification attempt")
	}
	return nil
}

func restartable(evidence Evidence, reason string) bool {
	for _, gate := range evidence.Gates {
		if gate.DurationMillis == 0 || (gate.Status != GateFail && gate.Status != GateBlocked) {
			continue
		}
		if reason == "environment_repaired" {
			return true
		}
		timeout := gate.TimeoutMillis
		if timeout == 0 { // Retain compatibility with already-recorded timeouts.
			_, budget, _ := newCommandTargetRunner(evidence.Plan).command(gate.Target)
			timeout = budget.Milliseconds()
		}
		if gate.Status == GateFail && gate.ExitCode == nil && timeout > 0 && gate.DurationMillis >= timeout {
			return true
		}
	}
	return false
}

// Restart is called only under the worktree verification lock. It stages an
// empty attempt, journals it, then moves the old directory without rewriting
// any original evidence or log. An interrupted move is resumed explicitly.
func (s *fileEvidenceStore) Restart(plan Plan, reason string) (Evidence, error) {
	if !validRestartReason(reason) {
		return Evidence{}, errors.New("restart requires an environment recovery reason")
	}
	repository, err := os.OpenRoot(s.root)
	if err != nil {
		return Evidence{}, err
	}
	defer repository.Close()
	iteration, err := openStrictDir(repository, "build/iteration", true)
	if err != nil {
		return Evidence{}, err
	}
	defer iteration.Close()
	journal, exists, err := readRestartJournal(iteration)
	if err != nil {
		return Evidence{}, err
	}
	if exists {
		if !samePlan(journal.Source.Plan, plan) || journal.Target.Attempt.Reason != reason {
			return Evidence{}, errors.New("pending restart belongs to another plan or reason")
		}
		return s.completeRestart(repository, iteration, journal)
	}
	source, err := s.Load(plan)
	if err != nil {
		return Evidence{}, err
	}
	if !restartable(source, reason) {
		return Evidence{}, errors.New("restart requires a recorded executed failure or block matching its reason")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Evidence{}, err
	}
	id := hex.EncodeToString(nonce[:])
	target := emptyStoreEvidence(plan)
	target.Attempt = &AttemptInfo{ID: id, Reason: reason, PreviousPath: path.Join("build", "iteration", "history", plan.DiffDigest, id)}
	journal = restartJournal{SchemaVersion: 1, Source: source, Target: target}
	history, err := openStrictDir(repository, path.Dir(target.Attempt.PreviousPath), true)
	if err != nil {
		return Evidence{}, err
	}
	defer history.Close()
	stageName := "pending-" + id
	if err := history.Mkdir(stageName, 0o700); err != nil {
		return Evidence{}, err
	}
	stage, err := openStrictDir(history, stageName, false)
	if err != nil {
		return Evidence{}, err
	}
	defer stage.Close()
	if err := writeJSONAtomically(stage, "plan.json", plan, nil); err != nil {
		return Evidence{}, err
	}
	if err := writeJSONAtomically(stage, "evidence.json", target, nil); err != nil {
		return Evidence{}, err
	}
	if err := writeJSONAtomically(iteration, "restart.json", journal, nil); err != nil {
		return Evidence{}, err
	}
	return s.completeRestart(repository, iteration, journal)
}

func (s *fileEvidenceStore) completeRestart(repository, iteration *os.Root, journal restartJournal) (Evidence, error) {
	plan := journal.Source.Plan
	activePath := path.Join("build", "iteration", plan.DiffDigest)
	archivePath := journal.Target.Attempt.PreviousPath
	stagePath := path.Join(path.Dir(archivePath), "pending-"+journal.Target.Attempt.ID)
	active, activeExists, err := s.evidenceAt(repository, activePath, plan)
	if err != nil {
		return Evidence{}, err
	}
	archived, archiveExists, err := s.evidenceAt(repository, archivePath, plan)
	if err != nil {
		return Evidence{}, err
	}
	stage, stageExists, err := s.evidenceAt(repository, stagePath, plan)
	if err != nil {
		return Evidence{}, err
	}
	if archiveExists && !sameEvidence(archived, journal.Source) {
		return Evidence{}, errors.New("restart archive does not match original evidence")
	}
	move := s.move
	if move == nil {
		move = func(root *os.Root, from, to string) error { return root.Rename(from, to) }
	}
	if activeExists && sameEvidence(active, journal.Source) {
		if archiveExists || !stageExists || !sameEvidence(stage, journal.Target) {
			return Evidence{}, errors.New("restart staging state is inconsistent")
		}
		if err := move(repository, activePath, archivePath); err != nil {
			return Evidence{}, fmt.Errorf("archive previous attempt: %w", err)
		}
		activeExists, archiveExists = false, true
	}
	if !activeExists {
		if !archiveExists || !stageExists || !sameEvidence(stage, journal.Target) {
			return Evidence{}, errors.New("restart cannot recover a missing attempt")
		}
		if err := move(repository, stagePath, activePath); err != nil {
			return Evidence{}, fmt.Errorf("publish fresh attempt: %w", err)
		}
		active, activeExists, stageExists = journal.Target, true, false
	}
	if !activeExists || !sameEvidence(active, journal.Target) || !archiveExists || stageExists {
		return Evidence{}, errors.New("restart refuses to overwrite inconsistent or executed evidence")
	}
	if err := removeRegularFile(iteration, "restart.json"); err != nil {
		return Evidence{}, err
	}
	return journal.Target, nil
}

func (s *fileEvidenceStore) evidenceAt(root *os.Root, name string, plan Plan) (Evidence, bool, error) {
	dir, err := openStrictDir(root, name, false)
	if errors.Is(err, os.ErrNotExist) {
		return Evidence{}, false, nil
	}
	if err != nil {
		return Evidence{}, false, err
	}
	defer dir.Close()
	evidence, err := s.readStored(plan, dir)
	return evidence, true, err
}

func readRestartJournal(root *os.Root) (restartJournal, bool, error) {
	f, err := strictRegularFile(root, "restart.json")
	if errors.Is(err, os.ErrNotExist) {
		return restartJournal{}, false, nil
	}
	if err != nil {
		return restartJournal{}, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 2<<20 {
		return restartJournal{}, false, errors.New("restart journal exceeds its bound")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 2<<20))
	decoder.DisallowUnknownFields()
	var journal restartJournal
	if err := decoder.Decode(&journal); err != nil {
		return restartJournal{}, false, errors.New("invalid restart journal")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return restartJournal{}, false, errors.New("invalid restart journal trailing data")
	}
	if journal.SchemaVersion != 1 || journal.Target.Attempt == nil || !digestPattern.MatchString(journal.Source.Plan.DiffDigest) ||
		!samePlan(journal.Source.Plan, journal.Target.Plan) || len(journal.Target.Gates) != 0 ||
		validateStoredEvidence(journal.Source, journal.Source.Plan) != nil ||
		validateStoredEvidence(journal.Target, journal.Target.Plan) != nil ||
		!restartable(journal.Source, journal.Target.Attempt.Reason) {
		return restartJournal{}, false, errors.New("invalid restart journal contract")
	}
	return journal, true, nil
}

// A directory lock is portable and fail-closed after a crash. Never steal it:
// an operator must establish that no owner remains before removing a stale lock.
func acquireVerificationLock(root string) (func() error, error) {
	repository, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	iteration, err := openStrictDir(repository, "build/iteration", true)
	repository.Close()
	if err != nil {
		return nil, err
	}
	if err := iteration.Mkdir("verification.lock", 0o700); err != nil {
		iteration.Close()
		return nil, errors.New("verification is locked; do not restart or steal an active lock")
	}
	return func() error {
		defer iteration.Close()
		return iteration.Remove("verification.lock")
	}, nil
}

func rejectPendingRestart(root string) error {
	repository, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer repository.Close()
	iteration, err := openStrictDir(repository, "build/iteration", false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer iteration.Close()
	_, exists, err := readRestartJournal(iteration)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("restart is pending; explicitly resume the matching restart before verification")
	}
	return nil
}
