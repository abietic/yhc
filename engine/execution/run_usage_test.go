package execution

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestRunUsageConcurrentCallLimit(t *testing.T) {
	r, err := NewRunUsage(RunUsageLimits{MaxProviderCalls: 3})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			c, err := r.AdmitProviderUsage(context.Background(), ProviderUsageDescriptor{Model: "test"})
			if err == nil {
				_ = c.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 10, CompletionTokens: 2})
			}
		})
	}
	wg.Wait()
	s := r.Snapshot()
	if s.ProviderCalls != 3 || s.KnownCalls != 3 || s.DeniedCalls != 17 || s.TotalTokens != 36 || s.InFlight != 0 {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestRunUsageDefaultObserverPreservesRetry(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{})
	c, _ := r.AdmitProviderUsage(context.Background(), ProviderUsageDescriptor{})
	cause := errors.New("transient")
	if got := MarkProviderUsageAmbiguous(c, cause); got != cause {
		t.Fatalf("observer changed error: %v", got)
	}
	c, err := r.AdmitProviderUsage(context.Background(), ProviderUsageDescriptor{})
	if err != nil {
		t.Fatal(err)
	}
	if err := CompleteProviderUsage(c, nil); err != nil {
		t.Fatal(err)
	}
	if s := r.Snapshot(); s.UnknownCalls != 2 || s.Complete {
		t.Fatalf("snapshot = %+v", s)
	}
}

func TestRunUsageTokenThresholdAndUnknown(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{MaxTotalTokens: 10})
	c, _ := r.AdmitProviderUsage(context.Background(), ProviderUsageDescriptor{})
	usage := &schema.TokenUsage{PromptTokens: 8, CompletionTokens: 5, TotalTokens: 12}
	usage.PromptTokenDetails.CachedTokens = 6
	usage.CompletionTokensDetails.ReasoningTokens = 3
	if err := c.CompleteProviderUsage(usage); err != nil {
		t.Fatal(err)
	}
	if err := c.CompleteProviderUsage(usage); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AdmitProviderUsage(context.Background(), ProviderUsageDescriptor{}); !errors.Is(err, ErrRunBudgetExceeded) {
		t.Fatalf("admit = %v", err)
	}
	s := r.Snapshot()
	if s.TotalTokens != 13 || s.CachedPromptTokens != 6 || s.ReasoningTokens != 3 || s.KnownCalls != 1 {
		t.Fatalf("snapshot = %+v", s)
	}
	r, _ = NewRunUsage(RunUsageLimits{MaxTotalTokens: 10})
	c, _ = r.AdmitProviderUsage(context.Background(), ProviderUsageDescriptor{})
	if err := CompleteProviderUsage(c, nil); !errors.Is(err, ErrRunUsageUnknown) {
		t.Fatalf("missing = %v", err)
	}
	if _, err := r.AdmitProviderUsage(context.Background(), ProviderUsageDescriptor{}); !errors.Is(err, ErrRunUsageUnknown) {
		t.Fatalf("admit unknown = %v", err)
	}
}

func TestRunUsageReleaseAndSnapshotIsolation(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{MaxProviderCalls: 1})
	c, _ := r.AdmitProviderUsage(context.Background(), ProviderUsageDescriptor{Model: "a"})
	_ = c.ReleaseProviderUsageBeforeDispatch()
	_ = c.ReleaseProviderUsageBeforeDispatch()
	c, err := r.AdmitProviderUsage(context.Background(), ProviderUsageDescriptor{Model: "b"})
	if err != nil {
		t.Fatal(err)
	}
	_ = c.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 2})
	s := r.Snapshot()
	if s.ProviderCalls != 1 || s.ReleasedCalls != 1 || !s.Complete {
		t.Fatalf("snapshot = %+v", s)
	}
	s.Routes[0].Model = "changed"
	if r.Snapshot().Routes[0].Model == "changed" {
		t.Fatal("snapshot aliases live data")
	}
}

func TestRunUsageRetryUsesRealDispatchCount(t *testing.T) {
	for _, limit := range []int64{0, 2} {
		r, _ := NewRunUsage(RunUsageLimits{MaxProviderCalls: limit})
		m := &p242bStreamErrorModel{err: errors.New("overloaded_error: 529 after provider entry")}
		_, err := CallModelWithRetry(context.Background(), RetryConfig{MaxRetries: 3, BaseDelay: 1}, func(ctx context.Context, _ int) (*CallModelResult, error) {
			return CallModel(ctx, m, nil, nil, nil, CallModelOptions{ProviderUsage: r, UsageLogicalRoundID: r.NewLogicalRoundID()})
		}, nil)
		want := 4
		if limit == 2 {
			want = 2
		}
		if m.calls != want || r.Snapshot().UnknownCalls != uint64(want) {
			t.Fatalf("limit=%d calls=%d usage=%+v err=%v", limit, m.calls, r.Snapshot(), err)
		}
		if limit > 0 && !errors.Is(err, ErrRunBudgetExceeded) {
			t.Fatalf("budget err=%v", err)
		}
	}
}

func TestRunUsageCompositeKeepsStrictOwner(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{})
	strict := &p242bUsageReporter{}
	combined := CombineProviderUsage(r, strict)
	call, err := combined.AdmitProviderUsage(context.Background(), ProviderUsageDescriptor{})
	if err != nil {
		t.Fatal(err)
	}
	if call.ProviderCallID() != strict.calls[0].id {
		t.Fatal("lost Goal identity")
	}
	if err := MarkProviderUsageAmbiguous(call, errors.New("transient")); !IsProviderUsageTerminalError(err) {
		t.Fatalf("Goal became permissive: %v", err)
	}
	if r.Snapshot().UnknownCalls != 1 || strict.calls[0].ambiguous != 1 {
		t.Fatal("missing composite settlement")
	}
}

func TestRunUsageInFlightThresholdAndInvalidUsage(t *testing.T) {
	r, _ := NewRunUsage(RunUsageLimits{MaxTotalTokens: 10})
	a, _ := r.AdmitProviderUsage(context.Background(), ProviderUsageDescriptor{})
	b, _ := r.AdmitProviderUsage(context.Background(), ProviderUsageDescriptor{})
	_ = a.CompleteProviderUsage(&schema.TokenUsage{TotalTokens: 12})
	if s := r.Snapshot(); s.InFlight != 1 || s.Complete {
		t.Fatalf("pending=%+v", s)
	}
	// Previously admitted work is settled, not cancelled or omitted after overshoot.
	_ = b.CompleteProviderUsage(&schema.TokenUsage{TotalTokens: 13})
	if s := r.Snapshot(); s.TotalTokens != 25 || !s.Complete {
		t.Fatalf("overshoot=%+v", s)
	}
	invalid := &schema.TokenUsage{PromptTokens: 1}
	invalid.PromptTokenDetails.CachedTokens = 2
	for _, u := range []*schema.TokenUsage{nil, {PromptTokens: -1}, invalid} {
		r, _ := NewRunUsage(RunUsageLimits{})
		c, _ := r.AdmitProviderUsage(context.Background(), ProviderUsageDescriptor{})
		_ = c.CompleteProviderUsage(u)
		if s := r.Snapshot(); s.TotalTokens != 0 || s.UnknownCalls != 1 || s.Complete {
			t.Fatalf("invalid=%+v", s)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, _ = NewRunUsage(RunUsageLimits{})
	if _, err := r.AdmitProviderUsage(ctx, ProviderUsageDescriptor{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if r.Snapshot().ProviderCalls != 0 {
		t.Fatal("cancelled admission consumed budget")
	}
}
