package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
)

// CanonicalProjectionVersion is the only accepted envelope version for the
// engine-owned canonical projection lifecycle. P23.1 introduced the
// transport-neutral shape; P23.2 activates the tool producer and ACP consumer.
const CanonicalProjectionVersion = 1

// CanonicalProjectionKind is the closed union of lifecycle kinds carried by a
// CanonicalProjectionEvent.
type CanonicalProjectionKind string

const (
	// CanonicalProjectionAssistantDelta carries exact assistant text bytes
	// under a stable logical message identity.
	CanonicalProjectionAssistantDelta CanonicalProjectionKind = "assistant_delta"
	// CanonicalProjectionToolStart begins one engine tool invocation. It may
	// include effective input only when that input has already settled.
	CanonicalProjectionToolStart CanonicalProjectionKind = "tool_start"
	// CanonicalProjectionToolInput supplies the complete effective input after
	// hooks, permission rewrites, and policy normalization have settled.
	CanonicalProjectionToolInput CanonicalProjectionKind = "tool_input"
	// CanonicalProjectionToolProgress replaces the rendered content snapshot
	// of a running tool invocation.
	CanonicalProjectionToolProgress CanonicalProjectionKind = "tool_progress"
	// CanonicalProjectionToolTerminal settles one tool invocation exactly once
	// with a completed or failed outcome.
	CanonicalProjectionToolTerminal CanonicalProjectionKind = "tool_terminal"
)

// CanonicalToolOutcome is the closed outcome union for a terminal tool event.
type CanonicalToolOutcome string

const (
	CanonicalToolOutcomeCompleted CanonicalToolOutcome = "completed"
	CanonicalToolOutcomeFailed    CanonicalToolOutcome = "failed"
)

// CanonicalProjectionEvent is the engine-internal canonical projection
// lifecycle envelope introduced under P23.1. It is transport-neutral:
// QueryEvent's embedded RuntimeEventEnvelope remains the identity and ordering
// owner, and no SDK type appears in this shape. Exactly one payload pointer is
// set, selected by Kind.
type CanonicalProjectionEvent struct {
	Version   int
	Kind      CanonicalProjectionKind
	Assistant *CanonicalAssistantPayload
	Tool      *CanonicalToolPayload
}

// CanonicalAssistantPayload carries one exact assistant delta under its
// logical message identity. Delta bytes are preserved verbatim and are never
// normalized.
type CanonicalAssistantPayload struct {
	MessageID string
	Delta     []byte
}

// CanonicalToolPayload carries the lifecycle facts of one engine tool
// invocation. ToolCallID is required for every tool kind. Which of the
// remaining fields apply is fixed by the event kind; fields irrelevant to the
// kind must be unset.
type CanonicalToolPayload struct {
	ToolCallID string

	// ToolName applies only to tool_start. EffectiveInput is an optional JSON
	// object on tool_start and is required on tool_input. A separate tool_input
	// event lets the engine publish post-rewrite dispatch input without
	// duplicating a start that had to precede permission interaction.
	ToolName       string
	EffectiveInput json.RawMessage

	// Content applies only to tool_progress: a complete rendered snapshot,
	// never an increment.
	Content string

	// Outcome and RawOutput apply only to tool_terminal. RawOutput is the
	// optional raw output as raw JSON.
	Outcome   CanonicalToolOutcome
	RawOutput json.RawMessage
}

// Validation errors are bounded static values; payload bytes are never
// interpolated into diagnostics.
var (
	errCanonicalProjectionNil             = errors.New("canonical projection: nil event")
	errCanonicalProjectionVersion         = errors.New("canonical projection: unsupported version")
	errCanonicalProjectionKind            = errors.New("canonical projection: unknown kind")
	errCanonicalProjectionPayloadUnion    = errors.New("canonical projection: payload does not match kind")
	errCanonicalProjectionMessageID       = errors.New("canonical projection: assistant message ID is required")
	errCanonicalProjectionDelta           = errors.New("canonical projection: assistant delta is required")
	errCanonicalProjectionToolCallID      = errors.New("canonical projection: tool call ID is required")
	errCanonicalProjectionToolName        = errors.New("canonical projection: tool name is required")
	errCanonicalProjectionOutcome         = errors.New("canonical projection: terminal outcome must be completed or failed")
	errCanonicalProjectionEffectiveInput  = errors.New("canonical projection: effective input must be a valid JSON object")
	errCanonicalProjectionRawOutput       = errors.New("canonical projection: raw output must be valid JSON")
	errCanonicalProjectionIrrelevantField = errors.New("canonical projection: field is not valid for kind")
)

// Validate enforces the frozen P23.1 contract: version 1, the closed kind and
// payload union, kind-specific required fields, valid JSON raw values, and
// rejection of fields irrelevant to the kind.
func (e *CanonicalProjectionEvent) Validate() error {
	if e == nil {
		return errCanonicalProjectionNil
	}
	if e.Version != CanonicalProjectionVersion {
		return errCanonicalProjectionVersion
	}
	switch e.Kind {
	case CanonicalProjectionAssistantDelta:
		if e.Assistant == nil || e.Tool != nil {
			return errCanonicalProjectionPayloadUnion
		}
		return e.Assistant.validate()
	case CanonicalProjectionToolStart,
		CanonicalProjectionToolInput,
		CanonicalProjectionToolProgress,
		CanonicalProjectionToolTerminal:
		if e.Tool == nil || e.Assistant != nil {
			return errCanonicalProjectionPayloadUnion
		}
		return e.Tool.validate(e.Kind)
	default:
		return errCanonicalProjectionKind
	}
}

func (p *CanonicalAssistantPayload) validate() error {
	if p.MessageID == "" {
		return errCanonicalProjectionMessageID
	}
	if len(p.Delta) == 0 {
		return errCanonicalProjectionDelta
	}
	return nil
}

func (p *CanonicalToolPayload) validate(kind CanonicalProjectionKind) error {
	if p.ToolCallID == "" {
		return errCanonicalProjectionToolCallID
	}
	switch kind {
	case CanonicalProjectionToolStart:
		if p.ToolName == "" {
			return errCanonicalProjectionToolName
		}
		if len(p.EffectiveInput) > 0 &&
			!canonicalProjectionJSONObject(p.EffectiveInput) {
			return errCanonicalProjectionEffectiveInput
		}
		if p.Content != "" || p.Outcome != "" || len(p.RawOutput) > 0 {
			return errCanonicalProjectionIrrelevantField
		}
	case CanonicalProjectionToolInput:
		if !canonicalProjectionJSONObject(p.EffectiveInput) {
			return errCanonicalProjectionEffectiveInput
		}
		if p.ToolName != "" ||
			p.Content != "" ||
			p.Outcome != "" ||
			len(p.RawOutput) > 0 {
			return errCanonicalProjectionIrrelevantField
		}
	case CanonicalProjectionToolProgress:
		if p.ToolName != "" || len(p.EffectiveInput) > 0 || p.Outcome != "" || len(p.RawOutput) > 0 {
			return errCanonicalProjectionIrrelevantField
		}
	case CanonicalProjectionToolTerminal:
		if p.Outcome != CanonicalToolOutcomeCompleted && p.Outcome != CanonicalToolOutcomeFailed {
			return errCanonicalProjectionOutcome
		}
		if len(p.RawOutput) > 0 && !json.Valid(p.RawOutput) {
			return errCanonicalProjectionRawOutput
		}
		if p.ToolName != "" || len(p.EffectiveInput) > 0 || p.Content != "" {
			return errCanonicalProjectionIrrelevantField
		}
	}
	return nil
}

func canonicalProjectionJSONObject(raw json.RawMessage) bool {
	if len(raw) == 0 || !json.Valid(raw) {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}

// Clone returns a deep copy of the envelope: raw JSON values and byte slices
// are copied so the clone shares no mutable memory with the original.
func (e *CanonicalProjectionEvent) Clone() *CanonicalProjectionEvent {
	if e == nil {
		return nil
	}
	out := &CanonicalProjectionEvent{
		Version: e.Version,
		Kind:    e.Kind,
	}
	if e.Assistant != nil {
		out.Assistant = &CanonicalAssistantPayload{
			MessageID: e.Assistant.MessageID,
			Delta:     bytes.Clone(e.Assistant.Delta),
		}
	}
	if e.Tool != nil {
		out.Tool = &CanonicalToolPayload{
			ToolCallID:     e.Tool.ToolCallID,
			ToolName:       e.Tool.ToolName,
			EffectiveInput: bytes.Clone(e.Tool.EffectiveInput),
			Content:        e.Tool.Content,
			Outcome:        e.Tool.Outcome,
			RawOutput:      bytes.Clone(e.Tool.RawOutput),
		}
	}
	return out
}

// canonicalRedactedPlaceholder is the only literal ever substituted for
// redacted credential material. Diagnostics and payloads never interpolate
// the private bytes that were removed.
const canonicalRedactedPlaceholder = "[redacted]"

// Builder errors are bounded static values; private payload bytes are never
// interpolated into diagnostics.
var (
	errCanonicalProjectionNilToolCall = errors.New("canonical projection: nil tool call")
	errCanonicalProjectionNilResult   = errors.New("canonical projection: nil tool result")
	errCanonicalProjectionNilInput    = errors.New("canonical projection: nil effective input")
)

// canonicalCredentialKeyMarkers are matched after separator and camelCase
// normalization against complete object keys or underscore-delimited suffixes.
// Matching keys have their entire value replaced.
var canonicalCredentialKeyMarkers = []string{
	"secret",
	"token",
	"password",
	"credential",
	"authorization",
	"authorization_header",
	"cookie",
	"api_key",
	"apikey",
	"private_key",
	"access_key",
}

// canonicalSecretTextPatterns are high-confidence credential byte patterns
// (AWS/GCP/GitHub/OpenAI/Anthropic/Slack tokens and PEM private-key blocks)
// replaced inside every string value, progress snapshot, and terminal output.
var canonicalSecretTextPatterns = []*regexp.Regexp{
	// PEM private-key block; an unterminated block redacts through end of
	// string because everything after the header is key material.
	regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|\z)`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),    // AWS access key ID
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),        // GCP API key
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{16,}\b`),   // GitHub token
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`), // GitHub fine-grained PAT
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),        // OpenAI / Anthropic
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`), // Slack token
}

var (
	canonicalCredentialAcronymBoundary = regexp.MustCompile(
		`([A-Z]+)([A-Z][a-z])`,
	)
	canonicalCredentialWordBoundary = regexp.MustCompile(
		`([a-z0-9])([A-Z])`,
	)
	canonicalCredentialSeparator   = regexp.MustCompile(`[^A-Za-z0-9]+`)
	canonicalCredentialTextPattern = regexp.MustCompile(
		`(?i)\b([a-z0-9_.-]*(?:api[_-]?key|authorization(?:[_-]?header)?|credential|password|secret|token|access[_-]?token|refresh[_-]?token|auth[_-]?token|cookie))(\s*[:=]\s*)([^\s,;]+)`,
	)
	canonicalAuthorizationTextPattern = regexp.MustCompile(
		`(?i)\b([a-z0-9_.-]*authorization(?:[_-]?header)?)(\s*[:=]\s*)(?:bearer|basic)\s+[^\s,;]+`,
	)
	canonicalBearerTextPattern = regexp.MustCompile(
		`(?i)\bBearer\s+[^\s,;]+`,
	)
)

// canonicalConfigNameKeys are sibling keys whose string value can name a
// credential setting (Config tool "setting"/legacy "key" shapes, generic
// "name"); when one does, the sibling "value" entry is redacted.
var canonicalConfigNameKeys = []string{"setting", "key", "name"}

// buildCanonicalToolStartProjection constructs the version-1 tool_start
// projection fact for one committed tool call: call ID and name only, never
// input. It is transport-neutral: it does not emit, reorder, or mutate any
// execution state.
func buildCanonicalToolStartProjection(call *schema.ToolCall) (QueryEvent, error) {
	if call == nil {
		return QueryEvent{}, errCanonicalProjectionNilToolCall
	}
	if call.ID == "" {
		return QueryEvent{}, errCanonicalProjectionToolCallID
	}
	if call.Function.Name == "" {
		return QueryEvent{}, errCanonicalProjectionToolName
	}
	return newCanonicalToolProjectionEvent(CanonicalProjectionToolStart, &CanonicalToolPayload{
		ToolCallID: call.ID,
		ToolName:   call.Function.Name,
	})
}

// buildCanonicalToolInputProjection constructs the tool_input projection fact
// carrying the final redacted effective input as a JSON object. The caller's
// input map is never mutated.
func buildCanonicalToolInputProjection(toolCallID string, input map[string]any) (QueryEvent, error) {
	if toolCallID == "" {
		return QueryEvent{}, errCanonicalProjectionToolCallID
	}
	if input == nil {
		return QueryEvent{}, errCanonicalProjectionNilInput
	}
	redacted := redactCanonicalProjectionValue(input)
	encoded, err := json.Marshal(redacted)
	if err != nil {
		return QueryEvent{}, fmt.Errorf("canonical projection: encode effective input: %w", err)
	}
	return newCanonicalToolProjectionEvent(CanonicalProjectionToolInput, &CanonicalToolPayload{
		ToolCallID:     toolCallID,
		EffectiveInput: json.RawMessage(encoded),
	})
}

// buildCanonicalToolProgressProjection constructs the tool_progress
// projection fact carrying a redacted complete replacement snapshot.
func buildCanonicalToolProgressProjection(toolCallID, snapshot string) (QueryEvent, error) {
	if toolCallID == "" {
		return QueryEvent{}, errCanonicalProjectionToolCallID
	}
	return newCanonicalToolProjectionEvent(CanonicalProjectionToolProgress, &CanonicalToolPayload{
		ToolCallID: toolCallID,
		Content:    redactCanonicalProjectionText(snapshot),
	})
}

// buildCanonicalToolTerminalProjection constructs the tool_terminal
// projection fact for one normalized execution result: IsError maps to failed
// and success to completed, and RawOutput carries the redacted actual result
// string as JSON.
func buildCanonicalToolTerminalProjection(result *execution.ToolResult) (QueryEvent, error) {
	if result == nil {
		return QueryEvent{}, errCanonicalProjectionNilResult
	}
	if result.ToolCallID == "" {
		return QueryEvent{}, errCanonicalProjectionToolCallID
	}
	outcome := CanonicalToolOutcomeCompleted
	if result.IsError {
		outcome = CanonicalToolOutcomeFailed
	}
	encoded, err := json.Marshal(redactCanonicalProjectionText(result.Result))
	if err != nil {
		return QueryEvent{}, fmt.Errorf("canonical projection: encode raw output: %w", err)
	}
	return newCanonicalToolProjectionEvent(CanonicalProjectionToolTerminal, &CanonicalToolPayload{
		ToolCallID: result.ToolCallID,
		Outcome:    outcome,
		RawOutput:  json.RawMessage(encoded),
	})
}

// newCanonicalToolProjectionEvent wraps one tool payload in a version-1
// canonical projection QueryEvent and enforces that every returned payload
// validates against the frozen version-1 contract.
func newCanonicalToolProjectionEvent(
	kind CanonicalProjectionKind,
	payload *CanonicalToolPayload,
) (QueryEvent, error) {
	projection := &CanonicalProjectionEvent{
		Version: CanonicalProjectionVersion,
		Kind:    kind,
		Tool:    payload,
	}
	if err := projection.Validate(); err != nil {
		return QueryEvent{}, err
	}
	return QueryEvent{
		Type:                EventCanonicalProjection,
		CanonicalProjection: projection,
	}, nil
}

// redactCanonicalProjectionValue applies the project-owned diagnostic
// redaction: JSON shape and ordinary values are preserved, values under
// credential-like keys are replaced with the redacted placeholder, and
// high-confidence credential byte patterns are replaced inside strings. The
// input is never mutated; redacted containers are fresh copies.
func redactCanonicalProjectionValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redactSiblingValue := mapNamesCredentialSetting(typed)
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if canonicalCredentialKey(key) ||
				(redactSiblingValue && strings.EqualFold(key, "value")) {
				out[key] = canonicalRedactedPlaceholder
				continue
			}
			out[key] = redactCanonicalProjectionValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index := range typed {
			out[index] = redactCanonicalProjectionValue(typed[index])
		}
		return out
	case string:
		return redactCanonicalProjectionText(typed)
	default:
		return value
	}
}

// canonicalCredentialKey reports whether an object key names a credential.
// Matching normalizes separators, camelCase, and acronym prefixes; a marker
// exact or suffix match covers compound keys such as
// "aws_secret_access_key" without treating unrelated keys such as
// "tokenizer" as credentials.
func canonicalCredentialKey(key string) bool {
	normalized := normalizeCanonicalCredentialIdentifier(key)
	for _, marker := range canonicalCredentialKeyMarkers {
		if normalized == marker ||
			strings.HasSuffix(normalized, "_"+marker) {
			return true
		}
	}
	return false
}

// mapNamesCredentialSetting reports whether any Config-style sibling key
// ("setting", legacy "key", or "name") holds a string that names a
// credential, in which case the sibling "value" entry must be redacted.
func mapNamesCredentialSetting(object map[string]any) bool {
	for key, item := range object {
		if !canonicalConfigNameKey(key) {
			continue
		}
		name, ok := item.(string)
		if !ok {
			continue
		}
		normalized := normalizeCanonicalCredentialIdentifier(name)
		for _, marker := range canonicalCredentialKeyMarkers {
			if normalized == marker ||
				strings.HasSuffix(normalized, "_"+marker) {
				return true
			}
		}
	}
	return false
}

// normalizeCanonicalCredentialIdentifier converts common snake, kebab,
// dotted, spaced, camelCase, and acronym-prefixed names into one lowercase
// underscore form without making substring matches. That covers identifiers
// such as clientSecret and AWSSecretAccessKey while preserving tokenizer.
func normalizeCanonicalCredentialIdentifier(value string) string {
	value = canonicalCredentialAcronymBoundary.ReplaceAllString(
		value,
		"${1}_${2}",
	)
	value = canonicalCredentialWordBoundary.ReplaceAllString(
		value,
		"${1}_${2}",
	)
	value = canonicalCredentialSeparator.ReplaceAllString(value, "_")
	return strings.Trim(strings.ToLower(value), "_")
}

// canonicalConfigNameKey reports whether a key can name a setting in a
// Config-style name/value object.
func canonicalConfigNameKey(key string) bool {
	for _, name := range canonicalConfigNameKeys {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

// redactCanonicalProjectionText replaces high-confidence credential byte
// patterns inside one string with the redacted placeholder.
func redactCanonicalProjectionText(text string) string {
	for _, pattern := range canonicalSecretTextPatterns {
		text = pattern.ReplaceAllString(text, canonicalRedactedPlaceholder)
	}
	text = canonicalAuthorizationTextPattern.ReplaceAllString(
		text,
		"$1$2"+canonicalRedactedPlaceholder,
	)
	text = canonicalBearerTextPattern.ReplaceAllString(
		text,
		"Bearer "+canonicalRedactedPlaceholder,
	)
	text = canonicalCredentialTextPattern.ReplaceAllString(
		text,
		"$1$2"+canonicalRedactedPlaceholder,
	)
	return text
}
