package services

import (
	"context"
	"strings"
	"sync"
	"time"
)

const summaryInterval = 30 * time.Second

// SummaryModelFn calls a model to generate a progress summary.
// Takes a system prompt and conversation context, returns the response text.
type SummaryModelFn func(ctx context.Context, systemPrompt string, messages []string) (string, error)

// AgentSummaryUpdate is emitted when a new summary is generated.
type AgentSummaryUpdate struct {
	TaskID  string
	AgentID string
	Summary string
}

func buildSummaryPrompt(previousSummary string) string {
	var prevLine string
	if previousSummary != "" {
		prevLine = "\nPrevious: \"" + previousSummary + "\" — say something NEW.\n"
	}

	return `Describe your most recent action in 3-5 words using present tense (-ing). Name the file or function, not the branch. Do not use tools.
` + prevLine + `
Good: "Reading runAgent.ts"
Good: "Fixing null check in validate.ts"
Good: "Running auth module tests"
Good: "Adding retry logic to fetchUser"

Bad (past tense): "Analyzed the branch diff"
Bad (too vague): "Investigating the issue"
Bad (too long): "Reviewing full branch diff and AgentTool.tsx integration"
Bad (branch name): "Analyzed adam/background-summary branch diff"`
}

// AgentSummarizer periodically generates progress summaries for running
// background agents. Fires every 30 seconds and produces a 3-5 word
// present-tense description of the agent's current activity.
//
// Reference: src/services/AgentSummary/agentSummary.ts (~180 lines)
type AgentSummarizer struct {
	mu              sync.Mutex
	taskID          string
	agentID         string
	modelFn         SummaryModelFn
	getMessages     func() []string
	onUpdate        func(AgentSummaryUpdate)
	previousSummary string
	stopped         bool
	cancel          context.CancelFunc
}

// StartAgentSummarization begins periodic background summarization.
func StartAgentSummarization(
	taskID, agentID string,
	modelFn SummaryModelFn,
	getMessages func() []string,
	onUpdate func(AgentSummaryUpdate),
) *AgentSummarizer {
	ctx, cancel := context.WithCancel(context.Background())

	s := &AgentSummarizer{
		taskID:      taskID,
		agentID:     agentID,
		modelFn:     modelFn,
		getMessages: getMessages,
		onUpdate:    onUpdate,
		cancel:      cancel,
	}

	go s.run(ctx)
	return s
}

// Stop halts the periodic summarization.
func (s *AgentSummarizer) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *AgentSummarizer) run(ctx context.Context) {
	ticker := time.NewTicker(summaryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runOnce(ctx)
		}
	}
}

func (s *AgentSummarizer) runOnce(ctx context.Context) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	prev := s.previousSummary
	s.mu.Unlock()

	messages := s.getMessages()
	if len(messages) < 3 {
		return
	}

	prompt := buildSummaryPrompt(prev)

	summaryCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	result, err := s.modelFn(summaryCtx, prompt, messages)
	if err != nil {
		return
	}

	summary := strings.TrimSpace(result)
	summary = strings.Trim(summary, "\"")
	if summary == "" {
		return
	}

	s.mu.Lock()
	s.previousSummary = summary
	s.mu.Unlock()

	if s.onUpdate != nil {
		s.onUpdate(AgentSummaryUpdate{
			TaskID:  s.taskID,
			AgentID: s.agentID,
			Summary: summary,
		})
	}
}
