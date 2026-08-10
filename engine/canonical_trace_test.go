package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

const canonicalTraceSchemaVersion = 1

type canonicalTrace struct {
	SchemaVersion int                    `json:"schema_version"`
	Fixture       string                 `json:"fixture"`
	Records       []canonicalTraceRecord `json:"records"`
}

type canonicalTraceRecord struct {
	Kind          string                   `json:"kind"`
	ModelRequest  *canonicalModelRequest   `json:"model_request,omitempty"`
	Stream        *canonicalStreamRecord   `json:"stream,omitempty"`
	Tool          *canonicalToolRecord     `json:"tool,omitempty"`
	Event         *canonicalEventRecord    `json:"event,omitempty"`
	StateBoundary *canonicalStateBoundary  `json:"state_boundary,omitempty"`
	Terminal      *canonicalTerminalRecord `json:"terminal,omitempty"`
}

type canonicalModelRequest struct {
	Ordinal            int                   `json:"ordinal"`
	Model              string                `json:"model,omitempty"`
	SystemPromptDigest string                `json:"system_prompt_digest,omitempty"`
	Messages           []canonicalMessage    `json:"messages,omitempty"`
	Tools              []canonicalToolSchema `json:"tools,omitempty"`
	ToolChoice         string                `json:"tool_choice,omitempty"`
	MaxTokens          *int                  `json:"max_tokens,omitempty"`
	Thinking           string                `json:"thinking,omitempty"`
	TaskBudget         *int                  `json:"task_budget,omitempty"`
}

type canonicalMessage struct {
	Role             string              `json:"role"`
	Content          string              `json:"content,omitempty"`
	ReasoningContent string              `json:"reasoning_content,omitempty"`
	ToolCallID       string              `json:"tool_call_id,omitempty"`
	ToolCalls        []canonicalToolCall `json:"tool_calls,omitempty"`
	Subtype          string              `json:"subtype,omitempty"`
	IsError          bool                `json:"is_error,omitempty"`
	FinishReason     string              `json:"finish_reason,omitempty"`
	Usage            *canonicalUsage     `json:"usage,omitempty"`
}

type canonicalToolSchema struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

type canonicalToolCall struct {
	Index     *int   `json:"index,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type canonicalUsage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

type canonicalStreamRecord struct {
	Ordinal        int                 `json:"ordinal"`
	Content        string              `json:"content,omitempty"`
	Reasoning      string              `json:"reasoning,omitempty"`
	ToolCalls      []canonicalToolCall `json:"tool_calls,omitempty"`
	Usage          *canonicalUsage     `json:"usage,omitempty"`
	FinishReason   string              `json:"finish_reason,omitempty"`
	WithheldReason string              `json:"withheld_reason,omitempty"`
}

type canonicalToolRecord struct {
	Ordinal             int      `json:"ordinal"`
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Input               string   `json:"input,omitempty"`
	Admission           string   `json:"admission"`
	StartOrder          int      `json:"start_order,omitempty"`
	FinishOrder         int      `json:"finish_order,omitempty"`
	Batch               int      `json:"batch,omitempty"`
	ResultKinds         []string `json:"result_kinds,omitempty"`
	PreventContinuation bool     `json:"prevent_continuation"`
	ContextModified     bool     `json:"context_modified"`
}

type canonicalEventRecord struct {
	Sequence   uint64 `json:"sequence"`
	SessionID  string `json:"session_id,omitempty"`
	ThreadID   string `json:"thread_id,omitempty"`
	TurnID     string `json:"turn_id,omitempty"`
	AgentID    string `json:"agent_id,omitempty"`
	Type       string `json:"type"`
	Causation  string `json:"causation,omitempty"`
	Payload    string `json:"payload,omitempty"`
	OccurredAt string `json:"occurred_at,omitempty"`
}

type canonicalStateBoundary struct {
	Name             string             `json:"name"`
	MessageDigest    string             `json:"message_digest,omitempty"`
	Messages         []canonicalMessage `json:"messages,omitempty"`
	Transition       string             `json:"transition,omitempty"`
	RecoveryCounters map[string]int     `json:"recovery_counters,omitempty"`
	Compact          bool               `json:"compact,omitempty"`
	QueueConsumption []string           `json:"queue_consumption,omitempty"`
}

type canonicalTerminalRecord struct {
	Reason             string `json:"reason"`
	TurnCount          int    `json:"turn_count"`
	MaxTurns           int    `json:"max_turns,omitempty"`
	ErrorClass         string `json:"error_class,omitempty"`
	FinalMessageDigest string `json:"final_message_digest,omitempty"`
}

type canonicalTraceDiffCategory string

const (
	traceDiffRequestCount   canonicalTraceDiffCategory = "request_count"
	traceDiffToolOrder      canonicalTraceDiffCategory = "tool_order"
	traceDiffToolOutcome    canonicalTraceDiffCategory = "tool_outcome"
	traceDiffEventOrder     canonicalTraceDiffCategory = "event_order"
	traceDiffMessageState   canonicalTraceDiffCategory = "message_state"
	traceDiffTerminalReason canonicalTraceDiffCategory = "terminal_reason"
	traceDiffRecordPayload  canonicalTraceDiffCategory = "record_payload"
)

type canonicalTraceDiff struct {
	Category canonicalTraceDiffCategory `json:"category"`
	Want     any                        `json:"want,omitempty"`
	Got      any                        `json:"got,omitempty"`
}

type canonicalTraceNormalizer struct {
	identities map[string]string
	tempRoots  []string
}

var (
	credentialKeyPattern  = regexp.MustCompile(`(?i)^(api[_-]?key|authorization|credential|password|secret|token|access[_-]?token|refresh[_-]?token|auth[_-]?token|cookie)$`)
	credentialTextPattern = regexp.MustCompile(`(?i)\b(api[_-]?key|authorization|credential|password|secret|token|access[_-]?token|refresh[_-]?token|auth[_-]?token|cookie)(\s*[:=]\s*)([^\s,;]+)`)
	bearerTextPattern     = regexp.MustCompile(`(?i)\bBearer\s+[^\s,;]+`)
)

func newCanonicalTraceNormalizer(tempRoots ...string) *canonicalTraceNormalizer {
	roots := append([]string(nil), tempRoots...)
	sort.SliceStable(roots, func(i, j int) bool { return len(roots[i]) > len(roots[j]) })
	return &canonicalTraceNormalizer{
		identities: make(map[string]string),
		tempRoots:  roots,
	}
}

func (n *canonicalTraceNormalizer) identity(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	if normalized, ok := n.identities[raw]; ok {
		return normalized
	}
	normalized := fmt.Sprintf("<id-%d>", len(n.identities)+1)
	n.identities[raw] = normalized
	return normalized
}

func (n *canonicalTraceNormalizer) timestamp(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return "<timestamp>"
}

func (n *canonicalTraceNormalizer) text(value string) string {
	for _, root := range n.tempRoots {
		if root != "" {
			value = strings.ReplaceAll(value, root, "<tmp>")
		}
	}
	value = credentialTextPattern.ReplaceAllString(value, "$1$2<redacted>")
	value = bearerTextPattern.ReplaceAllString(value, "Bearer <redacted>")
	return value
}

func (n *canonicalTraceNormalizer) value(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if credentialKeyPattern.MatchString(key) {
				out[key] = "<redacted>"
				continue
			}
			out[key] = n.value(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = n.value(item)
		}
		return out
	case string:
		return n.text(typed)
	default:
		return typed
	}
}

func (n *canonicalTraceNormalizer) canonicalJSON(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return n.text(raw)
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(n.value(value)); err != nil {
		return n.text(raw)
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}

func canonicalDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:8])
}

func marshalCanonicalTrace(trace canonicalTrace) ([]byte, error) {
	if trace.SchemaVersion == 0 {
		trace.SchemaVersion = canonicalTraceSchemaVersion
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(trace); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}

func diffCanonicalTraces(want, got canonicalTrace) []canonicalTraceDiff {
	diffs := make([]canonicalTraceDiff, 0, 6)
	appendDiff := func(category canonicalTraceDiffCategory, wantValue, gotValue any) {
		diffs = append(diffs, canonicalTraceDiff{Category: category, Want: wantValue, Got: gotValue})
	}

	wantRequests := traceModelRequests(want)
	gotRequests := traceModelRequests(got)
	if len(wantRequests) != len(gotRequests) {
		appendDiff(traceDiffRequestCount, len(wantRequests), len(gotRequests))
	}
	if !reflect.DeepEqual(traceToolOrder(want), traceToolOrder(got)) {
		appendDiff(traceDiffToolOrder, traceToolOrder(want), traceToolOrder(got))
	}
	if !reflect.DeepEqual(traceToolState(want), traceToolState(got)) {
		appendDiff(traceDiffToolOutcome, traceToolState(want), traceToolState(got))
	}
	if !reflect.DeepEqual(traceEventOrder(want), traceEventOrder(got)) {
		appendDiff(traceDiffEventOrder, traceEventOrder(want), traceEventOrder(got))
	}
	if !reflect.DeepEqual(traceMessageState(want), traceMessageState(got)) {
		appendDiff(traceDiffMessageState, traceMessageState(want), traceMessageState(got))
	}
	if traceTerminalReason(want) != traceTerminalReason(got) {
		appendDiff(traceDiffTerminalReason, traceTerminalReason(want), traceTerminalReason(got))
	}
	if len(diffs) == 0 && !reflect.DeepEqual(want, got) {
		appendDiff(traceDiffRecordPayload, canonicalDigest(want), canonicalDigest(got))
	}
	return diffs
}

func traceModelRequests(trace canonicalTrace) []canonicalModelRequest {
	requests := make([]canonicalModelRequest, 0)
	for _, record := range trace.Records {
		if record.Kind == "model_request" && record.ModelRequest != nil {
			requests = append(requests, *record.ModelRequest)
		}
	}
	return requests
}

func traceToolOrder(trace canonicalTrace) []string {
	order := make([]string, 0)
	for _, record := range trace.Records {
		if record.Kind == "tool" && record.Tool != nil {
			order = append(order, record.Tool.ID+":"+record.Tool.Name)
		}
	}
	return order
}

func traceToolState(trace canonicalTrace) []canonicalToolRecord {
	state := make([]canonicalToolRecord, 0)
	for _, record := range trace.Records {
		if record.Kind == "tool" && record.Tool != nil {
			state = append(state, *record.Tool)
		}
	}
	return state
}

func traceEventOrder(trace canonicalTrace) []string {
	order := make([]string, 0)
	for _, record := range trace.Records {
		if record.Kind == "event" && record.Event != nil {
			order = append(order, record.Event.Type)
		}
	}
	return order
}

func traceMessageState(trace canonicalTrace) any {
	type state struct {
		Requests   [][]canonicalMessage
		Boundaries []canonicalStateBoundary
	}
	result := state{}
	for _, request := range traceModelRequests(trace) {
		result.Requests = append(result.Requests, request.Messages)
	}
	for _, record := range trace.Records {
		if record.Kind == "state_boundary" && record.StateBoundary != nil {
			result.Boundaries = append(result.Boundaries, *record.StateBoundary)
		}
	}
	return result
}

func traceTerminalReason(trace canonicalTrace) string {
	for i := len(trace.Records) - 1; i >= 0; i-- {
		record := trace.Records[i]
		if record.Kind == "terminal" && record.Terminal != nil {
			return record.Terminal.Reason
		}
	}
	return ""
}

func TestCanonicalTraceNormalizer(t *testing.T) {
	normalizer := newCanonicalTraceNormalizer("/private/tmp/run-123", "/private/tmp")
	first := normalizer.identity("generated-session")
	if first != normalizer.identity("generated-session") || first == normalizer.identity("generated-turn") {
		t.Fatalf("identity mapping did not preserve relationships")
	}
	payload := map[string]any{
		"api_key":    "do-not-record",
		"max_tokens": 2048,
		"nested": map[string]any{
			"Authorization": "Bearer secret",
			"path":          "/private/tmp/run-123/file.txt",
		},
		"items": []any{"second", "first"},
	}
	normalized := normalizer.value(payload).(map[string]any)
	if normalized["api_key"] != "<redacted>" {
		t.Fatalf("credential was not redacted: %#v", normalized)
	}
	if normalized["max_tokens"] != 2048 {
		t.Fatalf("non-credential token field was redacted: %#v", normalized)
	}
	nested := normalized["nested"].(map[string]any)
	if nested["Authorization"] != "<redacted>" || nested["path"] != "<tmp>/file.txt" {
		t.Fatalf("nested normalization = %#v", nested)
	}
	if got := normalized["items"].([]any); !reflect.DeepEqual(got, []any{"second", "first"}) {
		t.Fatalf("semantic list order changed: %#v", got)
	}
	if normalizer.timestamp(time.Unix(1, 0)) != "<timestamp>" || normalizer.timestamp(time.Time{}) != "" {
		t.Fatal("timestamp normalization is not stable")
	}
	if got := normalizer.canonicalJSON(`{"z":2,"secret":"hidden","a":"/private/tmp/a"}`); got != `{"a":"<tmp>/a","secret":"<redacted>","z":2}` {
		t.Fatalf("canonical JSON = %s", got)
	}
	if got := normalizer.text("authorization=Bearer-real token:abc password = hunter2"); got != "authorization=<redacted> token:<redacted> password = <redacted>" {
		t.Fatalf("credential text normalization = %q", got)
	}
	if got := normalizer.text("header Bearer standalone-secret"); got != "header Bearer <redacted>" {
		t.Fatalf("bearer text normalization = %q", got)
	}
}

func TestCanonicalTraceMarshalStable(t *testing.T) {
	trace := canonicalTrace{Fixture: "stable", Records: []canonicalTraceRecord{{
		Kind:  "event",
		Event: &canonicalEventRecord{Sequence: 1, Type: "assistant", Payload: "ok"},
	}}}
	first, err := marshalCanonicalTrace(trace)
	if err != nil {
		t.Fatal(err)
	}
	second, err := marshalCanonicalTrace(trace)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || !strings.Contains(string(first), `"schema_version": 1`) {
		t.Fatalf("unstable trace JSON:\n%s\n%s", first, second)
	}
}

func TestCanonicalTraceDiffCategories(t *testing.T) {
	base := canonicalTrace{Fixture: "diff", Records: []canonicalTraceRecord{
		{Kind: "model_request", ModelRequest: &canonicalModelRequest{Ordinal: 1, Messages: []canonicalMessage{{Role: "user", Content: "hello"}}}},
		{Kind: "tool", Tool: &canonicalToolRecord{Ordinal: 1, ID: "call-1", Name: "Read", Admission: "allowed"}},
		{Kind: "event", Event: &canonicalEventRecord{Sequence: 1, Type: "assistant"}},
		{Kind: "state_boundary", StateBoundary: &canonicalStateBoundary{Name: "after_model", MessageDigest: "one"}},
		{Kind: "terminal", Terminal: &canonicalTerminalRecord{Reason: "completed", TurnCount: 1}},
	}}
	if diffs := diffCanonicalTraces(base, base); len(diffs) != 0 {
		t.Fatalf("identical traces differ: %#v", diffs)
	}

	tests := []struct {
		name     string
		mutate   func(*canonicalTrace)
		category canonicalTraceDiffCategory
	}{
		{name: "request count", category: traceDiffRequestCount, mutate: func(trace *canonicalTrace) {
			trace.Records = append(trace.Records, canonicalTraceRecord{Kind: "model_request", ModelRequest: &canonicalModelRequest{Ordinal: 2}})
		}},
		{name: "tool order", category: traceDiffToolOrder, mutate: func(trace *canonicalTrace) {
			trace.Records[1].Tool.Name = "Write"
		}},
		{name: "tool outcome", category: traceDiffToolOutcome, mutate: func(trace *canonicalTrace) {
			trace.Records[1].Tool.Admission = "permission_denied"
		}},
		{name: "event order", category: traceDiffEventOrder, mutate: func(trace *canonicalTrace) {
			trace.Records[2].Event.Type = "tool_result"
		}},
		{name: "message state", category: traceDiffMessageState, mutate: func(trace *canonicalTrace) {
			trace.Records[3].StateBoundary.MessageDigest = "two"
		}},
		{name: "terminal reason", category: traceDiffTerminalReason, mutate: func(trace *canonicalTrace) {
			trace.Records[4].Terminal.Reason = "model_error"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := cloneCanonicalTrace(t, base)
			tt.mutate(&mutated)
			diffs := diffCanonicalTraces(base, mutated)
			found := false
			for _, diff := range diffs {
				if diff.Category == tt.category {
					found = true
				}
			}
			if !found {
				t.Fatalf("category %q missing from %#v", tt.category, diffs)
			}
		})
	}
}

func cloneCanonicalTrace(t *testing.T, trace canonicalTrace) canonicalTrace {
	t.Helper()
	encoded, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	var cloned canonicalTrace
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}
