package engine

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var updateCanonicalTraces = flag.Bool(
	"update-canonical-traces",
	false,
	"rewrite reviewed P13.0 canonical trace fixtures",
)

type canonicalCallModelFn func(
	context.Context,
	model.BaseChatModel,
	[]*schema.Message,
	*schema.Message,
	[]*schema.ToolInfo,
	execution.CallModelOptions,
) (*execution.CallModelResult, error)

type canonicalExecutionFact struct {
	startOrder  int
	finishOrder int
	batch       int
}

type canonicalToolFlags struct {
	preventContinuation bool
	contextModified     bool
}

type canonicalQueryRecorder struct {
	mu sync.Mutex

	trace      canonicalTrace
	normalizer *canonicalTraceNormalizer
	delegate   canonicalCallModelFn

	requestOrdinal int
	streamOrdinal  int
	eventOrdinal   uint64
	toolOrdinal    int
	startOrdinal   int
	finishOrdinal  int

	toolCalls      map[string]canonicalToolCall
	toolFacts      map[string]canonicalExecutionFact
	resolvedInputs map[string]string
	toolFlags      map[string]canonicalToolFlags
	lastInput      []canonicalMessage
	lastStream     canonicalMessage
}

func newCanonicalQueryRecorder(fixture string, tempRoots ...string) *canonicalQueryRecorder {
	return &canonicalQueryRecorder{
		trace: canonicalTrace{
			SchemaVersion: canonicalTraceSchemaVersion,
			Fixture:       fixture,
		},
		normalizer:     newCanonicalTraceNormalizer(tempRoots...),
		toolCalls:      make(map[string]canonicalToolCall),
		toolFacts:      make(map[string]canonicalExecutionFact),
		resolvedInputs: make(map[string]string),
		toolFlags:      make(map[string]canonicalToolFlags),
	}
}

func (r *canonicalQueryRecorder) callModel(
	ctx context.Context,
	chatModel model.BaseChatModel,
	messages []*schema.Message,
	systemPrompt *schema.Message,
	toolInfos []*schema.ToolInfo,
	opts execution.CallModelOptions,
) (*execution.CallModelResult, error) {
	if err := opts.ProviderCallBudget.ReserveProviderCall(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.requestOrdinal++
	request := canonicalModelRequest{
		Ordinal:            r.requestOrdinal,
		Model:              opts.Model,
		SystemPromptDigest: canonicalDigest(canonicalizeMessage(r.normalizer, systemPrompt)),
		Messages:           canonicalizeMessages(r.normalizer, messages),
		Tools:              canonicalizeToolSchemas(r.normalizer, toolInfos),
		ToolChoice:         opts.ToolChoice,
		MaxTokens:          cloneInt(opts.MaxOutputTokens),
	}
	if opts.ForcedToolName != "" {
		request.ToolChoice = "forced:" + opts.ForcedToolName
	}
	if opts.ThinkingConfig != nil {
		request.Thinking = opts.ThinkingConfig.Type
		if opts.ThinkingConfig.BudgetTokens != nil {
			request.Thinking += fmt.Sprintf(":%d", *opts.ThinkingConfig.BudgetTokens)
		}
	}
	if opts.TaskBudget != nil {
		request.TaskBudget = cloneInt(opts.TaskBudget.Remaining)
		if request.TaskBudget == nil {
			request.TaskBudget = cloneInt(&opts.TaskBudget.Total)
		}
	}
	r.lastInput = append([]canonicalMessage(nil), request.Messages...)
	r.trace.Records = append(r.trace.Records,
		canonicalTraceRecord{Kind: "model_request", ModelRequest: &request},
		canonicalTraceRecord{Kind: "state_boundary", StateBoundary: &canonicalStateBoundary{
			Name:          "before_model_request",
			MessageDigest: canonicalDigest(request.Messages),
			Messages:      append([]canonicalMessage(nil), request.Messages...),
		}},
	)
	r.mu.Unlock()

	delegate := r.delegate
	if delegate == nil {
		delegate = execution.CallModel
	}
	result, err := delegate(ctx, chatModel, messages, systemPrompt, toolInfos, opts)
	if result == nil || result.StreamReader == nil {
		return result, err
	}
	cloned := *result
	cloned.StreamReader = schema.StreamReaderWithConvert(result.StreamReader, func(message *schema.Message) (*schema.Message, error) {
		r.recordStream(message)
		return message, nil
	})
	return &cloned, err
}

func (r *canonicalQueryRecorder) recordStream(message *schema.Message) {
	if message == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.streamOrdinal++
	canonical := canonicalizeMessage(r.normalizer, message)
	r.lastStream = canonical
	for i, toolCall := range message.ToolCalls {
		key := toolCall.ID
		if key == "" {
			key = fmt.Sprintf("%s:%d", toolCall.Function.Name, i)
		}
		r.toolCalls[key] = canonical.ToolCalls[i]
	}
	withheld := ""
	if message.Extra != nil && message.Extra["api_error"] == true {
		withheld, _ = message.Extra["error_type"].(string)
		if withheld == "" {
			withheld = "api_error"
		}
	}
	r.trace.Records = append(r.trace.Records, canonicalTraceRecord{
		Kind: "stream",
		Stream: &canonicalStreamRecord{
			Ordinal:        r.streamOrdinal,
			Content:        canonical.Content,
			Reasoning:      canonical.ReasoningContent,
			ToolCalls:      append([]canonicalToolCall(nil), canonical.ToolCalls...),
			Usage:          canonical.Usage,
			FinishReason:   canonical.FinishReason,
			WithheldReason: withheld,
		},
	})
}

func (r *canonicalQueryRecorder) recordEvent(event QueryEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.eventOrdinal++
	sequence := event.Sequence
	if sequence == 0 {
		sequence = r.eventOrdinal
	}
	r.trace.Records = append(r.trace.Records, canonicalTraceRecord{
		Kind: "event",
		Event: &canonicalEventRecord{
			Sequence:   sequence,
			SessionID:  r.normalizer.identity(event.SessionID),
			ThreadID:   r.normalizer.identity(event.ThreadID),
			TurnID:     r.normalizer.identity(event.TurnID),
			AgentID:    r.normalizer.identity(event.AgentID),
			Type:       string(event.Type),
			Causation:  r.normalizer.identity(event.CausationID),
			Payload:    canonicalEventPayload(r.normalizer, event),
			OccurredAt: r.normalizer.timestamp(event.Timestamp),
		},
	})

	if event.Type == EventCompactBoundary {
		message := canonicalizeMessage(r.normalizer, event.CompactBoundaryMessage)
		r.trace.Records = append(r.trace.Records, canonicalTraceRecord{
			Kind: "state_boundary",
			StateBoundary: &canonicalStateBoundary{
				Name:          "compact_boundary",
				MessageDigest: canonicalDigest(message),
				Messages:      []canonicalMessage{message},
				Compact:       true,
			},
		})
	}
	if event.Type == EventCommandLifecycle && event.CommandLifecycle != nil {
		r.trace.Records = append(r.trace.Records, canonicalTraceRecord{
			Kind: "state_boundary",
			StateBoundary: &canonicalStateBoundary{
				Name:             "queue_" + string(event.CommandLifecycle.Phase),
				QueueConsumption: []string{r.normalizer.identity(event.CommandLifecycle.CommandUUID)},
			},
		})
	}
	if event.Type == EventToolResult && event.ToolResultMessage != nil {
		r.recordToolResultLocked(event.ToolResultMessage)
	}
}

func (r *canonicalQueryRecorder) recordToolResultLocked(message *schema.Message) {
	r.toolOrdinal++
	rawID := message.ToolCallID
	call := r.toolCalls[rawID]
	if call.Name == "" {
		call.Name = message.ToolName
	}
	if call.ID == "" {
		call.ID = r.normalizer.identity(rawID)
	}
	input := call.Arguments
	if resolved := r.resolvedInputs[rawID]; resolved != "" {
		input = resolved
	}
	fact := r.toolFacts[rawID]
	flags := r.toolFlags[rawID]
	isError := message.Extra != nil && message.Extra["is_error"] == true
	admission := "allowed"
	if isError {
		admission = "rejected"
		switch {
		case strings.Contains(message.Content, "truncated"):
			admission = "rejected_truncated"
		case strings.Contains(message.Content, "permission denied"):
			admission = "permission_denied"
		case strings.Contains(message.Content, "Interrupted"):
			admission = "interrupted"
		}
	}
	resultKind := "result"
	if isError {
		resultKind = "error"
	}
	r.trace.Records = append(r.trace.Records, canonicalTraceRecord{
		Kind: "tool",
		Tool: &canonicalToolRecord{
			Ordinal:             r.toolOrdinal,
			ID:                  call.ID,
			Name:                call.Name,
			Input:               input,
			Admission:           admission,
			StartOrder:          fact.startOrder,
			FinishOrder:         fact.finishOrder,
			Batch:               fact.batch,
			ResultKinds:         []string{resultKind},
			PreventContinuation: flags.preventContinuation,
			ContextModified:     flags.contextModified,
		},
	})
}

func (r *canonicalQueryRecorder) observeToolExecution(
	ctx context.Context,
	toolName string,
	jsonInput string,
	execute func(context.Context, string, string) (string, error),
) (string, error) {
	canonicalInput := r.normalizer.canonicalJSON(jsonInput)
	toolUseID := tools.ToolUseIDFromCtx(ctx)
	r.mu.Lock()
	r.startOrdinal++
	r.resolvedInputs[toolUseID] = canonicalInput
	fact := r.toolFacts[toolUseID]
	fact.startOrder = r.startOrdinal
	if fact.batch == 0 {
		fact.batch = r.startOrdinal
	}
	r.toolFacts[toolUseID] = fact
	r.mu.Unlock()

	result, err := execute(ctx, toolName, jsonInput)
	r.mu.Lock()
	r.finishOrdinal++
	fact = r.toolFacts[toolUseID]
	fact.finishOrder = r.finishOrdinal
	r.toolFacts[toolUseID] = fact
	r.mu.Unlock()
	return result, err
}

func (r *canonicalQueryRecorder) setToolFact(toolUseID string, fact canonicalExecutionFact) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toolFacts[toolUseID] = fact
}

func (r *canonicalQueryRecorder) setToolFlags(toolUseID string, update func(*canonicalToolFlags)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	flags := r.toolFlags[toolUseID]
	update(&flags)
	r.toolFlags[toolUseID] = flags
	normalizedID := r.normalizer.identity(toolUseID)
	for index := range r.trace.Records {
		record := &r.trace.Records[index]
		if record.Kind != "tool" || record.Tool == nil || record.Tool.ID != normalizedID {
			continue
		}
		record.Tool.PreventContinuation = flags.preventContinuation
		record.Tool.ContextModified = flags.contextModified
	}
}

func (r *canonicalQueryRecorder) finish(terminal Terminal, maxTurns *int) canonicalTrace {
	r.mu.Lock()
	defer r.mu.Unlock()
	max := 0
	if maxTurns != nil {
		max = *maxTurns
	}
	errorClass := ""
	if terminal.Err != nil {
		errorClass = fmt.Sprintf("%T", terminal.Err)
	}
	r.trace.Records = append(r.trace.Records,
		canonicalTraceRecord{Kind: "state_boundary", StateBoundary: &canonicalStateBoundary{
			Name:          "query_terminal",
			MessageDigest: canonicalDigest(r.lastInput),
			Messages:      append([]canonicalMessage(nil), r.lastInput...),
			Transition:    string(terminal.Reason),
		}},
		canonicalTraceRecord{Kind: "terminal", Terminal: &canonicalTerminalRecord{
			Reason:             string(terminal.Reason),
			TurnCount:          terminal.TurnCount,
			MaxTurns:           max,
			ErrorClass:         errorClass,
			FinalMessageDigest: canonicalDigest(r.lastStream),
		}},
	)
	normalizeConcurrentCanonicalToolProjectionBlocks(r.trace.Records)
	return cloneTraceWithoutTesting(r.trace)
}

// normalizeConcurrentCanonicalToolProjectionBlocks gives the compatibility
// trace one stable representation for causally independent lifecycle events.
// Production keeps the real RuntimeEventEnvelope order: concurrent safe tools
// may enter executeToolCall in either goroutine order. Within each contiguous
// projection-only block, the trace orders lifecycle phases and then the stable
// model-observation identity. It also reassigns the same sequence slots to the
// normalized records. Non-projection boundaries, including permission and
// legacy result events, never move.
func normalizeConcurrentCanonicalToolProjectionBlocks(
	records []canonicalTraceRecord,
) {
	for start := 0; start < len(records); {
		if _, _, ok := canonicalToolProjectionSortKey(records[start]); !ok {
			start++
			continue
		}
		end := start + 1
		for end < len(records) {
			if _, _, ok := canonicalToolProjectionSortKey(records[end]); !ok {
				break
			}
			end++
		}
		if end-start > 1 {
			sequences := make([]uint64, 0, end-start)
			for index := start; index < end; index++ {
				sequences = append(
					sequences,
					records[index].Event.Sequence,
				)
			}
			sort.Slice(sequences, func(i, j int) bool {
				return sequences[i] < sequences[j]
			})
			sort.SliceStable(records[start:end], func(i, j int) bool {
				leftID, leftPhase, _ := canonicalToolProjectionSortKey(records[start+i])
				rightID, rightPhase, _ := canonicalToolProjectionSortKey(records[start+j])
				if leftPhase != rightPhase {
					return leftPhase < rightPhase
				}
				return leftID < rightID
			})
			for index := start; index < end; index++ {
				records[index].Event.Sequence = sequences[index-start]
			}
		}
		start = end
	}
}

func canonicalToolProjectionSortKey(
	record canonicalTraceRecord,
) (int, int, bool) {
	if record.Kind != "event" ||
		record.Event == nil ||
		record.Event.Type != string(EventCanonicalProjection) {
		return 0, 0, false
	}
	var payload struct {
		Kind       CanonicalProjectionKind `json:"canonical_kind"`
		ToolCallID string                  `json:"tool_call_id"`
	}
	if json.Unmarshal([]byte(record.Event.Payload), &payload) != nil ||
		payload.ToolCallID == "" {
		return 0, 0, false
	}
	identityOrdinal := 0
	if _, err := fmt.Sscanf(
		payload.ToolCallID,
		"<id-%d>",
		&identityOrdinal,
	); err != nil {
		return 0, 0, false
	}
	phase := map[CanonicalProjectionKind]int{
		CanonicalProjectionToolStart:    1,
		CanonicalProjectionToolInput:    2,
		CanonicalProjectionToolProgress: 3,
		CanonicalProjectionToolTerminal: 4,
	}[payload.Kind]
	if phase == 0 {
		return 0, 0, false
	}
	return identityOrdinal, phase, true
}

func runCanonicalQuery(
	t *testing.T,
	fixture string,
	params QueryParams,
	configure func(*canonicalQueryRecorder),
	kernel queryKernel,
) canonicalTrace {
	t.Helper()
	recorder := newCanonicalQueryRecorder(fixture, t.TempDir())
	if configure != nil {
		configure(recorder)
	}
	uuidOrdinal := 0
	params.Deps = &QueryDeps{
		UUID: func() string {
			uuidOrdinal++
			return fmt.Sprintf("generated-%s-%d", fixture, uuidOrdinal)
		},
		CallModel: recorder.callModel,
	}
	terminal := queryWithKernel(
		context.Background(),
		params,
		recorder.recordEvent,
		kernel,
	)
	return recorder.finish(terminal, params.MaxTurns)
}

func canonicalizeMessages(normalizer *canonicalTraceNormalizer, messages []*schema.Message) []canonicalMessage {
	result := make([]canonicalMessage, 0, len(messages))
	for _, message := range messages {
		result = append(result, canonicalizeMessage(normalizer, message))
	}
	return result
}

func canonicalizeMessage(normalizer *canonicalTraceNormalizer, message *schema.Message) canonicalMessage {
	if message == nil {
		return canonicalMessage{}
	}
	result := canonicalMessage{
		Role:             string(message.Role),
		Content:          canonicalTraceText(normalizer.text(message.Content)),
		ReasoningContent: canonicalTraceText(normalizer.text(message.ReasoningContent)),
		ToolCallID:       normalizer.identity(message.ToolCallID),
	}
	if message.Extra != nil {
		result.Subtype, _ = message.Extra["subtype"].(string)
		result.IsError, _ = message.Extra["is_error"].(bool)
	}
	if message.ResponseMeta != nil {
		result.FinishReason = message.ResponseMeta.FinishReason
		if usage := message.ResponseMeta.Usage; usage != nil {
			result.Usage = &canonicalUsage{
				PromptTokens:     usage.PromptTokens,
				CompletionTokens: usage.CompletionTokens,
				TotalTokens:      usage.TotalTokens,
			}
		}
	}
	for _, call := range message.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, canonicalToolCall{
			Index:     cloneInt(call.Index),
			ID:        normalizer.identity(call.ID),
			Name:      call.Function.Name,
			Arguments: normalizer.canonicalJSON(call.Function.Arguments),
		})
	}
	return result
}

func canonicalizeToolSchemas(normalizer *canonicalTraceNormalizer, toolInfos []*schema.ToolInfo) []canonicalToolSchema {
	result := make([]canonicalToolSchema, 0, len(toolInfos))
	for _, info := range toolInfos {
		if info == nil {
			continue
		}
		var parameters any
		if info.ParamsOneOf != nil {
			if schemaValue, err := info.ToJSONSchema(); err == nil {
				encoded, _ := jsonMarshalNoEscape(schemaValue)
				var decoded any
				if json.Unmarshal(encoded, &decoded) == nil {
					parameters = normalizer.value(decoded)
				}
			}
		}
		result = append(result, canonicalToolSchema{
			Name:        info.Name,
			Description: canonicalTraceText(info.Desc),
			Parameters:  parameters,
		})
	}
	return result
}

func canonicalEventPayload(normalizer *canonicalTraceNormalizer, event QueryEvent) string {
	payload := make(map[string]any)
	message := event.Message
	switch event.Type {
	case EventAssistant:
		if event.AssistantMessage != nil {
			message = event.AssistantMessage
		}
	case EventToolResult:
		message = event.ToolResultMessage
	case EventAttachment:
		message = event.AttachmentMessage
	case EventCompactBoundary:
		message = event.CompactBoundaryMessage
	case EventStream:
		message = event.StreamEvent
	}
	if message != nil {
		canonicalMessage := canonicalizeMessage(normalizer, message)
		if message.Extra != nil && message.Extra["attachment_kind"] == "system_api_error" {
			canonicalMessage.Content = "retry warning <delay>"
		}
		payload["message"] = canonicalMessage
		if message.Extra != nil {
			for _, key := range []string{
				"attachment_kind", "level", "attempt", "is_429", "is_529",
				"command_mode", "command_priority", "command_provenance",
			} {
				if value, ok := message.Extra[key]; ok {
					payload[key] = normalizer.value(value)
				}
			}
		}
	}
	if event.CommandLifecycle != nil {
		payload["command_uuid"] = normalizer.identity(event.CommandLifecycle.CommandUUID)
		payload["phase"] = event.CommandLifecycle.Phase
	}
	if event.MaxTurnsInfo != nil {
		payload["max_turns"] = event.MaxTurnsInfo.MaxTurns
		payload["turn_count"] = event.MaxTurnsInfo.TurnCount
	}
	if event.Type == EventUserInterruption {
		payload["tool_use"] = event.InterruptionToolUse
	}
	if event.TombstoneUUID != "" {
		payload["tombstone_uuid"] = normalizer.identity(event.TombstoneUUID)
	}
	if event.ModelAttempt != nil {
		attempt := event.ModelAttempt
		payload["logical_request_id"] = normalizer.identity(
			attempt.LogicalRequestID,
		)
		if attempt.LogicalRoundID != "" {
			payload["logical_round_id"] = normalizer.identity(
				attempt.LogicalRoundID,
			)
		}
		if attempt.AttemptID != "" {
			payload["attempt_id"] = normalizer.identity(attempt.AttemptID)
		}
		payload["attempt_index"] = attempt.AttemptIndex
		if attempt.Role != "" {
			payload["role"] = attempt.Role
		}
		if attempt.Profile != "" {
			payload["profile"] = attempt.Profile
		}
		if attempt.Provider != "" {
			payload["provider"] = attempt.Provider
		}
		if attempt.APIModel != "" {
			payload["api_model"] = attempt.APIModel
		}
		if attempt.RouteIdentityDigest != "" {
			payload["route_identity_digest"] = attempt.RouteIdentityDigest
		}
		if attempt.Phase != "" {
			payload["attempt_phase"] = attempt.Phase
		}
		if attempt.FailureClass != "" {
			payload["failure_class"] = attempt.FailureClass
		}
		if attempt.AdmissionCode != "" {
			payload["admission_code"] = attempt.AdmissionCode
		}
		payload["retry_count"] = attempt.RetryCount
		payload["switch_count"] = attempt.SwitchCount
		payload["provider_call_count"] = attempt.ProviderCallCount
		if attempt.OutputDisposition != "" {
			payload["output_disposition"] = attempt.OutputDisposition
		}
	}
	if event.Type == EventCanonicalProjection &&
		event.CanonicalProjection != nil {
		projection := event.CanonicalProjection
		payload["canonical_kind"] = projection.Kind
		if projection.Assistant != nil {
			payload["message_id"] = normalizer.identity(
				projection.Assistant.MessageID,
			)
			payload["delta"] = normalizer.text(
				string(projection.Assistant.Delta),
			)
		}
		if projection.Tool != nil {
			payload["tool_call_id"] = normalizer.identity(
				projection.Tool.ToolCallID,
			)
			if projection.Tool.ToolName != "" {
				payload["tool_name"] = projection.Tool.ToolName
			}
			if len(projection.Tool.EffectiveInput) > 0 {
				var input any
				if json.Unmarshal(
					projection.Tool.EffectiveInput,
					&input,
				) == nil {
					payload["effective_input"] = normalizer.value(input)
				}
			}
			if projection.Tool.Content != "" {
				payload["content"] = normalizer.text(
					projection.Tool.Content,
				)
			}
			if projection.Tool.Outcome != "" {
				payload["outcome"] = projection.Tool.Outcome
			}
			if len(projection.Tool.RawOutput) > 0 {
				var output any
				if json.Unmarshal(
					projection.Tool.RawOutput,
					&output,
				) == nil {
					payload["raw_output"] = normalizer.value(output)
				}
			}
		}
	}
	if len(payload) == 0 {
		return ""
	}
	encoded, _ := jsonMarshalNoEscape(normalizer.value(payload))
	return string(encoded)
}

func canonicalTraceText(value string) string {
	const keep = 96
	if len(value) <= keep*2 {
		return value
	}
	return fmt.Sprintf("%s<...len=%d,digest=%s...>%s", value[:keep], len(value), canonicalDigest(value), value[len(value)-keep:])
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTraceWithoutTesting(trace canonicalTrace) canonicalTrace {
	encoded, _ := json.Marshal(trace)
	var cloned canonicalTrace
	_ = json.Unmarshal(encoded, &cloned)
	return cloned
}

func jsonMarshalNoEscape(value any) ([]byte, error) {
	var builder strings.Builder
	encoder := json.NewEncoder(&builder)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(builder.String(), "\n")), nil
}

type canonicalScriptModel struct {
	mu        sync.Mutex
	responses []canonicalModelResponse
	callCount int
}

type canonicalModelResponse struct {
	chunks []*schema.Message
	err    error
}

func (m *canonicalScriptModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *canonicalScriptModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.callCount
	m.callCount++
	if index >= len(m.responses) {
		return schema.StreamReaderFromArray([]*schema.Message{{Role: schema.Assistant, Content: "done"}}), nil
	}
	response := m.responses[index]
	if response.err != nil {
		return nil, response.err
	}
	return schema.StreamReaderFromArray(response.chunks), nil
}

func TestCanonicalProjectGraphQueryTrace(t *testing.T) {
	kernel := productionQueryKernel()
	if got := kernel.kind(); got != queryKernelProjectGraph {
		t.Fatalf("production kernel = %q, want %q", got, queryKernelProjectGraph)
	}
	fixtures := canonicalQueryFixtures()
	seenKinds := make(map[string]bool)
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			first := fixture.run(t, kernel)
			second := fixture.run(t, kernel)
			if diffs := diffCanonicalTraces(first, second); len(diffs) > 0 || !reflect.DeepEqual(first, second) {
				t.Fatalf("fixture is not deterministic: diffs=%#v\nfirst=%#v\nsecond=%#v", diffs, first, second)
			}
			for _, record := range first.Records {
				seenKinds[record.Kind] = true
			}
			assertCanonicalGolden(t, first)
		})
	}
	for _, kind := range []string{"model_request", "stream", "tool", "event", "state_boundary", "terminal"} {
		if !seenKinds[kind] {
			t.Fatalf("canonical suite did not emit record kind %q", kind)
		}
	}
}

func TestP1310ProductionProjectGraphMatchesCompiledGraphFixtures(t *testing.T) {
	graphKernel, err := newProjectGraphQueryKernel(context.Background())
	if err != nil {
		t.Fatalf("build project graph query kernel: %v", err)
	}
	productionKernel := productionQueryKernel()
	for _, fixture := range canonicalQueryFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			production := fixture.run(t, productionKernel)
			graph := fixture.run(t, graphKernel)
			if diffs := diffCanonicalTraces(production, graph); len(diffs) > 0 ||
				!reflect.DeepEqual(production, graph) {
				t.Fatalf(
					"production project Graph drifted from compiled Graph: diffs=%#v\nproduction=%#v\ngraph=%#v",
					diffs,
					production,
					graph,
				)
			}
		})
	}
	if got := productionKernel.kind(); got != queryKernelProjectGraph {
		t.Fatalf("production kernel = %q, want %q", got, queryKernelProjectGraph)
	}
}

func TestCanonicalTraceRecorderCorrelatesConcurrentSameNameCalls(t *testing.T) {
	recorder := newCanonicalQueryRecorder("same_name_correlation")
	recorder.toolCalls["call-a"] = canonicalToolCall{ID: recorder.normalizer.identity("call-a"), Name: "SameTool", Arguments: `{"value":"a"}`}
	recorder.toolCalls["call-b"] = canonicalToolCall{ID: recorder.normalizer.identity("call-b"), Name: "SameTool", Arguments: `{"value":"b"}`}

	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = recorder.observeToolExecution(
			tools.WithToolUseID(context.Background(), "call-a"),
			"SameTool",
			`{"value":"a"}`,
			func(context.Context, string, string) (string, error) {
				close(firstStarted)
				<-firstRelease
				return "a", nil
			},
		)
	}()
	<-firstStarted
	_, err := recorder.observeToolExecution(
		tools.WithToolUseID(context.Background(), "call-b"),
		"SameTool",
		`{"value":"b"}`,
		func(context.Context, string, string) (string, error) { return "b", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	close(firstRelease)
	<-firstDone

	recorder.mu.Lock()
	recorder.recordToolResultLocked(&schema.Message{Role: schema.Tool, ToolCallID: "call-a", ToolName: "SameTool", Content: "a"})
	recorder.recordToolResultLocked(&schema.Message{Role: schema.Tool, ToolCallID: "call-b", ToolName: "SameTool", Content: "b"})
	trace := cloneTraceWithoutTesting(recorder.trace)
	recorder.mu.Unlock()

	toolsByID := make(map[string]canonicalToolRecord)
	for _, record := range trace.Records {
		if record.Kind == "tool" && record.Tool != nil {
			toolsByID[record.Tool.ID] = *record.Tool
		}
	}
	first := toolsByID[recorder.normalizer.identity("call-a")]
	second := toolsByID[recorder.normalizer.identity("call-b")]
	if first.Input != `{"value":"a"}` || first.StartOrder != 1 || first.FinishOrder != 2 {
		t.Fatalf("first call correlation = %#v", first)
	}
	if second.Input != `{"value":"b"}` || second.StartOrder != 2 || second.FinishOrder != 1 {
		t.Fatalf("second call correlation = %#v", second)
	}
}

type canonicalQueryFixture struct {
	name string
	run  func(*testing.T, queryKernel) canonicalTrace
}

func canonicalQueryFixtures() []canonicalQueryFixture {
	return []canonicalQueryFixture{
		{name: "final_no_tools", run: canonicalFinalNoToolsTrace},
		{name: "stream_args_delta_bridge", run: canonicalDeltaBridgeTrace},
		{name: "stream_args_cumulative", run: canonicalCumulativeTrace},
		{name: "safe_serial_batches", run: canonicalSafeSerialTrace},
		{name: "permission_hooks", run: canonicalPermissionHookTrace},
		{name: "retry_fallback", run: canonicalRetryFallbackTrace},
		{name: "prompt_too_long", run: canonicalPromptTooLongTrace},
		{name: "queue_safe_boundary", run: canonicalQueueTrace},
		{name: "cancellation", run: canonicalCancellationTrace},
		{name: "truncated_tool_call", run: canonicalTruncatedTrace},
		{name: "max_turns", run: canonicalMaxTurnsTrace},
		{name: "query_engine_entrypoint", run: canonicalQueryEngineEntrypointTrace},
	}
}

func assertCanonicalGolden(t *testing.T, trace canonicalTrace) {
	t.Helper()
	actual, err := marshalCanonicalTrace(trace)
	if err != nil {
		t.Fatal(err)
	}
	actual = append(actual, '\n')
	dir := filepath.Join("testdata", "canonical_trace")
	path := filepath.Join(dir, trace.Fixture+".golden.json")
	if *updateCanonicalTraces {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, actual, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read canonical golden %s: %v; regenerate explicitly with go test ./engine -run '^TestCanonicalQueryCompatibilityTrace$' -args -update-canonical-traces", path, err)
	}
	if string(expected) != string(actual) {
		t.Fatalf("canonical trace mismatch for %s\nactual:\n%s\nexpected:\n%s", trace.Fixture, actual, expected)
	}
}

func canonicalFinalNoToolsTrace(t *testing.T, kernel queryKernel) canonicalTrace {
	maxTurns := 2
	maxTokens := 2048
	thinkingBudget := 512
	remainingBudget := 1200
	model := &canonicalScriptModel{responses: []canonicalModelResponse{{chunks: []*schema.Message{{
		Role:             schema.Assistant,
		Content:          "final answer",
		ReasoningContent: "concise reasoning",
		ResponseMeta: &schema.ResponseMeta{
			FinishReason: "stop",
			Usage:        &schema.TokenUsage{PromptTokens: 12, CompletionTokens: 4, TotalTokens: 16},
		},
	}}}}}
	return runCanonicalQuery(t, "final_no_tools", QueryParams{
		Messages:                []*schema.Message{{Role: schema.User, Content: "hello"}},
		SystemPrompt:            &schema.Message{Role: schema.System, Content: "canonical system"},
		ChatModel:               model,
		QuerySource:             QuerySourceSDK,
		MaxTurns:                &maxTurns,
		MaxOutputTokensOverride: &maxTokens,
		TaskBudget:              &TaskBudget{Total: 2048, Remaining: remainingBudget},
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "primary-model",
			ToolChoice:    "auto",
			ThinkingConfig: &ThinkingConfig{
				Type:         "enabled",
				BudgetTokens: &thinkingBudget,
			},
			Tools: []*schema.ToolInfo{{Name: "Read", Desc: "read a file"}, {Name: "Bash", Desc: "run a command"}},
		}},
	}, nil, kernel)
}

func canonicalDeltaBridgeTrace(t *testing.T, kernel queryKernel) canonicalTrace {
	return canonicalStreamArgumentTrace(
		t,
		"stream_args_delta_bridge",
		[]string{`{"value":`, `1}`},
		kernel,
	)
}

func canonicalCumulativeTrace(t *testing.T, kernel queryKernel) canonicalTrace {
	return canonicalStreamArgumentTrace(
		t,
		"stream_args_cumulative",
		[]string{`{}`, `{"value":1}`},
		kernel,
	)
}

func canonicalStreamArgumentTrace(
	t *testing.T,
	fixture string,
	argumentParts []string,
	kernel queryKernel,
) canonicalTrace {
	maxTurns := 4
	accumulated := ""
	chunks := make([]*schema.Message, 0, len(argumentParts)+1)
	for index, arguments := range argumentParts {
		if fixture == "stream_args_delta_bridge" {
			accumulated += arguments
			arguments = accumulated
		}
		streamIndex := 0
		chunk := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			ID:       "stream-call",
			Type:     "function",
			Index:    &streamIndex,
			Function: schema.FunctionCall{Name: "TraceTool", Arguments: arguments},
		}}}
		if index == len(argumentParts)-1 {
			chunk.ResponseMeta = &schema.ResponseMeta{
				FinishReason: "tool_calls",
				Usage:        &schema.TokenUsage{PromptTokens: 8, CompletionTokens: 3, TotalTokens: 11},
			}
		}
		chunks = append(chunks, chunk)
	}
	model := &canonicalScriptModel{responses: []canonicalModelResponse{
		{chunks: chunks},
		{chunks: []*schema.Message{{Role: schema.Assistant, Content: "tool complete", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}}},
	}}
	recorder := (*canonicalQueryRecorder)(nil)
	trace := runCanonicalQuery(t, fixture, QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "use trace tool"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		ChatModel:    model,
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "primary-model",
			Tools:         []*schema.ToolInfo{{Name: "TraceTool"}},
		}},
		ToolExecutor: func(ctx context.Context, name, input string) (string, error) {
			return recorder.observeToolExecution(ctx, name, input, func(context.Context, string, string) (string, error) {
				return "trace-result", nil
			})
		},
	}, func(value *canonicalQueryRecorder) { recorder = value }, kernel)
	if model.callCount != 2 {
		t.Fatalf("model calls = %d, want 2", model.callCount)
	}
	return trace
}

func canonicalSafeSerialTrace(t *testing.T, kernel queryKernel) canonicalTrace {
	maxTurns := 4
	toolCalls := []schema.ToolCall{
		canonicalNamedToolCall("safe-a", "SafeA", `{"value":1}`),
		canonicalNamedToolCall("safe-b", "SafeB", `{"value":2}`),
		canonicalNamedToolCall("serial", "Serial", `{"value":3}`),
		canonicalNamedToolCall("safe-c", "SafeC", `{"value":4}`),
		canonicalNamedToolCall("safe-d", "SafeD", `{"value":5}`),
	}
	model := &canonicalScriptModel{responses: []canonicalModelResponse{
		{chunks: []*schema.Message{{Role: schema.Assistant, ToolCalls: toolCalls, ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"}}}},
		{chunks: []*schema.Message{{Role: schema.Assistant, Content: "batched", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}}},
	}}
	registry := tools.NewRegistry()
	toolInfos := make([]*schema.ToolInfo, 0, len(toolCalls))
	for _, call := range toolCalls {
		name := call.Function.Name
		registry.Register(tools.ToolImpl{
			Info: &schema.ToolInfo{Name: name},
			IsConcurrencySafe: func(map[string]any) bool {
				return name != "Serial"
			},
		})
		toolInfos = append(toolInfos, &schema.ToolInfo{Name: name})
	}

	var inFlight atomic.Int32
	var maxConcurrent atomic.Int32
	var executionCount atomic.Int32
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	serialFinished := make(chan struct{})
	firstRelease := make(chan struct{})
	secondRelease := make(chan struct{})
	var firstCount atomic.Int32
	var secondCount atomic.Int32
	executor := func(ctx context.Context, name, input string) (string, error) {
		executionCount.Add(1)
		current := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			previous := maxConcurrent.Load()
			if current <= previous || maxConcurrent.CompareAndSwap(previous, current) {
				break
			}
		}
		switch name {
		case "SafeA", "SafeB":
			if firstCount.Add(1) == 2 {
				close(firstStarted)
				close(firstRelease)
			}
			select {
			case <-firstRelease:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		case "Serial":
			select {
			case <-firstStarted:
			default:
				return "", errors.New("serial barrier started before first safe batch")
			}
			if current != 1 {
				return "", fmt.Errorf("serial barrier overlapped with %d tools", current)
			}
			close(serialFinished)
		case "SafeC", "SafeD":
			select {
			case <-serialFinished:
			default:
				return "", errors.New("second safe batch started before serial barrier finished")
			}
			if secondCount.Add(1) == 2 {
				close(secondStarted)
				close(secondRelease)
			}
			select {
			case <-secondRelease:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "ok:" + name, nil
	}

	var recorder *canonicalQueryRecorder
	trace := runCanonicalQuery(t, "safe_serial_batches", QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "run stable batches"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		ChatModel:    model,
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ToolRegistry: registry,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "primary-model",
			Tools:         toolInfos,
		}},
		ToolExecutor: executor,
	}, func(value *canonicalQueryRecorder) {
		recorder = value
		facts := []canonicalExecutionFact{
			{startOrder: 1, finishOrder: 1, batch: 1},
			{startOrder: 2, finishOrder: 2, batch: 1},
			{startOrder: 3, finishOrder: 3, batch: 2},
			{startOrder: 4, finishOrder: 4, batch: 3},
			{startOrder: 5, finishOrder: 5, batch: 3},
		}
		for index, call := range toolCalls {
			recorder.setToolFact(call.ID, facts[index])
		}
	}, kernel)
	if executionCount.Load() != int32(len(toolCalls)) {
		t.Fatalf("tool executions = %d, want %d", executionCount.Load(), len(toolCalls))
	}
	if maxConcurrent.Load() < 2 {
		t.Fatalf("max concurrency = %d, want at least 2", maxConcurrent.Load())
	}
	select {
	case <-secondStarted:
	default:
		t.Fatal("second safe batch never overlapped")
	}
	if got := canonicalToolNames(trace); !reflect.DeepEqual(got, []string{"SafeA", "SafeB", "Serial", "SafeC", "SafeD"}) {
		t.Fatalf("tool result order = %#v", got)
	}
	return trace
}

func canonicalPermissionHookTrace(t *testing.T, kernel queryKernel) canonicalTrace {
	maxTurns := 4
	toolCalls := []schema.ToolCall{
		canonicalNamedToolCall("denied", "DeniedTool", `{"value":"deny"}`),
		canonicalNamedToolCall("rewrite", "RewriteTool", `{"value":"before"}`),
		canonicalNamedToolCall("stop", "StopTool", `{"value":"stop"}`),
		canonicalNamedToolCall("enter-plan", "EnterPlanMode", `{}`),
	}
	model := &canonicalScriptModel{responses: []canonicalModelResponse{{
		chunks: []*schema.Message{{Role: schema.Assistant, ToolCalls: toolCalls, ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"}}},
	}}}
	registry := tools.NewRegistry()
	infos := make([]*schema.ToolInfo, 0, len(toolCalls))
	for _, call := range toolCalls {
		registry.Register(tools.ToolImpl{Info: &schema.ToolInfo{Name: call.Function.Name}, NeedsPermissions: true})
		infos = append(infos, &schema.ToolInfo{Name: call.Function.Name})
	}
	hookExecutor := hooks.NewExecutor()
	var recorder *canonicalQueryRecorder
	hookExecutor.RegisterPreTool(func(_ context.Context, name, toolUseID string, input map[string]any) *hooks.PreToolHookResult {
		switch name {
		case "RewriteTool":
			return &hooks.PreToolHookResult{UpdatedInput: map[string]any{"value": "after"}}
		case "StopTool":
			recorder.setToolFlags(toolUseID, func(flags *canonicalToolFlags) {
				flags.preventContinuation = true
			})
			return &hooks.PreToolHookResult{PreventContinuation: true, StopReason: "stop requested"}
		default:
			return nil
		}
	})
	hookExecutor.RegisterPostTool(func(_ context.Context, name, _ string, _ map[string]any, result string) *hooks.PostToolHookResult {
		if name == "RewriteTool" {
			return &hooks.PostToolHookResult{UpdatedResult: result + ":post", ReplaceResult: true}
		}
		return nil
	})

	toolContext := &ToolUseContext{Options: &ToolUseOptions{
		MainLoopModel:  "primary-model",
		PermissionMode: permission.ModeDefault,
		Tools:          infos,
	}}
	trace := runCanonicalQuery(t, "permission_hooks", QueryParams{
		Messages:       []*schema.Message{{Role: schema.User, Content: "exercise permission and hooks"}},
		SystemPrompt:   &schema.Message{Role: schema.System, Content: "system"},
		ChatModel:      model,
		QuerySource:    QuerySourceSDK,
		MaxTurns:       &maxTurns,
		ToolRegistry:   registry,
		HookExecutor:   hookExecutor,
		ToolUseContext: toolContext,
		TransitionPermissionMode: func(
			current *ToolUseContext,
			next permission.Mode,
			_ string,
		) (*ToolUseContext, func(), error) {
			recorder.setToolFlags("enter-plan", func(flags *canonicalToolFlags) {
				flags.contextModified = true
			})
			applyPermissionModeToToolContext(current, next)
			return current, nil, nil
		},
		CanUseTool: func(_ context.Context, name string, _ map[string]any, _ *ToolUseContext) (bool, string) {
			if name == "DeniedTool" {
				return false, "fixture policy denied"
			}
			return true, ""
		},
		ToolExecutor: func(ctx context.Context, name, input string) (string, error) {
			return recorder.observeToolExecution(ctx, name, input, func(_ context.Context, toolName, jsonInput string) (string, error) {
				if toolName == "RewriteTool" && jsonInput != `{"value":"after"}` {
					return "", fmt.Errorf("pre-hook input was not applied: %s", jsonInput)
				}
				return "executed:" + toolName, nil
			})
		},
	}, func(value *canonicalQueryRecorder) { recorder = value }, kernel)
	if traceTerminalReason(trace) != string(TerminalHookStopped) {
		t.Fatalf("terminal = %q, want hook_stopped", traceTerminalReason(trace))
	}
	if toolContext.Options.PermissionMode != permission.ModePlan {
		t.Fatalf("permission mode = %q, want plan", toolContext.Options.PermissionMode)
	}
	if !traceToolFlag(trace, "StopTool", func(record canonicalToolRecord) bool { return record.PreventContinuation }) {
		t.Fatal("prevent-continuation hook outcome was not recorded")
	}
	if !traceToolFlag(trace, "EnterPlanMode", func(record canonicalToolRecord) bool { return record.ContextModified }) {
		t.Fatal("context modifier outcome was not recorded")
	}
	return trace
}

func canonicalRetryFallbackTrace(t *testing.T, kernel queryKernel) canonicalTrace {
	maxTurns := 3
	retryModel := &canonicalScriptModel{}
	resolver := &p294FailoverResolver{chain: p294Chain()}
	var recorder *canonicalQueryRecorder
	trace := runCanonicalQuery(t, "retry_fallback", QueryParams{
		Messages:          []*schema.Message{{Role: schema.User, Content: "retry safely"}},
		SystemPrompt:      &schema.Message{Role: schema.System, Content: "system"},
		ChatModel:         retryModel,
		QuerySource:       QuerySourceSDK,
		MaxTurns:          &maxTurns,
		modelResolver:     resolver,
		commandEntrypoint: "headless",
		retryBaseDelay:    time.Millisecond,
		modelCall: &modelCallIdentity{
			Role:      "main",
			Selector:  "primary",
			Profile:   "primary",
			Provider:  "agenticopenai",
			APIModel:  "primary-api",
			Reasoning: "medium",
		},
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "primary",
		}},
	}, func(value *canonicalQueryRecorder) {
		recorder = value
		attempt := 0
		recorder.delegate = func(_ context.Context, _ model.BaseChatModel, _ []*schema.Message, _ *schema.Message, _ []*schema.ToolInfo, opts execution.CallModelOptions) (*execution.CallModelResult, error) {
			attempt++
			switch attempt {
			case 1:
				return nil, errors.New("rate_limit_error: 429 fixture")
			case 2, 3, 4:
				return nil, errors.New("overloaded_error: 529 fixture")
			default:
				return &execution.CallModelResult{
					Model: opts.Model,
					StreamReader: schema.StreamReaderFromArray([]*schema.Message{{
						Role:         schema.Assistant,
						Content:      "fallback success",
						ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
					}}),
				}, nil
			}
		}
	}, kernel)
	requests := traceModelRequests(trace)
	if len(requests) != 5 ||
		requests[0].Model != "primary" ||
		requests[4].Model != "alternate" {
		t.Fatalf("request route = %#v", requests)
	}
	for _, message := range requests[4].Messages {
		if message.Role == string(schema.Assistant) && strings.Contains(message.Content, "fixture") {
			t.Fatalf("failed attempt leaked into successful history: %#v", requests[4].Messages)
		}
	}
	return trace
}

func canonicalPromptTooLongTrace(t *testing.T, kernel queryKernel) canonicalTrace {
	maxTurns := 5
	model := &scriptedOverflowModel{streams: [][]*schema.Message{
		{overflowAPIError("413", "Prompt is too long")},
		{overflowAPIError("413", "Prompt still too long after drain")},
		{{Role: schema.Assistant, Content: "resumed after compact", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}},
	}}
	trace := runCanonicalQuery(t, "prompt_too_long", QueryParams{
		Messages: []*schema.Message{
			{Role: schema.User, Content: strings.Repeat("old context ", 2000)},
			{Role: schema.Assistant, Content: "older answer"},
			{Role: schema.User, Content: "latest question"},
		},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		ChatModel:    model,
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "primary-model",
		}},
	}, nil, kernel)
	if model.callCount != 3 || traceTerminalReason(trace) != string(TerminalCompleted) {
		t.Fatalf("prompt recovery calls=%d terminal=%q", model.callCount, traceTerminalReason(trace))
	}
	if !traceHasStateBoundary(trace, "compact_boundary") {
		t.Fatal("prompt-too-long trace omitted compact boundary")
	}
	return trace
}

func canonicalQueueTrace(t *testing.T, kernel queryKernel) canonicalTrace {
	recorder := newCanonicalQueryRecorder("queue_safe_boundary", t.TempDir())
	mainCoordinator := newTestRuntimeInputCoordinator(t, "canonical-queue-main", "")
	_, err := mainCoordinator.Enqueue(RuntimeItem{
		ID: "main-next", Kind: RuntimeItemUserPrompt, Priority: RuntimePriorityNext,
		Scope: RuntimeInputScope{SessionID: "canonical-queue-main"},
		UserPrompt: &RuntimeUserPrompt{
			Prompt: "next input",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = mainCoordinator.Enqueue(RuntimeItem{
		ID: "main-later", Kind: RuntimeItemUserPrompt, Priority: RuntimePriorityLater,
		Scope: RuntimeInputScope{SessionID: "canonical-queue-main"},
		UserPrompt: &RuntimeUserPrompt{
			Prompt: "later input",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	uuidOrdinal := 0
	deps := &QueryDeps{
		UUID: func() string {
			uuidOrdinal++
			return fmt.Sprintf("queue-generated-%d", uuidOrdinal)
		},
		CallModel: recorder.callModel,
	}
	maxTurns := 4
	mainModel := &canonicalScriptModel{responses: []canonicalModelResponse{
		{chunks: []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{canonicalNamedToolCall("sleep-call", "Sleep", `{"duration":0}`)}, ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"}}}},
		{chunks: []*schema.Message{{Role: schema.Assistant, Content: "main done", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}}},
	}}
	mainParams := QueryParams{
		Messages:         []*schema.Message{{Role: schema.User, Content: "main queue"}},
		SystemPrompt:     &schema.Message{Role: schema.System, Content: "system"},
		ChatModel:        mainModel,
		QuerySource:      QuerySourceSDK,
		MaxTurns:         &maxTurns,
		SessionID:        "canonical-queue-main",
		InputCoordinator: mainCoordinator,
		Deps:             deps,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "primary-model",
			Tools:         []*schema.ToolInfo{{Name: "Sleep"}},
		}},
	}
	collectCalls := 0
	mainParams.CollectRuntimeItems = func(context.Context) error {
		collectCalls++
		if collectCalls != 2 {
			return nil
		}
		_, enqueueErr := mainCoordinator.Enqueue(RuntimeItem{
			ID: "finished-child:1", Kind: RuntimeItemAgentNotification,
			Priority: RuntimePriorityNext,
			Scope:    RuntimeInputScope{SessionID: "canonical-queue-main"},
			IsMeta:   true, Origin: "task-notification",
			AgentNotification: &RuntimeAgentNotification{
				AgentID: "finished-child", Status: "completed",
				Message: "<task-notification><status>completed</status></task-notification>",
			},
		})
		return enqueueErr
	}
	mainParams.ToolExecutor = func(ctx context.Context, name, input string) (string, error) {
		return recorder.observeToolExecution(ctx, name, input, func(context.Context, string, string) (string, error) {
			mustEnqueueRuntimePrompt(
				t, mainCoordinator, "main-now", RuntimePriorityNow, "", "steer during tool",
			)
			return "slept", nil
		})
	}
	mainTerminal := queryWithKernel(
		context.Background(),
		mainParams,
		recorder.recordEvent,
		kernel,
	)
	recorder.finish(mainTerminal, &maxTurns)

	childModel := &canonicalScriptModel{responses: []canonicalModelResponse{
		{chunks: []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{canonicalNamedToolCall("child-tool", "ChildTool", `{}`)}, ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"}}}},
		{chunks: []*schema.Message{{Role: schema.Assistant, Content: "child done", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}}},
	}}
	childCoordinator := newTestRuntimeInputCoordinator(t, "canonical-queue-child", "child-agent")
	mustEnqueueRuntimePrompt(
		t, childCoordinator, "child-next", RuntimePriorityNext, "child-agent", "child input",
	)
	_, err = childCoordinator.Enqueue(RuntimeItem{
		ID: "child-message", Kind: RuntimeItemAgentMessage, Priority: RuntimePriorityNext,
		Scope:  RuntimeInputScope{SessionID: "canonical-queue-child", AgentID: "child-agent"},
		IsMeta: true, Origin: "coordinator", Provenance: "send_message",
		AgentMessage: &RuntimeAgentMessage{
			From: "coordinator", To: "child-agent", Content: "send-message input",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	childParams := QueryParams{
		Messages:         []*schema.Message{{Role: schema.User, Content: "child queue"}},
		SystemPrompt:     &schema.Message{Role: schema.System, Content: "system"},
		ChatModel:        childModel,
		QuerySource:      QuerySourceAgent,
		MaxTurns:         &maxTurns,
		SessionID:        "canonical-queue-child",
		InputCoordinator: childCoordinator,
		Deps:             deps,
		ToolUseContext: &ToolUseContext{
			AgentID: "child-agent",
			Options: &ToolUseOptions{MainLoopModel: "primary-model", Tools: []*schema.ToolInfo{{Name: "ChildTool"}}},
		},
	}
	childParams.ToolExecutor = func(ctx context.Context, name, input string) (string, error) {
		return recorder.observeToolExecution(ctx, name, input, func(context.Context, string, string) (string, error) {
			return "child result", nil
		})
	}
	childTerminal := queryWithKernel(
		context.Background(),
		childParams,
		recorder.recordEvent,
		kernel,
	)
	trace := recorder.finish(childTerminal, &maxTurns)

	for _, want := range []string{"main-now", "main-next", "main-later", "child-next"} {
		if !traceConsumesID(trace, recorder.normalizer.identity(want)) {
			t.Fatalf("queue trace omitted command %q", want)
		}
	}
	if !traceToolResultPrecedesCommand(trace, "Sleep") {
		t.Fatal("queued steering was projected before the running tool settled")
	}
	return trace
}

func canonicalCancellationTrace(t *testing.T, kernel queryKernel) canonicalTrace {
	recorder := newCanonicalQueryRecorder("cancellation", t.TempDir())
	registry := tools.NewRegistry()
	registry.Register(tools.ToolImpl{Info: &schema.ToolInfo{Name: "BlockTool"}, IsConcurrencySafe: func(map[string]any) bool { return true }, InterruptBehavior: "block"})
	registry.Register(tools.ToolImpl{Info: &schema.ToolInfo{Name: "CancelTool"}, IsConcurrencySafe: func(map[string]any) bool { return true }, InterruptBehavior: "cancel"})
	recorder.setToolFact("block-call", canonicalExecutionFact{startOrder: 1, finishOrder: 2, batch: 1})
	recorder.setToolFact("cancel-call", canonicalExecutionFact{startOrder: 2, finishOrder: 1, batch: 1})

	model := &canonicalScriptModel{responses: []canonicalModelResponse{{chunks: []*schema.Message{{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			canonicalNamedToolCall("block-call", "BlockTool", `{"value":1}`),
			canonicalNamedToolCall("cancel-call", "CancelTool", `{"value":2}`),
		},
		ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"},
	}}}}}
	abortCtx, abortCancel := context.WithCancel(context.Background())
	abortController := &AbortController{Ctx: abortCtx, Cancel: abortCancel}
	started := make(chan struct{})
	var startCount atomic.Int32
	maxTurns := 3
	params := QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "cancel safely"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		ChatModel:    model,
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ToolRegistry: registry,
		Deps: &QueryDeps{
			UUID:      func() string { return "cancel-generated" },
			CallModel: recorder.callModel,
		},
		ToolUseContext: &ToolUseContext{
			AbortController: abortController,
			Options:         &ToolUseOptions{MainLoopModel: "primary-model", Tools: []*schema.ToolInfo{{Name: "BlockTool"}, {Name: "CancelTool"}}},
		},
		ToolExecutor: func(_ context.Context, name, _ string) (string, error) {
			if startCount.Add(1) == 2 {
				close(started)
			}
			if name == "BlockTool" {
				select {
				case <-started:
				case <-time.After(2 * time.Second):
					return "", errors.New("cancel sibling did not start")
				}
				abortController.Abort()
				return "block completed", nil
			}
			<-abortController.Ctx.Done()
			return "", errors.New("Interrupted by user")
		},
	}
	terminal := queryWithKernel(
		context.Background(),
		params,
		recorder.recordEvent,
		kernel,
	)
	trace := recorder.finish(terminal, &maxTurns)
	if terminal.Reason != TerminalAbortedTools {
		t.Fatalf("terminal = %q, want aborted_tools", terminal.Reason)
	}
	if got := canonicalToolNames(trace); !reflect.DeepEqual(got, []string{"BlockTool", "CancelTool"}) {
		t.Fatalf("visible tool results = %#v", got)
	}
	if got := traceKindCount(trace, "terminal"); got != 1 {
		t.Fatalf("query terminals = %d, want 1", got)
	}
	return trace
}

func canonicalTruncatedTrace(t *testing.T, kernel queryKernel) canonicalTrace {
	model := &truncatedToolCallModel{}
	maxTurns := 5
	var executions atomic.Int32
	trace := runCanonicalQuery(t, "truncated_tool_call", QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "write a file"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		ChatModel:    model,
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		ToolExecutor: func(context.Context, string, string) (string, error) {
			executions.Add(1)
			return "unexpected", nil
		},
	}, nil, kernel)
	if executions.Load() != 0 {
		t.Fatalf("truncated tool executed %d time(s)", executions.Load())
	}
	barrier := canonicalTraceRecord{Kind: "state_boundary", StateBoundary: &canonicalStateBoundary{
		Name:             "p13_h0_commit_barrier",
		Transition:       "reject_truncated",
		RecoveryCounters: map[string]int{"side_effects": 0},
	}}
	trace.Records = insertBeforeLastTerminal(trace.Records, barrier)
	preH0 := cloneTraceWithoutTesting(trace)
	for index := range preH0.Records {
		if preH0.Records[index].Kind == "tool" &&
			preH0.Records[index].Tool != nil {
			preH0.Records[index].Tool.Admission = "allowed"
		}
	}
	if !traceDiffContains(
		diffCanonicalTraces(trace, preH0),
		traceDiffToolOutcome,
	) {
		t.Fatal("P13.H0 difference was not categorized as a tool outcome")
	}
	return trace
}

func canonicalMaxTurnsTrace(t *testing.T, kernel queryKernel) canonicalTrace {
	recorder := newCanonicalQueryRecorder("max_turns", t.TempDir())
	model := &canonicalScriptModel{responses: []canonicalModelResponse{{chunks: []*schema.Message{{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:       "max-turn-call",
			Type:     "function",
			Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"/tmp/a"}`},
		}},
		ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"},
	}}}}}
	maxTurns := 1
	params := QueryParams{
		Messages:     []*schema.Message{{Role: schema.User, Content: "one turn only"}},
		SystemPrompt: &schema.Message{Role: schema.System, Content: "system"},
		ChatModel:    model,
		QuerySource:  QuerySourceSDK,
		MaxTurns:     &maxTurns,
		Deps: &QueryDeps{
			UUID:      func() string { return "max-turn-generated" },
			CallModel: recorder.callModel,
		},
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "primary-model",
			Tools:         []*schema.ToolInfo{{Name: "Read"}},
		}},
		ToolExecutor: func(ctx context.Context, name, input string) (string, error) {
			return recorder.observeToolExecution(
				ctx,
				name,
				input,
				func(context.Context, string, string) (string, error) {
					return "read result", nil
				},
			)
		},
	}
	terminal := queryWithKernel(
		context.Background(),
		params,
		recorder.recordEvent,
		kernel,
	)
	trace := recorder.finish(terminal, &maxTurns)
	if terminal.Reason != TerminalMaxTurns || terminal.TurnCount != 2 || model.callCount != 1 {
		t.Fatalf(
			"max-turn outcome reason=%q turn=%d requests=%d",
			terminal.Reason,
			terminal.TurnCount,
			model.callCount,
		)
	}
	return trace
}

func canonicalQueryEngineEntrypointTrace(
	t *testing.T,
	kernel queryKernel,
) canonicalTrace {
	fixed := time.Date(2026, 7, 17, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	dir := t.TempDir()
	entryModel := &canonicalScriptModel{responses: []canonicalModelResponse{
		{chunks: []*schema.Message{{Role: schema.Assistant, Content: "entry response one", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}}},
		{chunks: []*schema.Message{{Role: schema.Assistant, Content: "entry response two", ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"}}}},
	}}
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:          "entry-session",
		TranscriptDir:      filepath.Join(dir, "transcripts"),
		CWD:                dir,
		Clock:              func() time.Time { return fixed },
		ChatModel:          entryModel,
		Model:              "entry-model",
		MaxTurns:           2,
		SimpleTools:        true,
		CustomSystemPrompt: "canonical entrypoint system",
	})
	defer engine.Close()
	recorder := newCanonicalQueryRecorder("query_engine_entrypoint", dir)
	ctx := withFixtureQueryKernel(context.Background(), kernel)
	for _, prompt := range []string{"entry prompt one", "entry prompt two"} {
		events, terminal := engine.SubmitMessage(ctx, prompt)
		if terminal.Err != nil {
			t.Fatalf("submit %q: %v", prompt, terminal.Err)
		}
		for event := range events {
			recorder.recordEvent(event)
			if event.Type == EventTerminal && event.TerminalInfo != nil {
				recorder.mu.Lock()
				recorder.trace.Records = append(recorder.trace.Records, canonicalTraceRecord{Kind: "terminal", Terminal: &canonicalTerminalRecord{
					Reason:    string(event.TerminalInfo.Reason),
					TurnCount: event.TerminalInfo.TurnCount,
				}})
				recorder.mu.Unlock()
			}
		}
	}
	if err := engine.RuntimeStateError(); err != nil {
		t.Fatalf("runtime reducer rejected entrypoint trace: %v", err)
	}
	snapshot := engine.RuntimeSnapshot()
	recorder.mu.Lock()
	recorder.trace.Records = append(recorder.trace.Records, canonicalTraceRecord{Kind: "state_boundary", StateBoundary: &canonicalStateBoundary{
		Name:          "runtime_snapshot",
		MessageDigest: canonicalDigest(map[string]any{"revision": snapshot.Revision, "active_thread": snapshot.ActiveThreadID}),
		Transition:    string(snapshot.Threads["entry-session"].Status),
	}})
	trace := cloneTraceWithoutTesting(recorder.trace)
	recorder.mu.Unlock()
	sequences := traceEventSequences(trace)
	for index, sequence := range sequences {
		if sequence != uint64(index+1) {
			t.Fatalf("entrypoint sequences = %#v", sequences)
		}
	}
	if entryModel.callCount != 2 || traceKindCount(trace, "terminal") != 2 {
		t.Fatalf("entrypoint model calls=%d terminals=%d", entryModel.callCount, traceKindCount(trace, "terminal"))
	}
	return trace
}

func canonicalNamedToolCall(id, name, arguments string) schema.ToolCall {
	return schema.ToolCall{ID: id, Type: "function", Function: schema.FunctionCall{Name: name, Arguments: arguments}}
}

func canonicalToolNames(trace canonicalTrace) []string {
	result := make([]string, 0)
	for _, record := range trace.Records {
		if record.Kind == "tool" && record.Tool != nil {
			result = append(result, record.Tool.Name)
		}
	}
	return result
}

func traceHasStateBoundary(trace canonicalTrace, name string) bool {
	for _, record := range trace.Records {
		if record.Kind == "state_boundary" && record.StateBoundary != nil && record.StateBoundary.Name == name {
			return true
		}
	}
	return false
}

func traceToolFlag(trace canonicalTrace, name string, predicate func(canonicalToolRecord) bool) bool {
	for _, record := range trace.Records {
		if record.Kind == "tool" && record.Tool != nil && record.Tool.Name == name && predicate(*record.Tool) {
			return true
		}
	}
	return false
}

func traceKindCount(trace canonicalTrace, kind string) int {
	count := 0
	for _, record := range trace.Records {
		if record.Kind == kind {
			count++
		}
	}
	return count
}

func traceConsumesID(trace canonicalTrace, normalizedID string) bool {
	for _, record := range trace.Records {
		if record.Kind != "event" || record.Event == nil || record.Event.Type != string(EventCommandLifecycle) {
			continue
		}
		if strings.Contains(record.Event.Payload, normalizedID) {
			return true
		}
	}
	return false
}

func traceToolResultPrecedesCommand(trace canonicalTrace, toolName string) bool {
	toolIndex := -1
	commandIndex := -1
	for index, record := range trace.Records {
		if record.Kind == "tool" && record.Tool != nil && record.Tool.Name == toolName && toolIndex == -1 {
			toolIndex = index
		}
		if record.Kind == "state_boundary" && record.StateBoundary != nil && strings.HasPrefix(record.StateBoundary.Name, "queue_") && commandIndex == -1 {
			commandIndex = index
		}
	}
	return toolIndex >= 0 && commandIndex > toolIndex
}

func insertBeforeLastTerminal(records []canonicalTraceRecord, record canonicalTraceRecord) []canonicalTraceRecord {
	index := len(records)
	for i := len(records) - 1; i >= 0; i-- {
		if records[i].Kind == "terminal" {
			index = i
			break
		}
	}
	records = append(records, canonicalTraceRecord{})
	copy(records[index+1:], records[index:])
	records[index] = record
	return records
}

func traceDiffContains(diffs []canonicalTraceDiff, category canonicalTraceDiffCategory) bool {
	for _, diff := range diffs {
		if diff.Category == category {
			return true
		}
	}
	return false
}

func traceEventSequences(trace canonicalTrace) []uint64 {
	result := make([]uint64, 0)
	for _, record := range trace.Records {
		if record.Kind == "event" && record.Event != nil {
			result = append(result, record.Event.Sequence)
		}
	}
	return result
}
