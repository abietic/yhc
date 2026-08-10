package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
)

// VerifyLevel deliberately has no "deep" or remote values: this store is for
// the two local claims that may participate in merge evidence.
type VerifyLevel string

const (
	VerifyFocused VerifyLevel = "focused"
	VerifyMerge   VerifyLevel = "merge"
)

type RunResult struct {
	Status           GateStatus
	ExitCode         *int
	DurationMillis   int64
	FailureLogPath   string
	FirstFailingSeed string
}

type EvidenceStore interface {
	Load(Plan) (Evidence, error)
	Record(Plan, GateEvidence) (Evidence, error)
}

type focusedEvidencePromoter interface {
	promoteFocused(Plan) (Evidence, error)
}

type focusedPromotion struct {
	SchemaVersion  int      `json:"schema_version"`
	SourcePlan     Plan     `json:"source_plan"`
	SourceEvidence Evidence `json:"source_evidence"`
	TargetPlan     Plan     `json:"target_plan"`
	TargetEvidence Evidence `json:"target_evidence"`
}

type fileEvidenceStore struct {
	root   string
	rename func(*os.Root, string, string) error
}

var (
	digestPattern                = regexp.MustCompile(`^[0-9a-f]{64}$`)
	failureSeedPattern           = regexp.MustCompile(`^Fuzz[A-Za-z0-9_]+/[0-9a-f]{16}$`)
	focusedEvidenceTargetPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	fixedEvidenceTargetPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

func newFileEvidenceStore(root string) *fileEvidenceStore { return &fileEvidenceStore{root: root} }

func (s *fileEvidenceStore) Load(plan Plan) (Evidence, error) {
	root, dir, err := s.openEvidenceDir(plan, false)
	if errors.Is(err, os.ErrNotExist) {
		return emptyStoreEvidence(plan), nil
	}
	if err != nil {
		return Evidence{}, err
	}
	defer root.Close()
	defer dir.Close()
	return s.readStored(plan, dir)
}

func (s *fileEvidenceStore) Record(plan Plan, gate GateEvidence) (Evidence, error) {
	if err := validateGate(plan, gate); err != nil {
		return Evidence{}, err
	}
	root, dir, err := s.openEvidenceDir(plan, true)
	if err != nil {
		return Evidence{}, err
	}
	defer root.Close()
	defer dir.Close()

	evidence, exists, err := s.loadOrCreate(plan, dir)
	if err != nil {
		return Evidence{}, err
	}
	if !exists {
		if err := writeJSONAtomically(dir, "plan.json", plan, nil); err != nil {
			return Evidence{}, err
		}
	}
	for i := range evidence.Gates {
		if evidence.Gates[i].Target == gate.Target && evidence.Gates[i].Level == gate.Level {
			if !mayReplace(evidence.Gates[i], gate) {
				return evidence, nil
			}
			evidence.Gates[i] = gate
			persisted, persistErr := s.persistTransition(dir, plan, evidence)
			return s.finishFirstRecord(dir, exists, persisted, persistErr)
		}
	}
	evidence.Gates = append(evidence.Gates, gate)
	persisted, persistErr := s.persistTransition(dir, plan, evidence)
	return s.finishFirstRecord(dir, exists, persisted, persistErr)
}

func (s *fileEvidenceStore) promoteFocused(plan Plan) (Evidence, error) {
	root, dir, err := s.openEvidenceDir(plan, false)
	if err != nil {
		return Evidence{}, err
	}
	defer root.Close()
	defer dir.Close()

	if promotion, exists, readErr := readFocusedPromotion(dir); readErr != nil {
		return Evidence{}, readErr
	} else if exists {
		return s.applyFocusedPromotion(dir, plan, promotion)
	}

	storedPlan, err := readPlan(dir)
	if err != nil {
		return Evidence{}, err
	}
	if samePlan(storedPlan, plan) {
		return s.readStored(plan, dir)
	}
	if !samePlanExceptHead(storedPlan, plan) {
		return Evidence{}, errors.New("stored plan is not an equivalent focused candidate")
	}
	stored, err := s.readStored(storedPlan, dir)
	if err != nil {
		return Evidence{}, err
	}
	if stored.State != "focused_verified" {
		return Evidence{}, errors.New("only successful focused evidence may move to a committed head")
	}
	for _, gate := range stored.Gates {
		if gate.Level != string(VerifyFocused) ||
			(gate.Status != GatePass && gate.Status != GateNotApplicable) {
			return Evidence{}, errors.New("focused promotion cannot carry failed, blocked, or merge evidence")
		}
	}

	promoted := stored
	promoted.Plan = plan
	promoted.State = derivedEvidenceState(promoted, plan)
	if promoted.State != "focused_verified" {
		return Evidence{}, errors.New("promoted evidence is not focused complete")
	}
	if err := validateStoredEvidence(promoted, plan); err != nil {
		return Evidence{}, err
	}
	promotion := focusedPromotion{
		SchemaVersion:  1,
		SourcePlan:     storedPlan,
		SourceEvidence: stored,
		TargetPlan:     plan,
		TargetEvidence: promoted,
	}
	if err := validateFocusedPromotion(promotion); err != nil {
		return Evidence{}, err
	}
	if err := writeJSONAtomically(dir, "promotion.json", promotion, s.rename); err != nil {
		return Evidence{}, fmt.Errorf("write focused promotion journal: %w", err)
	}
	promoted, err = s.applyFocusedPromotion(dir, plan, promotion)
	if err != nil {
		rollbackErr := rollbackFocusedPromotion(dir, promotion)
		if rollbackErr != nil {
			return Evidence{}, fmt.Errorf(
				"apply focused promotion: %w; restore source evidence: %w",
				err,
				rollbackErr,
			)
		}
		return Evidence{}, fmt.Errorf("apply focused promotion: %w", err)
	}
	return promoted, nil
}

func (s *fileEvidenceStore) applyFocusedPromotion(
	dir *os.Root,
	requested Plan,
	promotion focusedPromotion,
) (Evidence, error) {
	if err := validateFocusedPromotion(promotion); err != nil {
		return Evidence{}, err
	}
	if !samePlan(requested, promotion.TargetPlan) {
		return Evidence{}, errors.New("focused promotion journal targets another plan")
	}
	currentPlan, err := readPlan(dir)
	if err != nil {
		return Evidence{}, err
	}
	currentEvidence, err := readEvidence(dir)
	if err != nil {
		return Evidence{}, err
	}
	if !samePlan(currentPlan, promotion.SourcePlan) &&
		!samePlan(currentPlan, promotion.TargetPlan) {
		return Evidence{}, errors.New("focused promotion journal does not own current plan")
	}
	if !sameEvidence(currentEvidence, promotion.SourceEvidence) &&
		!sameEvidence(currentEvidence, promotion.TargetEvidence) {
		return Evidence{}, errors.New("focused promotion journal does not own current evidence")
	}
	if err := writeJSONAtomically(dir, "plan.json", promotion.TargetPlan, s.rename); err != nil {
		return Evidence{}, fmt.Errorf("write promoted plan: %w", err)
	}
	if err := writeJSONAtomically(dir, "evidence.json", promotion.TargetEvidence, s.rename); err != nil {
		return Evidence{}, fmt.Errorf("write promoted evidence: %w", err)
	}
	if err := removeRegularFile(dir, "promotion.json"); err != nil {
		return Evidence{}, fmt.Errorf("remove focused promotion journal: %w", err)
	}
	return promotion.TargetEvidence, nil
}

func rollbackFocusedPromotion(dir *os.Root, promotion focusedPromotion) error {
	if err := writeJSONAtomically(dir, "evidence.json", promotion.SourceEvidence, nil); err != nil {
		return err
	}
	if err := writeJSONAtomically(dir, "plan.json", promotion.SourcePlan, nil); err != nil {
		return err
	}
	return removeRegularFile(dir, "promotion.json")
}

func (s *fileEvidenceStore) finishFirstRecord(
	dir *os.Root,
	existed bool,
	evidence Evidence,
	err error,
) (Evidence, error) {
	if err == nil || existed {
		return evidence, err
	}
	_ = dir.Remove("evidence.json")
	_ = dir.Remove("plan.json")
	return Evidence{}, err
}

func (s *fileEvidenceStore) openEvidenceDir(plan Plan, create bool) (*os.Root, *os.Root, error) {
	if !digestPattern.MatchString(plan.DiffDigest) {
		return nil, nil, errors.New("invalid iteration diff digest")
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, nil, fmt.Errorf("open repository root: %w", err)
	}
	dir, err := openStrictDir(root, path.Join("build", "iteration", plan.DiffDigest), create)
	if err != nil {
		root.Close()
		return nil, nil, err
	}
	return root, dir, nil
}

func openStrictDir(root *os.Root, name string, create bool) (*os.Root, error) {
	current := root
	ownedCurrent := false
	for _, component := range strings.Split(name, "/") {
		info, err := current.Lstat(component)
		if errors.Is(err, os.ErrNotExist) && create {
			if err := current.Mkdir(component, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				if ownedCurrent {
					current.Close()
				}
				return nil, err
			}
			info, err = current.Lstat(component)
		}
		if err != nil {
			if ownedCurrent {
				current.Close()
			}
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			if ownedCurrent {
				current.Close()
			}
			return nil, fmt.Errorf("iteration path component %q is not a directory", component)
		}
		next, err := current.OpenRoot(component)
		if ownedCurrent {
			current.Close()
		}
		if err != nil {
			return nil, err
		}
		current, ownedCurrent = next, true
	}
	return current, nil
}

func (s *fileEvidenceStore) loadOrCreate(plan Plan, dir *os.Root) (Evidence, bool, error) {
	planFile, planErr := strictRegularFile(dir, "plan.json")
	if planFile != nil {
		_ = planFile.Close()
	}
	evidenceFile, evidenceErr := strictRegularFile(dir, "evidence.json")
	if evidenceFile != nil {
		_ = evidenceFile.Close()
	}
	if errors.Is(planErr, os.ErrNotExist) && errors.Is(evidenceErr, os.ErrNotExist) {
		return emptyStoreEvidence(plan), false, nil
	}
	if planErr != nil || evidenceErr != nil {
		return Evidence{}, false, errors.New("iteration documents must be created together")
	}
	evidence, err := s.readStored(plan, dir)
	return evidence, true, err
}

func (s *fileEvidenceStore) readStored(plan Plan, dir *os.Root) (Evidence, error) {
	storedPlan, err := readPlan(dir)
	if err != nil {
		return Evidence{}, err
	}
	if !samePlan(storedPlan, plan) {
		return Evidence{}, errors.New("stored plan does not match requested plan")
	}
	evidence, err := readEvidence(dir)
	if err != nil {
		return Evidence{}, err
	}
	if !samePlan(evidence.Plan, plan) {
		return Evidence{}, errors.New("embedded evidence plan does not match requested plan")
	}
	if err := validateStoredEvidence(evidence, plan); err != nil {
		return Evidence{}, err
	}
	return evidence, nil
}

func readEvidence(dir *os.Root) (Evidence, error) {
	f, err := strictRegularFile(dir, "evidence.json")
	if err != nil {
		return Evidence{}, err
	}
	defer f.Close()
	return decodeEvidence(f)
}

func readFocusedPromotion(dir *os.Root) (focusedPromotion, bool, error) {
	f, err := strictRegularFile(dir, "promotion.json")
	if errors.Is(err, os.ErrNotExist) {
		return focusedPromotion{}, false, nil
	}
	if err != nil {
		return focusedPromotion{}, false, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	var promotion focusedPromotion
	if err := decoder.Decode(&promotion); err != nil {
		return focusedPromotion{}, false, fmt.Errorf("decode focused promotion: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return focusedPromotion{}, false, fmt.Errorf("decode trailing focused promotion: %w", err)
		}
		return focusedPromotion{}, false, errors.New("decode focused promotion: multiple JSON values are not allowed")
	}
	if err := validateFocusedPromotion(promotion); err != nil {
		return focusedPromotion{}, false, err
	}
	return promotion, true, nil
}

func validateFocusedPromotion(promotion focusedPromotion) error {
	if promotion.SchemaVersion != 1 ||
		samePlan(promotion.SourcePlan, promotion.TargetPlan) ||
		!samePlanExceptHead(promotion.SourcePlan, promotion.TargetPlan) {
		return errors.New("invalid focused promotion plans")
	}
	if !samePlan(promotion.SourceEvidence.Plan, promotion.SourcePlan) ||
		!samePlan(promotion.TargetEvidence.Plan, promotion.TargetPlan) {
		return errors.New("focused promotion evidence has the wrong plan")
	}
	if err := validateStoredEvidence(promotion.SourceEvidence, promotion.SourcePlan); err != nil {
		return err
	}
	if err := validateStoredEvidence(promotion.TargetEvidence, promotion.TargetPlan); err != nil {
		return err
	}
	if promotion.SourceEvidence.State != "focused_verified" ||
		promotion.TargetEvidence.State != "focused_verified" {
		return errors.New("focused promotion evidence is not focused complete")
	}
	for _, evidence := range []Evidence{promotion.SourceEvidence, promotion.TargetEvidence} {
		for _, gate := range evidence.Gates {
			if gate.Level != string(VerifyFocused) ||
				(gate.Status != GatePass && gate.Status != GateNotApplicable) {
				return errors.New("focused promotion contains unsafe gate evidence")
			}
		}
	}
	normalized := promotion.TargetEvidence
	normalized.Plan = promotion.SourcePlan
	if !sameEvidence(normalized, promotion.SourceEvidence) {
		return errors.New("focused promotion changes more than plan head")
	}
	return nil
}

func readPlan(dir *os.Root) (Plan, error) {
	f, err := strictRegularFile(dir, "plan.json")
	if err != nil {
		return Plan{}, err
	}
	defer f.Close()
	return decodePlan(f)
}

func decodePlan(reader io.Reader) (Plan, error) {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var plan Plan
	if err := decoder.Decode(&plan); err != nil {
		return Plan{}, fmt.Errorf("decode plan: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return Plan{}, fmt.Errorf("decode trailing plan: %w", err)
		}
		return Plan{}, errors.New("decode plan: multiple JSON values are not allowed")
	}
	if plan.SchemaVersion != 1 || !digestPattern.MatchString(plan.DiffDigest) {
		return Plan{}, errors.New("invalid stored plan")
	}
	return plan, nil
}

func strictRegularFile(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	return root.Open(name)
}

func removeRegularFile(root *os.Root, name string) error {
	f, err := strictRegularFile(root, name)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return root.Remove(name)
}

func samePlan(left, right Plan) bool {
	leftJSON, leftErr := canonicalJSON(left)
	rightJSON, rightErr := canonicalJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func sameEvidence(left, right Evidence) bool {
	leftJSON, leftErr := canonicalJSON(left)
	rightJSON, rightErr := canonicalJSON(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func samePlanExceptHead(left, right Plan) bool {
	left.Head = right.Head
	return samePlan(left, right)
}

func canonicalJSON(value any) ([]byte, error) { return json.Marshal(value) }

func emptyStoreEvidence(plan Plan) Evidence {
	return Evidence{SchemaVersion: 1, Plan: plan, State: stateForPlan(plan)}
}

func validateGate(plan Plan, gate GateEvidence) error {
	if !oneOf(gate.Level, string(VerifyFocused), string(VerifyMerge)) ||
		!oneOf(string(gate.Status), string(GatePass), string(GateFail), string(GateBlocked), string(GateNotApplicable)) ||
		gate.DurationMillis < 0 {
		return errors.New("invalid gate evidence")
	}
	if !slices.Contains(expectedTargets(plan, VerifyLevel(gate.Level)), gate.Target) || !safeTarget(gate.Target) {
		return errors.New("invalid gate target")
	}
	if gate.FirstFailingSeed != "" && !failureSeedPattern.MatchString(gate.FirstFailingSeed) {
		return errors.New("invalid first failing seed")
	}
	if gate.FirstFailingSeed != "" && gate.Status != GateFail {
		return errors.New("only a failed gate may retain a fuzz seed")
	}
	if gate.FailureLogPath != "" && gate.Status != GateFail && gate.Status != GateBlocked {
		return errors.New("only a failed or blocked gate may retain a log")
	}
	wantLog := ""
	if gate.FailureLogPath != "" {
		wantLog = path.Join("build", "iteration", plan.DiffDigest, "logs", safeTargetLogName(gate.Target)+".log")
	}
	if gate.FailureLogPath != "" && gate.FailureLogPath != wantLog {
		return errors.New("invalid failure log path")
	}
	return nil
}

func safeTarget(target string) bool {
	if target == "head-tree-clean" || target == "fmt" || strings.HasPrefix(target, "focused/") {
		if strings.ContainsAny(target, "\\\r\n\x00") || strings.Contains(target, "..") || strings.Count(target, "/") > 1 {
			return false
		}
		if strings.HasPrefix(target, "focused/") {
			owner := strings.TrimPrefix(target, "focused/")
			return owner != "" && focusedEvidenceTargetPattern.MatchString(owner)
		}
		return true
	}
	return fixedEvidenceTargetPattern.MatchString(target)
}

func safeTargetLogName(target string) string { return strings.ReplaceAll(target, "/", "-") }

func expectedTargets(plan Plan, level VerifyLevel) []string {
	if level == VerifyFocused {
		return focusedTargets(plan)
	}
	return append([]string{"fmt", "head-tree-clean"}, mergeTargets(plan)...)
}

func validateStoredEvidence(evidence Evidence, plan Plan) error {
	if err := validateEvidence(evidence); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(evidence.Gates))
	for _, gate := range evidence.Gates {
		if err := validateGate(plan, gate); err != nil {
			return err
		}
		key := gate.Level + "\x00" + gate.Target
		if _, exists := seen[key]; exists {
			return errors.New("duplicate evidence gate")
		}
		seen[key] = struct{}{}
	}
	desired := derivedEvidenceState(evidence, plan)
	if evidence.State != desired && (evidence.State != "merge_verified" || desired != "evidence_ready") {
		return errors.New("stored evidence state is not derived from its gates")
	}
	return nil
}

func mayReplace(existing, next GateEvidence) bool {
	return existing.Target == next.Target && existing.Level == next.Level &&
		existing.Status == GateBlocked && existing.DurationMillis == 0 && existing.ExitCode == nil &&
		(next.Status == GatePass || next.Status == GateFail || next.Status == GateBlocked)
}

func (s *fileEvidenceStore) persistTransition(dir *os.Root, plan Plan, evidence Evidence) (Evidence, error) {
	slices.SortFunc(evidence.Gates, func(left, right GateEvidence) int {
		if left.Level == right.Level {
			return strings.Compare(left.Target, right.Target)
		}
		return strings.Compare(left.Level, right.Level)
	})
	evidence.Plan = plan
	desired := derivedEvidenceState(evidence, plan)
	if desired == "evidence_ready" {
		evidence.State = "merge_verified"
		if err := writeJSONAtomically(dir, "evidence.json", evidence, s.rename); err != nil {
			return Evidence{}, err
		}
		evidence.State = desired
		if err := writeJSONAtomically(dir, "evidence.json", evidence, s.rename); err != nil {
			return Evidence{}, err
		}
		return evidence, nil
	}
	evidence.State = desired
	if err := writeJSONAtomically(dir, "evidence.json", evidence, s.rename); err != nil {
		return Evidence{}, err
	}
	return evidence, nil
}

func derivedEvidenceState(evidence Evidence, plan Plan) string {
	if len(plan.Changed) == 0 {
		return "planned"
	}
	if !allExpectedComplete(evidence, expectedTargets(plan, VerifyFocused), VerifyFocused) {
		return "changed"
	}
	if !allExpectedComplete(evidence, expectedTargets(plan, VerifyMerge), VerifyMerge) {
		return "focused_verified"
	}
	return "evidence_ready"
}

func allExpectedComplete(evidence Evidence, targets []string, level VerifyLevel) bool {
	for _, target := range targets {
		gate := gateFor(evidence, target, string(level))
		if gate == nil || (gate.Status != GatePass && gate.Status != GateNotApplicable) {
			return false
		}
	}
	return true
}

func writeJSONAtomically(dir *os.Root, name string, value any, rename func(*os.Root, string, string) error) error {
	data, err := canonicalJSON(value)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := "." + name + "-" + hex.EncodeToString(nonce[:])
	f, err := dir.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Remove(temp) }()
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if closeErr := f.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return writeErr
	}
	if rename != nil {
		return rename(dir, temp, name)
	}
	return dir.Rename(temp, name)
}
