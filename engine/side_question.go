package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/execution"
)

const (
	MaxSideQuestionBytes = 4096
	MaxSideAnswerBytes   = 64 << 10
	maxSideContextBytes  = 32 << 10
	maxSideQuestions     = 8
	sideQuestionTimeout  = 30 * time.Second
)

type SideQuestionStatus string

const (
	SideQuestionPending   SideQuestionStatus = "pending"
	SideQuestionRunning   SideQuestionStatus = "running"
	SideQuestionAnswered  SideQuestionStatus = "answered"
	SideQuestionFailed    SideQuestionStatus = "failed"
	SideQuestionCancelled SideQuestionStatus = "cancelled"
)

// SideQuestionSnapshot is ephemeral engine-owned state. Its text never enters
// the main conversation/transcript unless a client explicitly stages it there.
type SideQuestionSnapshot struct {
	ID, SessionID, ThreadID           string
	Question, Answer                  string
	Status                            SideQuestionStatus
	Reason                            string
	Revision                          uint64
	RouteRevision                     uint64
	StartedAt, FinishedAt             time.Time
	ContextEntries                    int
	ContextTruncated, AnswerTruncated bool
}

type sideQuestionEvent struct {
	ID              string
	Status          SideQuestionStatus
	Answer, Reason  string
	At              time.Time
	AnswerTruncated bool
}

// The local reducer is separate from canonical main-turn sequencing. A side
// question cannot advance a main turn or resurrect a cancelled request.
func reduceSideQuestion(s SideQuestionSnapshot, event sideQuestionEvent) (SideQuestionSnapshot, bool) {
	if s.ID != event.ID {
		return s, false
	}
	valid := s.Status == SideQuestionPending && (event.Status == SideQuestionRunning || event.Status == SideQuestionCancelled || event.Status == SideQuestionFailed) || s.Status == SideQuestionRunning && (event.Status == SideQuestionAnswered || event.Status == SideQuestionCancelled || event.Status == SideQuestionFailed)
	if !valid {
		return s, false
	}
	s.Status, s.Reason = event.Status, event.Reason
	s.Revision++
	if event.Status != SideQuestionRunning {
		s.FinishedAt = event.At.UTC()
	}
	if event.Status == SideQuestionAnswered {
		s.Answer, s.AnswerTruncated = event.Answer, event.AnswerTruncated
	}
	return s, true
}

type sideQuestionCall struct {
	id, sessionID, threadID string
	route                   uint64
	context                 []string
	started                 bool
	cancel                  context.CancelFunc
}

type sideQuestionController struct {
	mu      sync.Mutex
	records []SideQuestionSnapshot
	active  *sideQuestionCall
	closed  bool
}

func (c *sideQuestionController) getLocked(id string) (SideQuestionSnapshot, bool) {
	for _, s := range c.records {
		if s.ID == id {
			return s, true
		}
	}
	return SideQuestionSnapshot{}, false
}

func (c *sideQuestionController) transitionLocked(event sideQuestionEvent) SideQuestionSnapshot {
	for i, s := range c.records {
		if s.ID == event.ID {
			if next, ok := reduceSideQuestion(s, event); ok {
				c.records[i] = next
				return next
			}
			return s
		}
	}
	return SideQuestionSnapshot{}
}

func (e *QueryEngine) sideQuestionIdentity() (string, string, uint64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.config.SessionID, e.config.ThreadID, e.promptRouteGeneration, e.promptRouteClosed
}

// SideQuestionAvailability is metadata-only; it never dispatches a provider.
func (e *QueryEngine) SideQuestionAvailability() (bool, string) {
	if e == nil {
		return false, "Side questions require an interactive engine."
	}
	e.mu.Lock()
	hasModel, child, closed := e.config.ChatModel != nil, e.config.AgentID != "", e.promptRouteClosed
	blocked := e.modelDispatchBlock != nil
	e.mu.Unlock()
	switch {
	case closed:
		return false, "The engine is closed."
	case child:
		return false, "Side questions use the leader conversation."
	case !hasModel || blocked:
		return false, "The selected model is unavailable."
	case e.goalProviderUsageRequired():
		return false, "An unfinished Goal requires its exact provider accounting boundary."
	case e.permissionCoordinator != nil && e.permissionCoordinator.PendingCount() > 0:
		return false, "Resolve the pending permission request first."
	}
	e.planMu.Lock()
	pendingPlan := e.planState.Phase == PlanPhaseAwaitingApproval
	e.planMu.Unlock()
	if pendingPlan {
		return false, "Resolve the pending Plan approval first."
	}
	e.sideQuestions.mu.Lock()
	defer e.sideQuestions.mu.Unlock()
	if e.sideQuestions.closed {
		return false, "The engine is closed."
	}
	if e.sideQuestions.active != nil {
		return false, "A side question is still running; cancel or wait for it to finish."
	}
	return true, ""
}

// BeginSideQuestion captures one bounded text snapshot and reserves a single
// engine-owned request. Preparing it performs no model call or durable write.
func (e *QueryEngine) BeginSideQuestion(question string) (SideQuestionSnapshot, error) {
	question = strings.TrimSpace(question)
	if question == "" || !utf8.ValidString(question) || len(question) > MaxSideQuestionBytes || invalidSideText(question) {
		return SideQuestionSnapshot{}, errors.New("side question must contain 1-4096 bytes of valid text")
	}
	if ok, reason := e.SideQuestionAvailability(); !ok {
		return SideQuestionSnapshot{}, errors.New(reason)
	}
	e.mu.Lock()
	sid, tid, route := e.config.SessionID, e.config.ThreadID, e.promptRouteGeneration
	entries := backgroundMessageStrings(e.messages)
	clipped := len(e.messages) > 80
	for _, message := range e.messages[max(0, len(e.messages)-80):] {
		if message != nil && len(strings.TrimSpace(message.Content)) > 2000 {
			clipped = true
		}
	}
	e.mu.Unlock()
	if e.runtimeState != nil {
		if thread, ok := e.runtimeState.ThreadSnapshot(tid); ok {
			entries = append(entries, fmt.Sprintf("Current runtime: %s; snapshot turn: %s", thread.Status, thread.ActiveTurnID))
			for _, id := range thread.ToolOrder[max(0, len(thread.ToolOrder)-6):] {
				tool := thread.Tools[id]
				clipped = clipped || len(tool.InputPreview) > 1024 || len(tool.OutputPreview) > 2048
				entries = append(entries, fmt.Sprintf("Tool %s [%s]: input %s; output %s", tool.Name, tool.Status, sideTextPrefix(tool.InputPreview, 1024), sideTextPrefix(tool.OutputPreview, 2048)))
			}
			if thread.LiveMessage != nil && thread.LiveMessage.Content != "" {
				clipped = clipped || len(thread.LiveMessage.Content) > 2048
				entries = append(entries, "Current assistant text: "+sideTextPrefix(thread.LiveMessage.Content, 2048))
			}
			clipped = clipped || thread.DroppedMessages > 0 || thread.DroppedEvents > 0
		}
	}
	used := 0
	start := len(entries)
	for start > 0 && used+len(entries[start-1])+32 <= maxSideContextBytes {
		start--
		used += len(entries[start]) + 32
	}
	clipped = clipped || start > 0
	entries = append([]string(nil), entries[start:]...)
	snapshot := SideQuestionSnapshot{ID: generateUUID(), SessionID: sid, ThreadID: tid, Question: question, Status: SideQuestionPending, Revision: 1, RouteRevision: route, StartedAt: time.Now().UTC(), ContextEntries: len(entries), ContextTruncated: clipped}
	e.sideQuestions.mu.Lock()
	defer e.sideQuestions.mu.Unlock()
	if e.sideQuestions.closed || e.sideQuestions.active != nil {
		return SideQuestionSnapshot{}, errors.New("side question slot is unavailable")
	}
	e.sideQuestions.active = &sideQuestionCall{id: snapshot.ID, sessionID: sid, threadID: tid, route: route, context: entries}
	e.sideQuestions.records = append(e.sideQuestions.records, snapshot)
	if len(e.sideQuestions.records) > maxSideQuestions {
		e.sideQuestions.records = append([]SideQuestionSnapshot(nil), e.sideQuestions.records[len(e.sideQuestions.records)-maxSideQuestions:]...)
	}
	return snapshot, nil
}

// SideQuestion returns a detached snapshot only for the current session and
// thread. It is read-only and never restores or dispatches a request.
func (e *QueryEngine) SideQuestion(id string) (SideQuestionSnapshot, bool) {
	if e == nil {
		return SideQuestionSnapshot{}, false
	}
	e.sideQuestions.mu.Lock()
	s, ok := e.sideQuestions.getLocked(id)
	closed := e.sideQuestions.closed
	e.sideQuestions.mu.Unlock()
	sid, tid, route, routeClosed := e.sideQuestionIdentity()
	return s, ok && !closed && !routeClosed && sid == s.SessionID && tid == s.ThreadID && route == s.RouteRevision
}

func (e *QueryEngine) CancelSideQuestion(id string) {
	if e == nil {
		return
	}
	c := &e.sideQuestions
	c.mu.Lock()
	defer c.mu.Unlock()
	call := c.active
	if call == nil || call.id != id {
		return
	}
	c.transitionLocked(sideQuestionEvent{ID: id, Status: SideQuestionCancelled, Reason: "cancelled", At: time.Now()})
	if call.cancel != nil {
		call.cancel()
	}
	if !call.started {
		c.active = nil
	}
}

func (e *QueryEngine) closeSideQuestions() {
	c := &e.sideQuestions
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.active != nil && c.active.cancel != nil {
		c.active.cancel()
	}
	c.records = nil
	if c.active != nil && !c.active.started {
		c.active = nil
	}
}

// Session restoration cancels auxiliary work before waiting for the provider
// boundary, and invalidates even pending requests that were never dispatched.
func (e *QueryEngine) invalidateSideQuestions() {
	c := &e.sideQuestions
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil {
		c.transitionLocked(sideQuestionEvent{ID: c.active.id, Status: SideQuestionCancelled, Reason: "session_changed", At: time.Now()})
		if c.active.cancel != nil {
			c.active.cancel()
		}
		if !c.active.started {
			c.active = nil
		}
	}
	c.records = nil
}

// RunSideQuestion dispatches at most once for an accepted ID. Cancellation
// retains the slot until the provider returns, bounding uncooperative callers.
func (e *QueryEngine) RunSideQuestion(ctx context.Context, id string) (SideQuestionSnapshot, error) {
	if e == nil || ctx == nil {
		return SideQuestionSnapshot{}, errors.New("side question context is required")
	}
	runCtx, cancel := context.WithTimeout(ctx, sideQuestionTimeout)
	defer cancel()
	c := &e.sideQuestions
	c.mu.Lock()
	call := c.active
	if call == nil || call.id != id || call.started || c.closed {
		s, _ := c.getLocked(id)
		c.mu.Unlock()
		return s, errors.New("side question is not pending")
	}
	call.started, call.cancel = true, cancel
	s := c.transitionLocked(sideQuestionEvent{ID: id, Status: SideQuestionRunning, At: time.Now()})
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.active == call {
			c.active = nil
		}
		c.mu.Unlock()
	}()
	answer, reason, err := e.runSideQuestionProvider(runCtx, call, s.Question)
	status := SideQuestionAnswered
	if err != nil {
		status = SideQuestionFailed
	}
	if runCtx.Err() != nil {
		status = SideQuestionCancelled
		reason = "cancelled"
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			status = SideQuestionFailed
			reason = "timeout"
		}
		err = runCtx.Err()
	}
	truncated := len(answer) > MaxSideAnswerBytes
	if truncated {
		answer = sideTextPrefix(answer, MaxSideAnswerBytes)
	}
	c.mu.Lock()
	s = c.transitionLocked(sideQuestionEvent{ID: id, Status: status, Reason: reason, Answer: answer, AnswerTruncated: truncated, At: time.Now()})
	c.mu.Unlock()
	return s, err
}

const sideQuestionPrompt = "SIDE QUESTION MODE. Answer the user's independent question using only the supplied conversation/runtime snapshot. Match the user's language. State uncertainty if context is incomplete. Give a concise explanation, never execute actions or call tools. The main task continues independently; this answer is not part of its conversation."

func (e *QueryEngine) runSideQuestionProvider(ctx context.Context, call *sideQuestionCall, question string) (string, string, error) {
	e.goalProviderBoundary.RLock()
	defer e.goalProviderBoundary.RUnlock()
	sid, tid, route, closed := e.sideQuestionIdentity()
	if closed || sid != call.sessionID || tid != call.threadID || route != call.route {
		return "", "stale", errors.New("side question context changed")
	}
	if ctx.Err() != nil {
		return "", "cancelled", ctx.Err()
	}
	if e.goalProviderUsageRequired() {
		return "", "goal_active", errors.New("side question blocked by unfinished Goal")
	}
	if e.permissionCoordinator != nil && e.permissionCoordinator.PendingCount() > 0 {
		return "", "pending_permission", errors.New("side question blocked by pending permission")
	}
	e.planMu.Lock()
	pendingPlan := e.planState.Phase == PlanPhaseAwaitingApproval
	e.planMu.Unlock()
	if pendingPlan {
		return "", "pending_plan", errors.New("side question blocked by pending Plan approval")
	}
	providerCall, err := e.promptSuggestionProviderCall()
	if err != nil {
		return "", "unavailable", errors.New("side question model is unavailable")
	}
	if providerCall.routeGeneration != call.route {
		return "", "stale", errors.New("side question model changed")
	}
	maxTokens := 2048
	opts := providerCall.options
	opts.SystemPrompt, opts.QuerySource, opts.MaxOutputTokens, opts.ToolChoice = sideQuestionPrompt, "side_question", &maxTokens, "none"
	// Textualization deliberately excludes tool schemas, media, reasoning and
	// unresolved protocol messages. Snapshot data remains ordinary user content.
	opts.Messages = sideQuestionMessages(call.context, question)
	opts.ProviderUsage = e.currentProviderUsageAdmitter()
	if opts.ProviderUsage != nil {
		opts.UsageLogicalRoundID = opts.ProviderUsage.NewLogicalRoundID()
	}
	response, err := execution.SideQuery(ctx, providerCall.chatModel, opts)
	if err != nil {
		reason := "provider_error"
		if execution.IsProviderUsageTerminalError(err) {
			reason = "usage_blocked"
		}
		return "", reason, errors.New("side question provider request failed")
	}
	if response == nil {
		return "", "empty", errors.New("side question returned no answer")
	}
	// Close takes this same local lease before sealing the recorder. Provider
	// usage admission/settlement already happened in SideQuery; late metadata
	// must never reopen a recorder after the engine has closed.
	e.sideQuestions.mu.Lock()
	if e.sideQuestions.closed {
		e.sideQuestions.mu.Unlock()
		return "", "stale", errors.New("side question engine closed")
	}
	usageErr := e.observeAuxiliaryProviderUsageMessage(response)
	e.sideQuestions.mu.Unlock()
	if usageErr != nil {
		return "", "usage_persistence", errors.New("side question usage could not be persisted")
	}
	sid, tid, route, closed = e.sideQuestionIdentity()
	if closed || sid != call.sessionID || tid != call.threadID || route != call.route {
		return "", "stale", errors.New("side question context changed")
	}
	if len(response.ToolCalls) > 0 {
		return "", "tools_not_allowed", errors.New("side question returned a forbidden tool call")
	}
	answer := strings.TrimSpace(response.Content)
	if answer == "" {
		return "", "empty", errors.New("side question returned no answer")
	}
	if !utf8.ValidString(answer) || invalidSideText(answer) {
		return "", "invalid_text", errors.New("side question returned invalid text")
	}
	return answer, "", nil
}

func invalidSideText(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' })
}

func sideTextPrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
