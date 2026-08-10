package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

type VerifyOptions struct {
	Level VerifyLevel
	Plan  Plan
}

type planAwareTargetRunner interface {
	UsePlan(Plan)
}

var (
	inspectMergeTree = cleanTree
	lstatTargetPath  = os.Lstat
)

func verify(
	ctx context.Context,
	root string,
	options VerifyOptions,
	runner TargetRunner,
	store EvidenceStore,
	replan func(context.Context) (Plan, error),
) (Evidence, error) {
	if runner == nil || store == nil {
		return Evidence{}, errors.New("verification dependencies are unavailable")
	}
	if options.Level != VerifyFocused && options.Level != VerifyMerge {
		return Evidence{}, errors.New("invalid verify level")
	}
	if options.Level == VerifyFocused {
		return runTargetSequence(
			ctx,
			root,
			options.Plan,
			VerifyFocused,
			focusedTargets(options.Plan),
			runner,
			store,
		)
	}
	if replan == nil {
		return Evidence{}, errors.New("merge verification replanner is unavailable")
	}
	return verifyMerge(ctx, root, options.Plan, runner, store, replan)
}

func verifyMerge(
	ctx context.Context,
	root string,
	plan Plan,
	runner TargetRunner,
	store EvidenceStore,
	replan func(context.Context) (Plan, error),
) (Evidence, error) {
	current, err := store.Load(plan)
	if err != nil {
		promoter, ok := store.(focusedEvidencePromoter)
		if !ok {
			return Evidence{}, err
		}
		if inspectErr := inspectMergeTree(ctx, root, plan); inspectErr != nil {
			return Evidence{}, inspectErr
		}
		current, err = promoter.promoteFocused(plan)
		if err != nil {
			return Evidence{}, err
		}
	}
	if !targetsSatisfied(current, string(VerifyFocused), focusedTargets(plan)) {
		return current, errors.New("merge verification requires current focused evidence")
	}

	merge := mergeTargets(plan)
	started := time.Now()
	if err := inspectMergeTree(ctx, root, plan); err != nil {
		gate := GateEvidence{
			Target:         "head-tree-clean",
			Level:          string(VerifyMerge),
			Status:         GateBlocked,
			DurationMillis: nonZeroDurationMillis(started),
		}
		current, recordErr := store.Record(plan, gate)
		if recordErr != nil {
			return Evidence{}, recordErr
		}
		return recordUnexecuted(store, plan, VerifyMerge, current, append([]string{"fmt"}, merge...))
	}

	fmtResult := runner.Run(ctx, root, plan.DiffDigest, "fmt")
	fmtGate := gateFromRunResult("fmt", VerifyMerge, fmtResult)
	current, err = store.Record(plan, fmtGate)
	if err != nil {
		return Evidence{}, err
	}
	storedFmt := gateFor(current, "fmt", string(VerifyMerge))
	if storedFmt == nil {
		return Evidence{}, errors.New("format result was not persisted")
	}
	if storedFmt.Status == GateFail || storedFmt.Status == GateBlocked {
		return recordUnexecuted(
			store,
			plan,
			VerifyMerge,
			current,
			append([]string{"head-tree-clean"}, merge...),
		)
	}

	postFormatPlan, err := replan(ctx)
	if err != nil {
		return current, fmt.Errorf("rebuild post-format plan: %w", err)
	}
	if updater, ok := runner.(planAwareTargetRunner); ok {
		updater.UsePlan(postFormatPlan)
	} else if postFormatPlan.Base != plan.Base {
		return current, errors.New("verification runner cannot accept changed merge base")
	}
	if postFormatPlan.DiffDigest != plan.DiffDigest {
		_, err = store.Record(postFormatPlan, *storedFmt)
		if err != nil {
			return Evidence{}, err
		}
	}
	plan = postFormatPlan

	started = time.Now()
	if err := inspectMergeTree(ctx, root, plan); err != nil {
		current, recordErr := store.Record(plan, GateEvidence{
			Target:         "head-tree-clean",
			Level:          string(VerifyMerge),
			Status:         GateBlocked,
			DurationMillis: nonZeroDurationMillis(started),
		})
		if recordErr != nil {
			return Evidence{}, recordErr
		}
		return recordUnexecuted(store, plan, VerifyMerge, current, merge)
	}
	current, err = store.Record(plan, GateEvidence{
		Target:         "head-tree-clean",
		Level:          string(VerifyMerge),
		Status:         GatePass,
		DurationMillis: nonZeroDurationMillis(started),
	})
	if err != nil {
		return Evidence{}, err
	}
	if !targetsSatisfied(current, string(VerifyFocused), focusedTargets(plan)) {
		current, blockErr := recordUnexecuted(store, plan, VerifyMerge, current, merge)
		if blockErr != nil {
			return Evidence{}, blockErr
		}
		return current, errors.New("post-format plan requires new focused evidence")
	}
	return runTargetSequence(ctx, root, plan, VerifyMerge, merge, runner, store)
}

func runTargetSequence(
	ctx context.Context,
	root string,
	plan Plan,
	level VerifyLevel,
	targets []string,
	runner TargetRunner,
	store EvidenceStore,
) (Evidence, error) {
	evidence, err := store.Load(plan)
	if err != nil {
		return Evidence{}, err
	}
	for index, target := range targets {
		if existing := gateFor(evidence, target, string(level)); existing != nil {
			switch existing.Status {
			case GatePass, GateNotApplicable:
				continue
			case GateFail:
				return recordUnexecuted(store, plan, level, evidence, targets[index+1:])
			case GateBlocked:
				if existing.DurationMillis > 0 || existing.ExitCode != nil {
					return recordUnexecuted(store, plan, level, evidence, targets[index+1:])
				}
			}
		}

		status, shouldRun, applicabilityErr := applicabilityForPlan(plan, root, target, runtime.GOOS)
		if applicabilityErr != nil {
			evidence, err = store.Record(plan, GateEvidence{
				Target: target,
				Level:  string(level),
				Status: GateBlocked,
			})
			if err != nil {
				return Evidence{}, err
			}
			return recordUnexecuted(store, plan, level, evidence, targets[index+1:])
		}
		if !shouldRun {
			evidence, err = store.Record(plan, GateEvidence{
				Target: target,
				Level:  string(level),
				Status: status,
			})
			if err != nil {
				return Evidence{}, err
			}
			continue
		}

		result := runner.Run(ctx, root, plan.DiffDigest, target)
		evidence, err = store.Record(plan, gateFromRunResult(target, level, result))
		if err != nil {
			return Evidence{}, err
		}
		stored := gateFor(evidence, target, string(level))
		if stored == nil {
			return Evidence{}, fmt.Errorf("target %q result was not persisted", target)
		}
		if stored.Status == GateFail || stored.Status == GateBlocked {
			return recordUnexecuted(store, plan, level, evidence, targets[index+1:])
		}
	}
	return store.Load(plan)
}

func gateFromRunResult(target string, level VerifyLevel, result RunResult) GateEvidence {
	return GateEvidence{
		Target:           target,
		Level:            string(level),
		Status:           result.Status,
		ExitCode:         result.ExitCode,
		DurationMillis:   result.DurationMillis,
		FailureLogPath:   result.FailureLogPath,
		FirstFailingSeed: result.FirstFailingSeed,
	}
}

func recordUnexecuted(
	store EvidenceStore,
	plan Plan,
	level VerifyLevel,
	evidence Evidence,
	targets []string,
) (Evidence, error) {
	var err error
	for _, target := range targets {
		if existing := gateFor(evidence, target, string(level)); existing != nil {
			continue
		}
		evidence, err = store.Record(plan, GateEvidence{
			Target: target,
			Level:  string(level),
			Status: GateBlocked,
		})
		if err != nil {
			return Evidence{}, err
		}
	}
	return evidence, nil
}

func gateFor(evidence Evidence, target, level string) *GateEvidence {
	for index := range evidence.Gates {
		gate := &evidence.Gates[index]
		if gate.Target == target && gate.Level == level {
			return gate
		}
	}
	return nil
}

func targetsSatisfied(evidence Evidence, level string, targets []string) bool {
	for _, target := range targets {
		gate := gateFor(evidence, target, level)
		if gate == nil || gate.Status != GatePass && gate.Status != GateNotApplicable {
			return false
		}
	}
	return true
}

func focusedTargets(plan Plan) []string {
	targets := make([]string, 0, len(plan.FocusedChecks)+3)
	for _, check := range plan.FocusedChecks {
		targets = append(targets, "focused/"+check.Owner)
	}
	if has(plan.RequiredTargets, "test-contract") {
		targets = append(targets, "test-contract")
	}
	if has(plan.Risks, "fuzz") && has(plan.RequiredTargets, "test-fuzz-smoke") {
		targets = append(targets, "test-fuzz-smoke")
	}
	if len(plan.ChangeClasses) == 1 && plan.ChangeClasses[0] == "governance" {
		targets = append(targets, "docs-check-ci")
	}
	if documentationOnly(plan) {
		targets = []string{"docs-check-ci", "git-diff-check"}
	}
	return unique(targets)
}

func mergeTargets(plan Plan) []string {
	targets := []string{"lint", "test", "build", "docs-check-ci", "docs-check", "git-diff-check"}
	if documentationOnly(plan) {
		targets = []string{"docs-check-ci", "docs-check", "git-diff-check"}
	}
	var riskTargets []string
	for _, target := range plan.RequiredTargets {
		if has([]string{
			"fmt", "lint", "test", "build", "docs-check-ci", "docs-check", "git-diff-check",
		}, target) {
			continue
		}
		riskTargets = append(riskTargets, target)
	}
	slices.Sort(riskTargets)
	return unique(append(targets, riskTargets...))
}

func unique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func has(values []string, want string) bool { return slices.Contains(values, want) }

func documentationOnly(plan Plan) bool {
	return len(plan.Changed) > 0 && len(plan.Modules) == 0 &&
		len(plan.ChangeClasses) == 1 && plan.ChangeClasses[0] == "documentation"
}

func applicabilityForPlan(plan Plan, root, target, goos string) (GateStatus, bool, error) {
	if slices.Contains(plan.NotApplicable, target) {
		return GateNotApplicable, false, nil
	}
	return targetApplicability(root, target, goos)
}

func targetApplicability(root, target, goos string) (GateStatus, bool, error) {
	if target == "test-pty" && goos == "windows" {
		return GateNotApplicable, false, nil
	}
	if target != "docs-check" {
		return "", true, nil
	}
	_, err := lstatTargetPath(filepath.Join(root, ".reference"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return GateNotApplicable, false, nil
	case err != nil:
		return GateBlocked, false, fmt.Errorf("inspect optional reference directory: %w", err)
	default:
		return "", true, nil
	}
}

func cleanTree(ctx context.Context, root string, plan Plan) error {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(plan.Head) == "" {
		return errors.New("merge verification requires a repository root and plan head")
	}
	source := commandGitSource{root: root}
	if err := source.ensureReady(ctx); err != nil {
		return err
	}
	branchOutput, err := source.runUnchecked(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return errors.New("merge verification requires a named topic branch")
	}
	branch := strings.TrimSpace(string(branchOutput))
	if branch == "" || branch == "master" {
		return errors.New("merge verification requires a non-master topic branch")
	}
	clean, err := source.TrackedWorktreeClean(ctx)
	if err != nil {
		return fmt.Errorf("inspect tracked worktree: %w", err)
	}
	if !clean {
		return errors.New("tracked worktree or index does not match HEAD")
	}
	head, err := source.Resolve(ctx, "HEAD")
	if err != nil {
		return err
	}
	if head != plan.Head {
		return fmt.Errorf("plan head %s does not match current HEAD %s", plan.Head, head)
	}
	return nil
}

func nonZeroDurationMillis(started time.Time) int64 {
	duration := time.Since(started).Milliseconds()
	if duration < 1 {
		return 1
	}
	return duration
}
