package provider

import (
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	openaischema "github.com/cloudwego/eino/schema/openai"
)

// These fixtures are the executable P38.0 target contract. They are
// deliberately test-only: production must keep stripping private provider
// continuation state until the route owner supplies and rechecks every field.
const p38ReasoningOriginFixtureVersion uint16 = 1

const (
	p38OriginStateAbsent   = "absent"
	p38OriginStateLegacy   = "legacy_unverified"
	p38OriginStateVerified = "verified"
)

type p38ReasoningOriginFixture struct {
	Version             uint16
	Provider            Provider
	AccountID           string
	APIFamily           string
	APIModel            string
	RouteIdentityDigest string
	CredentialOriginID  string
}

type p38DispatchOriginFixture struct {
	Origin                   p38ReasoningOriginFixture
	CapturedRoutePublication uint64
	CurrentRoutePublication  uint64
	RecoveryVerified         bool
}

type p38MessageOriginBindingFixture struct {
	EntryVersion     int
	EntryID          string
	MessageIndex     int
	LogicalMessageID string
	PayloadDigest    string
}

type p38PublishedRouteFixture struct {
	Origin      p38ReasoningOriginFixture
	Publication uint64
}

type p38RouteRegistryFixture struct {
	mu          sync.Mutex
	publication uint64
	current     *p38PublishedRouteFixture
}

func p38OriginFixturePtr(value p38ReasoningOriginFixture) *p38ReasoningOriginFixture {
	return &value
}

func p38EvaluateOriginFixture(
	state string,
	history *p38ReasoningOriginFixture,
	dispatch p38DispatchOriginFixture,
) (bool, string) {
	if state == p38OriginStateAbsent || history == nil {
		return false, "origin_absent"
	}
	if state == p38OriginStateLegacy ||
		history.Version != p38ReasoningOriginFixtureVersion {
		return false, "origin_legacy_unverified"
	}
	if !dispatch.RecoveryVerified {
		return false, "origin_recovery_mismatch"
	}
	if history.Provider != dispatch.Origin.Provider {
		return false, "origin_provider_mismatch"
	}
	if history.AccountID != dispatch.Origin.AccountID {
		return false, "origin_account_mismatch"
	}
	if history.APIFamily != dispatch.Origin.APIFamily {
		return false, "origin_api_family_mismatch"
	}
	if history.APIModel != dispatch.Origin.APIModel {
		return false, "origin_api_model_mismatch"
	}
	if history.CredentialOriginID != dispatch.Origin.CredentialOriginID {
		return false, "origin_credential_mismatch"
	}
	if dispatch.Origin.Version != p38ReasoningOriginFixtureVersion ||
		history.RouteIdentityDigest != dispatch.Origin.RouteIdentityDigest ||
		dispatch.CapturedRoutePublication == 0 ||
		dispatch.CapturedRoutePublication != dispatch.CurrentRoutePublication {
		return false, "origin_route_stale"
	}
	return true, "origin_exact"
}

func p38ExactOriginFixture() p38ReasoningOriginFixture {
	return p38ReasoningOriginFixture{
		Version:             p38ReasoningOriginFixtureVersion,
		Provider:            ProviderAgenticOpenAI,
		AccountID:           "work-openai",
		APIFamily:           "openai-responses/v1",
		APIModel:            "gpt-5.4",
		RouteIdentityDigest: strings.Repeat("a", 64),
		CredentialOriginID:  "credential-origin-17",
	}
}

func p38DispatchFixture(origin p38ReasoningOriginFixture) p38DispatchOriginFixture {
	return p38DispatchOriginFixture{
		Origin:                   origin,
		CapturedRoutePublication: 41,
		CurrentRoutePublication:  41,
		RecoveryVerified:         true,
	}
}

func (r *p38RouteRegistryFixture) publish(
	origin p38ReasoningOriginFixture,
) p38PublishedRouteFixture {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil || !reflect.DeepEqual(r.current.Origin, origin) {
		r.publication++
		r.current = &p38PublishedRouteFixture{
			Origin:      origin,
			Publication: r.publication,
		}
	}
	return *r.current
}

func (r *p38RouteRegistryFixture) dispatch(
	captured p38PublishedRouteFixture,
) p38DispatchOriginFixture {
	r.mu.Lock()
	defer r.mu.Unlock()
	return p38DispatchOriginFixture{
		Origin:                   captured.Origin,
		CapturedRoutePublication: captured.Publication,
		CurrentRoutePublication:  r.current.Publication,
		RecoveryVerified:         true,
	}
}

func TestP380OriginFixtureAllowsOnlyExactCurrentOrigin(t *testing.T) {
	t.Parallel()

	for _, entrypoint := range []string{"generate", "stream"} {
		t.Run(entrypoint, func(t *testing.T) {
			t.Parallel()
			origin := p38ExactOriginFixture()
			allowed, reason := p38EvaluateOriginFixture(
				p38OriginStateVerified,
				&origin,
				p38DispatchFixture(origin),
			)
			if !allowed || reason != "origin_exact" {
				t.Fatalf("decision = (%v, %q), want exact-origin allow", allowed, reason)
			}
		})
	}

	origin := p38ExactOriginFixture()
	providerSwitch := origin
	providerSwitch.Provider = ProviderAgenticClaude
	accountFallback := origin
	accountFallback.AccountID = "fallback-openai"
	apiFamilySwitch := origin
	apiFamilySwitch.APIFamily = "openai-chat-completions/v1"
	manualModelSwitch := origin
	manualModelSwitch.APIModel = "gpt-5.4-mini"
	credentialRotation := origin
	credentialRotation.CredentialOriginID = "credential-origin-18"
	routeChange := origin
	routeChange.RouteIdentityDigest = strings.Repeat("b", 64)
	legacyOrigin := origin
	legacyOrigin.Version = 0

	tests := []struct {
		name     string
		state    string
		history  *p38ReasoningOriginFixture
		dispatch p38DispatchOriginFixture
		want     string
	}{
		{name: "origin absent", state: p38OriginStateAbsent, dispatch: p38DispatchFixture(origin), want: "origin_absent"},
		{name: "legacy origin", state: p38OriginStateLegacy, history: p38OriginFixturePtr(legacyOrigin), dispatch: p38DispatchFixture(origin), want: "origin_legacy_unverified"},
		{name: "provider switch", state: p38OriginStateVerified, history: &origin, dispatch: p38DispatchFixture(providerSwitch), want: "origin_provider_mismatch"},
		{name: "bounded account fallback", state: p38OriginStateVerified, history: &origin, dispatch: p38DispatchFixture(accountFallback), want: "origin_account_mismatch"},
		{name: "api family switch", state: p38OriginStateVerified, history: &origin, dispatch: p38DispatchFixture(apiFamilySwitch), want: "origin_api_family_mismatch"},
		{name: "manual model switch", state: p38OriginStateVerified, history: &origin, dispatch: p38DispatchFixture(manualModelSwitch), want: "origin_api_model_mismatch"},
		{name: "credential rotation", state: p38OriginStateVerified, history: &origin, dispatch: p38DispatchFixture(credentialRotation), want: "origin_credential_mismatch"},
		{name: "route identity change", state: p38OriginStateVerified, history: &origin, dispatch: p38DispatchFixture(routeChange), want: "origin_route_stale"},
		{name: "recovery mismatch", state: p38OriginStateVerified, history: &origin, dispatch: func() p38DispatchOriginFixture {
			value := p38DispatchFixture(origin)
			value.RecoveryVerified = false
			return value
		}(), want: "origin_recovery_mismatch"},
		{name: "publication race", state: p38OriginStateVerified, history: &origin, dispatch: func() p38DispatchOriginFixture {
			value := p38DispatchFixture(origin)
			value.CurrentRoutePublication++
			return value
		}(), want: "origin_route_stale"},
		{name: "missing publication", state: p38OriginStateVerified, history: &origin, dispatch: func() p38DispatchOriginFixture {
			value := p38DispatchFixture(origin)
			value.CapturedRoutePublication = 0
			return value
		}(), want: "origin_route_stale"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			allowed, reason := p38EvaluateOriginFixture(test.state, test.history, test.dispatch)
			if allowed || reason != test.want {
				t.Fatalf("decision = (%v, %q), want deny reason %q", allowed, reason, test.want)
			}
			for _, privateValue := range []string{
				origin.AccountID,
				origin.APIFamily,
				origin.APIModel,
				origin.RouteIdentityDigest,
				origin.CredentialOriginID,
			} {
				if strings.Contains(reason, privateValue) {
					t.Fatalf("reason %q contains private identity %q", reason, privateValue)
				}
			}
		})
	}
}

func TestP380OriginFixtureRequiresPhysicalMessageBinding(t *testing.T) {
	t.Parallel()

	exact := p38MessageOriginBindingFixture{
		EntryVersion:     1,
		EntryID:          "physical-entry-17",
		MessageIndex:     2,
		LogicalMessageID: "logical-assistant-9",
		PayloadDigest:    strings.Repeat("c", 64),
	}
	if !p38MessageBindingMatchesFixture(exact, exact) {
		t.Fatal("exact physical and logical binding did not match")
	}
	invalidDigest := exact
	invalidDigest.PayloadDigest = "ABC"
	if p38MessageBindingMatchesFixture(invalidDigest, invalidDigest) {
		t.Fatal("non-canonical payload digest matched")
	}

	tests := []struct {
		name    string
		binding p38MessageOriginBindingFixture
	}{
		{name: "entry version", binding: func() p38MessageOriginBindingFixture { value := exact; value.EntryVersion++; return value }()},
		{name: "physical entry", binding: func() p38MessageOriginBindingFixture {
			value := exact
			value.EntryID = "physical-entry-18"
			return value
		}()},
		{name: "message index", binding: func() p38MessageOriginBindingFixture { value := exact; value.MessageIndex++; return value }()},
		{name: "logical id", binding: func() p38MessageOriginBindingFixture {
			value := exact
			value.LogicalMessageID = "logical-assistant-10"
			return value
		}()},
		{name: "payload digest", binding: func() p38MessageOriginBindingFixture {
			value := exact
			value.PayloadDigest = strings.Repeat("d", 64)
			return value
		}()},
	}
	origin := p38ExactOriginFixture()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dispatch := p38DispatchFixture(origin)
			dispatch.RecoveryVerified = p38MessageBindingMatchesFixture(exact, test.binding)
			allowed, reason := p38EvaluateOriginFixture(
				p38OriginStateVerified,
				&origin,
				dispatch,
			)
			if allowed || reason != "origin_recovery_mismatch" {
				t.Fatalf("decision = (%v, %q), want recovery mismatch", allowed, reason)
			}
		})
	}
}

func TestP380OriginFixtureRoutePublicationRaceStripsBeforeAgenticLeaf(t *testing.T) {
	t.Parallel()

	origin := p38ExactOriginFixture()
	registry := &p38RouteRegistryFixture{}
	captured := registry.publish(origin)
	rotated := origin
	rotated.CredentialOriginID = "credential-origin-18"
	current := registry.publish(rotated)
	if current.Publication <= captured.Publication {
		t.Fatalf("publication did not advance: captured=%d current=%d", captured.Publication, current.Publication)
	}

	converted, allowed, reason, err := p38PrepareAgenticTransportFixture(
		[]*schema.Message{p38RichAssistantFixture()},
		p38OriginStateVerified,
		&origin,
		registry.dispatch(captured),
	)
	if err != nil {
		t.Fatalf("prepare stale transport returned error: %v", err)
	}
	if allowed || reason != "origin_route_stale" {
		t.Fatalf("decision = (%v, %q), want stale publication", allowed, reason)
	}
	assertP38AgenticPublicFixture(t, converted, false)

	converted, allowed, reason, err = p38PrepareAgenticTransportFixture(
		[]*schema.Message{p38RichAssistantFixture()},
		p38OriginStateVerified,
		&rotated,
		registry.dispatch(current),
	)
	if err != nil {
		t.Fatalf("prepare current transport returned error: %v", err)
	}
	if !allowed || reason != "origin_exact" {
		t.Fatalf("decision = (%v, %q), want current publication", allowed, reason)
	}
	assertP38AgenticPublicFixture(t, converted, true)
}

func TestP380OriginFixtureCannotForgeProductionSelfMarker(t *testing.T) {
	t.Parallel()

	input := &schema.Message{
		Role:             schema.Assistant,
		Content:          "public answer",
		ReasoningContent: "private summary",
		Extra: map[string]any{
			"openai-generated": true,
			"reasoning_origin": p38ExactOriginFixture(),
		},
		ToolCalls: []schema.ToolCall{{
			ID:   "call-1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "Read",
				Arguments: `{"file_path":"README.md"}`,
			},
		}},
	}

	converted, err := messagesToAgentic([]*schema.Message{input})
	if err != nil {
		t.Fatalf("messagesToAgentic returned error: %v", err)
	}
	if len(converted) != 1 {
		t.Fatalf("converted messages = %d, want 1", len(converted))
	}
	if converted[0].Extra != nil {
		t.Fatalf("converted Extra = %#v, want no caller-authorized marker", converted[0].Extra)
	}
	if len(converted[0].ContentBlocks) != 3 {
		t.Fatalf("content blocks = %#v, want reasoning, text, and tool call", converted[0].ContentBlocks)
	}
	if input.Extra["openai-generated"] != true {
		t.Fatalf("input metadata mutated: %#v", input.Extra)
	}
}

func TestP380OriginFixtureStripsEveryPrivatePartWithoutChangingPublicHistory(t *testing.T) {
	t.Parallel()

	input := p38RichAssistantFixture()

	origin := p38ExactOriginFixture()
	credentialRotation := origin
	credentialRotation.CredentialOriginID = "credential-origin-18"
	converted, allowed, reason, err := p38PrepareAgenticTransportFixture(
		[]*schema.Message{input},
		p38OriginStateVerified,
		&origin,
		p38DispatchFixture(credentialRotation),
	)
	if err != nil {
		t.Fatalf("prepare rejected transport returned error: %v", err)
	}
	if allowed || reason != "origin_credential_mismatch" {
		t.Fatalf("decision = (%v, %q), want credential mismatch", allowed, reason)
	}
	assertP38AgenticPublicFixture(t, converted, false)

	exact, allowed, reason, err := p38PrepareAgenticTransportFixture(
		[]*schema.Message{input},
		p38OriginStateVerified,
		&origin,
		p38DispatchFixture(origin),
	)
	if err != nil {
		t.Fatalf("prepare exact transport returned error: %v", err)
	}
	if !allowed || reason != "origin_exact" {
		t.Fatalf("decision = (%v, %q), want exact origin", allowed, reason)
	}
	assertP38AgenticPublicFixture(t, exact, true)

	if input.ReasoningContent != "private-a private-b" ||
		len(input.AssistantGenMultiContent) != 4 ||
		input.AssistantGenMultiContent[0].Reasoning.Signature != "signature-a" {
		t.Fatalf("caller-owned history mutated: %#v", input)
	}
}

func p38RichAssistantFixture() *schema.Message {
	index0, index1, index2, index3 := 0, 1, 2, 3
	return &schema.Message{
		Role:             schema.Assistant,
		Content:          "public-a public-b",
		ReasoningContent: "private-a private-b",
		Extra:            map[string]any{"adapter-private": true},
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: "private-a", Signature: "signature-a"}, StreamingMeta: &schema.MessageStreamingMeta{Index: index0}},
			{Type: schema.ChatMessagePartTypeText, Text: "public-a", StreamingMeta: &schema.MessageStreamingMeta{Index: index1}},
			{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: "private-b", Signature: "signature-b"}, StreamingMeta: &schema.MessageStreamingMeta{Index: index2}},
			{Type: schema.ChatMessagePartTypeText, Text: "public-b", StreamingMeta: &schema.MessageStreamingMeta{Index: index3}},
		},
		ToolCalls: []schema.ToolCall{{
			ID:       "call-1",
			Type:     "function",
			Function: schema.FunctionCall{Name: "Read", Arguments: `{"file_path":"README.md"}`},
		}},
	}
}

func p38MessageBindingMatchesFixture(
	expected p38MessageOriginBindingFixture,
	actual p38MessageOriginBindingFixture,
) bool {
	return expected.EntryVersion > 0 &&
		expected.EntryID != "" &&
		expected.MessageIndex >= 0 &&
		expected.LogicalMessageID != "" &&
		p38CanonicalDigestFixture(expected.PayloadDigest) &&
		reflect.DeepEqual(expected, actual)
}

func p38CanonicalDigestFixture(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func p38PrepareAgenticTransportFixture(
	input []*schema.Message,
	state string,
	history *p38ReasoningOriginFixture,
	dispatch p38DispatchOriginFixture,
) ([]*schema.AgenticMessage, bool, string, error) {
	allowed, reason := p38EvaluateOriginFixture(state, history, dispatch)
	prepared := make([]*schema.Message, len(input))
	for index, message := range input {
		if allowed || message == nil || message.Role != schema.Assistant {
			prepared[index] = message
			continue
		}
		prepared[index] = p38StripPrivateFixture(message)
	}
	converted, err := messagesToAgentic(prepared)
	if err != nil {
		return nil, false, reason, err
	}
	if allowed {
		for _, message := range converted {
			if message.Role != schema.AgenticRoleTypeAssistant {
				continue
			}
			if message.ResponseMeta == nil {
				message.ResponseMeta = &schema.AgenticResponseMeta{}
			}
			message.ResponseMeta.OpenAIExtension = &openaischema.ResponseMetaExtension{}
		}
	}
	return converted, allowed, reason, nil
}

func p38StripPrivateFixture(input *schema.Message) *schema.Message {
	clone := *input
	clone.ReasoningContent = ""
	clone.Extra = nil
	clone.AssistantGenMultiContent = make(
		[]schema.MessageOutputPart,
		0,
		len(input.AssistantGenMultiContent),
	)
	for _, part := range input.AssistantGenMultiContent {
		if part.Type == schema.ChatMessagePartTypeReasoning {
			continue
		}
		clone.AssistantGenMultiContent = append(clone.AssistantGenMultiContent, part)
	}
	return &clone
}

func assertP38AgenticPublicFixture(
	t *testing.T,
	converted []*schema.AgenticMessage,
	wantPrivate bool,
) {
	t.Helper()
	if len(converted) != 1 {
		t.Fatalf("converted messages = %d, want 1", len(converted))
	}
	message := converted[0]
	marked := message.ResponseMeta != nil && message.ResponseMeta.OpenAIExtension != nil
	if wantPrivate != marked {
		t.Fatalf("typed self marker = %#v, want private=%v", message.ResponseMeta, wantPrivate)
	}

	var (
		reasoning []schema.Reasoning
		texts     []string
		tools     []schema.FunctionToolCall
	)
	for _, block := range message.ContentBlocks {
		switch block.Type {
		case schema.ContentBlockTypeReasoning:
			if block.Reasoning != nil {
				reasoning = append(reasoning, *block.Reasoning)
			}
		case schema.ContentBlockTypeAssistantGenText:
			if block.AssistantGenText != nil {
				texts = append(texts, block.AssistantGenText.Text)
			}
		case schema.ContentBlockTypeFunctionToolCall:
			if block.FunctionToolCall != nil {
				tools = append(tools, *block.FunctionToolCall)
			}
		}
	}
	if !reflect.DeepEqual(texts, []string{"public-a", "public-b"}) {
		t.Fatalf("public texts = %#v", texts)
	}
	wantTools := []schema.FunctionToolCall{{
		CallID:    "call-1",
		Name:      "Read",
		Arguments: `{"file_path":"README.md"}`,
	}}
	if !reflect.DeepEqual(tools, wantTools) {
		t.Fatalf("public tools = %#v", tools)
	}
	if wantPrivate {
		wantReasoning := []schema.Reasoning{
			{Text: "private-a", Signature: "signature-a"},
			{Text: "private-b", Signature: "signature-b"},
		}
		if !reflect.DeepEqual(reasoning, wantReasoning) {
			t.Fatalf("private reasoning = %#v", reasoning)
		}
	} else if len(reasoning) != 0 {
		t.Fatalf("private reasoning survived rejected transport: %#v", reasoning)
	}
}
