package execution

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

// RunUsageLimits applies to one invocation and all engines sharing its collector.
// Zero disables each limit. Tokens are a post-response stop threshold: already
// admitted calls may overshoot it. MaxEstimatedCost is likewise a post-response
// native-currency threshold with an explicit price card, never an invoice cap.
type RunUsageLimits struct {
	MaxProviderCalls          int64  `json:"max_provider_calls"`
	MaxTotalTokens            int64  `json:"max_total_tokens"`
	MaxEstimatedCost          string `json:"max_estimated_cost,omitempty"`
	ReserveCostBeforeDispatch bool   `json:"reserve_cost_before_dispatch,omitempty"`
	MaxOutputTokensPerCall    int    `json:"max_output_tokens_per_call,omitempty"`
}

var (
	ErrRunBudgetExceeded = errors.New("run usage budget exceeded")
	ErrRunUsageUnknown   = errors.New("run budget cannot continue with unknown usage or pricing")
)

// RunUsageTotals counts known provider usage, never estimates missing usage.
// Cache reads and writes are disjoint input subsets; reasoning is an output
// subset. None are added to input/output again. ReportedCalls distinguishes
// known cache-write zero from no observed cache-write detail.
type RunUsageTotals struct {
	ProviderCalls           uint64 `json:"provider_calls"`
	KnownCalls              uint64 `json:"known_calls"`
	UnknownCalls            uint64 `json:"unknown_calls"`
	InFlight                uint64 `json:"in_flight"`
	ReleasedCalls           uint64 `json:"released_calls"`
	PromptTokens            uint64 `json:"prompt_tokens"`
	CompletionTokens        uint64 `json:"completion_tokens"`
	TotalTokens             uint64 `json:"total_tokens"`
	CachedPromptTokens      uint64 `json:"cached_prompt_tokens"`
	ReasoningTokens         uint64 `json:"reasoning_tokens"`
	CacheWritePromptTokens  uint64 `json:"cache_write_prompt_tokens"`
	CacheWriteReportedCalls uint64 `json:"cache_write_reported_calls"`
	ProviderDurationMillis  uint64 `json:"provider_duration_ms"`
}

type RunUsageRoute struct {
	Model  string `json:"model"`
	Source string `json:"source"`
	Role   string `json:"role"`
	Effort string `json:"effort"`
	RunUsageTotals
}

type RunUsageSnapshot struct {
	RunUsageTotals
	Limits         RunUsageLimits   `json:"limits"`
	DeniedCalls    uint64           `json:"denied_calls"`
	UntrackedCalls uint64           `json:"untracked_calls"`
	Complete       bool             `json:"complete"`
	StopReason     string           `json:"stop_reason,omitempty"`
	ElapsedMillis  int64            `json:"elapsed_ms"`
	Routes         []RunUsageRoute  `json:"routes"`
	CallLedger     RunUsageLedger   `json:"call_ledger"`
	Cost           *RunCostSnapshot `json:"cost,omitempty"`

	// BudgetStopCause identifies the rejected limit, not a reached-but-unused limit.
	BudgetStopCause string `json:"budget_stop_cause,omitempty"`
}

// RunUsage is an invocation-local, concurrency-safe provider admission and
// accounting capability. It is intentionally separate from the durable Goal ledger.
type RunUsage struct {
	mu             sync.Mutex
	limits         RunUsageLimits
	started        time.Time
	totals         RunUsageTotals
	denied         uint64
	untracked      uint64
	stopReason     string
	budgetCause    string
	sealed         bool
	routes         map[runUsageRouteKey]*RunUsageRoute
	segmentID      string
	records        []RunUsageRecord
	droppedRecords uint64
	cost           *runCostState
}

type runUsageRouteKey struct{ model, source, role, effort string }

func NewRunUsage(limits RunUsageLimits) (*RunUsage, error) {
	return newRunUsage(limits, nil)
}

func newRunUsage(limits RunUsageLimits, card *RunPriceCard) (*RunUsage, error) {
	if limits.MaxProviderCalls < 0 || limits.MaxTotalTokens < 0 {
		return nil, fmt.Errorf("run usage limits must be non-negative (0 disables)")
	}
	r := &RunUsage{limits: limits, started: time.Now(), segmentID: uuid.NewString(), routes: make(map[runUsageRouteKey]*RunUsageRoute)}
	if card != nil {
		var err error
		r.cost, err = compileRunPriceCard(*card)
		if err != nil {
			return nil, err
		}
	}
	if limits.MaxEstimatedCost != "" {
		limit, err := costDecimal(limits.MaxEstimatedCost, 12)
		if err != nil {
			return nil, fmt.Errorf("max estimated cost must be a nonnegative decimal string with at most twelve decimal places")
		}
		if limit.Sign() > 0 {
			if r.cost == nil {
				return nil, fmt.Errorf("max estimated cost requires an explicit native currency price card")
			}
			r.cost.limit = limit
		}
	}
	if limits.MaxOutputTokensPerCall < 0 || (limits.MaxOutputTokensPerCall > 0 && !limits.ReserveCostBeforeDispatch) || (limits.ReserveCostBeforeDispatch && (r.cost == nil || r.cost.limit == nil || limits.MaxOutputTokensPerCall <= 0 || r.cost.card.Window != "upper_bound")) {
		return nil, fmt.Errorf("cost reservation requires a positive cost limit, output cap and upper_bound price card")
	}
	if limits.ReserveCostBeforeDispatch {
		for _, route := range r.cost.card.Routes {
			if route.APIModel == "" {
				return nil, fmt.Errorf("cost reservation requires explicit API models in the price card")
			}
		}
	}
	return r, nil
}

func (r *RunUsage) ProviderCostReservationPolicy() (bool, int) {
	return r.limits.ReserveCostBeforeDispatch, r.limits.MaxOutputTokensPerCall
}

func (r *RunUsage) NewLogicalRoundID() string { return uuid.NewString() }

func (r *RunUsage) RequestUsageAvailable(d ProviderUsageDescriptor) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Even observation-only accounting must not pretend a paid tool is priced.
	if r.cost == nil {
		return !r.LimitsEnabled()
	}
	i := r.cost.route(d)
	return i >= 0 && r.cost.card.Routes[i].PerRequest != nil
}

func (r *RunUsage) AdmitProviderUsage(ctx context.Context, d ProviderUsageDescriptor) (ProviderUsageCall, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return nil, &ProviderUsageTerminalError{Err: context.Canceled}
	}
	var cause error
	var budgetCause string
	priceIndex := -1
	if r.cost != nil {
		priceIndex = r.cost.route(d)
	}
	switch {
	case r.cost != nil && priceIndex >= 0 && d.Request != (r.cost.card.Routes[priceIndex].PerRequest != nil):
		cause = ErrRunUsageUnknown
	case r.cost != nil && r.cost.limit != nil && (r.cost.unpriced > 0 || r.untracked > 0 || priceIndex < 0):
		cause = ErrRunUsageUnknown
	case r.cost != nil && r.cost.limit != nil && !r.limits.ReserveCostBeforeDispatch && r.cost.amount.Cmp(r.cost.limit) >= 0:
		cause = ErrRunBudgetExceeded
		budgetCause = "estimated_cost_threshold"
	case r.limits.MaxTotalTokens > 0 && r.totals.UnknownCalls > 0:
		cause = ErrRunUsageUnknown
	case r.limits.MaxProviderCalls > 0 && r.totals.ProviderCalls >= uint64(r.limits.MaxProviderCalls):
		cause = ErrRunBudgetExceeded
		budgetCause = "provider_calls"
	case r.limits.MaxTotalTokens > 0 && r.totals.TotalTokens >= uint64(r.limits.MaxTotalTokens):
		cause = ErrRunBudgetExceeded
		budgetCause = "total_tokens"
	}
	var reservation *RunCallReservation
	var reserved *big.Int
	if cause == nil {
		reservation, reserved, cause = r.reserveCost(d, priceIndex)
		if errors.Is(cause, ErrRunBudgetExceeded) {
			budgetCause = "estimated_cost_reservation"
		}
	}
	if cause != nil {
		r.denied++
		r.stopReason = runUsageErrorCode(cause)
		r.budgetCause = budgetCause
		return nil, &ProviderUsageTerminalError{Err: cause}
	}
	key := runUsageRouteKey{d.Model, d.QuerySource, d.ModelRole, d.ReasoningEffort}
	// Keep diagnostics bounded even for embedded callers using dynamic labels.
	if _, ok := r.routes[key]; !ok && len(r.routes) >= 128 {
		key = runUsageRouteKey{source: "other"}
	}
	route := r.routes[key]
	if route == nil {
		route = &RunUsageRoute{Model: key.model, Source: key.source, Role: key.role, Effort: key.effort}
		r.routes[key] = route
	}
	r.totals.ProviderCalls++
	r.totals.InFlight++
	route.ProviderCalls++
	route.InFlight++
	call := &runUsageCall{owner: r, route: route, id: uuid.NewString(), started: time.Now(), priceIndex: priceIndex, reservation: reservation, reserved: reserved, request: d.Request}
	r.appendCallRecord(d, call)
	return call, nil
}

func (r *RunUsage) Snapshot() RunUsageSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := RunUsageSnapshot{
		RunUsageTotals: r.totals, Limits: r.limits,
		DeniedCalls: r.denied, UntrackedCalls: r.untracked, StopReason: r.stopReason,
		BudgetStopCause: r.budgetCause,
		Complete:        r.untracked == 0 && r.totals.UnknownCalls == 0 && r.totals.InFlight == 0,
		ElapsedMillis:   time.Since(r.started).Milliseconds(),
		Cost:            r.costSnapshot(),
		Routes:          make([]RunUsageRoute, 0, len(r.routes)),
	}
	for _, route := range r.routes {
		s.Routes = append(s.Routes, *route)
	}
	s.CallLedger = RunUsageLedger{Version: 1, SegmentID: r.segmentID, DroppedRecords: r.droppedRecords, Records: make([]RunUsageRecord, len(r.records))}
	for i, record := range r.records {
		s.CallLedger.Records[i] = record
		if record.Requests != nil {
			n := *record.Requests
			s.CallLedger.Records[i].Requests = &n
		}
		if record.Tokens != nil {
			copied := *record.Tokens
			if copied.CacheWritePromptTokens != nil {
				n := *copied.CacheWritePromptTokens
				copied.CacheWritePromptTokens = &n
			}
			s.CallLedger.Records[i].Tokens = &copied
		}
		if record.Cost != nil {
			copied := *record.Cost
			if copied.EstimatedAmount != nil {
				value := *copied.EstimatedAmount
				copied.EstimatedAmount = &value
			}
			if copied.Reservation != nil {
				reservation := *copied.Reservation
				copied.Reservation = &reservation
			}
			s.CallLedger.Records[i].Cost = &copied
		}
	}
	sort.Slice(s.Routes, func(i, j int) bool {
		a, b := s.Routes[i], s.Routes[j]
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		return a.Effort < b.Effort
	})
	return s
}

func runUsageErrorCode(err error) string {
	if errors.Is(err, ErrRunUsageUnknown) {
		return "run_usage_unknown"
	}
	return "run_budget_exceeded"
}

type runUsageCall struct {
	owner                 *RunUsage
	route                 *RunUsageRoute
	id                    string
	started               time.Time
	settled               bool
	settlementErr         error
	recordIndex           int
	responseModelSeen     bool
	responseModelConflict bool
	cacheWriteSeen        bool
	cacheWriteConflict    bool
	cacheWriteTokens      int
	cacheTTL              string
	cacheTTLConflict      bool
	priceIndex            int
	reservation           *RunCallReservation
	reserved              *big.Int
	request               bool
}

func (c *runUsageCall) ProviderCallID() string { return c.id }
func (c *runUsageCall) FailClosedOnAmbiguousUsage() bool {
	return c.owner.limits.MaxTotalTokens > 0 || c.owner.cost != nil && c.owner.cost.limit != nil
}
func (c *runUsageCall) CompleteProviderUsage(u *schema.TokenUsage) error { return c.settle(u, false) }
func (c *runUsageCall) MarkProviderUsageAmbiguous(error) error           { return c.settle(nil, false) }
func (c *runUsageCall) ReleaseProviderUsageBeforeDispatch() error        { return c.settle(nil, true) }

func (c *runUsageCall) CompleteRequestUsage(billable bool) error {
	return c.settleRequest(&schema.TokenUsage{}, false, &billable)
}

func (c *runUsageCall) settle(u *schema.TokenUsage, release bool) error {
	return c.settleRequest(u, release, nil)
}

func (c *runUsageCall) settleRequest(u *schema.TokenUsage, release bool, billable *bool) error {
	r := c.owner
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.settled {
		return c.settlementErr
	}
	c.settled = true
	var record *RunUsageRecord
	if c.recordIndex >= 0 {
		record = &r.records[c.recordIndex]
	}
	r.totals.InFlight--
	c.route.InFlight--
	if release {
		_ = c.settleCost(RunUsageTotals{}, false, true, 0)
		if record != nil {
			record.State = "released"
		}
		r.totals.ProviderCalls--
		c.route.ProviderCalls--
		r.totals.ReleasedCalls++
		c.route.ReleasedCalls++
		return nil
	}
	duration := uint64(time.Since(c.started).Milliseconds())
	if record != nil {
		record.ProviderDurationMillis = duration
	}
	r.totals.ProviderDurationMillis += duration
	c.route.ProviderDurationMillis += duration
	values, valid := runUsageTokens(u)
	valid = valid && (c.request == (billable != nil))
	if valid && c.cacheWriteSeen {
		valid = !c.cacheWriteConflict && c.cacheWriteTokens == u.PromptTokenDetails.CacheWriteTokens
		if valid {
			values.CacheWriteReportedCalls = 1
		}
	}
	if valid {
		valid = canAddRunTokens(r.totals, values) && canAddRunTokens(c.route.RunUsageTotals, values)
	}
	if !valid {
		_ = c.settleCost(RunUsageTotals{}, false, false, 0)
		if record != nil {
			record.State = "unknown"
		}
		r.totals.UnknownCalls++
		c.route.UnknownCalls++
		if c.FailClosedOnAmbiguousUsage() {
			c.settlementErr = ErrRunUsageUnknown
			r.stopReason = runUsageErrorCode(c.settlementErr)
			r.budgetCause = ""
		}
		return c.settlementErr
	}
	var requests uint64
	if billable != nil && *billable {
		requests = 1
	}
	if record != nil {
		record.State = "known"
		if c.request {
			n := requests
			record.Requests = &n
		}
		record.Tokens = &RunUsageCallTokens{PromptTokens: values.PromptTokens, CompletionTokens: values.CompletionTokens, TotalTokens: values.TotalTokens, CachedPromptTokens: values.CachedPromptTokens, UncachedPromptTokens: values.PromptTokens - values.CachedPromptTokens, ReasoningTokens: values.ReasoningTokens}
		if values.CacheWriteReportedCalls > 0 {
			n := values.CacheWritePromptTokens
			record.Tokens.CacheWritePromptTokens = &n
			if c.cacheWriteSeen && !c.cacheTTLConflict {
				record.CacheTTL = c.cacheTTL
			}
		}
	}
	addRunTokens(&r.totals, values)
	addRunTokens(&c.route.RunUsageTotals, values)
	c.settlementErr = c.settleCost(values, true, false, requests)
	if c.settlementErr != nil {
		r.stopReason = runUsageErrorCode(c.settlementErr)
		r.budgetCause = ""
	}
	return c.settlementErr
}

func runUsageTokens(u *schema.TokenUsage) (RunUsageTotals, bool) {
	if u == nil || u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 || u.PromptTokenDetails.CachedTokens < 0 || u.PromptTokenDetails.CacheWriteTokens < 0 || u.CompletionTokensDetails.ReasoningTokens < 0 || u.PromptTokenDetails.CachedTokens > u.PromptTokens || u.PromptTokenDetails.CacheWriteTokens > u.PromptTokens-u.PromptTokenDetails.CachedTokens || u.CompletionTokensDetails.ReasoningTokens > u.CompletionTokens {
		return RunUsageTotals{}, false
	}
	p, o := uint64(u.PromptTokens), uint64(u.CompletionTokens)
	if math.MaxUint64-p < o {
		return RunUsageTotals{}, false
	}
	values := RunUsageTotals{PromptTokens: p, CompletionTokens: o, TotalTokens: max(uint64(u.TotalTokens), p+o), CachedPromptTokens: uint64(u.PromptTokenDetails.CachedTokens), ReasoningTokens: uint64(u.CompletionTokensDetails.ReasoningTokens)}
	if u.PromptTokenDetails.CacheWriteTokens > 0 {
		values.CacheWritePromptTokens = uint64(u.PromptTokenDetails.CacheWriteTokens)
		values.CacheWriteReportedCalls = 1
	}
	return values, true
}

func canAddRunTokens(a, b RunUsageTotals) bool {
	return math.MaxUint64-a.PromptTokens >= b.PromptTokens && math.MaxUint64-a.CompletionTokens >= b.CompletionTokens && math.MaxUint64-a.TotalTokens >= b.TotalTokens && math.MaxUint64-a.CachedPromptTokens >= b.CachedPromptTokens && math.MaxUint64-a.ReasoningTokens >= b.ReasoningTokens && math.MaxUint64-a.CacheWritePromptTokens >= b.CacheWritePromptTokens && math.MaxUint64-a.CacheWriteReportedCalls >= b.CacheWriteReportedCalls
}

func addRunTokens(a *RunUsageTotals, b RunUsageTotals) {
	a.KnownCalls++
	a.PromptTokens += b.PromptTokens
	a.CompletionTokens += b.CompletionTokens
	a.TotalTokens += b.TotalTokens
	a.CachedPromptTokens += b.CachedPromptTokens
	a.ReasoningTokens += b.ReasoningTokens
	a.CacheWritePromptTokens += b.CacheWritePromptTokens
	a.CacheWriteReportedCalls += b.CacheWriteReportedCalls
}

func (r *RunUsage) LimitsEnabled() bool {
	return r.limits.MaxProviderCalls > 0 || r.limits.MaxTotalTokens > 0 || r.cost != nil && r.cost.limit != nil
}

// RecordUntrackedCall flags an opaque embedded extension whose provider calls
// cannot be observed. Such extensions must be suppressed while limits apply.
func (r *RunUsage) RecordUntrackedCall() { r.mu.Lock(); defer r.mu.Unlock(); r.untracked++ }

// Seal prevents late asynchronous work from starting new calls after the owner
// has closed. Already admitted calls can still settle; Snapshot exposes them.
func (r *RunUsage) Seal() { r.mu.Lock(); defer r.mu.Unlock(); r.sealed = true }
