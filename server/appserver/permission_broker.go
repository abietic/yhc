package appserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
)

type permissionWaiter struct {
	request          engine.PermissionPromptRequest
	digest           string
	turnID           string
	callbackObserved bool
	eventObserved    bool
	order            uint64
	reviewedRevision uint64
	reviewedDigest   string
	settled          bool
	result           engine.PermissionInteractionResult
	ready            chan struct{}
	readyClosed      bool
	done             chan struct{}
}

type permissionBroker struct {
	mu        sync.Mutex
	pending   map[string]*permissionWaiter
	retired   map[string]struct{}
	closed    bool
	nextOrder uint64
}

func newPermissionBroker() *permissionBroker {
	return &permissionBroker{
		pending: make(map[string]*permissionWaiter),
		retired: make(map[string]struct{}),
	}
}

// prepare records the callback observation. It never merges mutable request
// fields: a second non-identical observation is a protocol conflict.
func (b *permissionBroker) prepare(request engine.PermissionPromptRequest) *permissionWaiter {
	return b.observe(request, "", true, false)
}

func (b *permissionBroker) observeEvent(request engine.PermissionPromptRequest, turnID string) {
	turnID = strings.TrimSpace(turnID)
	if _, ok := projectInteraction(request, turnID); !ok {
		b.fail(strings.TrimSpace(request.ToolUseID))
		return
	}
	b.observe(request, turnID, false, true)
}

func (b *permissionBroker) observe(
	request engine.PermissionPromptRequest,
	turnID string,
	callback, event bool,
) *permissionWaiter {
	id := strings.TrimSpace(request.ToolUseID)
	if id == "" {
		return nil
	}
	if event && strings.TrimSpace(turnID) == "" {
		b.fail(id)
		return nil
	}
	digest, ok := permissionRequestDigest(request)
	if !ok {
		b.fail(id)
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	if _, retired := b.retired[id]; retired {
		return nil
	}
	w := b.pending[id]
	if w == nil {
		b.nextOrder++
		w = &permissionWaiter{
			request: clonePromptRequest(request),
			digest:  digest,
			order:   b.nextOrder,
			ready:   make(chan struct{}),
			done:    make(chan struct{}),
		}
		b.pending[id] = w
	} else if w.digest != digest || (event && turnID != "" && w.turnID != "" && w.turnID != turnID) {
		delete(b.pending, id)
		b.retired[id] = struct{}{}
		settlePermissionWaiterLocked(w, engine.PermissionInteractionResult{
			Decision: engine.PermissionCancelled,
			Message:  "permission request conflict",
		})
		return w
	} else if event && w.turnID == "" {
		w.turnID = turnID
	}
	if callback {
		w.callbackObserved = true
	}
	if event {
		w.eventObserved = true
		if w.turnID == "" {
			w.turnID = turnID
		}
	}
	if w.callbackObserved && w.eventObserved && strings.TrimSpace(w.turnID) != "" {
		closePermissionWaiterReadyLocked(w)
	}
	return w
}

func (b *permissionBroker) wait(ctx context.Context, request engine.PermissionPromptRequest) engine.PermissionInteractionResult {
	id := strings.TrimSpace(request.ToolUseID)
	if id == "" {
		return engine.PermissionInteractionResult{Decision: engine.PermissionDeny, Message: "permission request has no tool use id"}
	}
	w := b.prepare(request)
	if w == nil {
		return engine.PermissionInteractionResult{
			Decision: engine.PermissionCancelled,
			Message:  "app-server permission request is unavailable",
		}
	}
	select {
	case <-w.done:
		return w.result
	case <-ctx.Done():
		b.mu.Lock()
		if b.pending[id] == w && !w.settled {
			delete(b.pending, id)
			b.retired[id] = struct{}{}
			settlePermissionWaiterLocked(w, engine.PermissionInteractionResult{
				Decision: engine.PermissionCancelled,
				Message:  "permission request cancelled",
			})
		}
		b.mu.Unlock()
		<-w.done
		return w.result
	}
}

func (b *permissionBroker) prompt(ctx context.Context, request engine.PermissionPromptRequest) engine.PermissionInteractionResult {
	return b.wait(ctx, request)
}

func (b *permissionBroker) repeatedPrompt(
	ctx context.Context,
	toolName string,
	toolUseID string,
	attempt int,
	toolContext *engine.ToolUseContext,
) (bool, string) {
	request := engine.PermissionPromptRequest{
		Kind:      engine.PermissionInteractionKindRepeatedTool,
		Attempt:   attempt,
		Source:    "repeated_tool_guard",
		ToolName:  toolName,
		ToolUseID: toolUseID,
		Message:   engine.RepeatedToolInteractionPromptMessage,
	}
	if toolContext != nil {
		request.SessionID = strings.TrimSpace(toolContext.SessionID)
		request.ThreadID = strings.TrimSpace(toolContext.ThreadID)
		request.AgentID = strings.TrimSpace(toolContext.AgentID)
	}
	result := b.wait(ctx, request)
	if result.Allowed() {
		return true, result.Message
	}
	if strings.TrimSpace(result.Message) == "" {
		return false, "user chose to stop and change strategy"
	}
	return false, result.Message
}

type interactionResolveStatus uint8

const (
	interactionResolveNotFound interactionResolveStatus = iota
	interactionResolveInvalid
	interactionResolveAccepted
)

func (b *permissionBroker) resolve(id string, input ResolveInteractionRequest) interactionResolveStatus {
	b.mu.Lock()
	w, ok := b.pending[strings.TrimSpace(id)]
	if !ok || b.closed || !w.callbackObserved || !w.eventObserved || strings.TrimSpace(w.turnID) == "" {
		b.mu.Unlock()
		return interactionResolveNotFound
	}
	result, valid := interactionResult(w, input)
	if !valid {
		b.mu.Unlock()
		return interactionResolveInvalid
	}
	delete(b.pending, strings.TrimSpace(id))
	b.retired[strings.TrimSpace(id)] = struct{}{}
	settled := settlePermissionWaiterLocked(w, result)
	b.mu.Unlock()
	if !settled {
		return interactionResolveNotFound
	}
	return interactionResolveAccepted
}

func (b *permissionBroker) reviewable(id string) (*permissionWaiter, engine.PermissionPromptRequest, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.pending[strings.TrimSpace(id)]
	if b.closed || w == nil || w.settled || !w.callbackObserved || !w.eventObserved || strings.TrimSpace(w.turnID) == "" {
		return nil, engine.PermissionPromptRequest{}, false
	}
	return w, clonePromptRequest(w.request), true
}

func (b *permissionBroker) awaitInteraction(
	ctx context.Context,
	id string,
) (InteractionSnapshot, bool) {
	b.mu.Lock()
	w := b.pending[strings.TrimSpace(id)]
	if b.closed || w == nil || w.settled {
		b.mu.Unlock()
		return InteractionSnapshot{}, false
	}
	ready := w.ready
	b.mu.Unlock()
	select {
	case <-ready:
		return b.interaction(id)
	case <-ctx.Done():
		return InteractionSnapshot{}, false
	}
}

func (b *permissionBroker) recordPlanReview(
	id string,
	expected *permissionWaiter,
	revision uint64,
	digest string,
) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.pending[strings.TrimSpace(id)]
	if b.closed || w == nil || w != expected || w.settled || w.request.PlanApproval == nil ||
		w.request.PlanApproval.PlanRevision != revision ||
		w.request.PlanApproval.InitialPlanDigest != digest {
		return false
	}
	w.reviewedRevision = revision
	w.reviewedDigest = digest
	return true
}

func (b *permissionBroker) fail(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	w := b.pending[id]
	if w != nil {
		delete(b.pending, id)
	}
	b.retired[id] = struct{}{}
	if w != nil {
		settlePermissionWaiterLocked(w, engine.PermissionInteractionResult{
			Decision: engine.PermissionCancelled,
			Message:  "permission request conflict",
		})
	}
}

func (b *permissionBroker) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, w := range b.pending {
		delete(b.pending, id)
		b.retired[id] = struct{}{}
		settlePermissionWaiterLocked(w, engine.PermissionInteractionResult{
			Decision: engine.PermissionCancelled,
			Message:  "app-server session closed",
		})
	}
}

func settlePermissionWaiterLocked(
	w *permissionWaiter,
	result engine.PermissionInteractionResult,
) bool {
	if w == nil || w.settled {
		return false
	}
	w.result = result
	w.settled = true
	closePermissionWaiterReadyLocked(w)
	close(w.done)
	return true
}

func closePermissionWaiterReadyLocked(w *permissionWaiter) {
	if w == nil || w.readyClosed {
		return
	}
	w.readyClosed = true
	close(w.ready)
}

func clonePromptRequest(request engine.PermissionPromptRequest) engine.PermissionPromptRequest {
	request.Input = cloneInput(request.Input)
	if request.PlanApproval != nil {
		p := *request.PlanApproval
		request.PlanApproval = &p
	}
	if request.Presentation != nil {
		p := *request.Presentation
		p.Evidence = append([]engine.PermissionPresentationEvidence(nil), p.Evidence...)
		p.GrantScopes = append([]engine.PermissionInteractionDecision(nil), p.GrantScopes...)
		request.Presentation = &p
	}
	return request
}

func cloneInput(v map[string]any) map[string]any {
	if v == nil {
		return nil
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(encoded, &out) != nil {
		return nil
	}
	return out
}

func permissionRequestDigest(r engine.PermissionPromptRequest) (string, bool) {
	if !validBrokerPermissionRequest(r) {
		return "", false
	}
	input, err := json.Marshal(r.Input)
	if err != nil {
		return "", false
	}
	inputHash := sha256.Sum256(input)
	payload := struct {
		ID            string
		Session       string
		Thread        string
		Agent         string
		Kind          string
		Source        string
		Tool          string
		CanonicalTool string
		Message       string
		InputHash     string
		Attempt       int
		Plan          *engine.PlanApprovalRequest
		Presentation  *engine.PermissionPresentation
	}{
		ID:            strings.TrimSpace(r.ToolUseID),
		Session:       strings.TrimSpace(r.SessionID),
		Thread:        strings.TrimSpace(r.ThreadID),
		Agent:         strings.TrimSpace(r.AgentID),
		Kind:          strings.TrimSpace(r.Kind),
		Source:        strings.TrimSpace(r.Source),
		Tool:          strings.TrimSpace(r.ToolName),
		CanonicalTool: strings.TrimSpace(r.CanonicalToolName),
		Message:       r.Message,
		InputHash:     hex.EncodeToString(inputHash[:]),
		Attempt:       r.Attempt,
		Plan:          r.PlanApproval,
		Presentation:  r.Presentation,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), true
}

// validBrokerPermissionRequest is deliberately stricter than the legacy
// adapter boundary. The app protocol has no untyped fallback: every
// request-producing path must freeze a complete typed identity before the
// broker can coalesce its callback and event observations.
func validBrokerPermissionRequest(request engine.PermissionPromptRequest) bool {
	if strings.TrimSpace(request.ToolUseID) == "" ||
		strings.TrimSpace(request.Kind) == "" ||
		strings.TrimSpace(request.Source) == "" {
		return false
	}
	switch request.Kind {
	case engine.PermissionInteractionKindPermission:
		return request.Attempt == 0 && request.PlanApproval == nil
	case engine.PermissionInteractionKindQuestion:
		return request.Attempt == 0 && request.PlanApproval == nil && request.Presentation == nil
	case engine.PermissionInteractionKindPlanApproval:
		approval := request.PlanApproval
		return request.Attempt == 0 && request.Presentation == nil && approval != nil &&
			approval.RequestID == request.ToolUseID && approval.PlanRevision > 0 &&
			strings.TrimSpace(approval.PlanFileIdentity) != "" &&
			strings.TrimSpace(approval.InitialPlanDigest) != ""
	case engine.PermissionInteractionKindRepeatedTool:
		return request.Attempt == 3 && request.Source == "repeated_tool_guard" &&
			request.PlanApproval == nil && request.Presentation == nil
	default:
		return false
	}
}

const (
	maxInteractionMessageBytes = 4 << 10
	maxInteractionAnswerBytes  = 16 << 10
	maxInteractionAnswersBytes = 32 << 10
	maxPlanFeedbackBytes       = 16 << 10

	repeatedToolExplanation = "This repeated tool call needs your decision."
	repeatedToolStopMessage = "user chose to stop and change strategy"
)

func (b *permissionBroker) interaction(id string) (InteractionSnapshot, bool) {
	b.mu.Lock()
	w := b.pending[strings.TrimSpace(id)]
	if b.closed || w == nil || w.settled || !w.callbackObserved || !w.eventObserved || strings.TrimSpace(w.turnID) == "" {
		b.mu.Unlock()
		return InteractionSnapshot{}, false
	}
	request := clonePromptRequest(w.request)
	turnID := w.turnID
	b.mu.Unlock()
	return projectInteraction(request, turnID)
}

func (b *permissionBroker) interactions() []InteractionSnapshot {
	type pendingProjection struct {
		request engine.PermissionPromptRequest
		turnID  string
		order   uint64
	}
	b.mu.Lock()
	pending := make([]pendingProjection, 0, len(b.pending))
	if !b.closed {
		for _, w := range b.pending {
			if w.settled || !w.callbackObserved || !w.eventObserved || strings.TrimSpace(w.turnID) == "" {
				continue
			}
			pending = append(pending, pendingProjection{
				request: clonePromptRequest(w.request),
				turnID:  w.turnID,
				order:   w.order,
			})
		}
	}
	b.mu.Unlock()
	sort.Slice(pending, func(i, j int) bool { return pending[i].order < pending[j].order })
	out := make([]InteractionSnapshot, 0, len(pending))
	for _, item := range pending {
		interaction, ok := projectInteraction(item.request, item.turnID)
		if ok {
			out = append(out, interaction)
		}
	}
	return out
}

func projectInteraction(request engine.PermissionPromptRequest, turnID string) (InteractionSnapshot, bool) {
	requestID := strings.TrimSpace(request.ToolUseID)
	turnID = strings.TrimSpace(turnID)
	if requestID == "" || turnID == "" {
		return InteractionSnapshot{}, false
	}
	interaction := InteractionSnapshot{RequestID: requestID, TurnID: turnID, Kind: request.Kind}
	switch request.Kind {
	case engine.PermissionInteractionKindPermission:
		interaction.Permission = projectPermissionInteraction(request.Presentation)
	case engine.PermissionInteractionKindQuestion:
		questions, ok := frozenQuestions(request)
		if !ok {
			return InteractionSnapshot{}, false
		}
		interaction.Question = &QuestionInteractionSnapshot{Questions: projectQuestions(questions)}
	case engine.PermissionInteractionKindPlanApproval:
		approval := request.PlanApproval
		if approval == nil || approval.RequestID != requestID || approval.PlanRevision == 0 ||
			strings.TrimSpace(approval.PlanFileIdentity) == "" || strings.TrimSpace(approval.InitialPlanDigest) == "" {
			return InteractionSnapshot{}, false
		}
		targets := engine.PlanApprovalTargetModes(approval.ReturnMode)
		targetModes := make([]string, len(targets))
		for index, target := range targets {
			targetModes[index] = string(target)
		}
		interaction.PlanApproval = &PlanApprovalInteractionSnapshot{
			Revision: approval.PlanRevision, TargetModes: targetModes, ReviewAvailable: true,
		}
	case engine.PermissionInteractionKindRepeatedTool:
		if request.Attempt <= 0 {
			return InteractionSnapshot{}, false
		}
		interaction.RepeatedTool = &RepeatedToolInteractionSnapshot{
			Attempt: request.Attempt, Explanation: repeatedToolExplanation,
			Outcomes: []string{"continue", "stop"},
		}
	default:
		return InteractionSnapshot{}, false
	}
	return interaction, true
}

func projectPermissionInteraction(presentation *engine.PermissionPresentation) *PermissionInteractionSnapshot {
	unavailable := &PermissionInteractionSnapshot{
		Available: false, Evidence: []PermissionEvidenceSnapshot{}, GrantScopes: []string{string(engine.PermissionAllowOnce)},
	}
	if !validPublicPermissionPresentation(presentation) || presentation.Unavailable {
		return unavailable
	}
	evidence := make([]PermissionEvidenceSnapshot, len(presentation.Evidence))
	for index, item := range presentation.Evidence {
		evidence[index] = PermissionEvidenceSnapshot{Label: item.Label, Value: item.Value}
	}
	scopes := make([]string, len(presentation.GrantScopes))
	for index, scope := range presentation.GrantScopes {
		scopes[index] = string(scope)
	}
	return &PermissionInteractionSnapshot{
		Available: true, ToolLabel: presentation.ToolLabel, Summary: presentation.Summary,
		Evidence: evidence, GrantScopes: scopes,
	}
}

func validPublicPermissionPresentation(p *engine.PermissionPresentation) bool {
	if p == nil || p.Version != 1 || len(p.Evidence) > 6 || len(p.GrantScopes) > 3 ||
		!boundedRunes(p.ToolLabel, 96) || !boundedRunes(p.Summary, 256) {
		return false
	}
	if p.Unavailable {
		return p.ToolLabel == "" && p.Summary == "" && len(p.Evidence) == 0 &&
			len(p.GrantScopes) == 1 && p.GrantScopes[0] == engine.PermissionAllowOnce
	}
	if strings.TrimSpace(p.ToolLabel) == "" || p.ToolLabel != strings.TrimSpace(p.ToolLabel) ||
		p.Summary != "Allow this tool action?" || len(p.GrantScopes) != 3 ||
		p.GrantScopes[0] != engine.PermissionAllowOnce ||
		p.GrantScopes[1] != engine.PermissionAllowSession ||
		p.GrantScopes[2] != engine.PermissionAllowAlways {
		return false
	}
	labels := make(map[string]struct{}, len(p.Evidence))
	access := 0
	for _, item := range p.Evidence {
		if !boundedRunes(item.Label, 48) || !boundedRunes(item.Value, 160) ||
			!allowedPermissionEvidence(item.Label, item.Value) {
			return false
		}
		if _, exists := labels[item.Label]; exists {
			return false
		}
		labels[item.Label] = struct{}{}
		if item.Label == "Access" {
			access++
		}
	}
	return access == 1
}

func allowedPermissionEvidence(label, value string) bool {
	switch label + "\x00" + value {
	case "Access\x00Reads data", "Access\x00May make destructive changes", "Access\x00May change data",
		"Access\x00May perform an action", "Path scope\x00Within workspace",
		"Path scope\x00Outside workspace boundary", "Network\x00Uses network access",
		"Process\x00Starts a child process":
		return true
	default:
		return false
	}
}

func boundedRunes(value string, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func frozenQuestions(request engine.PermissionPromptRequest) ([]tools.UserQuestion, bool) {
	raw, ok := request.Input["questions"]
	if !ok {
		return nil, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil || len(encoded) > tools.MaxAskUserQuestionsBytes {
		return nil, false
	}
	var questions []tools.UserQuestion
	if err := json.Unmarshal(encoded, &questions); err != nil || tools.ValidateUserQuestions(questions) != nil {
		return nil, false
	}
	return questions, true
}

func projectQuestions(questions []tools.UserQuestion) []QuestionSnapshot {
	out := make([]QuestionSnapshot, len(questions))
	for questionIndex, question := range questions {
		questionID := fmt.Sprintf("q-%d", questionIndex+1)
		options := make([]QuestionOptionSnapshot, len(question.Options))
		for optionIndex, option := range question.Options {
			options[optionIndex] = QuestionOptionSnapshot{
				ID: fmt.Sprintf("%s-o-%d", questionID, optionIndex+1), Label: option.Label,
				Description: option.Description,
			}
		}
		out[questionIndex] = QuestionSnapshot{
			ID: questionID, Header: question.Header, Text: question.Question, Options: options,
			MultiSelect: question.MultiSelect, FreeText: len(question.Options) == 0,
		}
	}
	return out
}

func interactionResult(w *permissionWaiter, input ResolveInteractionRequest) (engine.PermissionInteractionResult, bool) {
	if strings.TrimSpace(input.Kind) != w.request.Kind || interactionVariantCount(input) != 1 {
		return engine.PermissionInteractionResult{}, false
	}
	switch w.request.Kind {
	case engine.PermissionInteractionKindPermission:
		return permissionInteractionResult(w.request, input)
	case engine.PermissionInteractionKindQuestion:
		return questionInteractionResult(w.request, input)
	case engine.PermissionInteractionKindPlanApproval:
		return planInteractionResult(w, input)
	case engine.PermissionInteractionKindRepeatedTool:
		return repeatedToolInteractionResult(input)
	default:
		return engine.PermissionInteractionResult{}, false
	}
}

func interactionVariantCount(input ResolveInteractionRequest) int {
	count := 0
	for _, present := range []bool{
		input.Permission != nil, input.Question != nil, input.PlanApproval != nil, input.RepeatedTool != nil,
	} {
		if present {
			count++
		}
	}
	return count
}

func permissionInteractionResult(
	request engine.PermissionPromptRequest,
	input ResolveInteractionRequest,
) (engine.PermissionInteractionResult, bool) {
	if input.Permission == nil {
		return engine.PermissionInteractionResult{}, false
	}
	decision := engine.PermissionInteractionDecision(strings.TrimSpace(input.Permission.Decision))
	message := input.Permission.Message
	if !boundedBytes(message, maxInteractionMessageBytes) {
		return engine.PermissionInteractionResult{}, false
	}
	switch decision {
	case engine.PermissionAllowOnce, engine.PermissionAllowSession, engine.PermissionAllowAlways:
		if message != "" || !permissionPresentationAdvertises(request.Presentation, decision) {
			return engine.PermissionInteractionResult{}, false
		}
	case engine.PermissionDeny, engine.PermissionCancelled:
	default:
		return engine.PermissionInteractionResult{}, false
	}
	return engine.PermissionInteractionResult{Decision: decision, Message: strings.TrimSpace(message)}, true
}

func questionInteractionResult(
	request engine.PermissionPromptRequest,
	input ResolveInteractionRequest,
) (engine.PermissionInteractionResult, bool) {
	if input.Question == nil {
		return engine.PermissionInteractionResult{}, false
	}
	switch strings.TrimSpace(input.Question.Outcome) {
	case "discuss":
		if len(input.Question.Answers) != 0 {
			return engine.PermissionInteractionResult{}, false
		}
		return engine.PermissionInteractionResult{Decision: engine.PermissionDeny, Message: "user chose to discuss the question"}, true
	case "cancel":
		if len(input.Question.Answers) != 0 {
			return engine.PermissionInteractionResult{}, false
		}
		return engine.PermissionInteractionResult{Decision: engine.PermissionCancelled, Message: "question cancelled"}, true
	case "submit":
	default:
		return engine.PermissionInteractionResult{}, false
	}
	questions, ok := frozenQuestions(request)
	if !ok || len(input.Question.Answers) != len(questions) {
		return engine.PermissionInteractionResult{}, false
	}
	answers := make(map[string]string, len(questions))
	totalTextBytes := 0
	for index, question := range questions {
		answer := input.Question.Answers[index]
		questionID := fmt.Sprintf("q-%d", index+1)
		if answer.QuestionID != questionID || !boundedBytes(answer.Text, maxInteractionAnswerBytes) {
			return engine.PermissionInteractionResult{}, false
		}
		totalTextBytes += len(answer.Text)
		if totalTextBytes > maxInteractionAnswersBytes {
			return engine.PermissionInteractionResult{}, false
		}
		value, valid := reconstructQuestionAnswer(questionID, question, answer)
		if !valid {
			return engine.PermissionInteractionResult{}, false
		}
		answers[question.Question] = value
	}
	return engine.PermissionInteractionResult{
		Decision: engine.PermissionAllowOnce,
		UpdatedInput: map[string]any{
			"questions": questions,
			"answers":   answers,
		},
	}, true
}

func reconstructQuestionAnswer(
	questionID string,
	question tools.UserQuestion,
	answer QuestionAnswerResult,
) (string, bool) {
	textPresent := strings.TrimSpace(answer.Text) != ""
	if len(question.Options) == 0 {
		if len(answer.OptionIDs) != 0 || !textPresent {
			return "", false
		}
		return answer.Text, true
	}
	selected := make(map[int]struct{}, len(answer.OptionIDs))
	for _, optionID := range answer.OptionIDs {
		matched := -1
		for index := range question.Options {
			if optionID == fmt.Sprintf("%s-o-%d", questionID, index+1) {
				matched = index
				break
			}
		}
		if matched < 0 {
			return "", false
		}
		if _, duplicate := selected[matched]; duplicate {
			return "", false
		}
		selected[matched] = struct{}{}
	}
	if question.MultiSelect {
		if len(selected) == 0 && !textPresent {
			return "", false
		}
	} else if (len(selected) == 1) == textPresent || len(selected) > 1 {
		return "", false
	}
	parts := make([]string, 0, len(selected)+1)
	for index, option := range question.Options {
		if _, ok := selected[index]; ok {
			parts = append(parts, option.Label)
		}
	}
	if textPresent {
		parts = append(parts, answer.Text)
	}
	return strings.Join(parts, ", "), true
}

func planInteractionResult(
	w *permissionWaiter,
	input ResolveInteractionRequest,
) (engine.PermissionInteractionResult, bool) {
	approval := w.request.PlanApproval
	decision := input.PlanApproval
	if approval == nil || decision == nil || decision.Revision != approval.PlanRevision ||
		decision.Revision != w.reviewedRevision || strings.TrimSpace(decision.ReviewedDigest) == "" ||
		decision.ReviewedDigest != w.reviewedDigest || !boundedBytes(decision.Feedback, maxPlanFeedbackBytes) {
		return engine.PermissionInteractionResult{}, false
	}
	outcome := engine.PlanApprovalOutcome(strings.TrimSpace(decision.Outcome))
	switch outcome {
	case engine.PlanApprovalApprove, engine.PlanApprovalRevise, engine.PlanApprovalCancel:
	default:
		return engine.PermissionInteractionResult{}, false
	}
	target := permission.Mode(strings.TrimSpace(decision.TargetMode))
	validTarget := false
	for _, candidate := range engine.PlanApprovalTargetModes(approval.ReturnMode) {
		if target == candidate {
			validTarget = true
			break
		}
	}
	if !validTarget {
		return engine.PermissionInteractionResult{}, false
	}
	if outcome == engine.PlanApprovalApprove {
		if (target == permission.ModeBypassPermissions) != decision.Confirmed {
			return engine.PermissionInteractionResult{}, false
		}
	} else if decision.Confirmed {
		return engine.PermissionInteractionResult{}, false
	}
	feedback := strings.TrimSpace(decision.Feedback)
	if outcome == engine.PlanApprovalRevise && feedback == "" ||
		outcome == engine.PlanApprovalApprove && feedback != "" {
		return engine.PermissionInteractionResult{}, false
	}
	result := engine.PermissionInteractionResult{
		Decision: engine.PermissionDeny,
		PlanApproval: &engine.PlanApprovalDecision{
			RequestID: approval.RequestID, PlanRevision: approval.PlanRevision, Outcome: outcome,
			ReviewedPlanDigest: decision.ReviewedDigest, TargetMode: target,
			Confirmed: decision.Confirmed, Feedback: feedback,
		},
	}
	if outcome == engine.PlanApprovalApprove {
		result.Decision = engine.PermissionAllowOnce
	}
	return result, true
}

func repeatedToolInteractionResult(input ResolveInteractionRequest) (engine.PermissionInteractionResult, bool) {
	if input.RepeatedTool == nil {
		return engine.PermissionInteractionResult{}, false
	}
	switch strings.TrimSpace(input.RepeatedTool.Outcome) {
	case "continue":
		return engine.PermissionInteractionResult{Decision: engine.PermissionAllowOnce}, true
	case "stop":
		return engine.PermissionInteractionResult{Decision: engine.PermissionDeny, Message: repeatedToolStopMessage}, true
	default:
		return engine.PermissionInteractionResult{}, false
	}
}

func boundedBytes(value string, maximum int) bool {
	return utf8.ValidString(value) && len(value) <= maximum
}

func permissionPresentationAdvertises(
	presentation *engine.PermissionPresentation,
	decision engine.PermissionInteractionDecision,
) bool {
	projected := projectPermissionInteraction(presentation)
	for _, scope := range projected.GrantScopes {
		if scope == string(decision) {
			return true
		}
	}
	return false
}
