package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	engineconfig "github.com/abietic/yhc/engine/config"
	"github.com/abietic/yhc/engine/execution"
	"github.com/abietic/yhc/engine/provider"
	"github.com/abietic/yhc/engine/session"
	"github.com/abietic/yhc/engine/transcript"
	"github.com/abietic/yhc/tools"
)

func TestP293ChildRoleAdmissionRoutesExplicitAndDynamicMain(t *testing.T) {
	resolver := newP293RoleResolver()
	resolver.setExplicit(engineconfig.RoleExplore, "secondary")
	chatModel := &p293RecordingModel{}
	engine := newP293RoleEngine(t, resolver, chatModel, nil, "")

	runP293Agent(t, engine, "explore-child", "Explore")
	if _, err := engine.ChangeModel(context.Background(), "switched"); err != nil {
		t.Fatal(err)
	}
	runP293Agent(t, engine, "general-child", "custom-worker")
	runP293Agent(t, engine, "explore-after-switch", "Explore")

	if got := chatModel.modelCalls(); !p293EqualStrings(
		got,
		[]string{"secondary", "switched", "secondary"},
	) {
		t.Fatalf("role model calls = %#v", got)
	}
	assertP293ChildMetadata(
		t,
		engine.agentRunner.DurableTranscriptDir(),
		"explore-child",
		"Explore",
		"explore",
		"secondary",
	)
	assertP293ChildMetadata(
		t,
		engine.agentRunner.DurableTranscriptDir(),
		"general-child",
		"custom-worker",
		"general",
		"switched",
	)
}

func TestP293ChildResumeUsesPersistedBindingNotCurrentRolePolicy(t *testing.T) {
	resolver := newP293RoleResolver()
	resolver.setExplicit(engineconfig.RoleExplore, "secondary")
	chatModel := &p293RecordingModel{}
	engine := newP293RoleEngine(t, resolver, chatModel, nil, "")

	runP293Agent(t, engine, "resume-child", "Explore")
	resolver.setExplicit(engineconfig.RoleExplore, "switched")
	agentID, action, err := engine.agentRunner.SendOrResumeAgentMessage(
		"resume-child",
		tools.MessagePayload{Content: "continue with exact binding"},
	)
	if err != nil || action != "resumed" || agentID != "resume-child" {
		t.Fatalf("resume = id %q action %q err %v", agentID, action, err)
	}
	waitP293AgentTerminal(t, engine.agentRunner, "resume-child")

	if got := chatModel.modelCalls(); !p293EqualStrings(
		got,
		[]string{"secondary", "secondary"},
	) {
		t.Fatalf("resumed model calls = %#v", got)
	}
	assertP293ChildMetadata(
		t,
		engine.agentRunner.DurableTranscriptDir(),
		"resume-child",
		"Explore",
		"explore",
		"secondary",
	)
}

func TestP293BackgroundChildPersistsRoleBeforeDispatch(t *testing.T) {
	resolver := newP293RoleResolver()
	resolver.setExplicit(engineconfig.RoleGeneral, "switched")
	chatModel := &p293RecordingModel{}
	engine := newP293RoleEngine(t, resolver, chatModel, nil, "")

	started, err := tools.RunAgentBackground(
		context.Background(),
		engine.agentRunner,
		tools.AgentExecOptions{
			Task:            "complete one background role-routed turn",
			SessionID:       "background-child",
			ThreadID:        "background-thread",
			ParentSessionID: engine.config.SessionID,
			ParentThreadID:  engine.config.ThreadID,
			AgentID:         "background-child",
			SubagentType:    "worker",
			Name:            "background-worker",
			CWD:             engine.config.CWD,
			MaxTurns:        1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != "running" {
		t.Fatalf("background launch = %#v", started)
	}
	waitP293AgentTerminal(t, engine.agentRunner, "background-child")
	if !p293EqualStrings(chatModel.modelCalls(), []string{"switched"}) {
		t.Fatalf("background model calls = %#v", chatModel.modelCalls())
	}
	assertP293ChildMetadata(
		t,
		engine.agentRunner.DurableTranscriptDir(),
		"background-child",
		"worker",
		"general",
		"switched",
	)
	loaded, err := transcript.NewRecorder(
		"background-child",
		engine.agentRunner.DurableTranscriptDir(),
	).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	if metadata := session.ReadSessionMetadataFull(loaded); metadata == nil ||
		metadata.AgentName != "background-worker" {
		t.Fatalf("background Agent name metadata = %#v", metadata)
	}
}

func TestP293OldChildUpgradesThroughLegacyInheritanceWithoutPolicyRewrite(
	t *testing.T,
) {
	resolver := newP293RoleResolver()
	resolver.setExplicit(engineconfig.RoleExplore, "secondary")
	chatModel := &p293RecordingModel{}
	engine := newP293RoleEngine(t, resolver, chatModel, nil, "")
	transcriptDir := engine.agentRunner.DurableTranscriptDir()
	if err := seedP293LegacyChild(
		transcriptDir,
		engine.config.CWD,
		"legacy-child",
		"legacy-thread",
	); err != nil {
		t.Fatal(err)
	}

	runP293AgentWithThread(
		t,
		engine,
		"legacy-child",
		"legacy-thread",
		"Explore",
	)
	if got := chatModel.modelCalls(); !p293EqualStrings(got, []string{"primary"}) {
		t.Fatalf("legacy child was reinterpreted by current role policy: %#v", got)
	}
	assertP293ChildMetadata(
		t,
		transcriptDir,
		"legacy-child",
		"Explore",
		"explore",
		"primary",
	)
}

func TestP293PartialDurableChildIdentityFailsClosed(t *testing.T) {
	tests := []struct {
		name        string
		agentRole   string
		agentName   string
		requestName string
		want        string
	}{
		{
			name: "missing Agent role",
			want: "no original Agent role",
		},
		{
			name:        "missing named Agent identity",
			agentRole:   "Explore",
			requestName: "named-explore",
			want:        "incompatible Agent name",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolver := newP293RoleResolver()
			chatModel := &p293RecordingModel{}
			engine := newP293RoleEngine(t, resolver, chatModel, nil, "")
			sessionID := "partial-child"
			threadID := "partial-thread"
			transcriptDir := engine.agentRunner.DurableTranscriptDir()
			if err := seedP293LegacyChild(
				transcriptDir,
				engine.config.CWD,
				sessionID,
				threadID,
			); err != nil {
				t.Fatal(err)
			}
			recorder := transcript.NewRecorder(sessionID, transcriptDir)
			loaded, err := recorder.LoadFull()
			if err != nil {
				t.Fatal(err)
			}
			metadata := session.ReadSessionMetadataFull(loaded)
			metadata.AgentRole = test.agentRole
			metadata.AgentName = test.agentName
			metadata.ModelRole = "explore"
			metadata.ModelBinding = engine.modelBinding.Clone()
			metadata.Model = engine.modelBinding.APIModel
			metadata.Provider = engine.modelBinding.Provider
			if err := session.WriteSessionMetadata(recorder, metadata); err != nil {
				t.Fatal(err)
			}
			if err := recorder.Close(); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(recorder.Path())
			if err != nil {
				t.Fatal(err)
			}

			_, err = tools.RunAgent(
				context.Background(),
				engine.agentRunner,
				tools.AgentExecOptions{
					Task:            "must fail before dispatch",
					SessionID:       sessionID,
					ThreadID:        threadID,
					ParentSessionID: engine.config.SessionID,
					ParentThreadID:  engine.config.ThreadID,
					AgentID:         sessionID,
					SubagentType:    "Explore",
					Name:            test.requestName,
					CWD:             engine.config.CWD,
					MaxTurns:        1,
				},
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("partial durable identity error = %v", err)
			}
			if len(chatModel.modelCalls()) != 0 {
				t.Fatalf(
					"partial durable identity reached model: %#v",
					chatModel.modelCalls(),
				)
			}
			after, err := os.ReadFile(recorder.Path())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("rejected partial durable identity was rewritten")
			}
		})
	}
}

func TestP293TrustedSubagentInjectionUsesTruthfulSelectorAndExplicitWins(
	t *testing.T,
) {
	t.Run("injected compatibility", func(t *testing.T) {
		resolver := newP293RoleResolver()
		shared := &p293RecordingModel{}
		injected := &p293RecordingModel{}
		engine := newP293RoleEngine(
			t,
			resolver,
			shared,
			injected,
			"secondary",
		)
		selected, err := engine.subagentExecutor.resolveNewChildModelCall(
			context.Background(),
			tools.AgentExecOptions{SubagentType: "Plan"},
			nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		if selected.Identity == nil ||
			selected.Identity.Source != provider.RoleCallSourceCompatibility {
			t.Fatalf("injected role source = %#v", selected.Identity)
		}
		runP293Agent(t, engine, "injected-child", "Plan")
		if len(shared.modelCalls()) != 0 ||
			!p293EqualStrings(injected.modelCalls(), []string{"secondary"}) {
			t.Fatalf(
				"shared=%#v injected=%#v",
				shared.modelCalls(),
				injected.modelCalls(),
			)
		}
	})

	t.Run("explicit role wins", func(t *testing.T) {
		resolver := newP293RoleResolver()
		resolver.setExplicit(engineconfig.RolePlan, "switched")
		shared := &p293RecordingModel{}
		injected := &p293RecordingModel{}
		engine := newP293RoleEngine(
			t,
			resolver,
			shared,
			injected,
			"secondary",
		)
		runP293Agent(t, engine, "explicit-plan", "Plan")
		if !p293EqualStrings(shared.modelCalls(), []string{"switched"}) ||
			len(injected.modelCalls()) != 0 {
			t.Fatalf(
				"shared=%#v injected=%#v",
				shared.modelCalls(),
				injected.modelCalls(),
			)
		}
	})
}

func TestP293NamedPromptMediaUsesSelectedProfileMetadata(t *testing.T) {
	fallback := PromptCapabilityResolverFunc(
		func(provider.Provider, string) PromptCapabilityDecision {
			return PromptCapabilityDecision{
				Status: PromptCapabilityUnsupported,
				Source: "legacy-fixture",
			}
		},
	)
	resolver := newP293RoleResolver()
	resolver.snapshot.Entries[0].Metadata.Images = p292MetadataField(true)
	engine := newP293RoleEngine(
		t,
		resolver,
		&p293RecordingModel{},
		nil,
		"",
	)
	engine.config.PromptCapabilityResolver = fallback
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
	if admitted.binding == nil ||
		admitted.binding.capabilitySource != "profile-metadata:test" {
		t.Fatalf("named media binding = %#v", admitted.binding)
	}
	engine.releaseAdmittedPrompt(admitted)

	unknownResolver := newP293RoleResolver()
	unknownResolver.snapshot.Entries[0].Metadata.Images.Source = "unknown"
	unknownEngine := newP293RoleEngine(
		t,
		unknownResolver,
		&p293RecordingModel{},
		nil,
		"",
	)
	unknownEngine.config.PromptCapabilityResolver = DefaultPromptCapabilityResolver()
	_, err = unknownEngine.admitPromptInput(
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
	if err == nil || !strings.Contains(err.Error(), "capability_unknown") {
		t.Fatalf("unknown selected-profile image capability = %v", err)
	}
}

func TestP293ChildContextAdmissionRejectsBeforeModelDispatch(t *testing.T) {
	resolver := newP293RoleResolver()
	resolver.snapshot.Entries[0].Metadata.ContextWindowTokens = p292MetadataField(1)
	chatModel := &p293RecordingModel{}
	engine := newP293RoleEngine(t, resolver, chatModel, nil, "")

	_, err := tools.RunAgent(
		context.Background(),
		engine.agentRunner,
		tools.AgentExecOptions{
			Task:            strings.Repeat("context ", 32),
			SessionID:       "context-child",
			ThreadID:        "context-child-thread",
			ParentSessionID: engine.config.SessionID,
			ParentThreadID:  engine.config.ThreadID,
			AgentID:         "context-child",
			SubagentType:    "worker",
			CWD:             engine.config.CWD,
			MaxTurns:        1,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "context window exceeded") {
		t.Fatalf("child context admission error = %v", err)
	}
	if len(chatModel.modelCalls()) != 0 {
		t.Fatalf(
			"child context rejection reached model: %#v",
			chatModel.modelCalls(),
		)
	}
}

func TestP293SummaryRolePrecedenceAndUsageAttribution(t *testing.T) {
	resolver := newP293RoleResolver()
	resolver.setExplicit(engineconfig.RoleSummary, "secondary")
	shared := &p293RecordingModel{}
	injected := &p293RecordingModel{}
	engine := newP293RoleEngine(t, resolver, shared, nil, "")
	engine.config.EmitToolUseSummaries = true
	engine.config.SummaryModel = injected
	engine.config.SummaryModelSelector = "switched"

	selected := engine.toolUseSummaryModelCall(context.Background())
	if selected.Model != shared ||
		selected.Identity == nil ||
		selected.Identity.Role != "summary" ||
		selected.Identity.Profile != "secondary" {
		t.Fatalf("explicit summary role = %#v", selected)
	}

	resolver.setExplicit(engineconfig.RoleSummary, "")
	selected = engine.toolUseSummaryModelCall(context.Background())
	if selected.Model != injected ||
		selected.Identity == nil ||
		selected.Identity.Profile != "switched" ||
		selected.Identity.Source != provider.RoleCallSourceCompatibility {
		t.Fatalf("injected summary compatibility = %#v", selected)
	}

	usage := &p293UsageAdmitter{}
	summary := generateToolUseSummaryWithCall(
		context.Background(),
		injected,
		selected.Identity,
		[]*schema.ToolCall{{
			ID: "tool-1",
			Function: schema.FunctionCall{
				Name: "Read", Arguments: `{"file_path":"x"}`,
			},
		}},
		[]*schema.Message{{
			Role: schema.Tool, ToolCallID: "tool-1", Content: "content",
		}},
		nil,
		usage,
		usage.NewLogicalRoundID(),
	)
	if summary != "done" ||
		!p293EqualStrings(injected.modelCalls(), []string{"switched"}) ||
		usage.descriptor.ModelRole != "summary" ||
		usage.descriptor.ModelProfile != "switched" ||
		usage.descriptor.ReasoningEffort != selected.Identity.Reasoning {
		t.Fatalf(
			"summary=%q calls=%#v usage=%#v",
			summary,
			injected.modelCalls(),
			usage.descriptor,
		)
	}

	engine.config.AgentID = "child"
	if child := engine.toolUseSummaryModelCall(context.Background()); child.Model != nil ||
		child.Identity != nil {
		t.Fatalf("child summary route should be disabled: %#v", child)
	}
}

func TestP293ReasoningDefaultsResetAndIncompatibleSwitch(t *testing.T) {
	resolver := newP293RoleResolver()
	resolver.snapshot.Entries[0].ReasoningDefault = "medium"
	engine := newP293RoleEngine(
		t,
		resolver,
		&p293RecordingModel{},
		nil,
		"",
	)
	if engine.ReasoningEffort() != "medium" ||
		engine.modelBinding.ReasoningEffort != "medium" {
		t.Fatalf(
			"initial profile default = %q binding=%#v",
			engine.ReasoningEffort(),
			engine.modelBinding,
		)
	}
	if effort, err := engine.ChangeReasoningEffort(
		context.Background(),
		"high",
	); err != nil || effort != "high" {
		t.Fatalf("set high effort = %q, %v", effort, err)
	}
	if effort, err := engine.ChangeReasoningEffort(
		context.Background(),
		"default",
	); err != nil || effort != "default" ||
		engine.ReasoningEffort() != "medium" ||
		engine.modelBinding.ReasoningEffort != "medium" {
		t.Fatalf(
			"reset default = %q, %v live=%q binding=%#v",
			effort,
			err,
			engine.ReasoningEffort(),
			engine.modelBinding,
		)
	}

	if _, err := engine.ChangeReasoningEffort(
		context.Background(),
		"high",
	); err != nil {
		t.Fatal(err)
	}
	resolver.snapshot.Entries[1].Metadata.SupportedReasoningEfforts = p292MetadataField([]string{"low"})
	resolver.snapshot.Entries[1].ReasoningDefault = "low"
	state, err := engine.ChangeModel(context.Background(), "secondary")
	if err != nil {
		t.Fatal(err)
	}
	if state.ReasoningEffort != "low" ||
		engine.ReasoningEffort() != "low" ||
		len(state.Warnings) != 1 ||
		!strings.Contains(state.Warnings[0], "reasoning_cleared") {
		t.Fatalf("incompatible model switch = %#v", state)
	}
}

func TestP293ResumeRejectsUnknownOrMismatchedModelRole(t *testing.T) {
	binding := &session.PersistedModelBinding{
		Version:             session.PersistedModelBindingVersion,
		Kind:                session.ModelBindingKindProfile,
		Value:               "primary",
		Provider:            string(provider.ProviderAgenticOpenAI),
		APIModel:            "gpt-4o",
		PortfolioRevision:   strings.Repeat("a", 64),
		RouteIdentityDigest: strings.Repeat("b", 64),
		MetadataDigest:      strings.Repeat("c", 64),
	}
	tests := []session.SessionMetadata{
		{
			AgentID: "child", AgentRole: "Explore",
			ModelRole: "unknown", ModelBinding: binding,
		},
		{
			AgentID: "child", AgentRole: "Plan",
			ModelRole: "explore", ModelBinding: binding,
		},
		{
			AgentID: "child", AgentRole: "Explore",
			ModelRole: "explore",
		},
		{
			ModelRole: "summary", ModelBinding: binding,
		},
	}
	for index, metadata := range tests {
		if err := validateResumedModelRole(metadata); err == nil {
			t.Fatalf("case %d should fail closed: %#v", index, metadata)
		}
	}
}

type p293RoleResolver struct {
	*p292InventoryResolver
	mu       sync.RWMutex
	explicit map[engineconfig.ModelRole]string
}

func newP293RoleResolver() *p293RoleResolver {
	base := newP292InventoryResolver(200000)
	switched := base.snapshot.Entries[0]
	switched.Selector = "switched"
	switched.ProfileID = "switched"
	switched.Provider = string(provider.ProviderAgenticGemini)
	switched.APIModel = "gemini-2.5-flash"
	switched.RouteIdentityDigest = strings.Repeat("5", 64)
	switched.MetadataDigest = strings.Repeat("6", 64)
	switched.Metadata.SupportedReasoningEfforts = p292MetadataField(
		[]string{"low", "high"},
	)
	base.snapshot.Entries = append(base.snapshot.Entries, switched)
	return &p293RoleResolver{
		p292InventoryResolver: base,
		explicit:              make(map[engineconfig.ModelRole]string),
	}
}

func (r *p293RoleResolver) UsesNamedPortfolio() bool { return true }

func (r *p293RoleResolver) setExplicit(
	role engineconfig.ModelRole,
	selector string,
) {
	r.mu.Lock()
	if selector == "" {
		delete(r.explicit, role)
	} else {
		r.explicit[role] = selector
	}
	r.mu.Unlock()
}

func (r *p293RoleResolver) ResolveRoleCall(
	input provider.RoleResolutionInput,
) (provider.RoleCallSnapshot, error) {
	role := engineconfig.ModelRole(strings.ToLower(strings.TrimSpace(
		string(input.Role),
	)))
	switch role {
	case engineconfig.RoleMain,
		engineconfig.RoleExplore,
		engineconfig.RolePlan,
		engineconfig.RoleGeneral,
		engineconfig.RoleSummary:
	default:
		return provider.RoleCallSnapshot{}, fmt.Errorf("unknown role %q", role)
	}
	selector := strings.TrimSpace(input.MainSelector)
	source := provider.RoleCallSourceInheritedMain
	r.mu.RLock()
	explicit := r.explicit[role]
	r.mu.RUnlock()
	if role != engineconfig.RoleMain && explicit != "" {
		selector = explicit
		source = provider.RoleCallSourceConfigured
	}
	entry, err := r.ResolveInventorySelector(selector)
	if err != nil {
		return provider.RoleCallSnapshot{}, err
	}
	if input.Requirements.PromptTokens > entry.Metadata.ContextWindowTokens.Value {
		return provider.RoleCallSnapshot{}, fmt.Errorf("context window exceeded")
	}
	if input.Requirements.NeedReasoningHistory &&
		!entry.Metadata.Thinking.Value {
		return provider.RoleCallSnapshot{}, fmt.Errorf(
			"thinking history is unsupported",
		)
	}
	effort := strings.ToLower(strings.TrimSpace(
		input.Requirements.RequestedEffort,
	))
	if effort == "" && source == provider.RoleCallSourceInheritedMain {
		effort = strings.ToLower(strings.TrimSpace(input.MainReasoning))
	}
	if effort == "" {
		effort = entry.ReasoningDefault
	}
	return provider.RoleCallSnapshot{
		Role:                role,
		Source:              source,
		Selector:            entry.Selector,
		ProfileID:           entry.ProfileID,
		Provider:            entry.Provider,
		APIModel:            entry.APIModel,
		PortfolioRevision:   r.snapshot.Revision,
		RouteIdentityDigest: entry.RouteIdentityDigest,
		MetadataDigest:      entry.MetadataDigest,
		ContextWindowTokens: knownPositiveMetadataInt(
			entry.Metadata.ContextWindowTokens,
		),
		MaxOutputTokens: knownPositiveMetadataInt(
			entry.Metadata.MaxOutputTokens,
		),
		ReasoningEffort: effort,
		Metadata:        entry.Metadata,
	}, nil
}

type p293RecordingModel struct {
	mu    sync.Mutex
	calls []string
}

func (m *p293RecordingModel) Generate(
	_ context.Context,
	_ []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	m.record(opts)
	return &schema.Message{Role: schema.Assistant, Content: "done"}, nil
}

func (m *p293RecordingModel) Stream(
	_ context.Context,
	_ []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	m.record(opts)
	return schema.StreamReaderFromArray([]*schema.Message{{
		Role:    schema.Assistant,
		Content: "done",
	}}), nil
}

func (m *p293RecordingModel) record(opts []model.Option) {
	common := model.GetCommonOptions(nil, opts...)
	selected := ""
	if common.Model != nil {
		selected = *common.Model
	}
	m.mu.Lock()
	m.calls = append(m.calls, selected)
	m.mu.Unlock()
}

func (m *p293RecordingModel) modelCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

type p293UsageAdmitter struct {
	next       int
	descriptor execution.ProviderUsageDescriptor
}

func (u *p293UsageAdmitter) NewLogicalRoundID() string {
	u.next++
	return fmt.Sprintf("summary-round-%d", u.next)
}

func (u *p293UsageAdmitter) AdmitProviderUsage(
	_ context.Context,
	descriptor execution.ProviderUsageDescriptor,
) (execution.ProviderUsageCall, error) {
	u.descriptor = descriptor
	return p293UsageCall{}, nil
}

type p293UsageCall struct{}

func (p293UsageCall) ProviderCallID() string { return "summary-provider-call" }
func (p293UsageCall) CompleteProviderUsage(*schema.TokenUsage) error {
	return nil
}
func (p293UsageCall) ReleaseProviderUsageBeforeDispatch() error { return nil }
func (p293UsageCall) MarkProviderUsageAmbiguous(error) error    { return nil }

func newP293RoleEngine(
	t *testing.T,
	resolver *p293RoleResolver,
	shared model.BaseChatModel,
	injected model.BaseChatModel,
	injectedSelector string,
) *QueryEngine {
	t.Helper()
	cwd := t.TempDir()
	engine := NewQueryEngine(QueryEngineConfig{
		SessionID:             "p29-3-root",
		ThreadID:              "p29-3-root",
		CWD:                   cwd,
		TranscriptDir:         filepath.Join(cwd, "transcripts"),
		Model:                 "primary",
		ModelResolver:         resolver,
		ChatModel:             shared,
		SubagentModel:         injected,
		SubagentModelSelector: injectedSelector,
		ToolRegistry:          tools.NewRegistry(),
	})
	t.Cleanup(engine.Close)
	engine.agentRunner.SetOutputDir(filepath.Join(cwd, "agent-output"))
	return engine
}

func runP293Agent(
	t *testing.T,
	engine *QueryEngine,
	agentID string,
	agentType string,
) {
	t.Helper()
	runP293AgentWithThread(
		t,
		engine,
		agentID,
		agentID+"-thread",
		agentType,
	)
}

func runP293AgentWithThread(
	t *testing.T,
	engine *QueryEngine,
	agentID string,
	threadID string,
	agentType string,
) {
	t.Helper()
	_, err := tools.RunAgent(
		context.Background(),
		engine.agentRunner,
		tools.AgentExecOptions{
			Task:            "complete one role-routed turn",
			SessionID:       agentID,
			ThreadID:        threadID,
			ParentSessionID: engine.config.SessionID,
			ParentThreadID:  engine.config.ThreadID,
			AgentID:         agentID,
			SubagentType:    agentType,
			CWD:             engine.config.CWD,
			MaxTurns:        1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func waitP293AgentTerminal(
	t *testing.T,
	runner *tools.AgentRunner,
	agentID string,
) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, ok := runner.GetAgentSnapshot(agentID)
		if ok && snapshot.Status != "running" {
			if snapshot.Status != "completed" {
				t.Fatalf("resumed Agent status = %q error=%v", snapshot.Status, snapshot.Error)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("resumed Agent did not reach terminal state")
}

func assertP293ChildMetadata(
	t *testing.T,
	transcriptDir string,
	sessionID string,
	agentRole string,
	modelRole string,
	bindingValue string,
) {
	t.Helper()
	loaded, err := transcript.NewRecorder(sessionID, transcriptDir).LoadFull()
	if err != nil {
		t.Fatal(err)
	}
	metadata := session.ReadSessionMetadataFull(loaded)
	if metadata == nil ||
		metadata.AgentRole != agentRole ||
		metadata.ModelRole != modelRole ||
		metadata.ModelBinding == nil ||
		metadata.ModelBinding.Value != bindingValue {
		t.Fatalf("child metadata = %#v", metadata)
	}
}

func seedP293LegacyChild(
	transcriptDir string,
	cwd string,
	sessionID string,
	threadID string,
) error {
	recorder := transcript.NewRecorder(sessionID, transcriptDir)
	if err := recorder.Replace(nil); err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := session.WriteSessionMetadata(
		recorder,
		&session.SessionMetadataFull{
			SessionID:          sessionID,
			ParentSessionID:    "p29-3-root",
			ParentThreadID:     "p29-3-root",
			ThreadID:           threadID,
			AgentID:            sessionID,
			AgentGeneration:    1,
			QueryKernelVersion: queryKernelVersionProjectGraph,
			QueryKernelStage: string(
				queryKernelStageForegroundChild,
			),
			Status:    "running",
			CWD:       cwd,
			CreatedAt: now,
			UpdatedAt: now,
		},
	); err != nil {
		return err
	}
	return recorder.Close()
}

func p293EqualStrings(left, right []string) bool {
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

var (
	_ ModelResolver         = (*p293RoleResolver)(nil)
	_ runtimeModelInventory = (*p293RoleResolver)(nil)
	_ runtimeModelRoles     = (*p293RoleResolver)(nil)
)
