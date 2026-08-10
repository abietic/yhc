package hooks

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
)

// ---------------------------------------------------------------------------
// StopReason represents why the query loop turn ended.
// Mirrors the stop reasons from src/query/stopHooks.ts.
// ---------------------------------------------------------------------------

// StopReason is a string enum describing why the agent loop turn ended.
type StopReason string

const (
	// StopReasonEndTurn indicates the model naturally finished its response.
	StopReasonEndTurn StopReason = "end_turn"
	// StopReasonMaxTurns indicates the configured turn limit was reached.
	StopReasonMaxTurns StopReason = "max_turns"
	// StopReasonInterrupt indicates the user interrupted the turn.
	StopReasonInterrupt StopReason = "interrupt"
	// StopReasonError indicates an unrecoverable error occurred.
	StopReasonError StopReason = "error"
	// StopReasonToolError indicates a tool execution error ended the turn.
	StopReasonToolError StopReason = "tool_error"
	// StopReasonStopTool indicates a tool explicitly requested the loop to stop.
	StopReasonStopTool StopReason = "stop_tool"
)

// ---------------------------------------------------------------------------
// StopHookContext carries the state available to stop hooks at turn end.
// ---------------------------------------------------------------------------

// StopHookContext provides the full context of the completed turn to the
// stop hooks handler. It contains the conversation state needed to extract
// memories, decide on continuation, and generate prompt suggestions.
type StopHookContext struct {
	// Reason is why the turn ended.
	Reason StopReason
	// Messages is the full conversation history for this session.
	Messages []*schema.Message
	// TurnCount is the number of turns completed so far in this query loop.
	TurnCount int
	// ModelName is the name of the model used for this turn.
	ModelName string
	// SessionID is the session identifier.
	SessionID string
	// FinalResponse is the content of the last assistant message.
	FinalResponse string
}

// ---------------------------------------------------------------------------
// MemoryEntry represents a notable event extracted from the turn.
// ---------------------------------------------------------------------------

// MemoryEntry represents a single piece of memory extracted from a turn's
// messages. These are persisted across the session for context preservation.
type MemoryEntry struct {
	// Content is the memory text describing what happened.
	Content string
	// Source describes where this memory was extracted from.
	Source string
	// Timestamp is when this memory was extracted.
	Timestamp time.Time
}

// ---------------------------------------------------------------------------
// RunStopHooksResult is the aggregated output of RunStopHooks.
// Named to avoid conflict with the existing StopHookResult type (which
// represents individual hook execution results).
// ---------------------------------------------------------------------------

// RunStopHooksResult aggregates the output of the full stop hooks phase:
// extracted memories, continuation decision, and prompt suggestions.
type RunStopHooksResult struct {
	// MemoryEntries contains memories extracted from the turn to persist.
	MemoryEntries []MemoryEntry
	// ShouldContinue indicates whether the loop should continue with another turn.
	ShouldContinue bool
	// ContinuationPrompt is the prompt to send if continuing.
	ContinuationPrompt string
	// PromptSuggestions are suggested follow-up prompts for the UI.
	PromptSuggestions []string
}

// ---------------------------------------------------------------------------
// RunStopHooks executes the full stop hooks phase at the end of a query turn.
// ---------------------------------------------------------------------------

// RunStopHooks performs end-of-turn processing:
//   - Extracts key memories from the final turn messages (task completions,
//     file changes, decisions made)
//   - Determines if continuation is warranted (e.g., tool errors that should retry)
//   - Generates prompt suggestions based on conversation state
//   - Returns the aggregated result
//
// This mirrors the behavior of src/query/stopHooks.ts handleStopHooks,
// focusing on the memory extraction, continuation decision, and prompt
// suggestion aspects of stop hook processing.
func RunStopHooks(ctx context.Context, hookCtx *StopHookContext) (*RunStopHooksResult, error) {
	if hookCtx == nil {
		return &RunStopHooksResult{}, nil
	}

	// Check for context cancellation early.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	result := &RunStopHooksResult{}

	// 1. Extract memories from the turn messages (pattern-based, always runs).
	result.MemoryEntries = ExtractMemoriesFromTurn(hookCtx.Messages)

	// 2. Determine if continuation is warranted.
	result.ShouldContinue, result.ContinuationPrompt = determineContinuation(hookCtx)

	// 3. Generate prompt suggestions based on conversation state.
	result.PromptSuggestions = generatePromptSuggestions(hookCtx)

	return result, nil
}

// ---------------------------------------------------------------------------
// determineContinuation decides whether the loop should auto-continue.
// ---------------------------------------------------------------------------

// determineContinuation checks if the turn ended in a way that warrants
// automatic retry or continuation. Tool errors and certain stop reasons
// indicate the model should be given another chance.
func determineContinuation(hookCtx *StopHookContext) (shouldContinue bool, prompt string) {
	switch hookCtx.Reason {
	case StopReasonToolError:
		// Tool errors should trigger a retry — the model may try a different approach.
		return true, "The previous tool call resulted in an error. Please try a different approach or fix the issue."
	case StopReasonError:
		// General errors may be transient; allow one retry.
		return true, "An error occurred during the previous turn. Please continue where you left off."
	case StopReasonEndTurn, StopReasonMaxTurns, StopReasonInterrupt, StopReasonStopTool:
		// Normal endings do not warrant auto-continuation.
		return false, ""
	default:
		return false, ""
	}
}

// ---------------------------------------------------------------------------
// generatePromptSuggestions produces follow-up prompt suggestions.
// ---------------------------------------------------------------------------

// generatePromptSuggestions analyzes the conversation state and final response
// to suggest relevant follow-up prompts for the UI.
func generatePromptSuggestions(hookCtx *StopHookContext) []string {
	if hookCtx.FinalResponse == "" {
		return nil
	}

	var suggestions []string

	// If the turn involved file modifications, suggest verification.
	if containsFileOperations(hookCtx.Messages) {
		suggestions = append(suggestions, "Run tests to verify the changes")
	}

	// If the turn ended with an error, suggest debugging.
	if hookCtx.Reason == StopReasonError || hookCtx.Reason == StopReasonToolError {
		suggestions = append(suggestions, "Explain what went wrong and suggest a fix")
	}

	// If the final response mentions remaining work, suggest continuation.
	if mentionsRemainingWork(hookCtx.FinalResponse) {
		suggestions = append(suggestions, "Continue with the remaining tasks")
	}

	// If there were test results, suggest next steps.
	if containsTestResults(hookCtx.Messages) {
		suggestions = append(suggestions, "Fix any failing tests")
	}

	// Limit suggestions to avoid clutter.
	if len(suggestions) > 4 {
		suggestions = suggestions[:4]
	}

	return suggestions
}

// ---------------------------------------------------------------------------
// ExtractMemoriesFromTurn scans messages for notable events.
// ---------------------------------------------------------------------------

// ExtractMemoriesFromTurn scans messages for notable events worth persisting
// as session memory. It detects:
//   - File writes/edits (tool calls that modified files)
//   - Test results (pass/fail patterns in tool output)
//   - Decisions made (explicit decision language)
//   - Error patterns (recurring errors that should be remembered)
func ExtractMemoriesFromTurn(messages []*schema.Message) []MemoryEntry {
	if len(messages) == 0 {
		return nil
	}

	now := time.Now()
	var entries []MemoryEntry
	filesSeen := make(map[string]bool)

	for _, msg := range messages {
		if msg == nil {
			continue
		}

		// Extract file modifications from assistant tool calls.
		for _, tc := range msg.ToolCalls {
			name := tc.Function.Name
			if isFileModification(name) {
				paths := extractFilePaths(tc.Function.Arguments)
				for _, p := range paths {
					if !filesSeen[p] {
						filesSeen[p] = true
						entries = append(entries, MemoryEntry{
							Content:   fmt.Sprintf("File modified: %s (via %s)", p, name),
							Source:    fmt.Sprintf("tool_call_%s", name),
							Timestamp: now,
						})
					}
				}
			}
		}

		// Extract test results from tool response messages.
		if msg.Role == schema.Tool && msg.Content != "" {
			if result := extractTestResult(msg.Content); result != "" {
				entries = append(entries, MemoryEntry{
					Content:   result,
					Source:    "test_result",
					Timestamp: now,
				})
			}
		}

		// Extract decisions from user messages.
		if msg.Role == schema.User && msg.Content != "" {
			if decision := extractDecision(msg.Content); decision != "" {
				entries = append(entries, MemoryEntry{
					Content:   decision,
					Source:    "user_decision",
					Timestamp: now,
				})
			}
		}

		// Extract error patterns from tool results.
		if msg.Role == schema.Tool && msg.Content != "" {
			if errPattern := extractErrorPattern(msg.Content); errPattern != "" {
				entries = append(entries, MemoryEntry{
					Content:   errPattern,
					Source:    "error_pattern",
					Timestamp: now,
				})
			}
		}
	}

	return entries
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

// isFileModification returns true if the tool name modifies files.
func isFileModification(name string) bool {
	switch strings.ToLower(name) {
	case "write", "edit", "multiwrite", "notebook_edit":
		return true
	}
	return false
}

// filePathArgPattern matches file_path arguments in tool call JSON.
var filePathArgPattern = regexp.MustCompile(`"(?:file_path|path|filename)":\s*"([^"]+)"`)

// extractFilePaths extracts file paths from tool call arguments.
func extractFilePaths(args string) []string {
	matches := filePathArgPattern.FindAllStringSubmatch(args, -1)
	var paths []string
	for _, match := range matches {
		if len(match) > 1 && match[1] != "" {
			paths = append(paths, match[1])
		}
	}
	return paths
}

// testResultPattern matches common test result summaries.

// testPassPattern matches passing test output.
var testPassPattern = regexp.MustCompile(`(?m)(?:PASS|ok\s+\S+|(\d+)\s+passed)`)

// testFailPattern matches failing test output.
var testFailPattern = regexp.MustCompile(`(?m)(?:FAIL|(\d+)\s+failed|---\s+FAIL)`)

// extractTestResult extracts a summary of test results from tool output.
func extractTestResult(content string) string {
	if len(content) < 10 {
		return ""
	}

	hasFail := testFailPattern.MatchString(content)
	hasPass := testPassPattern.MatchString(content)

	if hasFail {
		// Try to extract the first FAIL line for context.
		lines := strings.Split(content, "\n")
		for _, line := range lines {
			if testFailPattern.MatchString(line) {
				trimmed := strings.TrimSpace(line)
				if len(trimmed) > 150 {
					trimmed = trimmed[:150] + "..."
				}
				return fmt.Sprintf("Test failure: %s", trimmed)
			}
		}
		return "Tests failed"
	}

	if hasPass {
		return "Tests passed"
	}

	return ""
}

// decisionPatterns matches explicit decision statements.
var stopHookDecisionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:i\s+)?decided?\b`),
	regexp.MustCompile(`(?i)\blet'?s\s+(?:go\s+with|use|do)\b`),
	regexp.MustCompile(`(?i)\bwe(?:'ll|\s+will)\s+`),
	regexp.MustCompile(`(?i)\bplease\s+(?:use|always|never)\b`),
}

// extractDecision extracts a decision statement from user message content.
func extractDecision(content string) string {
	for _, pat := range stopHookDecisionPatterns {
		if loc := pat.FindStringIndex(content); loc != nil {
			// Extract the sentence around the match.
			sentence := extractSentenceAround(content, loc[0], loc[1])
			if sentence != "" {
				return fmt.Sprintf("Decision: %s", sentence)
			}
		}
	}
	return ""
}

// errorPatternRe matches common error patterns worth remembering.
var errorPatternRe = regexp.MustCompile(`(?m)(?:error|Error|ERROR)[\s:]+(.{10,80})`)

// extractErrorPattern extracts a notable error pattern from content.
func extractErrorPattern(content string) string {
	matches := errorPatternRe.FindStringSubmatch(content)
	if len(matches) > 1 {
		errMsg := strings.TrimSpace(matches[1])
		if len(errMsg) > 100 {
			errMsg = errMsg[:100] + "..."
		}
		return fmt.Sprintf("Error encountered: %s", errMsg)
	}
	return ""
}

// extractSentenceAround extracts a sentence around the given position.
func extractSentenceAround(text string, matchStart, matchEnd int) string {
	start := matchStart
	end := matchEnd

	// Walk back to sentence start.
	for start > 0 && text[start-1] != '.' && text[start-1] != '\n' && text[start-1] != '!' && text[start-1] != '?' {
		start--
	}

	// Walk forward to sentence end.
	for end < len(text) && text[end] != '.' && text[end] != '\n' && text[end] != '!' && text[end] != '?' {
		end++
	}
	if end < len(text) && (text[end] == '.' || text[end] == '!' || text[end] == '?') {
		end++
	}

	sentence := strings.TrimSpace(text[start:end])
	if len(sentence) > 200 {
		sentence = sentence[:200] + "..."
	}
	return sentence
}

// containsFileOperations checks if any messages contain file-modification tool calls.
func containsFileOperations(messages []*schema.Message) bool {
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if isFileModification(tc.Function.Name) {
				return true
			}
		}
	}
	return false
}

// remainingWorkPatterns matches phrases indicating incomplete work.
var remainingWorkPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:still\s+need|remaining|todo|left\s+to\s+do)\b`),
	regexp.MustCompile(`(?i)\bnext\s+(?:step|steps|thing)\b`),
	regexp.MustCompile(`(?i)\bafter\s+that\b`),
	regexp.MustCompile(`(?i)\badditionally\b`),
}

// mentionsRemainingWork checks if the final response mentions remaining work.
func mentionsRemainingWork(response string) bool {
	for _, pat := range remainingWorkPatterns {
		if pat.MatchString(response) {
			return true
		}
	}
	return false
}

// containsTestResults checks if any messages contain test execution results.
func containsTestResults(messages []*schema.Message) bool {
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if msg.Role == schema.Tool && msg.Content != "" {
			if testPassPattern.MatchString(msg.Content) || testFailPattern.MatchString(msg.Content) {
				return true
			}
		}
	}
	return false
}
