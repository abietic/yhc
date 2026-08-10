package permission

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

// PermissionHandlerStrategy defines a pluggable strategy for resolving "ask"
// permission decisions. Different execution contexts (interactive, coordinator,
// swarm worker) use different strategies.
// Mirrors the handler selection pattern in useCanUseTool.tsx.
type PermissionHandlerStrategy interface {
	// HandleAsk attempts to resolve an "ask" permission decision.
	// Returns non-nil PermissionResult if resolved, nil to fall through to
	// the next strategy or interactive dialog.
	HandleAsk(ctx context.Context, req *PermissionRequest, specClassifier *SpeculativeClassifier) *PermissionResult
}

// --- Coordinator Handler ---

// CoordinatorHandler resolves permissions for background workers in coordinator mode.
// Runs hooks → classifier sequentially (blocking) before showing any dialog.
// Only interrupts the user if automated checks cannot decide.
// Mirrors coordinatorHandler.ts handleCoordinatorPermission.
type CoordinatorHandler struct {
	// ClassifierCfg for running the full classifier (not speculative).
	ClassifierCfg *ClassifierConfig
	// Messages provides recent conversation context for the classifier.
	Messages []*schema.Message
	// RunHooks executes permission hooks synchronously.
	// Returns non-nil result if a hook resolved the permission.
	RunHooks func(ctx context.Context, toolName string, input map[string]any) *PermissionResult
}

// HandleAsk runs automated checks sequentially:
//  1. Permission hooks (fast, local)
//  2. Classifier (slow, LLM inference) — consumes speculative if available
//  3. Returns allow/deny if decisive, nil if uncertain (falls through to interactive)
//
// Mirrors coordinatorHandler.ts: sequential hooks → classifier → return or null.
func (h *CoordinatorHandler) HandleAsk(ctx context.Context, req *PermissionRequest, specClassifier *SpeculativeClassifier) *PermissionResult {
	if h == nil || req == nil {
		return nil
	}

	// Step 1: Run permission hooks.
	if h.RunHooks != nil {
		if result := h.RunHooks(ctx, req.ToolName, req.Input); result != nil {
			return result
		}
	}

	// Step 2: Try speculative classifier first (already in flight).
	if specClassifier != nil && req.ToolName == "Bash" {
		command, _ := req.Input["command"].(string)
		if command != "" {
			// Consume the speculative result — wait for it without timeout
			// (coordinator blocks on automated checks).
			decision, ok := specClassifier.Peek(command, SpeculativeGracePeriod*2)
			if ok {
				switch decision {
				case ClassifierAllow:
					result := &PermissionResult{
						Decision: DecisionAllow,
						Reason:   ReasonClassifier,
						Message:  "coordinator classifier approved",
						ToolName: req.ToolName,
					}
					return result
				case ClassifierDeny:
					result := &PermissionResult{
						Decision: DecisionDeny,
						Reason:   ReasonClassifier,
						Message:  "coordinator classifier denied",
						ToolName: req.ToolName,
					}
					return result
				}
				// ClassifierAsk → fall through
			}
		}
	}

	// Step 3: Run full classifier if speculative wasn't available.
	if h.ClassifierCfg != nil && h.ClassifierCfg.ChatModel != nil {
		decision, err := ClassifyToolUse(ctx, h.ClassifierCfg, req.ToolName, req.Input, h.Messages)
		if err == nil {
			switch decision {
			case ClassifierAllow:
				result := &PermissionResult{
					Decision: DecisionAllow,
					Reason:   ReasonClassifier,
					Message:  "coordinator classifier approved",
					ToolName: req.ToolName,
				}
				return result
			case ClassifierDeny:
				result := &PermissionResult{
					Decision: DecisionDeny,
					Reason:   ReasonClassifier,
					Message:  "coordinator classifier denied",
					ToolName: req.ToolName,
				}
				return result
			}
		}
		// ClassifierAsk or error → fall through to interactive
	}

	// Automated checks inconclusive — caller should fall through to interactive dialog.
	return nil
}

// --- Swarm Worker Handler ---

// PermissionMailbox is the interface for communicating permission requests
// between a swarm worker and its leader. The worker sends a request and
// waits for the leader to respond.
// Mirrors swarmWorkerHandler.ts mailbox delegation pattern.
type PermissionMailbox interface {
	// Send forwards a permission request to the leader.
	Send(req *PermissionRequest) error
	// Receive returns a channel that delivers the leader's response.
	// The channel is closed if the leader cannot be reached.
	Receive(toolUseID string) <-chan PermissionResult
}

// SwarmWorkerHandler resolves permissions for swarm workers by first trying
// the classifier, then delegating to the leader via a mailbox.
// Mirrors swarmWorkerHandler.ts handleSwarmWorkerPermission.
type SwarmWorkerHandler struct {
	// ClassifierCfg for auto-approval attempts.
	ClassifierCfg *ClassifierConfig
	// Messages provides recent conversation context.
	Messages []*schema.Message
	// Mailbox for delegating to the swarm leader.
	Mailbox PermissionMailbox
}

// HandleAsk tries classifier auto-approval, then delegates to the leader.
//  1. Run classifier — if allow → return immediately
//  2. Otherwise, send to leader via mailbox and wait
//  3. Return leader's decision or nil if mailbox unavailable
//
// Mirrors swarmWorkerHandler.ts: classifier → mailbox → wait.
func (h *SwarmWorkerHandler) HandleAsk(ctx context.Context, req *PermissionRequest, specClassifier *SpeculativeClassifier) *PermissionResult {
	if h == nil || req == nil {
		return nil
	}

	// Step 1: Try speculative classifier.
	if specClassifier != nil && req.ToolName == "Bash" {
		command, _ := req.Input["command"].(string)
		if command != "" {
			decision, ok := specClassifier.Peek(command, SpeculativeGracePeriod)
			if ok && decision == ClassifierAllow {
				result := &PermissionResult{
					Decision: DecisionAllow,
					Reason:   ReasonClassifier,
					Message:  "swarm worker classifier approved",
					ToolName: req.ToolName,
				}
				return result
			}
		}
	}

	// Step 2: Try full classifier if speculative wasn't conclusive.
	if h.ClassifierCfg != nil && h.ClassifierCfg.ChatModel != nil {
		decision, err := ClassifyToolUse(ctx, h.ClassifierCfg, req.ToolName, req.Input, h.Messages)
		if err == nil && decision == ClassifierAllow {
			result := &PermissionResult{
				Decision: DecisionAllow,
				Reason:   ReasonClassifier,
				Message:  "swarm worker classifier approved",
				ToolName: req.ToolName,
			}
			return result
		}
	}

	// Step 3: Delegate to leader via mailbox.
	if h.Mailbox == nil {
		return nil // no mailbox — fall through to local interactive
	}

	if err := h.Mailbox.Send(req); err != nil {
		return nil // mailbox send failed — fall through
	}

	// Wait for leader's response.
	responseCh := h.Mailbox.Receive(req.ToolUseID)
	if responseCh == nil {
		return nil
	}

	select {
	case result, ok := <-responseCh:
		if !ok {
			return nil // channel closed — leader unreachable
		}
		return &result
	case <-ctx.Done():
		result := &PermissionResult{
			Decision: DecisionDeny,
			Reason:   ReasonPermissionPrompt,
			Message:  "swarm worker permission request cancelled",
			ToolName: req.ToolName,
		}
		return result
	}
}
