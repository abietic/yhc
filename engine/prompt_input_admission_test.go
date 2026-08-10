package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/hooks"
	"github.com/abietic/yhc/engine/permission"
	"github.com/abietic/yhc/engine/provider"
)

func TestSubmitPromptInputPreservesOrderedTextAndImageParts(t *testing.T) {
	chatModel := &captureInputModel{}
	hookExecutor := hooks.NewExecutor()
	var hookPrompt string
	hookExecutor.RegisterUserPromptSubmit(
		func(_ context.Context, prompt string) *hooks.UserPromptSubmitHookResult {
			hookPrompt = prompt
			return nil
		},
	)
	engine := newPromptInputTestEngine(
		t,
		chatModel,
		hookExecutor,
		DefaultPromptCapabilityResolver(),
	)

	events, _ := engine.SubmitPromptInput(
		context.Background(),
		NewUntrustedPromptInput(
			NewPromptTextPart("alpha"),
			NewPromptImagePart(
				testUserImagePNGBase64,
				" IMAGE/PNG ",
				PromptImageDetailLow,
			),
			NewPromptTextPart("beta"),
			NewPromptImagePart(
				testUserImagePNGBase64,
				"image/png",
				PromptImageDetailHigh,
			),
		),
	)
	terminal, _ := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", terminal)
	}
	if hookPrompt != "alphabeta" {
		t.Fatalf("hook prompt = %q", hookPrompt)
	}
	if len(chatModel.inputs) != 1 {
		t.Fatalf("model inputs = %d", len(chatModel.inputs))
	}
	user := findPromptInputUserMessage(chatModel.inputs[0], "alphabeta")
	if user == nil {
		t.Fatalf("model messages = %#v", chatModel.inputs[0])
	}
	if len(user.UserInputMultiContent) != 4 {
		t.Fatalf("ordered parts = %#v", user.UserInputMultiContent)
	}
	assertPromptTextPart(t, user.UserInputMultiContent[0], "alpha")
	assertPromptImagePart(
		t,
		user.UserInputMultiContent[1],
		schema.ImageURLDetailLow,
	)
	assertPromptTextPart(t, user.UserInputMultiContent[2], "beta")
	assertPromptImagePart(
		t,
		user.UserInputMultiContent[3],
		schema.ImageURLDetailHigh,
	)
	assertNoActivePromptMedia(t, engine)
}

func TestSubmitPromptInputTextOnlyIsLiteralAndNeedsNoCapability(t *testing.T) {
	chatModel := &captureInputModel{}
	engine := NewQueryEngine(QueryEngineConfig{
		ChatModel:     chatModel,
		CWD:           t.TempDir(),
		TranscriptDir: t.TempDir(),
		MaxTurns:      2,
	})
	t.Cleanup(engine.Close)

	events, _ := engine.SubmitPromptInput(
		context.Background(),
		NewUntrustedPromptInput(NewPromptTextPart("/help")),
	)
	terminal, observed := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", terminal)
	}
	for _, event := range observed {
		if event.Type == EventCommandResult {
			t.Fatalf("literal prompt dispatched as a command: %#v", event)
		}
	}
	if len(chatModel.inputs) != 1 ||
		findPromptInputUserMessage(chatModel.inputs[0], "/help") == nil {
		t.Fatalf("literal prompt did not reach model: %#v", chatModel.inputs)
	}
}

func TestDefaultPromptCapabilityResolverRequiresCanonicalProviderAgreement(t *testing.T) {
	resolver := DefaultPromptCapabilityResolver()
	alias := resolver.ResolvePromptCapability(
		provider.ProviderAgenticClaude,
		"claude-sonnet-4",
	)
	if alias.Status != PromptCapabilitySupported || alias.Source == "" {
		t.Fatalf("canonical alias decision = %#v", alias)
	}
	mismatch := resolver.ResolvePromptCapability(
		provider.ProviderAgenticOpenAI,
		"claude-sonnet-4",
	)
	if mismatch.Status != PromptCapabilityUnknown {
		t.Fatalf("provider mismatch decision = %#v", mismatch)
	}
}

func TestSubmitPromptInputRejectsUnboundedPartCount(t *testing.T) {
	parts := make([]UntrustedPromptPart, maxPromptInputParts+1)
	for index := range parts {
		parts[index] = NewPromptTextPart("private")
	}
	engine := NewQueryEngine(QueryEngineConfig{
		ChatModel:     &captureInputModel{},
		CWD:           t.TempDir(),
		TranscriptDir: t.TempDir(),
	})
	t.Cleanup(engine.Close)
	events, terminal := engine.SubmitPromptInput(
		context.Background(),
		NewUntrustedPromptInput(parts...),
	)
	var admissionErr *PromptInputAdmissionError
	if terminal.Reason != TerminalPromptInputError ||
		!errors.As(terminal.Err, &admissionErr) ||
		admissionErr.ReasonCode != "too_many_parts" ||
		strings.Contains(terminal.Err.Error(), "private") {
		t.Fatalf("terminal = %#v", terminal)
	}
	_, observed := collectPromptInputEvents(t, events)
	if len(observed) != 1 || observed[0].Type != EventTerminal {
		t.Fatalf("events = %#v", observed)
	}
}

func TestSubmitPromptInputFailsClosedBeforeHooksOrDurability(t *testing.T) {
	for _, tc := range []struct {
		name       string
		model      string
		resolver   ModelResolver
		capability PromptCapabilityResolver
		reason     string
	}{
		{
			name:       "missing route resolver",
			model:      "gpt-4o",
			capability: DefaultPromptCapabilityResolver(),
			reason:     "route_unknown",
		},
		{
			name:     "missing capability resolver",
			model:    "gpt-4o",
			resolver: promptInputOpenAIResolver(),
			reason:   "capability_unknown",
		},
		{
			name:     "unsupported capability",
			model:    "gpt-4o",
			resolver: promptInputOpenAIResolver(),
			capability: PromptCapabilityResolverFunc(
				func(provider.Provider, string) PromptCapabilityDecision {
					return PromptCapabilityDecision{
						Status: PromptCapabilityUnsupported,
						Source: "fixture-v1",
					}
				},
			),
			reason: "capability_unsupported",
		},
		{
			name:     "unknown capability",
			model:    "gpt-4o",
			resolver: promptInputOpenAIResolver(),
			capability: PromptCapabilityResolverFunc(
				func(provider.Provider, string) PromptCapabilityDecision {
					return PromptCapabilityDecision{
						Status: PromptCapabilityUnknown,
						Source: "fixture-v1",
					}
				},
			),
			reason: "capability_unknown",
		},
		{
			name:  "registry provider mismatch",
			model: "gpt-4o",
			resolver: ModelResolverFunc(
				func(string) (provider.ResolvedConfig, error) {
					return provider.ResolvedConfig{Config: provider.Config{
						Provider: provider.ProviderAgenticClaude,
						Model:    "gpt-4o",
					}}, nil
				},
			),
			capability: DefaultPromptCapabilityResolver(),
			reason:     "capability_unknown",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chatModel := &captureInputModel{}
			hookCalls := 0
			hookExecutor := hooks.NewExecutor()
			hookExecutor.RegisterUserPromptSubmit(
				func(context.Context, string) *hooks.UserPromptSubmitHookResult {
					hookCalls++
					return nil
				},
			)
			engine := NewQueryEngine(QueryEngineConfig{
				ChatModel:                chatModel,
				CWD:                      t.TempDir(),
				TranscriptDir:            t.TempDir(),
				MaxTurns:                 2,
				Model:                    tc.model,
				ModelResolver:            tc.resolver,
				PromptCapabilityResolver: tc.capability,
				HookExecutor:             hookExecutor,
			})
			t.Cleanup(engine.Close)

			events, terminal := engine.SubmitPromptInput(
				context.Background(),
				NewUntrustedPromptInput(
					NewPromptTextPart("private-prompt"),
					NewPromptImagePart(
						testUserImagePNGBase64,
						"image/png",
						PromptImageDetailAuto,
					),
				),
			)
			var admissionErr *PromptInputAdmissionError
			if terminal.Reason != TerminalPromptInputError ||
				!errors.As(terminal.Err, &admissionErr) ||
				admissionErr.PartIndex != 1 ||
				admissionErr.PartKind != string(promptPartImage) ||
				admissionErr.ReasonCode != tc.reason {
				t.Fatalf("terminal = %#v", terminal)
			}
			if strings.Contains(terminal.Err.Error(), "private-prompt") ||
				strings.Contains(terminal.Err.Error(), testUserImagePNGBase64) {
				t.Fatalf("admission error leaked input: %v", terminal.Err)
			}
			_, observed := collectPromptInputEvents(t, events)
			if len(observed) != 1 ||
				observed[0].Type != EventTerminal ||
				hookCalls != 0 ||
				len(chatModel.inputs) != 0 ||
				len(engine.GetMessages()) != 0 ||
				len(engine.permissionReviewUserIntent) != 0 {
				t.Fatalf(
					"events=%#v hooks=%d model=%d history=%#v intents=%#v",
					observed,
					hookCalls,
					len(chatModel.inputs),
					engine.GetMessages(),
					engine.permissionReviewUserIntent,
				)
			}
			assertEmptyPromptTranscript(t, engine)
			assertNoActivePromptMedia(t, engine)
		})
	}
}

func TestSubmitPromptInputHookRewriteIsUnambiguous(t *testing.T) {
	t.Run("sole text part maps back into ordered prompt", func(t *testing.T) {
		chatModel := &captureInputModel{}
		hookExecutor := hooks.NewExecutor()
		hookExecutor.RegisterUserPromptSubmit(
			func(context.Context, string) *hooks.UserPromptSubmitHookResult {
				return &hooks.UserPromptSubmitHookResult{
					UpdatedPrompt: "rewritten",
				}
			},
		)
		engine := newPromptInputTestEngine(
			t,
			chatModel,
			hookExecutor,
			DefaultPromptCapabilityResolver(),
		)
		events, _ := engine.SubmitPromptInput(
			context.Background(),
			NewUntrustedPromptInput(
				NewPromptTextPart("original"),
				NewPromptImagePart(
					testUserImagePNGBase64,
					"image/png",
					PromptImageDetailAuto,
				),
			),
		)
		terminal, _ := collectPromptInputEvents(t, events)
		if terminal.Reason != TerminalCompleted {
			t.Fatalf("terminal = %#v", terminal)
		}
		user := findPromptInputUserMessage(chatModel.inputs[0], "rewritten")
		if user == nil || len(user.UserInputMultiContent) != 2 {
			t.Fatalf("rewritten prompt = %#v", chatModel.inputs)
		}
		assertPromptTextPart(t, user.UserInputMultiContent[0], "rewritten")
		assertNoActivePromptMedia(t, engine)
	})

	t.Run("multiple text parts fail before events and transcript", func(t *testing.T) {
		chatModel := &captureInputModel{}
		hookExecutor := hooks.NewExecutor()
		hookExecutor.RegisterUserPromptSubmit(
			func(context.Context, string) *hooks.UserPromptSubmitHookResult {
				return &hooks.UserPromptSubmitHookResult{
					UpdatedPrompt: "ambiguous",
				}
			},
		)
		engine := newPromptInputTestEngine(
			t,
			chatModel,
			hookExecutor,
			DefaultPromptCapabilityResolver(),
		)
		events, _ := engine.SubmitPromptInput(
			context.Background(),
			NewUntrustedPromptInput(
				NewPromptTextPart("before"),
				NewPromptImagePart(
					testUserImagePNGBase64,
					"image/png",
					PromptImageDetailAuto,
				),
				NewPromptTextPart("after"),
			),
		)
		terminal, observed := collectPromptInputEvents(t, events)
		var admissionErr *PromptInputAdmissionError
		if terminal.Reason != TerminalPromptInputError ||
			!errors.As(terminal.Err, &admissionErr) ||
			admissionErr.ReasonCode != "ambiguous_hook_rewrite" {
			t.Fatalf("terminal = %#v", terminal)
		}
		if len(observed) != 1 || observed[0].Type != EventTerminal {
			t.Fatalf("pre-admission events escaped: %#v", observed)
		}
		if len(chatModel.inputs) != 0 ||
			len(engine.GetMessages()) != 0 ||
			len(engine.permissionReviewUserIntent) != 0 {
			t.Fatalf(
				"model=%d history=%#v intents=%#v",
				len(chatModel.inputs),
				engine.GetMessages(),
				engine.permissionReviewUserIntent,
			)
		}
		assertEmptyPromptTranscript(t, engine)
		assertNoActivePromptMedia(t, engine)
	})
}

func TestP303RecoveryRouteBindingFreezesCandidateCapabilityAndGeneration(
	t *testing.T,
) {
	engine := newPromptInputTestEngine(
		t,
		&captureInputModel{},
		hooks.NewExecutor(),
		PromptCapabilityResolverFunc(func(
			provider.Provider,
			string,
		) PromptCapabilityDecision {
			return PromptCapabilityDecision{
				Status: PromptCapabilitySupported,
				Source: "p303-fixture-v1",
			}
		}),
	)
	admitted, err := engine.admitPromptInput(
		context.Background(),
		NewUntrustedPromptInput(
			NewPromptTextPart("inspect"),
			NewPromptImagePart(
				testUserImagePNGBase64,
				"image/png",
				PromptImageDetailAuto,
			),
		),
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		engine.releaseAdmittedPrompt(admitted)
	})
	binding, err := engine.bindAdmittedPromptRecoveryRoute(
		admitted,
		"fallback-model",
	)
	if err != nil {
		t.Fatal(err)
	}
	if binding.selectedModelSpec != "fallback-model" ||
		binding.model != "fallback-model" ||
		binding.capabilitySource != "p303-fixture-v1" {
		t.Fatalf("candidate binding = %#v", binding)
	}
	if err := engine.checkAdmittedPromptBinding(
		admitted,
		binding,
		"fallback-model",
	); err != nil {
		t.Fatalf("fresh candidate binding: %v", err)
	}
	engine.mu.Lock()
	engine.promptRouteGeneration++
	engine.mu.Unlock()
	if err := engine.checkAdmittedPromptBinding(
		admitted,
		binding,
		"fallback-model",
	); err == nil {
		t.Fatal("stale recovery route binding remained eligible")
	}
}

func TestSubmitPromptInputDoesNotSendRichTurnToUnadmittedSummaryModel(
	t *testing.T,
) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "2000")
	chatModel := &captureInputModel{}
	summaryModel := &promptInputCountingSummaryModel{}
	engine := newPromptInputTestEngine(
		t,
		chatModel,
		hooks.NewExecutor(),
		DefaultPromptCapabilityResolver(),
	)
	engine.mu.Lock()
	engine.config.SummaryModel = summaryModel
	engine.messages = []*schema.Message{
		{
			Role:    schema.User,
			Content: strings.Repeat("older question ", 220),
		},
		{
			Role:    schema.Assistant,
			Content: strings.Repeat("older answer ", 200),
		},
	}
	engine.mu.Unlock()

	events, _ := engine.SubmitPromptInput(
		context.Background(),
		NewUntrustedPromptInput(
			NewPromptTextPart("inspect"),
			NewPromptImagePart(
				testUserImagePNGBase64,
				"image/png",
				PromptImageDetailAuto,
			),
		),
	)
	terminal, _ := collectPromptInputEvents(t, events)
	if terminal.Reason != TerminalCompleted {
		t.Fatalf("terminal = %#v", terminal)
	}
	if summaryModel.calls != 0 {
		t.Fatalf(
			"unadmitted summary model received rich input: calls=%d",
			summaryModel.calls,
		)
	}
	if len(chatModel.inputs) != 1 ||
		findPromptInputUserMessage(chatModel.inputs[0], "inspect") == nil {
		t.Fatalf("bound model input = %#v", chatModel.inputs)
	}
	assertNoActivePromptMedia(t, engine)
}

func TestAdmittedPromptGenerationAndMediaStoreFailClosed(t *testing.T) {
	t.Run("success destroys store", func(t *testing.T) {
		engine := newPromptInputTestEngine(
			t,
			&captureInputModel{},
			hooks.NewExecutor(),
			DefaultPromptCapabilityResolver(),
		)
		admitted, err := engine.admitPromptInput(
			context.Background(),
			NewUntrustedPromptInput(
				NewPromptImagePart(
					testUserImagePNGBase64,
					"image/png",
					PromptImageDetailAuto,
				),
			),
			"",
		)
		if err != nil {
			t.Fatal(err)
		}
		store := admitted.store
		events, _ := engine.submitMessageWithRuntimeItem(
			context.Background(),
			"",
			nil,
			nil,
			nil,
			admitted,
		)
		terminal, _ := collectPromptInputEvents(t, events)
		if terminal.Reason != TerminalCompleted {
			t.Fatalf("terminal = %#v", terminal)
		}
		assertPromptMediaDestroyed(t, store)
		assertNoActivePromptMedia(t, engine)
	})

	t.Run("hook rejection destroys store", func(t *testing.T) {
		hookExecutor := hooks.NewExecutor()
		hookExecutor.RegisterUserPromptSubmit(
			func(context.Context, string) *hooks.UserPromptSubmitHookResult {
				return &hooks.UserPromptSubmitHookResult{
					Reject:        true,
					RejectReason:  "fixture rejection",
					UpdatedPrompt: "must not override denial",
				}
			},
		)
		chatModel := &captureInputModel{}
		engine := newPromptInputTestEngine(
			t,
			chatModel,
			hookExecutor,
			DefaultPromptCapabilityResolver(),
		)
		admitted, err := engine.admitPromptInput(
			context.Background(),
			NewUntrustedPromptInput(
				NewPromptTextPart("before"),
				NewPromptImagePart(
					testUserImagePNGBase64,
					"image/png",
					PromptImageDetailAuto,
				),
				NewPromptTextPart("after"),
			),
			"",
		)
		if err != nil {
			t.Fatal(err)
		}
		store := admitted.store
		events, _ := engine.submitMessageWithRuntimeItem(
			context.Background(),
			"",
			nil,
			nil,
			nil,
			admitted,
		)
		terminal, _ := collectPromptInputEvents(t, events)
		if terminal.Reason != TerminalHookStopped {
			t.Fatalf("terminal = %#v", terminal)
		}
		if len(chatModel.inputs) != 0 {
			t.Fatalf("hook rejection reached model: %#v", chatModel.inputs)
		}
		assertPromptMediaDestroyed(t, store)
		assertNoActivePromptMedia(t, engine)
	})

	t.Run("model error destroys store", func(t *testing.T) {
		chatModel := &promptInputErrorModel{}
		engine := newPromptInputTestEngine(
			t,
			chatModel,
			hooks.NewExecutor(),
			DefaultPromptCapabilityResolver(),
		)
		admitted, err := engine.admitPromptInput(
			context.Background(),
			NewUntrustedPromptInput(
				NewPromptImagePart(
					testUserImagePNGBase64,
					"image/png",
					PromptImageDetailAuto,
				),
			),
			"",
		)
		if err != nil {
			t.Fatal(err)
		}
		store := admitted.store
		events, _ := engine.submitMessageWithRuntimeItem(
			context.Background(),
			"",
			nil,
			nil,
			nil,
			admitted,
		)
		terminal, _ := collectPromptInputEvents(t, events)
		if terminal.Reason != TerminalModelError || chatModel.calls != 1 {
			t.Fatalf("terminal=%#v calls=%d", terminal, chatModel.calls)
		}
		assertPromptMediaDestroyed(t, store)
		assertNoActivePromptMedia(t, engine)
	})

	t.Run("model mutation invalidates admitted route", func(t *testing.T) {
		chatModel := &captureInputModel{}
		engine := newPromptInputTestEngine(
			t,
			chatModel,
			hooks.NewExecutor(),
			PromptCapabilityResolverFunc(
				func(provider.Provider, string) PromptCapabilityDecision {
					return PromptCapabilityDecision{
						Status: PromptCapabilitySupported,
						Source: "fixture-v1",
					}
				},
			),
		)
		engine.config.ModelResolver = ModelResolverFunc(
			func(modelSpec string) (provider.ResolvedConfig, error) {
				return provider.ResolvedConfig{Config: provider.Config{
					Provider: provider.ProviderAgenticOpenAI,
					Model:    modelSpec,
				}}, nil
			},
		)
		admitted, err := engine.admitPromptInput(
			context.Background(),
			NewUntrustedPromptInput(
				NewPromptImagePart(
					testUserImagePNGBase64,
					"image/png",
					PromptImageDetailAuto,
				),
			),
			"",
		)
		if err != nil {
			t.Fatal(err)
		}
		store := admitted.store
		if _, err := engine.ChangeModel(
			context.Background(),
			"other-model",
		); err != nil {
			t.Fatal(err)
		}
		events, terminal := engine.submitMessageWithRuntimeItem(
			context.Background(),
			"",
			nil,
			nil,
			nil,
			admitted,
		)
		if terminal.Reason != TerminalPromptInputError {
			t.Fatalf("terminal = %#v", terminal)
		}
		collectPromptInputEvents(t, events)
		if len(chatModel.inputs) != 0 {
			t.Fatalf("stale input reached model: %#v", chatModel.inputs)
		}
		assertPromptMediaDestroyed(t, store)
		assertNoActivePromptMedia(t, engine)
	})

	t.Run("cancellation destroys store", func(t *testing.T) {
		engine := newPromptInputTestEngine(
			t,
			&captureInputModel{},
			hooks.NewExecutor(),
			DefaultPromptCapabilityResolver(),
		)
		admitted, err := engine.admitPromptInput(
			context.Background(),
			NewUntrustedPromptInput(
				NewPromptImagePart(
					testUserImagePNGBase64,
					"image/png",
					PromptImageDetailAuto,
				),
			),
			"",
		)
		if err != nil {
			t.Fatal(err)
		}
		store := admitted.store
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		events, _ := engine.submitMessageWithRuntimeItem(
			ctx,
			"",
			nil,
			nil,
			nil,
			admitted,
		)
		collectPromptInputEvents(t, events)
		assertPromptMediaDestroyed(t, store)
		assertNoActivePromptMedia(t, engine)
	})

	t.Run("close destroys all active stores", func(t *testing.T) {
		engine := newPromptInputTestEngine(
			t,
			&captureInputModel{},
			hooks.NewExecutor(),
			DefaultPromptCapabilityResolver(),
		)
		admitted, err := engine.admitPromptInput(
			context.Background(),
			NewUntrustedPromptInput(
				NewPromptImagePart(
					testUserImagePNGBase64,
					"image/png",
					PromptImageDetailAuto,
				),
			),
			"",
		)
		if err != nil {
			t.Fatal(err)
		}
		store := admitted.store
		engine.Close()
		assertPromptMediaDestroyed(t, store)
		assertNoActivePromptMedia(t, engine)
	})
}

func TestPromptRouteGenerationAdvancesOnPlanPhaseChanges(t *testing.T) {
	engine := NewQueryEngine(QueryEngineConfig{
		CWD:           t.TempDir(),
		TranscriptDir: t.TempDir(),
	})
	t.Cleanup(engine.Close)
	initial := engine.promptRouteGeneration
	_, changed, err := engine.applyPlanTransition(planTransitionRequest{
		Source:    planTransitionExternal,
		RequestID: "enter-plan",
		Mode:      permission.ModePlan,
	})
	if err != nil || !changed {
		t.Fatalf("enter plan changed=%v err=%v", changed, err)
	}
	if engine.promptRouteGeneration != initial+1 {
		t.Fatalf(
			"generation after enter = %d, want %d",
			engine.promptRouteGeneration,
			initial+1,
		)
	}
	_, changed, err = engine.applyPlanTransition(planTransitionRequest{
		Source:    planTransitionExternal,
		RequestID: "same-plan",
		Mode:      permission.ModePlan,
	})
	if err != nil || changed {
		t.Fatalf("same plan changed=%v err=%v", changed, err)
	}
	if engine.promptRouteGeneration != initial+1 {
		t.Fatalf("same phase advanced generation")
	}
	_, changed, err = engine.applyPlanTransition(planTransitionRequest{
		Source:    planTransitionExternal,
		RequestID: "exit-plan",
		Mode:      permission.ModeDefault,
	})
	if err != nil || !changed {
		t.Fatalf("exit plan changed=%v err=%v", changed, err)
	}
	if engine.promptRouteGeneration != initial+2 {
		t.Fatalf(
			"generation after exit = %d, want %d",
			engine.promptRouteGeneration,
			initial+2,
		)
	}
}

type promptInputErrorModel struct {
	calls int
}

type promptInputCountingSummaryModel struct {
	calls int
}

func (m *promptInputCountingSummaryModel) Generate(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.Message, error) {
	m.calls++
	return &schema.Message{
		Role:    schema.Assistant,
		Content: "fixture summary",
	}, nil
}

func (m *promptInputCountingSummaryModel) Stream(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	return schema.StreamReaderFromArray(
		[]*schema.Message{{Role: schema.Assistant, Content: "fixture summary"}},
	), nil
}

func (m *promptInputErrorModel) Generate(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.Message, error) {
	return nil, errors.New("fixture provider failure")
}

func (m *promptInputErrorModel) Stream(
	context.Context,
	[]*schema.Message,
	...einomodel.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	return nil, errors.New("fixture provider failure")
}

func newPromptInputTestEngine(
	t *testing.T,
	chatModel einomodel.BaseChatModel,
	hookExecutor *hooks.Executor,
	capabilityResolver PromptCapabilityResolver,
) *QueryEngine {
	t.Helper()
	engine := NewQueryEngine(QueryEngineConfig{
		ChatModel:                chatModel,
		CWD:                      t.TempDir(),
		TranscriptDir:            t.TempDir(),
		MaxTurns:                 2,
		Model:                    "gpt-4o",
		ModelResolver:            promptInputOpenAIResolver(),
		PromptCapabilityResolver: capabilityResolver,
		HookExecutor:             hookExecutor,
	})
	t.Cleanup(engine.Close)
	return engine
}

func promptInputOpenAIResolver() ModelResolver {
	return ModelResolverFunc(
		func(modelSpec string) (provider.ResolvedConfig, error) {
			return provider.ResolvedConfig{Config: provider.Config{
				Provider: provider.ProviderAgenticOpenAI,
				Model:    modelSpec,
			}}, nil
		},
	)
}

func collectPromptInputEvents(
	t *testing.T,
	events <-chan QueryEvent,
) (Terminal, []QueryEvent) {
	t.Helper()
	observed := make([]QueryEvent, 0)
	var terminal *Terminal
	for event := range events {
		observed = append(observed, event)
		if event.Type == EventTerminal && event.TerminalInfo != nil {
			copy := *event.TerminalInfo
			terminal = &copy
		}
	}
	if terminal == nil {
		t.Fatalf("event stream has no terminal: %#v", observed)
	}
	return *terminal, observed
}

func findPromptInputUserMessage(
	messages []*schema.Message,
	content string,
) *schema.Message {
	for _, message := range messages {
		if message != nil &&
			message.Role == schema.User &&
			message.Content == content {
			return message
		}
	}
	return nil
}

func assertPromptTextPart(
	t *testing.T,
	part schema.MessageInputPart,
	want string,
) {
	t.Helper()
	if part.Type != schema.ChatMessagePartTypeText || part.Text != want {
		t.Fatalf("text part = %#v, want %q", part, want)
	}
}

func assertPromptImagePart(
	t *testing.T,
	part schema.MessageInputPart,
	wantDetail schema.ImageURLDetail,
) {
	t.Helper()
	if part.Type != schema.ChatMessagePartTypeImageURL ||
		part.Image == nil ||
		part.Image.Base64Data == nil ||
		*part.Image.Base64Data != testUserImagePNGBase64 ||
		part.Image.MIMEType != "image/png" ||
		part.Image.Detail != wantDetail {
		t.Fatalf("image part = %#v", part)
	}
}

func assertEmptyPromptTranscript(t *testing.T, engine *QueryEngine) {
	t.Helper()
	loaded, err := engine.GetTranscript().LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 0 {
		t.Fatalf("prompt reached transcript: %#v", loaded.Messages)
	}
}

func assertNoActivePromptMedia(t *testing.T, engine *QueryEngine) {
	t.Helper()
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.activePromptMedia) != 0 {
		t.Fatalf("active prompt media stores = %d", len(engine.activePromptMedia))
	}
}

func assertPromptMediaDestroyed(t *testing.T, store *turnMediaStore) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if !store.destroyed || len(store.media) != 0 {
		t.Fatalf(
			"store destroyed=%v media=%d",
			store.destroyed,
			len(store.media),
		)
	}
}
