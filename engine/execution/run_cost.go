package execution

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// RunPriceCard is an explicit invocation-fixed estimate, never an invoice.
// Rates are native currency per million tokens, with at most six decimals.
type RunPriceCard struct {
	Version  int             `json:"version"`
	Currency string          `json:"currency"`
	Source   string          `json:"source"`
	AsOf     string          `json:"as_of"`
	Window   string          `json:"window"`
	Routes   []RunPriceRoute `json:"routes"`
}

type RunPriceRoute struct {
	Provider                string `json:"provider"`
	Model                   string `json:"model"`
	CachedInputPerMillion   string `json:"cached_input_per_million"`
	UncachedInputPerMillion string `json:"uncached_input_per_million"`
	OutputPerMillion        string `json:"output_per_million"`
}

type RunCallCost struct {
	State           string  `json:"state"`
	PriceIndex      int     `json:"price_index"` // -1 means no matching price route.
	EstimatedAmount *string `json:"estimated_amount"`
}

type RunCostSnapshot struct {
	Version              int          `json:"version"`
	SegmentID            string       `json:"segment_id"`
	PriceCard            RunPriceCard `json:"price_card"`
	KnownEstimatedAmount string       `json:"known_estimated_amount"`
	ActualBilledAmount   *string      `json:"actual_billed_amount"`
	PricedCalls          uint64       `json:"priced_calls"`
	UnpricedCalls        uint64       `json:"unpriced_calls"`
	Complete             bool         `json:"complete"`
	LimitMode            string       `json:"limit_mode"`
}

type runCostState struct {
	card             RunPriceCard
	prices           [][3]*big.Int
	amount           big.Int // 10^-12 currency units, exact for rates with six decimals.
	limit            *big.Int
	priced, unpriced uint64
}

// DecodeRunPriceCard accepts a bounded, strict public pricing document. It does
// not fetch prices, credentials, balance, or a mutable FX rate.
func DecodeRunPriceCard(reader io.Reader) (RunPriceCard, error) {
	data, err := io.ReadAll(io.LimitReader(reader, 65537))
	if err != nil || len(data) > 65536 {
		return RunPriceCard{}, fmt.Errorf("price card must be readable and at most 64 KiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var card RunPriceCard
	if err := decoder.Decode(&card); err != nil {
		return RunPriceCard{}, fmt.Errorf("invalid price card schema")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return RunPriceCard{}, fmt.Errorf("price card must contain one JSON object")
	}
	if _, err := compileRunPriceCard(card); err != nil {
		return RunPriceCard{}, err
	}
	return card, nil
}

var currencyPattern = regexp.MustCompile(`^[A-Z]{3}$`)

func compileRunPriceCard(card RunPriceCard) (*runCostState, error) {
	source, err := url.Parse(card.Source)
	if err != nil || len(card.Source) > 512 || source.Host == "" || source.User != nil || source.RawQuery != "" || source.Fragment != "" || (source.Scheme != "https" && source.Scheme != "http") {
		return nil, fmt.Errorf("price card source must be a public HTTP(S) URL without credentials or query")
	}
	if len(card.AsOf) > 64 {
		return nil, fmt.Errorf("price card as_of must be bounded RFC3339")
	}
	if _, err := time.Parse(time.RFC3339, card.AsOf); err != nil {
		return nil, fmt.Errorf("price card as_of must be RFC3339")
	}
	if card.Version != 1 || !currencyPattern.MatchString(card.Currency) || len(card.Routes) == 0 || len(card.Routes) > 128 {
		return nil, fmt.Errorf("price card requires version 1, native currency and 1..128 routes")
	}
	switch card.Window {
	case "fixed", "peak", "off_peak", "upper_bound":
	default:
		return nil, fmt.Errorf("invalid price card window")
	}
	c := &runCostState{card: card}
	c.card.Routes = append([]RunPriceRoute(nil), card.Routes...)
	seen := make(map[[2]string]bool)
	for _, route := range card.Routes {
		key := [2]string{route.Provider, route.Model}
		if seen[key] || runUsageProvider(route.Provider) == "unknown" || runUsageModelLabel(route.Model) == "unknown" {
			return nil, fmt.Errorf("price card requires unique canonical provider/model routes")
		}
		seen[key] = true
		var prices [3]*big.Int
		for i, rate := range []string{route.CachedInputPerMillion, route.UncachedInputPerMillion, route.OutputPerMillion} {
			value, err := costDecimal(rate, 6)
			if err != nil {
				return nil, fmt.Errorf("price rates must be nonnegative decimal strings with at most six decimal places")
			}
			prices[i] = value
		}
		c.prices = append(c.prices, prices)
	}
	return c, nil
}

func costDecimal(value string, scale int) (*big.Int, error) {
	parts := strings.Split(value, ".")
	if len(parts) > 2 || len(parts[0]) == 0 || len(parts[0]) > 18 {
		return nil, fmt.Errorf("invalid decimal amount")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if len(fraction) == 0 || len(fraction) > scale {
			return nil, fmt.Errorf("invalid decimal amount")
		}
	}
	for _, c := range parts[0] + fraction {
		if c < '0' || c > '9' {
			return nil, fmt.Errorf("invalid decimal amount")
		}
	}
	value = parts[0] + fraction + strings.Repeat("0", scale-len(fraction))
	result, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return nil, fmt.Errorf("invalid decimal amount")
	}
	return result, nil
}

func costAmount(value *big.Int) string {
	s := value.String()
	if len(s) <= 12 {
		s = strings.Repeat("0", 13-len(s)) + s
	}
	return s[:len(s)-12] + "." + s[len(s)-12:]
}

// NewPricedRunUsage locks a detached price card for this incremental invocation.
// MaxEstimatedCost is a post-response threshold; in-flight calls can overshoot.
func NewPricedRunUsage(limits RunUsageLimits, card RunPriceCard) (*RunUsage, error) {
	return newRunUsage(limits, &card)
}

func (c *runCostState) route(d ProviderUsageDescriptor) int {
	for i, route := range c.card.Routes {
		if route.Provider == d.Provider && route.Model == d.Model {
			return i
		}
	}
	return -1
}

func (r *RunUsage) costSnapshot() *RunCostSnapshot {
	if r.cost == nil {
		return nil
	}
	card := r.cost.card
	card.Routes = append([]RunPriceRoute(nil), card.Routes...)
	mode := "disabled"
	if r.cost.limit != nil {
		mode = "post_response_threshold"
	}
	return &RunCostSnapshot{Version: 1, SegmentID: r.segmentID, PriceCard: card, KnownEstimatedAmount: costAmount(&r.cost.amount), PricedCalls: r.cost.priced, UnpricedCalls: r.cost.unpriced, Complete: r.cost.unpriced == 0 && r.totals.InFlight == 0 && r.untracked == 0, LimitMode: mode}
}

func (c *runUsageCall) settleCost(values RunUsageTotals, known, release bool) {
	r := c.owner
	if r.cost == nil {
		return
	}
	result := &RunCallCost{State: "unpriced", PriceIndex: c.priceIndex}
	switch {
	case release:
		result.State = "released"
	case known && c.priceIndex >= 0:
		prices := r.cost.prices[c.priceIndex]
		amount := new(big.Int)
		for i, tokens := range []uint64{values.CachedPromptTokens, values.PromptTokens - values.CachedPromptTokens, values.CompletionTokens} {
			amount.Add(amount, new(big.Int).Mul(new(big.Int).SetUint64(tokens), prices[i]))
		}
		r.cost.amount.Add(&r.cost.amount, amount)
		r.cost.priced++
		result.State = "priced"
		value := costAmount(amount)
		result.EstimatedAmount = &value
	default:
		r.cost.unpriced++
	}
	if c.recordIndex >= 0 {
		r.records[c.recordIndex].Cost = result
	}
}
