// Package bridge provides a centralized state observation layer that enables
// TUI, ACP, and IDE clients to observe engine state changes reactively.
//
// It mirrors the state management patterns found in the reference implementation's
// src/state/store.ts and src/bridge/ modules, adapted for Go's concurrency model
// using channels and mutexes rather than callback listeners.
package bridge

// Topic identifies a category of state that observers can subscribe to.
// Observers only receive notifications for topics they have subscribed to.
type Topic string

const (
	// TopicConversation covers conversation status changes (idle, thinking, tool-running).
	TopicConversation Topic = "conversation"

	// TopicModel covers model selection and fallback changes.
	TopicModel Topic = "model"

	// TopicSession covers session lifecycle (start, end, resume).
	TopicSession Topic = "session"

	// TopicTokens covers token usage updates (input/output counts).
	TopicTokens Topic = "tokens"

	// TopicTools covers active tool state (tool starts, completions).
	TopicTools Topic = "tools"

	// TopicPermission covers permission state changes (mode, grants, denials).
	TopicPermission Topic = "permission"

	// TopicClients covers client registry changes (connect, disconnect).
	TopicClients Topic = "clients"

	// TopicAgents covers sub-agent lifecycle (register, status change, unregister).
	TopicAgents Topic = "agents"

	// TopicAll is a wildcard topic that matches all state changes.
	TopicAll Topic = "*"
)

// AllTopics returns the list of all concrete (non-wildcard) topics.
func AllTopics() []Topic {
	return []Topic{
		TopicConversation,
		TopicModel,
		TopicSession,
		TopicTokens,
		TopicTools,
		TopicPermission,
		TopicClients,
		TopicAgents,
	}
}
