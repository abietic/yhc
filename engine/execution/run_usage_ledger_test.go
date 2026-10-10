package execution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

func TestRunUsageLedgerSettlementOrderAndIsolation(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{MaxProviderCalls: 2})
	round := uuid.NewString()
	a, _ := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{LogicalRoundID: round, Model: "deepseek-flash", Provider: "agenticdeepseek", QuerySource: "agent", ReasoningEffort: "low", ModelAttemptIndex: 1, ModelRetryIndex: 2})
	b, _ := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{LogicalRoundID: round})
	b.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 10, CompletionTokens: 4})
	a.MarkProviderUsageAmbiguous(errors.New("private error payload"))
	a.ReleaseProviderUsageBeforeDispatch() // Never refund after dispatch settlement.
	if _, err := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{}); !errors.Is(err, ErrRunBudgetExceeded) {
		t.Fatal(err)
	}
	s := r.Snapshot()
	records := s.CallLedger.Records
	if len(records) != 2 || records[0].State != "unknown" || records[1].State != "known" || records[0].Ordinal != 1 || records[1].Ordinal != 2 || records[1].Tokens.TotalTokens != 14 || records[0].LogicalRoundID != round || records[0].RetryIndex != 2 || s.DeniedCalls != 1 {
		t.Fatalf("snapshot=%+v", s)
	}
	if records[0].Tokens != nil || records[0].ResolvedModel != "unknown" {
		t.Fatalf("unknown=%+v", records[0])
	}
	records[1].Tokens.TotalTokens = 999
	records[0].State = "mutated"
	again := r.Snapshot()
	if again.CallLedger.Records[0].State != "unknown" || again.CallLedger.Records[1].Tokens.TotalTokens != 14 {
		t.Fatal("snapshot aliases ledger")
	}
	encoded, _ := json.Marshal(again)
	if strings.Contains(string(encoded), "private error payload") {
		t.Fatal("error payload leaked")
	}
}

func TestRunUsageLedgerBoundedConcurrentAndReleased(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{})
	released, _ := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{})
	released.ReleaseProviderUsageBeforeDispatch()
	released.ReleaseProviderUsageBeforeDispatch()
	var wg sync.WaitGroup
	for i := 0; i < MaxRunUsageRecords+5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _ := r.AdmitProviderUsage(context.Background(), ProviderUsageDescriptor{})
			_ = r.Snapshot()
			c.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 1})
		}()
	}
	wg.Wait()
	s := r.Snapshot()
	if len(s.CallLedger.Records) != MaxRunUsageRecords || s.CallLedger.DroppedRecords != 6 || s.CallLedger.Records[0].State != "released" || s.CallLedger.Records[0].Tokens != nil || s.ReleasedCalls != 1 || s.ProviderCalls != MaxRunUsageRecords+5 || s.TotalTokens != MaxRunUsageRecords+5 || !s.Complete {
		t.Fatalf("totals=%+v ledger=%+v", s.RunUsageTotals, s.CallLedger)
	}
	if s.CallLedger.SegmentID == "" || s.CallLedger.SegmentID == func() string { other, _ := NewRunUsage(RunUsageLimits{}); return other.Snapshot().CallLedger.SegmentID }() {
		t.Fatal("missing invocation identity")
	}
}

func TestRunUsageLedgerResponseIdentityAndLabelBounds(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{})
	c, _ := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{LogicalRoundID: "private round body", Model: strings.Repeat("x", 129), QuerySource: "private source text", ModelRole: "private role", ReasoningEffort: "private effort"})
	ctx := withProviderUsageResponseObserver(t.Context(), c)
	ObserveProviderResponseModel(ctx, "deepseek-v4-flash")
	ObserveProviderResponseModel(ctx, "deepseek-v4-flash")
	c.CompleteProviderUsage(&schema.TokenUsage{})
	ObserveProviderResponseModel(ctx, "late-overwrite")
	record := r.Snapshot().CallLedger.Records[0]
	if record.ResolvedModel != "deepseek-v4-flash" || record.RequestedModel != "unknown" || record.LogicalRoundID != "unknown" || record.Source != "other" || record.Role != "other" || record.Effort != "unknown" {
		t.Fatalf("record=%+v", record)
	}
	conflict, _ := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{})
	ctx = withProviderUsageResponseObserver(t.Context(), conflict)
	ObserveProviderResponseModel(ctx, "model-a")
	ObserveProviderResponseModel(ctx, "model-b")
	conflict.CompleteProviderUsage(&schema.TokenUsage{})
	if r.Snapshot().CallLedger.Records[1].ResolvedModel != "unknown" {
		t.Fatal("conflicting identity became known")
	}
}

func TestRunUsageLedgerCompositeObservationAndSideQueryRetry(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{})
	strict := &p242bUsageReporter{}
	call, err := CombineProviderUsage(r, strict).AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{})
	if err != nil {
		t.Fatal(err)
	}
	ObserveProviderResponseModel(withProviderUsageResponseObserver(t.Context(), call), "actual-model")
	if err := call.CompleteProviderUsage(&schema.TokenUsage{}); err != nil {
		t.Fatal(err)
	}
	if r.Snapshot().CallLedger.Records[0].ResolvedModel != "actual-model" || call.ProviderCallID() != strict.calls[0].id {
		t.Fatal("composite lost identity observation or durable wire identity")
	}
	r, _ = NewRunUsage(RunUsageLimits{})
	m := &p242bStreamErrorModel{err: errors.New("overloaded_error: 529 after provider entry")}
	round := r.NewLogicalRoundID()
	_, err = SideQueryWithRetry(t.Context(), m, SideQueryOptions{ProviderUsage: r, UsageLogicalRoundID: round}, &SideQueryRetryConfig{MaxRetries: 2, BaseDelay: 1000000})
	s := r.Snapshot()
	if err == nil || m.calls != 3 || len(s.CallLedger.Records) != 3 {
		t.Fatalf("calls=%d snapshot=%+v err=%v", m.calls, s, err)
	}
	for i, record := range s.CallLedger.Records {
		if record.State != "unknown" || record.RetryIndex != i || record.LogicalRoundID != round {
			t.Fatalf("retry=%+v", record)
		}
	}
}

func TestRunUsageLedgerKeepsCoverageSource(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{})
	c, _ := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{QuerySource: "independent_verification_coverage", ModelRole: "summary"})
	c.CompleteProviderUsage(&schema.TokenUsage{})
	record := r.Snapshot().CallLedger.Records[0]
	if record.Source != "independent_verification_coverage" || record.Role != "summary" {
		t.Fatalf("record=%+v", record)
	}
}
