package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/session"
)

// PermissionInteractionDecision is the structured terminal decision returned
// by an interactive adapter. Policy allow/deny decisions remain separate in
// engine/permission; these values describe how one user interaction ended.
type PermissionInteractionDecision string

const (
	PermissionAllowOnce    PermissionInteractionDecision = "allow_once"
	PermissionAllowSession PermissionInteractionDecision = "allow_session"
	PermissionAllowAlways  PermissionInteractionDecision = "allow_always"
	PermissionDeny         PermissionInteractionDecision = "deny"
	PermissionCancelled    PermissionInteractionDecision = "cancelled"
	PermissionTimedOut     PermissionInteractionDecision = "timed_out"
)

// PermissionDecisionConstraint limits the choices an adapter may offer for a
// single live permission interaction. Its zero value preserves existing
// behavior.
type PermissionDecisionConstraint string

const (
	PermissionDecisionUnconstrained PermissionDecisionConstraint = ""
	PermissionAllowOnceOnly         PermissionDecisionConstraint = "allow_once_only"
)

func (c PermissionDecisionConstraint) valid() bool {
	return c == PermissionDecisionUnconstrained || c == PermissionAllowOnceOnly
}

func (c PermissionDecisionConstraint) permits(decision PermissionInteractionDecision) bool {
	return c != PermissionAllowOnceOnly || (decision != PermissionAllowSession && decision != PermissionAllowAlways)
}

const (
	PermissionInteractionKindPermission   = "permission"
	PermissionInteractionKindQuestion     = "question"
	PermissionInteractionKindPlanApproval = "plan_approval"
	PermissionInteractionKindRepeatedTool = "repeated_tool"
)

// RepeatedToolInteractionPromptMessage is the immutable semantic prompt for a
// third consecutive identical tool call. Both the event and callback paths use
// it so replay never invents a second interaction identity.
const RepeatedToolInteractionPromptMessage = "This is the third consecutive identical tool call. Run this call once, or stop and change strategy."

// PlanApprovalOutcome is the canonical semantic result of a Plan review.
// The generic permission decision remains transport compatibility only.
type PlanApprovalOutcome string

const (
	PlanApprovalApprove PlanApprovalOutcome = "approve"
	PlanApprovalRevise  PlanApprovalOutcome = "revise"
	PlanApprovalCancel  PlanApprovalOutcome = "cancel"
)

// PermissionInteractionResult is the terminal value delivered to the engine.
// UpdatedInput is used by structured question adapters such as
// AskUserQuestion.
type PermissionInteractionResult struct {
	Decision      PermissionInteractionDecision `json:"decision"`
	Message       string                        `json:"message,omitempty"`
	UpdatedInput  map[string]any                `json:"updated_input,omitempty"`
	PlanApproval  *PlanApprovalDecision         `json:"plan_approval,omitempty"`
	settledAction *PermissionActionDescriptor

	// submittedDecision and settlementSource are process-local audit
	// provenance. They distinguish the direct adapter response from a
	// post-response fail-closed rewrite or a coalesced/context settlement.
	submittedDecision         PermissionInteractionDecision
	submittedDecisionCaptured bool
	settlementSource          string
}

func clonePermissionInteractionResult(
	result PermissionInteractionResult,
) PermissionInteractionResult {
	if result.UpdatedInput != nil {
		encoded, err := json.Marshal(result.UpdatedInput)
		if err != nil {
			return PermissionInteractionResult{
				Decision: PermissionDeny,
				Message:  "permission updated input is not durable JSON",
			}
		}
		var updated map[string]any
		if err := json.Unmarshal(encoded, &updated); err != nil {
			return PermissionInteractionResult{
				Decision: PermissionDeny,
				Message:  "permission updated input is not durable JSON",
			}
		}
		result.UpdatedInput = updated
	}
	result.PlanApproval = clonePlanApprovalDecision(result.PlanApproval)
	if result.settledAction != nil {
		settled := *result.settledAction
		settled.Input, _ = detachedJSONInput(result.settledAction.Input)
		settled.WorkingRoots = append([]string(nil), result.settledAction.WorkingRoots...)
		settled.Path.Paths = append([]string(nil), result.settledAction.Path.Paths...)
		result.settledAction = &settled
	}
	return result
}

// Allowed reports whether the terminal decision authorizes this invocation.
func (r PermissionInteractionResult) Allowed() bool {
	switch r.Decision {
	case PermissionAllowOnce, PermissionAllowSession, PermissionAllowAlways:
		return true
	default:
		return false
	}
}

// PermissionPromptRequest is the presentation-safe request passed to TUI,
// plain CLI, and ACP adapters. The engine retains canonical ownership of the
// live waiter and terminal transition.
type PermissionPromptRequest struct {
	Kind               string
	Attempt            int
	Source             string
	ToolName           string
	CanonicalToolName  string
	ToolUseID          string
	Input              map[string]any
	Message            string
	SessionScope       string
	ProjectIdentity    PermissionProjectIdentity
	RootSessionID      string
	SessionID          string
	ThreadID           string
	AgentID            string
	ToolContext        *ToolUseContext
	PlanApproval       *PlanApprovalRequest
	Presentation       *PermissionPresentation
	DecisionConstraint PermissionDecisionConstraint
	action             *PermissionActionDescriptor
}

// PlanApprovalRequest is the immutable engine-owned identity presented for one
// ExitPlanMode approval. Adapters may render it, but cannot change its request
// identity, revision, plan file, or return-mode context.
type PlanApprovalRequest struct {
	RequestID         string
	PlanRevision      uint64
	PlanFileIdentity  string
	InitialPlanDigest string
	ReturnMode        permission.Mode
}

// PlanApprovalDecision is the typed terminal response to one Plan approval.
// Confirmed is required for bypass so a generic allow response can never
// become a permission-bypass grant.
type PlanApprovalDecision struct {
	RequestID          string
	PlanRevision       uint64
	Outcome            PlanApprovalOutcome
	ReviewedPlanDigest string
	TargetMode         permission.Mode
	Confirmed          bool
	Feedback           string

	// settled is process-local execution authority. Adapters and durable wire
	// data may express an Approve outcome, but only engine settlement may turn
	// that intent into an executable ExitPlanMode capability.
	settled bool

	// Approved is a deprecated, read-only compatibility input retained for one
	// release after P20.0. New adapters emit Outcome, and settlement clears this
	// field before the decision leaves the engine.
	Approved bool `json:"Approved,omitempty"`
}

// PlanApprovalTargetModes returns the normalized, de-duplicated Plan exit
// targets exposed by every interactive adapter. A prior bypass target remains
// visible so it cannot silently become a different approval path.
func PlanApprovalTargetModes(returnMode permission.Mode) []permission.Mode {
	if returnMode == "" {
		returnMode = permission.ModeDefault
	}
	targets := []permission.Mode{returnMode, permission.ModeAcceptEdits, permission.ModeBypassPermissions}
	seen := make(map[permission.Mode]struct{}, len(targets))
	unique := make([]permission.Mode, 0, len(targets))
	for _, target := range targets {
		if _, ok := seen[target]; ok {
			continue
		}
		seen[target] = struct{}{}
		unique = append(unique, target)
	}
	return unique
}

// PermissionPromptFn presents one request and returns a structured terminal
// decision. It must not persist grants or emit permission lifecycle events.
type PermissionPromptFn func(context.Context, PermissionPromptRequest) PermissionInteractionResult

// PermissionProjectIdentity identifies one process-local project permission
// runtime. Project-root discovery is intentionally not performed: the current
// configured CWD remains the project root and is only canonicalized.
type PermissionProjectIdentity struct {
	Root       string
	RulesScope string
}

func (i PermissionProjectIdentity) key() string {
	return i.Root + "\x00" + i.RulesScope
}

// ResolvePermissionProjectIdentity canonicalizes the configured project root
// and the directory that owns project/local permission settings.
func ResolvePermissionProjectIdentity(projectRoot string) PermissionProjectIdentity {
	abs, err := filepath.Abs(strings.TrimSpace(projectRoot))
	if err != nil {
		abs = filepath.Clean(projectRoot)
	}
	root := filepath.Clean(abs)
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = filepath.Clean(resolved)
	}
	return PermissionProjectIdentity{
		Root:       root,
		RulesScope: filepath.Join(root, ".claude"),
	}
}

type permissionRequestKey struct {
	engineID  string
	toolUseID string
}

type permissionPendingRequest struct {
	request     PermissionPromptRequest
	result      chan PermissionInteractionResult
	done        chan struct{}
	cancel      context.CancelFunc
	emit        func(QueryEvent)
	commit      func(PermissionInteractionResult) PermissionInteractionResult
	grantAllows func(
		permissionCoalescingGrant,
	) (PermissionActionDescriptor, bool)
}

type permissionCoalescingGrant struct {
	Decision      PermissionInteractionDecision
	RootSessionID string
	SessionKey    permission.ApprovalKey
	AlwaysRule    permission.PermissionRule
}

// PermissionCoordinator owns live request identity and exactly-once terminal
// settlement for one project runtime.
type PermissionCoordinator struct {
	identity PermissionProjectIdentity

	mu       sync.Mutex
	engines  map[string]struct{}
	pending  map[permissionRequestKey]*permissionPendingRequest
	settling map[permissionRequestKey]*permissionPendingRequest
	grantMu  sync.Mutex
}

func newPermissionCoordinator(identity PermissionProjectIdentity) *PermissionCoordinator {
	return &PermissionCoordinator{
		identity: identity,
		engines:  make(map[string]struct{}),
		pending:  make(map[permissionRequestKey]*permissionPendingRequest),
		settling: make(map[permissionRequestKey]*permissionPendingRequest),
	}
}

// ProjectIdentity returns the canonical project identity owned by the
// coordinator.
func (c *PermissionCoordinator) ProjectIdentity() PermissionProjectIdentity {
	if c == nil {
		return PermissionProjectIdentity{}
	}
	return c.identity
}

// EngineCount returns the number of live engines registered in this project
// runtime.
func (c *PermissionCoordinator) EngineCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.engines)
}

// PendingCount returns the number of unresolved canonical requests.
func (c *PermissionCoordinator) PendingCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

func (c *PermissionCoordinator) actionableRequestIDs(
	engineID string,
	sessionID string,
	candidates []string,
) []string {
	if c == nil || len(candidates) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			wanted[candidate] = struct{}{}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	actionable := make([]string, 0, len(wanted))
	for key, pending := range c.pending {
		if key.engineID != engineID || pending == nil {
			continue
		}
		requestSessionID := firstNonEmptyString(
			pending.request.SessionID,
			pending.request.RootSessionID,
		)
		if requestSessionID != sessionID {
			continue
		}
		if _, ok := wanted[key.toolUseID]; ok {
			actionable = append(actionable, key.toolUseID)
		}
	}
	sort.Strings(actionable)
	return actionable
}

func (c *PermissionCoordinator) hasLivePlanApproval(
	engineID string,
	sessionID string,
	threadID string,
	record *session.PersistedPlanState,
) bool {
	if c == nil || record == nil ||
		strings.TrimSpace(record.ApprovalRequestID) == "" {
		return false
	}
	key := permissionRequestKey{
		engineID:  engineID,
		toolUseID: strings.TrimSpace(record.ApprovalRequestID),
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	pending := c.pending[key]
	if pending == nil || pending.request.PlanApproval == nil {
		return false
	}
	request := pending.request
	approval := request.PlanApproval
	return request.SessionID == sessionID &&
		request.ThreadID == threadID &&
		approval.RequestID == record.ApprovalRequestID &&
		approval.PlanRevision == record.Revision &&
		approval.PlanFileIdentity == record.PlanFileIdentity &&
		approval.InitialPlanDigest == record.ApprovalInitialDigest &&
		string(approval.ReturnMode) == record.ReturnMode
}

func (c *PermissionCoordinator) cancelPlanApprovals(
	engineID string,
	sessionID string,
	threadID string,
) int {
	if c == nil {
		return 0
	}
	type cancellation struct {
		key      permissionRequestKey
		approval PlanApprovalRequest
	}
	c.mu.Lock()
	cancellations := make([]cancellation, 0)
	for key, pending := range c.pending {
		if key.engineID != engineID || pending == nil ||
			pending.request.PlanApproval == nil ||
			pending.request.SessionID != sessionID ||
			pending.request.ThreadID != threadID {
			continue
		}
		cancellations = append(cancellations, cancellation{
			key:      key,
			approval: *pending.request.PlanApproval,
		})
	}
	c.mu.Unlock()
	sort.Slice(cancellations, func(i, j int) bool {
		return cancellations[i].key.toolUseID <
			cancellations[j].key.toolUseID
	})
	cancelled := 0
	for _, candidate := range cancellations {
		if c.settleRequest(candidate.key, PermissionInteractionResult{
			Decision: PermissionCancelled,
			Message:  "Plan approval cancelled by session restore",
			PlanApproval: &PlanApprovalDecision{
				RequestID:    candidate.approval.RequestID,
				PlanRevision: candidate.approval.PlanRevision,
				Outcome:      PlanApprovalCancel,
				Confirmed:    false,
				TargetMode:   candidate.approval.ReturnMode,
			},
		}, "resume", false) {
			cancelled++
		}
	}
	return cancelled
}

func (c *PermissionCoordinator) registerEngine(engineID string) {
	c.mu.Lock()
	c.engines[engineID] = struct{}{}
	c.mu.Unlock()
}

func (c *PermissionCoordinator) unregisterEngine(engineID string) bool {
	c.mu.Lock()
	delete(c.engines, engineID)
	c.mu.Unlock()

	for {
		c.mu.Lock()
		keys := make([]permissionRequestKey, 0)
		settling := make([]<-chan struct{}, 0)
		for key := range c.pending {
			if key.engineID == engineID {
				keys = append(keys, key)
			}
		}
		for key, request := range c.settling {
			if key.engineID == engineID {
				settling = append(settling, request.done)
			}
		}
		if len(keys) == 0 && len(settling) == 0 {
			idle := len(c.engines) == 0 && len(c.pending) == 0 && len(c.settling) == 0
			c.mu.Unlock()
			return idle
		}
		c.mu.Unlock()

		for _, key := range keys {
			c.settle(key, PermissionInteractionResult{
				Decision: PermissionCancelled,
				Message:  "permission request cancelled by engine shutdown",
			}, "shutdown")
		}
		for _, done := range settling {
			<-done
		}
	}
}

func (c *PermissionCoordinator) request(
	ctx context.Context,
	engineID string,
	request PermissionPromptRequest,
	prompt PermissionPromptFn,
	emit func(QueryEvent),
	commit func(PermissionInteractionResult) PermissionInteractionResult,
	grantAllows func(
		permissionCoalescingGrant,
	) (PermissionActionDescriptor, bool),
) PermissionInteractionResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil || strings.TrimSpace(engineID) == "" || strings.TrimSpace(request.ToolUseID) == "" {
		return PermissionInteractionResult{Decision: PermissionDeny, Message: "invalid permission request identity"}
	}
	if request.ProjectIdentity.Root != "" && request.ProjectIdentity.key() != c.identity.key() {
		return PermissionInteractionResult{Decision: PermissionDeny, Message: "permission request project identity mismatch"}
	}
	if err := validatePermissionPromptIdentity(request); err != nil {
		return PermissionInteractionResult{Decision: PermissionDeny, Message: err.Error()}
	}
	if !request.DecisionConstraint.valid() {
		return PermissionInteractionResult{Decision: PermissionDeny, Message: "invalid permission decision constraint"}
	}
	request.PlanApproval = clonePlanApprovalRequest(request.PlanApproval)
	request.Presentation = normalizedPermissionPresentation(
		permissionInteractionKind(request),
		permissionPresentationToolName(request),
		request.Presentation,
		request.DecisionConstraint,
	)

	key := permissionRequestKey{engineID: engineID, toolUseID: request.ToolUseID}
	adapterCtx, adapterCancel := context.WithCancel(ctx)
	pending := &permissionPendingRequest{
		request:     request,
		result:      make(chan PermissionInteractionResult, 1),
		done:        make(chan struct{}),
		cancel:      adapterCancel,
		emit:        emit,
		commit:      commit,
		grantAllows: grantAllows,
	}
	if request.DecisionConstraint == PermissionAllowOnceOnly {
		pending.grantAllows = nil
	}

	c.mu.Lock()
	if _, registered := c.engines[engineID]; !registered {
		c.mu.Unlock()
		adapterCancel()
		return PermissionInteractionResult{Decision: PermissionCancelled, Message: "permission request owner is closed"}
	}
	if _, duplicate := c.pending[key]; duplicate {
		c.mu.Unlock()
		adapterCancel()
		return PermissionInteractionResult{Decision: PermissionDeny, Message: "duplicate pending permission request"}
	}
	c.pending[key] = pending
	if emit != nil {
		emit(QueryEvent{
			Type: EventPermissionRequest,
			PermissionRequest: &PermissionRequestEvent{
				ToolName: request.ToolName, CanonicalToolName: request.CanonicalToolName,
				ToolUseID: request.ToolUseID,
				Input:     cloneInputMap(request.Input), Message: request.Message,
				Source: permissionInteractionSource(request), Kind: permissionInteractionKind(request), Attempt: request.Attempt,
				PlanApproval:       clonePlanApprovalRequest(request.PlanApproval),
				Presentation:       clonePermissionPresentation(request.Presentation),
				DecisionConstraint: request.DecisionConstraint,
			},
		})
	}
	c.mu.Unlock()

	if prompt != nil {
		go func() {
			result := callPermissionPrompt(adapterCtx, prompt, request)
			c.settle(key, result, "adapter")
		}()
	}

	select {
	case result := <-pending.result:
		return result
	case <-ctx.Done():
		decision := PermissionCancelled
		message := "permission request cancelled"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			decision = PermissionTimedOut
			message = "permission request timed out"
		}
		c.settle(key, PermissionInteractionResult{Decision: decision, Message: message}, "context")
		return <-pending.result
	}
}

func callPermissionPrompt(ctx context.Context, prompt PermissionPromptFn, request PermissionPromptRequest) (result PermissionInteractionResult) {
	defer func() {
		if recovered := recover(); recovered != nil {
			result = PermissionInteractionResult{
				Decision: PermissionDeny,
				Message:  fmt.Sprintf("permission adapter panicked: %v", recovered),
			}
			result = normalizePermissionInteractionResult(result)
			return
		}
		submittedDecision := result.Decision
		result = normalizePermissionInteractionResultForConstraint(result, request.DecisionConstraint)
		result.submittedDecision = submittedDecision
		result.submittedDecisionCaptured = true
	}()
	return prompt(withCoordinatorOwnedPermissionPrompt(ctx), request)
}

func normalizePermissionInteractionResult(result PermissionInteractionResult) PermissionInteractionResult {
	result = clonePermissionInteractionResult(result)
	result.PlanApproval = clonePlanApprovalDecision(result.PlanApproval)
	switch result.Decision {
	case PermissionAllowOnce, PermissionAllowSession, PermissionAllowAlways,
		PermissionDeny, PermissionCancelled, PermissionTimedOut:
		return result
	default:
		return PermissionInteractionResult{Decision: PermissionDeny, Message: "invalid permission adapter decision"}
	}
}

func normalizePermissionInteractionResultForConstraint(result PermissionInteractionResult, constraint PermissionDecisionConstraint) PermissionInteractionResult {
	result = normalizePermissionInteractionResult(result)
	if !constraint.valid() || !constraint.permits(result.Decision) {
		return PermissionInteractionResult{Decision: PermissionDeny, Message: "permission decision is not allowed by request constraint"}
	}
	return result
}

func (c *PermissionCoordinator) settle(key permissionRequestKey, result PermissionInteractionResult, source string) bool {
	return c.settleRequest(key, result, source, true)
}

func (c *PermissionCoordinator) settleRequest(
	key permissionRequestKey,
	result PermissionInteractionResult,
	source string,
	commitGrant bool,
) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	pending, ok := c.pending[key]
	if ok {
		delete(c.pending, key)
		c.settling[key] = pending
	}
	c.mu.Unlock()
	if !ok {
		return false
	}
	pending.cancel()

	submittedDecision := result.Decision
	submittedDecisionCaptured := result.submittedDecisionCaptured
	if result.submittedDecisionCaptured {
		submittedDecision = result.submittedDecision
	}
	constraintViolation := !pending.request.DecisionConstraint.permits(submittedDecision)
	result = normalizePermissionInteractionResultForConstraint(result, pending.request.DecisionConstraint)
	if commitGrant && pending.commit != nil && !constraintViolation {
		if result.Decision == PermissionAllowAlways {
			c.grantMu.Lock()
			result = normalizePermissionInteractionResultForConstraint(pending.commit(result), pending.request.DecisionConstraint)
			c.grantMu.Unlock()
		} else {
			result = normalizePermissionInteractionResultForConstraint(pending.commit(result), pending.request.DecisionConstraint)
		}
	}
	result.submittedDecision = submittedDecision
	result.submittedDecisionCaptured = submittedDecisionCaptured
	result.settlementSource = source
	grant, scan := c.coalescingGrant(pending, result, commitGrant)
	if pending.emit != nil {
		pending.emit(QueryEvent{
			Type: EventPermissionResolved,
			PermissionResolved: &PermissionResolvedEvent{
				ToolUseID: pending.request.ToolUseID,
				Decision:  string(result.Decision),
				Reason:    source,
				Message:   result.Message,
				Kind:      permissionInteractionKind(pending.request),
				Attempt:   pending.request.Attempt,
				PlanApproval: clonePlanApprovalDecision(
					result.PlanApproval,
				),
			},
		})
	}
	pending.result <- result
	if scan {
		c.coalescePending(key, grant)
	}
	c.mu.Lock()
	delete(c.settling, key)
	close(pending.done)
	c.mu.Unlock()
	return true
}

func permissionInteractionKind(request PermissionPromptRequest) string {
	switch request.Kind {
	case "":
		if request.PlanApproval != nil {
			return PermissionInteractionKindPlanApproval
		}
		return PermissionInteractionKindPermission
	case PermissionInteractionKindPermission, PermissionInteractionKindQuestion, PermissionInteractionKindPlanApproval, PermissionInteractionKindRepeatedTool:
		return request.Kind
	default:
		return request.Kind
	}
}

func validatePermissionPromptIdentity(request PermissionPromptRequest) error {
	kind := permissionInteractionKind(request)
	if kind != PermissionInteractionKindPermission &&
		request.DecisionConstraint != PermissionDecisionUnconstrained {
		return errors.New("decision constraint is only valid for permission interactions")
	}
	switch kind {
	case PermissionInteractionKindPermission, PermissionInteractionKindQuestion:
		if request.PlanApproval != nil || request.Attempt != 0 {
			return errors.New("invalid typed permission request identity")
		}
		if kind == PermissionInteractionKindQuestion && request.Presentation != nil {
			return errors.New("question interaction cannot carry permission presentation")
		}
	case PermissionInteractionKindPlanApproval:
		if request.PlanApproval == nil || request.Attempt != 0 || request.Presentation != nil {
			return errors.New("invalid Plan approval request identity")
		}
	case PermissionInteractionKindRepeatedTool:
		if request.PlanApproval != nil || request.Attempt != 3 ||
			strings.TrimSpace(request.Source) != "repeated_tool_guard" ||
			request.Presentation != nil {
			return errors.New("invalid repeated-tool request identity")
		}
	default:
		return errors.New("unknown permission interaction kind")
	}
	return nil
}

func permissionPresentationToolName(request PermissionPromptRequest) string {
	if strings.TrimSpace(request.CanonicalToolName) != "" {
		return request.CanonicalToolName
	}
	return request.ToolName
}

func permissionInteractionSource(request PermissionPromptRequest) string {
	if source := strings.TrimSpace(request.Source); source != "" {
		return source
	}
	return "coordinator"
}

func clonePlanApprovalRequest(
	request *PlanApprovalRequest,
) *PlanApprovalRequest {
	if request == nil {
		return nil
	}
	cloned := *request
	return &cloned
}

func clonePlanApprovalDecision(
	decision *PlanApprovalDecision,
) *PlanApprovalDecision {
	if decision == nil {
		return nil
	}
	cloned := *decision
	return &cloned
}

func (c *PermissionCoordinator) coalescingGrant(
	pending *permissionPendingRequest,
	result PermissionInteractionResult,
	commitGrant bool,
) (permissionCoalescingGrant, bool) {
	if c == nil || pending == nil || !commitGrant {
		return permissionCoalescingGrant{}, false
	}
	grant := permissionCoalescingGrant{
		Decision:      result.Decision,
		RootSessionID: pending.request.RootSessionID,
	}
	toolName := pending.request.ToolName
	input := pending.request.Input
	if result.settledAction != nil {
		toolName = result.settledAction.CanonicalToolName
		input = result.settledAction.Input
	}
	switch result.Decision {
	case PermissionAllowSession:
		key, _, err := sessionApprovalKey(c.identity.Root, toolName, input)
		if err != nil {
			return permissionCoalescingGrant{}, false
		}
		grant.SessionKey = key
	case PermissionAllowAlways:
		exactRule, err := permission.BuildExactRuleFromInvocation(
			toolName,
			input,
			c.identity.Root,
		)
		if err != nil {
			return permissionCoalescingGrant{}, false
		}
		exactRule.Rule.Source = "coalesced-grant"
		grant.AlwaysRule = exactRule.Rule
	default:
		return permissionCoalescingGrant{}, false
	}
	return grant, true
}

func (c *PermissionCoordinator) coalescePending(sourceKey permissionRequestKey, grant permissionCoalescingGrant) {
	type candidate struct {
		key     permissionRequestKey
		pending *permissionPendingRequest
	}

	c.mu.Lock()
	candidates := make([]candidate, 0, len(c.pending))
	for key, pending := range c.pending {
		if key == sourceKey || pending == nil || pending.grantAllows == nil {
			continue
		}
		if grant.Decision == PermissionAllowSession && pending.request.RootSessionID != grant.RootSessionID {
			continue
		}
		candidates = append(candidates, candidate{key: key, pending: pending})
	}
	c.mu.Unlock()

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].key.engineID != candidates[j].key.engineID {
			return candidates[i].key.engineID < candidates[j].key.engineID
		}
		return candidates[i].key.toolUseID < candidates[j].key.toolUseID
	})

	for _, candidate := range candidates {
		action, allowed := candidateAllowsGrant(candidate.pending, grant)
		if !allowed {
			continue
		}
		message := "allowed by a newly committed session permission"
		if grant.Decision == PermissionAllowAlways {
			message = "allowed by a newly committed project permission"
		}
		c.settleRequest(candidate.key, PermissionInteractionResult{
			Decision:      grant.Decision,
			Message:       message,
			UpdatedInput:  action.Input,
			settledAction: &action,
		}, "coalesced", false)
	}
}

func candidateAllowsGrant(
	pending *permissionPendingRequest,
	grant permissionCoalescingGrant,
) (action PermissionActionDescriptor, allowed bool) {
	if pending == nil || pending.grantAllows == nil {
		return PermissionActionDescriptor{}, false
	}
	defer func() {
		if recover() != nil {
			action = PermissionActionDescriptor{}
			allowed = false
		}
	}()
	return pending.grantAllows(grant)
}

func (c *PermissionCoordinator) resolve(engineID, toolUseID string, result PermissionInteractionResult, source string) bool {
	return c.settle(permissionRequestKey{engineID: engineID, toolUseID: toolUseID}, result, source)
}

// PermissionCoordinatorRegistry owns process-local coordinators for a runtime
// host such as one CLI process or ACP Agent.
type PermissionCoordinatorRegistry struct {
	mu      sync.Mutex
	entries map[string]*PermissionCoordinator
}

var defaultPermissionCoordinatorRegistry = NewPermissionCoordinatorRegistry()

// NewPermissionCoordinatorRegistry creates an empty project runtime registry.
func NewPermissionCoordinatorRegistry() *PermissionCoordinatorRegistry {
	return &PermissionCoordinatorRegistry{entries: make(map[string]*PermissionCoordinator)}
}

func (r *PermissionCoordinatorRegistry) acquire(projectRoot, engineID string) (*PermissionCoordinator, PermissionProjectIdentity) {
	if r == nil {
		r = NewPermissionCoordinatorRegistry()
	}
	identity := ResolvePermissionProjectIdentity(projectRoot)
	r.mu.Lock()
	coordinator := r.entries[identity.key()]
	if coordinator == nil {
		coordinator = newPermissionCoordinator(identity)
		r.entries[identity.key()] = coordinator
	}
	coordinator.registerEngine(engineID)
	r.mu.Unlock()
	return coordinator, identity
}

func (r *PermissionCoordinatorRegistry) release(identity PermissionProjectIdentity, engineID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	coordinator := r.entries[identity.key()]
	r.mu.Unlock()
	if coordinator == nil || !coordinator.unregisterEngine(engineID) {
		return
	}
	r.mu.Lock()
	if r.entries[identity.key()] == coordinator && coordinator.isIdle() {
		delete(r.entries, identity.key())
	}
	r.mu.Unlock()
}

func (c *PermissionCoordinator) isIdle() bool {
	if c == nil {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.engines) == 0 && len(c.pending) == 0 && len(c.settling) == 0
}

// ActiveProjectCount returns the number of project runtimes with registered
// engines or unresolved requests.
func (r *PermissionCoordinatorRegistry) ActiveProjectCount() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// CoordinatorForProject returns the live coordinator for a canonicalized
// project root. It is intended for lifecycle diagnostics and focused tests.
func (r *PermissionCoordinatorRegistry) CoordinatorForProject(projectRoot string) (*PermissionCoordinator, bool) {
	if r == nil {
		return nil, false
	}
	identity := ResolvePermissionProjectIdentity(projectRoot)
	r.mu.Lock()
	defer r.mu.Unlock()
	coordinator, ok := r.entries[identity.key()]
	return coordinator, ok
}

type coordinatorOwnedPermissionPromptKey struct{}

func withCoordinatorOwnedPermissionPrompt(ctx context.Context) context.Context {
	return context.WithValue(ctx, coordinatorOwnedPermissionPromptKey{}, true)
}

func isCoordinatorOwnedPermissionPrompt(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	owned, _ := ctx.Value(coordinatorOwnedPermissionPromptKey{}).(bool)
	return owned
}

func (e *QueryEngine) rebindPermissionRuntime(projectRoot, rootSessionID string) {
	if e == nil || e.permissionRegistry == nil {
		return
	}
	identity := ResolvePermissionProjectIdentity(projectRoot)
	if identity.key() == e.permissionProjectIdentity.key() {
		e.permissionRootSessionID = rootSessionID
		return
	}
	e.permissionRegistry.release(e.permissionProjectIdentity, e.permissionEngineID)
	e.permissionCoordinator, e.permissionProjectIdentity = e.permissionRegistry.acquire(
		projectRoot,
		e.permissionEngineID,
	)
	e.permissionRootSessionID = rootSessionID
}
