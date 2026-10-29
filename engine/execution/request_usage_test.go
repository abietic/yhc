package execution

import (
	"errors"
	"sync"
	"testing"
)

func requestUsageFixture(t *testing.T, cap string) *RunUsage {
	t.Helper()
	price := "0.005"
	card := fixturePriceCard()
	card.Window = "upper_bound"
	card.Routes = []RunPriceRoute{{Provider: "agenticdeepseek", Model: "search", APIModel: "search", CachedInputPerMillion: "0", UncachedInputPerMillion: "0", OutputPerMillion: "0", PerRequest: &price}}
	r, err := NewPricedRunUsage(RunUsageLimits{MaxEstimatedCost: cap, ReserveCostBeforeDispatch: true, MaxOutputTokensPerCall: 100}, card)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRequestUsageConcurrentAdmissionSharesFeeCap(t *testing.T) {
	r := requestUsageFixture(t, "0.005")
	d := ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: "search", Request: true}
	var wg sync.WaitGroup
	admitted := make(chan ProviderUsageCall, 20)
	for range 20 {
		wg.Go(func() {
			call, err := r.AdmitProviderUsage(t.Context(), d)
			if err == nil {
				admitted <- call
			} else if !errors.Is(err, ErrRunBudgetExceeded) {
				t.Errorf("unexpected denial: %v", err)
			}
		})
	}
	wg.Wait()
	close(admitted)
	if len(admitted) != 1 {
		t.Fatalf("fee reservation admitted %d requests", len(admitted))
	}
	for call := range admitted {
		if err := CompleteRequestUsage(call, true); err != nil {
			t.Fatal(err)
		}
	}
	if s := r.Snapshot(); s.Cost.KnownEstimatedAmount != "0.005000000000" || s.DeniedCalls != 19 {
		t.Fatal("concurrent fee cap not conserved")
	}
}

func TestRequestUsageReservesAndSettlesNativeFee(t *testing.T) {
	r := requestUsageFixture(t, "0.005")
	d := ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: "search", QuerySource: "web_search", Request: true}
	c, err := r.AdmitProviderUsage(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AdmitProviderUsage(t.Context(), d); !errors.Is(err, ErrRunBudgetExceeded) {
		t.Fatalf("second dispatch must be denied: %v", err)
	}
	if err := CompleteRequestUsage(c, true); err != nil {
		t.Fatal(err)
	}
	s := r.Snapshot()
	if s.Cost.KnownEstimatedAmount != "0.005000000000" || s.TotalTokens != 0 || !s.Complete || !s.Cost.Complete || s.Cost.Reservation.OutstandingAmount != "0.000000000000" {
		t.Fatalf("unexpected fee receipt: %+v", s)
	}
	if n := s.CallLedger.Records[0].Requests; n == nil || *n != 1 {
		t.Fatal("missing request unit")
	}
	*s.CallLedger.Records[0].Requests = 0
	*s.Cost.PriceCard.Routes[0].PerRequest = "100"
	if *r.Snapshot().CallLedger.Records[0].Requests != 1 || *r.Snapshot().Cost.PriceCard.Routes[0].PerRequest != "0.005" {
		t.Fatal("snapshot aliased mutable request pricing")
	}
}

func TestRequestUsageEmptyAndUnknownLiability(t *testing.T) {
	d := ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: "search", Request: true}
	r := requestUsageFixture(t, "0.005")
	c, _ := r.AdmitProviderUsage(t.Context(), d)
	if err := CompleteRequestUsage(c, false); err != nil {
		t.Fatal(err)
	}
	if s := r.Snapshot(); s.Cost.KnownEstimatedAmount != "0.000000000000" || !s.Cost.Complete || s.KnownCalls != 1 {
		t.Fatal("empty search is a known zero-fee request")
	}
	c, err := r.AdmitProviderUsage(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	if err := MarkProviderUsageAmbiguous(c, errors.New("connection lost")); !IsProviderUsageTerminalError(err) {
		t.Fatal("ambiguous search must stop a budgeted run")
	}
	s := r.Snapshot()
	if s.Cost.Reservation.RetainedAmount != "0.005000000000" || s.Cost.Complete || s.UnknownCalls != 1 {
		t.Fatal("unknown fee liability was lost")
	}
	if _, err := r.AdmitProviderUsage(t.Context(), d); !errors.Is(err, ErrRunUsageUnknown) {
		t.Fatal("unknown liability must fence continuation")
	}
}

func TestRequestUsageCannotReuseTokenPriceOrSettlement(t *testing.T) {
	r := requestUsageFixture(t, "0.005")
	if r.RequestUsageAvailable(ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: "unpriced", Request: true}) {
		t.Fatal("unpriced paid tool must use its free fallback")
	}
	d := ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: "search", Request: true}
	c, _ := r.AdmitProviderUsage(t.Context(), d)
	if err := c.CompleteProviderUsage(nil); !errors.Is(err, ErrRunUsageUnknown) {
		t.Fatal("request settlement must be explicit")
	}
}
