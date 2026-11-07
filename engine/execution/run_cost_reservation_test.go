package execution

import (
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/abietic/yhc/engine/internal/providercost"
	"github.com/cloudwego/eino/schema"
)

func reservationFixture(t *testing.T, limit string) (*RunUsage, ProviderUsageDescriptor) {
	t.Helper()
	card := fixturePriceCard()
	card.Window = "upper_bound"
	card.Routes[0].APIModel = "deepseek-flash"
	r, err := NewPricedRunUsage(RunUsageLimits{MaxEstimatedCost: limit, ReserveCostBeforeDispatch: true, MaxOutputTokensPerCall: 10}, card)
	if err != nil {
		t.Fatal(err)
	}
	return r, ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: "flash", CostBound: &providercost.Bound{Provider: "agenticdeepseek", Model: "deepseek-flash", InputTokens: 20, OutputTokens: 10}}
}

func TestRunCostReservationSerializesConcurrentAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, d := reservationFixture(t, "0.0001") // One bound is 80 microcurrency.
		var wg sync.WaitGroup
		var mu sync.Mutex
		admitted, denied := 0, 0
		for range 20 {
			wg.Go(func() {
				c, err := r.AdmitProviderUsage(t.Context(), d)
				if err == nil {
					if err := c.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 5, CompletionTokens: 5}); err != nil {
						t.Error(err)
					}
				} else if !errors.Is(err, ErrRunBudgetExceeded) {
					t.Error(err)
				}
				mu.Lock()
				defer mu.Unlock()
				if err == nil {
					admitted++
				} else {
					denied++
				}
			})
		}
		wg.Wait()
		if admitted != 1 || denied != 19 {
			t.Fatalf("admitted=%d denied=%d", admitted, denied)
		}
		// Known cost is 30; another full 80 cannot fit the 100 allowance.
		s := r.Snapshot()
		if s.Cost.KnownEstimatedAmount != "0.000030000000" || s.Cost.Reservation.OutstandingAmount != "0.000000000000" || s.Cost.Reservation.PeakExposureAmount != "0.000080000000" || !s.Cost.Complete {
			t.Fatalf("settled=%+v", s.Cost)
		}
	})
}

func TestRunCostReservationRefundReleaseAndAmbiguousHold(t *testing.T) {
	r, d := reservationFixture(t, "0.00016")
	a, _ := r.AdmitProviderUsage(t.Context(), d)
	b, _ := r.AdmitProviderUsage(t.Context(), d)
	if e := a.ReleaseProviderUsageBeforeDispatch(); e != nil {
		t.Fatal(e)
	}
	c, e := r.AdmitProviderUsage(t.Context(), d)
	if e != nil {
		t.Fatal(e)
	}
	_ = c.ReleaseProviderUsageBeforeDispatch()
	if e = b.MarkProviderUsageAmbiguous(errors.New("response lost")); !errors.Is(e, ErrRunUsageUnknown) {
		t.Fatal(e)
	}
	_ = b.ReleaseProviderUsageBeforeDispatch() // A later release must not erase uncertainty.
	s := r.Snapshot()
	if s.Cost.Reservation.RetainedAmount != "0.000080000000" || s.Cost.Reservation.OutstandingAmount != "0.000000000000" || s.Cost.Complete {
		t.Fatalf("held=%+v", s.Cost)
	}
	if _, e = r.AdmitProviderUsage(t.Context(), d); !errors.Is(e, ErrRunUsageUnknown) {
		t.Fatal(e)
	}
	s.Cost.Reservation.RetainedAmount = "0"
	s.CallLedger.Records[1].Cost.Reservation.ReservedAmount = "0"
	if r.Snapshot().Cost.Reservation.RetainedAmount != "0.000080000000" || r.Snapshot().CallLedger.Records[1].Cost.Reservation.ReservedAmount != "0.000080000000" {
		t.Fatal("snapshot aliases reservation")
	}
}

func TestRunCostReservationRejectsMissingBoundAndProviderViolation(t *testing.T) {
	r, d := reservationFixture(t, "1")
	d.CostBound = nil
	if _, e := r.AdmitProviderUsage(t.Context(), d); !errors.Is(e, ErrRunUsageUnknown) {
		t.Fatal(e)
	}
	r, d = reservationFixture(t, "1")
	c, e := r.AdmitProviderUsage(t.Context(), d)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 21, CompletionTokens: 1, TotalTokens: 22}); !errors.Is(e, ErrRunUsageUnknown) {
		t.Fatalf("bound violation=%v", e)
	}
	s := r.Snapshot()
	if s.Cost.Reservation.BoundViolations != 1 || s.Cost.Reservation.RetainedAmount != "0.000080000000" || s.Cost.Complete {
		t.Fatalf("violation=%+v", s.Cost)
	}
}

func TestRunCostReservationCombinedDenialRefundsBeforeDispatch(t *testing.T) {
	r, d := reservationFixture(t, "0.00008")
	other, _ := NewRunUsage(RunUsageLimits{MaxProviderCalls: 1})
	_, _ = other.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{})
	if _, err := CombineProviderUsage(r, other).AdmitProviderUsage(t.Context(), d); !errors.Is(err, ErrRunBudgetExceeded) {
		t.Fatalf("combined denial=%v", err)
	}
	if s := r.Snapshot(); s.Cost.Reservation.OutstandingAmount != "0.000000000000" || s.Cost.KnownEstimatedAmount != "0.000000000000" || !s.Cost.Complete {
		t.Fatalf("denial leaked reservation: %+v", s.Cost)
	}
	if _, err := r.AdmitProviderUsage(t.Context(), d); err != nil {
		t.Fatalf("refunded allowance unusable: %v", err)
	}
}

func TestRunCostReservationUsesHigherInputRateAndRequiresWireModel(t *testing.T) {
	card := fixturePriceCard()
	card.Window = "upper_bound"
	card.Routes[0].APIModel = "deepseek-flash"
	card.Routes[0].CachedInputPerMillion = "3"
	limits := RunUsageLimits{MaxEstimatedCost: "0.0001", ReserveCostBeforeDispatch: true, MaxOutputTokensPerCall: 10}
	r, err := NewPricedRunUsage(limits, card)
	if err != nil {
		t.Fatal(err)
	}
	_, d := reservationFixture(t, "1")
	c, err := r.AdmitProviderUsage(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if s := r.Snapshot(); s.Cost.Reservation.OutstandingAmount != "0.000100000000" {
		t.Fatalf("input rate was not conservative: %+v", s.Cost)
	}
	_ = c.ReleaseProviderUsageBeforeDispatch()
	d.CostBound.Model = "deepseek-v4-pro"
	if _, err = r.AdmitProviderUsage(t.Context(), d); !errors.Is(err, ErrRunUsageUnknown) {
		t.Fatalf("wire identity mismatch=%v", err)
	}
	for _, mutate := range []func(*RunUsageLimits, *RunPriceCard){
		func(l *RunUsageLimits, c *RunPriceCard) { l.ReserveCostBeforeDispatch = false },
		func(l *RunUsageLimits, c *RunPriceCard) { l.MaxEstimatedCost = "0" },
		func(l *RunUsageLimits, c *RunPriceCard) { l.MaxOutputTokensPerCall = 0 },
		func(l *RunUsageLimits, c *RunPriceCard) { c.Window = "fixed" },
		func(l *RunUsageLimits, c *RunPriceCard) { c.Routes[0].APIModel = "" },
	} {
		lc, cc := limits, fixturePriceCard()
		cc.Window, cc.Routes[0].APIModel = "upper_bound", "deepseek-flash"
		mutate(&lc, &cc)
		if _, err := NewPricedRunUsage(lc, cc); err == nil {
			t.Fatal("invalid reservation configuration accepted")
		}
	}
}
