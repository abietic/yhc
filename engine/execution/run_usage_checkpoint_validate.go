package execution

import (
	"math"
	"math/big"
	"reflect"
)

func validateCheckpointSnapshot(p runUsageCheckpointPayload) error {
	s := p.Snapshot
	if s.Checkpoint != nil || s.ElapsedMillis < 0 || s.CallLedger.Version != 1 ||
		s.CallLedger.SegmentID != p.SegmentID || s.CallLedger.DroppedRecords != 0 || len(s.CallLedger.Records) > MaxRunUsageRecords {
		return ErrRunUsageCheckpoint
	}
	switch p.Phase {
	case "initial":
		if p.AdmissionClosed || len(s.CallLedger.Records) != 0 || s.DeniedCalls != 0 || s.UntrackedCalls != 0 {
			return ErrRunUsageCheckpoint
		}
	case "admitted", "settled", "denied", "untracked":
	case "sealed":
		if !p.AdmissionClosed {
			return ErrRunUsageCheckpoint
		}
	default:
		return ErrRunUsageCheckpoint
	}
	if s.StopReason != "" && s.StopReason != "run_usage_unknown" && s.StopReason != "run_budget_exceeded" {
		return ErrRunUsageCheckpoint
	}
	if s.BudgetStopCause != "" && (s.StopReason != "run_budget_exceeded" || s.DeniedCalls == 0 ||
		(s.BudgetStopCause != "provider_calls" && s.BudgetStopCause != "total_tokens" &&
			s.BudgetStopCause != "estimated_cost_threshold" && s.BudgetStopCause != "estimated_cost_reservation")) {
		return ErrRunUsageCheckpoint
	}
	var card *RunPriceCard
	if s.Cost != nil {
		card = &s.Cost.PriceCard
	}
	collector, err := newRunUsage(s.Limits, card)
	if err != nil {
		return ErrRunUsageCheckpoint
	}
	costs := checkpointCostTotals{}
	var totals RunUsageTotals
	ids := make(map[string]bool)
	for i, record := range s.CallLedger.Records {
		if record.Ordinal != uint64(i+1) || !validRunUsageCheckpointUUID(record.CallID) || ids[record.CallID] ||
			record.StartedOffsetMillis < 0 || record.StartedOffsetMillis > s.ElapsedMillis ||
			record.ProviderDurationMillis > uint64(s.ElapsedMillis) ||
			runUsageProvider(record.Provider) != record.Provider || runUsageModelLabel(record.RequestedModel) != record.RequestedModel ||
			runUsageModelLabel(record.ResolvedModel) != record.ResolvedModel || runUsageSource(record.Source) != record.Source ||
			runUsageRole(record.Role) != record.Role || runUsageEffort(record.Effort) != record.Effort ||
			record.AttemptIndex < 0 || record.RetryIndex < 0 {
			return ErrRunUsageCheckpoint
		}
		ids[record.CallID] = true
		for _, id := range []string{record.LogicalRoundID, record.LogicalRequestID, record.ModelAttemptID} {
			if id != "unknown" && runUsageUUID(id) != id {
				return ErrRunUsageCheckpoint
			}
		}
		row := RunUsageTotals{ProviderCalls: 1, ProviderDurationMillis: record.ProviderDurationMillis}
		switch record.State {
		case "in_flight":
			row.InFlight = 1
		case "released":
			row.ProviderCalls, row.ReleasedCalls = 0, 1
		case "unknown":
			row.UnknownCalls = 1
		case "known":
			if record.Tokens == nil {
				return ErrRunUsageCheckpoint
			}
			t := record.Tokens
			if t.CachedPromptTokens > t.PromptTokens || t.UncachedPromptTokens != t.PromptTokens-t.CachedPromptTokens ||
				t.ReasoningTokens > t.CompletionTokens || math.MaxUint64-t.PromptTokens < t.CompletionTokens ||
				t.TotalTokens < t.PromptTokens+t.CompletionTokens {
				return ErrRunUsageCheckpoint
			}
			row.KnownCalls, row.PromptTokens, row.CompletionTokens = 1, t.PromptTokens, t.CompletionTokens
			row.TotalTokens, row.CachedPromptTokens, row.ReasoningTokens = t.TotalTokens, t.CachedPromptTokens, t.ReasoningTokens
			if t.CacheWritePromptTokens != nil {
				if *t.CacheWritePromptTokens > t.UncachedPromptTokens {
					return ErrRunUsageCheckpoint
				}
				row.CacheWritePromptTokens, row.CacheWriteReportedCalls = *t.CacheWritePromptTokens, 1
			}
		default:
			return ErrRunUsageCheckpoint
		}
		if record.State != "known" && (record.Tokens != nil || record.Requests != nil) ||
			(record.State == "in_flight" || record.State == "released") && record.ProviderDurationMillis != 0 ||
			record.CacheTTL != "" && record.CacheTTL != "5m" && record.CacheTTL != "1h" {
			return ErrRunUsageCheckpoint
		}
		if record.Requests != nil && (*record.Requests > 1 || row.PromptTokens != 0 || row.CompletionTokens != 0 || row.TotalTokens != 0) {
			return ErrRunUsageCheckpoint
		}
		if !addCheckpointTotals(&totals, row) || !costs.observe(record, collector) {
			return ErrRunUsageCheckpoint
		}
	}
	if totals != s.RunUsageTotals || s.Complete != (totals.InFlight == 0 && totals.UnknownCalls == 0 && s.UntrackedCalls == 0) {
		return ErrRunUsageCheckpoint
	}
	var routes RunUsageTotals
	if len(s.Routes) > 128 {
		return ErrRunUsageCheckpoint
	}
	for _, route := range s.Routes {
		if runUsageModelLabel(route.Model) != route.Model || runUsageSource(route.Source) != route.Source ||
			runUsageRole(route.Role) != route.Role || runUsageEffort(route.Effort) != route.Effort ||
			!addCheckpointTotals(&routes, route.RunUsageTotals) {
			return ErrRunUsageCheckpoint
		}
	}
	if routes != totals || !costs.matches(s, collector) {
		return ErrRunUsageCheckpoint
	}
	return nil
}

func addCheckpointTotals(a *RunUsageTotals, b RunUsageTotals) bool {
	pairs := [][2]*uint64{
		{&a.ProviderCalls, &b.ProviderCalls},
		{&a.KnownCalls, &b.KnownCalls},
		{&a.UnknownCalls, &b.UnknownCalls},
		{&a.InFlight, &b.InFlight},
		{&a.ReleasedCalls, &b.ReleasedCalls},
		{&a.PromptTokens, &b.PromptTokens},
		{&a.CompletionTokens, &b.CompletionTokens},
		{&a.TotalTokens, &b.TotalTokens},
		{&a.CachedPromptTokens, &b.CachedPromptTokens},
		{&a.ReasoningTokens, &b.ReasoningTokens},
		{&a.CacheWritePromptTokens, &b.CacheWritePromptTokens},
		{&a.CacheWriteReportedCalls, &b.CacheWriteReportedCalls},
		{&a.ProviderDurationMillis, &b.ProviderDurationMillis},
	}
	for _, pair := range pairs {
		if math.MaxUint64-*pair[0] < *pair[1] {
			return false
		}
		*pair[0] += *pair[1]
	}
	return true
}

type checkpointCostTotals struct {
	amount, outstanding, retained big.Int
	priced, unpriced, violations  uint64
}

func (c *checkpointCostTotals) observe(record RunUsageRecord, collector *RunUsage) bool {
	if collector.cost == nil {
		return record.Cost == nil
	}
	value := record.Cost
	if value == nil {
		return false
	}
	index := collector.cost.route(ProviderUsageDescriptor{Provider: record.Provider, Model: record.RequestedModel})
	if value.PriceIndex != index {
		return false
	}
	reserved := new(big.Int)
	reservation := value.Reservation
	if collector.limits.ReserveCostBeforeDispatch {
		if reservation == nil || index < 0 || reservation.Provider != record.Provider ||
			reservation.Model != collector.cost.card.Routes[index].APIModel {
			return false
		}
		if fee := collector.cost.requestPrices[index]; fee != nil {
			if reservation.Requests != 1 || reservation.InputTokens != 0 || reservation.OutputTokens != 0 {
				return false
			}
			reserved.Set(fee)
		} else {
			if reservation.Provider != "agenticdeepseek" || reservation.Requests != 0 || reservation.InputTokens == 0 ||
				reservation.OutputTokens == 0 || reservation.OutputTokens > uint64(collector.limits.MaxOutputTokensPerCall) {
				return false
			}
			prices := collector.cost.prices[index]
			inputPrice := prices[1]
			if prices[0].Cmp(inputPrice) > 0 {
				inputPrice = prices[0]
			}
			reserved.Mul(new(big.Int).SetUint64(reservation.InputTokens), inputPrice)
			reserved.Add(reserved, new(big.Int).Mul(new(big.Int).SetUint64(reservation.OutputTokens), prices[2]))
		}
		if reservation.ReservedAmount != costAmount(reserved) {
			return false
		}
	} else if reservation != nil {
		return false
	}
	retained := false
	switch record.State {
	case "in_flight":
		if value.State != "in_flight" || value.EstimatedAmount != nil {
			return false
		}
		c.outstanding.Add(&c.outstanding, reserved)
	case "released":
		if value.State != "released" || value.EstimatedAmount != nil {
			return false
		}
	default:
		known := record.State == "known"
		violation := known && reservation != nil && (record.Tokens.PromptTokens > reservation.InputTokens || record.Tokens.CompletionTokens > reservation.OutputTokens)
		if violation {
			c.violations++
		}
		if !known || index < 0 || violation {
			if value.State != "unpriced" || value.EstimatedAmount != nil {
				return false
			}
			c.unpriced++
			c.retained.Add(&c.retained, reserved)
			retained = true
		} else {
			if value.State != "priced" || value.EstimatedAmount == nil {
				return false
			}
			amount := new(big.Int)
			t := record.Tokens
			for i, tokens := range []uint64{t.CachedPromptTokens, t.UncachedPromptTokens, t.CompletionTokens} {
				amount.Add(amount, new(big.Int).Mul(new(big.Int).SetUint64(tokens), collector.cost.prices[index][i]))
			}
			if fee := collector.cost.requestPrices[index]; fee != nil {
				if record.Requests == nil {
					return false
				}
				amount.Add(amount, new(big.Int).Mul(new(big.Int).SetUint64(*record.Requests), fee))
			} else if record.Requests != nil {
				return false
			}
			if *value.EstimatedAmount != costAmount(amount) {
				return false
			}
			c.amount.Add(&c.amount, amount)
			c.priced++
		}
	}
	if reservation != nil {
		expected := costAmount(new(big.Int))
		if retained {
			expected = reservation.ReservedAmount
		}
		if reservation.RetainedAmount != expected {
			return false
		}
	}
	return true
}

func (c *checkpointCostTotals) matches(s RunUsageSnapshot, collector *RunUsage) bool {
	if collector.cost == nil {
		return s.Cost == nil
	}
	value := s.Cost
	if value.Version != 1 || value.SegmentID != s.CallLedger.SegmentID || value.ActualBilledAmount != nil ||
		value.KnownEstimatedAmount != costAmount(&c.amount) || value.PricedCalls != c.priced || value.UnpricedCalls != c.unpriced ||
		value.Complete != (c.unpriced == 0 && s.InFlight == 0 && s.UntrackedCalls == 0 && c.violations == 0) ||
		value.LimitMode != collector.costSnapshot().LimitMode {
		return false
	}
	if !collector.limits.ReserveCostBeforeDispatch {
		return value.Reservation == nil
	}
	r := value.Reservation
	if r == nil || r.OutstandingAmount != costAmount(&c.outstanding) || r.RetainedAmount != costAmount(&c.retained) || r.BoundViolations != c.violations {
		return false
	}
	peak, err := costDecimal(r.PeakExposureAmount, 12)
	exposure := new(big.Int).Add(&c.amount, &c.outstanding)
	exposure.Add(exposure, &c.retained)
	return err == nil && peak.Cmp(exposure) >= 0 && reflect.DeepEqual(collector.cost.card, value.PriceCard)
}
