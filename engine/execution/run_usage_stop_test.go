package execution

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestRunUsageReportsBudgetStopCause(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limits RunUsageLimits
		priced bool
		want   string
	}{
		{name: "provider calls", limits: RunUsageLimits{MaxProviderCalls: 1}, want: "provider_calls"},
		{name: "tokens", limits: RunUsageLimits{MaxTotalTokens: 10}, want: "total_tokens"},
		{name: "cost threshold", limits: RunUsageLimits{MaxEstimatedCost: "0.000025"}, priced: true, want: "estimated_cost_threshold"},
		{name: "existing call before token priority", limits: RunUsageLimits{MaxProviderCalls: 1, MaxTotalTokens: 10}, want: "provider_calls"},
		{name: "existing cost before call priority", limits: RunUsageLimits{MaxEstimatedCost: "0.000025", MaxProviderCalls: 1}, priced: true, want: "estimated_cost_threshold"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r *RunUsage
			var err error
			if tc.priced {
				r, err = NewPricedRunUsage(tc.limits, fixturePriceCard())
			} else {
				r, err = NewRunUsage(tc.limits)
			}
			if err != nil {
				t.Fatal(err)
			}
			d := ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: "flash"}
			call, err := r.AdmitProviderUsage(t.Context(), d)
			if err != nil {
				t.Fatal(err)
			}
			if err := call.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 10, CompletionTokens: 4}); err != nil {
				t.Fatal(err)
			}
			assertBudgetStopCause(t, r.Snapshot(), "") // Reaching a limit is not itself a denial.
			if _, err := r.AdmitProviderUsage(t.Context(), d); !errors.Is(err, ErrRunBudgetExceeded) {
				t.Fatalf("denial = %v", err)
			}
			snapshot := r.Snapshot()
			if snapshot.StopReason != "run_budget_exceeded" || snapshot.ProviderCalls != 1 || snapshot.DeniedCalls != 1 {
				t.Fatalf("existing budget contract changed: %+v", snapshot)
			}
			assertBudgetStopCause(t, snapshot, tc.want)
		})
	}
}

func TestRunUsageReportsReservationBeforeCostExhaustion(t *testing.T) {
	r, d := reservationFixture(t, "0.0001")
	call, err := r.AdmitProviderUsage(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if err := call.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 5, CompletionTokens: 5}); err != nil {
		t.Fatal(err)
	}
	// Known cost is 30 microcurrency, below 100; the next full 80 cannot fit.
	if _, err := r.AdmitProviderUsage(t.Context(), d); !errors.Is(err, ErrRunBudgetExceeded) {
		t.Fatal(err)
	}
	snapshot := r.Snapshot()
	if snapshot.Cost.KnownEstimatedAmount != "0.000030000000" || snapshot.Cost.Reservation.OutstandingAmount != "0.000000000000" {
		t.Fatalf("wrong monetary state: %+v", snapshot.Cost)
	}
	assertBudgetStopCause(t, snapshot, "estimated_cost_reservation")
}

func TestRunUsageClearsBudgetCauseWhenUnknownUsageTakesPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		settle func(ProviderUsageCall) error
	}{
		{name: "missing usage", settle: func(call ProviderUsageCall) error {
			return call.MarkProviderUsageAmbiguous(errors.New("response lost"))
		}},
		{name: "bound violation", settle: func(call ProviderUsageCall) error {
			return call.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 21, CompletionTokens: 1})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, d := reservationFixture(t, "0.00008")
			call, err := r.AdmitProviderUsage(t.Context(), d)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.AdmitProviderUsage(t.Context(), d); !errors.Is(err, ErrRunBudgetExceeded) {
				t.Fatal(err)
			}
			assertBudgetStopCause(t, r.Snapshot(), "estimated_cost_reservation")
			if err := tc.settle(call); !errors.Is(err, ErrRunUsageUnknown) {
				t.Fatal(err)
			}
			snapshot := r.Snapshot()
			if snapshot.StopReason != "run_usage_unknown" || snapshot.Cost.Reservation.RetainedAmount != "0.000080000000" {
				t.Fatalf("unknown liability was lost: %+v", snapshot)
			}
			assertBudgetStopCause(t, snapshot, "")
		})
	}
}

func TestRunUsageOmitsBudgetCauseWithoutDenial(t *testing.T) {
	r, err := NewRunUsage(RunUsageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	assertBudgetStopCause(t, r.Snapshot(), "")
}

// Assert the public JSON field independently of its internal representation.
func assertBudgetStopCause(t *testing.T, snapshot RunUsageSnapshot, want string) {
	t.Helper()
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	value, exists := fields["budget_stop_cause"]
	if want == "" {
		if exists {
			t.Fatalf("budget cause must be absent: %s", value)
		}
		return
	}
	var got string
	if !exists || json.Unmarshal(value, &got) != nil || got != want {
		t.Fatalf("budget_stop_cause = %s, want %q", value, want)
	}
}
