package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/abietic/yhc/engine"
)

type taskExplorerExecutionDetailIdentity struct {
	selection taskExplorerSelection
	sessionID string
	threadID  string
}

func (i taskExplorerExecutionDetailIdentity) valid() bool {
	return i.selection.agentID != "" && i.selection.generation > 0 &&
		strings.TrimSpace(i.sessionID) != "" && strings.TrimSpace(i.threadID) != ""
}

type taskExplorerExecutionDetailRequestMsg struct {
	selection         taskExplorerSelection
	sessionID         string
	threadID          string
	requestGeneration uint64
	tab               taskExplorerDetailTab
	cursor            string
}

type taskExplorerExecutionDetailLoadedMsg struct {
	request taskExplorerExecutionDetailRequestMsg
	detail  engine.AgentExecutionDetail
	found   bool
	err     error
}

type taskExplorerExecutionDetailProvider func(engine.AgentExecutionDetailRequest) (engine.AgentExecutionDetail, bool, error)

type taskExplorerExecutionDetailTabState struct {
	loading           bool
	initialized       bool
	unavailable       bool
	requestGeneration uint64
	cursor            string
	detail            engine.AgentExecutionDetail
}

// taskExplorerExecutionDetailState retains only the exact execution detail
// requested by the currently selected row. It owns correlation and cache
// state; rendering observes this value and never invokes a provider.
type taskExplorerExecutionDetailState struct {
	identity          taskExplorerExecutionDetailIdentity
	requestGeneration uint64
	tabs              map[taskExplorerDetailTab]taskExplorerExecutionDetailTabState
}

func (s *taskExplorerExecutionDetailState) reset() {
	s.requestGeneration++
	s.identity = taskExplorerExecutionDetailIdentity{}
	s.tabs = nil
}

func (s *taskExplorerExecutionDetailState) bind(identity taskExplorerExecutionDetailIdentity) bool {
	if s.identity == identity {
		return false
	}
	s.requestGeneration++
	s.identity = identity
	s.tabs = nil
	return true
}

func (s *taskExplorerExecutionDetailState) begin(tab taskExplorerDetailTab, force bool) (taskExplorerExecutionDetailRequestMsg, bool) {
	if !s.identity.valid() || !taskExplorerExecutionDetailTabSupported(tab) {
		return taskExplorerExecutionDetailRequestMsg{}, false
	}
	state := s.tabState(tab)
	if !force && (state.loading || state.initialized) {
		return taskExplorerExecutionDetailRequestMsg{}, false
	}
	s.requestGeneration++
	state = taskExplorerExecutionDetailTabState{
		loading:           true,
		requestGeneration: s.requestGeneration,
	}
	if s.tabs == nil {
		s.tabs = make(map[taskExplorerDetailTab]taskExplorerExecutionDetailTabState, 2)
	}
	s.tabs[tab] = state
	return taskExplorerExecutionDetailRequestMsg{
		selection:         s.identity.selection,
		sessionID:         s.identity.sessionID,
		threadID:          s.identity.threadID,
		requestGeneration: s.requestGeneration,
		tab:               tab,
	}, true
}

func (s *taskExplorerExecutionDetailState) invalidate(tab taskExplorerDetailTab) {
	if !taskExplorerExecutionDetailTabSupported(tab) || s.tabs == nil {
		return
	}
	state, ok := s.tabs[tab]
	if !ok || !state.loading {
		return
	}
	s.requestGeneration++
	delete(s.tabs, tab)
}

func (s *taskExplorerExecutionDetailState) apply(msg taskExplorerExecutionDetailLoadedMsg) bool {
	request := msg.request
	if !taskExplorerExecutionDetailTabSupported(request.tab) ||
		request.selection != s.identity.selection ||
		request.sessionID != s.identity.sessionID ||
		request.threadID != s.identity.threadID {
		return false
	}
	state, ok := s.tabs[request.tab]
	if !ok || !state.loading || state.requestGeneration != request.requestGeneration ||
		state.cursor != request.cursor {
		return false
	}
	state.loading = false
	state.initialized = true
	state.unavailable = msg.err != nil || !msg.found ||
		!taskExplorerExecutionDetailMatches(request, msg.detail)
	if state.unavailable {
		state.detail = engine.AgentExecutionDetail{}
	} else {
		state.detail = cloneTaskExplorerExecutionDetail(msg.detail)
	}
	s.tabs[request.tab] = state
	return true
}

func (s *taskExplorerExecutionDetailState) tabState(tab taskExplorerDetailTab) taskExplorerExecutionDetailTabState {
	if s.tabs == nil {
		return taskExplorerExecutionDetailTabState{}
	}
	return s.tabs[tab]
}

func taskExplorerExecutionDetailTabSupported(tab taskExplorerDetailTab) bool {
	return tab == taskExplorerDetailOutput || tab == taskExplorerDetailLineage
}

func taskExplorerExecutionDetailMatches(request taskExplorerExecutionDetailRequestMsg, detail engine.AgentExecutionDetail) bool {
	agent := detail.Agent
	return agent.AgentID == request.selection.agentID &&
		agent.Generation == request.selection.generation &&
		agent.SessionID == request.sessionID &&
		agent.ThreadID == request.threadID
}

func cloneTaskExplorerExecutionDetail(detail engine.AgentExecutionDetail) engine.AgentExecutionDetail {
	detail.Agent.Progress.RecentActivities = append(
		[]engine.RuntimeAgentActivitySnapshot(nil),
		detail.Agent.Progress.RecentActivities...,
	)
	return detail
}

func taskExplorerExecutionDetailCmd(provider taskExplorerExecutionDetailProvider, request taskExplorerExecutionDetailRequestMsg) tea.Cmd {
	if !taskExplorerExecutionDetailTabSupported(request.tab) ||
		!(taskExplorerExecutionDetailIdentity{
			selection: request.selection,
			sessionID: request.sessionID,
			threadID:  request.threadID,
		}.valid()) {
		return nil
	}
	return func() tea.Msg {
		if provider == nil {
			return taskExplorerExecutionDetailLoadedMsg{request: request}
		}
		detail, found, err := provider(engine.AgentExecutionDetailRequest{
			AgentID:       request.selection.agentID,
			Generation:    request.selection.generation,
			SessionID:     request.sessionID,
			ThreadID:      request.threadID,
			IncludeOutput: request.tab == taskExplorerDetailOutput,
		})
		return taskExplorerExecutionDetailLoadedMsg{
			request: request,
			detail:  detail,
			found:   found,
			err:     err,
		}
	}
}
