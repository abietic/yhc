package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestExplicitTestBudgetIsBoundedAndDoesNotChangeOtherTargets(t *testing.T) {
	runner := testRunner(&fakeProcess{})
	if _, timeout, ok := runner.command("test"); !ok || timeout != 15*time.Minute {
		t.Fatal("default test budget changed")
	}
	for _, test := range []struct {
		timeout time.Duration
		reason  string
	}{
		{0, "host_contention"},
		{time.Second, "cold_cache"},
		{61 * time.Minute, "cold_cache"},
		{45 * time.Minute, ""},
		{45 * time.Minute, "untrusted private text"},
		{-time.Minute, "host_contention"},
	} {
		if err := runner.UseTestBudget(test.timeout, test.reason); err == nil {
			t.Fatalf("unsafe budget accepted: %s", test.timeout)
		}
	}
	if err := runner.UseTestBudget(45*time.Minute, "host_contention"); err != nil {
		t.Fatal(err)
	}
	runner.withTimeout = func(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
		if timeout != 45*time.Minute {
			t.Fatalf("command budget = %s", timeout)
		}
		return ctx, func() {}
	}
	result := runner.Run(t.Context(), t.TempDir(), strings.Repeat("a", 64), "test")
	gate := gateFromRunResult("test", VerifyMerge, result)
	if result.Status != GatePass || gate.TimeoutMillis != 2700000 || gate.BudgetReason != "host_contention" {
		t.Fatalf("recorded budget = %#v", gate)
	}
	if err := validateGateBudget(gate); err != nil {
		t.Fatal(err)
	}
	if _, timeout, _ := runner.command("lint"); timeout != 10*time.Minute {
		t.Fatal("test override changed lint")
	}
	if err := runner.UseTestBudget(0, ""); err != nil {
		t.Fatal(err)
	}
	if _, timeout, _ := runner.command("test"); timeout != 15*time.Minute {
		t.Fatal("test override cannot be cleared")
	}
}

func TestRecordedBudgetRejectsUnexplainedAndNonTestOverrides(t *testing.T) {
	for _, gate := range []GateEvidence{
		{Target: "test", DurationMillis: 1, TimeoutMillis: 2700000},
		{Target: "lint", DurationMillis: 1, TimeoutMillis: 2700000, BudgetReason: "host_contention"},
		{Target: "test", TimeoutMillis: 2700000, BudgetReason: "host_contention"},
		{Target: "test", DurationMillis: 1, TimeoutMillis: 3600001, BudgetReason: "cold_cache"},
	} {
		if err := validateGateBudget(gate); err == nil {
			t.Fatalf("invalid budget accepted: %#v", gate)
		}
	}
}

func TestAttemptBudgetCannotRelabelReusedTestEvidence(t *testing.T) {
	plan := storePlan()
	for _, test := range []struct {
		name    string
		gate    *GateEvidence
		timeout time.Duration
		reason  string
		wantErr bool
	}{
		{name: "new", timeout: 45 * time.Minute, reason: "host_contention"},
		{name: "placeholder", gate: &GateEvidence{Status: GateBlocked}, timeout: 45 * time.Minute, reason: "host_contention"},
		{name: "matching", gate: &GateEvidence{Status: GatePass, DurationMillis: 1, TimeoutMillis: 2700000, BudgetReason: "host_contention"}, timeout: 45 * time.Minute, reason: "host_contention"},
		{name: "duration_changed", gate: &GateEvidence{Status: GatePass, DurationMillis: 1, TimeoutMillis: 1800000, BudgetReason: "host_contention"}, timeout: 45 * time.Minute, reason: "host_contention", wantErr: true},
		{name: "reason_changed", gate: &GateEvidence{Status: GatePass, DurationMillis: 1, TimeoutMillis: 2700000, BudgetReason: "cold_cache"}, timeout: 45 * time.Minute, reason: "host_contention", wantErr: true},
		{name: "legacy_unknown", gate: &GateEvidence{Status: GatePass, DurationMillis: 1}, timeout: 45 * time.Minute, reason: "host_contention", wantErr: true},
		{name: "failed", gate: &GateEvidence{Status: GateFail, DurationMillis: 900000, TimeoutMillis: 900000}, timeout: 45 * time.Minute, reason: "host_contention", wantErr: true},
		{name: "omitted_override", gate: &GateEvidence{Status: GatePass, DurationMillis: 1, TimeoutMillis: 2700000, BudgetReason: "host_contention"}},
		{name: "not_applicable", gate: &GateEvidence{Status: GateNotApplicable}, timeout: 45 * time.Minute, reason: "host_contention", wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := Evidence{Plan: plan}
			if test.gate != nil {
				gate := *test.gate
				gate.Target, gate.Level = "test", string(VerifyMerge)
				evidence.Gates = []GateEvidence{gate}
			}
			if err := validateAttemptBudget(plan, evidence, test.timeout, test.reason); (err != nil) != test.wantErr {
				t.Fatalf("attempt budget = %v, want error %t", err, test.wantErr)
			}
		})
	}
	plan.NotApplicable = []string{"test"}
	if err := validateAttemptBudget(plan, Evidence{}, 45*time.Minute, "host_contention"); err == nil {
		t.Fatal("inapplicable test accepted a budget")
	}
	plan = Plan{Changed: []ChangedPath{{Path: "docs/owner.md"}}, ChangeClasses: []string{"documentation"}}
	if err := validateAttemptBudget(plan, Evidence{}, 45*time.Minute, "host_contention"); err == nil {
		t.Fatal("documentation-only plan accepted a test budget")
	}
}
