package permission

import (
	"context"
	"time"
)

// InteractiveHandler orchestrates multiple concurrent resolution sources for
// a permission prompt: user (via PermissionPrompter), speculative classifier,
// and hooks. The first to claim via ResolveOnce wins.
// Mirrors interactiveHandler.ts multi-racer pattern.
type InteractiveHandler struct {
	Prompter    *PermissionPrompter
	Speculative *SpeculativeClassifier
	// GraceDelay is the minimum time before the classifier can auto-approve,
	// preventing UI flicker when the user is about to interact.
	// Default: 200ms. Mirrors interactiveHandler.ts userInteracted grace.
	GraceDelay time.Duration
}

// ApprovalSource tracks which racer won the permission resolution.
type ApprovalSource string

const (
	ApprovalSourceUser       ApprovalSource = "user"
	ApprovalSourceClassifier ApprovalSource = "classifier"
	ApprovalSourceHook       ApprovalSource = "hook"
)

// InteractiveResult wraps a PermissionResult with metadata about how it was resolved.
type InteractiveResult struct {
	PermissionResult
	Source ApprovalSource
}

// HandlePermission runs the multi-racer interactive permission flow:
//  1. Sends the permission request to the prompter (UI shows dialog)
//  2. Starts the speculative classifier consumption (with grace delay)
//  3. First to resolve via ResolveOnce wins
//
// If specCmd is non-empty and a speculative check was started for it,
// the classifier races against the user's response.
// Mirrors interactiveHandler.ts handleInteractivePermission.
func (h *InteractiveHandler) HandlePermission(ctx context.Context, toolName, toolUseID string, input map[string]any, message, specCmd string) InteractiveResult {
	if h == nil || h.Prompter == nil {
		return InteractiveResult{
			PermissionResult: PermissionResult{
				Decision: DecisionDeny,
				Reason:   ReasonPermissionPrompt,
				Message:  "no permission handler configured",
				ToolName: toolName,
			},
			Source: ApprovalSourceUser,
		}
	}

	graceDelay := h.GraceDelay
	if graceDelay <= 0 {
		graceDelay = 200 * time.Millisecond
	}

	// Create the permission request with response channel.
	req := &PermissionRequest{
		ToolName:  toolName,
		ToolUseID: toolUseID,
		Input:     input,
		Message:   message,
		Response:  make(chan PermissionResult, 1),
	}

	// Register with prompter so the UI can show the dialog.
	h.Prompter.mu.Lock()
	h.Prompter.pending[toolUseID] = req
	h.Prompter.mu.Unlock()

	if h.Prompter.onChange != nil {
		h.Prompter.onChange(req)
	}

	// Create ResolveOnce guard for atomic resolution.
	resolver := NewResolveOnce(req.Response)

	// Start speculative classifier racer (if available).
	var classifierCh <-chan ClassifierDecision
	if specCmd != "" && h.Speculative != nil {
		classifierCh = h.Speculative.Consume(specCmd)
	}

	if classifierCh != nil {
		go h.classifierRacer(ctx, classifierCh, resolver, toolName, graceDelay)
	}

	// Block until resolved (by user, classifier, or context cancellation).
	select {
	case result := <-req.Response:
		h.Prompter.mu.Lock()
		delete(h.Prompter.pending, toolUseID)
		h.Prompter.mu.Unlock()

		var source ApprovalSource
		switch result.Reason {
		case ReasonClassifier:
			source = ApprovalSourceClassifier
		case ReasonHook:
			source = ApprovalSourceHook
		default:
			source = ApprovalSourceUser
		}
		return InteractiveResult{
			PermissionResult: result,
			Source:           source,
		}

	case <-ctx.Done():
		h.Prompter.mu.Lock()
		delete(h.Prompter.pending, toolUseID)
		h.Prompter.mu.Unlock()

		return InteractiveResult{
			PermissionResult: PermissionResult{
				Decision: DecisionDeny,
				Reason:   ReasonPermissionPrompt,
				Message:  "permission request cancelled",
				ToolName: toolName,
			},
			Source: ApprovalSourceUser,
		}
	}
}

// classifierRacer waits for the speculative classifier result and attempts to
// resolve the permission if the classifier allows. Respects the grace delay
// to avoid racing with the user during initial dialog display.
func (h *InteractiveHandler) classifierRacer(ctx context.Context, ch <-chan ClassifierDecision, resolver *ResolveOnce, toolName string, graceDelay time.Duration) {
	// Wait for grace period — don't auto-approve while UI is still rendering.
	select {
	case <-time.After(graceDelay):
	case <-ctx.Done():
		return
	}

	// Wait for classifier result.
	select {
	case decision := <-ch:
		if decision == ClassifierAllow {
			resolver.Resolve(PermissionResult{
				Decision: DecisionAllow,
				Reason:   ReasonClassifier,
				Message:  "speculative classifier approved",
				ToolName: toolName,
			})
		}
		// On deny/ask, don't resolve — let the user decide.
	case <-ctx.Done():
		return
	}
}
