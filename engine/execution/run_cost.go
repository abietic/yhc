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
	APIModel                string `json:"api_model,omitempty"`
	CachedInputPerMillion   string `json:"cached_input_per_million"`
	UncachedInputPerMillion string `json:"uncached_input_per_million"`
	OutputPerMillion        string `json:"output_per_million"`
	// PerRequest switches this route to standalone request billing. All token
	// rates must be zero; nil retains the existing token-only schema.
	PerRequest *string `json:"per_request,omitempty"`
}

type RunCallCost struct {
	State           string              `json:"state"`
	PriceIndex      int                 `json:"price_index"` // -1 means no matching price route.
	EstimatedAmount *string             `json:"estimated_amount"`
	Reservation     *RunCallReservation `json:"reservation,omitempty"`
}

type RunCallReservation struct {
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	InputTokens    uint64 `json:"input_tokens"`
	OutputTokens   uint64 `json:"output_tokens"`
	ReservedAmount string `json:"reserved_amount"`
	RetainedAmount string `json:"retained_amount"`
	Requests       uint64 `json:"requests,omitempty"`
}

type RunCostReservationSnapshot struct {
	OutstandingAmount  string `json:"outstanding_amount"`
	RetainedAmount     string `json:"retained_amount"`
	PeakExposureAmount string `json:"peak_exposure_amount"`
	BoundViolations    uint64 `json:"bound_violations"`
}

type RunCostSnapshot struct {
	Version              int                         `json:"version"`
	SegmentID            string                      `json:"segment_id"`
	PriceCard            RunPriceCard                `json:"price_card"`
	KnownEstimatedAmount string                      `json:"known_estimated_amount"`
	ActualBilledAmount   *string                     `json:"actual_billed_amount"`
	PricedCalls          uint64                      `json:"priced_calls"`
	UnpricedCalls        uint64                      `json:"unpriced_calls"`
	Complete             bool                        `json:"complete"`
	LimitMode            string                      `json:"limit_mode"`
	Reservation          *RunCostReservationSnapshot `json:"reservation,omitempty"`
}

type runCostState struct {
	card                        RunPriceCard
	prices                      [][3]*big.Int
	requestPrices               []*big.Int
	amount                      big.Int // 10^-12 currency units, exact for rates with six decimals.
	limit                       *big.Int
	priced, unpriced            uint64
	outstanding, retained, peak big.Int
	boundViolations             uint64
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
		if seen[key] || runUsageProvider(route.Provider) == "unknown" || runUsageModelLabel(route.Model) == "unknown" || (route.APIModel != "" && runUsageModelLabel(route.APIModel) == "unknown") {
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
		var requestPrice *big.Int
		if route.PerRequest != nil {
			requestPrice, err = costDecimal(*route.PerRequest, 12)
			if err != nil || prices[0].Sign() != 0 || prices[1].Sign() != 0 || prices[2].Sign() != 0 {
				return nil, fmt.Errorf("request routes require an explicit nonnegative fee and zero token rates")
			}
			fee := *route.PerRequest
			c.card.Routes[len(c.prices)-1].PerRequest = &fee
		}
		c.requestPrices = append(c.requestPrices, requestPrice)
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
	card.Routes = cloneRunPriceRoutes(card.Routes)
	mode := "disabled"
	if r.cost.limit != nil {
		mode = "post_response_threshold"
	}
	s := &RunCostSnapshot{Version: 1, SegmentID: r.segmentID, PriceCard: card, KnownEstimatedAmount: costAmount(&r.cost.amount), PricedCalls: r.cost.priced, UnpricedCalls: r.cost.unpriced, Complete: r.cost.unpriced == 0 && r.totals.InFlight == 0 && r.untracked == 0 && r.cost.boundViolations == 0, LimitMode: mode}
	if r.limits.ReserveCostBeforeDispatch {
		s.LimitMode = "pre_dispatch_reservation"
		s.Reservation = &RunCostReservationSnapshot{OutstandingAmount: costAmount(&r.cost.outstanding), RetainedAmount: costAmount(&r.cost.retained), PeakExposureAmount: costAmount(&r.cost.peak), BoundViolations: r.cost.boundViolations}
	}
	return s
}

func (r *RunUsage) reserveCost(d ProviderUsageDescriptor, index int) (*RunCallReservation, *big.Int, error) {
	if !r.limits.ReserveCostBeforeDispatch {
		return nil, nil, nil
	}
	if d.Request {
		if index < 0 || r.cost.requestPrices[index] == nil || d.CostBound != nil {
			return nil, nil, ErrRunUsageUnknown
		}
		amount := new(big.Int).Set(r.cost.requestPrices[index])
		if err := r.reserveAmount(amount); err != nil {
			return nil, nil, err
		}
		return &RunCallReservation{Provider: d.Provider, Model: r.cost.card.Routes[index].APIModel, Requests: 1, ReservedAmount: costAmount(amount), RetainedAmount: costAmount(new(big.Int))}, amount, nil
	}
	b := d.CostBound
	if b == nil || index < 0 || b.Provider != d.Provider || b.Provider != "agenticdeepseek" || b.Model != r.cost.card.Routes[index].APIModel || runUsageModelLabel(b.Model) == "unknown" || b.InputTokens == 0 || b.OutputTokens == 0 || b.OutputTokens > uint64(r.limits.MaxOutputTokensPerCall) {
		return nil, nil, ErrRunUsageUnknown
	}
	prices := r.cost.prices[index]
	inputPrice := prices[1]
	if prices[0].Cmp(inputPrice) > 0 {
		inputPrice = prices[0]
	}
	amount := new(big.Int).Mul(new(big.Int).SetUint64(b.InputTokens), inputPrice)
	amount.Add(amount, new(big.Int).Mul(new(big.Int).SetUint64(b.OutputTokens), prices[2]))
	if err := r.reserveAmount(amount); err != nil {
		return nil, nil, err
	}
	return &RunCallReservation{Provider: b.Provider, Model: b.Model, InputTokens: b.InputTokens, OutputTokens: b.OutputTokens, ReservedAmount: costAmount(amount), RetainedAmount: costAmount(new(big.Int))}, amount, nil
}

func (r *RunUsage) reserveAmount(amount *big.Int) error {
	exposure := new(big.Int).Add(&r.cost.amount, &r.cost.outstanding)
	exposure.Add(exposure, &r.cost.retained)
	exposure.Add(exposure, amount)
	if exposure.Cmp(r.cost.limit) > 0 {
		return ErrRunBudgetExceeded
	}
	r.cost.outstanding.Add(&r.cost.outstanding, amount)
	if exposure.Cmp(&r.cost.peak) > 0 {
		r.cost.peak.Set(exposure)
	}
	return nil
}

func (c *runUsageCall) settleCost(values RunUsageTotals, known, release bool, requests uint64) error {
	r := c.owner
	if r.cost == nil {
		return nil
	}
	var reservation *RunCallReservation
	if c.reservation != nil {
		copied := *c.reservation
		reservation = &copied
		r.cost.outstanding.Sub(&r.cost.outstanding, c.reserved)
	}
	violation := known && reservation != nil && (values.PromptTokens > reservation.InputTokens || values.CompletionTokens > reservation.OutputTokens)
	if violation {
		known = false
		r.cost.boundViolations++
	}
	result := &RunCallCost{State: "unpriced", PriceIndex: c.priceIndex, Reservation: reservation}
	switch {
	case release:
		result.State = "released"
	case known && c.priceIndex >= 0:
		prices := r.cost.prices[c.priceIndex]
		amount := new(big.Int)
		for i, tokens := range []uint64{values.CachedPromptTokens, values.PromptTokens - values.CachedPromptTokens, values.CompletionTokens} {
			amount.Add(amount, new(big.Int).Mul(new(big.Int).SetUint64(tokens), prices[i]))
		}
		if fee := r.cost.requestPrices[c.priceIndex]; fee != nil {
			amount.Add(amount, new(big.Int).Mul(new(big.Int).SetUint64(requests), fee))
		}
		r.cost.amount.Add(&r.cost.amount, amount)
		r.cost.priced++
		result.State = "priced"
		value := costAmount(amount)
		result.EstimatedAmount = &value
	default:
		r.cost.unpriced++
		if reservation != nil {
			r.cost.retained.Add(&r.cost.retained, c.reserved)
			reservation.RetainedAmount = reservation.ReservedAmount
		}
	}
	if c.recordIndex >= 0 {
		r.records[c.recordIndex].Cost = result
	}
	if violation {
		return ErrRunUsageUnknown
	}
	return nil
}

func cloneRunPriceRoutes(routes []RunPriceRoute) []RunPriceRoute {
	result := append([]RunPriceRoute(nil), routes...)
	for i := range result {
		if result[i].PerRequest != nil {
			fee := *result[i].PerRequest
			result[i].PerRequest = &fee
		}
	}
	return result
}
