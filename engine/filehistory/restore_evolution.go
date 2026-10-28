package filehistory

import (
	"encoding/json"
	"fmt"
	"reflect"
	"time"
)

const maxRestoreEvolutionSteps = 512

// Evolution is published in the same atomic file as progress. Summaries contain
// no captured payload and no physical root, so a later import preserves them.
type restoreEvolution struct {
	Version int                    `json:"version"`
	Base    *restoreEvolutionState `json:"base"`
	Steps   []restoreEvolutionStep `json:"steps"`
}

type restoreEvolutionStep struct {
	PreviousSHA256  string                `json:"previous_sha256"`
	JournalRevision uint64                `json:"journal_revision"`
	After           restoreEvolutionState `json:"after"`
}

type restoreEvolutionState struct {
	Plan               restorePlan `json:"plan"`
	ConfirmationID     string      `json:"confirmation_id"`
	PermissionRevision string      `json:"permission_revision"`
	Direction          string      `json:"direction"`
	State              string      `json:"state"`
	Statuses           []string    `json:"statuses"`
}

func restoreSummary(op RestoreOperation) restoreEvolutionState {
	plan := restoreOperationPlan(op)
	plan.Root, plan.RootIdentity = "", ""
	s := restoreEvolutionState{Plan: plan, ConfirmationID: op.ConfirmationID, PermissionRevision: op.PermissionRevision, Direction: op.Direction, State: op.State, Statuses: make([]string, len(op.Paths))}
	for i, path := range op.Paths {
		s.Statuses[i] = path.Status
	}
	return s
}

func restoreSummarySHA(s *restoreEvolutionState) string {
	raw, _ := json.Marshal(s)
	return coldSHA(raw)
}

func validRestorePlanState(s restorePlanState) bool {
	return s.Captured && ((!s.Exists && s.Mode == 0 && s.Digest == "") || s.Exists && s.Mode & ^uint32(0o777) == 0 && coldHex(s.Digest))
}

func (j *RevisionJournal) validRestoreSummary(s restoreEvolutionState) error {
	p := s.Plan
	if p.Root != "" || p.RootIdentity != "" || !validRestoreText(p.ID) || !validRestoreText(p.TurnID) || p.SessionID != j.sessionID || p.JournalRevision > j.frameSequence || p.StartedAt.IsZero() || p.StartedAt.Location() != time.UTC || !validRestoreText(s.ConfirmationID) || !validRestoreText(s.PermissionRevision) || !validRestoreDirection(s.Direction) || !validRestoreState(s.State) || len(p.Paths) == 0 || len(p.Paths) > maxRestorePaths || len(s.Statuses) != len(p.Paths) {
		return fmt.Errorf("invalid restore evolution identity")
	}
	seen := map[string]bool{}
	previous := ""
	for i, path := range p.Paths {
		if path.Path <= previous || !validRestoreSiblingNames(p.ID, i, RestorePath{Path: path.Path, BackupPath: path.BackupPath, TempPath: path.TempPath}) || !validRestorePlanState(path.Original) || !validRestorePlanState(path.Target) || !validRestoreStatus(s.Statuses[i]) {
			return fmt.Errorf("invalid restore evolution path")
		}
		for _, name := range []string{path.Path, path.BackupPath, path.TempPath} {
			if !validStoredPath(name) || seen[name] {
				return fmt.Errorf("invalid restore evolution namespace")
			}
			seen[name] = true
		}
		previous = path.Path
		if s.State == "completed" && (s.Direction != "restore" || s.Statuses[i] != "applied" && s.Statuses[i] != "already_applied") || s.State == "rolled_back" && (s.Direction != "rollback" || s.Statuses[i] != "rolled_back" && s.Statuses[i] != "already_applied") {
			return fmt.Errorf("invalid terminal restore evolution")
		}
	}
	return nil
}

func validRestoreEvolutionTransition(previous *restoreEvolutionState, next restoreEvolutionState, revision uint64) bool {
	if previous == nil || previous.Plan.ID != next.Plan.ID {
		if previous != nil && !terminalRestoreState(previous.State) {
			return false
		}
		if next.State != "applying" || next.Direction != "restore" || next.Plan.JournalRevision != revision {
			return false
		}
		for _, status := range next.Statuses {
			if status != "pending" {
				return false
			}
		}
		return true
	}
	if !reflect.DeepEqual(previous.Plan, next.Plan) || terminalRestoreState(previous.State) || previous.Direction == "rollback" && next.Direction != "rollback" {
		return false
	}
	if terminalRestoreState(next.State) && previous.State != "applying" {
		return false
	}
	if previous.Direction != next.Direction || previous.State == "partial" && next.State == "applying" {
		return next.State == "applying" && reflect.DeepEqual(previous.Statuses, next.Statuses)
	}
	for i, status := range next.Statuses {
		old := previous.Statuses[i]
		if old == status || status == "failed" {
			continue
		}
		if next.Direction == "restore" {
			switch status {
			case "applied", "already_applied":
				if old == "rolled_back" {
					return false
				}
			case "conflict":
				if old != "pending" && old != "failed" && old != "conflict" {
					return false
				}
			default:
				return false
			}
		} else if status != "rolled_back" && status != "already_applied" {
			return false
		}
	}
	return true
}

func (j *RevisionJournal) validRestoreEnvelope(e restoreOperationEnvelope) error {
	if e.Kind != restoreOperationKind {
		return fmt.Errorf("invalid restore envelope kind")
	}
	if e.Version == restoreOperationVersion && e.Evolution == nil {
		return nil
	}
	chain := e.Evolution
	if e.Version != 2 || chain == nil || chain.Version != 1 || len(chain.Steps) == 0 || len(chain.Steps) > maxRestoreEvolutionSteps {
		return fmt.Errorf("invalid restore evolution envelope")
	}
	if e.Operation.Root != j.rootPath || e.Operation.RootIdentity != j.rootIdentity || e.Operation.PlanDigest != RestoreOperationPlanDigest(e.Operation) {
		return fmt.Errorf("restore evolution operation binding mismatch")
	}
	var payload int
	for _, path := range e.Operation.Paths {
		payload += len(path.Original.Data) + len(path.Target.Data)
		if !validCapturedRestoreState(path.Original) || !validCapturedRestoreState(path.Target) || payload > maxRestoreRawBytes {
			return fmt.Errorf("restore evolution payload invalid")
		}
	}
	previous := chain.Base
	if previous != nil && (j.validRestoreSummary(*previous) != nil || !terminalRestoreState(previous.State)) {
		return fmt.Errorf("invalid restore evolution baseline")
	}
	var revision uint64
	if previous != nil {
		revision = previous.Plan.JournalRevision
	}
	used := map[string]bool{}
	if previous != nil {
		used[previous.Plan.ID] = true
	}
	for _, step := range chain.Steps {
		if step.PreviousSHA256 != restoreSummarySHA(previous) || step.JournalRevision < revision || step.JournalRevision > j.frameSequence || step.After.Plan.JournalRevision > step.JournalRevision || j.validRestoreSummary(step.After) != nil || !validRestoreEvolutionTransition(previous, step.After, step.JournalRevision) {
			return fmt.Errorf("invalid restore evolution transition")
		}
		if previous == nil || previous.Plan.ID != step.After.Plan.ID {
			if used[step.After.Plan.ID] {
				return fmt.Errorf("restore operation identity reused")
			}
			used[step.After.Plan.ID] = true
		}
		after := step.After
		previous, revision = &after, step.JournalRevision
	}
	if !reflect.DeepEqual(*previous, restoreSummary(e.Operation)) {
		return fmt.Errorf("restore evolution head mismatch")
	}
	return nil
}

func (j *RevisionJournal) nextRestoreEnvelope(prior restoreOperationEnvelope, found bool, op RestoreOperation) (restoreOperationEnvelope, error) {
	e := restoreOperationEnvelope{Kind: restoreOperationKind, Version: restoreOperationVersion, Operation: op}
	if j.coldMigrationID == "" && prior.Evolution == nil {
		return e, nil
	}
	if found && prior.Operation.ID != op.ID {
		if err := (&coldState{journal: j}).verifyRestoreReceipts(prior.Operation); err != nil {
			return restoreOperationEnvelope{}, err
		}
		if err := j.restoreSiblingsAbsent(prior.Operation); err != nil {
			return restoreOperationEnvelope{}, err
		}
	}
	chain := &restoreEvolution{Version: 1, Steps: []restoreEvolutionStep{}}
	var previous *restoreEvolutionState
	if found {
		value := restoreSummary(prior.Operation)
		previous, chain.Base = &value, &value
	}
	if prior.Evolution != nil {
		chain.Base = prior.Evolution.Base
		chain.Steps = append(chain.Steps, prior.Evolution.Steps...)
	}
	after := restoreSummary(op)
	if previous != nil && reflect.DeepEqual(*previous, after) {
		return prior, nil
	}
	chain.Steps = append(chain.Steps, restoreEvolutionStep{PreviousSHA256: restoreSummarySHA(previous), JournalRevision: j.frameSequence, After: after})
	e.Version, e.Evolution = 2, chain
	if err := j.validRestoreEnvelope(e); err != nil {
		return restoreOperationEnvelope{}, err
	}
	return e, nil
}

func (s *coldState) verifyRestoreEvolution() error {
	if s.evolution == nil {
		return nil
	}
	states := make([]restoreEvolutionState, 0, len(s.evolution.Steps)+1)
	if s.evolution.Base != nil {
		states = append(states, *s.evolution.Base)
	}
	for _, step := range s.evolution.Steps {
		states = append(states, step.After)
	}
	for _, state := range states {
		if !terminalRestoreState(state.State) {
			continue
		}
		if err := s.journal.verifyRestoreReceiptPlan(state.Plan, state.Direction); err != nil {
			return err
		}
	}
	return nil
}

func restoreStateMetadata(s RevisionFileState) restorePlanState {
	return restorePlanState{Captured: s.Captured, Exists: s.Exists, Mode: s.Mode, Digest: s.Digest}
}

func (s *coldState) verifyEvolutionSiblingsAbsent(actual *RevisionJournal) error {
	if s.evolution == nil {
		return nil
	}
	states := make([]restoreEvolutionState, 0, len(s.evolution.Steps)+1)
	if s.evolution.Base != nil {
		states = append(states, *s.evolution.Base)
	}
	for _, step := range s.evolution.Steps {
		states = append(states, step.After)
	}
	for _, state := range states {
		if !terminalRestoreState(state.State) {
			continue
		}
		op := RestoreOperation{ID: state.Plan.ID}
		for _, p := range state.Plan.Paths {
			op.Paths = append(op.Paths, RestorePath{Path: p.Path, BackupPath: p.BackupPath, TempPath: p.TempPath})
		}
		if err := actual.restoreSiblingsAbsent(op); err != nil {
			return err
		}
	}
	return nil
}

// Check enough bounded metadata space before the first filesystem transition.
// It is a dry run of the existing writer, never a reservation or permission.
func (j *RevisionJournal) admitRestoreEvolutionLocked(op RestoreOperation) error {
	prior, found, err := j.readRestoreEnvelope(j.dirRoot)
	if err != nil {
		return err
	}
	if j.coldMigrationID == "" && prior.Evolution == nil {
		return nil
	}
	steps := len(op.Paths) + 2
	if prior.Evolution != nil && len(prior.Evolution.Steps)+steps > maxRestoreEvolutionSteps {
		return fmt.Errorf("restore evolution capacity exhausted")
	}
	e, err := j.nextRestoreEnvelope(prior, found, op)
	if err != nil {
		return err
	}
	// A summary's longest legal status is already_applied. Include every path
	// update and the final terminal save, without writing speculative progress.
	for i := 0; i < steps-1; i++ {
		s := restoreSummary(op)
		s.State = "rolled_back"
		for k := range s.Statuses {
			s.Statuses[k] = "already_applied"
		}
		e.Evolution.Steps = append(e.Evolution.Steps, restoreEvolutionStep{PreviousSHA256: restoreSummarySHA(&s), JournalRevision: j.frameSequence, After: s})
	}
	e.Operation.State = "rolled_back"
	e.Operation.Paths = append([]RestorePath(nil), e.Operation.Paths...)
	for i := range e.Operation.Paths {
		e.Operation.Paths[i].Status = "already_applied"
	}
	raw, err := json.Marshal(e)
	if err != nil || len(raw) > maxRestoreEncodedBytes || len(e.Evolution.Steps) > maxRestoreEvolutionSteps {
		return fmt.Errorf("restore evolution capacity exhausted")
	}
	return nil
}

func (j *RevisionJournal) verifyRestoreExtension(baseline, current map[string][]byte, baselineRevision uint64) error {
	if coldFileSHA(baseline, restoreOperationFile) == coldFileSHA(current, restoreOperationFile) {
		return nil
	}
	var old, next restoreOperationEnvelope
	if raw, found := baseline[restoreOperationFile]; found {
		if coldDecode(raw, &old) != nil || j.validRestoreEnvelope(old) != nil {
			return fmt.Errorf("invalid imported restore baseline")
		}
	}
	if coldDecode(current[restoreOperationFile], &next) != nil || j.validRestoreEnvelope(next) != nil || next.Evolution == nil {
		return fmt.Errorf("unproved imported restore mutation")
	}
	if old.Evolution == nil {
		var expected *restoreEvolutionState
		if old.Kind != "" {
			value := restoreSummary(old.Operation)
			expected = &value
		}
		if !reflect.DeepEqual(next.Evolution.Base, expected) {
			return fmt.Errorf("restore evolution baseline changed")
		}
	} else {
		n := len(old.Evolution.Steps)
		if len(next.Evolution.Steps) <= n || !reflect.DeepEqual(next.Evolution.Base, old.Evolution.Base) || !reflect.DeepEqual(next.Evolution.Steps[:n], old.Evolution.Steps) {
			return fmt.Errorf("restore evolution prefix changed")
		}
	}
	start := 0
	if old.Evolution != nil {
		start = len(old.Evolution.Steps)
	}
	for _, step := range next.Evolution.Steps[start:] {
		if step.JournalRevision < baselineRevision {
			return fmt.Errorf("restore evolution predates imported journal")
		}
	}
	return nil
}
