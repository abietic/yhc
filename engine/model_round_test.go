package engine

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/provider"
)

type p135c1ModelRequest struct {
	messages []*schema.Message
	tools    []*schema.ToolInfo
	options  execution.CallModelOptions
}

type p135c1RequestRecorder struct {
	mu       sync.Mutex
	requests []p135c1ModelRequest
	call     func(int, execution.CallModelOptions) (*execution.CallModelResult, error)
}

func (r *p135c1RequestRecorder) callModel(
	_ context.Context,
	_ model.BaseChatModel,
	messages []*schema.Message,
	_ *schema.Message,
	tools []*schema.ToolInfo,
	options execution.CallModelOptions,
) (*execution.CallModelResult, error) {
	r.mu.Lock()
	ordinal := len(r.requests)
	r.requests = append(r.requests, p135c1ModelRequest{
		messages: append([]*schema.Message(nil), messages...),
		tools:    append([]*schema.ToolInfo(nil), tools...),
		options:  options,
	})
	r.mu.Unlock()
	return r.call(ordinal, options)
}

func (r *p135c1RequestRecorder) snapshot() []p135c1ModelRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]p135c1ModelRequest(nil), r.requests...)
}

func TestP135c1ProjectGraphUsesCanonicalModelRoundOptionsAndCommit(t *testing.T) {
	t.Parallel()

	maxTokens := 4096
	thinkingBudget := 512
	remainingBudget := 1024
	var modelToolExecutions atomic.Int32
	var graphToolCalls atomic.Int32
	var eventsMu sync.Mutex
	var eventTypes []QueryEventType
	recorder := &p135c1RequestRecorder{
		call: func(_ int, options execution.CallModelOptions) (*execution.CallModelResult, error) {
			return &execution.CallModelResult{
				Model: options.Model,
				StreamReader: schema.StreamReaderFromArray([]*schema.Message{
					{
						Role:    schema.Assistant,
						Content: "partial",
						ToolCalls: []schema.ToolCall{{
							ID:   "call-1",
							Type: "function",
							Function: schema.FunctionCall{
								Name:      "Read",
								Arguments: `{"file_path":`,
							},
						}},
					},
					{
						Role: schema.Assistant,
						ToolCalls: []schema.ToolCall{{
							ID:   "call-1",
							Type: "function",
							Function: schema.FunctionCall{
								Name:      "Read",
								Arguments: `{"file_path":"/tmp/a"}`,
							},
						}},
						ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls"},
					},
				}),
			}, nil
		},
	}
	params := QueryParams{
		ChatModel:               &canonicalScriptModel{},
		QuerySource:             QuerySourceSDK,
		SessionID:               "session-fallback",
		MaxOutputTokensOverride: &maxTokens,
		TaskBudget:              &TaskBudget{Total: 2048},
		ToolExecutor: func(context.Context, string, string) (string, error) {
			modelToolExecutions.Add(1)
			return "must not execute in model node", nil
		},
		ToolUseContext: &ToolUseContext{
			SessionID: "session-tool-context",
			Options: &ToolUseOptions{
				MainLoopModel: "primary-model",
				ToolChoice:    "auto",
				ThinkingConfig: &ThinkingConfig{
					Type:         "enabled",
					BudgetTokens: &thinkingBudget,
				},
				Tools: []*schema.ToolInfo{{Name: "Read", Desc: "read a file"}},
			},
		},
		repeatedToolGuard: newRepeatedToolCallGuard(),
	}
	inputBuilder := func(
		context.Context,
		projectGraphRound,
	) (canonicalModelRoundInput, error) {
		return canonicalModelRoundInput{
			params:                    params,
			deps:                      &QueryDeps{UUID: func() string { return "uuid" }, CallModel: recorder.callModel},
			messagesForQuery:          []*schema.Message{{Role: schema.User, Content: "read"}},
			fullSystemPrompt:          &schema.Message{Role: schema.System, Content: "system"},
			userContext:               map[string]string{"cwd": "/tmp/project"},
			queryTracking:             &QueryTracking{ChainID: "chain", Depth: 2},
			taskBudgetRemaining:       &remainingBudget,
			maxOutputTokensOverride:   &maxTokens,
			toolUseContext:            params.ToolUseContext,
			cancellationChain:         NewCancellationChain(context.Background()),
			shouldPreventContinuation: false,
			yield: func(event QueryEvent) {
				eventsMu.Lock()
				eventTypes = append(eventTypes, event.Type)
				eventsMu.Unlock()
			},
		}, nil
	}
	runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
		prepare: func(_ context.Context, round projectGraphRound) (projectGraphPreparedRound, error) {
			return projectGraphPreparedRound{Values: round.Values}, nil
		},
		model: bindProjectGraphCanonicalModelRound(inputBuilder),
		tool: func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
			graphToolCalls.Add(1)
			return projectGraphToolRound{
				Decision: projectGraphAfterToolReturn,
				Value:    "tool-round-owned",
			}, nil
		},
	})

	result, err := runnable.Invoke(context.Background(), projectGraphKernelInput{
		RunID: "canonical-tool-round",
	})
	if err != nil {
		t.Fatalf("invoke project graph: %v", err)
	}
	if result.Kind != projectGraphResultReturned || result.Value != "tool-round-owned" {
		t.Fatalf("graph result = %#v", result)
	}
	if got := modelToolExecutions.Load(); got != 0 {
		t.Fatalf("model node executed %d tool side effect(s), want 0", got)
	}
	if got := graphToolCalls.Load(); got != 1 {
		t.Fatalf("graph tool node calls = %d, want 1", got)
	}

	requests := recorder.snapshot()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(requests))
	}
	request := requests[0]
	if request.options.Model != "primary-model" ||
		request.options.ToolChoice != "auto" ||
		request.options.MaxOutputTokens == nil ||
		*request.options.MaxOutputTokens != maxTokens ||
		request.options.TaskBudget == nil ||
		request.options.TaskBudget.Remaining == nil ||
		*request.options.TaskBudget.Remaining != remainingBudget ||
		request.options.QueryTracking == nil ||
		request.options.QueryTracking.ChainID != "chain" ||
		request.options.QueryTracking.Depth != 2 ||
		request.options.SessionID != "session-tool-context" {
		t.Fatalf("provider options = %#v", request.options)
	}
	if got := []string{request.tools[0].Name}; !reflect.DeepEqual(got, []string{"Read"}) {
		t.Fatalf("projected tools = %#v", got)
	}
	if len(request.messages) == 0 ||
		request.messages[len(request.messages)-1].Content != "read" {
		t.Fatalf("prepared messages = %#v", request.messages)
	}
	eventsMu.Lock()
	gotEvents := append([]QueryEventType(nil), eventTypes...)
	eventsMu.Unlock()
	if !reflect.DeepEqual(gotEvents, []QueryEventType{EventAssistant, EventAssistant}) {
		t.Fatalf("model events = %#v", gotEvents)
	}
}

func TestP135c1ProjectGraphBranchesOnlyAfterCommittedModelRound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		chunks        []*schema.Message
		wantKind      projectGraphResultKind
		wantValue     string
		wantToolCalls int32
	}{
		{
			name: "no-tool terminal",
			chunks: []*schema.Message{{
				Role:         schema.Assistant,
				Content:      "done",
				ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
			}},
			wantKind:  projectGraphResultTerminal,
			wantValue: "done",
		},
		{
			name: "truncated tool call is not executable",
			chunks: []*schema.Message{
				{
					Role: schema.Assistant,
					ToolCalls: []schema.ToolCall{{
						ID:       "truncated",
						Type:     "function",
						Function: schema.FunctionCall{Name: "Write", Arguments: `{"file_path":"/tmp/a"}`},
					}},
				},
				{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{FinishReason: "max_tokens"}},
			},
			wantKind: projectGraphResultTerminal,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var graphToolCalls atomic.Int32
			recorder := &p135c1RequestRecorder{
				call: func(_ int, options execution.CallModelOptions) (*execution.CallModelResult, error) {
					return &execution.CallModelResult{
						Model:        options.Model,
						StreamReader: schema.StreamReaderFromArray(test.chunks),
					}, nil
				},
			}
			params := QueryParams{
				ChatModel:         &canonicalScriptModel{},
				QuerySource:       QuerySourceSDK,
				repeatedToolGuard: newRepeatedToolCallGuard(),
			}
			runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
				prepare: func(_ context.Context, round projectGraphRound) (projectGraphPreparedRound, error) {
					return projectGraphPreparedRound{Values: round.Values}, nil
				},
				model: bindProjectGraphCanonicalModelRound(func(
					context.Context,
					projectGraphRound,
				) (canonicalModelRoundInput, error) {
					return canonicalModelRoundInput{
						params:            params,
						deps:              &QueryDeps{UUID: func() string { return "uuid" }, CallModel: recorder.callModel},
						messagesForQuery:  []*schema.Message{{Role: schema.User, Content: "input"}},
						cancellationChain: NewCancellationChain(context.Background()),
						yield:             func(QueryEvent) {},
					}, nil
				}),
				tool: func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
					graphToolCalls.Add(1)
					return projectGraphToolRound{Decision: projectGraphAfterToolReturn}, nil
				},
			})

			result, err := runnable.Invoke(context.Background(), projectGraphKernelInput{
				RunID: test.name,
			})
			if err != nil {
				t.Fatalf("invoke project graph: %v", err)
			}
			if result.Kind != test.wantKind || result.Value != test.wantValue {
				t.Fatalf("graph result = %#v, want kind=%q value=%q", result, test.wantKind, test.wantValue)
			}
			if got := graphToolCalls.Load(); got != test.wantToolCalls {
				t.Fatalf("tool node calls = %d, want %d", got, test.wantToolCalls)
			}
			if requests := recorder.snapshot(); len(requests) != 1 {
				t.Fatalf("provider requests = %d, want 1", len(requests))
			}
		})
	}
}

func TestP135c1ProjectGraphPreservesFallbackRequestCountAndTerminalMapping(t *testing.T) {
	recorder := &p135c1RequestRecorder{
		call: func(ordinal int, options execution.CallModelOptions) (*execution.CallModelResult, error) {
			if ordinal < 3 {
				return nil, errors.New("overloaded_error: 529 fixture")
			}
			return &execution.CallModelResult{
				Model: options.Model,
				StreamReader: schema.StreamReaderFromArray([]*schema.Message{{
					Role:         schema.Assistant,
					Content:      "fallback done",
					ResponseMeta: &schema.ResponseMeta{FinishReason: "stop"},
				}}),
			}, nil
		},
	}
	params := QueryParams{
		ChatModel:     &canonicalScriptModel{},
		FallbackModel: "fallback-model",
		QuerySource:   QuerySourceSDK,
		modelResolver: &p294FailoverResolver{chain: provider.FailoverChainSnapshot{
			Role: "main",
			Primary: provider.RoleCallSnapshot{
				Role:                "main",
				Selector:            "primary-model",
				ProfileID:           "primary",
				Provider:            "agenticclaude",
				APIModel:            "primary-model",
				RouteIdentityDigest: "primary-route",
				ReasoningEffort:     "high",
			},
			Alternates: []provider.FailoverCandidateSnapshot{{
				ProfileID: "fallback",
				Call: provider.RoleCallSnapshot{
					Role:                "main",
					Selector:            "fallback-model",
					ProfileID:           "fallback",
					Provider:            "agenticclaude",
					APIModel:            "fallback-model",
					RouteIdentityDigest: "fallback-route",
				},
			}},
			On:               []string{"overloaded"},
			MaxSwitches:      1,
			MaxProviderCalls: 6,
			MaxElapsedMS:     45000,
		}},
		commandEntrypoint: "headless",
		modelCall: &modelCallIdentity{
			Role:      "main",
			Selector:  "primary-model",
			Profile:   "primary",
			Provider:  "agenticclaude",
			APIModel:  "primary-model",
			Reasoning: "high",
		},
		retryBaseDelay: time.Millisecond,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "primary-model",
			EffortValue:   "high",
		}},
		repeatedToolGuard: newRepeatedToolCallGuard(),
	}
	runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
		prepare: func(_ context.Context, round projectGraphRound) (projectGraphPreparedRound, error) {
			return projectGraphPreparedRound{Values: round.Values}, nil
		},
		model: bindProjectGraphCanonicalModelRound(func(
			context.Context,
			projectGraphRound,
		) (canonicalModelRoundInput, error) {
			return canonicalModelRoundInput{
				params: params,
				deps: &QueryDeps{
					UUID: func() string { return "uuid" },
					CallModel: func(
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
						return recorder.callModel(
							ctx,
							chatModel,
							messages,
							systemPrompt,
							toolInfos,
							opts,
						)
					},
				},
				messagesForQuery:  []*schema.Message{{Role: schema.User, Content: "retry"}},
				toolUseContext:    params.ToolUseContext,
				cancellationChain: NewCancellationChain(context.Background()),
				yield:             func(QueryEvent) {},
			}, nil
		}),
		tool: func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
			return projectGraphToolRound{}, errors.New("tool node must not run")
		},
	})

	result, err := runnable.Invoke(context.Background(), projectGraphKernelInput{
		RunID: "fallback",
	})
	if err != nil {
		t.Fatalf("invoke project graph: %v", err)
	}
	if result.Kind != projectGraphResultTerminal || result.Value != "fallback done" {
		t.Fatalf("graph result = %#v", result)
	}
	requests := recorder.snapshot()
	if len(requests) != 4 {
		t.Fatalf("provider requests = %d, want 4", len(requests))
	}
	models := make([]string, 0, len(requests))
	efforts := make([]string, 0, len(requests))
	for _, request := range requests {
		models = append(models, request.options.Model)
		efforts = append(efforts, request.options.EffortValue)
	}
	if !reflect.DeepEqual(models, []string{
		"primary-model",
		"primary-model",
		"primary-model",
		"fallback-model",
	}) {
		t.Fatalf("provider routes = %#v", models)
	}
	if !reflect.DeepEqual(efforts, []string{"high", "high", "high", ""}) {
		t.Fatalf("provider reasoning effort routes = %#v", efforts)
	}
}

func TestP135c1ProjectGraphCancellationStopsBeforeToolBranch(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	var requests atomic.Int32
	var toolCalls atomic.Int32
	params := QueryParams{
		ChatModel:         &canonicalScriptModel{},
		QuerySource:       QuerySourceSDK,
		repeatedToolGuard: newRepeatedToolCallGuard(),
	}
	runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
		prepare: func(_ context.Context, round projectGraphRound) (projectGraphPreparedRound, error) {
			return projectGraphPreparedRound{Values: round.Values}, nil
		},
		model: bindProjectGraphCanonicalModelRound(func(
			ctx context.Context,
			_ projectGraphRound,
		) (canonicalModelRoundInput, error) {
			return canonicalModelRoundInput{
				params: params,
				deps: &QueryDeps{
					UUID: func() string { return "uuid" },
					CallModel: func(
						callCtx context.Context,
						_ model.BaseChatModel,
						_ []*schema.Message,
						_ *schema.Message,
						_ []*schema.ToolInfo,
						_ execution.CallModelOptions,
					) (*execution.CallModelResult, error) {
						requests.Add(1)
						close(entered)
						<-callCtx.Done()
						return nil, callCtx.Err()
					},
				},
				messagesForQuery:  []*schema.Message{{Role: schema.User, Content: "cancel"}},
				cancellationChain: NewCancellationChain(ctx),
				yield:             func(QueryEvent) {},
			}, nil
		}),
		tool: func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
			toolCalls.Add(1)
			return projectGraphToolRound{Decision: projectGraphAfterToolReturn}, nil
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	type invocation struct {
		result projectGraphKernelResult
		err    error
	}
	done := make(chan invocation, 1)
	go func() {
		result, err := runnable.Invoke(ctx, projectGraphKernelInput{RunID: "cancel"})
		done <- invocation{result: result, err: err}
	}()

	waitP135c0Signal(t, entered, "provider request")
	cancel()
	outcome := waitP135c0Result(t, done, "cancelled model round")
	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("cancellation error = %v, want context canceled", outcome.err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("provider requests = %d, want 1", got)
	}
	if got := toolCalls.Load(); got != 0 {
		t.Fatalf("tool node calls = %d, want 0", got)
	}
}

func TestP135c1ProjectGraphPreservesModelFailuresAndWithheldTerminal(t *testing.T) {
	t.Parallel()

	providerFailure := errors.New("provider fixture failure")
	streamFailure := errors.New("stream fixture failure")
	tests := []struct {
		name          string
		call          func() (*execution.CallModelResult, error)
		wantErr       error
		wantTerminal  TerminalReason
		wantKind      projectGraphResultKind
		wantValue     string
		wantToolCalls int32
	}{
		{
			name: "provider failure remains typed",
			call: func() (*execution.CallModelResult, error) {
				return nil, providerFailure
			},
			wantErr:      providerFailure,
			wantTerminal: TerminalModelError,
		},
		{
			name: "stream failure remains typed",
			call: func() (*execution.CallModelResult, error) {
				reader, writer := schema.Pipe[*schema.Message](2)
				go func() {
					defer writer.Close()
					writer.Send(&schema.Message{
						Role: schema.Assistant,
						ToolCalls: []schema.ToolCall{{
							ID:       "stream-failure-call",
							Type:     "function",
							Function: schema.FunctionCall{Name: "Write", Arguments: `{}`},
						}},
					}, nil)
					writer.Send(nil, streamFailure)
				}()
				return &execution.CallModelResult{StreamReader: reader}, nil
			},
			wantErr:      streamFailure,
			wantTerminal: TerminalModelError,
		},
		{
			name: "withheld truncation is a non-tool terminal",
			call: func() (*execution.CallModelResult, error) {
				return &execution.CallModelResult{
					StreamReader: schema.StreamReaderFromArray([]*schema.Message{
						{
							Role: schema.Assistant,
							ToolCalls: []schema.ToolCall{{
								ID:       "withheld-call",
								Type:     "function",
								Function: schema.FunctionCall{Name: "Write", Arguments: `{}`},
							}},
						},
						{
							Role:    schema.Assistant,
							Content: "output limit",
							Extra: map[string]any{
								"api_error":  true,
								"error_type": "max_output_tokens",
							},
						},
					}),
				}, nil
			},
			wantKind:  projectGraphResultTerminal,
			wantValue: "max_output_tokens",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var toolCalls atomic.Int32
			params := QueryParams{
				ChatModel:         &canonicalScriptModel{},
				QuerySource:       QuerySourceSDK,
				repeatedToolGuard: newRepeatedToolCallGuard(),
			}
			runnable := mustBuildP135c0Graph(t, projectGraphKernelNodes{
				prepare: func(_ context.Context, round projectGraphRound) (projectGraphPreparedRound, error) {
					return projectGraphPreparedRound{Values: round.Values}, nil
				},
				model: bindProjectGraphCanonicalModelRound(func(
					ctx context.Context,
					_ projectGraphRound,
				) (canonicalModelRoundInput, error) {
					return canonicalModelRoundInput{
						params: params,
						deps: &QueryDeps{
							UUID: func() string { return "uuid" },
							CallModel: func(
								context.Context,
								model.BaseChatModel,
								[]*schema.Message,
								*schema.Message,
								[]*schema.ToolInfo,
								execution.CallModelOptions,
							) (*execution.CallModelResult, error) {
								return test.call()
							},
						},
						messagesForQuery:  []*schema.Message{{Role: schema.User, Content: "failure"}},
						cancellationChain: NewCancellationChain(ctx),
						yield:             func(QueryEvent) {},
					}, nil
				}),
				tool: func(context.Context, projectGraphRound) (projectGraphToolRound, error) {
					toolCalls.Add(1)
					return projectGraphToolRound{Decision: projectGraphAfterToolReturn}, nil
				},
			})

			result, err := runnable.Invoke(context.Background(), projectGraphKernelInput{
				RunID: test.name,
			})
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("invoke error = %v, want %v", err, test.wantErr)
				}
				var failure *projectGraphModelRoundError
				if !errors.As(err, &failure) {
					t.Fatalf("invoke error = %T, want projectGraphModelRoundError", err)
				}
				if failure.terminal.Reason != test.wantTerminal ||
					!errors.Is(failure.terminal.Err, test.wantErr) {
					t.Fatalf("terminal = %#v", failure.terminal)
				}
			} else {
				if err != nil {
					t.Fatalf("invoke project graph: %v", err)
				}
				if result.Kind != test.wantKind || result.Value != test.wantValue {
					t.Fatalf(
						"graph result = %#v, want kind=%q value=%q",
						result,
						test.wantKind,
						test.wantValue,
					)
				}
			}
			if got := toolCalls.Load(); got != test.wantToolCalls {
				t.Fatalf("tool node calls = %d, want %d", got, test.wantToolCalls)
			}
		})
	}
}
