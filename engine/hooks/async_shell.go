package hooks

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/abietic/yhc/engine/containment"
	"github.com/cloudwego/eino/schema"
)

const maxAsyncHookOutputBytes = 64 << 10

// AsyncShellHookCompletion is the immutable completion record for one
// executor-owned asynchronous shell hook.
type AsyncShellHookCompletion struct {
	ID                    string
	TurnID                string
	Event                 string
	ToolName              string
	HookName              string
	StatusMessage         string
	AsyncRewake           bool
	Phase                 string
	Result                ShellHookResult
	StartedAt             time.Time
	CompletedAt           time.Time
	ExecutionPolicyDigest string
}

// Outcome returns the presentation-level terminal state for the hook.
func (c AsyncShellHookCompletion) Outcome() string {
	switch {
	case c.Phase == "running":
		return "running"
	case c.Result.Cancelled:
		return "cancelled"
	case c.Result.TimedOut:
		return "timed_out"
	case c.Result.StartFailed || c.Result.ExitCode != 0:
		return "failed"
	default:
		return "completed"
	}
}

// ModelMessage projects a completed result into a hidden model-visible
// attachment. Empty successful hooks remain presentation-only.
func (c AsyncShellHookCompletion) ModelMessage() *schema.Message {
	stdout := strings.TrimSpace(c.Result.Stdout)
	stderr := strings.TrimSpace(c.Result.Stderr)
	if stdout == "" && stderr == "" && c.Outcome() == "completed" {
		return nil
	}
	if stdout == "" && stderr == "" {
		stderr = c.Outcome()
	}
	content := fmt.Sprintf(
		"<async-hook-response>\n<hook-id>%s</hook-id>\n<event>%s</event>\n<command>%s</command>\n<outcome>%s</outcome>\n<exit-code>%d</exit-code>\n<stdout>%s</stdout>\n<stderr>%s</stderr>\n</async-hook-response>",
		escapeHookXML(c.ID), escapeHookXML(c.Event), escapeHookXML(c.HookName), escapeHookXML(c.Outcome()), c.Result.ExitCode, escapeHookXML(stdout), escapeHookXML(stderr),
	)
	return &schema.Message{
		Role:    schema.User,
		Content: content,
		Extra: map[string]any{
			"is_meta":         true,
			"attachment_kind": "async_hook_response",
			"hook_id":         c.ID,
			"hook_event":      c.Event,
			"hook_name":       c.HookName,
			"tool_name":       c.ToolName,
			"hook_outcome":    c.Outcome(),
			"hook_exit_code":  c.Result.ExitCode,
			"async_rewake":    c.AsyncRewake,
		},
	}
}

func escapeHookXML(value string) string {
	var escaped strings.Builder
	_ = xml.EscapeText(&escaped, []byte(value))
	return escaped.String()
}

type asyncShellRuntime struct {
	registry *AsyncRegistry
	nextID   atomic.Uint64

	mu          sync.Mutex
	deliverable []AsyncShellHookCompletion
	handler     func(AsyncShellHookCompletion)
}

func newAsyncShellRuntime() *asyncShellRuntime {
	return &asyncShellRuntime{registry: NewAsyncRegistry()}
}

func (r *asyncShellRuntime) setHandler(handler func(AsyncShellHookCompletion)) {
	r.mu.Lock()
	r.handler = handler
	r.mu.Unlock()
}

func (r *asyncShellRuntime) dispatch(ctx context.Context, turnID, event, toolName string, hook ShellHook, env map[string]string) {
	if r == nil || r.registry == nil {
		return
	}
	id := fmt.Sprintf("async_hook_%d", r.nextID.Add(1))
	startedAt := time.Now().UTC()
	policy, _ := containment.FromContext(ctx)
	binding := executionBinding(ctx)
	policyMismatch := executionPolicyMismatch(ctx)
	policyDigest := ""
	if policy != nil {
		policyDigest = policy.Digest()
	}
	r.notify(AsyncShellHookCompletion{
		ID: id, TurnID: turnID, Event: event, ToolName: toolName,
		HookName: hook.Command, StatusMessage: hook.StatusMessage,
		AsyncRewake: hook.AsyncRewake, Phase: "running", StartedAt: startedAt,
		ExecutionPolicyDigest: policyDigest,
	})
	_, err := r.registry.ExecuteAsyncTransient(context.Background(), event, func(ctx context.Context) (any, error) {
		ctx = containment.WithSnapshot(ctx, policy)
		ctx = withExecutionBinding(ctx, binding)
		if policyMismatch {
			ctx = withExecutionPolicyMismatch(ctx)
		}
		result, executeErr := ExecuteShellHook(ctx, &hook, env)
		if result == nil {
			result = failedShellHookResult(&hook, executeErr)
		}
		result.Stdout = boundAsyncHookOutput(result.Stdout)
		result.Stderr = boundAsyncHookOutput(result.Stderr)
		completion := AsyncShellHookCompletion{
			ID:                    id,
			TurnID:                turnID,
			Event:                 event,
			ToolName:              toolName,
			HookName:              hook.Command,
			StatusMessage:         hook.StatusMessage,
			AsyncRewake:           hook.AsyncRewake,
			Phase:                 "completed",
			Result:                *result,
			StartedAt:             startedAt,
			CompletedAt:           time.Now().UTC(),
			ExecutionPolicyDigest: policyDigest,
		}
		r.record(completion)
		return result, executeErr
	})
	if err != nil {
		return
	}
}

func boundAsyncHookOutput(value string) string {
	if len(value) <= maxAsyncHookOutputBytes {
		return value
	}
	end := maxAsyncHookOutputBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end] + "\n...[truncated]"
}

func (r *asyncShellRuntime) record(completion AsyncShellHookCompletion) {
	r.mu.Lock()
	if (!completion.AsyncRewake && completion.ModelMessage() != nil) ||
		(completion.AsyncRewake && completion.Result.ExitCode == 2) {
		r.deliverable = append(r.deliverable, completion)
	}
	r.mu.Unlock()
	r.notify(completion)
}

func (r *asyncShellRuntime) notify(completion AsyncShellHookCompletion) {
	r.mu.Lock()
	handler := r.handler
	r.mu.Unlock()
	if handler != nil {
		handler(completion)
	}
}

func (r *asyncShellRuntime) drainModelMessages() []*schema.Message {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	completions := make([]AsyncShellHookCompletion, 0, len(r.deliverable))
	remaining := make([]AsyncShellHookCompletion, 0, len(r.deliverable))
	for _, completion := range r.deliverable {
		if completion.AsyncRewake && completion.Result.ExitCode == 2 {
			remaining = append(remaining, completion)
			continue
		}
		completions = append(completions, completion)
	}
	r.deliverable = remaining
	r.mu.Unlock()
	messages := make([]*schema.Message, 0, len(completions))
	for _, completion := range completions {
		if message := completion.ModelMessage(); message != nil {
			messages = append(messages, message)
		}
	}
	return messages
}

func (r *asyncShellRuntime) acknowledgeDeliverable(id string) bool {
	if r == nil || strings.TrimSpace(id) == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for index, completion := range r.deliverable {
		if completion.ID != id {
			continue
		}
		r.deliverable = append(r.deliverable[:index], r.deliverable[index+1:]...)
		return true
	}
	return false
}

func (r *asyncShellRuntime) cancelAll() {
	if r != nil && r.registry != nil {
		r.registry.CancelAll()
	}
}

func (r *asyncShellRuntime) shutdown(ctx context.Context) error {
	if r == nil || r.registry == nil {
		return nil
	}
	return r.registry.Shutdown(ctx)
}
