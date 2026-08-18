package tui

import (
	jsonPkg "encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
	"github.com/abietic/yhc/engine/permission"
)

const defaultThreadAttentionLimit = 64

type threadAttentionKind string

const (
	threadAttentionPermission   threadAttentionKind = "permission"
	threadAttentionQuestion     threadAttentionKind = "question"
	threadAttentionPlan         threadAttentionKind = "plan"
	threadAttentionRepeatedTool threadAttentionKind = "repeated_tool"
)

type threadAttentionRequest struct {
	ID                 string
	ThreadID           string
	AgentID            string
	OwnerLabel         string
	Kind               threadAttentionKind
	Tool               string
	Input              string
	SessionScope       string
	Attempt            int
	SessionID          string
	Source             string
	PlanApproval       *engine.PlanApprovalRequest
	decisionConstraint engine.PermissionDecisionConstraint
	responseCh         chan<- PermissionResponse
	uiResponse         chan PermissionResponse
	responseData       threadAttentionResponseData
	dataCaptured       bool
	order              uint64
}

type threadAttentionResponseData struct {
	answerJSON         string
	feedback           string
	planReviewedDigest string
	planTarget         permission.Mode
	planConfirmed      bool
	planResult         *engine.PlanApprovalDecision
}

type threadAttentionStore struct {
	limit      int
	nextOrder  uint64
	requests   map[string]*threadAttentionRequest
	order      []string
	activeID   string
	suppressed map[string]struct{}
}

func newThreadAttentionStore(limit int) *threadAttentionStore {
	if limit <= 0 {
		limit = defaultThreadAttentionLimit
	}
	return &threadAttentionStore{
		limit: limit, requests: make(map[string]*threadAttentionRequest), suppressed: make(map[string]struct{}),
	}
}

func (s *threadAttentionStore) upsert(request threadAttentionRequest) bool {
	if s == nil {
		return false
	}
	request.ID = strings.TrimSpace(request.ID)
	if request.ID == "" {
		s.nextOrder++
		request.ID = fmt.Sprintf("tui-attention-%d", s.nextOrder)
	}
	if existing := s.requests[request.ID]; existing != nil {
		mergeThreadAttentionRequest(existing, request)
		return true
	}
	if _, suppressed := s.suppressed[request.ID]; suppressed {
		if request.responseCh != nil {
			select {
			case request.responseCh <- PermissionDeny:
			default:
			}
		}
		return false
	}
	if len(s.requests) >= s.limit {
		if request.responseCh != nil {
			select {
			case request.responseCh <- PermissionDeny:
			default:
			}
		}
		return false
	}
	s.nextOrder++
	request.order = s.nextOrder
	cloned := request
	s.requests[request.ID] = &cloned
	s.order = append(s.order, request.ID)
	return true
}

func mergeThreadAttentionRequest(current *threadAttentionRequest, incoming threadAttentionRequest) {
	if current == nil {
		return
	}
	if incoming.ThreadID != "" {
		current.ThreadID = incoming.ThreadID
	}
	if incoming.AgentID != "" {
		current.AgentID = incoming.AgentID
	}
	if incoming.OwnerLabel != "" {
		current.OwnerLabel = incoming.OwnerLabel
	}
	if incoming.Kind != "" {
		current.Kind = incoming.Kind
	}
	if incoming.Tool != "" {
		current.Tool = incoming.Tool
	}
	if incoming.Input != "" {
		current.Input = incoming.Input
	}
	if incoming.SessionScope != "" {
		current.SessionScope = incoming.SessionScope
	}
	current.decisionConstraint = incoming.decisionConstraint
	if incoming.Attempt != 0 {
		current.Attempt = incoming.Attempt
	}
	if incoming.SessionID != "" {
		current.SessionID = incoming.SessionID
	}
	if incoming.Source != "" {
		current.Source = incoming.Source
	}
	if incoming.PlanApproval != nil {
		approval := *incoming.PlanApproval
		current.PlanApproval = &approval
	}
	if incoming.responseCh != nil {
		current.responseCh = incoming.responseCh
	}
}

func (s *threadAttentionStore) get(requestID string) (*threadAttentionRequest, bool) {
	if s == nil {
		return nil, false
	}
	request, ok := s.requests[requestID]
	return request, ok
}

func (s *threadAttentionStore) nextForThread(threadID string) (*threadAttentionRequest, bool) {
	if s == nil || s.activeID != "" {
		return nil, false
	}
	for _, requestID := range s.order {
		request := s.requests[requestID]
		if request != nil && request.ThreadID == threadID && !(request.Source == "callback" && request.responseCh == nil) {
			return request, true
		}
	}
	return nil, false
}

func (s *threadAttentionStore) remove(requestID string, suppress bool) *threadAttentionRequest {
	if s == nil {
		return nil
	}
	request := s.requests[requestID]
	if request == nil {
		if suppress && requestID != "" {
			s.suppressed[requestID] = struct{}{}
		}
		return nil
	}
	closeThreadAttentionUIResponse(request)
	delete(s.requests, requestID)
	for i, id := range s.order {
		if id == requestID {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	if s.activeID == requestID {
		s.activeID = ""
	}
	if suppress {
		s.suppressed[requestID] = struct{}{}
	}
	return request
}

func closeThreadAttentionUIResponse(request *threadAttentionRequest) {
	if request == nil || request.uiResponse == nil {
		return
	}
	responseCh := request.uiResponse
	request.uiResponse = nil
	close(responseCh)
}

func forwardThreadAttentionResponse(
	responseCh <-chan PermissionResponse,
	deliver func(PermissionResponse),
) {
	response, ok := <-responseCh
	if ok {
		deliver(response)
	}
}

func (s *threadAttentionStore) inactiveSummary(activeThreadID string) (count int, ownerLabel string) {
	if s == nil {
		return 0, ""
	}
	for _, requestID := range s.order {
		request := s.requests[requestID]
		if request == nil || request.ThreadID == activeThreadID {
			continue
		}
		count++
		if ownerLabel == "" {
			ownerLabel = request.OwnerLabel
		}
	}
	return count, ownerLabel
}

func (a *App) enqueueThreadAttention(request threadAttentionRequest) tea.Cmd {
	if a == nil || a.threadAttention == nil {
		if request.responseCh != nil {
			request.responseCh <- PermissionDeny
		}
		return nil
	}
	request.ThreadID = normalizeThreadViewID(request.ThreadID)
	if request.OwnerLabel == "" {
		request.OwnerLabel = a.threadAttentionOwnerLabel(request.ThreadID, request.AgentID)
	}
	if !a.threadAttention.upsert(request) {
		return nil
	}
	if request.ThreadID != a.activeThreadViewID() {
		a.showNotification("Input needed in "+request.OwnerLabel, NotifyWarning)
		return nil
	}
	stored, _ := a.threadAttention.get(request.ID)
	if stored != nil && stored.Source == "callback" && stored.responseCh == nil {
		return nil
	}
	return a.presentNextThreadAttention()
}

func (a *App) presentNextThreadAttention() tea.Cmd {
	if a == nil || a.threadAttention == nil {
		return nil
	}
	request, ok := a.threadAttention.nextForThread(a.activeThreadViewID())
	if !ok {
		return nil
	}
	a.threadAttention.activeID = request.ID
	uiResponse := make(chan PermissionResponse, 1)
	if a.program != nil {
		requestID := request.ID
		program := a.program
		request.uiResponse = uiResponse
		go func() {
			forwardThreadAttentionResponse(uiResponse, func(response PermissionResponse) {
				program.Send(threadAttentionAnsweredMsg{requestID: requestID, response: response})
			})
		}()
	}
	switch request.Kind {
	case threadAttentionQuestion:
		a.questionDialog.Show(request.Input, uiResponse)
		a.pushDialog(StateAskUser)
	case threadAttentionPlan:
		a.planDialog.Show(
			request.ThreadID,
			request.SessionID,
			request.AgentID,
			request.PlanApproval,
			uiResponse,
		)
		a.pushDialog(StatePlanApproval)
	case threadAttentionRepeatedTool:
		a.dialog.ShowRepeatedTool(request.Tool, request.SessionScope, request.Attempt, uiResponse)
		a.pushDialog(StatePermission)
		return permissionTimeoutTick()
	default:
		a.dialog.ShowWithConstraint(request.Tool, request.Input, request.SessionScope, request.decisionConstraint, uiResponse)
		a.pushDialog(StatePermission)
		return permissionTimeoutTick()
	}
	return nil
}

// suspendThreadAttentionPresentation detaches only the modal owned by the
// thread being left. The coordinator request remains unresolved and becomes
// presentable again when its exact owner thread is activated.
func (a *App) suspendThreadAttentionPresentation(previousThreadID, nextThreadID string) {
	if a == nil || a.threadAttention == nil ||
		normalizeThreadViewID(previousThreadID) == normalizeThreadViewID(nextThreadID) {
		return
	}
	requestID := a.threadAttention.activeID
	request, ok := a.threadAttention.get(requestID)
	if !ok || request.ThreadID != normalizeThreadViewID(previousThreadID) {
		return
	}

	a.threadAttention.activeID = ""
	a.dismissThreadAttentionDialogWithoutResponse(request)
	a.removeDialog(threadAttentionDialogState(request.Kind))
	closeThreadAttentionUIResponse(request)
}

func (a *App) resolveThreadAttention(requestID string, response PermissionResponse) tea.Cmd {
	if a == nil || a.threadAttention == nil {
		return nil
	}
	wasActive := a.threadAttention.activeID == requestID
	request := a.threadAttention.remove(requestID, true)
	if request == nil {
		return nil
	}
	responseData := request.responseData
	if !request.dataCaptured && wasActive {
		responseData = a.threadAttentionDialogResponseData(request)
	}
	directGraphResume := request.Source == "project_graph" &&
		request.responseCh == nil &&
		a.engine != nil
	if directGraphResume {
		result := permissionInteractionResult(
			engine.PermissionPromptRequest{
				ToolName:           request.Tool,
				ToolUseID:          request.ID,
				Message:            request.SessionScope,
				SessionScope:       request.SessionScope,
				DecisionConstraint: request.decisionConstraint,
				SessionID:          request.SessionID,
				ThreadID:           request.ThreadID,
				AgentID:            request.AgentID,
				PlanApproval:       request.PlanApproval,
			},
			response,
			responseData,
		)
		if !a.engine.ResolvePermissionInteraction(request.ID, result) {
			a.showNotification(
				"Graph permission request is no longer active",
				NotifyWarning,
			)
		}
	} else if responseData.answerJSON != "" ||
		responseData.feedback != "" ||
		request.Kind == threadAttentionPlan {
		a.threadAttentionResponses.Store(requestID, responseData)
	}
	if request.responseCh != nil {
		select {
		case request.responseCh <- response:
		default:
		}
	}
	// A submitted response from a previous owner may arrive after the new owner
	// has presented the same dialog kind. Only the still-active request owns the
	// current stack frame.
	if wasActive {
		a.removeDialog(threadAttentionDialogState(request.Kind))
	}
	next := a.presentNextThreadAttention()
	if directGraphResume {
		return tea.Batch(next, a.scheduleNextRuntimeWork())
	}
	return next
}

func (a *App) takeThreadAttentionResponse(requestID string) threadAttentionResponseData {
	if a == nil || requestID == "" {
		return threadAttentionResponseData{}
	}
	value, ok := a.threadAttentionResponses.LoadAndDelete(requestID)
	if !ok {
		return threadAttentionResponseData{}
	}
	response, _ := value.(threadAttentionResponseData)
	return response
}

func (a *App) cancelThreadAttention(requestID string) tea.Cmd {
	if a == nil || a.threadAttention == nil {
		return nil
	}
	wasActive := a.threadAttention.activeID == requestID
	activeRequest, _ := a.threadAttention.get(requestID)
	if wasActive && activeRequest != nil {
		a.threadAttention.activeID = ""
		a.dismissThreadAttentionDialogWithoutResponse(activeRequest)
		a.removeDialog(threadAttentionDialogState(activeRequest.Kind))
	}
	a.threadAttention.remove(requestID, true)
	return a.presentNextThreadAttention()
}

func (a *App) dismissThreadAttentionDialogWithoutResponse(request *threadAttentionRequest) {
	if a == nil || request == nil {
		return
	}
	switch request.Kind {
	case threadAttentionQuestion:
		a.questionDialog.dismissWithoutResponse()
	case threadAttentionPlan:
		a.planDialog.dismissWithoutResponse()
	default:
		a.dialog.dismissWithoutResponse()
	}
}

func (a *App) captureActiveThreadAttentionResponseData() {
	if a == nil || a.threadAttention == nil {
		return
	}
	request, ok := a.threadAttention.get(a.threadAttention.activeID)
	if !ok {
		return
	}
	request.responseData = a.threadAttentionDialogResponseData(request)
	request.dataCaptured = true
}

func (a *App) threadAttentionDialogResponseData(
	request *threadAttentionRequest,
) threadAttentionResponseData {
	data := threadAttentionResponseData{}
	if a == nil || request == nil {
		return data
	}
	switch request.Kind {
	case threadAttentionQuestion:
		data.answerJSON = a.questionDialog.AnswerJSON()
	case threadAttentionPlan:
		data.planResult = a.planDialog.PlanResult()
		if data.planResult != nil {
			data.feedback = data.planResult.Feedback
			data.planReviewedDigest = data.planResult.ReviewedPlanDigest
			data.planTarget, data.planConfirmed = data.planResult.TargetMode, data.planResult.Confirmed
		}
	}
	return data
}

func threadAttentionDialogState(kind threadAttentionKind) AppState {
	switch kind {
	case threadAttentionQuestion:
		return StateAskUser
	case threadAttentionPlan:
		return StatePlanApproval
	default:
		return StatePermission
	}
}

func (a *App) syncRuntimeThreadAttention() tea.Cmd {
	if a == nil || a.threadAttention == nil || a.threadAttentionProvider == nil {
		return nil
	}
	canonical := make(map[string]struct{})
	for _, thread := range a.threadAttentionProvider() {
		for _, interaction := range thread.Requests {
			canonical[interaction.ID] = struct{}{}
			if _, suppressed := a.threadAttention.suppressed[interaction.ID]; suppressed {
				continue
			}
			input, _ := jsonPkg.Marshal(interaction.Input)
			kind := threadAttentionPermission
			if interaction.Kind == "repeated_tool" {
				kind = threadAttentionRepeatedTool
			} else if interaction.Kind == "question" {
				kind = threadAttentionQuestion
			} else if interaction.ToolName == "ExitPlanMode" {
				kind = threadAttentionPlan
			}
			var planApproval *engine.PlanApprovalRequest
			if kind == threadAttentionPlan &&
				interaction.ID != "" &&
				interaction.PlanRevision > 0 &&
				interaction.PlanFile != "" {
				planApproval = &engine.PlanApprovalRequest{
					RequestID:         interaction.ID,
					PlanRevision:      interaction.PlanRevision,
					PlanFileIdentity:  interaction.PlanFile,
					InitialPlanDigest: interaction.PlanInitialDigest,
					ReturnMode: permission.Mode(
						interaction.PlanReturnMode,
					),
				}
			}
			a.threadAttention.upsert(threadAttentionRequest{
				ID: interaction.ID, ThreadID: thread.ThreadID, AgentID: thread.AgentID,
				OwnerLabel: a.threadAttentionOwnerLabel(thread.ThreadID, thread.AgentID),
				Kind:       kind, Tool: interaction.ToolName, Input: string(input), SessionScope: interaction.Message, Attempt: interaction.Attempt, decisionConstraint: interaction.DecisionConstraint,
				SessionID:    thread.SessionID,
				Source:       interaction.Source,
				PlanApproval: planApproval,
			})
		}
	}
	for requestID := range a.threadAttention.suppressed {
		if _, exists := canonical[requestID]; !exists {
			delete(a.threadAttention.suppressed, requestID)
		}
	}
	for requestID, request := range a.threadAttention.requests {
		if request.Source == "callback" || request.Source == "prompter" || request.Source == "runtime" {
			if _, exists := canonical[requestID]; !exists {
				if a.threadAttention.activeID == requestID {
					a.cancelThreadAttention(requestID)
				} else {
					a.threadAttention.remove(requestID, false)
				}
			}
		}
	}
	return a.presentNextThreadAttention()
}

func (a *App) threadAttentionStatus() string {
	if a == nil || a.threadAttention == nil {
		return ""
	}
	count, owner := a.threadAttention.inactiveSummary(a.activeThreadViewID())
	if count == 0 {
		return ""
	}
	if strings.TrimSpace(owner) == "" {
		owner = "another thread"
	}
	return fmt.Sprintf("attention:%d %s", count, owner)
}

func (a *App) threadAttentionOwnerLabel(threadID, agentID string) string {
	if threadID == a.leaderThreadViewID() {
		return "main"
	}
	if entry, ok := a.threadNavigationEntry(threadID); ok {
		return "@" + agentThreadLabel(entry, a.leaderThreadViewID())
	}
	if strings.TrimSpace(agentID) != "" {
		return "@" + agentID
	}
	return "@" + threadID
}
