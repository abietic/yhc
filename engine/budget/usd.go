package budget

import (
	"fmt"
	"sync"
)

// ModelUsage tracks token usage and cost for a single model.
type ModelUsage struct {
	Model        string
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	Calls        int
}

// USDBudget tracks API costs and signals when the budget is exceeded.
// Supports per-model cost breakdown for the /cost command.
type USDBudget struct {
	mu                 sync.Mutex
	MaxBudgetUSD       float64
	TotalCostUSD       float64
	CostPerInputToken  float64
	CostPerOutputToken float64
	// Per-model breakdown for cost reporting.
	modelUsage map[string]*ModelUsage
}

// NewUSDBudget creates a new USD budget tracker.
func NewUSDBudget(maxBudgetUSD, costPerInput, costPerOutput float64) *USDBudget {
	return &USDBudget{
		MaxBudgetUSD:       maxBudgetUSD,
		CostPerInputToken:  costPerInput,
		CostPerOutputToken: costPerOutput,
		modelUsage:         make(map[string]*ModelUsage),
	}
}

// RecordUsage records token usage and updates cost using default rates.
func (b *USDBudget) RecordUsage(inputTokens, outputTokens int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cost := float64(inputTokens)*b.CostPerInputToken + float64(outputTokens)*b.CostPerOutputToken
	b.TotalCostUSD += cost
}

// RecordModelUsage records token usage for a specific model with its own pricing.
func (b *USDBudget) RecordModelUsage(modelName string, inputTokens, outputTokens int, costPerInput, costPerOutput float64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	cost := float64(inputTokens)*costPerInput + float64(outputTokens)*costPerOutput
	b.TotalCostUSD += cost

	usage, ok := b.modelUsage[modelName]
	if !ok {
		usage = &ModelUsage{Model: modelName}
		b.modelUsage[modelName] = usage
	}
	usage.InputTokens += inputTokens
	usage.OutputTokens += outputTokens
	usage.CostUSD += cost
	usage.Calls++
}

// Exceeded returns true if the budget has been exceeded.
func (b *USDBudget) Exceeded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.MaxBudgetUSD > 0 && b.TotalCostUSD >= b.MaxBudgetUSD
}

// GetCostBreakdown returns a formatted string showing per-model cost breakdown.
func (b *USDBudget) GetCostBreakdown() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.modelUsage) == 0 {
		return fmt.Sprintf("Total cost: $%.4f (no per-model breakdown available)", b.TotalCostUSD)
	}

	result := fmt.Sprintf("Total cost: $%.4f / $%.2f budget\n\nPer-model breakdown:\n", b.TotalCostUSD, b.MaxBudgetUSD)
	for _, usage := range b.modelUsage {
		result += fmt.Sprintf("  %s: $%.4f (%d calls, %d in / %d out tokens)\n",
			usage.Model, usage.CostUSD, usage.Calls, usage.InputTokens, usage.OutputTokens)
	}
	return result
}

// GetTotalCost returns the current total cost in USD.
func (b *USDBudget) GetTotalCost() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.TotalCostUSD
}

// GetModelUsage returns usage data for all models.
func (b *USDBudget) GetModelUsage() []*ModelUsage {
	b.mu.Lock()
	defer b.mu.Unlock()
	result := make([]*ModelUsage, 0, len(b.modelUsage))
	for _, u := range b.modelUsage {
		copy := *u
		result = append(result, &copy)
	}
	return result
}
