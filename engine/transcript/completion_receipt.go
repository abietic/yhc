package transcript

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/abietic/yhc/engine/internal/promptrecord"
	"github.com/cloudwego/eino/schema"
)

const (
	// AgentCompletionReceiptVersion is the current parent-owned receipt codec.
	AgentCompletionReceiptVersion = 1

	// maxLoadedAgentCompletionReceipts bounds the replay projection. The
	// append-only transcript remains the audit authority; active transcript
	// messages independently retain runtime-item identities after eviction.
	maxLoadedAgentCompletionReceipts = 256

	agentCompletionReceiptExtraKey = "agent_completion_receipt"
)

// AgentCompletionReceipt proves that one durable child terminal was projected
// into its exact parent transcript. Transport may redeliver the same
// CompletionID, but model and runtime projections collapse it by this identity.
type AgentCompletionReceipt struct {
	Version          int       `json:"version"`
	CompletionID     string    `json:"completion_id"`
	AgentID          string    `json:"agent_id"`
	Generation       int64     `json:"generation"`
	TerminalStatus   string    `json:"terminal_status"`
	TerminalSequence uint64    `json:"terminal_sequence"`
	ParentSessionID  string    `json:"parent_session_id"`
	ParentThreadID   string    `json:"parent_thread_id"`
	ParentAgentID    string    `json:"parent_agent_id,omitempty"`
	ParentToolUseID  string    `json:"parent_tool_use_id,omitempty"`
	DeliveredAt      time.Time `json:"delivered_at"`
}

// AgentCompletionReceiptExtraKey is the schema.Message Extra key persisted by
// the parent transcript/checkpoint owner.
func AgentCompletionReceiptExtraKey() string {
	return agentCompletionReceiptExtraKey
}

// AgentCompletionReceiptFromMessage decodes both an in-process typed receipt
// and its JSON-round-tripped map representation. Unknown versions remain
// visible to callers so recovery can fail closed for their CompletionID.
func AgentCompletionReceiptFromMessage(
	message *schema.Message,
) (AgentCompletionReceipt, bool) {
	if message == nil || message.Extra == nil {
		return AgentCompletionReceipt{}, false
	}
	raw, ok := message.Extra[agentCompletionReceiptExtraKey]
	if !ok || raw == nil {
		return AgentCompletionReceipt{}, false
	}
	var receipt AgentCompletionReceipt
	switch value := raw.(type) {
	case AgentCompletionReceipt:
		receipt = value
	case *AgentCompletionReceipt:
		if value == nil {
			return AgentCompletionReceipt{}, false
		}
		receipt = *value
	default:
		data, err := json.Marshal(value)
		if err != nil || json.Unmarshal(data, &receipt) != nil {
			return AgentCompletionReceipt{}, false
		}
	}
	receipt.CompletionID = strings.TrimSpace(receipt.CompletionID)
	if receipt.CompletionID == "" {
		return AgentCompletionReceipt{}, false
	}
	return receipt, true
}

func appendLoadedAgentCompletionReceipt(
	receipts []AgentCompletionReceipt,
	receipt AgentCompletionReceipt,
) []AgentCompletionReceipt {
	for index := range receipts {
		if receipts[index].CompletionID != receipt.CompletionID {
			continue
		}
		copy(receipts[index:], receipts[index+1:])
		receipts = receipts[:len(receipts)-1]
		break
	}
	receipts = append(receipts, receipt)
	if overflow := len(receipts) - maxLoadedAgentCompletionReceipts; overflow > 0 {
		receipts = append(
			make([]AgentCompletionReceipt, 0, maxLoadedAgentCompletionReceipts),
			receipts[overflow:]...,
		)
	}
	return receipts
}

// RuntimeItemDeliveryCoverage scans the parent transcript audit for only the
// requested identities. This keeps replay memory bounded by current
// candidates while preserving every historical receipt as a correctness
// authority across compact boundaries.
func (r *Recorder) RuntimeItemDeliveryCoverage(
	ids []string,
) (map[string]struct{}, error) {
	targets := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			targets[id] = struct{}{}
		}
	}
	covered := make(map[string]struct{})
	if r == nil || len(targets) == 0 || r.Path() == "" {
		return covered, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	file, err := os.Open(r.Path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return covered, nil
		}
		return nil, err
	}
	defer file.Close() //nolint:errcheck

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var entry recordEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, fmt.Errorf(
				"scan runtime-item delivery coverage at line %d: %w",
				line,
				err,
			)
		}
		if err := collectPromptDeliveryCoverage(
			entry,
			targets,
			covered,
		); err != nil {
			return nil, fmt.Errorf(
				"scan runtime-item delivery coverage at line %d: %w",
				line,
				err,
			)
		}
		collectMessageDeliveryCoverage(entry.Message, targets, covered)
		for _, message := range entry.Messages {
			collectMessageDeliveryCoverage(message, targets, covered)
		}
		if len(covered) == len(targets) {
			return covered, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan runtime-item delivery coverage: %w", err)
	}
	return covered, nil
}

func collectPromptDeliveryCoverage(
	entry recordEntry,
	targets map[string]struct{},
	covered map[string]struct{},
) error {
	if entry.Kind == promptrecord.Kind {
		if entry.UserPrompt == nil ||
			entry.Message != nil ||
			len(entry.Messages) != 0 ||
			len(entry.PromptMessages) != 0 ||
			entry.RuntimeItemID != "" &&
				!validRuntimeItemDeliveryID(entry.RuntimeItemID) {
			return errors.New("invalid durable prompt delivery envelope")
		}
		if _, wanted := targets[entry.RuntimeItemID]; wanted {
			covered[entry.RuntimeItemID] = struct{}{}
		}
		return nil
	}
	if entry.UserPrompt != nil || entry.RuntimeItemID != "" {
		return errors.New("unexpected durable prompt delivery payload")
	}
	if len(entry.PromptMessages) == 0 {
		return nil
	}
	if !isLifecycleBoundaryKind(LifecycleBoundaryKind(entry.Kind)) {
		return errors.New("unexpected durable prompt delivery snapshot")
	}
	seen := make(map[int]struct{}, len(entry.PromptMessages))
	for _, prompt := range entry.PromptMessages {
		if prompt.Index < 0 ||
			prompt.Index >= len(entry.Messages) ||
			entry.Messages[prompt.Index] != nil {
			return errors.New("invalid durable prompt delivery snapshot index")
		}
		if _, exists := seen[prompt.Index]; exists {
			return errors.New("duplicate durable prompt delivery snapshot index")
		}
		seen[prompt.Index] = struct{}{}
		if _, wanted := targets[prompt.RuntimeItemID]; wanted {
			covered[prompt.RuntimeItemID] = struct{}{}
		}
	}
	return nil
}

func collectMessageDeliveryCoverage(
	message *schema.Message,
	targets map[string]struct{},
	covered map[string]struct{},
) {
	if message == nil {
		return
	}
	if receipt, ok := AgentCompletionReceiptFromMessage(message); ok {
		if _, wanted := targets[receipt.CompletionID]; wanted {
			covered[receipt.CompletionID] = struct{}{}
		}
	}
	if message.Extra == nil {
		return
	}
	for _, key := range []string{"runtime_item_id", "command_uuid"} {
		id, _ := message.Extra[key].(string)
		id = strings.TrimSpace(id)
		if _, wanted := targets[id]; wanted {
			covered[id] = struct{}{}
		}
	}
}
