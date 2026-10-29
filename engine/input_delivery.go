package engine

import (
	"fmt"
	"strings"
)

// PromptDeliveryStatus reports ledger ownership or transcript-confirmed delivery.
// Unknown does not imply delivery, cancellation, or successful model execution.
type PromptDeliveryStatus string

const (
	PromptDeliveryPending    PromptDeliveryStatus = "pending"
	PromptDeliveryProcessing PromptDeliveryStatus = "claimed"
	PromptDeliveryDelivered  PromptDeliveryStatus = "delivered"
	PromptDeliveryUnknown    PromptDeliveryStatus = "unknown"
)

// QueuedPromptDeliveryStatuses resolves at most one bounded queue's identities.
// It exposes no prompt/media payload and never dispatches or claims work.
func (e *QueryEngine) QueuedPromptDeliveryStatuses(ids []string) (map[string]PromptDeliveryStatus, error) {
	if len(ids) > maxQueuedUserInputs {
		return nil, fmt.Errorf("too many prompt delivery identities")
	}
	c, scope, err := e.runtimeInputOwner()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	items := make([]RuntimeItem, 0, len(c.items))
	for _, item := range c.items {
		if runtimeScopesEqual(item.Scope, scope) {
			items = append(items, RuntimeItem{ID: item.ID, Kind: item.Kind, State: item.State})
		}
	}
	c.mu.Unlock()
	states := make(map[string]PromptDeliveryStatus, len(ids))
	unsettled := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, fmt.Errorf("prompt delivery identity is required")
		}
		states[id] = PromptDeliveryUnknown
		found := false
		for _, item := range items {
			if item.ID != id || item.Kind != RuntimeItemUserPrompt {
				continue
			}
			found = true
			if item.State == RuntimeItemPending {
				states[id] = PromptDeliveryPending
			} else if item.State == RuntimeItemProcessing {
				states[id] = PromptDeliveryProcessing
			}
			break
		}
		if !found {
			unsettled = append(unsettled, id)
		}
	}
	delivered, err := c.ResolveDelivered(unsettled)
	if err != nil {
		return nil, err
	}
	for id := range delivered {
		if _, requested := states[id]; requested {
			states[id] = PromptDeliveryDelivered
		}
	}
	return states, nil
}
