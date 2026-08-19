package engine

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/abietic/yhc/engine/commands"
	"github.com/abietic/yhc/engine/execution"
	modelcaps "github.com/abietic/yhc/engine/model"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
)

type p292InventoryResolver struct {
	snapshot        provider.RuntimeInventorySnapshot
	resolveStarted  chan struct{}
	resolveContinue chan struct{}
	resolveOnce     sync.Once
}

func newP292InventoryResolver(contextWindow int) *p292InventoryResolver {
	metadata := modelcaps.EffectiveModelMetadata{
		ContextWindowTokens: p292MetadataField(contextWindow),
		MaxOutputTokens:     p292MetadataField(8192),
		Text:                p292MetadataField(true),
		Streaming:           p292MetadataField(true),
		Tools:               p292MetadataField(true),
		SystemPrompt:        p292MetadataField(true),
		Images:              p292MetadataField(false),
		PDFs:                p292MetadataField(false),
		Thinking:            p292MetadataField(true),
		SupportedReasoningEfforts: p292MetadataField(
			[]string{"none", "minimal", "low", "medium", "high", "xhigh", "max"},
		),
	}
	return &p292InventoryResolver{
		snapshot: provider.RuntimeInventorySnapshot{
			Revision: strings.Repeat("a", 64),
			Default:  "primary",
			Entries: []provider.RuntimeInventoryEntry{
				{
					Selector:            "primary",
					ProfileID:           "primary",
					DisplayName:         "Primary",
					Provider:            string(provider.ProviderAgenticClaude),
					APIModel:            "claude-sonnet-4-6",
					Metadata:            metadata,
					RouteIdentityDigest: strings.Repeat("1", 64),
					MetadataDigest:      strings.Repeat("2", 64),
				},
				{
					Selector:            "secondary",
					ProfileID:           "secondary",
					DisplayName:         "Secondary",
					Provider:            string(provider.ProviderAgenticOpenAI),
					APIModel:            "gpt-4o",
					Metadata:            metadata,
					RouteIdentityDigest: strings.Repeat("3", 64),
					MetadataDigest:      strings.Repeat("4", 64),
				},
			},
		},
	}
}

func p292MetadataField[T any](value T) modelcaps.MetadataField[T] {
	return modelcaps.MetadataField[T]{Value: value, Source: "test"}
}

func (r *p292InventoryResolver) InventorySnapshot() provider.RuntimeInventorySnapshot {
	return r.snapshot
}

func (r *p292InventoryResolver) ResolveInventorySelector(
	selector string,
) (provider.RuntimeInventoryEntry, error) {
	if strings.EqualFold(strings.TrimSpace(selector), "secondary") &&
		r.resolveStarted != nil &&
		r.resolveContinue != nil {
		r.resolveOnce.Do(func() {
			close(r.resolveStarted)
			<-r.resolveContinue
		})
	}
	for _, entry := range r.snapshot.Entries {
		if strings.EqualFold(strings.TrimSpace(selector), entry.Selector) {
			return entry, nil
		}
	}
	return provider.RuntimeInventoryEntry{}, errors.New("selector unavailable")
}

func (r *p292InventoryResolver) ResolveModel(
	selector string,
) (provider.ResolvedConfig, error) {
	entry, err := r.ResolveInventorySelector(selector)
	if err != nil {
		return provider.ResolvedConfig{}, err
	}
	return provider.ResolvedConfig{Config: provider.Config{
		Provider: provider.Provider(entry.Provider),
		Model:    entry.APIModel,
	}}, nil
}

func newP292BindingEngine(
	t *testing.T,
	resolver *p292InventoryResolver,
	chatModels ...einomodel.BaseChatModel,
) *QueryEngine {
	t.Helper()
	cwd := t.TempDir()
	var chatModel einomodel.BaseChatModel
	if len(chatModels) > 0 {
		chatModel = chatModels[0]
	}
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:     "p29-2-binding",
		CWD:           cwd,
		TranscriptDir: filepath.Join(cwd, "transcripts"),
		Model:         "primary",
		ModelResolver: resolver,
		ChatModel:     chatModel,
	})
	t.Cleanup(engine.Close)
	if projection := session.SafeModelBindingProjection(engine.modelBinding); projection.State != session.ModelBindingStateValid ||
		projection.Value != "primary" {
		t.Fatalf("initial model binding = %#v", projection)
	}
	return engine
}

func TestP292ModelSwitchCommitsCheckpointBeforeLiveMutation(t *testing.T) {
	engine := newP292BindingEngine(t, newP292InventoryResolver(200000))
	called := false
	engine.modelCheckpointWriter = func(
		candidate *session.PersistedModelBinding,
		model string,
		modelProvider string,
	) error {
		called = true
		if got := engine.GetModelName(); got != "primary" {
			t.Fatalf("live model changed before checkpoint: %q", got)
		}
		if candidate == nil ||
			candidate.Value != "secondary" ||
			model != "gpt-4o" ||
			modelProvider != string(provider.ProviderAgenticOpenAI) {
			t.Fatalf(
				"checkpoint candidate = %#v, %q, %q",
				candidate,
				model,
				modelProvider,
			)
		}
		return nil
	}

	state, err := engine.ChangeModel(context.Background(), "secondary")
	if err != nil {
		t.Fatal(err)
	}
	if !called || state.Requested != "secondary" ||
		engine.GetModelName() != "secondary" {
		t.Fatalf("switch result = %#v, called=%t", state, called)
	}
}

func TestP292RecorderlessModelSwitchReportsProcessLocalCommit(t *testing.T) {
	engine := newP292BindingEngine(t, newP292InventoryResolver(200000))
	engine.mu.Lock()
	recorder := engine.transcript
	engine.transcript = nil
	engine.mu.Unlock()
	t.Cleanup(func() {
		if recorder != nil {
			_ = recorder.Close()
		}
	})

	state, err := engine.ChangeModel(context.Background(), "secondary")
	if err != nil {
		t.Fatal(err)
	}
	if state.Durable ||
		state.Requested != "secondary" ||
		engine.GetModelName() != "secondary" {
		t.Fatalf("recorderless switch state = %#v", state)
	}
}

func TestP292ModelSwitchFailureDoesNotMutateLiveBinding(t *testing.T) {
	engine := newP292BindingEngine(t, newP292InventoryResolver(200000))
	engine.mu.Lock()
	engine.modelDispatchBlock = newModelDispatchBlock(
		ModelDispatchBlockRouteRevision,
		"primary",
		"review the changed route",
		false,
	)
	engine.mu.Unlock()
	engine.modelCheckpointWriter = func(
		*session.PersistedModelBinding,
		string,
		string,
	) error {
		return errors.New("injected pre-commit failure")
	}

	if _, err := engine.ChangeModel(
		context.Background(),
		"secondary",
	); err == nil || !strings.Contains(err.Error(), "pre-commit") {
		t.Fatalf("model switch failure = %v", err)
	}
	if engine.GetModelName() != "primary" ||
		engine.modelBinding.Value != "primary" ||
		engine.ModelDispatchBlockSnapshot() == nil ||
		engine.ModelDispatchBlockSnapshot().Code !=
			ModelDispatchBlockRouteRevision {
		t.Fatalf(
			"failed switch mutated state: model=%q binding=%#v block=%#v",
			engine.GetModelName(),
			engine.modelBinding,
			engine.ModelDispatchBlockSnapshot(),
		)
	}
}

func TestP292UncertainCheckpointBlocksAllProviderDispatch(t *testing.T) {
	model := &p172NoDispatchModel{}
	engine := newP292BindingEngine(
		t,
		newP292InventoryResolver(200000),
		model,
	)
	engine.modelCheckpointWriter = func(
		*session.PersistedModelBinding,
		string,
		string,
	) error {
		return &transcript.DurabilityUncertainError{
			Operation: "sync transcript file",
			Err:       errors.New("injected"),
		}
	}

	if _, err := engine.ChangeModel(
		context.Background(),
		"secondary",
	); err == nil || !transcript.IsDurabilityUncertain(err) {
		t.Fatalf("uncertain switch = %v", err)
	}
	block := engine.ModelDispatchBlockSnapshot()
	if block == nil ||
		block.Code != ModelDispatchBlockCheckpointUncertain ||
		engine.GetModelName() != "primary" {
		t.Fatalf("uncertain state = model %q, block %#v", engine.GetModelName(), block)
	}

	events, _ := engine.SubmitMessage(context.Background(), "must not dispatch")
	for range events {
	}
	if calls := model.CallCount(); calls != 0 {
		t.Fatalf("blocked binding reached provider %d times", calls)
	}
	if _, _, err := engine.applyCommandAction(
		context.Background(),
		&commands.CommandResult{Action: commands.ActionCompact},
		"",
		func(QueryEvent) bool { return true },
	); err == nil ||
		!strings.Contains(
			err.Error(),
			ModelDispatchBlockCheckpointUncertain,
		) {
		t.Fatalf("identity-blocked compaction = %v", err)
	}
	if calls := model.CallCount(); calls != 0 {
		t.Fatalf("identity-blocked compaction reached provider %d times", calls)
	}
	if _, err := engine.ChangeModel(
		context.Background(),
		"secondary",
	); err == nil ||
		!strings.Contains(
			err.Error(),
			ModelDispatchBlockCheckpointUncertain,
		) {
		t.Fatalf("uncertain binding accepted another switch: %v", err)
	}
	if block := engine.ModelDispatchBlockSnapshot(); block == nil ||
		block.Code != ModelDispatchBlockCheckpointUncertain {
		t.Fatalf("uncertain block was cleared without reload: %#v", block)
	}
}

func TestP292RetryRechecksDispatchGuardBeforeEveryProviderAttempt(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MAX_RETRIES", "1")
	blocked := false
	callCount := 0
	_, terminal := collectEvents(context.Background(), QueryParams{
		Messages:    []*schema.Message{{Role: schema.User, Content: "retry"}},
		ChatModel:   &p172NoDispatchModel{},
		QuerySource: QuerySourceSDK,
		ToolUseContext: &ToolUseContext{Options: &ToolUseOptions{
			MainLoopModel: "primary",
		}},
		modelDispatchGuard: func(string) error {
			if blocked {
				return errors.New(ModelDispatchBlockCheckpointUncertain)
			}
			return nil
		},
		Deps: &QueryDeps{
			UUID: func() string { return "p29-2-retry-guard" },
			CallModel: func(
				context.Context,
				einomodel.BaseChatModel,
				[]*schema.Message,
				*schema.Message,
				[]*schema.ToolInfo,
				execution.CallModelOptions,
			) (*execution.CallModelResult, error) {
				callCount++
				blocked = true
				return nil, errors.New("429 rate_limit_error")
			},
		},
	})
	if terminal.Reason != TerminalPromptInputError ||
		terminal.Err == nil ||
		!strings.Contains(
			terminal.Err.Error(),
			ModelDispatchBlockCheckpointUncertain,
		) {
		t.Fatalf("retry dispatch terminal = %#v", terminal)
	}
	if callCount != 1 {
		t.Fatalf("blocked retry reached provider %d times", callCount)
	}
}

func TestP292NewSessionWithIncompatibleMetadataFailsClosed(t *testing.T) {
	resolver := newP292InventoryResolver(200000)
	resolver.snapshot.Entries[0].Metadata.Tools.Source = "unknown"
	model := &p172NoDispatchModel{}
	cwd := t.TempDir()
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:     "p29-2-invalid-startup",
		CWD:           cwd,
		TranscriptDir: filepath.Join(cwd, "transcripts"),
		Model:         "primary",
		ModelResolver: resolver,
		ChatModel:     model,
	})
	t.Cleanup(engine.Close)
	block := engine.ModelDispatchBlockSnapshot()
	if block == nil ||
		block.Code != ModelDispatchBlockMetadataChanged ||
		block.Selector != "primary" {
		t.Fatalf("startup metadata block = %#v", block)
	}
	events, _ := engine.SubmitMessage(context.Background(), "must not dispatch")
	for range events {
	}
	if calls := model.CallCount(); calls != 0 {
		t.Fatalf("incompatible startup metadata reached provider %d times", calls)
	}
}

func TestP292FirstAndSwitchedCheckpointsPersistExactBinding(t *testing.T) {
	resolver := newP292InventoryResolver(200000)
	engine := newP292BindingEngine(t, resolver)
	if err := engine.persistSessionCheckpoint("created"); err != nil {
		t.Fatal(err)
	}
	loaded, err := transcript.NewRecorder(
		engine.SessionID(),
		engine.config.TranscriptDir,
	).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	metadata := session.ReadSessionMetadataFull(loaded)
	if metadata == nil ||
		metadata.ModelBinding == nil ||
		metadata.ModelBinding.Value != "primary" ||
		metadata.Model != "claude-sonnet-4-6" {
		t.Fatalf("first checkpoint metadata = %#v", metadata)
	}

	if _, err := engine.ChangeModel(
		context.Background(),
		"secondary",
	); err != nil {
		t.Fatal(err)
	}
	loaded, err = transcript.NewRecorder(
		engine.SessionID(),
		engine.config.TranscriptDir,
	).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	metadata = session.ReadSessionMetadataFull(loaded)
	if metadata == nil ||
		metadata.ModelBinding == nil ||
		metadata.ModelBinding.Value != "secondary" ||
		metadata.Model != "gpt-4o" ||
		metadata.Provider != string(provider.ProviderAgenticOpenAI) {
		t.Fatalf("switched checkpoint metadata = %#v", metadata)
	}
}

func TestP292ReasoningEffortUsesTheSameDurableBoundary(t *testing.T) {
	engine := newP292BindingEngine(t, newP292InventoryResolver(200000))
	engine.modelCheckpointWriter = func(
		candidate *session.PersistedModelBinding,
		_ string,
		_ string,
	) error {
		if engine.ReasoningEffort() != "" {
			t.Fatalf("live effort changed before checkpoint")
		}
		if candidate == nil || candidate.ReasoningEffort != "high" {
			t.Fatalf("reasoning checkpoint candidate = %#v", candidate)
		}
		return nil
	}
	if effort, err := engine.ChangeReasoningEffort(
		context.Background(),
		"high",
	); err != nil || effort != "high" ||
		engine.modelBinding.ReasoningEffort != "high" {
		t.Fatalf(
			"reasoning result = %q, %v, binding %#v",
			effort,
			err,
			engine.modelBinding,
		)
	}
}

func TestP292ExplicitReasoningMetadataRejectsUnsupportedChoice(t *testing.T) {
	resolver := newP292InventoryResolver(200000)
	resolver.snapshot.Entries[0].Metadata.SupportedReasoningEfforts = p292MetadataField([]string{"low"})
	engine := newP292BindingEngine(t, resolver)

	if _, err := engine.ChangeReasoningEffort(
		context.Background(),
		"high",
	); err == nil || !strings.Contains(err.Error(), `reasoning effort "high"`) {
		t.Fatalf("unsupported configured effort = %v", err)
	}
	if engine.ReasoningEffort() != "" ||
		engine.modelBinding.ReasoningEffort != "" {
		t.Fatalf("rejected configured effort mutated binding")
	}
	if effort, err := engine.ChangeReasoningEffort(
		context.Background(),
		"low",
	); err != nil || effort != "low" {
		t.Fatalf("supported configured effort = %q, %v", effort, err)
	}
}

func TestDeepSeekV4ReasoningOptionsUseExactModelAdapterIntersection(t *testing.T) {
	resolver := newP292InventoryResolver(1000000)
	primary := &resolver.snapshot.Entries[0]
	primary.Provider = string(provider.ProviderAgenticDeepSeek)
	primary.APIModel = "deepseek-v4-flash"
	primary.Metadata.SupportedReasoningEfforts = p292MetadataField(
		[]string{"none", "high", "max"},
	)
	engine := newP292BindingEngine(t, resolver)

	options, err := engine.ReasoningEffortOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(options, ","); got != "none,high,max" {
		t.Fatalf("DeepSeek V4 effort options = %q", got)
	}
	if effort, err := engine.ChangeReasoningEffort(
		context.Background(),
		"max",
	); err != nil || effort != "max" {
		t.Fatalf("DeepSeek V4 max effort = %q, %v", effort, err)
	}
	if _, err := engine.ChangeReasoningEffort(
		context.Background(),
		"low",
	); err == nil || !strings.Contains(err.Error(), `reasoning effort "low"`) {
		t.Fatalf("DeepSeek V4 compatibility alias was not rejected: %v", err)
	}
	if engine.ReasoningEffort() != "max" {
		t.Fatalf("rejected effort mutated state to %q", engine.ReasoningEffort())
	}
}

func TestReasoningControlsRejectInconsistentThinkingDisabledMetadata(t *testing.T) {
	resolver := newP292InventoryResolver(1000000)
	primary := &resolver.snapshot.Entries[0]
	primary.Provider = string(provider.ProviderAgenticDeepSeek)
	primary.APIModel = "deepseek-v4-flash"
	primary.Metadata.Thinking = p292MetadataField(false)
	primary.Metadata.SupportedReasoningEfforts = p292MetadataField(
		[]string{"none", "high", "max"},
	)
	engine := newP292BindingEngine(t, resolver)

	options, err := engine.ReasoningEffortOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 0 {
		t.Fatalf("thinking=false exposed effort options: %#v", options)
	}
	if _, err := engine.ChangeReasoningEffort(
		context.Background(),
		"high",
	); err == nil {
		t.Fatal("thinking=false admitted a reasoning effort")
	}
}

func TestP292ConcurrentReasoningChangeInvalidatesPreparedModelCandidate(
	t *testing.T,
) {
	resolver := newP292InventoryResolver(200000)
	engine := newP292BindingEngine(t, resolver)
	resolver.resolveStarted = make(chan struct{})
	resolver.resolveContinue = make(chan struct{})
	switchResult := make(chan error, 1)
	go func() {
		_, err := engine.ChangeModel(context.Background(), "secondary")
		switchResult <- err
	}()
	<-resolver.resolveStarted

	if _, err := engine.ChangeReasoningEffort(
		context.Background(),
		"high",
	); err != nil {
		t.Fatal(err)
	}
	close(resolver.resolveContinue)
	if err := <-switchResult; err == nil ||
		!strings.Contains(err.Error(), "reasoning changed") {
		t.Fatalf("stale model candidate result = %v", err)
	}
	if engine.GetModelName() != "primary" ||
		engine.ReasoningEffort() != "high" ||
		engine.modelBinding.ReasoningEffort != "high" {
		t.Fatalf(
			"concurrent controls = model %q, effort %q, binding %#v",
			engine.GetModelName(),
			engine.ReasoningEffort(),
			engine.modelBinding,
		)
	}
}

func TestP292ResumeAdmissionFailsClosedAndWarnsOnlyOnCompatibleDrift(
	t *testing.T,
) {
	engine := newP292BindingEngine(t, newP292InventoryResolver(24000))
	current := engine.modelBinding.Clone()

	invalid := current.Clone()
	invalid.Value = "INVALID VALUE"
	if block := engine.admitResumedModelBinding(
		context.Background(),
		invalid,
		current.APIModel,
		100,
	).block; block == nil || block.Code != ModelDispatchBlockInvalidBinding {
		t.Fatalf("invalid admission block = %#v", block)
	}

	var unsupported session.PersistedModelBinding
	if err := json.Unmarshal(
		[]byte(`{"version":2,"kind":"profile","value":"primary"}`),
		&unsupported,
	); err != nil {
		t.Fatal(err)
	}
	if block := engine.admitResumedModelBinding(
		context.Background(),
		&unsupported,
		current.APIModel,
		100,
	).block; block == nil ||
		block.Code != ModelDispatchBlockUnsupportedVersion {
		t.Fatalf("unsupported admission block = %#v", block)
	}

	missing := current.Clone()
	missing.Value = "missing"
	if block := engine.admitResumedModelBinding(
		context.Background(),
		missing,
		current.APIModel,
		100,
	).block; block == nil || block.Code != ModelDispatchBlockRebindRequired {
		t.Fatalf("missing admission block = %#v", block)
	}

	routeChanged := current.Clone()
	routeChanged.RouteIdentityDigest = strings.Repeat("f", 64)
	if block := engine.admitResumedModelBinding(
		context.Background(),
		routeChanged,
		current.APIModel,
		100,
	).block; block == nil || block.Code != ModelDispatchBlockRouteRevision {
		t.Fatalf("route-change admission block = %#v", block)
	}

	identityChanged := current.Clone()
	identityChanged.Provider = string(provider.ProviderAgenticOpenAI)
	if block := engine.admitResumedModelBinding(
		context.Background(),
		identityChanged,
		current.APIModel,
		100,
	).block; block == nil || block.Code != ModelDispatchBlockRouteChanged {
		t.Fatalf("provider-change admission block = %#v", block)
	}

	compatible := current.Clone()
	compatible.PortfolioRevision = strings.Repeat("b", 64)
	compatible.MetadataDigest = strings.Repeat("c", 64)
	largerContext := *compatible.ContextWindowTokens + 1000
	largerOutput := *compatible.MaxOutputTokens + 1000
	compatible.ContextWindowTokens = &largerContext
	compatible.MaxOutputTokens = &largerOutput
	admission := engine.admitResumedModelBinding(
		context.Background(),
		compatible,
		current.APIModel,
		100,
	)
	warnings := strings.Join(admission.warnings, "\n")
	if admission.block != nil ||
		!strings.Contains(warnings, "portfolio_changed") ||
		!strings.Contains(warnings, "metadata_changed") ||
		!strings.Contains(warnings, "context_limit_decreased") ||
		!strings.Contains(warnings, "output_limit_decreased") {
		t.Fatalf("compatible admission = %#v", admission)
	}

	resolver := engine.config.ModelResolver.(*p292InventoryResolver)
	originalMetadata := resolver.snapshot.Entries[0].Metadata
	resolver.snapshot.Entries[0].Metadata.Tools.Value = false
	if block := engine.admitResumedModelBinding(
		context.Background(),
		current,
		current.APIModel,
		100,
	).block; block == nil || block.Code != ModelDispatchBlockMetadataChanged {
		t.Fatalf("metadata-incompatible admission block = %#v", block)
	}
	resolver.snapshot.Entries[0].Metadata = originalMetadata
	resolver.snapshot.Entries[1].Metadata.SupportedReasoningEfforts = p292MetadataField([]string{"low"})

	_, secondary, err := engine.resolveModelBindingCandidate(
		context.Background(),
		"secondary",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	secondary.ReasoningEffort = "high"
	reasoningAdmission := engine.admitResumedModelBinding(
		context.Background(),
		secondary,
		secondary.APIModel,
		100,
	)
	if reasoningAdmission.reasoning != "" ||
		!strings.Contains(
			strings.Join(reasoningAdmission.warnings, "\n"),
			"reasoning_cleared",
		) {
		t.Fatalf("unsupported reasoning admission = %#v", reasoningAdmission)
	}

	contextBlocked := engine.admitResumedModelBinding(
		context.Background(),
		current,
		current.APIModel,
		21000,
	)
	if contextBlocked.block == nil ||
		contextBlocked.block.Code != ModelDispatchBlockCompactRequired ||
		!contextBlocked.block.ContextOnly {
		t.Fatalf("context admission block = %#v", contextBlocked.block)
	}
}

func TestP292FailedCompactionKeepsContextOnlyDispatchBlock(t *testing.T) {
	model := &promptInputErrorModel{}
	engine := newP292BindingEngine(
		t,
		newP292InventoryResolver(24000),
		model,
	)
	engine.mu.Lock()
	engine.messages = []*schema.Message{{
		Role:    schema.User,
		Content: strings.Repeat("history ", 100),
	}}
	engine.modelDispatchBlock = newModelDispatchBlock(
		ModelDispatchBlockCompactRequired,
		"primary",
		"compact the Session successfully",
		true,
	)
	engine.mu.Unlock()

	_, _, err := engine.applyCommandAction(
		context.Background(),
		&commands.CommandResult{Action: commands.ActionCompact},
		"",
		func(QueryEvent) bool { return true },
	)
	if err == nil || !strings.Contains(err.Error(), "fixture provider failure") {
		t.Fatalf("compact failure = %v", err)
	}
	block := engine.ModelDispatchBlockSnapshot()
	if block == nil ||
		block.Code != ModelDispatchBlockCompactRequired ||
		!block.ContextOnly {
		t.Fatalf("failed compaction cleared block: %#v", block)
	}
}

func TestP292DurableFittingCompactionClearsOnlyContextBlock(t *testing.T) {
	model := &p172NoDispatchModel{}
	engine := newP292BindingEngine(
		t,
		newP292InventoryResolver(24000),
		model,
	)
	engine.mu.Lock()
	engine.messages = []*schema.Message{{
		Role:    schema.User,
		Content: strings.Repeat("history ", 100),
	}}
	engine.modelDispatchBlock = newModelDispatchBlock(
		ModelDispatchBlockCompactRequired,
		"primary",
		"compact the Session successfully",
		true,
	)
	engine.mu.Unlock()

	_, _, err := engine.applyCommandAction(
		context.Background(),
		&commands.CommandResult{Action: commands.ActionCompact},
		"",
		func(QueryEvent) bool { return true },
	)
	if err != nil {
		t.Fatal(err)
	}
	if block := engine.ModelDispatchBlockSnapshot(); block != nil {
		t.Fatalf("fitting durable compaction retained block: %#v", block)
	}
	if calls := model.CallCount(); calls != 1 {
		t.Fatalf("compaction provider calls = %d, want 1", calls)
	}
}

func TestP292ActiveForkSamplesLatestLiveBinding(t *testing.T) {
	engine := newP292BindingEngine(t, newP292InventoryResolver(200000))
	messages := []*schema.Message{
		{Role: schema.User, Content: "question"},
		{Role: schema.Assistant, Content: "answer"},
	}
	engine.SetResumedMessages(messages)
	if err := engine.transcript.RecordLifecycleBoundary(
		transcript.LifecycleCheckpoint,
		messages,
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ChangeModel(
		context.Background(),
		"secondary",
	); err != nil {
		t.Fatal(err)
	}

	forked, err := engine.SessionService().CreateFork(
		context.Background(),
		SessionForkRequest{
			BranchName:  "latest-binding",
			OperationID: "p292-latest-binding",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	child, err := transcript.NewRecorder(
		forked.Branch.NewSessionID,
		engine.config.TranscriptDir,
	).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	metadata := session.ReadSessionMetadataFull(child)
	if metadata == nil ||
		metadata.ModelBinding == nil ||
		metadata.ModelBinding.Value != "secondary" ||
		metadata.Model != "gpt-4o" {
		t.Fatalf("forked latest binding = %#v", metadata)
	}
}
