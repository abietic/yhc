package execution

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

// RunUsageLimits applies to one invocation and all engines sharing its collector.
// Zero disables each limit. Tokens are a post-response stop threshold: already
// admitted calls may overshoot it. No currency or provider-side retry bound is implied.
type RunUsageLimits struct {
	MaxProviderCalls int64 `json:"max_provider_calls"`
	MaxTotalTokens   int64 `json:"max_total_tokens"`
}

var (
	ErrRunBudgetExceeded = errors.New("run usage budget exceeded")
	ErrRunUsageUnknown   = errors.New("run token budget cannot continue with unknown usage")
)

// RunUsageTotals counts known provider usage, never estimates missing usage.
// Cached and reasoning tokens are subsets of input/output, not extra tokens.
type RunUsageTotals struct {
	ProviderCalls          uint64 `json:"provider_calls"`
	KnownCalls             uint64 `json:"known_calls"`
	UnknownCalls           uint64 `json:"unknown_calls"`
	InFlight               uint64 `json:"in_flight"`
	ReleasedCalls          uint64 `json:"released_calls"`
	PromptTokens           uint64 `json:"prompt_tokens"`
	CompletionTokens       uint64 `json:"completion_tokens"`
	TotalTokens            uint64 `json:"total_tokens"`
	CachedPromptTokens     uint64 `json:"cached_prompt_tokens"`
	ReasoningTokens        uint64 `json:"reasoning_tokens"`
	ProviderDurationMillis uint64 `json:"provider_duration_ms"`
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
	Limits         RunUsageLimits  `json:"limits"`
	DeniedCalls    uint64          `json:"denied_calls"`
	UntrackedCalls uint64          `json:"untracked_calls"`
	Complete       bool            `json:"complete"`
	StopReason     string          `json:"stop_reason,omitempty"`
	ElapsedMillis  int64           `json:"elapsed_ms"`
	Routes         []RunUsageRoute `json:"routes"`
}

// RunUsage is an invocation-local, concurrency-safe provider admission and
// accounting capability. It is intentionally separate from the durable Goal ledger.
type RunUsage struct {
	mu         sync.Mutex
	limits     RunUsageLimits
	started    time.Time
	totals     RunUsageTotals
	denied     uint64
	untracked  uint64
	stopReason string
	sealed     bool
	routes     map[runUsageRouteKey]*RunUsageRoute
}

type runUsageRouteKey struct{ model, source, role, effort string }

func NewRunUsage(limits RunUsageLimits) (*RunUsage, error) {
	if limits.MaxProviderCalls < 0 || limits.MaxTotalTokens < 0 {
		return nil, fmt.Errorf("run usage limits must be non-negative (0 disables)")
	}
	return &RunUsage{limits: limits, started: time.Now(), routes: make(map[runUsageRouteKey]*RunUsageRoute)}, nil
}

func (r *RunUsage) NewLogicalRoundID() string { return uuid.NewString() }

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
	switch {
	case r.limits.MaxTotalTokens > 0 && r.totals.UnknownCalls > 0:
		cause = ErrRunUsageUnknown
	case r.limits.MaxProviderCalls > 0 && r.totals.ProviderCalls >= uint64(r.limits.MaxProviderCalls):
		cause = ErrRunBudgetExceeded
	case r.limits.MaxTotalTokens > 0 && r.totals.TotalTokens >= uint64(r.limits.MaxTotalTokens):
		cause = ErrRunBudgetExceeded
	}
	if cause != nil {
		r.denied++
		r.stopReason = runUsageErrorCode(cause)
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
	return &runUsageCall{owner: r, route: route, id: uuid.NewString(), started: time.Now()}, nil
}

func (r *RunUsage) Snapshot() RunUsageSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := RunUsageSnapshot{
		RunUsageTotals: r.totals, Limits: r.limits,
		DeniedCalls: r.denied, UntrackedCalls: r.untracked, StopReason: r.stopReason,
		Complete:      r.untracked == 0 && r.totals.UnknownCalls == 0 && r.totals.InFlight == 0,
		ElapsedMillis: time.Since(r.started).Milliseconds(),
		Routes:        make([]RunUsageRoute, 0, len(r.routes)),
	}
	for _, route := range r.routes {
		s.Routes = append(s.Routes, *route)
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
	owner         *RunUsage
	route         *RunUsageRoute
	id            string
	started       time.Time
	settled       bool
	settlementErr error
}

func (c *runUsageCall) ProviderCallID() string                           { return c.id }
func (c *runUsageCall) FailClosedOnAmbiguousUsage() bool                 { return c.owner.limits.MaxTotalTokens > 0 }
func (c *runUsageCall) CompleteProviderUsage(u *schema.TokenUsage) error { return c.settle(u, false) }
func (c *runUsageCall) MarkProviderUsageAmbiguous(error) error           { return c.settle(nil, false) }
func (c *runUsageCall) ReleaseProviderUsageBeforeDispatch() error        { return c.settle(nil, true) }

func (c *runUsageCall) settle(u *schema.TokenUsage, release bool) error {
	r := c.owner
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.settled {
		return c.settlementErr
	}
	c.settled = true
	r.totals.InFlight--
	c.route.InFlight--
	if release {
		r.totals.ProviderCalls--
		c.route.ProviderCalls--
		r.totals.ReleasedCalls++
		c.route.ReleasedCalls++
		return nil
	}
	duration := uint64(time.Since(c.started).Milliseconds())
	r.totals.ProviderDurationMillis += duration
	c.route.ProviderDurationMillis += duration
	values, valid := runUsageTokens(u)
	if valid {
		valid = canAddRunTokens(r.totals, values) && canAddRunTokens(c.route.RunUsageTotals, values)
	}
	if !valid {
		r.totals.UnknownCalls++
		c.route.UnknownCalls++
		if r.limits.MaxTotalTokens > 0 {
			c.settlementErr = ErrRunUsageUnknown
			r.stopReason = runUsageErrorCode(c.settlementErr)
		}
		return c.settlementErr
	}
	addRunTokens(&r.totals, values)
	addRunTokens(&c.route.RunUsageTotals, values)
	return nil
}

func runUsageTokens(u *schema.TokenUsage) (RunUsageTotals, bool) {
	if u == nil || u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 || u.PromptTokenDetails.CachedTokens < 0 || u.CompletionTokensDetails.ReasoningTokens < 0 || u.PromptTokenDetails.CachedTokens > u.PromptTokens || u.CompletionTokensDetails.ReasoningTokens > u.CompletionTokens {
		return RunUsageTotals{}, false
	}
	p, o := uint64(u.PromptTokens), uint64(u.CompletionTokens)
	if math.MaxUint64-p < o {
		return RunUsageTotals{}, false
	}
	return RunUsageTotals{PromptTokens: p, CompletionTokens: o, TotalTokens: max(uint64(u.TotalTokens), p+o), CachedPromptTokens: uint64(u.PromptTokenDetails.CachedTokens), ReasoningTokens: uint64(u.CompletionTokensDetails.ReasoningTokens)}, true
}

func canAddRunTokens(a, b RunUsageTotals) bool {
	return math.MaxUint64-a.PromptTokens >= b.PromptTokens && math.MaxUint64-a.CompletionTokens >= b.CompletionTokens && math.MaxUint64-a.TotalTokens >= b.TotalTokens && math.MaxUint64-a.CachedPromptTokens >= b.CachedPromptTokens && math.MaxUint64-a.ReasoningTokens >= b.ReasoningTokens
}

func addRunTokens(a *RunUsageTotals, b RunUsageTotals) {
	a.KnownCalls++
	a.PromptTokens += b.PromptTokens
	a.CompletionTokens += b.CompletionTokens
	a.TotalTokens += b.TotalTokens
	a.CachedPromptTokens += b.CachedPromptTokens
	a.ReasoningTokens += b.ReasoningTokens
}

func (r *RunUsage) LimitsEnabled() bool {
	return r.limits.MaxProviderCalls > 0 || r.limits.MaxTotalTokens > 0
}

// RecordUntrackedCall flags an opaque embedded extension whose provider calls
// cannot be observed. Such extensions must be suppressed while limits apply.
func (r *RunUsage) RecordUntrackedCall() { r.mu.Lock(); defer r.mu.Unlock(); r.untracked++ }

// Seal prevents late asynchronous work from starting new calls after the owner
// has closed. Already admitted calls can still settle; Snapshot exposes them.
func (r *RunUsage) Seal() { r.mu.Lock(); defer r.mu.Unlock(); r.sealed = true }
