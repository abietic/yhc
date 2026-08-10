package bridge

import "time"

// ConversationStatus represents the current state of the agent's conversation loop.
type ConversationStatus string

const (
	StatusIdle        ConversationStatus = "idle"
	StatusThinking    ConversationStatus = "thinking"
	StatusToolRunning ConversationStatus = "tool_running"
)

// StateField identifies a typed field within the state store.
type StateField string

const (
	FieldConversationStatus StateField = "conversation_status"
	FieldCurrentModel       StateField = "current_model"
	FieldFallbackModel      StateField = "fallback_model"
	FieldSessionID          StateField = "session_id"
	FieldSessionCWD         StateField = "session_cwd"
	FieldInputTokens        StateField = "input_tokens"
	FieldOutputTokens       StateField = "output_tokens"
	FieldActiveTools        StateField = "active_tools"
	FieldPermissionMode     StateField = "permission_mode"
	FieldTurnCount          StateField = "turn_count"
	FieldIsCompacting       StateField = "is_compacting"
	FieldConnectedClients   StateField = "connected_clients"
	FieldActiveAgents       StateField = "active_agents"
)

// fieldTopicMap maps each state field to its governing topic.
var fieldTopicMap = map[StateField]Topic{
	FieldConversationStatus: TopicConversation,
	FieldCurrentModel:       TopicModel,
	FieldFallbackModel:      TopicModel,
	FieldSessionID:          TopicSession,
	FieldSessionCWD:         TopicSession,
	FieldInputTokens:        TopicTokens,
	FieldOutputTokens:       TopicTokens,
	FieldActiveTools:        TopicTools,
	FieldPermissionMode:     TopicPermission,
	FieldTurnCount:          TopicConversation,
	FieldIsCompacting:       TopicConversation,
	FieldConnectedClients:   TopicClients,
	FieldActiveAgents:       TopicAgents,
}

// TopicForField returns the topic that governs a given state field.
func TopicForField(field StateField) Topic {
	if t, ok := fieldTopicMap[field]; ok {
		return t
	}
	return TopicAll
}

// StateChange represents a single field change in the state store.
type StateChange struct {
	// Field that changed.
	Field StateField

	// Topic this change belongs to.
	Topic Topic

	// OldValue before the change (nil for first set).
	OldValue any

	// NewValue after the change.
	NewValue any

	// Timestamp when the change was applied.
	Timestamp time.Time

	// Version is the monotonically increasing store version after this change.
	Version uint64
}

// StateSnapshot is an immutable point-in-time copy of the full state store.
// Modifying values in the snapshot has no effect on the store.
type StateSnapshot struct {
	// Version is the store version at the time of the snapshot.
	Version uint64

	// Timestamp when the snapshot was taken.
	Timestamp time.Time

	// Fields contains all current field values, keyed by StateField.
	Fields map[StateField]any
}

// Get returns the value of a field in the snapshot, or nil if not set.
func (s *StateSnapshot) Get(field StateField) any {
	if s.Fields == nil {
		return nil
	}
	return s.Fields[field]
}

// GetString returns the string value of a field, or empty string if not set or not a string.
func (s *StateSnapshot) GetString(field StateField) string {
	v, _ := s.Fields[field].(string)
	return v
}

// GetInt returns the int value of a field, or 0 if not set or not an int.
func (s *StateSnapshot) GetInt(field StateField) int {
	v, _ := s.Fields[field].(int)
	return v
}

// GetBool returns the bool value of a field, or false if not set or not a bool.
func (s *StateSnapshot) GetBool(field StateField) bool {
	v, _ := s.Fields[field].(bool)
	return v
}
