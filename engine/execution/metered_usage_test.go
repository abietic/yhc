package execution

import (
	"errors"
	"testing"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/internal/providercost"
)

func meteredRoute(model string, unit BillingUnit, amount string, per uint64) RunPriceRoute {
	return RunPriceRoute{
		Provider: "agenticdeepseek", Model: model, APIModel: model,
		CachedInputPerMillion: "0", UncachedInputPerMillion: "0", OutputPerMillion: "0",
		Metered: &RunUnitPrice{Unit: unit, Amount: amount, Per: per},
	}
}

func meteredDescriptor(model string, unit BillingUnit, quantity uint64) ProviderUsageDescriptor {
	return ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: model, MeteredBound: MeteredUsage{Unit: unit, Quantity: quantity}}
}

func TestMeteredUsageSharesBudgetWithModelAndLegacyRequest(t *testing.T) {
	card := fixturePriceCard()
	card.Window = "upper_bound"
	fee := "0.005"
	card.Routes[0].APIModel = "deepseek-flash"
	card.Routes = append(card.Routes,
		meteredRoute("asr", BillingAudioSecond, "0.78", 3600),
		meteredRoute("tts", BillingCharacter, "3", 10000),
		meteredRoute("ocr", BillingPage, "1.5", 1000),
		RunPriceRoute{Provider: "agenticdeepseek", Model: "search", APIModel: "search", CachedInputPerMillion: "0", UncachedInputPerMillion: "0", OutputPerMillion: "0", PerRequest: &fee},
	)
	r, err := NewPricedRunUsage(RunUsageLimits{MaxEstimatedCost: "0.01016", ReserveCostBeforeDispatch: true, MaxOutputTokensPerCall: 10}, card)
	if err != nil {
		t.Fatal(err)
	}
	card.Routes[1].Metered.Amount = "10"
	modelCall, err := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{
		Provider: "agenticdeepseek", Model: "flash",
		CostBound: &providercost.Bound{Provider: "agenticdeepseek", Model: "deepseek-flash", InputTokens: 20, OutputTokens: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := modelCall.CompleteProviderUsage(&schema.TokenUsage{PromptTokens: 10, CompletionTokens: 5}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		descriptor ProviderUsageDescriptor
		actual     uint64
		reserved   string
		charged    string
	}{
		{meteredDescriptor("asr", BillingAudioSecond, 4), 3, "0.000866666667", "0.000650000000"},
		{meteredDescriptor("tts", BillingCharacter, 10), 10, "0.003000000000", "0.003000000000"},
		{meteredDescriptor("ocr", BillingPage, 2), 1, "0.003000000000", "0.001500000000"},
	} {
		call, err := r.AdmitProviderUsage(t.Context(), tc.descriptor)
		if err != nil {
			t.Fatal(err)
		}
		if got := r.Snapshot().Cost.Reservation.OutstandingAmount; got != tc.reserved {
			t.Fatalf("reservation for %s = %s, want %s", tc.descriptor.Model, got, tc.reserved)
		}
		usage := MeteredUsage{Unit: tc.descriptor.MeteredBound.Unit, Quantity: tc.actual}
		if err := CompleteMeteredUsage(call, usage); err != nil {
			t.Fatal(err)
		}
		records := r.Snapshot().CallLedger.Records
		record := records[len(records)-1]
		if record.Metered == nil || *record.Metered != usage || *record.Cost.EstimatedAmount != tc.charged {
			t.Fatalf("wrong metered receipt for %s: %+v", tc.descriptor.Model, record)
		}
	}
	if _, err := r.AdmitProviderUsage(t.Context(), ProviderUsageDescriptor{Provider: "agenticdeepseek", Model: "search", Request: true}); !errors.Is(err, ErrRunBudgetExceeded) {
		t.Fatalf("legacy request must share monetary cap: %v", err)
	}
	s := r.Snapshot()
	if s.Cost.KnownEstimatedAmount != "0.005190000000" || s.TotalTokens != 15 || !s.Cost.Complete {
		t.Fatalf("unexpected accounting totals: %+v", s.Cost)
	}
	s.Cost.PriceCard.Routes[1].Metered.Amount = "20"
	s.CallLedger.Records[1].Metered.Quantity = 0
	s.CallLedger.Records[1].Cost.Reservation.MeteredBound.Quantity = 0
	next := r.Snapshot()
	if next.Cost.PriceCard.Routes[1].Metered.Amount != "0.78" ||
		next.CallLedger.Records[1].Metered.Quantity != 3 || next.CallLedger.Records[1].Cost.Reservation.MeteredBound.Quantity != 4 {
		t.Fatal("snapshot aliases accounting state")
	}
}

func TestMeteredUsageCheckpointPreservesUnknownExposure(t *testing.T) {
	for _, tt := range []struct {
		name  string
		close func(ProviderUsageCall) error
		known bool
	}{
		{"zero", func(call ProviderUsageCall) error {
			return CompleteMeteredUsage(call, MeteredUsage{Unit: BillingPage})
		}, true},
		{"lost", func(call ProviderUsageCall) error {
			return MarkProviderUsageAmbiguous(call, errors.New("lost response"))
		}, false},
		{"wrong_unit", func(call ProviderUsageCall) error {
			return CompleteMeteredUsage(call, MeteredUsage{Unit: BillingCharacter, Quantity: 1})
		}, false},
		{"token_receipt", func(call ProviderUsageCall) error {
			return call.CompleteProviderUsage(&schema.TokenUsage{})
		}, false},
		{"over_bound", func(call ProviderUsageCall) error {
			return CompleteMeteredUsage(call, MeteredUsage{Unit: BillingPage, Quantity: 3})
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			card := fixturePriceCard()
			card.Window = "upper_bound"
			card.Routes = []RunPriceRoute{meteredRoute("ocr", BillingPage, "1.5", 1000)}
			r, err := NewPricedRunUsage(RunUsageLimits{MaxEstimatedCost: "1", ReserveCostBeforeDispatch: true}, card)
			if err != nil {
				t.Fatal(err)
			}
			dir := checkpointDirectory(t)
			if err := r.EnableCheckpointDir(dir); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = r.Close() })
			descriptor := meteredDescriptor("ocr", BillingPage, 2)
			call, err := r.AdmitProviderUsage(t.Context(), descriptor)
			if err != nil {
				t.Fatal(err)
			}
			if pending := readCheckpoint(t, dir, r); pending.Snapshot.Cost.Reservation.OutstandingAmount != "0.003000000000" {
				t.Fatal("metered bound missing from durable admission")
			}
			if err := tt.close(call); tt.known && err != nil || !tt.known && !errors.Is(err, ErrRunUsageUnknown) {
				t.Fatalf("unexpected settlement: %v", err)
			}
			if !tt.known {
				if _, err := r.AdmitProviderUsage(t.Context(), descriptor); !errors.Is(err, ErrRunUsageUnknown) {
					t.Fatalf("ambiguous cost did not fence the next dispatch: %v", err)
				}
			}
			receipt := readCheckpoint(t, dir, r).Snapshot.Cost
			wantRetained := "0.000000000000"
			if !tt.known {
				wantRetained = "0.003000000000"
			}
			if receipt.Complete != tt.known || receipt.Reservation.OutstandingAmount != "0.000000000000" ||
				receipt.Reservation.RetainedAmount != wantRetained {
				t.Fatalf("unexpected durable settlement: %+v", receipt.Reservation)
			}
		})
	}
}

func TestMeteredUsageRejectsIncompatibleAdmission(t *testing.T) {
	card := fixturePriceCard()
	card.Window = "upper_bound"
	card.Routes = []RunPriceRoute{meteredRoute("ocr", BillingPage, "1.5", 1000)}
	r, err := NewPricedRunUsage(RunUsageLimits{MaxEstimatedCost: "1", ReserveCostBeforeDispatch: true}, card)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []ProviderUsageDescriptor{
		meteredDescriptor("ocr", BillingCharacter, 1),
		meteredDescriptor("ocr", BillingPage, 0),
		meteredDescriptor("unpriced", BillingPage, 1),
		{Provider: "agenticdeepseek", Model: "ocr", Request: true, MeteredBound: MeteredUsage{Unit: BillingPage, Quantity: 1}},
		{Provider: "agenticdeepseek", Model: "ocr"},
	} {
		if RequestUsageAvailable(r, d) {
			t.Fatal("incompatible billing contract reported available")
		}
		if call, err := r.AdmitProviderUsage(t.Context(), d); call != nil || !errors.Is(err, ErrRunUsageUnknown) {
			t.Fatalf("incompatible admission = %v, %v", call, err)
		}
	}
}
