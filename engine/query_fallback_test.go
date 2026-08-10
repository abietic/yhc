package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/provider"
)

type fallbackRetryModel struct{}

func (m *fallbackRetryModel) Generate(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.Message, error) {
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *fallbackRetryModel) Stream(
	context.Context,
	[]*schema.Message,
	...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role: schema.Assistant, Content: "done",
	}}), nil
}

type p294FailoverResolver struct {
	mu           sync.Mutex
	chain        provider.FailoverChainSnapshot
	prepared     []string
	prepareErr   map[string]error
	resolveChain func(provider.RoleResolutionInput) provider.FailoverChainSnapshot
}

type p294CallModelFn func(
	context.Context,
	model.BaseChatModel,
	[]*schema.Message,
	*schema.Message,
	[]*schema.ToolInfo,
	execution.CallModelOptions,
) (*execution.CallModelResult, error)

func (r *p294FailoverResolver) ResolveModel(
	modelSpec string,
) (provider.ResolvedConfig, error) {
	return provider.ResolvedConfig{
		Config: provider.Config{
			Provider: provider.ProviderAgenticOpenAI,
			Model:    modelSpec,
		},
	}, nil
}

func (r *p294FailoverResolver) ResolveFailoverChain(
	input provider.RoleResolutionInput,
) (provider.FailoverChainSnapshot, error) {
	if r.resolveChain != nil {
		return r.resolveChain(input), nil
	}
	return r.chain, nil
}

func (r *p294FailoverResolver) PrepareModel(
	_ context.Context,
	modelSpec string,
) (provider.ResolvedConfig, error) {
	r.mu.Lock()
	r.prepared = append(r.prepared, modelSpec)
	err := r.prepareErr[modelSpec]
	r.mu.Unlock()
	if err != nil {
		return provider.ResolvedConfig{}, err
	}
	return r.ResolveModel(modelSpec)
}

func (r *p294FailoverResolver) preparedModels() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.prepared...)
}

func p294FailoverParams(
	entrypoint string,
	chain provider.FailoverChainSnapshot,
	call p294CallModelFn,
) QueryParams {
	resolver := &p294FailoverResolver{chain: chain}
	countedCall := func(
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
		return call(
			ctx,
			chatModel,
			messages,
			systemPrompt,
			toolInfos,
			opts,
		)
	}
	return QueryParams{
		Messages: []*schema.Message{{
			Role: schema.User, Content: "hello",
		}},
		SystemPrompt: &schema.Message{
			Role: schema.System, Content: "You are helpful.",
		},
		QuerySource:       QuerySourceSDK,
		ChatModel:         &fallbackRetryModel{},
		modelResolver:     resolver,
		commandEntrypoint: entrypoint,
		modelCall: &modelCallIdentity{
			Role:      "main",
			Selector:  "primary",
			Profile:   "primary",
			Provider:  "agenticopenai",
			APIModel:  "primary-api",
			Reasoning: "medium",
		},
		ToolUseContext: &ToolUseContext{
			Options: &ToolUseOptions{MainLoopModel: "primary"},
		},
		retryBaseDelay: time.Millisecond,
		Deps: &QueryDeps{
			UUID:      p294IDs(),
			CallModel: countedCall,
		},
	}
}

func p294IDs() func() string {
	next := 0
	return func() string {
		next++
		return fmt.Sprintf("p294-id-%d", next)
	}
}

func p294Chain() provider.FailoverChainSnapshot {
	return provider.FailoverChainSnapshot{
		Role:              "main",
		PortfolioRevision: "revision",
		Primary: provider.RoleCallSnapshot{
			Role:                "main",
			Selector:            "primary",
			ProfileID:           "primary",
			Provider:            "agenticopenai",
			APIModel:            "primary-api",
			PortfolioRevision:   "revision",
			RouteIdentityDigest: "route-primary",
			ReasoningEffort:     "medium",
		},
		Alternates: []provider.FailoverCandidateSnapshot{{
			ProfileID: "alternate",
			Call: provider.RoleCallSnapshot{
				Role:                "main",
				Selector:            "alternate",
				ProfileID:           "alternate",
				Provider:            "agenticopenai",
				APIModel:            "alternate-api",
				PortfolioRevision:   "revision",
				RouteIdentityDigest: "route-primary",
				ReasoningEffort:     "medium",
			},
		}},
		On:               []string{"overloaded"},
		MaxSwitches:      1,
		MaxProviderCalls: 6,
		MaxElapsedMS:     45000,
	}
}

func TestP461CompletePromptFootprintSkipsSmallerContextCandidates(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*QueryParams)
	}{
		{
			name: "system prompt",
			configure: func(params *QueryParams) {
				params.SystemPrompt = &schema.Message{
					Role:    schema.System,
					Content: strings.Repeat("system ", 256),
				}
			},
		},
		{
			name: "tool definition including extra",
			configure: func(params *QueryParams) {
				params.ToolUseContext.Options.Tools = []*schema.ToolInfo{{
					Name: "probe",
					Desc: "probe",
					Extra: map[string]any{
						"context_probe": strings.Repeat("x", 2048),
					},
				}}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			smallLimit := 64
			largeLimit := 4096
			chain := p294Chain()
			chain.MaxSwitches = 2
			small := chain.Alternates[0]
			small.ProfileID = "small"
			small.Call.Selector = "small"
			small.Call.ProfileID = "small"
			small.Call.APIModel = "small-api"
			small.Call.ContextWindowTokens = &smallLimit
			large := small
			large.ProfileID = "large"
			large.Call.Selector = "large"
			large.Call.ProfileID = "large"
			large.Call.APIModel = "large-api"
			large.Call.ContextWindowTokens = &largeLimit
			chain.Alternates = []provider.FailoverCandidateSnapshot{small, large}

			var models []string
			params := p294FailoverParams(
				"headless",
				chain,
				func(
					_ context.Context,
					_ model.BaseChatModel,
					_ []*schema.Message,
					_ *schema.Message,
					_ []*schema.ToolInfo,
					opts execution.CallModelOptions,
				) (*execution.CallModelResult, error) {
					models = append(models, opts.Model)
					if opts.Model == "primary" {
						return nil, errors.New("529 overloaded_error")
					}
					return &execution.CallModelResult{
						StreamReader: schema.StreamReaderFromArray(
							[]*schema.Message{{
								Role: schema.Assistant, Content: "done",
							}},
						),
						Model: opts.Model,
					}, nil
				},
			)
			test.configure(&params)
			resolver := params.modelResolver.(*p294FailoverResolver)
			observedTokens := 0
			resolver.resolveChain = func(
				input provider.RoleResolutionInput,
			) provider.FailoverChainSnapshot {
				observedTokens = input.Requirements.PromptTokens
				admitted := chain
				admitted.Alternates = append(
					[]provider.FailoverCandidateSnapshot(nil),
					chain.Alternates...,
				)
				for index := range admitted.Alternates {
					limit := admitted.Alternates[index].Call.ContextWindowTokens
					if limit != nil && observedTokens > *limit {
						admitted.Alternates[index].AdmissionCode = "context_window"
						admitted.Alternates[index].Call = provider.RoleCallSnapshot{}
					}
				}
				return admitted
			}

			events, terminal := collectEvents(context.Background(), params)
			if terminal.Reason != TerminalCompleted {
				t.Fatalf("terminal = %q (%v)", terminal.Reason, terminal.Err)
			}
			if observedTokens <= smallLimit || observedTokens > largeLimit {
				t.Fatalf(
					"complete prompt tokens = %d, want %d..%d",
					observedTokens,
					smallLimit+1,
					largeLimit,
				)
			}
			if want := []string{
				"primary", "primary", "primary", "large",
			}; !equalStrings(models, want) {
				t.Fatalf("models = %#v, want %#v", models, want)
			}
			if want := []string{
				"primary", "large",
			}; !equalStrings(resolver.preparedModels(), want) {
				t.Fatalf(
					"prepared = %#v, want %#v",
					resolver.preparedModels(),
					want,
				)
			}

			smallSkipped := false
			smallStarted := false
			largeStarted := false
			for _, event := range events {
				if event.Type != EventModelAttempt || event.ModelAttempt == nil {
					continue
				}
				attempt := event.ModelAttempt
				switch {
				case attempt.Profile == "small" &&
					attempt.Phase == ModelAttemptCandidateSkipped:
					smallSkipped = attempt.AdmissionCode == "context_window" &&
						attempt.SwitchCount == 0 &&
						attempt.ProviderCallCount == 3
				case attempt.Profile == "small" &&
					attempt.Phase == ModelAttemptStarted:
					smallStarted = true
				case attempt.Profile == "large" &&
					attempt.Phase == ModelAttemptStarted:
					largeStarted = attempt.AttemptIndex == 1 &&
						attempt.SwitchCount == 1 &&
						attempt.ProviderCallCount == 3
				}
			}
			if !smallSkipped || smallStarted || !largeStarted {
				t.Fatalf(
					"attempt events: smallSkipped=%t smallStarted=%t largeStarted=%t",
					smallSkipped,
					smallStarted,
					largeStarted,
				)
			}
		})
	}
}

func TestP294OverloadRetriesThenSwitchesThroughOneCoordinator(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MAX_RETRIES", "10")
	callCount := 0
	var models []string
	var attribution []struct {
		requestID string
		attemptID string
		attempt   int
		retry     int
		profile   string
	}
	params := p294FailoverParams(
		"headless",
		p294Chain(),
		func(
			_ context.Context,
			_ model.BaseChatModel,
			_ []*schema.Message,
			_ *schema.Message,
			_ []*schema.ToolInfo,
			opts execution.CallModelOptions,
		) (*execution.CallModelResult, error) {
			callCount++
			models = append(models, opts.Model)
			attribution = append(attribution, struct {
				requestID string
				attemptID string
				attempt   int
				retry     int
				profile   string
			}{
				requestID: opts.UsageLogicalRequestID,
				attemptID: opts.UsageModelAttemptID,
				attempt:   opts.UsageModelAttemptIndex,
				retry:     opts.UsageModelRetryIndex,
				profile:   opts.ModelProfile,
			})
			if opts.Model == "primary" {
				return nil, errors.New(
					"POST \"url\": 529 Overloaded overloaded_error",
				)
			}
			return &execution.CallModelResult{
				StreamReader: schema.StreamReaderFromArray([]*schema.Message{{
					Role: schema.Assistant, Content: "done on alternate",
				}}),
				Model: opts.Model,
			}, nil
		},
	)
	resolver := params.modelResolver.(*p294FailoverResolver)

	events, terminal := collectEvents(context.Background(), params)
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %q (%v)", terminal.Reason, terminal.Err)
	}
	if callCount != 4 {
		t.Fatalf("provider calls = %d, want 4", callCount)
	}
	if want := []string{
		"primary", "primary", "primary", "alternate",
	}; !equalStrings(models, want) {
		t.Fatalf("models = %#v, want %#v", models, want)
	}
	for index := 0; index < 3; index++ {
		if attribution[index].requestID == "" ||
			attribution[index].attemptID == "" ||
			attribution[index].attempt != 0 ||
			attribution[index].retry != index ||
			attribution[index].profile != "primary" {
			t.Fatalf(
				"primary attribution[%d] = %#v",
				index,
				attribution[index],
			)
		}
	}
	if attribution[3].requestID != attribution[0].requestID ||
		attribution[3].attemptID == attribution[0].attemptID ||
		attribution[3].attempt != 1 ||
		attribution[3].retry != 0 ||
		attribution[3].profile != "alternate" {
		t.Fatalf("alternate attribution = %#v", attribution[3])
	}
	if want := []string{
		"primary", "alternate",
	}; !equalStrings(resolver.preparedModels(), want) {
		t.Fatalf(
			"prepared = %#v, want %#v",
			resolver.preparedModels(),
			want,
		)
	}
	assertP294AttemptOrder(t, events, []ModelAttemptPhase{
		ModelAttemptStarted,
		ModelAttemptRetryWait,
		ModelAttemptRetryWait,
		ModelAttemptDiscarded,
		ModelAttemptStarted,
		ModelAttemptCommitted,
	})
}

func TestP462DiscardedAttemptPrecedesTombstoneAndFallbackDispatch(
	t *testing.T,
) {
	t.Setenv("CLAUDE_CODE_MAX_RETRIES", "0")
	tests := []struct {
		name        string
		partial     bool
		entrypoint  string
		wantTrace   []string
		wantDispose ModelAttemptOutputDisposition
	}{
		{
			name:       "zero output",
			entrypoint: "headless",
			wantTrace: []string{
				"attempt:0:started:never_started",
				"dispatch:primary",
				"attempt:0:discarded:never_started",
				"attempt:1:started:never_started",
				"dispatch:alternate",
				"assistant:1",
				"attempt:1:committed:committed",
			},
			wantDispose: ModelAttemptOutputNeverStarted,
		},
		{
			name:       "retractable partial output",
			partial:    true,
			entrypoint: "tui",
			wantTrace: []string{
				"attempt:0:started:never_started",
				"dispatch:primary",
				"assistant:0",
				"attempt:0:discarded:discarded",
				"tombstone:0",
				"attempt:1:started:never_started",
				"dispatch:alternate",
				"assistant:1",
				"attempt:1:committed:committed",
			},
			wantDispose: ModelAttemptOutputDiscarded,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var trace []string
			var events []QueryEvent
			params := p294FailoverParams(
				test.entrypoint,
				p294Chain(),
				func(
					_ context.Context,
					_ model.BaseChatModel,
					_ []*schema.Message,
					_ *schema.Message,
					_ []*schema.ToolInfo,
					opts execution.CallModelOptions,
				) (*execution.CallModelResult, error) {
					trace = append(trace, "dispatch:"+opts.Model)
					if opts.Model == "primary" {
						if !test.partial {
							return nil, errors.New("529 overloaded_error")
						}
						reader, writer := schema.Pipe[*schema.Message](2)
						go func() {
							defer writer.Close()
							writer.Send(&schema.Message{
								Role: schema.Assistant, Content: "partial",
							}, nil)
							writer.Send(nil, errors.New("529 overloaded_error"))
						}()
						return &execution.CallModelResult{
							StreamReader: reader,
							Model:        opts.Model,
						}, nil
					}
					return &execution.CallModelResult{
						StreamReader: schema.StreamReaderFromArray(
							[]*schema.Message{{
								Role: schema.Assistant, Content: "done",
							}},
						),
						Model: opts.Model,
					}, nil
				},
			)
			terminal := Query(context.Background(), params, func(event QueryEvent) {
				events = append(events, event)
				switch {
				case event.Type == EventModelAttempt && event.ModelAttempt != nil:
					attempt := event.ModelAttempt
					trace = append(trace, fmt.Sprintf(
						"attempt:%d:%s:%s",
						attempt.AttemptIndex,
						attempt.Phase,
						attempt.OutputDisposition,
					))
				case event.Type == EventTombstone && event.ModelAttempt != nil:
					trace = append(trace, fmt.Sprintf(
						"tombstone:%d",
						event.ModelAttempt.AttemptIndex,
					))
				case event.Type == EventAssistant && event.ModelAttempt != nil:
					trace = append(trace, fmt.Sprintf(
						"assistant:%d",
						event.ModelAttempt.AttemptIndex,
					))
				}
			})
			if terminal.Reason != TerminalCompleted {
				t.Fatalf("terminal = %v", terminal)
			}
			if !equalStrings(trace, test.wantTrace) {
				t.Fatalf("trace = %#v, want %#v", trace, test.wantTrace)
			}

			var discarded *ModelAttemptEvent
			for _, event := range events {
				if event.Type != EventModelAttempt || event.ModelAttempt == nil {
					continue
				}
				attempt := event.ModelAttempt
				if attempt.AttemptIndex == 0 &&
					attempt.Phase == ModelAttemptDiscarded {
					discarded = attempt
				}
				if attempt.AttemptIndex == 0 &&
					attempt.Phase == ModelAttemptFailed {
					t.Fatalf("switched attempt also failed: %#v", attempt)
				}
			}
			if discarded == nil ||
				discarded.FailureClass != string(execution.ModelFailureOverloaded) ||
				discarded.OutputDisposition != test.wantDispose ||
				discarded.ProviderCallCount != 1 ||
				discarded.SwitchCount != 0 ||
				discarded.AttemptID == "" {
				t.Fatalf("discarded attempt = %#v", discarded)
			}
		})
	}
}

func TestP294PartialOutputSwitchesOnlyForRetractableTUI(t *testing.T) {
	for _, test := range []struct {
		name       string
		entrypoint string
		wantCalls  int
		wantDone   bool
		wantTomb   bool
	}{
		{
			name: "tui retracts then switches", entrypoint: "tui",
			wantCalls: 2, wantDone: true, wantTomb: true,
		},
		{
			name: "plain commits on first output", entrypoint: "plain",
			wantCalls: 1, wantDone: false, wantTomb: false,
		},
		{
			name: "acp commits on first output", entrypoint: "acp",
			wantCalls: 1, wantDone: false, wantTomb: false,
		},
		{
			name: "headless commits on first output", entrypoint: "headless",
			wantCalls: 1, wantDone: false, wantTomb: false,
		},
		{
			name: "library defaults to first output commitment", entrypoint: "",
			wantCalls: 1, wantDone: false, wantTomb: false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			params := p294FailoverParams(
				test.entrypoint,
				p294Chain(),
				func(
					_ context.Context,
					_ model.BaseChatModel,
					_ []*schema.Message,
					_ *schema.Message,
					_ []*schema.ToolInfo,
					opts execution.CallModelOptions,
				) (*execution.CallModelResult, error) {
					calls++
					if opts.Model == "primary" {
						reader, writer := schema.Pipe[*schema.Message](2)
						go func() {
							defer writer.Close()
							writer.Send(&schema.Message{
								Role: schema.Assistant, Content: "partial",
							}, nil)
							writer.Send(nil, errors.New(
								"529 overloaded_error",
							))
						}()
						return &execution.CallModelResult{
							StreamReader: reader,
							Model:        opts.Model,
						}, nil
					}
					return &execution.CallModelResult{
						StreamReader: schema.StreamReaderFromArray(
							[]*schema.Message{{
								Role:    schema.Assistant,
								Content: "committed",
							}},
						),
						Model: opts.Model,
					}, nil
				},
			)
			events, terminal := collectEvents(
				context.Background(),
				params,
			)
			if calls != test.wantCalls {
				t.Fatalf("calls = %d, want %d", calls, test.wantCalls)
			}
			if got := terminal.Reason == TerminalCompleted; got != test.wantDone {
				t.Fatalf(
					"completed = %v, want %v (terminal=%v)",
					got,
					test.wantDone,
					terminal,
				)
			}
			tombstoneIndex := -1
			nextAttemptIndex := -1
			for index, event := range events {
				if event.Type == EventTombstone {
					tombstoneIndex = index
				}
				if tombstoneIndex >= 0 &&
					event.Type == EventModelAttempt &&
					event.ModelAttempt != nil &&
					event.ModelAttempt.Phase == ModelAttemptStarted &&
					event.ModelAttempt.AttemptIndex == 1 {
					nextAttemptIndex = index
					break
				}
			}
			if got := tombstoneIndex >= 0; got != test.wantTomb {
				t.Fatalf("tombstone = %v, want %v", got, test.wantTomb)
			}
			if test.wantTomb &&
				(nextAttemptIndex < 0 ||
					tombstoneIndex >= nextAttemptIndex) {
				t.Fatalf(
					"tombstone index %d, next attempt index %d",
					tombstoneIndex,
					nextAttemptIndex,
				)
			}
		})
	}
}

func TestP294ZeroOutputSwitchesAcrossModelEntrypoints(t *testing.T) {
	for _, entrypoint := range []string{"tui", "plain", "headless", "acp", ""} {
		name := entrypoint
		if name == "" {
			name = "library"
		}
		t.Run(name, func(t *testing.T) {
			calls := 0
			params := p294FailoverParams(
				entrypoint,
				p294Chain(),
				func(
					_ context.Context,
					_ model.BaseChatModel,
					_ []*schema.Message,
					_ *schema.Message,
					_ []*schema.ToolInfo,
					opts execution.CallModelOptions,
				) (*execution.CallModelResult, error) {
					calls++
					if opts.Model == "primary" {
						return nil, errors.New("529 overloaded_error")
					}
					return &execution.CallModelResult{
						StreamReader: schema.StreamReaderFromArray(
							[]*schema.Message{{
								Role:    schema.Assistant,
								Content: "alternate",
							}},
						),
						Model: opts.Model,
					}, nil
				},
			)
			_, terminal := collectEvents(context.Background(), params)
			if terminal.Reason != TerminalCompleted || calls != 4 {
				t.Fatalf("terminal=%v calls=%d", terminal, calls)
			}
		})
	}
}

func TestP294CandidateSkipsAreOrderedAndNoCall(t *testing.T) {
	chain := p294Chain()
	chain.MaxSwitches = 2
	chain.Alternates = []provider.FailoverCandidateSnapshot{
		{ProfileID: "primary", Call: chain.Primary},
		{
			ProfileID:     "no-pdf",
			AdmissionCode: "capability_pdf",
		},
		{
			ProfileID: "alternate",
			Call:      chain.Alternates[0].Call,
		},
	}
	var models []string
	params := p294FailoverParams(
		"headless",
		chain,
		func(
			_ context.Context,
			_ model.BaseChatModel,
			_ []*schema.Message,
			_ *schema.Message,
			_ []*schema.ToolInfo,
			opts execution.CallModelOptions,
		) (*execution.CallModelResult, error) {
			models = append(models, opts.Model)
			if opts.Model == "primary" {
				return nil, errors.New("529 overloaded_error")
			}
			return &execution.CallModelResult{
				StreamReader: schema.StreamReaderFromArray(
					[]*schema.Message{{
						Role:    schema.Assistant,
						Content: "alternate",
					}},
				),
				Model: opts.Model,
			}, nil
		},
	)
	resolver := params.modelResolver.(*p294FailoverResolver)
	events, terminal := collectEvents(context.Background(), params)
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %v", terminal)
	}
	if !equalStrings(
		models,
		[]string{"primary", "primary", "primary", "alternate"},
	) {
		t.Fatalf("models = %#v", models)
	}
	if !equalStrings(
		resolver.preparedModels(),
		[]string{"primary", "alternate"},
	) {
		t.Fatalf("prepared models = %#v", resolver.preparedModels())
	}
	var codes []string
	for _, event := range events {
		if event.Type == EventModelAttempt &&
			event.ModelAttempt != nil &&
			event.ModelAttempt.Phase == ModelAttemptCandidateSkipped {
			codes = append(codes, event.ModelAttempt.AdmissionCode)
		}
	}
	if !equalStrings(codes, []string{"duplicate", "capability_pdf"}) {
		t.Fatalf("skip codes = %#v", codes)
	}
}

func TestP294SharedProviderCallAndSwitchBudgetsDoNotReset(t *testing.T) {
	makeThird := func() provider.FailoverCandidateSnapshot {
		return provider.FailoverCandidateSnapshot{
			ProfileID: "third",
			Call: provider.RoleCallSnapshot{
				Role:                "main",
				Selector:            "third",
				ProfileID:           "third",
				Provider:            "agenticopenai",
				APIModel:            "third-api",
				RouteIdentityDigest: "route-third",
			},
		}
	}
	t.Run("provider call exhaustion", func(t *testing.T) {
		chain := p294Chain()
		chain.Alternates = append(chain.Alternates, makeThird())
		chain.MaxSwitches = 2
		chain.MaxProviderCalls = 4
		var models []string
		params := p294FailoverParams(
			"headless",
			chain,
			func(
				_ context.Context,
				_ model.BaseChatModel,
				_ []*schema.Message,
				_ *schema.Message,
				_ []*schema.ToolInfo,
				opts execution.CallModelOptions,
			) (*execution.CallModelResult, error) {
				models = append(models, opts.Model)
				return nil, errors.New("529 overloaded_error")
			},
		)
		resolver := params.modelResolver.(*p294FailoverResolver)
		_, terminal := collectEvents(context.Background(), params)
		if terminal.Reason != TerminalModelError ||
			!errors.Is(
				terminal.Err,
				execution.ErrProviderCallBudgetExhausted,
			) {
			t.Fatalf("terminal = %v", terminal)
		}
		if !equalStrings(
			models,
			[]string{"primary", "primary", "primary", "alternate"},
		) {
			t.Fatalf("models = %#v", models)
		}
		if !equalStrings(
			resolver.preparedModels(),
			[]string{"primary", "alternate"},
		) {
			t.Fatalf("prepared = %#v", resolver.preparedModels())
		}
	})

	t.Run("switch exhaustion", func(t *testing.T) {
		chain := p294Chain()
		chain.Alternates = append(chain.Alternates, makeThird())
		chain.MaxSwitches = 1
		chain.MaxProviderCalls = 6
		var models []string
		params := p294FailoverParams(
			"headless",
			chain,
			func(
				_ context.Context,
				_ model.BaseChatModel,
				_ []*schema.Message,
				_ *schema.Message,
				_ []*schema.ToolInfo,
				opts execution.CallModelOptions,
			) (*execution.CallModelResult, error) {
				models = append(models, opts.Model)
				return nil, errors.New(
					"529 overloaded_error endpoint=https://secret.example token=secret",
				)
			},
		)
		events, terminal := collectEvents(context.Background(), params)
		if terminal.Reason != TerminalModelError ||
			strings.Contains(terminal.Err.Error(), "secret") ||
			!strings.Contains(terminal.Err.Error(), "6 provider calls") ||
			!strings.Contains(terminal.Err.Error(), "1 switches") {
			t.Fatalf("terminal = %v", terminal)
		}
		if len(models) != 6 ||
			models[0] != "primary" ||
			models[3] != "alternate" {
			t.Fatalf("models = %#v", models)
		}
		foundSkip := false
		for _, event := range events {
			if event.Type == EventModelAttempt &&
				event.ModelAttempt != nil &&
				event.ModelAttempt.Phase == ModelAttemptCandidateSkipped &&
				event.ModelAttempt.Profile == "third" &&
				event.ModelAttempt.AdmissionCode ==
					"switch_budget_exhausted" {
				foundSkip = true
			}
		}
		if !foundSkip {
			t.Fatal("switch-exhausted candidate skip was not emitted")
		}
	})
}

func TestP294RouteConstructionAndUsageAmbiguityAreTerminal(t *testing.T) {
	t.Run("route construction", func(t *testing.T) {
		calls := 0
		params := p294FailoverParams(
			"headless",
			p294Chain(),
			func(
				_ context.Context,
				_ model.BaseChatModel,
				_ []*schema.Message,
				_ *schema.Message,
				_ []*schema.ToolInfo,
				_ execution.CallModelOptions,
			) (*execution.CallModelResult, error) {
				calls++
				return nil, errors.New("529 overloaded_error")
			},
		)
		resolver := params.modelResolver.(*p294FailoverResolver)
		resolver.prepareErr = map[string]error{
			"alternate": errors.New("route construction rejected"),
		}
		events, terminal := collectEvents(context.Background(), params)
		if terminal.Reason != TerminalModelError ||
			calls != 3 ||
			!strings.Contains(terminal.Err.Error(), "0 switches") ||
			!equalStrings(
				resolver.preparedModels(),
				[]string{"primary", "alternate"},
			) {
			t.Fatalf(
				"terminal=%v calls=%d prepared=%#v",
				terminal,
				calls,
				resolver.preparedModels(),
			)
		}
		foundSkip := false
		for _, event := range events {
			if event.Type == EventModelAttempt &&
				event.ModelAttempt != nil &&
				event.ModelAttempt.Phase == ModelAttemptCandidateSkipped &&
				event.ModelAttempt.Profile == "alternate" &&
				event.ModelAttempt.AdmissionCode == "route_construction" &&
				event.ModelAttempt.SwitchCount == 0 {
				foundSkip = true
			}
			if event.Type == EventModelAttempt &&
				event.ModelAttempt != nil &&
				event.ModelAttempt.Phase == ModelAttemptStarted &&
				event.ModelAttempt.AttemptIndex > 0 {
				t.Fatal("unconstructable candidate started an attempt")
			}
		}
		if !foundSkip {
			t.Fatal("route-construction skip was not emitted")
		}
	})

	t.Run("route construction skips to later candidate", func(t *testing.T) {
		chain := p294Chain()
		later := chain.Alternates[0]
		later.ProfileID = "later"
		later.Call.Selector = "later"
		later.Call.ProfileID = "later"
		later.Call.APIModel = "later-api"
		chain.Alternates = append(chain.Alternates, later)
		var models []string
		params := p294FailoverParams(
			"headless",
			chain,
			func(
				_ context.Context,
				_ model.BaseChatModel,
				_ []*schema.Message,
				_ *schema.Message,
				_ []*schema.ToolInfo,
				opts execution.CallModelOptions,
			) (*execution.CallModelResult, error) {
				models = append(models, opts.Model)
				if opts.Model == "primary" {
					return nil, errors.New("529 overloaded_error")
				}
				return &execution.CallModelResult{
					StreamReader: schema.StreamReaderFromArray(
						[]*schema.Message{{
							Role:    schema.Assistant,
							Content: "later succeeded",
						}},
					),
					Model: opts.Model,
				}, nil
			},
		)
		resolver := params.modelResolver.(*p294FailoverResolver)
		resolver.prepareErr = map[string]error{
			"alternate": errors.New("route construction rejected"),
		}
		events, terminal := collectEvents(context.Background(), params)
		if terminal.Reason != TerminalCompleted ||
			!equalStrings(
				models,
				[]string{"primary", "primary", "primary", "later"},
			) ||
			!equalStrings(
				resolver.preparedModels(),
				[]string{"primary", "alternate", "later"},
			) {
			t.Fatalf(
				"terminal=%v models=%#v prepared=%#v",
				terminal,
				models,
				resolver.preparedModels(),
			)
		}
		startedLater := false
		for _, event := range events {
			if event.Type == EventModelAttempt &&
				event.ModelAttempt != nil &&
				event.ModelAttempt.Phase == ModelAttemptStarted &&
				event.ModelAttempt.Profile == "later" &&
				event.ModelAttempt.AttemptIndex == 1 &&
				event.ModelAttempt.SwitchCount == 1 {
				startedLater = true
			}
		}
		if !startedLater {
			t.Fatal("later constructable candidate did not own the first switch")
		}
	})

	t.Run("all construction failures retain TUI partial output", func(t *testing.T) {
		chain := p294Chain()
		later := chain.Alternates[0]
		later.ProfileID = "later"
		later.Call.Selector = "later"
		later.Call.ProfileID = "later"
		chain.Alternates = append(chain.Alternates, later)
		calls := 0
		params := p294FailoverParams(
			"tui",
			chain,
			func(
				_ context.Context,
				_ model.BaseChatModel,
				_ []*schema.Message,
				_ *schema.Message,
				_ []*schema.ToolInfo,
				opts execution.CallModelOptions,
			) (*execution.CallModelResult, error) {
				calls++
				reader, writer := schema.Pipe[*schema.Message](2)
				go func() {
					defer writer.Close()
					writer.Send(&schema.Message{
						Role:    schema.Assistant,
						Content: "keep partial",
					}, nil)
					writer.Send(nil, errors.New("529 overloaded_error"))
				}()
				return &execution.CallModelResult{
					StreamReader: reader,
					Model:        opts.Model,
				}, nil
			},
		)
		resolver := params.modelResolver.(*p294FailoverResolver)
		resolver.prepareErr = map[string]error{
			"alternate": errors.New("alternate rejected"),
			"later":     errors.New("later rejected"),
		}
		events, terminal := collectEvents(context.Background(), params)
		if terminal.Reason != TerminalModelError ||
			calls != 1 {
			t.Fatalf("terminal=%v calls=%d", terminal, calls)
		}
		partialVisible := false
		constructionSkips := 0
		for _, event := range events {
			if event.Type == EventTombstone {
				t.Fatal("partial output was retracted without a constructable candidate")
			}
			if event.Type == EventAssistant &&
				event.Message != nil &&
				event.Message.Content == "keep partial" {
				partialVisible = true
			}
			if event.Type == EventModelAttempt &&
				event.ModelAttempt != nil {
				if event.ModelAttempt.SwitchCount != 0 {
					t.Fatalf("construction skip consumed switch: %#v", event.ModelAttempt)
				}
				if event.ModelAttempt.Phase == ModelAttemptCandidateSkipped &&
					event.ModelAttempt.AdmissionCode == "route_construction" {
					constructionSkips++
				}
				if event.ModelAttempt.Phase == ModelAttemptStarted &&
					event.ModelAttempt.AttemptIndex > 0 {
					t.Fatal("unconstructable TUI candidate started an attempt")
				}
			}
		}
		if !partialVisible {
			t.Fatal("attempt partial output was not preserved")
		}
		if constructionSkips != 2 {
			t.Fatalf("construction skips = %d, want 2", constructionSkips)
		}
	})

	t.Run("usage ambiguity", func(t *testing.T) {
		usage := &p294UsageCall{}
		calls := 0
		params := p294FailoverParams(
			"headless",
			p294Chain(),
			func(
				_ context.Context,
				_ model.BaseChatModel,
				_ []*schema.Message,
				_ *schema.Message,
				_ []*schema.ToolInfo,
				opts execution.CallModelOptions,
			) (*execution.CallModelResult, error) {
				calls++
				reader, writer := schema.Pipe[*schema.Message](1)
				go func() {
					defer writer.Close()
					writer.Send(nil, errors.New("529 overloaded_error"))
				}()
				return &execution.CallModelResult{
					StreamReader:      reader,
					Model:             opts.Model,
					ProviderUsageCall: usage,
				}, nil
			},
		)
		events, terminal := collectEvents(context.Background(), params)
		if terminal.Reason != TerminalModelError ||
			calls != 1 ||
			usage.ambiguous != 1 {
			t.Fatalf(
				"terminal=%v calls=%d usage=%#v",
				terminal,
				calls,
				usage,
			)
		}
		for _, event := range events {
			if event.Type == EventModelAttempt &&
				event.ModelAttempt != nil &&
				event.ModelAttempt.Phase == ModelAttemptStarted &&
				event.ModelAttempt.AttemptIndex > 0 {
				t.Fatal("usage ambiguity switched profiles")
			}
		}
	})
}

func TestP294SwitchReplaysImmutableInputWithThinkingCleanup(t *testing.T) {
	chain := p294Chain()
	var captured [][]*schema.Message
	var capturedSystemPrompts []string
	var capturedToolDescriptions []string
	var capturedToolOwners []string
	params := p294FailoverParams(
		"headless",
		chain,
		func(
			_ context.Context,
			_ model.BaseChatModel,
			messages []*schema.Message,
			systemPrompt *schema.Message,
			toolInfos []*schema.ToolInfo,
			opts execution.CallModelOptions,
		) (*execution.CallModelResult, error) {
			cloned, err := cloneProjectGraphMessages(messages)
			if err != nil {
				return nil, err
			}
			captured = append(captured, cloned)
			capturedSystemPrompts = append(
				capturedSystemPrompts,
				systemPrompt.Content,
			)
			capturedToolDescriptions = append(
				capturedToolDescriptions,
				toolInfos[0].Desc,
			)
			capturedToolOwners = append(
				capturedToolOwners,
				toolInfos[0].Extra["owner"].(string),
			)
			messages[0].Content = "mutated provider input"
			systemPrompt.Content = "mutated provider system prompt"
			toolInfos[0].Desc = "mutated provider tool schema"
			toolInfos[0].Extra["owner"] = "mutated"
			if opts.Model == "primary" {
				return nil, errors.New("529 overloaded_error")
			}
			return &execution.CallModelResult{
				StreamReader: schema.StreamReaderFromArray(
					[]*schema.Message{{
						Role:    schema.Assistant,
						Content: "clean alternate",
					}},
				),
				Model: opts.Model,
			}, nil
		},
	)
	params.ToolUseContext.Options.Tools = []*schema.ToolInfo{{
		Name:  "Read",
		Desc:  "read a file",
		Extra: map[string]any{"owner": "project"},
	}}
	params.Messages = []*schema.Message{
		{Role: schema.User, Content: "first"},
		{
			Role:             schema.Assistant,
			Content:          "prior answer",
			ReasoningContent: "provider-bound-signature",
			AssistantGenMultiContent: []schema.MessageOutputPart{
				{
					Type: schema.ChatMessagePartTypeReasoning,
					Reasoning: &schema.MessageOutputReasoning{
						Text:      "provider-bound-signature",
						Signature: "encrypted-private-signature",
					},
				},
				{Type: schema.ChatMessagePartTypeText, Text: "prior answer"},
			},
			ToolCalls: []schema.ToolCall{{
				ID:   "prior-call",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "Read",
					Arguments: `{"file_path":"public.go"}`,
				},
			}},
			Extra: map[string]any{"openai-generated": true},
		},
		{Role: schema.User, Content: "again"},
	}
	_, terminal := collectEvents(context.Background(), params)
	if terminal.Reason != TerminalCompleted || len(captured) != 4 {
		t.Fatalf("terminal=%v captured=%d", terminal, len(captured))
	}
	for index := 0; index < 3; index++ {
		if captured[index][1].ReasoningContent !=
			"provider-bound-signature" {
			t.Fatalf("primary retry %d lost immutable reasoning", index)
		}
		if len(captured[index][1].AssistantGenMultiContent) != 2 ||
			captured[index][1].Extra["openai-generated"] != true {
			t.Fatalf(
				"primary retry %d lost structured private state: %#v",
				index,
				captured[index][1],
			)
		}
	}
	for index := range captured {
		if captured[index][0].Content != "first" {
			t.Fatalf(
				"dispatch %d reused mutated messages: %#v",
				index,
				captured[index],
			)
		}
		if capturedSystemPrompts[index] != "You are helpful." {
			t.Fatalf(
				"dispatch %d reused mutated system prompt: %q",
				index,
				capturedSystemPrompts[index],
			)
		}
		if capturedToolDescriptions[index] != "read a file" ||
			capturedToolOwners[index] != "project" {
			t.Fatalf(
				"dispatch %d reused mutated tool schema: desc=%q owner=%q",
				index,
				capturedToolDescriptions[index],
				capturedToolOwners[index],
			)
		}
	}
	if captured[3][1].ReasoningContent != "" {
		t.Fatalf("alternate replay kept provider-bound thinking: %#v", captured[3][1])
	}
	if len(captured[3][1].AssistantGenMultiContent) != 1 ||
		captured[3][1].AssistantGenMultiContent[0].Type !=
			schema.ChatMessagePartTypeText ||
		captured[3][1].AssistantGenMultiContent[0].Text != "prior answer" {
		t.Fatalf(
			"alternate replay kept provider-bound structured state: %#v",
			captured[3][1],
		)
	}
	if captured[3][1].Extra != nil {
		t.Fatalf(
			"alternate replay kept provider-bound message metadata: %#v",
			captured[3][1].Extra,
		)
	}
	if len(captured[3][1].ToolCalls) != 1 ||
		captured[3][1].ToolCalls[0].ID != "prior-call" ||
		captured[3][1].ToolCalls[0].Function.Arguments !=
			`{"file_path":"public.go"}` {
		t.Fatalf(
			"alternate replay changed public tool history: %#v",
			captured[3][1].ToolCalls,
		)
	}
	if params.Messages[1].ReasoningContent != "provider-bound-signature" {
		t.Fatal("switch mutated caller history")
	}
	if len(params.Messages[1].AssistantGenMultiContent) != 2 ||
		params.Messages[1].Extra["openai-generated"] != true {
		t.Fatalf("switch mutated caller private state: %#v", params.Messages[1])
	}
}

func TestP294FailedToolStreamHasNoCommittedToolSideEffect(t *testing.T) {
	calls := 0
	params := p294FailoverParams(
		"tui",
		p294Chain(),
		func(
			_ context.Context,
			_ model.BaseChatModel,
			_ []*schema.Message,
			_ *schema.Message,
			_ []*schema.ToolInfo,
			opts execution.CallModelOptions,
		) (*execution.CallModelResult, error) {
			calls++
			if opts.Model == "primary" {
				reader, writer := schema.Pipe[*schema.Message](2)
				go func() {
					defer writer.Close()
					writer.Send(&schema.Message{
						Role: schema.Assistant,
						ToolCalls: []schema.ToolCall{{
							ID: "failed-tool",
							Function: schema.FunctionCall{
								Name:      "Read",
								Arguments: `{"file_path":"/tmp/failed"}`,
							},
						}},
					}, nil)
					writer.Send(nil, errors.New("529 overloaded_error"))
				}()
				return &execution.CallModelResult{
					StreamReader: reader,
					Model:        opts.Model,
				}, nil
			}
			return &execution.CallModelResult{
				StreamReader: schema.StreamReaderFromArray(
					[]*schema.Message{{
						Role:    schema.Assistant,
						Content: "safe completion",
					}},
				),
				Model: opts.Model,
			}, nil
		},
	)
	events, terminal := collectEvents(context.Background(), params)
	if terminal.Reason != TerminalCompleted || calls != 2 {
		t.Fatalf("terminal=%v calls=%d", terminal, calls)
	}
	tombstoned := ""
	for _, event := range events {
		if event.Type == EventTombstone {
			tombstoned = event.TombstoneUUID
		}
		if event.Type == EventCanonicalProjection &&
			event.CanonicalProjection != nil &&
			event.CanonicalProjection.Tool != nil {
			t.Fatalf(
				"failed attempt committed a canonical tool projection: %#v",
				event,
			)
		}
		if event.Type == EventToolResult {
			if event.ModelAttempt == nil ||
				event.ModelAttempt.AttemptIndex != 0 {
				t.Fatalf(
					"tool protocol repair escaped failed attempt: %#v",
					event,
				)
			}
		}
	}
	if tombstoned == "" {
		t.Fatal("failed tool attempt was not tombstoned before switching")
	}
}

func TestP294ExplicitLowerRetryLimitRemainsLowerCeiling(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MAX_RETRIES", "0")
	var models []string
	params := p294FailoverParams(
		"headless",
		p294Chain(),
		func(
			_ context.Context,
			_ model.BaseChatModel,
			_ []*schema.Message,
			_ *schema.Message,
			_ []*schema.ToolInfo,
			opts execution.CallModelOptions,
		) (*execution.CallModelResult, error) {
			models = append(models, opts.Model)
			if opts.Model == "primary" {
				return nil, errors.New("529 overloaded_error")
			}
			return &execution.CallModelResult{
				StreamReader: schema.StreamReaderFromArray(
					[]*schema.Message{{
						Role:    schema.Assistant,
						Content: "alternate",
					}},
				),
				Model: opts.Model,
			}, nil
		},
	)
	_, terminal := collectEvents(context.Background(), params)
	if terminal.Reason != TerminalCompleted ||
		!equalStrings(models, []string{"primary", "alternate"}) {
		t.Fatalf("terminal=%v models=%#v", terminal, models)
	}
}

func TestQueryRetryOn429WithSystemAPIErrorAttachment(t *testing.T) {
	callCount := 0
	events, terminal := collectEvents(
		context.Background(),
		QueryParams{
			Messages: []*schema.Message{{
				Role: schema.User, Content: "hello",
			}},
			QuerySource:    QuerySourceSDK,
			ChatModel:      &fallbackRetryModel{},
			retryBaseDelay: time.Millisecond,
			Deps: &QueryDeps{
				UUID: p294IDs(),
				CallModel: func(
					_ context.Context,
					_ model.BaseChatModel,
					_ []*schema.Message,
					_ *schema.Message,
					_ []*schema.ToolInfo,
					opts execution.CallModelOptions,
				) (*execution.CallModelResult, error) {
					callCount++
					if callCount <= 2 {
						return nil, errors.New(
							"POST \"url\": 429 rate_limit_error",
						)
					}
					return &execution.CallModelResult{
						StreamReader: schema.StreamReaderFromArray(
							[]*schema.Message{{
								Role:    schema.Assistant,
								Content: "success",
							}},
						),
						Model: opts.Model,
					}, nil
				},
			},
		},
	)
	if terminal.Reason != TerminalCompleted || callCount != 3 {
		t.Fatalf(
			"terminal=%v calls=%d",
			terminal,
			callCount,
		)
	}
	retries := 0
	for _, event := range events {
		if event.Type == EventAttachment &&
			event.AttachmentMessage != nil &&
			event.AttachmentMessage.Extra["attachment_kind"] ==
				"system_api_error" {
			retries++
		}
	}
	if retries != 2 {
		t.Fatalf("retry attachments = %d, want 2", retries)
	}
}

func TestConversationHistoryDropsPendingAssistantOnTombstone(t *testing.T) {
	history := newConversationHistory([]*schema.Message{{
		Role: schema.User, Content: "hello",
	}})
	history.Observe(QueryEvent{
		Type: EventAssistant,
		Message: &schema.Message{
			Role: schema.Assistant, Content: "partial",
		},
	})
	history.Observe(QueryEvent{Type: EventTombstone})
	messages := history.Messages()
	if len(messages) != 1 ||
		messages[0].Role != schema.User ||
		messages[0].Content != "hello" {
		t.Fatalf("messages after tombstone = %#v", messages)
	}
}

type p294UsageCall struct {
	ambiguous int
}

func (*p294UsageCall) ProviderCallID() string { return "p294-call" }

func (*p294UsageCall) CompleteProviderUsage(*schema.TokenUsage) error {
	return nil
}

func (*p294UsageCall) ReleaseProviderUsageBeforeDispatch() error {
	return nil
}

func (c *p294UsageCall) MarkProviderUsageAmbiguous(error) error {
	c.ambiguous++
	return nil
}

func assertP294AttemptOrder(
	t *testing.T,
	events []QueryEvent,
	want []ModelAttemptPhase,
) {
	t.Helper()
	got := make([]ModelAttemptPhase, 0)
	for _, event := range events {
		if event.Type == EventModelAttempt &&
			event.ModelAttempt != nil {
			got = append(got, event.ModelAttempt.Phase)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("attempt phases = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("attempt phases = %#v, want %#v", got, want)
		}
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
