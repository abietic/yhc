package provider

import (
	"context"
	"strings"
	"sync"
	"testing"

	engineconfig "github.com/abietic/yhc/engine/config"
	enginemodel "github.com/abietic/yhc/engine/model"
	"github.com/cloudwego/eino/components/model"
)

func TestRoleResolverExplicitAndDynamicMainInheritance(t *testing.T) {
	runtime := roleTestRuntime(
		map[engineconfig.ModelRole]engineconfig.ProfileID{
			engineconfig.RoleExplore: "secondary",
		},
		roleTestEntry("primary", ProviderAgenticOpenAI, "gpt-5.1", ""),
		roleTestEntry("secondary", ProviderAgenticClaude, "claude-sonnet-4-6", ""),
		roleTestEntry("switched", ProviderAgenticGemini, "gemini-2.5-flash", ""),
	)

	explore, err := runtime.ResolveRoleCall(RoleResolutionInput{
		Role:         engineconfig.RoleExplore,
		MainSelector: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if explore.Source != RoleCallSourceConfigured ||
		explore.Selector != "secondary" ||
		explore.Provider != string(ProviderAgenticClaude) {
		t.Fatalf("explicit explore = %#v", explore)
	}

	first, err := runtime.ResolveRoleCall(RoleResolutionInput{
		Role:         engineconfig.RolePlan,
		MainSelector: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.ResolveRoleCall(RoleResolutionInput{
		Role:         engineconfig.RolePlan,
		MainSelector: "switched",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Source != RoleCallSourceInheritedMain ||
		first.Selector != "primary" ||
		second.Selector != "switched" {
		t.Fatalf("dynamic inheritance = %#v then %#v", first, second)
	}
}

func TestRoleResolverPreservesOnlyLegacyBoundInheritedMain(t *testing.T) {
	metadata, err := enginemodel.ResolvePortfolioMetadata(
		"haiku",
		enginemodel.MetadataOverrides{},
	)
	if err != nil {
		t.Fatal(err)
	}
	entry := RuntimeInventoryEntry{
		Selector:            "legacy:haiku",
		ProfileID:           "legacy.main",
		Provider:            string(ProviderAgenticClaude),
		APIModel:            "haiku",
		RouteIdentityDigest: "legacy-route",
		MetadataDigest:      "legacy-metadata",
		Metadata:            metadata,
	}
	runtime := roleTestRuntime(nil, entry)
	runtime.portfolio.Default = "legacy.main"

	main, err := runtime.ResolveFailoverChain(RoleResolutionInput{
		Role:         engineconfig.RoleMain,
		MainSelector: "legacy:haiku",
	})
	if err != nil {
		t.Fatal(err)
	}
	if main.Primary.Source != RoleCallSourceInheritedMain ||
		main.Primary.Selector != "legacy:haiku" {
		t.Fatalf("legacy main = %#v", main.Primary)
	}
	if _, err := runtime.ResolveRoleCall(RoleResolutionInput{
		Role:         engineconfig.RoleExplore,
		MainSelector: "legacy:haiku",
	}); err == nil || !strings.Contains(err.Error(), "authoritative text") {
		t.Fatalf("legacy automatic role admission error = %v", err)
	}

	runtime.portfolio.Default = "named-main"
	if _, err := runtime.ResolveRoleCall(RoleResolutionInput{
		Role:         engineconfig.RoleMain,
		MainSelector: "legacy:haiku",
	}); err == nil || !strings.Contains(err.Error(), "authoritative text") {
		t.Fatalf("named runtime legacy main admission error = %v", err)
	}
}

func TestRoleResolverStaticAndDynamicCapabilityAdmission(t *testing.T) {
	entry := roleTestEntry("primary", ProviderAgenticOpenAI, "gpt-5.1", "")
	entry.Metadata.Tools.Value = false
	runtime := roleTestRuntime(nil, entry)
	if _, err := runtime.ResolveRoleCall(RoleResolutionInput{
		Role:         engineconfig.RoleSummary,
		MainSelector: "primary",
	}); err != nil {
		t.Fatalf("summary should not require tools: %v", err)
	}
	if _, err := runtime.ResolveRoleCall(RoleResolutionInput{
		Role:         engineconfig.RoleGeneral,
		MainSelector: "primary",
	}); err == nil || !strings.Contains(err.Error(), "tools") {
		t.Fatalf("general tools admission error = %v", err)
	}

	tests := []struct {
		name         string
		mutate       func(*Runtime)
		requirements RoleRequirements
		want         string
	}{
		{
			name: "image",
			mutate: func(runtime *Runtime) {
				runtime.inventory.Entries[0].Metadata.Images.Value = false
			},
			requirements: RoleRequirements{NeedImage: true},
			want:         "images",
		},
		{
			name: "pdf unknown",
			mutate: func(runtime *Runtime) {
				runtime.inventory.Entries[0].Metadata.PDFs.Source = "unknown"
			},
			requirements: RoleRequirements{NeedPDF: true},
			want:         "pdfs",
		},
		{
			name: "reasoning history",
			mutate: func(runtime *Runtime) {
				runtime.inventory.Entries[0].Metadata.Thinking.Value = false
			},
			requirements: RoleRequirements{NeedReasoningHistory: true},
			want:         "thinking",
		},
		{
			name:         "context overflow",
			mutate:       func(*Runtime) {},
			requirements: RoleRequirements{PromptTokens: 8193},
			want:         "exceed context window",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := roleTestRuntime(
				nil,
				roleTestEntry(
					"primary",
					ProviderAgenticOpenAI,
					"gpt-5.1",
					"",
				),
			)
			test.mutate(runtime)
			_, err := runtime.ResolveRoleCall(RoleResolutionInput{
				Role:         engineconfig.RoleGeneral,
				MainSelector: "primary",
				Requirements: test.requirements,
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("admission error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRoleResolverReasoningPrecedenceAndAdapterTable(t *testing.T) {
	entry := roleTestEntry(
		"primary",
		ProviderAgenticOpenAI,
		"gpt-5.1",
		"medium",
	)
	runtime := roleTestRuntime(nil, entry)
	inherited, err := runtime.ResolveRoleCall(RoleResolutionInput{
		Role:          engineconfig.RolePlan,
		MainSelector:  "primary",
		MainReasoning: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if inherited.ReasoningEffort != "high" {
		t.Fatalf("inherited effort = %q", inherited.ReasoningEffort)
	}
	requested, err := runtime.ResolveRoleCall(RoleResolutionInput{
		Role:          engineconfig.RolePlan,
		MainSelector:  "primary",
		MainReasoning: "high",
		Requirements:  RoleRequirements{RequestedEffort: "minimal"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if requested.ReasoningEffort != "minimal" {
		t.Fatalf("requested effort = %q", requested.ReasoningEffort)
	}

	explicitRuntime := roleTestRuntime(
		map[engineconfig.ModelRole]engineconfig.ProfileID{
			engineconfig.RolePlan: "primary",
		},
		entry,
	)
	explicit, err := explicitRuntime.ResolveRoleCall(RoleResolutionInput{
		Role:          engineconfig.RolePlan,
		MainSelector:  "primary",
		MainReasoning: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.ReasoningEffort != "medium" {
		t.Fatalf("explicit role did not use profile default: %q", explicit.ReasoningEffort)
	}

	cases := []struct {
		provider Provider
		effort   string
		ok       bool
	}{
		{ProviderAgenticClaude, "max", true},
		{ProviderAgenticOpenAI, "none", true},
		{ProviderAgenticArk, "minimal", true},
		{ProviderAgenticGemini, "high", true},
		{ProviderAgenticGemini, "medium", false},
		{ProviderAgenticDeepSeek, "low", false},
		{ProviderAgenticQwen, "high", false},
	}
	for _, test := range cases {
		t.Run(string(test.provider)+"-"+test.effort, func(t *testing.T) {
			entry := roleTestEntry("primary", test.provider, "model", "")
			entry.Metadata.SupportedReasoningEfforts.Value = []string{
				"none", "minimal", "low", "medium", "high", "xhigh", "max",
			}
			runtime := roleTestRuntime(nil, entry)
			_, err := runtime.ResolveRoleCall(RoleResolutionInput{
				Role:         engineconfig.RoleMain,
				MainSelector: "primary",
				Requirements: RoleRequirements{RequestedEffort: test.effort},
			})
			if (err == nil) != test.ok {
				t.Fatalf("adapter effort error = %v, want ok=%t", err, test.ok)
			}
		})
	}
}

func TestRoleResolverReturnsDetachedSnapshotWithoutRouteConstruction(t *testing.T) {
	entry := roleTestEntry("primary", ProviderAgenticOpenAI, "gpt-5.1", "high")
	factoryCalls := 0
	runtime := roleTestRuntime(nil, entry)
	runtime.routes.factory = func(
		_ context.Context,
		_ Config,
	) (model.BaseChatModel, error) {
		factoryCalls++
		return nil, nil
	}
	snapshot, err := runtime.ResolveRoleCall(RoleResolutionInput{
		Role:         engineconfig.RoleMain,
		MainSelector: "primary",
		Requirements: RoleRequirements{
			NeedImage:       true,
			PromptTokens:    10,
			RequestedEffort: " HIGH ",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if factoryCalls != 0 {
		t.Fatalf("role resolution constructed %d provider clients", factoryCalls)
	}
	if !snapshot.Requirements.NeedImage ||
		snapshot.Requirements.PromptTokens != 10 ||
		snapshot.Requirements.RequestedEffort != "high" {
		t.Fatalf("admitted dynamic requirements = %#v", snapshot.Requirements)
	}
	*snapshot.ContextWindowTokens = 1
	snapshot.Metadata.SupportedReasoningEfforts.Value[0] = "mutated"
	again, err := runtime.ResolveRoleCall(RoleResolutionInput{
		Role:         engineconfig.RoleMain,
		MainSelector: "primary",
	})
	if err != nil {
		t.Fatal(err)
	}
	if *again.ContextWindowTokens != 8192 ||
		again.Metadata.SupportedReasoningEfforts.Value[0] == "mutated" {
		t.Fatalf("snapshot retained caller mutation: %#v", again)
	}
}

func TestRoleResolverSnapshotsReachSharedRuntimeAcrossProvidersAndProfiles(
	t *testing.T,
) {
	factoryCalls := make(map[Provider]int)
	recorders := make(map[Provider]*routeRecorder)
	var factoryMu sync.Mutex
	runtime, err := NewConfiguredRuntime(
		context.Background(),
		ConfiguredRuntimeOptions{
			Sources: &engineconfig.ConfigSources{
				User: &engineconfig.Config{
					ModelProfile: "main",
					ProviderAccounts: map[string]engineconfig.ProviderAccountConfig{
						"openai": {
							Provider: "openai",
							Auth: engineconfig.AccountAuthConfig{
								Kind: "env", Name: "OPENAI_ROLE_KEY",
							},
						},
						"google": {
							Provider: "google",
							Auth: engineconfig.AccountAuthConfig{
								Kind: "env", Name: "GOOGLE_ROLE_KEY",
							},
						},
					},
					ModelProfiles: map[string]engineconfig.ModelProfileConfig{
						"main": {
							Account: "openai", APIModel: "gpt-4o",
						},
						"general": {
							Account: "openai", APIModel: "gpt-4o-mini",
						},
						"explore": {
							Account: "google", APIModel: "gemini-2.5-flash",
						},
					},
					ModelRoles: map[string]string{
						"general": "general",
						"explore": "explore",
					},
				},
				Project: &engineconfig.Config{},
			},
			Resolution: ResolveInput{
				Getenv: getenvMap(map[string]string{
					"OPENAI_ROLE_KEY": "openai-key",
					"GOOGLE_ROLE_KEY": "google-key",
				}),
			},
			factory: func(
				_ context.Context,
				config Config,
			) (model.BaseChatModel, error) {
				factoryMu.Lock()
				defer factoryMu.Unlock()
				factoryCalls[config.Provider]++
				recorder := recorders[config.Provider]
				if recorder == nil {
					recorder = &routeRecorder{}
					recorders[config.Provider] = recorder
				}
				return &routeModel{
					provider: config.Provider,
					recorder: recorder,
				}, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if factoryCalls[ProviderAgenticOpenAI] != 1 ||
		factoryCalls[ProviderAgenticGemini] != 0 {
		t.Fatalf("startup factory calls = %#v", factoryCalls)
	}

	explore, err := runtime.ResolveRoleCall(RoleResolutionInput{
		Role:         engineconfig.RoleExplore,
		MainSelector: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if factoryCalls[ProviderAgenticGemini] != 0 {
		t.Fatal("role resolution initialized the Gemini client")
	}
	response, err := runtime.ChatModel.Generate(
		context.Background(),
		nil,
		model.WithModel(explore.Selector),
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != string(ProviderAgenticGemini) ||
		factoryCalls[ProviderAgenticGemini] != 1 ||
		len(recorders[ProviderAgenticGemini].calls) != 1 ||
		recorders[ProviderAgenticGemini].calls[0].model !=
			"gemini-2.5-flash" {
		t.Fatalf(
			"cross-provider route response=%#v calls=%#v recorder=%#v",
			response,
			factoryCalls,
			recorders[ProviderAgenticGemini],
		)
	}

	general, err := runtime.ResolveRoleCall(RoleResolutionInput{
		Role:         engineconfig.RoleGeneral,
		MainSelector: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err = runtime.ChatModel.Generate(
		context.Background(),
		nil,
		model.WithModel(general.Selector),
	)
	if err != nil {
		t.Fatal(err)
	}
	openAICalls := recorders[ProviderAgenticOpenAI].calls
	if response.Content != string(ProviderAgenticOpenAI) ||
		factoryCalls[ProviderAgenticOpenAI] != 1 ||
		len(openAICalls) != 1 ||
		openAICalls[0].model != "gpt-4o-mini" {
		t.Fatalf(
			"same-route profile response=%#v calls=%#v recorder=%#v",
			response,
			factoryCalls,
			openAICalls,
		)
	}
}

func TestResolveFailoverChainIsDetachedOrderedAndAdmissionAware(t *testing.T) {
	primary := roleTestEntry(
		"primary",
		ProviderAgenticOpenAI,
		"gpt-5",
		"medium",
	)
	alternate := roleTestEntry(
		"alternate",
		ProviderAgenticOpenAI,
		"gpt-5-mini",
		"medium",
	)
	incompatible := roleTestEntry(
		"incompatible",
		ProviderAgenticOpenAI,
		"gpt-5-nano",
		"medium",
	)
	incompatible.Metadata.Images = enginemodel.MetadataField[bool]{
		Value:  false,
		Source: "test",
	}
	runtime := roleTestRuntime(nil, primary, alternate, incompatible)
	runtime.portfolio.Failover = map[engineconfig.ModelRole]engineconfig.ResolvedFailoverPolicy{
		engineconfig.RoleMain: {
			Alternates: []engineconfig.ProfileID{
				"alternate",
				"incompatible",
			},
			On:               []string{"overloaded"},
			MaxSwitches:      2,
			MaxProviderCalls: 7,
			MaxElapsedMS:     1234,
		},
	}

	snapshot, err := runtime.ResolveFailoverChain(RoleResolutionInput{
		Role:         engineconfig.RoleMain,
		MainSelector: "primary",
		Requirements: RoleRequirements{
			NeedImage:       true,
			RequestedEffort: "medium",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Primary.ProfileID != "primary" ||
		snapshot.PortfolioRevision != "portfolio-revision" ||
		snapshot.MaxSwitches != 2 ||
		snapshot.MaxProviderCalls != 7 ||
		snapshot.MaxElapsedMS != 1234 {
		t.Fatalf("unexpected failover snapshot: %#v", snapshot)
	}
	if len(snapshot.Alternates) != 2 {
		t.Fatalf("alternate count = %d, want 2", len(snapshot.Alternates))
	}
	if snapshot.Alternates[0].Call.ProfileID != "alternate" ||
		snapshot.Alternates[0].AdmissionCode != "" {
		t.Fatalf("admitted alternate = %#v", snapshot.Alternates[0])
	}
	if snapshot.Alternates[1].ProfileID != "incompatible" ||
		snapshot.Alternates[1].AdmissionCode != "capability_image" {
		t.Fatalf("incompatible alternate = %#v", snapshot.Alternates[1])
	}
	if len(runtime.routes.models) != 0 {
		t.Fatalf("detached resolution initialized %d routes", len(runtime.routes.models))
	}

	snapshot.On[0] = "mutated"
	if runtime.portfolio.Failover[engineconfig.RoleMain].On[0] != "overloaded" {
		t.Fatal("failover snapshot aliases portfolio policy")
	}
}

func TestP294FailoverCandidateAdmissionCodesAreStableAndNoCall(t *testing.T) {
	tests := []struct {
		name         string
		requirements RoleRequirements
		mutate       func(*RuntimeInventoryEntry)
		wantCode     string
	}{
		{
			name:         "image",
			requirements: RoleRequirements{NeedImage: true},
			mutate: func(entry *RuntimeInventoryEntry) {
				entry.Metadata.Images.Value = false
			},
			wantCode: "capability_image",
		},
		{
			name:         "pdf",
			requirements: RoleRequirements{NeedPDF: true},
			mutate: func(entry *RuntimeInventoryEntry) {
				entry.Metadata.PDFs.Value = false
			},
			wantCode: "capability_pdf",
		},
		{
			name:         "reasoning history",
			requirements: RoleRequirements{NeedReasoningHistory: true},
			mutate: func(entry *RuntimeInventoryEntry) {
				entry.Metadata.Thinking.Value = false
			},
			wantCode: "capability_reasoning_history",
		},
		{
			name:         "reasoning effort",
			requirements: RoleRequirements{RequestedEffort: "medium"},
			mutate: func(entry *RuntimeInventoryEntry) {
				entry.Metadata.SupportedReasoningEfforts.Value = []string{"low"}
			},
			wantCode: "capability_reasoning_effort",
		},
		{
			name:         "smaller context",
			requirements: RoleRequirements{PromptTokens: 9},
			mutate: func(entry *RuntimeInventoryEntry) {
				entry.Metadata.ContextWindowTokens.Value = 8
			},
			wantCode: "context_window",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primary := roleTestEntry(
				"primary",
				ProviderAgenticOpenAI,
				"gpt-5",
				"medium",
			)
			alternate := roleTestEntry(
				"alternate",
				ProviderAgenticOpenAI,
				"gpt-5-mini",
				"medium",
			)
			test.mutate(&alternate)
			runtime := roleTestRuntime(nil, primary, alternate)
			runtime.portfolio.Failover = map[engineconfig.ModelRole]engineconfig.ResolvedFailoverPolicy{
				engineconfig.RoleMain: {
					Alternates:       []engineconfig.ProfileID{"alternate"},
					On:               []string{"overloaded"},
					MaxSwitches:      1,
					MaxProviderCalls: 6,
					MaxElapsedMS:     45000,
				},
			}
			snapshot, err := runtime.ResolveFailoverChain(
				RoleResolutionInput{
					Role:         engineconfig.RoleMain,
					MainSelector: "primary",
					Requirements: test.requirements,
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Alternates) != 1 ||
				snapshot.Alternates[0].AdmissionCode != test.wantCode {
				t.Fatalf(
					"candidate = %#v, want code %q",
					snapshot.Alternates,
					test.wantCode,
				)
			}
			if len(runtime.routes.models) != 0 {
				t.Fatalf(
					"candidate admission initialized %d routes",
					len(runtime.routes.models),
				)
			}
		})
	}
}

func roleTestRuntime(
	explicit map[engineconfig.ModelRole]engineconfig.ProfileID,
	entries ...RuntimeInventoryEntry,
) *Runtime {
	return &Runtime{
		routes: &routeRegistry{},
		portfolio: &engineconfig.PortfolioSnapshot{
			Revision:      "portfolio-revision",
			ExplicitRoles: explicit,
		},
		inventory: RuntimeInventorySnapshot{
			Revision: "portfolio-revision",
			Default:  entries[0].Selector,
			Entries:  entries,
		},
	}
}

func roleTestEntry(
	profile string,
	provider Provider,
	apiModel string,
	defaultEffort string,
) RuntimeInventoryEntry {
	return RuntimeInventoryEntry{
		Selector:            profile,
		ProfileID:           profile,
		Provider:            string(provider),
		APIModel:            apiModel,
		ReasoningDefault:    defaultEffort,
		RouteIdentityDigest: "route-" + profile,
		MetadataDigest:      "metadata-" + profile,
		Metadata: enginemodel.EffectiveModelMetadata{
			ContextWindowTokens: enginemodel.MetadataField[int]{
				Value: 8192, Source: "test",
			},
			MaxOutputTokens: enginemodel.MetadataField[int]{
				Value: 2048, Source: "test",
			},
			Text:         enginemodel.MetadataField[bool]{Value: true, Source: "test"},
			Streaming:    enginemodel.MetadataField[bool]{Value: true, Source: "test"},
			Tools:        enginemodel.MetadataField[bool]{Value: true, Source: "test"},
			SystemPrompt: enginemodel.MetadataField[bool]{Value: true, Source: "test"},
			Images:       enginemodel.MetadataField[bool]{Value: true, Source: "test"},
			PDFs:         enginemodel.MetadataField[bool]{Value: true, Source: "test"},
			Thinking:     enginemodel.MetadataField[bool]{Value: true, Source: "test"},
			SupportedReasoningEfforts: enginemodel.MetadataField[[]string]{
				Value: []string{
					"none", "minimal", "low", "medium", "high", "xhigh", "max",
				},
				Source: "test",
			},
		},
	}
}
