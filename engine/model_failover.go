package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/compact"
	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/provider"
)

type modelProjectionPolicy string

const (
	modelProjectionCommitOnFirstOutput modelProjectionPolicy = "commit_on_first_output"
	modelProjectionAttemptRetractable  modelProjectionPolicy = "attempt_retractable"
)

type modelFailoverCandidate struct {
	profileID     string
	call          provider.RoleCallSnapshot
	admissionCode string
}

type modelFailoverRequest struct {
	messages     []*schema.Message
	systemPrompt *schema.Message
	toolInfos    []*schema.ToolInfo
}

type modelAttemptCoordinator struct {
	enabled          bool
	logicalRequestID string
	logicalRoundID   string
	projection       modelProjectionPolicy
	candidates       []modelFailoverCandidate
	nextCandidate    int
	visited          map[string]struct{}
	maxSwitches      int
	switches         int
	attempts         int
	budget           *execution.ModelAttemptBudget
	newID            func() string
	startedAt        time.Time
}

type activeModelAttempt struct {
	candidate     modelFailoverCandidate
	id            string
	index         int
	retries       int
	outputOffered bool
	routePrepared bool
	startedAt     time.Time
}

func newModelAttemptCoordinator(
	params QueryParams,
	request modelFailoverRequest,
	logicalRoundID string,
	newID func() string,
) (*modelAttemptCoordinator, error) {
	coordinator := &modelAttemptCoordinator{
		logicalRoundID: logicalRoundID,
		projection:     modelProjectionCommitOnFirstOutput,
		visited:        make(map[string]struct{}),
		newID:          newID,
	}
	if strings.EqualFold(
		strings.TrimSpace(params.commandEntrypoint),
		"tui",
	) {
		coordinator.projection = modelProjectionAttemptRetractable
	}
	if newID == nil {
		newID = generateUUID
		coordinator.newID = newID
	}
	coordinator.logicalRequestID = strings.TrimSpace(newID())
	if coordinator.logicalRequestID == "" {
		return nil, fmt.Errorf("model failover logical request identity is empty")
	}
	resolver, ok := params.modelResolver.(runtimeModelFailover)
	if !ok || params.modelCall == nil {
		return coordinator, nil
	}
	requirements, err := modelFailoverRequirements(request)
	if err != nil {
		return nil, fmt.Errorf("freeze model failover requirements: %w", err)
	}
	requirements.RequestedEffort = params.modelCall.Reasoning
	snapshot, err := resolver.ResolveFailoverChain(
		provider.RoleResolutionInput{
			Role:          engineconfig.ModelRole(params.modelCall.Role),
			MainSelector:  params.modelCall.Selector,
			MainReasoning: params.modelCall.Reasoning,
			Requirements:  requirements,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("freeze model failover chain: %w", err)
	}
	if len(snapshot.Alternates) == 0 {
		return coordinator, nil
	}
	if snapshot.MaxProviderCalls <= 0 ||
		snapshot.MaxSwitches <= 0 ||
		snapshot.MaxElapsedMS <= 0 ||
		!containsFailureClass(snapshot.On, execution.ModelFailureOverloaded) {
		return nil, fmt.Errorf("model failover policy is not executable")
	}
	coordinator.enabled = true
	coordinator.maxSwitches = snapshot.MaxSwitches
	coordinator.budget = execution.NewModelAttemptBudget(
		snapshot.MaxProviderCalls,
		time.Duration(snapshot.MaxElapsedMS)*time.Millisecond,
	)
	coordinator.candidates = append(
		coordinator.candidates,
		modelFailoverCandidate{
			profileID: snapshot.Primary.ProfileID,
			call:      snapshot.Primary,
		},
	)
	for _, alternate := range snapshot.Alternates {
		coordinator.candidates = append(
			coordinator.candidates,
			modelFailoverCandidate{
				profileID:     alternate.ProfileID,
				call:          alternate.Call,
				admissionCode: alternate.AdmissionCode,
			},
		)
	}
	return coordinator, nil
}

func containsFailureClass(
	classes []string,
	target execution.ModelFailureClass,
) bool {
	for _, class := range classes {
		if strings.EqualFold(strings.TrimSpace(class), string(target)) {
			return true
		}
	}
	return false
}

func modelFailoverRequirements(
	request modelFailoverRequest,
) (provider.RoleRequirements, error) {
	promptTokens := compact.EstimateTokenCount(request.messages)
	if request.systemPrompt != nil {
		promptTokens = addPromptTokenEstimate(
			promptTokens,
			compact.EstimateTokenCount([]*schema.Message{request.systemPrompt}),
		)
	}
	if len(request.toolInfos) > 0 {
		encoded, err := json.Marshal(request.toolInfos)
		if err != nil {
			return provider.RoleRequirements{}, fmt.Errorf(
				"encode model tools for context admission: %w",
				err,
			)
		}
		promptTokens = addPromptTokenEstimate(
			promptTokens,
			bytesTokenEstimate(encoded),
		)
	}
	requirements := provider.RoleRequirements{
		NeedReasoningHistory: messagesContainReasoning(request.messages),
		PromptTokens:         promptTokens,
	}
	for _, message := range request.messages {
		if message == nil {
			continue
		}
		for _, part := range message.MultiContent { //nolint:staticcheck
			if part.Type == schema.ChatMessagePartTypeImageURL {
				requirements.NeedImage = true
			}
		}
		for _, part := range message.UserInputMultiContent {
			switch part.Type {
			case schema.ChatMessagePartTypeImageURL:
				requirements.NeedImage = true
			case schema.ChatMessagePartTypeFileURL:
				if part.File != nil &&
					strings.EqualFold(
						strings.TrimSpace(part.File.MIMEType),
						"application/pdf",
					) {
					requirements.NeedPDF = true
				}
			}
		}
	}
	return requirements, nil
}

func addPromptTokenEstimate(total, additional int) int {
	if additional <= 0 {
		return total
	}
	if total > math.MaxInt-additional {
		return math.MaxInt
	}
	return total + additional
}

func bytesTokenEstimate(encoded []byte) int {
	tokens := len(encoded) / 4
	if len(encoded)%4 != 0 {
		tokens++
	}
	return tokens
}

func (c *modelAttemptCoordinator) next(
	yield func(QueryEvent),
) (*activeModelAttempt, bool) {
	if c == nil || !c.enabled {
		return nil, false
	}
	for c.nextCandidate < len(c.candidates) {
		candidate := c.candidates[c.nextCandidate]
		c.nextCandidate++
		profile := modelFailoverCandidateIdentity(candidate)
		if candidate.admissionCode != "" {
			c.emitCandidateSkip(yield, candidate, candidate.admissionCode)
			continue
		}
		if profile == "" {
			c.emitCandidateSkip(yield, candidate, "missing_identity")
			continue
		}
		if _, duplicate := c.visited[profile]; duplicate {
			c.emitCandidateSkip(yield, candidate, "duplicate")
			continue
		}
		if c.attempts > 0 && c.switches >= c.maxSwitches {
			c.emitCandidateSkip(yield, candidate, "switch_budget_exhausted")
			continue
		}
		return c.startCandidate(yield, candidate, false), true
	}
	return nil, false
}

func (c *modelAttemptCoordinator) startCandidate(
	yield func(QueryEvent),
	candidate modelFailoverCandidate,
	routePrepared bool,
) *activeModelAttempt {
	profile := modelFailoverCandidateIdentity(candidate)
	c.visited[profile] = struct{}{}
	if c.attempts > 0 {
		c.switches++
	}
	attemptID := strings.TrimSpace(c.newID())
	if attemptID == "" {
		attemptID = fmt.Sprintf(
			"%s:%d",
			c.logicalRequestID,
			c.attempts,
		)
	}
	attempt := &activeModelAttempt{
		candidate:     candidate,
		id:            attemptID,
		index:         c.attempts,
		routePrepared: routePrepared,
		startedAt:     time.Now(),
	}
	c.attempts++
	c.startedAt = attempt.startedAt
	c.emit(
		yield,
		attempt,
		ModelAttemptStarted,
		"",
		"",
		ModelAttemptOutputNeverStarted,
	)
	return attempt
}

func modelFailoverCandidateIdentity(candidate modelFailoverCandidate) string {
	profile := strings.ToLower(strings.TrimSpace(candidate.profileID))
	if profile == "" {
		profile = strings.ToLower(strings.TrimSpace(candidate.call.Selector))
	}
	return profile
}

func (c *modelAttemptCoordinator) emitCandidateSkip(
	yield func(QueryEvent),
	candidate modelFailoverCandidate,
	code string,
) {
	event := c.baseEvent(nil)
	event.Profile = candidate.profileID
	event.Provider = candidate.call.Provider
	event.APIModel = candidate.call.APIModel
	event.RouteIdentityDigest = candidate.call.RouteIdentityDigest
	event.Phase = ModelAttemptCandidateSkipped
	event.AdmissionCode = code
	event.OutputDisposition = ModelAttemptOutputNeverStarted
	yield(QueryEvent{Type: EventModelAttempt, ModelAttempt: &event})
}

func (c *modelAttemptCoordinator) emit(
	yield func(QueryEvent),
	attempt *activeModelAttempt,
	phase ModelAttemptPhase,
	failure execution.ModelFailureClass,
	admissionCode string,
	disposition ModelAttemptOutputDisposition,
) {
	event := c.baseEvent(attempt)
	event.Phase = phase
	event.FailureClass = string(failure)
	event.AdmissionCode = admissionCode
	event.OutputDisposition = disposition
	if attempt != nil {
		event.LatencyMS = max(time.Since(attempt.startedAt).Milliseconds(), 0)
	}
	yield(QueryEvent{Type: EventModelAttempt, ModelAttempt: &event})
}

func (c *modelAttemptCoordinator) baseEvent(
	attempt *activeModelAttempt,
) ModelAttemptEvent {
	event := ModelAttemptEvent{
		LogicalRequestID:  c.logicalRequestID,
		LogicalRoundID:    c.logicalRoundID,
		SwitchCount:       c.switches,
		ProviderCallCount: c.budget.ProviderCalls(),
	}
	if attempt == nil {
		return event
	}
	event.AttemptID = attempt.id
	event.AttemptIndex = attempt.index
	event.Role = string(attempt.candidate.call.Role)
	event.Profile = attempt.candidate.call.ProfileID
	event.Provider = attempt.candidate.call.Provider
	event.APIModel = attempt.candidate.call.APIModel
	event.RouteIdentityDigest = attempt.candidate.call.RouteIdentityDigest
	event.RetryCount = attempt.retries
	return event
}

func (c *modelAttemptCoordinator) annotate(
	event QueryEvent,
	attempt *activeModelAttempt,
) QueryEvent {
	if c == nil || attempt == nil {
		return event
	}
	attemptEvent := c.baseEvent(attempt)
	event.ModelAttempt = &attemptEvent
	if event.Type == EventAssistant || event.Type == EventStream {
		attempt.outputOffered = true
	}
	return event
}

func (c *modelAttemptCoordinator) nextSwitchCandidate(
	ctx context.Context,
	attempt *activeModelAttempt,
	failure execution.ModelFailureClass,
	preparer runtimeModelPreparer,
	yield func(QueryEvent),
) (modelFailoverCandidate, bool) {
	if c == nil || !c.enabled ||
		attempt == nil ||
		failure != execution.ModelFailureOverloaded ||
		ctx.Err() != nil {
		return modelFailoverCandidate{}, false
	}
	if attempt.outputOffered &&
		c.projection != modelProjectionAttemptRetractable {
		return modelFailoverCandidate{}, false
	}
	for c.nextCandidate < len(c.candidates) {
		candidate := c.candidates[c.nextCandidate]
		c.nextCandidate++
		profile := modelFailoverCandidateIdentity(candidate)
		code := candidate.admissionCode
		switch {
		case code != "":
		case profile == "":
			code = "missing_identity"
		case c.switches >= c.maxSwitches:
			code = "switch_budget_exhausted"
		default:
			_, duplicate := c.visited[profile]
			if duplicate {
				code = "duplicate"
			} else if preparer != nil {
				if _, err := preparer.PrepareModel(
					ctx,
					candidate.call.Selector,
				); err != nil {
					code = "route_construction"
				}
			}
			if code == "" {
				return candidate, true
			}
		}
		c.emitCandidateSkip(yield, candidate, code)
	}
	return modelFailoverCandidate{}, false
}

func (c *modelAttemptCoordinator) terminalDisposition(
	attempt *activeModelAttempt,
) ModelAttemptOutputDisposition {
	if attempt == nil || !attempt.outputOffered {
		return ModelAttemptOutputNeverStarted
	}
	return ModelAttemptOutputCommitted
}

func (c *modelAttemptCoordinator) safeTerminalError(
	attempt *activeModelAttempt,
	failure execution.ModelFailureClass,
) error {
	if attempt != nil &&
		attempt.outputOffered &&
		c.projection != modelProjectionAttemptRetractable {
		return fmt.Errorf(
			"model attempt stopped after output commitment (class %s)",
			failure,
		)
	}
	return fmt.Errorf(
		"model failover exhausted after %d provider calls and %d switches (last class %s)",
		c.budget.ProviderCalls(),
		c.switches,
		failure,
	)
}

func (c *modelAttemptCoordinator) discard(
	yield func(QueryEvent),
	attempt *activeModelAttempt,
	failure execution.ModelFailureClass,
) {
	disposition := ModelAttemptOutputNeverStarted
	if attempt.outputOffered {
		disposition = ModelAttemptOutputDiscarded
	}
	c.emit(
		yield,
		attempt,
		ModelAttemptDiscarded,
		failure,
		"",
		disposition,
	)
	if attempt.outputOffered {
		yield(QueryEvent{
			Type:          EventTombstone,
			TombstoneUUID: attempt.id,
			ModelAttempt:  modelAttemptPointer(c.baseEvent(attempt)),
		})
	}
}

func modelAttemptPointer(event ModelAttemptEvent) *ModelAttemptEvent {
	return &event
}

func (c *modelAttemptCoordinator) commit(
	yield func(QueryEvent),
	attempt *activeModelAttempt,
) {
	c.emit(
		yield,
		attempt,
		ModelAttemptCommitted,
		"",
		"",
		ModelAttemptOutputCommitted,
	)
}
