package diagnostics

import "time"

// FieldState describes whether one diagnostic value is authoritative at the
// observation boundary. Zero values are meaningful only when StateKnown is
// explicit.
type FieldState string

const (
	StateKnown       FieldState = "known"
	StateUnavailable FieldState = "unavailable"
	StateStale       FieldState = "stale"
	StateRefreshing  FieldState = "refreshing"
)

// FieldMeta records the provenance and freshness of one diagnostic value.
type FieldMeta struct {
	State      FieldState `json:"state"`
	Source     string     `json:"source,omitempty"`
	ObservedAt time.Time  `json:"observed_at,omitempty"`
	Detail     string     `json:"detail,omitempty"`
}

type StringField struct {
	FieldMeta
	Value string `json:"value,omitempty"`
}

type IntField struct {
	FieldMeta
	Value int `json:"value"`
}

type BoolField struct {
	FieldMeta
	Value bool `json:"value"`
}

// Snapshot is the single renderer-neutral diagnostics result owned by one
// QueryEngine. Slash commands and future CLI projections consume this value;
// they do not rediscover configuration or recompute usage.
type Snapshot struct {
	ObservedAt time.Time       `json:"observed_at"`
	Status     StatusSnapshot  `json:"status"`
	Context    ContextSnapshot `json:"context"`
	Usage      UsageSnapshot   `json:"usage"`
	Config     ConfigSnapshot  `json:"config"`
	Doctor     DoctorSnapshot  `json:"doctor"`
}

type StatusSnapshot struct {
	SessionID      StringField `json:"session_id"`
	Model          StringField `json:"model"`
	Provider       StringField `json:"provider"`
	CWD            StringField `json:"cwd"`
	MessageCount   IntField    `json:"message_count"`
	ToolCount      IntField    `json:"tool_count"`
	Transcript     StringField `json:"transcript"`
	UsageCoverage  StringField `json:"usage_coverage"`
	ContextPercent IntField    `json:"context_percent"`
}

type ContextSnapshot struct {
	Model                 StringField `json:"model"`
	ContextWindowTokens   IntField    `json:"context_window_tokens"`
	CurrentInputTokens    IntField    `json:"current_input_tokens"`
	UsagePercent          IntField    `json:"usage_percent"`
	TotalMessages         IntField    `json:"total_messages"`
	UserMessages          IntField    `json:"user_messages"`
	AssistantMessages     IntField    `json:"assistant_messages"`
	ToolMessages          IntField    `json:"tool_messages"`
	SystemMessages        IntField    `json:"system_messages"`
	ToolCalls             IntField    `json:"tool_calls"`
	CompactionBoundaries  IntField    `json:"compaction_boundaries"`
	TransientContributors StringField `json:"transient_contributors"`
}

type UsageSnapshot struct {
	PromptTokens             IntField    `json:"prompt_tokens"`
	CompletionTokens         IntField    `json:"completion_tokens"`
	TotalTokens              IntField    `json:"total_tokens"`
	ResponsesWithMetadata    IntField    `json:"responses_with_metadata"`
	ResponsesWithoutMetadata IntField    `json:"responses_without_metadata"`
	Coverage                 StringField `json:"coverage"`
}

type ConfigSnapshot struct {
	Provider             StringField `json:"provider"`
	Model                StringField `json:"model"`
	CredentialConfigured BoolField   `json:"credential_configured"`
	Endpoint             StringField `json:"endpoint"`
	FallbackModel        StringField `json:"fallback_model"`
	PermissionMode       StringField `json:"permission_mode"`
	Precedence           StringField `json:"precedence"`
}

type CheckOutcome string

const (
	CheckPass    CheckOutcome = "pass"
	CheckWarn    CheckOutcome = "warn"
	CheckFail    CheckOutcome = "fail"
	CheckSkipped CheckOutcome = "skipped"
)

type DoctorCheck struct {
	ID          string       `json:"id"`
	Outcome     CheckOutcome `json:"outcome"`
	FieldMeta   FieldMeta    `json:"field"`
	Summary     string       `json:"summary"`
	Remediation string       `json:"remediation,omitempty"`
}

type DoctorSnapshot struct {
	Checks []DoctorCheck `json:"checks"`
}
