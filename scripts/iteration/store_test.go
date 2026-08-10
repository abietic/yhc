package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvidenceStoreInitialEvidenceHasNoSyntheticGates(t *testing.T) {
	plan := storePlan()
	store := newFileEvidenceStore(t.TempDir())
	evidence, err := store.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.State != "changed" || len(evidence.Gates) != 0 {
		t.Fatalf("initial evidence = %#v, want changed with no gates", evidence)
	}
}

func TestEvidenceStoreReplacesOnlyUnexecutedBlockedGate(t *testing.T) {
	plan := storePlan()
	store := newFileEvidenceStore(t.TempDir())
	if _, err := store.Record(plan, GateEvidence{Target: "test-contract", Level: string(VerifyFocused), Status: GateBlocked}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(plan, GateEvidence{Target: "test-contract", Level: string(VerifyFocused), Status: GatePass}); err != nil {
		t.Fatal(err)
	}
	evidence, err := store.Record(plan, GateEvidence{Target: "test-contract", Level: string(VerifyFocused), Status: GateFail})
	if err != nil {
		t.Fatal(err)
	}
	if got := gateFor(evidence, "test-contract", string(VerifyFocused)); got == nil || got.Status != GatePass {
		t.Fatalf("immutable gate = %#v, want pass", got)
	}
}

func TestEvidenceStoreExecutedAndNotApplicableGatesAreImmutable(t *testing.T) {
	plan := storePlan()
	store := newFileEvidenceStore(t.TempDir())
	code := 1
	for _, gate := range []GateEvidence{
		{Target: "test-contract", Level: string(VerifyFocused), Status: GateBlocked, DurationMillis: 1},
		{Target: "fmt", Level: string(VerifyMerge), Status: GateNotApplicable},
	} {
		if _, err := store.Record(plan, gate); err != nil {
			t.Fatal(err)
		}
	}
	for _, gate := range []GateEvidence{
		{Target: "test-contract", Level: string(VerifyFocused), Status: GateFail, ExitCode: &code},
		{Target: "fmt", Level: string(VerifyMerge), Status: GatePass},
	} {
		if _, err := store.Record(plan, gate); err != nil {
			t.Fatal(err)
		}
	}
	evidence, err := store.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := gateFor(evidence, "test-contract", string(VerifyFocused)); got == nil || got.DurationMillis != 1 || got.Status != GateBlocked {
		t.Fatalf("executed blocked gate = %#v", got)
	}
	if got := gateFor(evidence, "fmt", string(VerifyMerge)); got == nil || got.Status != GateNotApplicable {
		t.Fatalf("N/A gate = %#v", got)
	}
}

func TestEvidenceStorePromotesEquivalentSuccessfulFocusedPlanToCommittedHead(t *testing.T) {
	plan := storePlan()
	plan.Head = plan.Base
	store := newFileEvidenceStore(t.TempDir())
	for _, target := range focusedTargets(plan) {
		if _, err := store.Record(plan, GateEvidence{
			Target: target, Level: string(VerifyFocused), Status: GatePass,
		}); err != nil {
			t.Fatal(err)
		}
	}

	committed := plan
	committed.Head = strings.Repeat("d", 40)
	if _, err := store.Load(committed); err == nil {
		t.Fatal("ordinary Load rebound focused evidence to a new head")
	}
	promoted, err := store.promoteFocused(committed)
	if err != nil {
		t.Fatal(err)
	}
	if promoted.State != "focused_verified" || promoted.Plan.Head != committed.Head {
		t.Fatalf("promoted evidence = %#v", promoted)
	}
	if _, err := store.Load(committed); err != nil {
		t.Fatalf("Load committed plan after promotion: %v", err)
	}
	if _, err := store.Load(plan); err == nil {
		t.Fatal("promotion left the pre-commit plan active")
	}
}

func TestEvidenceStorePromotionRejectsNonEquivalentOrUnsafeCandidates(t *testing.T) {
	tests := []struct {
		name   string
		seed   func(*testing.T, EvidenceStore, Plan)
		mutate func(*Plan)
	}{
		{
			name: "different policy",
			seed: seedFocusedEvidence,
			mutate: func(plan *Plan) {
				plan.PolicyVersion++
			},
		},
		{
			name: "incomplete focused evidence",
			seed: func(t *testing.T, store EvidenceStore, plan Plan) {
				t.Helper()
				if _, err := store.Record(plan, GateEvidence{
					Target: focusedTargets(plan)[0], Level: string(VerifyFocused), Status: GatePass,
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "failed focused evidence",
			seed: func(t *testing.T, store EvidenceStore, plan Plan) {
				t.Helper()
				if _, err := store.Record(plan, GateEvidence{
					Target: focusedTargets(plan)[0], Level: string(VerifyFocused), Status: GateFail,
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "executed blocked focused evidence",
			seed: func(t *testing.T, store EvidenceStore, plan Plan) {
				t.Helper()
				if _, err := store.Record(plan, GateEvidence{
					Target: focusedTargets(plan)[0], Level: string(VerifyFocused), Status: GateBlocked, DurationMillis: 1,
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "merge evidence present",
			seed: func(t *testing.T, store EvidenceStore, plan Plan) {
				t.Helper()
				seedFocusedEvidence(t, store, plan)
				if _, err := store.Record(plan, GateEvidence{
					Target: "fmt", Level: string(VerifyMerge), Status: GatePass,
				}); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			plan := storePlan()
			plan.Head = plan.Base
			store := newFileEvidenceStore(root)
			test.seed(t, store, plan)
			base := filepath.Join(root, "build", "iteration", plan.DiffDigest)
			beforePlan, err := os.ReadFile(filepath.Join(base, "plan.json"))
			if err != nil {
				t.Fatal(err)
			}
			beforeEvidence, err := os.ReadFile(filepath.Join(base, "evidence.json"))
			if err != nil {
				t.Fatal(err)
			}

			committed := plan
			committed.Head = strings.Repeat("d", 40)
			if test.mutate != nil {
				test.mutate(&committed)
			}
			if _, err := store.promoteFocused(committed); err == nil {
				t.Fatal("unsafe focused promotion succeeded")
			}
			afterPlan, planErr := os.ReadFile(filepath.Join(base, "plan.json"))
			afterEvidence, evidenceErr := os.ReadFile(filepath.Join(base, "evidence.json"))
			if planErr != nil || evidenceErr != nil ||
				!bytes.Equal(beforePlan, afterPlan) || !bytes.Equal(beforeEvidence, afterEvidence) {
				t.Fatalf("rejected promotion changed source: planErr=%v evidenceErr=%v", planErr, evidenceErr)
			}
		})
	}
}

func TestEvidenceStorePromotionWriteFailureRestoresSourcePlan(t *testing.T) {
	root := t.TempDir()
	plan := storePlan()
	plan.Head = plan.Base
	store := newFileEvidenceStore(root)
	seedFocusedEvidence(t, store, plan)
	committed := plan
	committed.Head = strings.Repeat("d", 40)

	calls := 0
	store.rename = func(root *os.Root, old, new string) error {
		calls++
		if calls == 3 {
			return errors.New("promoted evidence rename fault")
		}
		return root.Rename(old, new)
	}
	if _, err := store.promoteFocused(committed); err == nil {
		t.Fatal("promotion succeeded when evidence write failed")
	}
	store.rename = nil
	if _, err := store.Load(plan); err != nil {
		t.Fatalf("source plan was not restored: %v", err)
	}
	if _, err := store.Load(committed); err == nil {
		t.Fatal("failed promotion activated committed plan")
	}
}

func TestEvidenceStorePromotionJournalRecoversInterruptedReplacement(t *testing.T) {
	root := t.TempDir()
	plan := storePlan()
	plan.Head = plan.Base
	store := newFileEvidenceStore(root)
	seedFocusedEvidence(t, store, plan)
	committed := plan
	committed.Head = strings.Repeat("d", 40)

	store.rename = func(root *os.Root, old, new string) error {
		if err := root.Rename(old, new); err != nil {
			return err
		}
		if new == "plan.json" {
			panic("simulated process interruption")
		}
		return nil
	}
	interrupted := false
	func() {
		defer func() {
			if recover() != nil {
				interrupted = true
			}
		}()
		_, _ = store.promoteFocused(committed)
	}()
	if !interrupted {
		t.Fatal("promotion did not reach the simulated interruption")
	}
	if _, err := store.Load(plan); err == nil {
		t.Fatal("interrupted promotion exposed the source plan as complete")
	}
	if _, err := store.Load(committed); err == nil {
		t.Fatal("interrupted promotion exposed the target plan as complete")
	}

	recovered, err := newFileEvidenceStore(root).promoteFocused(committed)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != "focused_verified" || recovered.Plan.Head != committed.Head {
		t.Fatalf("recovered promotion = %#v", recovered)
	}
	journal := filepath.Join(root, "build", "iteration", plan.DiffDigest, "promotion.json")
	if _, err := os.Lstat(journal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("promotion journal remains after recovery: %v", err)
	}
}

func TestEvidenceStorePromotionJournalRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	plan := storePlan()
	plan.Head = plan.Base
	store := newFileEvidenceStore(root)
	seedFocusedEvidence(t, store, plan)
	committed := plan
	committed.Head = strings.Repeat("d", 40)
	base := filepath.Join(root, "build", "iteration", plan.DiffDigest)
	sentinel := filepath.Join(t.TempDir(), "promotion.json")
	if err := os.WriteFile(sentinel, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, filepath.Join(base, "promotion.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.promoteFocused(committed); err == nil {
		t.Fatal("promotion followed a journal symlink")
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "{}" {
		t.Fatalf("journal sentinel changed: contents=%q err=%v", contents, err)
	}
}

func TestEvidenceTransitionRequiresCompleteExpectedGateSets(t *testing.T) {
	plan := storePlan()
	store := newFileEvidenceStore(t.TempDir())
	for _, gate := range []GateEvidence{
		{Target: "focused/iteration", Level: string(VerifyFocused), Status: GatePass},
		{Target: "test-contract", Level: string(VerifyFocused), Status: GatePass},
		{Target: "fmt", Level: string(VerifyMerge), Status: GatePass},
		{Target: "head-tree-clean", Level: string(VerifyMerge), Status: GatePass},
	} {
		if _, err := store.Record(plan, gate); err != nil {
			t.Fatal(err)
		}
	}
	evidence, err := store.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.State != "focused_verified" {
		t.Fatalf("state with missing merge targets = %q, want focused_verified", evidence.State)
	}
	for _, target := range mergeTargets(plan) {
		if _, err := store.Record(plan, GateEvidence{Target: target, Level: string(VerifyMerge), Status: GatePass}); err != nil {
			t.Fatal(err)
		}
	}
	evidence, err = store.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.State != "evidence_ready" {
		t.Fatalf("completed evidence state = %q, want evidence_ready", evidence.State)
	}
}

func TestEvidenceStoreRejectsUnsafeInputsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	plan := storePlan()
	store := newFileEvidenceStore(root)
	if _, err := store.Load(Plan{DiffDigest: "../outside"}); err == nil {
		t.Fatal("Load accepted traversal digest")
	}
	for _, target := range []string{"../test", `focused/../owner`, `focused\\owner`, "focused/a/b", "bad\nname"} {
		if _, err := store.Record(plan, GateEvidence{Target: target, Level: string(VerifyFocused), Status: GatePass}); err == nil {
			t.Fatalf("Record accepted unsafe target %q", target)
		}
	}
	dir := filepath.Join(root, "build", "iteration")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, plan.DiffDigest)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(plan); err == nil {
		t.Fatal("Load followed digest directory symlink")
	}
}

func TestEvidenceStoreRejectsPlanAndEvidenceDocumentAttacks(t *testing.T) {
	root := t.TempDir()
	plan := storePlan()
	store := newFileEvidenceStore(root)
	if _, err := store.Record(plan, GateEvidence{Target: "test-contract", Level: string(VerifyFocused), Status: GatePass}); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "build", "iteration", plan.DiffDigest)
	if err := os.WriteFile(filepath.Join(base, "plan.json"), []byte(`{"schema_version":1,"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(plan); err == nil {
		t.Fatal("Load accepted unknown plan field")
	}
	if err := os.WriteFile(filepath.Join(base, "plan.json"), append(canonicalPlan(t, plan), []byte(`{}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(plan); err == nil {
		t.Fatal("Load accepted second plan JSON value")
	}
	if err := os.Remove(filepath.Join(base, "plan.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("evidence.json", filepath.Join(base, "plan.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(plan); err == nil {
		t.Fatal("Load followed plan symlink")
	}
}

func TestEvidenceStoreRejectsEvidenceAttacksAndPlanMismatch(t *testing.T) {
	root := t.TempDir()
	plan := storePlan()
	store := newFileEvidenceStore(root)
	if _, err := store.Record(plan, GateEvidence{Target: "test-contract", Level: string(VerifyFocused), Status: GatePass}); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "build", "iteration", plan.DiffDigest)
	evidencePath := filepath.Join(base, "evidence.json")
	original, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string][]byte{
		"unknown field": []byte(`{"schema_version":1,"unknown":true}`),
		"second value":  append(append([]byte(nil), original...), []byte(`{}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(evidencePath, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load(plan); err == nil {
				t.Fatalf("Load accepted %s", name)
			}
		})
	}
	if err := os.WriteFile(evidencePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	var tampered Evidence
	if err := json.Unmarshal(original, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.Plan.Head = strings.Repeat("d", 40)
	tamperedJSON, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(evidencePath, tamperedJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(plan); err == nil {
		t.Fatal("Load accepted a mismatched embedded plan")
	}
	if err := os.Remove(evidencePath); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(t.TempDir(), "sentinel.json")
	if err := os.WriteFile(sentinel, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sentinel, evidencePath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(plan); err == nil {
		t.Fatal("Load followed an evidence symlink")
	}
	unchanged, err := os.ReadFile(sentinel)
	if err != nil || string(unchanged) != string(original) {
		t.Fatalf("outside sentinel changed: err=%v", err)
	}
}

func TestEvidenceStoreRejectsUnsafeMetadata(t *testing.T) {
	plan := storePlan()
	store := newFileEvidenceStore(t.TempDir())
	validLog := filepath.ToSlash(filepath.Join("build", "iteration", plan.DiffDigest, "logs", "test-contract.log"))
	for _, gate := range []GateEvidence{
		{Target: "test-contract", Level: string(VerifyFocused), Status: GatePass, FailureLogPath: validLog},
		{Target: "test-contract", Level: string(VerifyFocused), Status: GateBlocked, FailureLogPath: "../outside.log"},
		{Target: "test-contract", Level: string(VerifyFocused), Status: GatePass, FirstFailingSeed: "FuzzName/0123456789abcdef"},
		{Target: "test-contract", Level: string(VerifyFocused), Status: GateFail, FirstFailingSeed: "FuzzName/../../outside"},
	} {
		if _, err := store.Record(plan, gate); err == nil {
			t.Fatalf("Record accepted unsafe gate %#v", gate)
		}
	}
}

func TestEvidenceStoreFirstRenameFailureLeavesNoPartialPair(t *testing.T) {
	plan := storePlan()
	store := newFileEvidenceStore(t.TempDir())
	store.rename = func(*os.Root, string, string) error { return errors.New("rename fault") }
	if _, err := store.Record(plan, GateEvidence{Target: "test-contract", Level: string(VerifyFocused), Status: GatePass}); err == nil {
		t.Fatal("Record succeeded with first-write rename failure")
	}
	base := filepath.Join(store.root, "build", "iteration", plan.DiffDigest)
	for _, name := range []string{"plan.json", "evidence.json"} {
		if _, err := os.Lstat(filepath.Join(base, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("partial %s remains: %v", name, err)
		}
	}
}

func TestEvidenceStoreRenameFailurePreservesOldEvidence(t *testing.T) {
	plan := storePlan()
	store := newFileEvidenceStore(t.TempDir())
	if _, err := store.Record(plan, GateEvidence{Target: "test-contract", Level: string(VerifyFocused), Status: GateBlocked}); err != nil {
		t.Fatal(err)
	}
	store.rename = func(*os.Root, string, string) error { return errors.New("rename fault") }
	if _, err := store.Record(plan, GateEvidence{Target: "test-contract", Level: string(VerifyFocused), Status: GatePass}); err == nil {
		t.Fatal("Record succeeded with injected rename failure")
	}
	store.rename = nil
	evidence, err := store.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	if got := gateFor(evidence, "test-contract", string(VerifyFocused)); got == nil || got.Status != GateBlocked {
		t.Fatalf("evidence after failed rename = %#v", got)
	}
	entries, err := os.ReadDir(filepath.Join(store.root, "build", "iteration", plan.DiffDigest))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".evidence-") {
			t.Fatalf("temporary evidence file remains: %s", entry.Name())
		}
	}
}

func TestEvidenceStoreReadyWriteFailureLeavesMergeVerified(t *testing.T) {
	plan := storePlan()
	store := newFileEvidenceStore(t.TempDir())
	for _, gate := range []GateEvidence{
		{Target: "focused/iteration", Level: string(VerifyFocused), Status: GatePass},
		{Target: "test-contract", Level: string(VerifyFocused), Status: GatePass},
		{Target: "fmt", Level: string(VerifyMerge), Status: GatePass},
		{Target: "head-tree-clean", Level: string(VerifyMerge), Status: GatePass},
	} {
		if _, err := store.Record(plan, gate); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range mergeTargets(plan)[:len(mergeTargets(plan))-1] {
		if _, err := store.Record(plan, GateEvidence{Target: target, Level: string(VerifyMerge), Status: GatePass}); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	store.rename = func(root *os.Root, old, new string) error {
		calls++
		if calls == 2 {
			return errors.New("ready rename fault")
		}
		return root.Rename(old, new)
	}
	last := mergeTargets(plan)[len(mergeTargets(plan))-1]
	if _, err := store.Record(plan, GateEvidence{Target: last, Level: string(VerifyMerge), Status: GatePass}); err == nil {
		t.Fatal("Record succeeded when evidence_ready write failed")
	}
	store.rename = nil
	evidence, err := store.Load(plan)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.State != "merge_verified" {
		t.Fatalf("state after ready write failure = %q, want merge_verified", evidence.State)
	}
}

func storePlan() Plan {
	return Plan{
		SchemaVersion:   1,
		Repository:      "github.com/abietic/yhc",
		PolicyVersion:   1,
		Base:            strings.Repeat("a", 40),
		Head:            strings.Repeat("b", 40),
		DiffDigest:      strings.Repeat("c", 64),
		Changed:         []ChangedPath{{Path: "scripts/iteration/store.go", Status: "M", Owner: "iteration", Kind: PathProduction}},
		FocusedChecks:   []FocusedCheck{{Owner: "iteration"}},
		RequiredTargets: []string{"fmt", "lint", "test", "build", "docs-check-ci", "docs-check", "git-diff-check", "test-contract"},
	}
}

func canonicalPlan(t *testing.T, plan Plan) []byte {
	t.Helper()
	data, err := canonicalJSON(plan)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
