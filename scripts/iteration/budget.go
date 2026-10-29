package main

import (
	"errors"
	"time"
)

func validateTestBudget(timeout time.Duration, reason string) error {
	if timeout == 0 && reason == "" {
		return nil
	}
	if timeout < time.Minute || timeout > time.Hour || !oneOf(reason, "host_contention", "cold_cache") {
		return errors.New("test budget requires 1m..60m and host_contention or cold_cache")
	}
	return nil
}

func validateGateBudget(gate GateEvidence) error {
	if gate.TimeoutMillis < 0 || gate.TimeoutMillis > time.Hour.Milliseconds() {
		return errors.New("invalid recorded timeout")
	}
	if gate.BudgetReason != "" {
		if gate.Target != "test" || gate.DurationMillis == 0 {
			return errors.New("only an executed full test may declare a budget override")
		}
		return validateTestBudget(time.Duration(gate.TimeoutMillis)*time.Millisecond, gate.BudgetReason)
	}
	if gate.Target == "test" && gate.TimeoutMillis != 0 && gate.TimeoutMillis != targetDeadlines["test"].Milliseconds() {
		return errors.New("non-default test budget has no declared reason")
	}
	return nil
}

// A budget override describes an execution, not permission to relabel a reused
// result. An ordinary retry without an override keeps its existing evidence.
func validateAttemptBudget(plan Plan, evidence Evidence, timeout time.Duration, reason string) error {
	if err := validateTestBudget(timeout, reason); err != nil {
		return err
	}
	if timeout == 0 {
		return nil
	}
	if !has(mergeTargets(plan), "test") || has(plan.NotApplicable, "test") {
		return errors.New("explicit test budget requires an applicable merge test gate")
	}
	gate := gateFor(evidence, "test", string(VerifyMerge))
	if gate == nil || (gate.Status == GateBlocked && gate.DurationMillis == 0 && gate.ExitCode == nil) {
		return nil
	}
	if gate.Status == GateNotApplicable || gate.TimeoutMillis != timeout.Milliseconds() || gate.BudgetReason != reason {
		return errors.New("test gate already has different recorded budget; restart a diagnosed environment failure before changing it")
	}
	return nil
}
